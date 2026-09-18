package queue

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testQueue(t *testing.T) (*Queue, string) {
	t.Helper()
	q := New(filepath.Join(t.TempDir(), "shared", "pending.jsonl"))
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }
	return q, q.path
}

func TestEnqueueAndPending(t *testing.T) {
	q, path := testQueue(t)
	id := q.Enqueue("create_note", map[string]any{"title": "Hi", "body": "x"}, "test", 0, 0, "")
	if id == "" {
		t.Fatal("enqueue returned no id")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("queue file mode = %v, err = %v", info.Mode(), err)
	}
	items := q.Pending("")
	if len(items) != 1 || items[0].ID != id || items[0].Kind != "create_note" {
		t.Fatalf("pending = %+v", items)
	}
	if q.CountPending("") != 1 || q.CountPending("other-blocker") != 0 {
		t.Fatal("count mismatch")
	}
}

func TestEnqueueDedupesIdenticalWork(t *testing.T) {
	q, _ := testQueue(t)
	params := map[string]any{"title": "Hi"}
	first := q.Enqueue("create_note", params, "", 0, 0, "")
	second := q.Enqueue("create_note", params, "", 0, 0, "")
	if first == "" || second != first {
		t.Fatalf("dedupe failed: %q %q", first, second)
	}
	if q.CountPending("") != 1 {
		t.Fatal("duplicate queued")
	}
	// A drain retry carries attempts and must not match itself.
	third := q.Enqueue("create_note", params, "", 0, 1, "")
	if third == "" || third == first {
		t.Fatalf("retry dropped: %q", third)
	}
}

func TestMarkDoneClearsPending(t *testing.T) {
	q, _ := testQueue(t)
	id := q.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	if !q.Mark(id, "done", map[string]any{"landed": true}) {
		t.Fatal("mark failed")
	}
	if q.CountPending("") != 0 {
		t.Fatal("done item still pending")
	}
}

func TestStrandedReportedNotRetried(t *testing.T) {
	q, _ := testQueue(t)
	id := q.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	if !q.Mark(id, "attempting", map[string]any{"attempt": 1}) {
		t.Fatal("mark failed")
	}
	if q.CountPending("") != 0 {
		t.Fatal("attempting item still pending")
	}
	stranded := q.Stranded()
	if len(stranded) != 1 || stranded[0].Kind != "create_note" {
		t.Fatalf("stranded = %+v", stranded)
	}
}

func TestTruncatedLineIgnored(t *testing.T) {
	q, path := testQueue(t)
	id := q.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.WriteString("{truncated\n")
	fh.Close()
	if q.CountPending("") != 1 {
		t.Fatal("truncated line broke the file")
	}
	_ = id
}

func TestAgeHoursUsesFirstTS(t *testing.T) {
	q, _ := testQueue(t)
	id := q.Enqueue("create_note", map[string]any{}, "", 0, 0, "")
	items := q.Pending("")
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("pending = %+v", items)
	}
	// Re-queue carrying the original timestamp: age still measures from
	// when the owner asked.
	q2now := q.now().Add(30 * time.Hour)
	q.now = func() time.Time { return q2now }
	retry := q.Enqueue("create_note", map[string]any{}, "", items[0].FirstTS, 1, "")
	if retry == "" {
		t.Fatal("requeue failed")
	}
	if age := q.AgeHours(Item{FirstTS: items[0].FirstTS}); age < 29 || age > 31 {
		t.Fatalf("age = %v", age)
	}
}
