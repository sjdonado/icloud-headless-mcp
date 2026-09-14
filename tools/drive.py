#!/usr/bin/env python3
"""What the iCloud Drive pull last fetched, per library. Status only.

**The pull itself is not a tool and deliberately never will be.** `bin/icloud_drive_fetch.py`
fetches and stages files as this account, on whatever schedule you give it, and whatever consumes
those files runs as its own account with its own access. Separate accounts on purpose: the fetcher
cannot reach what reads the staged files, so a compromised browser cannot rewrite data it was
never meant to touch. An MCP tool that could fetch, move and import would collapse that into one.

So what a client gets is the one thing it needs and cannot infer: whether the pull is current. A
week of data that stops on Tuesday is a pull that has not run, or a library the source app stopped
writing to, and that is a different fact from a week with nothing to record. Reporting freshness
here means whatever reads those files does not have to guess why they are short.
"""
import json
from datetime import datetime, timezone

# Which libraries this install pulls, and where they are staged, both from the environment. The
# etag file is keyed by Apple's opaque `docwsid`, so it can say how many files are tracked in
# total and nothing about which library they belong to; the staging tree is what carries that.
from icloud_lib.drive_libraries import ETAGS, LIBRARIES, STAGING

from app import mcp


def _age(ts: float | None) -> dict:
    if ts is None:
        return {"at": None, "hours_ago": None}
    when = datetime.fromtimestamp(ts, timezone.utc)
    return {"at": when.isoformat(timespec="seconds"),
            "hours_ago": round((datetime.now(timezone.utc) - when).total_seconds() / 3600, 1)}


@mcp.tool()
def drive_status() -> dict:
    """When the iCloud Drive pull last ran, and what it holds per library.

    Read this before concluding that data pulled off Drive is missing rather than stale. Compare
    the age of the last run against how often the pull is scheduled: a gap much larger than that
    interval means it has not run.
    """
    try:
        etags = json.loads(ETAGS.read_text())
    except OSError:
        etags = {}
        last_run = None
    except json.JSONDecodeError as exc:
        return {"error": f"{ETAGS} is not readable as JSON: {exc}",
                "note": "the pull writes this file at the end of every run, so an unparseable "
                        "one means a run was interrupted partway"}
    else:
        last_run = ETAGS.stat().st_mtime

    per_library = {}
    for lib in LIBRARIES:
        staged = STAGING / lib["dest"]
        times = [p.stat().st_mtime for p in staged.rglob("*") if p.is_file()] \
            if staged.is_dir() else []
        per_library[lib["name"]] = {
            "kind": lib["kind"],
            "staged_under": lib["dest"],
            # Zero is the healthy answer. The sync script moves everything out of staging after
            # every fetch, so a non-zero count means a run stopped between the fetch and the
            # import, which is worth saying rather than hiding.
            "staged_now": len(times),
            "newest_staged_file": _age(max(times) if times else None),
        }

    stale = last_run is not None and (
        datetime.now(timezone.utc).timestamp() - last_run) > 25 * 3600
    return {
        "last_pull": _age(last_run),
        # Named rather than implied: the answer to "is this stale" should not require the reader
        # to do the arithmetic, and 25 hours is one daily run plus an hour of slack.
        "stale": stale if last_run is not None else None,
        "schedule": "a daily timer, persistent, so a missed run catches up on boot",
        # Total, not per library: the etag file is keyed by Apple's opaque docwsid.
        "files_tracked": len(etags),
        "libraries": per_library,
        "note": "Status only. The pull runs as this account from a timer and the import runs as "
                "the owning service's account; no tool here can trigger either, because the "
                "fetcher deliberately cannot reach those databases. `last_pull` is the freshness "
                "signal; `staged_now` is 0 in normal operation because the sync empties staging "
                "after every run.",
    }
