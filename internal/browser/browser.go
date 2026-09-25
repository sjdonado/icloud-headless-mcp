// Package browser holds what the server modules and the bin helpers share:
// the CDP attach surface, the blocked latch, quiet hours, and the tab-use
// stamps that keep the reaper from closing a tab mid-session.
//
// Browser DRIVING (DOM, clicks, app flows) is Phase 4 and lives behind the
// driver decision. Everything here is driver-neutral: plain CDP over HTTP
// plus a minimal websocket client, so session-check, the tab reaper, and
// the drain work regardless of which driver wins the spike.
package browser

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// Quiet hours bound fresh app-page loads, in the owner's zone,
	// inclusive of the start: no prompt ever wakes the owner at night.
	QuietFrom, QuietUntil = 23, 7
)

// State bundles the directories one install runs under.
type State struct {
	// Dir is ICLOUD_STATE: profile, locks, stamps. STATE_DIR is Dir/state.
	Dir string
	// Shared is ICLOUD_SHARED_STATE: the blocked latch and the queue.
	Shared string
	// CDP is where the resident Chromium listens for DevTools.
	CDP string
}

// StateDir is Dir/state, home of locks and tab-use stamps.
func (s State) StateDir() string { return filepath.Join(s.Dir, "state") }

// Latch is the blocked-latch path. While it exists, app helpers refuse
// before touching the browser: retrying cannot produce a different answer
// until the owner approves.
func (s State) Latch() string { return filepath.Join(s.Shared, "blocked") }

// Blocked reports whether iCloud is latched as needing approval.
func (s State) Blocked() bool {
	_, err := os.Stat(s.Latch())
	return err == nil
}

// Touch records that app was just used, so the reaper will not close its
// tab for idleness. Every entry point calls this.
func (s State) Touch(app string) {
	dir := s.StateDir()
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, app+".last"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
	now := time.Now()
	_ = os.Chtimes(filepath.Join(dir, app+".last"), now, now)
}

// LastUsed is when app last touched its stamp, or the zero time when no
// marker exists: nothing has claimed the tab, so it counts as idle.
func (s State) LastUsed(app string) time.Time {
	info, err := os.Stat(filepath.Join(s.StateDir(), app+".last"))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// QuietHours reports whether it is the middle of the owner's night: hour
// 23:00 through 06:59 in the owner's zone. A fresh app-page load then would
// raise a device prompt nobody can answer before it expires.
func QuietHoursAt(owner *time.Location, now time.Time) bool {
	hour := now.In(owner).Hour()
	return hour >= QuietFrom || hour < QuietUntil
}

// Target is one CDP target from /json/list.
type Target struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`

	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// CDP speaks the DevTools HTTP endpoints. No driver library: these calls
// are plain HTTP and stable across drivers.
type CDP struct {
	BaseURL string
	client  *http.Client
}

// NewCDP attaches to the resident browser at baseURL.
func NewCDP(baseURL string) *CDP {
	return &CDP{BaseURL: baseURL, client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *CDP) get(path string, out any) error {
	resp, err := c.client.Get(c.BaseURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CDP %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Targets lists live targets. Unreachable when the browser is down.
func (c *CDP) Targets() ([]Target, error) {
	var targets []Target
	if err := c.get("/json/list", &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// CloseTarget closes one tab by id.
func (c *CDP) CloseTarget(id string) error {
	resp, err := c.client.Get(c.BaseURL + "/json/close/" + id)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CDP close %s: %s", id, resp.Status)
	}
	return nil
}

// DebuggerURL is the browser websocket endpoint from /json/version.
func (c *CDP) DebuggerURL() (string, error) {
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := c.get("/json/version", &version); err != nil {
		return "", err
	}
	if version.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("CDP /json/version has no debugger URL")
	}
	return version.WebSocketDebuggerURL, nil
}

// NewTarget opens a tab on url.
func (c *CDP) NewTarget(url string) (Target, error) {
	var tg Target
	req, err := http.NewRequest(http.MethodPut, c.BaseURL+"/json/new?"+url, nil)
	if err != nil {
		return tg, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return tg, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tg, fmt.Errorf("CDP new tab: %s", resp.Status)
	}
	return tg, json.NewDecoder(resp.Body).Decode(&tg)
}
