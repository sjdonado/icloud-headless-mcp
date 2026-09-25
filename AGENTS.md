# iCloud Headless MCP Everywhere (`icloud-headless-mcp`)

One static binary over stdio, an MCP server for any MCP client on macOS or Linux. `cmd/icloud-mcp` holds the server (default, no subcommand) and every helper as subcommands; `icloud-mcp help` lists them. The agent spawns it as a child process on the same host: directly on a single-user machine, or `stdio.sh` through sudo on the service-account server install. The only listener is the login door (`icloud-mcp door`), opened on demand for at most 20 minutes on a Tailscale or loopback address.

Start with `README.md` (overview, capabilities, critical caveats, contributing), `docs/ARCHITECTURE.md` (layout, session lifecycle, Drive mechanics, pitfalls), `docs/SETUP.md` (first-run sequence), and `SECURITY.md` (threat model, read before touching credentials or the session).

## Layout

See `docs/ARCHITECTURE.md` The layout; not repeated here.

No `AGENTS.md` nesting, no task runner (`Makefile`, `justfile`, `pyproject.toml`, `package.json` all absent). Building needs a Go toolchain (`go.mod`); the host needs only the static binary plus a system Chromium.

## Prerequisites

Runtime needs a system Chromium or Google Chrome (run headless, no display) and, for a remote login, a private network (Tailscale or SSH tunnel). A macOS or Linux checkout runs the deterministic checks below and the live happy path through `./local.sh`; it cannot exercise the systemd units or the sudoers split.

Never read `.env`, `.state/`, `cookies.json`, a door password file, or session state. `.env.example` is the readable contract. Never load the env file into a client unit; the calling uid must not read the app-specific password.

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

Integration, defined but not runnable here (need the Linux host, service account, resident browser, Apple credentials). Prefer these over reconstructing steps; see `docs/SETUP.md` section 8:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp-host session-check
```

Then one tool per surface: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders`, `drive_status`. `session-check` exit codes: 0 healthy, 1 signed out, 2 no browser, 3 needs device approval.

## Happy path

Run it from the checkout with `./local.sh`, on this machine, against a local Chrome. No container, no service account: `.env` in the repo root holds the account (copy `.env.example`: `ICLOUD_APPLE_ID` and `ICLOUD_APP_PASSWORD`, plus `AGENT_TZ` only on a UTC host; never read it, the wrapper sources it into the child process only), and every piece of state (profile, cookie jar, locks, door record) lands in `.state/`. Both are gitignored. Proven 2026-09-25 on macOS with Chrome 145 through the machine's own pi agent plus `pi-mcp-adapter`: door login, five surfaces, and a resident relaunch keeping the session. Reproduce it after any transport or registry change.

The server starts the headless resident itself when nothing answers on the loopback CDP port (`ICLOUD_RESIDENT=external` turns that off; `stdio.sh` sets it because systemd owns the browser there). Login goes through the login door, never a local window: signed out, a Notes or Reminders call returns `needs_login`, the agent calls `open_login`, the owner approves the elicitation, and the result carries a link, username `root` and a fresh one-time password. From a shell, `./local.sh login` prints the same. The owner signs in, completes 2FA, ticks "Trust this browser"; the door shows "Signed in" and exits. Then:

```
./local.sh session-check  # must print OK
```

Gate: `session-check` prints `OK`, and the hour in `AGENT_TZ` sits outside 23:00 through 06:59. Inside that window a call needing a fresh app-page load refuses by design (quiet hours); already-loaded tabs plus `list_calendars`, `list_mail`, and `drive_status` still answer. A refusal is not a failure, but it is not a green happy path either.

