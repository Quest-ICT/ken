#!/usr/bin/env bash
# cc-wake.sh — wake a Claude Code session in tmux when a trigger fires.
#
# A trigger is any program. cc-wake runs it and, when it fires, types a fixed prompt into the
# target session through cc-nudge.sh, which types nothing unless the session is idle, shows its
# input box and holds no draft. A session that is busy is retried until it is idle; events that
# arrive meanwhile are coalesced into that one wake.
#
# Two kinds of trigger:
#   repeat (default)  the trigger is run again and again; exit 0 = fired, anything else = not yet.
#                     A blocking trigger (a long-poll) fits as it is; a quick check wants
#                     --interval to space out the runs.
#   --stream          the trigger runs once and keeps running; every line on its stdout is an
#                     event (a log tail, a watcher). If it exits, it is restarted after --interval.
#
# The trigger's output is NEVER typed into the session: it is untrusted data, and a session takes
# what it is typed as an instruction. The prompt is fixed by --text; the session itself goes and
# looks (pulls its mail, reads the file).
#
# Usage:
#   cc-wake.sh --session NAME [options] -- TRIGGER [ARGS...]
# Options:
#   --session NAME     tmux session to wake (cc-nudge.sh --list shows them)      [required]
#   --text 'TEXT'      prompt to type, one line                         [default: pull comm]
#   --stream           stream mode (see above)
#   --interval S       seconds between trigger runs (repeat) or restarts (stream)   [60]
#   --timeout S        kill a repeat-mode trigger run after S seconds; 0 = never     [0]
#   --retry S          seconds between attempts while the session is busy           [30]
#   --min-gap S        at least S seconds between two wakes                         [60]
#   --once             exit after the first successful wake
#   --dry-run          log what would be typed; type nothing
#   --log FILE         log file   [${XDG_STATE_HOME:-~/.local/state}/cc-wake/<session>.log]
#   --config FILE      read settings from FILE, a bash file setting any of SESSION TEXT STREAM
#                      INTERVAL TIMEOUT RETRY MINGAP ONCE DRY LOG and TRIGGER=(cmd args...);
#                      options given after --config override it
#
# Examples:
#   cc-wake.sh --session myproj --text 'the build finished; read build.log' \
#       --once -- test -e /path/to/project/.claude/work/build.done
#   cc-wake.sh --session myproj --stream -- tail -n0 -F /var/tmp/alerts.log
#   cc-wake.sh --session myproj -- ../triggers/ken-mail.sh <station-id>  (Ken mail, one session)
#
# Run it permanently with the systemd user template scripts/systemd/cc-wake@.service
# (README.md). A waker types only its fixed --text and never bypasses cc-nudge.sh's refusals;
# put one on another person's session only with their agreement.

set -uo pipefail
PROG=$(basename "$0")
HERE=$(dirname "$(readlink -f "$0")")
NUDGE="$HERE/cc-nudge.sh"

SESSION="" TEXT='pull comm' STREAM=0 INTERVAL=60 TIMEOUT=0 RETRY=30 MINGAP=60 ONCE=0 DRY=0 LOG=""
while [ $# -gt 0 ]; do
    case "$1" in
        --session)  SESSION=${2:?}; shift ;;
        --text)     TEXT=${2:?}; shift ;;
        --stream)   STREAM=1 ;;
        --interval) INTERVAL=${2:?}; shift ;;
        --timeout)  TIMEOUT=${2:?}; shift ;;
        --retry)    RETRY=${2:?}; shift ;;
        --min-gap)  MINGAP=${2:?}; shift ;;
        --once)     ONCE=1 ;;
        --dry-run)  DRY=1 ;;
        --log)      LOG=${2:?}; shift ;;
        --config)   [ -r "${2:?}" ] || { echo "$PROG: cannot read $2" >&2; exit 2; }
                    # shellcheck source=/dev/null
                    . "$2"; shift ;;
        -h|--help)  sed -n '2,48p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        --)         shift; break ;;
        *)          echo "$PROG: unknown option $1 (the trigger goes after --)" >&2; exit 2 ;;
    esac
    shift
