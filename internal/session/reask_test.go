package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	nws "nhooyr.io/websocket"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
)

// TestReaskOpensMissingTabAndClearsLatch is the box's 2026-09-27 state:
// the grant latched, then the browser restarted, leaving only the
// icloud.com page. The re-ask opens the Reminders tab, navigates it and
// clears the latch; "no tab for" never reaches the caller.
func TestReaskOpensMissingTabAndClearsLatch(t *testing.T) {
	defer func(n int) { browser.RenavigateSettleMS = n }(browser.RenavigateSettleMS)
	browser.RenavigateSettleMS = 0
	var mu sync.Mutex
	var created, navigated []string
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/ws"})
	})
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]browser.Target{{ID: "t-home", Type: "page", URL: "https://www.icloud.com/"}})
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := nws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg struct {
				ID     float64        `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			_ = json.Unmarshal(data, &msg)
			result := map[string]any{}
			mu.Lock()
			switch msg.Method {
			case "Target.createTarget":
				created = append(created, msg.Params["url"].(string))
				result["targetId"] = "t-new"
			case "Target.attachToTarget":
				result["sessionId"] = "S1"
			case "Page.navigate":
				navigated = append(navigated, msg.Params["url"].(string))
			case "Page.getFrameTree":
				result["frameTree"] = map[string]any{"frame": map[string]any{"id": "F1", "url": "about:blank"}}
			case "Page.createIsolatedWorld":
				result["executionContextId"] = 1
			}
			mu.Unlock()
			raw, _ := json.Marshal(map[string]any{"id": msg.ID, "result": result})
			_ = conn.Write(context.Background(), nws.MessageText, raw)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	state, cfg := testState(t)
	cfg.CDP, state.CDP = srv.URL, srv.URL
	state.LatchBlocked("Reminders reported a lapsed grant")

	msg, code := Reask(cfg)
	if code != 0 || strings.Contains(msg, "no tab for") {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
	if state.Blocked() {
		t.Fatal("the latch should be cleared after the re-ask")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 1 || len(navigated) != 1 || navigated[0] != remindersURL {
		t.Fatalf("created=%v navigated=%v, want one tab navigated to %s", created, navigated, remindersURL)
	}
}
