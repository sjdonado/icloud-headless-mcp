# icloud-headless

**Run your iCloud apps from anywhere, not only from a Mac.** On a Mac mini you can script the local machine and drive the native apps, and that is the setup most iCloud automation quietly assumes. Off a Mac, iCloud is very hard to work with: Notes and Reminders have no API at all, and the surfaces that do have one are scattered across protocols. This project is the answer to that.

It runs on a small VPS, or on a Raspberry Pi, or on any always-on Linux box you already have. That is the point: all of these iCloud capabilities, reachable from anywhere, with minimum requirements and no Apple hardware in the path.

It is an MCP server over stdio, so any MCP client that can spawn a process can drive it. The tool names, the tiers and the confirmations are decided here rather than in whatever client you point at it.

**It runs on the same machine as the agent runtime that drives it.** The runtime spawns `stdio.sh` as a child process and speaks to it over stdin and stdout. There is no network listener and nothing here is consumed remotely. This repository covers installing and running the server; how a particular agent runtime is told to spawn it belongs to that runtime's own setup, so the last step of installing this is always "configure your agent runtime to spawn `stdio.sh`", in whatever form that runtime takes.

Everything an agent can do with one iCloud account, in one directory and one process: calendar, contacts and mail over open protocols, Notes and Reminders through a resident browser holding a live web session, and the status of the iCloud Drive pull.

Headless, and the name says so. Calendar, contacts and mail need only the credentials. Notes and Reminders have no protocol at all, so they are driven through a Chromium that stays signed in, which is why a call there can take ninety seconds and why a lapsed web-access grant is a failure this server names rather than hides.

## Requirements

An always-on Linux machine with Python 3.11 or newer, and no screen needed. A small VPS is enough, and so is a Raspberry Pi: those are the setups this exists for.

- 64-bit, and on an ARM board check that `playwright install chromium` produces a binary before going further. If it does not, install the distribution's own Chromium and pass its path to Playwright's launch call as the executable path.
- A virtual X display, because Chromium must run headed. Apple binds the session to the user agent and the headless build reports a different one.
- An Apple ID with an app-specific password, and a device you can approve prompts on during setup.
- A private network between you and that machine, such as Tailscale or any other WireGuard-style network, before the first login. The first-time sign-in is done through a remote desktop onto a browser you are typing Apple credentials into, so it must never cross a public interface. See [`SECURITY.md`](SECURITY.md).
- Nothing on the client side. It is an ordinary MCP server over stdio, it runs on the same host as the runtime that spawns it, and it needs no part of this repository to be present on whatever spawns it.

The real cost is Chromium's memory, and it is worth measuring rather than quoting: start the resident browser, open the app tabs you actually use, and read the resident set size on your own box. Calendar, contacts and mail need no browser at all, so an install that skips Notes and Reminders is far cheaper than one that does not.

## Quick start

The full first-run sequence, including the two-factor login and Apple's Advanced Data Protection grant, is in [`docs/SETUP.md`](docs/SETUP.md). Follow it once, end to end; two of its steps need you to be holding an Apple device.

## Tools

23 of them, on one server entry, so a Notes tool is `<prefix>_create_note` rather than living behind a second server. `list_calendars` first when the question is about the calendar, `drive_status` first when data pulled off Drive looks short.

| Surface | Tools | Tier |
| --- | --- | --- |
| Calendar | `list_calendars`, `list_events` | read only |
| Calendar | `create_event` | silent |
| Calendar | `update_event` | silent for an event the owner is alone on, confirmed when the event has other attendees, because moving it reaches their calendars |
| Calendar | `delete_event` | confirmed, through MCP elicitation |
| Contacts | `search_contacts` | read only, over CardDAV |
| Mail | `list_mailboxes`, `list_mail`, `read_mail`, `search_mail` | read only, over IMAP, and it never marks a message as seen |
| Mail | `send_mail` | confirmed, through MCP elicitation |
| Notes | `notes_folders`, `notes_list`, `notes_read`, `notes_search` | read only, on the browser |
| Notes | `update_note` | confirmed. Replaces one note's body by selecting it and pasting, which leaves the app's own undo holding the old version |
| Notes | `create_note` | silent, on the browser. Creates only: no tool here edits or deletes a note |
| Reminders | `reminder_lists`, `list_reminders`, `completed_reminders` | read only, the first two on the browser and the third out of Apple's own records |
| Reminders | `complete_reminder`, `create_reminder` | silent, on the browser, and each verifies its write against what the app shows |
| Drive | `drive_status` | read only. Status, never the pull |

