-- ============================================================================
-- Ken — migration 0026: link requests are retired, and so is their mute table
-- ============================================================================
-- Part of the one breaking wave Vlad authorised on 2026-08-27; Rule 4 is suspended for it, so
-- this ships alongside 0025 and the code that goes with both.
--
-- *** WHY: THERE IS NOTHING LEFT TO APPROVE. ***
--
-- A link request existed to put one question in front of the human: may these two stations talk?
-- Vlad removed that gate — "links auto-approved on first contact" — so a link is now created by
-- the send path itself, born active, kept only for the audit trail and the off-switch. What
-- remained was a tool that told a session to ask for permission it already had, a console button
-- that granted what was already granted, and a pending row that would sit there forever because
-- nothing would ever decide it.
--
-- That is precisely the thing he said must not survive this upgrade: "nothing may keep calling
-- the old name without anybody noticing it." A surface that still asks after the asking stopped
-- mattering is the same defect wearing a different word.
--
-- *** WHAT GOES, AND WHY EACH IS SAFE TO DROP ***
--
--   kind='link' rows       DELETED, not migrated. Every one is a question whose answer is now
--                          "yes, automatically" — and Vlad was explicit: "Do not waste time on
--                          migrating existing 'anything'." A pending link request converted into
--                          a link would also fabricate a human decision that was never made; the
--                          first message between those stations creates the real link anyway.
--
--   the 'link' CHECK value REMOVED, so the column can no longer accept one. Leaving it would cost
--                          nothing today and would read, to the next reader, as a kind the system
--                          still supports. The CHECK is the only place left that would still say
--                          the word.
--
--   station_link_denial    DROPPED WHOLE. It was the escalating mute for repeated link asks (1h →
--                          6h → 24h → 7d), read only by CreateStationLinkRequest and cleared only
--                          by ApproveLinkRequest. Both are gone. Room requests DO still escalate,
--                          but on their own ladder: CreateRoomRequest counts the asking station's
--                          own denied rows, because a room request has no pair to key a mute on.
--                          So dropping this table takes nothing from the surviving kind — checked
--                          in code, not assumed.
--
-- Two request kinds survive, both real: 'station' (a session with no post asking for one) and
-- 'room' (a station asking a human to create a room, naming no members — migration 0024's whole
-- argument, untouched, because a human is still the sole decider of who talks to whom in a room).
--
-- REBUILD SHAPE, checked the same way 0024 checked it: nothing in either database points a foreign
-- key at station_request, no trigger reads it, no view names it. Its only dependent objects are
-- the automatic index behind `request_id UNIQUE` and one hand-made index, recreated below.

BEGIN;

-- Deleted BEFORE the rebuild: the new CHECK would reject these rows on the way in, and a
-- migration that fails on a live database because of data it intends to discard is a bad way to
-- find out. The count is whatever it is — on a fresh install, zero.
DELETE FROM station_request WHERE kind = 'link';

CREATE TABLE station_request_new (
  id                     INTEGER PRIMARY KEY,
  request_id             TEXT    NOT NULL UNIQUE,
  -- 'station' (an unknown session asking for a post) or 'room' (a station asking a human to
  -- create a room — naming no members). 'link' was removed in 0026: links are created on first
  -- contact and there is no longer a decision to file.
  kind                   TEXT    NOT NULL CHECK (kind IN ('station','room')),
  from_station           TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  from_token_id          TEXT    NOT NULL,        -- audit string; may dangle by design
  to_station             TEXT    REFERENCES station(station_id) ON DELETE CASCADE,
  name_hint              TEXT,                    -- NON-BINDING; the human types the name
  purpose                TEXT    NOT NULL DEFAULT '',
  reason                 TEXT    NOT NULL DEFAULT '',
  -- The transitive path: A cannot make a room but can talk B into asking for one, and B's request
  -- then reaches the human looking like B's own idea. Computed like entry_version.via_comm.
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
-- one used, and the console's pending-request query is exactly (state, created_at).
CREATE INDEX idx_station_request_pending ON station_request(state, created_at);

-- The link mute, with no reader and no writer left.
DROP TABLE station_link_denial;

-- *** AND THE BINDING-VOUCHER TABLE, INERT SINCE 2026-08-25 AND SAID SO IN A COMMENT. ***
--
-- The voucher chain was deleted with docs/IDENTITY.md §10 step 3: a voucher existed SOLELY so a
-- station key never crossed to the comm surface as a tool argument, and once one identity spanned
-- every surface there was no key to keep off it. The code went; the table stayed, with a note
-- explaining that dropping it was a schema change and Rule 4 says such a release carries nothing
-- else, "so it ships alone, later."
--
-- This is later. Rule 4 is suspended for this wave by Vlad's decision, so the table that has been
-- waiting for a release of its own rides along with the two others.
DROP TABLE IF EXISTS station_binding_voucher;

INSERT INTO schema_migration(version) VALUES (26);

COMMIT;
