#!/usr/bin/env bash
# Nightly consistent, age-encrypted snapshot of the Ken database (backup tier 2).
# Litestream (tier 1) gives continuous ~1s-RPO replication; this gives named
# point-in-time restore points. Everything that leaves the box is encrypted.
#
# Env:
#   KEN_HOME, KEN_BIN, KEN_DB        as in ken.sh
#   KEN_BACKUP_DIR                   where snapshots land (default $KEN_HOME/backups)
#   KEN_AGE_RECIPIENT                age public key (age1...) — set it for ENCRYPTED snapshots
#   KEN_BACKUP_KEEP                  snapshots to retain (default 14)
set -euo pipefail

SELF="$(readlink -f "$0")"
SCRIPT_DIR="$(dirname "$SELF")"
KEN_HOME="${KEN_HOME:-$(dirname "$SCRIPT_DIR")}"
KEN_BIN="${KEN_BIN:-$KEN_HOME/bin/ken}"
export KEN_DB="${KEN_DB:-$KEN_HOME/data/ken.db}"
BACKUP_DIR="${KEN_BACKUP_DIR:-$KEN_HOME/backups}"
KEEP="${KEN_BACKUP_KEEP:-14}"
case "$KEEP" in '' | *[!0-9]*) echo "[ken-snapshot] WARNING: invalid KEN_BACKUP_KEEP='$KEEP'; using 14" >&2; KEEP=14 ;; esac
[ "$KEEP" -ge 1 ] 2>/dev/null || KEEP=14

mkdir -p "$BACKUP_DIR"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RAW="$BACKUP_DIR/ken-$STAMP.db"
ENC="$RAW.age"

# Consistent copy + mandatory integrity check (fails loudly on corruption).
"$KEN_BIN" backup snapshot --out "$RAW"
chmod 600 "$RAW"   # lock down the plaintext immediately, before any encryption step

FAILED=0
if [ -n "${KEN_AGE_RECIPIENT:-}" ]; then
  if command -v age >/dev/null 2>&1; then
    if age -r "$KEN_AGE_RECIPIENT" -o "$ENC" "$RAW"; then
      chmod 600 "$ENC"
      rm -f "$RAW"   # remove the plaintext ONLY after a successful encrypt
      echo "[ken-snapshot] wrote $ENC"
    else
      echo "[ken-snapshot] ERROR: age encryption failed — removing plaintext, no snapshot kept this run" >&2
      rm -f "$RAW" "$ENC"
      FAILED=1
    fi
  else
    echo "[ken-snapshot] ERROR: KEN_AGE_RECIPIENT set but 'age' is not installed — removing plaintext" >&2
    rm -f "$RAW"
    FAILED=1
  fi
else
  echo "[ken-snapshot] WARNING: KEN_AGE_RECIPIENT not set — snapshot left UNENCRYPTED (0600) at $RAW" >&2
fi

# Retention: keep the newest $KEEP snapshots (KEEP validated >= 1 above).
ls -1t "$BACKUP_DIR"/ken-*.db* 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f || true

# Non-zero exit if an intended encryption didn't happen, so systemd flags it.
exit "$FAILED"
