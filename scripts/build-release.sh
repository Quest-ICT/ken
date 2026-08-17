#!/usr/bin/env bash
#
# build-release.sh — cross-compile ken for linux/amd64 + linux/arm64, stage each
# into a distribution bundle, and emit both a plain dist tarball and a single-file
# self-extracting .bin installer per architecture, under dist/.
#
# Ken is pure Go (its SQLite is a WASM build via ncruces/go-sqlite3), so
# CGO_ENABLED=0 cross-compilation needs no C toolchain and no target libraries.
#
# Usage:
#   scripts/build-release.sh [--version V] [--arch "amd64 arm64"] [--out dist]
#
# Version resolution (first hit wins):
#   --version flag  >  $KEN_VERSION  >  ./VERSION file  >  git describe --tags
#   --always  >  0.1.0-dev
#
# Requires: the Go toolchain on PATH (or $KEN_GO_BIN pointing at its bin/), tar, gzip.
#
set -euo pipefail

SELF="$(readlink -f "$0")"
SCRIPTS_DIR="$(dirname "$SELF")"
REPO="$(dirname "$SCRIPTS_DIR")"

# --- Go toolchain: prefer PATH, else $KEN_GO_BIN (a toolchain not on PATH). --
if ! command -v go >/dev/null 2>&1; then
    if [ -n "${KEN_GO_BIN:-}" ] && [ -x "${KEN_GO_BIN}/go" ]; then
        export PATH="${KEN_GO_BIN}:$PATH"
    else
        echo "build-release: 'go' not found on PATH (set \$KEN_GO_BIN to a Go bin/ directory)" >&2
        exit 1
    fi
fi

# Build hygiene: never auto-download a different toolchain, and no cgo (so the
# cross-compile needs no C toolchain and no target libraries).
export CGO_ENABLED=0
export GOTOOLCHAIN=local

# --- Defaults / args --------------------------------------------------------
VERSION=""
ARCHES="amd64 arm64"
OUT="$REPO/dist"

while [ $# -gt 0 ]; do
    case "$1" in
        --version) VERSION="${2:?}"; shift 2 ;;
        --arch)    ARCHES="${2:?}"; shift 2 ;;
        --out)     OUT="${2:?}"; shift 2 ;;
        -h|--help)
            grep -E '^#( |$)' "$SELF" | sed 's/^# \{0,1\}//'
            exit 0 ;;
        *) echo "build-release: unknown arg: $1" >&2; exit 2 ;;
    esac
done

# --- Resolve version --------------------------------------------------------
resolve_version() {
    if [ -n "$VERSION" ]; then printf '%s' "$VERSION"; return; fi
    if [ -n "${KEN_VERSION:-}" ]; then printf '%s' "$KEN_VERSION"; return; fi
    if [ -f "$REPO/VERSION" ]; then
        local v; v="$(head -n1 "$REPO/VERSION" | tr -d '[:space:]')"
        [ -n "$v" ] && { printf '%s' "$v"; return; }
    fi
    if command -v git >/dev/null 2>&1 && git -C "$REPO" rev-parse --git-dir >/dev/null 2>&1; then
        local v; v="$(git -C "$REPO" describe --tags --always 2>/dev/null || true)"
        [ -n "$v" ] && { printf '%s' "$v"; return; }
    fi
    printf '%s' "0.1.0-dev"
}
VERSION="$(resolve_version)"
# Strip a leading v so filenames read ken-1.2.3 not ken-v1.2.3.
VERSION="${VERSION#v}"

echo "[build-release] Ken ${VERSION}  (go: $(go version | awk '{print $3}'))"

STUB="$SCRIPTS_DIR/selfextract-stub.sh"
MAKEBIN="$SCRIPTS_DIR/make-selfextract.sh"
[ -f "$STUB" ]    || { echo "build-release: missing $STUB" >&2; exit 1; }
[ -f "$MAKEBIN" ] || { echo "build-release: missing $MAKEBIN" >&2; exit 1; }

rm -rf "$OUT"
mkdir -p "$OUT"
STAGE_ROOT="$OUT/stage"

