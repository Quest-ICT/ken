# Ken — backup & restore

Ken's **durable** data lives in one SQLite file (`data/ken.db` + its `-wal`/`-shm` sidecars), and
everything below is about that file. COMM keeps a second database and its relayed files under
`data/comm/`, and they are **deliberately not backed up** — that is a design decision with a
rationale, not a gap, and it is stated in full below. Read it before adding `data/comm/` to
anything.

> This sentence said "Ken's data lives in one SQLite file" until 2026-08-24. That was true when it
> was written on 2026-07-26 and false the next day, when `comm.db` arrived — so the opening line of
> the backup document contradicted its own COMM section 43 lines later for a month. It is corrected
> rather than deleted because the correction is the useful part: a document whose first sentence
> under-describes what an instance holds invites exactly one conclusion, and a session reading only
> that line reached it.

**What is in a snapshot.** A snapshot is a byte-complete copy of the knowledge base: every entry and
every superseded revision (including proposals a human never promoted), the full curation history
(`entry_version` + `curation_event`), embeddings, the human curator accounts, and the agent/MCP token
records — **and, since the station vault shipped, any credential a session deliberately stored there,
in the clear.**

**Every credential Ken MINTS is stored as a verifier and is not replayable:** passwords are Argon2id
hashes, API-token secrets are stored hashed (station keys included), and web-session ids are stored as a
SHA-256 of the cookie (so a captured session cannot be replayed either — fixed in 1.4.1; before that, a
snapshot taken while a curator was logged in did carry a usable session).

**Every credential a session PUTS IN THE VAULT is stored as plaintext and is replayable by anyone
holding the file.** That is the vault's stated design, not an oversight: encrypting it would need a key,
the key would live in the same database, and lock and key would travel together in every snapshot — so
the encryption would protect nobody who can read the file while inviting you to relax a control that was
never there. The confidentiality boundary for those values is **this file and the machine it sits on**.

What a snapshot still *is*: the entire content of your knowledge base, the list of who has access to it,
and whatever your sessions have put in their vaults. Treat one snapshot file as equivalent to a full
dump of the instance **plus its credentials**.

> **The wording is "credential Ken stores", and the narrowing is deliberate — it arrived with stations
> in 1.4.2.** Notebook pages and locker blobs are opaque content Ken does not inspect: it cannot look at
> a blob and know it is a key. Sessions are instructed never to put a token, key or password there, and
> the tool descriptions say so, but that is a documented expectation and **not a control Ken enforces**.
> So the guarantee covers what Ken *hashes*, not what a session *wrote*. Stations are core and on by
> default, so a snapshot may contain whatever the notebook and locker were given, verbatim — one more
> reason to read a station's locker in the console if you ever
> suspect one.
>
> **The vault made this explicit rather than incidental.** Until it shipped, every secret in ken.db was a
> verifier, and a credential landing in a snapshot was a session ignoring its instructions. The vault is
> the place a session is *told* to put credentials, so a deployment using it has plaintext secrets in
> every snapshot **by design**. `/stations` lists what each vault holds — names, sizes and read counts,
> never values — so you can see what a snapshot of yours would carry without revealing anything to find
> out. If that is not a trade you want, the answer is not to use the vault; there is no configuration
> that makes those values safe inside a file somebody else can read.

> **Inter-session communication (COMM) state is deliberately NOT backed up.** COMM is on by default; it
> keeps its own database and relayed files under `data/comm/`, and neither tier above covers them:
> Litestream replicates one explicitly named path, and the snapshot script copies only the knowledge
> base. That is the design, not an oversight — message traffic is expendable, and losing it costs an
> in-flight conversation rather than knowledge. Do not add `data/comm/` to either tier; if you back up
> the whole volume for other reasons, it is harmless but pointless.
Because there is **no git mirror** (design decision D5), backup is the only
durability path — so it is treated as non-negotiable, in three tiers.

> **Never `cp` a live WAL database** — you get a torn file. Always use
> `ken backup snapshot` (VACUUM INTO) or Litestream, which are WAL-safe.

## THE VAULT KEY IS NOT IN THE BACKUP, AND A SNAPSHOT WITHOUT IT IS UNREADABLE

**Since 3.32.0, station-vault secrets are ENCRYPTED in `ken.db`.** The key is a 32-byte file,
`data/vault.key`, mode `0600`, sitting beside the database next to the existing `dedup.key`.

**`scripts/ken-snapshot.sh` copies only `ken.db`, so the key is outside every snapshot by
construction rather than by an exclusion rule someone has to remember.** That is the entire
security argument for the scheme: the copies that leave this box carry ciphertext, and the thing
that opens them does not travel with them.

