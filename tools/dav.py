#!/usr/bin/env python3
"""Calendar and contacts over CalDAV, and the confirmed calendar delete.

Reminders are deliberately absent from this module. `caldav.icloud.com` exposes a legacy
Reminders store that Apple left behind when Reminders moved to CloudKit: it lists Today, Home and
Work, while the account's real lists are the ones every device shows. Writes to it succeed, return
a uid, and survive read-back, yet are invisible everywhere. That cost a real reminder before it
was noticed, so reminders live in `reminders.py`, on the browser. Never restore them here.

This module takes no lock. DAV needs none, which is the whole reason a Notes call cannot stall a
calendar read even though both now run in one process.
"""
import asyncio
import os
from datetime import date, datetime, timedelta, timezone
from typing import Any
from zoneinfo import ZoneInfo

import caldav
import vobject
from mcp.server.mcpserver import Context
from app import PW, TZ, USER, _day, ask_approval, mcp

def _principal():
    client = caldav.DAVClient(url="https://caldav.icloud.com", username=USER, password=PW)
    return client.principal()


def _collections():
    """Split collections by what they hold; iCloud keeps events and todos apart."""
    events, todos = {}, {}
    for c in _principal().calendars():
        try:
            comps = c.get_supported_components()
        except Exception:
            continue
        name = c.get_display_name()
        if "VEVENT" in comps:
            events[name] = c
        if "VTODO" in comps:
            todos[name] = c
    return events, todos


def _zone(name: str | None) -> ZoneInfo:
    """The named zone to store an event in, defaulting to the owner's."""
    if not name:
        return TZ
    try:
        return ZoneInfo(name)
    except Exception as exc:
        raise ValueError(f"unknown timezone {name!r}: {exc}") from exc


def _parse(when: str, tz: str | None = None) -> datetime:
    """Accept an ISO timestamp, naive or not, and return it in a named zone.

    An offset-carrying value is **converted**, not passed through. `fromisoformat` gives a
    fixed-offset tzinfo for "…+02:00", which the calendar layer serialises by taking the wall
    clock and calling it UTC: asking for 22:00+02:00 stored 22:00Z, which is 00:00 the next day
    one zone east. Converting to the named zone first gives icalendar a real TZID to write.

    `tz` names the zone the event happens in, which is not always the owner's. A flight leaving at
    00:55 local is a 00:55 event in that airport's zone; storing it as the same instant in the
    owner's zone shows the wrong number on every device and forces the real time into the
    description as prose. Naming the zone stores a real TZID, so the calendar shows the local
    departure time where it departs and the right hour at home, which is the point of timezones.
    """
    zone = _zone(tz)
    dt = datetime.fromisoformat(when)
    return dt.replace(tzinfo=zone) if dt.tzinfo is None else dt.astimezone(zone)


# A window wide enough to mean "every event", used by the fallback lookup below. CalDAV has no
# "give me everything" query, and an open-ended one is a per-server guess, so the range is stated.
_ALL_TIME = (datetime(2000, 1, 1, tzinfo=TZ), datetime(2040, 1, 1, tzinfo=TZ))


# Which calendar an event goes to when the caller names none. Set it in the env file: the
# fallback below is alphabetical, and on a real account the first name is often a shared
# calendar, so an event meant to be private lands somewhere other people can see.
DEFAULT_CALENDAR = os.environ.get("AGENT_DEFAULT_CALENDAR", "")


def _default_calendar(events: dict) -> str:
    """The configured calendar, and only then whichever one sorts first.

    Alphabetical order is not a statement about whose calendar it is, and the failure is quiet,
    because the event does get created either way.
    """
    if DEFAULT_CALENDAR in events:
        return DEFAULT_CALENDAR
    return next(iter(sorted(events)))


