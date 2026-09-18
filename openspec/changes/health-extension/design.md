## Context

Single static binary (`cmd/icloud-mcp`, one `sub_*.go` per subcommand, domain logic under `internal/`). Tools register by name in `internal/mcpserver` with a frozen golden list; handlers merge in `cmd/icloud-mcp/main.go`; confirmed tools ask through the elicitation gate, read-only tools do not. Config is env keys with fail-fast on missing required values. Scheduling precedent is the tab-reaper systemd timer. The Drive pull stages export folders but never interprets them, and its scheduling stays externally owned. See proposal.md for motivation.

## Goals / Non-Goals

**Goals:**
- A Health consumer that cannot break the generic Drive pull or any existing tool.
- Import semantics a re-run cannot corrupt: uuid identity, early tombstones, ledgered runs.
- Query tools an agent cannot damage anything through: read-only handles, one gated SELECT.

**Non-Goals:**
- No chat ingestion: the backfill arrives as a file the operator places in staging.
- No write tools, no schema migrations beyond `CREATE TABLE IF NOT EXISTS`, no per-metric plugin framework (unknown metrics ride the generic path).

## Decisions

- **`internal/health` in three files: `store.go`, `import.go`, `query.go`.** `store.go` owns the schema constant (ported as-is from the ticket), the read-write open (applies `PRAGMA journal_mode=WAL`) and the read-only open. `import.go` owns parsing, upsert, tombstones, the ledger, and `--self-test`. `query.go` owns the six tool handlers. Alternative (one file) was rejected: the rw/ro split is the safety property and deserves file-level visibility.
- **One subcommand, `health-import`, with a `--self-test` flag.** Follows the `sub_*.go` layout; the timer calls it with no flags daily. Alternative (separate `health-selftest` subcommand) rejected: the ticket's verification item defines self-test as importer behavior, and one binary surface stays reviewable in one `ls`.
- **Read-only tools, no ask.** They join the read-only tier like the other reads; nothing they do reaches the owner's devices, so no elicitation. The SELECT gate plus `mode=ro` plus `PRAGMA query_only=ON` is triple coverage, each layer cheap.
- **SELECT gate: single statement, first keyword SELECT, nothing stacked.** Leading comments/whitespace stripped, at most one trailing semicolon. `WITH` is rejected for now: recursive CTEs are the exfiltration-adjacent edge nobody needs yet; revisit with a recorded decision if a rollup ever needs one.
- **Rollup definitions (tunable, recorded here so output changes are deliberate):** steps/active-energy sums per `localDate`; resting heart rate = minimum of resting samples per `localDate`, null when none; HRV = mean of samples per `localDate`, null when none; effort = summed durations of heart-rate samples at or above the floor; recovery = mean of daily values over each window, baseline annotated with actual days covered; sleep nights keyed by `localDate` of onset with per-stage durations preserved.
- **`localDate` is the day authority; `AGENT_TZ` only anchors "last N days" windows to the owner's today.** No timestamp conversion decides day membership, per the spec.
- **Neutral config, off unless configured:** `HEALTH_DB` defaulting under `ICLOUD_STATE/state/`, `HEALTH_EXPORT_DIR` defaulting to empty (importer no-ops, tools report unconfigured). No app name in code, defaults, tables, or tool names; HealthMirror appears in README/`.env.example` prose only.
- **Timer owns daily runs; Drive scheduling untouched.** New `agent-health-import.service` (oneshot, calls the subcommand) plus `.timer` (daily, persistent catch-up), mirroring the tab-reaper pair. No `sudoers.d` change: nothing here crosses uids.
- **Build stays static and reproducible:** `modernc.org/sqlite` (pure Go; `mattn` rejected: cgo breaks cross-compilation), with `-trimpath -buildvcs=false` added to the build invocations so the binary embeds neither the build machine's home nor the git commit.

## Risks / Trade-offs

- [Risk] `modernc.org/sqlite` grows compile time and binary size for every build, including hosts that never enable Health → Mitigation: accepted; the single-binary layout has no per-feature builds, and the cost is measured once at implementation (tasks record it).
- [Risk] Unapplied tombstones accumulate when a sample never arrives → Mitigation: one row per uuid with an applied flag; the status tool surfaces unapplied counts, so the backlog is visible, not silent.
- [Risk] `health_sql` widens what an agent can read in one call → Mitigation: same local data the other tools already expose, read-only handle plus query-only pragma plus single-SELECT gate; no new data enters scope.
- [Risk] Backfill provenance is an operator step, so a wrong file imports cleanly → Mitigation: imports ledger per file plus the round-trip verification (tasks), and unknown folders import visibly rather than merging silently.

## Migration Plan

Deploy is additive: new package, new subcommand, six new tool names, new env keys, new timer (disabled until enabled). Nothing existing changes behavior, so rollback is stopping/disabling the timer and ignoring or deleting the SQLite file. No data migration: schema is `IF NOT EXISTS` and the per-row `schema_version` rides along untouched for future use.

## Open Questions

(none: rollup formulas above are tunable constants, not unknowns; changing one later is a deliberate diff, not a redesign)
