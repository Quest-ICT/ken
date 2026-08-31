# Upgrading the database

**Ken does not migrate databases.** It creates one from a single schema file when the file is
empty, and otherwise reads the recorded schema version and **refuses to start** if it is not the
version that binary requires. Moving an existing database from one version to the next is a
deliberate act you perform, with stock `sqlite3`, while Ken is stopped.

This document is the whole procedure. If Ken refused to start and sent you here, [start at the
refusal](#what-the-refusal-looks-like).

## Why it works this way

A migration runner is code that rewrites data nobody is watching, on a schedule set by whoever
restarts the service. Ken is designed to be installed **fresh**, so the runner existed almost
entirely for a case that rarely arises — and when it did arise, it was the least trustworthy code
in the tree: three adversarial audit rounds before 4.0.0 found the same migration broken three
separate times, each in a way a fully green test suite could not see, because every unit test opens
a *fresh* database and the data-moving arms of a migration copy zero rows.

Moving the rewrite out of the server makes it something you run on purpose, read the output of, and
verify with the same `sqlite3` you already have. That last part is load-bearing: `ken.db` and
`comm.db` are plain SQLite files, readable read-only from outside the process, and every upgrade
verification in this document depends on that.

## Before you touch anything

**`ken.db` is durable and irreplaceable** — the knowledge base and every station's notebook, tasks,
locker and vault. **`comm.db` is expendable by design** and is in no backup tier: if it is ever
unrecoverable, the supported answer is to delete it and start Ken, which rebuilds messaging empty
and affects nothing durable.

Take a snapshot and **verify it**. An unverified snapshot is a belief, not a backup:

```
ken backup snapshot
ken backup verify
```

## The procedure

```
sudo systemctl stop ken
sqlite3 /opt/ken/data/comm/comm.db < upgrade/comm-4.x-to-5.0.0.sql
sudo systemctl start ken
```

Run only the scripts between the version you are on and the version you are going to, in order.
Each names the versions it moves in its first line.

**Then prove it, rather than assuming the script ran clean.** Every upgrade script ends with the
three commands to run; they are worth running even when the script printed nothing:

```
sqlite3 /opt/ken/data/comm/comm.db "SELECT MAX(version) FROM schema_migration;"   -- the new version
sqlite3 /opt/ken/data/comm/comm.db "PRAGMA integrity_check;"                      -- 'ok'
sqlite3 /opt/ken/data/comm/comm.db "PRAGMA foreign_key_check;"                    -- NOTHING at all
```

Ken runs the foreign-key check itself on every boot and says so in the log, so a clean start is a
second opinion:

```
schema: comm.db at version 22, as required
schema: comm.db foreign_key_check clean
```

## What the refusal looks like

```
comm.db is at schema version 21 and this binary requires 22 — the database is OLDER than this
binary. Stop Ken, run the upgrade for this database (see docs/UPGRADING-THE-DATABASE.md), and
start it again. Ken does not migrate databases: it creates one and checks the rest, so nothing
here will change on a restart.
```

**A refusal is the system working.** The alternative — which Ken did until 5.0.0 — is a binary
opening a database whose shape it does not know: measured before 4.0.0, the v3.42.0 binary booted
against a 4.0.0 database with a completely ordinary startup log and then returned 500s on a table
that no longer existed. "Unsupported" was indistinguishable from "fine" at startup.

The refusal names the direction, because the remedy differs:

| It says | You are | Do |
|---|---|---|
| `OLDER than this binary` | on an old database, new Ken | run the upgrade scripts, in order |
| `NEWER than this binary` | on a new database, old Ken | **do not run anything.** Restore the pre-upgrade snapshot, or run the newer Ken |

There is no upgrade script for the second row and there will not be one. Going backwards is what
the snapshot is for.

## If an upgrade fails part-way

Every script is one transaction: it either applies or it does not, and a failure leaves the
database as it was. Read the error, fix the cause, run it again.

For **comm.db** you always have the other exit: stop Ken, delete `comm.db` and its `-wal`/`-shm`
files, and start. You lose in-flight mail and nothing else.

For **ken.db** there is no such exit, which is why the snapshot above is not optional.

## Scripts

| From | To | Script | What it does |
|---|---|---|---|
| comm 21 (4.x) | comm 22 (5.0.0) | `upgrade/comm-4.x-to-5.0.0.sql` | drops the channel table and its columns, rebuilds the file-offer idempotency index on `scope_id`, drops the mailbox secret and binding columns |

**ken.db does not change in 5.0.0.** A 4.x `ken.db` is already at version 26, which is what 5.0.0
requires, so there is nothing to run and Ken starts against it untouched.

## For the curious: what guarantees these agree

`schema/comm.sql` and `upgrade/comm-4.x-to-5.0.0.sql` are two routes to one shape — one for a fresh
install, one for an existing database. Nothing but a test forces them to agree, and if they drift,
an upgraded deployment runs on a schema no fresh install was ever tested against **while every
version check still passes**.

So `TestAnUpgradedDatabaseMatchesAFreshOne` builds the previous release, upgrades its database with
the shipped script, creates a fresh one with the current binary, and compares the full DDL of both.
It is the same check ken-prod-ops runs from outside with a hash, and for the same reason.