def _find_event(uid: str) -> tuple[Any, str] | None:
    """The event object with this uid, and the calendar holding it.

    This is the third attempt, and the first two are worth recording because both look right.

    `cal.objects()` walks every event in every calendar. Listing them is fast, but reading one
    fetches it in its own request, so 1,689 events across the owner's three calendars is 1,689 round trips.
    That is not a slow function, it is a delete that never returns, and it is why a real deletion
    timed out and left the owner with two copies of a flight to clean up by hand.

    `cal.event_by_uid()` is the obvious replacement and **iCloud rejects it**, along with
    `search(uid=...)`: both answer `412 Precondition Failed` on every calendar in this account. So
    the lookup that the library documents for exactly this purpose is not available here.

    What iCloud does allow is fetching an event by URL, and it stores events created through this
    server at `<calendar>/<uid>.ics`, which is one request and about half a second. An event
    created on the owner's phone may live at a different href, so the fallback is a single windowed
    `search` per calendar: one REPORT that returns everything inline, a few seconds rather than
    minutes, and correct whatever the href.
    """
    events, _ = _collections()
    for name, cal in events.items():
        try:
            obj = caldav.Event(client=cal.client,
                               url=str(cal.url).rstrip("/") + "/" + uid + ".ics", parent=cal)
            obj.load()
        except Exception:
            continue
        v = obj.icalendar_component
        if v is not None and str(v.get("uid", "")) == uid:
            return obj, name

    for name, cal in events.items():
        try:
            found = cal.search(start=_ALL_TIME[0], end=_ALL_TIME[1], event=True)
        except Exception:
            continue
        for obj in found:
            v = obj.icalendar_component
            if v is not None and str(v.get("uid", "")) == uid:
                return obj, name
    return None


def _fmt(value: Any) -> str:
    if isinstance(value, datetime):
        return value.astimezone(TZ).isoformat(timespec="minutes")
    if isinstance(value, date):
        return value.isoformat()
    return str(value)



@mcp.tool()
def list_calendars() -> dict:
    """List the calendars and reminder lists this account exposes over CalDAV."""
    events, _ = _collections()
    return {"calendars": sorted(events), "timezone": str(TZ),
            "note": "reminders are not here; use the reminder tools, which reach the "
                    "store the owner's devices actually use"}


@mcp.tool()
def list_events(days_ahead: int = 7, days_back: int = 0, calendar: str | None = None,
                start: str | None = None, end: str | None = None) -> dict:
    """Read calendar events in a window, relative to today or between two dates.

    `start` and `end` are `YYYY-MM-DD` and **inclusive**, and either one overrides the relative
    window entirely, returning every event in that range however far in the past. Without them
    the window is `days_back` before now to `days_ahead` after.

    A window given as dates is anchored to the owner's local midnight rather than to the current
    time, so `start` and `end` on the same day is that whole day and not the hours since now.
    """
    events, _ = _collections()
    now = datetime.now(TZ)
    try:
        if start or end:
            first = _day(start, "start") if start else now.date()
            last = _day(end, "end") if end else first
            if last < first:
                return {"error": f"end {last} is before start {first}"}
            lo = datetime.combine(first, datetime.min.time(), tzinfo=TZ)
            # Inclusive: the day named by `end` is in the window, so the bound is its midnight.
            hi = datetime.combine(last + timedelta(days=1), datetime.min.time(), tzinfo=TZ)
        else:
            lo, hi = now - timedelta(days=days_back), now + timedelta(days=days_ahead)
    except ValueError as exc:
        return {"error": str(exc)}
    out = []
    for name, cal in events.items():
        if calendar and name != calendar:
            continue
        # expand=True matters: without it a recurring event returns its master
        # occurrence, so a weekly meeting reports the date it was first created
        # rather than the instance actually falling in this window.
        for e in cal.search(start=lo, end=hi, event=True, expand=True):
            v = e.icalendar_component
            out.append({
                "calendar": name,
                "summary": str(v.get("summary", "")),
                "start": _fmt(v.get("dtstart").dt if v.get("dtstart") else ""),
                "end": _fmt(v.get("dtend").dt if v.get("dtend") else ""),
                "location": str(v.get("location", "")) or None,
                "uid": str(v.get("uid", "")),
            })
    # The window is reported rather than assumed, because a caller that passed no bounds and a
    # caller whose bounds were ignored would otherwise read the same result identically.
    return {"count": len(out),
            "window": {"start": lo.isoformat(timespec="minutes"),
                       "end": hi.isoformat(timespec="minutes"),
                       "absolute": bool(start or end)},
            "events": sorted(out, key=lambda e: e["start"])}


