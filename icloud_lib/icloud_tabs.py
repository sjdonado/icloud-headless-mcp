"""One warm tab per iCloud app in the resident browser.

Loading an iCloud web app costs twenty to forty seconds. Sharing a single tab between Notes
and Reminders would pay that on every switch, so each app keeps its own tab and each stays
where it was left.

The browser is still one process, so operations are serialised by a per-app lock: two calls
into the same app must not interleave clicks, while Notes and Reminders can proceed
independently.
"""
import json
import os
import time
from datetime import datetime
from zoneinfo import ZoneInfo
from contextlib import contextmanager
from fcntl import LOCK_EX, LOCK_NB, LOCK_UN, flock
from pathlib import Path

from playwright.sync_api import sync_playwright

CDP = os.environ.get("ICLOUD_CDP", "http://127.0.0.1:9222")
# Everything this account writes for itself: the per-app locks, the browser profile, the cookie
# jar, the tab-use stamps and the Drive staging tree. Under the running account's home by
# default, which is where a fresh install works with no env file at all.
STATE = Path(os.environ.get("ICLOUD_STATE", Path.home()))
LOCK_DIR = STATE
STATE_DIR = STATE / "state"
READY_TIMEOUT = 90
LOCK_TIMEOUT = 150


class SignedOut(RuntimeError):
    """The browser no longer holds an iCloud session, so nothing can be read or written.

    Worth its own type because the answer differs from every other failure: no retry helps and no
    restart helps, only a human signing in through VNC. Raised before any navigation, since a
    signed-out page still renders an app-shaped shell that accepts clicks and silently discards
    them, which is how a reminder came back "created" at the wrong day and time.
    """


class NeedsApproval(RuntimeError):
    """Apple's temporary web access has lapsed and only a human can restore it."""


def _touch(app: str) -> None:
    """Record that this app was used, so the reaper knows what is still warm."""
    try:
        STATE_DIR.mkdir(parents=True, exist_ok=True)
        (STATE_DIR / f"{app.lower()}.last").touch()
    except Exception:
        # Never fail a user-facing call because a bookkeeping file could not be written.
        pass


@contextmanager
def app_lock(app: str):
    """Serialise operations within one app, without blocking the other."""
    path = LOCK_DIR / f"{app}.lock"
    path.touch(exist_ok=True)
    with path.open("r+") as handle:
        deadline = time.time() + LOCK_TIMEOUT
        while True:
            try:
                flock(handle, LOCK_EX | LOCK_NB)
                break
            except BlockingIOError:
                if time.time() > deadline:
                    raise RuntimeError(f"another {app} operation is still running")
                time.sleep(1)
        try:
            yield
        finally:
            flock(handle, LOCK_UN)


def collect_js() -> str:
    """Return elements matching an exact class token, from every open shadow root.

    Both traversals are needed: the apps nest shadow roots several levels deep, and
    substring class matching catches wrappers and buttons that repeat every entry.
    """
    return r"""(cls) => {
      const found = [];
      const walk = (root, depth) => {
        if (depth > 14) return;
        for (const el of root.querySelectorAll('*')) {
          if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
          if ((el.className || '').toString().split(/\s+/).includes(cls)) found.push(el);
        }
      };
      walk(document, 0);
      return found.map(el => (el.innerText || '').trim());
    }"""