There is no mail delete or move tool, no note edit or delete, and no tool that can trigger the Drive pull.

**Asking is MCP elicitation, and it is the only gate here.** `ask_approval` in `app.py` is the whole mechanism: the question goes to the client over the protocol, the client renders it however it asks its user, and the tool executes only on an explicit approval. Nothing leaves this process by any other channel, so the server needs no messaging credential to ask a question, and anything the owner has to know about a completed write is in the tool result for the client to relay. A session that cannot ask, which is what a scheduled run looks like from in here, gets a refusal and no write. The elicitation schema has no required field, and the tool timeout must exceed the client's elicitation timeout; both traps are in https://github.com/sjdonado/personal-agent/blob/main/docs/confirmation-gate.md

**The tiering lives in this code, not in the client's configuration.** It is this server that decides a delete asks while creating an event solo does not. The client only renders the question.

## One process, two lock regimes

Notes and Reminders each hold their own per-app lock, because there is one browser and two calls must not drive it at once. DAV and IMAP take no lock at all. So a Notes call holding the browser does not serialise a mail read behind it, and that is the property worth re-testing after any change here: call `list_mail` while a Notes call holds its lock.

## The layout

| Path | What it is |
| --- | --- |
| `server.py` | imports the five tool modules and runs the transport. Importing a module is what registers its tools |
| `app.py` | the one `MCPServer`, the account's env contract, `ask_approval`, and the `YYYY-MM-DD` parser both protocol modules use |
| `tools/dav.py` | calendar and contacts over CalDAV and CardDAV, and the confirmed delete |
| `tools/mail.py` | mail over IMAP, and the confirmed send |
| `tools/notes.py` | Notes, through the browser |
| `tools/reminders.py` | Reminders, through the browser and through CloudKit for completions |
| `tools/drive.py` | `drive_status`, and nothing that touches the pull |
| `icloud_lib/` | the CDP attach, the per-app locks, the blocked latch, quiet hours, the timezone override, the pending-write queue, and which Drive libraries this install pulls. Shared between the server modules and the `bin/` helpers, and by nothing outside this directory |
| `bin/` | the browser plumbing: the resident, the session bootstrap and login, the session check, the re-ask, the drain, the tab reaper and the Drive fetch |
| `systemd/` | six units: the display, the browser, VNC and noVNC, and the tab reaper with its timer |
| `sudoers.d/` | two rules: the one wrapper a caller may spawn, and the re-ask |

## The session, and what a restart costs

The resident browser holds the iCloud session in process memory, and its profile under `$ICLOUD_STATE` persists the cookies, so a relaunch comes back signed in. A restart does not cost the session.

What a restart costs is Apple's data-access grant, which is bound to the browser instance. Loading an iCloud app page is what asks Apple for it, so the next app-page load after a restart raises "Allow your iCloud data to be accessed via the web?" on the owner's devices. The resident therefore loads only `icloud.com` at startup and opens no app tab, which is what makes `Restart=on-failure` safe.

A fresh app-page load is refused between 23:00 and 07:00 local. An already-loaded tab is untouched, so reading notes and reminders through the night still works; only a load that would raise a prompt is deferred, with a message saying so.

