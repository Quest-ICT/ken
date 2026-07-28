#!/usr/bin/env bash
#
# Ken — Linux installer / upgrader.
#
# Ships inside the distribution bundle (ken-<version>/scripts/install.sh) and is
# the entry point the self-extracting ken-<version>-linux-<arch>.bin runs after
# it unpacks. You can also run it by hand after unpacking the dist tarball:
#
#     sudo ./ken-<version>/scripts/install.sh [options]
#
# What it does (idempotent — safe to re-run / use for upgrades):
#   * creates the `ken` system user + group if missing
#   * lays the immutable code under  <prefix>/releases/<version>/  (root-owned)
#   * keeps editable state in version-independent  <prefix>/{data,logs,backups}/
#     symlinked into the versioned dir, so an upgrade NEVER touches the database,
#     logs or snapshots
#   * installs + adjusts ken.service, ken-snapshot.service and ken-snapshot.timer
#   * flips the  <prefix>/current -> releases/<version>  symlink to the new version
#   * fixes ownership / permissions and restores SELinux contexts
#   * (re)starts + enables the service, and enables the nightly snapshot timer
#
# Ken keeps ALL of its state in one SQLite file (<prefix>/data/ken.db) — there is
# no external database to provision. It does NOT create an admin account: the
# first curator login and agent (MCP) tokens are created afterwards (see the
# printed next-steps).
#
# Layout it produces:
#
#   <prefix>/                          (default /opt/ken)
#     releases/<version>/              immutable code (root-owned)
#       bin/ken                        the single static binary
#       scripts/ken.sh                 launcher (ExecStart -f)
#       scripts/ken-snapshot.sh        nightly backup
#       deploy/*.service *.timer       unit templates
#       data    -> ../../data          symlink into shared state
#       logs    -> ../../logs
#       backups -> ../../backups
#     current -> releases/<version>    "current" symlink (KEN_HOME)
#     data/    ken.db + dedup.key      editable state (survives upgrades)
#     logs/    ken.out
#     backups/ nightly snapshots
#
set -euo pipefail

# ----------------------------------------------------------------------------
# Defaults (override with flags).
# ----------------------------------------------------------------------------
PREFIX="/opt/ken"
APP="ken"
# Where snapshots land, and who may read them. Both empty = the shipped behaviour:
# <prefix>/backups, files 0600 (owner + root only). On an UPGRADE both are re-discovered
# from the installed unit when not given, so re-running the installer never relocates an
# existing archive or silently drops a configured group.
BACKUP_DIR=""
BACKUP_GROUP=""
NO_BACKUP_GROUP="no"    # --no-backup-group: revoke a previously configured group
SVC_USER="ken"
SVC_GROUP="ken"
PORT=""                 # empty = keep the unit's KEN_ADDR (:8080; forced to :443 when TLS on)
# In-process TLS. TLS_MODE="" leaves the unit as-is (plain HTTP, for behind-a-proxy).
# acme = Let's Encrypt (needs TLS_DOMAINS); file = operator cert (needs TLS_CERT+TLS_KEY).
TLS_MODE=""
TLS_DOMAINS=""
TLS_EMAIL=""
TLS_CERT=""
TLS_KEY=""
DO_START="yes"
DO_ENABLE="yes"
# Open the app port(s) in the host firewall (firewalld on RHEL-family, an
# already-active ufw on Debian-family). Off by default — opening a port is a
# security decision the operator opts into. FIREWALL_PORTS overrides the
# auto-computed list; NO_FIREWALL is the explicit "never touch the firewall"
# override (wins over the two above and skips the wizard prompt).
OPEN_FIREWALL="no"
FIREWALL_PORTS=""
NO_FIREWALL="no"
DRY_RUN="no"
# auto = wizard when stdin is a TTY; yes = force; no = never (flags + defaults).
INTERACTIVE="auto"

