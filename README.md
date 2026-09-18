# iCloud Headless MCP

**Run your iCloud apps from anywhere, not only from a Mac.** On a Mac mini you can script the local machine and drive the native apps, and that is the setup most iCloud automation quietly assumes. Off a Mac, iCloud is very hard to work with: Notes and Reminders have no API at all, and the surfaces that do have one are scattered across protocols. This project is the answer to that.

It runs on a small VPS, or on a Raspberry Pi, or on any always-on Linux box you already have. That is the point: all of these iCloud capabilities, reachable from anywhere, with minimum requirements and no Apple hardware in the path.

It is an MCP server over stdio, so any MCP client that can spawn a process can drive it. The tool names, the tiers and the confirmations are decided here rather than in whatever client you point at it.

**It runs on the same machine as the agent runtime that drives it.** The runtime spawns `stdio.sh` as a child process and speaks to it over stdin and stdout. There is no network listener and nothing here is consumed remotely. This repository covers installing and running the server; how a particular agent runtime is told to spawn it belongs to that runtime's own setup, so the last step of installing this is always "configure your agent runtime to spawn `stdio.sh`", in whatever form that runtime takes.

Everything an agent can do with one iCloud account, in one directory and one process: calendar, contacts and mail over open protocols, Notes and Reminders through a resident browser holding a live web session, and the status of the iCloud Drive pull.

Headless, and the name says so. Calendar, contacts and mail need only the credentials. Notes and Reminders have no protocol at all, so they are driven through a Chromium that stays signed in, which is why a call there can take ninety seconds and why a lapsed web-access grant is a failure this server names rather than hides.

## How it's different

Protocol-only libraries such as the well-known `pyicloud` speak to Apple's private webservice endpoints, so they cover exactly what those endpoints expose. Notes exposes no usable endpoint at all, and Reminders moved to the CloudKit format those endpoints do not carry. This bridge instead drives the real iCloud web apps through a resident headed Chromium holding a live session, which is what unlocks the full suite: Notes, current Reminders and Drive exports, with every write verified against what the app shows rather than assumed from a response code.

## Requirements

An always-on Linux machine, and no screen needed. A small VPS is enough, and so is a Raspberry Pi: those are the setups this exists for. No toolchain lives on the host: the install is one static binary plus a system Chromium.

- 64-bit. A distribution Chromium is fine on any board, Pi included. The binary finds it via `CHROMIUM_BIN`, else `chromium`, `chromium-browser` or `google-chrome` on `PATH`, else the `PLAYWRIGHT_BROWSERS_PATH` browser root as a last resort (legacy; unset matches nothing). On a fresh apt machine `install.sh` installs the `chromium` package when none of those exist and `CHROMIUM_BIN` is unset; on a non-apt host install your distribution's Chromium first (or export `CHROMIUM_BIN`) or the installer refuses with that message.
- A virtual X display, because Chromium must run headed. Apple binds the session to the user agent and the headless build reports a different one. `install.sh` installs `xvfb`, `x11vnc`, `novnc` and `websockify` for the display and the one interactive login; the VNC door binds loopback only and is stopped after use.
- An Apple ID with an app-specific password, and a device you can approve prompts on during setup.
- A private network between you and that machine, such as Tailscale or any other WireGuard-style network, before the first login. The first-time sign-in is done through a remote desktop onto a browser you are typing Apple credentials into, so it must never cross a public interface. See [`SECURITY.md`](SECURITY.md).
- Nothing on the client side. It is an ordinary MCP server over stdio, it runs on the same host as the runtime that spawns it, and it needs no part of this repository to be present on whatever spawns it.

The real cost is Chromium's memory, and it is worth measuring rather than quoting: start the resident browser, open the app tabs you actually use, and read the resident set size on your own box. Calendar, contacts and mail need no browser at all, so an install that skips Notes and Reminders is far cheaper than one that does not.

## Quick start

As root on the host, install the latest build in one line (rebuilt on every merge to main):

```
curl -fsSL https://raw.githubusercontent.com/sjdonado/icloud-headless-mcp/main/install.sh | sh
```

That lays down the worked example this document describes (service account `agent-icloud`, `/opt/agent-icloud`, the browser stack) and enables it. Yes, the script deals with the permissions: as root it creates the account, lays down agent-owned files (mode 755 under a mode-700 dir) plus the root-owned re-ask helper in `/usr/local/bin`, writes exactly the two shipped sudoers rules, installs the units, seeds a blank env file only when none exists yet, installs the X stack on the apt path and Chromium only when no browser is found, and enables `agent-xvfb`, `agent-browser` and `agent-tab-reaper.timer`. A different account or prefix means the manual install below instead of this script.

