// Package mcpserver builds Ken's Model Context Protocol server: the AI-facing
// interface (kb_search, kb_get, kb_propose_enhancement, kb_save, kb_flag_stale)
// over streamable HTTP, behind scoped bearer-token auth.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Quest-ICT/ken/internal/embed"
	"github.com/Quest-ICT/ken/internal/metrics"
	"github.com/Quest-ICT/ken/internal/model"
	"github.com/Quest-ICT/ken/internal/ratelimit"
	"github.com/Quest-ICT/ken/internal/store"
	"github.com/Quest-ICT/ken/internal/version"
)

// Deps are the collaborators an MCP server needs.
type Deps struct {
	Store        *store.Store
	DedupSecret  []byte            // HMAC key for the search->save dedup token
	Embedder     embed.Embedder    // optional; when set, kb_search adds a semantic arm
	TokenLimiter ratelimit.Limiter // optional; per-token rate limit (nil = unlimited)
	Metrics      *metrics.Registry // optional; records per-tool call counts + auth failures

	// CurationLangs, when non-empty, are the normalized language codes the human
	// curator reads (settings.curation_langs). They are interpolated into the AI
	// instructions so agents author in a language the curator can promote. Refresh
	// live via (*Handler).SetCurationLangs.
	CurationLangs []string
	// CommProvenance, when non-nil, reports whether the calling ACTOR has recently
	// RECEIVED an inter-session message. Versions authored by such a token are
	// marked as possible hearsay so the curator can ask for a first-hand citation
	// before promoting (docs/COMM.md §7).
	//
	// A FUNCTION rather than a *comm.Store on purpose: this package must not import
	// the optional subsystem — the knowledge base's hot path stays free of it, and
	// the two databases stay decoupled behind a boolean. Nil (COMM off) means no
	// marking, which reads as "no signal", never "known first-hand".
	// Keyed on the actor rather than the token because a COMM token must be
	// dedicated — the token that receives messages is never the one that authors an
	// entry, so a token-keyed check could never fire.
	// Returns whether to mark, and WHICH KIND of traffic was seen: "directed" when
	// somebody addressed this actor specifically, "broadcast" when it was one of
	// several recipients. The kind is what keeps the marker informative now that one
	// send can reach nine stations — a badge that is almost always on carries less
	// information than one that is sometimes absent.
	CommProvenance func(ctx context.Context, actorID int64) (bool, string)

	// ResourceMetadataURL, when set (OAuth enabled), builds the RFC 9728
	// protected-resource-metadata URL advertised in the 401 WWW-Authenticate
	// header so an OAuth client can discover the authorization server. nil ⇒ the
	// 401 stays a plain challenge (OAuth off; static bearer tokens only).
	ResourceMetadataURL func(*http.Request) string
}

// addTool registers a tool and, when a registry is present, wraps its handler so
// each call increments the per-tool metric (tool name + success/error). Using a
// helper (rather than wrapping at each call site) keeps the registration sites
// unchanged apart from the extra argument.
// addTool registers a tool and wraps it with the two things every handler needs and
// none of them should have to remember: the caller's identity, and timing.
//
// THE IDENTITY WRAP IS NOT DECORATION — it is the only reason a handler sees who is
// actually calling it. The go-sdk binds a session to the INITIALIZE request's context
// (`server.Connect(req.Context(), …)` in mcp/streamable.go), so anything a handler
// reads from its context was fixed when the connection opened. Ken's middleware
// authenticates every HTTP request and puts a principal in that request's context —
// and the handler never sees it, because the handler runs on the connection's context.
//
// Demonstrated, not theorised: a kb_save presented with token B on a session opened by
// token A was written with A as author_actor_id. Ken records that field on every
// version and a human reads it when deciding whether to promote, so the durable record
// carried false provenance with nothing on the page to say so.
//
// The fix belongs HERE rather than in ~40 call sites: req.Extra.Header is the only
// per-call channel the SDK offers, and re-deriving the principal once per tool call
// leaves principalFrom / requireScope / requireStation working unchanged while
// meaning what they always claimed to.
func addTool[In, Out any](s *mcp.Server, d Deps, t *mcp.Tool,
	h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	name := t.Name
	reg := d.Metrics
	handler := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		ctx = withCaller(ctx, d.Store, req)
		if reg == nil {
			return h(ctx, req, in)
		}
		start := time.Now()
		res, out, err := h(ctx, req, in)
		reg.RecordMCP(name, err == nil)
		// Every kb_* tool is a bounded request/response, so the handler duration
		// is clean work-time latency — none of them block.
		reg.RecordMCPDuration(name, time.Since(start))
		return res, out, err
	}
	mcp.AddTool(s, t, handler)
}

