-- ============================================================================
-- Ken — migration 0024: `station_request.kind` learns a third value, 'room'
-- ============================================================================
-- SHIPS ALONE WITH ITS CODE under Rule 4. One table rebuilt to widen one CHECK; no column
-- added or removed, no type changed, every existing row copied verbatim.
--
-- *** WHY: A DECISION VLAD MADE ON 2026-08-06 WAS DECLINED IN CODE ON GROUNDS THAT ARE NOW FALSE. ***
--
-- His words, from station task t-CtGY9i1q the same day:
--
--   "(3) ROOM CREATION: sessions may REQUEST, human approves — NOT the humans-only option I
--    recommended. This costs a new request table: station_request.kind is CHECK-constrained to
--    ('station','link') and SQLite cannot alter a CHECK in place. Same shape as the curation
--    gate, which is the right instinct — the agent proposes, the human promotes."
--
-- He decided it OVERRIDING the session's own recommendation, and he decided it having been told
-- the schema cost in the same sentence. Six days later 0017_comm_rooms.sql declined to build it,
-- citing that cost. It was never reversed; it was quietly not done.
--
-- *** THE COST OBJECTION WAS DEFENSIBLE THEN AND IS MEASURABLY FALSE NOW. ***
--
-- When 0017 was written (2026-08-12) ken.db's migration runner had NO foreign-key handling, which
-- made any ken.db table rebuild genuinely dangerous. That precondition was removed 2026-08-20.
-- Then on 2026-08-26 migration 0022 performed exactly this operation — widening a CHECK on a
-- station table by rebuild — in about 19 lines, and treated it as routine. Measured against a
-- fully-migrated ken.db, this rebuild takes 2.53 ms, preserves every row, and passes
-- foreign_key_check and integrity_check. It does not scale into anything either: 0 rows 1.4 ms,
-- 100 rows 1.6 ms, 10,000 rows 18.9 ms.
--
-- station_request is the CHEAP rebuild shape, and that was checked rather than assumed: NOTHING in
-- either database points a foreign key at it, no trigger reads it, no view names it. Its only
-- dependent objects are the automatic index behind `request_id UNIQUE` and one hand-made index.
--
-- *** WHAT THIS DELIBERATELY DOES NOT DO: LET AN AGENT DECIDE WHO IS IN A ROOM. ***
--
-- 0017's SECOND reason is a principle, was stated as independently sufficient, and survives every
-- finding above untouched: "a room is a set of stations a human decided should talk to each other.
-- There is no version of that decision an agent should be making for itself."
--
-- That is an argument against an agent CREATING a room. It is not an argument against an agent
-- ASKING, and Ken already ships two request tools that coexist with human-only creation. So the
-- shape built on top of this column is narrow on purpose:
--
--   a room request names NO OTHER STATION. It carries a reason and a non-binding name hint.
--   The human creates the room and picks every member at the console, exactly as today.
--
-- Naming no station is not a simplification, it is the safety property. It keeps the human as the
-- sole decider of membership — the principle above, intact — and it avoids the enumeration oracle
-- that `station_link_request` needed StationByNameVisibleTo to close, because there is no station
-- name in the request to resolve. `reason` is already shown ONLY to the human and never delivered
-- to a target before approval (0012's own note, S9), so the one-shot-message-channel hazard does
-- not reopen either.
--
-- COLUMNS REUSED AS-IS, none added:
--   from_station   the asking station (already how link requests work)
--   to_station     NULL — a room request is about no one else. Already nullable.
--   name_hint      the suggested room name. ALREADY documented NON-BINDING, "the human types the
--                  name", which is exactly the semantics a room name needs.
--   reason         why. Already human-only.
--   prompted_by_peer_traffic  already computed and badged; a room ask talked into existence by a
--                  peer is exactly as worth flagging as a link ask.

BEGIN;

CREATE TABLE station_request_new (
  id                     INTEGER PRIMARY KEY,
  request_id             TEXT    NOT NULL UNIQUE,
  -- 'station' (an unknown session asking for a post), 'link' (a station asking to be joined to
  -- another), or 'room' (a station asking a human to create a room — naming no members).
  kind                   TEXT    NOT NULL CHECK (kind IN ('station','link','room')),
  from_station           TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  from_token_id          TEXT    NOT NULL,        -- audit string; may dangle by design
  to_station             TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  name_hint              TEXT,                    -- NON-BINDING; the human types the name
  purpose                TEXT    NOT NULL DEFAULT '',
  reason                 TEXT    NOT NULL DEFAULT '',
  -- The transitive path: A cannot make a channel but can talk B into asking for one to
  -- C, and B's request then reaches the human looking like B's own idea. Computed like
  -- entry_version.via_comm, badged in the console (S9).
  prompted_by_peer_traffic INTEGER,
  state                  TEXT    NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending','approved','denied','expired')),
  created_at             TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  decided_at             TEXT,
  decided_by_actor_id    INTEGER REFERENCES actor(id),
  decision_reason        TEXT
);

INSERT INTO station_request_new (id, request_id, kind, from_station, from_token_id, to_station,
                                 name_hint, purpose, reason, prompted_by_peer_traffic, state,
                                 created_at, decided_at, decided_by_actor_id, decision_reason)
SELECT id, request_id, kind, from_station, from_token_id, to_station,
       name_hint, purpose, reason, prompted_by_peer_traffic, state,
       created_at, decided_at, decided_by_actor_id, decision_reason
FROM station_request;

DROP TABLE station_request;

ALTER TABLE station_request_new RENAME TO station_request;

-- Recreated by hand: an index does not follow a RENAME onto the new table under the name the old
-- one used, and the console's pending-request query is exactly (state, created_at). The UNIQUE on
-- request_id needs no line here — its automatic index is part of the table definition above.
CREATE INDEX idx_station_request_pending ON station_request(state, created_at);

INSERT INTO schema_migration(version) VALUES (24);

COMMIT;
