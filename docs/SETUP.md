# First run, on a fresh Linux server

This is the whole sequence for the server install: an always-on Linux box, a service account that owns the Apple session, and an agent runtime on the same box that spawns the server through sudo. It covers the prerequisites, the environment file, the resident browser, the first Apple login with two-factor, the Advanced Data Protection grant, how to check that each surface answers, and what to do months later when Apple's grant lapses.

For a laptop or any single-user machine, you do not need this document: the README's quick start is the whole setup (one MCP entry with two environment variables, and the first call opens the login door).

Read it once before starting. Two steps need you to be holding an Apple device.

The systemd units in `systemd/` are the worked example throughout. They are the units from a real deployment, with the paths and the account name that deployment used. Nothing forces you to use those names: they are install-time choices, and if you change them you change them in the units, in the wrapper and in the sudoers rules together.

Fast path: `install.sh` from the README performs sections 1 through 5 for the worked example. What follows is the same sequence by hand, for when you deviate from it.

## Minimum requirements, plainly

A Linux machine that is always on, that you can reach over SSH. A small VPS is enough. A Raspberry Pi is enough. Neither needs a screen or a display server: Chromium runs headless. Nothing is built on the host: the install is one static binary plus a system Chromium.

64-bit. Any distribution Chromium works; where it is not on `PATH`, set `CHROMIUM_BIN` to its path.

Disk is modest: the code is small, a Chromium build is the bulk of it, and the browser profile grows slowly. Leave room for the staging directory if you use the Drive pull.

The real cost is Chromium's memory, and it is the only requirement worth measuring rather than quoting. One headless Chromium holding an iCloud session, plus an app tab or two, is the whole footprint of this system. Measure it on your own box: start the resident, open the tabs you actually use, and read the resident set size of the browser process tree. Give the machine swap before you decide it is too small, and if you are tight on memory, let the tab reaper close idle tabs aggressively, because a warm tab is a convenience and not a requirement.

Do not run this on a machine with no network privacy or with other people's shells on it. It holds a logged-in Apple session. See [`../SECURITY.md`](../SECURITY.md).

The server itself runs on the same machine as the agent runtime that drives it, as a child process over stdio. The only listener it ever opens is the login door, for twenty minutes at most, on a private address.

## 0. A private network, before anything else

Set up Tailscale, or an equivalent WireGuard-style private network, between your own devices and this host before the first login. Do this first, not later.

The reason is step 6. The first login is you typing an Apple ID, a password and a two-factor code into the login door, a web page that streams the headless browser on this host, and what you leave behind is a browser holding a logged-in Apple account. The door binds the host's Tailscale address when Tailscale answers, and loopback otherwise. It never binds a wildcard or a public interface.

