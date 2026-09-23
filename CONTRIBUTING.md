# Contributing

Thanks for looking. This project drives a real, logged-in Apple account, so a few rules are stricter than they would be in a normal repository. Read [`SECURITY.md`](SECURITY.md) before you touch anything related to credentials, the browser session, or the sudoers rules.

## Before you start

- **Never commit a credential or session artefact.** `.env`, `cookies.json`, `state.json`, `*.vncpass`, any SQLite store and any browser profile are gitignored, and that is not an invitation to find a way around it. If one lands in a commit by accident, treat the account as compromised: revoke the app-specific password, sign the session out from an Apple device, and rebuild the profile.
- **Do not widen the sudoers rules.** Each rule names one exact command with no variable argument, which is what keeps the containment shape reviewable. A rule granting `systemctl`, a shell, or a command with a free argument hands the caller the whole service.
- **Do not restart the browser to make something work.** Restarting `agent-browser`, `agent-xvfb`, `agent-vnc` or `agent-novnc` discards the warm session and costs the owner a device approval. A navigation is usually enough, and it leaves the browser standing.

## Checks

From the repo root, all three must pass:

```
go build ./...
go vet ./...
go test ./...
```

`go test ./...` is deterministic and offline: it talks to in-process fakes, never to Apple. There is no configured linter or typecheck, and no formatter beyond `gofmt`; run `gofmt -l .` and expect silence.

Both Linux targets must build, because the release publishes both:

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -o /dev/null ./cmd/icloud-mcp
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -o /dev/null ./cmd/icloud-mcp
```

Keep the build `CGO_ENABLED=0`. A dependency that needs cgo ends the static cross-compiled build, which is the whole deployment story.

## Things that bite

These are the mistakes this codebase has already made once. [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#pitfalls) has the full list.

- **Re-test concurrency after a change to the shared browser.** Notes and Reminders each hold a per-app lock on one browser; calendar, contacts and mail take no lock. After touching any of it, call `list_mail` while a Notes call holds its lock.
- **Keep the MCP timeout above the elicitation timeout.** The README uses 330 seconds for a reason: a browser-backed call routinely takes 60 to 90 seconds and a cold app tab longer.
- **A quiet-hours refusal is not a failure.** Fresh app-page loads refuse from 23:00 through 06:59 owner-local by design, so no prompt wakes the owner at night.
- **Judge signed-in state from page content, never from a cookie.** A signed-out page renders an app-shaped shell that accepts clicks and discards them.
- **Verify writes against what the app shows.** The web apps rewrite a matched row's title around the search hit, so verification matches the longest word with the ellipsis stripped. If a write cannot be confirmed, say so rather than reporting success.
- **`AGENT_TZ` is load-bearing.** A guessed zone moves a reminder by hours and nothing errors.

## Tests

A change that adds behaviour adds a test for it, and the test should fail if the logic is deleted. Prefer the smallest test that pins the property: this repo has no test framework beyond `go test`, and no fixtures beyond in-process fakes.

If your change touches something the offline suite cannot reach, say what you verified by hand and how, in the pull request. "Verified live against a real account" with the exact calls is worth more than a green suite that never left the process.

## Pull requests

- One line of work, one branch, one pull request.
- Say what changed, why, and what you ran. If you deviated from an agreed plan, say so and why.
- Keep unrelated refactors out. A review that has to separate two intentions is a review that misses a bug.
- Never merge your own pull request; leave it for review.

## Reporting a bug

Include the tool name, the arguments, the exact result, and whether the session was healthy at the time (`icloud-mcp session-check`). Redact your Apple ID, any message content, any note body, and anything from the env file. If the bug involves credentials or the session, report it privately as [`SECURITY.md`](SECURITY.md) describes rather than opening an issue.

## What is most useful

- **Live verification of the write paths.** They are implemented and unit-tested but have not been run against Apple. A careful report of a first live `create_event`, `send_mail` or `update_note` is the single most valuable contribution right now.
- **Fixes for the browser-backed surfaces when Apple changes the DOM.** They will break eventually; the tool should refuse rather than lie.
- **Another consumer for the Drive pull.** The staging layer is deliberately general and knows nothing about what it fetches.
