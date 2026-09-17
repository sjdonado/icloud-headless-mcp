package browser

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file extends the raw CDP session with driving primitives: frames,
// input, navigation, and event listening. Everything the tools do through
// Playwright maps to one of these; nothing here needs a driver library.

// CollectJS returns elements matching an exact class token, from every open
// shadow root. Substring class matching catches wrappers that repeat every
// entry, so the token comparison is exact.
const CollectJS = `(cls) => {
  const found = [];
  const walk = (root, depth) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      if ((el.className || '').toString().split(/\s+/).includes(cls)) found.push(el);
    }
  };
  walk(document, 0);
  return found.map(el => (el.innerText || '').trim());
}`

// ClickJS clicks the nth element with a class token using real pointer
// events. A bare .click() is ignored by these apps, and the virtualised
// lists put rows outside the viewport where a coordinate click dies.
const ClickJS = `([cls, index]) => {
  const found = [];
  const walk = (root, depth) => {
    if (depth > 14) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
      if ((el.className || '').toString().split(/\s+/).includes(cls)) found.push(el);
    }
  };
  walk(document, 0);
  const target = found[index];
  if (!target) return false;
  target.scrollIntoView({block: 'center'});
  const box = target.getBoundingClientRect();
  const x = box.left + box.width / 2, y = box.top + box.height / 2;
  const fire = (type, Ctor) => target.dispatchEvent(new Ctor(type, {
    bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y, button: 0,
    pointerId: 1, isPrimary: true,
  }));
  fire('pointerdown', PointerEvent);
  fire('mousedown', MouseEvent);
  fire('pointerup', PointerEvent);
  fire('mouseup', MouseEvent);
  fire('click', MouseEvent);
  return true;
}`

// AlertJS returns the position of a blocking alert's OK button, which is a
// div.cw-button rather than a button, so tag searches miss it. It returns
// the position instead of clicking: a synthetic click on one is ignored,
// only a real pointer event dismisses it.
const AlertJS = `() => {
  const walk = (root, d, fn) => {
    if (d > 20) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, d + 1, fn);
      fn(el);
    }
  };
  let title = '', ok = null;
  walk(document, 0, (el) => {
    const cls = (el.className || '').toString();
    if (!ok && cls.includes('cw-button') && /^(OK|Ok|Dismiss)$/.test((el.innerText || '').trim())) {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) ok = el;
    }
    if (!title && cls.split(/\s+/).includes('alert-title')) title = (el.innerText || '').trim();
  });
  if (!ok) return JSON.stringify({none: true});
  if (!title) {
    walk(document, 0, (el) => {
      const cls = (el.className || '').toString();
      if (!title && /cw-alert/.test(cls)) {
        const t = (el.innerText || '').trim().split('\n')[0];
        if (t && t.length < 80) title = t;
      }
    });
  }
  const r = ok.getBoundingClientRect();
  return JSON.stringify({title: title || 'an iCloud alert',
                         x: Math.round(r.x + r.width/2),
                         y: Math.round(r.y + r.height/2)});
}`

// FrameIDs lists every frame id under the tab, via the frame tree.
func (c *wsConn) frameIDs(session, targetID string) ([]frameNode, error) {
	var tree struct {
		FrameTree struct {
			Frame frameNode `json:"frame"`
		} `json:"frameTree"`
	}
	_ = targetID
	if err := c.call(session, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
		return nil, err
	}
	var out []frameNode
	var walk func(f frameNode)
	walk = func(f frameNode) {
		out = append(out, f)
		for _, child := range f.Children {
			walk(child)
		}
	}
	walk(tree.FrameTree.Frame)
	return out, nil
}

// isolatedWorld runs expr in an isolated world of the first frame whose URL
// contains hint, and returns the raw result. Isolated worlds see the same
// DOM (shadow roots included) without depending on page JS, and they reach
// cross-origin iframes the way the frame loop does. arg is passed as the
// snippet's single argument when non-nil.
func (c *wsConn) isolatedWorld(session, targetID, hint, expr string, arg any) (json.RawMessage, error) {
	frames, err := c.frameIDs(session, targetID)
	if err != nil {
		return nil, err
	}
	frameID := ""
	for _, f := range frames {
		if hint == "" || strings.Contains(f.URL, hint) {
			frameID = f.ID
			break
		}
	}
	if frameID == "" {
		return nil, fmt.Errorf("no frame matching %q", hint)
	}
	return c.evalInFrame(session, frameID, expr, arg)
}

func (c *wsConn) evalInFrame(session, frameID, expr string, arg any) (json.RawMessage, error) {
	var world struct {
		ExecutionContextID int `json:"executionContextId"`
	}
	if err := c.call(session, "Page.createIsolatedWorld",
		map[string]any{"frameId": frameID, "worldName": "icloud-agent"}, &world); err != nil {
		return nil, err
	}
	args := []any{}
	if arg != nil {
		args = append(args, arg)
	}
	// Wrap so snippets written as (args) => ... or () => ... both work,
	// with or without an argument.
	wrapped := fmt.Sprintf("(%s).apply(null, %s)", expr, mustJSON(args))
	var eval struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails,omitempty"`
	}
	if err := c.call(session, "Runtime.evaluate", map[string]any{
		"expression":    wrapped,
		"contextId":     world.ExecutionContextID,
		"returnByValue": true,
		"awaitPromise":  true,
	}, &eval); err != nil {
		return nil, err
	}
	if eval.Exception != nil {
		return nil, fmt.Errorf("evaluate: %s", eval.Exception.Text)
	}
	return eval.Result.Value, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TabAttach attaches to a tab target and returns its flattened session.