log()  { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARNING: %s\n' "$*" >&2; }
die()  { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

# Run a privileged/side-effecting command, or just echo it under --dry-run.
run() {
    if [ "$DRY_RUN" = "yes" ]; then
        printf '[dry-run] %s\n' "$*"
    else
        "$@"
    fi
}

# --- Interactive prompt helpers -------------------------------------------
prompt_value() {
    local __var="$1" __prompt="$2" __def="${3:-}" __reply
    if [ -n "$__def" ]; then
        read -r -p "  $__prompt [$__def]: " __reply || true
        [ -z "$__reply" ] && __reply="$__def"
    else
        read -r -p "  $__prompt: " __reply || true
    fi
    printf -v "$__var" '%s' "$__reply"
}
prompt_yesno() {
    local __var="$1" __prompt="$2" __def="$3" __reply __hint="[y/N]"
    [ "$__def" = "yes" ] && __hint="[Y/n]"
    read -r -p "  $__prompt $__hint: " __reply || true
    if   [ -z "$__reply" ]; then __reply="$__def"
    elif [[ "$__reply" =~ ^[Yy] ]]; then __reply="yes"
    else __reply="no"
    fi
    printf -v "$__var" '%s' "$__reply"
}

# --- Firewall helpers ------------------------------------------------------
# Print (one per line, de-duplicated) the TCP ports this install exposes. An
# explicit --firewall-ports list wins; otherwise it is the app's listen port
# (--port, else the 8080 unit default) plus port 80 when the app is put on 443
# (needed for the in-process ACME HTTP-01 challenge + an HTTP->HTTPS redirect).
firewall_target_ports() {
    if [ -n "$FIREWALL_PORTS" ]; then
        printf '%s\n' "$FIREWALL_PORTS" | tr ',; ' '\n' | grep -E '^[0-9]+$'
    else
        local p="${PORT:-8080}"
        printf '%s\n' "$p"
        # :80 = ACME HTTP-01 challenge + HTTP->HTTPS redirect — needed in ANY TLS
        # mode (the :80 listener is on whenever TLS is on), and also when the app
        # sits on 443 without in-process TLS (a proxy in front).
        if { [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ]; } || [ "$p" = "443" ]; then
            printf '%s\n' 80
        fi
    fi | awk '!seen[$0]++'
}

# On a re-run/upgrade, seed TLS + listen port from the currently-installed unit so
# an unattended upgrade never silently downgrades a live HTTPS site to plain HTTP.
# Only fills values the operator did NOT pass on the command line.
INSTALLED_UNIT="${KEN_INSTALLED_UNIT:-/etc/systemd/system/ken.service}"
seed_var() {  # seed_var VARNAME KEY — set VARNAME from Environment=KEY= if VARNAME is empty
    local __var="$1" __key="$2" __cur __v
    eval "__cur=\${$__var}"
    [ -z "$__cur" ] || return 0
    __v="$(sed -n "s/^Environment=$__key=\\(.*\\)\$/\\1/p" "$INSTALLED_UNIT" | head -n1)"
    [ -n "$__v" ] && printf -v "$__var" '%s' "$__v"
    return 0
}
seed_from_installed_unit() {
    [ -r "$INSTALLED_UNIT" ] || return 0
    seed_var TLS_MODE    KEN_TLS
    seed_var TLS_DOMAINS KEN_TLS_DOMAINS
    seed_var TLS_EMAIL   KEN_TLS_EMAIL
    seed_var TLS_CERT    KEN_TLS_CERT
    seed_var TLS_KEY     KEN_TLS_KEY
    if [ -z "$PORT" ]; then
        local __p; __p="$(sed -n 's/^Environment=KEN_ADDR=:\([0-9]\{1,\}\)$/\1/p' "$INSTALLED_UNIT" | head -n1)"
        [ -n "$__p" ] && PORT="$__p" || true
    fi
    if [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ]; then
        log "preserving TLS from the installed unit (KEN_TLS=$TLS_MODE); pass --tls off to disable"
    fi
    return 0
}

# Open the given space-separated TCP ports in whatever host firewall is actually
# managing this box. firewalld and an already-active ufw are driven directly;
# everything else is left untouched (we never enable ufw — that can drop the SSH
# session — nor poke raw nftables/iptables) with a printed manual hint. Always
# best-effort: a firewall failure warns, never aborts the install.
open_firewall() {
    local ports="$1" p
    [ -n "$ports" ] || { warn "no firewall ports to open (empty list)"; return 0; }

    # firewalld — RHEL / Rocky / Fedora / Alma. `--state` is read-only.
    if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        log "opening firewalld ports (default zone): $ports"
        for p in $ports; do
            run firewall-cmd --permanent --add-port="${p}/tcp" || warn "firewalld: could not add ${p}/tcp"
        done
        run firewall-cmd --reload || warn "firewalld: reload failed"
        return 0
    fi

    # ufw — Debian / Ubuntu. Only when ALREADY active; enabling it ourselves
    # could lock out the current SSH session.
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qiE '^Status:[[:space:]]*active'; then
        log "opening ufw ports: $ports"
        for p in $ports; do
            run ufw allow "${p}/tcp" || warn "ufw: could not allow ${p}/tcp"
        done
        return 0
    fi

    # No firewall we will drive automatically — explain and hand over the exact
    # commands.
    if command -v ufw >/dev/null 2>&1; then
        warn "ufw is installed but inactive — not enabling it (could drop your SSH session)."
        for p in $ports; do warn "  to open manually: ufw allow ${p}/tcp"; done
    elif command -v nft >/dev/null 2>&1 || command -v iptables >/dev/null 2>&1; then
        warn "no firewalld/ufw manager active — leaving raw nftables/iptables untouched."
        warn "  open these TCP ports manually: $ports"
    else
        log "no active host firewall detected — ports ($ports) should already be reachable."
    fi
    return 0
}

# Collect every install parameter interactively, then review + confirm.
run_wizard() {
    echo
    log "Interactive setup — press Enter to accept each [default]."
    prompt_value PREFIX   "Install prefix" "$PREFIX"
    prompt_value SVC_USER "Service user"   "$SVC_USER"; SVC_GROUP="$SVC_USER"
    recompute_paths

    prompt_value TLS_MODE "HTTPS mode — acme (Let's Encrypt, recommended), file (your cert), or off (plain HTTP, only behind a TLS proxy)" "${TLS_MODE:-acme}"
    case "$TLS_MODE" in
        acme)
            prompt_value TLS_DOMAINS "  Public hostname(s) for the certificate (comma-separated)" "$TLS_DOMAINS"
            prompt_value TLS_EMAIL   "  Let's Encrypt account email (recommended)" "$TLS_EMAIL"
            ;;
        file)
            prompt_value TLS_CERT "  PEM certificate (fullchain) path" "$TLS_CERT"
            prompt_value TLS_KEY  "  PEM private key path" "$TLS_KEY"
            ;;
    esac

    local _port="${PORT:-keep}"
    { [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ] && [ -z "$PORT" ]; } && _port="443"
    prompt_value PORT "Listen port — sets KEN_ADDR ('keep' leaves the :8080 default)" "$_port"
    [ "$PORT" = "keep" ] && PORT=""

    # ACME needs :80 + :443 reachable, so default the firewall prompt to yes there.
    { [ "$TLS_MODE" = "acme" ] && [ "$OPEN_FIREWALL" = "no" ]; } && OPEN_FIREWALL="yes"

    if [ "$NO_FIREWALL" != "yes" ]; then
        prompt_yesno OPEN_FIREWALL "Open the app port(s) in the host firewall — $(firewall_target_ports | paste -sd, -)/tcp" "$OPEN_FIREWALL"
    fi

    prompt_yesno DO_START  "Start ken now"      "$DO_START"
    prompt_yesno DO_ENABLE "Enable ken at boot" "$DO_ENABLE"

    echo
    log "Review:"
    log "  install root : $DEST"
    log "  data dir     : $DATA"
    log "  current link : $LINK"
    log "  service user : $SVC_USER"
    log "  listen port  : ${PORT:-<unchanged, :8080>}"
    log "  TLS          : $(tls_summary)"
    if [ "$OPEN_FIREWALL" = "yes" ]; then log "  firewall     : open $(firewall_target_ports | paste -sd, -)/tcp"; else log "  firewall     : leave unchanged"; fi
    log "  start/enable : $DO_START / $DO_ENABLE"
    [ "$DRY_RUN" = "yes" ] && log "  (dry run — no changes will be made)"
    echo
    local _go
    prompt_yesno _go "Proceed" "yes"
    [ "$_go" = "yes" ] || die "aborted by user"
}

