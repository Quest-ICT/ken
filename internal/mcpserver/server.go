// Package mcpserver builds Ken's Model Context Protocol server: the AI-facing
// interface (kb_search, kb_get, kb_propose_enhancement, kb_save, kb_flag_stale)
// over streamable HTTP, behind scoped bearer-token auth.
package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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
	// the two databases stay decoupled behind a boolean. Nil (COMM unavailable — a fault, not a setting) means no
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

// sessionIdentity is WHO IS CALLING, derived by the server and never taken from the caller.
//
// It used to be a caller-supplied `session_id` field — optional, with no jsonschema
// description, unmentioned in the tool's own Description and unmentioned in the
// server-delivered instructions. ken-prod-ops measured the result on a live deployment:
// **37 of 37 entry_outcome rows had it NULL, and 282 of 282 curation_event rows.** Two
// independent AI actors, three weeks, not one identity recorded. That is not careless
// callers; it is exactly what an undescribed optional field predicts.
//
// The consequence was severe and self-inflicted: the maturity badge counts DISTINCT
// sessions reporting `helped`, so with every id NULL the top tier was unreachable — not
// merely empty today, but unreachable by accumulating more evidence, because the evidence
// being accumulated carried no identity. A worse failure than the inverted counter it
// replaced.
//
// DESCRIBING THE FIELD BETTER WOULD NOT HAVE FIXED IT, and this is prod's argument rather
// than mine: a caller-supplied identity is unreliable AND unfalsifiable. A session that
// wants a badge sends three different strings. Ken already knows who is calling, so it
// should look rather than ask.
//
// The MCP transport's own session id is preferred; a token falls back when the transport
// has none (stdio, or a client that does not negotiate one). Both are server-side facts.
// Empty only when neither exists, which is a dev-token bypass.
func sessionIdentity(ctx context.Context, req *mcp.CallToolRequest) string {
	if req != nil && req.Session != nil {
		if id := req.Session.ID(); id != "" {
			return "mcp:" + id
		}
	}
	if p := principalFrom(ctx); p != nil && p.TokenID != "" {
		return "tok:" + p.TokenID
	}
	return ""
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
	Results []model.SearchResult `json:"results"`
	// Matched is how many DISTINCT entries matched your words, before ranking and
	// before this page was cut. NOT omitempty: zero is the most important value it can
	// take, and omitting it would restore the exact ambiguity this field exists to
	// remove.
	//
	// matched=0            your words are absent from the knowledge base
	// matched>len(results) something matched and RANKING chose these; ask differently
	//                      or page rather than concluding the rest does not exist
	Matched int `json:"matched"`
	// DeadTerms are the words that matched NOTHING anywhere. The actionable half: it
	// turns "no results" into "the word `generated` is not in this corpus", which is a
	// next query rather than a conclusion.
	DeadTerms  []string `json:"terms_that_matched_nothing,omitempty"`
	HasMore    bool     `json:"has_more"`
	NextOffset int      `json:"next_offset,omitempty"`
	// KenVersion is what is running now. A knowledge-base-only session calls neither
	// station_me nor comm_poll, and cannot call the ken_version TOOL either when this
	// conversation predates it — whole tools do not cross the freeze, only parameters
	// do. A result is the one channel that always arrives.
	KenVersion      string `json:"ken_version"`
	DedupCheckToken string `json:"dedup_check_token"`
}

// --- kb_get ---

type getIn struct {
	Slugs          []string `json:"slugs" jsonschema:"entry slugs to fetch (max 10)"`
	ResponseFormat string   `json:"response_format,omitempty" jsonschema:"concise (default) | detailed"`
}

