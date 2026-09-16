## Purpose

Preserves the mail behaviors clients rely on: reads that never disturb mailbox state, honest search semantics over an unreliable server-side search, safe attachment handling, and confirmed sends.

## ADDED Requirements

### Requirement: Reads never mark seen

`list_mailboxes`, `list_mail`, `read_mail`, and `search_mail` SHALL be read-only over IMAP and SHALL never mark a message as seen. Read/unseen state SHALL be derived from dedicated UID searches, never from fetch-tuple side effects.

#### Scenario: Reading leaves state untouched

- **WHEN** `read_mail` opens an unread message, with or without saving attachments
- **THEN** the message remains unread on the server

### Requirement: Mailbox selection failures name the mailbox

Selecting a mailbox that does not exist SHALL raise an error naming the requested mailbox and the mailboxes that do exist, rather than leaking a connection-state error from a later command.

#### Scenario: Unknown mailbox errors clearly

- **WHEN** `list_mail` names a mailbox that does not exist
- **THEN** the error names that mailbox and lists the available ones, and suggests `list_mailboxes` rather than guessing

### Requirement: Honest union search

`search_mail` SHALL union a server-side pass with a local pass over recent messages with decoded headers, SHALL sort newest first rather than slicing server order, and SHALL report `matched` and `scanned_recent` so a caller can tell a slice from a whole answer.

#### Scenario: Encoded subject still matches

- **WHEN** searching a word that appears only inside a MIME-encoded subject of a recent message
- **THEN** the message appears in results via the local decoded pass with accurate `matched`/`scanned_recent` counts

### Requirement: Attachment safety limits

Saved attachments SHALL be held to the same four limits: a type denylist including archives, a size cap (nothing over 25 MB), basenames reduced to a safe character set, and an expiry swept on every call. Every refusal SHALL appear in `attachments_skipped` with its reason, so an unsaved attachment never reads the same as no attachment.

#### Scenario: Oversized attachment is refused loudly

- **WHEN** `read_mail` with saving encounters an attachment over the size cap
- **THEN** the file is not written and `attachments_skipped` names it with the reason

### Requirement: Confirmed send

`send_mail` SHALL elicit approval of the exact message before sending and SHALL send nothing when approval is absent or declined.

#### Scenario: Send requires approval of the exact message

- **WHEN** `send_mail` is called
- **THEN** the elicitation carries the exact recipients, subject, and body, and nothing is sent until approved
