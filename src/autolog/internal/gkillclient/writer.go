package gkillclient

// 編集前に読む: .claude/skills/autolog-gkill-api/SKILL.md（この領域の不変条件の正本）

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/ledger"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/normalize"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// URLogRateLimit は add_urlog の発行間隔。
//
// gkill サーバは add_urlog のたびに対象URLを再取得する
// (handle_add_urlog.go -> FillURLogField -> getBody が無条件実行)。
// 連続して投入するとサーバから大量の外向きフェッチが出るため間隔を空ける。
const URLogRateLimit = time.Second

// ClientResolver は端末に対応する接続を返す。
//
// 端末ごとに別の gkill ユーザーへ書き分けるため、接続も端末ごとに要る。
type ClientResolver func(rawlog.Device) (*Client, error)

// WriteOptions は書き込みの設定。
type WriteOptions struct {
	// DryRun が true なら API を呼ばず、発行予定の内容だけを記録する。
	DryRun bool
	// DescribeUser は dry-run の表示で使う。書き込み先ユーザー名を返す。
	DescribeUser func(rawlog.Device) string
}

// WriteStats は書き込み結果の集計。
type WriteStats struct {
	// Written は新しく書き込んだ件数。
	Written int
	// Skipped は台帳にあり既に書き込み済みだった件数。
	Skipped int
	// Overlapped は書き込み済みの区間と一部重なるため書き込まなかった件数。
	// 遅れて届いた生ログが確定済みの区間を延ばしたときに起きる。
	Overlapped int
	// Failed は書き込みに失敗した件数。
	Failed int
	// FailedProposalIDs は書き込みに失敗した提案。次回の再処理範囲を決めるのに使う。
	FailedProposalIDs []string
	// TagsAdded は付与したタグの件数。
	TagsAdded int
}

// Writer は提案を gkill の HTTP API で書き込む。
type Writer struct {
	resolve ClientResolver
	ledger  *ledger.Ledger
	logger  *slog.Logger
	opts    WriteOptions

	// lastURLogAt はレート制限のための直前の add_urlog 時刻。
	lastURLogAt time.Time
}

// NewWriter は Writer を作る。resolve が nil なら dry-run 専用。
func NewWriter(resolve ClientResolver, ledgerDB *ledger.Ledger, logger *slog.Logger, opts WriteOptions) *Writer {
	return &Writer{resolve: resolve, ledger: ledgerDB, logger: logger, opts: opts}
}

// WriteAll は提案をまとめて書き込む。
//
// 1件の失敗で全体を止めない。失敗した分は台帳に記録しないので次回再処理される。
func (w *Writer) WriteAll(ctx context.Context, proposals []normalize.Proposal) (*WriteStats, error) {
	stats := &WriteStats{}

	written, err := w.ledger.LoadWritten(ctx)
	if err != nil {
		return nil, err
	}

	for _, proposal := range proposals {
		if _, ok := written[proposal.ID]; ok {
			stats.Skipped++
			continue
		}
		// 元イベントがすべて書き込み済みなら、カーソルの引き戻しで
		// 確定済み区間を途中から読み直してできた断片。id は違っても中身は
		// 既に書いた区間の一部なので、書き込むと二重登録になる。
		covered, total, err := w.ledger.CoveredCount(ctx,
			string(proposal.Kind), proposal.Source, string(proposal.Device), proposal.SourceEventIDs)
		if err != nil {
			return nil, err
		}
		if total > 0 && covered == total {
			stats.Skipped++
			w.logger.Debug("書き込み済み区間の断片のため書き込まない",
				"proposal_id", proposal.ID, "kind", proposal.Kind, "source", proposal.Source)
			continue
		}
		// 一部だけが書き込み済みの区間は、遅れて届いた生ログが確定済みの区間を
		// 延ばした形。そのまま書くと、既に書いた短い区間と重なる長い区間が
		// もう1本できてしまうので、区間系の収集元では書き込まない。
		// 延びた分の記録は失われるが、重複した TimeIs を作るよりましと判断する。
		//
		// 接続系 (Wi-Fi・Bluetooth・充電) は対象にしない。観測の切れ目マーカーの
		// セッションイベントを複数の区間が共有するので、一部重複が正常な形。
		// URLog は元イベントが1つなので一部重複になりえず、Kmemo (通知) は
		// 同じ通知の更新列が正当に重なるので、どちらも対象にしない。
		if covered > 0 && overlapProtected(proposal) {
			stats.Overlapped++
			w.logger.Warn("書き込み済みの区間と一部重なるため書き込まない (遅着イベントによる区間の延長)",
				"proposal_id", proposal.ID, "kind", proposal.Kind, "source", proposal.Source,
				"covered", covered, "total", total,
				"start", formatOptional(proposal.StartTime), "end", formatOptional(proposal.EndTime))
			continue
		}
		if err := w.writeOne(ctx, proposal, stats); err != nil {
			stats.Failed++
			stats.FailedProposalIDs = append(stats.FailedProposalIDs, proposal.ID)
			w.logger.Error("提案の書き込みに失敗した",
				"proposal_id", proposal.ID, "kind", proposal.Kind, "source", proposal.Source, "error", err)
			continue
		}
		stats.Written++
	}
	return stats, nil
}

