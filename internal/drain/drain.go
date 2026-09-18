// Package drain runs the writes that were waiting for the owner's
// approval, now that it has arrived.
//
// Prints one line per item on stdout for the watchdog to relay, and
// nothing at all when there is nothing to say, so a quiet day stays
// quiet. Anything that goes wrong internally goes to stderr, which the
// watchdog reports separately.
//
// --report-only ages items out without touching the browser: expiry must
// stay reachable even when the browser is unhealthy and the latch is set,
// or the 48-hour report the queue promises would never arrive.
package drain

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/queue"
)

// MaxAttempts bounds retries of one item. A lock timeout, a wedged browser,
// or a signed-out session are transient; marking any non-latch failure
// failed on the first pass would burn the whole queue in one go.
const MaxAttempts = 4

// Blocker is the one readiness this drain resolves: Apple's device
// approval for the browser. Work waiting on anything else stays queued
// under its own name.
const Blocker = "icloud-approval"

// Runner executes one queued write. Production wires the browser-backed
// tool functions (Phase 4); unknown kinds report an error rather than a
// guess, and the retry accounting below treats that as transient.
type Runner func(ctx context.Context, kind string, params map[string]any) map[string]any

// Drain holds the drain's inputs. Out receives owner-relayed lines,
// ErrOut internal problems. Now and OwnerZone make quiet-hours and expiry
// deterministic in tests.
type Drain struct {
	Queue     *queue.Queue
	State     browser.State
	OwnerZone *time.Location
	Now       func() time.Time
	Run       Runner
	Out       io.Writer
	ErrOut    io.Writer
}

func summarise(kind string, params map[string]any) string {
	str := func(key string) string {
		s, _ := params[key].(string)
		return s
	}
	switch kind {
	case "create_note":
		where := str("folder")
		if where == "" {
			where = "the default folder"
		}
		return fmt.Sprintf("note \u201c%s\u201d in %s", str("title"), where)
	case "create_reminder":
		due := ""
		if d := str("due"); d != "" {
			due = ", due " + d
		}
		return fmt.Sprintf("reminder \u201c%s\u201d on %s%s", str("title"), str("list_name"), due)
	case "complete_reminder":
		return fmt.Sprintf("ticking off \u201c%s\u201d on %s", str("title"), str("list_name"))
	default:
		// No params in the fallback: a note body can be long, and this
		// line is relayed onward.
		if kind == "" {
			kind = "write"
		}
		return fmt.Sprintf("a queued %s", kind)
	}
}

// expire ages items out and says what they would have done. It touches no
// browser: acting on a two-day-old request unasked is its own mistake, so
// the owner decides.
func (d *Drain) expire(items []queue.Item) {
	for _, item := range items {
		age := d.Queue.AgeHours(item)
		if age <= queue.MaxAgeHours {
			continue
		}
		if !d.Queue.Mark(item.ID, "expired", map[string]any{"age_hours": round1(age)}) {
			fmt.Fprintf(d.ErrOut, "could not record the expiry of %s\n", item.ID)
			continue
		}
		fmt.Fprintf(d.Out, "expired after %.0fh without access, so I did not run it: %s. "+
			"Ask me again if you still want it.\n", age, summarise(item.Kind, item.Params))
	}
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// Run executes one drain pass. It returns nothing to say on quiet paths:
// still latched, quiet hours, or nothing pending all exit silently.
func (d *Drain) RunPass(ctx context.Context, reportOnly bool) {
	// An item claimed by a pass that then died. Reported, not retried:
	// from here "created and the mark was lost" and "never happened" are
	// indistinguishable, and writing twice is the worse mistake.
	for _, item := range d.Queue.Stranded() {
		d.Queue.Mark(item.ID, "abandoned", nil)
		fmt.Fprintf(d.Out, "interrupted mid-write, so I cannot tell whether it landed: %s. "+
			"Check, and ask me again if it is missing.\n", summarise(item.Kind, item.Params))
	}

	items := d.Queue.Pending(Blocker)
	if len(items) == 0 {
		return
	}

	// Expiry first, and before any browser check: an item that aged out
	// while access was still down must still be reported.
	d.expire(items)
	if reportOnly {
		return
	}

	items = d.Queue.Pending(Blocker)
	if len(items) == 0 {
		return
	}

	if d.State.Blocked() {
		// Still latched. Say nothing and change nothing: draining now
		// would re-queue everything and spend an app-load timeout per
		// item.
		return
	}

	if browser.QuietHoursAt(d.OwnerZone, d.Now()) {
		// A fresh page load is what raises Apple's prompt, and in the
		// night that is a prompt nobody can answer. The writes keep until
		// morning; they have already waited hours.
		return
	}

	for _, item := range items {
		d.runOne(ctx, item)
	}
}

func (d *Drain) runOne(ctx context.Context, item queue.Item) {
	kind, params := item.Kind, item.Params
	if params == nil {
		params = map[string]any{}
	}
	// Claim the item before touching the browser. Without this, a kill
	// between the write and the done mark leaves the item pending, and
	// the next pass writes it twice.
	if !d.Queue.Mark(item.ID, "attempting", map[string]any{"attempt": item.Attempts + 1}) {
		fmt.Fprintf(d.ErrOut, "could not claim %s, so it was not attempted\n", item.ID)
		return
	}

	result := d.Run(ctx, kind, params)

	landed, _ := result["created"]
	if landed == nil {
		landed, _ = result["completed"]
	}
	if landed == true || landed == "unconfirmed" {
		if !d.Queue.Mark(item.ID, "done", map[string]any{"landed": landed}) {
			// The write happened but the record did not. Say so loudly:
			// this is the one state that leads to a duplicate.
			fmt.Fprintf(d.ErrOut, "WROTE but could not record it: %s. Check for a duplicate.\n",
				summarise(kind, params))
		}
		state := "done"
		if landed == "unconfirmed" {
			state = "done but unconfirmed"
		}
		fmt.Fprintf(d.Out, "%s: %s\n", state, summarise(kind, params))
		return
	}

	if queued, _ := result["queued"].(bool); queued {
		// It hit the latch again and re-queued itself under a new id.
		// Retire this one, carrying the original timestamp so age is
		// measured from when the owner asked, not from the last bounce.
		qid, _ := result["queue_id"].(string)
		d.Queue.Mark(item.ID, "superseded", map[string]any{"replaced_by": qid})
		return
	}

	// Anything else is transient until proven otherwise.
	if item.Attempts+1 >= MaxAttempts {
		errText := "no reason given"
		if e, ok := result["error"]; ok {
			errText = fmt.Sprintf("%v", e)
		}
		d.Queue.Mark(item.ID, "failed", map[string]any{
			"attempts": item.Attempts + 1, "error": truncate(errText, 300),
		})
		fmt.Fprintf(d.Out, "gave up after %d attempts: %s: %s\n",
			item.Attempts+1, summarise(kind, params), firstLine(errText))
		return
	}
	d.Queue.Enqueue(kind, params, item.Origin, item.FirstTS, item.Attempts+1, Blocker)
	fmt.Fprintf(d.ErrOut, "could not write it yet, will try again: %s\n", summarise(kind, params))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
