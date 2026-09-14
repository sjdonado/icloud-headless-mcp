#!/bin/bash
# Brings up a virtual display and a loopback-only VNC server, then opens iCloud in a headed
# Chromium bound to the persisted profile. Loopback-only on purpose: reach it through an SSH
# tunnel, never over the public interface.
#
# Detaches, so the login survives an SSH disconnect. Progress goes to /tmp/icloud-login.log.
# -noshm because the viewer and the X server run as different users here.
set -euo pipefail
export DISPLAY=:99
export PLAYWRIGHT_BROWSERS_PATH=/opt/playwright

pkill -f "icloud_session.py login" 2>/dev/null || true
pkill -f "x11vnc -display :99" 2>/dev/null || true
# Xvfb is a systemd service now (agent-xvfb), never killed here
sleep 1

sleep 2
x11vnc -display :99 -localhost -rfbauth "$HOME/.vncpass" -forever -noshm -quiet \
  >/tmp/x11vnc.log 2>&1 &
sleep 1

nohup /opt/agent-icloud/.venv/bin/python /opt/agent-icloud/bin/icloud_session.py login \
  >/tmp/icloud-login.log 2>&1 &
sleep 12
echo "VNC on 127.0.0.1:5900 (loopback, password required). Log: /tmp/icloud-login.log"
cat /tmp/icloud-login.log
