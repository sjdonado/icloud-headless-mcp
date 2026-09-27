package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	nws "nhooyr.io/websocket"
)

// fakeCDP scripts a browser: cookies, per-target frame text, and the tabs
// the HTTP endpoints report. Method calls the script does not know fail,
// the way an unexpected CDP call should.
type fakeCDP struct {
	t *testing.T

	mu         sync.Mutex
	cookies    []map[string]any
	texts      map[string]string
	targets    []Target
	closed     []string
	hits       map[string]int
	created    []string
	frameURLs  map[string]string
	evalArrays map[string][]any
	calls      []string
	attaches   int
	navigated  []string
}

func newFakeCDP(t *testing.T) (*fakeCDP, *httptest.Server, *CDP) {
	t.Helper()
	f := &fakeCDP{t: t, texts: map[string]string{}, hits: map[string]int{},
		frameURLs: map[string]string{}, evalArrays: map[string][]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		wsURL := "ws://" + r.Host + "/ws"
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": wsURL})
	})
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.targets)
	})
	mux.HandleFunc("/json/close/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed = append(f.closed, strings.TrimPrefix(r.URL.Path, "/json/close/"))
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := nws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(nws.StatusNormalClosure, "")
		f.serveWS(conn)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv, NewCDP(srv.URL)
}

func (f *fakeCDP) reply(ws *nws.Conn, id float64, result any) {
	f.t.Helper()
	raw, err := json.Marshal(map[string]any{"id": id, "result": result})
	if err != nil {
		f.t.Fatalf("fake marshal: %v", err)
	}
	// A client that refused a message has hung up; nothing to report.
	_ = ws.Write(context.Background(), nws.MessageText, raw)
}

func (f *fakeCDP) serveWS(ws *nws.Conn) {
	attached := ""
	// Sessions live on the connection that attached them, as in Chrome:
	// a session id from another connection is refused.
	sessions := map[string]bool{}
	for {
		_, data, err := ws.Read(context.Background())
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(data, &msg); err != nil {
			return
		}
		method, _ := msg["method"].(string)
		id, _ := msg["id"].(float64)
		params, _ := msg["params"].(map[string]any)
		f.mu.Lock()
		f.hits[method]++
		f.calls = append(f.calls, method)
		if sid, _ := msg["sessionId"].(string); sid != "" && !sessions[sid] {
			f.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{
				"id": id, "error": map[string]any{"message": "Session with given id not found."},
			})
			_ = ws.Write(context.Background(), nws.MessageText, raw)
			continue
		}
		switch method {
		case "Storage.getCookies":
			cookies := f.cookies
			if cookies == nil {
				cookies = []map[string]any{}
			}
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"cookies": cookies})
		case "Storage.clearCookies":
			f.cookies = nil
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{})
		case "Storage.clearDataForOrigin", "Input.dispatchMouseEvent":
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{})
		case "Target.attachToTarget":
			if p, ok := params["targetId"].(string); ok {
				attached = p
			}
			f.attaches++
			sid := "S" + strconv.Itoa(f.attaches)
			sessions[sid] = true
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"sessionId": sid})
		case "Target.createTarget":
			u, _ := params["url"].(string)
			f.created = append(f.created, u)
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"targetId": "t-new"})
		case "Page.navigate":
			u, _ := params["url"].(string)
			f.navigated = append(f.navigated, u)
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{})
		case "Target.detachFromTarget", "Page.bringToFront",
			"Browser.grantPermissions", "Emulation.setTimezoneOverride", "Network.enable":
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{})
		case "Runtime.enable":
			f.mu.Unlock()
			// Shaped like Chrome: existing contexts report first (which is
			// what identifies the frame's main world), then the page's
			// whole console buffer replays, then the reply. Real
			// Reminders replayed 281 messages, which once trimmed the
			// context event out of the queue.
			raw, _ := json.Marshal(map[string]any{
				"method": "Runtime.executionContextCreated",
				"params": map[string]any{"context": map[string]any{
					"id": 77,
					"auxData": map[string]any{
						"isDefault": true, "frameId": "F1",
					},
				}},
			})
			_ = ws.Write(context.Background(), nws.MessageText, raw)
			for i := 0; i < 300; i++ {
				raw, _ := json.Marshal(map[string]any{"method": "Runtime.consoleAPICalled", "params": map[string]any{"type": "log"}})
				_ = ws.Write(context.Background(), nws.MessageText, raw)
			}
			f.reply(ws, id, map[string]any{})
		case "Page.createIsolatedWorld":
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"executionContextId": 7})
		case "Page.getFrameTree":
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"frameTree": map[string]any{
				"frame": map[string]any{"id": "F1", "url": f.frameURLs[attached]},
			}})
		case "Runtime.evaluate":
			arrs := f.evalArrays[attached]
			n := f.hits[method]
			var value any
			if len(arrs) > 0 {
				if n > len(arrs) {
					n = len(arrs)
				}
				value = arrs[n-1]
			} else {
				value = f.texts[attached]
			}
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"result": map[string]any{"value": value}})
		default:
			f.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{
				"id": id, "error": map[string]any{"message": "unknown method " + method},
			})
			_ = ws.Write(context.Background(), nws.MessageText, raw)
		}
	}
}

