#!/usr/bin/env bash
# Nightly consistent snapshot of the Ken database (backup tier 2): gzip-compressed,
# 0600, and NOT encrypted. Transport and destination belong to whoever moves the file.
# Litestream (tier 1) gives continuous ~1s-RPO replication; this gives named
# point-in-time restore points. Encryption is what makes an off-box copy safe: see
# docs/BACKUP.md ("Encryption: turning it on") for the decision and the runbook.
#
# Env:
#   KEN_HOME, KEN_BIN, KEN_DB        as in ken.sh
#   KEN_BACKUP_DIR                   where snapshots land (default $KEN_HOME/backups)
#   KEEP_PRE_UPGRADE                 rollback points to keep by count (default 3)
#   KEEP_PRE_UPGRADE_DAYS            rollback points to keep by age  (default 7)
#                                    a file survives if EITHER floor keeps it
#   KEN_BACKUP_KEEP                  snapshots to retain (default 14)
#   KEN_BACKUP_GROUP                 group that may READ snapshots (0640 instead of
#                                    0600) — lets an unprivileged off-box backup
#                                    account pull them without root. Unset = 0600.
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

KEEP_PRE_UPGRADE="${KEEP_PRE_UPGRADE:-3}"
KEEP_PRE_UPGRADE_DAYS="${KEEP_PRE_UPGRADE_DAYS:-7}"
[ "$KEEP_PRE_UPGRADE" -ge 1 ] 2>/dev/null || KEEP_PRE_UPGRADE=3
[ "$KEEP_PRE_UPGRADE_DAYS" -ge 1 ] 2>/dev/null || KEEP_PRE_UPGRADE_DAYS=7

# Create the archive dir 0750 if it is ours to create; never re-chmod one that already
# exists (the installer may have made it 2750 for the backup group — see docs/BACKUP.md).
if [ ! -d "$BACKUP_DIR" ]; then
  mkdir -p "$BACKUP_DIR"
  chmod 0750 "$BACKUP_DIR"
fi
# A retired variable must not go quiet. An operator who set KEN_AGE_RECIPIENT asked for
# encrypted snapshots; from 2.0.0 they get compressed plaintext instead. That is the
# intended behaviour and it is still a change they did not make, so it is said out loud
# once per run rather than discovered from a file listing months later.
if [ -n "${KEN_AGE_RECIPIENT:-}" ]; then
  echo "[ken-snapshot] NOTE: KEN_AGE_RECIPIENT is set but RETIRED in 2.0.0 — Ken no longer encrypts snapshots." >&2
  echo "[ken-snapshot]       This snapshot is compressed plaintext at 0600. Encryption, transport and" >&2
  echo "[ken-snapshot]       destination are now yours; see docs/BACKUP.md. Unset the variable to silence this." >&2
fi

RAW="$BACKUP_DIR/ken-$(ken_snapshot_stamp).db.gz"

# Consistent copy + mandatory integrity check (fails loudly on corruption).
"$KEN_BIN" backup snapshot --out "$RAW"

# Lock the mode down. Ken no longer encrypts here — it writes a compressed snapshot at
# 0600 and stops; transport and destination belong to whoever moves the file.
FAILED=0
if FINAL="$(ken_snapshot_secure "$RAW" "${KEN_BACKUP_GROUP:-}")"; then
  [ -n "$FINAL" ] && echo "[ken-snapshot] wrote $FINAL"
else
  FAILED=1
fi

# Retention: keep the newest $KEEP nightlies. This glob deliberately matches ONLY
# ken-*, never pre-upgrade-* — those are a different artifact with a different value and
# they are pruned separately below.
ls -1t "$BACKUP_DIR"/ken-*.db* 2>/dev/null | tail -n +$((KEEP + 1)) | xargs -r rm -f || true

# Pre-upgrade rollback points, which NOTHING pruned until now. The glob above cannot
# match them — `ken-*` and `pre-upgrade-*` share no prefix — so every upgrade left one
# behind forever. ken-prod-ops measured nine of them, 19.6 MB, 30% of the whole archive,
# oldest from the day the deployment was built.
#
# TWO FLOORS, keep whichever is MORE, because ken-prod-ops measured both failure modes
# inside one thirteen-day window:
#   - a COUNT alone breaks on a burst. Four upgrades landed on one day; keeping the last
#     three evicts the rollback point taken before that day's work began, which is
#     precisely the one you want when that day's work is what broke things.
#   - an AGE alone breaks on a drought. 255 hours passed with no upgrade; a seven-day
#     bound would have left ZERO rollback points at the moment of the next one, and a
#     deployment upgrading monthly hits that every single time.
ken_prune_pre_upgrade "$BACKUP_DIR" "$KEEP_PRE_UPGRADE" "$KEEP_PRE_UPGRADE_DAYS"

# Non-zero exit if an intended encryption didn't happen, so systemd flags it.
exit "$FAILED"
