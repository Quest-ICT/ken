#!/usr/bin/env bash
#
# verify-artifact.sh — verify built release artifacts before they are published.
#
#   scripts/verify-artifact.sh --dist dist --version X.Y.Z [--ref <git-ref>]
#
# WHY THIS EXISTS, separately from check-public-hygiene.sh:
# the hygiene gate scans the git TREE. The release binary is built AFTER that gate,
# from a build environment the gate never sees, and is uploaded to the same audience
# as the source. It is a second, independent publication channel — and it has been
# contaminated in practice: a stray $KEN_SOURCE_URL in the build shell baked a
# private URL into the binary as its AGPL §13 "Source" link, with a clean tree.
#
# So this checks the ARTIFACTS themselves:
#   1. no private host string survives in any shipped file, including the compiled
#      binary's string table (where the module path and the injected URL live)
#   2. every staged doc/script is byte-identical to the same path at the tagged
#      commit — "built from a different tree" becomes structurally impossible
#   3. the bundled VERSION and the binary's own `ken version` agree with the tag,
#      which is the only way to catch a SILENTLY failed -ldflags injection
#
# Exit 0 = safe to publish.
set -uo pipefail

DIST="dist"; VERSION=""; REF=""
while [ $# -gt 0 ]; do
    case "$1" in
        --dist)    DIST="${2:?}"; shift 2 ;;
        --version) VERSION="${2:?}"; shift 2 ;;
        --ref)     REF="${2:?}"; shift 2 ;;
        *) echo "verify-artifact: unknown arg: $1" >&2; exit 2 ;;
    esac
done
[ -n "$VERSION" ] || { echo "verify-artifact: --version is required" >&2; exit 2; }
[ -n "$REF" ] || REF="v$VERSION"

REPO="$(git rev-parse --show-toplevel)"
fail=0
note() { echo "artifact: $*" >&2; fail=1; }
ok()   { echo "artifact: ok — $*"; }

work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT

# Files the bundle stages verbatim from the repo; each must match the tagged tree.
STAGED="docs/INSTALL.md docs/BACKUP.md configs/litestream.yml
        scripts/install.sh scripts/ken.sh scripts/ken-snapshot.sh
        deploy/ken.service deploy/ken-snapshot.service deploy/ken-snapshot.timer LICENSE"

for tgz in "$DIST"/ken-"$VERSION"-linux-*.tar.gz; do
    [ -e "$tgz" ] || { note "no bundles found for $VERSION in $DIST"; break; }
    arch="$(basename "$tgz" .tar.gz)"; arch="${arch##*-}"
    d="$work/$arch"; mkdir -p "$d"
    tar -xzf "$tgz" -C "$d" || { note "$tgz: cannot extract"; continue; }
    b="$d/ken-$VERSION"

    # --- 1. no private host survives anywhere in the bundle -------------------
    # `strings -a` the binary too: the module path and the injected SourceURL live
    # in its string table, which no text grep would ever see.
    hits="$(
        find "$b" -type f -print0 | while IFS= read -r -d '' f; do
            strings -a "$f" 2>/dev/null | grep -oE '[A-Za-z0-9_-]+\.quest\.mx' | sed "s|^|$(basename "$f"): |"
        done | grep -v ': ken\.quest\.mx$' | sort -u
    )"
    [ -n "$hits" ] && note "$arch: private host in bundle -> $(printf '%s' "$hits" | tr '\n' ' ')"

    # --- 2. staged files identical to the tagged tree -------------------------
    for rel in $STAGED; do
        [ -f "$b/$rel" ] || continue
        want="$(git -C "$REPO" show "$REF:$rel" 2>/dev/null | sha256sum | cut -d' ' -f1)"
        got="$(sha256sum "$b/$rel" | cut -d' ' -f1)"
        [ -n "$want" ] && [ "$want" != "$got" ] && note "$arch: $rel differs from $REF (bundle not built from the tagged tree)"
    done

    # --- 3. version identity: bundle VERSION, and the binary's own report -----
    [ -f "$b/VERSION" ] && [ "$(tr -d '[:space:]' <"$b/VERSION")" = "$VERSION" ] \
        || note "$arch: bundle VERSION does not match $VERSION"

    # Only the host-arch binary can be executed here; that is enough to catch a
    # failed -ldflags injection, which is arch-independent.
    host="$(uname -m)"; case "$host" in x86_64) host=amd64 ;; aarch64|arm64) host=arm64 ;; esac
    if [ "$arch" = "$host" ] && [ -x "$b/bin/ken" ]; then
        out="$("$b/bin/ken" version 2>&1)"
        printf '%s' "$out" | grep -q "Ken $VERSION " \
            || note "$arch: 'ken version' reports [$out], expected $VERSION (silent -ldflags failure?)"
        printf '%s' "$out" | grep -q 'source build' \
            && note "$arch: binary is marked a source build — the version ldflag did not apply"
        printf '%s' "$out" | grep -q 'source: https://github.com/Quest-ICT/ken' \
            || note "$arch: 'ken version' reports the wrong Source URL: [$out]"
    fi
    [ "$fail" -eq 0 ] && ok "$arch bundle"
done

# --- 4. checksums cover every shippable artifact and are correct -------------
if [ -f "$DIST/SHA256SUMS" ]; then
    ( cd "$DIST" && sha256sum -c SHA256SUMS >/dev/null 2>&1 ) || note "SHA256SUMS does not verify"
else
    note "SHA256SUMS missing"
fi

[ "$fail" -eq 0 ] && echo "artifact: all checks passed — safe to publish"
exit "$fail"
