package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// innerTextExpr reads a frame the way a person would: the rendered text,
// tolerating frames with no body.
const innerTextExpr = `(() => { try { return (document.body && document.body.innerText) || ''; } catch (e) { return ''; } })()`

// wsConn is one CDP session over the browser websocket. Commands go out
// with ascending ids; events (frames with a "method" key) are skipped while
// waiting for the matching reply. Calls serialize on a mutex: without it
// concurrent calls interleave sends and steal each other's replies.
// Single-flight: callers still sequence their own flows.
type wsConn struct {
	ws   *websocket.Conn
	mu   sync.Mutex
	next int64
}

func dialWS(url string) (*wsConn, error) {
	ws, err := websocket.Dial(url, "", "http://localhost/")
	if err != nil {
		return nil, err
	}
	return &wsConn{ws: ws}, nil
}

func (c *wsConn) close() error { return c.ws.Close() }

func nowMS() int64 { return time.Now().UnixNano() / 1e6 }

func sleepMS(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// recvTimeout reads one message, giving up after ms. A timeout is a miss,
// not an error: event listeners poll with it.
func (c *wsConn) recvTimeout(ms int) (map[string]any, bool) {
	_ = c.ws.SetReadDeadline(time.Now().Add(time.Duration(ms) * time.Millisecond))
	defer c.ws.SetReadDeadline(time.Time{})
	var raw map[string]any
	if err := websocket.JSON.Receive(c.ws, &raw); err != nil {
		return nil, false
	}
	return raw, true
}

func (c *wsConn) call(sessionID, method string, params, result any) error {
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
	if err := websocket.JSON.Send(c.ws, msg); err != nil {
		return err
	}
	// Bounded: a stalled browser must surface as a timeout, never hang a
	// lock-holding tool call forever. 300s clears the slowest legitimate
	// call (a first CloudKit sync pass) with margin.
	deadline := time.Now().Add(300 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("CDP %s: timed out waiting for a reply", method)
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(minDuration(remaining, 30*time.Second)))
		var raw map[string]any
		if err := websocket.JSON.Receive(c.ws, &raw); err != nil {
			if isTimeout(err) {
				continue
			}
			return err
		}
		if _, isEvent := raw["method"]; isEvent {
			continue
		}
		gotID, _ := raw["id"].(float64)
		if int(gotID) != id {
			continue
		}
		if errmsg, ok := raw["error"].(map[string]any); ok {
			return fmt.Errorf("CDP %s: %v", method, errmsg["message"])
		}
		if result == nil {
			return nil
		}
		reb, err := json.Marshal(raw["result"])
		if err != nil {
			return err
		}
		return json.Unmarshal(reb, result)
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

type timeoutError interface{ Timeout() bool }

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
	ID       string      `json:"id"`
	URL      string      `json:"url"`
	Children []frameNode `json:"childFrames"`
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
