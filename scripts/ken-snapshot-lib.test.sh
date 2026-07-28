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

echo "# ken_snapshot_secure — leak safety"
# no recipient -> kept 0600, path on stdout, rc0, UNENCRYPTED warning
f="$T/a.db"; echo d > "$f"; chmod 644 "$f"
out="$(ken_snapshot_secure "$f" "" 2>"$T/e")"; rc=$?
{ [ "$rc" = 0 ] && [ -f "$f" ] && [ "$out" = "$f" ] && [ "$(stat -c%a "$f")" = 600 ]; } \
    && ok "no-recipient: kept, 0600, stdout, rc0" || no "no-recipient" "rc=$rc out=$out"
grep -q UNENCRYPTED "$T/e" && ok "no-recipient: warns UNENCRYPTED" || no "no-recipient warn" "$(cat "$T/e")"

# recipient + age OK -> plaintext gone, .age 0600, stdout=.age, rc0
mkbin "$T/ok" ok; f="$T/b.db"; echo d > "$f"
out="$(PATH="$T/ok:$PATH" ken_snapshot_secure "$f" "age1x" 2>/dev/null)"; rc=$?
{ [ "$rc" = 0 ] && [ ! -f "$f" ] && [ -f "$f.age" ] && [ "$out" = "$f.age" ] && [ "$(stat -c%a "$f.age")" = 600 ]; } \
    && ok "encrypt-ok: plaintext gone, .age 0600" || no "encrypt-ok" "rc=$rc out=$out"

# recipient + age MISSING -> fail closed: plaintext removed, rc1, nothing kept
mkdir -p "$T/noage"; for c in date chmod rm cat; do ln -s "$(command -v "$c")" "$T/noage/$c"; done
f="$T/c.db"; echo d > "$f"
out="$(PATH="$T/noage" ken_snapshot_secure "$f" "age1x" 2>/dev/null)"; rc=$?
{ [ "$rc" = 1 ] && [ ! -f "$f" ] && [ ! -f "$f.age" ] && [ -z "$out" ]; } \
    && ok "age-missing: fail closed, no plaintext left" || no "age-missing" "rc=$rc"

# recipient + age FAILS (writes partial then exits 1) -> plaintext + partial removed, rc1
mkbin "$T/fail" fail; f="$T/d.db"; echo d > "$f"
PATH="$T/fail:$PATH" ken_snapshot_secure "$f" "age1x" >/dev/null 2>&1; rc=$?
{ [ "$rc" = 1 ] && [ ! -f "$f" ] && [ ! -f "$f.age" ]; } \
    && ok "encrypt-fail: plaintext + partial removed" || no "encrypt-fail" "rc=$rc"

# missing raw -> rc1
ken_snapshot_secure "$T/nope.db" "" >/dev/null 2>&1; rc=$?
[ "$rc" = 1 ] && ok "missing-raw: rc1" || no "missing-raw" "rc=$rc"

echo "# ken_recipient_from_env — systemctl show -p Environment parsing"
eq "empty env -> empty"          "$(ken_recipient_from_env 'Environment=KEN_HOME=/x KEN_DB=/y')" ""
eq "recipient first -> value"    "$(ken_recipient_from_env 'Environment=KEN_AGE_RECIPIENT=age1FIRST KEN_HOME=/x')" "age1FIRST"
eq "recipient later -> value"    "$(ken_recipient_from_env 'Environment=KEN_HOME=/x KEN_AGE_RECIPIENT=age1LATER')" "age1LATER"
eq "no Environment -> empty"     "$(ken_recipient_from_env '')" ""

echo "# ken_recipient_from_unit_files — comment-safe file parsing (the harvest regression)"
printf '# Environment=KEN_AGE_RECIPIENT=age1yourpublickey...\n' > "$T/comment.conf"
eq "commented example -> EMPTY (never harvested)" "$(ken_recipient_from_unit_files "$T/comment.conf")" ""
printf '[Service]\nEnvironment="KEN_AGE_RECIPIENT=age1drop"\n' > "$T/drop.conf"
eq "real drop-in (quoted) -> value" "$(ken_recipient_from_unit_files "$T/drop.conf")" "age1drop"
printf 'Environment=KEN_AGE_RECIPIENT=age1plain\n' > "$T/plain.conf"
eq "real unquoted -> value" "$(ken_recipient_from_unit_files "$T/plain.conf")" "age1plain"
printf '# Environment=KEN_AGE_RECIPIENT=age1example...\nEnvironment=KEN_AGE_RECIPIENT=age1real\n' > "$T/both.conf"
eq "comment + real -> only the real" "$(ken_recipient_from_unit_files "$T/both.conf")" "age1real"
eq "missing files -> empty" "$(ken_recipient_from_unit_files "$T/none1.conf" "$T/none2.conf")" ""

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