The full first-run sequence, including the two-factor login and Apple's Advanced Data Protection grant, is in [`docs/SETUP.md`](docs/SETUP.md). Follow it once, end to end; two of its steps need you to be holding an Apple device.

## Tools

31 of them, on one server entry, so a Notes tool is `<prefix>_create_note` rather than living behind a second server. `list_calendars` first when the question is about the calendar, `drive_status` first when data pulled off Drive looks short. The last six cover the optional health extension and report unconfigured until it is set up.

| Surface   | Tools                                                       | Tier                                                                                                                               | Asks?               |
| --------- | ----------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| Calendar  | `list_calendars`, `list_events`                             | read only                                                                                                                          | No                  |
| Calendar  | `create_event`                                              | silent                                                                                                                             | No                  |
| Calendar  | `update_event`                                              | silent for an event the owner is alone on, confirmed when the event has other attendees, because moving it reaches their calendars | Only with attendees |
| Calendar  | `delete_event`                                              | confirmed, through MCP elicitation                                                                                                 | Yes                 |
| Contacts  | `search_contacts`                                           | read only, over CardDAV                                                                                                            | No                  |
| Mail      | `list_mailboxes`, `list_mail`, `read_mail`, `search_mail`   | read only, over IMAP, and it never marks a message as seen                                                                         | No                  |
| Mail      | `send_mail`                                                 | confirmed, through MCP elicitation                                                                                                 | Yes                 |
| Notes     | `notes_folders`, `notes_list`, `notes_read`, `notes_search` | read only, on the browser                                                                                                          | No                  |
| Notes     | `update_note`                                               | confirmed. Replaces one note's body by selecting it and pasting, which leaves the app's own undo holding the old version           | Yes                 |
| Notes     | `create_note`                                               | silent, on the browser. Creates only: no tool here edits or deletes a note                                                         | No                  |
| Reminders | `reminder_lists`, `list_reminders`, `completed_reminders`   | read only, the first two on the browser and the third out of Apple's own records                                                   | No                  |
| Reminders | `complete_reminder`, `create_reminder`                      | silent, on the browser, and each verifies its write against what the app shows                                                     | No                  |
| Drive     | `drive_status`                                              | read only. Status, never the pull                                                                                                  | No                  |
| Recovery  | `reask_access`                                              | confirmed, through MCP elicitation. Re-fires Apple's data-access prompt; refuses unless iCloud is actually waiting, and never at night | Yes                 |
| Recovery  | `open_login`                                                | confirmed, through MCP elicitation. Opens the supervised VNC login door: a link plus a one-time password, processes exit after 20 minutes | Yes                 |
| Health    | `health_status`, `health_days`, `health_sleep`              | read only, out of the local health store; report unconfigured until the extension is set up                                             | No                  |
| Health    | `health_effort`, `health_recovery`, `health_sql`            | read only, out of the local health store; `health_sql` takes one SELECT and refuses everything else                                   | No                  |

There is no mail delete or move tool, no note edit or delete, and no tool that can trigger the Drive pull.

**Asking is MCP elicitation, and it is the only gate here.** One approval gate in the server is the whole mechanism: the question goes to the client over the protocol, the client renders it however it asks its user, and the tool executes only on an explicit approval. Nothing leaves this process by any other channel, so the server needs no messaging credential to ask a question, and anything the owner has to know about a completed write is in the tool result for the client to relay. A session that cannot ask, which is what a scheduled run looks like from in here, gets a refusal and no write. Keep the elicitation schema free of required fields, so a plain accept with an empty object validates, and keep the tool timeout above the client's elicitation timeout.

**The tiering lives in this code, not in the client's configuration.** It is this server that decides a delete asks while creating an event solo does not. The client only renders the question.

**Recovery is two tools with two different jobs, and the errors route between them.** A lapsed grant carries `needs_device_approval` and is fixed by `reask_access`, which re-fires the prompt onto your devices. An expired session carries `needs_login` and is fixed by `open_login`, which opens the VNC door and hands back a link plus a one-time password: a Tailscale link when the host is on a tailnet, a loopback link otherwise. Use them the way the failure happens. In a conversation, call `reask_access` now and tell the owner to approve; the prompt arrives within seconds. In a background run, do not call it: report that access lapsed and timed out, and reload only when the owner replies asking to try again. Same for the door: open it when the owner is there to click through, never speculatively.

## How it works

