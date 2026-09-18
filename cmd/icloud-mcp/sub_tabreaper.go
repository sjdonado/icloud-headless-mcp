// Subcommand tab-reaper (icloud-mcp tab-reaper) closes app tabs idle
// longer than N minutes (default 15), keeping browser warmth within a
// working session. Every tool entry point touches its app's stamp file so
// a tab is never closed mid-session.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

func runTabReaper(args []string) int {
	idleMinutes := 15
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad idle minutes %q: %v\n", args[0], err)
			return 2
		}
		idleMinutes = n
	}
	stateDir := os.Getenv("ICLOUD_STATE")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "no state directory: %v\n", err)
			return 2
		}
		stateDir = home
	}
	shared := os.Getenv("ICLOUD_SHARED_STATE")
	cdpURL := os.Getenv("ICLOUD_CDP")
	if cdpURL == "" {
		cdpURL = "http://127.0.0.1:9222"
	}
	state := browser.State{Dir: stateDir, Shared: shared, CDP: cdpURL}
	for _, line := range state.Reap(browser.NewCDP(cdpURL), idleMinutes, time.Now()) {
		fmt.Println(line)
	}
	return 0
}