def click_js() -> str:
    """Click the nth element with a class token, using real pointer events.

    A bare .click() is ignored by these apps: the detail pane stays on "No selection" while
    the call appears to succeed. The lists are virtualised too, so a Playwright mouse click
    lands outside the viewport.
    """
    return r"""([cls, index]) => {
      const found = [];
      const walk = (root, depth) => {
        if (depth > 14) return;
        for (const el of root.querySelectorAll('*')) {
          if (el.shadowRoot) walk(el.shadowRoot, depth + 1);
          if ((el.className || '').toString().split(/\s+/).includes(cls)) found.push(el);
        }
      };
      walk(document, 0);
      const target = found[index];
      if (!target) return false;
      target.scrollIntoView({block: 'center'});
      const box = target.getBoundingClientRect();
      const x = box.left + box.width / 2, y = box.top + box.height / 2;
      const fire = (type, Ctor) => target.dispatchEvent(new Ctor(type, {
        bubbles: true, cancelable: true, composed: true, clientX: x, clientY: y, button: 0,
        pointerId: 1, isPrimary: true,
      }));
      fire('pointerdown', PointerEvent);
      fire('mousedown', MouseEvent);
      fire('pointerup', PointerEvent);
      fire('mouseup', MouseEvent);
      fire('click', MouseEvent);
      return true;
    }"""


# Where the owner is, and therefore what "08:00" means.
#
# The box runs Etc/UTC, so the browser page reports UTC and Apple's own pickers read and write in
# that zone: typing 08:00 into the Reminders time picker stored 08:00 UTC, which the owner's devices
# showed as "Tomorrow, 10:00 (08:00 GMT)". Two hours wrong, and invisible from the server side,
# because nothing disagrees: CalDAV and the browser simply operate in different zones.
#
# CDP overrides the page's zone without restarting Chromium, which matters because a restart costs
# an interactive Apple login. Applied per connection, read from a file you can change when you
# travel, so everything typed into an Apple picker is in the owner's local time.
# A file rather than a variable, because the owner's zone changes when the owner travels and a
# restart is not the moment to find out. Whatever writes it needs no access to this account's
# home, which is why the path is outside it by default. `AGENT_TZ` is the fallback, and it is
# required by the server, so there is no city hardcoded anywhere.
_TZ_FILE = os.environ.get("AGENT_TZ_FILE", "/etc/agent/timezone")


_NOTICE_DIR = os.environ.get("HOME", "/tmp")


# State two uids share: this account writes the latch, the ask log and the pending-write queue,
# and a watchdog running as somebody else reads them. So it is deliberately not under this
# account's home, and it is named by the environment rather than assumed.
SHARED_STATE = Path(os.environ.get("ICLOUD_SHARED_STATE", Path.home() / "shared-state"))

BLOCKED_LATCH = str(SHARED_STATE / "blocked")

ASKING_STAMP = str(SHARED_STATE / "asking")

ASKING_LOG = str(SHARED_STATE / "asks.log")


def log_ask(kind: str, app: str, outcome: str) -> None:
    """Record that something asked the owner for iCloud web access, or would have.

    The owner asked how often this happens per day, and until now nothing knew. The rate limit hid the
    answer: it suppressed messages without counting them, so a quiet afternoon and an afternoon of
    thirty suppressed asks looked identical from outside.

    Suppressed calls are logged too, and that is the point rather than a detail. The message count
    is what reaches the owner; the call count is the pressure behind it, and the gap between the two is
    the thing worth watching. Ten suppressed asks an hour means something is reloading pages in a
    loop, which is invisible if only the messages are counted.

    Append-only, one line, and never a reason to fail a caller: this is telemetry about asking for
    access, and it must not become a way to break reading the owner's data.
    """
    try:
        os.makedirs(os.path.dirname(ASKING_LOG), exist_ok=True)
        new = not os.path.exists(ASKING_LOG)
        with open(ASKING_LOG, "a") as fh:
            fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')}\t{kind}\t{app}\t{outcome}\n")
        if new:
            os.chmod(ASKING_LOG, 0o664)
    except OSError:
        pass