// withCaller replaces the connection-time principal with the one presented on THIS
// call, when the transport can tell us.
//
// Falls back to the existing context principal in three cases, all of which mean "no
// per-call evidence available": a transport with no HTTP request behind it (in-process
// tests), a request carrying no bearer, and a bearer that no longer authenticates.
//
// THAT LAST FALLBACK IS DELIBERATE AND NARROW. It cannot admit anyone: the middleware
// has already rejected unauthenticated requests with 401 before the SDK sees them, so
// reaching here at all means a valid credential was presented on this request. Failing
// open to the connection principal rather than erroring keeps a transient store
// hiccup from turning into a tool failure, and the honest limit is that a credential
// revoked mid-session is still stopped by the middleware, not by this.
func withCaller(ctx context.Context, st *store.Store, req *mcp.CallToolRequest) context.Context {
	if st == nil || req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ctx
	}
	tok := bearerFromHeader(req.Extra.Header)
	if tok == "" {
		return ctx
	}
	p, err := authenticate(ctx, st, tok)
	if err != nil || p == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, p)
}

// viaComm reports whether the caller should have its authored version marked as
// possible hearsay. Errors and a disabled subsystem both yield false — the marker
// is advisory, and a failed check must never block a save.
func (d Deps) viaComm(ctx context.Context) (bool, string) {
	if d.CommProvenance == nil {
		return false, ""
	}
	id := actorID(ctx)
	if id == 0 {
		return false, ""
	}
	return d.CommProvenance(ctx, id)
}

func actorID(ctx context.Context) int64 {
	if p := principalFrom(ctx); p != nil {
		return p.ActorID
	}
	return 0
}

// --- kb_search ---

type searchIn struct {
	Query    string `json:"query" jsonschema:"natural-language query plus the concrete symptoms you are seeing"`
	Scope    string `json:"scope,omitempty" jsonschema:"curated (default) | proposals | history | all"`
	Kind     string `json:"kind,omitempty" jsonschema:"filter by kind: user | feedback | project | reference"`
	Category string `json:"category,omitempty" jsonschema:"filter by category"`
	K        int    `json:"k,omitempty" jsonschema:"max results, default 12, max 25"`
	Offset   int    `json:"offset,omitempty" jsonschema:"pagination offset"`
}

type searchOut struct {
	Results         []model.SearchResult `json:"results"`
	HasMore         bool                 `json:"has_more"`
	NextOffset      int                  `json:"next_offset,omitempty"`
	DedupCheckToken string               `json:"dedup_check_token"`
}

// --- kb_get ---

type getIn struct {
	Slugs          []string `json:"slugs" jsonschema:"entry slugs to fetch (max 10)"`
	ResponseFormat string   `json:"response_format,omitempty" jsonschema:"concise (default) | detailed"`
}

type getOut struct {
	Entries []model.Entry `json:"entries"`
	Missing []string      `json:"missing,omitempty"`
}

// --- kb_save ---

type linkIn struct {
	ToSlug   string `json:"to_slug"`
	LinkType string `json:"link_type" jsonschema:"relates | supersedes | refutes | depends_on"`
}

