#!/usr/bin/env bash
#
# Ken @VERSION@ — self-extracting Linux installer.
#
# This file is a shell script with a gzip-compressed tar archive appended after
# the __KEN_ARCHIVE__ marker line. Running it unpacks the bundle to a temporary
# directory and launches scripts/install.sh; any arguments pass straight through
# to the installer.
#
#   sudo ./ken-@VERSION@-linux-amd64.bin              # install / upgrade (see --help)
#   sudo ./ken-@VERSION@-linux-amd64.bin --port 443 --open-firewall
#   ./ken-@VERSION@-linux-amd64.bin --extract DIR     # just unpack the bundle into DIR
#   ./ken-@VERSION@-linux-amd64.bin --help            # installer help
#
# Only stock tools are needed to run it: bash, tar, gzip, awk, tail.
#
set -euo pipefail

KEN_VERSION="@VERSION@"
SELF="$(readlink -f "$0")"

# First line of the marker; the gzip payload starts on the NEXT line.
archive_start() {
    awk '/^__KEN_ARCHIVE__$/ { print NR + 1; exit 0 }' "$SELF"
}

extract_to() {
    local dest="$1" start
    start="$(archive_start)"
    [ -n "$start" ] || { echo "Ken installer: corrupt archive (no marker)" >&2; exit 1; }
    mkdir -p "$dest"
    tail -n +"$start" "$SELF" | tar xzf - -C "$dest"
}

# --extract DIR: unpack only (no install). Handy for inspection or staging.
if [ "${1:-}" = "--extract" ]; then
    DEST="${2:?--extract needs a target directory}"
    extract_to "$DEST"
    echo "Ken ${KEN_VERSION} bundle extracted to: ${DEST}/ken-${KEN_VERSION}"
    echo "Install with: sudo ${DEST}/ken-${KEN_VERSION}/scripts/install.sh [options]"
    exit 0
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ken-install.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

extract_to "$WORK"
INSTALL="$WORK/ken-${KEN_VERSION}/scripts/install.sh"
[ -f "$INSTALL" ] || { echo "Ken installer: install.sh missing from bundle" >&2; exit 1; }
chmod +x "$INSTALL"
bash "$INSTALL" "$@"
exit $?

# Everything below the next line is the gzip-compressed tar payload.
__KEN_ARCHIVE__
