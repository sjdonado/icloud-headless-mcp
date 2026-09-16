package browser

import (
	"fmt"
	"strings"
)

// Blocked markers: the phrases Apple's grant-lapsed pages show. Worded
// several ways across surfaces; matching one of them was how a lapse once
// went unnoticed while the watchdog reported healthy.
var blockedMarkers = []string{
	"no response to the request",
	"Can't Display",
	"Can’t Display",
	"grant iCloud.com access",
	"Getting Access",
	"temporary access",
}

// AppFragments are the tabs that matter. The home page renders fine either
// way, so only app tabs carry the health signal.
var appFragments = []string{"/reminders/", "/notes/"}

// Outcome is the session verdict. The four answers need four different
// fixes, so they stay distinct.
type Outcome int

const (
	// OK means the resident browser can reach iCloud data.
	OK Outcome = iota
	// SignedOut means the session is gone: only a human signing in
	// through VNC fixes it.
	SignedOut
	// Unreachable means no browser to talk to.
	Unreachable
	// NeedsApproval means the session is fine but Apple's grant lapsed: a
	// device approval fixes it.
	NeedsApproval
)

// Check reports whether the resident browser can actually reach iCloud
// data. The cookie check alone once reported OK while the apps sat on
// "Getting Access", so page text in every frame is the signal for a
// lapsed grant, not the cookies.
func (c *CDP) Check() (Outcome, string) {
	wsURL, err := c.DebuggerURL()
	if err != nil {
		return Unreachable, fmt.Sprintf("UNREACHABLE %T", err)
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return Unreachable, fmt.Sprintf("UNREACHABLE %T", err)
	}
	defer conn.close()

	cookies, err := conn.cookies()
	if err != nil {
		return Unreachable, fmt.Sprintf("UNREACHABLE %T", err)
	}
	hasToken := false
	for _, ck := range cookies {
		if name, _ := ck["name"].(string); name == "X-APPLE-WEBAUTH-TOKEN" {
			hasToken = true
		}
	}
	if !hasToken {
		return SignedOut, fmt.Sprintf("SIGNED_OUT (cookies present: %d)", len(cookies))
	}

	targets, err := c.Targets()
	if err != nil {
		return Unreachable, fmt.Sprintf("UNREACHABLE %T", err)
	}
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		app := ""
		for _, frag := range appFragments {
			if strings.Contains(target.URL, frag) {
				app = strings.Trim(frag, "/")
			}
		}
		if app == "" {
			continue
		}
		text, err := conn.frameTexts(target.ID)
		if err != nil {
			continue
		}
		if isBlocked(text) {
			return NeedsApproval, fmt.Sprintf("NEEDS_APPROVAL (%s is blocked on a device approval)", app)
		}
	}
	return OK, "OK"
}

func isBlocked(body string) bool {
	for _, marker := range blockedMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}
