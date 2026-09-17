package browser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// App describes one iCloud web app session: its tab, frame, and readiness
// signal.
type App struct {
	// Name is the short name: notes, Reminders, Drive. It keys the lock
	// file and the tab-use stamp.
	Name string
	// URL is where the app lives.
	URL string
	// FrameHint identifies the application iframe by URL substring.
	FrameHint string
	// ReadyClass is the exact class token that only appears once the app
	// has real content.
	ReadyClass string
}

// NeedsApprovalError means Apple's temporary web access lapsed (or would
// need requesting overnight). The message is the owner-facing text: the
// tool result is the only way out of this process.
type NeedsApprovalError struct{ Msg string }

func (e *NeedsApprovalError) Error() string { return e.Msg }

// SignedOutError means the browser session expired. No retry helps and no
// restart helps; only a human signing in through VNC.
type SignedOutError struct{ Msg string }

func (e *SignedOutError) Error() string { return e.Msg }

// LatchBlocked records that the owner was asked, returning true when this
// call set it. When latching is impossible it still reports true: speak
// rather than swallow the problem.
func (s State) LatchBlocked(reason string) bool {
	if s.Blocked() {
		return false
	}
	path := s.Latch()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	stamp := time.Now().Format("2006-01-02T15:04:05-0700") + " " + reason + "\n"
	if err := os.WriteFile(path, []byte(stamp), 0o600); err != nil {
		return true
	}
	return true
}

// ClearBlocked releases the latch. Only a proven-working surface or an
// explicit re-ask does this.
func (s State) ClearBlocked() {
	_ = os.Remove(s.Latch())
}

