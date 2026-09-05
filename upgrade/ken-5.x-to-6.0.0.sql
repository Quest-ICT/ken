-- Ken ken.db: 5.x (schema 26) -> 6.0.0 (schema 27)
--
--     sudo systemctl stop ken
--     ken backup snapshot && ken backup verify          -- do NOT skip this
--     sqlite3 -bail /opt/ken/data/ken.db < ken-5.x-to-6.0.0.sql
--     sudo systemctl restart ken                        -- RESTART, never START
--
-- `restart`, not `start`: ken-upgrade starts the service before you run this, so `start` is a
-- no-op and Ken keeps the pre-script database open — unit active, /healthz ok, messaging and KB
-- quietly wrong. That cost a patch release on 2026-08-31.
--
-- If `sqlite3` is not installed (it is absent on Rocky 10), use the python3 fallback documented in
-- docs/UPGRADING-THE-DATABASE.md — it needs isolation_level=None or the transaction below is
-- silently wrapped in a second one.
--
-- *** READ THIS BEFORE YOU RUN IT. THIS SCRIPT MAKES A DECISION ON YOUR BEHALF. ***
--
-- Every proposal still pending becomes LIVE. 6.0.0 deletes the curation gate: an agent's write is
-- the head the moment it is stored, and the /proposals page is gone. Anything sitting in that
-- queue is adopted here, because leaving it behind would strand it forever with no surface left to
-- promote it from.
--
-- Open /proposals and read the list BEFORE you run this. Step 0 prints exactly what will change.
--
-- WHAT IS NOT ADOPTED, and this is deliberate: versions you REJECTED, and versions marked
-- withdrawn. You said no to those. The gate is going; that decision is not.

PRAGMA foreign_keys=OFF;
BEGIN;

-- 0. PRINTED, NOT SILENT. Every version about to become the live head of its entry.
SELECT '--- becoming live ---';
SELECT e.slug, ev.rev_no, ev.title
  FROM entry_version ev JOIN entry e ON e.id = ev.entry_id
 WHERE ev.state = 'proposed'
 ORDER BY e.slug, ev.rev_no;

-- 0b. And every entry that will end up with NO head at all, because every version it ever had was
--     rejected. These are ARCHIVED rather than resurrected, and they are listed so that an entry
--     disappearing from search is something you were told about rather than something you find.
SELECT '--- retired (no acceptable version) ---';
SELECT e.slug FROM entry e
 WHERE NOT EXISTS (SELECT 1 FROM entry_version ev
                    WHERE ev.entry_id = e.id
                      AND (ev.state = 'proposed' OR ev.id = e.curated_version_id));

-- 1. THE INVARIANT FIRST, so a mis-ordered conversion cannot commit at all. One head per entry.
-- Byte-identical to the statement in schema/ken.sql. sqlite_master stores the text VERBATIM, so a
-- different line break or a space around the '=' makes an upgraded database differ from a fresh one
-- forever, in the exact way TestAnUpgradedDatabaseMatchesAFreshOne exists to catch.
CREATE UNIQUE INDEX idx_ev_one_head ON entry_version(entry_id) WHERE state='curated';

-- 2. Winner per entry = the highest rev_no among {the current head} UNION {everything proposed}.
--
--    THE UNION IS LOAD-BEARING. A human who reverted an entry can leave the head at rev 5 with
--    rev 4 still sitting 'proposed' — Promote superseded only the outgoing head, never sibling
--    proposals. "Highest proposed wins" alone would regress that entry to rev 4 and silently undo
--    a deliberate human decision on upgrade day.
--
--    REJECTED AND WITHDRAWN ARE NEVER CANDIDATES.
DROP TABLE IF EXISTS temp.head_pick;
CREATE TEMP TABLE head_pick AS
SELECT e.id AS entry_id,
       (SELECT ev.id FROM entry_version ev
         WHERE ev.entry_id = e.id
           AND (ev.state = 'proposed' OR ev.id = e.curated_version_id)
         ORDER BY ev.rev_no DESC LIMIT 1) AS win_vid
  FROM entry e;

-- 3. LOSERS FIRST — the ordering the unique index in step 1 requires. `IS NOT` is NULL-safe.
UPDATE entry_version
   SET state = 'superseded',
       superseded_by_version_id = (SELECT win_vid FROM head_pick WHERE entry_id = entry_version.entry_id)
 WHERE state IN ('proposed', 'curated')
   AND id IS NOT (SELECT win_vid FROM head_pick WHERE entry_id = entry_version.entry_id);

-- 4. Then the winner, clearing any superseded_by it was carrying.
UPDATE entry_version
   SET state = 'curated', superseded_by_version_id = NULL
 WHERE id IN (SELECT win_vid FROM head_pick WHERE win_vid IS NOT NULL);

