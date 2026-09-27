# Performance

How long each tool takes, where the time goes, and what changed to bring it down. Every number here comes from a recorded run: the conditions are written next to the table, and a number without a run is not written down.

## How the numbers are taken

- The server runs over stdio in the Linux rig (`./verify.sh`: Debian, headless Chromium, signed in through the login door), unless a table says otherwise.
- A small JSON-RPC client calls each tool twice in one session and records the wall time and the size of the text block. The first call can include opening an app tab; the second call shows the steady state.
- A comparison between two builds uses one signed-in session: the older build is copied into the container next to the newer one, so both read the same account through the same browser.
- Writes run only on items titled `[seed] ...` or `[icloud-mcp verify] ...`.

The test account has two states in these tables:

| State | Mail | Notes | Reminders |
|---|---|---|---|
| Near empty (2026-09-26) | 3 messages | 19 notes, 1 folder | about 5 open, 1 list |
| Seeded (2026-09-26/27) | 88 messages: reply threads of 6 to 16 messages, attachments up to 2.5 MB, HTML-only mail, encoded subjects (`tools/seedmail`) | 45 notes, 6 folders (checklists, long notes over one 8,000-character page) | about 20 open, 4 lists, timed, all-day and undated |

## Where the time goes

The calls over 5 s spend most of their time in fixed `sleep(...)` waits in the browser flows, not in Apple's servers or the DevTools protocol. A CDP round trip on loopback costs tens of milliseconds. A read-only pass over the code (2026-09-26) added up the waits for each slow call:

| Call | Time | What made it up |
|---|---|---|
| `notes_read` by title | 12.1 s | 4.0 s selecting "All iCloud", 6.0 s after opening the note, 1.5 s copying its text |
| `notes_search` | 11.9 s | 4.0 s selecting "All iCloud", 7.0 s after typing the query, 0.7 s clearing it |
| `complete_reminder` by id | 8.3 s | 4.0 s after opening the list, two 3 s checks after the click, 0.5 s per record sync |
| First call that opens an app tab | 4.6 s | readiness polling at 3 s intervals, then a settle wait |

The other cost that grows with use is the connection: mail opens a TLS connection and logs in on every call, about 1.5 s of each mail call.

## Changes, and what they bought

### PR #17: read Apple's records instead of the page

- **What changed**:
  - `notes_list` reads the Notes app's CloudKit records instead of scraping the rendered list.
  - `list_reminders` reads the Reminders records that `completed_reminders` already synced, instead of opening a list and reading its rows.
  - `notes_read` by id decodes the note's record body itself.
  - Mail listings fetch headers, flags, sizes and structure for a whole page in one FETCH, and the local search pass fetches its window of up to 300 messages in one UID-set FETCH instead of one FETCH per message.
- **Why it is faster**: a record read is one CloudKit round trip from the page's main world, with no clicks, no waits for the page to draw, and no virtualised list that shows only part of the data.

Near-empty account, `main` before (4742136) and after (079eb08), first / second call:

| Tool | Before | After |
|---|---|---|
| `notes_list` | 1253 B, 4.0 / 4.0 s (rendered rows only) | 4928 B, 0.5 / 0.5 s (all 19 notes) |
| `list_reminders`, one list | 1206 B, 4.0 / 4.0 s | 1883 B, 1.3 / 0.5 s |
| `list_reminders`, all lists | not possible | 1889 B, 0.5 / 0.5 s |
| `notes_read` by id | not possible | 426 B, 0.6 / 0.5 s |
| `completed_reminders` | 1377 B, 0.7 / 0.5 s | 2247 B, 0.5 / 0.5 s |
| `list_mail`, 10 rows | 572 B, 1.9 / 1.7 s | 2240 B, 2.1 to 2.6 s (one more round trip for snippets) |
| `read_mail` | 1904 B, 1.6 / 1.9 s | 2261 B, 1.8 / 1.8 s |
| `search_mail` | 449 B, 2.3 / 2.3 s | 1710 B, 2.4 / 2.4 s |
| `notes_read` by title | 244 B, 12.1 / 12.1 s | 301 B, 12.1 / 12.1 s (unchanged: still the page) |
| `notes_search` | 1379 B, 11.9 / 11.8 s | 959 B, 11.9 / 11.9 s (unchanged: still the page) |

### Scaling to the seeded account

The same build (079eb08) on the seeded account, against the near-empty numbers above. Record reads grew by 0.2 to 0.4 s for two to four times the data. Mail at the default 10 rows stayed flat.

