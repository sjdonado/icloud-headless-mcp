#!/bin/sh
# Run icloud-mcp from this checkout, without the service account: the
# single-user install for a laptop or any box you are signed in to.
#
#   ./local.sh resident       keep the headless browser running (the session)
#   ./local.sh login          prints a login-door link: sign in to iCloud there
#   ./local.sh session-check  0 healthy, 1 signed out, 2 no browser, 3 approval
#   ./local.sh                the MCP server over stdio: point your agent here
#
# An agent can do the login itself: when a call reports needs_login it
# calls open_login, which returns the same door link and password.
#
# Credentials come from .env next to this script (copy .env.example), and
# every piece of state (browser profile, cookie jar, locks) lands in .state/.
# Both are gitignored.
set -eu
cd "$(dirname "$0")"
# The server refuses without the account in .env; the browser side does not need it.
if [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi
export ICLOUD_STATE="$PWD/.state"
mkdir -p .state
chmod 700 .state
go build -o .state/icloud-mcp ./cmd/icloud-mcp >&2
exec .state/icloud-mcp "$@"
