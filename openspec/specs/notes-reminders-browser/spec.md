# notes-reminders-browser Specification

## Purpose

What the Notes and Reminders tools return, and how they read it, so an agent gets the whole record in one call at the cost the browser lock already allows.

## Requirements

### Requirement: Reminders read from Apple's records
`list_reminders` SHALL read reminders from the Reminders app's record store (the one `completed_reminders` already reads), and SHALL fall back to reading the app's rendered list only when the record store does not answer. The result SHALL say which source answered (`source: records` or `source: page`). Reading SHALL add no clicks per reminder.

#### Scenario: The record store answers
- **WHEN** `list_reminders` runs on a healthy session
- **THEN** it returns every open reminder of the list, including ones the page would not have rendered, with `source: records`

#### Scenario: Fallback
- **WHEN** the record store does not answer
- **THEN** `list_reminders` returns what the page shows, with `source: page`, and says which fields are unavailable that way

### Requirement: Rich reminder metadata
Every reminder a Reminders read returns SHALL carry: `id`; `list`; `title`; `notes`; `due` as ISO 8601 in the owner's zone with `all_day` (a date when all-day), plus `due_display` with the app's text when read from the page; `priority`; `flagged`; `completed` and, for completed reminders, `completed_at` as ISO 8601; `created` and `modified`; `alarms` with their time; `recurring: true` when the reminder repeats, and the rule itself when the record store exposes it; `tag_count` when it has tags, and the tag names when the record store exposes them; `url` and `parent` or `subtasks` when the record store exposes them. A field the source does not hold SHALL be omitted, never guessed.

#### Scenario: A repeating reminder with notes
- **WHEN** a repeating reminder has a notes line, a due time and an alarm
- **THEN** it returns the notes text, `due` as an ISO instant, `recurring: true`, and the alarm's time as ISO 8601

### Requirement: All lists in one call
`list_reminders` SHALL return the open reminders of every list when `list_name` is omitted, each reminder naming its list, and SHALL keep returning one list when `list_name` is given.

#### Scenario: What is pending
- **WHEN** an agent calls `list_reminders` with no `list_name`
- **THEN** it receives every open reminder across all lists in one result

### Requirement: Complete by id
`complete_reminder` SHALL accept the `id` a Reminders read returned, as an alternative to `title` and `list_name`, and SHALL complete exactly that reminder.

#### Scenario: Two reminders with the same title
- **WHEN** two open reminders are titled "Call mom" and the agent passes one's `id`
- **THEN** only that reminder is completed

### Requirement: Rich note metadata
Notes reads SHALL return, for every note: an identifier `notes_read` and `update_note` accept; `title`; `folder`; `created` and `modified` as ISO 8601 in the owner's zone, with the app's display text as `modified_display` where only that is known; `snippet`; `pinned`, `locked` and `shared`; `checklist` items with their done state; `attachments` by kind (and by name when the record store exposes it); and `tags` when the record store exposes them. A field the source does not hold SHALL be omitted or null, never guessed.

#### Scenario: A checklist note
- **WHEN** `notes_read` opens a note with a three-item checklist, one item done
- **THEN** the result lists the three items with `done: true` on one of them

### Requirement: Note bodies continue past the cap
`notes_read` SHALL bound the text it returns per call and SHALL accept an `offset` to continue. A result that does not reach the end SHALL carry `truncated: true`, `total_length` and `next_offset`.

#### Scenario: A long note
- **WHEN** a note's text exceeds one page
- **THEN** the first result has `truncated: true` and `next_offset`, and the following call with that offset returns the rest

### Requirement: The browser budget holds
Richer Notes and Reminders output SHALL come from data the tool already loads or from reading the app's own records, and SHALL NOT add clicks or navigation per row or per note in a listing.

#### Scenario: Listing notes
- **WHEN** `notes_list` returns 25 notes with their metadata
- **THEN** it opens no individual note to get it
