#!/bin/sh
# Install icloud-headless-mcp from the rolling latest build in one line:
#
#   curl -fsSL https://raw.githubusercontent.com/sjdonado/icloud-headless-mcp/main/install.sh | sh
#
# Every merge to main republishes that release, so this always installs
# latest main, stamped with its commit in `icloud-mcp version`. There are
# no pinned versions: a pinning argument is refused rather than silently
# installing latest.
#
# POSIX sh, run as root on the always-on Linux host. Lays down the worked
# example from README (service account agent-icloud, /opt/agent-icloud,
# loopback-only browser stack). A different prefix or account means the
# manual install in README instead of this script.
set -eu

REPO="sjdonado/icloud-headless-mcp"
PREFIX="/opt/agent-icloud"
ACCOUNT="agent-icloud"

if [ $# -gt 0 ]; then
  echo "pinned versions are gone with tag releases; this installs latest main" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root (it creates a service account and writes units):" >&2
  echo "  curl -fsSL https://raw.githubusercontent.com/$REPO/main/install.sh | sudo sh" >&2
  exit 1
fi
if [ "$(uname -s)" != "Linux" ]; then
  echo "this installer targets the always-on Linux host, not $(uname -s)" >&2
  exit 1
fi
case "$(uname -m)" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac
for dep in curl tar sha256sum systemctl visudo useradd; do
  command -v "$dep" >/dev/null 2>&1 || { echo "missing dependency: $dep" >&2; exit 1; }
done

BASE="https://github.com/$REPO/releases/latest/download"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
cd "$tmp"
# The asset name carries the build commit, which the latest redirect does
# not reveal, so resolve it first: same directory, one glob, no jq.
curl -fsSL "$BASE/sha256sums.txt" -o sha256sums.txt
TARBALL="$(awk '{print $NF}' sha256sums.txt | grep -F "linux-$ARCH.tar.gz" | head -n 1)"
[ -n "$TARBALL" ] || { echo "no linux-$ARCH asset in this release" >&2; exit 1; }
curl -fsSL "$BASE/$TARBALL" -o "$TARBALL"
sha256sum -c sha256sums.txt --status --ignore-missing || { echo "checksum mismatch, refusing to install" >&2; exit 1; }
tar xzf "$TARBALL"
SRC="$tmp/${TARBALL%.tar.gz}"

id "$ACCOUNT" >/dev/null 2>&1 || useradd -r -m -d "$PREFIX" -s /usr/sbin/nologin "$ACCOUNT"
install -d -o "$ACCOUNT" -g "$ACCOUNT" -m 700 "$PREFIX/bin"
install -o "$ACCOUNT" -g "$ACCOUNT" -m 755 -t "$PREFIX/bin" \
  "$SRC/bin/icloud-mcp" "$SRC/bin/stdio.sh" "$SRC/bin/icloud-mcp-host" "$SRC/bin/agent-reask-access"
install -o root -g root -m 700 "$SRC/bin/agent-reask-access" /usr/local/bin/agent-reask-access
install -o root -g root -m 440 "$SRC/sudoers.d/agent-icloud" /etc/sudoers.d/agent-icloud
install -o root -g root -m 440 "$SRC/sudoers.d/agent-browser-restart" /etc/sudoers.d/agent-browser-restart
visudo -c
install -o root -g root -m 644 -t /etc/systemd/system "$SRC"/systemd/*

if [ ! -f /etc/agent/icloud.env ]; then
  install -d -o root -g root -m 755 /etc/agent
  install -o "$ACCOUNT" -g "$ACCOUNT" -m 400 "$SRC/.env.example" /etc/agent/icloud.env
  echo "wrote a blank env file to /etc/agent/icloud.env: fill in the Apple ID and app-specific password (and AGENT_TZ on a UTC host)"
fi

# The only browser dependency: a system Chromium, run headless. No display,
# no VNC: the login door streams the headless browser itself.
if [ -z "${CHROMIUM_BIN:-}" ] && ! command -v chromium >/dev/null 2>&1 && ! command -v chromium-browser >/dev/null 2>&1 && ! command -v google-chrome >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y chromium
  else
    echo "install a system Chromium (or set CHROMIUM_BIN), then re-run" >&2
    exit 1
  fi
fi

# Upgrades from the X-display era: the virtual display, VNC and noVNC units
# are gone. Stopping the display ends a headed browser, and systemd brings
# agent-browser straight back headless on the same profile.
for unit in agent-novnc agent-vnc agent-xvfb; do
  systemctl disable --now "$unit" >/dev/null 2>&1 || true
  rm -f "/etc/systemd/system/$unit.service"
done
rm -f "$PREFIX/bin/icloud-login.sh"

systemctl daemon-reload
systemctl enable --now agent-browser agent-tab-reaper.timer

echo "installed $("$PREFIX/bin/icloud-mcp" version)."
echo "Point the agent runtime at: sudo -n -u $ACCOUNT $PREFIX/bin/stdio.sh"
echo "The first call that needs iCloud asks to open the login door; or sign in now with:"
echo "  sudo -u $ACCOUNT -H $PREFIX/bin/icloud-mcp-host login"