func appTargets() []Target {
	return []Target{
		{ID: "t-notes", Type: "page", URL: "https://www.icloud.com/notes/"},
		{ID: "t-reminders", Type: "page", URL: "https://www.icloud.com/reminders/"},
		{ID: "t-home", Type: "page", URL: "https://www.icloud.com/"},
		{ID: "t-dev", Type: "devtools", URL: "devtools://x"},
	}
}

func TestCheckOK(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = appTargets()
	fake.texts["t-notes"] = "Milk\nEggs"
	fake.texts["t-reminders"] = "Call mom"
	outcome, msg := cdp.Check()
	if outcome != OK || msg != "OK" {
		t.Fatalf("Check = %v %q", outcome, msg)
	}
}

func TestCheckSignedOut(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "other"}}
	outcome, msg := cdp.Check()
	if outcome != SignedOut || !strings.Contains(msg, "SIGNED_OUT") {
		t.Fatalf("Check = %v %q", outcome, msg)
	}
}

func TestCheckUnreachable(t *testing.T) {
	cdp := NewCDP("http://127.0.0.1:1")
	outcome, msg := cdp.Check()
	if outcome != Unreachable || !strings.Contains(msg, "UNREACHABLE") {
		t.Fatalf("Check = %v %q", outcome, msg)
	}
}

func TestCheckNeedsApproval(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = appTargets()
	fake.texts["t-notes"] = "Notes"
	fake.texts["t-reminders"] = "Getting Access"
	outcome, msg := cdp.Check()
	if outcome != NeedsApproval || !strings.Contains(msg, "reminders") {
		t.Fatalf("Check = %v %q", outcome, msg)
	}
}

func TestCheckHomePageIgnored(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = []Target{{ID: "t-home", Type: "page", URL: "https://www.icloud.com/"}}
	fake.texts["t-home"] = "Getting Access"
	outcome, _ := cdp.Check()
	if outcome != OK {
		t.Fatalf("home page must not carry the health signal, got %v", outcome)
	}
}

func testState(t *testing.T) State {
	t.Helper()
	return State{Dir: t.TempDir(), Shared: t.TempDir(), CDP: ""}
}

func TestReapClosesOnlyIdleAppTabs(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.targets = appTargets()
	state := testState(t)
	now := time.Now()
	// Reminders was just used: its stamp is fresh. Notes has no stamp.
	state.Touch("reminders")
	lines := state.Reap(cdp, 15, now)
	if len(fake.closed) != 1 || fake.closed[0] != "t-notes" {
		t.Fatalf("closed = %v, want only the idle notes tab", fake.closed)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "closed: notes") {
		t.Fatalf("lines = %v", lines)
	}
}

