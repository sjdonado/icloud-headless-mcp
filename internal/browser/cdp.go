package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	ws "nhooyr.io/websocket"
)

// innerTextExpr reads a frame the way a person would: the rendered text,
// tolerating frames with no body. A bare function, not an invoked one:
// evalInFrame applies it, and applying an already-invoked result throws.
const innerTextExpr = `(() => { try { return (document.body && document.body.innerText) || ''; } catch (e) { return ''; } })`

// wsConn is one CDP session over the browser websocket. Commands go out
// with ascending ids; events skipped while awaiting a reply are queued
// rather than dropped, so request sniffing sees traffic that fired during
// another call. Calls serialize on a mutex: without it concurrent calls
// interleave sends and steal each other's replies.
//
// A read or write error leaves the websocket unusable: the library closes
// it on a timeout, and after an oversized message it resumes mid-frame
// ("unexpected rsv bits"). So an error marks the connection broken, and
// the next call re-dials and re-attaches every tab attached through it.
// Callers keep the session ids they were given; tabs maps each to the
// live one.
type wsConn struct {
	url     string
	ws      *ws.Conn
	broken  bool
	mu      sync.Mutex
	next    int64
	pending []map[string]any
	tabs    map[string]*attachment
	// clipboard records a clipboard grant, which dies with the socket.
	clipboard bool
}

// attachment is one tab attached through a wsConn: the target, its live
// session id, and the timezone override the session carries.
type attachment struct{ target, session, zone string }

// cdpReadLimit caps one CDP message. The library's default of 32 KiB
// refused a real account's Reminders records (a full CloudKit pass is one
// Runtime.evaluate reply); the socket is loopback, so the cap only guards
// against a runaway reply.
var cdpReadLimit int64 = 256 << 20

func dial(wsURL string) (*ws.Conn, error) {
	// No Origin header, deliberately: Chromium's DevTools endpoint 403s
	// any handshake carrying one and accepts handshakes without one.
	// x/net/websocket always sends Origin and can never connect here,
	// which is why this transport is nhooyr.
	dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := ws.Dial(dialCtx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(cdpReadLimit)
	return conn, nil
}

func dialWS(wsURL string) (*wsConn, error) {
	conn, err := dial(wsURL)
	if err != nil {
		return nil, err
	}
	return &wsConn{url: wsURL, ws: conn, tabs: map[string]*attachment{}}, nil
}

func (c *wsConn) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return c.ws.CloseNow()
	}
	return c.ws.Close(ws.StatusNormalClosure, "")
}

// redial replaces a broken websocket and re-attaches its tabs, with their
// timezone overrides, which live on the session. Events already queued
// stay queued: they happened. A tab that no longer attaches (closed by
// the reaper or a crash) is dropped, so only calls on it fail. Runs
// under c.mu.
func (c *wsConn) redial() error {
	_ = c.ws.CloseNow()
	conn, err := dial(c.url)
	if err != nil {
		return fmt.Errorf("the CDP connection broke and could not be re-dialled: %w", err)
	}
	c.ws, c.broken = conn, false
	if c.clipboard {
		_ = c.roundTrip("", "Browser.grantPermissions", clipboardGrant, nil, 15*time.Second)
	}
	for key, a := range c.tabs {
		if c.broken {
			break
		}
		var attached struct {
			SessionID string `json:"sessionId"`
		}
		if err := c.roundTrip("", "Target.attachToTarget",
			map[string]any{"targetId": a.target, "flatten": true}, &attached, 15*time.Second); err != nil {
			delete(c.tabs, key)
			continue
		}
		a.session = attached.SessionID
		if a.zone != "" {
			_ = c.roundTrip(a.session, "Emulation.setTimezoneOverride", map[string]any{"timezoneId": a.zone}, nil, 15*time.Second)
		}
	}
	if c.broken {
		return fmt.Errorf("the CDP connection broke again while it was re-dialled")
	}
	return nil
}

func nowMS() int64 { return time.Now().UnixNano() / 1e6 }

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func (c *wsConn) call(sessionID, method string, params, result any) error {
	return c.callTimeout(sessionID, method, params, result, 300*time.Second)
}

// callTimeoutError is a CDP round-trip that outlived its deadline. It
// implements Timeout so the health check reports a timeout, not a blob.
type callTimeoutError struct{ method string }

func (e *callTimeoutError) Error() string {
	return "CDP " + e.method + ": timed out waiting for a reply"
}

func (e *callTimeoutError) Timeout() bool { return true }

func (c *wsConn) callTimeout(sessionID, method string, params, result any, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		if err := c.redial(); err != nil {
			return err
		}
	}
	if a := c.tabs[sessionID]; a != nil {
		sessionID = a.session
	}
	return c.roundTrip(sessionID, method, params, result, timeout)
}

