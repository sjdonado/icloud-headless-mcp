## Purpose

What the Health tools return, so an agent can say what a number means (its unit, where it came from, which days it covers) without a follow-up `health_sql` call.

## ADDED Requirements

### Requirement: Units and provenance
Every Health rollup SHALL name the unit of each value it returns, taken from the stored samples, and SHALL name the sources that recorded them. `health_status` SHALL give each metric its unit and sources, and the owner's zone.

#### Scenario: Steps and resting heart rate
- **WHEN** `health_days` returns steps and resting heart rate
- **THEN** its `units` map shows `steps: count` and `resting_bpm: bpm`, and `metrics_used` names the stored metric behind each field

### Requirement: Richer rollups from stored samples
`health_days` SHALL add each day's heart-rate minimum, maximum and average. `health_sleep` SHALL return onset and wake as ISO 8601 in the owner's zone, and split each night into asleep, awake and in-bed minutes next to the per-stage minutes, with the segment count. `health_effort` SHALL add each day's sample count, maximum and average heart rate. `health_recovery` SHALL name the date range of both windows and give the delta as a percentage of the baseline. A value with no samples behind it SHALL be null, never zero.

#### Scenario: A night with an awake stretch
- **WHEN** a night has 400 minutes of sleep stages and 20 minutes labelled awake
- **THEN** `health_sleep` reports `asleep_minutes: 400`, `awake_minutes: 20`, and the onset and wake in the owner's zone
