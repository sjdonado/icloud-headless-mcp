#!/usr/bin/env python3
"""Re-ask Apple for web access by re-navigating the app tabs, not by restarting the browser.

Apple's data-access grant for iCloud.com expires on its own, and the page keeps showing the old
refusal until it asks again. Until now the only way to make it ask was to restart the browser,
which throws away warm tabs, costs the warm-up, and made every recovery restart raise an approval
prompt as a side effect.

A navigation is enough and is survivable. Measured on 2026-08-21: `page.goto` to the same URL left
the browser and all three tabs intact, where `page.reload()` had killed Chromium outright on two
earlier occasions. So this re-navigates and nothing else.

The timezone override is re-applied first, because Apple's apps read the zone once at startup and
a fresh navigation is a fresh startup.
"""
import sys

from icloud_lib.icloud_tabs import clear_blocked, local_timezone       # noqa: E402
from playwright.sync_api import sync_playwright           # noqa: E402

CDP = "http://127.0.0.1:9222"
# One app, not two. Apple's grant is for iCloud.com data, not per application, so a single
# navigation raises a single prompt and approving it covers notes as well. Navigating both put two
# "iCloud Data Access Request" notifications on the owner's phone for every re-ask, and four when a
# re-ask was repeated.
APPS = ("https://www.icloud.com/reminders/",)


def main() -> int:
    try:
        with sync_playwright() as p:
            browser = p.chromium.connect_over_cdp(CDP, timeout=30_000)
            ctx = browser.contexts[0]
            zone = local_timezone()
            done = 0
            for url in APPS:
                page = next((x for x in ctx.pages if url.rstrip("/") in x.url), None)
                if page is None:
                    print(f"no tab for {url}, skipped")
                    continue
                try:
                    page.context.new_cdp_session(page).send(
                        "Emulation.setTimezoneOverride", {"timezoneId": zone})
                except Exception:
                    pass
                try:
                    # goto, never reload: reload kills the browser, this does not.
                    page.goto(url, timeout=90_000, wait_until="domcontentloaded")
                    page.wait_for_timeout(8_000)
                    try:
                        page.evaluate("() => { window.__agentTzApplied = true; }")
                    except Exception:
                        pass
                    print(f"re-asked {url}")
                    done += 1
                except Exception as exc:
                    print(f"{url}: {exc.__class__.__name__}", file=sys.stderr)
            if done:
                # Release the latch so the next real call can find out whether the owner approved.
                # This is the only place that clears it on purpose: a re-ask is you saying you are there.
                clear_blocked()
                print("latch cleared, iCloud will be tried again")
            return 0 if done else 1
    except Exception as exc:
        print(f"could not reach the browser: {exc.__class__.__name__}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
