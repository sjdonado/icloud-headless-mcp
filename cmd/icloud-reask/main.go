// Command icloud-reask re-asks Apple for web access by re-navigating the
// Reminders tab, not by restarting the browser: a restart discards warm
// tabs and raises an approval prompt of its own, while a navigation
// re-requests the grant and leaves everything standing. One app, not two:
// the grant covers iCloud.com data, so one navigation raises one prompt.
package main

import (
	"fmt"
	"os"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
)

const remindersURL = "https://www.icloud.com/reminders/"

func main() {
	os.Exit(run())
}

func run() int {
	cfg := config.LoadEnv()
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	zone, err := cfg.LocalTimezone()
	if err != nil {
		fmt.Fprintf(os.Stderr, "no timezone: %v\n", err)
		return 2
	}
	cdp := browser.NewCDP(cfg.CDP)
	if _, err := cdp.DebuggerURL(); err != nil {
		fmt.Fprintf(os.Stderr, "could not reach the browser: %v\n", err)
		return 2
	}
	if err := browser.Renavigate(cdp, remindersURL, zone); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", remindersURL, err)
		return 1
	}
	fmt.Printf("re-asked %s\n", remindersURL)
	// Release the latch so the next real call can find out whether the
	// owner approved. A re-ask is the owner saying they are at a device.
	state.ClearBlocked()
	fmt.Println("latch cleared, iCloud will be tried again")
	return 0
}
