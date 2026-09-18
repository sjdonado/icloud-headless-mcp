## Purpose

The health importer turns staged Apple Health export files (continuous HealthMirror push plus the one-time official backfill) into a queryable SQLite store with stable identity and propagating deletions, so re-imports are free and the store never disagrees with the device.

## ADDED Requirements

### Requirement: Envelope parsing with split values

The importer SHALL parse every record's shared envelope (`uuid`, `metric`, `recordType`, `start`, `end`, `localDate`, `timezone`, `value`, `unit`, `source`, `sourceBundleId`, `device`, `wasUserEntered`, `recordedAt`, `schemaVersion`) and SHALL split the polymorphic `value`: quantities store an amount, categories store a code plus its readable label. Sleep stages MUST keep their labels; collapsing a stage into a number is a data-loss bug.

#### Scenario: Quantity record imports its amount
- **WHEN** a steps record with an amount value imports
- **THEN** the row carries the numeric amount with its unit and no code or label

#### Scenario: Category record keeps code and label
- **WHEN** a sleep record with a stage code and label imports
- **THEN** the row carries both the code and the label, and deep sleep remains distinguishable from lying awake

### Requirement: Unknown metrics import rather than reject

The importer SHALL import records from metric folders it has never seen, storing them under their folder name with the same envelope mapping.

#### Scenario: New metric folder appears
- **WHEN** a folder for a metric with no dedicated rollup imports
- **THEN** its rows land in the store queryable by metric name instead of erroring

### Requirement: Idempotent import on stable uuid

Re-importing a file SHALL insert zero new rows for unchanged samples. The sample `uuid` is the entire identity mechanism; the importer MUST NOT add a content hash.

#### Scenario: Monthly file re-copied and re-imported
- **WHEN** an unchanged month imports a second time
- **THEN** the run reports zero new rows and row counts are identical

### Requirement: Tombstones propagate, including early arrival

The importer SHALL apply `_tombstones/YYYY-MM.jsonl` deletions by `uuid`, deleting matching sample rows and marking the tombstone applied. A tombstone naming a `uuid` not yet imported SHALL be stored unapplied and applied when the sample later arrives, never discarded.

#### Scenario: Tombstone arrives before its sample
- **WHEN** a tombstone imports in a month whose sample arrives in a later import
- **THEN** the tombstone waits stored and the sample row is deleted on arrival rather than living on

#### Scenario: Deleted sample stays deleted
- **WHEN** a sample deleted on the device re-exports and re-imports after its tombstone
- **THEN** no row for that `uuid` remains in the store

### Requirement: Imports ledger

Every import run SHALL record per file the metric, rows seen, rows new, and import time, so freshness and coverage are answerable from the store itself.

#### Scenario: Coverage question asked
- **WHEN** a consumer asks what has been imported and when
- **THEN** the ledger names each file with its row counts and timestamp

### Requirement: Only the importer writes

The importer SHALL open the database read-write; every query path SHALL open it read-only. No tool, job, or REPL in this change writes to the store.

#### Scenario: Query path opens the store
- **WHEN** any read tool runs against a populated database
- **THEN** it opens read-only and cannot insert, update, or delete

### Requirement: Daily import schedule

The importer SHALL run on a daily schedule owned by this change (timer plus service), independent of the Drive pull schedule, which stays externally owned and unchanged.

#### Scenario: New export lands overnight
- **WHEN** fresh month files stage during the day
- **THEN** the next daily run imports them with no manual step

### Requirement: Backfill is a staged file

The one-time official Apple export SHALL enter through staging like any other file (saved from agent chat by the operator), with no chat-ingestion code in this change.

#### Scenario: Backfill saved to staging
- **WHEN** the operator places the official export in staging and the importer runs
- **THEN** its samples import under the same envelope rules as continuous-push files

### Requirement: Self-test without side effects

The importer SHALL expose a self-test covering envelope parsing for a quantity and a category, an unknown metric folder, a tombstone arriving before its sample, a re-import producing zero new rows, and the day rollup, with no filesystem or network touch.

#### Scenario: Self-test runs on a bare checkout
- **WHEN** the self-test runs where no export files exist
- **THEN** all six cases pass without reading or writing files