// writeOne は1件の提案を書き込み、タグを付けて台帳へ記録する。
func (w *Writer) writeOne(ctx context.Context, proposal normalize.Proposal, stats *WriteStats) error {
	if w.opts.DryRun {
		w.logger.Info("(dry-run) 書き込む予定",
			"kind", proposal.Kind, "source", proposal.Source, "device", proposal.Device,
			"gkill_user", w.describeUser(proposal.Device),
			"title", proposal.Title, "url", proposal.URL,
			"start", formatOptional(proposal.StartTime), "end", formatOptional(proposal.EndTime),
			"related", formatOptional(proposal.RelatedTime),
			"tags", tagsFor(proposal))
		return nil
	}

	client, err := w.resolve(proposal.Device)
	if err != nil {
		return err
	}

	kyouID, err := w.add(ctx, client, proposal)
	if err != nil {
		return err
	}

	for _, tag := range tagsFor(proposal) {
		_, err := client.AddTag(ctx, Tag{
			ID:          tagIDFor(proposal, tag),
			Device:      proposal.Device,
			TargetID:    kyouID,
			Tag:         tag,
			RelatedTime: proposal.Time(),
		})
		if err != nil {
			// タグが付かなくても本体は書けている。台帳へは記録して二重登録を防ぐ。
			w.logger.Warn("タグを付けられなかった", "kyou_id", kyouID, "tag", tag, "error", err)
			continue
		}
		stats.TagsAdded++
	}

	return w.ledger.Record(ctx, proposal.ID,
		string(proposal.Kind), proposal.Source, string(proposal.Device), kyouID, proposal.SourceEventIDs)
}

// add は提案の種類に応じて追加 API を呼ぶ。
func (w *Writer) add(ctx context.Context, client *Client, proposal normalize.Proposal) (string, error) {
	switch proposal.Kind {
	case normalize.KindTimeIs:
		if proposal.StartTime == nil || proposal.EndTime == nil {
			return "", fmt.Errorf("timeis proposal %s has no interval", proposal.ID)
		}
		return client.AddTimeIs(ctx, TimeIs{
			ID:        kyouIDFor(proposal),
			Device:    proposal.Device,
			Title:     proposal.Title,
			StartTime: *proposal.StartTime,
			EndTime:   *proposal.EndTime,
		})

	case normalize.KindURLog:
		if proposal.RelatedTime == nil {
			return "", fmt.Errorf("urlog proposal %s has no related time", proposal.ID)
		}
		w.waitForURLogRateLimit(ctx)
		return client.AddURLog(ctx, URLog{
			ID:          kyouIDFor(proposal),
			Device:      proposal.Device,
			URL:         proposal.URL,
			Title:       proposal.Title,
			RelatedTime: *proposal.RelatedTime,
		})

	case normalize.KindKmemo:
		if proposal.RelatedTime == nil {
			return "", fmt.Errorf("kmemo proposal %s has no related time", proposal.ID)
		}
		return client.AddKmemo(ctx, Kmemo{
			ID:          kyouIDFor(proposal),
			Device:      proposal.Device,
			Content:     proposal.Content,
			RelatedTime: *proposal.RelatedTime,
		})

	default:
		return "", fmt.Errorf("unknown proposal kind %q", proposal.Kind)
	}
}

// kyouIDFor は提案から Kyou の ID を決定的に導く。
//
// gkill の各表は ID に一意制約が無い追記型で、読み出しは UPDATE_TIME の
// 最新版を採用する (gkill 本体の LatestDataRepositoryAddress)。
// 同じ id の再追加は「最新版で上書き」なので、書き込みは成功したのに
// 応答を受け取れなかった・台帳への記録前に落ちた、という再試行でも
// 同じ Kyou がもう1つ増えることはない。
func kyouIDFor(proposal normalize.Proposal) string {
	return deterministicKyouID("kyou", proposal.ID)
}

// tagIDFor は提案とタグ名からタグ行の ID を決定的に導く。
// 再試行で同じタグが2行になるのを防ぐ。
func tagIDFor(proposal normalize.Proposal, tag string) string {
	return deterministicKyouID("tag:"+tag, proposal.ID)
}

// deterministicKyouID は名前ベース (SHA-1) の UUID を作る。
// 同じ入力からは常に同じ UUID になる。
func deterministicKyouID(kind, proposalID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("gkill_autolog:"+kind+":"+proposalID)).String()
}

// describeUser は dry-run の表示用に書き込み先ユーザー名を返す。
func (w *Writer) describeUser(device rawlog.Device) string {
	if w.opts.DescribeUser == nil {
		return ""
	}
	return w.opts.DescribeUser(device)
}

// tagsFor は付けるタグを返す。
//
// 端末は Kyou 自身の create_device に入るのでタグにはしない。
// MCP 経由だった頃は create_device が mcp 固定で端末が残らず、
// 端末タグが唯一の識別手段だったが、HTTP API を直接叩くようになって不要になった。
func tagsFor(proposal normalize.Proposal) []string {
	return []string{proposal.Source}
}

// overlapProtected は「書き込み済みイベントとの一部重複」を二重登録の兆候として
// 弾く対象かを返す。
//
// 対象は結合で区間が延びうる TimeIs (ウィンドウ・アプリ利用、メディア再生、端末利用)。
// これらは確定済みの区間が遅着イベントと結合されて上位集合の提案になる経路があり、
// 書き込むと同じ時間帯の TimeIs が2本になる。
func overlapProtected(proposal normalize.Proposal) bool {
	if proposal.Kind != normalize.KindTimeIs {
		return false
	}
	switch proposal.Source {
	case normalize.SourceWindow, normalize.SourceMedia, normalize.SourceDevice:
		return true
	}
	return false
}

// waitForURLogRateLimit は add_urlog の間隔を空ける。
func (w *Writer) waitForURLogRateLimit(ctx context.Context) {
	if w.lastURLogAt.IsZero() {
		w.lastURLogAt = time.Now()
		return
	}
	wait := URLogRateLimit - time.Since(w.lastURLogAt)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
	}
	w.lastURLogAt = time.Now()
}

func formatOptional(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rawlog.FormatTime(*t)
}
