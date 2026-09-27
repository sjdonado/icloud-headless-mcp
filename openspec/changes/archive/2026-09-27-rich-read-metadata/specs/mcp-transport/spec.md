## Purpose

The output envelope every iCloud tool shares, so a calling agent can parse any result the same way, tell a failure from data without reading prose, and know from the tool list what each tool returns.

## ADDED Requirements

### Requirement: Structured results with declared schemas
Every tool SHALL return its payload as MCP `structuredContent` and SHALL declare an `outputSchema` for it in `tools/list`. Every tool SHALL also return the same payload as JSON in a text content block, so a client without structured-content support still receives it.

#### Scenario: A structured-content client reads a result
- **WHEN** a client calls any tool and the call succeeds
- **THEN** the result carries `structuredContent` that validates against the tool's declared `outputSchema`, and a text content block holding the same JSON

#### Scenario: An older client reads a result
- **WHEN** a client that ignores `structuredContent` calls a tool
- **THEN** it still receives the full payload as JSON text

### Requirement: Failures are marked as errors
A tool call that fails SHALL return a result with `isError: true`. The error payload SHALL carry a human-readable `error` string and SHALL keep machine-readable routing fields: `needs_login: true` when the session is signed out and `needs_device_approval: true` when Apple's data-access grant lapsed. A call that succeeds SHALL NOT carry `isError` or an `error` key.

#### Scenario: A signed-out session
- **WHEN** a Notes or Reminders tool runs while the browser session is signed out
- **THEN** the result has `isError: true` and its payload has `needs_login: true` and an `error` string naming the fix

#### Scenario: A lapsed grant
- **WHEN** a browser-backed tool runs while Apple's data-access grant is waiting for approval
- **THEN** the result has `isError: true` and its payload has `needs_device_approval: true`

#### Scenario: A refused approval is not data
- **WHEN** the owner declines an elicitation for a confirmed tool
- **THEN** the result has `isError: true` and nothing was written

### Requirement: Tool annotations
Every tool SHALL declare MCP annotations. Every read-only tool SHALL set `readOnlyHint: true`. `delete_event`, `update_event`, `update_note` and `sign_out` SHALL set `destructiveHint: true`. A tool whose repeated call with the same arguments has no further effect SHALL set `idempotentHint: true`. Tools that only touch this account's iCloud SHALL set `openWorldHint: false`, except `send_mail`, which reaches other people.

#### Scenario: Listing tools
- **WHEN** a client calls `tools/list`
- **THEN** every read tool shows `readOnlyHint: true`, and `delete_event`, `update_event`, `update_note` and `sign_out` show `destructiveHint: true`

### Requirement: One date format
Every date or time a tool returns SHALL be ISO 8601 with a UTC offset, expressed in the owner's zone (`AGENT_TZ`, or the zone file). An all-day value SHALL be a date (`YYYY-MM-DD`) together with `all_day: true`. A tool MAY return the source's own display string beside the ISO value under a separate field (`*_display` or `*_raw`), and SHALL NOT return a display string in place of an ISO value.

#### Scenario: A reminder due tomorrow at 18:00
- **WHEN** `list_reminders` returns a reminder due tomorrow at 18:00 owner-local
- **THEN** it has `due` as that instant in ISO 8601 with the owner's offset, `all_day: false`, and may carry `due_display` with the app's text

#### Scenario: A mail date
- **WHEN** `list_mail` returns a message
- **THEN** its `date` is ISO 8601 in the owner's zone and `date_raw` holds the RFC 2822 header as sent

### Requirement: Descriptions state what comes back
Every tool description SHALL name the fields the tool returns, their units, the time zone of its dates, and any truncation or pagination rule, so a caller knows what it will receive without a second call.

#### Scenario: Reading a description
- **WHEN** a client reads `read_mail` in `tools/list`
- **THEN** the description names the returned fields, says dates are ISO 8601 in the owner's zone, and states the body page size and how to continue

### Requirement: Bounded bodies with continuation
Every body or text a tool returns SHALL be bounded. Where a tool truncates, the result SHALL say so (`truncated: true` and the total size where known) and SHALL offer a continuation the same tool accepts (`next_offset`). A tool SHALL NOT cut text silently. Untrusted content (mail bodies, note text) SHALL keep its untrusted-input warning.

#### Scenario: A long mail body
- **WHEN** `read_mail` returns a body longer than one page
- **THEN** the result has `truncated: true`, `next_offset`, and calling `read_mail` again with that offset returns the next page

### Requirement: Follow-up ids are returned
Every read result SHALL include each identifier a follow-up tool accepts: reminder ids for `complete_reminder`, mail uids and mailbox for `read_mail`, event uids for `update_event` and `delete_event`, and note identifiers for `notes_read` and `update_note`.

#### Scenario: Completing a listed reminder
- **WHEN** an agent lists reminders and then completes one
- **THEN** it can pass the returned `id` to `complete_reminder` without matching on the title
