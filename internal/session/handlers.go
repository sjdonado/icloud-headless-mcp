// Tool wiring for recovery: reask_access re-fires Apple's data-access
// prompt, open_login opens the supervised login door, sign_out ends the
// session. All three are confirmed: they act on the owner's devices, open
// the login screen or end the session, so a session that cannot ask gets
// a refusal and nothing happens.
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

// Handlers wires the recovery tools.
func Handlers(cfg *config.Config, ask func(ctx context.Context, question string) string) map[string]server.ToolHandlerFunc {
	state := browser.State{Dir: cfg.StateDir, Shared: cfg.SharedState, CDP: cfg.CDP}
	return map[string]server.ToolHandlerFunc{
		"reask_access": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return reaskAccess(ctx, cfg, state, ask)
		},
		"open_login": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return openLogin(ctx, cfg, state, ask)
		},
		"sign_out": func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return signOut(ctx, state, ask)
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

// signOut ends the iCloud session in the resident browser, after the
// owner approves: Apple's own Sign Out, then the profile's cookies and
// site data, so nothing is left to resume. Signing back in is open_login.
func signOut(ctx context.Context, state browser.State, ask func(ctx context.Context, question string) string) (*mcp.CallToolResult, error) {
	reaped := ReapDoors(state)
	outcome, _ := browser.NewCDP(state.CDP).Check()
	if outcome == browser.Unreachable {
		return mcpserver.ResultJSON(map[string]any{"signed_out": false,
			"error": "no browser answers, so there is no session to sign out of"})
	}
	signedIn := outcome != browser.SignedOut
	if signedIn {
		if ask == nil {
			return mcpserver.ResultJSON(map[string]any{"signed_out": false,
				"error": "nobody was present to approve it, so nothing was done"})
		}
		if refused := ask(ctx, "Sign this server out of iCloud? Notes and Reminders stop working "+
			"until you sign in again through the login door, which needs your Apple ID password "+
			"and a two-factor code. Calendar, contacts and mail keep working: they use the "+
			"app-specific password, which you revoke at account.apple.com."); refused != "" {
			return mcpserver.ResultJSON(map[string]any{"signed_out": false, "error": refused})
		}
	}
	// Every browser user waits: a Notes or Reminders write must not lose
	// its cookies halfway, no door may open onto a half-wiped profile, and
	// the resident must not save a jar read before the wipe. One deadline
	// for all four, so the call stays inside the client's timeout.
	deadline := time.Now().Add(150 * time.Second)
	for _, lock := range []string{"notes", "Reminders", "login-door", browser.JarLock} {
		unlock, err := browser.AppLock(state.Dir, lock, time.Until(deadline))
		if err != nil {
			return mcpserver.ResultJSON(map[string]any{"signed_out": false,
				"error": "a " + lock + " operation is still running; nothing was signed out, try again when it finishes"})
		}
		defer unlock()
	}
	// A door opened between the question and the locks would stay open
	// onto the wiped profile: close it now.
	if d := liveDoor(state); d != nil {
		closeDoor(d)
		removeRecordFor(state, d.Pid)
	}
	// Already signed out still gets the local wipe (no Apple menu, nothing
	// to approve): leftover cookies, site data and a stale jar go too.
	res, err := browser.SignOut(state, signedIn)
	out := map[string]any{"signed_out": err == nil, "already": !signedIn, "steps": res, "reaped": reaped,
		"next": "open_login signs back in"}
	if err != nil {
		out["error"] = err.Error()
	}
	if signedIn && !res.AppleSignOut {
		out["note"] = "Apple's own Sign Out did not complete, so the session was ended by wiping this browser's cookies and site data. Remove the browser from your account's devices at account.apple.com if you want Apple to forget it."
	}
	return mcpserver.ResultJSON(out)
}
