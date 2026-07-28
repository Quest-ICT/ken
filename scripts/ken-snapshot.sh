#!/usr/bin/env bash
# Nightly consistent snapshot of the Ken database (backup tier 2), age-encrypted
# when KEN_AGE_RECIPIENT is set — otherwise left plaintext at 0600, with a warning.
# Litestream (tier 1) gives continuous ~1s-RPO replication; this gives named
# point-in-time restore points. Encryption is what makes an off-box copy safe: see
# docs/BACKUP.md ("Encryption: turning it on") for the decision and the runbook.
#
# Env:
#   KEN_HOME, KEN_BIN, KEN_DB        as in ken.sh
#   KEN_BACKUP_DIR                   where snapshots land (default $KEN_HOME/backups)
#   KEN_AGE_RECIPIENT                age public key (age1...) — set it for ENCRYPTED snapshots
#   KEN_BACKUP_KEEP                  snapshots to retain (default 14)
set -euo pipefail

SELF="$(readlink -f "$0")"
SCRIPT_DIR="$(dirname "$SELF")"
# Snapshot naming + securing policy is shared with the installer's pre-upgrade
# snapshot, so the two can never drift on timezone, mode, or encryption again.
# shellcheck source=ken-snapshot-lib.sh
. "$SCRIPT_DIR/ken-snapshot-lib.sh"
KEN_HOME="${KEN_HOME:-$(dirname "$SCRIPT_DIR")}"
KEN_BIN="${KEN_BIN:-$KEN_HOME/bin/ken}"
export KEN_DB="${KEN_DB:-$KEN_HOME/data/ken.db}"
BACKUP_DIR="${KEN_BACKUP_DIR:-$KEN_HOME/backups}"
KEEP="${KEN_BACKUP_KEEP:-14}"
case "$KEEP" in '' | *[!0-9]*) echo "[ken-snapshot] WARNING: invalid KEN_BACKUP_KEEP='$KEEP'; using 14" >&2; KEEP=14 ;; esac
[ "$KEEP" -ge 1 ] 2>/dev/null || KEEP=14

mkdir -p "$BACKUP_DIR"
RAW="$BACKUP_DIR/ken-$(ken_snapshot_stamp).db"

# Consistent copy + mandatory integrity check (fails loudly on corruption).
"$KEN_BIN" backup snapshot --out "$RAW"

# Lock the mode down and (if a recipient is set) age-encrypt, leak-safe. The shared
# helper is the single home for that policy; it returns non-zero only when an
# INTENDED encryption did not happen (and it has already removed the plaintext),
# which we turn into a non-zero exit below so systemd flags the run.
FAILED=0
if FINAL="$(ken_snapshot_secure "$RAW" "${KEN_AGE_RECIPIENT:-}")"; then
  [ -n "$FINAL" ] && echo "[ken-snapshot] wrote $FINAL"
else
  FAILED=1
fi

# Retention: keep the newest $KEEP snapshots (KEEP validated >= 1 above).
ls -1t "$BACKUP_DIR"/ken-*.db* 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f || true

# Non-zero exit if an intended encryption didn't happen, so systemd flags it.
exit "$FAILED"
