## 1. Phase 0 scaffold and spikes

- [x] 1.1 Scaffold the Go module with `cmd/icloud-mcp`, helper `cmd/` stubs, and `internal/` packages; pin the toolchain in `go.mod`
- [x] 1.2 Register all 23 tools with exact names and tiers behind a stub handler; verify `tools/list` output matches the Python server
- [x] 1.3 Elicitation smoke test: confirmed-tool ask, accept, decline, and no-back-channel refusal verified against the real client
- [x] 1.4 Confirm whether the loopback `streamable-http` branch stays for parity
- [x] 1.5 Driver spike: read-only `notes_folders` flow on chromedp and go-rod; pick the winner on shadow-DOM, trusted input, and clipboard; delete the loser

## 2. Phase 1 DAV, contacts, and mail

- [x] 2.1 Port config contract: env parsing, required-key fail-fast naming the key, `AGENT_TZ` plus `AGENT_TZ_FILE` precedence
- [x] 2.2 Port calendar/contacts against an in-process fake CalDAV/CardDAV server: expand, named-zone conversion, read-back writes, SEQUENCE bump, attendee-conditional approval, confirmed delete
- [x] 2.3 Port mail against an in-process fake IMAP/SMTP server: never-mark-seen reads, mailbox error naming, UID SEARCH flags, union search with `matched`/`scanned_recent`, attachment limits with `attachments_skipped`, confirmed send
- [x] 2.4 Deterministic suite green: `go vet` plus `go test` covering all Phase 1 specs scenarios

## 3. Phase 2 Drive status and non-browser helpers

- [x] 3.1 Port `dvlibraries` (entry validation, safe staging paths, folder/tree/snapshot semantics) with unit tests
- [x] 3.2 Port `drive_status` (status only, last-run reporting, unconfigured-means-nothing)
- [x] 3.3 Port non-browser helpers: `session_check` with 0/1/2/3 exit codes, drain, tab reaper
- [x] 3.4 Draft the systemd/sudoers path swaps without applying them

## 4. Phase 3 browser surfaces and session helpers

- [ ] 4.1 Port Notes tools and verify live: folders, list, title-checked canvas read with retry-then-refuse, confirmed update, create-only create
- [ ] 4.2 Port Reminders tools and verify live: lists, verified writes with longest-word matching, date ordering with Save-to-commit, `due_error` on hour mismatch
- [ ] 4.3 Port Drive fetch mechanics and verify live: request-context API calls, etag skips, staging layout
- [ ] 4.4 Port resident launcher, login/bootstrap, and re-ask; verify latch discipline, quiet-hours refusal, and Trust-this-browser recovery live

## 5. Cutover and docs

- [ ] 5.1 Run the full parity gate: all 23 tools plus every session-lifecycle scenario verified live against the specs
- [ ] 5.2 Apply the wrapper/unit path swaps; verify `stdio.sh </dev/null` smoke and `session_check` from the calling uid
- [ ] 5.3 Update install/dependencies/verification docs (`README.md`, `docs/SETUP.md`, `AGENTS.md`, `SECURITY.md` toolchain paragraphs)
- [ ] 5.4 Remove Python from the install path; confirm rollback (wrapper revert) documented and tested once
