package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Session is a human web-login session (server-side).
type Session struct {
	ID        string
	ActorID   int64
	ActorName string
	CSRF      string
	ExpiresAt string
}

// CreateSession creates a session for actorID with the given TTL and a fresh
// CSRF token (rotated per login).
func (s *Store) CreateSession(ctx context.Context, actorID int64, ttl time.Duration) (*Session, error) {
	id, err := randBase62(32)
	if err != nil {
		return nil, err
	}
	csrf, err := randBase62(32)
	if err != nil {
		return nil, err
	}
	if _, err := s.W.ExecContext(ctx,
		`INSERT INTO web_session(id, actor_id, csrf_token, expires_at)
		 VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		id, actorID, csrf, fmt.Sprintf("%+d seconds", int(ttl.Seconds()))); err != nil { // %+d keeps the sign valid for negative ttls too
		return nil, err
	}
	return &Session{ID: id, ActorID: actorID, CSRF: csrf}, nil
}

// SessionByID returns a live (unexpired) session, or ErrNotFound.
func (s *Store) SessionByID(ctx context.Context, id string) (*Session, error) {
	var se Session
	err := s.R.QueryRowContext(ctx, `
SELECT w.id, w.actor_id, a.display_name, w.csrf_token, w.expires_at
FROM web_session w JOIN actor a ON a.id=w.actor_id
WHERE w.id=? AND w.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`, id).
		Scan(&se.ID, &se.ActorID, &se.ActorName, &se.CSRF, &se.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &se, nil
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM web_session WHERE id=?`, id)
	return err
}

// DeleteExpiredSessions purges sessions past their expiry; returns the count.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.W.ExecContext(ctx,
		`DELETE FROM web_session WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
