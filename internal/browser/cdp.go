package browser

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"golang.org/x/net/websocket"
)

// innerTextExpr reads a frame the way a person would: the rendered text,
// tolerating frames with no body.
const innerTextExpr = `(() => { try { return (document.body && document.body.innerText) || ''; } catch (e) { return ''; } })()`

// wsConn is one CDP session over the browser websocket. Commands go out
// with ascending ids; events (frames with a "method" key) are skipped while
// waiting for the matching reply. Single-flight: callers serialize.
type wsConn struct {
	ws   *websocket.Conn
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

func (c *wsConn) call(sessionID, method string, params, result any) error {
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
	for {
		var raw map[string]any
		if err := websocket.JSON.Receive(c.ws, &raw); err != nil {
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
	ID          string      `json:"id"`
	ChildFrames []frameNode `json:"childFrames"`
}

func frameIDs(root frameNode, out *[]string) {
	*out = append(*out, root.ID)
	for _, child := range root.ChildFrames {
		frameIDs(child, out)
	}
}

// frameTexts evaluates innerText in the tab's own frame tree, one isolated
// world per frame. The isolated world reaches cross-origin iframes the way
// page.frames does: Apple's refusal renders inside an iframe while the page
// body stays empty, and a check blind to it reports OK while the apps are
// dead.
func (c *wsConn) frameTexts(targetID string) (string, error) {
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call("", "Target.attachToTarget",
		map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return "", err
	}
	session := attached.SessionID
	defer c.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)

	var tree struct {
		FrameTree struct {
			Frame frameNode `json:"frame"`
		} `json:"frameTree"`
	}
	if err := c.call(session, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return "", err
	}
	var ids []string
	frameIDs(tree.FrameTree.Frame, &ids)
	var texts []string
	for _, frameID := range ids {
		var world struct {
			ExecutionContextID int `json:"executionContextId"`
		}
		if err := c.call(session, "Page.createIsolatedWorld",
			map[string]any{"frameId": frameID, "worldName": "icloud-check"}, &world); err != nil {
			continue
		}
		var eval struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := c.call(session, "Runtime.evaluate", map[string]any{
			"expression":    innerTextExpr,
			"contextId":     world.ExecutionContextID,
			"returnByValue": true,
		}, &eval); err != nil {
			continue
		}
		texts = append(texts, eval.Result.Value)
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
