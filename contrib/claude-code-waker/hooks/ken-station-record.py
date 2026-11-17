#!/usr/bin/env python3
"""ken-station-record.py — PostToolUse hook for mcp__claude_ai_Ken__station_me.

Records which Ken station this Claude Code session staffs, so cc-ken-waker.py can ask Ken whether
mail is waiting for it. The station id is copied verbatim from station_me's own result, never
typed: a wrong id is a "miss" to Ken, and 10 misses in 600 s block the mail check for every
session on this machine (Ken docs/COMM.md §11a).

Writes ~/.local/state/cc-wake/stations/<session_id>.json:
  {"station_id": "...", "station_name": "...", "session_id": "...", "cwd": "...",
   "recorded_at": "...", "source": "hook"}

Silent and never blocking: any problem means nothing is written and exit 0.
Manual: README.md of this package.
"""
import datetime, json, os, re, sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.realpath(__file__)), '..', 'bin'))
from lib_station_id import station_from_result  # noqa: E402

SID = re.compile(r'^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$')


def main():
    try:
        ev = json.load(sys.stdin)
    except Exception:
        return
    if not str(ev.get('tool_name', '')).endswith('station_me'):
        return
    sid = str(ev.get('session_id', ''))
    if not SID.match(sid):
        return
    # Only the result's top-level station_id counts (lib_station_id.py).
    st, name = station_from_result(ev.get('tool_response'))
    if not st:
        return
    rec = {
        'station_id': st,
        'station_name': name,
        'session_id': sid,
        'cwd': ev.get('cwd', ''),
        'recorded_at': datetime.datetime.now().astimezone().isoformat(timespec='seconds'),
        'source': 'hook',
    }
    d = os.path.join(os.environ.get('XDG_STATE_HOME', os.path.expanduser('~/.local/state')),
                     'cc-wake', 'stations')
    os.makedirs(d, exist_ok=True)
    tmp = os.path.join(d, f'.{sid}.tmp')
    with open(tmp, 'w') as f:
        json.dump(rec, f)
    os.replace(tmp, os.path.join(d, f'{sid}.json'))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        pass
    sys.exit(0)