type getOut struct {
	Entries []model.Entry `json:"entries"`
	Missing []string      `json:"missing,omitempty"`
	// OutcomeOwed names the slugs just handed over, and OutcomeNote says what to do with them.
	//
	// *** THE INSTRUCTION EXISTS AND IS IGNORED 85% OF THE TIME. *** Measured on the live
	// deployment: 250 recorded uses against 37 outcomes — 14.8% — and only 22 of 108 entries
	// carry any outcome at all. The connect-time text says "close the loop EVERY time … do not
	// skip it", and every session skips it, including the ones that wrote that sentence.
	//
	// The diagnosis in FINISHING.md is what this field acts on: it is "something the
	// instructions request that nothing prompts for AT THE MOMENT IT MATTERS." A rule delivered
	// once at connect, hundreds of tool calls before the occasion, competes with everything
	// else in the conversation. A rule delivered IN THE RESULT arrives at the occasion itself.
	//
	// kb_get is exactly that occasion, and the denominator says so: `use_count` is bumped ONLY
	// by Store.Get, whose sole caller is this tool — the console uses GetEntry, which
	// deliberately does not bump, and kb_search does not bump either. So a "use" is precisely
	// "an agent fetched the full entry to apply it", which is the moment an outcome is owed.
	//
	// Same shape as every other fix this week: the connect-time channel is pinned, truncated
	// and early; the result channel is current, whole, and arrives when it is needed.
	OutcomeOwed []string `json:"outcome_owed,omitempty"`
	OutcomeNote string   `json:"outcome_note,omitempty"`
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
	Triggers        []string            `json:"triggers,omitempty" jsonschema:"ARRAY of short symptom strings, one per entry — e.g. [\"connection refused\", \"502 after deploy\"]. Not one delimited string"`
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
	SessionID string `json:"session_id,omitempty" jsonschema:"IGNORED. Kept only so a caller that still sends it is not rejected. Ken derives the recording session from the connection itself — a caller-supplied identity is both unreliable and unfalsifiable"`
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
const baseInstructions = `Ken is your durable, curated knowledge base (kb_* tools). KEN SERVES THREE SURFACES, each a SEPARATE MCP entry your human configures: /mcp (kb_*, this one), /comm/mcp (comm_*, messaging with other AI sessions), /station/mcp (station_*, a durable identity outliving this session). One tells you nothing about the others. If you lack one you need, ask your human for it BY NAME.

Belongs in Ken: durable, reusable knowledge — solved problems, pitfalls, caveats, decisions and their rationale. NOT session state, secrets, or chatter.

You are the sole author; a human curates. Your writes land as PROPOSED revisions, usable now and promoted only by the human. You never curate, never assert freshness.

THE LOOP; each tool's description carries its own rules.
- SEARCH FIRST, before debugging an error or solving anything non-trivial: kb_search, then kb_get the few that matter.
- Act, then close the loop EVERY time: kb_record_outcome. The only evidence Ken collects, skipped six times in seven. IF IT IS NOT IN YOUR TOOL LIST — some clients hide tools — say so to your human in words, naming the entry and what happened.
- Record what you learned: kb_propose_enhancement (same problem, better answer), kb_save (a different one), kb_flag_stale (a dependency moved).

You have no clock. Before any claim about time — how long, how old, still current — read one: date -u, or a timestamp in view. An unread duration was not estimated, it was generated, and the errors run upward. 'Recently' and 'long-standing' are the same claim with the number hidden. A measured endpoint does not license a claim about the span. Write absolute times: Ken cannot tell a measured figure from a generated one, and neither can your curator.`

// buildInstructions returns the AI-facing instructions, appending a curation-
// language paragraph when the operator has declared the language(s) they curate in
// (settings.curation_langs). With none declared the base guide is returned
// unchanged, so a single-language KB sees no difference.
func buildInstructions(curationLangs []string) string { return baseInstructions }

// curationSentence is the curation-language rule, addressed to the tools that WRITE.
//
// IT USED TO BE APPENDED TO THE INSTRUCTIONS, which put it past the client's 2048-character cut
// on any deployment that declared a curation language — so the operator who configured the
// feature was the only one whose sessions were guaranteed never to be told about it. Delivered
// now on kb_save and kb_propose_enhancement, which are the only two calls it can change.
//
// Returns "" when no language is declared, so a single-language KB sees no text at all.
func curationSentence(curationLangs []string) string {
	if len(curationLangs) == 0 {
		return ""
	}
	names := make([]string, len(curationLangs))
	for i, l := range curationLangs {
		names[i] = langLabel(l)
	}
	return " CURATION LANGUAGE: this KB is curated in " + strings.Join(names, ", ") +
		". Write every human-readable field — title, summary, problem, solution, rationale, caveats — in one of those; " +
		"a proposal the curator cannot read is stranded and can never be promoted. Keep triggers, code, identifiers and " +
		"verbatim error text in their original form: they are language-neutral retrieval keys, so never translate them."
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
// mcpKeepAlive is how often the server sends a JSON-RPC ping on an idle MCP stream.
//
// KEN USED TO SEND NOTHING AT ALL, from 1.x through 3.9.0. The SDK ships a server-side
// keepalive and every ServerOptions literal set only Instructions, so the ping loop never
// started — and a stream carrying no bytes is indistinguishable, from the client's side, from
// a stream whose server has gone away. Clients gave up and reconnected.
//
// ken-prod-ops measured it across three surfaces over 17 days: 804 teardowns, clustering at
// ~299, ~599 and ~900 seconds. Those are the first, second and third idle windows of a ~300s
// CLIENT read timeout that occasional real traffic resets. A fixed server-side deadline would
// produce ONE mode; harmonics at integer multiples of one interval are what a client timer
// being reset by intermittent bytes looks like. Nothing on this side was closing them.
//
// 30s is chosen against that ~300s window with an order of magnitude to spare, and sits inside
// Ken's own 120s IdleTimeout so an active stream never looks idle to our own server.
//
// Server.ReadTimeout (60s) does NOT interact with this, and that was checked rather than
// assumed: Go's HTTP/2 server does arm a per-stream deadline from it, but onReadTimeout closes
// the REQUEST body (st.body.CloseWithError) and never touches the response stream — which is
// also why it was never the cause of the teardowns.
const mcpKeepAlive = 30 * time.Second

// NewServer builds the knowledge-base surface as its own MCP server.
//
// Split from RegisterTools in 3.36.0 so the SAME tools can also be registered onto the unified
// endpoint, which serves all three surfaces from one connector. Two servers, one set of tool
// definitions — the alternative was a second copy that drifts.
func NewServer(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "ken",
		Title:   "Ken knowledge base",
		Version: version.Version,
	}, &mcp.ServerOptions{Instructions: version.InstructionStamp() + buildInstructions(d.CurationLangs), KeepAlive: mcpKeepAlive})
	RegisterTools(s, d)
	return s
}

