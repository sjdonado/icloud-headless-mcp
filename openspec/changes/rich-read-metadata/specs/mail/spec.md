## Purpose

What the mail tools return, so an agent can triage an inbox, answer "anything pending?", and decide which messages to open from one listing, without marking anything seen.

## ADDED Requirements

### Requirement: Rich message metadata in listings
`list_mail` and `search_mail` SHALL return, for every message: `uid` and `mailbox`; `from`, `to`, `cc` and `reply_to` as lists of `{name, address}`; `subject`; `message_id` and `in_reply_to`; `flags` with `seen`, `answered`, `flagged` and `draft` as booleans; `size` in bytes; `internal_date` and `date` as ISO 8601 in the owner's zone plus `date_raw` as the header was sent; `has_attachments` and `attachments` as `{name, mime_type, size}` taken from the message structure; and `snippet`, up to about 300 characters of the first text part with whitespace collapsed.

#### Scenario: Triage from a listing
- **WHEN** an agent calls `list_mail` on INBOX
- **THEN** each message carries sender name and address, recipients, flags, size, an ISO date, whether it has attachments with their names, and a snippet, so the agent can say what the message is about without `read_mail`

#### Scenario: A message with an attachment
- **WHEN** a listed message has a PDF attachment
- **THEN** it shows `has_attachments: true` and an `attachments` entry with the file name, `application/pdf` and its size

### Requirement: Reads never change a message
Mail reads SHALL NOT change any message's state: listings, searches, snippets and `read_mail` SHALL read with peeking fetches on a mailbox opened read-only, and no read SHALL set `\Seen`.

#### Scenario: Snippet fetch
- **WHEN** `list_mail` returns snippets for unread messages
- **THEN** those messages are still unread afterwards

### Requirement: Full reads with pagination
`read_mail` SHALL return the listing fields for the message plus `body` and the attachment list, whether or not attachments are saved. The body SHALL be the text/plain part; when there is none, it SHALL be the HTML part converted to text, and the result SHALL say which (`body_format`). The body SHALL be returned in pages: a result that does not reach the end SHALL carry `truncated: true`, `total_length` and `next_offset`, and `read_mail` SHALL accept `offset` to continue.

#### Scenario: HTML-only newsletter
- **WHEN** `read_mail` opens a message with only an HTML part
- **THEN** `body` is readable text converted from the HTML and `body_format` is `html-to-text`

#### Scenario: Reading the rest of a long message
- **WHEN** the first `read_mail` page returns `next_offset: 8000`
- **THEN** `read_mail` with `offset: 8000` returns the following text, and the last page has `truncated: false`

### Requirement: Search stays inside the tool budget
`search_mail` SHALL combine the server-side search with the local decoded pass, and the local pass SHALL fetch the headers it scans in batched requests rather than one request per message. `search_mail` SHALL report how many recent messages it scanned.

#### Scenario: Searching the recent window
- **WHEN** `search_mail` scans the newest 300 messages locally
- **THEN** it completes in a small, bounded number of round trips and returns within the tool timeout, with `scanned_recent: 300`
