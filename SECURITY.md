# Security

This is not boilerplate, because the thing being secured is unusual. A running install of this server holds a logged-in Apple session on a machine, in the memory of a browser process, with a profile on disk that will resume that session after a reboot. Treat the host the way you would treat a phone that is signed into the same account and never locks.

Read this before pointing it at an Apple ID you cannot afford to lose.

## What an attacker who reaches the box gets

Assume an attacker who gets code execution as the service account. What they have is not a credential problem, it is a session problem.

They get the live iCloud web session, because the resident browser is holding it and the cookie jar on disk persists it. That is read and write access to Notes, Reminders, Calendar, Contacts, Mail and iCloud Drive through the web apps, with no further prompt, for as long as Apple keeps the session alive. Under Advanced Data Protection the grant for a given surface can lapse, which slows an attacker down, and it is not a control: a device approval is exactly the thing the legitimate operator will eventually give.

They get the Apple ID and the app-specific password out of the environment file. An app-specific password authenticates CalDAV, CardDAV, IMAP and SMTP, so it is enough to read all the mail and send as the account. It is revocable from the Apple ID account page without touching the primary password, and revoking it is the first thing to do if the host is compromised.

They get whatever the browser can reach next, which includes Apple ID account pages. This is why the service account exists: it should own the session and the credentials and nothing else, and it should not be the account any other service or any agent runs as.

What they do not automatically get is anything the service account cannot reach, and that is worth arranging deliberately. This account should own the session and the credentials and nothing else. Anything that consumes what this server produces, such as whatever imports the files the Drive pull stages, belongs to a different account, so a compromised browser cannot reach or rewrite what is downstream of it.

The approval gate is a containment control, not an authentication one. Confirmed tools ask over MCP elicitation, and the answer comes back from the client rather than from anything inside this process, so a caller that can talk to the server still cannot produce an approval by itself. An attacker with code execution on the host bypasses it entirely, because they no longer need to go through the tool.

## Set up a private network before the first login

This is a prerequisite, not a hardening step, and it comes before you start the browser stack or type an Apple password anywhere.

The first-time login is an interactive session: a remote desktop onto a browser on a headless machine, into which you type an Apple ID, a password and a two-factor code, and which afterwards holds a logged-in Apple account. The only safe way to do that is over a private network between you and the host.

Tailscale is one example that fits, and the noVNC unit in this repository requires `tailscaled.service` for exactly that reason. Any WireGuard-style private network you control is an equivalent, and an SSH tunnel to a loopback-bound VNC is the smallest version of the same thing. Never expose VNC or noVNC on a public interface, not during setup and not afterwards.

## VNC and noVNC must stay on loopback

Those units exist for one reason: a human has to sit in front of Apple's two-factor prompt at first login, and the browser holding the session is on a machine with no screen. That is a recovery door, and it is a door onto a signed-in Apple account.

Anyone who reaches the VNC port gets the browser as the operator sees it, which means the whole account, without needing a password Apple ever asks for again. A VNC password is not a defence worth relying on for that.

So the VNC unit binds `127.0.0.1` with `-localhost` and is reached over an SSH tunnel, the noVNC front end bridges one loopback port to another, and neither is exposed on a public interface. Neither has an `[Install]` section in this repository, which is deliberate: they are started by hand when a login is needed and stopped afterwards, because a VNC server that returns on every boot is an open door nobody remembers leaving open.

If you front noVNC with anything, front it with something that terminates on the same host or on the private network above, and understand that you are publishing a logged-in Apple session behind whatever that thing's authentication is. The safe default is the SSH tunnel.

Never commit a VNC password file. `*.vncpass` is ignored for that reason.

## The sudoers grants are single-binary on purpose

Two rules ship here, and both name one exact command with no argument that can vary.

One lets a caller spawn the stdio wrapper as the service account, so the credentials are read inside a process the calling uid cannot inspect. The wrapper is owned by the service account rather than by the caller, so the caller can run it and cannot rewrite what it is allowed to run.

The other lets a caller ask Apple to re-issue the web-access grant. It used to grant `systemctl restart` on the browser unit, which worked and was far too large: a restart discards the warm tabs and raises an approval prompt of its own, so a tool meant to satisfy a prompt created one. It is now one root-owned script that re-navigates an app tab and drops to the service account internally. The caller never gains the browser account and cannot stop or restart any unit.

Copy the shape, not just the effect. A rule granting `systemctl` or a command with a free argument gives the caller the whole service, and that is the predictable failure when somebody adapts these files.

## Credentials live in one file that only the service account reads

The Apple ID and the app-specific password live in an environment file owned by the service account at mode 400, sourced by the wrapper inside the process. They are not in the code, not in a unit file, not in a client's configuration, and not in the environment of whatever uid calls the server.

Never load that file into the client's own service unit. An app-specific password can write the whole Apple account, and the calling uid must not be able to read it. This is the single most common way an install of this quietly becomes insecure.

The browser profile and the cookie jar are credentials too, and stronger ones than the password in some respects, because they are the live session. Keep them readable only by the service account, and never copy them off the machine or into a backup you would not protect as a credential.

`.env`, `cookies.json`, `*.vncpass` and any session state are ignored by git here and must never be committed. If one is committed by accident, treat the account as compromised: revoke the app-specific password, sign the session out from an Apple device, and start the profile again.

## Where account content goes

Nowhere except back to the MCP client that asked for it.

No note, reminder, message, calendar event or contact is sent to any third party, any telemetry endpoint, or any service of the author's. This server holds no messaging credential and cannot notify anybody on its own: everything the operator needs to know about a completed write is in the tool result, for the client to relay. There is no analytics, no crash reporting and no phone-home in this codebase.

Two local exceptions are worth naming because they are files on disk rather than network traffic. Saved mail attachments are written to a directory you configure, and that directory is content out of a mailbox: it is held to a type denylist, a size cap, a sanitised basename and an expiry, and it should not be group-writable. The Drive pull stages fetched files in a directory you configure, for another account to import. Both are local, both are yours, and both deserve the permissions the deployment sample gives them.

Everything the browser sees, Apple sees, because the browser is talking to Apple. That is the whole technique and it is not something this server can change.

## Reporting a vulnerability

Report privately, not in a public issue.

Use GitHub's private vulnerability reporting for this repository: https://github.com/sjdonado/icloud-headless-mcp/security/advisories/new

Please include what you found, how to reproduce it, and what an attacker gets. A proof of concept against your own Apple account is welcome; never test against an account that is not yours.

Expect an acknowledgement, a fix or an explanation of why it is not one, and credit in the advisory unless you would rather not have it. Please hold off on public disclosure until a fix is available, and say so if you have a deadline in mind.

This is a personal project with one maintainer and no service level agreement. What is promised is an honest answer, not a fast one.