One latch decides whether the owner has already been asked, at `$ICLOUD_SHARED_STATE/blocked`. While it exists, the app helper refuses before touching the browser, nothing retries, and no watchdog restarts or re-requests, because retrying cannot produce a different answer until the owner approves. It is released by `agent-reask-access`, which is the owner saying they are at a device, or by `session_check` reporting `OK`. A re-ask navigates one app rather than two, because Apple's grant covers iCloud.com data rather than a single application.

When the session itself has expired, that is a different failure with a different message and it is interactive: start `agent-vnc`, tunnel to it over loopback, sign in, and tick "Trust this browser", or Apple never issues `X-APPLE-WEBAUTH-TOKEN` and the session dies at the next restart. Re-grant data access when prompted, then stop `agent-vnc`; the unit has no `[Install]` section on purpose, so it never comes back at boot.

## The Drive pull

`bin/icloud_drive_fetch.py` runs as the service account and stages files under `$DRIVE_STAGING`. It loads the Drive web app once to capture the query string the app appends to its own API calls, then talks to that API directly with the browser's cookies through Playwright's request context: `retrieveItemDetailsInFolders` on `drivews.icloud.com` lists a folder by id, `download/batch` on `docws.icloud.com` turns a file id into a signed URL, and a GET on that URL is the file. A `fetch` from the page's own JavaScript is refused by CORS, which is why the request context and not the page does the talking. Files whose `etag` is unchanged are skipped, and `$DRIVE_ETAGS` is what records that.

Scheduling it is yours. Nothing in this repository runs it for you: point a systemd timer or a cron entry at it, once a day being what the reference deployment does, and `drive_status` will tell a client whether that schedule is actually keeping up.

Which folders are pulled is configuration, not code: `DRIVE_LIBRARIES` is a JSON array of `{name, kind, dest}` entries. Use `folder` for any app export folder: every file beneath it is fetched and its relative layout is preserved under `dest`. `tree` is the narrower monthly-export layout, and `snapshot` is one file rewritten whole. An empty or unparseable value pulls nothing and says so rather than guessing at names. `icloud_lib/drive_libraries.py` documents the entry shape.

What happens to a staged file afterwards is outside this server. It fetches, it stages, and it reports; it never reads a staged file back and it never hands one on. If something else on the host imports what is staged, run that as a different account, so the account holding the browser session cannot reach whatever it feeds.

`drive_status` reports when the pull last ran and what is staged. It cannot trigger the pull, and neither can anything else here.

## Environment

One env file, owned by the account this runs as, mode 400, sourced by `stdio.sh`. `.env.example` carries the long form of every key.

| Key | What it is |
| --- | --- |
| `ICLOUD_APPLE_ID` | the Apple ID this server speaks for |
| `ICLOUD_APP_PASSWORD` | an app-specific password from the Apple ID account page, revocable there without touching anything else. Not the primary Apple password |
| `AGENT_TZ` | required, an IANA zone name. No default: a guessed zone types the wrong hour into an Apple picker and nothing errors |
| `AGENT_TZ_FILE` | optional, a file holding the zone, which wins over `AGENT_TZ` whenever it holds anything, because a zone changes when the owner travels and a restart is not the moment to find out. Defaults to `/etc/agent/timezone`. Whatever writes it needs no access to this account |
| `AGENT_DEFAULT_LIST` | which reminder list a reminder goes to when the caller names none |
| `AGENT_DEFAULT_CALENDAR` | which calendar an event goes to when the caller names none. Set it: the fallback is whichever calendar sorts first, and on a real account that is often a shared one |
| `ICLOUD_STATE` | everything this account writes for itself: the browser profile, the cookie jar, the per-app locks, the tab stamps. Defaults to `$HOME` |
| `ICLOUD_SHARED_STATE` | state two uids share: the blocked latch, the ask log, and the queue of writes deferred while the grant had lapsed. A watchdog running as somebody else reads these, so it belongs outside this account's home. Defaults to `$HOME/shared-state`, which is right for a single-uid install only |
| `ICLOUD_CDP` | where the resident Chromium listens for DevTools. Defaults to `http://127.0.0.1:9222` |
| `MAIL_ATTACHMENTS_DIR` | where `read_mail(save_attachments=True)` writes. Shared: this server writes it and whichever uid stages a saved file reads it, so it is group-readable and never group-writable. Defaults to `$HOME/mail-attachments` |
| `DRIVE_STAGING` | where the Drive pull leaves fetched files for another account to import. Defaults to `$ICLOUD_STATE/drive-staging` |
| `DRIVE_ETAGS` | the file recording what has already been fetched. Defaults to `$ICLOUD_STATE/state/drive-etags.json` |
| `DRIVE_LIBRARIES` | which Drive app folders to pull, as a JSON array. `folder` copies an arbitrary export folder recursively, while `tree` and `snapshot` support monthly and one-file exporters. These folders are deployment facts, not this server's. Empty means nothing is pulled |

