# Ken — backup & restore

Ken's data lives in one SQLite file (`data/ken.db` + its `-wal`/`-shm` sidecars).

**What is in a snapshot.** A snapshot is a byte-complete copy of the knowledge base: every entry and
every superseded revision (including proposals a human never promoted), the full curation history
(`entry_version` + `curation_event`), embeddings, the human curator accounts, and the agent/MCP token
records. **No credential Ken *stores* is replayable:** passwords are Argon2id hashes, API-token secrets
are stored hashed (station keys included), and web-session ids are stored as a SHA-256 of the cookie (so
a captured session cannot be replayed either — fixed in 1.4.1; before that, a snapshot taken while a
curator was logged in did carry a usable session). What a snapshot still *is*: the entire content of your
knowledge base plus the list of who has access to it. Treat one snapshot file as equivalent to a full
dump of the instance.

> **The wording is "credential Ken stores", and the narrowing is deliberate — it arrived with stations
> in 1.4.2.** Notebook pages and locker blobs are opaque content Ken does not inspect: it cannot look at
> a blob and know it is a key. Sessions are instructed never to put a token, key or password there, and
> the tool descriptions say so, but that is a documented expectation and **not a control Ken enforces**.
> So the guarantee covers what Ken *hashes*, not what a session *wrote*. If you enable stations, a
> snapshot may contain whatever the notebook and locker were given, verbatim — one more reason for
> `KEN_AGE_RECIPIENT`, and a reason to read a station's locker in the console if you ever suspect one.

> **Inter-session communication (COMM) state is deliberately NOT backed up.** When COMM is enabled it
> keeps its own database and relayed files under `data/comm/`, and neither tier above covers them:
> Litestream replicates one explicitly named path, and the snapshot script copies only the knowledge
> base. That is the design, not an oversight — message traffic is expendable, and losing it costs an
> in-flight conversation rather than knowledge. Do not add `data/comm/` to either tier; if you back up
> the whole volume for other reasons, it is harmless but pointless.
Because there is **no git mirror** (design decision D5), backup is the only
durability path — so it is treated as non-negotiable, in three tiers.

> **Never `cp` a live WAL database** — you get a torn file. Always use
> `ken backup snapshot` (VACUUM INTO) or Litestream, which are WAL-safe.

## Tiers

| Tier | Mechanism | RPO | Purpose |
|---|---|---|---|
| 1 | **Litestream → S3-compatible** (`configs/litestream.yml`) | ~1 s | Primary DR; continuous WAL shipping |
| 2 | **Nightly snapshot** (`scripts/ken-snapshot.sh` via the timer) — encrypted **only if** you set `KEN_AGE_RECIPIENT` | 24 h | Named "give me last Tuesday" restore points |
| 3 | Off-box copy of the snapshots (any file sync) | — | Last resort |

**Encryption is opt-in and OFF by default — on both off-box tiers.** Out of the box, tier 2 writes a
plaintext `.db` at mode `0600` (with a warning in the journal), and tier 1 replicates unencrypted
unless you uncomment the `age:` block in `configs/litestream.yml` or enable bucket SSE. Check what you
have right now — `ls -l /opt/ken/backups`: a bare `.db` is a full plaintext copy of the knowledge base,
`.db.age` is encrypted.

**Why this matters: `0600` protects the file on this box, and nowhere else.** That mode is enforced by
this host's kernel for this filesystem. The file's *contents* travel with every copy; its *protection*
does not. Push a snapshot to S3/B2/R2 and it becomes an object whose ACL you configured. Put it in a
provider disk image, a VM snapshot, an rclone/Syncthing target, or a tarball on your laptop, and it is
readable by whoever reaches that medium — including whoever ends up with the disk after the VPS is
destroyed. **Tier 3 above _is_ an off-box copy**: if you follow this runbook, your snapshots leave the
box by design.

**The deciding question: do your backups ever leave this machine?** If yes — or might — encrypt
(below). If they genuinely never do, plaintext `0600` is a defensible choice, but make it a conscious
one rather than a default you never noticed.

## Make / verify a snapshot manually

```sh
KEN_DB=/opt/ken/data/ken.db \
  ken backup snapshot --out /tmp/ken-$(date -u +%Y%m%dT%H%M%SZ).db   # consistent copy + integrity check
                                                                     # (KEN_DB required: it otherwise
                                                                     #  falls back to a relative ./data)
ken backup verify   /tmp/ken-….db                                  # integrity_check + FK check + FTS5 integrity + MATCH canary + embedding parity + entry count
```

