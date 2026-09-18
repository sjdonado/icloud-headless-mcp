# Health extension

## Why

The Drive pull already stages iCloud Drive app-export folders onto the host, but staged files are inert: no consumer reads them. The Apple Health export (continuous push from the HealthMirror iOS app into a Drive folder, plus a one-time backfill of the official Apple export saved from agent chat) is the first consumer. Ingesting it into SQLite and exposing read-only query tools makes health data answerable from anywhere the bridge runs, with the same approval-free read posture as the other read-only surfaces.

## What Changes

- New `health-import` subcommand: parses top-level `<metric>/YYYY-MM.jsonl` plus sibling `_tombstones/YYYY-MM.jsonl` out of a configured export directory and upserts into SQLite. Idempotent on sample `uuid`; unknown metric folders import rather than reject; tombstones apply even when they arrive before their sample. A root with directories but zero readable sample files fails loudly, never reports a zero-new success.
- New read-only MCP tool group (`health_status`, `health_days`, `health_sleep`, `health_effort`, `health_recovery`, `health_sql`): rollups and one guarded read-only SELECT. Only the importer opens the database read-write.
- New neutral config keys (database path, export directory, schedule inputs), documented in README and `.env.example`. No source-app name in code or defaults.
- New daily background job (systemd timer, following the tab-reaper precedent) running the importer. Drive-pull scheduling stays outside the repo, unchanged.
- New dependency: pure-Go SQLite (`modernc.org/sqlite`), keeping `CGO_ENABLED=0` static builds.

## Decisions recorded

- **In-repo separable extension, not a separate repository.** The single-binary layout (`sub_*.go` per subcommand, domain logic under `internal/`) already anticipates this: one `internal/health` package plus one subcommand file plus six tool handlers, off unless configured, with the Drive layer untouched and no Health concept leaking into it. What would flip it: a second consumer domain pulling the importer in conflicting directions, or scheduling needs beyond a daily timer.
- **Source-app naming: HealthMirror is current, HealthBridge is stale.** Confirmed by the operator (HealthMirror, App Store id6779814305). Written only in documentation and env examples, never in code, defaults, table names, or tool names.
- **Backfill path:** the official Apple export is saved from agent chat into staging by the operator (agent-assisted manual step); the importer treats it like any other staged file. No chat-ingestion code in this change.
- **Off-unless-configured means present-but-reporting, not absent.** The six tools stay in the registry (the golden tool list is a frozen contract); with no database configured they report unconfigured, following the `drive_status` unconfigured-means-nothing precedent. Importer with no export directory is a no-op success.

## Capabilities

### New Capabilities

- `health-ingest`: export parsing, idempotent uuid upsert, tombstone semantics including early arrival, imports ledger, `--self-test`, read-write importer discipline, daily scheduling contract.
- `health-query`: the six read-only tools, null-instead-of-zero error semantics, empty-vs-missing distinction, read-only database access, guarded single-SELECT gate.

### Modified Capabilities

(none: the Drive pull, the tool registry contract shape, and existing tools are unchanged)

## Impact

- `cmd/icloud-mcp/`: one new `sub_health*.go` file(s), registry grows 25 to 31 tools, golden tool list updated.
- `internal/health/`: new package (importer, store, query layer).
- `go.mod`: adds `modernc.org/sqlite`; build flags gain `-trimpath -buildvcs=false` for reproducibility.
- `systemd/`: one new timer (+service) for the daily import; `sudoers.d/` unchanged.
- `.env.example`, `README.md`, `docs/SETUP.md`, `docs/ARCHITECTURE.md`, `AGENTS.md`: new keys, new tool rows, scheduling and backfill runbook.
- Runtime cost: one SQLite file under state; importer runs daily, tools are index-backed reads.
