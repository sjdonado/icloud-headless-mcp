// Subcommand drain (icloud-mcp drain) runs the writes that were waiting for
// approval, now that it has arrived.
//
// Prints one line per item on stdout for the watchdog to relay, and
// nothing at all when there is nothing to say. Anything that goes wrong
// internally goes to stderr. Runs as the account that owns the browser and
// calls the same tool functions the agent calls rather than reimplementing
// them.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/drain"
	"github.com/sjdonado/icloud-headless-mcp/internal/notes"
	"github.com/sjdonado/icloud-headless-mcp/internal/queue"
	"github.com/sjdonado/icloud-headless-mcp/internal/reminders"
)

// productionRunner dispatches queued kinds to the same tool functions the
// agent calls rather than reimplementing them.
func productionRunner(cfg *config.Config) drain.Runner {
	refuse := func(context.Context, string) string {
		return "nobody was present to approve it, so nothing was done"
	}
	return func(ctx context.Context, kind string, params map[string]any) map[string]any {
		str := func(key string) string {
			s, _ := params[key].(string)
			return s
		}
		folder := func() *string {
			if s := str("folder"); s != "" {
				return &s
			}
			return nil
		}
		switch kind {
		case "create_note":
			nc, err := notes.Dial(cfg)
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			nc.Ask = refuse
			out, err := nc.Create(ctx, str("title"), str("body"), folder())
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			return out
		case "create_reminder":
			rc, err := reminders.Dial(cfg)
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			rc.Ask = refuse
			out, err := rc.Create(ctx, str("title"), str("list_name"), str("due"))
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			return out
		case "complete_reminder":
			rc, err := reminders.Dial(cfg)
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			rc.Ask = refuse
			out, err := rc.Complete(ctx, str("title"), str("list_name"))
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			return out
		default:
			return map[string]any{"error": fmt.Sprintf("nothing here knows how to run %s", kind)}
		}
	}
}

func runDrain(args []string) int {
	reportOnly := false
	for _, arg := range args {
		if arg == "--report-only" {
			reportOnly = true
		}
	}
	cfg := config.LoadEnv()
	tzName, err := cfg.LocalTimezone()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad environment: %v\n", err)
		return 1
	}
	owner, err := time.LoadLocation(tzName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad environment: unknown timezone %q\n", tzName)
		return 1
	}
	d := &drain.Drain{
		Queue:     queue.New(filepath.Join(cfg.SharedState, "pending.jsonl")),
		State:     browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP},
		OwnerZone: owner,
		Now:       time.Now,
		Run:       productionRunner(cfg),
		Out:       os.Stdout,
		ErrOut:    os.Stderr,
	}
	d.RunPass(context.Background(), reportOnly)
	return 0
}