def notify_asking(app: str, notify) -> None:
    """Tell the owner a page is about to ask Apple for access, before it asks.

    The owner's rule, and it is the right one: the owner must never see "Allow your iCloud data
    via the web?" without knowing where it came from. Loading a fresh iCloud page is the only
    thing that raises that prompt, so this fires at the load rather than after a refusal is
    detected. A load that then times out never reaches the detection, which is exactly how a
    prompt arrived out of nowhere while the job reported "the Reminders app timed out".

    Rate limited to one message per fifteen minutes, so a burst of crashed tabs reloading does not
    become a burst of messages.
    """
    if notify is None:
        log_ask("page-load", app, "no-notifier")
        return
    try:
        if (os.path.exists(ASKING_STAMP)
                and time.time() - os.path.getmtime(ASKING_STAMP) < 900):
            log_ask("page-load", app, "suppressed")
            return
        os.makedirs(os.path.dirname(ASKING_STAMP), exist_ok=True)
        with open(ASKING_STAMP, "w") as fh:
            fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} {app}\n")
    except OSError:
        pass
    log_ask("page-load", app, "sent")
    notify(f"I am opening your iCloud {app} page, which makes Apple ask permission. If your "
           f"Mac or iPhone shows \"Allow your iCloud data to be accessed via the web?\", that is "
           f"me, and tapping Allow Access is what lets me read it. If you miss it, nothing breaks: "
           f"I will tell you it is blocked and you can ask me to try again.")


# Apple words a lapsed access grant several ways and only one of them used to be matched, so a
# lapse surfaced as "the app did not load in time": a timeout the owner can do nothing with, instead
# one message that leads to a fix. Seen on 2026-08-20 as "Can't Display Your Reminders / There was
# no response to the request sent to your devices." Shared, because notes_mcp.py kept its own
# two-phrase copy of this list and therefore failed to recognise four of the six.
NEEDS_DEVICE = ("Getting Access", "temporary access", "no response to the request",
                "Can't Display", "Can’t Display", "grant iCloud.com access")

QUIET_FROM, QUIET_UNTIL = 23, 7          # local hours, inclusive of the start


def quiet_hours() -> bool:
    """Is it the middle of the owner's night, in the zone the owner is standing in?

    Loading an iCloud app page re-requests Apple's data-access grant, which puts a prompt on the
    owner's devices that expires within minutes. At 04:00 that is guaranteed waste: the owner wakes
    to a dead prompt and a job that failed anyway. So a *fresh* load is deferred until morning. An
    already loaded tab is untouched by this, so reading notes and reminders overnight still works.
    """
    hour = datetime.now(ZoneInfo(local_timezone())).hour
    return hour >= QUIET_FROM or hour < QUIET_UNTIL


def blocked() -> bool:
    """Is iCloud latched as needing the owner's approval?

    One latch, shared by every path. Without it each caller kept its own idea of "I already told
    the owner": the MCP servers, the watchdog's approval branch, its wedge recovery, and the cron
    jobs each announced the same lapse, and the owner woke to six requests to allow access. Rate
    limiting each voice separately cannot fix that; there has to be one fact.
    """
    return os.path.exists(BLOCKED_LATCH)


def latch_blocked(reason: str = "") -> bool:
    """Mark iCloud unavailable. True if this call is what set it, so only that caller speaks."""
    if blocked():
        return False
    try:
        os.makedirs(os.path.dirname(BLOCKED_LATCH), exist_ok=True)
        with open(BLOCKED_LATCH, "w") as fh:
            fh.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} {reason}\n")
        return True
    except OSError:
        return True          # cannot latch, so speak rather than swallow the problem


def clear_blocked() -> None:
    """Release the latch. Only a proven-working surface or an explicit re-ask should do this."""
    try:
        os.remove(BLOCKED_LATCH)
    except OSError:
        pass


def _notice_due(kind: str, hours: int = 6) -> bool:
    """True at most once per `hours` for this kind of notice.

    The same "waiting for you to approve" message arrived every thirty minutes for hours, once per
    cron run, while every request had already been approved. A message the owner cannot act on,
    repeated, is worse than silence: it trains the owner to ignore the channel that carries the ones
    the owner can act on.
    """
    stamp = os.path.join(_NOTICE_DIR, f".notice-{kind}")
    try:
        if os.path.exists(stamp) and time.time() - os.path.getmtime(stamp) < hours * 3600:
            return False
        with open(stamp, "w") as fh:
            fh.write("")
        return True
    except OSError:
        return True          # cannot track it, so err towards telling the owner


