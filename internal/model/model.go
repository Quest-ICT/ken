// Package model holds Ken's domain types, shared across the store, MCP, and web layers.
package model

// SearchResult is one token-light row returned by kb_search (no bodies).
type SearchResult struct {
	Slug           string  `json:"slug"`
	Title          string  `json:"title"`
	Summary        string  `json:"summary"`
	Kind           string  `json:"kind"`
	Category       string  `json:"category,omitempty"`
	Staleness      string  `json:"staleness"`
	Maturity       string  `json:"maturity"`
	Score          float64 `json:"score"`
	HasProvisional bool    `json:"has_provisional"`
	// Language is the detected content language (BCP-47 primary subtag) of the
	// matched version, or "" when undetected. Lets a polyglot agent spot an entry
	// stranded in a language the curator can't read and offer a re-authored revision.
	Language string `json:"language,omitempty"`
}

// CodeSnippet is a language-tagged example inside an entry version.
type CodeSnippet struct {
	Lang    string `json:"lang"`
	Caption string `json:"caption,omitempty"`
	Snippet string `json:"snippet"`
}

// VerifiedRef records a tool/version an entry was verified against (staleness).
type VerifiedRef struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Date    string `json:"date,omitempty"`
}

// VersionBody is the content of one entry version (e.g. the curated head).
type VersionBody struct {
	RevNo           int           `json:"rev_no"`
	Problem         string        `json:"problem,omitempty"`
	Solution        string        `json:"solution,omitempty"`
	Rationale       string        `json:"rationale,omitempty"`
	Caveats         string        `json:"caveats,omitempty"`
	Code            []CodeSnippet `json:"code,omitempty"`
	VerifiedAgainst []VerifiedRef `json:"verified_against,omitempty"`
}

// EntryProvenance is attached to kb_get responses in 'detailed' mode.
type EntryProvenance struct {
	State      string  `json:"state"`
	AuthorKind string  `json:"author_kind,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	ChangeNote string  `json:"change_note,omitempty"`
}

// Entry is a full knowledge entry returned by kb_get.
type Entry struct {
	Slug           string           `json:"slug"`
	Kind           string           `json:"kind"`
	Title          string           `json:"title"`
	Summary        string           `json:"summary"`
	Category       string           `json:"category,omitempty"`
	Tags           []string         `json:"tags"`
	Triggers       []string         `json:"triggers"`
	Lifecycle      string           `json:"lifecycle"`
	Staleness      string           `json:"staleness"`
	CuratedRev     int              `json:"curated_rev"`
	UseCount       int              `json:"use_count"`
	HasProvisional bool             `json:"has_provisional"`
	Head           *VersionBody     `json:"curated_head,omitempty"`
	Provenance     *EntryProvenance `json:"provenance,omitempty"` // only in 'detailed' mode
}
