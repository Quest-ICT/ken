-- ============================================================================
-- Ken COMM — migration 0019: an endpoint can be CLAIMED by the conversation that owns it
-- ============================================================================
-- SHIPS ALONE WITH ITS CODE under Rule 4. Additive only: one nullable column and one partial
-- unique index. No row is rewritten, no constraint tightens, and every endpoint that exists keeps
-- authenticating exactly as it does today.
--
-- *** WHY: THE ENDPOINT SECRET IS WHAT LOCKS CHAT SESSIONS OUT OF COMM. ***
--
-- `comm_register` returns a secret and tells the session, in capitals: "WRITE THEM TO A FILE ON
-- DISK NOW, before you do anything else (mode 0600, outside any git repo)."
--
-- **A claude.ai chat session has no disk.** It can register once and then lose the ability to poll
-- forever, because the secret is shown once and nothing it controls survives a context compaction.
-- The same instruction is the per-machine credential tax IDENTITY.md §4b exists to remove.
--
-- *** AND THE SECRET EXISTS ONLY BECAUSE KEN COULD NOT TELL TWO SESSIONS APART. ***
--
-- `0001_init.sql:38`, unchanged since the first release: "secret_sha256 exists because the
-- operating convention is one Ken token per MACHINE, so every session on a box shares a token.
-- Without a per-endpoint secret, two sessions could poll and ack each other's messages — most
-- likely by ACCIDENT, when both register with the same label."
--
-- That is a disambiguation problem, and 3.35.0 solved it elsewhere: a conversation declares a
-- stable `session_key`, and Ken knows which session is which. **An endpoint claimed by a
-- conversation key needs no secret, because the key already does the job the secret was invented
-- for** — and does it better, since it survives a client restart while a secret survives only a
-- file.
--
-- *** WHAT THIS DELIBERATELY DOES NOT DO: DELETE THE SECRET. ***
--
-- Vlad asked for the secret and its write-to-disk instruction to be eliminated. This removes the
-- instruction for every new session and makes the secret unnecessary — but it does NOT drop the
-- column or stop honouring existing secrets, and that restraint is measured rather than cautious:
--
--   `station_me` returns `comm_endpoint_ids` — "the comm endpoints bound to this station" — so
--   endpoint ids are HANDED OUT, not private. Every comm tool takes endpoint_id + secret. Remove
--   the secret with nothing in its place and any session that has seen an id can poll and ack
--   that endpoint's mail, which is exactly and only what the secret prevents. Production holds
--   eight bound endpoints today.
--
-- So: claimed endpoints need no secret; unclaimed ones keep theirs. The secret can be retired for
-- real once measurement shows nothing depends on it — the same way the voucher chain went, when
-- zero were outstanding rather than when it became unfashionable.
--
-- UNIQUE AND PARTIAL. One conversation owns at most one endpoint, and the many endpoints with no
-- owning conversation must not collide with each other — so uniqueness applies WHERE the column
-- is set and nowhere else. A full unique index would collide on every existing NULL.
--
-- THE KEY AUTHORISES, WHICH IS WHY IT IS NOT LIKE THE WORKSPACE ID. The station `session_key`
-- SELECTS a workspace and grants nothing (IDENTITY.md §4, §9.2). This one is different and the
-- difference must be stated: presenting it drives an endpoint's mail. It is a CREDENTIAL, it is
-- as sensitive as the secret it replaces, and it must never be logged, put in a URL, or written
-- to a notebook. A session that leaks its conversation key has leaked its mailbox.

BEGIN;

ALTER TABLE endpoint ADD COLUMN session_key TEXT;

CREATE UNIQUE INDEX idx_endpoint_session_key
    ON endpoint(session_key) WHERE session_key IS NOT NULL;

INSERT INTO schema_migration(version) VALUES (19);

COMMIT;