func TestReapNothingIdle(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.targets = appTargets()
	state := testState(t)
	now := time.Now()
	state.Touch("notes")
	state.Touch("reminders")
	lines := state.Reap(cdp, 15, now)
	if len(fake.closed) != 0 {
		t.Fatalf("closed = %v", fake.closed)
	}
	if len(lines) != 1 || lines[0] != "nothing idle enough to close" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestReapUnreachable(t *testing.T) {
	state := testState(t)
	lines := state.Reap(NewCDP("http://127.0.0.1:1"), 15, time.Now())
	if len(lines) != 1 || lines[0] != "browser not reachable" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestQuietHours(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	at := func(h, m int) time.Time {
		return time.Date(2026, 3, 10, h, m, 0, 0, loc)
	}
	for _, tc := range []struct {
		when time.Time
		want bool
	}{
		{at(22, 59), false}, {at(23, 0), true}, {at(3, 0), true},
		{at(6, 59), true}, {at(7, 0), false}, {at(12, 0), false},
	} {
		if got := QuietHoursAt(loc, tc.when); got != tc.want {
			t.Errorf("QuietHoursAt(%v) = %v, want %v", tc.when, got, tc.want)
		}
	}
}

func TestTouchAndLastUsed(t *testing.T) {
	state := testState(t)
	if !state.LastUsed("notes").IsZero() {
		t.Fatal("missing stamp must read zero")
	}
	before := time.Now()
	state.Touch("notes")
	if got := state.LastUsed("notes"); got.Before(before) {
		t.Fatalf("stamp = %v", got)
	}
}

func TestBlockedLatch(t *testing.T) {
	state := testState(t)
	if state.Blocked() {
		t.Fatal("fresh state must not be blocked")
	}
	if err := os.WriteFile(state.Latch(), []byte("2026-10-08T12:00:00Z test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !state.Blocked() {
		t.Fatal("latch file must read blocked")
	}
}

func testNotesApp() App {
	return App{Name: "notes", URL: "https://www.icloud.com/notes/", FrameHint: "notes3", ReadyClass: "note-list-item-container"}
}

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestAppOpenWarmTab(t *testing.T) {
	fake, srv, cdp := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = []Target{{ID: "t-notes", Type: "page", URL: "https://www.icloud.com/notes/"}}
	fake.frameURLs["t-notes"] = "https://www.icloud.com/notes/notes3.html"
	fake.evalArrays["t-notes"] = []any{[]any{"L1"}}
	state := testState(t)
	state.CDP = srv.URL
	_ = cdp
	loc := amsterdam(t)
	noon := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
	tab, err := testNotesApp().Open(state, loc, noon)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tab.Close()
	if got := state.LastUsed("notes"); got.IsZero() {
		t.Fatal("warm tab did not touch the stamp")
	}
	if fake.hits["Page.navigate"] != 0 {
		t.Fatal("warm tab must not navigate")
	}
}

func TestAppOpenLatched(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	state := testState(t)
	state.CDP = srv.URL
	if err := os.WriteFile(state.Latch(), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := testNotesApp().Open(state, amsterdam(t), time.Now())
	if _, ok := err.(*NeedsApprovalError); !ok {
		t.Fatalf("err = %v, want NeedsApproval", err)
	}
	if fake.hits["Target.createTarget"] != 0 {
		t.Fatal("latched open must not touch tabs")
	}
}

func TestAppOpenSignedOut(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "other"}}
	state := testState(t)
	state.CDP = srv.URL
	_, err := testNotesApp().Open(state, amsterdam(t), time.Now())
	if _, ok := err.(*SignedOutError); !ok {
		t.Fatalf("err = %v, want SignedOut", err)
	}
}

func TestAppOpenQuietRefusal(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = []Target{}
	state := testState(t)
	state.CDP = srv.URL
	loc := amsterdam(t)
	night := time.Date(2026, 3, 10, 3, 0, 0, 0, loc)
	_, err := testNotesApp().Open(state, loc, night)
	need, ok := err.(*NeedsApprovalError)
	if !ok {
		t.Fatalf("err = %v, want NeedsApproval", err)
	}
	if !strings.Contains(need.Msg, "middle of the night") {
		t.Fatalf("msg = %q", need.Msg)
	}
	if len(fake.created) != 1 {
		t.Fatalf("created = %v, want the fresh tab", fake.created)
	}
	if len(fake.closed) != 1 || fake.closed[0] != "t-new" {
		t.Fatalf("closed = %v, want the fresh tab closed", fake.closed)
	}
	if fake.hits["Page.navigate"] != 0 {
		t.Fatal("quiet refusal must not navigate")
	}
}

func TestAppOpenSettlesGrowingList(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	fake.targets = []Target{}
	state := testState(t)
	state.CDP = srv.URL
	fake.frameURLs["t-new"] = "https://www.icloud.com/notes/notes3.html"
	fake.evalArrays["t-new"] = []any{[]any{"a"}, []any{"a", "b"}, []any{"a", "b"}}
	loc := amsterdam(t)
	noon := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
	tab, err := testNotesApp().Open(state, loc, noon)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tab.Close()
	if n := fake.hits["Runtime.evaluate"]; n < 3 {
		t.Fatalf("settle loop evaluated %d times, want >= 3", n)
	}
}

func TestEvalMainFindsDefaultContext(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.targets = []Target{{ID: "t-app", Type: "page", URL: "https://www.icloud.com/reminders/"}}
	fake.frameURLs["t-app"] = "https://www.icloud.com/reminders/reminders2.html"
	fake.texts["t-app"] = "main-world-here"
	state := testState(t)
	state.CDP = srv.URL
	cdp := NewCDP(srv.URL)
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	session, err := conn.tabAttach("t-app")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.tabDetach(session)
	raw, err := conn.evalMain(session, "t-app", "reminders2", `() => "x"`, nil)
	if err != nil {
		t.Fatalf("evalMain: %v", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s != "main-world-here" {
		t.Fatalf("got %s, %v", raw, err)
	}
	_ = state
}

// TestSignOutWipesTheLocalSession covers the local half of sign_out: the
// jar and the latch go first, then every cookie and the site data, and
// the Notes and Reminders tabs close. (Apple's own menu is driven only
// with menu=true, against the real page.)
func TestSignOutWipesTheLocalSession(t *testing.T) {
	fake, srv, _ := newFakeCDP(t)
	fake.targets = appTargets()
	fake.cookies = []map[string]any{{"name": "X-APPLE-WEBAUTH-TOKEN"}}
	state := testState(t)
	state.CDP = srv.URL
	jar := filepath.Join(state.Dir, "cookies.json")
	if err := os.WriteFile(jar, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	state.LatchBlocked("test")
	res, err := SignOut(state, false)
	if err != nil {
		t.Fatalf("SignOut: %v", err)
	}
	if !res.CookiesCleared || !res.JarRemoved || res.AppleSignOut {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(jar); !os.IsNotExist(err) {
		t.Fatal("the cookie jar survived the sign-out")
	}
	if state.Blocked() {
		t.Fatal("the latch survived the sign-out; the next failure would route to reask_access")
	}
	if fake.hits["Storage.clearCookies"] != 1 || fake.hits["Storage.clearDataForOrigin"] != len(signedOutOrigins) {
		t.Fatalf("wipe calls: %v", fake.hits)
	}
	if res.TabsClosed != 2 {
		t.Fatalf("closed %d app tabs (%v), want the Notes and Reminders tabs", res.TabsClosed, fake.closed)
	}
}

// dialFake attaches to one target of the fake browser the way a Tab does.
func dialFake(t *testing.T, cdp *CDP, target string) (*wsConn, string) {
	t.Helper()
	wsURL, err := cdp.DebuggerURL()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialWS(wsURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.close() })
	session, err := conn.tabAttach(target)
	if err != nil {
		t.Fatal(err)
	}
	return conn, session
}

// TestCDPReadsReplyOver32KiB covers the box's failure: a Reminders
// records reply over the websocket library's default 32 KiB limit was
// refused ("read limited at 32769 bytes").
func TestCDPReadsReplyOver32KiB(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	fake.targets = []Target{{ID: "t-app", Type: "page", URL: "https://www.icloud.com/reminders/"}}
	fake.frameURLs["t-app"] = "https://www.icloud.com/reminders/reminders2.html"
	big := strings.Repeat("r", 5<<20)
	fake.evalArrays["t-app"] = []any{big}
	conn, session := dialFake(t, cdp, "t-app")
	raw, err := conn.isolatedWorld(session, "t-app", "reminders2", `() => ""`, nil)
	if err != nil {
		t.Fatalf("large reply: %v", err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || len(s) != len(big) {
		t.Fatalf("got %d bytes, %v; want %d", len(s), err, len(big))
	}
}

// TestCDPCallAfterFailedRead covers the call after a refused message: the
// same connection resumed mid-frame and failed with "unexpected rsv bits".
// It re-dials instead, re-attaches the tab and keeps the caller's session
// id working, timezone included.
func TestCDPCallAfterFailedRead(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	defer func(n int64) { cdpReadLimit = n }(cdpReadLimit)
	cdpReadLimit = 4096
	fake.targets = []Target{{ID: "t-app", Type: "page", URL: "https://www.icloud.com/reminders/"}}
	fake.frameURLs["t-app"] = "https://www.icloud.com/reminders/reminders2.html"
	fake.evalArrays["t-app"] = []any{"tz marker", strings.Repeat("r", 64<<10), "small"}
	conn, session := dialFake(t, cdp, "t-app")
	conn.setTimezone(session, "Europe/Berlin")
	if _, err := conn.isolatedWorld(session, "t-app", "reminders2", `() => ""`, nil); err == nil {
		t.Fatal("reply over the lowered limit was read")
	}
	fake.mu.Lock()
	tzBefore := fake.hits["Emulation.setTimezoneOverride"]
	fake.mu.Unlock()
	raw, err := conn.isolatedWorld(session, "t-app", "reminders2", `() => ""`, nil)
	if err != nil {
		t.Fatalf("call after the failed read: %v", err)
	}
	if string(raw) != `"small"` {
		t.Fatalf("got %s", raw)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.attaches != 2 {
		t.Fatalf("attaches = %d, want the tab re-attached once", fake.attaches)
	}
	if fake.hits["Emulation.setTimezoneOverride"] != tzBefore+1 {
		t.Fatal("the re-attached session lost its timezone override")
	}
}

// TestRenavigateOpensMissingTab covers the re-ask deadlock: the grant is
// latched, the browser restarted, and only the icloud.com page is left.
// The re-ask opens the app tab, applies the owner's zone before the
// navigation, and never answers "no tab for".
func TestRenavigateOpensMissingTab(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	defer func(n int) { RenavigateSettleMS = n }(RenavigateSettleMS)
	RenavigateSettleMS = 0
	state := testState(t)
	state.LatchBlocked("reminders reported a lapsed grant")
	fake.targets = []Target{{ID: "t-home", Type: "page", URL: "https://www.icloud.com/"}}
	if err := Renavigate(cdp, "https://www.icloud.com/reminders/", "Europe/Berlin"); err != nil {
		t.Fatalf("Renavigate: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.created) != 1 || fake.created[0] != "about:blank" {
		t.Fatalf("created = %v, want one blank tab", fake.created)
	}
	if len(fake.navigated) != 1 || fake.navigated[0] != "https://www.icloud.com/reminders/" {
		t.Fatalf("navigated = %v, want the app URL once", fake.navigated)
	}
	tz, nav := -1, -1
	for i, m := range fake.calls {
		if m == "Emulation.setTimezoneOverride" && tz < 0 {
			tz = i
		}
		if m == "Page.navigate" {
			nav = i
		}
	}
	if tz < 0 || tz > nav {
		t.Fatalf("calls = %v, want the timezone override before the navigation", fake.calls)
	}
	if len(fake.closed) != 0 {
		t.Fatalf("closed = %v, the new app tab must stay open", fake.closed)
	}
}

func TestRenavigateReusesExistingTab(t *testing.T) {
	fake, _, cdp := newFakeCDP(t)
	defer func(n int) { RenavigateSettleMS = n }(RenavigateSettleMS)
	RenavigateSettleMS = 0
	fake.targets = appTargets()
	if err := Renavigate(cdp, "https://www.icloud.com/reminders/", "Europe/Berlin"); err != nil {
		t.Fatalf("Renavigate: %v", err)
	}
	if len(fake.created) != 0 || fake.hits["Page.navigate"] != 1 {
		t.Fatalf("created = %v, navigations = %d", fake.created, fake.hits["Page.navigate"])
	}
}
