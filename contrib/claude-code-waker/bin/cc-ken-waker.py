#!/usr/bin/env python3
"""cc-ken-waker.py — wake every live Claude Code session on this machine that has Ken mail waiting.

One process serves all sessions; nothing is configured per session.

LIST OF SESSIONS. Every --scan seconds it reads ~/.claude/sessions/<pid>.json and keeps those whose
process is alive and runs in tmux. New sessions appear by themselves; ended ones drop out.

STATIONS. A session's station id comes from ~/.local/state/cc-wake/stations/<session_id>.json,
written by the PostToolUse hook ken-station-record.py from the session's own station_me result
(or once, by cc-ken-bootstrap.py, from its transcript). A session with no record yet is skipped:
before its first prompt it has no station, and its first station_me shows its mail anyway.
Records whose id is not 16 letters or digits are ignored: a wrong id is a Ken "miss", and
10 misses in 600 s block the check for the whole machine.

CHECK. Each station is asked every --interval seconds (default 600), staggered over the cycle by a
hash of its id, with POST {"station":id} to Ken's /comm/waiting (Ken 6.1.0, no token, no MCP).
200 {"mail":true} queues a wake. 429 pauses all checks for Retry-After; 403 pauses them 900 s
and says so in the log; anything else is "no news".

WAKE. Types "pull comm" through cc-nudge.sh, which refuses busy sessions, open dialogs, drafts,
recent keystrokes in an attached terminal, and sessions idle only for a moment. A refused wake is
retried every --retry seconds, and the mail is re-checked before each retry: if the session has
pulled it meanwhile, the wake is dropped.

Usage:
  cc-ken-waker.py [--interval 600] [--retry 60] [--scan 15] [--dry-run] [--exclude TMUX-SESSION ...]
  cc-ken-waker.py --list           sessions, their stations and their next check, then exit
  cc-ken-waker.py --once           check every registered station now, wake if needed, exit
Log: ~/.local/state/cc-wake/ken-waker.log. Systemd: scripts/systemd/cc-ken-waker.service.
KEN_URL (required) is the Ken server, e.g. https://ken.example.com. Manual: README.md.
"""
import argparse, glob, hashlib, json, os, re, subprocess, sys, time, urllib.error, urllib.request
from datetime import datetime

HERE = os.path.dirname(os.path.realpath(__file__))
NUDGE = os.path.join(HERE, 'cc-nudge.sh')
STATE = os.path.join(os.environ.get('XDG_STATE_HOME', os.path.expanduser('~/.local/state')), 'cc-wake')
REG = os.path.join(STATE, 'stations')
LOG = os.path.join(STATE, 'ken-waker.log')
URL = os.environ.get('KEN_URL', '').rstrip('/') + '/comm/waiting'   # KEN_URL is required
ID = re.compile(r'^[A-Za-z0-9]{16}$')


def now():
    return datetime.now().astimezone().strftime('%Y-%m-%d %H:%M:%S %Z')


def log(msg):
    os.makedirs(STATE, exist_ok=True)
    with open(LOG, 'a') as f:
        f.write(f'{now()} {msg}\n')


def live_sessions(exclude):
    """{session_id: {'tmux': name, 'cwd': ..., 'pid': ...}} for live Claude sessions in tmux."""
    out = {}
    for f in glob.glob(os.path.expanduser('~/.claude/sessions/*.json')):
        try:
            d = json.load(open(f))
        except Exception:
            continue
        pid, tm, sid = d.get('pid'), d.get('tmux') or '', d.get('sessionId') or ''
        if not pid or not tm or not sid or not os.path.exists(f'/proc/{pid}'):
            continue
        name = tm.partition(':')[0]
        if name in exclude:
            continue
        out[sid] = {'tmux': name, 'cwd': d.get('cwd', ''), 'pid': pid}
    return out


def station_of(sid):
    try:
        rec = json.load(open(os.path.join(REG, f'{sid}.json')))
    except Exception:
        return None, ''
    st = str(rec.get('station_id', ''))
    return (st if ID.match(st) else None), rec.get('station_name', '')


