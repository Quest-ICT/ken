package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Quest-ICT/ken/internal/lang"
	"github.com/Quest-ICT/ken/internal/model"
)

// Content is the writable content of an entry version.
type Content struct {
	Title, Summary, Problem, Solution, Rationale, Caveats string
	Code                                                  []model.CodeSnippet
	Tags, Triggers, AppliesTo                             []string
	VerifiedAgainst                                       []model.VerifiedRef
}

// Patch carries the fields to change in an enhancement; nil fields inherit from
// the based-on version (so an enhancement need only send what it changes).
type Patch struct {
	Title, Summary, Problem, Solution, Rationale, Caveats *string
	Code                                                  *[]model.CodeSnippet
	Tags, Triggers, AppliesTo                             *[]string
	VerifiedAgainst                                       *[]model.VerifiedRef
}

// rowQ is satisfied by both *sql.Tx and *sql.DB.
type rowQ interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SaveInput creates a new draft entry with its first (proposed) version.
type SaveInput struct {
	Slug          string // optional; derived from the title if empty
	Kind          string
	Category      string
	Content       Content
	Confidence    float64
	AuthorActorID int64 // 0 => NULL (e.g. the dev token)
	AuthorKind    string
	SessionID     string
	Links         []LinkInput
	// ViaCommKind says WHICH kind of traffic the marker saw: "directed" when somebody
	// addressed this actor specifically, "broadcast" when it was one of several
	// recipients of a room message. Empty when unknown — including every version
	// written before the distinction existed, which is honest rather than convenient:
	// inventing a kind for those would be fabricating provenance.
	ViaCommKind string

	// ViaComm marks the version as possibly second-hand: the authoring token had
	// recently RECEIVED an inter-session message (docs/COMM.md §7). It is a prompt
	// for the curator's judgement, not a verdict — false means "no signal", never
	// "known first-hand".
	ViaComm bool
}

// LinkInput is a [[wikilink]] to create from the new entry.
type LinkInput struct{ ToSlug, LinkType string }

// SaveResult reports the created entry.
type SaveResult struct {
	Slug      string
	EntryID   int64
	VersionID int64
	RevNo     int
	Lifecycle string
	State     string
}

// Save creates a new draft entry (lifecycle 'draft', one 'proposed' version).
func (s *Store) Save(ctx context.Context, in SaveInput) (SaveResult, error) {
	if in.Content.Title == "" || in.Content.Summary == "" || in.Kind == "" {
		return SaveResult{}, fmt.Errorf("%w: kind, title and summary are required", ErrInvalid)
	}
	if err := validateContent(in.Content); err != nil {
		return SaveResult{}, err
	}
	if !validKinds[in.Kind] {
		return SaveResult{}, fmt.Errorf("%w: kind must be one of user, feedback, project, reference (got %q)", ErrInvalid, in.Kind)
	}
	// Validate link_types up front: a bad one used to be swallowed by INSERT OR IGNORE
	// below (the entry saved, the requested link silently vanished on a success call).
	for _, l := range in.Links {
		if l.ToSlug == "" || l.LinkType == "" {
			continue
		}
		if !validLinkTypes[l.LinkType] {
			return SaveResult{}, fmt.Errorf("%w: link_type must be one of relates, supersedes, refutes, depends_on (got %q)", ErrInvalid, l.LinkType)
		}
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback()

	explicit := in.Slug != ""
	slug := in.Slug
	if slug == "" {
		slug = slugify(in.Content.Title)
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM entry WHERE slug=?`, slug).Scan(&one)
	switch {
	case err == nil && explicit:
		return SaveResult{}, ErrSlugConflict
	case err == nil && !explicit:
		if slug, err = freeSlug(ctx, tx, slug); err != nil {
			return SaveResult{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		// slug is free
	default:
		return SaveResult{}, err
	}

	res, err := tx.ExecContext(ctx, `
INSERT INTO entry(slug,kind,title,summary,category,tags,triggers,lifecycle,staleness,updater)
VALUES(?,?,?,?,?,?,?,'draft','fresh',?)`,
		slug, in.Kind, in.Content.Title, in.Content.Summary, nullStr(in.Category),
		jsonArr(in.Content.Tags), jsonArr(in.Content.Triggers), authorLabel(in.AuthorKind))
	if err != nil {
		return SaveResult{}, err
	}
	entryID, _ := res.LastInsertId()

	vid, revNo, err := insertVersion(ctx, tx, entryID, 1, "proposed", 0, in.Content,
		in.AuthorActorID, in.AuthorKind, in.SessionID, in.Confidence, "initial capture",
		s.detectLang(in.Content.prose()...), in.ViaComm, in.ViaCommKind)
	if err != nil {
		return SaveResult{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE entry SET provisional_version_id=?, lock_version=lock_version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		vid, entryID); err != nil {
		return SaveResult{}, err
	}

	for _, l := range in.Links {
		if l.ToSlug == "" || l.LinkType == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO entry_link(from_entry_id,to_slug,to_entry_id,link_type,created_by)
VALUES(?,?,(SELECT id FROM entry WHERE slug=?),?,?)`,
			entryID, l.ToSlug, l.ToSlug, l.LinkType, nullActor(in.AuthorActorID)); err != nil {
			return SaveResult{}, err
		}
	}

	if err := insertEvent(ctx, tx, entryID, vid, "proposed", "", "proposed",
		in.AuthorActorID, in.AuthorKind, in.SessionID, "initial capture"); err != nil {
		return SaveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Slug: slug, EntryID: entryID, VersionID: vid, RevNo: revNo, Lifecycle: "draft", State: "proposed"}, nil
}

