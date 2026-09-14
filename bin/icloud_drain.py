#!/usr/bin/env python3
"""Run the iCloud writes that were waiting for the owner's approval, now that it has arrived.

Called by `agent-watch.sh`, which already runs every ten minutes and already knows whether the
blocked latch is set. That is why there is no timer of its own: the thing that notices access coming
back should be the thing that drains, or the two disagree about when it is safe to try.

Prints one line per item on **stdout** for the watchdog to relay, and nothing at all when there is
nothing to say, so a quiet day stays quiet. Anything that goes wrong internally goes to stderr,
which the watchdog reports separately: folding the two together turned a stray warning into "I ran
what had been waiting" with an empty queue.

`--report-only` ages items out without touching the browser. It exists because expiry used to be
reachable only from the drain, which runs only when the browser is healthy and the latch is clear.
If the owner never approved, the 48-hour report this queue promises would never arrive at any age.

Runs as agent-icloud, the account that owns the browser. It calls the same tool functions the agent
calls rather than reimplementing them: a second copy of "how to write a note" is a second thing to
keep correct, and this one would only ever be exercised on the rare path.
"""
from __future__ import annotations

import sys
from pathlib import Path

# This directory, whatever it is called and wherever it was installed.
sys.path.insert(0, str(Path(__file__).resolve().parent))

from icloud_lib import icloud_queue                             # noqa: E402
from icloud_lib.icloud_tabs import blocked, quiet_hours          # noqa: E402

# How many times one item may be attempted before it is given up on. A lock timeout, a wedged
# browser or a signed-out session are transient, and the first version marked any non-latch failure
# `failed` and forgot it, so a Telegram-driven note holding the Notes lock could burn the whole
# queue in a single pass.
MAX_ATTEMPTS = 4

# This drain resolves one blocker: Apple's device approval for the browser. Work waiting on anything
# else, an expired OAuth token or an unauthenticated MCP, stays queued under its own name and needs
# its own readiness probe, because "the browser is usable" says nothing about whether Google is.
BLOCKER = "icloud-approval"


def _summarise(kind: str, params: dict) -> str:
    if kind == "create_note":
        where = params.get("folder") or "the default folder"
        return f"note “{params.get('title', '')}” in {where}"
    if kind == "create_reminder":
        due = f", due {params['due']}" if params.get("due") else ""
        return f"reminder “{params.get('title', '')}” on {params.get('list_name', '')}{due}"
    if kind == "complete_reminder":
        return f"ticking off “{params.get('title', '')}” on {params.get('list_name', '')}"
    # No params in the fallback: a note body can be long, and this line is relayed to Telegram.
    return f"a queued {kind or 'write'}"


def _resolve(tool):
    """The plain function behind an `@mcp.tool()`, or an error rather than a guess.

    `getattr(tool, "fn", tool)` was assuming the decorator's shape. If that assumption is ever
    wrong the drain raises on its first item, and the traceback is the last thing the owner hears
    about a dictated note.
    """
    fn = getattr(tool, "fn", tool)
    if not callable(fn):
        raise TypeError(f"cannot resolve {tool!r} to a callable tool function")
    return fn


def _run(kind: str, params: dict) -> dict:
    if kind == "create_note":
        import notes_mcp
        return _resolve(notes_mcp.create_note)(
            title=params.get("title", ""), body=params.get("body", ""),
            folder=params.get("folder"))
    if kind == "create_reminder":
        import reminders_mcp
        return _resolve(reminders_mcp.create_reminder)(
            title=params.get("title", ""), list_name=params.get("list_name", ""),
            due=params.get("due", "") or "")
    if kind == "complete_reminder":
        import reminders_mcp
        return _resolve(reminders_mcp.complete_reminder)(
            title=params.get("title", ""), list_name=params.get("list_name", ""))
    return {"error": f"nothing here knows how to run {kind}"}


