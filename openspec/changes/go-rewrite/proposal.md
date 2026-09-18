## Why

The bridge works, but every install carries a Python virtualenv, pip, and the Playwright/Node toolchain just to run I/O-bound Apple integrations and drive a browser over CDP. A single static Go binary removes the venv, pip, and Node driver from target hosts, cross-compiles trivially to ARM boards, and replaces `imaplib` (the source of several documented quirks) with a better-designed IMAP stack. Prior analysis is on record: this buys deployment simplicity and maintainability, not speed or memory (calls are Apple- and Chromium-bound; Chromium dominates RSS either way).

## What Changes

- Port the MCP server (`server.py`, `app.py`) to Go: same server id `icloud-headless-mcp`, same 23 tool names, same read-only / silent / confirmed tiers, same MCP-elicitation approval gate with the same fail-closed behavior when the client cannot ask.
- Port all five tool surfaces with strict behavior parity: `dav` (CalDAV/CardDAV), `mail` (IMAP/SMTP), `notes`, `reminders` (resident browser over CDP), `drive` (status only).
- Replace Playwright with a CDP-native Go driver (chromedp or go-rod, decided in design) attaching to the same resident headed Chromium; keep the headed-on-Xvfb requirement, per-app locks, quiet hours, blocked latch, tab-reaper stamps, and timezone-override semantics.
- Port the `bin/` helpers (resident launcher, login/bootstrap, `session_check` with the same 0/1/2/3 exit codes, re-ask, drain, tab reaper, Drive fetch) so no Python remains on the host path.
- Keep the deployment contract byte-for-byte: env file keys and mode 400, `stdio.sh` wrapper shape (env sourcing, `DISPLAY`, `PLAYWRIGHT_BROWSERS_PATH`-equivalent, `HOME`) with only the executed binary swapped, systemd units, and the two single-binary sudoers rules.
- Replace `requirements.txt`/venv install docs with `go build` (plus system Chromium); add `go vet` and `go test` to the deterministic checks. No new tools, no behavior changes, no new transports.

## Capabilities

### New Capabilities

- `mcp-transport`: stdio transport, the 23-tool registry with names and tiers, elicitation approval gate (ask set, fail-closed refusals, schema with no required fields), per-app locking, timeout posture.
- `calendar-contacts`: CalDAV listing (`expand`), named-zone conversion, read-back writes, SEQUENCE bump, attendee-conditional approval, confirmed delete; CardDAV substring search.
- `mail`: IMAP reads that never mark seen, mailbox-select checking, UID SEARCH flags, server-plus-local union search with `matched`/`scanned_recent`, attachment limits with `attachments_skipped`, confirmed send.
- `notes-reminders-browser`: per-app locks, quiet-hours refusal, `needs_device_approval` as refusal (never empty list), canvas copy with title check and one retry, write verification with longest-word matching, reminder date ordering with Save-to-commit, `due_error` on hour mismatch.
- `drive-status`: status-only reporting, scheduling outside the repo, empty/unparseable `DRIVE_LIBRARIES` pulls nothing.
- `session-lifecycle`: resident browser session model, CDP attach, blocked latch and re-ask, tab-reaper stamp discipline, `session_check` exit-code contract, restart-is-a-decision rule.

### Modified Capabilities

- None. No existing specs under `openspec/specs/`; behavior must not change, so every capability above pins the parity contract the port must satisfy.

## Impact

- New Go module (`go.mod`, `cmd/`, `internal/`); Python sources become the frozen reference until cutover, then are removed from the install path.
- `README.md` install/dependencies/verification sections, `docs/SETUP.md`, `AGENTS.md` checks, and `SECURITY.md` (toolchain paragraphs) updated at cutover.
- `stdio.sh` executes the Go binary; systemd units and sudoers rules keep their shape with adjusted `ExecStart`/paths.
- Dependencies move from `requirements.txt` to `go.mod`: `mcp-go`, CDP driver, `go-imap`/`go-message`, `go-webdav`/`go-ical`.