One process serves all 31 tools. Notes and Reminders each hold their own per-app lock on the shared resident browser, while calendar, contacts and mail take no lock at all, so a Notes call never serialises a mail read behind it.

The resident Chromium holds the iCloud session in memory and loads only `icloud.com` at startup, so a restart by itself costs nothing: no prompt is raised until the next app-page load, which is what asks Apple for the data-access grant. That is why the resident opens no app tab on its own, and why restarting the browser is a decision rather than a side effect. A fresh app-page load is refused from 23:00 through 06:59 owner-local so no prompt ever wakes the owner at night; already-loaded tabs keep working.

The Drive pull stages files for another account to import, and `drive_status` only reports on it: nothing here triggers the pull.

Internals (layout, session lifecycle, Drive mechanics, pitfalls) are in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Things to keep in mind

- **Restarting the browser is a decision, never a side effect.** Restarting `agent-browser`, `agent-xvfb`, `agent-vnc` or `agent-novnc` costs the session warmth plus a device approval.
- **Chromium must run headed** on a virtual display; a headless relaunch reads as a different browser and the session dies server-side.
- **Fresh app-page loads refuse 23:00 through 06:59 owner-local.** Already-loaded tabs still work; that refusal is the quiet-hours rule working, not a failure.
- **`needs_device_approval` is a refusal, not an empty list.** At the first login, tick "Trust this browser" or the session dies on the next relaunch.
- **Never load the env file into the client's own service unit.** The calling uid must not be able to read the app-specific password.
- **`AGENT_TZ` is load-bearing.** A guessed zone moves a reminder by hours and nothing errors.
- **The CalDAV Reminders store is dead.** Reminders stay browser-backed; never restore them on CalDAV.

For the whole list see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#pitfalls); for the threat model see [`SECURITY.md`](SECURITY.md).

## Environment

One env file, owned by the account this runs as, mode 400, sourced by `stdio.sh`. `.env.example` carries the long form of every key.

| Key                      | What it is                                                                                                                                                                                                                                                                                    |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `ICLOUD_APPLE_ID`        | the Apple ID this server speaks for                                                                                                                                                                                                                                                           |
| `ICLOUD_APP_PASSWORD`    | an app-specific password from the Apple ID account page, revocable there without touching anything else. Not the primary Apple password                                                                                                                                                       |
| `AGENT_TZ`               | required, an IANA zone name. No default: a guessed zone types the wrong hour into an Apple picker and nothing errors                                                                                                                                                                          |
| `AGENT_TZ_FILE`          | optional, a file holding the zone, which wins over `AGENT_TZ` whenever it holds anything, because a zone changes when the owner travels and a restart is not the moment to find out. Defaults to `/etc/agent/timezone`. Whatever writes it needs no access to this account                    |
| `AGENT_DEFAULT_LIST`     | which reminder list a reminder goes to when the caller names none                                                                                                                                                                                                                             |
| `AGENT_DEFAULT_CALENDAR` | which calendar an event goes to when the caller names none. Set it: the fallback is whichever calendar sorts first, and on a real account that is often a shared one                                                                                                                          |
| `ICLOUD_STATE`           | everything this account writes for itself: the browser profile, the cookie jar, the per-app locks, the tab stamps. Defaults to `$HOME`                                                                                                                                                        |
| `ICLOUD_SHARED_STATE`    | state two uids share: the blocked latch, the ask log, and the queue of writes deferred while the grant had lapsed. A watchdog running as somebody else reads these, so it belongs outside this account's home. Defaults to `$HOME/shared-state`, which is right for a single-uid install only |
| `ICLOUD_CDP`             | where the resident Chromium listens for DevTools. Defaults to `http://127.0.0.1:9222`                                                                                                                                                                                                         |
| `MAIL_ATTACHMENTS_DIR`   | where `read_mail(save_attachments=True)` writes. Shared: this server writes it and whichever uid stages a saved file reads it, so it is group-readable and never group-writable. Defaults to `$HOME/mail-attachments`                                                                         |
| `DRIVE_STAGING`          | where the Drive pull leaves fetched files for another account to import. Defaults to `$ICLOUD_STATE/drive-staging`                                                                                                                                                                            |
| `DRIVE_ETAGS`            | the file recording what has already been fetched. Defaults to `$ICLOUD_STATE/state/drive-etags.json`                                                                                                                                                                                          |
| `DRIVE_LIBRARIES`        | which Drive app folders to pull, as a JSON array. `folder` copies an arbitrary export folder recursively, while `tree` and `snapshot` support monthly and one-file exporters. These folders are deployment facts, not this server's. Empty means nothing is pulled                            |

