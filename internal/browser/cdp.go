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
type wsConn struct {
	ws      *ws.Conn
	mu      sync.Mutex
	next    int64
	pending []map[string]any
}

func dialWS(wsURL string) (*wsConn, error) {
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
	return &wsConn{ws: conn}, nil
}

func (c *wsConn) close() error { return c.ws.Close(ws.StatusNormalClosure, "") }

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
		return err
	}
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return &callTimeoutError{method: method}
			}
			return err
		}
		var reply map[string]any
		if err := json.Unmarshal(data, &reply); err != nil {
			continue
		}
		if _, isEvent := reply["method"]; isEvent {
			// Queue, don't drop: request sniffing and crash watching
			// read the stream between calls.
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
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
