#!/usr/bin/env python3
"""Reminders, through the resident browser.

CalDAV cannot do this. `caldav.icloud.com` exposes a legacy Reminders store that Apple left
behind when Reminders moved to the CloudKit format: it lists Today, Home and Work, while
the account's real lists are the ones the web app and every device show. Writes to the
legacy store succeed, return a uid, survive read-back, and are invisible everywhere. That
cost a real reminder, so this server exists and the CalDAV reminder tools were removed.

Same machinery as Notes: its own warm tab, exact class-token matching, real pointer events,
and an honest failure when Apple's temporary access lapses.
"""
import base64
import json
import re
import zlib
from datetime import date, datetime, timedelta
from pathlib import Path
from zoneinfo import ZoneInfo

from icloud_lib import icloud_queue
from icloud_lib.icloud_tabs import STATE_DIR as state_dir
from icloud_lib.icloud_tabs import (NeedsApproval, app_lock, blocked, click_js, collect_js,
                                    dismiss_alert, icloud_app, local_timezone)

APP = "Reminders"
URL = "https://www.icloud.com/reminders/"
FRAME = "reminders2"
READY = "rm-list-menu-item"

# The one shared server object. Importing this module is what registers its tools on it.
from app import mcp

COLLECT = collect_js()
CLICK = click_js()


def _lists(frame) -> list[str]:
    return [name.strip() for name in frame.evaluate(COLLECT, "rm-list-menu-item")
            if name.strip()]


ROWS = r"""() => {
  const walk = (root, depth, fn) => {
    if (depth > 16) return;
    for (const el of root.querySelectorAll('*')) {
      if (el.shadowRoot) walk(el.shadowRoot, depth + 1, fn);
      fn(el);
    }
  };
  const items = [];
  walk(document, 0, (el) => {
    if ((el.className || '').toString().split(/\s+/).includes('reminder-item')) items.push(el);
  });
  return items.map((it) => {
    // The due date is a leaf with no class of its own, which is why reading the row's
    // first line only ever returned titles. Collect every leaf and classify after.
    const leaves = [];
    walk(it, 0, (el) => {
      const text = (el.innerText || '').trim();
      if (text && el.children.length === 0) {
        leaves.push({cls: (el.className || '').toString().trim(), text});
      }
    });
    // The title element wraps spans, so it is not a leaf: look it up directly rather
    // than among the leaves, which returned one row for a list of fifteen.
    let title = '';
    walk(it, 0, (el) => {
      if (!title && (el.className || '').toString().split(/\s+/).includes('content-title')) {
        title = (el.innerText || '').trim();
      }
    });
    if (!title) {
      const first = (it.innerText || '').split('\n').map((t) => t.trim()).filter(Boolean)[0];
      title = first || '';
    }
    // Priority is a child *inside* content-title, so its label lands in the title's
    // innerText: "High Priority\nFacturación electrónica". Read it separately and strip
    // it, or every prioritised reminder is reported with a two-line name.
    let priorityText = '', priorityLevel = '';
    walk(it, 0, (el) => {
      if (priorityText) return;
      if ((el.className || '').toString().split(/\s+/).includes('priority')) {
        priorityText = (el.innerText || '').trim();
        priorityLevel = el.getAttribute('data-priority-level') || '';
      }
    });
    if (priorityText && title.startsWith(priorityText)) {
      title = title.slice(priorityText.length).trim();
    }
    // A flag renders as a child of the otherwise-empty flag container.
    let flagged = false;
    walk(it, 0, (el) => {
      if ((el.className || '').toString().split(/\s+/).includes('flag-container')) {
        flagged = flagged || el.children.length > 0;
      }
    });
    // Completion is an attribute on the row, `data-completed="true"`, not a class token: with
    // Show Completed on, 1,380 completed rows came back with `completed: false` under the old
    // class check, and every read reported them as open. The `due-date` span is a proper
    // selector for the date where one exists; the leaf heuristic below stays as the fallback.
    let dueText = '';
    walk(it, 0, (el) => {
      if (!dueText && (el.className || '').toString().split(/\s+/).includes('due-date')) {
        dueText = (el.innerText || '').trim();
      }
    });
    return {
      id: it.id || '',
      title,
      leaves: leaves.map((l) => l.text),
      dueText,
      completed: it.getAttribute('data-completed') === 'true',
      priorityText,
      priorityLevel,
      flagged,
    };
  });
}"""

# "Today, 4:00 PM, Weekly", "11/22/2026", "Tomorrow, 09:00", "Mon, Aug 17".
_DUE_HINT = re.compile(
    r"(\d{1,2}/\d{1,2}/\d{2,4})"
    r"|\b(today|tomorrow|yesterday)\b"
    r"|\b(mon|tue|wed|thu|fri|sat|sun)\b"
    r"|\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)\b"
    r"|\d{1,2}:\d{2}",
    re.I,
)


def _items(frame) -> list[dict]:
    """One entry per reminder, with its due date when the row shows one.

    Apple renders the due text as an unclassed leaf at the end of the row, so it is found
    by shape rather than by selector: the label "Notes" is a placeholder and is dropped,
    and anything date-shaped becomes `due`.
    """
    out, seen = [], set()
    for row in frame.evaluate(ROWS):
        title = (row.get("title") or "").strip()
        # Dedup by Apple's row id where there is one. By title alone, a weekly reminder ticked
        # off twenty times collapses to a single completed entry and the history under-counts.
        key = row.get("id") or title
        if not title or key in seen:
            continue
        seen.add(key)
        # The span repeats an overdue date on a second, visually hidden line: "9/3/2026, Overdue\n9/3/2026".
        due = (row.get("dueText") or "").strip().split("\n")[0].strip()
        for leaf in ([] if due else row.get("leaves", [])):
            text = leaf.strip()
            if not text or text == title or text.lower() == "notes":
                continue
            if _DUE_HINT.search(text):
                due = text
                break
        # Apple's own numbering, RFC 5545: 1 is the most urgent, 9 the least. Observed
        # live as 1 = High and 5 = Medium; Low is 9 by the same scheme.
        level = (row.get("priorityLevel") or "").strip()
        priority = {"1": "high", "5": "medium", "9": "low"}.get(level)
        if not priority and row.get("priorityText"):
            priority = row["priorityText"].replace("Priority", "").strip().lower() or None
        out.append({
            "id": (row.get("id") or "").rsplit("/", 1)[-1] or None,
            "title": title,
            "due": due or None,
            "completed": bool(row.get("completed")),
            "priority": priority,
            "flagged": bool(row.get("flagged")),
        })
    return out


