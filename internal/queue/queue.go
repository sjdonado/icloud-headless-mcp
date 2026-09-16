// Package queue holds writes that could not happen because Apple was
// waiting for a tap, kept until it comes.
//
// Append-only, one event per line: a file that is rewritten is a file that
// loses things, and this one exists precisely so nothing gets lost. An item
// is pending when its most recent event is queued. Expiry is 48 hours, and
// expiry is not silence: an old item is reported with what it would have
// done, leaving the decision with the owner.
package queue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MaxAgeHours is how long an item may wait before it is reported instead
// of run: acting on a two-day-old request unasked is its own mistake.
const MaxAgeHours = 48.0

// DefaultBlocker is what an item waits on when nothing else is named.
const DefaultBlocker = "icloud-approval"

// Queue is one pending-writes file. It lives in the shared state so a
// drain running as somebody else can read what was queued here.
type Queue struct {
	path string
	now  func() time.Time
}

// New opens the queue file. Nothing is created until the first write.
func New(path string) *Queue {
	return &Queue{path: path, now: time.Now}
}

// SetNow fixes the clock, for tests.
func (q *Queue) SetNow(now func() time.Time) {
	q.now = now
}

// appendRecord writes one line. It reports whether the line actually
// reached disk: a queue that cannot say whether it recorded something is
// not a queue. The file is 0600: these lines hold whole note bodies.
func (q *Queue) appendRecord(record map[string]any) bool {
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "queue: could not write %s: %v\n", q.path, err)
		return false
	}
	_, isNew := os.Stat(q.path)
	fh, err := os.OpenFile(q.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "queue: could not write %s: %v\n", q.path, err)
		return false
	}
	defer fh.Close()
	raw, err := json.Marshal(record)
	if err != nil {
		return false
	}
	if _, err := fh.Write(append(raw, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "queue: could not write %s: %v\n", q.path, err)
		return false
	}
	if err := fh.Sync(); err != nil {
		fmt.Fprintf(os.Stderr, "queue: could not write %s: %v\n", q.path, err)
		return false
	}
	if isNew != nil {
		_ = os.Chmod(q.path, 0o600)
	}
	return true
}

func newID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Enqueue records a write to be retried once access is back. It returns the
// id, or "" when the record never reached disk. Identical pending work is
// not queued twice; retries from the drain carry attempts and bypass the
// dedupe, or a retry would match itself and be dropped. firstTS carries the
// original queue time across a re-queue so the age limit can fire.
func (q *Queue) Enqueue(kind string, params map[string]any, origin string, firstTS float64, attempts int, blocker string) string {
	now := q.now().UnixNano() / 1e9
	nowF := float64(now)
	if firstTS == 0 {
		firstTS = nowF
	}
	if blocker == "" {
		blocker = DefaultBlocker
	}
	if attempts == 0 {
		for _, existing := range q.Pending("") {
			if existing.Kind == kind && mapsEqual(existing.Params, params) {
				return existing.ID
			}
		}
	}
	id := newID()
	ok := q.appendRecord(map[string]any{
		"ts": nowF, "first_ts": firstTS, "id": id, "event": "queued",
		"attempts": attempts, "blocker": blocker,
		"kind": kind, "params": params, "origin": origin,
	})
	if !ok {
		return ""
	}
	return id
}

// Mark records what happened to an item. The drain must not move on from
// an item whose mark failed: an unrecorded done is a note that gets
// written again on the next pass.
func (q *Queue) Mark(id, event string, extra map[string]any) bool {
	record := map[string]any{"ts": float64(q.now().UnixNano()) / 1e9, "id": id, "event": event}
	for k, v := range extra {
		record[k] = v
	}
	return q.appendRecord(record)
}

// Item is one pending write with its latest queued record.
type Item struct {
	ID       string
	Kind     string
	Params   map[string]any
	Origin   string
	Attempts int
	FirstTS  float64
	TS       float64
	Blocker  string
}

func toItem(ev map[string]any) Item {
	params, _ := ev["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	blocker, _ := ev["blocker"].(string)
	if blocker == "" {
		blocker = DefaultBlocker
	}
	return Item{
		ID:       stringOf(ev["id"]),
		Kind:     stringOf(ev["kind"]),
		Params:   params,
		Origin:   stringOf(ev["origin"]),
		Attempts: intOf(ev["attempts"]),
		FirstTS:  floatOf(ev["first_ts"], floatOf(ev["ts"], 0)),
		TS:       floatOf(ev["ts"], 0),
		Blocker:  blocker,
	}
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func floatOf(v any, def float64) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f
		}
	}
	return def
}

func intOf(v any) int {
	return int(floatOf(v, 0))
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}

// events reads every record. A truncated line is history, not a reason to
// refuse the rest of the file.
func (q *Queue) events() []map[string]any {
	raw, err := os.ReadFile(q.path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range splitLines(string(raw)) {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if stringOf(ev["id"]) == "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// Pending lists items whose last event is queued, oldest first, optionally
// for one blocker only.
func (q *Queue) Pending(blocker string) []Item {
	last := map[string]map[string]any{}
	for _, ev := range q.events() {
		last[stringOf(ev["id"])] = ev
	}
	var out []Item
	for _, ev := range last {
		if stringOf(ev["event"]) != "queued" {
			continue
		}
		item := toItem(ev)
		if blocker != "" && item.Blocker != blocker {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

// Stranded lists items whose last event is attempting: claimed, then the
// process died mid-write. Reported rather than retried: "created and the
// mark was lost" and "never happened" are indistinguishable from here, and
// writing twice is the worse mistake.
func (q *Queue) Stranded() []Item {
	last := map[string]map[string]any{}
	detail := map[string]map[string]any{}
	for _, ev := range q.events() {
		id := stringOf(ev["id"])
		last[id] = ev
		if stringOf(ev["event"]) == "queued" {
			detail[id] = ev
		}
	}
	var out []Item
	for id, ev := range last {
		if stringOf(ev["event"]) != "attempting" {
			continue
		}
		if d, ok := detail[id]; ok {
			ev = d
		}
		out = append(out, toItem(ev))
	}
	return out
}

// AgeHours is the age since the owner first asked, not since the last
// re-queue.
func (q *Queue) AgeHours(item Item) float64 {
	return (float64(q.now().UnixNano())/1e9 - item.FirstTS) / 3600.0
}

// CountPending counts pending items, optionally for one blocker.
func (q *Queue) CountPending(blocker string) int {
	return len(q.Pending(blocker))
}