// ProposeInput appends an enhancement to an existing entry.
type ProposeInput struct {
	Slug          string
	BasedOnRev    int // 0 => base on the current curated head
	ChangeNote    string
	Confidence    float64
	AuthorActorID int64
	AuthorKind    string
	SessionID     string
	Patch         Patch
	// ViaComm — see SaveInput.ViaComm.
	ViaComm bool
	// ViaCommKind — see SaveInput.ViaCommKind.
	ViaCommKind string
}

// ProposeResult reports the appended version and any rebase warning.
type ProposeResult struct {
	Slug      string
	VersionID int64
	RevNo     int
	State     string
	Warning   string
}

// ProposeEnhancement appends an immutable 'proposed' version. It never moves the
// curated head — knowledge is persisted the instant it is proposed.
func (s *Store) ProposeEnhancement(ctx context.Context, in ProposeInput) (ProposeResult, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return ProposeResult{}, err
	}
	defer tx.Rollback()

	var (
		entryID    int64
		curatedVID sql.NullInt64
		maxRev     int
	)
	err = tx.QueryRowContext(ctx, `
SELECT id, curated_version_id,
       (SELECT COALESCE(MAX(rev_no),0) FROM entry_version WHERE entry_id=entry.id)
FROM entry WHERE slug=?`, in.Slug).Scan(&entryID, &curatedVID, &maxRev)
	if errors.Is(err, sql.ErrNoRows) {
		return ProposeResult{}, ErrNotFound
	}
	if err != nil {
		return ProposeResult{}, err
	}

	var headRev int
	if curatedVID.Valid {
		_ = tx.QueryRowContext(ctx, `SELECT rev_no FROM entry_version WHERE id=?`, curatedVID.Int64).Scan(&headRev)
	}

	var baseVID int64
	var baseRev int
	switch {
	case in.BasedOnRev > 0:
		err = tx.QueryRowContext(ctx, `SELECT id, rev_no FROM entry_version WHERE entry_id=? AND rev_no=?`, entryID, in.BasedOnRev).Scan(&baseVID, &baseRev)
	case curatedVID.Valid:
		err = tx.QueryRowContext(ctx, `SELECT id, rev_no FROM entry_version WHERE id=?`, curatedVID.Int64).Scan(&baseVID, &baseRev)
	default:
		err = tx.QueryRowContext(ctx, `SELECT id, rev_no FROM entry_version WHERE entry_id=? ORDER BY rev_no DESC LIMIT 1`, entryID).Scan(&baseVID, &baseRev)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ProposeResult{}, errors.New("based_on_rev not found")
	}
	if err != nil {
		return ProposeResult{}, err
	}

	base, err := loadContent(ctx, tx, baseVID)
	if err != nil {
		return ProposeResult{}, err
	}
	merged := applyPatch(base, in.Patch)
	if err := validateContent(merged); err != nil {
		return ProposeResult{}, err
	}

	// Content language: detect over the DELTA (the prose fields this enhancement
	// changed) so a small foreign edit is flagged, not hidden behind the merged
	// text's dominant language. When the enhancement touched no prose (e.g. only
	// code or tags), inherit the base version's language rather than re-detecting.
	contentLang := s.detectLang(patchProse(in.Patch)...)
	if len(patchProse(in.Patch)) == 0 {
		contentLang = lang.Und
		_ = tx.QueryRowContext(ctx, `SELECT COALESCE(content_lang, ?) FROM entry_version WHERE id=?`, lang.Und, baseVID).Scan(&contentLang)
	}

	newRev := maxRev + 1
	vid, _, err := insertVersion(ctx, tx, entryID, newRev, "proposed", baseVID, merged,
		in.AuthorActorID, in.AuthorKind, in.SessionID, in.Confidence, in.ChangeNote, contentLang, in.ViaComm, in.ViaCommKind)
	if err != nil {
		return ProposeResult{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE entry SET provisional_version_id=?, lock_version=lock_version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		vid, entryID); err != nil {
		return ProposeResult{}, err
	}
	if err := insertEvent(ctx, tx, entryID, vid, "proposed", "", "proposed",
		in.AuthorActorID, in.AuthorKind, in.SessionID, in.ChangeNote); err != nil {
		return ProposeResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProposeResult{}, err
	}

	out := ProposeResult{Slug: in.Slug, VersionID: vid, RevNo: newRev, State: "proposed"}
	if curatedVID.Valid && baseRev != headRev {
		out.Warning = fmt.Sprintf("rebase: curated head is rev %d but you based on rev %d — review will show the drift", headRev, baseRev)
	}
	return out, nil
}

