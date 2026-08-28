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
// exists so a human can SEE that something is recoverable and ask for it back, not so a
// listing can hand out every value a name ever had.
//
// ADDRESSED BY ID, NOT BY REV. This doc used to say "ask for it back by rev" and that was
// never constructible: station_vault_history has no unique constraint on
// (station_id, name, rev), and a put -> delete -> restore sequence really does produce two
// rows at the same rev. `rev` is the revision the value was superseded FROM, so it repeats.
// The row id is the only stable handle, which is why it is on the struct.
type StationVaultHistoryEntry struct {
	ID         int64 // the handle RestoreStationVaultSecret takes; rev is not unique
	Name       string
	Note       string
	SizeBytes  int
	SHA256     string
	Rev        int
	Reason     string // 'updated' or 'deleted'
	ReplacedAt string
}

// StationVaultRead is one logged read.
//
// The reader is carried as a NAME, not as by_actor_id. The console is the only consumer
// and a bare integer is not an identity a human can read — this struct shipped with the
// id populated and the template printing name, via and time, so the trail answered every
// question about a read except the one it is kept for. ActorKind/ActorName mirror
// StationKey (stations.go:305) because the same page already renders an actor that way.
//
// Both are empty when the row carries no actor or its actor row is gone. That case is
// rendered in words rather than as an em-dash: a half-finished audit line that looks
// exactly like a finished one is the mistake the link-revoke count already paid for
// (web/stations.go, "rendering that as an em-dash made the half-finished case look
// exactly like the finished one").
type StationVaultRead struct {
	Name      string
	Via       string // 'station' or 'console'
	ReadAt    string
	ActorKind string
	ActorName string
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

	// DIGEST AND SIZE ARE COMPUTED OVER THE PLAINTEXT, DELIBERATELY. The digest is what the
	// console shows to identify a secret and what an operator compares against an external copy;
	// hashing the ciphertext would make it change on every rewrite of an unchanged value and stop
	// being comparable to anything. The cost is stated in vaultcrypt.go: a LOW-ENTROPY secret
	// stays guessable from its digest, which travels in the same backup as the ciphertext.
	sum := sha256.Sum256([]byte(secret))
	digest := hex.EncodeToString(sum[:])

	// Encrypted from here on. `sealed` goes to the database; `secret` is used only for the size
	// and digest above — keeping them separate variables is what stops a later edit from
	// accidentally storing the plaintext.
	sealed, err := s.sealVaultSecret(secret)
	if err != nil {
		return nil, 0, err
	}

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
			sealed, note, len(secret), digest, newRev, nullStr(tokenID), actorID, stationID, name); err != nil {
			return nil, 0, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault(station_id, name, secret, note, size_bytes, sha256, rev, updated_by_token_id, updated_by_actor_id)
VALUES(?,?,?,?,?,?,1,?,?)`,
			stationID, name, sealed, note, len(secret), digest, nullStr(tokenID), actorID); err != nil {
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
	// 'transfer' is the third provenance, added with SendStationVaultSecret: it records that a
	// value left this vault for ANOTHER station's, which is a materially different event from a
	// session reading its own secret and must not be filed as one.
	if via != "station" && via != "console" && via != "transfer" {
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
	// Decrypt. A value with no `kv1:` prefix predates encryption and comes back unchanged, which
	// is what lets an existing deployment upgrade without a migration — see openVaultSecret.
	if e.Secret, err = s.openVaultSecret(e.Secret); err != nil {
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
	// *** A TRANSFER DOES NOT COUNT AS A RETRIEVAL. ***
	//
	// The console renders read_count as "how often this credential was RETRIEVED", and a session
	// handing a secret to another station is a different event — which is the entire reason it
	// got its own `via` and its own migration. Counting it here as well would state the same act
	// twice, once under a label that means something else, and an operator auditing "this key was
	// read 4 times" would be reading a number that includes sends.
	//
	// The audit row above is written for EVERY provenance including 'transfer', so nothing is
	// lost: the event is recorded, it is attributable, and it is distinguishable. Only the
	// COUNTER — the one number that carries a specific English meaning into the console — stays
	// about retrievals.
	//
	// Found by ken-prod-ops on 2026-08-26, in the first live transfer ever performed: m600 never
	// called station_vault_get and its sender copy still showed read_count 1. Vlad's ruling: "I
	// have no reason to go against your inclination. If anything, it should be documented so we
	// don't chase after it later." This comment is that documentation.
	if via != "transfer" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE station_vault SET read_count=read_count+1 WHERE station_id=? AND name=?`,
			stationID, name); err != nil {
			return nil, err
		}
	}
	// Bounded, but read_count above keeps the TRUE total of RETRIEVALS, so a console can say
	// "the last N of M" rather than implying M is N. Transfers are in this log and not in that
	// counter, so the two answer different questions on purpose.
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

