// Package collect は端末上で常駐し、操作ログを生ログストアへ書き込む。
package collect

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// flushInterval は溜まったイベントを書き出す間隔。
const flushInterval = 5 * time.Second

// flushBatchSize はこの件数に達したら間隔を待たずに書き出す。
const flushBatchSize = 64

// Emitter は各コレクタが作ったイベントを受け取り、まとめて生ログストアへ書き込む。
//
// 収集側を DB の都合で待たせないよう、投入は非同期にする。
// 入力フックの中から呼ばれることもあるため、Emit はブロックしない。
type Emitter struct {
	store  *rawlog.Store
	device rawlog.Device
	events chan *rawlog.Event
	logger *slog.Logger
}

// NewEmitter は Emitter を作る。Run を呼ぶまで書き込みは始まらない。
func NewEmitter(store *rawlog.Store, device rawlog.Device, logger *slog.Logger) *Emitter {
	return &Emitter{
		store:  store,
		device: device,
		events: make(chan *rawlog.Event, 1024),
		logger: logger,
	}
}

// Emit はイベントを書き込みキューへ入れる。
// キューが詰まっている場合は捨てて警告を出す。操作を遅くしないことを優先する。
func (e *Emitter) Emit(eventType rawlog.EventType, startTime time.Time, endTime *time.Time, payload any) {
	event, err := rawlog.NewEvent(uuid.NewString(), e.device, eventType, startTime, endTime, payload)
	if err != nil {
		e.logger.Error("イベントを作れなかった", "event_type", eventType, "error", err)
		return
	}
	select {
	case e.events <- event:
	default:
		e.logger.Warn("書き込みキューが詰まったためイベントを捨てた", "event_type", eventType)
	}
}

// Run はキューのイベントを生ログストアへ書き込み続ける。
// ctx が終わったら、残っているイベントを書き出してから返る。
func (e *Emitter) Run(ctx context.Context) error {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	pending := make([]*rawlog.Event, 0, flushBatchSize)

	for {
		select {
		case <-ctx.Done():
			// 収集側が止まってからキューに残った分を拾う。
			for {
				select {
				case event := <-e.events:
					pending = append(pending, event)
					continue
				default:
				}
				break
			}
			e.flush(context.WithoutCancel(ctx), pending)
			return nil

		case event := <-e.events:
			pending = append(pending, event)
			// セッションの変化は件数が少ないうえ、失うと影響が大きい。
			// ロックやシャットダウンの直後はプロセスが強制終了されることがあるため、
			// 間隔を待たずに書き出す。
			if event.EventType == rawlog.EventSession || len(pending) >= flushBatchSize {
				pending = e.flush(ctx, pending)
			}

		case <-ticker.C:
			pending = e.flush(ctx, pending)
		}
	}
}

// flush は溜まったイベントを書き出し、空にしたスライスを返す。
// 書き込みに失敗しても収集は続ける（生ログは次回以降も追記されるため）。
func (e *Emitter) flush(ctx context.Context, pending []*rawlog.Event) []*rawlog.Event {
	if len(pending) == 0 {
		return pending
	}
	inserted, err := e.store.PutBatch(ctx, pending)
	if err != nil {
		e.logger.Error("生ログの書き込みに失敗した", "count", len(pending), "error", err)
		return pending[:0]
	}
	if inserted != len(pending) {
		e.logger.Debug("重複したイベントを除いて書き込んだ", "inserted", inserted, "total", len(pending))
	}
	return pending[:0]
}

// Device は収集元の端末名を返す。
func (e *Emitter) Device() rawlog.Device {
	return e.device
}

// validateDevice は端末名として使える書式かを確かめる。
//
// 使える名前を列挙はしない。端末を増やすたびにコード修正と再ビルドが要ると窮屈なため。
func validateDevice(device rawlog.Device) error {
	return rawlog.ValidateDeviceName(device)
}