type saveIn struct {
	DedupCheckToken string              `json:"dedup_check_token" jsonschema:"required; the token returned by a recent kb_search"`
	Slug            string              `json:"slug,omitempty" jsonschema:"optional; derived from the title if omitted"`
	Kind            string              `json:"kind" jsonschema:"user | feedback | project | reference"`
	Title           string              `json:"title"`
	Summary         string              `json:"summary" jsonschema:"one-line ranking summary (<=160 chars)"`
	Category        string              `json:"category,omitempty"`
	Problem         string              `json:"problem,omitempty" jsonschema:"when does this apply?"`
	Solution        string              `json:"solution,omitempty"`
	Rationale       string              `json:"rationale,omitempty" jsonschema:"the WHY + trade-offs"`
	Caveats         string              `json:"caveats,omitempty"`
	Code            []model.CodeSnippet `json:"code,omitempty"`
	Tags            []string            `json:"tags,omitempty"`
	Triggers        []string            `json:"triggers,omitempty" jsonschema:"symptoms that should surface this entry"`
	AppliesTo       []string            `json:"applies_to,omitempty"`
	VerifiedAgainst []model.VerifiedRef `json:"verified_against,omitempty"`
	Confidence      float64             `json:"confidence,omitempty" jsonschema:"0..1 self-rating"`
	SessionID       string              `json:"session_id,omitempty"`
	Links           []linkIn            `json:"links,omitempty"`
}

type saveOut struct {
	Slug      string `json:"slug"`
	RevNo     int    `json:"rev_no"`
	State     string `json:"state"`
	Lifecycle string `json:"lifecycle"`
}

// --- kb_propose_enhancement ---

type patchIn struct {
	Title           *string              `json:"title,omitempty"`
	Summary         *string              `json:"summary,omitempty"`
	Problem         *string              `json:"problem,omitempty"`
	Solution        *string              `json:"solution,omitempty"`
	Rationale       *string              `json:"rationale,omitempty"`
	Caveats         *string              `json:"caveats,omitempty"`
	Code            *[]model.CodeSnippet `json:"code,omitempty"`
	Tags            *[]string            `json:"tags,omitempty"`
	Triggers        *[]string            `json:"triggers,omitempty"`
	AppliesTo       *[]string            `json:"applies_to,omitempty"`
	VerifiedAgainst *[]model.VerifiedRef `json:"verified_against,omitempty"`
}

type proposeIn struct {
	Slug       string  `json:"slug"`
	BasedOnRev int     `json:"based_on_rev" jsonschema:"the rev you are enhancing (0 = current curated head)"`
	ChangeNote string  `json:"change_note" jsonschema:"the commit message: what changed and why"`
	Confidence float64 `json:"confidence,omitempty" jsonschema:"0..1 self-rating"`
	SessionID  string  `json:"session_id,omitempty"`
	Patch      patchIn `json:"patch" jsonschema:"fields to change; omitted fields inherit from based_on_rev"`
}

type proposeOut struct {
	Slug    string `json:"slug"`
	RevNo   int    `json:"rev_no"`
	State   string `json:"state"`
	Warning string `json:"warning,omitempty"`
}

// --- kb_flag_stale ---

type flagIn struct {
	Slug               string `json:"slug"`
	Reason             string `json:"reason"`
	SuspectedAppliesTo string `json:"suspected_applies_to,omitempty"`
}

type flagOut struct {
	Slug      string `json:"slug"`
	Staleness string `json:"staleness"`
}

// --- kb_diff ---

type diffIn struct {
	Slug string `json:"slug"`
	RevA int    `json:"rev_a" jsonschema:"first revision number"`
	RevB int    `json:"rev_b" jsonschema:"second revision number"`
}

type fieldDiffOut struct {
	Field   string `json:"field"`
	Changed bool   `json:"changed"`
	A       string `json:"a"`
	B       string `json:"b"`
}

type diffOut struct {
	Slug   string         `json:"slug"`
	RevA   int            `json:"rev_a"`
	RevB   int            `json:"rev_b"`
	StateA string         `json:"state_a"`
	StateB string         `json:"state_b"`
	Fields []fieldDiffOut `json:"fields"`
}

// --- kb_record_outcome ---

