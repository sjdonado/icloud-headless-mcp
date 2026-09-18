# Architecture

Technical internals for `icloud-headless-mcp`: process and lock model, directory layout, session lifecycle, Drive pull mechanics, and the full pitfalls list. For the general overview and the setup (environment, install, dependencies, verification) start with the [`README.md`](../README.md); for the first-run sequence follow [`SETUP.md`](SETUP.md).

## One process, two lock regimes

Notes and Reminders each hold their own per-app lock, because there is one browser and two calls must not drive it at once. DAV and IMAP take no lock at all. So a Notes call holding the browser does not serialise a mail read behind it, and that is the property worth re-testing after any change here: call `list_mail` while a Notes call holds its lock.

## The layout

| Path | What it is |
| --- | --- |
| `cmd/icloud-mcp` | the one static binary: the server (default, no subcommand) and every helper as subcommands (`session-check`, `resident`, `login`, `reask`, `drain`, `tab-reaper`, `drive-fetch`). `icloud-mcp help` lists them |
| `internal/mcpserver` | the one `MCPServer`, the transport, and the elicitation approval gate |
| `internal/config` | the account's env contract |
| `internal/dav` | calendar and contacts over CalDAV and CardDAV, and the confirmed delete |
| `internal/mail` | mail over IMAP, and the confirmed send |
| `internal/notes` | Notes, through the browser |
| `internal/reminders` | Reminders, through the browser and through CloudKit for completions |
| `internal/drive` | `drive_status`, and nothing that touches the pull |
| `internal/browser` | the CDP attach, the per-app locks, the blocked latch, quiet hours, the timezone override, and the tab reaper. Shared between the server handlers and the subcommands, and by nothing outside this directory |
| `internal/drivefetch`, `internal/dvlibraries` | the Drive fetch mechanics and which libraries this install pulls |
| `internal/queue`, `internal/drain` | the pending-write queue and the drain pass over it |
| `internal/health` | the optional health extension: export importer, SQLite store, and six read-only tools. Only the importer opens the store read-write; the Drive layer never learns what it stages for it. The store file belongs to the service account exclusively: adopting another account's database means copying it into place, never pointing at it live |
| `systemd/` | six units: the display, the browser, VNC and noVNC, and the tab reaper with its timer |
| `sudoers.d/` | two rules: the one wrapper a caller may spawn, and the re-ask |

`icloud-mcp` with no subcommand serves stdio, which is what `stdio.sh` spawns; the units and wrappers name the same binary with its subcommand, so the one-binary property is reviewable in one `ls`.

## The session, and what a restart costs

The resident browser holds the iCloud session in process memory, and its profile under `$ICLOUD_STATE` persists the cookies, so a relaunch comes back signed in. A restart does not cost the session.

What a restart costs is Apple's data-access grant, which is bound to the browser instance. Loading an iCloud app page is what asks Apple for it, so the next app-page load after a restart raises "Allow your iCloud data to be accessed via the web?" on the owner's devices. The resident therefore loads only `icloud.com` at startup and opens no app tab, which is what makes `Restart=on-failure` safe.

A fresh app-page load is refused between 23:00 and 07:00 local. An already-loaded tab is untouched, so reading notes and reminders through the night still works; only a load that would raise a prompt is deferred, with a message saying so.

One latch decides whether the owner has already been asked, at `$ICLOUD_SHARED_STATE/blocked`. While it exists, the app helper refuses before touching the browser, nothing retries, and no watchdog restarts or re-requests, because retrying cannot produce a different answer until the owner approves. It is released by `agent-reask-access`, which is the owner saying they are at a device, or by `icloud-mcp session-check` reporting healthy. A re-ask navigates one app rather than two, because Apple's grant covers iCloud.com data rather than a single application.

When the session itself has expired, that is a different failure with a different message and it is interactive: start `agent-vnc`, tunnel to it over loopback, sign in, and tick "Trust this browser", or Apple never issues `X-APPLE-WEBAUTH-TOKEN` and the session dies at the next restart. Re-grant data access when prompted, then stop `agent-vnc`; the unit has no `[Install]` section on purpose, so it never comes back at boot.

