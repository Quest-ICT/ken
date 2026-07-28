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

echo "# template invariant — the shipped unit carries no harvestable recipient token"
UNIT="$REPO/deploy/ken-snapshot.service"
if [ -f "$UNIT" ]; then
    harvest="$(grep -hoE 'KEN_AGE_RECIPIENT=[^[:space:]"]+' "$UNIT" 2>/dev/null | tail -n1 || true)"
    [ -z "$harvest" ] && ok "deploy/ken-snapshot.service: nothing harvestable" \
        || no "template harvestable" "[$harvest] — drop the example VALUE from the comment"
else
    ok "deploy/ken-snapshot.service not present (skipped)"
fi

echo ""
printf 'RESULT: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
