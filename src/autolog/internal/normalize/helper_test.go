package normalize

import (
	"fmt"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

// base はテストの基準時刻。10:00 なので 14 時間後が翌日 0:00 になる。
func base() time.Time {
	return time.Date(2026, 7, 25, 10, 0, 0, 0, time.FixedZone("JST", 9*60*60))
}

func eventID(i int) string {
	return fmt.Sprintf("ev-%03d", i)
}

// event は base からの相対時刻で生ログイベントを作る。
func event(t *testing.T, id string, eventType rawlog.EventType, start time.Duration, end *time.Duration, payload any) *rawlog.Event {
	t.Helper()

	var endTime *time.Time
	if end != nil {
		e := base().Add(*end)
		endTime = &e
	}

	e, err := rawlog.NewEvent(id, rawlog.Device("Laptop"), eventType, base().Add(start), endTime, payload)
	if err != nil {
		t.Fatalf("NewEvent(%s): %v", id, err)
	}
	return e
}

// androidEvent は Phone のイベントを作る。
func androidEvent(t *testing.T, id string, eventType rawlog.EventType, start time.Duration, end *time.Duration, payload any) *rawlog.Event {
	t.Helper()

	e := event(t, id, eventType, start, end, payload)
	e.Device = rawlog.Device("Phone")
	return e
}

func durationPtr(d time.Duration) *time.Duration { return &d }

// runNormalize は Cutoff を base + cutoff にして実行する。
func runNormalize(t *testing.T, events []*rawlog.Event, cutoff time.Duration) *Result {
	t.Helper()
	return runNormalizeWith(t, events, Options{Cutoff: base().Add(cutoff)})
}

// runNormalizeWith は設定を指定して normalize を回す。
func runNormalizeWith(t *testing.T, events []*rawlog.Event, opts Options) *Result {
	t.Helper()

	result, err := Run(events, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

// proposalsBySource は指定した収集元の提案だけを返す。
func proposalsBySource(result *Result, source string) []Proposal {
	var filtered []Proposal
	for _, p := range result.Proposals {
		if p.Source == source {
			filtered = append(filtered, p)
		}
	}
	return filtered
}