// LogAsk records that something asked the owner for web access, or would
// have. Append-only telemetry for the watchdog; never a reason to fail a
// caller.
func (s State) LogAsk(kind, app, outcome string) {
	path := filepath.Join(s.Shared, "asks.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer fh.Close()
	fmt.Fprintf(fh, "%s\t%s\t%s\t%s\n",
		time.Now().Format("2006-01-02T15:04:05-0700"), kind, app, outcome)
}

// Tab is one open app tab with its CDP session attached.
type Tab struct {
	conn     *wsConn
	browser  *CDP
	TargetID string
	Session  string
	FrameURL string
}

// Close detaches the session and closes the connection. It never closes
// the tab: warm tabs stay warm.
func (t *Tab) Close() {
	if t == nil {
		return
	}
	t.conn.tabDetach(t.Session)
	t.conn.close()
}

// Eval runs a snippet in the app frame and returns the raw result value.
// arg is passed as the snippet's single argument when non-nil.
func (t *Tab) Eval(expr string, arg any) (json.RawMessage, error) {
	return t.conn.isolatedWorld(t.Session, t.TargetID, t.FrameURL, expr, arg)
}

// EvalText runs a snippet expected to return a string.
func (t *Tab) EvalText(expr string, arg any) (string, error) {
	raw, err := t.Eval(expr, arg)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

// Collect returns inner texts of elements matching an exact class token.
func (t *Tab) Collect(class string) ([]string, error) {
	raw, err := t.Eval(CollectJS, class)
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Click dispatches real pointer events on the nth element with a class
// token.
func (t *Tab) Click(class string, index int) (bool, error) {
	raw, err := t.Eval(ClickJS, []any{class, index})
	if err != nil {
		return false, err
	}
	var ok bool
	if err := json.Unmarshal(raw, &ok); err != nil {
		return false, err
	}
	return ok, nil
}

// GrantClipboard allows clipboard read/write on the iCloud origin, which
// the canvas-copy and paste flows need.
func (t *Tab) GrantClipboard() error {
	return t.conn.grantClipboard()
}

// EvalJSON runs a snippet that returns a JSON.stringify'ed value and
// decodes it into out. Most flow snippets answer this way; snippets that
// return raw arrays, objects, or booleans decode straight from Eval.
func (t *Tab) EvalJSON(expr string, arg any, out any) error {
	s, err := t.EvalText(expr, arg)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), out)
}
func (t *Tab) MouseClick(x, y int) error {
	return t.conn.mouseClick(t.Session, x, y)
}

// MouseMove hovers without clicking.
func (t *Tab) MouseMove(x, y int) error {
	return t.conn.mouseMove(t.Session, x, y)
}

// TypeText types as the platform would.
func (t *Tab) TypeText(text string) error {
	return t.conn.insertText(t.Session, text)
}

// Key presses one named key: Enter, Tab, Escape, or a single letter.
func (t *Tab) Key(name string, delayMS int) error {
	return t.conn.keyPress(t.Session, name, delayMS)
}

// KeyCombo holds Control and taps key (Control+A/C/V/Z).
func (t *Tab) KeyCombo(key string) error {
	return t.conn.keyCombo(t.Session, key)
}

// FrameTexts concatenates innerText across the tab's frames.
func (t *Tab) FrameTexts() (string, error) {
	return t.conn.frameTextsSession(t.Session, t.TargetID)
}

// DismissAlert closes blocking iCloud alerts, returning the first title or
// "" when none was up. Loops, because dismissing one can reveal another.
func (t *Tab) DismissAlert() string {
	first := ""
	for i := 0; i < 5; i++ {
		var found struct {
			None  bool   `json:"none"`
			Title string `json:"title"`
			X     int    `json:"x"`
			Y     int    `json:"y"`
		}
		if err := t.EvalJSON(AlertJS, nil, &found); err != nil {
			return first
		}
		if found.None {
			return first
		}
		if first == "" {
			first = found.Title
		}
		if err := t.MouseClick(found.X, found.Y); err != nil {
			return first
		}
		sleepMS(1200)
	}
	return first
}

// Open attaches to the resident browser and hands back the app tab.
//
// The tab is kept open between calls: loading an app costs twenty to forty
// seconds, and paying that per call made reads time out. A tab this call
// created is closed on error; a reused warm one stays.
func (app App) Open(state State, owner *time.Location, now time.Time) (*Tab, error) {
	cdp := NewCDP(state.CDP)
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		return nil, err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return nil, err
	}
	closeConn := func() { conn.close() }

	// Before touching a tab: a reload against an expired grant turns
	// "probably still signed in" into "definitely signed out".
	cookies, err := conn.cookies()
	if err != nil {
		closeConn()
		return nil, err
	}
	if !hasToken(cookies) {
		closeConn()
		return nil, &SignedOutError{fmt.Sprintf(
			"the iCloud browser session has expired, so %s cannot be read or changed. "+
				"Nothing was read or written. Notes and reminders stay unavailable until a "+
				"human signs the browser in again; calendar, contacts and mail are unaffected, "+
				"because they do not use the browser.", app.Name)}
	}

	// Latched: refuse before touching anything.
	if state.Blocked() {
		closeConn()
		return nil, &NeedsApprovalError{fmt.Sprintf(
			"iCloud %s is waiting for your approval, so it is unavailable. You have "+
				"already been asked; nothing further will be tried until you release it. "+
				"Nothing was read or changed.", app.Name)}
	}

	targets, err := cdp.Targets()
	if err != nil {
		closeConn()
		return nil, err
	}
	match := strings.TrimSuffix(app.URL, "/")
	var mine []Target
	for _, tg := range targets {
		if tg.Type == "page" && strings.Contains(tg.URL, match) {
			mine = append(mine, tg)
		}
	}
	// A previous call may have left a duplicate; keep the newest.
	for _, stale := range mine[:max(0, len(mine)-1)] {
		_ = cdp.CloseTarget(stale.ID)
	}
	var tab Target
	openedHere := len(mine) == 0
	if openedHere {
		id, err := conn.newTab(app.URL)
		if err != nil {
			closeConn()
			return nil, err
		}
		tab = Target{ID: id, Type: "page", URL: app.URL}
	} else {
		tab = mine[len(mine)-1]
	}
	fail := func(err error) (*Tab, error) {
		if openedHere {
			_ = cdp.CloseTarget(tab.ID)
		}
		closeConn()
		return nil, err
	}

	session, err := conn.tabAttach(tab.ID)
	if err != nil {
		return fail(err)
	}
	conn.setTimezone(session, owner.String())

	navigated := openedHere || !strings.Contains(tab.URL, match)
	if navigated && QuietHoursAt(owner, now) {
		// Refuse rather than ask: the only place that would raise an
		// approval prompt overnight.
		_ = conn.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
		if openedHere {
			_ = cdp.CloseTarget(tab.ID)
		}
		closeConn()
		return nil, &NeedsApprovalError{fmt.Sprintf(
			"iCloud %s would have to be loaded fresh, which asks Apple for approval "+
				"on your devices, and it is the middle of the night. Deferred until morning "+
				"rather than spending a prompt you cannot answer. Nothing was read.", app.Name)}
	}
	if navigated {
		// Before the navigation, not after: the prompt appears the moment
		// the page loads. No notifier on an MCP server; the ask log keeps
		// the telemetry.
		state.LogAsk("page-load", app.Name, "no-notifier")
		if err := conn.navigate(session, app.URL); err != nil {
			_ = conn.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
			return fail(err)
		}
	}

	deadline := now.Add(90 * time.Second)
	for time.Now().Before(deadline) {
		frames, ferr := conn.frameIDs(session, tab.ID)
		if ferr != nil {
			sleepMS(3000)
			continue
		}
		var frameURL string
		for _, f := range frames {
			if strings.Contains(f.URL, app.FrameHint) {
				frameURL = f.URL
				break
			}
		}
		if frameURL == "" {
			sleepMS(3000)
			continue
		}
		raw, ferr := conn.isolatedWorld(session, tab.ID, app.FrameHint, CollectJS, app.ReadyClass)
		if ferr != nil {
			sleepMS(3000)
			continue
		}
		var ready []string
		if jerr := json.Unmarshal(raw, &ready); jerr != nil || len(ready) == 0 {
			sleepMS(3000)
			continue
		}
		// Ready means the shell rendered, not that the data arrived: when
		// this call loaded the tab, wait for the count to stop growing.
		if navigated {
			settled := false
			for i := 0; i < 8; i++ {
				sleepMS(1500)
				rraw, rerr := conn.isolatedWorld(session, tab.ID, app.FrameHint, CollectJS, app.ReadyClass)
				if rerr != nil {
					break
				}
				var again []string
				if jerr := json.Unmarshal(rraw, &again); jerr != nil {
					break
				}
				if len(again) <= len(ready) {
					settled = true
					break
				}
				ready = again
			}
			_ = settled
		}
		state.Touch(strings.ToLower(app.Name))
		return &Tab{conn: conn, browser: cdp, TargetID: tab.ID, Session: session, FrameURL: app.FrameHint}, nil
	}

	// Distinguish "needs approval" from "broken".
	text, _ := conn.frameTexts(tab.ID)
	if isBlocked(text) {
		state.LatchBlocked(fmt.Sprintf("%s reported a lapsed grant", app.Name))
		_ = conn.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
		if openedHere {
			_ = cdp.CloseTarget(tab.ID)
		}
		closeConn()
		return nil, &NeedsApprovalError{fmt.Sprintf(
			"iCloud %s needs device approval, so nothing was read or changed. Apple "+
				"is waiting for approval on one of the account's own devices: unlock an "+
				"iPhone or Mac, approve the iCloud.com request, and ask again. This is not "+
				"a sign-in problem; the session is fine and it is the data-access grant "+
				"that lapsed.", app.Name)}
	}
	_ = conn.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
	if openedHere {
		_ = cdp.CloseTarget(tab.ID)
	}
	closeConn()
	if navigated {
		state.LatchBlocked(fmt.Sprintf("%s did not become ready after a fresh load", app.Name))
		return nil, &NeedsApprovalError{fmt.Sprintf(
			"iCloud %s did not finish loading after a fresh navigation, which is "+
				"almost always Apple waiting for web access to be allowed on one of the "+
				"account's own devices. Nothing was read or changed. Ask for the access "+
				"request to be sent again from an iPhone or Mac, then try once more.", app.Name)}
	}
	return nil, fmt.Errorf("the %s app did not load in time", app.Name)
}

