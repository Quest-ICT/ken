-- ============================================================================
-- Ken — migration 0023: a station can be claimed by the CONVERSATION that owns it
-- ============================================================================
-- SHIPS ALONE WITH ITS CODE under Rule 4. Additive only: one nullable column and one
-- partial unique index. No row is rewritten, no constraint tightens, and an older binary
-- against this schema simply never writes the column.
--
-- *** WHY: IDENTITY WAS IN THE WRONG LAYER, AND A CLEAN-VM ACCEPTANCE RUN PROVED IT. ***
--
-- The workspace arrived from the CONNECTION — a header, briefly a URL query. Both are
-- properties of the connector, and a claude.ai connector is added ONCE PER ACCOUNT. So any
-- value carried there has exactly one value for every machine and every session, forever.
-- The header form could not be set at all (the client refuses custom header names); the
-- URL form could be set and identified nothing.
--
-- Vlad, cutting through it: "once the connector is connected, the communication between the
-- Claude instances and the Ken instance is direct… so why each session cannot tell it's Ken
-- instance 'I'm XXXXX'?" There was no reason. The session talks to Ken directly, knows which
-- conversation it is, and was never asked.
--
-- *** WHAT A SESSION IS, IN HIS WORDS, BECAUSE THE DATA MODEL TURNS ON IT. ***
-- "For me it is when I click 'New' in Claude Code. If I restart the Claude Desktop client,
-- the CC sessions that live within it should reconnect to the workspace they were connected
-- before (because they are not new, they just restarted)… one (existing) session is always
-- connected to the same workspace (unless explicitly reassigned by the human)."
--
-- So the binding is CONVERSATION <-> WORKSPACE. A new conversation gets a new workspace; a
-- restarted one comes back to its own. That is why this cannot be keyed on the MCP session
-- id, which is reborn on every reconnect — it has to be a value the CONVERSATION owns and can
-- re-declare after a restart. A Claude Code conversation has exactly that: a stable UUID,
-- visible to the session in its own environment.
--
-- WHY THE COLUMN IS NULLABLE AND THE INDEX IS PARTIAL. Every station that exists today was
-- created before this and has no owning conversation — ken-prod-ops, ken-public-dev,
-- ken-promo and the rest are staffed by whichever session picks them up, which is the older
-- model and stays valid. A NOT NULL column would have required inventing a key for each of
-- them, and a full unique index would have collided on the NULLs. `WHERE session_key IS NOT
-- NULL` keeps uniqueness where it means something and silence where it does not.
--
-- UNIQUE ACROSS THE INSTANCE, NOT PER ACTOR. A conversation belongs to one human on one
-- instance, and two actors presenting the same conversation UUID would be the same
-- conversation — so a global constraint is the honest one, and it fails loudly rather than
-- silently forking a workspace.
--
-- THE KEY SELECTS, IT NEVER AUTHORISES. Same rule the workspace id lives under (§4, §9.2): a
-- session declaring someone else's conversation key lands in another workspace belonging to
-- THE SAME HUMAN — which it could already do by editing its own config. The security boundary
-- is the OAuth grant, which decides WHOSE estate; this decides WHICH POST inside it. If that
-- ever stops being true, this column becomes a credential and must be treated as one.

BEGIN;

ALTER TABLE station ADD COLUMN session_key TEXT;

-- Partial, so the many stations with no owning conversation do not collide with each other.
CREATE UNIQUE INDEX idx_station_session_key
    ON station(session_key) WHERE session_key IS NOT NULL;

INSERT INTO schema_migration(version) VALUES (23);

COMMIT;