def _open_list(frame, page, list_name: str) -> str | None:
    """Select a list, returning None on success or an error string.

    Clears a blocking iCloud alert first. One sat over the app for an hour on 2026-08-17 and
    every click under it did nothing while the DOM looked perfectly healthy, which reads as a
    broken tool rather than a blocked app.
    """
    if dismiss_alert(frame, page):
        page.wait_for_timeout(1_200)
    names = _lists(frame)
    wanted = list_name.lower().strip()
    match = next((i for i, n in enumerate(names) if wanted in n.lower()), None)
    if match is None:
        return f"no list matching {list_name}; available: {names}"
    frame.evaluate(CLICK, ["rm-list-menu-item", match])
    page.wait_for_timeout(4_000)
    return None


@mcp.tool()
def reminder_lists() -> dict:
    """List the reminder lists on the account, as they appear on the owner's devices."""
    try:
        with app_lock(APP), icloud_app(APP, URL, FRAME, READY) as (frame, _):
            names = _lists(frame)
            return {"count": len(names), "lists": names}
    except NeedsApproval as exc:
        return {"error": str(exc), "needs_device_approval": True}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}


@mcp.tool()
def list_reminders(list_name: str) -> dict:
    """Read the open reminders in one list. For what the owner has ticked off, use completed_reminders."""
    try:
        with app_lock(APP), icloud_app(APP, URL, FRAME, READY) as (frame, page):
            problem = _open_list(frame, page, list_name)
            if problem:
                return {"error": problem}
            items = [r for r in _items(frame) if not r["completed"]]
            dated = sum(1 for r in items if r["due"])
            prioritised = sum(1 for r in items if r["priority"])
            return {"list": list_name, "count": len(items), "with_due_date": dated,
                    "with_priority": prioritised,
                    "reminders": items,
                    "note": ("due values are exactly what the app displays, so they are "
                             "relative: 'Today, 4:00 PM' means today in the owner's zone. "
                             "priority is high, medium or low as set on the reminder; "
                             "flagged is Apple's flag. Neither can be set from here yet.")}
    except NeedsApproval as exc:
        return {"error": str(exc), "needs_device_approval": True}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}



# ── Completed reminders, from CloudKit rather than the DOM ────────────────────
#
# The web app hides completed rows behind a per-list "Show Completed" toggle, loads them lazily
# in pages of about a hundred every few seconds, and shows no completion date anywhere: not in
# the row, not in the details popover. Measured 2026-09-07. Three probes into that path answered
# the wrong question well.
#
# The same page loads Apple's CloudKit JS, already authenticated as the owner, and the record behind
# each row carries `Completed`, `CompletionDate`, `DueDate`, `List` and the title as a compressed
# document. `fetchRecordZoneChanges` walks a whole zone with a sync token, so the first pass is
# slow (49s for 5,823 private records, 88s for the shared Home list) and every later pass is
# one round trip. The token and the slimmed records live under the browser account, where the
# agent cannot read them, and a stale token falls back to a full pass rather than to nothing.
#
# `performQuery` cannot filter this: the Reminder type is "not marked indexable", so the filter
# by date happens here, over the cache.

CK_CACHE = state_dir / "reminders-ck.json"
CK_KEYS = ["Completed", "CompletionDate", "DueDate", "AllDay", "CreationDate", "List", "TitleDocument",
           "Deleted", "Flagged", "Priority", "Name"]

_CK_ZONES = r"""async () => {
  const c = CloudKit.getDefaultContainer();
  const out = [{scope: 'private', zoneName: 'Reminders', owner: null}];
  try {
    const s = await c.sharedCloudDatabase.fetchAllRecordZones();
    for (const z of (s.zones || [])) if (z.zoneID.zoneName === 'Reminders')
      out.push({scope: 'shared', zoneName: 'Reminders', owner: z.zoneID.ownerRecordName});
  } catch (e) {}
  return out;
}"""

# Argument is an array of zone entries, read from the minified source: `asArray(e).map(...)`
# under the `changes/zone` entity. `{zones: [...]}` is silently read as the default zone.
_CK_SYNC = r"""async ([zone, token, keys]) => {
  const c = CloudKit.getDefaultContainer();
  const db = zone.scope === 'shared' ? c.sharedCloudDatabase : c.privateCloudDatabase;
  const zoneID = zone.owner ? {zoneName: zone.zoneName, ownerRecordName: zone.owner} : {zoneName: zone.zoneName};
  const t0 = Date.now(); let pages = 0; const changed = [], gone = [];
  while (pages < 120) {
    const entry = {zoneID, resultsLimit: 200, desiredKeys: keys};
    if (token) entry.syncToken = token;
    const r = await db.fetchRecordZoneChanges([entry]); pages++;
    const z = r.zones && r.zones[0];
    if (!z) return {error: (r.errors || []).map(e => String(e.reason || e.serverErrorCode || e)).join('; ') || 'no zone in response', pages};
    for (const rec of (z.records || [])) {
      if (rec.deleted) { gone.push(rec.recordName); continue; }
      const f = rec.fields || {}; const v = (k) => (f[k] ? f[k].value : undefined);
      changed.push({n: rec.recordName, t: rec.recordType, del: v('Deleted') === 1, c: v('Completed') === 1,
                    cd: v('CompletionDate'), dd: v('DueDate'), cr: v('CreationDate'),
                    list: f.List && f.List.value && f.List.value.recordName, title: v('TitleDocument'),
                    allday: v('AllDay') === 1, name: v('Name'), pri: v('Priority'), flag: v('Flagged') === 1});
    }
    token = z.syncToken;
    if (!z.moreComing) break;
  }
  return {token, changed, gone, pages, ms: Date.now() - t0};
}"""


def _ck_title(blob: str | None) -> str:
    """The title is a zlib or gzip stream holding a protobuf document; the text sits at field
    2 > 3 > 2. Measured on 2026-09-07 against the visible titles. Falls back to the first
    printable run rather than to nothing."""
    if not blob:
        return ""
    raw = zlib.decompress(base64.b64decode(blob), 47)

    def varint(b, i):
        n = shift = 0
        while True:
            byte = b[i]
            i += 1
            n |= (byte & 0x7F) << shift
            shift += 7
            if byte < 0x80:
                return n, i

    def fields(b):
        i, out = 0, []
        while i < len(b):
            key, i = varint(b, i)
            num, wire = key >> 3, key & 7
            if wire == 0:
                val, i = varint(b, i)
                out.append((num, val))
            elif wire == 2:
                n, i = varint(b, i)
                out.append((num, b[i:i + n]))
                i += n
            elif wire == 1:
                i += 8
            elif wire == 5:
                i += 4
            else:
                break
        return out

    try:
        doc = next(v for f, v in fields(raw) if f == 2 and isinstance(v, bytes))
        attr = next(v for f, v in fields(doc) if f == 3 and isinstance(v, bytes))
        return next(v for f, v in fields(attr) if f == 2 and isinstance(v, bytes)).decode("utf-8")
    except Exception:
        runs = re.findall(rb"(?:[\x20-\x7e]|[\xc2-\xf4][\x80-\xbf]+){2,}", raw)
        return runs[0].decode("utf-8", "replace") if runs else ""


