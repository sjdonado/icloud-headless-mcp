## 1. Baseline

- [x] 1.1 Record the "before" payload and wall time for every read tool on the current main, raw over stdio against the Linux rig (`./verify.sh`), in the same shape as the proposal's table; verify: a before table saved in the branch note
- [x] 1.2 Sign the rig in through the login door if it is signed out; verify: `icloud-mcp session-check` prints OK in the container

## 2. Shared envelope (mcp-transport)

- [x] 2.1 `ResultJSON` returns `structuredContent` plus the JSON text block, and sets `isError` when the payload has a non-empty `error`; `ErrorResult` always sets it; verify: a unit test for success, error, `needs_login` and `needs_device_approval` payloads
- [x] 2.2 Add annotations to `ToolDef` and set them for every tool (read-only on reads; destructive on `delete_event`, `update_event`, `update_note`, `sign_out`; idempotent where it holds; open-world only on `send_mail`); verify: a test that every tool declares annotations and every `TierReadOnly` tool is read-only
- [x] 2.3 One date helper: ISO 8601 with offset in the owner's zone, all-day as `YYYY-MM-DD` plus `all_day`; verify: a table test across zones and DST
- [x] 2.4 Output schemas per tool next to `ToolParams`, and a fixture per tool validated against its schema; verify: `go test ./internal/mcpserver`
- [x] 2.5 Tool descriptions state returned fields, units, zone and truncation; verify: a test that every description mentions its date zone when the tool returns dates

## 3. Reminders

- [x] 3.1 Probe a real Reminder record with `tools/cdp-eval.mjs` in the rig and record the keys and encodings (notes, due, all-day, recurrence, alarms, URL, tags, parent and subtasks, modified); verify: the findings written into design.md Decisions 5
- [x] 3.2 Extend `ckKeys` and the record decoding; decode notes like `ckTitle`; verify: unit tests with fixtures shaped like the probed records
- [x] 3.3 `list_reminders` from records with `source`, every list when `list_name` is omitted, DOM fallback with `source: page`; verify: unit tests for both sources and for all lists
- [x] 3.4 `completed_reminders` and `reminder_lists` use the same fields and date format; verify: unit tests
- [x] 3.5 `complete_reminder` accepts `id`, completes exactly that reminder, refuses when the id cannot be mapped; verify: live on the owner's account, twice (Mac and Linux rig, 2026-09-26): three same-titled "New Reminder" strays completed one id at a time while the others stayed open (Complete needs a real page; the id selection itself is `completeGeoJS`)

## 4. Mail

- [x] 4.1 Listings fetch the address and id header fields (HEADER.FIELDS, not ENVELOPE: see design Decision 6 deviation), FLAGS, RFC822.SIZE, INTERNALDATE, BODYSTRUCTURE, and a partial BODY.PEEK snippet of the first text part; return the spec's fields; verify: fixtures on the fake IMAP server for multipart/alternative, mixed with attachment, HTML-only and single-part, and a test that no message gains `\Seen`
- [x] 4.2 `read_mail` returns the listing fields plus attachments always, falls back from HTML to text with `body_format`, and pages the body with `offset`, `truncated`, `total_length`, `next_offset`; verify: unit tests for each
- [x] 4.3 Batch the local search pass into one UID-set FETCH per page of the scanned window; verify: a test on the fake server counting round trips, and the live timing in 7.2

## 5. Notes

- [x] 5.1 Probe whether the Notes page syncs note records (CloudKit) with `tools/cdp-eval.mjs`, and record what a note record holds; verify: the finding written into design.md Decisions 8
- [x] 5.2 Listings return an identifier, ISO created and modified where the source holds them (display text as `modified_display` otherwise), pinned, locked, shared; verify: unit tests
- [x] 5.3 `notes_read` returns checklist items with done state, attachments by id and kind (names and tags live in records the account had none of, so they are omitted per the spec), and pages the text with `offset`; verify: unit tests with a checklist fixture and a long note

## 6. Calendar and contacts

- [x] 6.1 `eventRow` returns description, all_day, status, organizer, attendees with participation, recurrence rule and `recurring_instance`, URL, alarms, created, last_modified (adding URL, CREATED, LAST-MODIFIED and VALARM to the named props); verify: dav fixtures with attendees and a recurring series
- [x] 6.2 Merge an event present under several calendars into one entry with `calendars`; verify: a fixture with the same UID in two calendars
- [x] 6.3 Contacts return organization, job title, birthday, addresses, notes, URLs, nickname, and typed emails and phones; verify: a vCard fixture with every field

## 8. Health

- [x] 8.1 Health rollups name units and sources from the stored samples; `health_status` adds unit, sources and zone per metric; verify: import fixtures with units and two sources
- [x] 8.2 `health_days` heart-rate min/max/avg and `metrics_used`; `health_sleep` owner-zone onset/wake, asleep/awake/in-bed minutes and segment count; `health_effort` samples, max and average bpm; `health_recovery` window dates and `delta_percent`; verify: unit tests per tool

## 7. Verification and docs

- [x] 7.1 Local ladder: `gofmt -l .`, `go vet ./...`, `go test ./...`, linux amd64 and arm64 builds, and the suite in `golang:bookworm`; verify: all green
- [x] 7.2 One live round with `./verify.sh` against the real account per AGENTS.md: every read tool raw over stdio, recording the "after" payload and wall time next to 1.1, plus `complete_reminder` by id on a marker reminder; verify: the before/after table
- [x] 7.3 README tools table and descriptions, AGENTS.md happy-path keys, SECURITY.md untrusted-input note where it changes; verify: docs name the new fields and the `isError` change
- [x] 7.4 PR body lists each tool's before and after payload and timing, and every breaking field change; verify: the PR
