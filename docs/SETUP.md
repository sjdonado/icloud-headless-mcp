# First run, on a fresh Linux box

This is the whole sequence for somebody who has never run this before: prerequisites, the service account, the environment file, the browser stack, the first Apple login with two-factor, the Advanced Data Protection grant, and how to check each surface actually answers. It ends with what to do months later when Apple's grant lapses, because it will.

Read it once before starting. Two steps in the middle need you to be holding an Apple device, and finding that out halfway through is annoying.

The systemd units in `systemd/` are the worked example throughout. They are the units from a real deployment, with the paths and the account name that deployment used. Nothing forces you to use those names: they are install-time choices, and if you change them you change them in the units, in the wrapper and in the sudoers rules together.

Fast path: `install.sh` from the README performs sections 1 through 5 for the worked example. What follows is the same sequence by hand, for when you deviate from it.

## Minimum requirements, plainly

A Linux machine that is always on, that you can reach over SSH. A small VPS is enough. A Raspberry Pi is enough. Neither needs a screen. Nothing is built on the host: the install is one static binary plus a system Chromium.

64-bit. Any distribution Chromium works; where it is not on `PATH`, set `CHROMIUM_BIN` to its path.

Disk is modest: the code is small, a Chromium build is the bulk of it, and the browser profile grows slowly. Leave room for the staging directory if you use the Drive pull.

The real cost is Chromium's memory, and it is the only requirement worth measuring rather than quoting. One headed Chromium holding an iCloud session, plus an app tab or two, is the whole footprint of this system. Measure it on your own box: start the resident, open the tabs you actually use, and read the resident set size of the browser process tree. Give the machine swap before you decide it is too small, and if you are tight on memory, let the tab reaper close idle tabs aggressively, because a warm tab is a convenience and not a requirement.

Do not run this on a machine with no network privacy or with other people's shells on it. It holds a logged-in Apple session. See [`../SECURITY.md`](../SECURITY.md).

The server itself runs on the same machine as the agent runtime that drives it, as a child process over stdio. There is nothing to expose and nothing to reach across a network.

## 0. A private network, before anything else

Set up Tailscale, or an equivalent WireGuard-style private network, between your own machine and this host before you start the browser stack. Do this first, not later.

The reason is step 6. The first login is you typing an Apple ID, a password and a two-factor code into a browser on a machine with no screen, reached through a remote desktop, and what you leave behind is a browser holding a logged-in Apple account. Neither VNC nor noVNC may ever be reachable on a public interface, at any point, including while you are setting it up.

The shipped noVNC unit requires `tailscaled.service` and is reachable only over that tailnet, which is why the dependency is in the unit rather than being an accident of one box. Any other private network you control works the same way, and so does the plain SSH tunnel used in step 6, which is the smallest version of the same idea. What is not an option is opening the port.

## 1. Packages

```
sudo apt update
sudo apt install xvfb x11vnc novnc websockify chromium
```

`xvfb` is the virtual display, because Chromium must run headed: Apple binds the session to the user agent and the headless build reports a different one, so a headless relaunch reads as a different browser and the session dies server-side. `x11vnc` and `novnc` exist only so a human can complete Apple's prompts once. `chromium` is the headed browser the resident launches; where your distribution names it differently, install that package instead and set `CHROMIUM_BIN` if it lands outside `PATH`.

## 2. A service account of its own

This account owns the session and the credentials and nothing else. Nothing else should run as it, and it should not be the account your MCP client runs as.

```
sudo useradd -r -m -d /opt/agent-icloud -s /usr/sbin/nologin agent-icloud
sudo install -d -o agent-icloud -g agent-icloud -m 700 /opt/agent-icloud/bin
```
The account name and the path above are a worked example, and they are the ones the shipped systemd units, `stdio.sh` and the sudoers rules already carry. Change them if you like, but change them in all four places together, or the units will start a server that is not there.

Unpack a release tarball into that `bin` directory: one `icloud-mcp` binary holding the server and every helper as subcommands, plus the `stdio.sh` wrapper and the login and re-ask helpers. Everything the account runs is that one binary, owned by the service account and not writable by anybody else. The install block in the `README.md` (`Install from scratch`) is one worked example of exactly that.

## 3. Chromium

Step 1 already installed it. Confirm the resident will find it: a `chromium`, `chromium-browser` or `google-chrome` on `PATH` is enough, otherwise export `CHROMIUM_BIN` with its path in the units that launch a browser. Prefer a root-owned browser binary the service account cannot modify over a per-account copy.

## 4. The environment file

Copy `.env.example` to `.env`, fill it in, and install it readable only by the service account:

```
cp .env.example .env  # then edit it
sudo install -d -m 755 /etc/agent
sudo install -o agent-icloud -g agent-icloud -m 400 .env /etc/agent/icloud.env
```

Two values need thought before you type them.

`ICLOUD_APP_PASSWORD` is an app-specific password, not your primary Apple password. It authenticates CalDAV, CardDAV, IMAP and SMTP, and it is revocable on its own without touching anything else, which is what you want on the day something goes wrong. Generate it at account.apple.com, signed in as the account this server speaks for, under Sign-In and Security → App-Specific Passwords: choose Generate (or `+`), give it a label naming this install so you recognise it later, and copy the `xxxx-xxxx-xxxx-xxxx` value it shows exactly once. It needs two-factor authentication on the account; without it Apple offers no app-specific passwords at all. Never put the primary password in this file.

`AGENT_TZ` is required and has deliberately no default. Your host may well run UTC while you do not, and Apple's web pickers store what they are typed as the page's zone, so the tools convert before typing. A guessed zone moves a reminder by hours and nothing errors. Set an IANA name such as `Europe/Amsterdam`. If you travel, point `AGENT_TZ_FILE` at a file holding the zone: it wins over `AGENT_TZ` whenever it holds anything, so the zone can change without a restart and without touching this account's credentials.

Set `AGENT_DEFAULT_CALENDAR` and `AGENT_DEFAULT_LIST` too. The calendar fallback is whichever calendar sorts first, and on a real account that is often a shared one, so an event meant to be private quietly is not.

Leave `DRIVE_LIBRARIES` empty unless you want the Drive pull. Empty means nothing is pulled, and the fetcher says so rather than guessing at folder names.

## 5. The browser stack

Install the units, adjusting the account name and paths if you changed them:

```
sudo install -o root -g root -m 644 -t /etc/systemd/system systemd/*
sudo systemctl daemon-reload
sudo systemctl enable --now agent-xvfb agent-browser
```

`agent-xvfb` is the display. `agent-browser` is the resident: one headed Chromium against the persisted profile, with DevTools on `127.0.0.1:9222` for the tools to attach to. The process is the session, which is the single most important sentence in this document.

The resident deliberately loads only `icloud.com` at startup and opens no app tab. Loading an app page is what asks Apple for the data-access grant, so a startup that opened app tabs would put approval prompts on your devices at any hour, and that is what makes an automatic restart on failure safe here.

Do not enable the VNC or noVNC units at boot. They have no `[Install]` section on purpose: they are started by hand when a login is needed and stopped afterwards.

The noVNC unit depends on the private network being up, because the only address it is meant to be reachable on is a private one. If you use a different private network, change that dependency to whatever brings yours up, and keep the unit bound to loopback behind it.

## 6. The first iCloud login, with two-factor

A fresh Chromium is not signed in, so this one step is interactive and needs you to be holding a device that receives Apple's code.

Start the login helper as the service account. It brings up a loopback-only VNC onto the same display the resident browser is on, so you sign into the running browser rather than into a new one:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-login.sh
```

Tunnel to it from your own machine. The VNC server binds loopback only, so this tunnel is the only way in, and that is the intended design:

```
ssh -N -L 5901:localhost:5900 your-host
```

Point a VNC viewer at `localhost:5901`, sign in with the Apple ID, and enter the two-factor code when Apple asks.

**Tick "Trust this browser".** Without it Apple never issues the remembered-browser cookie, and the session dies on the next relaunch no matter what else you do. This is the most common way a first setup fails, and it fails later rather than immediately, which is what makes it expensive.

When the session is up, stop the VNC server and close the tunnel. Leaving it running leaves a door open onto a signed-in Apple account.

## 7. Granting web access for Notes and Reminders

If your account has Advanced Data Protection enabled, the web gets only temporary access to some surfaces, and it has to be granted from a device you already trust.

Open the Notes app page and then the Reminders app page through the browser, which is what raises the request. Apple shows "Allow your iCloud data to be accessed via the web?" on your devices. Unlock the device, approve it, and go back to the page.

Approving once covers iCloud.com data rather than a single application, which is why the re-ask helper navigates one app rather than two: navigating both puts two notifications on the phone for every re-ask.

Until the grant is in place the apps sit on "Getting Access" and show nothing. The tools report that as `needs_device_approval` rather than as an empty list, so a refusal at this stage looks like a refusal and not like an empty account.

## 8. Verify each surface

First, prove the browser can reach iCloud data at all:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp session-check
```

It distinguishes the four answers that need four different fixes: healthy, signed out, needs a device approval, and no browser to talk to. Exit 0 is healthy.

