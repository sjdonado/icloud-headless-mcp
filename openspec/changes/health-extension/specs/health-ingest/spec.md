## Purpose

The health importer turns staged Apple Health export files (continuous HealthMirror push plus the one-time official backfill) into a queryable SQLite store with stable identity and propagating deletions, so re-imports are free and the store never disagrees with the device.

## ADDED Requirements

### Requirement: Export layout matches what the app writes

The importer SHALL read sample files from top-level metric directories (`<root>/<metric>/YYYY-MM.jsonl`) with `_tombstones/YYYY-MM.jsonl` as their sibling. There is no `raw/` level: that was the design draft's guess, and the real export nests nothing.

#### Scenario: Real staged export imports
- **WHEN** a root holds metric directories beside `_tombstones/`
- **THEN** every monthly file imports under its directory name

### Requirement: Layout mismatch fails loudly

A root holding directories but yielding zero sample files SHALL fail with an error naming the root and what it found, never a zero-new success. An empty root with no directories at all stays a quiet success: nothing staged yet.

#### Scenario: Wrong-level root
- **WHEN** a root holds a `raw/` level (or any other shape) with no readable sample files
- **THEN** the run errors naming the layout it wants instead of reporting success

#### Scenario: Tombstones without samples
- **WHEN** a root holds only tombstone files
- **THEN** the run errors rather than applying deletions while ingesting nothing

### Requirement: Envelope parsing with split values

The importer SHALL parse every record's shared envelope (`uuid`, `metric`, `recordType`, `start`, `end`, `localDate`, `timezone`, `value`, `unit`, `source`, `sourceBundleId`, `device`, `wasUserEntered`, `recordedAt`, `schemaVersion`) and SHALL branch the polymorphic `value` on its `type` field, never on the JSON kind: the app emits an object for both kinds. `{"amount":N,"type":"quantity"}` stores an amount; `{"code":C,"label":L,"type":"category"}` stores a code plus its readable label. A bare JSON number stays a quantity as a guard. Sleep stages MUST keep their labels; collapsing a stage into a number is a data-loss bug. An object whose type is neither quantity nor category, or a quantity with no numeric amount, SHALL fail the file: storing the row would mint a number that silently vanished.

#### Scenario: Quantity record imports its amount
- **WHEN** a steps record with an object amount value imports
- **THEN** the row carries the numeric amount with its unit and no code or label

#### Scenario: Category record keeps code and label
- **WHEN** a sleep record with a stage code and label imports
- **THEN** the row carries both the code and the label, and deep sleep remains distinguishable from lying awake

#### Scenario: Unknown value shape fails the file
- **WHEN** a record carries an object value with an unrecognized type, or a quantity with no amount
- **THEN** the file errors and its transaction rolls back instead of storing a mislabeled row

### Requirement: Unknown metrics import rather than reject

The importer SHALL import records from metric folders it has never seen, storing them under their folder name with the same envelope mapping.

#### Scenario: New metric folder appears
- **WHEN** a folder for a metric with no dedicated rollup imports
- **THEN** its rows land in the store queryable by metric name instead of erroring

### Requirement: Idempotent import on stable uuid, with visible refreshes

Re-importing a file SHALL insert zero new rows for unchanged samples. The sample `uuid` is the entire identity mechanism; the importer MUST NOT add a content hash. An existing row whose parsed content differs SHALL be refreshed and counted as updated, separately from inserts, so `0 new` always means nothing changed: identical content writes nothing at all. Tombstoned uuids never materialize either way.

#### Scenario: Monthly file re-copied and re-imported
- **WHEN** an unchanged month imports a second time
- **THEN** the run reports zero new and zero updated rows and row counts are identical

#### Scenario: Re-import meets changed content
- **WHEN** a month re-imports with one sample's parsed content different
- **THEN** the run reports zero new and one updated, and the stored row carries the new content

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

The importer SHALL expose a self-test covering envelope parsing for a quantity and a category in the real object shapes, an unknown metric folder, a tombstone arriving before its sample, a re-import producing zero new rows, a double import preserving the stored number, and the day rollup, with no filesystem or network touch.

#### Scenario: Self-test runs on a bare checkout
- **WHEN** the self-test runs where no export files exist
- **THEN** all seven cases pass without reading or writing files

### Requirement: Workout is a third value type

The exporter emits workouts as objects with no single number. The importer SHALL store them as `value_type='workout'` with the raw value object in `value_label`: strictly better than the three NULLs the old importer wrote, and distinct from the quantity bug (where a number existed and was discarded).

#### Scenario: Workout record imports whole
- **WHEN** a workout record with duration, distance, energy, and workout type imports
- **THEN** the row keeps the full object readable with no numeric columns set

### Requirement: Absent optional strings store NULL

Fields absent from a record SHALL store NULL, never `""`: empty string asserts "known to be blank" where NULL means absent, `WHERE ... IS NULL` must keep matching, and adopting an existing store must not rewrite rows for no semantic gain.

#### Scenario: Record without unit or device
- **WHEN** a record carrying no unit or device imports
- **THEN** those columns are NULL, and a later identical re-import reports zero updated

### Requirement: Failed runs report committed progress

Files commit one transaction at a time, so a failing run leaves committed work behind. The importer SHALL print per-file lines plus totals for everything that succeeded before reporting the failure, so the operator knows the state the store is already in before re-running.

#### Scenario: Third file of five fails
- **WHEN** a run fails on the third file
- **THEN** the output names the two committed files with their counts and totals before the error