The same two recoveries are tools, for when nobody can SSH. `reask_access` runs the re-navigate plus latch-clear core that lives in `session.Reask` (the `reask` subcommand delegates to it), but only when the latch is set, outside quiet hours, and the owner approves the ask: the re-navigation itself does not check the clock, so the tool carries that gate or a night call would spend a prompt. `open_login` opens a supervised door instead of the hand-started units: one x11vnc plus one websockify on the same fixed ports, behind a one-time 8-character password (VNC auth truncates longer ones) with a 20-minute TTL. The websockify listener binds the tailnet address when the tool advertises one and loopback otherwise, never a wildcard. Both servers run under `timeout`, so the processes die at TTL even with no further traffic; the door record lives under `$ICLOUD_STATE/state/login-door/` with the pids, the expiry, and the password file, and is swept by every session-tool entry and every successful browser call, early when a healthy session is observed. The password is issued once in the opening result and never stored in cleartext. Either tool refuses before acting when its precondition fails, so both are safe to probe.

## The Drive pull

`icloud-mcp drive-fetch` runs as the service account and stages files under `$DRIVE_STAGING`. It loads the Drive web app once to capture the query string the app appends to its own API calls, then talks to that API directly with the browser's cookies over plain HTTPS: `retrieveItemDetailsInFolders` on `drivews.icloud.com` lists a folder by id, `download/batch` on `docws.icloud.com` turns a file id into a signed URL, and a GET on that URL is the file. A `fetch` from the page's own JavaScript is refused by CORS, which is why a direct client carrying the cookies, and not the page, does the talking. Files whose `etag` is unchanged are skipped, and `$DRIVE_ETAGS` is what records that.

Scheduling it is yours. Nothing in this repository runs it for you: point a systemd timer or a cron entry at it, once a day being a common choice, and `drive_status` will tell a client whether that schedule is actually keeping up.

Which folders are pulled is configuration, not code: `DRIVE_LIBRARIES` is a JSON array of `{name, kind, dest}` entries. Use `folder` for any app export folder: every file beneath it is fetched and its relative layout is preserved under `dest`. `tree` is the narrower monthly-export layout, and `snapshot` is one file rewritten whole. An empty or unparseable value pulls nothing and says so rather than guessing at names. `internal/dvlibraries` documents the entry shape.

What happens to a staged file afterwards is outside this server. It fetches, it stages, and it reports; it never reads a staged file back and it never hands one on. If something else on the host imports what is staged, run that as a different account, so the account holding the browser session cannot reach whatever it feeds.

`drive_status` reports when the pull last ran and what is staged. It cannot trigger the pull, and neither can anything else here.

## Pitfalls

**Restarting the browser is a decision, never a side effect.** The resident process is the session, and the next app-page load after a restart costs the owner an approval tap on a device. Reloading unit files, moving these files and re-running the wrapper are all free; restarting `agent-browser`, `agent-xvfb`, `agent-vnc` or `agent-novnc` is not.

**Chromium must run headed.** Headless mode reports a different user agent, and Apple binds the session to the user agent, so a headless relaunch reads as a different browser and the session dies server-side. Hence the permanent virtual display.

**CalDAV's Reminders store is a dead one.** `caldav.icloud.com` serves a legacy store that Apple left behind when Reminders moved to the CloudKit format: writes there succeed, read back, and are invisible on every device and in Apple's own web UI. Reminders are browser-backed for that reason, and reminder tools must never be restored on CalDAV.

**Recurring events need `expand=True`**, or CalDAV returns the master occurrence and a listing reports events in the wrong month. Writes read back rather than echoing the request, which is what makes a stored time a checked fact.

**An ISO offset is not a timezone.** A parsed offset is a fixed shift, and the calendar layer would serialise that by taking the wall clock and calling it UTC, which shifts an event silently. Offset-carrying values are converted into the named zone so the event carries a real TZID to write.

**IMAP flags arrive after the literal**, so reading them off the first fetch marks every message unread. `unread` and `answered` each come from their own `UID SEARCH`.

**Server-side mail search is not substring search, and cannot see an encoded header.** A MIME-encoded subject cannot match a plain word, uids come back in no particular order, and a shorter query can return fewer results than a longer one that contains it. So `search_mail` unions a server-side pass with a local pass over recent messages with headers decoded, sorts newest first rather than slicing, and reports `matched` and `scanned_recent` so a caller can tell a slice from a whole answer. `SEARCH FROM` is unreliable on the domain part: search the local part or a subject word, and treat an empty search as weak evidence.

**A mailbox that does not exist does not raise.** A select on a NO leaves the connection in AUTH, so the next search fails with a message about state rather than about the name. The select helper checks the result and raises with the mailbox name and the mailboxes that do exist, and `list_mailboxes` exists so names never have to be guessed.

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
