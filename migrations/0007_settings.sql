-- 0007: app_setting — a small key/value store for operator-editable runtime
-- settings (rate limits, login lockout, trusted proxies, acme domains, …) that the
-- web UI writes and the app applies live. Overrides the env/compiled defaults;
-- an absent key falls back to the default, so an empty table = "all defaults".
BEGIN;

CREATE TABLE IF NOT EXISTS app_setting (
  key        TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  updater    TEXT
);

INSERT INTO schema_migration(version) VALUES (7);

COMMIT;