done
[ -n "$SESSION" ] || { echo "$PROG: --session is required" >&2; exit 2; }
if [ $# -eq 0 ] && [ -n "${TRIGGER+x}" ] && [ "${#TRIGGER[@]}" -gt 0 ]; then set -- "${TRIGGER[@]}"; fi
[ $# -gt 0 ] || { echo "$PROG: no trigger given (put it after --, or TRIGGER=(...) in --config)" >&2; exit 2; }
case "$TEXT" in *$'\n'*|'') echo "$PROG: --text must be one non-empty line" >&2; exit 2 ;; esac
for v in "$INTERVAL" "$TIMEOUT" "$RETRY" "$MINGAP"; do
    case "$v" in ''|*[!0-9]*) echo "$PROG: interval, timeout, retry and min-gap are whole seconds" >&2; exit 2 ;; esac
done
[ -x "$NUDGE" ] || { echo "$PROG: $NUDGE not found" >&2; exit 2; }
TRIGGER=("$@")

STATE=${XDG_STATE_HOME:-$HOME/.local/state}/cc-wake
mkdir -p "$STATE"
[ -n "$LOG" ] || LOG="$STATE/$SESSION.log"

# One waker per session and text: a second copy would wake the session twice.
LOCK="$STATE/$(printf '%s|%s' "$SESSION" "$TEXT" | sha256sum | cut -c1-16).lock"
exec 9>"$LOCK"
flock -n 9 || { echo "$PROG: another cc-wake already serves $SESSION with this text ($LOCK)" >&2; exit 3; }

log() { printf '%s %s\n' "$(date '+%F %H:%M:%S %Z')" "$*" >> "$LOG"; }
log "start: session=$SESSION text=\"$TEXT\" mode=$([ $STREAM = 1 ] && echo stream || echo repeat) trigger=${TRIGGER[*]}"
trap 'log "stop (signal)"; exit 0' INT TERM

last_wake=0
# Deliver one wake; blocks until typed (or, with --dry-run, until it would be). Returns 0 on success.
wake() {
    local gap=$(( $(date +%s) - last_wake ))
    if [ "$last_wake" -gt 0 ] && [ "$gap" -lt "$MINGAP" ]; then
        log "fired $gap s after the last wake; waiting out --min-gap"
        sleep $(( MINGAP - gap ))
    fi
    local args=(--text "$TEXT") out
    [ "$DRY" = 1 ] && args+=(--dry-run)
    while :; do
        out=$("$NUDGE" "${args[@]}" "$SESSION" 2>&1); local rc=$?
        log "nudge: $out"
        if [ $rc -eq 0 ]; then last_wake=$(date +%s); return 0; fi
        sleep "$RETRY"
    done
}

if [ "$STREAM" = 1 ]; then
    while :; do
        while IFS= read -r line; do
            log "event: ${line:0:200}"
            # drain whatever else is already queued: one wake covers it
            while IFS= read -r -t 1 more; do log "event (coalesced): ${more:0:200}"; done
            wake && [ "$ONCE" = 1 ] && { log "done (--once)"; exit 0; }
        done < <("${TRIGGER[@]}" 2>>"$LOG")
        log "trigger exited; restarting in $INTERVAL s"
        sleep "$INTERVAL"
    done
else
    while :; do
        if [ "$TIMEOUT" -gt 0 ]; then
            timeout "$TIMEOUT" "${TRIGGER[@]}" >>"$LOG" 2>&1; rc=$?
        else
            "${TRIGGER[@]}" >>"$LOG" 2>&1; rc=$?
        fi
        if [ $rc -eq 0 ]; then
            log "fired"
            wake && [ "$ONCE" = 1 ] && { log "done (--once)"; exit 0; }
        fi
        sleep "$INTERVAL"
    done
fi