`AGENT_TZ` is load-bearing. A host may well run UTC while the owner does not, and Apple's web pickers store what they are typed as the page's zone, so the tools convert before typing; getting it wrong moves a reminder by hours and nothing errors.

Three things the wrapper supplies and the env file does not, because they are about the host rather than the account: `DISPLAY` for the headed Chromium the browser-backed modules attach to, `PLAYWRIGHT_BROWSERS_PATH` for a shared root-owned Chromium rather than a per-account copy, and `HOME`, which every path default is under.

## Install from scratch

This directory plus its `requirements.txt` is the whole install. The paths and the account name below are one worked example and are install-time choices; a different prefix means changing it here, in `stdio.sh`, in the systemd units and in the sudoers rules together.

```
useradd -r -m -d /opt/agent-icloud -s /usr/sbin/nologin agent-icloud
install -d -o agent-icloud -g agent-icloud -m 700 /opt/agent-icloud/bin
install -o agent-icloud -g agent-icloud -m 750 -t /opt/agent-icloud/bin \
  stdio.sh server.py app.py bin/*.py bin/icloud-login.sh
install -o agent-icloud -g agent-icloud -m 640 requirements.txt /opt/agent-icloud/bin/
install -d -o agent-icloud -g agent-icloud -m 750 /opt/agent-icloud/bin/tools \
  /opt/agent-icloud/bin/icloud_lib
install -o agent-icloud -g agent-icloud -m 640 -t /opt/agent-icloud/bin/tools tools/*.py
install -o agent-icloud -g agent-icloud -m 640 -t /opt/agent-icloud/bin/icloud_lib icloud_lib/*.py
install -o root -g root -m 700 bin/agent-reask-access /usr/local/bin/agent-reask-access

su -s /bin/bash agent-icloud -c 'cd /opt/agent-icloud && python3 -m venv .venv \
  && .venv/bin/pip install -r /opt/agent-icloud/bin/requirements.txt'
install -d -o root -g root -m 755 /opt/playwright
PLAYWRIGHT_BROWSERS_PATH=/opt/playwright /opt/agent-icloud/.venv/bin/playwright install chromium

install -o agent-icloud -g agent-icloud -m 400 .env /etc/agent/icloud.env
install -o root -g root -m 440 sudoers.d/agent-icloud /etc/sudoers.d/agent-icloud
install -o root -g root -m 440 sudoers.d/agent-browser-restart /etc/sudoers.d/agent-browser-restart
visudo -c
install -o root -g root -m 644 -t /etc/systemd/system systemd/*
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

Then configure your agent runtime, on this same machine, to spawn the wrapper, with a timeout above the runtime's own elicitation timeout. The command is the whole of it:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh
```

```json
{"command": "sudo", "args": ["-n", "-u", "agent-icloud", "/opt/agent-icloud/bin/stdio.sh"], "timeout": 330}
```

