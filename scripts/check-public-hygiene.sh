#!/bin/sh
#
# check-public-hygiene.sh — refuse to publish a tree that leaks private context.
#
# Run by CI on every push/PR, and by hand before a release.
#
# DESIGN NOTE — why this names nothing.
# The obvious implementation is a denylist of the hosts, users and networks that
# must not appear. That is exactly backwards for a PUBLIC repository: the denylist
# would itself be an inventory of the infrastructure it protects, published in the
# repo it protects. Worse, it only ever catches strings someone already thought of.
#
# So every rule below is a SHAPE rule: "any host under our domain except the one
# permitted project link", "any developer home path", "any file that is not UTF-8".
# It therefore catches hosts and users nobody has thought of yet, and it reveals
# nothing to a reader. A literal denylist lives in the private ops repo instead,
# where publishing it costs nothing.
#
# Exit 0 = clean, 1 = something must be fixed before publishing.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 1
fail=0
note() { echo "hygiene: $*" >&2; fail=1; }

# Concatenate every TRACKED file as "path:line" text. Binary files are read via
# `strings` rather than skipped: `git grep -I` silently SKIPS binaries, so a
# UTF-16 or otherwise-binary file would sail through unscanned.
scan() {
    git ls-files -z | xargs -0 -I{} sh -c '
        if LC_ALL=C grep -qI . "$1" 2>/dev/null; then
            sed "s|^|$1:|" "$1"
        else
            strings -a "$1" 2>/dev/null | sed "s|^|$1:|"
        fi' _ {}
}
SCAN="$(scan)"

# --- 1. Our own domain: one permitted project link, nothing else. -------------
# The promo site may be referenced; no other host under the domain may be.
offenders="$(printf '%s\n' "$SCAN" | grep -oE '[A-Za-z0-9_-]+\.quest\.mx' | sort -u | grep -v '^ken\.quest\.mx$')"
[ -n "$offenders" ] && note "non-permitted host(s) under our domain: $(printf '%s' "$offenders" | tr '\n' ' ')"

# --- 2. The permitted link is a PROJECT link, never an endpoint. --------------
# Pasting it as an MCP/API endpoint produces a config that fails for every reader
# and sends probe traffic at the promo host.
printf '%s\n' "$SCAN" | grep -qE 'ken\.quest\.mx/(mcp|api|healthz)' \
    && note "ken.quest.mx used as a service endpoint (it is a marketing site)"

# --- 3. No developer machine paths. ------------------------------------------
printf '%s\n' "$SCAN" | grep -qE '/(home|Users)/[a-z][a-z0-9_-]*/' \
    && note "developer home path present"

# --- 4. Module path is the public one, and the ldflags symbol is DERIVED. -----
# `go build` does NOT error on an unknown -X symbol, so a hardcoded module path in
# the release script fails silently and ships mislabelled binaries.
if command -v go >/dev/null 2>&1; then
    [ "$(go list -m 2>/dev/null)" = "github.com/Quest-ICT/ken" ] || note "module path is not the public one"
fi
grep -q 'go list -m' scripts/build-release.sh || note "build-release.sh must DERIVE the -X module path (go list -m)"

# --- 5. Every tracked file is UTF-8, except an explicit binary allowlist. -----
# Keeps rule 1 honest (see the scan() note) and stops stray binaries appearing.
BINARY_OK='^internal/web/static/(favicon-(32|180)\.png|favicon\.ico|ken-logo\.svg)$'
nonutf8="$(git ls-files | grep -vE "$BINARY_OK" | while read -r f; do
        iconv -f UTF-8 -t UTF-8 <"$f" >/dev/null 2>&1 || echo "$f"
    done)"
[ -n "$nonutf8" ] && note "unexpected binary/non-UTF-8 file(s): $(printf '%s' "$nonutf8" | tr '\n' ' ')"

# --- 6. Monitoring bundles: structural, because a re-export is how these leak. -
# A dashboard re-exported from a live Grafana carries that server's datasource
# UID and dashboard links; an alerts file re-copied from prod carries an
# instance selector naming the real host.
if [ -f monitoring/ken-grafana-dashboard.json ] && command -v jq >/dev/null 2>&1; then
    jq -e '[.. | objects | select(has("datasource")) | .datasource | select(type=="object") | .uid]
           | all(. == "${DS_PROMETHEUS}")' monitoring/ken-grafana-dashboard.json >/dev/null 2>&1 \
        || note "Grafana dashboard has a hardcoded datasource uid (re-export with 'Export for sharing externally')"
    jq -e '(.links // []) | length == 0' monitoring/ken-grafana-dashboard.json >/dev/null 2>&1 \
        || note "Grafana dashboard carries dashboard links (they encode the source server)"
fi
grep -rqE 'instance[!=]~?=' monitoring/ 2>/dev/null \
    && note "monitoring rule pins a literal instance selector"

# --- 7. The newest CHANGELOG release must have a matching tag. ---------------
# Not a leak rule. An untagged release breaks every /releases/download/vX.Y.Z/ URL,
# including the one scripts/ken-upgrade builds, so the failure is silent until an
# upgrade 404s.
#
# Two ways this rule can report a FALSE positive, both handled:
#   * a shallow clone has no tags to compare against -> run with fetch-depth 0
#   * a brand-new repository legitimately has no tags at all (the first push
#     happens before the first tag), so requiring one would fail CI on commit 1
newest="$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md 2>/dev/null | tr -d '#[] ')"
if [ -n "$newest" ] && [ -n "$(git tag 2>/dev/null | head -1)" ]; then
    git rev-parse -q --verify "refs/tags/v$newest" >/dev/null 2>&1 \
        || note "CHANGELOG's newest release $newest has no v$newest tag"
fi

[ "$fail" -eq 0 ] && echo "hygiene: clean"
exit "$fail"
