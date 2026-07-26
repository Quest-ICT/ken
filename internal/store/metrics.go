package store

import "context"

// CountEntries returns the number of knowledge-base entries (curated or draft).
func (s *Store) CountEntries(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry`).Scan(&n)
	return n, err
}

// CountVersions returns the number of entry versions (the append-only history,
// which only ever grows).
func (s *Store) CountVersions(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry_version`).Scan(&n)
	return n, err
}

// CountActiveTokens returns the number of non-revoked agent API tokens.
func (s *Store) CountActiveTokens(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_token WHERE revoked_at IS NULL`).Scan(&n)
	return n, err
}