@mcp.tool()
def create_event(summary: str, start: str, end: str | None = None,
                 calendar: str | None = None, location: str | None = None,
                 description: str | None = None, timezone: str | None = None) -> dict:
    """Create an event: something that occupies time. A meeting, appointment, booking or class.

    **Not for a task with a deadline.** "Remember to call someone at 3pm" is a reminder with a due
    time, and `create_reminder` sets both the day and the time. An event cannot be ticked off, so
    a task filed here is silently useless.

    Times are ISO 8601; naive values are read in the local timezone.

    **`timezone` is where the event happens**, an IANA name, and it is what a flight or a call in
    another country needs. Give the local wall-clock time there and name the zone: a 00:55
    departure is `start="2027-01-07T00:55"` with that airport's zone. Do not convert to the
    owner's zone by hand and do not record the real time in the description; that shows the wrong
    number on every device.

    The stored start is read back from the server rather than echoed, because the request
    and what the server kept are not always the same thing.
    """
    events, _ = _collections()
    if not events:
        return {"error": "no writable calendars"}
    name = calendar or _default_calendar(events)
    if name not in events:
        return {"error": f"unknown calendar {name}", "available": sorted(events)}

    try:
        dtstart = _parse(start, timezone)
        dtend = _parse(end, timezone) if end else dtstart + timedelta(hours=1)
    except ValueError as exc:
        return {"error": str(exc)}
    ev = events[name].save_event(dtstart=dtstart, dtend=dtend, summary=summary,
                                 location=location or "", description=description or "")
    stored = ev.icalendar_component
    return {
        "created": True,
        "calendar": name,
        "uid": str(stored.get("uid", "")),
        "summary": str(stored.get("summary", "")),
        "stored_start": _fmt(stored.get("dtstart").dt),
        "stored_end": _fmt(stored.get("dtend").dt) if stored.get("dtend") else None,
        "stored_local": _local(stored.get("dtstart").dt),
    }


def _local(value: Any) -> str | None:
    """The wall clock and zone the event was actually stored in.

    `_fmt` answers "when is this for the owner", which is the right default for reading a
    calendar. It is
    the wrong answer for checking a write: converting everything to the owner's zone makes a
    foreign departure read the same whichever zone it was stored in, so the one mistake this
    parameter exists to prevent is invisible in the confirmation.
    """
    if not isinstance(value, datetime) or value.tzinfo is None:
        return None
    return f"{value.isoformat(timespec='minutes')} ({value.tzinfo})"


@mcp.tool()
async def update_event(uid: str, ctx: Context, summary: str | None = None,
                       start: str | None = None, end: str | None = None,
                       location: str | None = None, description: str | None = None,
                       timezone: str | None = None) -> dict:
    """Change an existing event in place: move it, rename it, or correct its details.

    **This is what to use instead of deleting and recreating.** Doing that loses the event's
    identity: invitations, the entry on other people's calendars, and any reply already given all
    hang off the uid. It also leaves two events if the delete then fails, and the owner is the one
    who has to clean that up.

    Only the fields given are touched; anything omitted keeps its current value. Pass `start` with
    `timezone` to move an event into the zone it really happens in, the same way `create_event`
    does.

    Silent for an event only the owner is on. An event with other attendees asks first, because
    moving it reaches their calendars too, and that is a message to other people rather than a
    note to oneself.
    """
    if all(v is None for v in (summary, start, end, location, description)):
        return {"error": "nothing to change: give at least one of summary, start, end, "
                         "location or description"}

    found = await asyncio.to_thread(_find_event, uid)
    if found is None:
        return {"error": f"no event with uid {uid}"}
    obj, name = found
    comp = obj.icalendar_component
    if comp is None:
        return {"error": f"event {uid} has no readable component"}

    was = {"summary": str(comp.get("summary", "")),
           "start": _fmt(comp.get("dtstart").dt) if comp.get("dtstart") else None,
           "end": _fmt(comp.get("dtend").dt) if comp.get("dtend") else None}

    attendees = comp.get("attendee")
    if attendees is not None:
        count = len(attendees) if isinstance(attendees, list) else 1
        refused = await ask_approval(
            ctx,
            f"Change “{was['summary']}” on your {name} calendar? It has {count} other "
            f"attendee{'s' if count != 1 else ''}, so they will see the change.")
        if refused:
            return {"updated": False, "reason": refused}

    try:
        if start is not None:
            comp.pop("DTSTART", None)
            comp.add("dtstart", _parse(start, timezone))
        if end is not None:
            comp.pop("DTEND", None)
            comp.add("dtend", _parse(end, timezone))
    except ValueError as exc:
        return {"error": str(exc)}

    for key, value in (("SUMMARY", summary), ("LOCATION", location),
                       ("DESCRIPTION", description)):
        if value is not None:
            comp.pop(key, None)
            comp.add(key.lower(), value)

    # A changed event that keeps its SEQUENCE is one other clients are entitled to ignore: the
    # number is how CalDAV says "this is a newer revision of the same event". Without bumping it a
    # write can succeed on the server and never reach the owner's phone, which looks identical to the
    # update silently failing.
    comp["SEQUENCE"] = int(comp.get("sequence", 0)) + 1
    comp.pop("LAST-MODIFIED", None)
    comp.add("last-modified", datetime.now(TZ))

    await asyncio.to_thread(obj.save)

    fresh = await asyncio.to_thread(_find_event, uid)
    stored = fresh[0].icalendar_component if fresh else comp
    return {
        "updated": True,
        "calendar": name,
        "uid": uid,
        "was": was,
        "summary": str(stored.get("summary", "")),
        "stored_start": _fmt(stored.get("dtstart").dt) if stored.get("dtstart") else None,
        "stored_end": _fmt(stored.get("dtend").dt) if stored.get("dtend") else None,
        "stored_local": _local(stored.get("dtstart").dt) if stored.get("dtstart") else None,
    }