# The group flows through the real securing path too (plaintext and encrypted).
f="$T/g4.db"; echo d > "$f"
ken_snapshot_secure "$f" "" "$mygrp" >/dev/null 2>&1
eq "secure(): plaintext honours group" "$(stat -c%a "$f")" "640"
f="$T/g5.db"; echo d > "$f"
PATH="$T/ok:$PATH" ken_snapshot_secure "$f" "age1x" "$mygrp" >/dev/null 2>&1
eq "secure(): .age honours group" "$(stat -c%a "$f.age")" "640"

# THE CRITICAL ONE: when a snapshot is going to be ENCRYPTED, the intermediate plaintext
# must NEVER be widened to the backup group -- that would expose the cleartext database to
# that group for the whole `age` run, and permanently if the encrypt were interrupted.
f="$T/g6.db"; echo d > "$f"
mkdir -p "$T/spy"
cat > "$T/spy/age" <<SPY
#!/usr/bin/env bash
o=""; i=""; while [ \$# -gt 0 ]; do case "\$1" in -o) o="\$2"; shift 2;; -r) shift 2;; *) i="\$1"; shift;; esac; done
stat -c%a "\$i" > "$T/plaintext_mode_during_encrypt"   # what the mode was WHILE encrypting
cp "\$i" "\$o"
SPY
chmod +x "$T/spy/age"
PATH="$T/spy:$PATH" ken_snapshot_secure "$f" "age1x" "$mygrp" >/dev/null 2>&1
eq "plaintext stays 0600 during encryption" "$(cat "$T/plaintext_mode_during_encrypt" 2>/dev/null)" "600"
eq "only the .age gets the group" "$(stat -c%a "$f.age")" "640"

echo "# ken_env_value — generalized to any variable (KEN_BACKUP_DIR, KEN_BACKUP_GROUP)"
eq "env: backup dir"   "$(ken_env_value 'Environment=KEN_HOME=/x KEN_BACKUP_DIR=/srv/snaps' KEN_BACKUP_DIR)" "/srv/snaps"
eq "env: backup group" "$(ken_env_value 'Environment=KEN_BACKUP_GROUP=kenbackup KEN_HOME=/x' KEN_BACKUP_GROUP)" "kenbackup"
eq "env: absent var"   "$(ken_env_value 'Environment=KEN_HOME=/x' KEN_BACKUP_GROUP)" ""
printf 'Environment=KEN_BACKUP_DIR=/srv/snaps\n' > "$T/bd.conf"
eq "unit file: backup dir" "$(ken_env_value_from_unit_files KEN_BACKUP_DIR "$T/bd.conf")" "/srv/snaps"
printf '# Environment=KEN_BACKUP_GROUP=example\n' > "$T/bg.conf"
eq "unit file: commented group ignored" "$(ken_env_value_from_unit_files KEN_BACKUP_GROUP "$T/bg.conf")" ""

echo "# template invariant — the shipped unit must never yield a recipient"
UNIT="$REPO/deploy/ken-snapshot.service"
if [ -f "$UNIT" ]; then
    # The invariant that actually matters, asserted through the REAL parser: whatever
    # the template says (it documents the drop-in shape, placeholder included), running
    # our extractor over the shipped file must yield NOTHING. This is what stops the
    # commented example being harvested as a live recipient — the defect that would make
    # a default-config pre-upgrade snapshot fail closed and delete itself.
    got="$(ken_recipient_from_unit_files "$UNIT")"
    [ -z "$got" ] && ok "shipped unit yields no recipient via the parser" \
        || no "template yields a recipient" "[$got] — the example must stay commented"
    # Belt and braces: no placeholder may LOOK like a real age key (age1 + bech32 tail),
    # so a future naive parser cannot mistake it for one either.
    plausible="$(grep -hoE 'age1[a-z0-9]{8,}' "$UNIT" 2>/dev/null | head -1 || true)"
    [ -z "$plausible" ] && ok "no realistic-looking age key in the template" \
        || no "template carries a realistic key" "[$plausible] — use an obvious placeholder"
else
    ok "deploy/ken-snapshot.service not present (skipped)"
fi

echo ""
printf 'RESULT: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
