package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// sessionKey is what actually goes in the `web_session.id` column: the SHA-256 of the
// cookie value, never the value itself.
//
// WHY: the raw cookie is a bearer credential — presenting it IS being logged in — and
// the database is copied by design. Every snapshot is a byte-complete copy of it, and
// as of KEN_BACKUP_GROUP a deliberately unprivileged account may read snapshots. Stored
// raw, a snapshot taken while a curator was logged in handed its reader that curator's
// session until it expired. Hashed, the stored value cannot be replayed: a lookup can
// confirm a cookie the client presents, but the file alone yields nothing usable.
// This mirrors `api_token`, which has always stored `secret_sha256` rather than the
// secret. Plain SHA-256 is right here (unlike a password): the input is 32 bytes of
// CSPRNG output, so there is nothing to brute-force and no need for a slow KDF.
func sessionKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

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
	// Store the HASH; hand the caller the raw value for the cookie. The raw value
	// never touches disk, so a database copy cannot yield a usable session.
	if _, err := s.W.ExecContext(ctx,
		`INSERT INTO web_session(id, actor_id, csrf_token, expires_at)
		 VALUES(?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		sessionKey(id), actorID, csrf, fmt.Sprintf("%+d seconds", int(ttl.Seconds()))); err != nil { // %+d keeps the sign valid for negative ttls too
		return nil, err
	}
	return &Session{ID: id, ActorID: actorID, CSRF: csrf}, nil
}

// SessionByID returns a live (unexpired) session for the RAW cookie value, or
// ErrNotFound. The lookup is by hash (see sessionKey); the returned Session carries the
// raw id the caller passed in, so callers are unaffected by the storage change.
func (s *Store) SessionByID(ctx context.Context, id string) (*Session, error) {
	var se Session
	err := s.R.QueryRowContext(ctx, `
SELECT w.actor_id, a.display_name, w.csrf_token, w.expires_at
FROM web_session w JOIN actor a ON a.id=w.actor_id
WHERE w.id=? AND w.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now')`, sessionKey(id)).
		Scan(&se.ActorID, &se.ActorName, &se.CSRF, &se.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	se.ID = id // the raw cookie value, as handed in — never the stored hash
	return &se, nil
}

// DeleteSession removes a session (logout). Takes the RAW cookie value.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM web_session WHERE id=?`, sessionKey(id))
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