`snapshot` runs `VACUUM INTO` (safe on the live DB) and then verifies the result;
it fails loudly on corruption. The nightly `ken-snapshot.sh` wraps this, age-encrypts
the output, and prunes to `KEN_BACKUP_KEEP` (default 14).

Naming and securing are shared, not duplicated. `scripts/ken-snapshot-lib.sh` is the one
home for the snapshot **stamp** (UTC, `…T…Z` — self-describing and time-sortable) and the
**secure** step (chmod `0600`, then age-encrypt when a recipient is set, removing the
plaintext only after a confirmed encrypt). Both the nightly snapshot **and the installer's
pre-upgrade snapshot** (`backups/pre-upgrade-<UTC-Z>.db`, taken before an upgrade flips the
`current` symlink) go through it, so the two can never drift on timezone, mode, or
encryption. The pre-upgrade snapshot is age-encrypted whenever you have set a recipient for
the nightlies; it is exempt from nightly retention and kept as a rollback point.

## Restore

> **Staging plaintext:** `ken backup snapshot` and `ken backup verify` write `0600` themselves, so a
> hand-run snapshot is owner-only wherever you put it. `age -d` does **not** — it creates its output at
> your umask. When you decrypt to a scratch path below, run `umask 077` first (or decrypt into a `0700`
> directory), so the decrypted knowledge base is not left world-readable in `/tmp`.

**Before you start:** the restore host needs the `age` binary **and** your escrowed private key
(`age.key`) — neither lives on the Ken server. A nightly is `ken-<stamp>.db.age`; the rollback point
the installer takes before an upgrade is `pre-upgrade-<stamp>.db.age` (or `.db` if you have not
enabled encryption).

**From a snapshot (tier 2/3):**
```sh
systemctl stop ken
cd /opt/ken/data
mv ken.db ken.db.pre-restore 2>/dev/null || true     # keep the outgoing file until the new one verifies
rm -f ken.db-wal ken.db-shm                          # stale sidecars belong to the OLD db — drop them FIRST

# encrypted snapshot (umask first: age creates its output at your umask):
umask 077
age -d -i /path/to/age.key -o /tmp/ken-restore.db /opt/ken/backups/ken-<stamp>.db.age
# plaintext snapshot (the default if you never set a recipient):
# cp /opt/ken/backups/ken-<stamp>.db /tmp/ken-restore.db

/opt/ken/current/bin/ken backup verify /tmp/ken-restore.db     # MUST pass BEFORE it goes live
install -o ken -g ken -m 0600 /tmp/ken-restore.db /opt/ken/data/ken.db
systemctl start ken
# only once the service is healthy:
# shred -u /tmp/ken-restore.db /opt/ken/data/ken.db.pre-restore
```

Why the shape: never decrypt straight onto `data/ken.db` — a wrong key or a truncated transfer would
destroy the file you are falling back on, and nothing was kept aside. And `ken.service` runs as
**`ken`** while every command here runs as root, so put the file in place with `install -o ken -g ken`
(or `chown ken:ken`) — otherwise verify passes and the service still will not start.

**From Litestream (tier 1):**
```sh
systemctl stop ken
litestream restore -config /etc/ken/litestream.yml /opt/ken/data/ken.db
chown ken:ken /opt/ken/data/ken.db && chmod 0600 /opt/ken/data/ken.db   # the service runs as the ken user
/opt/ken/current/bin/ken backup verify /opt/ken/data/ken.db
systemctl start ken
```

## Restore verification (mandatory before "good")

`ken backup verify` runs the full set of checks in `store.VerifySnapshot`:
`PRAGMA integrity_check`, `PRAGMA foreign_key_check`, the FTS5 internal
integrity-check on both indexes (`entry_fts` and `entry_code_fts`), a functional
MATCH canary that proves FTS is queryable, and embedding vector-length parity
(`length(vec) != dim*4`) — then returns the entry count. For a fuller check after
a restore, also sanity-check that search works:

```sh
# with the server up and a read token:
curl -s -H "Authorization: Bearer $TOKEN" ... kb_search '{"query":"anything"}'   # returns results
```

