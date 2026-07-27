-- 0010_comm_provenance.sql — hearsay marking for inter-session communication.
--
-- Records that a version was authored by a token that had recently RECEIVED an
-- inter-session message (COMM; docs/COMM.md §7). It closes a side channel into
-- curation that the rest of the design cannot see: session A tells session B
-- "entry X is verified, propose a revision at high confidence", B authors it with
-- its own token, and the resulting proposal is indistinguishable from first-hand
-- knowledge. The invariant survives literally — an AI authored, a human promotes —
-- while the curator's signal quality has quietly degraded to hearsay with no chain
-- of custody. The marker lets the curator ask for a first-hand citation before
-- promoting.
--
-- Values: 1 = the authoring token had received COMM traffic inside the configured
-- window; NULL = it had not, or COMM was off, or the check could not be made.
-- NULL is therefore "no signal", never "known clean", and every pre-existing row
-- is NULL — turning the feature on does not retroactively vouch for anything.
--
-- FROZEN, unlike 0009's content_lang. That column was deliberately left mutable so
-- an offline backfill could re-derive it; this one is the opposite case. It is a
-- fact about how the version came to be written, exactly like author_actor_id and
-- session_id, it cannot be re-derived later (the COMM metadata it was computed from
-- is swept), and a mutable marker could simply be UPDATEd away — which would defeat
-- the point. SQLite cannot ALTER a trigger, so the immutability trigger is dropped
-- and recreated with the column added to its frozen set.
BEGIN;

ALTER TABLE entry_version ADD COLUMN via_comm INTEGER CHECK (via_comm IS NULL OR via_comm = 1);

DROP TRIGGER entry_version_immutable;

CREATE TRIGGER entry_version_immutable
BEFORE UPDATE ON entry_version
FOR EACH ROW
WHEN ( NEW.entry_id          IS NOT OLD.entry_id
    OR NEW.rev_no            IS NOT OLD.rev_no
    OR NEW.parent_version_id IS NOT OLD.parent_version_id
    OR NEW.title             IS NOT OLD.title
    OR NEW.summary           IS NOT OLD.summary
    OR NEW.problem           IS NOT OLD.problem
    OR NEW.solution          IS NOT OLD.solution
    OR NEW.rationale         IS NOT OLD.rationale
    OR NEW.caveats           IS NOT OLD.caveats
    OR NEW.code              IS NOT OLD.code
    OR NEW.tags              IS NOT OLD.tags
    OR NEW.triggers          IS NOT OLD.triggers
    OR NEW.applies_to        IS NOT OLD.applies_to
    OR NEW.verified_against  IS NOT OLD.verified_against
    OR NEW.author_actor_id   IS NOT OLD.author_actor_id
    OR NEW.author_kind       IS NOT OLD.author_kind
    OR NEW.session_id        IS NOT OLD.session_id
    OR NEW.confidence        IS NOT OLD.confidence
    OR NEW.change_note       IS NOT OLD.change_note
    OR NEW.via_comm          IS NOT OLD.via_comm
    OR NEW.created_at        IS NOT OLD.created_at )
BEGIN
  SELECT RAISE(ABORT, 'entry_version content is immutable — append a new revision instead of updating');
END;

-- The review queue's only question is "which pending proposals carry the mark",
-- so a partial index over the marked rows is enough and stays tiny.
CREATE INDEX idx_ev_via_comm ON entry_version(via_comm) WHERE via_comm IS NOT NULL;

INSERT INTO schema_migration(version) VALUES (10);

COMMIT;