// roundTrip sends one command and waits for its reply. Runs under c.mu.
func (c *wsConn) roundTrip(sessionID, method string, params, result any, timeout time.Duration) error {
	id := int(atomic.AddInt64(&c.next, 1))
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Bounded: a stalled browser must surface as a timeout, never hang a
	// lock-holding tool call forever. The default 300s clears the slowest
	// legitimate call (a first CloudKit sync pass) with margin.
	if err := c.ws.Write(ctx, ws.MessageText, raw); err != nil {
		c.broken = true
		return err
	}
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.broken = true
			if ctx.Err() == context.DeadlineExceeded {
				return &callTimeoutError{method: method}
			}
			return err
		}
		var reply map[string]any
		if err := json.Unmarshal(data, &reply); err != nil {
			continue
		}
		if method, isEvent := reply["method"].(string); isEvent {
			// Queue, don't drop: request sniffing and crash watching
			// read the stream between calls. Console and exception
			// events are the exception: nothing reads them, and a
			// Runtime.enable replays the page's whole console buffer
			// (281 messages on Reminders, measured) right after the
			// context events, which trimmed those out of the queue.
			if method == "Runtime.consoleAPICalled" || method == "Runtime.exceptionThrown" {
				continue
			}
			c.pending = append(c.pending, reply)
			if len(c.pending) > 256 {
				c.pending = c.pending[len(c.pending)-256:]
			}
			continue
		}
		gotID, _ := reply["id"].(float64)
		if int(gotID) != id {
			continue
		}
		if errmsg, ok := reply["error"].(map[string]any); ok {
			return fmt.Errorf("CDP %s: %v", method, errmsg["message"])
		}
		if result == nil {
			return nil
		}
		reb, err := json.Marshal(reply["result"])
		if err != nil {
			return err
		}
		return json.Unmarshal(reb, result)
	}
}

type timeoutError interface{ Timeout() bool }

// nextEvent returns the next stream event, queued or live. Responses to
// other calls never appear here: call() consumes its own reply inline.
func (c *wsConn) nextEvent(timeoutMS int) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 {
		msg := c.pending[0]
		c.pending = c.pending[1:]
		return msg, true
	}
	if c.broken {
		// Waiting for events re-dials nothing: the events a caller
		// subscribed to belong to the old socket. The next call re-dials.
		sleepMS(min(timeoutMS, 200))
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		// The library closes the socket when a read times out, so even
		// "no event yet" leaves it unusable.
		c.broken = true
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	if _, isEvent := raw["method"]; !isEvent {
		return nil, false
	}
	return raw, true
}

func isTimeout(err error) bool {
	var te timeoutError
	return err != nil && errors.As(err, &te) && te.Timeout()
}

// Cookies returns the browser's cookies, the cheap session signal: without
// X-APPLE-WEBAUTH-TOKEN there is no session to check further.
func (c *wsConn) cookies() ([]map[string]any, error) {
	var result struct {
		Cookies []map[string]any `json:"cookies"`
	}
	if err := c.call("", "Storage.getCookies", map[string]any{}, &result); err != nil {
		return nil, err
	}
	return result.Cookies, nil
}

type frameNode struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// frameTree mirrors Page.getFrameTree: children nest as
// {frame: {...}, childFrames: [...]}, not as bare frames.
type frameTree struct {
	Frame    frameNode   `json:"frame"`
	Children []frameTree `json:"childFrames"`
}

func collectFrames(tree frameTree, out *[]frameNode) {
	*out = append(*out, tree.Frame)
	for _, child := range tree.Children {
		collectFrames(child, out)
	}
}

// frameTexts evaluates innerText in the tab's own frame tree, one isolated
// world per frame. The isolated world reaches cross-origin iframes the way
// page.frames does: Apple's refusal renders inside an iframe while the page
// body stays empty, and a check blind to it reports OK while the apps are
// dead.
func (c *wsConn) frameTexts(targetID string) (string, error) {
	session, err := c.tabAttach(targetID)
	if err != nil {
		return "", err
	}
	defer c.tabDetach(session)
	return c.frameTextsSession(session, targetID)
}

// frameTextsSession evaluates innerText across one tab's frames on an
// existing session, so callers with a tab attached do not attach twice.
func (c *wsConn) frameTextsSession(session, targetID string) (string, error) {

	frames, err := c.frameIDs(session, targetID)
	if err != nil {
		return "", err
	}
	var texts []string
	for _, frame := range frames {
		raw, err := c.evalInFrame(session, frame.ID, innerTextExpr, nil)
		if err != nil {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			continue
		}
		texts = append(texts, text)
	}
	joined := ""
	for i, t := range texts {
		if i > 0 {
			joined += "\n"
		}
		joined += t
	}
	return joined, nil
}
