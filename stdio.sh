#!/bin/bash
# Spawned through sudo, so this runs as its own service account and the Apple credentials are
# read inside a process the calling uid cannot inspect.
#
# One wrapper, one process, one entry: calendar, contacts and mail over the protocols, Notes and
# Reminders through the resident browser, and the Drive pull's status. The environment comes from
# icloud-mcp-host; the arguments deliberately do not, so the agent's one sudoers rule can start
# the server and nothing else.
exec /opt/agent-icloud/bin/icloud-mcp-host
