#!/bin/bash
# Login Flow V2 helper for the dev environment.
# Usage: loginflow.sh <command> [args...]
#
# The dev environment has no web UI, so this script plays its part: it calls the
# authenticated OCS endpoints (info / grant / deny) as a demo user. It can also
# play the sync client (start / poll) and manage connected clients.
#
# Environment variables:
#   LOGINFLOW_URL   — server base URL (default: http://localhost)
#   LOGINFLOW_USER  — user granting access (default: einstein)
#   LOGINFLOW_PASS  — that user's password (default: relativity)
#   LOGINFLOW_UA    — User-Agent of the simulated sync client for start/poll
#
# Commands:
#   start                         Start a flow as a sync client; prints the init JSON
#   info   <login-url|token>      Show what the grant page would show
#   grant  <login-url|token> [n]  Grant access (optional device name n)
#   deny   <login-url|token>      Deny access
#   poll   <poll-token>           Poll once as the sync client (404 while pending)
#   clients                       List the user's connected clients
#   revoke <client-id>            Revoke a connected client
#   help                          Show this help

set -euo pipefail

# ---------------------------------------------------------------------------
# Configuration — override via env vars
# ---------------------------------------------------------------------------
BASE_URL="${LOGINFLOW_URL:-http://localhost}"
USER="${LOGINFLOW_USER:-einstein}"
PASS="${LOGINFLOW_PASS:-relativity}"
CLIENT_UA="${LOGINFLOW_UA:-Mozilla/5.0 (Linux) mirall/0.1.0 (cernbox-sync)}"

BASE_URL="${BASE_URL%/}"
OCS_URL="${BASE_URL}/ocs/v2.php/cloud/user"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
usage() {
    sed -n '/^# Commands:/,/^$/p' "$0" | grep -v '^#$' | sed 's/^# //'
    exit 1
}

die() { echo "ERROR: $*" >&2; exit 1; }

pretty() {
    if command -v jq >/dev/null 2>&1; then jq .; else cat; echo; fi
}

# Accepts either the full login URL or the bare login token.
login_token() {
    local t="${1%/}"
    echo "${t##*/}"
}

# Authenticated as the demo user, with a non-sync-client User-Agent so the
# credentials are checked as a regular password (not as an app password).
CURL_USER=(curl --silent --show-error --fail-with-body --user "${USER}:${PASS}" -A "loginflow.sh")
# Anonymous, as the sync client.
CURL_CLIENT=(curl --silent --show-error --fail-with-body -A "${CLIENT_UA}")

# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------
cmd_start() {
    "${CURL_CLIENT[@]}" -X POST "${BASE_URL}/index.php/login/v2" | pretty
}

cmd_info() {
    [[ $# -ge 1 ]] || die "Usage: info <login-url|token>"
    "${CURL_USER[@]}" "${OCS_URL}/login-flow/$(login_token "$1")" | pretty
}

cmd_grant() {
    [[ $# -ge 1 ]] || die "Usage: grant <login-url|token> [device-name]"
    local body='{}'
    if [[ $# -ge 2 ]]; then
        body="$(printf '{"name":"%s"}' "$2")"
    fi
    "${CURL_USER[@]}" -X POST -H "Content-Type: application/json" -d "$body" \
        "${OCS_URL}/login-flow/$(login_token "$1")/grant" | pretty
}

cmd_deny() {
    [[ $# -ge 1 ]] || die "Usage: deny <login-url|token>"
    "${CURL_USER[@]}" -X POST "${OCS_URL}/login-flow/$(login_token "$1")/deny" | pretty
}

cmd_poll() {
    [[ $# -ge 1 ]] || die "Usage: poll <poll-token>"
    "${CURL_CLIENT[@]}" -X POST --data-urlencode "token=$1" "${BASE_URL}/index.php/login/v2/poll" | pretty
}

cmd_clients() {
    "${CURL_USER[@]}" -H "OCS-APIRequest: true" "${OCS_URL}/clients?format=json" | pretty
}

cmd_revoke() {
    [[ $# -ge 1 ]] || die "Usage: revoke <client-id>"
    "${CURL_USER[@]}" -X DELETE -H "OCS-APIRequest: true" "${OCS_URL}/clients/$1?format=json" | pretty
}

# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
[[ $# -ge 1 ]] || usage
COMMAND="$1"; shift

case "$COMMAND" in
    start)   cmd_start   "$@" ;;
    info)    cmd_info    "$@" ;;
    grant)   cmd_grant   "$@" ;;
    deny)    cmd_deny    "$@" ;;
    poll)    cmd_poll    "$@" ;;
    clients) cmd_clients "$@" ;;
    revoke)  cmd_revoke  "$@" ;;
    help|-h|--help) usage ;;
    *) die "Unknown command: $COMMAND (run '$0 help')" ;;
esac
