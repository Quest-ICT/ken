-- ============================================================================
-- Ken COMM — migration 0010: the room membership MIRROR
-- ============================================================================
-- A derived projection of ken.db's comm_room_member. ken.db is the authority; this
-- exists for one reason and it is worth stating plainly rather than leaving to be
-- inferred: `comm.Store` holds no ken.db handle (comm.go — the two databases are
-- opened separately and version separately), and Send must check membership INSIDE its
-- writer transaction. A cross-database read is not available there at any price.
--
-- SO THIS IS A CACHE, AND IT IS TREATED LIKE ONE. It is rebuilt at boot and after every
-- console or CLI membership write. Losing comm.db loses this table and loses nothing
-- else — the rooms themselves, and who is in them, are still in ken.db where a human
-- put them. That is S7's rule in its usual direction: the expendable database may point
-- at the durable one, never the reverse.
--
-- WHY A MIRROR RATHER THAN PASSING MEMBERSHIP INTO Send: because then every caller
-- would have to know the roster, and one that forgot would send to a room it computed
-- wrongly. The membership check belongs beside the insert it authorises, in the same
-- transaction, or it is advisory.
BEGIN;

CREATE TABLE room_member_mirror (
  room_id   TEXT NOT NULL,
  -- ALWAYS 's:<station_id>'. Rooms hold stations, never endpoints — an endpoint is a
  -- connection and a room is a standing arrangement between posts. Stored in party form
  -- anyway so Send can use it directly against `delivery.party_key` with no translation
  -- step to get wrong.
  party_key TEXT NOT NULL,
  PRIMARY KEY (room_id, party_key)
) WITHOUT ROWID;
CREATE INDEX idx_room_mirror_party ON room_member_mirror(party_key);

-- What the mirror was built from, so a stale one can be RECOGNISED rather than trusted.
-- roster_epoch is ken.db's counter as of the last rebuild; if ken.db has moved past it,
-- this projection is behind and the caller can say so instead of silently addressing
-- yesterday's room.
CREATE TABLE mirror_state (
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  roster_epoch INTEGER NOT NULL DEFAULT 0,
  refreshed_at TEXT    NOT NULL
);
INSERT INTO mirror_state(id, roster_epoch, refreshed_at)
  VALUES (1, 0, strftime('%Y-%m-%dT%H:%M:%fZ','now'));

-- An attachment binds to the SCOPE rather than to a recipient endpoint.
--
-- The shipped grant check is `a.recipientRow != ep.ID` (file.go), which is the same
-- endpoint-versus-party mistake slice 3 removed everywhere else: a replacement session
-- staffing the same station is handed an attachment id and then told it does not exist.
-- Scoping the attachment fixes that and is what lets one offer serve N recipients — ONE
-- attachment row per offer regardless of audience, so a room broadcast is one charge
-- against the file budget rather than N.
ALTER TABLE attachment ADD COLUMN scope_id TEXT;

-- Backfill: every existing attachment belongs to its channel's scope.
UPDATE attachment
   SET scope_id = 'ch:' || (SELECT c.channel_id FROM channel c WHERE c.id = attachment.channel_id)
 WHERE scope_id IS NULL;

CREATE INDEX idx_attachment_scope ON attachment(scope_id) WHERE scope_id IS NOT NULL;

INSERT INTO schema_migration(version) VALUES (10);

COMMIT;
