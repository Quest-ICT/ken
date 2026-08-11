-- The station VAULT — the half of a working identity the locker is forbidden to hold.
--
-- The locker's own tool text says "NEVER put a token, key or password here", which was
-- correct and left a station with nowhere to put one at all. Vlad authorised a vault
-- alongside the locker (not replacing it) on 2026-08-10, under an operating model he
-- stated rather than one inferred: ONE human per instance, station services are private
-- session assets, and cross-session access is untidy rather than dangerous because the
-- same human owns every session.
--
-- WHY THE VALUES ARE STORED IN PLAINTEXT, WHICH IS THE DECISION MOST WORTH ARGUING WITH
--
-- Encrypting them here would need a key, and the key would live in this same database.
-- Lock and key then travel together in every backup, so the encryption protects against
-- nobody who can read the file — it is theatre, and theatre in a security store is worse
-- than an honest absence because it invites the operator to relax a control that is not
-- there. Vlad's instruction was explicit after age-encrypted snapshots cost real
-- production pain: security is not a functional concern of Ken, and he would rather have
-- "a non-encrypted database up to the backup point".
--
-- So the confidentiality boundary is stated instead of simulated: it is the HOST and the
-- BACKUP, not this table. docs/BACKUP.md is corrected in the same change — its promise
-- that "no credential Ken STORES is replayable" becomes false the day this ships, and an
-- operator designing a backup chain around the old sentence must be told, not left to
-- discover it.
--
-- WHY EVERY WRITE IS REVERSIBLE
--
-- Vlad's condition on secrets living here was "not a problem as long as it does not
-- modify them or at least it is reversible". station_locker_delete is destructive today;
-- the vault must not be. So an update pushes the previous value into history and a delete
-- is a tombstone, never a DELETE. A session that overwrites the wrong name loses nothing
-- a human cannot put back from the console.
--
-- WHY READS ARE AUDITED
--
-- A secret store whose reads are invisible cannot answer the only question worth asking
-- after something leaks: who saw it, and when. The log is bounded (see the store's
-- limits) and the console states what it dropped — an unbounded audit table would grow
-- with usage forever, and a SILENTLY bounded one is the station-notebook defect again,
-- where a page lost its first seventeen revisions and no surface said so.
BEGIN;

-- Current state. One live row per (station, name); a tombstone keeps its row.
CREATE TABLE station_vault (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  secret        TEXT    NOT NULL,
  -- What this is and where it came from. The console shows it in place of the value, so
  -- a human can identify a secret without revealing one.
  note          TEXT    NOT NULL DEFAULT '',
  size_bytes    INTEGER NOT NULL,
  -- Lets the console and the audit trail compare two values, and lets a session confirm
  -- it stored what it meant to, without either surface handling the secret itself.
  sha256        TEXT    NOT NULL,
  rev           INTEGER NOT NULL DEFAULT 1,
  -- The TRUE number of times this value has been handed out, kept here because the read
  -- log below is bounded. Without it the console could only ever say "here are the last
  -- N reads" and never "of how many" — which is the exact shape of the notebook's silent
  -- pruning: a surface that cannot distinguish "few reads" from "many reads, most
  -- dropped".
  read_count    INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updated_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- Soft delete. Set, never DELETEd, so the value is recoverable from history.
  deleted_at    TEXT,
  updated_by_token_id TEXT,
  updated_by_actor_id INTEGER REFERENCES actor(id),
  UNIQUE (station_id, name)
);
-- Deleted rows keep their name, so the uniqueness above covers tombstones too: putting
-- a name back REVIVES its row rather than colliding with it, which is what keeps the
-- history of that name in one chain instead of forking it.

-- Every superseded value, append-only. This is what makes a write reversible.
CREATE TABLE station_vault_history (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  secret        TEXT    NOT NULL,
  note          TEXT    NOT NULL DEFAULT '',
  size_bytes    INTEGER NOT NULL,
  sha256        TEXT    NOT NULL,
  rev           INTEGER NOT NULL,
  -- 'updated' or 'deleted' — why this value stopped being current.
  reason        TEXT    NOT NULL CHECK (reason IN ('updated','deleted')),
  replaced_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  replaced_by_token_id TEXT,
  replaced_by_actor_id INTEGER REFERENCES actor(id)
);
CREATE INDEX idx_station_vault_history ON station_vault_history(station_id, name, rev DESC);

-- Read audit. One row per value actually handed out, by a session or by the console.
CREATE TABLE station_vault_read (
  id            INTEGER PRIMARY KEY,
  station_id    TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  name          TEXT    NOT NULL,
  -- 'station' (a station_vault_get call) or 'console' (a human clicked reveal). Both are
  -- reads of the same value and both belong in one trail; the surface is what differs.
  via           TEXT    NOT NULL CHECK (via IN ('station','console')),
  read_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  by_token_id   TEXT,
  by_actor_id   INTEGER REFERENCES actor(id)
);
CREATE INDEX idx_station_vault_read ON station_vault_read(station_id, read_at DESC);

INSERT INTO schema_migration(version) VALUES (16);

COMMIT;
