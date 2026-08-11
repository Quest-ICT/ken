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

// The station VAULT (migrations/0016_station_vault.sql) — the half of a working identity
// the LOCKER is forbidden to hold.
//
// The two are siblings on purpose and their differences are all deliberate:
//
//	locker  opaque blobs, listed with their bytes available, deleted destructively,
//	        reads unaudited, and its own tool text forbids credentials
//	vault   small text secrets, NEVER listed with their values, every write reversible,
//	        every read logged, and credentials are the whole point
//
// What the vault does NOT do is encrypt. A key stored beside the ciphertext protects
// nobody who can read the file, and pretending otherwise invites an operator to relax a
// control that does not exist. The confidentiality boundary is the host and the backup,
// and it is documented rather than simulated — see the migration's header and
// docs/BACKUP.md, whose "no credential Ken STORES is replayable" guarantee this feature
// deliberately breaks and correspondingly rewrites.

// StationVaultLimits bound a vault. A secret is a credential, not a payload: the caps are
// small on purpose, and every one of them REFUSES rather than evicting, except the two
// histories, which are bounded and REPORT what they dropped.
type StationVaultLimits struct {
	MaxSecretBytes    int // 8 KiB — a PEM private key fits; a database does not
	MaxEntries        int // 64 secrets per station
	MaxHistoryPerName int // 16 superseded values per name: an undo buffer, not an archive
	MaxReadLog        int // 500 audit rows per station
}

// DefaultStationVaultLimits are the shipped numbers.
func DefaultStationVaultLimits() StationVaultLimits {
	return StationVaultLimits{MaxSecretBytes: 8 << 10, MaxEntries: 64, MaxHistoryPerName: 16, MaxReadLog: 500}
}

// StationVaultEntry is one secret. Secret is populated ONLY by GetStationVaultSecret —
// every other path leaves it empty, so a listing cannot leak a value by accident and a
// caller cannot get one without generating an audit row.
type StationVaultEntry struct {
	Name      string
	Note      string
	SizeBytes int
	SHA256    string
	Rev       int
	ReadCount int
	CreatedAt string
	UpdatedAt string
	DeletedAt string // non-empty means this name is a tombstone, recoverable via Restore
	Secret    string
}

// StationVaultHistoryEntry is one superseded value. It never carries the secret: history
// exists so a human can SEE that something is recoverable and ask for it back by rev,
// not so a listing can hand out every value a name ever had.
type StationVaultHistoryEntry struct {
	Name       string
	Note       string
	SizeBytes  int
	SHA256     string
	Rev        int
	Reason     string // 'updated' or 'deleted'
	ReplacedAt string
}

// StationVaultRead is one logged read.
type StationVaultRead struct {
	Name    string
	Via     string // 'station' or 'console'
	ReadAt  string
	ActorID int64
}

// ErrVaultCapReached is returned when a limit refuses a write.
var ErrVaultCapReached = errors.New("vault cap reached")

// ErrVaultDeleted distinguishes a tombstone from a name that never existed, because the
// recovery action differs: one is Restore, the other is Put.
var ErrVaultDeleted = errors.New("this vault entry is deleted — its value is still recoverable from the console")

