#!/usr/bin/env python3
"""Report whether the resident browser can actually reach iCloud data.

Two different things can be wrong, and they need different answers:

  SIGNED_OUT      the session is gone. Only a human signing in through VNC fixes it.
  NEEDS_APPROVAL  the session is fine but Apple's Advanced Data Protection grant for
                  iCloud.com has lapsed. A device approval fixes it, and then the page has
                  to re-request, which in practice means restarting the browser.

The cookie check alone reported OK for the second case, so the watchdog said "nothing needs
you" while notes and reminders were dead. The page text is the only signal for it.

Exit 0 healthy, 1 signed out, 2 no browser to talk to, 3 needs a device approval.
"""
import sys

from playwright.sync_api import sync_playwright

CDP = "http://127.0.0.1:9222"

# Apple words this several ways; matching one of them was how it went unnoticed.
BLOCKED = ("no response to the request", "Can't Display", "Can’t Display",
           "grant iCloud.com access", "Getting Access", "temporary access")


def main():
    try:
        with sync_playwright() as p:
            browser = p.chromium.connect_over_cdp(CDP, timeout=15_000)
            ctx = browser.contexts[0]
            names = {c["name"] for c in ctx.cookies()}
            if "X-APPLE-WEBAUTH-TOKEN" not in names:
                print(f"SIGNED_OUT (cookies present: {len(names)})")
                return 1

            # Only the app tabs matter. The home page renders fine either way.
            for page in ctx.pages:
                if "/reminders/" not in page.url and "/notes/" not in page.url:
                    continue
                # Every frame, not just the top document. Apple renders the app inside an
                # iframe, and after a restart the refusal appears there while the page body is
                # empty: this check reported OK while the MCP, which reads all frames, found the
                # app blocked. A health check that is blind to the failure is worse than none,
                # because the watchdog then says "nothing needs you".
                body = ""
                for f in [page] + list(page.frames):
                    try:
                        body += f.evaluate("() => document.body.innerText || ''") or ""
                    except Exception:
                        continue
                if any(s in body for s in BLOCKED):
                    which = "reminders" if "/reminders/" in page.url else "notes"
                    print(f"NEEDS_APPROVAL ({which} is blocked on a device approval)")
                    return 3
    except Exception as exc:
        print(f"UNREACHABLE {exc.__class__.__name__}")
        return 2

    print("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
