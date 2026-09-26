## Why

An agent asked "reminders or emails pending?" got back titles plus a relative due string for reminders, and sender plus subject for mail, and had to answer "I haven't opened the emails, so I can't verify their contents". A browser-backed call takes 20 to 90 seconds, so a response too thin to act on is the expensive failure: every read tool must return the metadata its source already holds, in one consistent shape, so the calling agent can decide its next step from one response.

Measured on 2026-09-26 against 2cdefe6, raw over stdio:

| Tool | Time | What came back |
| --- | --- | --- |
| reminder_lists | 19.8s | list names only |
| list_reminders | 4.1s | id, title, due as display text ("Tomorrow, 6:00 PM, Weekly"), priority, flagged, completed; one list per call |
| completed_reminders | 3.1s | same plus completed_at, but due is "YYYY-MM-DD" here |
| list_mail | 2.4s | uid, from (raw header), subject, date (raw RFC 2822), unread, answered |
| search_mail | 90.6s | same as list_mail, 3 results; scanned_recent 300 |
| read_mail | 1.7s | from, to, subject, date, body capped at 4000 runes |
| list_events | 1.8s | calendar, summary, start, end, location, uid; a shared event appears twice |
| search_contacts | 1.5s | name, emails, phones |
| notes_* | not measured | the grant was pending on the devices |

## What Changes

- **BREAKING (output contract)**: most read tools return more fields, and fields that were display text become ISO 8601 with offset in the owner's zone, with the display string kept beside them (`due_display`, `date_raw`). Clients that parsed the old text shapes must read the new fields.
- **BREAKING (errors)**: failures come back as MCP results with `isError: true` instead of successful results carrying an `error` key. `needs_login` and `needs_device_approval` stay as fields of the error payload.
- Every tool returns `structuredContent` with a declared `outputSchema`, and keeps the JSON text content for older clients.
- Every tool carries MCP annotations: `readOnlyHint` on every read, `destructiveHint` on `delete_event`, `update_event`, `update_note` and `sign_out`, `idempotentHint` where it holds.
- Every tool description states the fields it returns, units, time zone, and truncation rules.
- Reminders: `list_reminders` reads the CloudKit record cache (the path `completed_reminders` already syncs) and keeps the DOM scrape as a fallback; returns notes, ISO due plus `all_day`, created, modified, recurrence, alarms, URL, subtasks or parent, tags; returns every list when `list_name` is omitted. `complete_reminder` accepts the returned id.
- Mail: `list_mail` and `search_mail` return from/to/cc/reply-to as name plus address, message-id, in-reply-to, every flag, size, internal date, ISO date plus raw header, attachments from BODYSTRUCTURE, and a ~300-character snippet from a partial `BODY.PEEK`. `read_mail` returns the same plus attachments always, falls back from HTML to text, and paginates the body with an offset. The local search pass batches headers into one UID-set FETCH.
- Calendar: events return description, all_day, status, organizer, attendees with participation status, recurrence rule, recurring-instance flag, URL, alarms, created and last-modified; an event under several calendars merges into one entry listing its calendars.
- Contacts: organization, job title, birthday, addresses, notes, URLs, nickname, and the TYPE label of each email and phone.
- Notes: created and modified as ISO dates, pinned, locked, shared, checklist items with done state, attachments by name and kind, tags; `notes_read` continues past its cap with an offset. The design decides whether Notes CloudKit records can replace the DOM scrape.
- Health (added 2026-09-26 at the owner's request): rollups name the unit and sources of every value from the stored samples; `health_days` adds heart-rate min, max and average; `health_sleep` gives onset and wake in the owner's zone and splits asleep, awake and in-bed minutes; `health_effort` adds sample counts and max and average bpm; `health_recovery` names its window dates and a percentage delta.
- Every id a follow-up tool accepts is returned (reminder id, mail uid, event uid, note identifier).

## Capabilities

### New Capabilities
- `mcp-transport`: the output envelope shared by every tool: structured content and output schemas, `isError` failures, annotations, one date format, descriptions that state fields and truncation, bounded bodies with continuations.
- `mail`: the metadata `list_mail`, `search_mail` and `read_mail` return, attachment reporting, snippets, body pagination, and the batched search pass.
- `calendar-contacts`: the event and contact fields returned, and merging an event shared across calendars.
- `notes-reminders-browser`: reminder reads from the CloudKit cache with a DOM fallback, the reminder and note fields returned, id-based completion, all-lists reads, and note pagination.
- `health`: the units, provenance and per-tool fields the Health rollups return.

### Modified Capabilities
None: `openspec/specs/` holds no capabilities yet (the 2026-09-18 go-rewrite archive was not synced), so these land as new capabilities under the names that archive used.

## Impact

- Code: `internal/mcpserver` (result envelope, annotations, output schemas, descriptions), `internal/mail`, `internal/dav`, `internal/reminders` (CloudKit keys and a record-backed `list_reminders`), `internal/notes`, and every handler that returns `ResultJSON` or an `error` key.
- Clients: any client or skill that parsed the old text payloads or the `error` key on a successful result. The README tools table and descriptions change with it.
- Performance: must stay inside the browser lock budget; richer output comes from data already fetched or cheap protocol fetches (IMAP BODYSTRUCTURE and partial BODY.PEEK, one batched FETCH), never extra clicks per row. `search_mail` is expected to get faster.
- Side effects: none added; mail stays EXAMINE plus BODY.PEEK and nothing marks a message seen.
- Verification: `go test ./...` with fixtures for every new field, one `./verify.sh` round against a real account, and before/after payload and timing per tool in the PR.