`AGENT_TZ` is load-bearing. A host may well run UTC while the owner does not, and Apple's web pickers store what they are typed as the page's zone, so the tools convert before typing; getting it wrong moves a reminder by hours and nothing errors.

Three things the wrapper supplies and the env file does not, because they are about the host rather than the account: `DISPLAY` for the headed Chromium the browser-backed modules attach to, `HOME`, which every path default is under, and the Chromium lookup (`CHROMIUM_BIN` first, then the `chromium` names on `PATH`; the `PLAYWRIGHT_BROWSERS_PATH` export it still carries is only the last-resort fallback for hosts that kept a Playwright browser root).

## Install from scratch (or audit the installer)

This is what `install.sh` does, step by step, written for an apt host: not a second installer, but the same steps expanded so they can be audited, or followed by hand when you deviate from the worked example. A different prefix or account means changing it here, in `stdio.sh`, in the systemd units and in the sudoers rules together; a non-apt host substitutes its own packages and refuses without a browser, exactly like the script's other branch.

```
ARCH=amd64  # or arm64 on a Pi
set -eu
curl -fsSL https://github.com/sjdonado/icloud-headless-mcp/releases/latest/download/sha256sums.txt -o sha256sums.txt
TARBALL=$(awk '{print $NF}' sha256sums.txt | grep -F "linux-$ARCH.tar.gz" | head -n 1)
[ -n "$TARBALL" ] || { echo "no linux-$ARCH asset in this release" >&2; exit 1; }
curl -fsSL -O "https://github.com/sjdonado/icloud-headless-mcp/releases/latest/download/$TARBALL"
grep -F "$TARBALL" sha256sums.txt
sha256sum -c --status --ignore-missing sha256sums.txt
tar xzf "$TARBALL"
SRC=${TARBALL%.tar.gz}

id agent-icloud >/dev/null 2>&1 || useradd -r -m -d /opt/agent-icloud -s /usr/sbin/nologin agent-icloud
install -d -o agent-icloud -g agent-icloud -m 700 /opt/agent-icloud/bin
install -o agent-icloud -g agent-icloud -m 755 -t /opt/agent-icloud/bin \
  "$SRC/bin/icloud-mcp" "$SRC/bin/stdio.sh" \
  "$SRC/bin/agent-reask-access" "$SRC/bin/icloud-login.sh"
install -o root -g root -m 700 "$SRC/bin/agent-reask-access" /usr/local/bin/agent-reask-access

apt-get update && apt-get install -y xvfb x11vnc novnc websockify  # the display and the one-time login door
command -v chromium || command -v chromium-browser || command -v google-chrome || [ -n "${CHROMIUM_BIN:-}" ] || apt-get install -y chromium
# Non-apt host: substitute your distribution's packages for the line above and set CHROMIUM_BIN if the browser lands outside PATH.

install -m 600 "$SRC/.env.example" .env  # fill in the Apple ID, app-specific password, and AGENT_TZ
install -d -m 755 /etc/agent
if [ ! -f /etc/agent/icloud.env ]; then install -o agent-icloud -g agent-icloud -m 400 .env /etc/agent/icloud.env; fi  # never overwrite a filled env file
rm -f .env  # the filled copy must not linger outside the service account's file
install -o root -g root -m 440 "$SRC/sudoers.d/agent-icloud" /etc/sudoers.d/agent-icloud
install -o root -g root -m 440 "$SRC/sudoers.d/agent-browser-restart" /etc/sudoers.d/agent-browser-restart
visudo -c
install -o root -g root -m 644 -t /etc/systemd/system "$SRC"/systemd/*
systemctl daemon-reload
systemctl enable --now agent-xvfb agent-browser agent-tab-reaper.timer
```

The directory `ICLOUD_SHARED_STATE` names has to exist and be writable by this account, and readable by whichever uid runs a watchdog over the latch, which is why it belongs outside this account's home on any install with more than one account.

The env file must never be loaded into the client's own service unit: an app-specific password can write the whole Apple account, and the calling uid must not be able to read it.

Then the interactive bootstrap, once, because a fresh Chromium is not signed in:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-login.sh
ssh -N -L 5901:localhost:5900 <host>
```

Sign in through the loopback VNC, and **tick "Trust this browser"**: without it Apple never issues the remembered-browser cookie and the session dies on the next relaunch. Approve the data-access prompt on a device when it appears.

## Connect a client

The client must run on the same Linux host as the calling user `agent`: the sudoers rule allows exactly `agent`, and only `agent`, to spawn the wrapper as `agent-icloud`, and there is no network listener to reach across machines. Run your agent on the host itself (over SSH, or permanently); an agent on a laptop cannot reach this server.

The command is the whole of it, with a timeout above the client's own elicitation timeout:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh
```

