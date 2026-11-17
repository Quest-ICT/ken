#!/usr/bin/env python3
"""cc-ken-bootstrap.py — one-time registration of the Ken station of sessions already running.

The hook ken-station-record.py registers a session when it calls station_me, but sessions that
started before the hook was installed do not run it. For those, this reads the session's own
transcript (~/.claude/projects/*/<session_id>.jsonl) and takes station_id from the LAST
station_me result in it: the same verbatim value the hook would have recorded. Nothing else in
the transcript is read out or stored.

Transcripts are private to their sessions: run it only with their owner's agreement, for named
tmux sessions or --all.

Usage: cc-ken-bootstrap.py [--dry-run] [--force] (--all | TMUX-SESSION ...)
  --force   overwrite a record the hook already wrote (normally kept: the hook is fresher)
"""
import argparse, datetime, glob, json, os, re, sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
from lib_station_id import station_from_result  # noqa: E402
STATE = os.path.join(os.environ.get('XDG_STATE_HOME', os.path.expanduser('~/.local/state')), 'cc-wake')
REG = os.path.join(STATE, 'stations')


def live():
    out = {}
    for f in glob.glob(os.path.expanduser('~/.claude/sessions/*.json')):
        try:
            d = json.load(open(f))
        except Exception:
            continue
        pid, tm, sid = d.get('pid'), d.get('tmux') or '', d.get('sessionId') or ''
        if pid and tm and sid and os.path.exists(f'/proc/{pid}'):
            out[tm.partition(':')[0]] = sid
    return out


def last_station(transcript):
    ids, found = set(), None
    with open(transcript, errors='replace') as f:
        for line in f:
            if 'station_me' not in line and 'tool_use_id' not in line:
                continue
            try:
                ev = json.loads(line)
            except Exception:
                continue
            content = (ev.get('message') or {}).get('content')
            if not isinstance(content, list):
                continue
            for c in content:
                if not isinstance(c, dict):
                    continue
                if c.get('type') == 'tool_use' and str(c.get('name', '')).endswith('station_me'):
                    ids.add(c.get('id'))
                elif c.get('type') == 'tool_result' and c.get('tool_use_id') in ids:
                    st, nm = station_from_result(c.get('content'))
                    if st:
                        found = (st, nm)
    return found


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--all', action='store_true')
    ap.add_argument('--dry-run', action='store_true')
    ap.add_argument('--force', action='store_true')
    ap.add_argument('names', nargs='*')
    a = ap.parse_args()
    sessions = live()
    names = sorted(sessions) if a.all else a.names
    if not names:
        ap.error('name tmux sessions, or --all')
    os.makedirs(REG, exist_ok=True)
    for name in names:
        sid = sessions.get(name)
        if not sid:
            print(f'{name}: no live Claude session in tmux'); continue
        dest = os.path.join(REG, f'{sid}.json')
        if os.path.exists(dest) and not a.force:
            print(f'{name}: already registered ({dest}); kept'); continue
        tr = glob.glob(os.path.expanduser(f'~/.claude/projects/*/{sid}.jsonl'))
        if not tr:
            print(f'{name}: transcript {sid}.jsonl not found; not registered'); continue
        st = last_station(tr[0])
        if not st:
            print(f'{name}: no station_me result in its transcript; not registered'); continue
        rec = {'station_id': st[0], 'station_name': st[1], 'session_id': sid, 'cwd': '',
               'recorded_at': datetime.datetime.now().astimezone().isoformat(timespec='seconds'),
               'source': 'bootstrap-transcript'}
        if a.dry_run:
            print(f'{name}: would register {st[0]} {st[1]}'); continue
        with open(dest + '.tmp', 'w') as f:
            json.dump(rec, f)
        os.replace(dest + '.tmp', dest)
        print(f'{name}: registered {st[0]} {st[1]}')


if __name__ == '__main__':
    main()