def ask(station):
    """('mail'|'none'|'pause', seconds) for one station."""
    req = urllib.request.Request(URL, data=json.dumps({'station': station}).encode(), method='POST',
                                 headers={'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=3) as r:
            body = r.read(512).decode('utf-8', 'replace')
            return ('mail' if re.search(r'"mail"\s*:\s*true', body) else 'none'), 0
    except urllib.error.HTTPError as e:
        if e.code == 429:
            ra = e.headers.get('Retry-After', '60')
            ra = int(ra) if ra.isdigit() else 60
            log(f'429 from Ken: pausing all checks {min(ra, 900)}s')
            return 'pause', min(ra, 900)
        if e.code == 403:
            log('403 from Ken: the mail check is BLOCKED for this address. Silence is NOT an empty '
                'inbox; sessions must pull with comm_poll. Pausing all checks 900s.')
            return 'pause', 900
        return 'none', 0
    except Exception:
        return 'none', 0


def nudge(tmux_name, dry):
    cmd = [NUDGE, '--text', 'pull comm'] + (['--dry-run'] if dry else []) + [tmux_name]
    p = subprocess.run(cmd, capture_output=True, text=True)
    return p.returncode == 0, (p.stdout + p.stderr).strip()


def offset(station, interval):
    return int(hashlib.sha256(station.encode()).hexdigest(), 16) % max(interval, 1)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n')[1])
    ap.add_argument('--interval', type=int, default=600)
    ap.add_argument('--retry', type=int, default=60)
    ap.add_argument('--scan', type=int, default=15)
    ap.add_argument('--dry-run', action='store_true')
    ap.add_argument('--exclude', nargs='*', default=[])
    ap.add_argument('--list', action='store_true')
    ap.add_argument('--once', action='store_true')
    a = ap.parse_args()
    if not os.environ.get('KEN_URL') and not a.list:
        ap.error('set KEN_URL to your Ken server, e.g. KEN_URL=https://ken.example.com')
    exclude = set(a.exclude)

    if a.list:
        for sid, s in sorted(live_sessions(exclude).items(), key=lambda kv: kv[1]['tmux']):
            st, nm = station_of(sid)
            print(f"{s['tmux']:24s} {sid}  {st or '(no station recorded)':18s} {nm}")
        return 0

    if a.once:
        rc = 0
        for sid, s in sorted(live_sessions(exclude).items(), key=lambda kv: kv[1]['tmux']):
            st, _ = station_of(sid)
            if not st:
                print(f"{s['tmux']}: no station recorded, skipped"); continue
            res, wait = ask(st)
            if res == 'pause':
                print(f'Ken asked to pause {wait}s; stopping'); return 1
            if res == 'mail':
                ok, out = nudge(s['tmux'], a.dry_run)
                print(f"{s['tmux']}: mail waiting -> {out}"); rc |= (not ok)
            else:
                print(f"{s['tmux']}: no mail")
        return rc

    log(f'start: interval={a.interval}s retry={a.retry}s scan={a.scan}s dry_run={a.dry_run} '
        f'exclude={sorted(exclude)}')
    next_check, pending, known = {}, {}, {}   # by station / station / sid
    paused_until = 0
    t0 = time.time()
    while True:
        t = time.time()
        sessions = live_sessions(exclude)
        current = {}
        for sid, s in sessions.items():
            st, nm = station_of(sid)
            if not st:
                if known.get(sid) != 'none':
                    log(f"session {s['tmux']} ({sid[:8]}): no station recorded yet, not checked")
                    known[sid] = 'none'
                continue
            if known.get(sid) != st:
                log(f"session {s['tmux']} ({sid[:8]}): station {st} {nm}, checked every {a.interval}s")
                known[sid] = st
            current[st] = s
            if st not in next_check:
                next_check[st] = t0 + offset(st, a.interval)
                while next_check[st] < t:
                    next_check[st] += a.interval
        for sid in [k for k in known if k not in sessions]:
            log(f'session {sid[:8]} ended; dropped')
            known.pop(sid)
        for st in [k for k in next_check if k not in current]:
            next_check.pop(st, None); pending.pop(st, None)

        if t >= paused_until:
            for st, s in current.items():
                due_check = t >= next_check[st]
                due_retry = st in pending and t >= pending[st]
                if not (due_check or due_retry):
                    continue
                res, wait = ask(st)
                if res == 'pause':
                    paused_until = time.time() + wait
                    break
                if due_check:
                    next_check[st] += a.interval
                if res == 'none':
                    if st in pending:
                        log(f"{s['tmux']}: mail no longer waiting (pulled meanwhile); wake dropped")
                        pending.pop(st)
                    continue
                ok, out = nudge(s['tmux'], a.dry_run)
                log(f"{s['tmux']}: mail waiting -> {out}")
                if ok:
                    pending.pop(st, None)
                else:
                    pending[st] = time.time() + a.retry
        time.sleep(a.scan)


if __name__ == '__main__':
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        log('stop (signal)')
