# Ken — MCP tool contracts

The **AI-facing interface**. Ken exposes these tools over a remote **streamable-HTTP** MCP
endpoint. This document is the contract: shapes here are stable API; change them only by
*adding* optional fields, never by removing or retyping an existing one.

- **Endpoint:** `https://<ken-host>/mcp` (TLS-only). **This document covers the knowledge-base
  surface only.** Ken has two other MCP endpoints, both **core and on by default** (each with an
  independent opt-out — `KEN_COMM_ENABLED=0`, `KEN_STATION_ENABLED=0`; turning COMM off leaves
  stations fully working), each with
  its own token family and its own contract document — a client registers Ken once per surface it
  uses, which is a security property rather than packaging taste (a knowledge-base token cannot send
  messages, and a comm token cannot write knowledge):
  | surface | endpoint | tools | credential | contract |
  |---|---|---|---|---|
  | knowledge base | `/mcp` | `kb_*` | `ken_` token, knowledge-base scopes | this document |
  | inter-session comms | `/comm/mcp` | `comm_*` | `ken_` token, `comm` scope | [COMM.md](COMM.md) |
  | stations | `/station/mcp` | `station_*` | `kens_` key, bound to a station | [STATIONS.md](STATIONS.md) |

  The comm surface takes an ordinary `ken_` token that carries the `comm` scope — the separation is
  enforced by the SCOPE, not by a distinct prefix, and a token may not hold both families. Stations
  are the exception with a prefix of their own, because a station key additionally carries a station
  binding that an ordinary token has no field for. (`kenc_` is unrelated: it prefixes OAuth *client
  ids*, not tokens.)
- **Register in Claude Code:**
  `claude mcp add --transport http ken https://<ken-host>/mcp --header "Authorization: Bearer $KEN_TOKEN" --scope user`
- **Tool prefix:** `kb_*` (chosen).
- **Self-describing:** on `initialize`, Ken returns its operating loop (warm-up → search-first →
  record-outcome → save/enhance) as MCP **server instructions**, so an agent learns the protocol
  without a human pasting a prompt (`internal/mcpserver/server.go`).
- **Design references:** entry model → [DESIGN.md](DESIGN.md) §3; schema → [../migrations/0001_init.sql](../migrations/0001_init.sql).

---

## Auth, scopes, errors

Every call carries `Authorization: Bearer ken_<tokenId>_<secret>`. The server looks the
token up by `tokenId`, constant-time-compares `SHA-256(secret)`, and resolves it to an **actor**
(who is recorded as the author of anything written) and a **scope set**.

| Scope | Grants |
|---|---|
| `read` | `kb_search`, `kb_get`, `kb_diff`, `kb_recent_context` |
| `write-draft` | `kb_save` (create a new **draft** entry) |
| `propose` | `kb_propose_enhancement`, `kb_flag_stale`, `kb_record_outcome` |
| `curate` | promotion / reject — **reserved; required by no MCP tool.** Curation happens in the human web UI. |
| `comm` / `comm-file` | a *separate* surface, core and on by default — see below. Never combined with the scopes above. |

> **The standard agent token is `["read","write-draft","propose"]` — never `curate`.**
> That exclusion *is* the curation gate: an AI can capture and enhance knowledge all day, but
> only a human promotion turns a version `curated`.

**Two token shapes reach the same scoped principal.** Besides a static
`ken_<tokenId>_<secret>` API token (minted in the CLI / web UI), Ken can run an optional
**OAuth 2.1 authorization server** (`KEN_OAUTH_ENABLED`, off by default) so **claude.ai** can add
Ken as a custom connector. A connector authenticates with an opaque OAuth access token (no `_`, so
it never collides with the `ken_` shape) and always resolves to the **same agent capability set —
`read`, `write-draft`, `propose`, never `curate`** — and is revocable from the web UI's Tokens page
(*Connected apps (OAuth)*). Full setup and security model: [OAUTH.md](OAUTH.md).

