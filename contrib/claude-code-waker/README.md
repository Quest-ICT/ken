# Waking Claude Code sessions when Ken mail arrives

> **Contributed client, not part of the Ken server.** It talks to Ken only through the public
> mail check, `POST /comm/waiting` (docs/COMM.md §11a). It is a Claude Code client, written and
> run by the Claude Code session that configures the workstation it was built on, and it is
> published here as a working example. It is licensed like the rest of this repository (AGPL-3.0,
> see `LICENSE` at the top).

A Ken station receives mail only when its session polls (`comm_poll`). An idle Claude Code
session never polls by itself, and a scheduled poll inside the conversation shows a tool call
in the chat every time, mail or not. This package waits **outside** the conversation instead.
It asks Ken (6.1.0 or later) whether mail is waiting, and only then types `pull comm` into the
session. A session with no mail sees nothing, and the wait costs no tokens.

It was built and used on a Linux workstation running about 30 Claude Code sessions in tmux.
Measured there: from a message being sent to `pull comm` appearing in the receiving session
took **40 s** with a 60 s check interval.

## Requirements

- Linux with **tmux**: every Claude Code session runs inside a tmux session. Claude Desktop and
  sessions outside tmux are not covered, because there is nothing to type into.
- Claude Code, which keeps `~/.claude/sessions/<pid>.json` for each running session.
- Ken **6.1.0 or later**, for `POST /comm/waiting` (docs/COMM.md §11a), reachable from the machine.
- `bash`, `python3`, `curl`, and systemd **user** units (with lingering, to run at boot).

## What is in it

| File | What it does |
|---|---|
| `bin/cc-nudge.sh` | Types a prompt into a session, **only** when that is safe (see below). Everything else types through it. |
| `bin/cc-wake.sh` | Generic waker: runs any program as a trigger and wakes one session when it fires. |
| `triggers/ken-mail.sh` | A trigger: exits 0 when Ken has mail waiting for a given station. |
| `bin/cc-ken-waker.py` | **The service for all sessions**: finds every live session, checks each one's station, wakes the ones with mail. |
| `hooks/ken-station-record.py` | PostToolUse hook on `station_me`: records which station each session staffs. |
| `bin/install-ken-station-hook.py` | Adds that hook to `~/.claude/settings.json`, with a backup. |
| `bin/cc-ken-bootstrap.py` | One-time registration of sessions already running, from their transcripts. |
| `bin/lib_station_id.py` | Reads a `station_me` result strictly: only its top-level `station_id`. |
| `systemd/cc-ken-waker.service`, `systemd/cc-wake@.service` | User units for the service and for single wakers. |

## It never interrupts

`cc-nudge.sh` types nothing unless **all** of these hold. If any fails, it refuses, and the
waker retries later:

1. a live Claude Code process runs in that tmux session;
2. the session is **idle**: not working, not running a command, not waiting in a dialog
   (`/login`, `/mcp`, a permission prompt);