The in-DB append-only version history (`entry_version` + `curation_event`) is
included in every snapshot, so a restore brings back the full curated history and
every superseded/rejected/refuted version — nothing is lost that promotion earned.

## Encryption: turning it on

Do these in order. **Step 1 before step 3 is not cosmetic — getting it backwards costs you your
backups.**

**1. Install `age` on the server — first.**

```sh
sudo apt install age      # Debian/Ubuntu
sudo dnf install age      # Fedora; RHEL/Rocky/Alma: enable EPEL first
sudo -u ken age --version # must succeed AS THE SERVICE USER — the timer runs as `ken`
```

Install it system-wide (`/usr/bin` or `/usr/local/bin`). `ken-snapshot.service` runs with
`ProtectHome=true`, so a binary under any `/home` path is invisible to it.

> **This step fails closed.** If `KEN_AGE_RECIPIENT` is set but `age` is missing (or the encrypt
> fails), the snapshot step **deletes the plaintext and keeps nothing** — deliberately, so a snapshot
> you asked to have encrypted is never left behind in the clear instead. The run exits non-zero and
> `ken-snapshot.service` is marked failed, but **there is no snapshot for that night.** Setting the
> recipient before installing `age` does not downgrade you to plaintext backups — it silently gives
> you *no* backups until you notice.

**2. Generate the keypair on your workstation — never on the Ken host.**

```sh
age-keygen -o age.key     # prints the public recipient (age1…)
chmod 600 age.key
age-keygen -y age.key     # re-print the recipient later, straight from the key file
```

`age.key` is the **private identity**. It must never exist on the box it protects — a disk image, a
stolen disk, or your own off-box sync would otherwise carry both the ciphertext and its key. Escrow it
(password manager, sealed secret, offline media) and record *where*. Copy only the `age1…` line to the
server.

> **The one rule: if you lose `age.key`, every encrypted snapshot is permanently unrecoverable.** That
> standing responsibility is the entire cost of turning this on. Escrow it *before* step 3.

**3. Put the PUBLIC recipient on the snapshot unit — as a drop-in.**

```sh
sudo systemctl edit ken-snapshot.service
#   [Service]
#   Environment=KEN_AGE_RECIPIENT=age1…
sudo systemctl daemon-reload
systemctl show ken-snapshot.service -p Environment   # read back what is actually in force
```

The recipient is public — it is safe on the box; it can only encrypt, never decrypt. Two constraints:

