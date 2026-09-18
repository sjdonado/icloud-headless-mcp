# iCloud Headless MCP Everywhere (`icloud-headless-mcp`)

One static binary over stdio, reproducible on any Linux host as an MCP server for Hermes or any MCP client. `cmd/icloud-mcp` holds the server (default, no subcommand) and every helper as subcommands; `icloud-mcp help` lists them. The runtime spawns `stdio.sh` as a child process on the same host. There is no network listener.

Start with `README.md` (overview, capabilities, critical caveats, contributing), `docs/ARCHITECTURE.md` (layout, session lifecycle, Drive mechanics, pitfalls), `docs/SETUP.md` (first-run sequence), and `SECURITY.md` (threat model, read before touching credentials or the session).

## Layout

See `docs/ARCHITECTURE.md` The layout; not repeated here.

No `AGENTS.md` nesting, no task runner (`Makefile`, `justfile`, `pyproject.toml`, `package.json` all absent). Building needs a Go toolchain (`go.mod`); the host needs only the static binary plus a system Chromium.

## Prerequisites

Target host is always-on 64-bit Linux, virtual X display (`xvfb`), headed system Chromium, and a private network (Tailscale or SSH tunnel) before the first login. Local macOS checkout can run the deterministic checks below but cannot run the browser stack, systemd units, or Apple-credential paths.

Never read `.env`, `cookies.json`, `*.vncpass`, or session state. `.env.example` is the readable contract. Never load the env file into a client unit; the calling uid must not read the app-specific password.

## Checks

Deterministic, run from the repo root:

```
go build ./...
go vet ./...
go test ./...
GOOS=linux GOARCH=amd64 go build -o /dev/null ./cmd/icloud-mcp
GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/icloud-mcp
```

All green at time of writing. There is no configured linter or typecheck; do not invent one.

Integration, defined but not runnable here (need the Linux host, service account, resident browser, Apple credentials). Prefer these over reconstructing steps; see `README.md` Verification and `docs/SETUP.md` section 8:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp session-check
```

Then one tool per surface: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders`, `drive_status`. `session-check` exit codes: 0 healthy, 1 signed out, 2 no browser, 3 needs device approval.

## Change guidance

- DAV/IMAP (`internal/dav`, `internal/mail`): needs credentials only, no browser lock. After a change, re-test concurrency: `list_mail` while a Notes call holds its lock.
- Browser-backed (`internal/notes`, `internal/reminders`): per-app locks, 60-90s calls are normal, cold app load longer. Keep the MCP timeout above the elicitation timeout (README uses 330s).
- Drive (`internal/drive`, `internal/dvlibraries`, `icloud-mcp drive-fetch`): status only in the server; scheduling is outside the repo. Empty or unparseable `DRIVE_LIBRARIES` pulls nothing by design.

## Constraints worth missing once

Full list in `docs/ARCHITECTURE.md` Pitfalls; the load-bearing subset:

- Chromium must run headed on a virtual display; headless reports a different user agent and kills the session server-side.
- Fresh app-page loads refuse 23:00-07:00 local (quiet hours); already-loaded tabs still work.
- CalDAV Reminders store is dead (writes succeed, invisible everywhere). Reminders stay browser-backed; never restore them on CalDAV.
- Calendar listings need `expand=True`; convert ISO offsets into the named `AGENT_TZ` zone before writing (fixed offsets serialize as wrong UTC hours).
- IMAP: check the select result (missing mailbox leaves AUTH state); flags come from separate `UID SEARCH`; server-side search is not substring search, `search_mail` unions server plus local decoded pass.
- Attachments: type denylist (incl. archives), size cap, sanitized basename, expiry; refusals go in `attachments_skipped`.
- Notes body is canvas-rendered: open, copy, title-check, retry once, then refuse rather than return the wrong note.
- Web apps are shadow DOM, virtualized, pointer-real: recursive `shadowRoot` walk, exact class-token match, full pointer event sequence, search box cleared via keyboard.
- Verify every note/reminder write against what the app shows; match on longest word with ellipsis stripped; report unconfirmed writes as unconfirmed.
- Reminder dates: untick time first for all-day, pick day by `aria-label` containment, Save is required to commit.
- Timezone override applies before app load plus one reload per tab lifetime; `create_reminder` returns `due_error` on hour mismatch.
- Every entry point touches the app stamp file so the tab reaper (15 min idle) does not close a tab mid-session.
- Judge signed-in state from page content (`SignedOut` before navigation); `needs_device_approval` is a refusal, not an empty list; dismiss modal alerts via list-open; screenshot when the DOM looks fine and clicks do nothing.
- Restarting `agent-browser`, `agent-xvfb`, `agent-vnc`, or `agent-novnc` costs the session warmth plus a device approval. It is a decision, never a side effect.
- VNC/noVNC bind loopback only, no `[Install]` section, started by hand and stopped after use. Sudoers rules name one exact command with no variable argument; do not widen to `systemctl` or parameterized commands.

## Unknowns

- No lint, format, or typecheck command is defined in the repo; nothing to run for those rungs.
- Chromium memory footprint is deployment-specific; measure RSS on the target box per `docs/SETUP.md`.
- `DRIVE_LIBRARIES` values are deployment facts; empty default pulls nothing.