def local_timezone() -> str:
    """The owner's current zone: whatever the file holds, else what the environment says.

    The file wins because it is the thing that changes when the owner travels, and a restart is
    not the moment to find that out. Neither existing raises rather than guessing: a zone guessed
    here types the wrong hour into a picker and nothing errors.
    """
    try:
        with open(_TZ_FILE) as fh:
            name = fh.read().strip()
        if name:
            return name
    except OSError:
        pass
    zone = os.environ.get("AGENT_TZ")
    if not zone:
        raise RuntimeError(f"no timezone: {_TZ_FILE} is unreadable or empty and AGENT_TZ is "
                           f"unset, so nothing here can say what a wall-clock time means")
    return zone


def session_ok(ctx) -> bool:
    """Is there still a usable iCloud session? Checked by cookie, costing no network."""
    try:
        return "X-APPLE-WEBAUTH-TOKEN" in {c["name"] for c in ctx.cookies()}
    except Exception:
        return True          # a cookie read that fails is not evidence of being signed out


def _apply_timezone(page) -> None:
    """Make the page believe it is in the owner's zone, and reload once if it loaded before that.

    The override has to be in place **before the app loads**. Apple's web apps read the zone at
    startup and keep it: a tab opened while the box was UTC kept writing UTC even after
    `Emulation.setTimezoneOverride` made `Intl` report the right one. Reloading with the override
    already applied fixed it, and afterwards a typed 08:00 stored 08:00 local.

    A marker on the window means each warm tab reloads at most once per lifetime, which matters
    because these tabs are deliberately long-lived and a reload can cost Apple's temporary-access
    grant.
    """
    try:
        session = page.context.new_cdp_session(page)
        session.send("Emulation.setTimezoneOverride", {"timezoneId": local_timezone()})
    except Exception:
        return
    # Deliberately no reload. Reloading an iCloud app tab reliably kills Chromium, twice measured,
    # and because every relaunch produced a fresh unmarked tab this turned into a restart loop:
    # warm the tabs, reload one, crash, recover, repeat. Eleven browser starts in a day.
    #
    # The tab is loaded with the zone already in place by icloud_resident, and tabs this module
    # opens itself get the override before `goto`. So there is nothing left for a reload to fix.
    try:
        page.evaluate("() => { window.__agentTzApplied = true; }")
    except Exception:
        pass