- Use `systemctl edit` (a drop-in under `…/ken-snapshot.service.d/`) — **do not edit
  `/etc/systemd/system/ken-snapshot.service` itself.** The installer regenerates that unit on every
  upgrade, so a direct edit is silently overwritten and your nightlies revert to plaintext. Drop-ins
  survive. (The `[Service]` header is required; without it systemd logs "Assignment outside of
  section. Ignoring." and discards the line.)
- It must be a real **`Environment=`** line **on `ken-snapshot.service`**. That unit's environment is
  also what the installer reads to decide whether to encrypt the **pre-upgrade** snapshot, and it does
  not expand `EnvironmentFile=`. A recipient hidden in an `EnvironmentFile=`, or set on `ken.service`,
  still encrypts the nightlies but leaves every pre-upgrade snapshot in plaintext.

**4. Prove it worked — don't wait for 03:30.**

```sh
sudo systemctl start ken-snapshot.service
sudo systemctl status ken-snapshot.service      # must be success, not failed
sudo journalctl -u ken-snapshot.service -n 20 --no-pager
sudo ls -l /opt/ken/backups                     # newest file must end in .db.age
```

| Journal line | Meaning |
|---|---|
| `[ken-snapshot] wrote …/ken-<stamp>.db.age` | Encrypted. Good. |
| `WARNING: KEN_AGE_RECIPIENT not set — snapshot left UNENCRYPTED (0600)` | The drop-in is not being read — recheck step 3. |
| `ERROR: a recipient is set but 'age' is not installed — … no snapshot kept` | Do step 1, then re-run. **You have no snapshot from this run.** |
| `ERROR: age encryption failed — … no snapshot kept` | Bad recipient string, or no write room. Same: nothing was kept. |

**5. Prove the key opens it — the first night, and every quarter.**

A wrong-but-valid recipient (a colleague's key, an old keypair, a key whose private half was never
saved) encrypts happily and forever; age's header gives you no way to tell from the file. Run this on
the machine that holds `age.key` — it needs `age` installed too:

```sh
scp ken@host:/opt/ken/backups/ken-<stamp>.db.age .
umask 077                                                # age honours the umask; ken's own writes do not need it
age -d -i age.key -o /tmp/drill.db ken-<stamp>.db.age    # proves the KEY matches
/opt/ken/current/bin/ken backup verify /tmp/drill.db     # proves the PLAINTEXT is a sound DB
shred -u /tmp/drill.db
```

If `age -d` says `no identity matched any of the recipients`, every snapshot you hold is unopenable:
fix the recipient now and take a fresh one — the old ones are lost. Re-run this drill after any
recipient change. **A key you have never decrypted with is not a backup.**

## Pulling snapshots off the box (tier 3) without root

Tier 3 above is an off-box copy — but by default snapshots are `0600`, owned by the `ken` service
account (which is `nologin`), inside a `0750` directory. **Only root can read them.** That leaves an
archive host needing a root-authorized SSH key, or a root cron job staging copies somewhere readable —
both worse than the thing they enable. (POSIX ACLs are not a way around it: `chmod 0600` zeroes the
group bits, which sets the ACL mask to `---` and neuters any named-user entry, and the snapshot step
re-applies the mode on every run.)

**`KEN_BACKUP_GROUP` is the smaller grant:** name a group that may *read* snapshots, and files become
`0640` owned by it while the directory becomes setgid to it. A dedicated, unprivileged pull account in
that group can then fetch backups over SSH with no root anywhere.

```sh
# 1. a group, and an account the archive host logs in AS
sudo groupadd kenbackup
sudo useradd --system --gid kenbackup --create-home --shell /bin/sh kenpull
```

> **The pull account needs a real shell** (`/bin/sh`), not `/usr/sbin/nologin`: `sshd` runs the
> login shell to execute the forced command, so a `nologin` shell refuses the connection and the
> pull can never run. Lock the account down with the **key** instead of the shell — a forced
> command in `authorized_keys` is what makes it read-only:
>
> ```
> # ~kenpull/.ssh/authorized_keys  (one line)
> command="rrsync -ro /opt/ken/backups",restrict ssh-ed25519 AAAA…archive-host-key
> ```
>
> `restrict` disables port/agent/X11 forwarding and PTY allocation; `rrsync -ro` confines the key
> to *reading* that one directory. The account has a shell, but that key can do exactly one thing.
> Set a password? No — leave it locked (`useradd` with no password does), so the key is the only way in.

```sh
# 2. tell Ken about it — this is all the installer needs
sudo ./install.sh -y --backup-group kenbackup     # remembered on upgrade; no flag needed again

# 3. verify: take a snapshot and check what the pull account can actually read
sudo systemctl start ken-snapshot.service
ls -l /opt/ken/backups          # snapshots 0640, group kenbackup; the dir drwxr-s--- ken:kenbackup
sudo -u kenpull cat /opt/ken/backups/ken-*.db* > /dev/null && echo "pull account can read"
```

> Pass `-y` when re-running the installer for this (as above): without it, an installer run from a
> terminal starts the interactive wizard and will re-prompt for TLS and everything else.

> **Enabling this is a one-time ROOT action, on purpose.** Run the installer by hand, as root, once.
> It is deliberately *not* reachable through the scoped [`ken-upgrade`](../scripts/ken-upgrade)
> wrapper: that wrapper's safety rests on no accepted argument being able to change **who can read
> what**, and a flag that nominates a group allowed to read every database snapshot is exactly that —
> a caller could name its own group. After the one root run, the setting is re-discovered from the
> installed unit on every later upgrade, so an ordinary scoped `ken-upgrade` preserves it indefinitely
> and never needs the flag again.

> **Use the installer flag for this one — not a `systemctl edit` drop-in.** Unlike `KEN_AGE_RECIPIENT`
> (which the snapshot script consumes on its own), the backup group also needs the **directory** made
> setgid, and only the installer does that. Setting `KEN_BACKUP_GROUP` in a drop-in alone leaves the
> directory `0750 ken:ken`, so new snapshots never inherit the group, the service account cannot
> `chgrp` into a group it does not belong to, and every run falls back to `0600` with a warning —
> half-configured, and silently so.

**Initiate the transfer from the archive side** (the archive pulls; Ken never pushes). A compromised
Ken host then cannot reach into the archive and destroy its own off-box copies — which is the property
that makes tier 3 worth having at all.

**It fails safe.** If the group does not exist, or cannot be applied, snapshots keep `0600` and the run
warns — a snapshot is never left readable by the *wrong* group. And leaving `KEN_BACKUP_GROUP` unset
keeps the strict owner-only default exactly as it has always been.

> **This is a real widening, so weigh it:** anyone in that group can read every snapshot. Put nobody in
> it but the pull account. It pairs naturally with encryption — with a recipient set, the group only
> ever sees ciphertext, and the private key stays off the box entirely.

**Where snapshots live** is `KEN_BACKUP_DIR` (installer: `--backup-dir`), honoured by **both** the
nightly timer and the pre-upgrade snapshot — set it and the whole archive moves, rather than splitting.
Both settings are written into `ken-snapshot.service` and re-discovered on upgrade, so re-running the
installer never relocates an existing archive or drops the group.

### Rotating the recipient, and old snapshots

A snapshot can only ever be opened with the key it was encrypted to, and nothing in the file says
which one that is. So:

- **Keep every retired private key** until the last snapshot encrypted to it is gone. Nightlies age
  out in `KEN_BACKUP_KEEP` days (default 14), but `pre-upgrade-*` files are **exempt from retention**
  and can be years old — check `ls /opt/ken/backups` before destroying an old identity.
- `age -d` accepts repeated `-i` flags, so you can hand it every identity you hold:
  `age -d -i age-2026.key -i age-2025.key -o … <file>.age`.
- After rotating, take one snapshot immediately (`systemctl start ken-snapshot.service`) and run the
  drill above against it with the **new** key.

> **Holding an archive of snapshots taken before 1.4.1? Upgrade FIRST, then handle them.** Until 1.4.1
> session ids were stored in the clear, so a pre-1.4.1 snapshot contains cookies that were replayable
> against the live server. Migration `0011` clears the session table — which means **upgrading makes
> every cookie embedded in those old snapshots point at a row that no longer exists.** They become
> inert. Do it in the other order — re-encrypting or shipping the archive off-box first, then upgrading
> — and you spend effort carefully protecting a live credential. (Found in production while preparing
> exactly that migration of an old archive.)

### Retiring plaintext snapshots: the order is the whole procedure

Turning encryption on does not retire the plaintext files already on disk, and deleting them is the
step people get wrong. Do all three, in this order:

1. **Upgrade to 1.4.1 or later.** Migration `0011` clears the session table and defuses the replayable
   cookies in every pre-1.4.1 snapshot, per the note above.
2. **Decrypt and verify one encrypted snapshot off-box**, on the machine holding the private key — the
   drill under *Encryption: turning it on*. Check integrity **and** entry count against what the server
   reported at write time.
3. **Only then destroy the plaintext**, with `shred` rather than `rm`.

**Step 2 before step 3 is the one that matters, and it is not obvious.** Until an encrypted snapshot
has actually been decrypted and read, the encrypted archive is a *hypothesis* — a wrong recipient, a
lost key or a truncated transfer all produce files that look perfectly healthy in `ls`. The plaintext
copies are, at that moment, your only backups known to be readable. Deleting them first means giving
up a proven backup for an unproven one, and you discover which it was during a restore, which is the
worst possible time.

Doing 3 before 1 is the other trap: you carefully shred files whose most sensitive content — a live
session cookie — the upgrade would have made worthless anyway, and you keep the exposure alive in the
meantime.

> Production ran exactly this sequence to retire 18 plaintext snapshots (11 nightly, 7 pre-upgrade,
> oldest 2026-07-21) after enabling encryption. The ordering rule is theirs; it is written here because
> the reasoning is invisible once the outcome looks routine.

**Upgrading from 1.2.x?** Before 1.3.0 the pre-upgrade snapshot was always written in plaintext (with
a local, unmarked timestamp), even on hosts with encrypted nightlies. Those files are full copies of
the knowledge base and retention never prunes them:

```sh
ls -l /opt/ken/backups/pre-upgrade-*.db     # any plain .db here is unencrypted
```

Encrypt the one you want to keep as a rollback point (`age -r age1… -o <f>.db.age <f>.db`, then
`shred -u <f>.db`), delete the rest — and check whether your tier-3 sync already copied them off-box.
