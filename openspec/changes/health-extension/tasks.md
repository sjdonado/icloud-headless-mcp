## 1. Store and config

- [x] 1.1 Add `internal/health/store.go` with the ticket schema verbatim, read-write open applying `PRAGMA journal_mode=WAL`, and read-only open (`mode=ro` plus `PRAGMA query_only=ON`); verify `go vet` passes on the package
- [x] 1.2 Add neutral config keys (`HEALTH_DB` defaulting under `ICLOUD_STATE/state/`, empty-by-default export dir) with `.env.example` entries; verify unset keys fail soft (importer no-ops, tools report unconfigured) per the spec
- [x] 1.3 Vendor `modernc.org/sqlite`, add `-trimpath -buildvcs=false` to the build invocations, and record binary-size plus clean-build time delta in the PR; verify `GOOS=linux GOARCH=amd64 go build` and `GOOS=linux GOARCH=arm64 go build` stay `CGO_ENABLED=0`

## 2. Importer

- [ ] 2.1 Implement envelope parsing with quantity/category split in `internal/health/import.go` and verify quantity amount plus sleep code-and-label land on the right columns
- [ ] 2.2 Implement uuid-keyed idempotent upsert with unknown-metric passthrough and verify re-importing a month reports zero new rows with identical counts
- [ ] 2.3 Implement tombstones with store-then-apply for early arrivals plus the per-file imports ledger and verify a tombstone imported before its sample deletes the row on arrival
- [ ] 2.4 Implement `health-import --self-test` (envelope quantity and category, unknown metric, early tombstone, zero-new-row re-import, day rollup) and verify it passes on a bare checkout with no files

## 3. Query tools

- [x] 3.1 Implement `health_status`, `health_days`, `health_sleep`, `health_effort`, `health_recovery` with the design rollup formulas and verify null-with-reason output for errored metrics and named blind spots
- [x] 3.2 Implement `health_sql` with the single-SELECT gate (leading comments stripped, one trailing semicolon at most, `WITH`/stacked/writes refused) and verify each refusal class plus one legitimate SELECT
- [x] 3.3 Register the six tools read-only with no ask, extend the golden tool list, and verify `tools/list` shows 25 to 31 with tiers intact

## 4. Scheduling, docs, integration

- [x] 4.1 Add `cmd/icloud-mcp/sub_health*.go`, the daily `agent-health-import` timer plus service, and the backfill runbook (operator places the official export in staging); verify the timer fires the subcommand on the target host layout
- [x] 4.2 Update README tool rows, `.env.example`, `docs/SETUP.md`, `docs/ARCHITECTURE.md`, and `AGENTS.md`, writing the source-app name only in prose; verify no Health concept appears in Drive-layer code or defaults
- [x] 4.3 Round-trip a real export month twice through the importer and verify the second run reports zero new rows; delete one sample on the device, re-export, re-import, and verify the row is gone
- [x] 4.4 Exercise every tool against a populated database and against an empty one and verify the empty case says so rather than returning zeros; run `go vet`, `gofmt`, and both linux builds clean

## 5. Real-data follow-ups (feedback round 1, applied deferred: unverified)

- [x] 5.1 Match the real staged layout (top-level metric dirs, `_tombstones` sibling, no `raw/` level); fail loudly naming root and contents when directories exist but zero sample files read; empty root stays quiet. Fixtures moved, mismatch tests added
- [x] 5.2 Record exclusive database ownership (service account owns, importer sole writer, tools read-only; adopting another account's store is a manual copy, never a live pointer) in README, SETUP, ARCHITECTURE, and the config comment; print the store path on import
- [x] 5.3 Add the `health_status` staleness verdict (`stale`, `newest_sample_date`, `days_since_newest`; stale past yesterday) plus `last_sample_import` distinct from newest-file `last_import`; spec and tests updated

## 6. Real-data Bug 3 follow-ups (feedback round 2, applied deferred: unverified)

- [x] 6.1 Branch value parsing on value.type (object quantity/category), keep the bare-number guard, fail files on unknown types and amount-less quantities; convert every fixture to the three real shapes
- [x] 6.2 Replace unconditional rewrite with refresh-on-difference (single UPDATE with null-safe mismatch predicate, updated counted separately, identical writes nothing); surface updated counts in FileResult and CLI output; ledger schema unchanged
- [x] 6.3 Extend --self-test to seven cases with real fixtures plus double-import value preservation; add unit tests for typed objects, unchanged double import, and changed re-import counting

## 7. Real-data round 3 follow-ups (feedback round 3, applied deferred: unverified)

- [x] 7.1 Store workout objects as value_type='workout' with the raw object in value_label; unknown types and amount-less quantities still fail the file
- [x] 7.2 Store absent optional strings as NULL (record fields to pointers, label empties to nil, NullString scans in Status/Sleep/Effort) so adopted stores compare identical
- [x] 7.3 Report committed per-file lines plus totals before the import error, so a failed run never reads as "nothing happened"
- [x] 7.4 Unit tests for typed workout, NULL absence, partial-progress return values

## 8. Real-data round 4 follow-up (feedback round 4, applied deferred: unverified)

- [x] 8.1 Rename missing[].metric to missing[].rollup with reason plus folder_hints (a never-imported input has no stored name to use; the old field promised an unmakable match)