@contextmanager
def icloud_app(app: str, url: str, frame_hint: str, ready_class: str, notify=None):
    """Hand back the application frame for one iCloud app, on that app's own tab.

    app         short name, used for the lock and for choosing the tab
    url         where the app lives
    frame_hint  substring identifying the application iframe
    ready_class exact class token that only appears once the app has real content
    notify      optional, and no tool passes one. Kept for a non-MCP helper that has a channel of
                its own; an MCP server has none, so the text a caller needs is carried in the
                exception message and reaches the owner as part of the tool result.
    """
    with sync_playwright() as p:
        browser = p.chromium.connect_over_cdp(CDP, timeout=20_000)
        ctx = browser.contexts[0]

        # Before touching a tab. A reload against an expired grant is what turns "probably still
        # signed in" into "definitely signed out", and driving the signed-out shell writes rubbish.
        if not session_ok(ctx):
            if notify:
                notify(
                    f"I cannot reach your iCloud {app}: the browser session has expired. "
                    f"Notes and reminders are unavailable until you sign in again through VNC. "
                    f"Calendar, contacts and mail are unaffected; they do not use the browser."
                )
            raise SignedOut(
                f"the iCloud browser session has expired, so {app} cannot be read or changed. "
                f"Nothing was read or written. Notes and reminders stay unavailable until a "
                f"human signs the browser in again; calendar, contacts and mail are unaffected, "
                f"because they do not use the browser."
            )

        # Latched: refuse before touching anything. Retrying cannot produce a different answer
        # until the owner approves, and every attempt cost 90 seconds of app-load timeout and produced
        # another "could not verify reminders" line in whatever job was running.
        if blocked():
            raise NeedsApproval(
                f"iCloud {app} is waiting for your approval, so it is unavailable. You have "
                f"already been asked; nothing further will be tried until you release it. "
                f"Nothing was read or changed."
            )

        mine = [pg for pg in ctx.pages if url.rstrip("/") in pg.url]
        # A previous call may have left a duplicate; keep the newest.
        for stale in mine[:-1]:
            try:
                stale.close()
            except Exception:
                pass
        page = mine[-1] if mine else ctx.new_page()
        opened_here = not mine
        _apply_timezone(page)

        try:
            navigated = opened_here or url.rstrip("/") not in page.url
            if navigated and quiet_hours():
                # Refuse rather than ask. This is the only place that would raise an approval
                # prompt overnight, and a prompt the owner is asleep for is worse than a missed
                # check: it expires, and the owner wakes to several of them plus the failures that
                # followed.
                raise NeedsApproval(
                    f"iCloud {app} would have to be loaded fresh, which asks Apple for approval "
                    f"on your devices, and it is the middle of the night. Deferred until morning "
                    f"rather than spending a prompt you cannot answer. Nothing was read."
                )
            if navigated:
                # Before the navigation, not after: the prompt appears the moment the page loads.
                notify_asking(app, notify)
                page.goto(url, timeout=120_000)

            deadline = time.time() + READY_TIMEOUT
            while time.time() < deadline:
                frame = next((f for f in page.frames if frame_hint in f.url), None)
                if frame is not None:
                    try:
                        ready = frame.evaluate(collect_js(), ready_class)
                    except Exception:
                        ready = None
                    if ready:
                        # `ready_class` appearing means the shell rendered, not that the data
                        # arrived. On a cold tab this returned 4 of 5 reminder lists: a wrong
                        # answer that looks like a right one, which is the failure this whole
                        # design exists to avoid. So when we loaded the tab ourselves, wait for
                        # the count to stop growing before handing it over. A warm tab, which is
                        # the normal case, skips this entirely and stays sub-second.
                        if navigated:
                            for _ in range(8):
                                page.wait_for_timeout(1_500)
                                again = frame.evaluate(collect_js(), ready_class)
                                if again is not None and len(again) <= len(ready):
                                    break
                                ready = again
                        _touch(app)
                        yield frame, page
                        return
                page.wait_for_timeout(3_000)

            # Distinguish "needs approval" from "broken": the answer to the owner differs.
            body = " ".join(f.evaluate("() => document.body.innerText || ''")
                            for f in page.frames)
            # Apple words this several ways and only one of them was matched, so a lapsed access
            # grant surfaced as "the app did not load in time": a timeout the owner can do nothing with,
            # instead of the one message that leads to a fix. Seen on 2026-08-20 as "Can't Display
            # Your Reminders / There was no response to the request sent to your devices."
            if any(s in body for s in NEEDS_DEVICE):
                first = latch_blocked(f"{app} reported a lapsed grant")
                if notify and first:
                    notify(
                        f"I cannot reach your iCloud {app}: Apple is waiting for you to "
                        f"approve access on one of your devices. Unlock your iPhone or Mac and "
                        f"approve the iCloud.com request, then ask me again. Nothing was read or "
                        f"changed, and this is not a sign-in problem: the session is fine, it is "
                        f"the data-access grant that lapsed."
                    )
                # The text the owner needs is the message, because a tool result is the only
                # way out of this process: nothing here has a channel of its own.
                raise NeedsApproval(
                    f"iCloud {app} needs device approval, so nothing was read or changed. Apple "
                    f"is waiting for approval on one of the account's own devices: unlock an "
                    f"iPhone or Mac, approve the iCloud.com request, and ask again. This is not "
                    f"a sign-in problem; the session is fine and it is the data-access grant "
                    f"that lapsed."
                )
            if navigated:
                first = latch_blocked(f"{app} did not become ready after a fresh load")
                if notify and first:
                    notify(
                        f"Your iCloud {app} page would not finish loading, which almost "
                        f"always means Apple is waiting for you to allow web access on one of "
                        f"your devices. Nothing was read or changed. Reply \"ask Apple again\" "
                        f"when you are at your Mac or iPhone and I will re-request it."
                    )
                raise NeedsApproval(
                    f"iCloud {app} did not finish loading after a fresh navigation, which is "
                    f"almost always Apple waiting for web access to be allowed on one of the "
                    f"account's own devices. Nothing was read or changed. Ask for the access "
                    f"request to be sent again from an iPhone or Mac, then try once more."
                )
            raise RuntimeError(f"the {app} app did not load in time")
        except Exception:
            if opened_here:
                try:
                    page.close()
                except Exception:
                    pass
            raise


