## Purpose

Preserves the session model and its safety properties: the browser process is the session, restarts cost a device approval, recovery stays loopback-bound, and credentials keep their single-file, single-reader containment.

## ADDED Requirements

### Requirement: Resident session model

The session SHALL live in one headed resident Chromium on a virtual display, attaching over CDP rather than launching browsers per call. Its profile SHALL persist cookies so a relaunch resumes signed in. The resident SHALL load only `icloud.com` at startup and open no app tab, so an automatic restart never raises a data-access prompt by itself.

#### Scenario: Restart alone raises no prompt

- **WHEN** the resident process restarts
- **THEN** no approval prompt appears on the owner's devices until the next app-page load

### Requirement: Latch and re-ask discipline

While the blocked latch exists at `$ICLOUD_SHARED_STATE/blocked`, app helpers SHALL refuse before touching the browser with no retries and no restarts. The latch SHALL be released by the re-ask flow (owner at a device) or by a healthy session check. Re-asking SHALL navigate one app, not two, since one grant covers iCloud.com data.

#### Scenario: Latched state does not retry

- **WHEN** the latch exists and a Notes call arrives
- **THEN** the call refuses immediately without touching the browser or scheduling a retry

### Requirement: First login and recovery stay interactive and loopback-bound

A fresh Chromium SHALL be signed in interactively through loopback-only VNC with "Trust this browser" ticked, and an expired session SHALL require the same interactive recovery. VNC/noVNC units SHALL bind loopback only, carry no `[Install]` section, and be started by hand and stopped after use.

#### Scenario: Recovery requires a human at a private endpoint

- **WHEN** the session has expired
- **THEN** only an interactive sign-in over the loopback-bound remote desktop restores it; no automated path bypasses it

### Requirement: Session health contract

The session check SHALL distinguish exactly four outcomes with stable exit codes: 0 healthy, 1 signed out, 2 no browser to talk to, 3 needs device approval.

#### Scenario: Each failure maps to its fix

- **WHEN** the check runs against a signed-out browser, an unreachable browser, and a grant-lapsed browser
- **THEN** it exits 1, 2, and 3 respectively with messages naming the distinct remedy

### Requirement: Restart and privilege boundaries

Restarting the browser, display, VNC, or noVNC units SHALL remain a deliberate decision (it costs warmth plus a device approval), never a side effect of any tool or helper. The sudoers rules SHALL keep naming one exact command with no variable argument, and the env file SHALL stay mode 400, sourced inside the service-account process and never loaded into the client's unit.

#### Scenario: Helpers cannot restart the browser

- **WHEN** any helper or tool runs, including the re-ask flow
- **THEN** no browser, display, or VNC unit is restarted, stopped, or started as a side effect
