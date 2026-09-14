#!/bin/bash
# Spawned through sudo, so this runs as its own service account and the Apple credentials are
# read inside a process the calling uid cannot inspect.
#
# One wrapper, one process, one entry: calendar, contacts and mail over the protocols, Notes and
# Reminders through the resident browser, and the Drive pull's status.
#
# Three things the wrapper supplies and the env file does not, because they are about this host
# rather than about the account:
#
#   DISPLAY                   the headed Chromium the browser-backed modules attach to over CDP
#   PLAYWRIGHT_BROWSERS_PATH  a shared root-owned Chromium, not a per-account copy
#   HOME                      the account's own directory, which every path default is under
#
# Everything else, the credentials included, is in the env file: the zone, the state paths, the
# default list and calendar, and which Drive libraries to pull.
set -euo pipefail
set -a
. /etc/agent/icloud.env
set +a
export HOME=/opt/agent-icloud
export DISPLAY=:99
export PLAYWRIGHT_BROWSERS_PATH=/opt/playwright
exec /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/server.py
