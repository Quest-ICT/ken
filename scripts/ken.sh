#!/usr/bin/env bash
# Ken launcher — detached (default) / foreground (-f, for systemd) / stop.
# Wraps the single static binary so systemd (or a shell) has a stable entry point.
#
# Env:
#   KEN_HOME  deployment root (holds bin/, data/, logs/, scripts/). Default: parent of this script's dir.
#   KEN_BIN   path to the ken binary. Default: $KEN_HOME/bin/ken
#   KEN_OPTS  extra serve flags (e.g. --addr :8080 --secure-cookies)
set -euo pipefail

SELF="$(readlink -f "$0")"
SCRIPT_DIR="$(dirname "$SELF")"
KEN_HOME="${KEN_HOME:-$(dirname "$SCRIPT_DIR")}"
KEN_BIN="${KEN_BIN:-$KEN_HOME/bin/ken}"
KEN_OPTS="${KEN_OPTS:-}"
PID_FILE="$KEN_HOME/ken.pid"
LOG_DIR="$KEN_HOME/logs"
LOG_FILE="$LOG_DIR/ken.out"

cd "$KEN_HOME"

mode="detached"
case "${1:-}" in
  -f|--foreground) mode="foreground"; shift ;;
  stop)            mode="stop";       shift ;;
esac

if [ "$mode" = "stop" ]; then
  if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
    kill "$(cat "$PID_FILE")"; echo "[ken] stopped pid $(cat "$PID_FILE")"
  else
    echo "[ken] not running"
  fi
  rm -f "$PID_FILE"
  exit 0
fi

if [ "$mode" = "foreground" ]; then
  exec "$KEN_BIN" serve $KEN_OPTS "$@"
fi

# detached
if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  echo "[ken] already running (pid $(cat "$PID_FILE"))"; exit 1
fi
mkdir -p "$LOG_DIR"
nohup "$KEN_BIN" serve $KEN_OPTS "$@" >>"$LOG_FILE" 2>&1 &
echo $! > "$PID_FILE"
echo "[ken] started pid $(cat "$PID_FILE") (log: $LOG_FILE)"
