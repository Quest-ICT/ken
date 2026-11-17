#!/usr/bin/env bash
# ken-mail.sh — a cc-wake trigger: exit 0 when Ken mail is waiting for a station, 1 otherwise.
#
# Asks Ken 6.1.0's POST /comm/waiting (docs/COMM.md §11a in the public Ken repo, tag v6.1.0):
#   POST {"station":"<16-char station id>"} -> 200 {"mail":true} | 200 {"mail":false}
# No token and no MCP: it works while the claude.ai connector is down. It delivers nothing and
# starts no expiry clock. "mail":true means queued and not yet delivered to that station.
#
# Usage: ken-mail.sh <station-id | file holding it>
#        e.g. ken-mail.sh /path/to/project/.claude/ken-station-id
# Exit:  0 mail waiting · 1 no mail, or no answer (treated as no news) · 2 bad station id
#
# THE HAZARD (Ken docs/COMM.md §11a): an id that names no station gets {"mail":false}, exactly like
# an empty station, but Ken counts it as a MISS. 10 misses in 600 s from one address block
# /comm/waiting for that address for 900 s. Every session here shares one address, so one wrong
# id silences the check for ALL of them. Hence the id comes only from station_me's output
# (written verbatim by the session), its shape is checked here, and a missing or malformed id
# sends nothing.
#
# Responses other than 200 are "no news". 429 waits out Retry-After and 403 (address blocked)
# waits 900 s before returning, so a waker never hammers Ken. Both say so on stderr, which
# cc-wake keeps in its log. KEN_URL (required) is the Ken server, e.g. https://ken.example.com.

set -uo pipefail
arg=${1:-}
[ -n "$arg" ] || { echo "ken-mail: usage: ken-mail.sh <station-id | file>" >&2; exit 2; }
if [ -f "$arg" ]; then id=$(head -n1 -- "$arg" | tr -d '[:space:]'); else id=$arg; fi
if ! [[ $id =~ ^[A-Za-z0-9]{16}$ ]]; then
    echo "ken-mail: '$arg' does not hold a 16-character station id; nothing sent (write it from station_me's station_id)" >&2
    exit 2
fi

[ -n "${KEN_URL:-}" ] || { echo "ken-mail: set KEN_URL to your Ken server" >&2; exit 2; }
url="${KEN_URL%/}/comm/waiting"
hdr=$(mktemp); trap 'rm -f "$hdr"' EXIT
body=$(curl -s --max-time 3 -X POST -H 'Content-Type: application/json' \
            -D "$hdr" -o - -w '\n%{http_code}' -d "{\"station\":\"$id\"}" "$url" 2>/dev/null)
code=${body##*$'\n'}; body=${body%$'\n'*}

case "$code" in
    200)
        [[ $body =~ \"mail\"[[:space:]]*:[[:space:]]*true ]] && exit 0
        exit 1 ;;
    429)
        ra=$(awk -F': *' 'tolower($1)=="retry-after"{gsub("\r","",$2); print $2}' "$hdr")
        [[ $ra =~ ^[0-9]+$ ]] || ra=60; [ "$ra" -le 900 ] || ra=900
        echo "ken-mail: 429 over budget; waiting ${ra}s (Retry-After)" >&2
        sleep "$ra"; exit 1 ;;
    403)
        echo "ken-mail: 403 — the mail check is BLOCKED for this address (too many misses, or not allowed). Silence is NOT an empty inbox; pull with comm_poll. Waiting 900s." >&2
        sleep 900; exit 1 ;;
    *)
        exit 1 ;;   # 404 before Ken 6.1.0, timeouts, 5xx: no news
esac
