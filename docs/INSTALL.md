# Ken — installation & administration

Ken ships as a single static Go binary. On Linux it installs from a one-file
self-extracting `.bin` (no dependencies beyond stock `bash`/`tar`/`gzip`), runs
under systemd, and keeps the knowledge base in one SQLite database. There is **no
external database, runtime or JVM** to provision. (Inter-session messaging is core
rather than optional, and keeps its own expendable database beside it at
`data/comm/comm.db` — deliberately outside the backups; see [`docs/BACKUP.md`](BACKUP.md).)

- Reference layout after install: `/opt/ken/{current,releases,data,logs,backups}`
- Service: `ken.service` (foreground under systemd) + `ken-snapshot.timer` (nightly backup)
- First-run setup runs in the **web UI**: the first visit shows a one-time wizard
  that creates the admin account (the installer creates no account). See
  [Complete setup](#complete-setup).

---

## Linux — the one-file installer

Download the `.bin` for your CPU architecture (`amd64` for most servers,
`arm64` for Graviton/Ampere/Raspberry-Pi-class hosts) and run it as root:

```sh
sudo ./ken-<version>-linux-amd64.bin
```

### Always-latest download links

Every release ships **stable-named aliases** (`ken-latest-linux-<arch>`), so these
public links always fetch the newest installer (no need to look up the version):

```
https://github.com/Quest-ICT/ken/releases/latest/download/ken-latest-linux-amd64.bin
https://github.com/Quest-ICT/ken/releases/latest/download/ken-latest-linux-arm64.bin
https://github.com/Quest-ICT/ken/releases/latest/download/SHA256SUMS
```

To grab and run the latest amd64 installer in one go:

```sh
curl -fL -o ken-latest-linux-amd64.bin \
  https://github.com/Quest-ICT/ken/releases/latest/download/ken-latest-linux-amd64.bin
sudo ./ken-latest-linux-amd64.bin
```

The browser page `https://github.com/Quest-ICT/ken/releases/latest` also always opens
the newest release. (Verify against `SHA256SUMS`; it lists both the versioned and the
alias names — the aliases are byte-identical copies.)

## Verifying what you downloaded

`SHA256SUMS` detects a **corrupted download**. It cannot detect a **compromised release**,
because it is served from the same place as the thing it vouches for — if that place is
wrong, so is the checksum. Every release therefore also carries a **build-provenance
attestation**, which is signed at build time and resolved out of band:

```sh
gh attestation verify ken-latest-linux-amd64.bin --repo Quest-ICT/ken
```

**IT SUCCEEDS SILENTLY — exit 0, no output.** That is indistinguishable from a command that
did nothing, so before you trust a pass, prove the check can fail. Two controls, and run
them once on any machine where you rely on this:

```sh
head -c 2048 /dev/urandom > /tmp/junk.bin
gh attestation verify /tmp/junk.bin --repo Quest-ICT/ken          # expect: exit 1
gh attestation verify ken-latest-linux-amd64.bin --repo torvalds/linux   # expect: exit 1
```

The **second** is the one that matters. The first only shows that an unknown file fails.
The second takes the genuine artifact and asks the wrong repository about it — so passing
it proves the command resolves your file's hash against *a specific repository's*
attestations, rather than rubber-stamping whatever it is handed. With both failing as
shown, an exit 0 on the real command is a pass you can justify.

Reported by ken-prod-ops, who nearly recorded a pass they had not earned — and by the
maintainer, who actually did, in the same week, in a message recommending the command.

On a terminal it runs an **interactive wizard** (install prefix, service user,
listen port, firewall, start/enable); piped or with `-y` it runs unattended with
sensible defaults. Useful flags:

```sh
sudo ./ken-<version>-linux-amd64.bin -y                    # unattended, all defaults
sudo ./ken-<version>-linux-amd64.bin --port 8080 --open-firewall -y
sudo ./ken-<version>-linux-amd64.bin --prefix /srv/ken --user ken -y
sudo ./ken-<version>-linux-amd64.bin --help                # full option list
```

Preview everything without touching the machine (works without root):

```sh
./ken-<version>-linux-amd64.bin --dry-run -y               # prints every action
./ken-<version>-linux-amd64.bin --extract /tmp/ken-bundle  # just unpack, don't install
```

What the installer does (idempotent — the same command upgrades):

- creates the `ken` system user/group (nologin),
- lays the immutable code under `/opt/ken/releases/<version>/` (root-owned),
- keeps state in version-independent `/opt/ken/{data,logs,backups}/`, symlinked
  into the release dir, so **upgrades never touch the database, logs or snapshots**,
- installs + path-adjusts `ken.service`, `ken-snapshot.service`, `ken-snapshot.timer`,
- flips `/opt/ken/current -> releases/<version>`,
- restores SELinux contexts (see below), optionally opens the firewall,
- enables + starts the service and enables the nightly snapshot timer.

### Install from the plain tarball (alternative)

The `.bin` is just a stub with a gzip tar appended; the same bundle ships as
`ken-<version>-linux-<arch>.tar.gz`:

```sh
tar xzf ken-<version>-linux-amd64.tar.gz
sudo ./ken-<version>/scripts/install.sh -y
```

---

## HTTPS (TLS) — strongly recommended

**Serve Ken over HTTPS.** It handles browser logins and bearer tokens; over plain
HTTP those cross the wire in the clear. Ken can terminate TLS **itself** — obtaining
and auto-renewing a real Let's Encrypt certificate — so there is no reason to run it
unencrypted. Plain HTTP (`KEN_TLS=off`, the default) is valid **only** when a reverse
proxy in front of Ken already terminates TLS.

The posture is chosen with `KEN_TLS` (`acme` | `file` | `off`).

### Standalone with Let's Encrypt (recommended)

Ken issues and renews the certificate in-process via ACME — no certbot, no cron.
Prerequisites:

- a **public DNS name** (e.g. `kb.example.com`) with an A/AAAA record pointing here;
- **ports 80 and 443 reachable from the internet** — Let's Encrypt validates over
  them, and `:80` also serves the HTTP→HTTPS redirect;
- the host firewall open for both (the installer offers to do this).

Install with ACME in one line:

```sh
sudo ./ken-<version>-linux-amd64.bin \
    --tls acme --domain kb.example.com --email you@example.com --open-firewall -y
```

That writes `KEN_TLS=acme`, `KEN_TLS_DOMAINS`, `KEN_TLS_EMAIL`, `KEN_ADDR=:443` into
the unit and opens 80+443. On the **first HTTPS request** Ken fetches the certificate
and caches it under `/opt/ken/data/acme/`; renewals happen automatically, well before
expiry. Trigger and watch issuance:

```sh
curl -sI https://kb.example.com/ >/dev/null &   # first hit triggers issuance
sudo journalctl -u ken -f                        # watch the ACME exchange
```

Choosing `--tls acme` accepts the Let's Encrypt Terms of Service. Nothing is served
on `:80` except the ACME challenge and a 301 to HTTPS.

### Your own certificate (file mode)

For a corporate-CA, wildcard, or otherwise pre-obtained certificate:

```sh
sudo ./ken-<version>-linux-amd64.bin \
    --tls file --cert /etc/ken/tls/fullchain.pem --key /etc/ken/tls/privkey.pem -y
```

Ken serves it on `:443` (with the same `:80`→`:443` redirect) and **hot-reloads** the
files when they change, so a renewal needs no restart. Keep the key `0600` and readable
by the `ken` user.

### Behind a reverse proxy (plain HTTP)

Only when something else already terminates TLS (nginx, Caddy, HAProxy, a load
balancer): leave `KEN_TLS` unset and keep Ken on `:8080`. Pass `--secure-cookies`
(or set `KEN_SECURE_COOKIES=on`) so session cookies are marked Secure/`__Host-`,
and set `KEN_TRUSTED_PROXIES` to the proxy's CIDR so the login guard and rate
limiter read the real client IP. (These are independent: trusting the proxy's
`X-Forwarded-For` does not imply it terminates TLS, so Secure cookies stay an
explicit choice.)

Enable or change TLS after install by editing the unit — `sudo systemctl edit ken`,
set the `KEN_TLS*` keys (the commented block in `ken.service` lists them) and
`KEN_ADDR=:443`, then `sudo systemctl restart ken`.

---

## Complete setup

The installer creates no account — setup happens in the browser (use the `https://`
URL when TLS is on, `http://<host>:8080/` only behind a TLS-terminating proxy):

1. **Open the web UI** — the first visit runs a one-time **setup wizard** that
   creates the admin (curator) account:

   ```
   http://<host>:8080/
   ```

   Ken has no self-registration — user #1 is the admin you create here. Prefer the
   CLI? Create it instead with
   `sudo -u ken env KEN_DB=/opt/ken/data/ken.db /opt/ken/current/bin/ken user add --name admin`.

2. **Issue an agent (MCP) token** so an AI client can read/write the base:

   ```sh
   sudo -u ken env KEN_DB=/opt/ken/data/ken.db /opt/ken/current/bin/ken \
       token add --actor "my-agent" --scopes read,write-draft,propose
   ```

   The secret is printed **once**. Point the MCP client at
   `http://<host>:8080/mcp` with the header `Authorization: Bearer <token>`.
   Scopes: `read`, `write-draft`, `propose`, `curate`.

3. **There are no other credentials to issue.** Ken serves **one** machine endpoint, `/mcp`, and it
   carries every tool: `kb_*`, `comm_*` and `station_*`. `/comm/mcp` and `/station/mcp` are deleted,
   along with the `kens_` station key and the rule that a token could hold knowledge-base scopes or
   comm scopes but never both.

   **A credential reaching `/mcp` must carry EVERY capability**, and is refused at the transport
   otherwise. So the token above is not enough on its own:

   ```sh
   sudo -u ken env KEN_DB=/opt/ken/data/ken.db /opt/ken/current/bin/ken \
       token add --actor my-agent \
       --scopes read,write-draft,propose,comm,comm-file,station
   ```

   In practice you will not mint one at all: the ordinary credential is the **OAuth grant** a
   human approves once when adding Ken as a connector, which carries the whole set. A static token
   is for a CLI or a script.

   Creating a station is still a human act, and still names it:

   ```sh
   sudo -u ken env KEN_DB=/opt/ken/data/ken.db /opt/ken/current/bin/ken \
       station add --name prod-ops
   ```

   A session then claims a station by stating its conversation id (`session_key`) on its first
   `station_me` call — no key to mint, deliver or protect.

   **There is no opt-out.** `KEN_COMM_ENABLED` and `KEN_STATION_ENABLED` were removed in
   2.0.0 — setting either does nothing. Both surfaces are core and always registered.
   COMM still turns ITSELF off if `comm.db` cannot be opened, which is a runtime state
   rather than a setting: an expendable database must never take the durable knowledge
   base down. Stations keep working in that state, because the notebook and task list
   are worth having to a
   session with no peers. The variables were kept rather than deleted because Ken
   *already* has a runtime "COMM off" state: an unopenable `comm.db` degrades into it on
   purpose, so an expendable database can never take the durable knowledge base down.
   Deleting the variable would not remove that state — only the operator's control of
   it, which is their one remedy if COMM misbehaves in production. Details:
   [`docs/COMM.md`](COMM.md), [`docs/STATIONS.md`](STATIONS.md).

---

## Optional: OAuth connector for claude.ai

Ken ships an **OAuth 2.1 authorization server** so claude.ai can add it
as a *custom connector* with the normal **Connect** button (no static token to
paste). **It is ON and cannot be turned off** (see below) — static bearer tokens above are
unaffected either way.

It requires a public **HTTPS** origin (so `KEN_TLS=acme|file`, or a
TLS-terminating proxy). **There is nothing to enable** — the OAuth authorization
server is on by default and cannot be turned off. `KEN_OAUTH_ENABLED` was removed
in **2.0.0**; setting it does nothing, and this page told you to set it until
2026-08-20.

The reason it is unconditional is in `cmd/ken/main.go`: defaulting it off meant a
fresh install could not be connected the documented way until the operator found a
variable nothing pointed them at.

A connector gets the same capability as an agent token — `read`, `write-draft`,
`propose`, **never `curate`** — and is revocable from the Tokens page
(*Connected apps (OAuth)*). Full flow (discovery,
consent, connecting from claude.ai) is in [`docs/OAUTH.md`](OAUTH.md).

---

## Optional: semantic search (embeddings)

Ken's search is keyword-first (BM25 + trigram) and works fully without embeddings.
To add **semantic** recall, point Ken at an OpenAI-compatible embeddings endpoint.
Embeddings stay **off** unless `KEN_EMBED_PROVIDER` is set.

Set these in the service environment — a systemd drop-in is the clean place (see
[Service management](#service-management), and note `.d/` drop-ins survive upgrades):

```ini
[Service]
Environment=KEN_EMBED_PROVIDER=http
Environment=KEN_EMBED_URL=https://api.openai.com/v1/embeddings
Environment=KEN_EMBED_MODEL=text-embedding-3-small
Environment=KEN_EMBED_DIM=1536
Environment=KEN_EMBED_KEY=sk-...        # optional; the endpoint's bearer key
```

| Var | Role |
|-----|------|
| `KEN_EMBED_PROVIDER` | `http` (hosted, OpenAI-compatible) · `hash` (offline; tests / air-gapped) · unset = off |
| `KEN_EMBED_URL` | http: the embeddings endpoint — **required** |
| `KEN_EMBED_MODEL` | http: model name — **required** |
| `KEN_EMBED_DIM` | vector dimension; must match the model (e.g. 1536) — http: **required**; hash: default 256 |
| `KEN_EMBED_KEY` | http: bearer / API key — optional |

The **query** side embeds automatically once a provider is set (`kb_search` gains a
semantic arm, fused with the keyword ranks via RRF). **Stored** content is embedded
by an explicit backfill — run it after configuring, and again after adding a batch of
entries (there is no auto-embed-on-write yet):

```sh
sudo systemctl restart ken          # pick up the KEN_EMBED_* drop-in
sudo -u ken env KEN_DB=/opt/ken/data/ken.db \
  KEN_EMBED_PROVIDER=http KEN_EMBED_URL=… KEN_EMBED_MODEL=… KEN_EMBED_DIM=… KEN_EMBED_KEY=… \
  /opt/ken/current/bin/ken embed backfill
# progress / coverage:  ken embed status
```

A `KEN_EMBED_DIM` that doesn't match the model is rejected at startup.

## Upgrade

Re-run the newer `.bin` (or `install.sh`) — same command as install. The old
release stays under `releases/<old-version>/`; the `current` symlink flips to the
new one; your `data/`, `logs/` and `backups/` are preserved untouched. Roll back
by pointing `current` at a previous release and restarting:

```sh
sudo ln -sfn /opt/ken/releases/<old-version> /opt/ken/current
sudo systemctl restart ken
```

Before it flips the symlink, the installer takes a **pre-upgrade snapshot** of the
live database into `backups/pre-upgrade-<UTC-Z>.db.gz` — the same naming and securing
policy as the nightly snapshot (`scripts/ken-snapshot-lib.sh`): gzip-compressed, mode
`0600`, not encrypted. It is best-effort — a snapshot failure never aborts the upgrade,
and your live DB is untouched.

> **These are pruned, and until 1.8.0 they were not.** A rollback point survives if it is among the
> newest `KEEP_PRE_UPGRADE` (default 3) **or** younger than `KEEP_PRE_UPGRADE_DAYS` (default 7) —
> whichever keeps more. Before that, the nightly retention globbed `ken-*` and could not match
> `pre-upgrade-*`, so every upgrade left one behind permanently. See
> [BACKUP.md](BACKUP.md) for why both floors are needed.

---

## Uninstall

```sh
sudo systemctl disable --now ken.service ken-snapshot.timer
sudo rm -f /etc/systemd/system/ken.service \
           /etc/systemd/system/ken-snapshot.service \
           /etc/systemd/system/ken-snapshot.timer
sudo systemctl daemon-reload
sudo rm -rf /opt/ken/releases /opt/ken/current
# Keep /opt/ken/data + /opt/ken/backups unless you really want to lose the
# knowledge base, then:  sudo rm -rf /opt/ken
# Remove the service account if unused elsewhere:  sudo userdel ken
```

---

## Service management

```sh
systemctl status ken            # state
journalctl -u ken -f            # follow logs
systemctl restart ken           # restart
systemctl stop ken              # stop
```

The unit runs `ken.sh -f`, which `exec`s the binary so systemd supervises the
real process; a clean `SIGTERM` shutdown exits 143 and is treated as success.
The unit is hardened (`ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`)
and is granted `CAP_NET_BIND_SERVICE`, so the unprivileged service account can bind
`:80`/`:443` directly for in-process TLS. See **HTTPS (TLS)** above for enabling
encryption — the strong default is `KEN_TLS=acme` (Let's Encrypt); plain HTTP is for
behind-a-proxy deployments only.

Change the listen port after install:

```sh
sudo systemctl edit ken           # add:  [Service]\nEnvironment=KEN_ADDR=:443
sudo systemctl restart ken
```

---

## SELinux (RHEL / Rocky / Fedora / Alma)

On enforcing systems the installer runs `restorecon -RF /opt/ken` for you. This
is **required**: the `.bin` unpacks into a user-temp directory, so the copy would
otherwise carry the `user_tmp_t` context onto the installed files and systemd
would refuse to exec the launcher with `status=203/EXEC`. If you ever see that,
relabel by hand:

```sh
sudo restorecon -RF /opt/ken
```

---

## Host firewall

Firewall changes are **opt-in** (`--open-firewall`). The installer drives only:

- **firewalld** (RHEL-family) — adds the port(s) to the default zone permanently
  and reloads;
- an **already-active ufw** (Debian-family) — `ufw allow <port>/tcp`.

It **never enables ufw** (that could drop your SSH session) and **never edits raw
nftables/iptables** — it prints the exact manual commands instead. It opens
`--port` (else 8080), plus port 80 when `--port` is 443 (Ken's in-process ACME
serves the HTTP-01 challenge and the HTTP→HTTPS redirect there). `--no-firewall`
guarantees the firewall is left untouched. Do it by hand any time:

```sh
sudo firewall-cmd --permanent --add-port=8080/tcp && sudo firewall-cmd --reload   # firewalld
sudo ufw allow 8080/tcp                                                           # ufw (if active)
```

---

## Backups (the only durability path)

Ken has no git mirror, so backups are non-negotiable — and are set up for you:

- **Nightly snapshots** via `ken-snapshot.timer` (03:30) are **enabled by default and are written as
  compressed PLAINTEXT at mode `0600`.** Snapshots land in `/opt/ken/backups` and are pruned to
  `KEN_BACKUP_KEEP` (default 14).

  **Ken cannot encrypt them, and there is no setting that turns encryption on.** 2.0.0 retired
  `KEN_AGE_RECIPIENT`; setting it produces a NOTE in the journal and a plaintext snapshot, which is
  written and kept. **If your snapshots ever leave this machine — and tier 3 in
  [docs/BACKUP.md](BACKUP.md) is exactly that — the encryption is yours to add outside Ken**, in
  whatever moves the file: pipe it through `age` or `gpg`, or use your sync tool's own. The one
  encryption option inside this system belongs to Litestream, not Ken: the `age:` block in
  `configs/litestream.yml`, which protects the continuous replica and not the nightly snapshot.

  **`0600` protects the file on this box and nowhere else.** A snapshot is a full copy of the
  knowledge base, the curator accounts and every station vault secret; the mode does not travel with
  it.

  > **THIS BLOCK GAVE A PROCEDURE FOR TURNING ON ENCRYPTION UNTIL 2026-08-25** — install `age`, escrow
  > a keypair, set a recipient — for a control removed in 2.0.0, complete with a false warning that
  > the snapshot "fails closed" and keeps nothing when `age` is missing. It does not. An operator who
  > followed it believed their off-box backups were ciphertext and had plaintext.

  ```sh
  sudo systemctl start ken-snapshot.service && ls -l /opt/ken/backups   # confirm a .db.gz appeared
  ```

- **Pulling snapshots off the box without root.** Snapshots default to `0600` owned by the `nologin`
  service account, so only root can read them. `--backup-group GRP` makes them `0640` owned by `GRP`
  (and the backups dir setgid to it), so a dedicated unprivileged account can be pulled from by an
  archive host with no root key anywhere. `--backup-dir DIR` relocates the archive — honoured by
  **both** the nightly timer and the pre-upgrade snapshot, so it never splits in half. Both are written
  into `ken-snapshot.service` and remembered across upgrades. Full runbook:
  [docs/BACKUP.md → Pulling snapshots off the box](BACKUP.md#pulling-snapshots-off-the-box-tier-3-without-root).

- **Litestream** (continuous ~1s-RPO replication to S3-compatible storage) is the
  recommended tier 1 — see [`configs/litestream.yml`](../configs/litestream.yml)
  and [`docs/BACKUP.md`](BACKUP.md).

Make/verify a snapshot by hand:

```sh
# KEN_DB is required: without it the command falls back to a RELATIVE ./data and fails.
sudo -u ken env KEN_DB=/opt/ken/data/ken.db /opt/ken/current/bin/ken backup snapshot --out /tmp/ken-$(date -u +%Y%m%dT%H%M%SZ).db
sudo -u ken /opt/ken/current/bin/ken backup verify   /tmp/ken-….db
```

Never `cp` the live `.db` (WAL sidecars make it torn) — always use
`ken backup snapshot` (VACUUM INTO) or Litestream.

---

## Building the release artifacts

From a checkout with the Go toolchain available:

```sh
scripts/build-release.sh                 # linux/amd64 + linux/arm64 -> dist/
scripts/build-release.sh --version 1.2.0
```

It cross-compiles both architectures (`CGO_ENABLED=0`, no C toolchain needed —
Ken's SQLite is a pure-Go WASM build), stages each bundle, and emits both
`ken-<version>-linux-<arch>.tar.gz` and `ken-<version>-linux-<arch>.bin` into
`dist/`.
