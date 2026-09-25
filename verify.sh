#!/bin/sh
# Linux verification: build Dockerfile.verify from this checkout, start the
# container with the login door published on the host's loopback only,
# copy in your pi config and auth plus .env, and open pi inside it.
#
#   ./verify.sh          (re)build, (re)create the container, open pi
#   ./verify.sh pi       reopen pi in the running container
#   ./verify.sh eval HINT 'EXPR'   run tools/cdp-eval.mjs in the container
#                                  (a console for the web apps; AGENTS.md)
#
# The container keeps its browser profile across `./verify.sh pi`; a
# rebuild starts signed out, and the first iCloud call routes to the door.
# The checklist the round runs through is in AGENTS.md, Happy path.
set -eu
cd "$(dirname "$0")"
NAME=icloud-mcp-verify
PI_DIR="${PI_CODING_AGENT_DIR:-$HOME/.pi/agent}"
ADAPTER=/usr/local/lib/node_modules/pi-mcp-adapter/index.ts

if [ "${1:-}" = "eval" ]; then
  shift
  docker cp tools/cdp-eval.mjs "$NAME:/tmp/cdp-eval.mjs" >/dev/null
  exec docker exec "$NAME" node /tmp/cdp-eval.mjs "$@"
fi
if [ "${1:-}" != "pi" ]; then
  [ -f .env ] || { echo "no .env: cp .env.example .env and fill in the two credentials" >&2; exit 1; }
  [ -f "$PI_DIR/auth.json" ] || { echo "no pi auth at $PI_DIR/auth.json: run pi and /login first" >&2; exit 1; }
  docker build -f Dockerfile.verify -t "$NAME" .
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  # The container clock runs UTC, which the server rightly refuses to guess
  # from; the owner's zone is this machine's.
  ZONE="${AGENT_TZ:-$(readlink /etc/localtime | sed 's|.*zoneinfo/||')}"
  # Docker's default seccomp profile forbids the namespaces Chromium's own
  # sandbox needs. Lifting it for this local rig keeps that sandbox on;
  # --no-sandbox would switch it off for a browser holding a session.
  docker run -d --name "$NAME" --shm-size=1g --security-opt seccomp=unconfined \
    -p 127.0.0.1:6080:6080 -e TZ="$ZONE" "$NAME" >/dev/null
  # Copied, not mounted: pi inside may refresh its token without touching
  # the host file. Files land owned by node, mode 600.
  docker cp -L .env "$NAME:/work/.env"
  docker cp -L "$PI_DIR/auth.json" "$NAME:/home/node/.pi/agent/auth.json"
  [ -f "$PI_DIR/settings.json" ] && docker cp -L "$PI_DIR/settings.json" "$NAME:/home/node/.pi/agent/settings.json"
  docker exec -u root "$NAME" sh -c 'chown node:node /work/.env /home/node/.pi/agent/* && chmod 600 /work/.env /home/node/.pi/agent/*'
fi
exec docker exec -it "$NAME" pi -e "$ADAPTER"