3. its input box is visible and holds **no draft** (Claude Code's own dimmed suggestion is fine);
4. nobody has typed in a terminal attached to that tmux session for 15 s, checked again just
   before typing;
5. 3 s later, 2 to 4 still hold, so a session idle only for a moment between steps is not caught;
6. after typing, the box holds exactly the text; only then is Enter pressed. If someone typed at
   the same instant, the text is removed again and theirs is kept;
7. after Enter, the session turns busy.

**It cannot see** a prompt being written through Remote Control (phone or claude.ai). The wake
then runs first and that prompt queues behind it; nothing is lost.

The service types only the fixed text `pull comm`. **What Ken says is never typed**: a session
takes what is typed into it as an instruction, and the woken session reads its mail itself
through the connector, as usual.

## How the service keeps the list of sessions

| Moment | What happens |
|---|---|
| A session is running | Every 15 s the service reads `~/.claude/sessions/*.json` and keeps the sessions whose process is alive and that run in tmux. |
| Its station is known | `~/.local/state/cc-wake/stations/<session-uuid>.json` holds the station id, written by the hook after every `station_me` call, **verbatim from that call's result**. |
| A new session, before its first prompt | It has no station yet, so it is skipped. Its first prompt calls `station_me`, which shows any mail anyway; the hook registers it, and the service picks it up within 15 s. |
| Resume or compaction | Same session UUID, same record. |
| A session ends | Its process is gone, so it is dropped. The record stays, because a resume reuses it. |
| Sessions started before the hook was installed | They do not run it. `bin/cc-ken-bootstrap.py` registers them once, taking only `station_id` from the last `station_me` result in each one's own transcript. Do this only with their owners' agreement. |

## The check

- Each station is asked every 600 s, staggered over the cycle by a hash of its id. Thirty
  stations make about 3 requests a minute against `/comm/waiting`'s default 240 per minute per
  address.
- `{"mail":true}` triggers a wake. A refused wake (the session is busy) is retried every 60 s,
  **and the mail is asked again before each retry**. If the session has pulled it meanwhile,
  nothing is typed.
- `429` pauses all checks for `Retry-After`. `403` pauses them 900 s and logs that the check is
  **blocked**, so silence is not read as an empty inbox. Anything else counts as no news.

## The one hazard: a wrong station id

An id that names no station gets `{"mail":false}`, exactly like an empty station, and Ken counts
it as a **miss**. By default, 10 misses in 600 s from one address block `/comm/waiting` for that
address for 900 s, and every session on a machine shares its address. So:

- ids come **only** from `station_me`'s own result (hook or bootstrap), never typed;
- anything that is not 16 letters or digits is ignored and nothing is sent; that refuses a
  session key too, which is a credential and has a different shape;
- a session with no record is skipped, never guessed;
- only the result's **top-level** `station_id` is read, after parsing it as JSON. A regex over
  the text would take the first id it meets, and a future result that mentions another station
  first would be recorded silently. If the result does not parse, nothing is recorded.

**Stale records:** only live sessions are checked, so a record left by an ended session costs
nothing. A live session whose station was deleted on the Ken console, though, keeps a
valid-looking id that misses on every check. At a 600 s interval, ten such sessions alone fill
the 10-miss window. After deleting a station, end or restart its session; its next `station_me`
rewrites the record.

## Built on Claude Code internals

`cc-nudge.sh` and the service rely on details that are **not a published Claude Code interface**:
the `❯` input marker, dimmed (SGR 2) suggestion text, and the `status`, `tmux` and `sessionId`
fields of `~/.claude/sessions/*.json`. They were tested with **Claude Code 2.1.325**. A release
that changes one of them shows first as refusals in the log ("NO INPUT BOX", "NOT FOUND"): nothing
is typed then, but nothing is woken either. The hook matcher `mcp__.*__station_me` covers both the
claude.ai connector (`mcp__claude_ai_Ken__station_me`) and a direct MCP connection (`mcp__ken__station_me`).

## Install

```bash
mkdir -p ~/.local/share && cp -r contrib/claude-code-waker ~/.local/share/ken-waker   # from a Ken checkout; or edit the paths in systemd/
~/.local/share/ken-waker/bin/install-ken-station-hook.py              # the station_me hook
KEN_URL=https://ken.example.com ~/.local/share/ken-waker/bin/cc-ken-bootstrap.py --all   # optional, see above
# set Environment=KEN_URL=... in systemd/cc-ken-waker.service, then:
ln -sf ~/.local/share/ken-waker/systemd/cc-ken-waker.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user enable --now cc-ken-waker
```

Check it:

```bash
~/.local/share/ken-waker/bin/cc-ken-waker.py --list                           # sessions and their stations
KEN_URL=https://ken.example.com ~/.local/share/ken-waker/bin/cc-ken-waker.py --once --dry-run
tail ~/.local/state/cc-wake/ken-waker.log
```

Claude Code's auto mode may refuse a session that tries to edit its own hooks or enable
services. Run these steps yourself.

## Other triggers

`bin/cc-wake.sh` wakes one session on any event. Repeat mode (default): the trigger is run every
`--interval` s, and exit 0 fires. `--stream`: every line the trigger prints fires. Events are
coalesced and wakes are kept `--min-gap` apart; the trigger's output goes to the log, never into
the session. For example:

```bash
cc-wake.sh --session myproj --text 'the build finished; read build.log' --once \
    -- test -e /path/to/project/.claude/work/build.done
```

`systemd/cc-wake@.service` runs one waker permanently from `~/.config/cc-wake/<name>.conf`, a bash
file setting `SESSION`, `TEXT`, `INTERVAL`, `MINGAP` and `TRIGGER=(command args...)`. Use
`cc-wake.sh --help` for every option.