type outcomeIn struct {
	Slug      string `json:"slug"`
	Outcome   string `json:"outcome" jsonschema:"helped | didnt-apply | was-wrong"`
	Note      string `json:"note,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

type outcomeOut struct {
	Slug      string `json:"slug"`
	Recorded  bool   `json:"recorded"`
	Staleness string `json:"staleness"`
}

// --- kb_recent_context ---

type recentIn struct {
	SinceDays int    `json:"since_days,omitempty" jsonschema:"lookback window in days (default 14)"`
	Kind      string `json:"kind,omitempty"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max entries (default 20, max 50)"`
}

type recentEntryOut struct {
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	Kind      string `json:"kind"`
	LastEvent string `json:"last_event"`
	LastAt    string `json:"last_at"`
}

type recentOut struct {
	Entries []recentEntryOut `json:"entries"`
}

// baseInstructions is delivered to a connecting MCP client (via the initialize
// response) so an AI agent learns HOW to use Ken without the human pasting a
// prompt: the curated-knowledge model + the search-first / record-outcome loop.
// buildInstructions appends a curation-language paragraph when the operator has
// declared one. Distilled from docs/AI-INTEGRATION.md — keep the two in sync.
const baseInstructions = `Ken is your durable, curated knowledge base, reached over these kb_* tools. You (the AI) are the sole author; a human curates. Everything you write lands as a PROPOSED revision — usable this session, but the curated head advances ONLY when the human promotes it. You capture, enhance, flag stale, and record outcomes; you NEVER curate or assert freshness.

The loop:
- Warm up (fresh session, no specific problem yet): kb_recent_context for a briefing of recently-curated entries.
- Search FIRST — your default first move before debugging an error or solving anything non-trivial: kb_search with natural language PLUS the exact symptoms/error text you see. It returns token-light summaries and a dedup_check_token — KEEP that token (kb_save requires it). Prefer 'mature' entries; distrust a 'staleness' badge; has_provisional means a proposal is already pending.
- Fetch the few that matter: kb_get by slug (concise by default; 'detailed' adds provenance).
- Act, then close the loop EVERY time: kb_record_outcome (helped | didnt-apply | was-wrong). 'was-wrong' also flags the entry stale. This is how Ken self-curates — do not skip it.
- Save vs enhance what you learned: same problem with a better answer → kb_propose_enhancement (a new rev on the same slug). A different problem that merely shares vocabulary → kb_save (needs a fresh dedup_check_token) plus a 'relates' link. Write triggers (symptoms a future agent would type) and applies_to well; give an honest confidence.
- Flag stale (kb_flag_stale) when a dependency moved or a fact changed. You can flag; you can never assert freshness.

You have no clock. STOP before any claim about time — how long it took, how old it is, how far back it goes, whether it is still current — and read one: 'date -u', or the timestamp already in front of you (created_at, an mtime, the value your own query keyed on). Wall time between tool calls registers as nothing, so an unread duration was not estimated, it was generated, drifting toward whatever the sentence wanted; the errors run UPWARD, so calibration cannot fix this — only reading. 'Recently' and 'long-standing' are the same claim with the number hidden. A measured endpoint does not license a claim about the span. Write absolute times: Ken cannot tell a measured figure from a generated one, and neither can your curator.

Belongs in Ken: durable, reusable knowledge — solved problems, pitfalls/gotchas, caveats, design decisions with rationale and trade-offs, verified facts. NOT transient session state, secrets, or chatter.`

// buildInstructions returns the AI-facing instructions, appending a curation-
// language paragraph when the operator has declared the language(s) they curate in
// (settings.curation_langs). With none declared the base guide is returned
// unchanged, so a single-language KB sees no difference.
func buildInstructions(curationLangs []string) string {
	if len(curationLangs) == 0 {
		return baseInstructions
	}
	names := make([]string, len(curationLangs))
	for i, l := range curationLangs {
		names[i] = langLabel(l)
	}
	return baseInstructions + "\n\nAuthor in the curation language(s): this KB is curated in " + strings.Join(names, ", ") +
		". Write every human-readable field — title, summary, problem, solution, rationale, caveats — in one of those, so the human curator can read and PROMOTE it; a proposal the curator cannot read is stranded and can never be promoted. Keep triggers, code, identifiers and verbatim error text in their original form — they are language-neutral retrieval keys; never translate them. If kb_search surfaces an entry whose pending proposal is in a language you were not asked to use, propose a re-authored revision in a curation language."
}

