## Purpose

Preserves the Drive contract: the server only ever reports on the pull and can never trigger it, with explicit behavior for unconfigured libraries and unsafe paths.

## ADDED Requirements

### Requirement: Status only, never the pull

`drive_status` SHALL report when the pull last ran and what is staged per library, and SHALL NOT offer any operation that triggers a pull. Scheduling the pull SHALL remain outside the repository.

#### Scenario: Status cannot trigger fetching

- **WHEN** any tool or argument combination is attempted
- **THEN** no fetch is started; only the last-run state and staged contents are reported

### Requirement: Unconfigured libraries pull nothing

An empty or unparseable `DRIVE_LIBRARIES` value SHALL pull nothing and SHALL say so rather than guessing at folder names. Only entries with a valid `kind` (`folder`, `tree`, `snapshot`) and a relative traversal-free `dest` SHALL be honored.

#### Scenario: Empty configuration is explicit

- **WHEN** `DRIVE_LIBRARIES` is empty or unparseable
- **THEN** the fetch reports that nothing is configured instead of inferring library names

### Requirement: Staging paths stay relative and safe

Staged paths SHALL be built only from relative components free of parent-directory references and absolute paths; `folder` libraries SHALL preserve the Drive relative layout under `dest`, `tree` SHALL take the configured recent months newest first, and `snapshot` SHALL stage one file rewritten whole.

#### Scenario: Traversal component is rejected

- **WHEN** a library entry or Drive-provided name would escape the staging root
- **THEN** it is rejected with an error instead of being written outside `dest`
