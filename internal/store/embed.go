package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// EmbedTarget is a version that needs an embedding computed.
type EmbedTarget struct {
	VersionID int64
	Text      string // title + summary + problem + solution
}

// VersionsNeedingEmbedding returns versions lacking an embedding for modelID
// (limit<=0 means all). Text is what the query vector is compared against.
func (s *Store) VersionsNeedingEmbedding(ctx context.Context, modelID string, limit int) ([]EmbedTarget, error) {
	q := `
SELECT ev.id,
       ev.title || ' ' || ev.summary || ' ' || COALESCE(ev.problem,'') || ' ' || COALESCE(ev.solution,'')
FROM entry_version ev
LEFT JOIN entry_embedding em ON em.version_id = ev.id AND em.model_id = ?
WHERE em.version_id IS NULL`
	args := []any{modelID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.R.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbedTarget
	for rows.Next() {
		var t EmbedTarget
		if err := rows.Scan(&t.VersionID, &t.Text); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertEmbedding stores (or replaces) a version's embedding for a given model.
// The (version_id, model_id) primary key lets multiple models coexist per version;
// OR REPLACE upserts the row for this exact (version, model) pair only.
func (s *Store) UpsertEmbedding(ctx context.Context, versionID int64, modelID string, vec []float32) error {
	_, err := s.W.ExecContext(ctx,
		`INSERT OR REPLACE INTO entry_embedding(version_id, model_id, dim, vec) VALUES(?,?,?,?)`,
		versionID, modelID, len(vec), encodeVec(vec))
	return err
}

// EmbeddingStats reports embedded vs total version counts.
func (s *Store) EmbeddingStats(ctx context.Context) (embedded, total int, err error) {
	if err = s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry_embedding`).Scan(&embedded); err != nil {
		return
	}
	err = s.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM entry_version`).Scan(&total)
	return
}

type vecPair struct {
	vid  int64
	rank int
}

// vectorCandidates cosine-ranks stored embeddings (for versions matching the
// scope state AND the query's model) against qvec, returning the top `limit` as
// (version_id, rank). Filtering by model_id prevents comparing vectors from a
// different model (e.g. during a partial re-backfill). Brute-force in Go — fine
// at single-user scale (the design's flat approach).
func (s *Store) vectorCandidates(ctx context.Context, qvec []float32, modelID, statePred string, limit int) ([]vecPair, error) {
	rows, err := s.R.QueryContext(ctx, fmt.Sprintf(`
SELECT em.version_id, em.vec FROM entry_embedding em
JOIN entry_version ev ON ev.id = em.version_id
WHERE em.model_id = ? AND %s`, statePred), modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		vid int64
		sim float64
	}
	var cands []scored
	qn := l2norm(qvec)
	if qn == 0 {
		return nil, nil // a zero query vector is orthogonal to everything — no semantic signal
	}
	for rows.Next() {
		var vid int64
		var blob []byte
		if err := rows.Scan(&vid, &blob); err != nil {
			return nil, err
		}
		v := decodeVec(blob)
		if len(v) != len(qvec) {
			continue // dim / model mismatch — skip
		}
		cands = append(cands, scored{vid, cosine(qvec, v, qn)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].sim > cands[j].sim })
	if len(cands) > limit {
		cands = cands[:limit]
	}
	out := make([]vecPair, len(cands))
	for i, c := range cands {
		out[i] = vecPair{c.vid, i + 1}
	}
	return out, nil
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

func l2norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func cosine(a, b []float32, anorm float64) float64 {
	var dot, bn float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		bn += float64(b[i]) * float64(b[i])
	}
	if anorm == 0 || bn == 0 {
		return 0
	}
	return dot / (anorm * math.Sqrt(bn))
}
