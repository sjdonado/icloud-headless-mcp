// Command icloud-drain runs the writes that were waiting for the owner's
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
	"github.com/sjdonado/icloud-headless-mcp/internal/queue"
)

// productionRunner dispatches queued kinds to the tool functions. The
// browser-backed kinds wire up in Phase 4; unknown kinds report an error
// rather than a guess, and the retry accounting treats that as transient.
func productionRunner(_ context.Context, kind string, _ map[string]any) map[string]any {
	return map[string]any{"error": fmt.Sprintf("nothing here knows how to run %s", kind)}
}

func main() {
	reportOnly := false
	for _, arg := range os.Args[1:] {
		if arg == "--report-only" {
			reportOnly = true
		}
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad environment: %v\n", err)
		os.Exit(1)
	}
	tzName, err := cfg.LocalTimezone()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad environment: %v\n", err)
		os.Exit(1)
	}
	owner, err := time.LoadLocation(tzName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad environment: unknown timezone %q\n", tzName)
		os.Exit(1)
	}
	d := &drain.Drain{
		Queue:     queue.New(filepath.Join(cfg.SharedState, "pending.jsonl")),
		State:     browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP},
		OwnerZone: owner,
		Now:       time.Now,
		Run:       productionRunner,
		Out:       os.Stdout,
		ErrOut:    os.Stderr,
	}
	d.RunPass(context.Background(), reportOnly)
}
