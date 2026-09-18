## Purpose

Six read-only MCP tools answer health questions from the ingested store with honest gaps: missing data is reported as missing with its reason, never smoothed into a number, and an empty answer is distinguishable from an absent one.

## ADDED Requirements

### Requirement: Six read-only tools

The system SHALL expose `health_status`, `health_days(days=14)`, `health_sleep(nights=14)`, `health_effort(days=14, floor_bpm=100)`, `health_recovery(recent=7, baseline=28)`, and `health_sql(query)`, all read-only.

#### Scenario: Each tool answers from a populated store
- **WHEN** each of the six tools runs against a populated database
- **THEN** each returns its documented shape without writing

### Requirement: Status names coverage, freshness, and blind spots

`health_status` SHALL report which metrics are present, how recent each is, and which expected rollup inputs lack backing data, naming the blind spots explicitly rather than implying completeness. Missing entries SHALL name the rollup input (not a metric: no stored name exists for data never imported) with its reason and the folder substrings resolution tried.
#### Scenario: Metric never imported
- **WHEN** status runs with no HRV samples ever imported
- **THEN** HRV appears as missing with its reason, not as zero or as absent from the answer

### Requirement: Day rollup keeps authoritative days

`health_days` SHALL roll up per-day steps, active energy, resting heart rate, and HRV, assigning each sample to its export `localDate`, never to a converted zone date.

#### Scenario: Samples near midnight
- **WHEN** a sample's UTC date differs from its export `localDate`
- **THEN** it counts toward the `localDate` day

### Requirement: Sleep keeps stage labels

`health_sleep` SHALL report sleep by night preserving stage labels rather than reducing a night to a single number.

#### Scenario: Mixed-stage night
- **WHEN** a night holds deep, core, REM, and awake segments
- **THEN** the answer keeps each stage with its durations

### Requirement: Effort honors the caller floor

`health_effort` SHALL report time above the caller's `floor_bpm` (default 100) over the given days.

#### Scenario: Custom floor requested
- **WHEN** effort runs with `floor_bpm=120`
- **THEN** only time at or above 120 counts

### Requirement: Recovery compares recent against baseline

`health_recovery` SHALL compare the recent window (default 7 days) against the longer baseline (default 28 days).

#### Scenario: Short history present
- **WHEN** recovery runs with only 10 days imported
- **THEN** the baseline covers what exists and says so, rather than failing or inventing history

### Requirement: Null with reason, never zero, for failures

Any metric that errored SHALL be reported as null carrying its reason. An empty result SHALL be distinguishable from a missing metric: "no data" is never returned as a number.

#### Scenario: Metric errors mid-rollup
- **WHEN** resting heart rate cannot be computed for a day that has steps
- **THEN** steps report their number and resting heart rate reports null with the reason

#### Scenario: Tools run against an empty store
- **WHEN** every tool runs with no samples imported
- **THEN** each says the store is empty rather than returning zeros

### Requirement: health_sql allows exactly one read-only SELECT

`health_sql` SHALL accept one read-only SELECT statement and reject anything else, including stacked statements, writes, pragmas that mutate, and attaches.

#### Scenario: Non-SELECT submitted
- **WHEN** `health_sql` receives an INSERT, UPDATE, DELETE, DROP, or multi-statement string
- **THEN** it refuses without touching the database

### Requirement: Unconfigured tools report instead of failing

With no database configured, every health tool SHALL report unconfigured (following the `drive_status` precedent) rather than erroring.

#### Scenario: Extension never configured
- **WHEN** any health tool runs with no database path set
- **THEN** it answers unconfigured with what to set, and the tool stays registered

### Requirement: Status carries a staleness verdict

`health_status` SHALL report the newest sample date, the days since it, and an explicit stale verdict alongside the per-metric dates. Data ending yesterday is normal (today is still being written); anything older is stale, said outright. Complete coverage over stale data MUST NOT read as fresh.

#### Scenario: Week-old data with full coverage
- **WHEN** status runs with every metric present but the newest sample seven days old
- **THEN** it reports `stale: true` with the newest sample date and days-since count beside the per-metric dates

### Requirement: Newest sample import reported distinctly

`health_status` SHALL report the newest sample-file import separately from the newest file of any kind, so a tombstones-only run never reads as fresh sample data.

#### Scenario: Tombstone file imports last
- **WHEN** the newest ledger row is a tombstone file
- **THEN** `last_import` names it literally while `last_sample_import` still names the newest sample file
