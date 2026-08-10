# shellcheck shell=bash
# ken-snapshot-lib.sh — shared snapshot naming + securing policy.
#
# SOURCED, never executed (no shebang, not +x). Two callers use it:
#   * scripts/ken-snapshot.sh   — the nightly systemd snapshot (backup tier 2)
#   * scripts/install.sh        — the pre-upgrade rollback snapshot
#
# WHY THIS FILE EXISTS. Those two paths independently answered the same three
# questions — how a snapshot is NAMED, what MODE it lands with, and whether it is
# ENCRYPTED — and drifted apart, one property at a time. The pre-upgrade path had
# already needed a follow-up fix for the file MODE (world-readable 0644; 1.2.1) and
# a second for the TIMESTAMP (local, unmarked, six hours off the nightly's UTC —
# which sent a production operator chasing a non-existent clock fault). Encryption
# was the third: the two paths disagreed about naming and mode.
# Three fixes to the same shape of defect — the rollback path not doing something
# the nightly path already did — is a sign the policy should live in ONE place. It
# does now: the callers own their control flow, this file owns the policy.
#
# The functions are written to be safe under the callers' `set -euo pipefail`: every
# fallible step is guarded, and a failure RETURNS non-zero rather than exiting, so a
# caller can keep going (retention, a best-effort upgrade) and choose its own exit.

# ken_snapshot_stamp — the ONE canonical snapshot timestamp: UTC, and self-describing.
#
# UTC removes the ambiguity that six-hour-apart local stamps created; the trailing Z
# says so to any human reading the directory. The `T…Z` (no separators) form sorts
# lexically in time order and is filename-safe. Every snapshot name in the system —
# nightly `ken-<stamp>.db` and pre-upgrade `pre-upgrade-<stamp>.db` — comes from here,
# so the two can never disagree about what a name means again.
ken_snapshot_stamp() {
    date -u +%Y%m%dT%H%M%SZ
}

# ken_snapshot_secure <path> [group] — apply the file-permission policy to a freshly
# written snapshot and echo its final path.
#
# IT NO LONGER ENCRYPTS. Ken used to age-encrypt here when KEN_AGE_RECIPIENT was set,
# and that is retired: Ken writes a compressed, unencrypted snapshot at 0600 and stops.
# Transport, destination and at-rest protection belong to whoever moves the file.
#
# Vlad's ruling, and the reasoning is worth keeping because it reverses an earlier one:
# the age layer cost him days of production trouble across three sessions for a property
# he was not buying, Ken is not designed to hold national-security material, and the
# operator — instructed clearly — owns the handling. Encryption on this box also made the
# archive incompressible AND undedupable, so removing it is what let compression happen
# at all: ciphertext shares no bytes with yesterday's ciphertext.
#
# The name is kept: what it secures is now the mode, which is the half that was always
# unconditional.
ken_snapshot_secure() {
    _kss_raw="$1"
    _kss_group="${2:-}"

    if [ ! -f "$_kss_raw" ]; then
        echo "snapshot: expected snapshot at $_kss_raw is missing" >&2
        return 1
    fi

    ken_snapshot_lock "$_kss_raw" "$_kss_group"
    printf '%s\n' "$_kss_raw"
    return 0
}

# ken_prune_pre_upgrade <dir> <keep_count> <keep_days> — bound the rollback points the
# installer leaves behind, keeping whichever floor preserves MORE.
#
# These were never pruned by anything. ken-snapshot.sh's retention globs `ken-*.db*`,
# which cannot match `pre-upgrade-*` — they share no prefix — so a rollback point was
# kept forever and every upgrade added one. Measured on the live deployment: nine files,
# 19.6 MB, 30% of the entire archive, the oldest dating from the day the box was built.
#
# WHY BOTH FLOORS, and it is not belt-and-braces. ken-prod-ops measured the real upgrade
# cadence and BOTH degenerate cases occurred inside thirteen days:
#
#   burst    four upgrades in one day. Keeping the newest three evicts the point taken
#            before that day started — the one you want when the day is what broke it.
#   drought  255 hours with no upgrade. A seven-day age bound alone would have deleted
#            every rollback point, leaving none at the moment of the next upgrade.
#
# So a file survives if it is among the newest N *or* younger than D days. Neither
# condition alone is safe and both conditions are ordinary.
#
# Deletion is by mtime order via `ls -1t`, matching the nightly policy directly above it
# rather than parsing the timestamp out of the filename — a name is a claim, an mtime is
# what the filesystem observed.
ken_prune_pre_upgrade() {
    _kpu_dir="$1"
    _kpu_keep="${2:-3}"
    _kpu_days="${3:-7}"

    [ -d "$_kpu_dir" ] || return 0

    # Everything OLDER than the age floor is a deletion candidate; everything younger is
    # kept outright. -mtime +N is "more than N days old", which is the floor we want.
    _kpu_old="$(find "$_kpu_dir" -maxdepth 1 -type f -name 'pre-upgrade-*' -mtime "+$_kpu_days" 2>/dev/null)"
    [ -n "$_kpu_old" ] || return 0

    # Of those, still keep the newest $_kpu_keep overall — the count floor, which is what
    # survives a drought where EVERY file is older than the age bound.
    _kpu_keepers="$(ls -1t "$_kpu_dir"/pre-upgrade-* 2>/dev/null | head -n "$_kpu_keep")"

    printf '%s\n' "$_kpu_old" | while IFS= read -r _kpu_f; do
        [ -n "$_kpu_f" ] || continue
        # Keep it if it is one of the newest N.
        if printf '%s\n' "$_kpu_keepers" | grep -Fxq "$_kpu_f"; then
            continue
        fi
        rm -f "$_kpu_f"
    done
    return 0
}

