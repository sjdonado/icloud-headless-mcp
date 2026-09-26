## Purpose

What the calendar and contact tools return, so an agent can answer who, where, how often and whether someone accepted, from one listing.

## ADDED Requirements

### Requirement: Rich event metadata
`list_events` SHALL return, for every event: `uid`; `calendars` (every calendar it appears in); `summary`; `start` and `end` as ISO 8601 in the owner's zone, or dates with `all_day: true`; `location`; `description`; `status`; `organizer` as `{name, address}`; `attendees` as `{name, address, role, participation}` where participation is the attendee's PARTSTAT (accepted, declined, tentative, needs-action); `recurrence_rule` when the series has one and `recurring_instance: true` for an occurrence of a series; `url`; `alarms` as offsets or absolute times; `created` and `last_modified` as ISO 8601.

#### Scenario: A meeting with attendees
- **WHEN** `list_events` returns a meeting with two attendees, one accepted and one not yet answered
- **THEN** the event lists both with `participation: accepted` and `participation: needs-action`, and names the organizer

#### Scenario: A weekly event
- **WHEN** a weekly event has an occurrence in the window
- **THEN** that occurrence has `recurring_instance: true` and the series' `recurrence_rule`

### Requirement: One entry per event
An event that appears under several calendars (a shared or subscribed calendar and the owner's own) SHALL appear once, with `calendars` listing every calendar that holds it.

#### Scenario: A shared event
- **WHEN** the same event uid is returned by two calendars
- **THEN** `list_events` returns it once, with both calendar names in `calendars`

### Requirement: Rich contact metadata
`search_contacts` SHALL return, for every contact: `name`, `nickname`, `organization`, `job_title`, `birthday` as `YYYY-MM-DD` (or `--MM-DD` without a year), `emails` and `phones` as `{value, type}` with the card's TYPE label, `addresses` as structured parts with their type, `urls`, and `notes`.

#### Scenario: A work contact
- **WHEN** a contact has a work email, a mobile phone and a company
- **THEN** the result shows the email with `type: work`, the phone with `type: cell`, and `organization`