# iCloud's own error alerts are modal and block every click underneath them, silently. On
# 2026-08-17 an "Unable to Delete Reminder — server error" alert sat over the Reminders app for
# an hour: the add button reported a successful click and created nothing, the DOM showed a
# perfectly healthy list, and only a screenshot revealed it. Dismiss any such alert before
# working, and say so, because a blocked app looks exactly like a broken tool.
#
# The OK is a `div.cw-button`, not a <button>, which is why a tag-based search misses it.
_ALERT_JS = r"""() => {
  const walk = (root, d, fn) => {
    if (d > 20) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, d + 1, fn);
      fn(el);
    }
  };
  // Return the OK button's position instead of clicking it. Apple builds these as `div.cw-button`
  // and a synthetic click on one is ignored: an "Unable to Update Reminder" alert stayed up
  // through six dismissal attempts, then closed on the first real pointer event. While it was up
  // it swallowed every click underneath, so creating a reminder failed with "no empty row to type
  // into" and the app looked like it had degraded. This was the cause of that whole class of
  // failure, and it is invisible in the DOM of the list itself, which reads as perfectly healthy.
  let title = '', ok = null;
  walk(document, 0, (el) => {
    const cls = (el.className || '').toString();
    if (!ok && cls.includes('cw-button') && /^(OK|Ok|Dismiss)$/.test((el.innerText || '').trim())) {
      const r = el.getBoundingClientRect();
      if (r.width > 0 && r.height > 0) ok = el;
    }
    if (!title && cls.split(/\s+/).includes('alert-title')) title = (el.innerText || '').trim();
  });
  if (!ok) return JSON.stringify({none: true});
  if (!title) {
    walk(document, 0, (el) => {
      const cls = (el.className || '').toString();
      if (!title && /cw-alert/.test(cls)) {
        const t = (el.innerText || '').trim().split('\n')[0];
        if (t && t.length < 80) title = t;
      }
    });
  }
  const r = ok.getBoundingClientRect();
  return JSON.stringify({title: title || 'an iCloud alert',
                         x: Math.round(r.x + r.width / 2),
                         y: Math.round(r.y + r.height / 2)});
}"""


def dismiss_alert(frame, page=None) -> str:
    """Close blocking iCloud alerts if any are up. Returns the first title, or "" if there was none.

    `page` is what makes this work: the OK is clicked as a real pointer event, because a synthetic
    click on Apple's `div.cw-button` is ignored. Called without a page it can only report.

    Loops, because dismissing one alert can reveal another queued behind it.
    """
    first = ""
    try:
        for _ in range(5):
            found = json.loads(frame.evaluate(_ALERT_JS))
            if found.get("none"):
                return first
            first = first or found["title"]
            if page is None:
                return first
            page.mouse.click(found["x"], found["y"])
            page.wait_for_timeout(1_200)
        return first
    except Exception:
        return first