// langLabel renders a BCP-47 primary subtag as "Name (code)" for the common
// languages, falling back to the bare code — so the instruction reads naturally
// ("curated in French (fr)") without pulling in a full CLDR table.
func langLabel(code string) string {
	if n, ok := langNames[code]; ok {
		return n + " (" + code + ")"
	}
	return code
}

var langNames = map[string]string{
	"en": "English", "es": "Spanish", "fr": "French", "de": "German",
	"pt": "Portuguese", "it": "Italian", "nl": "Dutch", "zh": "Chinese",
	"ja": "Japanese", "ko": "Korean", "ru": "Russian", "ar": "Arabic",
	"hi": "Hindi", "tr": "Turkish", "pl": "Polish", "uk": "Ukrainian",
	"vi": "Vietnamese", "id": "Indonesian", "th": "Thai", "sv": "Swedish",
	"cs": "Czech", "el": "Greek", "he": "Hebrew", "ro": "Romanian",
	"hu": "Hungarian", "fi": "Finnish", "da": "Danish", "no": "Norwegian",
	"ca": "Catalan",
}

// NewServer builds the MCP server with all tools registered.
func NewServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "ken",
		Title:   "Ken knowledge base",
		Version: version.Version,
	}, &mcp.ServerOptions{Instructions: buildInstructions(d.CurationLangs)})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_search",
		Description: "Search the knowledge base. Returns ranked, token-light summaries (no bodies) — your default first move; follow up with kb_get. Also returns a dedup_check_token required by kb_save.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		if err := requireScope(ctx, scopeRead); err != nil {
			return nil, searchOut{}, err
		}
		opt := store.SearchOpts{Kind: in.Kind, Category: in.Category, Scope: in.Scope, K: in.K, Offset: in.Offset}
		if d.Embedder != nil {
			// Embed the query for the semantic arm; degrade to keyword-only on error.
			if vecs, verr := d.Embedder.Embed(ctx, []string{in.Query}); verr == nil && len(vecs) == 1 {
				opt.QueryVec = vecs[0]
				opt.EmbedModel = d.Embedder.ID()
			}
		}
		res, hasMore, err := d.Store.SearchPage(ctx, in.Query, opt)
		if err != nil {
			return nil, searchOut{}, mcpError(err)
		}
		out := searchOut{Results: res, HasMore: hasMore, DedupCheckToken: issueDedupToken(d.DedupSecret, dedupSubject(ctx))}
		if hasMore {
			out.NextOffset = in.Offset + len(res)
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_get",
		Description: "Fetch full entries by slug (max 10). response_format 'concise' (default) returns the curated head body; 'detailed' adds provenance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, getOut, error) {
		if err := requireScope(ctx, scopeRead); err != nil {
			return nil, getOut{}, err
		}
		slugs := in.Slugs
		if len(slugs) > 10 {
			slugs = slugs[:10]
		}
		entries, missing, err := d.Store.Get(ctx, slugs, in.ResponseFormat == "detailed")
		if err != nil {
			return nil, getOut{}, mcpError(err)
		}
		return nil, getOut{Entries: entries, Missing: missing}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_save",
		Description: "Create a NEW draft entry. Requires a dedup_check_token from a recent kb_search (enforces search-before-save). If a close match already exists, prefer kb_propose_enhancement instead.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, saveOut, error) {
		if err := requireScope(ctx, scopeWriteDraft); err != nil {
			return nil, saveOut{}, err
		}
		if err := verifyDedupToken(d.DedupSecret, in.DedupCheckToken, dedupSubject(ctx)); err != nil {
			return nil, saveOut{}, err
		}
		viaComm, viaKind := d.viaComm(ctx)
		r, err := d.Store.Save(ctx, store.SaveInput{
			Slug:     in.Slug,
			Kind:     in.Kind,
			Category: in.Category,
			Content: store.Content{
				Title: in.Title, Summary: in.Summary, Problem: in.Problem, Solution: in.Solution,
				Rationale: in.Rationale, Caveats: in.Caveats, Code: in.Code,
				Tags: in.Tags, Triggers: in.Triggers, AppliesTo: in.AppliesTo, VerifiedAgainst: in.VerifiedAgainst,
			},
			Confidence: in.Confidence, AuthorActorID: actorID(ctx), AuthorKind: "ai", SessionID: in.SessionID,
			ViaComm: viaComm, ViaCommKind: viaKind,
			Links: toLinkInputs(in.Links),
		})
		if err != nil {
			return nil, saveOut{}, mcpError(err)
		}
		return nil, saveOut{Slug: r.Slug, RevNo: r.RevNo, State: r.State, Lifecycle: r.Lifecycle}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_propose_enhancement",
		Description: "Append an enhancement (a new immutable version) to an existing entry. Never overwrites the curated head; a human promotes it later.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in proposeIn) (*mcp.CallToolResult, proposeOut, error) {
		if err := requireScope(ctx, scopePropose); err != nil {
			return nil, proposeOut{}, err
		}
		if in.Slug == "" || in.ChangeNote == "" {
			return nil, proposeOut{}, errors.New("slug and change_note are required")
		}
		viaComm, viaKind := d.viaComm(ctx)
		r, err := d.Store.ProposeEnhancement(ctx, store.ProposeInput{
			Slug: in.Slug, BasedOnRev: in.BasedOnRev, ChangeNote: in.ChangeNote,
			Confidence: in.Confidence, AuthorActorID: actorID(ctx), AuthorKind: "ai", SessionID: in.SessionID,
			ViaComm: viaComm, ViaCommKind: viaKind,
			Patch: store.Patch{
				Title: in.Patch.Title, Summary: in.Patch.Summary, Problem: in.Patch.Problem,
				Solution: in.Patch.Solution, Rationale: in.Patch.Rationale, Caveats: in.Patch.Caveats,
				Code: in.Patch.Code, Tags: in.Patch.Tags, Triggers: in.Patch.Triggers,
				AppliesTo: in.Patch.AppliesTo, VerifiedAgainst: in.Patch.VerifiedAgainst,
			},
		})
		if err != nil {
			return nil, proposeOut{}, mcpError(err)
		}
		return nil, proposeOut{Slug: r.Slug, RevNo: r.RevNo, State: r.State, Warning: r.Warning}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_flag_stale",
		Description: "Flag an entry as possibly stale (a dependency moved, a fact changed). Raises a concern; it does not assert freshness (that is a curation act).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in flagIn) (*mcp.CallToolResult, flagOut, error) {
		if err := requireScope(ctx, scopePropose); err != nil {
			return nil, flagOut{}, err
		}
		if in.Slug == "" || in.Reason == "" {
			return nil, flagOut{}, errors.New("slug and reason are required")
		}
		note := in.Reason
		if in.SuspectedAppliesTo != "" {
			note += " (suspected: " + in.SuspectedAppliesTo + ")"
		}
		st, err := d.Store.FlagStale(ctx, in.Slug, note, actorID(ctx), "ai")
		if err != nil {
			return nil, flagOut{}, mcpError(err)
		}
		return nil, flagOut{Slug: in.Slug, Staleness: st}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_diff",
		Description: "Field-by-field diff of two revisions of an entry (rev_a vs rev_b) — e.g. compare a superseded version with the curated head.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, diffOut, error) {
		if err := requireScope(ctx, scopeRead); err != nil {
			return nil, diffOut{}, err
		}
		res, err := d.Store.VersionDiff(ctx, in.Slug, in.RevA, in.RevB)
		if err != nil {
			return nil, diffOut{}, mcpError(err)
		}
		out := diffOut{Slug: res.Slug, RevA: res.RevA, RevB: res.RevB, StateA: res.StateA, StateB: res.StateB}
		for _, f := range res.Fields {
			out.Fields = append(out.Fields, fieldDiffOut{Field: f.Field, Changed: f.Changed, A: f.A, B: f.B})
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_record_outcome",
		Description: "Report whether a fetched entry actually resolved your problem: helped | didnt-apply | was-wrong. 'was-wrong' flags the entry stale for human review. This feeds the self-curating signal — use it after acting on an entry.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in outcomeIn) (*mcp.CallToolResult, outcomeOut, error) {
		if err := requireScope(ctx, scopePropose); err != nil {
			return nil, outcomeOut{}, err
		}
		switch in.Outcome {
		case "helped", "didnt-apply", "was-wrong":
		default:
			return nil, outcomeOut{}, errors.New("outcome must be one of: helped, didnt-apply, was-wrong")
		}
		st, err := d.Store.RecordOutcome(ctx, in.Slug, in.Outcome, actorID(ctx), "ai", in.SessionID, in.Note)
		if err != nil {
			return nil, outcomeOut{}, mcpError(err)
		}
		return nil, outcomeOut{Slug: in.Slug, Recorded: true, Staleness: st}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "kb_recent_context",
		Description: "A compact briefing of entries recently added or curated (default last 14 days) — call it once to warm up a fresh session without a specific query.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		if err := requireScope(ctx, scopeRead); err != nil {
			return nil, recentOut{}, err
		}
		rows, err := d.Store.RecentContext(ctx, in.SinceDays, in.Limit, in.Kind)
		if err != nil {
			return nil, recentOut{}, mcpError(err)
		}
		var out recentOut
		for _, r := range rows {
			out.Entries = append(out.Entries, recentEntryOut{
				Slug: r.Slug, Title: r.Title, Summary: r.Summary, Kind: r.Kind, LastEvent: r.LastEvent, LastAt: r.LastAt,
			})
		}
		return nil, out, nil
	})

	return s
}