# Source URL baked into the binary for the AGPL §13 in-app "Source" link. Defaults to
# the public repository (also the value in internal/version/version.go); override with
# $KEN_SOURCE_URL if the source ever moves.
SOURCE_URL="${KEN_SOURCE_URL:-https://github.com/Quest-ICT/ken}"

# Guard: a stray $KEN_SOURCE_URL in the build shell silently bakes the WRONG AGPL §13
# "Source" link into every published binary — the artifact is a publication channel of
# its own, and nothing downstream re-checks it. Refuse anything but the public repo
# unless the operator deliberately opts out (private/internal builds).
if [ "$SOURCE_URL" != "https://github.com/Quest-ICT/ken" ] && [ "${KEN_ALLOW_ALT_SOURCE:-}" != "1" ]; then
    echo "build-release: refusing KEN_SOURCE_URL='$SOURCE_URL' — the AGPL §13 link in a" >&2
    echo "               published binary must be the public repository." >&2
    echo "               Set KEN_ALLOW_ALT_SOURCE=1 for a deliberate internal build." >&2
    exit 1
fi

# The -X symbol path is DERIVED from the module path, never hardcoded: `go build` does
# NOT error on an unknown -X symbol, so a stale literal here fails SILENTLY and ships
# binaries reporting the compiled-in default version with the wrong Source link.
MODPATH="$(cd "$REPO" && go list -m)"
# NOTE: the source-URL symbol is the UNEXPORTED `sourceURL` (version.SourceURL() is
# the accessor that layers KEN_SOURCE_URL on top). If either name changes, update it
# here in the same commit — an unknown -X symbol is silently ignored by `go build`.
LDFLAGS="-s -w -X ${MODPATH}/internal/version.Version=${VERSION} -X ${MODPATH}/internal/version.sourceURL=${SOURCE_URL}"
echo "[build-release] module     ${MODPATH}"
echo "[build-release] source-url ${SOURCE_URL}"

# gen_notices writes a THIRD-PARTY-NOTICES file reproducing the license of every Go
# module compiled into the binary (arch-independent, so generated once). Reads license
# files straight from the module cache — no external tool required.
gen_notices() {
    {
        echo "THIRD-PARTY NOTICES"
        echo
        echo "Ken (AGPL-3.0-only; see LICENSE) is compiled with the Go modules below,"
        echo "each distributed under its own license, reproduced here."
        echo
        ( cd "$REPO" && go list -deps -f '{{with .Module}}{{.Path}}@{{.Version}}|{{.Dir}}{{end}}' ./cmd/ken ) \
            | sort -u | while IFS='|' read -r modver dir; do
            [ -n "$modver" ] && [ -n "$dir" ] || continue
            lic="$(ls "$dir"/LICENSE* "$dir"/COPYING* "$dir"/License* 2>/dev/null | head -n1 || true)"
            echo "==================================================================="
            echo "$modver"
            echo "==================================================================="
            if [ -n "$lic" ] && [ -r "$lic" ]; then cat "$lic"; else echo "(license file not found in module cache; see the module source)"; fi
            [ -r "$dir/NOTICE" ] && { echo; echo "-- NOTICE --"; cat "$dir/NOTICE"; }
            echo
        done
    } > "$1"
}
mkdir -p "$STAGE_ROOT"
NOTICES="$STAGE_ROOT/THIRD-PARTY-NOTICES"
echo "[build-release] generating $NOTICES"
gen_notices "$NOTICES"

