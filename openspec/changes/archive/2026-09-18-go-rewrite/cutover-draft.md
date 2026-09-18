# Cutover draft (task 3.4): path swaps for the Go binaries

DRAFT ONLY. Nothing here is applied: `systemd/`, `sudoers.d/`, and
`stdio.sh` are untouched until the Phase 5 parity gate passes. When it
does, apply exactly these edits and nothing else; the shape of every rule
stays identical, only paths change.

## Binaries

The install lays down compiled Go binaries next to the Python files it
replaces, one per `cmd/` entry:

| Python (today)                | Go (cutover)                        |
| ---                           | ---                                 |
| `bin/server.py`               | `bin/icloud-mcp`                    |
| `bin/session_check.py`        | `bin/icloud-session-check`          |
| `bin/icloud_resident.py`      | `bin/icloud-resident`               |
| `bin/icloud-login.sh`         | `bin/icloud-login.sh` (unchanged wrapper; calls the Go resident) |
| `bin/reask_access.py`         | `bin/icloud-reask`                  |
| `bin/icloud_drain.py`         | `bin/icloud-drain`                  |
| `bin/tab_reaper.py`           | `bin/icloud-tab-reaper`             |
| `bin/icloud_drive_fetch.py`   | `bin/icloud-drive-fetch`            |

No `.venv`, no `requirements.txt`, no Playwright/Node toolchain on the
host. System Chromium (or the existing shared build) remains for the
resident browser.

## `stdio.sh`

One line changes:

```
- exec /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/server.py
+ exec /opt/agent-icloud/bin/icloud-mcp
```

Everything else in the wrapper (env sourcing, `HOME`, `DISPLAY`,
`PLAYWRIGHT_BROWSERS_PATH`-equivalent) stays: containment still comes
from the uid, and the env file is still read inside the service-account
process only.

## `systemd/agent-browser.service`

```
- ExecStart=/opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/icloud_resident.py
+ ExecStart=/opt/agent-icloud/bin/icloud-resident
```

User, working directory, `DISPLAY`, and `Restart=on-failure` unchanged.
`agent-xvfb`, `agent-vnc`, and `agent-novnc` are untouched (no Python).

## `systemd/agent-tab-reaper.service`

```
- ExecStart=/opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/tab_reaper.py 15
+ ExecStart=/opt/agent-icloud/bin/icloud-tab-reaper 15
```

The timer is untouched.

## `bin/agent-reask-access`

The `runuser` line swaps the interpreter for the binary:

```
- /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/reask_access.py
+ /opt/agent-icloud/bin/icloud-reask
```

Root ownership, `runuser -u agent-icloud`, and the environment it sets
stay exactly as they are.

## `sudoers.d/`

Neither rule changes at all: one already names `stdio.sh`, the other
names `/usr/local/bin/agent-reask-access`, and both keep working because
the wrappers keep their paths. This is the point of the layout decision
in `design.md`: the single-binary property is trivially reviewable in
the cutover diff.

## Rollback

Revert this file's edits (wrapper first): the Python tree stays on disk
until the gate passes, so a revert restores the previous behavior with
no rebuild. Python leaves the install path only after the gate.
