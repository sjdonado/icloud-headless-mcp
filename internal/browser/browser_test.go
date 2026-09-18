package browser

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
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
	mux.Handle("/ws", websocket.Handler(f.serveWS))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv, NewCDP(srv.URL)
}

func (f *fakeCDP) reply(ws *websocket.Conn, id float64, result any) {
	f.t.Helper()
	if err := websocket.JSON.Send(ws, map[string]any{"id": id, "result": result}); err != nil {
		f.t.Fatalf("fake send: %v", err)
	}
}

func (f *fakeCDP) serveWS(ws *websocket.Conn) {
	defer ws.Close()
	attached := ""
	for {
		var msg map[string]any
		if err := websocket.JSON.Receive(ws, &msg); err != nil {
			return
		}
		method, _ := msg["method"].(string)
		id, _ := msg["id"].(float64)
		params, _ := msg["params"].(map[string]any)
		f.mu.Lock()
		f.hits[method]++
		switch method {
		case "Storage.getCookies":
			cookies := f.cookies
			if cookies == nil {
				cookies = []map[string]any{}
			}
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"cookies": cookies})
		case "Target.attachToTarget":
			if p, ok := params["targetId"].(string); ok {
				attached = p
			}
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"sessionId": "S1"})
		case "Target.createTarget":
			u, _ := params["url"].(string)
			f.created = append(f.created, u)
			f.mu.Unlock()
			f.reply(ws, id, map[string]any{"targetId": "t-new"})
		case "Page.navigate", "Target.detachFromTarget",
			"Browser.grantPermissions", "Emulation.setTimezoneOverride", "Network.enable":
			f.mu.Unlock()
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
			_ = websocket.JSON.Send(ws, map[string]any{
				"id": id, "error": map[string]any{"message": "unknown method " + method},
			})
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
