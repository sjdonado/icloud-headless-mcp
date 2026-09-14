#!/usr/bin/env python3
"""The resident iCloud browser.

Measured on this box: an iCloud web session does not survive a Chromium relaunch. Apple
never issues X-APPLE-WEBAUTH-TOKEN here, so the only cookies on offer die with the browser
session, and Apple wipes them server-side on the next load. Two fixes were tried and kept
(an explicit cookie jar for issue 36139, and headed mode because Apple binds the session to
the user agent), and neither is sufficient on its own.

So the process is the session. This launches one headed Chromium against the persisted
profile, exposes CDP on loopback for tools to attach to, and stays up.

What a restart costs was misjudged for a long time and is worth stating exactly. It does **not**
cost an interactive login: the profile persists that, measured repeatedly. It costs an **approval
prompt**, because Apple's data-access grant is re-requested the next time an app page loads, and
those prompts expire within minutes. So a restart nobody is awake for spends something for
nothing, which is why nothing recycles this on a schedule or on memory pressure, and why the app
tabs are no longer loaded at startup.

Tools attach with playwright.chromium.connect_over_cdp("http://127.0.0.1:9222").
"""
import json
import signal
import sys
import time
from pathlib import Path

from icloud_lib.icloud_tabs import STATE
from playwright.sync_api import sync_playwright

PROFILE = STATE / "profile"
JAR = STATE / "cookies.json"
CDP_PORT = 9222
JAR_INTERVAL = 300

FLAGS = [
    # Chromium's own diagnosis of its own death, which until now went nowhere: a wedge left only
    # "UNREACHABLE TimeoutError" in the watchdog's message and nothing at all from the browser.
    # Level 1 is warnings and errors, not the firehose, and stderr lands in the unit's journal
    # next to everything else about this service.
    # A file, not stderr. Chromium's stderr is consumed by the Playwright driver, which is why
    # --enable-logging=stderr produced nothing in the journal even as it crashed: the fatal CHECK
    # message that precedes every one of these traps was being swallowed. Every crash so far is
    # SIGTRAP at the same offset, chrome + 0x7b6decc, which is a deliberate abort at one specific
    # place, and the CHECK text is the only thing that names it without symbols.
    "--enable-logging",
    f"--log-file={STATE / 'chrome.log'}",
    "--log-level=1",
    "--disable-dev-shm-usage",  # /dev/shm is 64MB on a small VM; Chromium dies without this
    "--disable-gpu",
    "--disable-extensions",
    "--disable-background-networking",
    "--disable-sync",
    "--disable-translate",
    "--no-first-run",
    f"--remote-debugging-port={CDP_PORT}",
    "--remote-debugging-address=127.0.0.1",
]

running = True

# Consecutive failed liveness checks, five seconds apart, before the browser is declared gone.
DEAD_LIMIT = 4


def stop(signum, frame):
    global running
    running = False


def save_jar(ctx):
    """Only ever overwrite the jar with a session that still carries auth cookies."""
    try:
        cookies = ctx.cookies()
        if any(c["name"].startswith("X-APPLE-WEBAUTH") for c in cookies):
            JAR.write_text(json.dumps(cookies, indent=1))
            JAR.chmod(0o600)
            return len(cookies)
    except Exception as exc:
        print(f"jar save failed: {exc}", flush=True)
    return 0


def main():
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)

    with sync_playwright() as p:
        ctx = p.chromium.launch_persistent_context(
            str(PROFILE),
            headless=False,  # Apple ties the session to the user agent
            args=FLAGS,
            viewport={"width": 1280, "height": 900},
        )
        if JAR.exists():
            try:
                ctx.add_cookies(json.loads(JAR.read_text()))
                print("restored jar", flush=True)
            except Exception as exc:
                print(f"jar unreadable: {exc}", flush=True)

        # Every crash dump so far is a **renderer** trapping, not the browser process: 12 to 14
        # threads, the same offset each time, while the other ten processes carry on. A dead
        # renderer takes CDP down with it, which is why this looked like a browser-wide wedge.
        #
        # So handle the tab rather than the process. The crashed page is closed, not reloaded:
        # reloading an iCloud app page re-asks Apple for access, and at 05:37 nobody is there to
        # approve it. The next real call opens a fresh tab, subject to the quiet-hours rule.
        def _on_crash(crashed):
            try:
                print(f"renderer crashed on {crashed.url[:60]}, closing that tab", flush=True)
                crashed.close()
            except Exception as exc:
                print(f"could not close the crashed tab: {exc.__class__.__name__}", flush=True)

        ctx.on("page", lambda pg: pg.on("crash", _on_crash))
        for existing in ctx.pages:
            existing.on("crash", _on_crash)

        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        page.on("crash", _on_crash)
        page.goto("https://www.icloud.com/", timeout=120_000)

        # Deliberately no app-tab warm-up.
        #
        # Warming was added because a cold Reminders tab reported 4 of 5 lists: readiness fires
        # before the data arrives. That correctness problem is now solved where it belongs, in
        # icloud_app, which waits for the collection to stop growing on any tab it loaded itself.
        #
        # What warming also did was **ask Apple for access twice on every start**. Loading an
        # iCloud app page re-requests the data-access grant, so each restart put two "Allow your
        # iCloud data to be accessed via the web?" prompts on the owner's devices. A restart at
        # 03:00 spends prompts that expire long before the owner wakes, and a crash-recovery
        # restart is exactly when nobody is watching. The tabs are now loaded on first real use instead, which is
        # when the owner is present to approve.
        print(f"resident browser up, CDP on 127.0.0.1:{CDP_PORT}", flush=True)

        last = 0
        dead_checks = 0
        while running:
            time.sleep(5)

            # Is the browser still there? Without this the unit lied for two hours: Chromium
            # exited around 01:49 on 2026-08-22 and this loop kept printing "jar: 14 cookies"
            # until 03:38 while the process table held no chrome at all. Exiting non-zero makes
            # the death visible in seconds instead of at the next attempt to use it.
            #
            # Only on repeated failure, though. A single failed evaluate is not death: a tab
            # mid-navigation raises too, and treating that as fatal made the resident exit moments
            # after a healthy start, which is worse than the blindness it replaced.
            #
            # `ctx.pages` alone is not enough either: it kept returning pages, and cookies kept
            # saving, for two hours after Chromium had actually gone. Only a call that has to reach
            # the process tells the truth.
            why = ""
            try:
                pages = ctx.pages
                if not pages:
                    # Genuinely empty. Closing a crashed app tab can leave only the home tab, and
                    # that is fine; zero pages means the process is gone.
                    # An empty list is a dead browser, not a quiet one: this context always
                    # holds at least the home tab. Treating empty as success reset the counter on
                    # every check and the death was never reached.
                    why = "no pages left"
                else:
                    pages[0].evaluate("() => 1")
            except Exception as exc:
                why = exc.__class__.__name__
            if why:
                dead_checks += 1
                print(f"browser did not answer ({why}), {dead_checks} of {DEAD_LIMIT}", flush=True)
                if dead_checks >= DEAD_LIMIT:
                    print("browser is gone, exiting so the failure is visible", flush=True)
                    return 1
            else:
                dead_checks = 0

            if time.time() - last > JAR_INTERVAL:
                n = save_jar(ctx)
                if n:
                    print(f"jar: {n} cookies", flush=True)
                last = time.time()

        print("shutting down, saving jar", flush=True)
        save_jar(ctx)
        ctx.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