func toLinkInputs(ls []linkIn) []store.LinkInput {
	out := make([]store.LinkInput, 0, len(ls))
	for _, l := range ls {
		out = append(out, store.LinkInput{ToSlug: l.ToSlug, LinkType: l.LinkType})
	}
	return out
}

// Handler is the /mcp endpoint. It wraps a streamable-HTTP MCP server whose
// AI-facing instructions can be swapped live (SetCurationLangs) without dropping
// the endpoint: the server is held in an atomic pointer and read per request, so
// an in-flight request never observes a half-built server. It implements
// http.Handler (the auth-wrapped handler is embedded).
type Handler struct {
	http.Handler
	d   Deps
	ptr atomic.Pointer[mcp.Server]
}

// NewHTTPHandler builds the /mcp Handler: the streamable-HTTP MCP server wrapped
// in bearer auth (which also does CORS + the OAuth 401 challenge).
// sessionTimeout closes an MCP session that has gone quiet.
//
// The SDK's zero value means "never close", which is what every Ken handler asked
// for by passing nil options: a session opened once stayed open, and stayed
// authorized from the handler's point of view, for as long as the client kept the
// connection — whatever happened to the credential that opened it.
//
// 30 minutes is chosen against how these sessions are actually used. A working
// conversation makes a call every few minutes at worst, so it never trips; a session
// left open by a closed laptop or a crashed client is gone within the half hour
// rather than never. It is a backstop, not an authorization control — the middleware
// re-authenticates every HTTP request — so it can afford to be generous.
const sessionTimeout = 30 * time.Minute

// maxReasonableSessionTimeout exists only so the test asserting sessionTimeout is set
// cannot be satisfied by a value so large it means "never" in a longer costume.
const maxReasonableSessionTimeout = 24 * time.Hour

func NewHTTPHandler(d Deps) *Handler {
	h := &Handler{d: d}
	h.ptr.Store(NewServer(d))
	inner := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return h.ptr.Load() },
		&mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout})
	h.Handler = authMiddleware(d.Store, newTouchThrottle(), d.TokenLimiter, d.Metrics, d.ResourceMetadataURL, inner)
	return h
}

// SetCurationLangs rebuilds the MCP server so connecting agents receive
// instructions naming the current curation language(s). Cheap and rare (fires only
// on a settings edit); existing connections keep working and pick up the new
// instructions on their next initialize.
func (h *Handler) SetCurationLangs(langs []string) {
	d := h.d
	d.CurationLangs = langs
	h.ptr.Store(NewServer(d))
}