Then connect any MCP stdio client with `./local.sh` as the command (absolute path, 330s tool timeout) and call `initialize`, `tools/list`, then `tools/call` for `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders`, `drive_status` with `{}`. Expect 31 tools. Parse each `result.content[0].text` as JSON and require no `error`, no `needs_device_approval`, and no `needs_login` key on the five data calls. Keys seen: `list_calendars` carries `calendars`; `list_mail` carries `mailbox`, `count`, `matched`; `reminder_lists` carries `count`, `lists`; `notes_folders` carries `count`, `folders`; `drive_status` carries `last_pull`, `stale`, `schedule`, `files_tracked`, `libraries` (`stale: null` means never pulled, `{}` libraries is healthy-empty). On a healthy session with a clear latch and no elicitation, `reask_access` answers `reasked: false` naming nothing-to-re-ask and `open_login` answers `door: not-needed`. Browser-backed calls take 60-90s each; the six `health_*` tools answer unconfigured without an export.

### Linux rig

`./verify.sh` builds `Dockerfile.verify` from the checkout (the binary, Debian Chromium, pi plus `pi-mcp-adapter`), starts `icloud-mcp-verify` with the door published on the host's loopback only (`-p 127.0.0.1:6080:6080`, `ICLOUD_DOOR_BIND=0.0.0.0` inside), copies `.env` and the host's pi `auth.json` and `settings.json` in with `docker cp -L` (never baked into a layer; `.dockerignore` keeps `.env` and `.state/` out of the build context), and opens pi inside. `./verify.sh pi` reopens pi in the running container, keeping its signed-in profile; a rebuild starts signed out. Two container facts it handles: the clock runs UTC, so it passes the host's zone as `TZ`; and Docker's default seccomp profile blocks Chromium's sandbox namespaces, so the rig runs with `--security-opt seccomp=unconfined` (never `--no-sandbox`). Check the session from outside with `docker exec icloud-mcp-verify icloud-mcp session-check`.

### Write checks

Run on the owner's real account, so every write touches only items the round creates, titled exactly `[icloud-mcp verify] <YYYYMMDD-HHMM>`, and the agent stops if a lookup would change anything without that title. Tell the agent the absolute dates (it misread "tomorrow" once) and to call tools directly rather than through the adapter's script wrapper, which gives up after 30s while a browser call takes 60 to 120s. Approve only elicitations that name the marker item, and check a `send_mail` recipient against `ICLOUD_APPLE_ID` without printing `.env`.

In order: `create_event` in Home tomorrow 10:00-10:15, no attendees, confirmed by `list_events`; `update_event` to 10:30-10:45 (solo, so silent); `delete_event` (asks) and confirm it is gone; `create_reminder` in Reminders due tomorrow 09:00 and confirm `list_reminders` shows it; `complete_reminder` and confirm in `completed_reminders`; `create_note` and confirm with `notes_search`; `update_note` (asks) and confirm with `notes_read`; `send_mail` to the Apple ID itself (asks) and confirm with `search_mail`.

State on 2026-09-25 in the Linux rig: every write passes end to end through pi against a real account, after the fixes in "Debugging the web apps" below: the calendar writes, `send_mail`, `create_reminder` with a due time, `complete_reminder`, `create_note` and `update_note`. One `update_note` right after its `create_note` refused on the identity check and succeeded on retry; that is the check working, not a failure to chase. Report any `left_behind` field; the owner deletes leftovers (notes and mail have no delete tool).

## Debugging the web apps

When a Notes or Reminders tool misbehaves, experiment in the page before writing Go. `tools/cdp-eval.mjs` is a console for the resident browser: it evaluates an expression in the app's frame, and it can click, type and press keys with the same trusted CDP events the tools send. One command per step, no rebuild, no redeploy. Against the Linux rig: `./verify.sh eval HINT 'EXPR'`. Against a local resident: `node tools/cdp-eval.mjs HINT 'EXPR'`. HINT picks the tab by URL (`reminders`, `notes`). EXPR can use `$$all(sel, root)` (querySelectorAll through shadow roots), `box(el)` (center and size) and `desc(el)` (tag, class, text, aria-label, role, box).

The loop that found the create and open bugs:

1. **Map the DOM.** List the elements a step touches: `./verify.sh eval reminders "$$all('ui-button, [role=button], button').map(desc).filter(d => d.w > 0)"`. Read the classes, roles, `aria-label`s and positions. Prefer `aria-label` and `role` over `innerText`, which also carries placeholders ("Notes" under every reminder title).
2. **Do the action by hand, one step at a time.** `--click X Y`, `--type TEXT`, `--key Tab`, and after each step inspect the state: what holds focus (walk `document.activeElement` through shadow roots), which row changed, what the row's attributes now say.
3. **Only then write the Go**, as the smallest sequence that worked in step 2, with the element facts in a comment. Rebuild the test binary only to confirm the whole flow.
4. **Leave the page clean.** A probe that creates something on the owner's account gives it the marker title right away; a probe that opens a popover closes it.

Facts found this way (2026-09-25, Chromium 153 headless):

- Clicks must look like a mouse: hover, press with `buttons: 1`, release with `buttons: 0`, with a short pause between. Without the button state the apps treated a click as a bare selection, which read as "the first click only selects".
- Only the front tab is visible in a headless window, and Chromium throttles the others. `App.Open` brings the driven tab to the front, and the resident runs with background throttling off.
- Reminders: each row is `div.reminder-item[role=row]` whose `aria-label` is the committed title (empty while a new row is typed). "+" is `ui-button.rm-new-reminder`; it adds a row whose `div.tt-input-field[role=textbox]` is already focused. `Input.insertText` types into it and Tab commits it, after which focus sits on the row's `button.info`. Escape discards an uncommitted row. Time segments are `role=spinbutton` and take key presses, not inserted text; focus them with `.focus()`, because clicking their measured center landed on AM/PM.
- Notes: the list is virtualised. Each note has a rendered `.note-list-item-container` and a parked copy at y=-9861, and DOM order is not screen order, so a note is opened by clicking the on-screen container whose first line is its exact title, never by index.
- Clipboard writes need `clipboardSanitizedWrite` granted as well as `clipboardReadWrite`, and a DevTools permission grant lasts only as long as the connection that made it.

## Change guidance

- DAV/IMAP (`internal/dav`, `internal/mail`): needs credentials only, no browser lock. After a change, re-test concurrency: `list_mail` while a Notes call holds its lock.
- Browser-backed (`internal/notes`, `internal/reminders`): per-app locks, 60-90s calls are normal, cold app load longer. Keep the MCP timeout above the elicitation timeout (README uses 330s).
- Drive (`internal/drive`, `internal/dvlibraries`, `icloud-mcp drive-fetch`): status only in the server; scheduling is outside the repo. Empty or unparseable `DRIVE_LIBRARIES` pulls nothing by design.

## Constraints worth missing once

Full list in `docs/ARCHITECTURE.md` Pitfalls; the load-bearing subset:

- Chromium runs `--headless=new` with Chrome's reduced user agent for its own major version (`chromeUA`); Apple binds the session to the user agent, so never relaunch the profile under a different string. `ICLOUD_HEADED=1` is the escape hatch.
- Fresh app-page loads refuse 23:00 through 06:59 owner-local (quiet hours); already-loaded tabs still work.
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
- Restarting the resident (`agent-browser`, or an auto-started one) costs the warm tabs and possibly a device approval. It is a decision, never a side effect.
- Login door: owner-approved, Basic auth `root` plus a 16-char `crypto/rand` password (0600 file, never argv), binds Tailscale IPv4 or loopback (never a wildcard), prefers port 6080, forwards only `Input.*` and `Page.reload` (never script), 20-minute TTL, exits on sign-in, every `open_login` replaces the open door. Do not widen any of that.
- Sudoers rules name one exact command with no variable argument; do not widen to `systemctl` or parameterized commands.

## Unknowns

- No lint, format, or typecheck command is defined in the repo; nothing to run for those rungs.
- Chromium memory footprint is deployment-specific; measure RSS on the target box per `docs/SETUP.md`.
- `DRIVE_LIBRARIES` values are deployment facts; empty default pulls nothing.