def _ck_sync(frame) -> dict:
    """Bring the local record cache up to date and return it, with `sync` telling how."""
    cache = {"tokens": {}, "records": {}}
    if CK_CACHE.is_file():
        try:
            cache = json.loads(CK_CACHE.read_text(encoding="utf-8"))
        except Exception:
            cache = {"tokens": {}, "records": {}}
    zones = frame.evaluate(_CK_ZONES)
    report = []
    for zone in zones:
        key = f"{zone['scope']}:{zone['owner'] or ''}"
        token = cache["tokens"].get(key)
        result = frame.evaluate(_CK_SYNC, [zone, token, CK_KEYS])
        if result.get("error") and token:
            # A token can expire or be refused; a full pass is the honest recovery.
            result = frame.evaluate(_CK_SYNC, [zone, None, CK_KEYS])
            token = None
        if result.get("error"):
            report.append({"zone": key, "error": result["error"]})
            continue
        if token is None:
            # Full pass: anything cached for this zone that did not come back is gone.
            for name in [n for n, r in cache["records"].items() if r.get("zone") == key]:
                del cache["records"][name]
        for name in result["gone"]:
            cache["records"].pop(name, None)
        for rec in result["changed"]:
            if rec["t"] in ("Reminder", "List"):
                rec["zone"] = key
                cache["records"][rec["n"]] = rec
        cache["tokens"][key] = result["token"]
        report.append({"zone": key, "pass": "incremental" if token else "full",
                       "changed": len(result["changed"]), "removed": len(result["gone"]),
                       "seconds": round(result["ms"] / 1000, 1)})
    CK_CACHE.parent.mkdir(parents=True, exist_ok=True)
    tmp = CK_CACHE.with_suffix(".tmp")
    tmp.write_text(json.dumps(cache), encoding="utf-8")
    tmp.chmod(0o600)
    tmp.replace(CK_CACHE)
    cache["sync"] = report
    return cache


def _local_day(ms: float | None, tz: ZoneInfo, all_day: bool = False) -> str | None:
    """An all-day due date is stored as midnight UTC, which renders as 02:00 east of it and as a
    time the owner never chose. Show the day alone when Apple says the reminder has no time."""
    if not ms:
        return None
    stamp = datetime.fromtimestamp(ms / 1000, tz)
    if all_day:
        return datetime.fromtimestamp(ms / 1000, ZoneInfo("UTC")).strftime("%Y-%m-%d")
    return stamp.strftime("%Y-%m-%d %H:%M")


def _parse_day(text: str, tz: ZoneInfo, end: bool = False) -> datetime | None:
    text = (text or "").strip()
    if not text:
        return None
    d = datetime.strptime(text, "%Y-%m-%d").replace(tzinfo=tz)
    return d + timedelta(days=1) if end else d


@mcp.tool()
def completed_reminders(list_name: str = "", since: str = "", until: str = "", limit: int = 50) -> dict:
    """What the owner has ticked off, with the exact time the owner did it, from Apple's records.

    `since` and `until` are `YYYY-MM-DD` in the owner's local time; `until` is inclusive. Empty means no
    bound, so "what did I finish between the injury and the surgery" is `since="2026-08-13",
    until="2026-09-04"`. `list_name` empty means every list. `limit` is per list, newest first.

    Every entry carries `completed_at`, which is when the owner ticked it, and `due`, which is what
    it was set for. They differ often, and the first one is the answer to "when did I do it".
    Reminders the owner deleted are excluded even when they were completed first.

    The first call in a fresh install walks Apple's whole store and can take two to three minutes;
    every later call is one round trip per list zone. `sync` in the result says which happened.
    """
    limit = max(1, min(int(limit or 50), 500))
    try:
        tz = ZoneInfo(local_timezone())
        lo = _parse_day(since, tz)
        hi = _parse_day(until, tz, end=True)
    except ValueError as exc:
        return {"error": f"since and until must be YYYY-MM-DD: {exc}"}
    try:
        with app_lock(APP), icloud_app(APP, URL, FRAME, READY) as (frame, _page):
            cache = _ck_sync(frame)
    except NeedsApproval as exc:
        return {"error": str(exc), "needs_device_approval": True}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}

    records = cache["records"]
    lists = {n: r.get("name") for n, r in records.items() if r["t"] == "List" and not r["del"]}
    wanted = list_name.lower().strip()
    chosen = {n: name for n, name in lists.items() if name and (not wanted or wanted in name.lower())}
    if not chosen:
        return {"error": f"no list matching {list_name}; available: {sorted(lists.values())}",
                "sync": cache["sync"]}
    lo_ms = lo.timestamp() * 1000 if lo else None
    hi_ms = hi.timestamp() * 1000 if hi else None
    per_list = {name: [] for name in chosen.values()}
    for r in records.values():
        if r["t"] != "Reminder" or r["del"] or not r["c"] or not r.get("cd"):
            continue
        name = chosen.get(r.get("list"))
        if name is None:
            continue
        if (lo_ms and r["cd"] < lo_ms) or (hi_ms and r["cd"] >= hi_ms):
            continue
        per_list[name].append(r)
    out = []
    for name in sorted(per_list):
        rows = sorted(per_list[name], key=lambda r: r["cd"], reverse=True)
        out.append({"list": name, "completed_total": len(rows), "returned": min(limit, len(rows)),
                    "completed": [{"id": r["n"].rsplit("/", 1)[-1], "title": _ck_title(r.get("title")),
                                   "completed_at": _local_day(r["cd"], tz),
                                   "due": _local_day(r.get("dd"), tz, bool(r.get("allday"))),
                                   "flagged": bool(r.get("flag")),
                                   "priority": {1: "high", 5: "medium", 9: "low"}.get(r.get("pri"))}
                                  for r in rows[:limit]]})
    return {"window": {"since": since or None, "until": until or None, "timezone": str(tz)},
            "total_in_window": sum(len(v) for v in per_list.values()),
            "lists": out, "sync": cache["sync"],
            "note": "completed_at is when the owner ticked it, in the owner's local time; due is "
                    "what it was set for. Deleted reminders are excluded."}


# ── Setting a due date ────────────────────────────────────────────────────────
#
# There is no shorter path than the details popover, and this is the whole of it, mapped by
# probing the live app rather than guessed:
#
#   hover the row  ->  click `button.info`  ->  a `ui-popover` opens
#   -> the `ui-switch` in `.date-switch-container` ("Remind me on a Day") reveals the controls
#   -> the calendar's day cells carry `aria-label` "Thursday, August 20, 2026", so a target day is
#      one unambiguous click; the month/day/year `spinbutton` segments are readable proof it took
#   -> `ui-button` "Save" commits. Selecting a day does NOT close the popover, and dismissing it
#      by clicking outside does NOT commit: without Save the date is silently dropped.
#
# Typing the date into the title does not work at all: the app does not parse it, so the phrase
# just stays in the name. Verified with a probe row whose date cell stayed empty.

