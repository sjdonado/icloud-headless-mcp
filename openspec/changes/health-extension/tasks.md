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