| Tool | Near empty | Seeded |
|---|---|---|
| `list_mail`, 10 rows | 2240 B, 2.1 to 2.6 s | 5491 B, 2.1 / 2.0 s |
| `list_mail`, 25 rows | not measured | 16947 B, 4.0 / 3.3 s |
| `read_mail` | 2261 B, 1.8 / 1.8 s | 2261 B, 2.1 / 1.8 s |
| `search_mail` "apple" | 1710 B, 2.4 / 2.4 s | 1712 B, 2.6 / 2.7 s |
| `search_mail`, a long thread | not measured | 5409 B, 3.6 / 3.7 s |
| `search_mail`, non-ASCII query | not measured | 699 B, 4.3 / 5.7 s |
| `notes_list` | 4928 B, 0.5 / 0.5 s | 5500 B, 1.0 / 0.9 s |
| `list_reminders`, all lists | 1889 B, 0.5 / 0.5 s | 5738 B, 1.1 / 0.7 s |
| `completed_reminders` | 2247 B, 0.5 / 0.5 s | 2722 B, 1.0 / 0.8 s |
| `notes_read` by id, a long note | not measured | 8395 B (one full page), 0.6 / 0.5 s |
| `notes_search` | 959 B, 11.9 / 11.9 s | 438 B, 12.8 / 12.7 s |
| `notes_read` by title, a note the page has not drawn | not measured | fails: the virtualised list does not show it |

### PR #18: wait on the page, not the clock

- **What changed**:
  - `selectAllICloud` checks the folder tree's selection marker and the search box before it clicks "All iCloud", and a click it still needs waits on that marker instead of 4 s.
  - `notes_read` by title resolves the title through the record walk `notes_list` uses and decodes the matched note's record. The page flow stays for a miss, an unknown folder, a `match=` index and a body that does not decode.
  - `openList` in Reminders skips the click when the list is already selected. Otherwise it polls the list menu's own selection marker (`div.rm-list-menu-item[aria-selected]`) instead of waiting 4 s.
  - `complete_reminder` polls the list every 300 ms after its click. The row counts as gone on two reads in a row, and a second click still waits for the whole 6 s window, so a slow completion is never clicked twice.
- **Why it is faster**: the waits end when the page reaches the state they were waiting for, usually well inside the old fixed time, and a step the page has already done is skipped.

Seeded account, 079eb08 before, this branch after:

| Call | Before | After |
|---|---|---|
| `notes_read` by title, a note the page has not drawn | fails (4.1 s) | 1.3 to 1.6 s |
| `notes_read` by title, a long note | 12.1 s | 1.4 s |
| `notes_read` by title, no such note | not measured | 0.8 s |
| `notes_search` | 12.7 s | 8.6 s |
| `complete_reminder` by id | 8.3 s | 2.2 s |
| `complete_reminder` by title, list already open | not measured | 0.9 s |

Writes, for reference (seeded account, this branch): `create_note` about 12 to 14 s; `create_reminder` 9 s without a due date and 23 to 39 s with one; `update_note` 29.9 s including the approval.

## What is still slow, and the next options

Ranked by expected gain for the risk. Each item comes from the code trace above; the expected times are estimates until measured.

1. **`notes_search` over a synced body cache.** Keep the note bodies from the record store (with a sync token, as the Reminders cache does) and match in Go. Expected about 1 s after the first sync, which is unmeasured and may take tens of seconds on a large account. The results can differ from Apple's search: substring matching finds more, and stemming finds less. A body cache on disk needs a check against `SECURITY.md`.
2. **Faster readiness polling when an app tab opens** (`browser/app.go`): poll every 250 ms and treat two equal counts 500 ms apart as settled. Expected 1.5 to 2.5 s instead of 4.6 s. Apple's page load sets the floor.
3. **`update_note` and `create_note` on waits instead of sleeps**: the 6 s after opening a note, the copy and paste waits, and the tail wait. Expected 3 to 5 s instead of about 12 to 16 s. Verify on the rig, because the editor draws to a canvas and its timing is not visible in the DOM.
4. **`create_reminder` with a due date**: the date picker flow is bounded by about 12 to 18 s of fixed waits. Polls might bring it to 6 to 10 s; under 5 s probably needs a record write.
5. **A reused IMAP connection**: one logged-in, read-only connection with a liveness check and a mutex, instead of a TLS connection and login per call. Expected about 0.3 to 0.5 s per mail call instead of about 2 s.
6. **Record writes (CloudKit)**:
   - Completing a reminder is one record update with its change tag, under 1 s. Medium risk: devices may expect fields the web app sets, and a repeating reminder's next date is computed by the app.
   - Creating notes or reminders is high risk: titles and bodies are Apple's CRDT documents, and a bad encoding can damage the item on every device. Do not attempt it without a probe that proves the round trip on a device.
