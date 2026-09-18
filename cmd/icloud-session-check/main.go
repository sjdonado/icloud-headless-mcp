// Command icloud-session-check reports whether the resident browser can
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

func main() {
	cdpURL := os.Getenv("ICLOUD_CDP")
	if cdpURL == "" {
		cdpURL = "http://127.0.0.1:9222"
	}
	outcome, msg := browser.NewCDP(cdpURL).Check()
	fmt.Println(msg)
	switch outcome {
	case browser.OK:
		os.Exit(0)
	case browser.SignedOut:
		os.Exit(1)
	case browser.Unreachable:
		os.Exit(2)
	case browser.NeedsApproval:
		os.Exit(3)
	}
}