# ken_snapshot_lock <path> [group] — apply the snapshot file-permission policy.
#
#   no group  -> 0600. Owner (the service account) and root only. The default, and
#                unchanged from every prior release.
#   group set -> 0640 owned by <group>, so a PURPOSE-MADE account in that group can
#                read snapshots without being root.
#
# WHY the group mode exists. Ken's own backup design names an off-box copy as a tier,
# but 0600 files owned by a `nologin` service account inside a 0750 directory can only
# be read by root — so the only ways to pull a backup off the host were a root-authorized
# SSH key or a root cron job staging copies elsewhere. Handing an archive host a root
# key to move files that are (ideally) already encrypted is a poor trade; a dedicated
# unprivileged group is the smaller grant. POSIX ACLs are NOT an alternative here:
# `chmod 0600` zeroes the group bits, which sets the ACL mask to `---` and neuters any
# named-user entry — so a re-chmod on every run defeats them by construction.
#
# Group assignment normally costs nothing at runtime: the installer makes the backups
# directory setgid to that group, so files created in it inherit it. The chgrp below is
# the fallback for hand-rolled layouts.
#
# FAILS SAFE, never open: if the group cannot be applied (it does not exist, or the
# snapshot user is not a member and the directory is not setgid), the file keeps 0600 and
# a warning says so. A snapshot is never left group-readable by the WRONG group.
ken_snapshot_lock() {
    _ksl_path="$1"
    _ksl_group="${2:-}"

    if [ -z "$_ksl_group" ]; then
        chmod 600 "$_ksl_path"
        return 0
    fi

    # Already in the target group (the setgid-directory path)? Nothing to change.
    if [ "$(stat -c '%G' "$_ksl_path" 2>/dev/null || echo '?')" = "$_ksl_group" ] \
       || chgrp "$_ksl_group" "$_ksl_path" 2>/dev/null; then
        chmod 640 "$_ksl_path"
        return 0
    fi

    chmod 600 "$_ksl_path"
    echo "snapshot: WARNING: could not put $_ksl_path in group '$_ksl_group' — keeping 0600." >&2
    echo "snapshot:          Off-box pulls by that group will not work until the group exists and" >&2
    echo "snapshot:          the backups directory is setgid to it (see docs/BACKUP.md)." >&2
    return 0
}

# ken_env_value <text> <VAR> — pull VAR out of a `systemctl show -p Environment` value.
# That output is `Environment=VAR1=v1 VAR2=v2 …`: the `Environment=` prefix attaches to
# the FIRST var only, so match position-independently (strip up to and including the key)
# rather than anchoring on it. Prints the value, or nothing. systemd never surfaces
# #-commented lines here, so this is the trustworthy source when systemctl is present.
ken_env_value() {
    printf '%s' "${1:-}" | tr ' ' '\n' | sed -n "s/.*${2}=//p" | tail -n1 || true
}

# ken_env_value_from_unit_files <VAR> <file…> — pull VAR from REAL `Environment=`
# assignment lines in the given unit files, used only on a non-systemd host (no
# systemctl). The anchor `^[[:space:]]*Environment=` is what makes this safe: it SKIPS a
# `#`-commented line, so a placeholder the unit documents by example
# (`# Environment=KEN_BACKUP_GROUP=…`) is never harvested as a live value. That defect
# was real once: a commented example read as a real setting made a default-config
# pre-upgrade snapshot fail closed and delete itself. Optional leading indent and quotes
# (as `systemctl edit` writes) are tolerated.
# Prints the last value found, or nothing.
ken_env_value_from_unit_files() {
    _kev_var="$1"
    shift
    sed -n "s/^[[:space:]]*Environment=\"\{0,1\}${_kev_var}=\([^[:space:]\"]*\).*/\1/p" \
        "$@" 2>/dev/null | tail -n1 || true
}

# Back-compat wrappers for the recipient, the original callers of the above.