How that is written down is the runtime's business rather than this server's, and so is the name the entry is given: where a runtime prefixes tool names with that name, it has to be a valid identifier. 330 seconds is deliberate: a browser-backed call routinely takes 60 to 90 seconds and a cold app tab longer.

## Dependencies

`requirements.txt`: `caldav` and `vobject` for calendar and contacts, `mcp`, and `playwright` for the browser-backed modules, which attach to the resident Chromium over CDP rather than launching anything. Mail is `imaplib` and `smtplib` from the standard library.

## Verification

Run it as the account it runs as, and without restarting the browser:

```
sudo -u <caller> -H sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
sudo -u agent-icloud -H /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/session_check.py
```

The first proves the sudo grant, the env file and the venv, from the uid that uses it: it should start and exit silently on EOF. The second reports whether the resident browser can actually reach iCloud data, and distinguishes the three answers that need different fixes: healthy, signed out, needs a device approval, or no browser to talk to.

Then, with the account's environment, call the tools directly and expect all four surfaces to answer: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders`, `drive_status`. Between 23:00 and 07:00 local, `reminder_lists` refuses rather than raising a prompt on the owner's devices; that is the quiet-hours rule working, not a failure.

## Pitfalls

**Restarting the browser is a decision, never a side effect.** The resident process is the session, and the next app-page load after a restart costs the owner an approval tap on a device. Reloading unit files, moving these files and re-running the wrapper are all free; restarting `agent-browser`, `agent-xvfb`, `agent-vnc` or `agent-novnc` is not.

**Chromium must run headed.** Playwright's headless mode is a different binary reporting `HeadlessChrome`, and Apple binds the session to the user agent, so a headless relaunch reads as a different browser and the session dies server-side. Hence the permanent virtual display.

**CalDAV's Reminders store is a dead one.** `caldav.icloud.com` serves a legacy store that Apple left behind when Reminders moved to the CloudKit format: writes there succeed, read back, and are invisible on every device and in Apple's own web UI. Reminders are browser-backed for that reason, and reminder tools must never be restored on CalDAV.

**Recurring events need `expand=True`**, or CalDAV returns the master occurrence and a listing reports events in the wrong month. Writes read back rather than echoing the request, which is what makes a stored time a checked fact.

**An ISO offset is not a timezone.** `datetime.fromisoformat` returns a fixed-offset tzinfo, and the calendar layer serialises that by taking the wall clock and calling it UTC, which shifts an event silently. Offset-carrying values are converted into the named zone so icalendar has a real TZID to write.

**IMAP flags arrive after the literal**, so reading them off the first fetch tuple marks every message unread. `unread` and `answered` each come from their own `UID SEARCH`.

**Server-side mail search is not substring search, and cannot see an encoded header.** A MIME-encoded subject cannot match a plain word, uids come back in no particular order, and a shorter query can return fewer results than a longer one that contains it. So `search_mail` unions a server-side pass with a local pass over recent messages with headers decoded, sorts newest first rather than slicing, and reports `matched` and `scanned_recent` so a caller can tell a slice from a whole answer. `SEARCH FROM` is unreliable on the domain part: search the local part or a subject word, and treat an empty search as weak evidence.

**A mailbox that does not exist does not raise.** `imaplib.select` returns a tuple on a NO and leaves the connection in AUTH, so the next search fails with a message about state rather than about the name. `_select` checks the result and raises with the mailbox name and the mailboxes that do exist, and `list_mailboxes` exists so names never have to be guessed.

**Mail is the most hostile byte stream this server writes to disk.** Saved attachments are held to four limits, none sufficient alone: a type denylist that includes archives, because their contents are not inspected; a size cap; a basename reduced to a safe character set, so a traversing filename lands in the directory rather than above it; and an expiry swept on every call. A refusal comes back in `attachments_skipped` with its reason, because a statement that was not saved and a statement that had no attachment must not read the same.

**Apple's Notes body is rendered to canvas, not into the DOM.** Text is read the way a person would read it, by opening the note and copying, and the copy is checked against the requested title, because returning another note's content silently is the one failure this must never have. A mismatch retries once and then refuses.

