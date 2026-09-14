#!/usr/bin/env python3
"""iCloud session bootstrap, persistence, and probe.

login  - headed Chromium on the virtual display, for an interactive Apple login over VNC
check  - cold relaunch against the persisted session, reports whether it held
cookies- show what the jar currently holds

Two things had to be true before the session would survive, and both were learned the
hard way, one bootstrap each.

First, Chromium's own cookie store is not sufficient. launchPersistentContext does not
write session-scoped cookies to disk (Playwright issue 36139) and Apple's
X-APPLE-WEBAUTH-VALIDATE is session-scoped, so every cookie is mirrored into an explicit
jar on close and restored before anything navigates.

Second, the browser must run headed. Playwright's headless build is a different binary
reporting HeadlessChrome, and Apple ties the session to the user agent, so a headless
relaunch reads as a different browser and the session is invalidated server-side. Hence
the permanent Xvfb display rather than headless mode.

The jar and the profile are both live credentials and stay readable only by this user.
"""
import json
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from icloud_lib.icloud_tabs import STATE
from playwright.sync_api import sync_playwright

PROFILE = STATE / "profile"
JAR = STATE / "cookies.json"
LOGIN_TIMEOUT = 20 * 60

# Set only once the account is through 2FA. VALIDATE is the session-scoped one.
AUTH_COOKIES = {
    "X-APPLE-WEBAUTH-TOKEN",
    "X-APPLE-WEBAUTH-USER",
    "X-APPLE-DS-WEB-SESSION-TOKEN",
    "X-APPLE-WEBAUTH-VALIDATE",
}

# Mandatory on a small box: the default /dev/shm is 64MB and Chromium dies without this.
FLAGS = [
    "--disable-dev-shm-usage",
    "--disable-gpu",
    "--disable-extensions",
    "--disable-background-networking",
    "--disable-sync",
    "--disable-translate",
    "--no-first-run",
]


def open_ctx(p):
    """Launch against the profile and put the saved jar back before anything navigates."""
    ctx = p.chromium.launch_persistent_context(
        str(PROFILE),
        headless=False,  # never headless: see the module docstring
        args=FLAGS,
        viewport={"width": 1280, "height": 900},
    )
    if JAR.exists():
        try:
            saved = json.loads(JAR.read_text())
            ctx.add_cookies(saved)
            print(f"restored {len(saved)} cookies from the jar", flush=True)
        except Exception as exc:
            print(f"jar unreadable, ignoring: {exc}", flush=True)
    return ctx


def close_ctx(ctx):
    """Mirror every cookie out, session-scoped ones included, then shut down.

    A jar without auth cookies is worse than no write at all: it would overwrite a good
    jar with a dead session and force another interactive login. So only save a jar that
    still carries the tokens.
    """
    try:
        cookies = ctx.cookies()
        if any(c["name"] in AUTH_COOKIES for c in cookies):
            JAR.write_text(json.dumps(cookies, indent=1))
            JAR.chmod(0o600)
            print(f"saved {len(cookies)} cookies to the jar", flush=True)
        else:
            print("no auth cookies left, keeping the previous jar", flush=True)
    except Exception as exc:
        print(f"could not save the jar: {exc}", flush=True)
    ctx.close()


def auth_cookies(ctx):
    return [c for c in ctx.cookies() if c["name"] in AUTH_COOKIES]


def describe(cookies):
    for c in sorted(cookies, key=lambda c: c["name"]):
        exp = c.get("expires", -1)
        when = "session-scoped" if not exp or exp < 0 else datetime.fromtimestamp(
            exp, timezone.utc
        ).isoformat(timespec="seconds")
        print(f"  {c['name']:<30} expires={when}")


def signed_in(page):
    """Apple serves the same URL and title signed out; the Sign In control is the tell."""
    try:
        return "Sign In" not in page.inner_text("body")
    except Exception:
        return False


def login():
    with sync_playwright() as p:
        ctx = open_ctx(p)
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        page.goto("https://www.icloud.com/", timeout=120_000)
        print("Browser up on the virtual display. Connect over VNC and sign in.", flush=True)
        print("Tick 'Trust this browser' when Apple offers it. This exits on its own.", flush=True)

        deadline = time.time() + LOGIN_TIMEOUT
        while time.time() < deadline:
            time.sleep(5)
            found = auth_cookies(ctx)
            if len(found) >= 2:
                print(f"AUTHENTICATED, {len(found)} auth cookies present", flush=True)
                describe(found)
                time.sleep(15)  # let Apple finish writing cookies
                close_ctx(ctx)
                return 0
        print("TIMED OUT waiting for sign-in", flush=True)
        close_ctx(ctx)
        return 1


def check():
    """Cold relaunch, then judge by the page rather than by the URL.

    Same headed mode as the login, or Apple sees a different browser.
    """
    with sync_playwright() as p:
        ctx = open_ctx(p)
        describe(auth_cookies(ctx))
        page = ctx.pages[0] if ctx.pages else ctx.new_page()
        page.goto("https://www.icloud.com/reminders/", timeout=120_000)
        page.wait_for_timeout(20_000)
        held = signed_in(page)
        print(f"url:   {page.url}")
        print(f"title: {page.title()}")
        print("RESULT:", "SESSION HELD" if held else "SIGNED OUT, session did not persist")
        close_ctx(ctx)
        return 0 if held else 1


def cookies():
    if not JAR.exists():
        print("no jar yet")
        return 1
    saved = json.loads(JAR.read_text())
    auth = [c for c in saved if c["name"] in AUTH_COOKIES]
    print(f"jar holds {len(saved)} cookies, {len(auth)} of them auth")
    describe(auth)
    return 0 if auth else 1


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "check"
    fn = {"login": login, "check": check, "cookies": cookies}.get(cmd, check)
    sys.exit(fn())