// Detach when done; sessions are cheap but not free.
func (c *wsConn) tabAttach(targetID string) (string, error) {
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call("", "Target.attachToTarget",
		map[string]any{"targetId": targetID, "flatten": true}, &attached); err != nil {
		return "", err
	}
	return attached.SessionID, nil
}

func (c *wsConn) tabDetach(session string) {
	// Canonical form: the session id travels as the detach parameter, not
	// the envelope. The envelope form addresses the session being
	// detached, which is exactly what is going away.
	_ = c.call("", "Target.detachFromTarget", map[string]any{"sessionId": session}, nil)
}

// Navigate loads url in the tab. Goto, never reload: reloading an iCloud
// app tab reliably kills Chromium; navigating does not.
func (c *wsConn) navigate(session, url string) error {
	return c.call(session, "Page.navigate", map[string]any{"url": url}, nil)
}

// NewTab opens a tab and returns its target id.
func (c *wsConn) newTab(url string) (string, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call("", "Target.createTarget", map[string]any{"url": url}, &created); err != nil {
		return "", err
	}
	return created.TargetID, nil
}

// GrantClipboard allows clipboard read/write on the iCloud origin, which
// the canvas-copy and paste flows need.
func (c *wsConn) grantClipboard() error {
	return c.call("", "Browser.grantPermissions", map[string]any{
		"origin":      "https://www.icloud.com",
		"permissions": []string{"clipboardReadWrite"},
	}, nil)
}

// SetTimezone makes the page believe it is in the owner's zone. Best
// effort: the override must precede the app load to matter, and callers
// arrange that; a failure here degrades to UTC behavior, not an error.
func (c *wsConn) setTimezone(session, zone string) {
	_ = c.call(session, "Emulation.setTimezoneOverride", map[string]any{"timezoneId": zone}, nil)
	_, _ = c.isolatedWorld(session, "", "", `() => { window.__agentTzApplied = true; }`, nil)
}

// MouseClick sends a real pointer click at viewport coordinates.
func (c *wsConn) mouseClick(session string, x, y int) error {
	for _, t := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": t, "x": x, "y": y, "button": "left", "clickCount": 1}
		if t == "mouseMoved" {
			delete(params, "button")
			delete(params, "clickCount")
		}
		if err := c.call(session, "Input.dispatchMouseEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}

// MouseMove hovers without clicking, which is what reveals the row's info
// control.
func (c *wsConn) mouseMove(session string, x, y int) error {
	return c.call(session, "Input.dispatchMouseEvent",
		map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil)
}

// InsertText types text as the platform would, advancing segmented inputs
// as they fill.
func (c *wsConn) insertText(session, text string) error {
	return c.call(session, "Input.insertText", map[string]any{"text": text}, nil)
}

// KeyPress presses one named key: Enter, Tab, Escape, arrows, or a
// single letter. delayMS paces keystrokes the way the delay= probes needed.
func (c *wsConn) keyPress(session, key string, delayMS int) error {
	code, windowsCode, text := key, 0, ""
	switch key {
	case "Enter":
		code, windowsCode, text = "Enter", 13, "\r"
	case "Tab":
		code, windowsCode = "Tab", 9
	case "Escape":
		code, windowsCode = "Escape", 27
	case "Delete":
		code, windowsCode = "Delete", 46
	case "Backspace":
		code, windowsCode = "Backspace", 8
	case "ArrowUp":
		code, windowsCode = "ArrowUp", 38
	case "ArrowDown":
		code, windowsCode = "ArrowDown", 40
	case "ArrowLeft":
		code, windowsCode = "ArrowLeft", 37
	case "ArrowRight":
		code, windowsCode = "ArrowRight", 39
	default:
		code, text = key, key
	}
	for _, typ := range []string{"keyDown", "keyUp"} {
		params := map[string]any{"type": typ, "key": key, "code": code}
		if windowsCode != 0 {
			params["windowsVirtualKeyCode"] = windowsCode
		}
		if typ == "keyDown" && text != "" {
			params["text"] = text
		}
		if err := c.call(session, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}
	if delayMS > 0 {
		sleepMS(delayMS)
	}
	return nil
}

// KeyCombo holds Control and taps key: Control+A/C/V/Z for select, copy,
// paste, and undo.
func (c *wsConn) keyCombo(session, key string) error {
	up := strings.ToUpper(key)
	code := "Key" + up
	vk := 0
	switch up {
	case "A":
		vk = 65
	case "C":
		vk = 67
	case "V":
		vk = 86
	case "Z":
		vk = 90
	}
	events := []map[string]any{
		{"type": "keyDown", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17},
		{"type": "keyDown", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": 2},
		{"type": "keyUp", "key": key, "code": code, "windowsVirtualKeyCode": vk, "modifiers": 2},
		{"type": "keyUp", "key": "Control", "code": "ControlLeft", "windowsVirtualKeyCode": 17},
	}
	for _, params := range events {
		if err := c.call(session, "Input.dispatchKeyEvent", params, nil); err != nil {
			return err
		}
	}
	return nil
}

// ListenEvents collects matching CDP events for up to dur. Used where the
// only signal is the event stream: request sniffing for Drive, crash
// reports for the resident.
func (c *wsConn) listenEvents(durMS int, match func(method string, params map[string]any) bool) []map[string]any {
	var out []map[string]any
	deadline := nowMS() + int64(durMS)
	for {
		remaining := deadline - nowMS()
		if remaining <= 0 {
			return out
		}
		msg, ok := c.recvTimeout(int(remaining))
		if !ok {
			return out
		}
		method, _ := msg["method"].(string)
		if method == "" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		if match(method, params) {
			out = append(out, params)
		}
	}
}