usage() {
    cat <<EOF
Ken installer — run as root.

Usage: sudo ./scripts/install.sh [options]

Options:
  --prefix DIR          Install root (default: ${PREFIX}). Produces
                        DIR/releases/<version>, DIR/current and DIR/{data,logs,backups}.
  --user NAME           Service user/group to own state (default: ${SVC_USER}).
  --port N              Listen port — sets KEN_ADDR=:N in ken.service (e.g. 8080, 443).
                        Defaults to 443 when TLS is enabled.
  --tls MODE            In-process TLS: acme (Let's Encrypt, auto-renew), file
                        (your own cert), or off (plain HTTP — valid ONLY behind a
                        TLS-terminating reverse proxy).
  --domain LIST         acme: comma-separated public hostname(s) for the certificate.
  --email ADDR          acme: Let's Encrypt account email (recommended).
  --cert PATH           file: PEM certificate (fullchain) path.
  --key PATH            file: PEM private key path.
  --open-firewall       Open the app port(s) in the host firewall (firewalld on
                        RHEL-family, an already-active ufw on Debian-family).
                        Opens --port (else 8080), plus 80 when --port is 443.
                        Best-effort; never enables ufw.
  --firewall-ports LIST Comma-separated TCP ports to open instead of the
                        auto-computed set (implies --open-firewall), e.g.
                        --firewall-ports 443,80.
  --backup-dir DIR      Where snapshots live (default: <prefix>/backups). Honoured by
                        BOTH the nightly timer and the pre-upgrade snapshot, and
                        remembered across upgrades.
  --backup-group GRP    Let members of GRP READ snapshots (dir setgid, files 0640
                        instead of 0600) so an unprivileged account can pull backups
                        off-box without root. The group must already exist.
  --no-backup-group     Revoke a previously configured --backup-group: snapshots go
                        back to 0600 (owner only).
  --no-firewall         Never touch the host firewall (also skips the wizard
                        prompt). Overrides --open-firewall / --firewall-ports
                        regardless of order — use when testing on a machine
                        whose firewall must stay exactly as-is.
  --no-start            Install but do not start the service.
  --no-enable           Do not enable the service (and snapshot timer) at boot.
  --interactive         Force the setup wizard (prompt for each value).
  -y, --yes,
  --non-interactive     Never prompt; use flags + defaults (for automation).
  --dry-run             Print every privileged action instead of doing it
                        (works without root — great for previewing).
  -h, --help            This help.

By default the installer runs the wizard when stdin is a terminal, and runs
unattended (flags + defaults) otherwise. Any value passed as a flag becomes the
wizard's pre-filled default, so flags and prompts compose.

Examples:
  sudo ./scripts/install.sh                 # wizard on a terminal
  sudo ./scripts/install.sh -y              # unattended, all defaults
  sudo ./scripts/install.sh --port 8080 --open-firewall -y
  sudo ./scripts/install.sh --tls acme --domain kb.example.com --email you@example.com --open-firewall -y
EOF
}

# ----------------------------------------------------------------------------
# Parse arguments.
# ----------------------------------------------------------------------------
while [ $# -gt 0 ]; do
    case "$1" in
        --prefix)          PREFIX="${2:?--prefix needs a value}"; shift 2 ;;
        --user)            SVC_USER="${2:?--user needs a value}"; SVC_GROUP="$SVC_USER"; shift 2 ;;
        --port)            PORT="${2:?--port needs a value}"; shift 2 ;;
        --tls)             TLS_MODE="${2:?--tls needs a value (acme|file|off)}"; shift 2 ;;
        --domain)          TLS_DOMAINS="${2:?--domain needs a value}"; shift 2 ;;
        --email)           TLS_EMAIL="${2:?--email needs a value}"; shift 2 ;;
        --cert)            TLS_CERT="${2:?--cert needs a value}"; shift 2 ;;
        --key)             TLS_KEY="${2:?--key needs a value}"; shift 2 ;;
        --open-firewall)   OPEN_FIREWALL="yes"; shift ;;
        --firewall-ports)  FIREWALL_PORTS="${2:?--firewall-ports needs a value}"; OPEN_FIREWALL="yes"; shift 2 ;;
        --backup-dir)      BACKUP_DIR="${2:?--backup-dir needs a value}"; shift 2 ;;
        --backup-group)    BACKUP_GROUP="${2:?--backup-group needs a value}"; NO_BACKUP_GROUP="no"; shift 2 ;;
        --no-backup-group) NO_BACKUP_GROUP="yes"; BACKUP_GROUP=""; shift ;;
        --no-firewall)     NO_FIREWALL="yes"; shift ;;
        --no-start)        DO_START="no"; shift ;;
        --no-enable)       DO_ENABLE="no"; shift ;;
        --interactive)     INTERACTIVE="yes"; shift ;;
        -y|--yes|--non-interactive) INTERACTIVE="no"; shift ;;
        --dry-run)         DRY_RUN="yes"; shift ;;
        -h|--help)         usage; exit 0 ;;
        *)                 die "unknown option: $1 (see --help)" ;;
    esac
done