> **Inter-session communication is a different endpoint, and this document does not cover it.**
> Ken also serves `comm_*` tools at `https://<ken-host>/comm/mcp` for
> AI-session-to-AI-session messaging and file exchange. They require a **dedicated** token carrying
> the `comm` (and, for files, `comm-file`) scope — a token may hold comm scopes or knowledge-base
> scopes, never both, so a client registers Ken **twice**. That surface is **core and on by
> default**, with `KEN_COMM_ENABLED=0` kept as an opt-OUT: Ken already degrades to a "COMM off"
> state when `comm.db` cannot be opened — on purpose, so an expendable database can never take the
> durable knowledge base down — and removing the variable would not remove that state, only the
> operator's control of it, which is their one remedy if COMM misbehaves in production. It still
> sits **outside** the byte-level compatibility contract, but no longer because it is optional: the
> surface is mid-redesign — notice-messages go, rooms and name-addressed send replace pairing codes
> and channel-pair addressing, and the *channel*, the central noun of today's tools, is retired — so
> promoting it now would buy a MAJOR bump, or a release cycle of deprecated v1 aliases, for no
> benefit. It is promoted when that redesign (COMM v2) lands. That is why its contract lives in
> [COMM.md](COMM.md) rather than here. Nothing in this document changes either way.

**Errors.** Tool errors are returned as ordinary MCP tool errors — a `CallToolResult` with
`isError: true` whose single text content item is the error **message string**. There is **no**
`{ "error": { code, message, retry_after_ms } }` envelope and no machine-readable error-code
vocabulary; match on the message text. Representative messages (verbatim from the code):

- `forbidden: token is missing the 'write-draft' scope` — scope check failed.
- `malformed dedup_check_token — call kb_search first` / `dedup_check_token expired — run kb_search again before saving` / `invalid dedup_check_token — run kb_search yourself before saving` — the `kb_save` search-before-save gate. The last one also covers a token issued to a *different* token holder (see below).
- `an entry with that slug already exists` — slug conflict on `kb_save`.
- `entry not found` · `version not found or not in a promotable state` · `invalid input` — store-level sentinels.
- `internal error` — any unexpected/internal error; the real detail is logged server-side, never leaked.

There is no MCP rate-limiting path, so no `retry_after_ms`. A `based_on_rev` drift is surfaced as a
non-error `warning` field on `kb_propose_enhancement`, never as a `rebase_required` error.

---

## `kb_search`  — *scope: read* — the default first move

Token-light. Returns ranked **summaries only** (no bodies), so I can scan many candidates cheaply
and then `kb_get` the few that matter. Also issues the `dedup_check_token` that `kb_save` requires,
which is what structurally forces search-before-save.

**Input**

```jsonc
{
  "query":    "string, required — natural language + the symptoms you're seeing",
  "scope":    "curated | proposals | history | all",   // default "curated"
  "kind":     "user | feedback | project | reference",   // optional filter
  "category": "string, optional",
  "k":        12,                // default 12, max 25
  "offset":   0                  // pagination
}
```

**Output**

```jsonc
{
  "results": [
    {
      "slug": "postgres-create-index-concurrently",
      "title": "CREATE INDEX locks writes; CONCURRENTLY does not, but has sharp edges",
      "summary": "A plain CREATE INDEX blocks writes for the whole build. CONCURRENTLY does not, but cannot run inside a transaction and leaves an INVALID index behind if it fails.",
      "kind": "project", "category": "db",
      "staleness": "fresh",           // fresh|aging|stale|refuted — a 'verify before relying' badge when not fresh
      "maturity": "battle-tested",    // derived from use_count + curated_rev
      "score": 0.031,                 // fused RRF score (higher = better)
      "has_provisional": false        // true ⇒ an un-curated enhancement exists (see trust policy)
    }
  ],
  "has_more": false,
  "next_offset": null,
  "dedup_check_token": "dct_v1.<hmac>"   // pass to kb_save; proves a recent search (not query-bound), ~10 min TTL
}
```

> The token is **bound to the calling token holder**: it is only valid on `kb_save`
> calls made with the same bearer token that ran the search. Passing one to another
> session does not work, and is not a supported way to skip a search. The wire shape
> is an opaque string either way — nothing to change on the client.

