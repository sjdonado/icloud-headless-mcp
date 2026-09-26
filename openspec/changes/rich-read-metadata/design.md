## Context

See proposal.md, Why, for the measured payloads. The state that shapes the approach:

- Every handler returns through `mcpserver.ResultJSON` (a JSON text block) or `mcpserver.ErrorResult` (also a successful text block with an `error` key). About 130 sites build an `error` key, so failures are indistinguishable from data at the protocol level.
- `mcp-go` v1.1.0, already pinned, supports `CallToolResult.StructuredContent`, `IsError`, `WithRawOutputSchema` and the annotation options (`WithReadOnlyHintAnnotation`, `WithDestructiveHintAnnotation`, `WithIdempotentHintAnnotation`, `WithOpenWorldHintAnnotation`). No new dependency.
- Tool registration lives in one table (`internal/mcpserver/server.go` `Tools`, and `params.go` `ToolParams`), so annotations, schemas and descriptions can be declared next to it.
- Reminders: `completed_reminders` already runs a CloudKit sync in the Reminders page's main world (`ckSyncEval`, `ckSyncJS`), passing the record fields it wants as `ckKeys`. `list_reminders` instead scrapes rendered DOM rows (virtualised, and the notes line is read then discarded).
- Mail: IMAP via go-imap v2 on an EXAMINE'd mailbox; `headerFetch` asks for FROM, SUBJECT and DATE; the local search pass fetches one UID per round trip over the newest 300 messages.
- Calendar: the CalDAV query already requests DESCRIPTION, RRULE, RECURRENCE-ID, STATUS, ORGANIZER and ATTENDEE (and names its props, because iCloud answers allprop with empty data); `eventRow` returns none of them.
- `sign_out` (merged in #16) is on main and is in scope for annotations.

## Goals / Non-Goals

**Goals:**
- One envelope for every tool (structured content, schema, `isError`, annotations), introduced centrally so handlers change as little as possible.
- Richer payloads only from data already fetched or cheap protocol fetches.
- Before/after payload and timing per tool, measured the same way as the proposal's table.

**Non-Goals:**
- New tools, new write capabilities, or changes to which tools ask for approval.
- Changing the approval tiering, quiet hours, or the login and recovery flow.
- A new CalDAV or IMAP library.

## Decisions

**1. Mark failures in one place.** `ResultJSON` sets `IsError` when the payload is a map with a non-empty `error`, and `ErrorResult` always does. Routing fields (`needs_login`, `needs_device_approval`) stay in the payload. Alternative: return `mcp.NewToolResultError` at each of ~130 sites, rejected as a large mechanical diff with no behavior gain. The central rule is tested against every tool's error path fixture.

**2. Structured content from the same map.** `ResultJSON` fills `StructuredContent` with the payload and keeps the text block (`json.Marshal` of the same value), so both always agree. Output schemas are hand-written JSON Schema per tool, declared next to `ToolParams` (`internal/mcpserver/schemas.go`), with `additionalProperties: true` so a field added later does not break validating clients. Alternative: generate schemas from Go structs with `WithOutputSchema[T]`, rejected because the payloads are built as maps across packages today; converting them all to structs is a rewrite. A test validates each tool's fixture payload against its declared schema.

**3. Annotations and descriptions in the registry.** `ToolDef` gains annotation fields and a longer description stating fields, units, zone and truncation. `readOnlyHint` follows the existing tier (`TierReadOnly`), `destructiveHint` is set on `delete_event`, `update_event` (it overwrites fields in place), `update_note` and `sign_out`, `idempotentHint` on reads, `complete_reminder`, `delete_event` and `sign_out`. A test asserts every tool declares annotations and that every read is read-only.

**4. Dates through one helper.** A shared `internal/timefmt` (or a function in `config`) turns a `time.Time` into ISO 8601 in the owner's zone, and an all-day date into `YYYY-MM-DD` with `all_day: true`. Every package uses it; display strings move to `*_display` or `*_raw`.

**5. Reminders from CloudKit records.** `list_reminders` reuses the sync `completed_reminders` runs, with more `ckKeys`, filtered to open reminders, and keeps the DOM scrape as the fallback with `source: page`. Field names are not guessed: before coding, probe a real Reminder record with `tools/cdp-eval.mjs` (per AGENTS.md, Debugging the web apps) and record which keys exist and how notes, recurrence, alarms and subtasks are encoded; decode notes the way `ckTitle` decodes TitleDocument. Alternative: parse the notes line out of the DOM leaves, rejected because the page is virtualised and shows only rendered rows. Probe results (2026-09-26, `tools/cdp-eval.mjs` against the Reminders zone): a Reminder record holds `TitleDocument` and `NotesDocument` (both the same compressed document, decoded by `ckTitle`), `CreationDate`, `LastModifiedDate`, `DueDate` with `AllDay` and `TimeZone`, `CompletionDate`, `Priority`, `Flagged`, `List`, and id lists `AlarmIDs`, `HashtagIDs`, `RecurrenceRuleIDs`, `AssignmentIDs`, `AttachmentIDs`. Alarms are separate records: `Alarm` references its `Reminder`, and `AlarmTrigger` references its `Alarm` with `Type` and `DateComponentsData`, base64 JSON date components with a zone. No URL or parent field exists, and the account had no recurrence-rule, hashtag or subtask records, so their encoding is unknown: rows carry `recurring: true` and `tag_count` from the id lists, and omit the rule, tag names, URL and subtasks. The record cache file name carries a version (`reminders-ck-v2.json`), so a cache written with fewer keys is never reused. The DOM row id ends in the record name (`Reminder/<uuid>`), which is how `complete_reminder` maps an id to exactly one row. `complete_reminder` takes `id`: the record names the list, the row is matched by its DOM id (which ends in `Reminder/<uuid>`) before clicking, and Apple's record confirms the completion afterwards; if the page cannot map an id to a row, it refuses rather than guessing.

**6. Mail from one richer FETCH.** (Deviation, as built: raw HEADER.FIELDS instead of ENVELOPE, because `decodeHeader` is the proven encoded-word path and ENVELOPE carries no `date_raw`.) Listings fetch ENVELOPE (addresses, message-id, in-reply-to), FLAGS, RFC822.SIZE, INTERNALDATE, BODYSTRUCTURE and a partial `BODY.PEEK[<first text part>]<0.2048>` for the snippet, in one FETCH per page. The first text part is found from BODYSTRUCTURE; a second FETCH for snippets is allowed when the part number is only known after the structure. `read_mail` pages the decoded body with `offset` (default page 8000 runes). The local search pass fetches headers for the scanned window in one UID-set FETCH instead of one per UID. Alternative: keep header parsing and add fields one by one, rejected because ENVELOPE already gives parsed addresses.

**7. Calendar rows from data already returned.** `eventRow` reads the props the query already requests (adding URL, CREATED, LAST-MODIFIED, VALARM to the named-prop list) and parses ATTENDEE parameters (CN, ROLE, PARTSTAT). Merging dedupes by UID plus RECURRENCE-ID across calendars. Contacts read the extra vCard properties from the cards already fetched.

**8. Notes: records if they exist, else what the page already shows.** Probe whether the Notes page syncs note records through CloudKit the way Reminders does (same probe technique). If it does, created and modified dates, pinned, locked, shared and checklist state come from records without opening notes. If it does not, listings keep the page's display date as `modified_display` and leave `created` null; checklist, attachments and tags come only from `notes_read`, which already opens the note. The spec allows both, since it says a field the source does not hold is omitted, never guessed. Probe result (2026-09-26): it does. The Notes page's CloudKit container (`com.apple.notes`, private zone `Notes`) holds `Note` records with `TitleEncrypted` and `SnippetEncrypted` (base64 UTF-8), `TextDataEncrypted` (the body: Apple's Notes protobuf, zlib when the web editor wrote it and gzip when a device did), `CreationDate`, `ModificationDate`, a `Folder` reference and, on device-made notes, `Deleted`; `Folder` records hold `TitleEncrypted`, and trashed notes sit in `TrashFolder-CloudKit`. No pinned, locked or shared field appeared, so those are omitted. `notes_list` reads these records (every note, no clicks), and `notes_read` by `id` decodes the body itself: text from Note field 2, and from the attribute runs (field 5, lengths in UTF-16 units) checklist lines (paragraph style 103, done flag in its checklist field) and attachments (field 12, id and type UTI). Checklist decoding follows the published schema and is tested on a built fixture; the account had no real checklist note to probe. Tags are omitted: hashtags are inline attachments whose names the text does not carry. `update_note` by `id` resolves the record's title and folder and then runs the existing verified page flow, so duplicate titles still stop at its ambiguity check.

## Risks / Trade-offs

- [Clients parsing the old text shapes break] → the text block keeps the full JSON, field renames are limited to display-to-ISO moves with the display kept beside it, and the PR lists every changed field.
- [CloudKit reminder keys differ from what the probe shows on another account] → decode defensively, omit what is missing, and keep the page fallback.
- [BODYSTRUCTURE on odd messages (nested multipart, no text part)] → fixtures for multipart/alternative, mixed with attachment, HTML-only and single-part; missing snippet is null, not an error.
- [Larger payloads cost client tokens] → listings stay bounded by the existing `limit`, snippets are capped at ~300 characters, bodies are paged.
- [Output schemas drift from payloads] → the fixture-validation test fails on drift.

## Migration Plan

No data migration. One PR; rollback is reverting it. The README tools table and descriptions change in the same PR, and the PR lists before/after payloads and timings.

## Open Questions

- The exact CloudKit keys for reminder notes, recurrence, alarms, URL, tags and subtasks, and whether Notes exposes records at all. The probes in tasks 3.1 and 5.1 answer these; the spec already covers either outcome.
