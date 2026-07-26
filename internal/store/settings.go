package store

import "context"

// GetSettings returns all operator-set setting overrides (key -> value). An empty
// map means "all defaults".
func (s *Store) GetSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT key, value FROM app_setting`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSettings applies setting changes in one transaction: it upserts each
// key/value in upsert (an empty value is a legitimate override, stored verbatim)
// and deletes each key in remove (reverting it to the default). Deletion is an
// explicit list, never inferred from an empty value.
func (s *Store) SetSettings(ctx context.Context, upsert map[string]string, remove []string, updater string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, k := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM app_setting WHERE key=?`, k); err != nil {
			return err
		}
	}
	for k, v := range upsert {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO app_setting(key, value, updater) VALUES(?,?,?)
			 ON CONFLICT(key) DO UPDATE SET value=excluded.value,
			   updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), updater=excluded.updater`,
			k, v, updater); err != nil {
			return err
		}
	}
	return tx.Commit()
}
