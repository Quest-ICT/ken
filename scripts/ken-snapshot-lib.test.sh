#!/usr/bin/env bash
# Regression tests for scripts/ken-snapshot-lib.sh — the shared snapshot naming +
# securing policy used by both the nightly snapshot and the installer's pre-upgrade
# snapshot. Dependency-free (no bats): run directly, exits non-zero on any failure.
#
#     bash scripts/ken-snapshot-lib.test.sh
#
# Covers the security-critical behaviour: leak-safe securing (a plaintext DB copy is
# never left behind when encryption was intended), and the recipient parsers — including
# the regression that a #-commented example in the unit template is NEVER harvested as a
# real recipient (which once made a default-config pre-upgrade snapshot delete itself).
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(dirname "$HERE")"
# shellcheck source=ken-snapshot-lib.sh
. "$HERE/ken-snapshot-lib.sh"

T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
pass=0; fail=0
ok(){ printf '  PASS %s\n' "$1"; pass=$((pass + 1)); }
no(){ printf '  FAIL %s -- %s\n' "$1" "$2"; fail=$((fail + 1)); }
eq(){ [ "$2" = "$3" ] && ok "$1" || no "$1" "got [$2] want [$3]"; }

# A mock `age` in its own bindir, prepended to PATH so the lib finds it but the
# script's own shebang (needs env/bash/cp) still resolves from the real PATH.
mkbin(){ # <dir> <mode: ok|fail>
    mkdir -p "$1"
    if [ "$2" = ok ]; then
        cat > "$1/age" <<'A'
#!/usr/bin/env bash
o=""; i=""; while [ $# -gt 0 ]; do case "$1" in -o) o="$2"; shift 2;; -r) shift 2;; *) i="$1"; shift;; esac; done; cp "$i" "$o"
A
    else
        cat > "$1/age" <<'A'
#!/usr/bin/env bash
o=""; while [ $# -gt 0 ]; do case "$1" in -o) o="$2"; shift 2;; -r) shift 2;; *) shift;; esac; done; echo partial > "$o"; exit 1
A
    fi
    chmod +x "$1/age"
}

echo "# ken_snapshot_stamp"
s="$(ken_snapshot_stamp)"
[[ "$s" =~ ^[0-9]{8}T[0-9]{6}Z$ ]] && ok "UTC-Z, sortable, filename-safe: $s" || no "stamp format" "$s"

echo "# ken_snapshot_secure — mode policy (encryption retired)"
# Ken no longer encrypts. The helper locks the mode and echoes the final path; every
# `age` case below this line was deleted with the feature rather than left skipped,
# because a skipped test for a removed feature reads as coverage forever.
f="$T/a.db"; echo d > "$f"; chmod 644 "$f"
out="$(ken_snapshot_secure "$f" 2>"$T/e")"; rc=$?
{ [ "$rc" = 0 ] && [ -f "$f" ] && [ "$out" = "$f" ] && [ "$(stat -c%a "$f")" = 600 ]; } \
    && ok "secure(): kept, 0600, stdout, rc0" || no "secure()" "rc=$rc out=$out"

# The UNENCRYPTED warning is GONE. It fired on what is now the intended and only
# configuration, and a warning that fires when nothing is wrong trains people to ignore
# warnings -- after which the next real one lands in a channel nobody reads.
grep -qi "UNENCRYPTED" "$T/e" && no "secure(): still warns about encryption" "$(cat "$T/e")" \
    || ok "secure(): no warning on the normal path"

# missing file -> rc1
ken_snapshot_secure "$T/nope.db" >/dev/null 2>&1; rc=$?
[ "$rc" = 1 ] && ok "missing-file: rc1" || no "missing-file" "rc=$rc"

echo "# ken_snapshot_lock — the file-permission policy (0600 default, 0640 + group opt-in)"
# Default: no group -> 0600, exactly as every prior release.
f="$T/g1.db"; echo d > "$f"; chmod 644 "$f"
ken_snapshot_lock "$f" ""
eq "no group -> 0600" "$(stat -c%a "$f")" "600"

# A group the caller is genuinely in -> 0640 owned by it, so an off-box puller can read.
mygrp="$(id -gn)"
f="$T/g2.db"; echo d > "$f"; chmod 600 "$f"
ken_snapshot_lock "$f" "$mygrp"
eq "own group -> 0640" "$(stat -c%a "$f")" "640"
eq "own group -> group applied" "$(stat -c%G "$f")" "$mygrp"

# FAILS SAFE: an impossible group must never leave the file group-readable by the WRONG
# group -- it keeps 0600 and warns, rather than half-applying the policy.
f="$T/g3.db"; echo d > "$f"; chmod 600 "$f"
ken_snapshot_lock "$f" "no-such-group-$$" 2>"$T/gerr"
eq "unknown group -> stays 0600" "$(stat -c%a "$f")" "600"
grep -q "could not put" "$T/gerr" && ok "unknown group warns" || no "unknown group warn" "$(cat "$T/gerr")"

# The group flows through the real securing path.
f="$T/g4.db"; echo d > "$f"
ken_snapshot_secure "$f" "$mygrp" >/dev/null 2>&1
eq "secure(): honours group" "$(stat -c%a "$f")" "640"

echo "# ken_prune_pre_upgrade — two floors, whichever keeps more"
# ken-snapshot.sh's nightly glob is `ken-*.db*`, which cannot match `pre-upgrade-*`, so
# these were kept forever: ken-prod-ops measured nine of them at 30% of the archive.
P="$T/prune"; mkdir -p "$P"
mkfile() { : > "$P/$1"; touch -d "$2" "$P/$1"; }

# A DROUGHT: every file older than the age floor. The count floor must still keep 3.
for i in 1 2 3 4 5; do mkfile "pre-upgrade-old$i.db.gz" "$((20 + i)) days ago"; done
ken_prune_pre_upgrade "$P" 3 7
n="$(ls -1 "$P"/pre-upgrade-* 2>/dev/null | wc -l)"
eq "drought: count floor keeps 3 when all are old" "$n" "3"

# A BURST: five upgrades today. The age floor must keep ALL of them, because the one you
# want after a bad day is the point taken BEFORE that day's work started.
rm -f "$P"/pre-upgrade-*
for i in 1 2 3 4 5; do mkfile "pre-upgrade-burst$i.db.gz" "now"; done
ken_prune_pre_upgrade "$P" 3 7
n="$(ls -1 "$P"/pre-upgrade-* 2>/dev/null | wc -l)"
eq "burst: age floor keeps all 5 despite a count of 3" "$n" "5"

# CONTROL: it must actually delete something, or both results above pass vacuously.
rm -f "$P"/pre-upgrade-*
for i in 1 2 3 4 5 6; do mkfile "pre-upgrade-mix$i.db.gz" "$((i * 10)) days ago"; done
ken_prune_pre_upgrade "$P" 3 7
n="$(ls -1 "$P"/pre-upgrade-* 2>/dev/null | wc -l)"
eq "mixed: prunes down to the count floor" "$n" "3"

# THE CASE THAT SHIPPED BROKEN: a SYMLINKED backup directory, which is the DEFAULT
# layout. BACKUP_DIR is $KEN_HOME/backups, KEN_HOME is /opt/ken/current, and
# current/backups is a symlink to /opt/ken/backups.
#
# find does not descend into a symlinked start point, so without -H this pruned nothing
# on every standard install — while the fixtures above, built in a plain temp dir,
# passed forever. ken-prod-ops found it by counting rollback points across an upgrade
# (9 -> 10 -> 10, expected 3), not by reading the code.
#
# The lesson is the fixture, not the flag: it has to reproduce the LAYOUT, not just the
# inputs. Everything above this line was a correct test of the wrong shape.
REAL="$T/real-backups"; mkdir -p "$REAL"
LINKED="$T/home/backups"; mkdir -p "$T/home"; ln -s "$REAL" "$LINKED"
for i in 1 2 3 4 5; do : > "$REAL/pre-upgrade-sym$i.db.gz"; touch -d "$((20 + i)) days ago" "$REAL/pre-upgrade-sym$i.db.gz"; done
ken_prune_pre_upgrade "$LINKED" 3 7
n="$(ls -1 "$REAL"/pre-upgrade-* 2>/dev/null | wc -l)"
eq "symlinked backup dir: prunes to the count floor" "$n" "3"

# CONTROL: prove the fixture is actually a symlink, or the assertion above is just the
# plain-directory case wearing a different path.
[ -L "$LINKED" ] && ok "the fixture directory really is a symlink" \
    || no "fixture is not a symlink" "the case that shipped broken is not being tested"

# And it must never touch a nightly.
: > "$P/ken-20260810T000000Z.db.gz"; touch -d "90 days ago" "$P/ken-20260810T000000Z.db.gz"
ken_prune_pre_upgrade "$P" 1 1
[ -f "$P/ken-20260810T000000Z.db.gz" ] && ok "never deletes a nightly" || no "nightly deleted" "gone"

echo "# ken_env_value — generalized to any variable (KEN_BACKUP_DIR, KEN_BACKUP_GROUP)"
eq "env: backup dir"   "$(ken_env_value 'Environment=KEN_HOME=/x KEN_BACKUP_DIR=/srv/snaps' KEN_BACKUP_DIR)" "/srv/snaps"
eq "env: backup group" "$(ken_env_value 'Environment=KEN_BACKUP_GROUP=kenbackup KEN_HOME=/x' KEN_BACKUP_GROUP)" "kenbackup"
eq "env: absent var"   "$(ken_env_value 'Environment=KEN_HOME=/x' KEN_BACKUP_GROUP)" ""
printf 'Environment=KEN_BACKUP_DIR=/srv/snaps\n' > "$T/bd.conf"
eq "unit file: backup dir" "$(ken_env_value_from_unit_files KEN_BACKUP_DIR "$T/bd.conf")" "/srv/snaps"
printf '# Environment=KEN_BACKUP_GROUP=example\n' > "$T/bg.conf"
eq "unit file: commented group ignored" "$(ken_env_value_from_unit_files KEN_BACKUP_GROUP "$T/bg.conf")" ""

echo "# template invariant — the shipped unit's mode policy"
UNIT="$REPO/deploy/ken-snapshot.service"
if [ -f "$UNIT" ]; then
    # What remains of this section after encryption was retired. The recipient-parser
    # invariants went with the feature: there is no recipient for the template to leak,
    # and a test asserting the absence of a removed thing is coverage theatre.
    grep -qE '^UMask=0077' "$UNIT" && ok "unit sets UMask=0077 (snapshot born 0600)" \
        || no "unit missing UMask=0077" "a dump would sit world-readable until the chmod"
fi
