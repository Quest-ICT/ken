-- Bind a binding voucher to the identity it was issued to (docs/STATIONS.md S5).
--
-- As shipped, the voucher was a BEARER capability. Redemption checked the hash,
-- that it was unredeemed, that it had not expired, and that its station was still
-- active. It checked nothing at all about WHO was redeeming. So the voucher string
-- alone bound any endpoint to the station's inbox, and the only thing standing
-- between a leaked voucher and a hijacked inbox was a human remembering a rule:
-- "never send a voucher over COMM, never write it to a file". A five-minute TTL
-- limits how long that matters; it does not change what the value IS.
--
-- These two columns record the identity that asked. Redemption then requires the
-- redeemer to be that same actor, which bounds the voucher's blast radius by the
-- comm token's: a leaked voucher now grants nothing that the credential needed to
-- use it does not already grant.
--
-- BOTH ARE NULLABLE, AND A NULL NEVER REDEEMS. Existing rows get NULL, and the
-- redemption predicate compares with `=`, which is never true against NULL in SQL
-- — so pre-upgrade vouchers are refused rather than grandfathered. That is
-- deliberate and it is safe: vouchers live five minutes, an upgrade takes longer,
-- so a voucher in flight across the restart is already dead by arithmetic. The
-- alternative — honouring old rows — would have left the bearer hole open inside
-- the very change that closes it. Relying on NULL-never-equals is stated here
-- because it is invisible at the call site and reads like an oversight.
--
-- issued_in_space is recorded but NOT enforced, because it cannot discriminate
-- yet: the station principal hardcodes SpaceID 1 (stationserver/auth.go). Writing
-- it now means the check can tighten to (actor, space) without a second migration
-- and without a backfill that would have nothing truthful to write.
BEGIN;

ALTER TABLE station_binding_voucher ADD COLUMN issued_to_actor  INTEGER;
ALTER TABLE station_binding_voucher ADD COLUMN issued_in_space  INTEGER;

INSERT INTO schema_migration(version) VALUES (14);

COMMIT;