_WALK = ("const walk = (root, d, fn) => { if (d > 20) return; "
         "for (const el of root.querySelectorAll('*')) { "
         "if (el.shadowRoot) walk(el.shadowRoot, d+1, fn); fn(el); } };")

_ROW_GEO = """(title) => {
  %s
  let row = null;
  walk(document, 0, (el) => {
    if (!row && (el.className||'').toString().split(/\\s+/).includes('reminder-item')
        && (el.innerText||'').toLowerCase().includes(title.toLowerCase())) row = el;
  });
  if (!row) return JSON.stringify({error: 'row not found'});
  // A new row is appended, so in a long list it sits below the fold. Coordinates taken there are
  // outside the viewport and every click against them is silently discarded.
  row.scrollIntoView({block: 'center'});
  const rr = row.getBoundingClientRect();
  let info = null;
  walk(row, 0, (el) => {
    if (!info && (el.className||'').toString().split(/\\s+/).includes('info')) info = el;
  });
  if (!info) return JSON.stringify({error: 'info control not found'});
  const ir = info.getBoundingClientRect();
  const off = (r) => r.y < 0 || r.y > window.innerHeight || r.x < 0 || r.x > window.innerWidth;
  if (off(rr) || off(ir)) return JSON.stringify({
    error: 'the row is outside the viewport at y=' + Math.round(rr.y) +
           ' (height ' + window.innerHeight + '); a click there would be discarded'});
  return JSON.stringify({row: {x: Math.round(rr.x+20), y: Math.round(rr.y+rr.height/2)},
                         info: {x: Math.round(ir.x+ir.width/2), y: Math.round(ir.y+ir.height/2)}});
}""" % _WALK

_TOGGLE_DAY = """() => {
  %s
  let c = null;
  walk(document, 0, (el) => {
    if (!c && (el.className||'').toString().includes('date-switch-container')) c = el;
  });
  if (!c) return 'no date switch: popover not open';
  let sw = null;
  walk(c, 0, (el) => { if (!sw && el.tagName.toLowerCase() === 'ui-switch') sw = el; });
  if (!sw) return 'no ui-switch';
  if (sw.getAttribute('aria-checked') === 'true') return 'already on';
  sw.click();
  return 'on';
}""" % _WALK

_CAL_MONTH = """() => {
  %s
  // Which month is on screen, read from the day cells we are about to click. The grid shows a few
  // days of the neighbouring months, so the month owning the most cells is the displayed one.
  const tally = {};
  walk(document, 0, (el) => {
    const m = (el.getAttribute('aria-label') || '').match(/([A-Z][a-z]+) \\d{1,2}, (\\d{4})/);
    if (m) { const k = m[1] + ' ' + m[2]; tally[k] = (tally[k] || 0) + 1; }
  });
  let best = null, n = 0;
  for (const k in tally) if (tally[k] > n) { n = tally[k]; best = k; }
  return best ? JSON.stringify({month: best.split(' ')[0], year: +best.split(' ')[1]})
              : JSON.stringify({error: 'no day cells on screen'});
}""" % _WALK


_PREV_MONTH = """() => {
  %s
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && (el.getAttribute('aria-label')||'') === 'Previous Month') btn = el;
  });
  if (!btn) return 'no previous-month control';
  btn.click();
  return 'moved';
}""" % _WALK


_NEXT_MONTH = """() => {
  %s
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && (el.getAttribute('aria-label')||'') === 'Next Month') btn = el;
  });
  if (!btn) return 'no next-month control';
  btn.click();
  return 'advanced';
}""" % _WALK

# Apple decorates the day cell's label rather than keeping it canonical: today reads
# "Today, Tuesday, August 18, 2026" and whichever day is currently selected gains a trailing
# " selected". Exact equality therefore missed the two days most likely to be asked for, and
# "remind me to call Julian at 5:30am" failed with "day not visible: Tuesday, August 18, 2026"
# while that very cell was on screen. Matching the canonical form as a substring accepts both
# decorations and stays unambiguous, because the weekday and year bracket the number: "Saturday,
# August 1, 2026" does not occur inside "Saturday, August 11, 2026".
_CLICK_DAY = """(label) => {
  %s
  const year = label.slice(-4);
  let hit = null;
  const seen = [];
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (!a.includes(', ' + year)) return;      // day cells only, whatever else carries a label
    seen.push(a);
    if (!hit && a.includes(label)) hit = el;
  });
  if (!hit) return 'day not visible: ' + label + '; the calendar shows ' + seen.slice(0, 3).join(' | ');
  hit.click();
  return 'selected';
}""" % _WALK

_SEGMENTS = """() => {
  %s
  const seg = {};
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (el.getAttribute('role') === 'spinbutton' && ['month','day','year'].includes(a)) {
      seg[a] = (el.innerText || '').trim();
    }
  });
  return JSON.stringify(seg);
}""" % _WALK

_SAVE = """() => {
  %s
  let btn = null;
  walk(document, 0, (el) => {
    if (!btn && el.tagName.toLowerCase() === 'ui-button'
        && (el.innerText||'').trim().toLowerCase() === 'save') btn = el;
  });
  if (!btn) return JSON.stringify({error: 'save not found'});
  const r = btn.getBoundingClientRect();
  return JSON.stringify({x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}""" % _WALK


_POPOVER_OPEN = """() => {
  %s
  let open = false;
  walk(document, 0, (el) => {
    if (open || el.tagName.toLowerCase() !== 'ui-popover') return;
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) open = true;
  });
  return open;
}""" % _WALK


_SCROLL_END = """() => {
  %s
  let area = null;
  walk(document, 0, (el) => {
    const cls = (el.className || '').toString();
    if (!area && (cls.includes('reminder-list-items') || cls.includes('scrollable-area'))
        && el.scrollHeight > el.clientHeight + 4) area = el;
  });
  if (!area) return 'nothing to scroll';
  area.scrollTop = area.scrollHeight;
  return 'scrolled';
}""" % _WALK


# Clicking "Add new reminder" creates the row but leaves focus on the sidebar's selected list
# item, so the typed title goes nowhere and the row stays blank. Measured: activeElement was
# `div.rm-list-menu-item is-selected` both before and after typing. So the new row's own text
# field is focused explicitly, by real pointer event, before anything is typed.
_FOCUSED_TEXT = """() => {
  // What the focused element holds, and whether focus is even in a row's input.
  //
  // Typing without checking this is what made creation unreliable: the row appears, the click on
  // it reports success, and the keystrokes go nowhere. The failure then looks like the list
  // refusing the write, when in fact nothing was ever typed.
  const a = document.activeElement;
  const deep = (el) => {
    while (el && el.shadowRoot && el.shadowRoot.activeElement) el = el.shadowRoot.activeElement;
    return el;
  };
  const el = deep(a);
  if (!el) return JSON.stringify({focused: false});
  const cls = (el.className || '').toString();
  return JSON.stringify({
    focused: /tt-input-field|content-title/.test(cls) || el.isContentEditable
             || el.tagName.toLowerCase() === 'input',
    cls: cls.slice(0, 40),
    text: (el.value !== undefined && el.value !== null ? el.value : (el.innerText || '')).trim()
  });
}"""


