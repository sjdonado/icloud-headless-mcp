## Purpose

Preserves the browser-backed Notes and Reminders behaviors, which encode months of hard-won Apple-specific rules: never return the wrong note, never claim an unconfirmed write, and never wake the owner at night.

## ADDED Requirements

### Requirement: Per-app browser locks and quiet hours

Notes and Reminders SHALL each serialize on their own per-app lock against the shared resident browser. A call needing a fresh app-page load between 23:00 and 07:00 local SHALL refuse with a quiet-hours message instead of raising a device prompt; already-loaded tabs SHALL keep working.

#### Scenario: Night load refuses, warm tab works

- **WHEN** a fresh app-page load is needed at 03:00 local
- **THEN** the tool refuses citing quiet hours, while a call servable from an already-loaded tab succeeds

### Requirement: Access lapse is a refusal, never an empty list

When Apple's web-access grant has lapsed, tools SHALL return an honest failure carrying `needs_device_approval` rather than an empty result.

#### Scenario: Lapsed grant reports honestly

- **WHEN** Notes is opened while the grant has lapsed
- **THEN** the result carries `needs_device_approval` instead of an empty folder or note list

### Requirement: Notes reads never return the wrong note

`notes_read` SHALL open the note, copy its canvas-rendered body, check the copy against the requested title, retry once on mismatch, and refuse rather than return another note's content.

#### Scenario: Title mismatch refuses

- **WHEN** the copied body does not match the requested title after one retry
- **THEN** the tool refuses instead of returning the mismatched content

### Requirement: Writes are verified, never assumed

`create_note`, `update_note`, `complete_reminder`, and `create_reminder` SHALL verify each write against what the app shows, matching on the longest word with ellipsis stripped. A write that cannot be confirmed SHALL be reported as typed-but-unconfirmed, never as success.

#### Scenario: Unconfirmed write says so

- **WHEN** a reminder is typed but its row cannot be confirmed in the app
- **THEN** the result states the write could not be confirmed rather than claiming success

### Requirement: Reminder date semantics

Due dates SHALL be set through the details popover by unticking time first for all-day dates, picking the day by `aria-label` containment, and committing with Save; dismissing without Save SHALL drop the date. Day cells SHALL match by containment, not equality. `create_reminder` SHALL compare the row hour against the requested hour and return `due_error` on disagreement instead of claiming a date that is not there.

#### Scenario: All-day date keeps its day

- **WHEN** creating an all-day reminder for a given date
- **THEN** the stored due date is that date with no midnight alarm attached

#### Scenario: Hour mismatch surfaces due_error

- **WHEN** the row hour disagrees with the requested hour
- **THEN** the result carries `due_error` explaining the mismatch

### Requirement: Timezone override before app load

The timezone override SHALL be applied before the app loads with one reload per tab lifetime, since the apps read their zone once at startup.

#### Scenario: Override precedes first load

- **WHEN** a fresh app tab is prepared
- **THEN** the override is in place before the app's first load, followed by at most one reload per tab lifetime

### Requirement: Session hygiene

Every entry point SHALL touch its app's stamp file so the tab reaper never closes a tab mid-session, and opening a list SHALL dismiss any modal alert before interacting with the app.

#### Scenario: Active session keeps its tab

- **WHEN** calls are in flight against an app tab
- **THEN** the tab reaper does not close that tab for idleness