// FlagStale marks an entry stale (still authoritative, ranks lower) and records
// the concern. Raising a concern is safe/additive; asserting freshness is not.
func (s *Store) FlagStale(ctx context.Context, slug, reason string, actorID int64, actorKind string) (string, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var entryID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM entry WHERE slug=?`, slug).Scan(&entryID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entry SET staleness='stale', lock_version=lock_version+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		entryID); err != nil {
		return "", err
	}
	if err := insertEvent(ctx, tx, entryID, 0, "flagged_stale", "", "stale", actorID, actorKind, "", reason); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "stale", nil
}

// --- shared write helpers ---

// viaCommVal maps the hearsay flag to its column value: 1 when set, SQL NULL
// otherwise. NULL rather than 0 on purpose — the column means "no signal", not
// "known first-hand", so an unmarked row is indistinguishable from every row that
// predates the feature, and the partial index stays small.
func viaCommVal(b bool) any {
	if b {
		return 1
	}
	return nil
}

func insertVersion(ctx context.Context, tx *sql.Tx, entryID int64, revNo int, state string, parentVID int64,
	c Content, actorID int64, authorKind, sessionID string, confidence float64, changeNote, contentLang string,
	viaComm bool, viaCommKind string) (int64, int, error) {
	res, err := tx.ExecContext(ctx, `
INSERT INTO entry_version(entry_id,rev_no,state,parent_version_id,title,summary,problem,solution,rationale,caveats,
                          code,tags,triggers,applies_to,verified_against,
                          author_actor_id,author_kind,session_id,confidence,change_note,content_lang,via_comm,via_comm_kind)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		entryID, revNo, state, nullVID(parentVID),
		c.Title, c.Summary, nullStr(c.Problem), nullStr(c.Solution), nullStr(c.Rationale), nullStr(c.Caveats),
		jsonCode(c.Code), jsonArr(c.Tags), jsonArr(c.Triggers), jsonArr(c.AppliesTo), jsonVerified(c.VerifiedAgainst),
		nullActor(actorID), nullStr(authorKind), nullStr(sessionID), confidence, nullStr(changeNote), nullStr(contentLang),
		viaCommVal(viaComm), nullStr(viaCommKind))
	if err != nil {
		return 0, 0, err
	}
	vid, _ := res.LastInsertId()
	return vid, revNo, nil
}

// prose returns a version's human-readable fields (the only text language is
// detected over — code/triggers/tags stay language-neutral retrieval keys).
func (c Content) prose() []string {
	return []string{c.Title, c.Summary, c.Problem, c.Solution, c.Rationale, c.Caveats}
}