_FOCUS_NEW_ROW = """() => {
  %s
  const rows = [];
  walk(document, 0, (el) => {
    if ((el.className||'').toString().split(/\\s+/).includes('reminder-item')) rows.push(el);
  });
  // The new row is the empty one; prefer the last such, since Apple appends.
  let target = null;
  for (const r of rows) {
    let ct = '';
    walk(r, 0, (x) => {
      if (!ct && (x.className||'').toString().split(/\\s+/).includes('content-title'))
        ct = (x.innerText||'').trim();
    });
    if (!ct) target = r;
  }
  if (!target) return JSON.stringify({error: 'no empty row to type into'});
  let field = null;
  walk(target, 0, (el) => {
    if (!field && (el.className||'').toString().includes('tt-input-field')) field = el;
  });
  if (!field) return JSON.stringify({error: 'no tt-input-field on the new row'});
  field.scrollIntoView({block: 'center'});
  const r = field.getBoundingClientRect();
  return JSON.stringify({x: Math.round(r.x + Math.min(40, r.width/2)),
                         y: Math.round(r.y + r.height/2)});
}""" % _WALK


# Time lives behind its own checkbox below the date picker: `ui-checkbox.time-checkbox`. Ticking
# it reveals `spinbutton` segments labelled hour, minute and AM/PM. They accept typing directly,
# and advance to the next segment as they fill, so "0500" then "P" sets 5:00 PM.
_TIME_CHECKBOX = """() => {
  %s
  let cb = null;
  walk(document, 0, (el) => {
    if (!cb && (el.className||'').toString().includes('time-checkbox')) cb = el;
  });
  if (!cb) return JSON.stringify({error: 'no time checkbox: is the date enabled?'});
  const checked = cb.getAttribute('aria-checked') === 'true';
  const r = cb.getBoundingClientRect();
  return JSON.stringify({checked, x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}""" % _WALK

_SEGMENT = """(which) => {
  %s
  let seg = null;
  walk(document, 0, (el) => {
    if (!seg && el.getAttribute('role') === 'spinbutton'
        && (el.getAttribute('aria-label')||'') === which) seg = el;
  });
  if (!seg) return JSON.stringify({error: 'no ' + which + ' segment'});
  const r = seg.getBoundingClientRect();
  return JSON.stringify({text: (seg.innerText||'').trim(),
                         x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}""" % _WALK

_TIME_SEGMENTS = """() => {
  %s
  const out = {};
  walk(document, 0, (el) => {
    const a = el.getAttribute('aria-label') || '';
    if (el.getAttribute('role') === 'spinbutton' && ['hour','minute','AM/PM'].includes(a)) {
      out[a] = (el.innerText || '').trim();
    }
  });
  return JSON.stringify(out);
}""" % _WALK


def _day_label(d: date) -> str:
    """Apple labels a cell "Thursday, August 20, 2026", with no leading zero on the day."""
    return f"{d.strftime('%A, %B')} {d.day}, {d.year}"


def _row_date(landed: str):
    """The date the row displays, or None when it shows a relative word instead.

    Numeric forms only, deliberately: resolving "Today" needs a clock, and which clock is exactly
    the question this path must not have an opinion about.
    """
    m = re.search(r"(\d{1,2})/(\d{1,2})/(\d{4})", landed or "")
    return date(int(m.group(3)), int(m.group(1)), int(m.group(2))) if m else None


def _check_day(landed: str, when: date) -> str:
    """Does the row's date match the date asked for? "" when it does, else the reason.

    Apple renders the due day three ways: "Today", "Tomorrow", or a numeric date. Only the numeric
    form is unambiguous, so the relative words are resolved against `today` in the owner's zone
    rather than the box's, which runs UTC and is a day ahead of the owner for two hours every evening.
    """
    today = datetime.now(ZoneInfo(local_timezone())).date()
    if "Today" in landed:
        got = today
    elif "Tomorrow" in landed:
        got = today + timedelta(days=1)
    else:
        m = re.search(r"(\d{1,2})/(\d{1,2})/(\d{4})", landed)
        if not m:
            return ""                       # an unrecognised form is not evidence of a wrong day
        got = date(int(m.group(3)), int(m.group(1)), int(m.group(2)))
    if got == when:
        return ""
    return (f"the row reads {landed!r}, which is {got.isoformat()}, but {when.isoformat()} "
            f"was asked for")


def _set_time(page, frame, hour: int, minute: int, when: date) -> str:
    """Tick the Time checkbox and fill the hour/minute/AM-PM segments. "" on success.

    **The typed value is read in the page's zone, and no conversion belongs here.** That took two
    measurements to get right. Overriding the page zone alone did not fix it: typing 08:00 still
    produced 10:00, because Apple's app had read its zone at startup and the tab had loaded while
    the box was `Etc/UTC`. Converting to UTC here compensated for that and looked correct. Once
    `icloud_tabs._apply_timezone` began reloading the tab so the override precedes the app load,
    the same conversion wrote 06:00 for a requested 08:00.

    So the zone is fixed one layer down, at page load, and this function types the wall-clock hour
    the owner asked for. Travel still works, because the override reads `local_timezone()`: the same
    "08:00" is eight in the morning wherever the owner is standing.

    The caller verifies the hour against what the row displays, so if the override ever fails to
    land the result carries a `due_error` instead of a reminder that is quietly two hours out.
    """
    box = json.loads(frame.evaluate(_TIME_CHECKBOX))
    if box.get("error"):
        return box["error"]
    if not box["checked"]:
        page.mouse.click(box["x"], box["y"])
        page.wait_for_timeout(1_500)

    seg = json.loads(frame.evaluate(_SEGMENT, "hour"))
    if seg.get("error"):
        return seg["error"]
    page.mouse.click(seg["x"], seg["y"])
    page.wait_for_timeout(600)

    # No conversion. The picker stores what it is typed **in the page's zone**, and the page is
    # forced to the owner's zone before the app loads (see icloud_tabs._apply_timezone). Converting to UTC
    # here was compensating for a tab that had loaded while the box was UTC: once the override
    # preceded the load, the same conversion put a reminder two hours early instead.
    # The picker is 12-hour with a separate AM/PM segment, and the segments advance as they fill.
    h12 = hour % 12 or 12
    page.keyboard.type(f"{h12:02d}{minute:02d}", delay=140)
    page.wait_for_timeout(700)

    # AM/PM will not take a keystroke typed after the minute rolls over: 08:00 came back as
    # 8:00 PM. Click that segment and set it explicitly, then nudge with arrows if the letter
    # is ignored, which some builds do.
    want_ampm = "AM" if hour < 12 else "PM"
    ampm = json.loads(frame.evaluate(_SEGMENT, "AM/PM"))
    if not ampm.get("error"):
        page.mouse.click(ampm["x"], ampm["y"])
        page.wait_for_timeout(500)
        page.keyboard.type(want_ampm[0], delay=140)
        page.wait_for_timeout(700)
        for _ in range(2):
            got = json.loads(frame.evaluate(_TIME_SEGMENTS))
            if got.get("AM/PM") == want_ampm:
                break
            page.keyboard.press("ArrowUp")
            page.wait_for_timeout(600)
    page.wait_for_timeout(500)

    got = json.loads(frame.evaluate(_TIME_SEGMENTS))
    want_ampm = "AM" if hour < 12 else "PM"
    if (got.get("hour") not in (str(h12), f"{h12:02d}")
            or got.get("minute") not in (str(minute), f"{minute:02d}")
            or got.get("AM/PM") != want_ampm):
        return f"the time picker did not take {hour:02d}:{minute:02d}, it reads {got}"
    return ""