// ListStationVault returns metadata for every secret, NEVER a value. Tombstones are
// included and marked, because a human deciding whether a secret is safely gone needs to
// see that it is only soft-deleted.
func (s *Store) ListStationVault(ctx context.Context, stationID string) ([]StationVaultEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT name, note, size_bytes, sha256, rev, read_count, created_at, updated_at, COALESCE(deleted_at,'')
FROM station_vault WHERE station_id=? ORDER BY name`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationVaultEntry
	for rows.Next() {
		var e StationVaultEntry
		if err := rows.Scan(&e.Name, &e.Note, &e.SizeBytes, &e.SHA256, &e.Rev, &e.ReadCount,
			&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PutStationVaultSecret stores or replaces a secret, and is REVERSIBLE: the outgoing
// value is pushed to history first, so an overwrite of the wrong name loses nothing a
// human cannot put back. Writing to a deleted name revives it, keeping one history chain
// per name rather than forking it.
//
// The returned int is how many history revisions were dropped to stay under the cap —
// non-zero means the oldest recoverable values for this name are gone, and the caller is
// expected to say so. A bound nobody is told about is the station-notebook defect, where
// a page silently lost its first seventeen revisions.
func (s *Store) PutStationVaultSecret(ctx context.Context, lim StationVaultLimits, stationID, name, secret, note, tokenID string, actorID int64) (*StationVaultEntry, int, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, 0, errors.New("a vault entry needs a name")
	}
	// Same rule as the locker: a name is a flat label, never a path.
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return nil, 0, fmt.Errorf("vault names are flat labels, not paths (got %q)", name)
	}
	if secret == "" {
		return nil, 0, errors.New("refusing to store an empty secret — to remove one, delete it, which is reversible")
	}
	if len(secret) > lim.MaxSecretBytes {
		return nil, 0, fmt.Errorf("%w: %d bytes, over the %d-byte per-secret cap — the vault holds credentials, not files; the locker takes payloads",
			ErrVaultCapReached, len(secret), lim.MaxSecretBytes)
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var curRev int
	err = tx.QueryRowContext(ctx, `SELECT rev FROM station_vault WHERE station_id=? AND name=?`,
		stationID, name).Scan(&curRev)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}

	if !exists {
		var live int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM station_vault WHERE station_id=? AND deleted_at IS NULL`, stationID).Scan(&live); err != nil {
			return nil, 0, err
		}
		if live >= lim.MaxEntries {
			return nil, 0, fmt.Errorf("%w: %d secrets already, at the %d-entry cap — a vault this full is holding something that belongs elsewhere",
				ErrVaultCapReached, live, lim.MaxEntries)
		}
	}

	sum := sha256.Sum256([]byte(secret))
	digest := hex.EncodeToString(sum[:])
	newRev := 1
	if exists {
		newRev = curRev + 1
		// Keep the OUTGOING value as history before overwriting. 'updated' rather than
		// 'deleted' even when reviving a tombstone: the reason describes why THIS value
		// stopped being current, and a revive supersedes the deleted value.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault_history(station_id, name, secret, note, size_bytes, sha256, rev, reason, replaced_by_token_id, replaced_by_actor_id)
SELECT station_id, name, secret, note, size_bytes, sha256, rev, 'updated', ?, ?
FROM station_vault WHERE station_id=? AND name=?`,
			nullStr(tokenID), actorID, stationID, name); err != nil {
			return nil, 0, err
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE station_vault SET secret=?, note=?, size_bytes=?, sha256=?, rev=?, deleted_at=NULL,
       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       updated_by_token_id=?, updated_by_actor_id=?
WHERE station_id=? AND name=?`,
			secret, note, len(secret), digest, newRev, nullStr(tokenID), actorID, stationID, name); err != nil {
			return nil, 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault(station_id, name, secret, note, size_bytes, sha256, rev, updated_by_token_id, updated_by_actor_id)
VALUES(?,?,?,?,?,?,1,?,?)`,
			stationID, name, secret, note, len(secret), digest, nullStr(tokenID), actorID); err != nil {
			return nil, 0, err
		}
	}

	dropped, err := pruneVaultHistory(ctx, tx, stationID, name, lim.MaxHistoryPerName)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	_ = s.TouchStationActivity(ctx, stationID)
	return &StationVaultEntry{Name: name, Note: note, SizeBytes: len(secret), SHA256: digest, Rev: newRev}, dropped, nil
}

// GetStationVaultSecret returns the value AND logs the read. The two are one operation
// on purpose: a read that can happen without its audit row is a vault whose trail cannot
// answer the only question worth asking after a leak.
//
// via is 'station' (a tool call) or 'console' (a human clicked reveal). Both are reads of
// the same value and belong in one trail.
func (s *Store) GetStationVaultSecret(ctx context.Context, lim StationVaultLimits, stationID, name, via, tokenID string, actorID int64) (*StationVaultEntry, error) {
	if via != "station" && via != "console" {
		return nil, fmt.Errorf("a vault read must say where it came from (got %q)", via)
	}

	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var e StationVaultEntry
	err = tx.QueryRowContext(ctx, `
SELECT name, secret, note, size_bytes, sha256, rev, read_count, created_at, updated_at, COALESCE(deleted_at,'')
FROM station_vault WHERE station_id=? AND name=?`, stationID, name).
		Scan(&e.Name, &e.Secret, &e.Note, &e.SizeBytes, &e.SHA256, &e.Rev, &e.ReadCount,
			&e.CreatedAt, &e.UpdatedAt, &e.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// A tombstone does not hand out its value. It is recoverable, which is a different
	// thing from readable, and the console is where a human decides to bring it back.
	if e.DeletedAt != "" {
		return nil, ErrVaultDeleted
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault_read(station_id, name, via, by_token_id, by_actor_id) VALUES(?,?,?,?,?)`,
		stationID, name, via, nullStr(tokenID), actorID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE station_vault SET read_count=read_count+1 WHERE station_id=? AND name=?`,
		stationID, name); err != nil {
		return nil, err
	}
	// Bounded, but read_count above keeps the TRUE total, so a console can say "the last
	// N of M" rather than implying M is N.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM station_vault_read
WHERE station_id=? AND id NOT IN (
  SELECT id FROM station_vault_read WHERE station_id=? ORDER BY id DESC LIMIT ?)`,
		stationID, stationID, lim.MaxReadLog); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.ReadCount++
	return &e, nil
}

