"""lib_station_id.py — read the station id out of a station_me result, strictly.

Only the TOP-LEVEL "station_id" of the result's JSON counts. A regex over the whole text would
take the first id it meets, and a future result that mentions another station first would then
be recorded silently: a Ken miss on every check (see the README's hazard section). Anything that
does not parse, or has no 16-character top-level id, gives (None, '').
"""
import json, re

ID = re.compile(r'^[A-Za-z0-9]{16}$')


def _texts(x):
    """Candidate JSON texts or objects from a tool result: a string, a list of content blocks,
    or a dict holding "content" blocks or the result itself."""
    if isinstance(x, str):
        yield x
    elif isinstance(x, list):
        for b in x:
            if isinstance(b, dict) and isinstance(b.get('text'), str):
                yield b['text']
            elif isinstance(b, str):
                yield b
    elif isinstance(x, dict):
        if 'station_id' in x:
            yield x
        for key in ('content', 'result'):
            if key in x:
                yield from _texts(x[key])


def station_from_result(result):
    for t in _texts(result):
        try:
            d = t if isinstance(t, dict) else json.loads(t)
        except Exception:
            continue
        if isinstance(d, dict):
            st = d.get('station_id')
            if isinstance(st, str) and ID.match(st):
                nm = d.get('name')
                return st, nm if isinstance(nm, str) else ''
    return None, ''
