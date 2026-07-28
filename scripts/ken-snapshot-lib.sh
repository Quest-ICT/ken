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
# was the third: the nightly age-encrypts, the pre-upgrade path wrote plaintext.
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

# ken_snapshot_secure <raw_path> [age_recipient] — lock down a freshly written
# plaintext snapshot, then encrypt it if (and only if) a recipient is given.
#
# The caller has just written a plaintext snapshot at <raw_path> (via
# `ken backup snapshot --out`). This function takes it from there:
#
#   * Lock the mode down IMMEDIATELY — before any encryption step — so a full copy of
#     the database is never even briefly world-readable. `ken backup snapshot` writes at
#     the caller's umask (root's is 0644), and a mode travels with COPIES: an off-box
#     backup of the backups dir would otherwise carry a world-readable DB wherever it
#     lands. Default is 0600 (owner only). With a <group> (KEN_BACKUP_GROUP) it is 0640
#     owned by that group instead — see ken_snapshot_lock below for why that exists.
#   * No recipient  -> leave the plaintext (0600), warn that it is UNENCRYPTED, print
#     its path on stdout, return 0. (A snapshot exists; the operator simply has not
#     opted into encryption. Same stance the nightly always took.)
#   * Recipient set -> age-encrypt to "<raw>.age" (0600) and remove the plaintext
#     ONLY after a successful encrypt; print the .age path, return 0. If `age` is not
#     installed, or encryption fails, remove the plaintext (and any partial .age) and
#     return 1 — FAIL CLOSED: an operator who asked for encryption never gets a
#     plaintext copy left behind instead. No snapshot is kept in that case.
#
# Contract: stdout = final snapshot path on success (nothing on failure); stderr =
# diagnostics; return 0 = a snapshot is present, 1 = an intended encryption did not
# happen and nothing was kept. The caller decides what a 1 means for it (the nightly
# exits non-zero so systemd flags the run; the installer warns and proceeds, because
# a missing rollback point must never abort an upgrade).
ken_snapshot_secure() {
    _kss_raw="$1"
    _kss_recipient="${2:-}"
    _kss_group="${3:-}"
    _kss_enc="$_kss_raw.age"

    if [ ! -f "$_kss_raw" ]; then
        echo "snapshot: expected plaintext snapshot at $_kss_raw is missing" >&2
        return 1
    fi

    # ALWAYS 0600 first, with no group. A plaintext snapshot that is about to be
    # encrypted is an INTERMEDIATE, and must never be widened to the backup group: doing
    # so would expose the cleartext database to that group for the whole `age` run — and
    # permanently if the encrypt is interrupted. Only the FINAL artifact gets the group.
    ken_snapshot_lock "$_kss_raw" ""

    if [ -z "$_kss_recipient" ]; then
        # No encryption: the plaintext IS the final artifact, so it takes the group.
        ken_snapshot_lock "$_kss_raw" "$_kss_group"
        echo "snapshot: WARNING: KEN_AGE_RECIPIENT not set — snapshot left UNENCRYPTED at $_kss_raw" >&2
        printf '%s\n' "$_kss_raw"
        return 0
    fi

    if ! command -v age >/dev/null 2>&1; then
        echo "snapshot: ERROR: a recipient is set but 'age' is not installed — removing plaintext, no snapshot kept" >&2
        rm -f "$_kss_raw"
        return 1
    fi

    if age -r "$_kss_recipient" -o "$_kss_enc" "$_kss_raw"; then
        ken_snapshot_lock "$_kss_enc" "$_kss_group"
        rm -f "$_kss_raw"   # drop the plaintext ONLY after a confirmed encrypt
        printf '%s\n' "$_kss_enc"
        return 0
    fi

    echo "snapshot: ERROR: age encryption failed — removing plaintext, no snapshot kept" >&2
    rm -f "$_kss_raw" "$_kss_enc"
    return 1
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
# `#`-commented line, so the bundled unit's placeholder example
# (`# Environment=KEN_AGE_RECIPIENT=…`) is never harvested as a bogus value — the defect
# that would otherwise make a default-config pre-upgrade snapshot fail closed and delete
# itself. Optional leading indent and quotes (as `systemctl edit` writes) are tolerated.
# Prints the last value found, or nothing.
ken_env_value_from_unit_files() {
    _kev_var="$1"
    shift
    sed -n "s/^[[:space:]]*Environment=\"\{0,1\}${_kev_var}=\([^[:space:]\"]*\).*/\1/p" \
        "$@" 2>/dev/null | tail -n1 || true
}

# Back-compat wrappers for the recipient, the original callers of the above.
ken_recipient_from_env()        { ken_env_value "${1:-}" KEN_AGE_RECIPIENT; }
ken_recipient_from_unit_files() { ken_env_value_from_unit_files KEN_AGE_RECIPIENT "$@"; }