for arch in $ARCHES; do
    echo "[build-release] === linux/${arch} ==="
    bundle="ken-${VERSION}"
    stage="$STAGE_ROOT/$arch/$bundle"
    rm -rf "$STAGE_ROOT/$arch"
    mkdir -p "$stage/bin" "$stage/scripts" "$stage/deploy" "$stage/docs" "$stage/configs"

    echo "[build-release] compiling bin/ken"
    ( cd "$REPO" && GOOS=linux GOARCH="$arch" \
        go build -trimpath -ldflags "$LDFLAGS" -o "$stage/bin/ken" ./cmd/ken )

    # Runtime + installer scripts (NOT the packaging scripts).
    install -m 0755 "$SCRIPTS_DIR/install.sh"          "$stage/scripts/install.sh"
    install -m 0755 "$SCRIPTS_DIR/ken.sh"              "$stage/scripts/ken.sh"
    install -m 0755 "$SCRIPTS_DIR/ken-snapshot.sh"     "$stage/scripts/ken-snapshot.sh"
    # Sourced by ken-snapshot.sh AND install.sh (shared naming/securing policy); not
    # executed, so 0644. Both the nightly and the pre-upgrade snapshot break without it.
    install -m 0644 "$SCRIPTS_DIR/ken-snapshot-lib.sh" "$stage/scripts/ken-snapshot-lib.sh"

    # systemd unit templates.
    install -m 0644 "$REPO/deploy/ken.service"          "$stage/deploy/ken.service"
    install -m 0644 "$REPO/deploy/ken-snapshot.service" "$stage/deploy/ken-snapshot.service"
    install -m 0644 "$REPO/deploy/ken-snapshot.timer"   "$stage/deploy/ken-snapshot.timer"

    # Operator docs + optional Litestream template.
    [ -f "$REPO/docs/INSTALL.md" ] && install -m 0644 "$REPO/docs/INSTALL.md" "$stage/docs/INSTALL.md"
    [ -f "$REPO/docs/BACKUP.md" ]  && install -m 0644 "$REPO/docs/BACKUP.md"  "$stage/docs/BACKUP.md"
    # OPERATION.md is the manual for the person running this, written BY a production operator.
    # It shipped nowhere until 3.10.0: an operator without a git checkout could not read the one
    # document addressed to them. FINISHING.md travels with it for the same reason in miniature —
    # its stated purpose is that a human can open it and know where things stand.
    [ -f "$REPO/docs/OPERATION.md" ] && install -m 0644 "$REPO/docs/OPERATION.md" "$stage/docs/OPERATION.md"
    [ -f "$REPO/docs/FINISHING.md" ] && install -m 0644 "$REPO/docs/FINISHING.md" "$stage/docs/FINISHING.md"
    [ -f "$REPO/configs/litestream.yml" ] && install -m 0640 "$REPO/configs/litestream.yml" "$stage/configs/litestream.yml"

    # License + third-party notices (required for AGPL distribution).
    install -m 0644 "$REPO/LICENSE" "$stage/LICENSE"
    install -m 0644 "$NOTICES"      "$stage/THIRD-PARTY-NOTICES"

    printf '%s\n' "$VERSION" > "$stage/VERSION"

    # Plain dist tarball.
    tarball="$OUT/ken-${VERSION}-linux-${arch}.tar.gz"
    echo "[build-release] packing $tarball"
    tar --numeric-owner --owner=0 --group=0 -czf "$tarball" -C "$STAGE_ROOT/$arch" "$bundle"

    # Self-extracting .bin installer.
    binout="$OUT/ken-${VERSION}-linux-${arch}.bin"
    "$MAKEBIN" --payload "$stage" --version "$VERSION" --stub "$STUB" --out "$binout"
done

# Stable-named alias copies, so a permanent "latest" download link exists: GitHub
# resolves <repo>/releases/latest/download/<name> to the newest (non-prerelease)
# release's asset, but only when the asset NAME is stable across releases (the
# versioned names are not) — so upload BOTH the versioned and the alias names to
# each release. These aliases are byte-identical to their versioned originals.
for arch in $ARCHES; do
    cp -f "$OUT/ken-${VERSION}-linux-${arch}.bin"    "$OUT/ken-latest-linux-${arch}.bin"
    cp -f "$OUT/ken-${VERSION}-linux-${arch}.tar.gz" "$OUT/ken-latest-linux-${arch}.tar.gz"
done

# Checksums over every shippable artifact (versioned + aliases). Aliases share the
# hash of their original, and SHA256SUMS itself has a stable name (latest link).
( cd "$OUT" && sha256sum \
    ken-"${VERSION}"-linux-*.bin ken-"${VERSION}"-linux-*.tar.gz \
    ken-latest-linux-*.bin ken-latest-linux-*.tar.gz > SHA256SUMS )

# Tidy: drop the staging tree; keep only the shippable artifacts.
rm -rf "$STAGE_ROOT"

echo
echo "[build-release] artifacts in $OUT:"
( cd "$OUT" && ls -lh ken-* SHA256SUMS | awk '{printf "  %-42s %s\n", $9, $5}' )
echo "[build-release] done."
