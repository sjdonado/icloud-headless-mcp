## Purpose

Preserves the exact MCP contract clients rely on: one stdio server entry, the same 23 tools with the same permission tiers, and the same elicitation approval gate, so any client working against the Python server works unchanged against the Go port.

## ADDED Requirements

### Requirement: Tool registry parity

The system SHALL expose exactly the same 23 tools under the same names on one server entry: `list_calendars`, `list_events`, `create_event`, `update_event`, `delete_event`, `search_contacts`, `list_mailboxes`, `list_mail`, `read_mail`, `search_mail`, `send_mail`, `notes_folders`, `notes_list`, `notes_read`, `notes_search`, `update_note`, `create_note`, `reminder_lists`, `list_reminders`, `completed_reminders`, `complete_reminder`, `create_reminder`, `drive_status`.

#### Scenario: Full tool listing matches

- **WHEN** a client lists tools on the Go server
- **THEN** the set of tool names is identical to the Python server's set, with no additions, removals, or renames

### Requirement: Permission tiers preserved

The system SHALL keep every tool's tier: read-only tools never write; `create_event` (solo), `create_note`, `complete_reminder`, and `create_reminder` stay silent; `delete_event`, `send_mail`, and `update_note` ask first; `update_event` stays silent for attendee-free events and asks when other attendees are present.

#### Scenario: Tier matrix unchanged

- **WHEN** each of the 23 tools is exercised for whether it asks
- **THEN** the Asks column matches the documented table exactly (Yes only for `delete_event`, `send_mail`, `update_note`; Only-with-attendees for `update_event`)

### Requirement: Elicitation approval gate

Confirmed tools SHALL ask the owner through MCP elicitation and execute only on explicit approval. A session that cannot ask (client without elicitation capability, or transport with no back-channel) SHALL fail closed: nothing executes and the result explains nobody was present. The elicitation schema SHALL carry no required fields so a plain accept with an empty object validates as approval.

#### Scenario: Approval granted executes

- **WHEN** the owner accepts the elicitation for `delete_event`
- **THEN** the deletion proceeds

#### Scenario: No back-channel refuses without writing

- **WHEN** a confirmed tool runs in a session that cannot elicit (e.g. a scheduled run)
- **THEN** nothing is written and the result states approval was impossible

#### Scenario: Decline refuses without writing

- **WHEN** the owner declines or answers no
- **THEN** nothing is written and the result reports it as not approved

### Requirement: Locking and concurrency posture

Notes and Reminders SHALL each serialize on their own per-app lock while calendar, contacts, and mail take no lock, so a browser-backed call never serializes a DAV/IMAP call behind it. The tool timeout SHALL remain above the client's elicitation timeout.

#### Scenario: Mail reads stay concurrent with Notes

- **WHEN** `list_mail` runs while a Notes call holds its lock
- **THEN** the mail call completes without waiting on the browser lock

### Requirement: No network listener in stdio mode

The default stdio mode SHALL open no network port; any HTTP mode kept for parity SHALL bind loopback only.

#### Scenario: Stdio run exposes nothing

- **WHEN** the server runs in default mode
- **THEN** it speaks only over stdin/stdout and listens on no TCP port
