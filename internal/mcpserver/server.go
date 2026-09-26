// Package mcpserver builds the shared MCP server: the server identity, the
// 25-tool registry, and the elicitation approval gate every confirmed tool
// asks through.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ServerID is the MCP server name. It matches the bridge id used in
// runtime wiring examples.
const ServerID = "icloud-headless-mcp"

// Tier describes whether a tool writes and whether it asks first.
type Tier string

const (
	TierReadOnly  Tier = "read only"
	TierSilent    Tier = "silent"
	TierConfirmed Tier = "confirmed"
)

// ToolDef is one registered tool: its wire name, tier, and whether it asks.
// Asks mirrors the README Asks column: "yes", "no", or "only-with-attendees".
type ToolDef struct {
	Name        string
	Description string
	Tier        Tier
	Asks        string
}

// Tools is the full 32-tool registry in README table order. Names are the
// frozen contract (README Tools table, openspec/changes/go-rewrite/specs/),
// not invented here.
var Tools = []ToolDef{
	{"list_calendars", "List the calendars on the account.", TierReadOnly, "no"},
	{"list_events", "List calendar events in a window.", TierReadOnly, "no"},
	{"create_event", "Create a calendar event.", TierSilent, "no"},
	{"update_event", "Change an existing event in place.", TierSilent, "only-with-attendees"},
	{"delete_event", "Delete a calendar event by uid. Asks the owner first.", TierConfirmed, "yes"},
	{"search_contacts", "Find contacts by name, email, or phone.", TierReadOnly, "no"},
	{"list_mailboxes", "List the mailboxes on the account.", TierReadOnly, "no"},
	{"list_mail", "List messages in a mailbox. Never marks anything seen.", TierReadOnly, "no"},
	{"read_mail", "Read one message body by uid. Opens read-only.", TierReadOnly, "no"},
	{"search_mail", "Search mail server-side plus a local decoded pass.", TierReadOnly, "no"},
	{"send_mail", "Send mail from the owner's address, after approval.", TierConfirmed, "yes"},
	{"notes_folders", "List the folders in Apple Notes.", TierReadOnly, "no"},
	{"notes_list", "List notes, newest first.", TierReadOnly, "no"},
	{"notes_read", "Read a note's full text by title.", TierReadOnly, "no"},
	{"notes_search", "Search every note, bodies included.", TierReadOnly, "no"},
	{"update_note", "Replace one note's body. Asks the owner first.", TierConfirmed, "yes"},
	{"create_note", "Write a new note. Creates only.", TierSilent, "no"},
	{"reminder_lists", "List the reminder lists on the account.", TierReadOnly, "no"},
	{"list_reminders", "Read the open reminders in one list.", TierReadOnly, "no"},
	{"completed_reminders", "What the owner has ticked off, from Apple's records.", TierReadOnly, "no"},
	{"complete_reminder", "Tick a reminder off.", TierSilent, "no"},
	{"create_reminder", "Add a reminder, optionally with a due time.", TierSilent, "no"},
	{"drive_status", "When the Drive pull last ran and what it holds. Status only.", TierReadOnly, "no"},
	{"reask_access", "Re-fire Apple's data-access prompt. Asks the owner first.", TierConfirmed, "yes"},
	{"open_login", "Open the login door: a one-time link where the owner signs in to iCloud from any browser. Use when a call reports needs_login. Asks the owner first.", TierConfirmed, "yes"},
	{"sign_out", "Sign the server out of iCloud: Apple's own Sign Out, then this browser's cookies and site data. Notes and Reminders need open_login and a two-factor code afterwards; calendar, contacts and mail keep working. Asks the owner first.", TierConfirmed, "yes"},
	{"health_status", "Health store coverage and freshness, blind spots named.", TierReadOnly, "no"},
	{"health_days", "Per-day steps, energy, resting heart rate, HRV.", TierReadOnly, "no"},
	{"health_sleep", "Sleep by night, stage labels kept.", TierReadOnly, "no"},
	{"health_effort", "Time above a heart-rate floor.", TierReadOnly, "no"},
	{"health_recovery", "Recent window against a longer baseline.", TierReadOnly, "no"},
	{"health_sql", "One read-only SELECT against the health store.", TierReadOnly, "no"},
}

// approveSchema asks for nothing: accept is yes, decline is no. A form
// with no fields renders as one confirmation (a boolean field made clients
// render a form plus a review step for a single yes). A legacy client that
// still sends approve=false is refused below.
var approveSchema = map[string]any{
	"type":       "object",
	"properties": map[string]any{},
}

// AskApproval asks the owner through MCP elicitation. It returns "" when
// approved, or the reason to refuse. It fails closed three ways: a
// decline/cancel, an explicit no, and any failure to ask at all (no
// elicitation capability, no back-channel) all refuse with nothing
// executed.
func AskApproval(ctx context.Context, s *server.MCPServer, question string) string {
	res, err := s.RequestElicitation(ctx, mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message:         question,
			RequestedSchema: approveSchema,
		},
	})
	if err != nil {
		return fmt.Sprintf("nobody was present to approve it, so nothing was done: this session cannot "+
			"ask (%v). Ask again in a conversation where the owner can answer", err)
	}
	if res.Action != mcp.ElicitationResponseActionAccept {
		return fmt.Sprintf("not approved (%s)", res.Action)
	}
	if content, ok := res.Content.(map[string]any); ok {
		if approve, ok := content["approve"].(bool); ok && !approve {
			return "not approved (the answer was no)"
		}
	}
	return ""
}

// ResultJSON renders a result map as a JSON text content block. Field names
// are the frozen result keys (same contract as Tools): clients parse these
// names.
func ResultJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("encoding result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// stubHandler stands in for not-yet-ported tools. Confirmed tools still run
// the real approval gate first, so the ask/approve/refuse paths are live
// from Phase 0; phases replace each stub with its implementation.
func stubHandler(ctx context.Context, s *server.MCPServer, def ToolDef) (*mcp.CallToolResult, error) {
	if def.Asks != "no" {
		if refused := AskApproval(ctx, s, fmt.Sprintf("Run %s? (scaffold build: the tool is not ported yet)", def.Name)); refused != "" {
			return ResultJSON(map[string]any{"tool": def.Name, "refused": refused})
		}
	}
	return ResultJSON(map[string]any{"tool": def.Name, "status": "not-implemented"})
}

// New builds the server and registers every tool behind its stub handler.
func New() *server.MCPServer {
	return NewWithHandlers(nil)
}

// NewWithHandlers builds the server, using the given handler for each named
// tool and the stub for the rest. Phases replace stubs tool by tool.
func NewWithHandlers(handlers map[string]server.ToolHandlerFunc) *server.MCPServer {
	s := server.NewMCPServer(ServerID, "1.0.0", server.WithToolCapabilities(false))
	for _, def := range Tools {
		handler, ok := handlers[def.Name]
		if !ok {
			handler = func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return stubHandler(ctx, s, def)
			}
		}
		tool := mcp.NewTool(def.Name, append([]mcp.ToolOption{mcp.WithDescription(def.Description)}, toolOptions(def.Name)...)...)
		s.AddTool(tool, handler)
	}
	return s
}
