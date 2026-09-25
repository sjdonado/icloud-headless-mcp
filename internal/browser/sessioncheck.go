package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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

// AuthCookieNames are set only once the account is through 2FA. VALIDATE
// is the session-scoped one.
var AuthCookieNames = map[string]bool{
	"X-APPLE-WEBAUTH-TOKEN":        true,
	"X-APPLE-WEBAUTH-USER":         true,
	"X-APPLE-DS-WEB-SESSION-TOKEN": true,
	"X-APPLE-WEBAUTH-VALIDATE":     true,
}

// HasAuthCookies reports whether the jar-worthy tokens are present.
func HasAuthCookies(cookies []map[string]any) bool {
	for _, ck := range cookies {
		if name, _ := ck["name"].(string); AuthCookieNames[name] {
			return true
		}
	}
	return false
}

// TokenPresent reports whether the persistent session token is present.
func (c *CDP) TokenPresent() (bool, error) {
	cookies, err := c.AllCookies()
	if err != nil {
		return false, err
	}
	return HasSessionToken(cookies), nil
}

// HasSessionToken reports whether the cookie set carries the persistent
// session token.
func HasSessionToken(cookies []map[string]any) bool {
	return hasToken(cookies)
}

// AllCookies returns the browser's cookies.
func (c *CDP) AllCookies() ([]map[string]any, error) {
	wsURL, err := c.DebuggerURL()
	if err != nil {
		return nil, err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return nil, err
	}
	defer conn.close()
	return conn.cookies()
}

// CookieHeader renders cookies as one Cookie header value.
func CookieHeader(cookies []map[string]any) string {
	var parts []string
	for _, ck := range cookies {
		name, _ := ck["name"].(string)
		value, _ := ck["value"].(string)
		if name == "" {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}

// SaveJar mirrors the cookie jar out, but only when it still carries auth
// cookies: a jar without them would overwrite a good jar with a dead
// session and force another interactive login.
func SaveJar(state State, cookies []map[string]any) int {
	if !HasAuthCookies(cookies) {
		return 0
	}
	path := filepath.Join(state.Dir, "cookies.json")
	raw, err := json.MarshalIndent(cookies, "", " ")
	if err != nil {
		return 0
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return 0
	}
	_ = os.Chmod(path, 0o600)
	return len(cookies)
}

// LoadJar reads the persisted jar, or nil when there is none.
func LoadJar(state State) []map[string]any {
	raw, err := os.ReadFile(filepath.Join(state.Dir, "cookies.json"))
	if err != nil {
		return nil
	}
	var cookies []map[string]any
	if err := json.Unmarshal(raw, &cookies); err != nil {
		return nil
	}
	return cookies
}

// Ping proves the browser process answers: a call that has to reach it,
// because tab lists and cookie reads have both lied about a dead browser.
// Bounded short: this runs every 5 seconds in the resident loop, so a
// wedged tab must read as one missed beat, not a minutes-long stall.
func (c *CDP) Ping() error {
	wsURL, err := c.DebuggerURL()
	if err != nil {
		return err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return err
	}
	defer conn.close()
	targets, err := c.Targets()
	if err != nil {
		return err
	}
	for _, tg := range targets {
		if tg.Type != "page" {
			continue
		}
		if err := conn.pingTab(tg.ID); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no live page answered")
}

func (c *wsConn) pingTab(targetID string) error {
	session, err := c.tabAttach(targetID)
	if err != nil {
		return err
	}
	defer c.tabDetach(session)
	// Liveness only, so the page's own main world will do: an isolated world
	// needs a real frame id, and current Chrome rejects the empty one.
	return c.callTimeout(session, "Runtime.evaluate", map[string]any{"expression": "1"}, nil, 15*time.Second)
}

// Outcome is the session verdict. The four answers need four different
// fixes, so they stay distinct.
type Outcome int

const (
	// OK means the resident browser can reach iCloud data.
	OK Outcome = iota
	// SignedOut means the session is gone: only a human signing in
	// through the login door fixes it.
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
		return Unreachable, unreachableErr(err)
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return Unreachable, unreachableErr(err)
	}
	defer conn.close()

	cookies, err := conn.cookies()
	if err != nil {
		return Unreachable, unreachableErr(err)
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
		return Unreachable, unreachableErr(err)
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
		// An app tab that will not attach is not verifiably healthy:
		// report it loudly rather than as OK. A crashed renderer reads
		// exactly like this.
		text, err := frameTextsIn(conn, target.ID)
		if err != nil {
			return Unreachable, fmt.Sprintf("UNREACHABLE app tab %s unreadable: %v", app, shortErr(err))
		}
		if isBlocked(text) {
			return NeedsApproval, fmt.Sprintf("NEEDS_APPROVAL (%s is blocked on a device approval)", app)
		}
	}
	return OK, "OK"
}

// frameTextsIn evaluates innerText across one tab's frames on an existing
// connection.
func frameTextsIn(conn *wsConn, targetID string) (string, error) {
	session, err := conn.tabAttach(targetID)
	if err != nil {
		return "", err
	}
	defer conn.tabDetach(session)
	return conn.frameTextsSession(session, targetID)
}

func shortErr(err error) string {
	return strings.TrimPrefix(fmt.Sprintf("%v", err), "CDP ")
}

// unreachableErr reports an unreachable browser as a stable contract
// string: the kind of failure, not a Go type name that renames itself on
// every refactor.
func unreachableErr(err error) string {
	if isTimeout(err) {
		return "UNREACHABLE Timeout"
	}
	var sys *os.SyscallError
	if errors.As(err, &sys) && sys.Err == syscall.ECONNREFUSED {
		return "UNREACHABLE ConnectionRefused"
	}
	if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
		return "UNREACHABLE ConnectionRefused"
	}
	return "UNREACHABLE Error"
}

func isBlocked(body string) bool {
	for _, marker := range blockedMarkers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}