**Notes** — `scope=curated` (default) searches only curated heads; `proposals` also surfaces
un-curated `proposed` versions; `history` includes `superseded`/`rejected`/`withdrawn` (for "what did
we already try and reject?"); `all` drops the state filter entirely — every version in every state,
the deliberate "show me everything" view. `rejected`/`withdrawn` rows are labeled, never silently mixed in.

### Canonical query (keyword-only; add the vector CTE when embeddings are on)

```sql
WITH prose AS (
  SELECT rowid AS version_id,
         row_number() OVER (ORDER BY bm25(entry_fts, 10,8,8,5,3,2,1,1)) AS rank  -- weights: title,summary,triggers,tags,problem,solution,rationale,caveats
  FROM entry_fts WHERE entry_fts MATCH :q LIMIT 200
),
code AS (
  SELECT rowid AS version_id,
         row_number() OVER (ORDER BY bm25(entry_code_fts)) AS rank
  FROM entry_code_fts WHERE entry_code_fts MATCH :q LIMIT 200
),
-- vec AS (                                   -- when embeddings are on (migration 0002)
--   built in Go: cosine-ranked candidates over the entry_embedding BLOB table, injected as an
--   inline VALUES CTE of (version_id, rank) pairs — no entry_vec/vec0 virtual table, no MATCH KNN
-- ),
fused AS (                                    -- Reciprocal Rank Fusion (k=60); fuse by RANK, never raw scores
  SELECT version_id, SUM(w) AS score FROM (
    SELECT version_id, 1.0/(60+rank) * 1.0 AS w FROM prose
    UNION ALL
    SELECT version_id, 1.0/(60+rank) * 0.7 AS w FROM code
    -- UNION ALL SELECT version_id, 1.0/(60+rank) * 1.0 AS w FROM vec
  ) GROUP BY version_id
)
SELECT e.slug, ev.title, ev.summary, e.kind, e.category, e.staleness,
       e.curated_rev, e.use_count, f.score,
       (e.provisional_version_id IS NOT NULL) AS has_provisional
FROM fused f
JOIN entry_version ev ON ev.id = f.version_id
JOIN entry e          ON e.id  = ev.entry_id
WHERE ev.state = :scope_state                 -- 'curated' by default; multi-state for history, dropped for all
  AND e.lifecycle != 'archived'
  -- AND e.kind = :kind
  -- AND EXISTS (SELECT 1 FROM json_each(e.tags) WHERE value IN (SELECT value FROM json_each(:tags)))
ORDER BY f.score DESC
LIMIT :k OFFSET :offset;
```

A tiny post-RRF tie-break nudges `battle-tested` / fresher `verified_at` up — never enough to
override a strong textual match.

---

## `kb_get`  — *scope: read* — full fetch, batchable

**Input**

```jsonc
{
  "slugs": ["string"],                 // required, max 10 per call
  "response_format": "concise | detailed"  // default "concise" = curated head body only
}
```

**Output** — `{ "entries": [ <entry> ], "missing": ["slug-not-found"] }`, where `<entry>` is the
shape in [DESIGN.md](DESIGN.md) §4. `concise` returns the curated head body; `detailed` adds the
provisional block (subject to trust policy), provenance (`state`, `author_kind`, `confidence`,
`change_note`), and `verified_against`. Each returned entry bumps `use_count`.

> **Planned (not yet implemented):** an `include_history` flag (git-log rev list) and a `version`
> selector (fetch a specific rev). Today the tool always returns the curated head (falling back to
> the provisional draft for a not-yet-promoted entry). Use `kb_diff` to compare two revs.

---

## `kb_propose_enhancement`  — *scope: propose* — append, never overwrite

Adds a new **immutable** `entry_version(state='proposed')` to an existing entry and points
`entry.provisional_version_id` at it if it is the best proposal. Moves **no** curated pointer —
persists the improvement instantly while leaving the curated base untouched until a human promotes.
Concurrent proposals on the same entry are conflict-free siblings.

**Input**

```jsonc
{
  "slug": "string, required",
  "based_on_rev": 3,               // optional (0 = current curated head) — the rev you are enhancing; drives rebase detection
  "change_note": "string, required — the commit message: WHAT changed and WHY",
  "confidence": 0.0,               // optional, 0..1 — your self-rating
  "session_id": "string, optional",
  // Full content for the NEW version. Omitted fields inherit from based_on_rev.
  "patch": {
    "title": "…", "summary": "…", "problem": "…", "solution": "…",
    "rationale": "…", "caveats": "…",
    "code": [{ "lang": "sql", "caption": "…", "snippet": "…" }],
    "tags": ["…"], "triggers": ["…"], "applies_to": ["…"],
    "verified_against": [{ "tool": "PostgreSQL", "version": "17", "date": "2026-07-01" }]
  }
}
```

**Output**

```jsonc
{ "slug": "…", "rev_no": 4, "state": "proposed",
  "warning": "rebase: curated head is now rev 5 (you based on rev 3) — review will show a 3-way diff" }
```

`warning` is present only when `based_on_rev` ≠ the current curated head (from `parent_version_id`
lineage). Never an error — the proposal is still recorded; the human just sees the drift at review.

---

## `kb_save`  — *scope: write-draft* — create a NEW entry (draft)

Creates a brand-new entry (`lifecycle='draft'`, one `entry_version(state='proposed', rev_no=1)`).
**Requires a `dedup_check_token` from a recent `kb_search`** — the server refuses otherwise
(a `malformed dedup_check_token …` / `dedup_check_token expired …` message), so the search-before-save
discipline cannot be skipped.

**Decide save-vs-enhance with the rubric Ken returns from search:** cosine ≥0.90 or exact
trigger/title overlap → *enhance that entry, don't create*; 0.78–0.90 same category → default to
*enhance*; else → *save new + link*. Rule of thumb: **same problem, better answer → enhance
(new version, same slug); different problem sharing vocabulary → new entry + `relates` link.**

**Input**

```jsonc
{
  "dedup_check_token": "dct_v1.<hmac>",   // required, from kb_search
  "slug": "string, optional",             // derived from title if omitted; must be unique
  "kind": "user | feedback | project | reference",   // required
  "title": "…", "summary": "…",            // required
  "category": "string, optional",
  "problem": "…", "solution": "…", "rationale": "…", "caveats": "…",
  "code": [{ "lang": "…", "caption": "…", "snippet": "…" }],
  "tags": ["…"], "triggers": ["…"], "applies_to": ["…"],
  "verified_against": [{ "tool": "…", "version": "…", "date": "…" }],
  "confidence": 0.0,
  "session_id": "string, optional",
  "links": [{ "to_slug": "…", "link_type": "relates | supersedes | refutes | depends_on" }]
}
```

**Output** — `{ "slug": "…", "rev_no": 1, "state": "proposed", "lifecycle": "draft" }`.
It will not appear in default (`curated`) searches until a human promotes it. Errors (message
strings, per the vocabulary above): an expired/invalid `dedup_check_token`, or `an entry with that
slug already exists` on a slug conflict.

---

## `kb_flag_stale`  — *scope: propose* — raise a concern (safe, additive)

Signals that an entry may no longer apply (a dependency moved, a fact changed). Sets
`entry.staleness` toward `stale` and records a `flagged_stale` event; the entry stays authoritative
but ranks lower with a badge. Raising a concern is always allowed for a contributing AI — *asserting
freshness is not*: that is a human curation act (see the reserved `kb_reverify` below).

**Input** — `{ "slug": "…", "reason": "string, required", "suspected_applies_to": "node 22 changed require() of ESM" }`
**Output** — `{ "slug": "…", "staleness": "stale" }`

---

## `kb_reverify`  — *reserved, NOT yet implemented*

Asserting "I checked this and it still holds" — bump `verified_at`, reset `staleness='fresh'`, record a
`reverified` event — is an authoritative **curation** act, so it would be curate-scoped and belong in the
human web UI, never exposed to agent tokens. **It is not built yet:** today `staleness` returns to `fresh`
only as a side effect of a human **promotion**. (The `reverified` event type is already reserved in the
schema for when it lands.)

---

## v1 tools (implemented)

- **`kb_diff`** — *read* — `{slug, rev_a, rev_b}` → field-by-field diff (changed flag + both values).
- **`kb_record_outcome`** — *propose* — `{slug, outcome: helped|didnt-apply|was-wrong, note?}` → records
  the outcome; `was-wrong` also flags the entry stale. The self-curating feedback loop — call it after
  acting on a fetched entry.
- **`kb_recent_context`** — *read* — `{since_days?=14, kind?, limit?=20}` → compact briefing of entries
  with recent curation activity, to warm up a fresh session without a query.

**Semantic search:** `kb_search` transparently gains a vector arm when an embedding provider is
configured (`KEN_EMBED_PROVIDER=http|hash`, `ken embed backfill`); off by default. Cosine KNN is
computed in Go over a plain `entry_embedding` BLOB table (no SQLite extension) and RRF-fused with the
keyword arms — the tool contract is unchanged.

## Still roadmap

`kb_link(from, to_slug, type)` · `kb_related(slug)` (explicit link-graph tools).