# --no-firewall is an explicit safety override: it wins over --open-firewall /
# --firewall-ports regardless of argument order.
if [ "$NO_FIREWALL" = "yes" ]; then OPEN_FIREWALL="no"; FIREWALL_PORTS=""; fi

# On a re-run/upgrade, inherit the installed unit's TLS + port unless overridden.
seed_from_installed_unit

# In-process TLS defaults to the standard HTTPS port when no port was chosen.
if [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ] && [ -z "$PORT" ]; then PORT="443"; fi

# One-line human summary of the TLS choice (used by the wizard review + summary).
tls_summary() {
    case "$TLS_MODE" in
        acme) printf 'in-process ACME (Let'\''s Encrypt) for %s' "$TLS_DOMAINS" ;;
        file) printf 'in-process (cert %s)' "$TLS_CERT" ;;
        *)    printf 'off — plain HTTP (valid ONLY behind a TLS-terminating proxy)' ;;
    esac
}

# ----------------------------------------------------------------------------
# Locate the bundle (this script lives in <bundle>/scripts/) and the version.
# ----------------------------------------------------------------------------
SELF="$(readlink -f "$0")"
SCRIPTS_DIR="$(dirname "$SELF")"
BUNDLE="$(dirname "$SCRIPTS_DIR")"

[ -x "$BUNDLE/bin/ken" ] || die "no ken binary at $BUNDLE/bin/ken — is this a complete bundle?"

# The pre-upgrade snapshot (§5b) uses the SAME naming + securing policy as the
# nightly snapshot — one shared library, so the two can never drift on timezone,
# mode, or encryption. It ships in every bundle alongside ken-snapshot.sh.
[ -f "$SCRIPTS_DIR/ken-snapshot-lib.sh" ] || die "missing $SCRIPTS_DIR/ken-snapshot-lib.sh — is this a complete bundle?"
# shellcheck source=ken-snapshot-lib.sh
. "$SCRIPTS_DIR/ken-snapshot-lib.sh"

