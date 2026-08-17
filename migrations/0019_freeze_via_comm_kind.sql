-- ============================================================================
-- Ken — migration 0019: freeze `via_comm_kind`, which its sibling's rule missed
-- ============================================================================
-- Batch 4 of docs/FINISHING.md, found by the sweep rather than listed in it: the
-- second generation of an idea did not inherit the first generation's invariant.
--
-- Migration 0010 dropped and recreated `entry_version_immutable` for the SOLE purpose
-- of adding `via_comm` to its frozen set, and stated why in one line: "a mutable
-- marker could simply be UPDATEd away — which would defeat the point". The marker is
-- provenance. A provenance field an author can edit after the fact records nothing.
--
-- Migration 0018 then split that boolean into a KIND — directed versus broadcast —
-- because one send to a nine-station room marked nine actors and a badge that is
-- almost always present carries less information than no badge. `via_comm_kind` is
-- written at `internal/store/write.go` and read at `internal/store/promote.go`,
-- exactly like its sibling. It was never added to the frozen set.
--
-- So the sharper of the two markers — the one that distinguishes "somebody addressed
-- YOU" from "you were in the room" — is the one that could be quietly rewritten. The
-- rule was correct and complete when it was written; it simply did not travel to the
-- field that replaced the one it was written for.
--
-- SQLite has no ALTER TRIGGER, so the whole trigger is dropped and recreated. Every
-- other frozen column is carried across verbatim from 0010 — this adds one line and
-- changes nothing else. Content-language metadata stays deliberately UNFROZEN, for the
-- reason 0009_content_lang.sql gives.

BEGIN;

DROP TRIGGER IF EXISTS entry_version_immutable;

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
    OR NEW.via_comm_kind     IS NOT OLD.via_comm_kind
    OR NEW.created_at        IS NOT OLD.created_at )
BEGIN
  SELECT RAISE(ABORT, 'entry_version content is immutable — append a new revision instead of updating');
END;

INSERT INTO schema_migration(version) VALUES (19);

COMMIT;
