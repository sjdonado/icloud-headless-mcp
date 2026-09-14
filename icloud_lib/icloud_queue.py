"""Writes that could not happen because Apple was waiting for a tap, kept until it comes.

The gap this closes: a browser write hit the blocked latch, raised `NeedsApproval`, and that was the
end of it. The owner approves access hours later, `reask_access.py` clears the latch, and nothing goes back
to do the work. For a voice note that is worse than it sounds, because the words reach the box over
a webhook and the note is the only place they were meant to end up.

**Append-only, one event per line.** The same shape as `state/followups.md`, and for the same reason:
a file that is rewritten is a file that loses things, and this one exists precisely so nothing gets
lost. An item is pending when its most recent event is `queued`.

    {"ts": ..., "id": ..., "event": "queued", "kind": "create_note", "params": {...}, "origin": "..."}
    {"ts": ..., "id": ..., "event": "done",    "result": {...}}
    {"ts": ..., "id": ..., "event": "failed",  "error": "..."}
    {"ts": ..., "id": ..., "event": "expired", "age_hours": 61.2}

**Expiry is 48 hours, and expiry is not silence.** A reminder from Tuesday may already be handled by
Thursday, so executing it unasked is its own kind of wrong. An expired item is reported to the owner with
what it would have done, which leaves the decision where it belongs.

Owned by agent-icloud and mode 0600: these lines hold whole note bodies, and root's
watchdog reads it as root. Nothing else has any business in it, least of all the `agent` uid, which
is the one account reachable through prompt injection.
"""
from __future__ import annotations

import json
import os
import sys
import time
import uuid
from typing import Any

from icloud_lib.icloud_tabs import SHARED_STATE

# Beside the blocked latch, in the state two uids share: a drain running as somebody else has
# to be able to read what was queued here.
QUEUE = str(SHARED_STATE / "pending.jsonl")
MAX_AGE_HOURS = 48.0


def _append(record: dict) -> bool:
    """One line. Returns whether it actually reached disk.

    It used to swallow OSError and return nothing, which is the worst possible failure here: the
    caller was told "queued, it will run by itself" for a write that had gone nowhere, and a failed
    `done` mark left a note that had already been written still pending, so the next drain wrote it
    a second time. A queue that cannot say whether it recorded something is not a queue.

    0600 rather than 0664: these lines hold whole note bodies, and the only reader is root's
    watchdog. Group-readable published the owner's notes to every uid on the box, including `agent`, which
    is the one account reachable through prompt injection.
    """
    try:
        os.makedirs(os.path.dirname(QUEUE), exist_ok=True)
        new = not os.path.exists(QUEUE)
        with open(QUEUE, "a") as fh:
            fh.write(json.dumps(record, ensure_ascii=False) + "\n")
            fh.flush()
            os.fsync(fh.fileno())
        if new:
            os.chmod(QUEUE, 0o600)
        return True
    except OSError as exc:
        print(f"icloud_queue: could not write {QUEUE}: {exc}", file=sys.stderr, flush=True)
        return False


def enqueue(kind: str, params: dict, origin: str = "", first_ts: float | None = None,
            attempts: int = 0, blocker: str = "icloud-approval") -> str | None:
    """Record a write to be retried once access is back. Returns the id, or None if it was not saved.

    `first_ts` carries the original queue time across a re-queue. Without it every bounce reset the
    clock, so `MAX_AGE_HOURS` could never fire and a permanently blocked item lived forever,
    reporting itself once per watchdog tick: exactly the repetition the shared latch exists to stop.

    Identical pending work is not queued twice. The caller may retry a request, and the ingress already
    dedupes a recording by id, so without this the same dictation could become two notes.
    """
    now = time.time()
    if not attempts:
        # Only new work is deduplicated. A retry from the drain deliberately re-queues the same
        # (kind, params), and matching it against itself would drop the retry entirely.
        for existing in pending():
            if existing.get("kind") == kind and existing.get("params") == params:
                return existing["id"]
    item = uuid.uuid4().hex[:12]
    ok = _append({"ts": now, "first_ts": first_ts if first_ts is not None else now,
                  "id": item, "event": "queued", "attempts": attempts, "blocker": blocker,
                  "kind": kind, "params": params, "origin": origin})
    return item if ok else None


def mark(item: str, event: str, **extra: Any) -> bool:
    """Record what happened to an item. Returns whether it was written.

    The drain must not move on from an item whose mark failed: an unrecorded `done` is a note that
    gets written again on the next pass.
    """
    return _append({"ts": time.time(), "id": item, "event": event, **extra})


def _events() -> list[dict]:
    if not os.path.exists(QUEUE):
        return []
    out = []
    try:
        with open(QUEUE) as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except json.JSONDecodeError:
                    # A truncated line is history, not a reason to refuse the rest of the file.
                    continue
    except OSError:
        return []
    return out


def pending(blocker: str | None = None) -> list[dict]:
    """Items whose last event is `queued`, oldest first, optionally for one blocker only.

    `blocker` names what has to be resolved before an item can run, so this queue is not specific to
    Apple's device approval: an expired OAuth token or an unauthenticated MCP is the same shape of
    problem, and a drain for one must not attempt work waiting on another.

    Oldest first on purpose: the owner dictated them in an order, and a note that refers to "the thing I
    just said" only makes sense if the thing it refers to was written first.
    """
    last: dict[str, dict] = {}
    queued: dict[str, dict] = {}
    for ev in _events():
        item = ev.get("id")
        if not item:
            continue
        last[item] = ev
        if ev.get("event") == "queued":
            queued[item] = ev
    out = [ev for ev in last.values() if ev.get("event") == "queued"]
    if blocker is not None:
        out = [ev for ev in out if ev.get("blocker", "icloud-approval") == blocker]
    return sorted(out, key=lambda e: e.get("ts", 0))


def stranded() -> list[dict]:
    """Items whose last event is `attempting`: claimed, then the process died mid-write.

    Reported rather than retried. From here, "the note was created and the mark was lost" and "the
    write never happened" are indistinguishable, and writing the owner's note a second time is the
    worse of the two mistakes to make on the owner's behalf.
    """
    last: dict[str, dict] = {}
    detail: dict[str, dict] = {}
    for ev in _events():
        item = ev.get("id")
        if not item:
            continue
        last[item] = ev
        if ev.get("event") == "queued":
            detail[item] = ev
    return [detail.get(i, last[i]) for i, ev in last.items()
            if ev.get("event") == "attempting"]


def age_hours(item: dict) -> float:
    """Age since the owner first asked, not since the last re-queue."""
    started = item.get("first_ts", item.get("ts", 0))
    return (time.time() - float(started)) / 3600.0


def count_pending(blocker: str | None = None) -> int:
    return len(pending(blocker))
