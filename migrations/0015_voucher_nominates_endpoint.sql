-- Pin a binding voucher to the ONE endpoint that may redeem it (docs/STATIONS.md S5).
--
-- 0014 required the redeemer's actor to match the actor the voucher was issued to.
-- That was the right direction and the wrong axis, and ken-prod-ops found the reason
-- by measuring their estate: SIX of their eight stations share one actor. The actor
-- is per MACHINE, not per station — correct for the hearsay marker, whose job is to
-- say "this token received mail on this box" — so keying the voucher on it narrowed
-- the credential to "any of six sessions on one workstation", not to the session it
-- was issued to.
--
-- Worse, the voucher inherited a WEAKER binding than its own issuer. The station key
-- that mints it is per-station; the check was per-actor. A voucher for station A,
-- leaked to a session holding a comm token under the same actor, bound that session
-- to station A's inbox — and the claim in 0014's comment, that a leaked voucher
-- "grants nothing the credential needed to use it does not already grant", was
-- FALSE: a comm token alone registers an UNBOUND endpoint, which cannot read any
-- station's mail. Binding is exactly the capability it does not confer.
--
-- So the voucher now names its redeemer. The session asks for a voucher FOR its own
-- endpoint id, and redemption requires that exact endpoint. Redeeming it therefore
-- demands that endpoint's own secret — a separate credential the voucher does not
-- carry — so a leaked voucher is inert in anyone else's hands.
--
-- This is why binding moved OUT of comm_register in the same change. Registration
-- has no endpoint id yet, by construction, so a voucher passed there could never
-- name one; that path could only ever have the weaker guarantee. Rather than ship
-- two strengths and document which is which, there is now one binding path
-- (register -> save your secret -> comm_bind) and one guarantee.
--
-- NOT NULL with no default and no backfill: every voucher must name an endpoint, and
-- a pre-existing row cannot. Vouchers live five minutes, so the table holds only
-- rows already dead or about to be; the redemption predicate refuses them anyway on
-- 0014's NULL-never-equals rule. The column is added as nullable ONLY because SQLite
-- cannot add a NOT NULL column without a default; the NOT NULL is enforced by the
-- redemption predicate, which compares it with `=`.
BEGIN;

ALTER TABLE station_binding_voucher ADD COLUMN issued_for_endpoint TEXT;

INSERT INTO schema_migration(version) VALUES (15);

COMMIT;