// DeleteStationVaultSecret is a TOMBSTONE, never a DELETE. Vlad's condition on secrets
// living in Ken at all was that storing them "does not modify them or at least it is
// reversible"; station_locker_delete is destructive today and this deliberately is not.
func (s *Store) DeleteStationVaultSecret(ctx context.Context, lim StationVaultLimits, stationID, name, tokenID string, actorID int64) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var deletedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT deleted_at FROM station_vault WHERE station_id=? AND name=?`,
		stationID, name).Scan(&deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if deletedAt.Valid {
		return nil // already a tombstone; deleting twice is not an error
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault_history(station_id, name, secret, note, size_bytes, sha256, rev, reason, replaced_by_token_id, replaced_by_actor_id)
SELECT station_id, name, secret, note, size_bytes, sha256, rev, 'deleted', ?, ?
FROM station_vault WHERE station_id=? AND name=?`,
		nullStr(tokenID), actorID, stationID, name); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_vault SET deleted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       updated_by_token_id=?, updated_by_actor_id=?
WHERE station_id=? AND name=?`, nullStr(tokenID), actorID, stationID, name); err != nil {
		return err
	}
	if _, err := pruneVaultHistory(ctx, tx, stationID, name, lim.MaxHistoryPerName); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreStationVaultSecret brings a name back to its most recent superseded value. This
// is the other half of "reversible" — the console action a human takes after a session
// deleted or overwrote the wrong thing.
func (s *Store) RestoreStationVaultSecret(ctx context.Context, stationID, name, tokenID string, actorID int64) (*StationVaultEntry, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var secret, note, digest string
	var size, rev int
	err = tx.QueryRowContext(ctx, `