func RegisterTools(s *mcp.Server, d Deps) {

	// THE CURATION SENTENCE RIDES ON THE TWO TOOLS THAT WRITE, not on the instructions.
	// Built here rather than at the const, because the language list is per deployment and
	// SetCurationLangs rebuilds this server when the operator changes it.
	curation := curationSentence(d.CurationLangs)

	addTool(s, d, &mcp.Tool{
		Name: "kb_search",
		Description: "Search the knowledge base. Returns ranked, token-light summaries (no bodies) — your default first move; follow up with kb_get. Also returns a dedup_check_token required by kb_save. " +
			"READ `matched` BEFORE YOU CONCLUDE ANYTHING FROM AN EMPTY OR THIN RESULT. It is how many entries matched your words at all, " +
			"before ranking cut the page: matched=0 means the words are genuinely absent, while matched above the number of results means " +
			"something IS there and ranking chose these — ask differently rather than deciding the knowledge does not exist. " +
			"`terms_that_matched_nothing` names the individual words that found nothing, which is usually the fastest way to a better query. " +
			"Long, specific queries are NOT reliably better: ranking penalises long documents, so an entry can be missed by a query built from " +
			"its own title while a single distinctive word finds it. If a search comes back thin, try FEWER and RARER words.",
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
		out := searchOut{Results: res, HasMore: hasMore, KenVersion: version.Version,
			DedupCheckToken: issueDedupToken(d.DedupSecret, dedupSubject(ctx))}
		// What the search MATCHED, independently of what it returned. A failure here is
		// swallowed: the diagnostic exists to stop a thin result being misread, and
		// failing the whole search to protect a hint would be a worse trade than the one
		// it fixes.
		if diag, derr := d.Store.Diagnose(ctx, in.Query, opt, len(res)); derr == nil {
			out.Matched, out.DeadTerms = diag.Matched, diag.DeadTerms
		}
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
		owed := make([]string, 0, len(entries))
		for _, e := range entries {
			owed = append(owed, e.Slug)
		}
		return nil, getOut{Entries: entries, Missing: missing, OutcomeOwed: owed, OutcomeNote: outcomeNote(len(owed))}, nil
	})

	addTool(s, d, &mcp.Tool{
		Name: "kb_save",
		Description: "Create a NEW draft entry. Requires a dedup_check_token from a recent kb_search (enforces search-before-save). " +
			"If a close match already exists, prefer kb_propose_enhancement instead. Use kb_save for a genuinely different problem that " +
			"merely shares vocabulary, and add a `relates` link to the entry it resembles. Write `triggers` as the symptoms a future agent " +
			"would actually type, fill `applies_to`, and give an HONEST confidence — an inflated one costs the next session more than a low " +
			"one costs you. NOT FOR: transient session state, secrets or credentials of any kind, or chatter." + curation,
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
		Name: "kb_propose_enhancement",
		Description: "Append an enhancement (a new immutable version) to an existing entry. Never overwrites the curated head; a human promotes it later. " +
			"Use it when the problem is the SAME and your answer is better. If a search surfaced an entry whose pending proposal is written in a " +
			"language you were not asked to use, propose a re-authored revision rather than adding a second one alongside it." + curation,
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
	}, func(ctx context.Context, req *mcp.CallToolRequest, in outcomeIn) (*mcp.CallToolResult, outcomeOut, error) {
		if err := requireScope(ctx, scopePropose); err != nil {
			return nil, outcomeOut{}, err
		}
		switch in.Outcome {
		case "helped", "didnt-apply", "was-wrong":
		default:
			return nil, outcomeOut{}, errors.New("outcome must be one of: helped, didnt-apply, was-wrong")
		}
		st, err := d.Store.RecordOutcome(ctx, in.Slug, in.Outcome, actorID(ctx), "ai",
			sessionIdentity(ctx, req), in.Note)
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

	addTool(s, d, &mcp.Tool{
		Name:        "ken_version",
		Description: version.ToolDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in version.InstructionsIn) (*mcp.CallToolResult, version.Info, error) {
		out := version.Current()
		// THE ARGUMENT IS THE ESCAPE HATCH FOR SESSIONS THAT CANNOT SEE ken_instructions.
		// Whole tools do not travel across the freeze; parameters do, because the server
		// validates what ARRIVES rather than the client's captured schema. So a session
		// frozen before ken_instructions existed can still ask for the current text here.
		if in.Wants() {
			i := version.InstructionsFor("/mcp", buildInstructions(d.CurationLangs))
			out.Instructions = &i
		}
		return nil, out, nil
	})

	addTool(s, d, &mcp.Tool{
		Name:        "ken_instructions",
		Description: version.InstructionsToolDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, version.InstructionsInfo, error) {
		return nil, version.InstructionsFor("/mcp", buildInstructions(d.CurationLangs)), nil
	})

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

// outcomeNote is the prompt kb_get returns with the entries it just handed over.
//
// A FUNCTION RATHER THAN AN INLINE STRING so the test reads the shipped text instead of a copy of
// it. A test that rebuilds the sentence it is checking asserts against itself, which is this
// project's own recurring defect one layer over — the same reason the instruction tests now read
// the delivered value rather than the const.
//
// Names the count because a session that fetched several must report on EACH: the tracker
// predicted the failure exactly — "one kb_get may carry several slugs and bumps each, while a
// session is likely to record at most one outcome for the batch."
func outcomeNote(n int) string {
	if n == 0 {
		return ""
	}
	suffix := "ies"
	if n == 1 {
		suffix = "y"
	}
	return "You now owe an outcome on " + strconv.Itoa(n) + " entr" + suffix +
		". After you act, call kb_record_outcome for EACH slug in outcome_owed: helped | didnt-apply | " +
		"was-wrong. This is the only evidence Ken collects, and it is currently skipped about six times in " +
		"seven — an entry nobody reports on stays unproven forever, and the next session cannot tell a good " +
		"answer from an untested one. If kb_record_outcome is not in your tool list, tell your human in words " +
		"instead, naming the slug and what happened."
}