def _clear_time(page, frame) -> str:
    """Untick the Time checkbox, so a dateless request lands as an all-day reminder. "" on success.

    Enabling the day switch leaves Apple's time checkbox ticked at 12:00 AM, so every reminder
    created here without a time became a midnight alarm rather than an all-day item. Measured
    against one the owner made on a device: the owner's read `date on, time off, ––:––` while ours
    read `date on, time on, 12:00 AM`, and the row showed "12/25/2026, 12:00 AM" where the owner's
    showed "11/22/2026". The owner asked for "in 4 months", which is a day, not a minute past
    midnight.
    """
    box = json.loads(frame.evaluate(_TIME_CHECKBOX))
    if box.get("error"):
        return box["error"]
    if not box["checked"]:
        return ""
    page.mouse.click(box["x"], box["y"])
    page.wait_for_timeout(1_500)
    after = json.loads(frame.evaluate(_TIME_CHECKBOX))
    if after.get("checked"):
        return "the time could not be cleared, so this would land as a midnight alarm"
    return ""


def _set_due(page, frame, title: str, when: date, at: tuple | None = None) -> str:
    """Set one reminder's due date, and its time when given. "" on success, else the reason."""
    page.keyboard.press("Escape")          # a popover left open would be toggled shut instead
    page.wait_for_timeout(1_200)
    frame.evaluate(_SCROLL_END)            # the row may be below the fold, and so not rendered
    page.wait_for_timeout(1_200)

    geo = json.loads(frame.evaluate(_ROW_GEO, title))
    if geo.get("error"):
        return geo["error"]
    page.mouse.move(geo["row"]["x"], geo["row"]["y"])
    page.wait_for_timeout(700)
    page.mouse.click(geo["info"]["x"], geo["info"]["y"])
    page.wait_for_timeout(2_500)

    state = frame.evaluate(_TOGGLE_DAY)
    if state not in ("on", "already on"):
        return f"could not enable the due date: {state}"
    page.wait_for_timeout(2_500)

    # Order matters, and the wrong order is not obviously wrong. Apple stores the picker's value
    # as an instant, and clearing the time re-derives the date from it in another zone: clearing
    # after choosing December 25 left the row reading 12/24/2026. Clearing first, while the
    # switch has just been enabled, then choosing the day, lands the day that was asked for.
    if at is None:
        problem = _clear_time(page, frame)
        if problem:
            return problem

    # Navigate by reading what is on screen. Counting hops from `date.today()` assumed the picker
    # opens on the current month, which is false once the popover has been used before: it sat on
    # December and the target day was reported "not visible". It also consulted the box's UTC
    # clock, which this path has no business knowing about.
    _MONTHS = ("January", "February", "March", "April", "May", "June", "July",
               "August", "September", "October", "November", "December")
    for _ in range(30):
        shown = json.loads(frame.evaluate(_CAL_MONTH))
        if shown.get("error"):
            return f"could not read the calendar month: {shown['error']}"
        cur = (shown["year"], _MONTHS.index(shown["month"]) + 1)
        if cur == (when.year, when.month):
            break
        step = _NEXT_MONTH if cur < (when.year, when.month) else _PREV_MONTH
        if frame.evaluate(step) not in ("advanced", "moved"):
            return "could not move the calendar month"
        page.wait_for_timeout(900)
    else:
        return f"the calendar would not reach {when.year}-{when.month:02d}"

    clicked = frame.evaluate(_CLICK_DAY, _day_label(when))
    if clicked != "selected":
        return clicked
    page.wait_for_timeout(1_200)

    seg = json.loads(frame.evaluate(_SEGMENTS))
    if seg.get("day") not in (str(when.day), f"{when.day:02d}"):
        return f"the picker did not take the date, it reads {seg}"

    if at is not None:
        problem = _set_time(page, frame, at[0], at[1], when)
        if problem:
            return problem

    saved = json.loads(frame.evaluate(_SAVE))
    if saved.get("error"):
        return ("the date was selected but not committed: Save was not found. Dismissing the "
                "popover does not commit, so nothing was changed.")
    # A pointer event, not el.click(). Save is a `ui-button` custom element and a synthetic click
    # on it reported success while leaving the popover open. The next reminder's title then went
    # into that popover instead of a row, which read as the list refusing new rows and made the
    # app look like it degraded with use.
    page.mouse.click(saved["x"], saved["y"])
    page.wait_for_timeout(3_500)

    for _ in range(6):
        if not frame.evaluate(_POPOVER_OPEN):
            return ""
        page.keyboard.press("Escape")
        page.wait_for_timeout(1_000)
    return "the date was saved but the popover would not close, so the app was left mid-edit"


_COMPLETE_GEO = """(title) => {
  %s
  const want = title.toLowerCase();
  const hits = [];
  walk(document, 0, (el) => {
    if (!(el.className||'').toString().split(/\\s+/).includes('reminder-item')) return;
    if ((el.className||'').toString().includes('completed')) return;
    let t = '';
    walk(el, 0, (k) => {
      if (!t && (k.className||'').toString().split(/\\s+/).includes('content-title')) {
        t = (k.innerText||'').trim();
      }
    });
    if (t && t.toLowerCase().includes(want)) hits.push({el, t});
  });
  if (hits.length === 0) return JSON.stringify({error: 'no open reminder matching that title'});
  if (hits.length > 1) {
    return JSON.stringify({error: 'more than one open reminder matches',
                           candidates: hits.map((h) => h.t).slice(0, 8)});
  }
  const row = hits[0].el;
  // A long list is virtualised and scrolled: a button below the fold has coordinates outside the
  // viewport, and a click there is discarded in silence.
  row.scrollIntoView({block: 'center'});
  let btn = null;
  walk(row, 0, (el) => {
    if (!btn && (el.className||'').toString().split(/\\s+/).includes('mark-completed')) btn = el;
  });
  if (!btn) return JSON.stringify({error: 'the completion control was not found on that row'});
  const r = btn.getBoundingClientRect();
  if (r.y < 0 || r.y > window.innerHeight || r.x < 0 || r.x > window.innerWidth) {
    return JSON.stringify({error: 'the row sits outside the viewport at y=' + Math.round(r.y)});
  }
  return JSON.stringify({title: hits[0].t,
                         x: Math.round(r.x + r.width/2), y: Math.round(r.y + r.height/2)});
}""" % _WALK


