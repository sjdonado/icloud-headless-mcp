## Purpose

Preserves the calendar and contacts behaviors clients depend on, including the iCloud-specific rules that keep stored times correct and attendee-affecting changes approved.

## ADDED Requirements

### Requirement: Calendar listing correctness

`list_calendars` SHALL list the account's calendars and `list_events` SHALL return occurrences in the requested window, with recurring events expanded so a listing never reports an event in the wrong month. Date arguments SHALL accept `YYYY-MM-DD`, and a malformed value SHALL error naming which argument was wrong.

#### Scenario: Recurring event lands in the right month

- **WHEN** listing a window containing an instance of a recurring event
- **THEN** the instance appears with its occurrence date, not the master's date

#### Scenario: Bad date names the argument

- **WHEN** `list_events` receives `start` in a non-`YYYY-MM-DD` form
- **THEN** the error names `start` and shows the received value

### Requirement: Event writes store correct times

`create_event` SHALL stay silent and SHALL convert offset-carrying timestamps into the named `AGENT_TZ` zone before writing, so a fixed offset is never serialized as wrong UTC hours. Every write SHALL read the stored event back and report stored times rather than echoing the request.

#### Scenario: Offset input stores the intended wall time

- **WHEN** creating an event with an ISO timestamp carrying an offset
- **THEN** the stored start equals the intended local wall time in the configured zone, as confirmed by read-back

### Requirement: In-place updates with attendee-conditional approval

`update_event` SHALL change the existing event in place (never delete-and-recreate, preserving identity, invitations, and replies), SHALL bump SEQUENCE so other clients accept the revision, and SHALL ask first only when the event has other attendees.

#### Scenario: Solo event updates silently

- **WHEN** updating an event with no attendees
- **THEN** no elicitation occurs and the stored event reflects the change

#### Scenario: Attended event asks first

- **WHEN** updating an event with other attendees
- **THEN** the tool elicits approval naming the event and the attendee count before writing

### Requirement: Confirmed delete and contact search

`delete_event` SHALL elicit approval before deleting. `search_contacts` SHALL remain a read-only, case-insensitive substring match over name, email, and phone.

#### Scenario: Delete requires approval

- **WHEN** `delete_event` is called
- **THEN** nothing is deleted until the owner approves through elicitation

### Requirement: Default calendar and list preserved

When the caller names no calendar or reminder list, the system SHALL fall back to the same configured defaults (`AGENT_DEFAULT_CALENDAR`, `AGENT_DEFAULT_LIST`) with the same semantics as today.

#### Scenario: Unnamed calendar uses the configured default

- **WHEN** creating an event without naming a calendar
- **THEN** it lands on the configured default calendar, never on whichever calendar sorts first
