#!/usr/bin/env python3
"""install-ken-station-hook.py — add the PostToolUse hook that records each session's Ken station.

Adds to ~/.claude/settings.json, once, with a dated backup:
  PostToolUse, matcher "mcp__.*__station_me" (the claude.ai connector's name and a direct MCP
  connection's both) ->
  <this package>/hooks/ken-station-record.py
A human runs it: a Claude Code session should not change its own hooks. Idempotent; --dry-run shows the result.
"""
import datetime, json, os, shutil, sys
P = os.path.expanduser('~/.claude/settings.json')
CMD = os.path.normpath(os.path.join(os.path.dirname(os.path.realpath(__file__)), '..', 'hooks', 'ken-station-record.py'))
MATCH = 'mcp__.*__station_me'
d = json.load(open(P))
post = d.setdefault('hooks', {}).setdefault('PostToolUse', [])
if any(h.get('command') == CMD for m in post for h in m.get('hooks', [])):
    print('already installed'); sys.exit(0)
post.append({'matcher': MATCH, 'hooks': [{'type': 'command', 'command': CMD}]})
if '--dry-run' in sys.argv:
    print(json.dumps(d['hooks'], indent=2)); sys.exit(0)
bak = P + '.bak-pre-ken-station-hook-' + datetime.datetime.now().strftime('%Y%m%d-%H%M%S')
shutil.copy2(P, bak)
tmp = P + '.tmp'
with open(tmp, 'w') as f:
    json.dump(d, f, indent=2); f.write('\n')
os.replace(tmp, P)
json.load(open(P))
print(f'installed; backup {bak}')