SELECT secret, note, size_bytes, sha256, rev FROM station_vault_history
WHERE station_id=? AND name=? ORDER BY rev DESC, id DESC LIMIT 1`, stationID, name).
		Scan(&secret, &note, &size, &digest, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// The restore itself is a write, so the value it displaces goes to history too —
	// otherwise restoring the wrong revision would be the one irreversible act.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault_history(station_id, name, secret, note, size_bytes, sha256, rev, reason, replaced_by_token_id, replaced_by_actor_id)
SELECT station_id, name, secret, note, size_bytes, sha256, rev, 'updated', ?, ?
FROM station_vault WHERE station_id=? AND name=?`,
		nullStr(tokenID), actorID, stationID, name); err != nil {
		return nil, err
	}

	var newRev int
	if err := tx.QueryRowContext(ctx, `SELECT rev+1 FROM station_vault WHERE station_id=? AND name=?`,
		stationID, name).Scan(&newRev); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_vault SET secret=?, note=?, size_bytes=?, sha256=?, rev=?, deleted_at=NULL,
       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       updated_by_token_id=?, updated_by_actor_id=?
WHERE station_id=? AND name=?`,
		secret, note, size, digest, newRev, nullStr(tokenID), actorID, stationID, name); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &StationVaultEntry{Name: name, Note: note, SizeBytes: size, SHA256: digest, Rev: newRev}, nil
}

// StationVaultHistoryFor lists what is recoverable for one name, newest first. Never a
// value: this answers "can I get it back", not "give it to me".
func (s *Store) StationVaultHistoryFor(ctx context.Context, stationID, name string) ([]StationVaultHistoryEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT name, note, size_bytes, sha256, rev, reason, replaced_at
FROM station_vault_history WHERE station_id=? AND name=? ORDER BY rev DESC, id DESC`, stationID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationVaultHistoryEntry
	for rows.Next() {
		var h StationVaultHistoryEntry
		if err := rows.Scan(&h.Name, &h.Note, &h.SizeBytes, &h.SHA256, &h.Rev, &h.Reason, &h.ReplacedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// StationVaultReads returns the retained audit trail for a station, newest first, with
// the TRUE total across every entry as the second return. The two differ once the log
// has been pruned, and the console is expected to show both — "the last 500 of 2,318" is
// a bound an operator can reason about; "500 reads" is a lie.
func (s *Store) StationVaultReads(ctx context.Context, stationID string, limit int) ([]StationVaultRead, int, error) {
	var total int
	if err := s.R.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(read_count),0) FROM station_vault WHERE station_id=?`, stationID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.R.QueryContext(ctx, `
SELECT name, via, read_at, COALESCE(by_actor_id,0)
FROM station_vault_read WHERE station_id=? ORDER BY id DESC LIMIT ?`, stationID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []StationVaultRead
	for rows.Next() {
		var r StationVaultRead
		if err := rows.Scan(&r.Name, &r.Via, &r.ReadAt, &r.ActorID); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// pruneVaultHistory keeps the newest maxRevs superseded values for one name and returns
// how many it dropped, so the caller can TELL somebody. Returning the count rather than
// swallowing it is the whole difference from the notebook's pruning, which is correct,
// silent, and cost a station its original context.
func pruneVaultHistory(ctx context.Context, tx *sql.Tx, stationID, name string, maxRevs int) (int, error) {
	res, err := tx.ExecContext(ctx, `
DELETE FROM station_vault_history
WHERE station_id=? AND name=? AND id NOT IN (
  SELECT id FROM station_vault_history WHERE station_id=? AND name=? ORDER BY rev DESC, id DESC LIMIT ?)`,
		stationID, name, stationID, name, maxRevs)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// StationVaultRecoverableNames reports which names actually have a superseded value to
// go back to. ONE query for the whole station rather than one per entry.
//
// The console needs this rather than inferring it from rev > 1, which is wrong in both
// directions: a secret written once and then deleted is still at rev 1 and IS
// recoverable, and a name whose history has been pruned to nothing by a zero
// history bound is at a high rev and is NOT. Offering a restore control that then fails
// is the same class of defect as hiding one that would have worked.
func (s *Store) StationVaultRecoverableNames(ctx context.Context, stationID string) (map[string]bool, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT DISTINCT name FROM station_vault_history WHERE station_id=?`, stationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}