func hasToken(cookies []map[string]any) bool {
	for _, ck := range cookies {
		if name, _ := ck["name"].(string); name == "X-APPLE-WEBAUTH-TOKEN" {
			return true
		}
	}
	return false
}

// ClosePage detaches, closes the tab, and drops the connection. Used
// where the caller owns the tab lifecycle (drive fetch); warm app tabs
// use Close instead, which leaves the tab standing.
func (t *Tab) ClosePage() {
	if t == nil {
		return
	}
	_ = t.browser.CloseTarget(t.TargetID)
	t.Close()
}

// Renavigate reloads one app tab in place to re-request Apple's
// data-access grant. Goto, never reload: reloading an iCloud app tab
// reliably kills Chromium; navigating does not. The timezone override is
// re-applied first, because a fresh navigation is a fresh startup.
func Renavigate(cdp *CDP, pageURL, zone string) error {
	targets, err := cdp.Targets()
	if err != nil {
		return err
	}
	var tab *Target
	for i, tg := range targets {
		if tg.Type == "page" && strings.Contains(tg.URL, strings.TrimSuffix(pageURL, "/")) {
			tab = &targets[i]
		}
	}
	if tab == nil {
		return fmt.Errorf("no tab for %s", pageURL)
	}
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		return err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return err
	}
	defer conn.close()
	session, err := conn.tabAttach(tab.ID)
	if err != nil {
		return err
	}
	defer conn.tabDetach(session)
	conn.setTimezone(session, zone)
	if err := conn.navigate(session, pageURL); err != nil {
		return err
	}
	sleepMS(8000)
	_, _ = conn.isolatedWorld(session, tab.ID, "", `() => { window.__agentTzApplied = true; }`, nil)
	return nil
}