Then prove the wrapper starts as the uid that will actually use it. It should start and exit silently on end of input:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
```

Then configure your agent runtime to spawn `stdio.sh`, with a timeout above 300 seconds, and call one tool per surface: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders` and `drive_status`. All five should answer. Anything less than all five is a setup that is not finished, and it is much cheaper to find that now than the first time you need it.

Between 23:00 through 06:59 owner-local (`AGENT_TZ`), a browser-backed call that would need a fresh app page load refuses instead of raising a prompt on your devices at night. An already-open tab still works. That is the quiet-hours rule doing its job, not a failure.

## 9. When the grant lapses later

It will, on its own schedule, and the symptom is that Notes and Reminders stop answering while mail and calendar are fine. The tools name it: `needs_device_approval`.

A latch records that you have already been asked, so nothing retries in a loop and no watchdog restarts anything, because retrying cannot produce a different answer until you approve. Clear it by running the re-ask helper when you are actually at a device, which re-navigates an app tab and raises the prompt again. Approve on the device and the surfaces come back.

Do not restart the browser to fix this. A restart discards the warm tabs, costs the warm-up, and raises an approval prompt of its own, so a restart meant to satisfy a prompt creates one. A navigation is enough and leaves the browser standing.

A genuinely expired session is the other failure and it looks different: `icloud-mcp session-check` reports signed out rather than needs approval. That one is interactive, and it is step 6 again, tunnel and all, including the "Trust this browser" tick.

Restarting the browser is a decision, never a side effect. Reloading unit files, moving these files and re-running the wrapper are all free. Restarting the browser or the display is not.

## 9b. Recovering without SSH, through the agent

Section 9 assumes you can reach the host. When the agent runs somewhere you cannot SSH from (a chat client on your phone, a scheduled run), the same two recoveries are tools, and the errors route between them: `needs_device_approval` means the grant lapsed and wants `reask_access`; `needs_login` means the session expired and wants `open_login`.

`reask_access` re-fires the prompt and clears the latch, but only when the latch is actually set, outside quiet hours, and the owner approves the ask. In a conversation, call it now and tell the owner to approve: the prompt lands within seconds. In a background run, do not call it and do not retry in a loop: report that access lapsed and the call timed out waiting, and reload only when the owner replies asking to try again.

`open_login` opens a supervised VNC door for 20 minutes and returns the link plus a one-time password in the tool result. The link uses the tailnet address when the host has Tailscale, and loopback otherwise (then you still need your own tunnel). Open the link, enter the password, sign in with the Apple ID and the two-factor code, tick "Trust this browser", approve the grant when it appears, and tell the agent to retry. The door processes exit at expiry and the record is swept on the next recovery-tool call, or earlier once the login is observed; the password is issued once and never stored in cleartext, so a lost password waits out the remaining minutes. Open the door only when the owner is there to click through, never speculatively and never from a scheduled run: anyone holding the link and the password drives the login screen.

## 10. Configure your agent runtime (Hermes or any MCP client) to spawn `stdio.sh`

That is the last step, and it is the only one this document cannot write for you. This server is a child process of the runtime that drives it, on this same machine, so what remains is telling that runtime to spawn `sudo -n -u <service account> /opt/agent-icloud/bin/stdio.sh` with a timeout above 300 seconds. Give the entry a name that is a valid identifier such as `icloud-headless-mcp`: a runtime that prefixes tool names with it needs one. Copy-paste entries for Hermes and OpenCode are in the README's `Connect a client` section; the same entry pattern on another host with its own install works the same way.

## 11. Optional: health extension

Skip this unless Apple Health data should be queryable. The extension reads a staged export folder, never the phone and never Drive directly.

Set the export directory to the staged folder (for example a HealthMirror app folder under `DRIVE_STAGING`), leaving `HEALTH_DB` at its default unless the state layout deviates:

```
HEALTH_EXPORT_DIR=/opt/agent-icloud/drive-staging/<dest>
```

Backfill once: save the official Apple export from agent chat into staging (beside the continuous-push folder, same layout) and import by hand to prove the plumbing:

```
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp health-import
sudo -u agent-icloud -H /opt/agent-icloud/bin/icloud-mcp health-import --self-test
```

The first prints per-file seen/new counts; the second runs the six contract checks with no files touched. Then enable the daily timer and confirm the tools answer (unconfigured until the first import lands, never zeros):

```
sudo systemctl enable --now agent-health-import.timer
```

The timer unit shipped with the rest of `systemd/`; enabling it is the only step the installer leaves to you, because an import schedule is a deployment choice. The Drive pull's own scheduling stays yours and stays separate.
