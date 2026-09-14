#!/usr/bin/env python3
"""Close idle iCloud app tabs so Chromium stops leaking disk.

Chromium keeps unlinked shared-memory files in /tmp, one set per renderer, and never
returns them while the renderer lives. Measured on this box: about 11 MB per minute per
open app tab, which reached 11.8 GB across two tabs left open for 42 hours. `du` cannot see
it, because the files are deleted-but-open, so the disk fills without anything looking
large.

The files are in /tmp rather than /dev/shm because of `--disable-dev-shm-usage`, and that
flag stays: /dev/shm is 1.9 GB against 3.7 GB of RAM, so moving the leak there trades a
slow visible disk problem for Chromium being killed within hours, taking the iCloud session
with it. Disk is the right place for a leak; the fix is to stop leaking.

Warmth is worth keeping, since loading an iCloud app costs 20 to 40 seconds, so a tab is
only closed once it has gone unused for a while. Closing a tab never touches the session:
that lives in the browser process and the profile, and the next call simply pays one cold
load.
"""
import json
import sys
import time
import urllib.request
from pathlib import Path

from icloud_lib.icloud_tabs import STATE_DIR

CDP = "http://127.0.0.1:9222"
STATE = STATE_DIR
IDLE_MINUTES = int(sys.argv[1]) if len(sys.argv) > 1 else 15

# The apps that get their own warm tab. The bare icloud.com page is left alone: it is the
# anchor the session was established on, and it is idle rather than growing.
APPS = {"notes": "/notes/", "reminders": "/reminders/"}


def cdp(path: str):
    with urllib.request.urlopen(f"{CDP}{path}", timeout=10) as r:
        body = r.read().decode()
    return json.loads(body) if body.strip().startswith(("[", "{")) else body


def last_used(app: str) -> float:
    marker = STATE / f"{app}.last"
    try:
        return marker.stat().st_mtime
    except FileNotFoundError:
        # No marker means nothing has claimed the tab since the reaper was installed.
        return 0.0


def main() -> int:
    try:
        tabs = cdp("/json/list")
    except Exception as exc:
        print(f"browser not reachable: {type(exc).__name__}")
        return 0  # not an error: the browser may be deliberately down

    now = time.time()
    closed = []
    for app, fragment in APPS.items():
        idle_for = (now - last_used(app)) / 60
        if idle_for < IDLE_MINUTES:
            continue
        for tab in tabs:
            if tab.get("type") != "page" or fragment not in tab.get("url", ""):
                continue
            try:
                cdp(f"/json/close/{tab['id']}")
                closed.append(f"{app} (idle {idle_for:.0f}m)")
            except Exception as exc:
                print(f"could not close {app}: {type(exc).__name__}")

    print("closed: " + ", ".join(closed) if closed else "nothing idle enough to close")
    return 0


if __name__ == "__main__":
    sys.exit(main())
