package drain

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/queue"
)

var fixedNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func testDrain(t *testing.T, run Runner) (*Drain, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(filepath.Join(root, "shared", "pending.jsonl"))
	q.SetNow(func() time.Time { return fixedNow })
	out, errout := &bytes.Buffer{}, &bytes.Buffer{}
	d := &Drain{
		Queue:     q,
		State:     browser.State{Dir: filepath.Join(root, "state-home"), Shared: filepath.Join(root, "shared")},
		OwnerZone: loc,
		Now:       func() time.Time { return fixedNow },
		Run:       run,
		Out:       out,
		ErrOut:    errout,
	}
	return d, out, errout
}

func okRunner(result map[string]any) Runner {
	return func(context.Context, string, map[string]any) map[string]any { return result }
}

func TestQuietDayStaysQuiet(t *testing.T) {
	called := false
	d, out, errout := testDrain(t, func(ctx context.Context, kind string, params map[string]any) map[string]any {
		called = true
		return map[string]any{"created": true}
	})
	d.RunPass(context.Background(), false)
	if called || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("quiet day spoke: out=%q err=%q", out, errout)
	}
}

func TestRunsPendingAndMarksDone(t *testing.T) {
	d, out, _ := testDrain(t, okRunner(map[string]any{"created": true}))
	id := d.Queue.Enqueue("create_note", map[string]any{"title": "Hi", "body": "x"}, "", 0, 0, "")
	if id == "" {
		t.Fatal("enqueue failed")
	}
	d.RunPass(context.Background(), false)
	if d.Queue.CountPending("") != 0 {
		t.Fatal("item still pending after success")
	}
	if got := out.String(); !strings.Contains(got, "done: note “Hi” in the default folder") {
		t.Fatalf("out = %q", got)
	}
}

func TestUnconfirmedLandsMarked(t *testing.T) {
	d, out, _ := testDrain(t, okRunner(map[string]any{"created": "unconfirmed"}))
	d.Queue.Enqueue("create_reminder",
		map[string]any{"title": "Call", "list_name": "Today", "due": "2026-10-09 10:00"}, "", 0, 0, "")
	d.RunPass(context.Background(), false)
	if got := out.String(); !strings.Contains(got, "done but unconfirmed: reminder “Call” on Today, due 2026-10-09 10:00") {
		t.Fatalf("out = %q", got)
	}
	if d.Queue.CountPending("") != 0 {
		t.Fatal("item still pending")
	}
}

func TestTransientRequeuesWithFirstTS(t *testing.T) {
	d, _, errout := testDrain(t, okRunner(map[string]any{"error": "locked"}))
	d.Queue.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	d.RunPass(context.Background(), false)
	items := d.Queue.Pending("")
	if len(items) != 1 || items[0].Attempts != 1 {
		t.Fatalf("pending = %+v", items)
	}
	if !strings.Contains(errout.String(), "could not write it yet, will try again") {
		t.Fatalf("err = %q", errout)
	}
	// Fourth attempt fails loudly on stdout.
	d.RunPass(context.Background(), false)
	d.RunPass(context.Background(), false)
	d2out := &bytes.Buffer{}
	d.Out = d2out
	d.RunPass(context.Background(), false)
	if d.Queue.CountPending("") != 0 {
		t.Fatal("item still pending after max attempts")
	}
	if got := d2out.String(); !strings.Contains(got, "gave up after 4 attempts") {
		t.Fatalf("out = %q", got)
	}
}

func writeLatch(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("test\n"), 0o600)
}

func TestLatchedDrainIsSilent(t *testing.T) {
	d, out, errout := testDrain(t, okRunner(map[string]any{"created": true}))
	d.Queue.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	if err := writeLatch(d.State.Latch()); err != nil {
		t.Fatal(err)
	}
	d.RunPass(context.Background(), false)
	if out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("latched drain spoke: out=%q err=%q", out, errout)
	}
	if d.Queue.CountPending("") != 1 {
		t.Fatal("latched drain consumed the item")
	}
}

func TestQuietHoursKeepsUntilMorning(t *testing.T) {
	d, out, _ := testDrain(t, okRunner(map[string]any{"created": true}))
	d.Queue.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	d.Now = func() time.Time { return time.Date(2026, 10, 8, 3, 0, 0, 0, d.OwnerZone) }
	d.RunPass(context.Background(), false)
	if out.Len() != 0 || d.Queue.CountPending("") != 1 {
		t.Fatalf("night drain ran: out=%q", out)
	}
}

func TestExpiryReportsWithoutBrowser(t *testing.T) {
	// No runner calls at all: expiry precedes every browser touch.
	d, out, _ := testDrain(t, func(context.Context, string, map[string]any) map[string]any {
		t.Fatal("runner touched during expiry")
		return nil
	})
	old := float64(fixedNow.Add(-50*time.Hour).UnixNano()) / 1e9
	d.Queue.Enqueue("create_reminder", map[string]any{"title": "Call doc"}, "", old, 0, "")
	d.RunPass(context.Background(), true)
	if got := out.String(); !strings.Contains(got, "expired after 50h without access") {
		t.Fatalf("out = %q", got)
	}
	if d.Queue.CountPending("") != 0 {
		t.Fatal("expired item still pending")
	}
}

func TestStrandedReportedOnce(t *testing.T) {
	d, out, _ := testDrain(t, okRunner(map[string]any{"created": true}))
	id := d.Queue.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	if !d.Queue.Mark(id, "attempting", map[string]any{"attempt": 1}) {
		t.Fatal("mark failed")
	}
	d.RunPass(context.Background(), false)
	if got := out.String(); !strings.Contains(got, "interrupted mid-write") {
		t.Fatalf("out = %q", got)
	}
	if d.Queue.CountPending("") != 0 {
		t.Fatal("stranded item still pending")
	}
}

func TestSupersededOnRequeue(t *testing.T) {
	d, _, _ := testDrain(t, okRunner(map[string]any{"queued": true, "queue_id": "new-id"}))
	d.Queue.Enqueue("create_note", map[string]any{"title": "Hi"}, "", 0, 0, "")
	d.RunPass(context.Background(), false)
	if d.Queue.CountPending("") != 0 {
		t.Fatal("superseded item still pending")
	}
}

func TestSummariseFallback(t *testing.T) {
	if got := summarise("mystery_kind", nil); got != "a queued mystery_kind" {
		t.Fatalf("got %q", got)
	}
	if got := summarise("complete_reminder", map[string]any{"title": "T", "list_name": "L"}); got != "ticking off “T” on L" {
		t.Fatalf("got %q", got)
	}
}
