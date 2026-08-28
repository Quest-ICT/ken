-- ============================================================================
-- Ken — migration 0025: links are born active, and `revoked` becomes `suspended`
-- ============================================================================
-- Rule 4 is SUSPENDED for this wave by Vlad's decision — it ships as one breaking release — so
-- this migration travels with the rest rather than alone.
--
-- *** WHY: BOTH HUMAN GATES ON COMM ARE REMOVED. ***
--
-- Vlad's decision: comm is available immediately to any session holding the connector, exactly
-- like the station surface. Link approval and the pairing-code requirement both go, and a link is
-- created automatically on first contact.
--
-- HIS REASONING IS KEN'S OWN DESIGN DOC. IDENTITY.md §4: "single-user makes that sufficient…
-- **There is no other tenant to protect against**." The approval was guarding a threat model the
-- design explicitly denies.
--
-- *** LINKS STILL EXIST, AND THAT IS ALSO HIS DECISION. *** ken-prod-ops put both options to him —
-- auto-approve, or delete the concept — and he took auto-approve specifically to keep the audit
-- trail and a surgical off-switch. A link still gets a row and still records who spoke to whom and
-- since when. It is simply born `active`.
--
-- *** 'revoked' LEAVES THE VOCABULARY. *** His words: "'suspend' button instead of revoke button
-- (I want to be able to 'resume' it). 'revoke' concept is out of the table." Terminal states are
-- the thing he does not want for a relationship between two of his own stations.
--
--   active      live
--   dormant     AUTOMATIC: a station was archived. Reverses when it is unarchived.
--   suspended   NEW, HUMAN: turned off deliberately. Reverses on Resume.
--   (pending)   never existed as a state here — approval lived in station_request
--   (revoked)   REMOVED
--
-- *** THE EXISTING revoked ROW IS DELETED, NOT MIGRATED. ***
--
-- Production holds exactly one, ken-prod-ops <-> proxmox-servers. Prod proposed keeping it as a
-- historical terminal value nothing new can enter. Vlad's answer: "I don't want historical shit.
-- What is not used, gets deleted." Migrating it to `suspended` would have been worse than either —
-- it would silently make a terminated relationship resumable, which is a different decision from
-- the one he made.
--
-- *** ON THE HAZARD ken-prod-ops FLAGGED, WHICH IS REAL IN GENERAL AND ABSENT HERE. ***
--
-- It warned that `dormant` and `suspended` are two INDEPENDENT reasons to be not-active and that
-- one column cannot hold both: archive a suspended link, unarchive it, and the unarchive silently
-- resumes something a human turned off.
--
-- That is true of an UNCONDITIONAL write and false of this one. ArchiveStation moves links with
-- `UPDATE station_link SET state=? WHERE state=? AND (...)` — guarded on the SOURCE state — so
-- archiving only ever touches `active` rows and unarchiving only ever touches `dormant` ones. A
-- `suspended` link is invisible to both. The column is sufficient BECAUSE the transitions are
-- guarded, which is a fact about that statement rather than about the schema, so it is asserted by
-- a test rather than trusted: see TestSuspendSurvivesAnArchiveAndUnarchive.

BEGIN;

-- THE ROW GOES FIRST, while `revoked` is still a legal value. After the rebuild the CHECK would
-- refuse to copy it, and the migration would fail on data rather than on a decision.
DELETE FROM station_link WHERE state = 'revoked';

CREATE TABLE station_link_new (
  id                   INTEGER PRIMARY KEY,
  link_id              TEXT    NOT NULL UNIQUE,
  station_a            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  station_b            TEXT    NOT NULL REFERENCES station(station_id) ON DELETE CASCADE,
  -- 'active' (live), 'dormant' (automatic — a station is archived), 'suspended' (a human turned
  -- it off and can turn it back on). There is no terminal state.
  state                TEXT    NOT NULL DEFAULT 'active'
                         CHECK (state IN ('active','dormant','suspended')),
  -- Kept, and still NOT NULL: a link records WHO allowed it. Auto-created links carry the actor
  -- whose grant the first contact arrived under, which is the same human either way — one
  -- instance, one human — and keeps the audit column meaningful rather than nullable.
  approved_by_actor_id INTEGER NOT NULL REFERENCES actor(id),
  approved_at          TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  -- suspended_at replaces revoked_at: same shape, reversible meaning. NULL whenever the link is
  -- not suspended, so "when was this turned off" has one answer and clearing it is the resume.
  suspended_at         TEXT,
  CHECK (station_a < station_b)
);

INSERT INTO station_link_new (id, link_id, station_a, station_b, state, approved_by_actor_id, approved_at)
SELECT id, link_id, station_a, station_b, state, approved_by_actor_id, approved_at
FROM station_link;

DROP TABLE station_link;

ALTER TABLE station_link_new RENAME TO station_link;

-- Recreated by hand: an index does not follow a RENAME, and the pair lookup is what every send
-- and every directory build reads.
CREATE UNIQUE INDEX idx_station_link_pair ON station_link(station_a, station_b);

INSERT INTO schema_migration(version) VALUES (25);

COMMIT;