# _ken_unit_env <VAR> — the effective value of VAR the operator configured for
# the NIGHTLY snapshots, so the pre-upgrade snapshot honors the same intent ("encrypt
# my backups") instead of dropping a plaintext DB next to encrypted ones. Read from
# the just-installed unit's merged environment (main unit + any `systemctl edit`
# drop-in), with a direct file grep as a fallback. Empty = the operator has not opted
# into encryption; the snapshot is then left plaintext 0600, exactly as a nightly is.
_ken_unit_env() {
    if command -v systemctl >/dev/null 2>&1; then
        # AUTHORITATIVE when systemd is present (which it is on any install target).
        # `systemctl show -p Environment` gives the unit's MERGED environment — main unit
        # plus any `systemctl edit` drop-in — and never surfaces #-commented lines. Its
        # answer stands even when EMPTY (the common default): we must NOT fall through to
        # reading unit files then, because the bundled template ships a commented example
        # that a file read could harvest as a bogus value (which would make the
        # pre-upgrade snapshot fail closed and delete itself every upgrade).
        ken_env_value "$(systemctl show ken-snapshot.service -p Environment 2>/dev/null || true)" "$1"
        return 0
    fi
    # No systemd (non-systemd host): read the unit files, comment-safe (see the lib).
    ken_env_value_from_unit_files "$1" \
        /etc/systemd/system/ken-snapshot.service \
        /etc/systemd/system/ken-snapshot.service.d/*.conf
}
if [ -f "$BUNDLE/VERSION" ]; then
    VERSION="$(head -n1 "$BUNDLE/VERSION" | tr -d '[:space:]')"
fi
[ -n "${VERSION:-}" ] || die "could not read version from $BUNDLE/VERSION"
# The version becomes a path segment that later flows into rm -rf and sed — reject
# anything but a plain version token (blocks / .. whitespace and sed/glob metachars).
case "$VERSION" in *[!A-Za-z0-9._+-]*) die "unsafe characters in version string: '$VERSION'" ;; esac
case "$VERSION" in '' | . | .. | -*) die "unsafe version string: '$VERSION'" ;; esac

# Derived paths (also recomputed by the wizard when PREFIX/user change).
recompute_paths() {
    RELEASES="$PREFIX/releases"
    DEST="$RELEASES/$VERSION"
    LINK="$PREFIX/current"
    DATA="$PREFIX/data"
    LOGS="$PREFIX/logs"
    BACKUPS="${BACKUP_DIR:-$PREFIX/backups}"
}

# On an UPGRADE, re-discover the snapshot settings from the installed unit whenever the
# operator did not pass them. Without this, re-running the installer would silently
# relocate an existing archive back to the default — splitting it in half, since the
# nightlies follow the unit while the pre-upgrade snapshots follow the installer — and
# would drop a configured backup group on every upgrade.
if [ -z "$BACKUP_DIR" ];   then BACKUP_DIR="$(_ken_unit_env KEN_BACKUP_DIR)"; fi
if [ -z "$BACKUP_GROUP" ] && [ "$NO_BACKUP_GROUP" != "yes" ]; then BACKUP_GROUP="$(_ken_unit_env KEN_BACKUP_GROUP)"; fi
recompute_paths

# ----------------------------------------------------------------------------
# Preconditions.
# ----------------------------------------------------------------------------
if [ "$DRY_RUN" != "yes" ] && [ "$(id -u)" -ne 0 ]; then
    die "must run as root (try: sudo $0 ...). Use --dry-run to preview without root."
fi
command -v systemctl >/dev/null 2>&1 || warn "systemctl not found — service install/start will be skipped."

log "Ken ${VERSION}"

# ----------------------------------------------------------------------------
# Interactive wizard: on by default when stdin is a terminal.
# ----------------------------------------------------------------------------
PROMPT="no"
case "$INTERACTIVE" in
    yes)  if [ -t 0 ]; then PROMPT="yes"; else warn "--interactive requested but stdin is not a TTY; continuing unattended."; fi ;;
    no)   PROMPT="no" ;;
    auto) [ -t 0 ] && PROMPT="yes" ;;
esac

if [ "$PROMPT" = "yes" ]; then
    run_wizard
    recompute_paths
else
    log "  install root : $DEST"
    log "  data dir     : $DATA"
    log "  current link : $LINK"
    log "  service user : $SVC_USER"
    log "  listen port  : ${PORT:-<unchanged, :8080>}"
    log "  TLS          : $(tls_summary)"
    [ "$OPEN_FIREWALL" = "yes" ] && log "  firewall     : open $(firewall_target_ports | paste -sd, -)/tcp"
    [ "$DRY_RUN" = "yes" ] && log "  (dry run — no changes will be made)"
fi

# ----------------------------------------------------------------------------
# 0b. Validate operator-supplied values before they reach rm -rf / sed / systemd.
# ----------------------------------------------------------------------------
case "$PREFIX" in /*) ;; *) die "--prefix must be an absolute path (got '$PREFIX')" ;; esac
case "$PREFIX" in *[!A-Za-z0-9._/-]*) die "unsafe characters in --prefix: '$PREFIX'" ;; esac
case "$SVC_USER" in '' | *[!A-Za-z0-9._-]*) die "unsafe --user: '$SVC_USER'" ;; esac
# --backup-dir reaches the same rm -rf / sed / systemd surfaces as --prefix, so it gets
# the same treatment. A relative path would make the versioned `backups` symlink dangle;
# a space or sed metacharacter would corrupt the unit and the value re-discovered from it.
if [ -n "$BACKUP_DIR" ]; then
    case "$BACKUP_DIR" in /*) ;; *) die "--backup-dir must be an absolute path (got '$BACKUP_DIR')" ;; esac
    case "$BACKUP_DIR" in *[!A-Za-z0-9._/-]*) die "unsafe characters in --backup-dir: '$BACKUP_DIR'" ;; esac
fi
# Resolve the backup group HERE, before anything is written. A group that does not exist
# must not reach the unit: the nightly would then fail safe to 0600 every run while the
# unit claimed otherwise, and the bogus value would be re-discovered on every upgrade.
if [ -n "$BACKUP_GROUP" ]; then
    case "$BACKUP_GROUP" in *[!A-Za-z0-9._-]*) die "unsafe --backup-group: '$BACKUP_GROUP'" ;; esac
    if ! getent group "$BACKUP_GROUP" >/dev/null 2>&1; then
        warn "backup group '$BACKUP_GROUP' does not exist — ignoring it; snapshots stay 0600."
        warn "  create it first:  groupadd $BACKUP_GROUP && usermod -aG $BACKUP_GROUP <pull-account>"
        BACKUP_GROUP=""
    fi
fi
case "$SVC_GROUP" in '' | *[!A-Za-z0-9._-]*) die "unsafe --group: '$SVC_GROUP'" ;; esac
[ -z "$PORT" ] || case "$PORT" in *[!0-9]*) die "--port must be numeric: '$PORT'" ;; esac
case "$TLS_MODE" in
    "" | off | acme | file) ;;
    *) die "--tls must be acme, file, or off (got '$TLS_MODE')" ;;
esac
if [ "$TLS_MODE" = "acme" ]; then
    [ -n "$TLS_DOMAINS" ] || die "--tls acme requires --domain (comma-separated public hostnames)"
    case "$TLS_DOMAINS" in *[!A-Za-z0-9.,*_-]*) die "unsafe --domain: '$TLS_DOMAINS'" ;; esac
    [ -z "$TLS_EMAIL" ] || case "$TLS_EMAIL" in *[!A-Za-z0-9.@+_-]*) die "unsafe --email: '$TLS_EMAIL'" ;; esac
    if [ -n "$PORT" ] && [ "$PORT" != "443" ]; then
        warn "acme on :$PORT — Let's Encrypt validates only on :80/:443; ensure :80 (HTTP-01) stays reachable, or use :443."
    fi
fi
if [ "$TLS_MODE" = "file" ]; then
    { [ -n "$TLS_CERT" ] && [ -n "$TLS_KEY" ]; } || die "--tls file requires --cert and --key (PEM paths)"
    case "$TLS_CERT" in /*) ;; *) die "--cert must be an absolute path: '$TLS_CERT'" ;; esac
    case "$TLS_KEY" in /*) ;; *) die "--key must be an absolute path: '$TLS_KEY'" ;; esac
    case "$TLS_CERT$TLS_KEY" in *[!A-Za-z0-9._/-]*) die "unsafe --cert/--key path" ;; esac
fi

# ----------------------------------------------------------------------------
# 1. Service user + group.
# ----------------------------------------------------------------------------
if getent group "$SVC_GROUP" >/dev/null 2>&1; then
    log "group '$SVC_GROUP' already exists"
else
    log "creating system group '$SVC_GROUP'"
    run groupadd --system "$SVC_GROUP"
fi
if id "$SVC_USER" >/dev/null 2>&1; then
    log "user '$SVC_USER' already exists"
else
    log "creating system user '$SVC_USER'"
    run useradd --system --gid "$SVC_GROUP" --home-dir "$LINK" \
        --shell /usr/sbin/nologin --comment "ken service account" "$SVC_USER"
fi

# ----------------------------------------------------------------------------
# 2. Stop a running service before swapping files (upgrade path).
# ----------------------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet ken 2>/dev/null; then
    log "stopping running ken service"
    run systemctl stop ken
fi

# ----------------------------------------------------------------------------
# 3. Lay down the immutable code under $DEST.
# ----------------------------------------------------------------------------
log "installing code to $DEST"
# Clean re-lay: $DEST holds only immutable CODE; all editable state lives in
# $DATA/$LOGS/$BACKUPS via the symlinks created below. Wiping $DEST first makes
# re-runs idempotent (and fixes a same-version re-install where $DEST already
# has data/ as a symlink). rm -rf removes those symlinks, never their targets.
case "$DEST" in
    "$RELEASES/"?*) run rm -rf "$DEST" ;;
    *) die "refusing to wipe an unexpected install dir: $DEST" ;;
esac
run mkdir -p "$DEST"
run cp -a "$BUNDLE"/. "$DEST"/
# The installed tree carries no installer or packaging scripts — only the
# runtime launchers (ken.sh, ken-snapshot.sh) remain in scripts/.
run rm -f "$DEST/scripts/install.sh" \
          "$DEST/scripts/make-selfextract.sh" \
          "$DEST/scripts/selfextract-stub.sh" \
          "$DEST/scripts/build-release.sh"

# ----------------------------------------------------------------------------
# 4. Version-independent state dirs: create on fresh install, preserve on upgrade.
# ----------------------------------------------------------------------------
if [ -e "$DATA/ken.db" ]; then
    log "upgrade: preserving existing database at $DATA/ken.db"
else
    log "fresh install: state dir $DATA will be created (DB built on first run)"
fi
run mkdir -p "$DATA" "$LOGS" "$BACKUPS"

# Replace any bundled data/logs/backups with symlinks into the shared state, so
# KEN_HOME=$LINK resolves them and the code dir carries no state of its own.
for d in data logs backups; do
    run rm -rf "$DEST/$d"
    if [ "$d" = "backups" ]; then
        run ln -sfn "$BACKUPS" "$DEST/$d"    # follows --backup-dir, not just $PREFIX
    else
        run ln -sfn "$PREFIX/$d" "$DEST/$d"
    fi
done

# ----------------------------------------------------------------------------
# 5. systemd units: adjust the bundled templates and install them.
# ----------------------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1; then
    # Read the unit templates from the (always-present) bundle, not $DEST —
    # under --dry-run the code copy is skipped, so $DEST/deploy won't exist.
    SVC_SRC="$BUNDLE/deploy/ken.service"
    SNAP_SRC="$BUNDLE/deploy/ken-snapshot.service"
    TIMER_SRC="$BUNDLE/deploy/ken-snapshot.timer"
    [ -f "$SVC_SRC" ]   || die "missing $SVC_SRC"
    [ -f "$SNAP_SRC" ]  || die "missing $SNAP_SRC"
    [ -f "$TIMER_SRC" ] || die "missing $TIMER_SRC"

    SVC_TMP="$(mktemp)"; cp "$SVC_SRC" "$SVC_TMP"
    sed -i \
        -e "s|^User=.*|User=$SVC_USER|" \
        -e "s|^Group=.*|Group=$SVC_GROUP|" \
        -e "s|^Environment=KEN_HOME=.*|Environment=KEN_HOME=$LINK|" \
        -e "s|^Environment=KEN_DB=.*|Environment=KEN_DB=$DATA/ken.db|" \
        -e "s|^ExecStart=.*|ExecStart=$LINK/scripts/ken.sh -f|" \
        -e "s|^ReadWritePaths=.*|ReadWritePaths=$DATA $LOGS $BACKUPS|" \
        "$SVC_TMP"
    [ -n "$PORT" ] && sed -i -e "s|^Environment=KEN_ADDR=.*|Environment=KEN_ADDR=:$PORT|" "$SVC_TMP"

    # In-process TLS: inject the KEN_TLS* Environment lines just after KEN_DB. The
    # ACME cert cache defaults to <db dir>/acme, which is already writable.
    if [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ]; then
        TLS_ENV="Environment=KEN_TLS=$TLS_MODE"
        if [ "$TLS_MODE" = "acme" ]; then
            TLS_ENV="$TLS_ENV\nEnvironment=KEN_TLS_DOMAINS=$TLS_DOMAINS"
            [ -n "$TLS_EMAIL" ] && TLS_ENV="$TLS_ENV\nEnvironment=KEN_TLS_EMAIL=$TLS_EMAIL"
        else
            TLS_ENV="$TLS_ENV\nEnvironment=KEN_TLS_CERT=$TLS_CERT\nEnvironment=KEN_TLS_KEY=$TLS_KEY"
        fi
        sed -i "/^Environment=KEN_DB=.*/a $TLS_ENV" "$SVC_TMP"
    fi

    SNAP_TMP="$(mktemp)"; cp "$SNAP_SRC" "$SNAP_TMP"
    sed -i \
        -e "s|^User=.*|User=$SVC_USER|" \
        -e "s|^Group=.*|Group=$SVC_GROUP|" \
        -e "s|^Environment=KEN_HOME=.*|Environment=KEN_HOME=$LINK|" \
        -e "s|^Environment=KEN_DB=.*|Environment=KEN_DB=$DATA/ken.db|" \
        -e "s|^ExecStart=.*|ExecStart=$LINK/scripts/ken-snapshot.sh|" \
        -e "s|^ReadWritePaths=.*|ReadWritePaths=$DATA $BACKUPS|" \
        "$SNAP_TMP"

    # Put the snapshot settings in the unit so the nightly script and the installer's
    # pre-upgrade snapshot resolve the SAME directory and group — the divergence that
    # otherwise splits the archive in half. Injected after KEN_DB, like the TLS block.
    SNAP_ENV=""
    if [ -n "$BACKUP_DIR" ]; then
        SNAP_ENV="Environment=KEN_BACKUP_DIR=$BACKUPS"
    fi
    if [ -n "$BACKUP_GROUP" ]; then
        if [ -n "$SNAP_ENV" ]; then SNAP_ENV="$SNAP_ENV\n"; fi
        SNAP_ENV="${SNAP_ENV}Environment=KEN_BACKUP_GROUP=$BACKUP_GROUP"
    fi
    if [ -n "$SNAP_ENV" ]; then
        sed -i "/^Environment=KEN_DB=.*/a $SNAP_ENV" "$SNAP_TMP"
    fi

    log "installing systemd units to /etc/systemd/system/"
    run install -o root -g root -m 0644 "$SVC_TMP"   /etc/systemd/system/ken.service
    run install -o root -g root -m 0644 "$SNAP_TMP"  /etc/systemd/system/ken-snapshot.service
    run install -o root -g root -m 0644 "$TIMER_SRC" /etc/systemd/system/ken-snapshot.timer
    rm -f "$SVC_TMP" "$SNAP_TMP"
    run systemctl daemon-reload
else
    warn "skipping systemd units (systemctl unavailable)"
fi

# ----------------------------------------------------------------------------
# 5b. Pre-upgrade DB snapshot.
# ----------------------------------------------------------------------------
# Before the "current" symlink flips (so $LINK still resolves the OLD binary) and
# before the new release runs its migrations, snapshot the live DB so there is a
# clean rollback point. Best-effort: a failure never aborts the upgrade, and the live
# DB is untouched. The chown -R below fixes ownership of the snapshot (and any WAL
# sidecar files the read left behind). Skipped on a fresh install (no DB yet).
if [ -e "$DATA/ken.db" ] && [ -x "$LINK/bin/ken" ]; then
    # Name and secure it through the SAME shared policy as the nightly snapshot: a
    # UTC-Z stamp (self-describing, sorts in time order — not the old local, unmarked
    # stamp that read six hours off the nightlies), 0600 mode, and age-encryption when
    # the operator has configured a recipient for their backups. `ken_snapshot_stamp`
    # and `_ken_unit_env` are read-only, so they run outside `run`; the snapshot
    # write and `ken_snapshot_secure` (which touch the disk) go through it, so
    # --dry-run only prints them.
    _presnap="$BACKUPS/pre-upgrade-$(ken_snapshot_stamp).db"
    _recipient="$(_ken_unit_env KEN_AGE_RECIPIENT)"
    log "pre-upgrade snapshot -> $_presnap${_recipient:+ (age-encrypted)}"
    if run env KEN_DB="$DATA/ken.db" "$LINK/bin/ken" backup snapshot --out "$_presnap"; then
        # Best-effort, and FAIL CLOSED on encryption: if a recipient is set but the
        # encrypt cannot happen, ken_snapshot_secure removes the plaintext and returns
        # non-zero — we warn and proceed rather than leave a plaintext DB copy the
        # operator asked to have encrypted. A missing rollback point never aborts an
        # upgrade; the live DB is untouched either way.
        run ken_snapshot_secure "$_presnap" "$_recipient" "$BACKUP_GROUP" \
            || warn "pre-upgrade snapshot could not be secured (see above) — continuing; your live DB is untouched"
    else
        warn "pre-upgrade snapshot failed — continuing; your live DB is untouched"
    fi
fi

# ----------------------------------------------------------------------------
# 6. Flip the "current" symlink.
# ----------------------------------------------------------------------------
log "pointing $LINK -> $DEST"
run ln -sfn "$DEST" "$LINK"

# ----------------------------------------------------------------------------
# 7. Ownership + permissions.
#    Code ($RELEASES): root-owned, world-readable, binary + launchers +x
#    (immutable to the service user). State ($DATA/$LOGS/$BACKUPS): owned by the
#    service user; the dedup HMAC key locked to 0600.
# ----------------------------------------------------------------------------
log "setting ownership and permissions"
run chown -Rh root:root "$RELEASES"
run chmod -R u=rwX,go=rX "$DEST"
run chmod 0755 "$DEST/bin/ken" "$DEST/scripts/ken.sh" "$DEST/scripts/ken-snapshot.sh"

run chown -R "$SVC_USER:$SVC_GROUP" "$DATA" "$LOGS"
run chmod 0750 "$DATA" "$LOGS"
# setgid on state dirs so files created at runtime inherit the group.
run find "$DATA" "$LOGS" -type d -exec chmod g+s {} +

# The backups directory is handled separately and NON-recursively. With --backup-dir it
# can be a location the operator already uses for other things, and a blanket `chown -R`
# / `chmod` there would rewrite files that are not ours. Only the directory itself and
# the snapshots we actually manage (ken-* / pre-upgrade-*) are touched.
run chown "$SVC_USER:$SVC_GROUP" "$BACKUPS"
run chmod 0750 "$BACKUPS"
run find "$BACKUPS" -maxdepth 1 -type f \( -name 'ken-*' -o -name 'pre-upgrade-*' \) \
    -exec chown "$SVC_USER:$SVC_GROUP" {} +

# Off-box backup access (opt-in, --backup-group). The generic block above just set the
# archive to ken:ken 0750/0600, which only root and the nologin service account can read
# — so pulling a backup off the host required a root-authorized key or a root job staging
# copies. Giving the archive a second, unprivileged group is the smaller grant: the dir
# becomes setgid to it (so every new snapshot inherits the group with no runtime chgrp)
# and snapshot files become 0640. Fails SAFE: an unknown group warns and changes nothing,
# leaving the strict 0600 posture rather than a half-applied one.
if [ -n "$BACKUP_GROUP" ]; then
    # (Existence was verified in section 0b; an unknown group was cleared there.)
    log "backup group: $BACKUP_GROUP may read snapshots (dir setgid, files 0640)"
    run chgrp "$BACKUP_GROUP" "$BACKUPS"
    run chmod 2750 "$BACKUPS"        # setgid: new snapshots inherit the group, no runtime chgrp
    run find "$BACKUPS" -maxdepth 1 -type f \( -name 'ken-*' -o -name 'pre-upgrade-*' \) \
        -exec chgrp "$BACKUP_GROUP" {} + -exec chmod 0640 {} +
elif [ "$NO_BACKUP_GROUP" = "yes" ]; then
    # Explicit revocation: put the archive back to owner-only. Without this there is no
    # way back from a widening — a plain re-run would re-discover the group from the unit.
    log "backup group: revoked — snapshots back to 0600, owner only"
    run chgrp "$SVC_GROUP" "$BACKUPS"
    run chmod 0750 "$BACKUPS"
    run find "$BACKUPS" -maxdepth 1 -type f \( -name 'ken-*' -o -name 'pre-upgrade-*' \) \
        -exec chgrp "$SVC_GROUP" {} + -exec chmod 0600 {} +
fi
[ -e "$DATA/dedup.key" ] && run chmod 0600 "$DATA/dedup.key"

# ----------------------------------------------------------------------------
# 7b. SELinux: relabel to the policy defaults for these paths.
#     The self-extracting .bin unpacks into a user-temp dir, so `cp -a` (which
#     preserves SELinux context) carries `user_tmp_t` onto $DEST. On an enforcing
#     system (RHEL/Rocky/Fedora/Alma) systemd then refuses to exec the launcher
#     with status=203/EXEC ("Permission denied"). restorecon resets the tree to
#     the correct contexts, which is what makes ken.service actually start.
#     No-op when SELinux is absent (most Debian).
# ----------------------------------------------------------------------------
if command -v restorecon >/dev/null 2>&1; then
    log "restoring SELinux file contexts on $PREFIX"
    run restorecon -RF "$PREFIX" || warn "restorecon failed — if the service won't start with status=203/EXEC, run: restorecon -RF $PREFIX"
fi

# ----------------------------------------------------------------------------
# 8. Enable + start.
# ----------------------------------------------------------------------------
if command -v systemctl >/dev/null 2>&1; then
    if [ "$DO_ENABLE" = "yes" ]; then
        log "enabling ken + nightly snapshot timer at boot"
        run systemctl enable ken.service
        run systemctl enable ken-snapshot.timer
    fi
    if [ "$DO_START" = "yes" ]; then
        log "starting ken"
        run systemctl restart ken.service
        run systemctl start ken-snapshot.timer
        [ "$DRY_RUN" = "yes" ] || sleep 1
        if [ "$DRY_RUN" != "yes" ] && systemctl is-active --quiet ken; then
            log "ken is running. Logs: journalctl -u ken -f"
        elif [ "$DRY_RUN" != "yes" ]; then
            warn "ken did not become active — check: journalctl -u ken -n 100 --no-pager"
        fi
    else
        log "skipping start (--no-start). Start later with: systemctl start ken"
    fi
fi

# ----------------------------------------------------------------------------
# 9. Host firewall (opt-in).
# ----------------------------------------------------------------------------
if [ "$OPEN_FIREWALL" = "yes" ]; then
    open_firewall "$(firewall_target_ports | paste -sd' ' -)"
fi

# ----------------------------------------------------------------------------
# 10. Next steps.
# ----------------------------------------------------------------------------
_host="$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo your-server)"
KEN="env KEN_DB=$DATA/ken.db $LINK/bin/ken"
# Public URL for the next-steps hints (scheme/host/port follow the TLS choice).
_scheme="http"; _urlhost="$_host"; _urlport=":${PORT:-8080}"
if [ -n "$TLS_MODE" ] && [ "$TLS_MODE" != "off" ]; then
    _scheme="https"
    [ "$TLS_MODE" = "acme" ] && _urlhost="${TLS_DOMAINS%%,*}"
    if [ "${PORT:-443}" = "443" ]; then _urlport=""; else _urlport=":${PORT}"; fi
elif [ "${PORT:-8080}" = "80" ]; then
    _urlport=""
fi
_url="${_scheme}://${_urlhost}${_urlport}"
_acme_note=""
if [ "$TLS_MODE" = "acme" ]; then
    _acme_note="HTTPS: the ACME (Let's Encrypt) certificate for ${TLS_DOMAINS} is obtained automatically
on the first HTTPS request. Ensure DNS for that name points here and ports 80+443 are
reachable from the internet, then watch issuance with:  journalctl -u ken -f
"
fi
cat <<EOF

[install] done — Ken ${VERSION} installed at $LINK
${_acme_note}
Next steps
----------
1. Open the web UI. On first visit Ken runs a one-time SETUP WIZARD that creates
   the admin (curator) account — there is no self-registration, user #1 is yours:

     ${_url}/

   (Prefer the CLI? Create the admin instead with — password hashed with Argon2id:
      sudo -u $SVC_USER $KEN user add --name admin )

2. Issue an agent (MCP) token so an AI client can read/write the knowledge base:

     sudo -u $SVC_USER $KEN token add --actor "my-agent" --scopes read,write-draft,propose

   The secret is printed ONCE. Point the MCP client at:

     ${_url}/mcp      header:  Authorization: Bearer <token>

Service
-------
  status : systemctl status ken
  logs   : journalctl -u ken -f
  stop   : systemctl stop ken
  upgrade: re-run this .bin / installer for a newer version (data is preserved)

Backups (design decision: backup is the only durability path)
-------
  The nightly snapshot timer (ken-snapshot.timer, 03:30) is enabled. Snapshots are
  written UNENCRYPTED at mode 0600 until you configure an age recipient.

  To encrypt them - read docs/BACKUP.md first; order matters:
    1. install 'age' on this host   (recipient set + age missing = NO snapshot kept)
    2. 'age-keygen -o age.key' on your workstation, NOT here; escrow age.key off-box
       (lose it and every encrypted snapshot is unrecoverable)
    3. systemctl edit ken-snapshot.service
         [Service]
         Environment=KEN_AGE_RECIPIENT=age1yourpublickey...
    4. systemctl start ken-snapshot.service && ls -l $BACKUPS   # expect a .db.age

  Also consider Litestream (continuous ~1s-RPO replication) — see docs/BACKUP.md
  and configs/litestream.yml. Snapshots land in $BACKUPS.
EOF
