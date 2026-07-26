-- 0009_content_lang.sql — content-language guardrail (curator comprehension).
--
-- Adds a nullable, auto-detected content-language tag to each immutable version.
-- The writer (internal/store, via internal/lang) sets it at INSERT time from the
-- PROSE fields (title/summary/problem/solution/rationale/caveats) as a lowercased
-- BCP-47 primary subtag ('en','es','fr','zh', …); 'und' when undecidable. It is
-- detection METADATA, deliberately NOT listed in the entry_version_immutable
-- trigger's frozen set, so a future offline `ken lang backfill` can (re)derive it
-- without fighting immutability.
--
-- Scope of this column is narrow ON PURPOSE: it feeds ONLY (a) the review-queue
-- foreign-language flag and (b) the server-side "can't promote what you can't
-- read" gate. It never enters retrieval (search stays language-blind) or the
-- write gate. NULL = undetected (every pre-existing row) and is treated exactly
-- like 'und': never flagged, always promotable — so turning the feature on does
-- NOT retroactively wall off the existing corpus.
BEGIN;

ALTER TABLE entry_version ADD COLUMN content_lang TEXT;

CREATE INDEX idx_ev_content_lang ON entry_version(content_lang);

INSERT INTO schema_migration(version) VALUES (9);

COMMIT;
