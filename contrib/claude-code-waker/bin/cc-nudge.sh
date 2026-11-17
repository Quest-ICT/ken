#!/usr/bin/env bash
# cc-nudge.sh — type a prompt (default "pull comm") into idle Claude Code sessions in tmux,
# so a station reads its Ken mail without a human visiting it.
#
# Typing into another person's session is that person's call: run it only where its owner
# agreed. Part of Ken's contrib/claude-code-waker (README.md there).
#
# For each named tmux session it checks, and types nothing unless every check passes:
#   1. a live Claude Code process runs in it (~/.claude/sessions/<pid>.json, "tmux" field);
#   2. that session is idle (status "idle"), not working;
#   3. its input box is visible (no permission or trust dialog open) and holds no draft:
#      dim text is Claude Code's own suggestion and is replaced; normal text is a draft;
#   4. nobody has typed in a terminal attached to that tmux session for --quiet-keys seconds
#      (default 15): a person at the keyboard is not interrupted (added 2026-11-17);
#   5. after --settle seconds (default 3), checks 2 and 3 still hold: a session that is idle
#      only for a moment between steps is not caught (added 2026-11-17; skipped with --dry-run);
#   6. after typing, the input box holds exactly the text; only then is Enter pressed. If it
#      holds more (someone typed at the same instant), our text is removed again when it is at
#      the end, and theirs is kept;
#   7. after Enter, the session turns busy.
#
# What it cannot see: a prompt being written through Remote Control (phone or claude.ai) does not
# appear in the local input box. A nudge then simply runs first, and that prompt queues behind it.
#
# Usage: cc-nudge.sh [--text 'TEXT'] [--dry-run] <tmux-session> [<tmux-session> ...]
#        cc-nudge.sh [--text 'TEXT'] [--dry-run] --all   every Claude session in tmux except your own
#        cc-nudge.sh --list      the tmux sessions that run Claude, with their status
# Exit status: 0 if every session was nudged (or would be, with --dry-run), 1 otherwise.

set -uo pipefail
PROG=$(basename "$0")
TEXT='pull comm'
SETTLE=3
QUIET=15
DRY=0
LIST=0
ALL=0
names=()

while [ $# -gt 0 ]; do
    case "$1" in
        --text)    TEXT=${2:?--text needs a value}; shift ;;
        --text=*)  TEXT=${1#*=} ;;
        --settle)  SETTLE=${2:?--settle needs seconds}; shift ;;
        --quiet-keys) QUIET=${2:?--quiet-keys needs seconds}; shift ;;
        --dry-run) DRY=1 ;;
        --list)    LIST=1 ;;
        --all)     ALL=1 ;;
        -h|--help) sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        -*)        echo "$PROG: unknown option $1" >&2; exit 2 ;;
        *)         names+=("$1") ;;
    esac
    shift
done
[ "$LIST" = 1 ] || [ "$ALL" = 1 ] || [ "${#names[@]}" -gt 0 ] || { echo "usage: $PROG [--text 'TEXT'] [--dry-run] <tmux-session>..." >&2; exit 2; }
case "$TEXT" in *$'\n'*) echo "$PROG: --text must be one line" >&2; exit 2 ;; esac

# Print "<status> <pane-id>" for the live Claude session in tmux session $1, or nothing.
# With --list (no argument): "<tmux-session> <status>" for every one.
session_info() {
    python3 - "$@" <<'PY'
import glob, json, os, sys
want = sys.argv[1] if len(sys.argv) > 1 else None
for f in glob.glob(os.path.expanduser('~/.claude/sessions/*.json')):
    try:
        d = json.load(open(f))
    except Exception:
        continue
    pid, tm = d.get('pid'), d.get('tmux') or ''
    if not pid or not tm or not os.path.exists(f'/proc/{pid}'):
        continue
    sess, _, rest = tm.partition(':')            # "myproj:@25.%25"
    pane = rest.rpartition('.')[2]                # "%25"
    if want is None:
        print(sess, d.get('status', '?'))
    elif sess == want:
        print(d.get('status', '?'), pane)
        break
PY
}

status_of_pane_session() { session_info "$1" | awk '{print $1}'; }

# The input line: the last line containing the prompt marker, with escapes kept (-e).
input_line() { tmux capture-pane -p -e -t "$1" 2>/dev/null | grep -F '❯' | tail -1; }

# Classify the input box: "empty", "suggestion" (dim only), "draft:<text>" or "none" (no box).
input_state() {
    local raw
    raw=$(input_line "$1")
    [ -n "$raw" ] || { echo none; return; }
    python3 - "$raw" <<'PY'
import re, sys
raw = sys.argv[1]
after = raw.split('❯', 1)[1]
visible = re.sub(r'\x1b\[[0-9;]*[A-Za-z]', '', after).replace(' ', ' ').strip()
if not visible:
    print('empty')
else:
    # dim (SGR 2) switched on before the first visible character means a suggestion
    m = re.match(r'((?:\x1b\[[0-9;]*[A-Za-z]|[\s ])*)', after)
    lead = m.group(1) if m else ''
    dim = any('2' in seq.split(';') for seq in re.findall(r'\x1b\[([0-9;]*)m', lead))
    print('suggestion' if dim else 'draft:' + visible)
PY
}

