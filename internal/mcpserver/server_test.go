package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// goldenToolNames is the 32-tool contract, frozen: change only alongside
// the spec, never by invention here.
var goldenToolNames = []string{
	"list_calendars", "list_events", "create_event", "update_event", "delete_event",
	"search_contacts",
	"list_mailboxes", "list_mail", "read_mail", "search_mail", "send_mail",
	"notes_folders", "notes_list", "notes_read", "notes_search", "update_note", "create_note",
	"reminder_lists", "list_reminders", "completed_reminders", "complete_reminder", "create_reminder",
	"drive_status",
	"reask_access", "open_login", "sign_out",
	"health_status", "health_days", "health_sleep", "health_effort", "health_recovery", "health_sql",
}

func TestRegistryMatchesContract(t *testing.T) {
	if len(Tools) != len(goldenToolNames) {
		t.Fatalf("registry has %d tools, contract has %d", len(Tools), len(goldenToolNames))
	}
	seen := map[string]int{}
	for _, def := range Tools {
		seen[def.Name]++
	}
	for _, want := range goldenToolNames {
		if seen[want] != 1 {
			t.Errorf("tool %q registered %d times, want exactly once", want, seen[want])
		}
	}
	// Tier spot checks from the README Asks column.
	asks := map[string]string{}
	tiers := map[string]Tier{}
	for _, def := range Tools {
		asks[def.Name] = def.Asks
		tiers[def.Name] = def.Tier
	}
	for name, want := range map[string]string{
		"delete_event": "yes", "send_mail": "yes", "update_note": "yes",
		"reask_access": "yes", "open_login": "yes",
		"update_event": "only-with-attendees",
		"list_mail":    "no", "create_note": "no", "create_reminder": "no", "drive_status": "no",
	} {
		if asks[name] != want {
			t.Errorf("tool %q asks %q, want %q", name, asks[name], want)
		}
	}
	if tiers["delete_event"] != TierConfirmed || tiers["list_mail"] != TierReadOnly || tiers["create_event"] != TierSilent {
		t.Errorf("tier mismatch: delete=%q list_mail=%q create_event=%q",
			tiers["delete_event"], tiers["list_mail"], tiers["create_event"])
	}
}

// wireClient speaks raw MCP JSON-RPC to the real server over pipes.
type wireClient struct {
	t      *testing.T
	stdin  io.WriteCloser
	lines  chan string
	nextID int
}

func newWireClient(t *testing.T) *wireClient {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	s := New()
	stdioServer := server.NewStdioServer(s)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverDone := make(chan error, 1)
	go func() { serverDone <- stdioServer.Listen(ctx, stdinR, stdoutW) }()

	c := &wireClient{t: t, stdin: stdinW, lines: make(chan string, 64)}
	go func() {
		sc := bufio.NewScanner(stdoutR)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	// Handshake on the last handshake revision.
	c.nextID = 1
	c.send(map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "wire-test", "version": "0"},
		},
	})
	c.recv("initialize response")
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return c
}

func (c *wireClient) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	if _, err := fmt.Fprintln(c.stdin, string(b)); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
}

func (c *wireClient) recv(what string) map[string]any {
	c.t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case line := <-c.lines:
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				c.t.Fatalf("unmarshal %s: %v (%s)", what, err, line)
			}
			if _, ok := msg["method"]; ok {
				// Server-initiated request (elicitation); stash back for the caller.
				c.lines <- line
				continue
			}
			return msg
		case <-timeout:
			c.t.Fatalf("timeout waiting for %s", what)
			return nil
		}
	}
}

// awaitElicitation blocks for the server's elicitation/create and returns its id.
func (c *wireClient) awaitElicitation() string {
	c.t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case line := <-c.lines:
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				c.t.Fatalf("unmarshal server message: %v", err)
			}
			method, _ := msg["method"].(string)
			if method != "elicitation/create" {
				continue
			}
			raw, _ := json.Marshal(msg["id"])
			return string(raw)
		case <-timeout:
			c.t.Fatal("timeout waiting for elicitation/create")
			return ""
		}
	}
}

func (c *wireClient) replyElicitation(idJSON string, result any, errMsg string) {
	c.t.Helper()
	var id any
	if err := json.Unmarshal([]byte(idJSON), &id); err != nil {
		c.t.Fatalf("parse elicitation id: %v", err)
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if errMsg != "" {
		msg["error"] = map[string]any{"code": -32000, "message": errMsg}
	} else {
		msg["result"] = result
	}
	c.send(msg)
}

func (c *wireClient) callTool(name string) map[string]any {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": map[string]any{}},
	})
	msg := c.recv("tools/call response")
	gotID, _ := json.Marshal(msg["id"])
	wantID, _ := json.Marshal(id)
	if string(gotID) != string(wantID) {
		c.t.Fatalf("response id %s, want %s (%v)", gotID, wantID, msg)
	}
	result, _ := msg["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		c.t.Fatalf("empty content in tools/call result: %v", msg)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		c.t.Fatalf("result text is not JSON: %v (%s)", err, text)
	}
	return out
}

