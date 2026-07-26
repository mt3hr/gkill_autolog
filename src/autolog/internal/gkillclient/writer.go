package gkillclient

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	// Dropped は判定で捨てた件数。
	Dropped int
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
// keep が false の提案は判定で捨てられたものとして書き込まない。
// keep に載っていない提案は判定対象外（URL 候補ではない）なので書き込む。
//
// 1件の失敗で全体を止めない。失敗した分は台帳に記録しないので次回再処理される。
func (w *Writer) WriteAll(ctx context.Context, proposals []normalize.Proposal, keep map[string]bool) (*WriteStats, error) {
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
		if decision, judged := keep[proposal.ID]; judged && !decision {
			stats.Dropped++
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
			Device:      proposal.Device,
			TargetID:    kyouID,
			Tag:         tag,
			RelatedTime: proposalTime(proposal),
		})
		if err != nil {
			// タグが付かなくても本体は書けている。台帳へは記録して二重登録を防ぐ。
			w.logger.Warn("タグを付けられなかった", "kyou_id", kyouID, "tag", tag, "error", err)
			continue
		}
		stats.TagsAdded++
	}

	return w.ledger.Record(ctx, proposal.ID, string(proposal.Kind), kyouID)
}

// add は提案の種類に応じて追加 API を呼ぶ。
func (w *Writer) add(ctx context.Context, client *Client, proposal normalize.Proposal) (string, error) {
	switch proposal.Kind {
	case normalize.KindTimeIs:
		if proposal.StartTime == nil || proposal.EndTime == nil {
			return "", fmt.Errorf("timeis proposal %s has no interval", proposal.ID)
		}
		return client.AddTimeIs(ctx, TimeIs{
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
			Device:      proposal.Device,
			Content:     proposal.Content,
			RelatedTime: *proposal.RelatedTime,
		})

	default:
		return "", fmt.Errorf("unknown proposal kind %q", proposal.Kind)
	}
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

// proposalTime は提案が指す時刻を返す。タグの関連時刻に使う。
func proposalTime(proposal normalize.Proposal) time.Time {
	switch {
	case proposal.StartTime != nil:
		return *proposal.StartTime
	case proposal.RelatedTime != nil:
		return *proposal.RelatedTime
	default:
		return time.Time{}
	}
}

func formatOptional(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rawlog.FormatTime(*t)
}
