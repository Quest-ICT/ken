package store

import (
	"context"
	"database/sql"
	"errors"
)

// ActorCandidate is an actor the console can act on, with whether it already holds a comm token.
type ActorCandidate struct {
	ID       int64
	Kind     string
	Name     string
	HasComm  bool
	CommTags string // labels of its comm tokens, for a human to recognise the machine
}

// ActorsWithCommStatus lists actors that could sensibly author an agent's writes,
// comm-token holders first.
//
// Two callers, one question. A STATION KEY must be minted under the actor holding
// that machine's comm token, or the hearsay marker silently never fires. A CONNECTOR
// must be pointed at one for exactly the same reason — its writes are authored by the
// grant's actor, and an actor invented from a client's display name can never match
// COMM traffic. Named for the property it reports rather than for the first caller.
//
// This exists because the previous default was actively wrong in a way nothing
// surfaced. A station key was minted under a HUMAN actor — the CLI hardcoded the
// kind, the console used the logged-in curator's — while COMM tokens default to an
// `ai` actor, and `(kind, display_name)` is unique, so the two were different rows
// with different ids. The hearsay window joins on the actor, so it could never match:
// `hearsay_at_write` was permanently false on any deployment that followed the
// documented setup, and the only remedy the shipped commands offered was to
// deliberately mislabel an AI session's token as human — repairing one provenance
// signal by corrupting the one the whole curation model rests on.
//
// The marker is biased toward over-reporting precisely because a false negative
// silently launders hearsay into the knowledge base. A mismatched actor produced
// exactly that false negative, on every station write, with no symptom.
func (s *Store) ActorsWithCommStatus(ctx context.Context) ([]ActorCandidate, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT a.id, a.kind, a.display_name,
       EXISTS(SELECT 1 FROM api_token t
               WHERE t.actor_id=a.id AND t.revoked_at IS NULL
                 AND t.scopes LIKE '%"comm"%') AS has_comm,
       COALESCE((SELECT GROUP_CONCAT(COALESCE(t.label,''), ', ') FROM api_token t
                  WHERE t.actor_id=a.id AND t.revoked_at IS NULL
                    AND t.scopes LIKE '%"comm"%'), '')
  FROM actor a
 ORDER BY has_comm DESC, a.kind, a.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActorCandidate
	for rows.Next() {
		var c ActorCandidate
		if err := rows.Scan(&c.ID, &c.Kind, &c.Name, &c.HasComm, &c.CommTags); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ActorExists reports whether an actor id names a live row.
//
// Used where an id arrives from a FORM rather than from a lookup: a connector's
// authoring actor is chosen by an operator at consent time, and authorship is the
// field a human reads when deciding whether to promote a proposal. An unvalidated id
// there would attribute one connector's writes to another identity, which is the one
// kind of wrong the curation gate cannot repair afterwards.
func (s *Store) ActorExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM actor WHERE id=?`, id).Scan(&n)
	return n > 0, err
}

// FindActor resolves an existing actor by kind and name WITHOUT creating one.
// Distinct from FindOrCreateActor on purpose: minting a station key should never
// invent an actor, because a typo would then produce a key that authenticates
// perfectly and marks nothing.
func (s *Store) FindActor(ctx context.Context, kind, name string) (int64, error) {
	var id int64
	err := s.R.QueryRowContext(ctx,
		`SELECT id FROM actor WHERE kind=? AND display_name=?`, kind, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}