> ### *** IT IS ALSO THE ENTIRE FAILURE MODE. ***
>
> **Restore `ken.db` onto a machine without the matching `vault.key` and every vault secret is
> gone.** Not corrupted, not recoverable with effort — the plaintext does not exist anywhere else.
> The restore will otherwise look completely successful: the database opens, every entry, task,
> note and message is intact, the row counts match, and `station_vault` still lists every secret
> by name, size and digest. Only the values are unrecoverable, and only when something tries to
> read one.
>
> **So back the key up SEPARATELY, and never into the same place as the snapshots.** A key stored
> beside the ciphertext protects nobody who can read the file — that is the reasoning migration
> 0016 used to refuse encryption in the first place, and putting `vault.key` into the backup set
> re-creates exactly the situation it was refusing.
>
> ```
> # copy it somewhere that is NOT the snapshot destination — a password manager,
> # an offline medium, a different custodian. It never changes, so this is once.
> sudo install -m 0600 /opt/ken/data/vault.key /path/to/somewhere/else/ken-vault.key
> sudo sha256sum /opt/ken/data/vault.key      # record this; it is how you check a restored copy
> ```
>
> **Verify the copy by its digest, not by its length.** A truncated key file is refused at
> startup — Ken will not run rather than guess — but a *wrong* 32-byte key starts fine and fails
> only when a secret is read, with an error naming `vault.key`.

**What this protects, stated exactly, because overstating it is worse than not having it:** copies
of the database that leave the host. It does **not** protect against someone with root here, who
can read the key beside the database and can read the running process's memory regardless. And a
**low-entropy** secret remains guessable from `station_vault.sha256`, which is computed over the
plaintext so the console can identify a secret and travels in the same snapshot. The vault is for
high-entropy credentials; a short passphrase stored here is protected far less than it appears.

**Secrets written before 3.32.0 stay plaintext until they are next written.** A value with no
`kv1:` prefix is read back as-is, which is what lets an existing deployment upgrade with no
migration — and it means an older snapshot, or a row nobody has touched, is still readable without
the key. Rewrite anything that matters.

## Tiers

| Tier | Mechanism | RPO | Purpose |
|---|---|---|---|
| 1 | **Litestream → S3-compatible** (`configs/litestream.yml`) | ~1 s | Primary DR; continuous WAL shipping |
| 2 | **Nightly snapshot** (`scripts/ken-snapshot.sh` via the timer) — compressed, `0600`, not encrypted | 24 h | Named "give me last Tuesday" restore points |
| 3 | Off-box copy of the snapshots (any file sync) | — | Last resort |

**KEN DOES NOT ENCRYPT ANYTHING. Tier 2 cannot be encrypted by Ken at all, and there is no setting
that changes that.** 2.0.0 retired `KEN_AGE_RECIPIENT` and `scripts/ken-snapshot-lib.sh` says so in
its own comment: *"IT NO LONGER ENCRYPTS … Ken writes a compressed, unencrypted snapshot at 0600 and
stops."* Setting the retired variable produces a NOTE in the journal and a plaintext snapshot,
which is written and KEPT.

**Tier 1 can be encrypted, by Litestream rather than by Ken** — uncomment the `age:` block in
`configs/litestream.yml`, or enable bucket SSE. That is the only encryption option in this system,
and it protects the continuous replica, not the nightly snapshot.

> **THIS PARAGRAPH SAID "encryption is opt-in and OFF by default — on both off-box tiers" UNTIL
> 2026-08-25**, and INSTALL.md, `scripts/install.sh` and `deploy/ken-snapshot.service` each carried a
> procedure for turning on a control that has not existed since 2.0.0 — down to a numbered step
> claiming the snapshot "fails closed" and keeps nothing if `age` is missing. It does not; it writes
> the plaintext and keeps it. **An operator who followed that procedure escrowed a key, believed
> their off-box snapshots were ciphertext, and had plaintext** — of the whole knowledge base, the
> curator accounts, and every station vault secret. Found by an audit for exactly this class:
> *text asserting a control that does not exist.* The worst instance of it yet, because the control
> was encryption.

Check what you have right now — `ls -l /opt/ken/backups`: a bare `.db` is a full plaintext copy of the
knowledge base, `.db.gz` is compressed plaintext; `ken backup verify` reads either directly.

**Why this matters: `0600` protects the file on this box, and nowhere else.** That mode is enforced by
this host's kernel for this filesystem. The file's *contents* travel with every copy; its *protection*
does not. Push a snapshot to S3/B2/R2 and it becomes an object whose ACL you configured. Put it in a
provider disk image, a VM snapshot, an rclone/Syncthing target, or a tarball on your laptop, and it is
readable by whoever reaches that medium — including whoever ends up with the disk after the VPS is
destroyed. **Tier 3 above _is_ an off-box copy**: if you follow this runbook, your snapshots leave the
box by design.

**The deciding question: do your backups ever leave this machine?** If yes — or might — **the
encryption is yours to add, outside Ken.** Pipe the snapshot through `age`, `gpg` or your sync tool's
own encryption in whatever moves it off the box; or rely on tier 1 with Litestream's `age:` block.
What you cannot do is switch something on inside Ken, because there is nothing there to switch. If
your backups genuinely never leave, plaintext `0600` is a defensible choice — but make it a conscious
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
it fails loudly on corruption. The nightly `ken-snapshot.sh` wraps this, compresses
the output, and prunes to `KEN_BACKUP_KEEP` (default 14).

