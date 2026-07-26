# Remote upgrade — a scoped, passwordless "run the installer" path

This lets an operator (or an AI agent) with only **unprivileged SSH access** as a
unprivileged deploy account upgrade Ken to the latest published release — and *nothing
else* — without full passwordless `sudo` and without hand-editing files under
`/opt/ken`. The single privileged action is running the vetted installer.

It is built from two root-owned pieces:

| File | Purpose | Perms |
|---|---|---|
| `/usr/local/sbin/ken-upgrade` | wrapper: fetch → verify → run the installer | `0755 root:root` |
| `/etc/sudoers.d/ken-upgrade` | one `NOPASSWD` rule for the wrapper | `0440 root:root` |

Releases are fetched from the **public GitHub Releases** over HTTPS, so no download
token is needed. Source lives in the repo:
[`scripts/ken-upgrade`](../scripts/ken-upgrade),
[`deploy/ken-upgrade.sudoers`](../deploy/ken-upgrade.sudoers).

## Why not just `sudo ./ken-<version>.bin`?

The `.bin` is a shell script that runs its payload as root. A `NOPASSWD` rule on a
file the deploy user can **write** (anything in its home) is equivalent to full
passwordless root — the user could replace the file's bytes with a root shell. So
the rule must point at a **root-owned, non-caller-writable** command, and the
caller must **not supply the binary**. The wrapper does exactly that: it downloads
the installer from the repo and checksum-verifies it before running.

## The security boundary is the wrapper

`sudoers` trusts one command; the **wrapper** decides what is safe. It:

- stays root-owned and root-only-writable (if the caller could edit it, the rule
  would be full root);
- fetches the installer + `SHA256SUMS` from the **public GitHub Releases** and
  **aborts on any checksum mismatch** — the caller never provides the binary;
- **validates every argument** against a strict allowlist and refuses everything
  else — no argument is passed through unchecked.

### Argument allowlist

| Accepted | Notes |
|---|---|
| *(no args)* | upgrade to the **latest** release (preserves existing TLS/port config) |
| `--version X.Y.Z` | install/rollback to a specific published version |
| `--tls acme` \| `--tls off`, `--domain <dns>`, `--email <addr>` | ACME/plain TLS posture (validated) |
| `--port <1-65535>` | listen port |
| `--open-firewall` \| `--no-firewall` | host firewall |
| `--dry-run` | print actions, change nothing |

**Refused** (they would let the caller write/read root-owned files at an arbitrary
path, i.e. escalate): `--prefix`, `--extract`, `--user`, `--cert`, `--key`,
`--tls file`, and any unknown flag. Do those locally as root if ever needed, or
extend the wrapper in a reviewed change.

## Install (run once, as root)

```sh
# 1) the wrapper
install -o root -g root -m 0755 scripts/ken-upgrade /usr/local/sbin/ken-upgrade

# 2) the sudoers rule
install -o root -g root -m 0440 deploy/ken-upgrade.sudoers /etc/sudoers.d/ken-upgrade
visudo -cf /etc/sudoers.d/ken-upgrade     # must print "parsed OK"
```

## Use

```sh
sudo /usr/local/sbin/ken-upgrade                          # → latest
sudo /usr/local/sbin/ken-upgrade --dry-run                # preview only
sudo /usr/local/sbin/ken-upgrade --version 0.3.2          # pin / roll back
sudo /usr/local/sbin/ken-upgrade --tls acme --domain kb.example.com --email you@example.com
```

## Trust boundary (be honest about it)

The wrapper runs code from your latest release, so the effective trust root is
"whoever can publish a Ken release" — you. The checksum guards against a corrupted
or MITM'd download, **not** a malicious release (which you control anyway). For a
stronger guarantee, GPG-sign releases and have the wrapper verify the signature.

## Tightest variant

If you don't need parameters, append `""` to the sudoers command so the wrapper
accepts **no** arguments (latest-only): `… NOPASSWD: /usr/local/sbin/ken-upgrade ""`.