**The web apps are shadow DOM, virtualised and pointer-real.** Selectors have to walk `shadowRoot` recursively, class tokens must be compared exactly rather than by substring, only rendered rows exist so a row below the fold is invisible to a read-back, and a synthetic `.click()` can appear to succeed while selecting nothing, so a full `pointerdown`/`mousedown`/`pointerup`/`mouseup`/`click` sequence is dispatched. Search goes through the app's own box and the query is cleared through the keyboard afterwards, since setting `.value` leaves the app's own state holding it.

**A note or reminder write is verified, never assumed.** A search-matched row has its title rewritten by Apple to the text around the hit with a leading ellipsis, so verification matches on the longest word with the ellipsis stripped rather than on an exact substring. Where a write cannot be confirmed, the tool says it typed the thing and could not confirm it rather than reporting success.

**A reminder's due date is only settable through the details popover, and the order matters.** Enabling the day switch leaves Apple's time checkbox ticked at midnight, so a date meant to be all-day becomes an alarm; clearing the time after picking the day moves the date back a day, because the picker holds an instant rather than a date. Untick the time first, then pick the day. Selecting a day does not close the popover and dismissing it does not commit: without Save the date is silently dropped. The day cell is matched by `aria-label` containing the canonical form, not by equality, since Apple decorates today and the selected day with extra words.

**The timezone override must be in place before the app loads.** Apple's web apps read their zone once at startup and keep it, so applying `Emulation.setTimezoneOverride` to an already-loaded tab changes what `Intl` reports and nothing the app has decided. The tab is reloaded once per lifetime after the override is set, and `create_reminder` compares the hour on the row against the hour asked for and returns a `due_error` if they disagree, because the web UI never displays the stored zone and that comparison is the only signal available.

**Warm tabs leak disk, invisibly.** Chromium's temporary files are deleted but held open, so `du` reports none of it while the disk fills. An app tab left open for days is the shape of the problem; the tab reaper closes one that has gone unused for fifteen minutes, which keeps warmth within a working session, and every entry point touches the app's stamp file so a tab is not closed mid-session. Warmth is worth keeping because a cold app load costs twenty to forty seconds.

**A signed-out page still looks like an app.** It renders an app-shaped shell that accepts clicks and discards them, and Apple serves a signed-out app URL with the app's own title, so signed-in state is judged from page content and `SignedOut` is raised before any navigation.

**Access can lapse on its own.** With Advanced Data Protection, the web gets only temporary access and the app then sits on "Getting Access" showing nothing. Reporting that as "you have no notes" would be a confident lie, so the tools detect it and return an honest failure with `needs_device_approval`.

**A modal alert blocks every click while the DOM looks healthy.** An Apple error alert sitting over the app makes a click succeed and do nothing, and its OK is a `div`, not a `button`, so a tag-based search finds nothing to dismiss. Opening a list dismisses any alert first. When the DOM says everything is fine and nothing works, take a screenshot.

## Reference deployment

This was extracted from a running personal-agent install, where it is one MCP server among several: https://github.com/sjdonado/personal-agent

That repository is the worked example of everything this README describes as an install-time choice: the service account, the paths, the systemd units, the sudoers rules, and how that deployment's agent runtime is configured to spawn the wrapper. Runtime wiring lives there rather than here, because it is a property of the runtime and not of this server. It is also where the dated measurements and the operating history live, so it is the place to look when you want to know what something cost in practice rather than how it works.

## Security

[`SECURITY.md`](SECURITY.md) is the threat model. Read it before pointing this at an Apple ID you cannot afford to lose: a running install holds a logged-in Apple session on a machine, the VNC door must stay on loopback, and the credentials belong to one account and one file. It also says how to report a vulnerability privately.

## History and measurements

https://github.com/sjdonado/personal-agent/blob/main/docs/mcps/icloud-headless.md