def _expire(items: list[dict]) -> list[str]:
    """Age items out and say what they would have done. Touches no browser.

    Not executed, and not silently dropped either. Two days on, "remind me to call the doctor" may
    already be done, so acting on it unasked is its own mistake. The owner decides.
    """
    said = []
    for item in items:
        age = icloud_queue.age_hours(item)
        if age <= icloud_queue.MAX_AGE_HOURS:
            continue
        if not icloud_queue.mark(item["id"], "expired", age_hours=round(age, 1)):
            print(f"could not record the expiry of {item['id']}", file=sys.stderr, flush=True)
            continue
        said.append(f"expired after {age:.0f}h without access, so I did not run it: "
                    f"{_summarise(item.get('kind', ''), item.get('params') or {})}. "
                    f"Ask me again if you still want it.")
    return said


def main() -> int:
    report_only = "--report-only" in sys.argv[1:]

    # An item claimed by a pass that then died. Reported once, on stdout, because the owner is the
    # who can tell whether the note exists; retrying it might write it twice.
    for item in icloud_queue.stranded():
        icloud_queue.mark(item["id"], "abandoned")
        print(f"interrupted mid-write, so I cannot tell whether it landed: "
              f"{_summarise(item.get('kind', ''), item.get('params') or {})}. "
              f"Check, and ask me again if it is missing.", flush=True)

    items = icloud_queue.pending(BLOCKER)
    if not items:
        return 0

    # Expiry first, and before any browser check. It used to sit below the blocked() guard, so an
    # item that aged out while access was still down was never reported: the promise only held if
    # the owner approved, which is exactly the case where it does not.
    for line in _expire(items):
        print(line, flush=True)
    if report_only:
        return 0

    items = icloud_queue.pending(BLOCKER)
    if not items:
        return 0

    if blocked():
        # Still latched. Say nothing and change nothing: draining now would re-queue everything and
        # spend an app-load timeout per item, which is how the old retry behaviour turned one
        # blocked write into a stream of identical failures.
        return 0

    if quiet_hours():
        # A fresh page load is what raises Apple's prompt, and at 03:00 that is a prompt the owner cannot
        # answer before it expires. The writes keep until morning; they have already waited hours.
        return 0

    for item in items:
        item_id = item["id"]
        kind = item.get("kind", "")
        params = item.get("params") or {}
        attempts = int(item.get("attempts", 0))
        first_ts = item.get("first_ts", item.get("ts"))

        # Claim the item before touching the browser. Without this, a kill between the note being
        # created and the `done` mark leaves the item pending, and the next pass writes it twice.
        # An item found still `attempting` is reported rather than retried, because from here the
        # two outcomes are indistinguishable and writing the owner's note twice is the worse one.
        if not icloud_queue.mark(item_id, "attempting", attempt=attempts + 1):
            print(f"could not claim {item_id}, so it was not attempted", file=sys.stderr, flush=True)
            continue

        result = _run(kind, params)

        landed = result.get("created", result.get("completed"))
        if landed is True or landed == "unconfirmed":
            if not icloud_queue.mark(item_id, "done", landed=landed):
                # The write happened but the record did not. Say so loudly: this is the one state
                # that leads to a duplicate, and the owner can delete one note far more easily than
                # discover a missing one.
                print(f"WROTE but could not record it: {_summarise(kind, params)}. "
                      f"Check for a duplicate.", file=sys.stderr, flush=True)
            state = "done" if landed is True else "done but unconfirmed"
            print(f"{state}: {_summarise(kind, params)}", flush=True)
            continue

        if result.get("queued"):
            # It hit the latch again and re-queued itself under a new id. Retire this one, carrying
            # the original timestamp so age is measured from when the owner asked, not from the last
            # bounce.
            icloud_queue.mark(item_id, "superseded", replaced_by=result.get("queue_id"))
            continue

        # Anything else is transient until proven otherwise: a Notes lock held by a live tool call,
        # a wedged browser, a signed-out session.
        if attempts + 1 >= MAX_ATTEMPTS:
            icloud_queue.mark(item_id, "failed", attempts=attempts + 1,
                              error=str(result.get("error", result))[:300])
            print(f"gave up after {attempts + 1} attempts: {_summarise(kind, params)}: "
                  f"{result.get('error', 'no reason given')}", flush=True)
        else:
            icloud_queue.enqueue(kind, params, origin=item.get("origin", ""), first_ts=first_ts,
                                 attempts=attempts + 1)
            print(f"could not write it yet, will try again: {_summarise(kind, params)}",
                  file=sys.stderr, flush=True)

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