@mcp.tool()
def search_contacts(query: str, limit: int = 10) -> dict:
    """Find contacts by name, email, or phone. Case-insensitive substring match."""
    q = query.strip().lower()
    if not q:
        return {"error": "empty query"}
    out = []
    for raw in _all_vcards():
        try:
            card = vobject.readOne(raw)
        except Exception:
            continue
        name = str(getattr(card, "fn", None).value).strip() if hasattr(card, "fn") else ""
        emails = [str(e.value).strip() for e in card.contents.get("email", [])]
        phones = [str(t.value).strip() for t in card.contents.get("tel", [])]
        haystack = " ".join([name, *emails, *phones]).lower()
        if q in haystack:
            out.append({"name": name, "emails": emails, "phones": phones})
        if len(out) >= limit:
            break
    return {"count": len(out), "contacts": out}



# ------------------------------------------------------- the confirmed calendar delete
#
# One approval mechanism, and it is the protocol's own. `ask_approval` in `app.py` is the only
# gate this server has: the question goes to the client as an MCP elicitation, the client renders
# it, and the tool executes only on an explicit approval. Nothing is sent from this process, so
# the server needs no messaging credential to ask a question.


@mcp.tool()
async def delete_event(uid: str, ctx: Context) -> dict:
    """Delete a calendar event by uid. Asks the owner first, and waits for the answer.

    The asking is an MCP **elicitation**: the question goes to the client over the protocol and
    the client renders it however it asks its user. Nothing leaves this process by any other
    channel, so the server needs no credential of its own to ask.

    The tiering lives here rather than in the client's configuration: it is this code that decides
    a delete is worth asking about while creating an event solo is not. The client only renders
    the question.
    """
    found = await asyncio.to_thread(_find_event, uid)
    if found is None:
        return {"error": f"no event with uid {uid}"}
    obj, name = found
    v = obj.icalendar_component
    start = v.get("dtstart") if v is not None else None
    question = (f"Delete “{v.get('summary', '') if v is not None else ''}” from your {name} "
                f"calendar, starting "
                f"{_fmt(start.dt) if start else 'at an unknown time'}?")

    refused = await ask_approval(ctx, question)
    if refused:
        return {"deleted": False, "reason": refused}

    summary = str(v.get("summary", "")) if v is not None else ""
    await asyncio.to_thread(obj.delete)
    # The confirmation is the result. This server sends the owner nothing of its own, so what the
    # owner needs to know about a completed write is here for the client to relay.
    return {"deleted": True, "calendar": name, "uid": uid, "summary": summary,
            "tell_the_owner": f"Deleted the event: {summary}"}
