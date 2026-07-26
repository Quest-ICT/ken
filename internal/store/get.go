package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Quest-ICT/ken/internal/model"
)

// Get returns full entries for the given slugs (curated head, or the provisional
// version for an uncurated draft). Unknown slugs are returned in missing. Each
// found entry bumps use_count. detailed adds provenance.
func (s *Store) Get(ctx context.Context, slugs []string, detailed bool) (entries []model.Entry, missing []string, err error) {
	for _, slug := range slugs {
		e, gerr := s.getOne(ctx, slug, true, detailed)
		switch {
		case errors.Is(gerr, sql.ErrNoRows):
			missing = append(missing, slug)
		case gerr != nil:
			return nil, nil, gerr
		default:
			entries = append(entries, *e)
		}
	}
	return entries, missing, nil
}

// GetEntry returns one entry without bumping use_count (for the human web UI).
func (s *Store) GetEntry(ctx context.Context, slug string) (*model.Entry, error) {
	e, err := s.getOne(ctx, slug, false, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func (s *Store) getOne(ctx context.Context, slug string, bump, detailed bool) (*model.Entry, error) {
	var (
		e          model.Entry
		curatedVID sql.NullInt64
		provVID    sql.NullInt64
		tagsJSON   string
		trigJSON   string
	)
	err := s.R.QueryRowContext(ctx, `
SELECT slug, kind, title, summary, COALESCE(category,''), tags, triggers,
       lifecycle, staleness, curated_rev, use_count,
       curated_version_id, provisional_version_id
FROM entry WHERE slug = ?`, slug).Scan(
		&e.Slug, &e.Kind, &e.Title, &e.Summary, &e.Category, &tagsJSON, &trigJSON,
		&e.Lifecycle, &e.Staleness, &e.CuratedRev, &e.UseCount, &curatedVID, &provVID)
	if err != nil {
		return nil, err
	}
	e.Tags = jsonStrings(tagsJSON)
	e.Triggers = jsonStrings(trigJSON)
	e.HasProvisional = provVID.Valid

	// Body = the curated head, or (for a draft with no curated version yet) the
	// best provisional, so an uncurated entry still returns content.
	headVID := curatedVID
	if !headVID.Valid {
		headVID = provVID
	}
	if headVID.Valid {
		body, err := s.versionBody(ctx, headVID.Int64)
		if err != nil {
			return nil, err
		}
		e.Head = body
		if detailed {
			prov, err := s.versionProvenance(ctx, headVID.Int64)
			if err != nil {
				return nil, err
			}
			e.Provenance = prov
		}
	}

	if bump {
		_, _ = s.W.ExecContext(ctx, `UPDATE entry SET use_count = use_count + 1 WHERE slug = ?`, slug)
	}
	return &e, nil
}

func (s *Store) versionBody(ctx context.Context, vid int64) (*model.VersionBody, error) {
	var (
		b            model.VersionBody
		codeJSON     string
		verifiedJSON string
	)
	err := s.R.QueryRowContext(ctx, `
SELECT rev_no, COALESCE(problem,''), COALESCE(solution,''),
       COALESCE(rationale,''), COALESCE(caveats,''), code, verified_against
FROM entry_version WHERE id = ?`, vid).Scan(
		&b.RevNo, &b.Problem, &b.Solution, &b.Rationale, &b.Caveats, &codeJSON, &verifiedJSON)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(codeJSON), &b.Code)
	_ = json.Unmarshal([]byte(verifiedJSON), &b.VerifiedAgainst)
	return &b, nil
}

func (s *Store) versionProvenance(ctx context.Context, vid int64) (*model.EntryProvenance, error) {
	var p model.EntryProvenance
	err := s.R.QueryRowContext(ctx, `
SELECT state, COALESCE(author_kind,''), COALESCE(confidence,0), COALESCE(change_note,'')
FROM entry_version WHERE id = ?`, vid).Scan(&p.State, &p.AuthorKind, &p.Confidence, &p.ChangeNote)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func jsonStrings(s string) []string {
	var out []string
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}
