#!/usr/bin/env bash
#
# Assemble the single-file self-extracting installer  ken-<version>-linux-<arch>.bin
# from an already-staged bundle directory (produced by build-release.sh).
#
# The .bin is a copy of selfextract-stub.sh with the version substituted, the
# __KEN_ARCHIVE__ marker, then a gzip-compressed tar of the bundle directory
# appended. Only stock tools are required at build time (bash, tar, gzip, sed)
# and at install time (bash, tar, gzip, awk, tail) — no `makeself` dependency.
#
# Usage:
#   scripts/make-selfextract.sh \
#       --payload dist/stage/amd64/ken-0.1.0 \
#       --version 0.1.0 \
#       --stub    scripts/selfextract-stub.sh \
#       --out     dist/ken-0.1.0-linux-amd64.bin
#
# --payload must point at the bundle root directory, which MUST be named
# ken-<version> (the stub reconstructs that path after extraction).
#
set -euo pipefail

PAYLOAD=""
VERSION=""
STUB=""
OUT=""

while [ $# -gt 0 ]; do
    case "$1" in
        --payload) PAYLOAD="${2:?}"; shift 2 ;;
        --version) VERSION="${2:?}"; shift 2 ;;
        --stub)    STUB="${2:?}";    shift 2 ;;
        --out)     OUT="${2:?}";     shift 2 ;;
        *) echo "make-selfextract: unknown arg: $1" >&2; exit 2 ;;
    esac
done

[ -n "$PAYLOAD" ] || { echo "make-selfextract: --payload is required" >&2; exit 2; }
[ -n "$VERSION" ] || { echo "make-selfextract: --version is required" >&2; exit 2; }
[ -n "$STUB" ]    || { echo "make-selfextract: --stub is required"    >&2; exit 2; }
[ -n "$OUT" ]     || { echo "make-selfextract: --out is required"     >&2; exit 2; }
[ -d "$PAYLOAD" ] || { echo "make-selfextract: payload dir not found: $PAYLOAD" >&2; exit 1; }
[ -f "$STUB" ]    || { echo "make-selfextract: stub not found: $STUB" >&2; exit 1; }

PAYLOAD="$(readlink -f "$PAYLOAD")"
parent="$(dirname "$PAYLOAD")"
base="$(basename "$PAYLOAD")"
[ "$base" = "ken-${VERSION}" ] || {
    echo "make-selfextract: payload dir must be named ken-${VERSION} (got: $base)" >&2
    exit 1
}

# Make sure the launchers inside the payload are executable.
chmod 0755 "$PAYLOAD/scripts/install.sh" \
           "$PAYLOAD/scripts/ken.sh" \
           "$PAYLOAD/scripts/ken-snapshot.sh" 2>/dev/null || true

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "[make-selfextract] building payload from $PAYLOAD"
payload_tgz="$work/payload.tar.gz"
# Deterministic-ish tar (sorted, numeric owners) for reproducible-ish builds.
tar --numeric-owner --owner=0 --group=0 -czf "$payload_tgz" -C "$parent" "$base"

mkdir -p "$(dirname "$OUT")"
echo "[make-selfextract] writing $OUT"
sed "s/@VERSION@/${VERSION}/g" "$STUB" > "$OUT"
cat "$payload_tgz" >> "$OUT"
chmod 0755 "$OUT"

bytes="$(wc -c < "$OUT")"
echo "[make-selfextract] done: $OUT ($(( bytes / 1024 / 1024 )) MB, ${bytes} bytes)"
