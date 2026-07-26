package inbox

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mt3hr/gkill_autolog/src/autolog/internal/rawlog"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// eventLine は1件分の JSONL 行を作る。
func eventLine(eventID, device string, at time.Time) string {
	return `{"schema_version":1,"event_id":"` + eventID + `","device":"` + device +
		`","event_type":"session","start_time":"` + rawlog.FormatTime(at) +
		`","captured_at":"` + rawlog.FormatTime(at) + `","payload":{"action":"unlock"}}`
}

func writeInboxFile(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	path := filepath.Join(dir, name)
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
	return path
}

func openTestStore(t *testing.T) *rawlog.Store {
	t.Helper()
	store, err := rawlog.OpenStore(filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestIngestReadsAndRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	path := writeInboxFile(t, dir, "20260726-1.jsonl",
		eventLine("e1", "Phone", at),
		eventLine("e2", "Phone", at.Add(time.Minute)),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir:            dir,
		AllowedDevices: []rawlog.Device{"Phone"},
		DefaultDevice:  "Phone",
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if stats.Files != 1 || stats.Read != 2 || stats.Inserted != 2 || stats.Skipped != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("取り込んだファイルが残っている")
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("raw.db の件数 = %d, want 2", len(events))
	}
}

// 同じイベントを二度取り込んでも増えないこと。
// 削除の直前に落ちて同じファイルを読み直しても壊れない前提のもと。
func TestIngestIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	store := openTestStore(t)

	for range 2 {
		writeInboxFile(t, dir, "batch.jsonl", eventLine("e1", "Phone", at))
		if _, err := Ingest(context.Background(), store, Options{
			Dir: dir, DefaultDevice: "Phone",
		}, quietLogger()); err != nil {
			t.Fatalf("Ingest: %v", err)
		}
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("件数 = %d, want 1 (重複は弾かれるはず)", len(events))
	}
}

// 許可していない端末のイベントは入れない。
func TestIngestRejectsUnknownDevice(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl",
		eventLine("e1", "Phone", at),
		eventLine("e2", "UnknownPhone", at),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir:            dir,
		AllowedDevices: []rawlog.Device{"Phone"},
		DefaultDevice:  "Phone",
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Inserted != 1 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Inserted=1 Skipped=1", stats)
	}
}

// 壊れた行があっても他の行は取り込む。
func TestIngestSkipsBrokenLines(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl",
		eventLine("e1", "Phone", at),
		"{壊れた行",
		"",
		eventLine("e2", "Phone", at.Add(time.Second)),
	)

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: dir, DefaultDevice: "Phone"}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Inserted != 2 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Inserted=2 Skipped=1", stats)
	}
}

// 書きかけの .jsonl.tmp は読まない。
func TestIngestIgnoresTempFiles(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	tmpPath := writeInboxFile(t, dir, "writing.jsonl.tmp", eventLine("e1", "Phone", at))

	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: dir, DefaultDevice: "Phone"}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 || stats.Inserted != 0 {
		t.Errorf("stats = %+v, want 何も取り込まない", stats)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Error("書きかけのファイルを消してしまった")
	}
}

// device が空なら、この端末の名前を補う。
func TestIngestFillsDefaultDevice(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Truncate(time.Second)
	writeInboxFile(t, dir, "a.jsonl", eventLine("e1", "", at))

	store := openTestStore(t)
	if _, err := Ingest(context.Background(), store, Options{
		Dir: dir, DefaultDevice: "Phone",
	}, quietLogger()); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	events, err := store.Range(context.Background(), rawlog.RangeQuery{})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(events) != 1 || events[0].Device != "Phone" {
		t.Errorf("device が補われていない: %+v", events)
	}
}

// 共有ディレクトリを使わない構成（Windows）では何もしない。
func TestIngestWithoutDirDoesNothing(t *testing.T) {
	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{Dir: ""}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

// ディレクトリがまだ無くてもエラーにしない（収集アプリが未起動の場合）。
func TestIngestMissingDirIsNotAnError(t *testing.T) {
	store := openTestStore(t)
	stats, err := Ingest(context.Background(), store, Options{
		Dir: filepath.Join(t.TempDir(), "not_created_yet"),
	}, quietLogger())
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if stats.Files != 0 {
		t.Errorf("stats = %+v", stats)
	}
}
