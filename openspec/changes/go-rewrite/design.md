# Design: Go rewrite

## Context

Current state: one Python process (`server.py` + `app.py` + five `tools/` modules) over stdio, with Notes/Reminders/Drive behind Playwright attached over CDP to a resident headed Chromium. Install is a venv + pip + Playwright/Node driver; the wrapper, units, and sudoers rules form a containment shape that must survive the port. Constraints: strict behavior parity across all 23 tools (see `specs/`), loopback-only operation, single-binary sudoers rules, headed Chromium on Xvfb, and live-Apple verification for anything browser-backed. See `proposal.md` for motivation.

## Goals / Non-Goals

**Goals:**

- One static binary per entry point, no venv/pip/Node toolchain on target hosts, clean ARM cross-compile.
- Same stdio contract, env contract, units, and sudoers shape: the port changes what runs, not how it is contained or deployed.
- A test seam for every deterministic behavior (DAV/IMAP against in-process fakes, pure helpers as unit tests).

**Non-Goals:**

- No new tools, transports, or approval-UX copy changes; user-visible strings stay identical.
- No headless Chromium, no per-call browser launches, no change to the resident-browser architecture.
- No fake-DOM testing of Apple pages: browser surfaces are verified live or not at all.

## Decisions

### Module and binary layout mirrors `bin/` and `tools/`

One Go module. `cmd/icloud-mcp` serves the 23 tools; each `bin/` helper becomes a small `cmd/icloud-<name>` main sharing `internal/` packages (`mcpserver`, `dav`, `mail`, `notes`, `reminders`, `drive`, `browser`, `dvlibraries`, `config`). Rationale: systemd `ExecStart` lines and the two sudoers rules change by path only, keeping the exact-command containment property trivially reviewable. Alternative (one binary with subcommands) was rejected because it rewrites every unit file and both sudoers rules instead of swapping paths.

### `mcp-go` for the protocol layer

Verified: stdio transport plus server-side elicitation over stdio exist upstream. Rationale: only maintained Go SDK with the full surface used here. Alternative (hand-rolled JSON-RPC) rejected: protocol churn becomes our bug surface.

### CDP driver settled by spike: chromedp default, go-rod fallback

Both attach to the existing resident browser over its DevTools endpoint and need no Node runtime. Default to chromedp; Phase 0 spikes `notes_folders` read-only on both and keeps the winner on shadow-DOM piercing ergonomics, trusted pointer-input dispatch, and clipboard read (the canvas-copy flow needs it). Decision gate is a task, not a guess; the loser is deleted the same phase.

### `go-imap/v2` behind a thin internal wrapper

v2 is still beta, but it is where maintenance goes and its client API covers everything used here (select with cond-store awareness, UID search, fetch with body sections). The wrapper isolates beta churn to one file and doubles as the seam for in-process fake-server tests. Alternative (v1 stable) avoids churn but starts the port on a legacy API; rejected. SMTP via `go-smtp`, MIME via `go-message`.

### `go-webdav` + `go-ical`/`go-vcard` for DAV

Only viable Go option for CalDAV/CardDAV clients. Accepted risk: thinner iCloud mileage than Python's `caldav`, so every quirk (expand, SEQUENCE bump, attendee check, `_select`-style error mapping) is ported quirk-for-quirk against the specs parity checklist, not assumed from library docs.

### Config fails fast with the key name

Python raises `KeyError` at import on missing required env; Go SHALL exit non-zero naming the missing key. Same contract (refuse to start, say which key), better message.

### Cutover by wrapper swap with instant rollback

The Go tree builds in parallel while Python stays live and frozen as the reference. Cutover swaps the executed binary in `stdio.sh` (plus unit paths); rollback reverts the wrapper. The parity gate for cutover: all 23 tools plus every session-lifecycle scenario verified live, deterministic suite green.

## Risks / Trade-offs

- [Risk] Browser logic re-verification dominates cost; Apple pages drift under us → Mitigation: port last, verify live in quiet-hours-aware windows, keep Python runnable until the gate passes.
- [Risk] `go-imap/v2` beta API churn → Mitigation: internal wrapper confines it; pin the beta in `go.mod`.
- [Risk] `go-webdav` gaps against iCloud (discovery, reports) → Mitigation: quirk-for-quirk port with live verification; raw PROPFIND/REPORT fallback inside the `dav` package if the client API falls short.
- [Risk] Elicitation edge semantics (empty-object accept, fail-closed paths) differ subtly in `mcp-go` → Mitigation: Phase 0 includes an elicitation smoke test against the real client (Hermes) before any tool logic is ported.
- [Risk] Single-maintainer bandwidth with two live trees → Mitigation: freeze Python at branch start; every Python-side fix during the port is a port-blocking parity item, not a fork.
- [Trade-off] No perf/memory win is expected (recorded in proposal); if the migration stalls, the spike and Phase 1 still leave reusable fakes and helpers.

## Migration Plan

1. Phase 0: scaffold module, tool registry, elicitation smoke test vs Hermes, driver spike with decision gate.
2. Phase 1: DAV/contacts + mail against in-process fakes; deterministic suite green.
3. Phase 2: drive status, `dvlibraries`, and non-browser helpers; systemd/sudoers path-swap drafted but not applied.
4. Phase 3: browser surfaces and Drive fetch mechanics, verified live; session helpers ported.
5. Cutover: apply wrapper/unit path swaps, update install/verification docs, run the full live checklist; rollback is a wrapper revert. Python leaves the install path only after the gate.

## Open Questions

- Go toolchain pin (assumed: current stable in `go.mod` at scaffold time; revisit only if a chosen library requires newer).
- Whether to keep the loopback `streamable-http` branch the Python server carries: default yes for parity, confirmed during Phase 0 when the transport surface is rebuilt.
