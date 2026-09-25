package session

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

//go:embed door.html
var doorPage []byte

// ServeDoor is the login door process (`icloud-mcp door`): one HTTP
// listener that streams the resident Chromium's iCloud tab as JPEG frames
// over CDP screencast and forwards the viewer's mouse and keys back as
// CDP input. It needs no display, no VNC server and no noVNC, so it works
// against a headless browser on any host. The password is read once from
// passFile and checked as HTTP Basic auth (username root) on every request.
// Once a sign-in is observed the stream stops, every viewer gets the done
// screen, and the process exits; otherwise it exits by itself at ttl
// whether or not anything reaps its record.
func ServeDoor(cdpURL, addr, passFile string, ttl time.Duration) error {
	raw, err := os.ReadFile(passFile)
	if err != nil {
		return fmt.Errorf("cannot read the door password: %v", err)
	}
	pass := strings.TrimSpace(string(raw))
	if pass == "" {
		return fmt.Errorf("the door password file is empty")
	}
	// The door clears its own record and password before it exits, at TTL
	// or after a sign-in. Otherwise the record outlives the process and a
	// later close signals the recorded pid, which on macOS (no /proc to
	// check a cmdline) could by then belong to another program.
	exit := func() {
		shred(passFile)
		// Only this door's record: a replacement may have written its own.
		record := filepath.Join(filepath.Dir(passFile), "door.json")
		if raw, err := os.ReadFile(record); err == nil {
			var d Door
			if json.Unmarshal(raw, &d) == nil && d.Pid == os.Getpid() {
				_ = os.Remove(record)
			}
		}
		os.Exit(0)
	}
	time.AfterFunc(ttl, exit)
	cdp := browser.NewCDP(cdpURL)
	// A completed sign-in is the door's job done. Stop streaming at once
	// (the signed-in iCloud home shows the owner's data, which the door has
	// no reason to show), tell the viewers, and exit shortly after rather
	// than wait for the TTL or a recovery-tool call to reap it. The parent
	// sweeps the dead record and shreds the password file.
	var finished doneState
	go func() {
		for ; ; time.Sleep(3 * time.Second) {
			switch outcome, _ := cdp.Check(); outcome {
			case browser.OK:
				finished.set("signed-in")
			case browser.NeedsApproval:
				finished.set("needs-approval")
			default:
				continue
			}
			time.AfterFunc(10*time.Second, exit)
			return
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(doorPage)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		if err := relay(r.Context(), cdp, c, &finished); err != nil {
			_ = c.Close(websocket.StatusInternalError, err.Error())
		}
	})
	srv := &http.Server{
		Addr:              addr,
		Handler:           basicAuth(pass, mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.ListenAndServe()
}

// doorUser is the one username the door accepts.
const doorUser = "root"

// basicAuth accepts exactly root and the door password.
func basicAuth(pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, got, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(doorUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(got), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="iCloud login door"`)
			http.Error(w, "door password required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// loginTarget picks the tab to sign in on: the resident's icloud.com home
// tab (never a Notes or Reminders app tab a tool may be driving), else a
// fresh one, reused by later viewers because it is then the home tab.
func loginTarget(cdp *browser.CDP) (string, error) {
	targets, err := cdp.Targets()
	if err != nil {
		return "", err
	}
	for _, tg := range targets {
		if tg.Type == "page" && tg.WebSocketDebuggerURL != "" && isHomeURL(tg.URL) {
			return tg.WebSocketDebuggerURL, nil
		}
	}
	tg, err := cdp.NewTarget("https://www.icloud.com/")
	if err != nil {
		return "", err
	}
	return tg.WebSocketDebuggerURL, nil
}

// isHomeURL reports an icloud.com page that is not an app page.
func isHomeURL(u string) bool {
	rest, ok := strings.CutPrefix(u, "https://www.icloud.com")
	if !ok {
		if rest, ok = strings.CutPrefix(u, "https://icloud.com"); !ok {
			return false
		}
	}
	return rest == "" || rest == "/" || strings.HasPrefix(rest, "/?") || strings.HasPrefix(rest, "/#")
}

// viewerCall maps one viewer message to the only CDP calls a viewer may
// make: pointer and key input, inserted text (the Paste button), and a
// page reload. Anything else, Runtime.evaluate included, is dropped.
func viewerCall(m viewerMsg) (string, any, bool) {
	switch m.Kind {
	case "mouse", "key":
		if m.Method == "Input.dispatchMouseEvent" || m.Method == "Input.dispatchKeyEvent" {
			return m.Method, m.Params, true
		}
	case "text":
		return "Input.insertText", map[string]any{"text": m.Text}, true
	case "reload":
		return "Page.reload", map[string]any{}, true
	}
	return "", nil, false
}

// viewerMsg is one input event from the door page.
type viewerMsg struct {
	Kind   string          `json:"kind"`   // "mouse", "key", "text", "reload"
	Method string          `json:"method"` // the Input.* CDP method for mouse and key
	Params json.RawMessage `json:"params"`
	Text   string          `json:"text"`
}

// doneState is set once, when the door has seen the sign-in it exists for.
type doneState struct {
	mu   sync.Mutex
	kind string
	ch   chan struct{}
}

// chanLocked returns the done channel, making it on first use. d.mu held.
func (d *doneState) chanLocked() chan struct{} {
	if d.ch == nil {
		d.ch = make(chan struct{})
	}
	return d.ch
}

func (d *doneState) wait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.chanLocked()
}

func (d *doneState) set(kind string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.kind == "" {
		d.kind = kind
		close(d.chanLocked())
	}
}

func (d *doneState) get() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.kind
}

// sendDone tells the viewer the login is over; the page swaps the stream
// for the done screen.
func sendDone(ctx context.Context, viewer *websocket.Conn, kind string) error {
	out, _ := json.Marshal(map[string]any{"done": kind})
	if err := viewer.Write(ctx, websocket.MessageText, out); err != nil {
		return err
	}
	return viewer.Close(websocket.StatusNormalClosure, "signed in")
}

// relay bridges one viewer socket to the login tab until either side
// closes, or until the sign-in is observed.
func relay(ctx context.Context, cdp *browser.CDP, viewer *websocket.Conn, finished *doneState) error {
	if kind := finished.get(); kind != "" {
		return sendDone(ctx, viewer, kind)
	}
	wsURL, err := loginTarget(cdp)
	if err != nil {
		return fmt.Errorf("no login tab: %v", err)
	}
	page, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("cannot attach to the login tab: %v", err)
	}
	defer page.CloseNow()
	page.SetReadLimit(64 << 20) // screencast frames are large
	viewer.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	id := 0
	send := func(method string, params any) error {
		mu.Lock()
		defer mu.Unlock()
		id++
		raw, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		if err != nil {
			return err
		}
		return page.Write(ctx, websocket.MessageText, raw)
	}
	if err := send("Page.enable", map[string]any{}); err != nil {
		return err
	}
	// Headless paints only the front tab; a tool may have raised another.
	if err := send("Page.bringToFront", map[string]any{}); err != nil {
		return err
	}
	if err := send("Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 70, "maxWidth": 1280, "maxHeight": 900,
	}); err != nil {
		return err
	}
	errc := make(chan error, 2)
	go func() { // tab -> viewer: frames, acked so the next one comes
		for {
			_, raw, err := page.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			var ev struct {
				Method string `json:"method"`
				Params struct {
					Data      string          `json:"data"`
					SessionID int             `json:"sessionId"`
					Metadata  json.RawMessage `json:"metadata"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &ev) != nil || ev.Method != "Page.screencastFrame" {
				continue
			}
			_ = send("Page.screencastFrameAck", map[string]any{"sessionId": ev.Params.SessionID})
			out, _ := json.Marshal(map[string]any{"data": ev.Params.Data, "metadata": ev.Params.Metadata})
			if err := viewer.Write(ctx, websocket.MessageText, out); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() { // viewer -> tab: input, limited to the Input domain
		for {
			_, raw, err := viewer.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			var m viewerMsg
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			method, params, ok := viewerCall(m)
			if !ok {
				continue
			}
			if err = send(method, params); err != nil {
				errc <- err
				return
			}
		}
	}()
	select {
	case err = <-errc:
	case <-finished.wait():
		return sendDone(ctx, viewer, finished.get())
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure || ctx.Err() != nil {
		return nil
	}
	return err
}
