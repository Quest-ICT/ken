-- ============================================================================
-- COMM migration 0020: the pairing code is gone, and so is its table
-- ============================================================================
-- *** WHY: THE SECOND HUMAN GATE ON COMM WAS REMOVED BY DECISION, NOT BY DRIFT. ***
--
-- A pairing code was COMM's structural gate. An agent could not conjure a channel, because
-- creating one required a value only the human web UI could produce — the same move that makes the
-- curation gate trustworthy, withhold the capability rather than instruct the model not to use it.
--
-- Vlad removed both gates on comm in the 2026-08-27 wave: links are approved automatically on first
-- contact, and the pairing code is no longer required. What replaced it is not a weaker gate but a
-- different place to stand: the human's control moved from authorising each conversation in advance
-- to SUSPENDING a relationship at the console — which, unlike a code, they can also resume.
--
-- The code was in practice the reason a session could not reach anyone rather than a decision
-- anybody made: it expired in fifteen minutes, typically while its human was away from the keyboard.
--
-- WHAT WENT WITH IT, in the same change: comm_join, MintPairingCode, JoinChannel, the console's
-- mint form, its one-time reveal, the pending-codes table, ListPendingCodes, and the
-- comm_pairing_code_ttl_sec setting. This table is the last of it.
--
-- CHANNELS SURVIVE. They are opened by OpenLinkedChannel, which the console has always used and
-- which comm_open_channel now reaches directly. Only the credential that used to be required to
-- create one is gone.
--
-- SAFE TO DROP OUTRIGHT. pairing_code.channel_id references channel(id) ON DELETE SET NULL, so this
-- table points OUT and nothing points in: no foreign key, trigger or view names it. Dropping it
-- cannot orphan a channel, and any channel a code once opened keeps working — the link between them
-- was only ever an audit trail for a credential that no longer exists.
--
-- AND comm.db IS EXPENDABLE ANYWAY. Vlad's ruling for this wave was that nothing is migrated and
-- comm.db may start empty. This migration exists for the deployment that upgrades in place, so its
-- schema matches a fresh one rather than carrying a table no code can reach.

BEGIN;

DROP TABLE pairing_code;

INSERT INTO schema_migration(version) VALUES (20);

COMMIT;
