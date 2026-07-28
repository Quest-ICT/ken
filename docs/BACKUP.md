# Ken — backup & restore

Ken's data lives in one SQLite file (`data/ken.db` + its `-wal`/`-shm` sidecars).

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
| 2 | **Nightly encrypted snapshot** (`scripts/ken-snapshot.sh` via the timer) | 24 h | Named "give me last Tuesday" restore points |
| 3 | Off-box copy of the snapshots (any file sync) | — | Last resort |

Everything that leaves the box is **age-encrypted** (tier 1 via litestream's age
option or bucket SSE; tier 2 via `KEN_AGE_RECIPIENT`).

## Make / verify a snapshot manually

```sh
ken backup snapshot --out /tmp/ken-$(date -u +%Y%m%dT%H%M%SZ).db   # consistent copy + integrity check
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

**From a snapshot (tier 2/3):**
```sh
systemctl stop ken
age -d -i /path/to/age.key -o /opt/ken/data/ken.db  /path/to/ken-….db.age   # decrypt into place
ken backup verify /opt/ken/data/ken.db                                       # MUST pass before starting
rm -f /opt/ken/data/ken.db-wal /opt/ken/data/ken.db-shm                      # drop stale sidecars
systemctl start ken
```

**From Litestream (tier 1):**
```sh
systemctl stop ken
litestream restore -config /etc/ken/litestream.yml /opt/ken/data/ken.db
ken backup verify /opt/ken/data/ken.db
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

## Encryption keys

Generate an age keypair once, keep the **private** key off the server (or in a
sealed secret), put the **public** recipient in `KEN_AGE_RECIPIENT`:

```sh
age-keygen -o age.key            # prints the public recipient (age1...); store age.key safely OFF the box
```

Losing the private key means the encrypted snapshots are unrecoverable — escrow it.