// RestoreStationVaultSecret brings a name back to a superseded value — `historyID` names
// which one, and 0 means the most recent. It is the other half of "reversible": the console
// action a human takes after a session deleted or overwrote the wrong thing.
//
// IT USED TO BE ABLE TO REACH EXACTLY ONE VALUE OF SIXTEEN, AND USING IT DESTROYED THE REST.
// Two defects, both measured on 2026-08-24 before this was written:
//
//	five puts A,B,C,D,E then six restores  ->  D E D E D E
//	history rows afterwards, bound of 3    ->  9
//
// The read was hardcoded to `ORDER BY rev DESC LIMIT 1`, and a restore is itself a write, so
// the value it displaced went BACK into history at a higher rev — the newest two swapped
// forever and A, B and C were unreachable by any code in the tree. And the restore path never
// called pruneVaultHistory (its only call sites were put and delete), so exercising recovery
// inflated history with churn duplicates of the same two values until an ordinary put dropped
// the real history to make room. Three documents promised otherwise — OPERATION.md's "what
// makes a vault write reversible", STATIONS.md's "16 revisions", and the settings help's "how
// many superseded values stay RECOVERABLE".
//
// A NAMED ROW THAT IS NOT THIS NAME'S IS REFUSED, NOT SILENTLY IGNORED. Falling back to the
// newest when the id does not match would restore a value the caller did not ask for and
// report success — the same defect in a new place.
//
// Returns the entry and how many history rows the prune dropped, because a recovery feature
// that silently consumes recovery depth is what this function just was.
func (s *Store) RestoreStationVaultSecret(ctx context.Context, lim StationVaultLimits, stationID, name string, historyID int64, tokenID string, actorID int64) (*StationVaultEntry, int, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var secret, note, digest string
	var size, rev int
	if historyID > 0 {
		err = tx.QueryRowContext(ctx, `
SELECT secret, note, size_bytes, sha256, rev FROM station_vault_history
WHERE id=? AND station_id=? AND name=?`, historyID, stationID, name).
			Scan(&secret, &note, &size, &digest, &rev)
	} else {
		err = tx.QueryRowContext(ctx, `
SELECT secret, note, size_bytes, sha256, rev FROM station_vault_history
WHERE station_id=? AND name=? ORDER BY rev DESC, id DESC LIMIT 1`, stationID, name).
			Scan(&secret, &note, &size, &digest, &rev)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}

	// The restore itself is a write, so the value it displaces goes to history too —
	// otherwise restoring the wrong revision would be the one irreversible act.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO station_vault_history(station_id, name, secret, note, size_bytes, sha256, rev, reason, replaced_by_token_id, replaced_by_actor_id)
SELECT station_id, name, secret, note, size_bytes, sha256, rev, 'updated', ?, ?
FROM station_vault WHERE station_id=? AND name=?`,
		nullStr(tokenID), actorID, stationID, name); err != nil {
		return nil, 0, err
	}

	var newRev int
	if err := tx.QueryRowContext(ctx, `SELECT rev+1 FROM station_vault WHERE station_id=? AND name=?`,
		stationID, name).Scan(&newRev); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE station_vault SET secret=?, note=?, size_bytes=?, sha256=?, rev=?, deleted_at=NULL,
       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
       updated_by_token_id=?, updated_by_actor_id=?
WHERE station_id=? AND name=?`,
		secret, note, size, digest, newRev, nullStr(tokenID), actorID, stationID, name); err != nil {
		return nil, 0, err
	}
	// PRUNE HERE TOO. Put and delete have always done this; restore never did, which is why
	// using the recovery feature was the thing that consumed the recovery depth.
	dropped, err := pruneVaultHistory(ctx, tx, stationID, name, lim.MaxHistoryPerName)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return &StationVaultEntry{Name: name, Note: note, SizeBytes: size, SHA256: digest, Rev: newRev}, dropped, nil
}