// patchProse returns the prose fields an enhancement actually CHANGED. Language is
// detected over the delta, not the merged version, so a small foreign addition to
// a mostly-in-language entry is still flagged (detecting the merged text would
// hide it behind the dominant language).
func patchProse(p Patch) []string {
	var out []string
	for _, f := range []*string{p.Title, p.Summary, p.Problem, p.Solution, p.Rationale, p.Caveats} {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

func insertEvent(ctx context.Context, tx *sql.Tx, entryID, versionID int64, eventType, fromState, toState string,
	actorID int64, actorKind, sessionID, note string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO curation_event(entry_id,version_id,event_type,from_state,to_state,actor_id,actor_kind,session_id,note)
VALUES(?,?,?,?,?,?,?,?,?)`,
		entryID, nullVID(versionID), eventType, nullStr(fromState), nullStr(toState),
		nullActor(actorID), nullStr(actorKind), nullStr(sessionID), nullStr(note))
	return err
}

func loadContent(ctx context.Context, q rowQ, vid int64) (Content, error) {
	var c Content
	var code, tags, trig, applies, verified string
	err := q.QueryRowContext(ctx, `
SELECT title, summary, COALESCE(problem,''), COALESCE(solution,''), COALESCE(rationale,''), COALESCE(caveats,''),
       code, tags, triggers, applies_to, verified_against
FROM entry_version WHERE id=?`, vid).Scan(
		&c.Title, &c.Summary, &c.Problem, &c.Solution, &c.Rationale, &c.Caveats,
		&code, &tags, &trig, &applies, &verified)
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(code), &c.Code)
	_ = json.Unmarshal([]byte(tags), &c.Tags)
	_ = json.Unmarshal([]byte(trig), &c.Triggers)
	_ = json.Unmarshal([]byte(applies), &c.AppliesTo)
	_ = json.Unmarshal([]byte(verified), &c.VerifiedAgainst)
	return c, nil
}

func applyPatch(c Content, p Patch) Content {
	if p.Title != nil {
		c.Title = *p.Title
	}
	if p.Summary != nil {
		c.Summary = *p.Summary
	}
	if p.Problem != nil {
		c.Problem = *p.Problem
	}
	if p.Solution != nil {
		c.Solution = *p.Solution
	}
	if p.Rationale != nil {
		c.Rationale = *p.Rationale
	}
	if p.Caveats != nil {
		c.Caveats = *p.Caveats
	}
	if p.Code != nil {
		c.Code = *p.Code
	}
	if p.Tags != nil {
		c.Tags = *p.Tags
	}
	if p.Triggers != nil {
		c.Triggers = *p.Triggers
	}
	if p.AppliesTo != nil {
		c.AppliesTo = *p.AppliesTo
	}
	if p.VerifiedAgainst != nil {
		c.VerifiedAgainst = *p.VerifiedAgainst
	}
	return c
}

func freeSlug(ctx context.Context, tx *sql.Tx, base string) (string, error) {
	for i := 2; i < 1000; i++ {
		cand := fmt.Sprintf("%s-%d", base, i)
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM entry WHERE slug=?`, cand).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return cand, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique slug")
}

// Content-size limits, so an agent can't store unbounded blobs.
const (
	maxTitleLen    = 300
	maxSummaryLen  = 256
	maxProseLen    = 20000
	maxSnippetLen  = 20000
	maxSnippets    = 20
	maxTagsLen     = 32
	maxTriggersLen = 32
)

// Controlled-vocabulary enums (also enforced by DB CHECK constraints). Validated
// in the store so a bad value returns an actionable ErrInvalid message to the AI
// client rather than an opaque "internal error" (a raw CHECK failure) or a
// silently-dropped row.
var (
	validKinds     = map[string]bool{"user": true, "feedback": true, "project": true, "reference": true}
	validLinkTypes = map[string]bool{"relates": true, "supersedes": true, "refutes": true, "depends_on": true}
)

// validateContent caps field lengths and collection sizes; returns an
// ErrInvalid-wrapped (client-safe) error on violation.
func validateContent(c Content) error {
	switch {
	case len(c.Title) > maxTitleLen:
		return fmt.Errorf("%w: title exceeds %d bytes", ErrInvalid, maxTitleLen)
	case len(c.Summary) > maxSummaryLen:
		return fmt.Errorf("%w: summary exceeds %d bytes", ErrInvalid, maxSummaryLen)
	case len(c.Problem) > maxProseLen || len(c.Solution) > maxProseLen ||
		len(c.Rationale) > maxProseLen || len(c.Caveats) > maxProseLen:
		return fmt.Errorf("%w: a prose field exceeds %d bytes", ErrInvalid, maxProseLen)
	case len(c.Code) > maxSnippets:
		return fmt.Errorf("%w: too many code snippets (max %d)", ErrInvalid, maxSnippets)
	case len(c.Tags) > maxTagsLen:
		return fmt.Errorf("%w: too many tags (max %d)", ErrInvalid, maxTagsLen)
	case len(c.Triggers) > maxTriggersLen:
		return fmt.Errorf("%w: too many triggers (max %d)", ErrInvalid, maxTriggersLen)
	}
	for _, cs := range c.Code {
		if len(cs.Snippet) > maxSnippetLen {
			return fmt.Errorf("%w: a code snippet exceeds %d bytes", ErrInvalid, maxSnippetLen)
		}
	}
	return nil
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if r := []rune(out); len(r) > 80 {
		out = strings.Trim(string(r[:80]), "-") // rune-safe: never cut a multibyte letter mid-way
	}
	if out == "" {
		out = "entry"
	}
	return out
}

func jsonArr(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonCode(v []model.CodeSnippet) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonVerified(v []model.VerifiedRef) string {
	if len(v) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullActor(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullVID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func nullSQLInt(n sql.NullInt64) any {
	if n.Valid {
		return n.Int64
	}
	return nil
}

func authorLabel(kind string) string {
	if kind == "" {
		return "system"
	}
	return kind
}
