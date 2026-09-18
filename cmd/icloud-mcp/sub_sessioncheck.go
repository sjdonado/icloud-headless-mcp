// Subcommand session-check (icloud-mcp session-check) reports whether the resident browser can
// actually reach iCloud data.
//
// Exit 0 healthy, 1 signed out, 2 no browser to talk to, 3 needs a device
// approval.
package main

import (
	"fmt"
	"os"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

func runSessionCheck() int {
	cdpURL := os.Getenv("ICLOUD_CDP")
	if cdpURL == "" {
		cdpURL = "http://127.0.0.1:9222"
	}
	outcome, msg := browser.NewCDP(cdpURL).Check()
	fmt.Println(msg)
	switch outcome {
	case browser.OK:
		return 0
	case browser.SignedOut:
		return 1
	case browser.Unreachable:
		return 2
	case browser.NeedsApproval:
		return 3
	}
	return 0
}