// OpenDriveTab opens the Drive app in its own tab and sniffs the API
// request URLs it emits. It returns early once enoughURLs is satisfied,
// or after timeoutMS. The caller owns the tab and closes it when done.
// The Network tap goes in before the navigation: afterwards is too late.
func OpenDriveTab(cdp *CDP, ownerZone, driveURL string, timeoutMS int, enoughURLs func(urls []string) bool) (*Tab, []string, error) {
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		return nil, nil, err
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return nil, nil, err
	}
	targetID, err := conn.newTab(driveURL)
	if err != nil {
		conn.close()
		return nil, nil, err
	}
	tab := &Tab{conn: conn, browser: cdp, TargetID: targetID}
	session, err := conn.tabAttach(targetID)
	if err != nil {
		tab.ClosePage()
		return nil, nil, err
	}
	tab.Session = session
	conn.setTimezone(session, ownerZone)
	if err := conn.call(session, "Network.enable", map[string]any{}, nil); err != nil {
		tab.ClosePage()
		return nil, nil, err
	}
	if err := conn.navigate(session, driveURL); err != nil {
		tab.ClosePage()
		return nil, nil, err
	}
	var urls []string
	seen := map[string]bool{}
	deadline := nowMS() + int64(timeoutMS)
	for nowMS() < deadline {
		msg, ok := conn.recvTimeout(2000)
		if !ok {
			continue
		}
		method, _ := msg["method"].(string)
		if method != "Network.requestWillBeSent" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		req, _ := params["request"].(map[string]any)
		url, _ := req["url"].(string)
		if url != "" && !seen[url] {
			seen[url] = true
			urls = append(urls, url)
		}
		if enoughURLs != nil && enoughURLs(urls) {
			break
		}
	}
	return tab, urls, nil
}

// CrashVictims returns tabs whose renderer just crashed. The resident
// closes those tabs rather than the process: a dead renderer takes CDP
// down with it, which otherwise reads as a browser-wide wedge.
func CrashVictims(cdp *CDP, waitMS int) []string {
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		return nil
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		return nil
	}
	defer conn.close()
	var out []string
	deadline := nowMS() + int64(waitMS)
	for nowMS() < deadline {
		msg, ok := conn.recvTimeout(500)
		if !ok {
			continue
		}
		method, _ := msg["method"].(string)
		if method != "Target.targetCrashed" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		if id, _ := params["targetId"].(string); id != "" {
			out = append(out, id)
		}
	}
	return out
}
