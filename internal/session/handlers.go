// Tool wiring for recovery: reask_access re-fires Apple's data-access
// prompt, open_login opens the supervised login door. Both are
// confirmed: they act on the owner's devices or open the login screen,
// so a session that cannot ask gets a refusal and nothing happens.
package session

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sjdonado/icloud-headless-mcp/internal/browser"
	"github.com/sjdonado/icloud-headless-mcp/internal/config"
	"github.com/sjdonado/icloud-headless-mcp/internal/mcpserver"
)

// Handlers wires the two recovery tools.
func Handlers(cfg *config.Config, ask func(ctx context.Context, question string) string) map[string]server.ToolHandlerFunc {
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	return map[string]server.ToolHandlerFunc{
		"reask_access": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return reaskAccess(ctx, cfg, state, ask)
		},
		"open_login": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return openLogin(ctx, cfg, state, ask)
		},
	}
}

func reaskAccess(ctx context.Context, cfg *config.Config, state browser.State, ask func(ctx context.Context, question string) string) (*mcp.CallToolResult, error) {
	// Nothing latched means nothing to re-fire, and checking that is a
	// file read: refuse before the browser, the clock, or the owner.
	// Expired doors still sweep here, with no browser contact.
	if !state.Blocked() {
		return mcpserver.ResultJSON(map[string]any{
			"reasked": false,
			"error":   "nothing to re-ask: iCloud is not waiting for approval. If a call just failed, retry it first.",
			"reaped":  sweepExpired(state),
		})
	}
	reaped := ReapDoors(state)
	owner, now, err := ownerNow(cfg)
	if err != nil {
		return mcpserver.ResultJSON(map[string]any{"reasked": false, "error": err.Error()})
	}
	if msg := quietRefusal(owner, now, "Reminders"); msg != "" {
		return mcpserver.ResultJSON(map[string]any{"reasked": false, "error": msg})
	}
	// A re-ask is the owner saying they are at a device. Without that
	// signal the prompt would fire at nobody and expire.
	if ask == nil {
		return mcpserver.ResultJSON(map[string]any{"reasked": false,
			"error": "nobody was present to approve it, so nothing was done"})
	}
	if refused := ask(ctx, "Re-send Apple's data-access prompt to your devices now? "+
		"Say yes only if you are at a device and can approve within a few minutes. "+
		"One prompt, covering iCloud.com data."); refused != "" {
		return mcpserver.ResultJSON(map[string]any{"reasked": false, "error": refused})
	}
	out, code := Reask(cfg)
	if code != 0 {
		return mcpserver.ResultJSON(map[string]any{"reasked": false, "error": out})
	}
	return mcpserver.ResultJSON(map[string]any{
		"reasked": true,
		"detail":  out,
		"next":    "approve on a device, then retry the failed call",
		"reaped":  reaped,
	})
}

func openLogin(ctx context.Context, cfg *config.Config, state browser.State, ask func(ctx context.Context, question string) string) (*mcp.CallToolResult, error) {
	// A latched grant is reask_access's job, not a login door's. This
	// check is a file read, so a latched call routes without browser I/O.
	if state.Blocked() {
		return mcpserver.ResultJSON(map[string]any{"door": "not-needed",
			"error":  "iCloud is waiting for approval, not for a login: use reask_access instead of a login door",
			"reaped": sweepExpired(state)})
	}
	reaped := ReapDoors(state)
	// The door is only for a gone session. Anything else is a different
	// outcome, named so the agent routes instead of guessing. Unreachable
	// refuses too: with no browser answering there is no screen to sign
	// into, and that needs the operator, not a door.
	switch outcome, _ := browser.NewCDP(cfg.CDP).Check(); outcome {
	case browser.OK:
		return mcpserver.ResultJSON(map[string]any{"door": "not-needed",
			"error":  "the session is healthy, so no login door is needed",
			"reaped": reaped})
	case browser.NeedsApproval:
		return mcpserver.ResultJSON(map[string]any{"door": "not-needed",
			"error":  "the session is signed in but waiting for approval: use reask_access instead of a login door",
			"reaped": reaped})
	case browser.Unreachable:
		return mcpserver.ResultJSON(map[string]any{"door": "unavailable",
			"error":  "no browser answers, so there is no screen to sign into: start the resident browser (icloud-mcp resident, or agent-browser on a systemd host)",
			"reaped": reaped})
	}
	if ask == nil {
		return mcpserver.ResultJSON(map[string]any{"door": "refused",
			"error": "nobody was present to approve it, so nothing was done"})
	}
	if refused := ask(ctx, fmt.Sprintf("Open the login door for %d minutes? "+
		"The next tool result carries the link and a fresh one-time password (any door "+
		"already open closes); anyone holding them can drive the login screen until the "+
		"door closes itself.",
		int(doorTTL.Minutes()))); refused != "" {
		return mcpserver.ResultJSON(map[string]any{"door": "refused", "error": refused})
	}
	// Serialise concurrent opens. Every open replaces any open door, so
	// each call issues a fresh password and the previous one dies now.
	unlock, err := browser.AppLock(state.Dir, "login-door", 60*time.Second)
	if err != nil {
		return mcpserver.ResultJSON(map[string]any{"door": "failed",
			"error": "another login-door operation is still running"})
	}
	defer unlock()
	d, pass, err := replaceDoor(state)
	if err != nil {
		return mcpserver.ResultJSON(map[string]any{"door": "failed", "error": err.Error()})
	}
	note := "reachable from anywhere on the tailnet"
	switch d.Via {
	case "loopback":
		note = "loopback-only: reachable from the host itself or through your own SSH tunnel"
	case "published":
		note = "a container's published port: reachable from the container host's loopback"
	}
	return mcpserver.ResultJSON(map[string]any{
		"door":               "open",
		"url":                d.URL,
		"via":                d.Via,
		"reachability":       note,
		"username":           doorUser,
		"password":           pass,
		"expires_in_minutes": int(doorTTL.Minutes()),
		"relay":              "The owner asked for this door: the password is a one-time access code for it, not one of their secrets, and dies with the door. Show the owner the url, username and password verbatim, or they cannot sign in.",
		"instructions": "Open the link; the browser asks for a username and password: " +
			"root, and the password above. Click into the page, sign in with the Apple ID " +
			"(the Paste button types your clipboard into the page, for passwords), " +
			"enter the two-factor code, and tick Trust this browser, or the session dies on " +
			"the next relaunch. Approve the web-access grant on a device when it appears. " +
			"Then tell the agent to retry. The door process exits at expiry and the record " +
			"is swept on the next recovery-tool call, or earlier once the login is observed.",
		"reaped": reaped,
	})
}