input_text() { input_line "$1" | python3 -c 'import re,sys; s=sys.stdin.read().split("❯",1)[-1]; print(re.sub(r"\x1b\[[0-9;]*[A-Za-z]","",s).replace(" "," ").strip())'; }

if [ "$LIST" = 1 ]; then
    session_info | sort
    exit 0
fi

if [ "$ALL" = 1 ]; then
    self=""
    [ -n "${TMUX:-}" ] && self=$(tmux display-message -p '#S' 2>/dev/null)
    while read -r sess _; do
        [ "$sess" = "$self" ] || names+=("$sess")
    done < <(session_info | sort)
    [ "${#names[@]}" -gt 0 ] || { echo "$PROG: no other Claude sessions in tmux"; exit 1; }
fi

rc=0
for name in "${names[@]}"; do
    info=$(session_info "$name")
    if [ -z "$info" ]; then
        echo "$name: NOT FOUND (no live Claude session in that tmux session) -> nothing typed"; rc=1; continue
    fi
    status=${info%% *}; pane=${info##* }
    if [ "$status" != idle ]; then
        echo "$name: ${status^^} (not idle) -> nothing typed"; rc=1; continue
    fi
    state=$(input_state "$pane")
    case "$state" in
        none)    echo "$name: NO INPUT BOX (a dialog or prompt is open; attach and look) -> nothing typed"; rc=1; continue ;;
        draft:*) echo "$name: input box has a DRAFT (\"${state#draft:}\") -> nothing typed"; rc=1; continue ;;
    esac
    # Someone at a terminal attached to this session typed recently: do not interrupt them.
    last_key=$(tmux list-clients -t "$name" -F '#{client_activity}' 2>/dev/null | sort -n | tail -1)
    if [ -n "$last_key" ] && [ $(( $(date +%s) - last_key )) -lt "$QUIET" ]; then
        echo "$name: someone typed in an attached terminal $(( $(date +%s) - last_key ))s ago -> nothing typed"; rc=1; continue
    fi
    # Settle: still idle and still empty a few seconds later (not a pause between two steps).
    if [ "$DRY" != 1 ] && [ "$SETTLE" -gt 0 ]; then
        sleep "$SETTLE"
        now=$(status_of_pane_session "$name"); state2=$(input_state "$pane")
        if [ "$now" != idle ]; then
            echo "$name: ${now^^} after the ${SETTLE}s settle (was idle only briefly) -> nothing typed"; rc=1; continue
        fi
        case "$state2" in
            empty|suggestion) ;;
            *) echo "$name: input box changed during the ${SETTLE}s settle ($state2) -> nothing typed"; rc=1; continue ;;
        esac
        last_key=$(tmux list-clients -t "$name" -F '#{client_activity}' 2>/dev/null | sort -n | tail -1)
        if [ -n "$last_key" ] && [ $(( $(date +%s) - last_key )) -lt "$QUIET" ]; then
            echo "$name: someone typed in an attached terminal during the settle -> nothing typed"; rc=1; continue
        fi
    fi
    if [ "$DRY" = 1 ]; then
        echo "$name: idle, input box $state -> would type \"$TEXT\" (dry run)"; continue
    fi

    tmux send-keys -t "$pane" -l "$TEXT"
    typed=""
    for _ in 1 2 3 4 5 6 7 8 9 10; do              # the TUI redraws asynchronously
        typed=$(input_text "$pane")
        [ "$typed" = "$TEXT" ] && break
        sleep 0.2
    done
    if [ "$typed" != "$TEXT" ]; then
        # Someone typed at the same instant. Take ours back out if it sits at the end; keep theirs.
        if [ "${typed%"$TEXT"}" != "$typed" ]; then
            tmux send-keys -t "$pane" $(printf 'BSpace %.0s' $(seq 1 ${#TEXT}))
            echo "$name: the input box also held someone else's text -> removed \"$TEXT\" again, kept theirs, Enter NOT pressed"
        else
            echo "$name: typed, but the input box shows \"$typed\" -> Enter NOT pressed; attach and check"
        fi
        rc=1; continue
    fi
    tmux send-keys -t "$pane" Enter

    now=""
    for _ in $(seq 1 25); do                       # up to about 5 s
        now=$(status_of_pane_session "$name")
        [ "$now" = busy ] && break
        sleep 0.2
    done
    if [ "$now" = busy ]; then
        echo "$name: idle, input box $state -> typed \"$TEXT\" -> busy at $(date '+%H:%M:%S %Z'). OK"
    else
        echo "$name: typed \"$TEXT\" and pressed Enter, but status is still \"$now\"; attach and check"; rc=1
    fi
done
exit "$rc"