// StationVaultHistoryFor lists what is recoverable for one name, newest first. Never a
// value: this answers "can I get it back", not "give it to me".
func (s *Store) StationVaultHistoryFor(ctx context.Context, stationID, name string) ([]StationVaultHistoryEntry, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT id, name, note, size_bytes, sha256, rev, reason, replaced_at
FROM station_vault_history WHERE station_id=? AND name=? ORDER BY rev DESC, id DESC`, stationID, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StationVaultHistoryEntry
	for rows.Next() {
		var h StationVaultHistoryEntry
		if err := rows.Scan(&h.ID, &h.Name, &h.Note, &h.SizeBytes, &h.SHA256, &h.Rev, &h.Reason, &h.ReplacedAt); err != nil {
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
	// LEFT JOIN, never an inner one: a read whose actor row cannot be resolved still
	// belongs in the trail. An audit log that silently drops the rows it cannot fully
	// explain understates exposure, which is the failure this whole table exists against.
	rows, err := s.R.QueryContext(ctx, `
SELECT r.name, r.via, r.read_at, COALESCE(a.kind,''), COALESCE(a.display_name,'')
FROM station_vault_read r LEFT JOIN actor a ON a.id = r.by_actor_id
WHERE r.station_id=? ORDER BY r.id DESC LIMIT ?`, stationID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []StationVaultRead
	for rows.Next() {
		var r StationVaultRead
		if err := rows.Scan(&r.Name, &r.Via, &r.ReadAt, &r.ActorKind, &r.ActorName); err != nil {
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

// ErrStationsNotLinked refuses a vault transfer between two stations a human has not connected.
//
// The SAME gate as comm_send{to_station}: an approved link is a human saying these two posts may
// talk. Reusing it means secret transfer needs no second approval ceremony — which is the whole
// point, since "numerous keys, tokens, vouchers, approvals" is the tax this is meant to remove —
// while still being impossible between two stations nobody connected.
var ErrStationsNotLinked = errors.New("those stations are not linked, so a secret cannot pass between them. A link is created by the first message: send the peer anything with comm_send{to_station} and the relationship exists. If you have already written to them, your human has SUSPENDED that link at Ken's console — tell them, do not retry")

// ErrCannotSendToSelf refuses a transfer whose source and destination are the same vault.
var ErrCannotSendToSelf = errors.New("that is this station's own vault — a transfer to yourself would overwrite the secret with itself and bump its revision for nothing")

// SendStationVaultSecret hands a secret from one station's vault into another's, WITHOUT the value
// ever leaving the server.
//
// *** WHY THIS EXISTS: THERE WAS NO SAFE WAY TO GIVE A CREDENTIAL TO A SESSION ON ANOTHER
// MACHINE. *** Every available route was wrong in a different way. Pasting it into a COMM message
// body puts it in the message store under retention AND in both sessions' transcripts. Relaying it
// as a file writes plaintext bytes to the server's disk until the sweeper runs. Asking the human to
// copy it by hand is exactly the credential tax Vlad's standing requirement exists to remove:
// "it should not require numerous keys, tokens, vouchers, approvals, etc."
//
// THE VALUE MOVES INSIDE THE SERVER AND NOWHERE ELSE. It is decrypted from the sender's row and
// re-encrypted into the recipient's under the same key (vaultcrypt.go), in this process. It never
// enters a message body, never touches data/comm/, never appears in a tool result on either side,
// and never reaches either session's transcript. What the sender gets back is a receipt; what the
// recipient gets is an entry in their own vault they must still read through the audited path.
//
// AUTHORISED BY THE LINK, NOT BY A NEW CEREMONY. AreStationsLinked is the same predicate
// comm_send{to_station} uses, so a human who has already said "these two may talk" has said enough.
//
// AUDITED ON BOTH SIDES BY CONSTRUCTION: the read is logged against the SENDER with via='transfer'
// — distinguishable from an ordinary read, so "who saw this secret" stays answerable — and the
// write lands through PutStationVaultSecret, which gives the recipient's copy the same encryption,
// history, caps and reversibility as anything they stored themselves.
//
// THE SENDER KEEPS THEIR COPY. This is a COPY, not a move: a transfer that emptied the sender's
// vault would make a mistyped station id destructive, and the vault's founding rule is that every
// write is reversible.
func (s *Store) SendStationVaultSecret(ctx context.Context, lim StationVaultLimits,
	fromStation, toStation, name, asName, tokenID string, actorID int64) (*StationVaultEntry, int, error) {

	if strings.TrimSpace(toStation) == "" {
		return nil, 0, errors.New("no destination station given")
	}
	if fromStation == toStation {
		return nil, 0, ErrCannotSendToSelf
	}
	if strings.TrimSpace(asName) == "" {
		asName = name
	}

	// The destination must exist before anything is read, so a typo cannot cost a read-audit
	// entry against a secret that was never going anywhere.
	ok, err := s.StationExists(ctx, toStation)
	if err != nil {
		return nil, 0, err
	}
	if !ok {
		return nil, 0, ErrNotFound
	}
	linked, err := s.AreStationsLinked(ctx, fromStation, toStation)
	if err != nil {
		return nil, 0, err
	}
	if !linked {
		return nil, 0, ErrStationsNotLinked
	}

	// Read through the ordinary audited path, so the transfer is recorded against the sender
	// exactly as a read is — with via='transfer' marking what kind of read it was.
	src, err := s.GetStationVaultSecret(ctx, lim, fromStation, name, "transfer", tokenID, actorID)
	if err != nil {
		return nil, 0, err
	}

	note := "received from station " + fromStation
	if src.Note != "" {
		note = src.Note + " (" + note + ")"
	}
	// The recipient's copy goes in through the normal write path: encrypted, capped, with the
	// displaced value kept in history. Their caps apply, not the sender's — a station cannot be
	// pushed past its own limits by someone else's generosity.
	entry, dropped, err := s.PutStationVaultSecret(ctx, lim, toStation, asName, src.Secret, note, tokenID, actorID)
	if err != nil {
		return nil, 0, err
	}
	return entry, dropped, nil
}