@mcp.tool()
def complete_reminder(title: str, list_name: str) -> dict:
    """Tick a reminder off. Use it when the owner says the thing is done, never to tidy a list.

    `title` matches on a substring, case-insensitively, and the match must be unique: two open
    reminders matching returns their names rather than guessing which one the owner meant. Ticking
    off the wrong thing is worse than asking, because the reminder disappears from the owner's list
    and neither of us finds out.

    Completing is reversible in the app, so this does not ask first. Deciding that something is
    done is still the owner's call, not mine.

    The result is read back from the list rather than reported from a successful click, for the
    same reason as `create_reminder`: a write that lands nowhere is the failure this server exists
    to prevent.

    **A repeating reminder does not disappear when ticked**, it rolls to its next occurrence, so a
    weekly task still shows afterwards with a later date. That is success, and the reply says so
    with `repeated: true` and the new due date, rather than calling a completed reminder a failure.
    """
    try:
        with app_lock(APP), icloud_app(APP, URL, FRAME, READY) as (frame, page):
            problem = _open_list(frame, page, list_name)
            if problem:
                return {"error": problem}

            before = {r["title"]: r for r in _items(frame) if not r["completed"]}
            spot = json.loads(frame.evaluate(_COMPLETE_GEO, title))
            if spot.get("error"):
                out = {"error": spot["error"], "list": list_name}
                if spot.get("candidates"):
                    out["candidates"] = spot["candidates"]
                elif not before:
                    out["open_reminders"] = []
                else:
                    out["open_reminders"] = sorted(before)[:20]
                return out

            target = spot["title"]
            was_due = (before.get(target) or {}).get("due", "")
            page.mouse.click(spot["x"], spot["y"])
            page.wait_for_timeout(3_000)

            after = {r["title"]: r for r in _items(frame) if not r["completed"]}
            if target not in after:
                return {"completed": True, "list": list_name, "title": target,
                        "was_due": was_due or None, "repeated": False,
                        "open_before": len(before), "open_after": len(after)}

            now_due = after[target].get("due", "")
            if was_due and now_due and now_due != was_due:
                # Still listed, but its date moved: Apple rolled the series forward, which is what
                # completing an occurrence of a repeating reminder does.
                return {"completed": True, "list": list_name, "title": target, "repeated": True,
                        "was_due": was_due, "next_due": now_due,
                        "note": "this reminder repeats, so it stays on the list with a new date"}
            return {"completed": False, "list": list_name, "title": target,
                    "error": "the reminder is still open after clicking its completion control",
                    "was_due": was_due or None}
    except NeedsApproval as exc:
        # Ticking something off is a write like any other, and losing it means the owner does the thing,
        # tells me, and finds it still open tomorrow. Queued on the same terms as creating.
        if not blocked():
            return {"error": str(exc), "needs_device_approval": False,
                    "detail": "not queued: this is a deferral rather than a block."}
        item = icloud_queue.enqueue("complete_reminder",
                                    {"title": title, "list_name": list_name},
                                    origin="complete_reminder")
        if item is None:
            return {"completed": False, "queued": False,
                    "error": "iCloud is waiting for your approval and I could not record this to "
                             "run later, so it is not saved. Tell me again once you have approved.",
                    "needs_device_approval": True}
        return {"completed": False, "queued": True, "queue_id": item,
                "waiting_on": "your approval of iCloud web access",
                "will_run": "by itself, within ten minutes of you approving",
                "expires_after_hours": icloud_queue.MAX_AGE_HOURS,
                "detail": str(exc)}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}