-- 5. Pointers, lifecycle, and the denormalized columns /browse and kb_get read from `entry`.
--
--    The four-column refresh matters here for the same reason it matters in the revision path:
--    get.go reads title/summary/tags/triggers from `entry` and the body from the version, and an
--    adopted proposal may have edited a title. Without this, upgrade day would serve old titles
--    beside new bodies, permanently, with nothing failing.
--
--    staleness is NOT reset. A standing was-wrong must survive an upgrade; 'aging' and 'refuted'
--    collapse to 'stale' only because no code produces them any more.
UPDATE entry SET
  curated_version_id = (SELECT win_vid FROM head_pick WHERE entry_id = entry.id),
  lifecycle = CASE
      WHEN (SELECT win_vid FROM head_pick WHERE entry_id = entry.id) IS NULL THEN 'archived'
      WHEN lifecycle = 'draft'      THEN 'active'
      WHEN lifecycle = 'deprecated' THEN 'archived'
      ELSE lifecycle END,
  title    = COALESCE((SELECT title    FROM entry_version WHERE id = (SELECT win_vid FROM head_pick WHERE entry_id = entry.id)), title),
  summary  = COALESCE((SELECT summary  FROM entry_version WHERE id = (SELECT win_vid FROM head_pick WHERE entry_id = entry.id)), summary),
  tags     = COALESCE((SELECT tags     FROM entry_version WHERE id = (SELECT win_vid FROM head_pick WHERE entry_id = entry.id)), tags),
  triggers = COALESCE((SELECT triggers FROM entry_version WHERE id = (SELECT win_vid FROM head_pick WHERE entry_id = entry.id)), triggers),
  staleness = CASE WHEN staleness IN ('aging','refuted') THEN 'stale' ELSE staleness END,
  -- CLEAR THE TWO COLUMNS WHOSE WRITERS THIS RELEASE DELETED.
  --
  -- provisional_version_id meant "a version exists that is not the head" — a state that no longer
  -- occurs. Left as it was, every entry that had a pending proposal would wear a permanent
  -- "pending proposal" badge on /browse and /search, pointing at a version that IS now the head.
  -- curated_rev counted promotions; nothing increments it any more, so a stale value would be
  -- displayed as a revision number beside rev numbers that had moved past it.
  provisional_version_id = NULL,
  curated_rev = 0,
  lock_version = lock_version + 1,
  updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
  updater = 'upgrade-6.0.0';

-- 6. curation_event gains four event types and LOSES NONE. Historical 'promoted' and 'rejected'
--    rows are true statements about what happened and keep their exact meaning; narrowing the
--    enum would rewrite history. SQLite cannot ALTER a CHECK, so the table is rebuilt.
CREATE TABLE curation_event_new (
  id         INTEGER PRIMARY KEY,
  entry_id   INTEGER NOT NULL REFERENCES entry(id) ON DELETE CASCADE,
  version_id INTEGER REFERENCES entry_version(id),
  event_type TEXT NOT NULL CHECK (event_type IN
               ('wrote','revised','reverted','restored',
                'proposed','promoted','superseded','rejected','withdrawn',
                'deprecated','archived','reverified','refuted','flagged_stale')),
  from_state TEXT,
  to_state   TEXT,
  actor_id   INTEGER REFERENCES actor(id),
  actor_kind TEXT CHECK (actor_kind IN ('ai','human','import','system')),
  session_id TEXT,
  note       TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
INSERT INTO curation_event_new SELECT * FROM curation_event;
DROP INDEX IF EXISTS idx_ce_created;
DROP INDEX IF EXISTS idx_ce_entry;
DROP TABLE curation_event;
-- RENAME quotes the name in sqlite_master ("curation_event"), which a fresh CREATE does not. That
-- one pair of quotes is a permanent fresh-vs-upgraded divergence, so the fresh schema declares the
-- name quoted too and the two shapes agree. (entry_embedding, station_link and station_request
-- already carry the same quotes from earlier rebuilds, for the same reason.)
ALTER TABLE curation_event_new RENAME TO curation_event;
CREATE INDEX idx_ce_entry   ON curation_event(entry_id);
CREATE INDEX idx_ce_created ON curation_event(created_at);

-- 7. Record the version. Ken refuses to start against anything else, which is the whole reason
--    this script is a version bump and not just a data conversion: without the refusal, an
--    operator who skipped it would get a Ken that boots cleanly and serves a knowledge base with
--    a chunk of its entries permanently unfindable — indistinguishable from a small KB.
INSERT INTO schema_migration(version) VALUES (27);

-- 8. ASSERT BEFORE COMMITTING. Each must print 0. If any does not, type ROLLBACK; and restore the
--    snapshot rather than committing — under `sqlite3 -bail` a failed statement aborts, but a
--    non-zero COUNT is not a failure, it is an answer. Read them.
SELECT '--- invariants, every one must be 0 ---';
SELECT 'entries with no head that are not archived:',
       COUNT(*) FROM entry WHERE curated_version_id IS NULL AND lifecycle <> 'archived';
SELECT 'entries with more than one curated version:',
       COUNT(*) FROM (SELECT entry_id FROM entry_version WHERE state='curated'
                       GROUP BY entry_id HAVING COUNT(*) > 1);
SELECT 'heads that are not marked curated:',
       COUNT(*) FROM entry e JOIN entry_version ev ON ev.id = e.curated_version_id
        WHERE ev.state <> 'curated';
SELECT 'versions still proposed:',
       COUNT(*) FROM entry_version WHERE state = 'proposed';
SELECT 'rejected versions that were adopted as a head:',
       COUNT(*) FROM entry e JOIN entry_version ev ON ev.id = e.curated_version_id
        WHERE ev.reviewed_at IS NOT NULL AND ev.state = 'curated'
          AND EXISTS (SELECT 1 FROM curation_event ce
                       WHERE ce.version_id = ev.id AND ce.event_type = 'rejected');

COMMIT;
PRAGMA foreign_keys=ON;
PRAGMA foreign_key_check;