func TestToolsListMatchesContract(t *testing.T) {
	c := newWireClient(t)
	c.nextID++
	id := c.nextID
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/list"})
	msg := c.recv("tools/list response")
	result, _ := msg["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != len(goldenToolNames) {
		t.Fatalf("tools/list returned %d tools, want %d", len(tools), len(goldenToolNames))
	}
	seen := map[string]bool{}
	for _, item := range tools {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		seen[name] = true
	}
	for _, want := range goldenToolNames {
		if !seen[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}
}

// driveDeleteEvent calls delete_event while answering the elicitation per
// script, and returns the parsed result map.
func driveDeleteEvent(t *testing.T, answerResult any, answerErr string) map[string]any {
	t.Helper()
	c := newWireClient(t)
	done := make(chan map[string]any, 1)
	go func() { done <- c.callTool("delete_event") }()
	idJSON := c.awaitElicitation()
	c.replyElicitation(idJSON, answerResult, answerErr)
	select {
	case out := <-done:
		return out
	case <-time.After(20 * time.Second):
		t.Fatal("timeout waiting for tools/call result")
		return nil
	}
}

func TestElicitationAcceptEmptyObjectProceeds(t *testing.T) {
	out := driveDeleteEvent(t, map[string]any{"action": "accept", "content": map[string]any{}}, "")
	if out["status"] != "not-implemented" {
		t.Fatalf("accepted call did not proceed: %v", out)
	}
}

func TestElicitationAcceptExplicitApproveProceeds(t *testing.T) {
	out := driveDeleteEvent(t, map[string]any{"action": "accept", "content": map[string]any{"approve": true}}, "")
	if out["status"] != "not-implemented" {
		t.Fatalf("approved call did not proceed: %v", out)
	}
}

func TestElicitationExplicitNoRefuses(t *testing.T) {
	out := driveDeleteEvent(t, map[string]any{"action": "accept", "content": map[string]any{"approve": false}}, "")
	refused, _ := out["refused"].(string)
	if refused == "" || !strings.Contains(refused, "not approved") {
		t.Fatalf("explicit no did not refuse: %v", out)
	}
	if _, executed := out["status"]; executed {
		t.Fatalf("refused call executed: %v", out)
	}
}

func TestElicitationDeclineRefuses(t *testing.T) {
	out := driveDeleteEvent(t, map[string]any{"action": "decline"}, "")
	refused, _ := out["refused"].(string)
	if refused == "" || !strings.Contains(refused, "not approved") {
		t.Fatalf("decline did not refuse: %v", out)
	}
}

func TestElicitationErrorFailsClosed(t *testing.T) {
	out := driveDeleteEvent(t, nil, "elicitation not supported")
	refused, _ := out["refused"].(string)
	if refused == "" || !strings.Contains(refused, "nobody was present") {
		t.Fatalf("elicitation error did not fail closed: %v", out)
	}
	if _, executed := out["status"]; executed {
		t.Fatalf("failed-closed call executed: %v", out)
	}
}

// TestToolSchemasDeclareArguments guards the empty-schema regression: a
// client that sees no properties cannot call create_event at all.
func TestToolSchemasDeclareArguments(t *testing.T) {
	names := map[string]bool{}
	for _, def := range Tools {
		names[def.Name] = true
	}
	for name := range ToolParams {
		if !names[name] {
			t.Errorf("ToolParams names %q, which is not a registered tool", name)
		}
	}
	s := New()
	tool := s.GetTool("create_event")
	if tool == nil {
		t.Fatal("create_event is not registered")
	}
	required := map[string]bool{}
	for _, r := range tool.Tool.InputSchema.Required {
		required[r] = true
	}
	if !required["summary"] || !required["start"] {
		t.Fatalf("create_event schema requires %v, want summary and start", tool.Tool.InputSchema.Required)
	}
	if _, ok := tool.Tool.InputSchema.Properties["calendar"]; !ok {
		t.Fatal("create_event schema lacks the optional calendar property")
	}
}

// TestEveryHandlerArgumentIsDeclared scans the tool handlers for the
// arguments they read and requires each one in ToolParams. The schema is
// the only way a client learns an argument exists, so an undeclared one
// is a feature nobody can use (since/until on list_mail went unseen).
func TestEveryHandlerArgumentIsDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, params := range ToolParams {
		for _, p := range params {
			declared[p.Name] = true
		}
	}
	single := regexp.MustCompile(`args\.[A-Za-z]+\("([a-z_]+)"`)
	multi := regexp.MustCompile(`args\.OptAll\(([^)]*)\)`)
	name := regexp.MustCompile(`"([a-z_]+)"`)
	files, _ := filepath.Glob("../*/*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "mcpserver") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var used []string
		for _, m := range single.FindAllStringSubmatch(string(raw), -1) {
			used = append(used, m[1])
		}
		for _, m := range multi.FindAllStringSubmatch(string(raw), -1) {
			for _, n := range name.FindAllStringSubmatch(m[1], -1) {
				used = append(used, n[1])
			}
		}
		for _, u := range used {
			if !declared[u] {
				t.Errorf("%s reads argument %q, which no tool declares in ToolParams", f, u)
			}
		}
	}
}