330 seconds is deliberate: a browser-backed call routinely takes 60 to 90 seconds and a cold app tab longer. Name the entry a valid identifier such as `icloud-headless-mcp`. Where each client takes its timeout differs, so it is set per client below; keep it above the client's own elicitation timeout either way.

### Hermes

In `~/.hermes/config.yaml`, under `mcp_servers`:

```yaml
mcp_servers:
  icloud-headless-mcp:
    command: "sudo"
    args: ["-n", "-u", "agent-icloud", "/opt/agent-icloud/bin/stdio.sh"]
    timeout: 330
```

`timeout` is seconds per tool call. Restart Hermes (or run `/reload-mcp`) after editing, then ask which MCP-backed tools are available to confirm the entry loaded. Nothing Hermes-specific is required beyond that.

### OpenCode

In `opencode.json` (or `opencode.jsonc`, global or project-level), under `mcp`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "icloud-headless-mcp": {
      "type": "local",
      "command": [
        "sudo",
        "-n",
        "-u",
        "agent-icloud",
        "/opt/agent-icloud/bin/stdio.sh"
      ],
      "enabled": true
    }
  }
}
```

Tools appear prefixed with the entry name (`icloud-headless-mcp_*`). This entry belongs in the OpenCode config on the Linux host, not on a laptop pointing at it: `type: local` spawns the command where OpenCode itself runs. OpenCode's `timeout` covers tool-list fetching only, so the block sets none: long browser calls simply run to completion.

Confirmed tools (`delete_event`, `send_mail`, `update_note`) ask through MCP elicitation and execute only on explicit approval; a session that cannot ask gets a refusal and no write.

## Dependencies

One static binary, `icloud-mcp`, holding the server and every helper as subcommands (`icloud-mcp help` lists them). Building it needs a Go toolchain; running it needs only a system Chromium beside it. Rollback is reverting the bad merge on main: the next build republishes latest without it.

## Health extension (optional, off unless configured)

Point `HEALTH_EXPORT_DIR` at a staged Apple Health export folder (monthly JSONL per metric plus tombstone files, as the HealthMirror iOS app writes into Drive), and a daily timer imports it into SQLite at `HEALTH_DB` via `icloud-mcp health-import`. The six `health_*` tools then answer coverage, day rollups, sleep stages, effort, recovery, and one guarded read-only SELECT each. Empty means off: the importer no-ops and the tools report unconfigured. The backfill is a file, not a feature: save the official Apple export from agent chat into staging and the next import picks it up. Days are the export's own `localDate`, never a converted zone date; a metric that errored reports null with its reason, never zero. See `docs/SETUP.md` for the runbook.

## Verification

Run it as the account it runs as, and without restarting the browser:

```
sudo -u <caller> -H sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp session-check
```

The first proves the sudo grant, the env file and the binary, from the uid that uses it: it should start and exit silently on EOF. The second reports whether the resident browser can actually reach iCloud data, and distinguishes the three answers that need different fixes: healthy, signed out, needs a device approval, or no browser to talk to.

Then, with the account's environment, call the tools directly and expect all five surfaces to answer: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders`, `drive_status`. From 23:00 through 06:59 owner-local, a call needing a fresh app-page load refuses rather than raising a prompt on the owner's devices; that is the quiet-hours rule working, not a failure.

## Deployments

The service account, paths, systemd units, sudoers rules, and runtime wiring in this repository are install-time choices, documented as one worked example. Any host reproducing them gets the same bridge: the server only needs its env file, the resident browser stack, and a runtime (Hermes or any MCP client) spawning `stdio.sh` over stdio on the same machine. Keep runtime-specific wiring in the runtime's own configuration, not in this server.

## Security

[`SECURITY.md`](SECURITY.md) is the threat model. Read it before pointing this at an Apple ID you cannot afford to lose: a running install holds a logged-in Apple session on a machine, the VNC door must stay on loopback, and the credentials belong to one account and one file. It also says how to report a vulnerability privately.

## Contributing

Read [`SECURITY.md`](SECURITY.md) before touching credentials or the session. From the repo root, run `go build ./...`, `go vet ./...` and `go test ./...`; all must pass. There is no configured linter or typecheck. Keep the MCP timeout above the elicitation timeout, re-test `list_mail` while a Notes call holds its lock after concurrency changes, and never widen the sudoers rules beyond one exact command with no variable argument.