Without Tailscale the door is loopback-only, and an SSH tunnel (`ssh -N -L 6080:127.0.0.1:6080 your-host`, with the port from the door's link: 6080 unless something else holds it) is the smallest private network that reaches it. What is not an option is exposing the port.

## 1. Packages

```
sudo apt update
sudo apt install chromium
```

That is the only package. Where your distribution names it differently, install that package instead and set `CHROMIUM_BIN` if it lands outside `PATH`. There is no virtual display, no VNC server and no noVNC: the resident runs Chromium headless, and the login door streams it over the DevTools protocol.

## 2. A service account of its own

This account owns the session and the credentials and nothing else. Nothing else should run as it, and it should not be the account your MCP client runs as.

```
sudo useradd -r -m -d /opt/agent-icloud -s /usr/sbin/nologin agent-icloud
sudo install -d -o agent-icloud -g agent-icloud -m 700 /opt/agent-icloud/bin
```

The account name and the path above are a worked example, and they are the ones the shipped systemd units, `stdio.sh` and the sudoers rules already carry. Change them if you like, but change them in all four places together, or the units will start a server that is not there.

Unpack a release tarball into that `bin` directory: one `icloud-mcp` binary holding the server and every helper as subcommands, plus the `stdio.sh` wrapper (what the agent spawns), `icloud-mcp-host` (the same environment for the operator's own commands below: it takes a subcommand, `stdio.sh` does not) and the re-ask helper. Everything the account runs is that one binary, owned by the service account and not writable by anybody else. `install.sh` does exactly that for the worked example.

## 3. Chromium

Step 1 already installed it. Confirm the resident will find it: a `chromium`, `chromium-browser` or `google-chrome` on `PATH` is enough, otherwise export `CHROMIUM_BIN` with its path in the units that launch a browser. Prefer a root-owned browser binary the service account cannot modify over a per-account copy.

## 4. The environment file

Copy `.env.example` to `.env`, fill it in, and install it readable only by the service account:

```
cp .env.example .env  # then edit it
sudo install -d -m 755 /etc/agent
sudo install -o agent-icloud -g agent-icloud -m 400 .env /etc/agent/icloud.env
```

Two keys are required: `ICLOUD_APPLE_ID` and `ICLOUD_APP_PASSWORD`. Everything else has a default.

`ICLOUD_APP_PASSWORD` is an app-specific password, not your primary Apple password. It authenticates CalDAV, CardDAV, IMAP and SMTP, and it is revocable on its own without touching anything else, which is what you want on the day something goes wrong. Generate it at account.apple.com, signed in as the account this server speaks for, under Sign-In and Security, App-Specific Passwords: choose Generate (or `+`), give it a label naming this install so you recognise it later, and copy the `xxxx-xxxx-xxxx-xxxx` value it shows exactly once. It needs two-factor authentication on the account; without it Apple offers no app-specific passwords at all. Never put the primary password in this file.

`AGENT_TZ` defaults to the host's own zone (from `TZ` or `/etc/localtime`). A server usually runs UTC while its owner does not, and on a UTC host (or one whose zone cannot be read) the server refuses to start until you set `AGENT_TZ`. Apple's web pickers store what they are typed as the page's zone, so the tools convert before typing, and a guessed zone moves a reminder by hours with no error. Set an IANA name such as `Europe/Amsterdam`. If you travel, point `AGENT_TZ_FILE` at a file holding the zone: it wins over `AGENT_TZ` whenever it holds anything, so the zone can change without a restart and without touching this account's credentials.

Set `AGENT_DEFAULT_CALENDAR` too. The calendar fallback is whichever calendar sorts first, and on a real account that is often a shared one, so an event meant to be private quietly is not.

Leave `DRIVE_LIBRARIES` empty unless you want the Drive pull. Empty means nothing is pulled, and the fetcher says so rather than guessing at folder names.

State paths default under `ICLOUD_STATE` (`$HOME/.icloud-mcp`). The worked example keeps state in the account home instead: `stdio.sh` and the units set `ICLOUD_STATE=/opt/agent-icloud` and `ICLOUD_SHARED_STATE=/opt/agent-icloud/shared-state`. Move a single path out (`ICLOUD_SHARED_STATE`, `MAIL_ATTACHMENTS_DIR`, `DRIVE_STAGING`, `DRIVE_ETAGS`, `HEALTH_DB`) only when another account has to read it.

## 5. The resident browser

Install the units, adjusting the account name and paths if you changed them:

```
sudo install -o root -g root -m 644 -t /etc/systemd/system systemd/*
sudo systemctl daemon-reload
sudo systemctl enable --now agent-browser agent-tab-reaper.timer
```

`agent-browser` is the resident: one headless Chromium against the persisted profile, with DevTools on `127.0.0.1:9222` for the tools to attach to. The process is the session, which is the single most important sentence in this document.

Chromium runs `--headless=new` and presents the ordinary Chrome user agent for its own major version, because Apple binds the session to the user agent and the headless build otherwise announces itself as `HeadlessChrome`. A relaunch on the same profile keeps the session. `ICLOUD_HEADED=1` runs it headed instead, for debugging on a machine with a display.

The resident deliberately loads only `icloud.com` at startup and opens no app tab. Loading an app page is what asks Apple for the data-access grant, so a startup that opened app tabs would put approval prompts on your devices at any hour, and that is what makes an automatic restart on failure safe here.

On this install systemd owns the browser, so `stdio.sh` sets `ICLOUD_RESIDENT=external`: the server never starts a browser of its own. On a laptop, where no unit exists, the server starts the resident itself when nothing answers on the loopback DevTools port.

Upgrading from an older install: `install.sh` disables and removes the `agent-xvfb`, `agent-vnc` and `agent-novnc` units. Stopping the display ends the old headed browser once, and systemd brings `agent-browser` straight back headless on the same profile.

## 6. The first iCloud login, with two-factor

A fresh Chromium is not signed in, so this one step is interactive and needs you to be holding a device that receives Apple's code.

Two ways in, and both open the same login door. From the agent: ask it for anything in Notes or Reminders; the call reports `needs_login`, the agent calls `open_login`, you approve the ask, and the result carries a link, the username `root` and a one-time password. From the host:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp-host login
```

It prints the same link and password, waits for the sign-in, and closes the door on Ctrl-C.

Open the link in any browser on your private network, enter `root` and the password, click into the page, and sign in with the Apple ID and the two-factor code. The Paste button types your clipboard into the focused field, which is the easy way to enter a password from a password manager.

**Tick "Trust this browser".** Without it Apple never issues the remembered-browser cookie, and the session dies on the next relaunch no matter what else you do. This is the most common way a first setup fails, and it fails later rather than immediately, which is what makes it expensive.

The door notices the sign-in by itself. It stops streaming, shows "Signed in to iCloud", and exits a few seconds later. Nothing is left listening.

## 7. Granting web access for Notes and Reminders

If your account has Advanced Data Protection enabled, the web gets only temporary access to some surfaces, and it has to be granted from a device you already trust.

The first Notes or Reminders call raises the request. Apple shows "Allow your iCloud data to be accessed via the web?" on your devices. Unlock the device and approve it. If the door is still open at that point, it shows "Signed in, one step left" until you do.

Approving once covers iCloud.com data rather than a single application, which is why the re-ask helper navigates one app rather than two: navigating both puts two notifications on the phone for every re-ask.

Until the grant is in place the apps sit on "Getting Access" and show nothing. The tools report that as `needs_device_approval` rather than as an empty list, so a refusal at this stage looks like a refusal and not like an empty account.

## 8. Verify each surface

First, prove the browser can reach iCloud data at all:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp-host session-check
```

It distinguishes the four answers that need four different fixes: exit 0 healthy, 1 signed out, 2 no browser to talk to, 3 needs a device approval.

Then prove the wrapper starts as the uid that will actually use it. It should start and exit silently on end of input:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
```

Then configure your agent runtime to spawn `stdio.sh`, with a timeout above 300 seconds, and call one tool per surface: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders` and `drive_status`. All five should answer. Anything less than all five is a setup that is not finished, and it is much cheaper to find that now than the first time you need it.

From 23:00 through 06:59 owner-local, a browser-backed call that would need a fresh app page load refuses instead of raising a prompt on your devices at night. An already-open tab still works. That is the quiet-hours rule doing its job, not a failure.

## 9. When something lapses later

Two different failures, two different tools, and the errors route between them.

`needs_device_approval` means Apple's grant lapsed: Notes and Reminders stop answering while mail and calendar are fine. A latch records that the owner has already been asked, so nothing retries in a loop and no watchdog restarts anything, because retrying cannot produce a different answer until the owner approves. `reask_access` re-fires the prompt and clears the latch, but only when the latch is actually set, outside quiet hours, and the owner approves the ask. In a conversation, call it and tell the owner to approve: the prompt lands within seconds. In a background run, do not call it and do not retry in a loop: report that access lapsed, and reload only when the owner replies asking to try again. On the host, `sudo /usr/local/bin/agent-reask-access` does the same re-navigation.

Do not restart the browser to fix a lapsed grant. A restart discards the warm tabs, costs the warm-up, and can raise an approval prompt of its own. A navigation is enough and leaves the browser standing.

`needs_login` means the session expired: `icloud-mcp session-check` reports signed out. That one is interactive, and it is step 6 again: `open_login` from the agent or `icloud-mcp login` on the host, including the "Trust this browser" tick. Every `open_login` issues a fresh password and closes any door already open, so a lost password costs one more call, not a wait. Open the door only when the owner is there to click through, never speculatively and never from a scheduled run: anyone holding the link and the password drives the login screen until the door closes.

Restarting the browser is a decision, never a side effect. Reloading unit files, moving these files and re-running the wrapper are all free.

## 10. Configure your agent runtime to spawn `stdio.sh`

That is the last step, and it is the only one this document cannot write for you. This server is a child process of the runtime that drives it, on this same machine, so what remains is telling that runtime to spawn `sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh` with a timeout above 300 seconds. Give the entry a name that is a valid identifier such as `icloud`: a runtime that prefixes tool names with it needs one. Copy-paste entries for common agents are in the README.

## 11. Optional: health extension

Skip this unless Apple Health data should be queryable. The extension reads a staged export folder, never the phone and never Drive directly.

Set the export directory to the staged folder (for example a HealthMirror app folder under `DRIVE_STAGING`), leaving `HEALTH_DB` at its default unless the state layout deviates:

```
HEALTH_EXPORT_DIR=/opt/agent-icloud/drive-staging/<dest>
```

Backfill once: save the official Apple export from agent chat into staging (beside the continuous-push folder, same layout) and import by hand to prove the plumbing:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp-host health-import
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp-host health-import --self-test
```

The first prints per-file seen/new counts; the second runs the six contract checks with no files touched. Then enable the daily timer and confirm the tools answer (unconfigured until the first import lands, never zeros):

```
sudo systemctl enable --now agent-health-import.timer
```

The timer unit shipped with the rest of `systemd/`; enabling it is the only step the installer leaves to you, because an import schedule is a deployment choice. The Drive pull's own scheduling stays yours and stays separate.

Ownership is exclusive and stays that way: the database at `HEALTH_DB` belongs to this service account, the importer running as it is the sole writer, and the tools open read-only. If a health database already exists under another account, do not point `HEALTH_DB` at it: that would make this account a writer of another account's store. Migrate instead, once, by hand: copy the file into place and give this account ownership, then the extension manipulates only its own copy from there on.
