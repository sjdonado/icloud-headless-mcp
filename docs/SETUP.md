# First run, on a fresh Linux box

This is the whole sequence for somebody who has never run this before: prerequisites, the service account, the environment file, the browser stack, the first Apple login with two-factor, the Advanced Data Protection grant, and how to check each surface actually answers. It ends with what to do months later when Apple's grant lapses, because it will.

Read it once before starting. Two steps in the middle need you to be holding an Apple device, and finding that out halfway through is annoying.

The systemd units in `systemd/` are the worked example throughout. They are the units from a real deployment, with the paths and the account name that deployment used. Nothing forces you to use those names: they are install-time choices, and if you change them you change them in the units, in the wrapper and in the sudoers rules together.

## Minimum requirements, plainly

A Linux machine that is always on, with a Python 3.11 or newer, that you can reach over SSH. A small VPS is enough. A Raspberry Pi is enough. Neither needs a screen.

64-bit. Playwright's Chromium download does not cover every architecture, so on an ARM board check that `playwright install chromium` actually produces a binary before you go further, and fall back to the distribution's own Chromium, passing its path to Playwright's launch call as the executable path, if it does not.

Disk is modest: the code is small, the Python environment and a Chromium build are the bulk of it, and the browser profile grows slowly. Leave room for the staging directory if you use the Drive pull.

The real cost is Chromium's memory, and it is the only requirement worth measuring rather than quoting. One headed Chromium holding an iCloud session, plus an app tab or two, is the whole footprint of this system. Measure it on your own box: start the resident, open the tabs you actually use, and read the resident set size of the browser process tree. Give the machine swap before you decide it is too small, and if you are tight on memory, let the tab reaper close idle tabs aggressively, because a warm tab is a convenience and not a requirement.

Do not run this on a machine with no network privacy or with other people's shells on it. It holds a logged-in Apple session. See [`../SECURITY.md`](../SECURITY.md).

The server itself runs on the same machine as the agent runtime that drives it, as a child process over stdio. There is nothing to expose and nothing to reach across a network.

## 0. A private network, before anything else

Set up Tailscale, or an equivalent WireGuard-style private network, between your own machine and this host before you start the browser stack. Do this first, not later.

The reason is step 6. The first login is you typing an Apple ID, a password and a two-factor code into a browser on a machine with no screen, reached through a remote desktop, and what you leave behind is a browser holding a logged-in Apple account. Neither VNC nor noVNC may ever be reachable on a public interface, at any point, including while you are setting it up.

The reference deployment uses Tailscale: the noVNC unit in `systemd/` requires `tailscaled.service` and is reachable only over that tailnet, which is why the dependency is in the unit rather than being an accident of that box. Any other private network you control works the same way, and so does the plain SSH tunnel used in step 6, which is the smallest version of the same idea. What is not an option is opening the port.

## 1. Packages

```
sudo apt update
sudo apt install python3 python3-venv xvfb x11vnc novnc websockify
```

`xvfb` is the virtual display, because Chromium must run headed: Apple binds the session to the user agent and the headless build reports a different one, so a headless relaunch reads as a different browser and the session dies server-side. `x11vnc` and `novnc` exist only so a human can complete Apple's prompts once.

## 2. A service account of its own

This account owns the session and the credentials and nothing else. Nothing else should run as it, and it should not be the account your MCP client runs as.

```
sudo useradd -r -m -d /opt/agent-icloud -s /usr/sbin/nologin agent-icloud
sudo install -d -o agent-icloud -g agent-icloud -m 700 /opt/agent-icloud/bin
```
The account name and the path above are a worked example, and they are the ones the shipped systemd units, `stdio.sh` and the sudoers rules already carry. Change them if you like, but change them in all four places together, or the units will start a server that is not there.

Copy this directory's code into that `bin` directory, keeping `tools/` and `icloud_lib/` as subdirectories, owned by the service account and not writable by anybody else. The README's install block is one worked example of exactly that.

## 3. The virtual environment and Chromium

```
sudo -u agent-icloud -H bash -c 'cd /opt/agent-icloud && python3 -m venv .venv && .venv/bin/pip install -r bin/requirements.txt'
sudo install -d -o root -g root -m 755 /opt/playwright
sudo PLAYWRIGHT_BROWSERS_PATH=/opt/playwright /opt/agent-icloud/.venv/bin/playwright install chromium
```

A shared root-owned browser root rather than a per-account copy, so the service account cannot modify the binary it runs.

## 4. The environment file

Copy `.env.example`, fill it in, and install it readable only by the service account:

```
sudo install -o agent-icloud -g agent-icloud -m 400 .env /etc/agent/icloud.env
```

Two values need thought before you type them.

`ICLOUD_APP_PASSWORD` is an app-specific password from the Apple ID account page, not your primary Apple password. It authenticates CalDAV, CardDAV, IMAP and SMTP, and it is revocable on its own without touching anything else, which is what you want on the day something goes wrong.

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
sudo -u agent-icloud -H /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/session_check.py
```

It distinguishes the four answers that need four different fixes: healthy, signed out, needs a device approval, and no browser to talk to. Exit 0 is healthy.

Then prove the wrapper starts as the uid that will actually use it. It should start and exit silently on end of input:

```
sudo -n -u agent-icloud /opt/agent-icloud/bin/stdio.sh </dev/null
```

Then configure your agent runtime to spawn `stdio.sh`, with a timeout above 300 seconds, and call one tool per surface: `list_calendars`, `list_mail`, `reminder_lists`, `notes_folders` and `drive_status`. All five should answer. Anything less than all five is a setup that is not finished, and it is much cheaper to find that now than the first time you need it.

Between 23:00 and 07:00 local, a browser-backed call that would need a fresh app page load refuses instead of raising a prompt on your devices at night. An already-open tab still works. That is the quiet-hours rule doing its job, not a failure.

## 9. When the grant lapses later

It will, on its own schedule, and the symptom is that Notes and Reminders stop answering while mail and calendar are fine. The tools name it: `needs_device_approval`.

A latch records that you have already been asked, so nothing retries in a loop and no watchdog restarts anything, because retrying cannot produce a different answer until you approve. Clear it by running the re-ask helper when you are actually at a device, which re-navigates an app tab and raises the prompt again. Approve on the device and the surfaces come back.

Do not restart the browser to fix this. A restart discards the warm tabs, costs the warm-up, and raises an approval prompt of its own, so a restart meant to satisfy a prompt creates one. A navigation is enough and leaves the browser standing.

A genuinely expired session is the other failure and it looks different: `session_check.py` reports signed out rather than needs approval. That one is interactive, and it is step 6 again, tunnel and all, including the "Trust this browser" tick.

Restarting the browser is a decision, never a side effect. Reloading unit files, moving these files and re-running the wrapper are all free. Restarting the browser or the display is not.

## 10. Configure your agent runtime to spawn `stdio.sh`

That is the last step, and it is the only one this document cannot write for you. This server is a child process of the runtime that drives it, on this same machine, so what remains is telling that runtime to spawn `sudo -n -u <service account> /opt/agent-icloud/bin/stdio.sh` with a timeout above 300 seconds. Where that is written down, and what the entry is called, belongs to the runtime's own configuration and its own documentation. Give the entry a name that is a valid identifier: a runtime that prefixes tool names with it needs one.
