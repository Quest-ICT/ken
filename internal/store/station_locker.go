package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// The station locker (docs/STATIONS.md S11) — the NON-SECRET half of a working identity.
//
// It exists for one job: letting a fresh session on another machine reconstitute itself.
// Memory and instruction files, tool preferences, paths, conventions. NOT a credential
// store, NOT bulk storage — every blob lands in every nightly snapshot, ×15 with
// retention and Litestream, which is why it is small and refuses rather than evicting.
//
// Ken CANNOT enforce the non-secret rule: it cannot inspect a blob and know it is a key.
// The tool description states it, the console shows what is stored, and the human can
// look. That is a documented expectation, not a control — and BACKUP.md's guarantee is
// narrowed to match: no credential KEN STORES is replayable; a locker blob is opaque
// content Ken does not inspect.

// StationLockerLimits are §9's numbers.
type StationLockerLimits struct {
	MaxBlobBytes  int // 256 KiB
	MaxTotalBytes int // 2 MiB per station
}

// DefaultStationLockerLimits are §9's numbers.
func DefaultStationLockerLimits() StationLockerLimits {
	return StationLockerLimits{MaxBlobBytes: 256 << 10, MaxTotalBytes: 2 << 20}
}

// StationLockerEntry is one stored file. Bytes are omitted from listings.
type StationLockerEntry struct {
	Name        string
	SizeBytes   int
	SHA256      string
	ContentType string
	UpdatedAt   string
	Bytes       []byte // only populated by GetStationLockerBlob
}

// ErrLockerCapReached refuses rather than evicting (S12).
var ErrLockerCapReached = errors.New("locker cap reached")

// ListStationLocker returns metadata only — names, sizes and digests. A caller that
// wants bytes asks for one file.
func (s *Store) ListStationLocker(ctx context.Context, stationID string) ([]StationLockerEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT name, size_bytes, sha256, content_type, updated_at
FROM station_locker WHERE station_id=? ORDER BY name`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationLockerEntry
	for rows.Next() {
		var e StationLockerEntry
		if err := rows.Scan(&e.Name, &e.SizeBytes, &e.SHA256, &e.ContentType, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutStationLockerBlob stores or replaces a file.
func (s *Store) PutStationLockerBlob(ctx context.Context, lim StationLockerLimits, stationID, name string,
	body []byte, contentType, tokenID string, actorID int64) (*StationLockerEntry, error) {

	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("a locker entry needs a name")
	}
	// A name is a flat label, never a path: a locker is not a filesystem and must not be
	// coaxed into traversing one if it is ever written to disk.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return nil, fmt.Errorf("locker names are flat labels, not paths (got %q)", name)
	}
	if len(body) > lim.MaxBlobBytes {
		return nil, fmt.Errorf("%w: %d bytes, over the %d-byte per-file cap — the locker holds memory and instruction files, not payloads; use scp or COMM file exchange for anything larger",
			ErrLockerCapReached, len(body), lim.MaxBlobBytes)
	}

	var otherBytes int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(size_bytes),0) FROM station_locker WHERE station_id=? AND name<>?`,
		stationID, name).Scan(&otherBytes); err != nil {
		return nil, err
	}
	if otherBytes+len(body) > lim.MaxTotalBytes {
		return nil, fmt.Errorf("%w: the locker would be %d bytes, over the %d-byte cap — every blob is carried by every nightly snapshot",
			ErrLockerCapReached, otherBytes+len(body), lim.MaxTotalBytes)
	}

	sum := sha256.Sum256(body)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err := s.W.ExecContext(ctx, `
INSERT INTO station_locker(station_id, name, bytes, size_bytes, sha256, content_type, updated_by_token_id, updated_by_actor_id)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(station_id, name) DO UPDATE SET
  bytes=excluded.bytes, size_bytes=excluded.size_bytes, sha256=excluded.sha256,
  content_type=excluded.content_type, updated_by_token_id=excluded.updated_by_token_id,
  updated_by_actor_id=excluded.updated_by_actor_id,
  updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		stationID, name, body, len(body), hex.EncodeToString(sum[:]), contentType,
		nullStr(tokenID), actorID); err != nil {
		return nil, err
	}
	_ = s.TouchStationActivity(ctx, stationID)
	return &StationLockerEntry{Name: name, SizeBytes: len(body), SHA256: hex.EncodeToString(sum[:]), ContentType: contentType}, nil
}

// GetStationLockerBlob returns one file's bytes.
func (s *Store) GetStationLockerBlob(ctx context.Context, stationID, name string) (*StationLockerEntry, error) {
	var e StationLockerEntry
	err := s.R.QueryRowContext(ctx, `
SELECT name, bytes, size_bytes, sha256, content_type, updated_at
FROM station_locker WHERE station_id=? AND name=?`, stationID, name).
		Scan(&e.Name, &e.Bytes, &e.SizeBytes, &e.SHA256, &e.ContentType, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &e, err
}

// DeleteStationLockerBlob removes one file.
func (s *Store) DeleteStationLockerBlob(ctx context.Context, stationID, name string) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM station_locker WHERE station_id=? AND name=?`, stationID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
