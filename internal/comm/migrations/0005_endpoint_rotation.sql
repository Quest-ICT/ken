-- ============================================================================
-- Ken COMM — migration 0005: rotating an endpoint secret
-- ============================================================================
-- Until now there was exactly one thing a human could do to a live endpoint:
-- revoke it. That makes a LEAKED endpoint secret unnecessarily expensive to
-- contain — the only remedy is to revoke, re-register, and then re-pair every
-- channel that endpoint belonged to, with every peer, from scratch. Rotation is
-- the missing incident-response primitive: replace the secret, keep the identity
-- and every channel membership, and leave the peers untouched.
--
-- WHY THIS DOES NOT REOPEN THE HOLE THE PER-ENDPOINT SECRET EXISTS TO CLOSE.
-- One bearer token covers a machine, so the endpoint pair is the only thing
-- separating two sessions that share it. Any reissue a SESSION could trigger
-- would therefore let any session on that machine seize any endpoint on it —
-- which is why deriving a new secret from token material was rejected. The flaw
-- there is the automation, not the reissuing: rotation is reachable ONLY from
-- the authenticated console, and curator authentication is a credential no
-- session holds or can obtain from the machine. A neighbouring session with the
-- COMM bearer token gains nothing, because the token is not what authorises it.
--
-- The secondary benefit is the one that started this: a session whose context is
-- compacted loses its secret irrecoverably, and today that costs one fresh
-- pairing code PER CHANNEL plus coordinated re-joins. Rotation collapses it to
-- one paste. It does not make the session self-healing — a human still acts —
-- so it shortens the work, not the wait.
--
-- WHY THE COUNTERS. Rotation is a security-relevant console action and wants a
-- trace, but comm.db is expendable and deliberately not backed up, so it is the
-- wrong place for the authoritative audit record — the server log is, and the
-- handler writes there too. These columns exist so the CONSOLE can show an
-- operator that an endpoint's secret has been rotated and when, which is the
-- question they actually ask while looking at the page ("did I already do this
-- one?"). Treat them as display state that happens to be durable, not as the
-- audit trail.
--
-- Additive and nullable: existing endpoints simply report no rotation.
BEGIN;

ALTER TABLE endpoint ADD COLUMN secret_rotated_at TEXT;
ALTER TABLE endpoint ADD COLUMN rotate_count INTEGER NOT NULL DEFAULT 0;

INSERT INTO schema_migration(version) VALUES (5);

COMMIT;