@mcp.tool()
def create_reminder(title: str, list_name: str, due: str = "") -> dict:
    """Add a reminder: something the owner has to do, optionally with a time it is due.

    This is the right tool for "remember to…", "remind me…", "don't let me forget…", **including
    when the request names a time**. A clock in the sentence sets `due`; it does not make the
    thing a calendar event. Use `create_event` only for something that occupies the owner's time.

    It appears on every device signed into the account.

    `due` is optional: a date as YYYY-MM-DD, or a date and time as "YYYY-MM-DD HH:MM" on a
    24-hour clock. Both are set in the reminder's own fields, never written into the title: the
    app does not parse dates typed into the name, so a date left there would be text forever.

    The picker itself is 12-hour with a separate AM/PM segment; the conversion happens here so
    the tool's own contract stays unambiguous.

    The result is read back from the list rather than reported from a successful click,
    because a write that silently lands nowhere is the exact failure this server exists to
    prevent. That applies to the due date too: if it cannot be set, the reminder is still
    created and `due_error` says why, rather than claiming a date that is not there.
    """
    when, at = None, None
    if due:
        raw = due.strip().replace("T", " ")
        parts = raw.split()
        try:
            when = date.fromisoformat(parts[0])
        except ValueError:
            return {"error": f"due must start with a date as YYYY-MM-DD, got {due!r}"}
        if len(parts) > 1:
            try:
                hh, mm = parts[1].split(":")[:2]
                at = (int(hh), int(mm))
                if not (0 <= at[0] <= 23 and 0 <= at[1] <= 59):
                    raise ValueError
            except ValueError:
                return {"error": f"the time in due must be HH:MM on a 24-hour clock, got {due!r}"}
    try:
        with app_lock(APP), icloud_app(APP, URL, FRAME, READY) as (frame, page):
            problem = _open_list(frame, page, list_name)
            if problem:
                return {"error": problem}
            # Never start typing while a popover is open, whoever left it there.
            for _ in range(6):
                if not frame.evaluate(_POPOVER_OPEN):
                    break
                page.keyboard.press("Escape")
                page.wait_for_timeout(1_000)

            # Several attempts at getting the title in. Each one verifies focus and then the
            # text before committing, so an attempt that fails writes nothing at all: retrying
            # cannot duplicate. Measured, a single attempt failed about one time in six.
            after, typed = [], False
            for attempt in range(4):
                if not frame.evaluate(CLICK, ["rm-new-reminder", 0]):
                    return {"error": "could not find the new-reminder control"}
                page.wait_for_timeout(1_800)

                # Apple appends the new row at the end, and the list is virtualised: in a list
                # longer than the viewport the empty row is never rendered, so looking for it here
                # reported "no empty row to type into" while the row existed perfectly well.
                frame.evaluate(_SCROLL_END)
                page.wait_for_timeout(1_200)

                spot = json.loads(frame.evaluate(_FOCUS_NEW_ROW))
                if spot.get("error"):
                    if attempt == 0:
                        continue
                    return {"error": f"the new row did not appear: {spot['error']}"}
                # Click until focus is actually in the field. One click in six lands without
                # taking focus, and then the typing is silently discarded.
                for _ in range(4):
                    page.mouse.click(spot["x"], spot["y"])
                    page.wait_for_timeout(900)
                    if json.loads(frame.evaluate(_FOCUSED_TEXT)).get("focused"):
                        break
                    spot = json.loads(frame.evaluate(_FOCUS_NEW_ROW))
                    if spot.get("error"):
                        break
                if spot.get("error") or not json.loads(
                        frame.evaluate(_FOCUSED_TEXT)).get("focused"):
                    continue          # let the outer attempt loop try again from the add control

                page.keyboard.type(title, delay=25)
                page.wait_for_timeout(500)
                # Confirm the text is in the field before committing it. Pressing Enter on an
                # empty field is what created the stray "New Reminder" rows.
                held = json.loads(frame.evaluate(_FOCUSED_TEXT)).get("text", "")
                if title.strip() not in held:
                    page.keyboard.press("Escape")
                    page.wait_for_timeout(800)
                    continue
                # Tab, not Enter. Enter commits the row **and opens another**, which is where
                # every stray "New Reminder" came from: one per created reminder, left behind as
                # soon as the empty row it opened got committed. Measured on a healthy app, four
                # gestures side by side: Enter added two rows, Tab exactly one, both with the
                # title committed. An earlier run of that comparison said Enter was innocent, but
                # it ran while Apple's access grant had lapsed and the list was a phantom.
                page.keyboard.press("Tab")
                page.wait_for_timeout(4_000)
                frame.evaluate(_SCROLL_END)
                page.wait_for_timeout(1_500)

                after = [r["title"] for r in _items(frame)]
                if title.strip() in after:
                    typed = True
                    break
                # Nothing landed. Clear the blank row this attempt opened, so the next one is not
                # blocked by it: with an unused blank present Apple stops producing a fresh row.
                dismiss_alert(frame, page)
                page.keyboard.press("Escape")
                page.wait_for_timeout(1_000)

            if not typed:
                return {"error": "the reminder was typed but did not appear in the list; "
                                 "nothing is claimed as created",
                        "list_now": after[:10]}

            result = {"created": True, "list": list_name, "title": title.strip()}
            if when is not None:
                problem = _set_due(page, frame, title.strip(), when, at)
                row = next((r for r in _items(frame) if r["title"] == title.strip()), None)
                landed = (row or {}).get("due")

                # Apple's *first* save of a date-only due lands a day early: local midnight
                # rendered from UTC. Measured on a fresh browser across four dates, including a
                # month and a year boundary, always exactly one day. The picker reads back the
                # right day, so nothing about how the day is chosen avoids it.
                #
                # A second save of the same date is faithful. So the repair is to save it again,
                # not to compute an offset: no timezone arithmetic, nothing that would be wrong in
                # another zone, and if Apple ever fixes the first save this stops firing by itself.
                if not problem and landed and _row_date(landed) not in (None, when):
                    problem = _set_due(page, frame, title.strip(), when, at)
                    row = next((r for r in _items(frame)
                                if r["title"] == title.strip()), None)
                    landed = (row or {}).get("due")
                if problem or not landed:
                    result["due_error"] = problem or "the due date did not appear on the row"
                    result["due"] = None
                else:
                    result["due"] = landed
                    result["note"] = ("due is the row's own text, so report it exactly as it "
                                      "reads rather than the date that was requested. A time in "
                                      "it means an alarm at that minute; no time means all day.")
                    # The hour the row shows must be the hour the owner asked for. A timezone regression
                    # is otherwise invisible: the reminder exists, looks right in the response,
                    # and fires two hours out. Checked against the displayed text, in the owner's zone.
                    if at is not None:
                        # The meridiem is part of the comparison. Without it "10:00" matches
                        # 10:00 AM as readily as the 10:00 PM that 22:00 means, and AM/PM is the
                        # segment most likely to be wrong: it refuses a keystroke typed after the
                        # minute segment rolls over, which is why it is clicked explicitly.
                        h12 = at[0] % 12 or 12
                        mer = "AM" if at[0] < 12 else "PM"
                        want = {f"{h12}:{at[1]:02d} {mer}", f"{h12:02d}:{at[1]:02d} {mer}"}
                        # A 24-hour rendering is only safe to accept from 13:00 up. Below that it
                        # is ambiguous: "12:00" appears inside "12:00 AM", and "10:00" inside
                        # "10:00 PM", which is how the meridiem slipped through in the first place.
                        if at[0] >= 13:
                            want.add(f"{at[0]}:{at[1]:02d}")
                        if not any(w in landed for w in want):
                            result["due_error"] = (
                                f"the row reads {landed!r} but {at[0]:02d}:{at[1]:02d} "
                                f"{local_timezone()} was asked for, which the row should show as "
                                f"{h12}:{at[1]:02d} {mer}")
                    day_problem = _check_day(landed, when)
                    if day_problem and "due_error" not in result:
                        result["due_error"] = day_problem
            return result
    except NeedsApproval as exc:
        # Only when the latch is set: the same exception also carries the overnight deferral, and
        # calling that "waiting on your approval" is a lie the owner cannot act on.
        if not blocked():
            return {"created": False, "queued": False, "error": str(exc),
                    "needs_device_approval": False,
                    "detail": "not queued: this is a deferral rather than a block."}
        # Queued rather than lost, the same as create_note.
        item = icloud_queue.enqueue("create_reminder",
                                    {"title": title, "list_name": list_name, "due": due},
                                    origin="create_reminder")
        if item is None:
            # The queue itself could not be written, so nothing will come back for this later.
            # Saying "queued" here would be the worst answer available: the owner would stop thinking
            # about it and nothing would ever run.
            return {"created": False, "queued": False,
                    "error": "iCloud is waiting for your approval and I could not even record "
                             "this to run later, so it is not saved anywhere. Ask me again once "
                             "you have approved.",
                    "needs_device_approval": True}
        return {"created": False, "queued": True, "queue_id": item,
                "waiting_on": "your approval of iCloud web access",
                "will_run": "by itself, within ten minutes of you approving",
                "expires_after_hours": icloud_queue.MAX_AGE_HOURS,
                "detail": str(exc)}
    except Exception as exc:
        return {"error": f"{type(exc).__name__}: {exc}"}