**Pre-upgrade rollback points are pruned separately, and until 1.8.0 they were not pruned at all.**
The nightly retention globs `ken-*`, which cannot match `pre-upgrade-*` — they share no prefix — so
every upgrade left one behind permanently. On the deployment that found it, nine had accumulated:
19.6 MB, 30% of the whole archive, the oldest dating from the day the box was built.

They now survive if they are among the newest `KEEP_PRE_UPGRADE` (default 3) **or** younger than
`KEEP_PRE_UPGRADE_DAYS` (default 7) — whichever keeps more. Both floors are needed and both failure
modes were measured on a real deployment inside one thirteen-day window: a count alone fails during a
BURST (four upgrades in a day evicts the point taken before that day's work began, which is the one
you want when that day is what broke things), and an age bound alone fails during a DROUGHT (255 hours
passed with no upgrade, which would have left none at all).

Naming and securing are shared, not duplicated. `scripts/ken-snapshot-lib.sh` is the one
home for the snapshot **stamp** (UTC, `…T…Z` — self-describing and time-sortable) and the
**secure** step (chmod `0600`, and the backup group when one is set — there is no
encryption step for it to sequence around; that clause described a 1.x code path). Both the nightly snapshot **and the installer's
pre-upgrade snapshot** (`backups/pre-upgrade-<UTC-Z>.db`, taken before an upgrade flips the
`current` symlink) go through it, so the two can never drift on timezone, mode, or
compression. The pre-upgrade snapshot is compressed exactly like a nightly, for
the nightlies; it is exempt from nightly retention and kept as a rollback point.

**One historical exception, worth knowing before reaching for an old file:** rollback points written
by 1.7.0/2.0.0-era installers can be UNCOMPRESSED despite the `.db.gz` name. See the warning under
*Restore*; the recipe there handles either shape, and `ken backup verify` never cared.

## Restore

> **Staging plaintext:** `ken backup snapshot` and `ken backup verify` write `0600` themselves, so a
> hand-run snapshot is owner-only wherever you put it. `gunzip` does **not** — it creates its output at
> your umask. When you decrypt to a scratch path below, run `umask 077` first (or decrypt into a `0700`
> directory), so the decrypted knowledge base is not left world-readable in `/tmp`.

**Before you start:** a nightly is `ken-<stamp>.db.gz`; the rollback point the installer takes before
an upgrade is `pre-upgrade-<stamp>.db.gz`. `ken backup verify` reads either directly — it detects
compression from the file's own **magic bytes**, so a snapshot that was renamed, or handed over
without an extension, still verifies.

> **DO NOT TRUST THE `.gz` IN THE NAME.** `ken-prod-ops` found a live
> `pre-upgrade-20260811T163126Z.db.gz` on a production box that is **uncompressed SQLite** — `file(1)`
> says "SQLite 3.x database", `gzip -t` exits 1, and the first sixteen bytes are
> `53 51 4c 69 74 65 20 66 6f 72 6d 61 74 20 33 00`. The data is intact and verifies; only the name
> lies. It comes from a 1.7.0/2.0.0-era installer — the current one does not reproduce it, and the
> sibling written on the same box today decompresses cleanly — but the artifact is real, on disk, and
> after the next prune it may be one of the few rollback points still held.
>
> An earlier version of this section told you to run `gunzip -c` on it, which fails. Use the recipe
> below: it asks the FILE what it is, exactly as `verify` does, rather than believing the extension.

```bash
# Decompress only if it IS compressed. Works on either shape, and on a file with no
# extension at all. (verify does not require this step — it reads either directly.)
umask 077
src=/opt/ken/backups/ken-<stamp>.db.gz
if gzip -t "$src" 2>/dev/null; then gunzip -c "$src" > /tmp/ken-restore.db
else cp "$src" /tmp/ken-restore.db; fi
/opt/ken/current/bin/ken backup verify /tmp/ken-restore.db
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

> **Use the installer flag for this one — not a `systemctl edit` drop-in.** Unlike the other snapshot
> variables, which the script consumes on its own, the backup group also needs the **directory** made
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

### What the operator owns

Ken writes a compressed snapshot at `0600` and stops. **Everything after that is yours:**
transport, destination, retention there, and who can read it. Ken makes no attempt to
protect the file once it leaves the box, and does not encrypt it.

That is a deliberate scope, not an omission. Ken is built to hold a knowledge base and
its working state, not national-security material, and encryption on this box bought
little while costing a great deal: it made the archive **incompressible and
undedupable**, since ciphertext shares no bytes with yesterday's ciphertext. Removing it
is what allowed the 68% saving above to exist at all.

**What a stolen snapshot yields:** every entry and its full revision history, every
station notebook, task and locker, **and every station vault in plaintext**. Passwords,
tokens and endpoint secrets that KEN mints are stored as verifiers and are **not**
replayable; credentials a session stored in a vault are, because that is what a vault is
for. Treat the file as though it were the knowledge it contains **and the credentials it
carries**, because it is both.

**A reasonable path, offered as an example rather than a requirement:** pull it over a
private link to a host you control, keep it on a filesystem only that host's operator can
read, and verify a restore periodically rather than assuming one works.
