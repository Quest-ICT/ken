# Ken — Design

> **Ken** *(noun, archaic)* — the range of one's knowledge or understanding. *"beyond my ken."*

An AI-first personal knowledge base. Its primary user is an **AI coding agent** (Claude Code) that
stores and retrieves *curated* knowledge — solutions, caveats, pitfalls, design decisions — gathered
while working with the human, so hard-won answers are neither reinvented nor missed. The human is the
**curator**: they browse, search, promote, and edit existing entries, but do not author brand-new ones.
An entry becomes "curated" only after AI and human have refined a matter until curated knowledge emerges.

Source: **github.com/Quest-ICT/ken** (AGPL-3.0).

---

## 0. Design provenance

This design was produced on **2026-07-18** via a 9-agent workflow (4 research agents → 4 design agents →
1 synthesis) covering SQLite hybrid search, embedding footprints, remote MCP, and minimal-RAM stacks.
The gating decisions below (D1, D3, D5, and the trust policy in D4) were then settled by hand against
the synthesis rather than taken from it — they are trade-offs, not findings, and §2 records the
reasoning for each so a future reader can re-litigate them on the evidence.

---

## 1. Goals & hard constraints

| # | Constraint | How the design honors it |
|---|---|---|
| 1 | Lightweight at **runtime** (not storage) | One Go binary, embedded SQLite; ~20–30 MB idle RSS. |
| 2 | Native-application-class performance | Compiled Go + in-process SQLite; no JVM, no DB daemon, no framework. |
| 3 | Runs on a **$5/mo VPS** (1 vCPU, 1 GB RAM) | Whole stack fits with the box nearly free; 2 GB/4 GB tiers unlock optional local embeddings. |
| 4 | Cloud-hosted | Remote streamable-HTTP MCP endpoint + web UI on the VPS. |
| 5 | Backup / restore is a must | Litestream (continuous) + nightly encrypted `VACUUM INTO` snapshots, with mandatory restore verification. |
| 6 | Security is a priority | Scoped bearer tokens (AI), Argon2id + sessions (human), login brute-force lockout, **per-IP + per-token rate limiting with auto-block**, security headers, body-size caps, **in-process TLS (Let's Encrypt/ACME or a supplied cert) + HTTP→HTTPS redirect**. |
| 7 | Keep old knowledge when enhanced ("git for knowledge") | Append-only immutable `entry_version` history; superseded/rejected/refuted versions retained + searchable. |
| 8 | All knowledge is enhanceable, yet the base stays curated | Enhancements are *appends* that never move the curated head; only human promotion curates. |
| — | Structured for an AI to exploit efficiently | Two-phase `kb_search`→`kb_get`; explicit `triggers`; token-light ranking rows. |
| — | Don't foreclose a future collaborative KB | Cheap identity/space seams built now; isolation machinery deferred. |

---

## 2. Locked decisions

Each decision is recorded the same way: what was chosen, the **why**, and the trade-off accepted.

### D1 — Stack: Go single static binary  *(chosen: Go)*
`CGO_ENABLED=0` Go binary is **both** the MCP server and the web UI. `net/http` + `chi`.
- **Why:** ~15–30 MB idle RSS, ~10 ms start, and a trivial cross-compile to a single file — which is
  what makes the whole deployment story (one binary + systemd + a self-extracting installer, no
  runtime to provision) possible. Official **Tier-1 MCP Go SDK**.
- **Trade-off accepted:** a compiled, GC'd language with a smaller ecosystem for line-of-business
  concerns than the JVM or .NET. Worth it because the service is small and well-scoped, and because
  constraint #3 (a 1 GB VPS) rules out the alternatives: a managed-runtime web stack idles at
  300–600 MB, and an AOT-compiled one still takes 70–150 MB plus multi-minute builds. When the
  footprint budget is the binding constraint, it selects the language.
- **The door stays open:** the schema, the wire protocol and the MCP surface described below are all
  language-agnostic. A reimplementation on another runtime would interoperate with the same database
  and the same clients.

### D2 — SQLite driver: `ncruces/go-sqlite3` (pure-Go WASM)
- **Why:** the only pure-Go (CGO-free) driver that supports **both** `sqlite-vec` **and** a whole-file
  **encrypted VFS**. Choosing it now avoids a painful driver swap when at-rest encryption or vectors
  are enabled later.
- **Trade-off accepted:** WASM SQLite is marginally heavier than `modernc`, but the future-proofing wins.

### D3 — Retrieval: keyword-first hybrid, embeddings off at launch  *(chosen: keyword-only)*
FTS5 BM25 (prose, `porter unicode61`) + a second **trigram** FTS index (code/identifiers like `LASTVAL`,
`busy_timeout`), fused by **Reciprocal Rank Fusion** (k=60). Embeddings ship **OFF** behind a pluggable
provider SPI; `sqlite-vec` table empty ⇒ FTS-only with **no query-path change**.
- **Why:** for a curated few-thousand-entry base, keyword hybrid is excellent; vectors add cost/complexity
  for marginal recall gain. Deferring keeps the $5 tier clean and postpones the off-box-privacy question.
- **Upgrade path:** flip on hosted API (pennies/year, degrade-to-keyword when offline) anywhere, or local
  int8 ONNX (bge-small at 2 GB, EmbeddingGemma-308M at 4 GB), then a background re-embed job. Ranking uses
  RRF over **rank position only** — never compare raw BM25 vs L2 distance.

### D4 — Versioning: "objects immutable, refs move" *(the CURATION half is REVERSED in 6.0.0)*

An **entry** is a stable identity carrying ONE moving pointer: `curated_version_id`, the live head.
Every write — a `kb_save` or a `kb_propose_enhancement` — is an **append** of a new immutable
`entry_version`, and it **advances the head in the same transaction that stores it**.

> **🔴 THE CURATION GATE IS DELETED, AND THE REVERSAL IS RECORDED HERE RATHER THAN EDITED OVER.**
>
> The original D4 read: *"the curated base advances **only** when the human moves the head
> (promotes). **The AI can never self-promote — that exclusion IS the curation gate.**"* It also
> specified a second pointer, `provisional_version_id`, and a per-entry **trust policy**
> (`curated_only` / `high_confidence` / `all_proposals`) governing whether an un-curated proposal
> was served at all.
>
> **What the owner measured, after using Ken since v1:** he did not curate. He opened the entry,
> frequently read only the title, and approved. That is not review — it is a queue being
> acknowledged. What the gate actually bought was DELAY, in the one direction that hurts: a lesson
> found in one session was invisible to the next until somebody clicked.
>
> **The project had already written this down and not acted on it.** `docs/STATIONS.md:456-457`:
> *"a human who reflexively approves has converted the gate into a rubber stamp. No server-side
> design fixes that, and this document will not pretend otherwise."*
>
> **Two things were found to be untrue of the shipped code while removing it, and both are worse
> than the feature being unloved:**
> - **`trust_policy` was never implemented.** The column exists and has **zero Go references** —
>   no reader, no writer, no default, no test. Every sentence above about it described machinery
>   that never ran. (Already sitting in PARKING-LOT as #140/#152.)
> - **The gate gated DISCOVERY, never ACCESS.** `kb_get` has always fallen back to the provisional
>   body, so an un-promoted entry was served in full to anyone who knew its slug, while
>   `kb_search` returned nothing. D4's stated default — *"existence signaled, body withheld"* — was
>   wrong in **both** directions.
> - And the queue showed the entry's **curated** title beside a **proposed** version's content. The
>   title a rubber-stamping human glanced at was not the title he approved.
>
> **Why it could not be repaired instead of removed.** Reaching one station has needed no human
> since 4.0.0; the directory filter and room-as-permission went for the same reason. There is one
> human, one Claude account, and no other tenant to protect against (IDENTITY.md §4). A gate whose
> only operator is the sole owner of everything behind it is a gate against himself.

**What survives, and it now carries more load, not less:**

- **Append-only versions are untouched.** Every version is immutable (enforced by the
  `entry_version_immutable` trigger, which freezes content and deliberately leaves `state` and
  `superseded_by_version_id` free so the head can move). With writes going live, **history is the
  entire recovery mechanism** — it was a nicety under the gate and is load-bearing without it.
- **Superseded / rejected versions stay first-class and retained**, not tombstones. Nothing new can
  produce a `rejected` row, and the state stays legal because existing rows are true statements
  about decisions a human really made.
- **The `curation_event` reflog keeps its name and gains rows** — one per write, revision, revert
  and retirement. It is what the `/activity` console feed reads, and it had nine writers and zero
  readers before 6.0.0.
- **Staleness is still orthogonal to lifecycle**, and `kb_record_outcome` is now the primary
  quality signal: nothing is reviewed before it goes live, so an entry's standing rests on what
  readers report about it. A `was-wrong` blocks the top maturity tier until the content is
  rewritten — re-anchored in 6.0.0 on the head version's own timestamp, because the event it used
  to anchor on (`promoted`) no longer occurs.

**What replaced the gate, all of it BELOW the write rather than in front of it:**

- **`/activity`** — what the KB has done, newest first. It reports; it asks for nothing, and it has
  no badge, because activity is not a debt.
- **Set head** — point an entry at any earlier version, one click. The human's undo, and the only
  way to take back an agent's write.
- **Retire / restore** (`kb_retract`, and the console) — an entry stops being findable, keeps every
  version, still answers `kb_get` saying it was retired and why, and comes back in one click.
  **Before 6.0.0 nobody could do this at all**: `lifecycle` carried `archived` with no writer
  anywhere and there is no `DELETE` in the tree. Removing the queue without adding this would have
  traded a delay barrier for a permanent inability to withdraw anything.

<details>
<summary>The original D4, verbatim, as it stood until 6.0.0</summary>

#### D4 (superseded)
An **entry** is a stable identity carrying two moving pointers: `curated_version_id` (authoritative head)
and `provisional_version_id` (best usable proposal). Every enhancement is an **append** — a new immutable
`entry_version(state='proposed')` — that moves **no** authoritative pointer.
- **Why this resolves the D7/D8 tension:** knowledge is persisted the instant it is proposed (never lost),
  and the curated base advances **only** when the human moves the head (promotes). **The AI can never
  self-promote — that exclusion IS the curation gate.**
- Superseded / rejected / refuted versions are **first-class and retained**, not tombstones: a rejected
  proposal is a recorded dead-end a future AI won't re-litigate; a superseded version rescues you when
  you're back on the old dependency version where it still applied.
- **Trust policy** (per-entry, resolved *explicit param ▸ entry ▸ global default*) governs whether an
  un-curated proposal is served to the AI: `curated_only` (default — existence signaled, body withheld),
  `high_confidence` (≥0.75 served alongside, labeled `NOT_YET_CURATED`), `all_proposals`. So an improvement
  is usable *within the session it was found*, while a fresh reader still gets the last human-blessed version.
- **Staleness is orthogonal to lifecycle:** each version carries `applies_to` (e.g. `["spring-boot 4.0.x"]`),
  `verified_at`, `verify_ttl_days`. Time / explicit flag / recorded dependency-bump can age an entry to
  `stale` — still authoritative, ranks lower, carries a "verify before relying" badge.

</details>

### D5 — Storage: SQLite is the single source of truth; **no** git/Markdown mirror  *(chosen: SQLite-only)*
- **Why:** the whole point of the mirror would have been durability and browsability, and neither needs it.
  The full "old knowledge saved us" value is preserved entirely in the
  in-DB append-only `entry_version` history + the `curation_event` reflog. What is dropped is only the
  *human-browsable Markdown diff view in an external git forge* — an additive, reversible v1 option if ever wanted.
- **Consequences:** backup is the only durability path, so it is treated as non-negotiable (§6). The
  "promotion = free git changelog" idea is replaced by the in-app `curation_event` reflog. Source **code**
  still lives in the `ken` git repository as normal — only the KB *data* is un-mirrored.

### D6 — Concurrency: WAL + single in-app writer
WAL mode; all writes serialized through **one writer connection** (turns contention into an in-process
queue, not `SQLITE_BUSY` races); readers on a separate pool. Every write is `BEGIN IMMEDIATE` and tiny.
The promotion guard is the proposal's `state='proposed'` check (a version is promotable only once, inside the single writer's IMMEDIATE tx); `lock_version` is still bumped on the entry row but no longer CAS-checked.
- **Why:** one human's curation is human-paced; several concurrent AI *sessions* mostly *propose*
  (conflict-free appends). The single-writer ceiling is a non-issue here.
- **Escape hatches (nothing forecloses):** WAL2 / `BEGIN CONCURRENT`, then Postgres/pgvector — only if
  genuine write **throughput** (not search latency) ever forces it.

### D7 — Name: `ken`  *(chosen)*
Short, ownable, on-theme. MCP tool names use the neutral `kb_*` prefix for clarity (cosmetic; could be `ken_*`).

### D8 — Web UI: self-contained, themeable, multilingual  *(chosen)*
The curator UI is a shared design system (dark-default + light, CSS custom-property tokens, IBM Plex
type, inline-SVG compass + icons, the "Lineage Rail" version history) rendered by `html/template`.
Three constraints are locked, each earning its keep:

- **Zero external requests.** All CSS, JS, fonts and icons are served same-origin. **Why:** a self-hosted
  KB must render identically **offline / air-gapped** and leak nothing to third-party CDNs, and it keeps the
  **strict self-only CSP** (`default-src 'self'`) clean by construction. Trade-off accepted: no icon-font or
  CDN convenience — icons are inline SVG, type falls back to the system stack.
- **No inline scripts or event handlers.** One small same-origin `app.js` wires everything through
  **delegated `data-*` handlers** (theme + language switch, copy, confirm-guarded destructive actions,
  password reveal, mobile nav). **Why:** the CSP forbids inline `<script>` and `on*=` attributes; a prior
  bug (0.5.1) came from a redirect that a form-action CSP blocked, so the rule is enforced, not aspirational.
  Inline **styles** are allowed (`style-src 'unsafe-inline'`) and used sparingly for one-off layout.
- **Theme resolved server-side.** The `ken_theme` cookie is read at render time and stamped on `<html>`, so
  there is **no first-paint flash**; the toggle also respects `prefers-color-scheme` when unset.

**i18n (multilingual).** The UI translates through a **reloadable** `.properties` catalog (`internal/i18n`):
English, Spanish + French embedded, operator drop-ins via `KEN_I18N_DIR` override/extend at runtime with a
`lang → English → key` fallback, a `ken_lang` cookie + `Accept-Language` selector, count-aware plurals
(`.one`/`.other`), and translatable settings-field labels (registry English as fallback). **Why reloadable
and drop-in:** adding a language or fixing a string must not require a rebuild or a restart of a
single-binary deployment. **Scope:** human UI only — the AI/MCP surface and logs stay English (a machine
contract, not end-user copy). Full reference: [`I18N.md`](I18N.md).

### D9 — Inter-session communication: in-process, core, ephemeral  *(chosen: embed)*
A second service on the same deployment: authenticated **session-to-session messaging** between AI
sessions (same machine or not), as `internal/comm` inside the same binary, with its **own** SQLite file,
its **own** MCP endpoint, its **own** `comm` scope, and **core and unconditional**
(`KEN_COMM_ENABLED` was removed in 2.0.0; there is no switch). Specified for 1.2.0;
full contract in [`COMM.md`](COMM.md).
- **Why it belongs in Ken at all:** the deployment already offers the two things such a service needs and
  are expensive to stand up twice — an authenticated endpoint every session already reaches, and a host
  with spare capacity. The alternative is a second daemon whose only novel asset is a token table.
- **Why a separate database, endpoint and scope:** message traffic is high-churn and **expendable**;
  knowledge is low-churn and **durable**. Separating the files keeps ephemeral WAL churn out of the
  replicated database and out of the KB's single writer; separating the endpoint and scope means a KB
  token cannot message and a comm token cannot write knowledge.
- **Why it is core now:** it shipped off by default so a default install stayed exactly the curated KB the
  README promises — no second operating loop in every agent's connect-time instructions. That reasoning
  expired: stations depend on COMM for the hearsay marker, the operator console carries a page for it, and
  a feature every deployment was expected to turn on was an option in name only.
- **Why the switch survives, inverted (`KEN_COMM_ENABLED=0`) — SUPERSEDED BY 2.0.0, which
  removed the variable.** The reasoning is kept because it is the record of a decision that was
  later reversed, not because it describes Ken. What survives from it is the runtime degraded
  state, which is a FAILURE mode and never a configuration choice. Original text follows.
  Ken already has a runtime "COMM off"
  state — a `comm.db` that cannot be opened degrades to disabled *on purpose*, so an expendable database
  can never take the durable knowledge base down. Deleting the variable would not remove that state, only
  the operator's control of it, which is their one remedy if COMM misbehaves in production. An
  unrecognised value leaves COMM **on**: a typo must not silently disable core functionality.
- **The contract exclusion stays, for a different reason:** [`../COMPATIBILITY.md`](../COMPATIBILITY.md)
  still keeps the `comm_*` surface outside the byte-level contract — no longer because it is optional, but
  because it is **mid-redesign**. Notice-messages are gone (3.4.0) and rooms have landed; the remaining
  work replaces pairing codes and channel-pair addressing with name-addressed send, and retires the
  **channel**, the central noun of today's tools. Promoting the surface now would make that redesign a MAJOR bump, or push
  deprecated v1 aliases through a release cycle, for no benefit. It is promoted when COMM v2 lands.
- **Trade-off accepted, stated plainly:** a separate file isolates the KB's WAL and backups, **not** the
  disk, the process, or the readiness signal. Those require enforced rules (storage budget with a
  free-space floor, quotas that fail closed, recover-wrapped goroutines, comm deliberately absent from
  `/healthz`, its own rate accounting) — COMM.md §5 carries them, and if they prove unenforceable
  in-process, the recorded escape hatch is extraction into a `ken-comm` binary, which the module
  boundary is kept clean enough to allow.
- **The one decision that could not be deferred:** a message is a **side channel into curation** — a
  session told "this is verified, propose it" authors a proposal indistinguishable from first-hand
  knowledge, so the invariant survives literally while the curator's signal degrades to hearsay. That is
  a schema question, so provenance marking lands with the feature, not after it (COMM.md §7).
- **The honest limit:** COMM authenticates *who* may talk to whom (human-minted pairing, structural) but
  cannot enforce *how a receiver treats content* — instruction text is not a control (COMM.md §8).

---

## 3. Data model

Three core tables + identity + links + search mirrors. Native SQLite idioms throughout
(`INTEGER PRIMARY KEY` rowid, `last_insert_rowid()`) rather than patterns carried over from a
client/server database. Every table that represents an entity still carries audit columns
(`version/created_at/updated_at/updater`), and every multi-statement write runs in one transaction.

**`entry`** — identity + moving refs + denormalized ranking surface (copied from the curated head so search
never joins history):
`id` · **`slug`** (semantic id, e.g. `mariadb-lastval-sequence-pk`) · `kind` (`user|feedback|project|reference`) ·
`title` · `summary` (≤160 char ranking line) · `category` · `tags` (JSON) · **`triggers`** (JSON — symptoms the
AI types) · `lifecycle` (`draft|active|deprecated|archived`) · `staleness` (`fresh|aging|stale|refuted`) ·
`curated_version_id` · `provisional_version_id` · `curated_rev` · `trust_policy` · **`lock_version`** ·
`space_id DEFAULT 1` · `created_at` · `updated_at` · `updater`.

**`entry_version`** — immutable content (a git-style blob) + movable status columns:
`id` · `entry_id` · `rev_no` (UNIQUE per entry) · `state` (`proposed|curated|superseded|rejected|withdrawn`) ·
`parent_version_id` (head it was based on — lineage / diff base / rebase warning) · **content (frozen):**
`title`, `summary`, `problem` ("when does this apply?"), `solution` (**How to apply**), `rationale`
(**Why** + trade-offs), `caveats`, `code` (JSON `[{lang,caption,snippet}]`), `context_json`
(`applies_to[]`, tags, links) · **provenance:** `author_actor_id`, `author_kind` (`ai|human`), `session_id`,
`confidence` (0–1), `change_note` (commit message) · **movable status:** `superseded_by_version_id`,
`reviewed_by/at`, `verified_at`, `verify_ttl_days` · `created_at`.

**`curation_event`** — append-only reflog: `entry_id`, `version_id`, `event_type`
(`proposed|promoted|superseded|rejected|deprecated|archived|reverified|refuted`), `from_state`, `to_state`,
`actor`, `actor_kind`, `session_id`, `note`, `created_at`.

**`entry_link`** — `from`, `to`, `link_type` (`relates|supersedes|refutes|depends_on`) — resolves `[[wikilinks]]`.

**`actors`** — `id`, `kind` (`human|ai`), `display_name`, `space_id DEFAULT 1`. The human logging in *is* an
actor; each API token maps to an actor. Every version/event is actor-attributed.

**`api_tokens`** — `token_id` (clear), `secret_sha256`, `actor_id`, `scopes` (JSON), `revoked_at`, `last_used_at`.

**Search mirrors, keyed by `entry_version.id`** (any version independently rankable): `entry_fts` (FTS5 prose,
BM25 weights `title 10, summary 8, triggers 8, tags 5, problem 3, solution 2, rationale/caveats 1`),
`entry_code_fts` (trigram), `entry_embedding` (a plain BLOB table: `version_id`, `model_id`, `dim`, `vec` little-endian float32; brute-force Go cosine KNN, **not** `sqlite-vec`/`vec0`; PK `(version_id, model_id)`; empty until embeddings on).
Default queries filter `state='curated'`.

Per-connection pragmas: `journal_mode=WAL, synchronous=NORMAL, busy_timeout=10000, foreign_keys=ON`.

---

## 4. AI retrieval protocol (MCP)

Remote **streamable-HTTP** MCP at `https://<ken-host>/mcp`, TLS-only.
Wiring: `claude mcp add --transport http ken https://<ken-host>/mcp --header "Authorization: Bearer $KEN_TOKEN" --scope user`.
On connect the server delivers **its own operating instructions** in the `initialize` response
(`serverInstructions`, `internal/mcpserver/server.go`), so an agent learns the search-first → record-outcome →
save/enhance loop without a human pasting a prompt. An **OAuth 2.1 authorization server** (on, unconditional since 2.0.0)
lets claude.ai add Ken as a custom connector instead of a pasted bearer token — see [`OAUTH.md`](OAUTH.md).

**Two-phase, token-light:**
- **`kb_search`** (default first move) — in: `query` (NL + symptoms), `filters` (kind/tags/status), `scope`
  (`curated|proposals|history`, default curated), `k` (12, max 25), `offset`. Out: ranked rows, **no bodies** —
  `{slug, title, summary, kind, category, staleness, maturity, score, has_provisional}`, ~40–70 tokens each,
  plus `has_more`/`next_offset`. Stays under MCP's 10 K-token line.
- **`kb_get`** — in: `slugs[]`, `response_format` (`concise|detailed`). Out: full body,
  batchable. Bumps `use_count`.
- **`kb_propose_enhancement`** — append a `proposed` version to an existing entry (append-only, conflict-free).
- **`kb_save`** — create a new entry (`draft`). **Requires a `dedup_check_token` that only `kb_search` issues**,
  so the AI structurally cannot skip search-before-save.
- **`kb_flag_stale`** — staleness signal (raises a concern; asserting freshness is a human curation act, not an MCP tool).
- **Built:** `kb_diff`, `kb_record_outcome`, `kb_recent_context`. **Still deferred:** `kb_link`.

**Hybrid ranking:** one SQL query, three CTEs (prose BM25, trigram BM25, vector KNN), filters applied *inside*
each CTE before fusion, fused by RRF (`Σ weight_m · 1/(60+rank_m)`, weights prose 1.0 / code 0.7 / vector 1.0),
with a small post-RRF tie-break toward `battle-tested`/fresher `verified_at`.

**Dedup rubric returned to the AI:** cosine ≥0.90 or exact trigger/title overlap → `duplicate` (enhance, don't
create); 0.78–0.90 same category → `likely-overlap` (default = enhance); <0.78 → `novel` (save + `relates` link).
Rule of thumb: *same problem, better answer → enhance (new version, same slug); different problem, shared
vocabulary → new entry + link.*

### Example curated entry (`kb_get … detailed`)

```json
{
  "slug": "mariadb-lastval-sequence-pk",
  "title": "Read SEQUENCE ids with LASTVAL(), not GeneratedKeyHolder",
  "summary": "GeneratedKeyHolder returns 0/null for MariaDB SEQUENCE PKs → FK violations; read the id with SELECT LASTVAL(seq) on the same tx-bound connection.",
  "kind": "project", "category": "persistence",
  "tags": ["mariadb","jdbc","sequence","primary-key","transaction"],
  "triggers": ["generated key returns 0","generated key null","FK violation on insert","LASTVAL returns null"],
  "lifecycle": "active", "staleness": "fresh",
  "confidence": 0.98, "use_count": 14, "curated_rev": 3, "has_provisional": false,
  "curated_head": {
    "problem": "Inserting into a table whose PK is BIGINT DEFAULT NEXT VALUE FOR <t>_id_seq and reading the new id via RETURN_GENERATED_KEYS / GeneratedKeyHolder. On MariaDB this frequently yields 0 or null, so the dependent insert fails with a FK violation.",
    "solution": "After the INSERT, read the id with SELECT LASTVAL(<t>_id_seq). LASTVAL is session-scoped, so INSERT and LASTVAL MUST run on the same JDBC connection — make the service method @Transactional so Spring binds one connection for both.",
    "rationale": "GeneratedKeyHolder relies on driver-returned auto-generated keys, unreliable for SEQUENCE-DEFAULT columns. LASTVAL is the sequence's own last-value accessor, deterministic within a session. Trade-off: one extra round-trip for correctness.",
    "caveats": "If LASTVAL returns null, INSERT and read ran on different connections — fix the tx binding, don't retry. this.foo() self-invocation bypasses the AOP proxy → no tx → wrong LASTVAL.",
    "code": [{"lang":"java","caption":"read new id in same tx","snippet":"Long id = jdbc.queryForObject(\"SELECT LASTVAL(foo_id_seq)\", Long.class);"}],
    "verified_against": [{"tool":"MariaDB","version":"12.0","date":"2026-07-01"}],
    "related": ["spring-self-invocation-bypasses-transaction","never-use-auto-increment-mariadb"]
  },
  "provenance": {"session_id":"s-2026-06-12-a1b2","project":"billing-service","author":"claude","first_seen":"2026-06-12"},
  "history": [
    {"rev_no":1,"state":"superseded","change_note":"import from memory/lastval.md","author":"import"},
    {"rev_no":2,"state":"superseded","change_note":"added self-invocation caveat","author":"claude"},
    {"rev_no":3,"state":"curated","change_note":"verified against MariaDB 12.0","author":"curator"}
  ]
}
```

---

## 5. State machines

**Entry lifecycle:** `draft` —*human promote*→ `active` —*human*→ `deprecated` (discoverable, flagged) or
`archived` (retained, out of default search; reversible).

**Version state:** `proposed` —*human promote (curation gate)*→ `curated` —*(newer promoted)*→ `superseded`;
or `proposed` → `rejected` / `withdrawn`. **Only human promotion produces `curated`.** All non-curated states
are retained and searchable via `scope=proposals|history`.

**Promotion** (the only head UPDATE) — one `BEGIN IMMEDIATE` txn guarded by the proposal's `state='proposed'`
check (a version is promotable only while proposed): supersede old head, mark proposal `curated`, advance
`curated_version_id`/`curated_rev`, reset `staleness='fresh'`, reindex FTS/vec, append `curation_event`. A
duplicate or stale promote (the version is already `curated`/`superseded`, or gone) returns `ErrBadVersion`
from that state check — reconcile, don't clobber. (`lock_version` is still bumped on the entry for auditing,
but it is no longer part of the guard — no CAS, no `rows_affected=0` reconciliation.)

---

## 6. Backup, restore, security, deploy

**Backup (two tiers; no git fallback under D5, so verification is mandatory):**
1. **Litestream → S3-compatible bucket** (B2 / R2 / MinIO), continuous WAL shipping, ~1 s RPO. Primary DR.
2. **Nightly `VACUUM INTO 'snapshot.db'`** as a named restore point — gzip-compressed, `0600`, and not
   encrypted. Transport, destination and at-rest protection belong to whoever moves the file.

Never `cp` a live WAL DB (torn file). **Restore verification before "good":** `PRAGMA integrity_check`, FTS5
self-check, `COUNT(vec)==COUNT(entries)` parity, a canary MATCH (+ KNN when vectors on), and count/`MAX(rev)`
match against the last checkpoint. **Intent: age-encrypt every backup** — the biggest real exposure is copies
leaving the box, and a file mode does not travel with a copy. *Shipped as opt-in:* encryption turns on when the
operator sets a recipient, because Ken cannot generate or escrow a private key on the operator's behalf, and a
key it minted itself would sit on the box it protects. The docs therefore have to sell the decision rather than
assume it — see [`BACKUP.md`](BACKUP.md).

**At-rest encryption:** live file whole-file only (a field-encrypted body is opaque to FTS/vec — would silently
break the product). MVP relies on provider volume encryption + encrypted backups; enable `ncruces` encrypted VFS
(whole-file, transparent to FTS/vec, key from a systemd credential, one long-lived connection) once the shared
$5 host is deemed outside the trust boundary. Verify Litestream + encrypted-VFS on the real VPS before committing.

**Security:**
- **AI/MCP:** scoped bearer tokens `ken_<id>_<secret>` (high-entropy secret, shown once). Store
  `token_id` clear + `SHA-256(secret)`, constant-time compare — **do not Argon2 a 256-bit token.** Scopes as
  capabilities: `read` / `write-draft` / `propose` / `curate`. **Standard agent token = `read,write-draft,propose`
  — never `curate`** (that exclusion is the curation gate). Soft-revoke via `revoked_at`; `last_used_at` surfaces
  stale tokens.
- **Human UI:** single owner actor, **Argon2id High (32 MiB, t=2, p=1)**; server-side `sessions` (random 32-byte
  id, `__Host-ken_sess` cookie `HttpOnly;Secure;SameSite=Strict`); per-session CSRF; honeypot + per-IP lockout;
  no public signup (owner seeded at first-run).
- **TLS — in-process (implemented, `internal/webtls`).** `KEN_TLS` selects the posture: `acme` (in-process
  Let's Encrypt via `autocert` — automatic issuance + renewal, HTTP-01/TLS-ALPN-01, certs cached under the
  data dir), `file` (an operator-supplied PEM cert/key, hot-reloaded when it changes on disk), or `off` (plain
  HTTP — valid ONLY behind a TLS-terminating reverse proxy). In the TLS modes a `:80` listener serves the ACME
  challenge and 301-redirects everything else to HTTPS; the unprivileged service binds `:80`/`:443` via the
  `CAP_NET_BIND_SERVICE` the unit grants. Terminating TLS in-process implies Secure/`__Host-` cookies; for the
  proxy posture pass `--secure-cookies` and set `KEN_TRUSTED_PROXIES` for the forwarded client IP. Keep URLs
  root-relative.
- **Abuse:** a per-IP **token-bucket** guard (`internal/ratelimit`) is the outermost handler — loopback,
  configured CIDRs and `/healthz` are exempt; over-limit requests get `429` + `Retry-After`, and a repeat
  offender is **auto-blocked** (`403`) for a lockout window. MCP additionally enforces a **per-token** limit
  (keyed by token id, so it survives an IP change). Both are configurable via `KEN_RATELIMIT*` (on by
  default). The per-IP login brute-force lockout (`loginGuard`) still guards the login form specifically.
  Plus HSTS, nosniff, SAMEORIGIN, a strict self-only CSP, and body-size caps (`MaxBytesReader`). Client-IP
  resolution is trusted-proxy-aware (`internal/clientip`, shared with the login guard). (App-layer abuse
  control — volumetric L3/4 DDoS stays upstream at the proxy/edge.)
- **Secrets:** systemd `LoadCredential=` (tmpfs) for DB key, token-HMAC/session keys, Litestream/age keys,
  (only if used) embedding API key. Config `0600`.

**Deploy shape:** one static binary → `scripts/ken.sh` launcher (detached/foreground/stop,
`KEN_HOME/BIN/OPTS`), `Type=simple` systemd unit (`SuccessExitStatus=143`, `AmbientCapabilities=CAP_NET_BIND_SERVICE`),
self-extracting `.bin` installer (SELinux `restorecon`, opt-in firewall).

**Human UI (deliberately minimal):** a **home dashboard** (KB stats + review queue + recent activity),
search / a dedicated **Browse** page (filter by kind/category/staleness/lifecycle) / read, the **proposal queue
with diff + Promote/Reject**, version history, `[[link]]` graph, token admin (incl. **Connected apps (OAuth)**
when enabled), plus — core, so present by default — the **COMM console** (`/comm`) and the **stations
console** (`/stations`). **No new-entry creation by the human, by design.** First-run
wizard seeds the owner + DB/token secrets.

---

## 7. Collaborative future — now vs deferred

**Build now (cheap, additive):** `actors` + `api_tokens.actor_id`; `author_actor_id`/`owner_actor_id` on
entries/versions; `space_id BIGINT NOT NULL DEFAULT 1` everywhere with seeded `spaces(1,'personal')`; auth as one
pluggable middleware; scopes as data.
**Defer until a second human exists:** space isolation (`WHERE space_id=?` / separate DB),
per-space RBAC / control points / sub-users, Postgres/pgvector. The migration is then purely additive — no existing
row retrofitted.
*(**OAuth 2.1 / DCR** was originally parked here but shipped early for a different reason: an **optional**
authorization server — unconditional since 2.0.0 — that lets claude.ai add Ken as a custom connector. It is
still a single-human deployment; a connector's capability is `read`/`write-draft`/`propose`, never `curate`. Full
details in [`OAUTH.md`](OAUTH.md).)*

*(**Inter-session communication** (**D9**, specified for 1.2.0) is the first subsystem to exercise these seams
in anger, and it sharpened one of them: ownership must be keyed on `space_id` **plus the authorizing human**,
never on the actor alone — actors resolve by display name, so every token minted with the same actor name is
**one** actor row, and an actor-keyed ownership check would reject nothing it was meant to reject. COMM
therefore carries `token_id` + `actor_id` + `space_id` on every endpoint, scopes every listing to the owner,
and makes channel establishment two-sided from day 1 — all cheap now, all MAJOR surgery later. See
[`COMM.md`](COMM.md) §10.)*

---

## 8. Roadmap

- **MVP** — Go + SQLite/WAL (`ncruces`); FTS5 prose + trigram, RRF, **embeddings off**; the core schema +
  `actors`/`api_tokens`; MCP `kb_search`/`kb_get`/`kb_save`/`kb_propose_enhancement`/`kb_flag_stale`;
  bearer tokens (`read,write-draft,propose`); minimal web UI (search, proposal queue w/ diff, history, tokens);
  Argon2id High + session + CSRF; in-process ACME TLS; per-IP + per-token limits; Litestream + encrypted nightly
  snapshots with restore verification; **importer from the flat Markdown/YAML memories** (each lands `rev_no=1,
  curated`); systemd unit + launcher + `.bin` installer; first-run wizard.
- **v1** — `kb_diff` + richer history/link-graph UI; staleness sweep + dependency-bump flagging + `verified_against`;
  trust-policy plumbing (`curated_only|high_confidence|all_proposals`); whole-file encrypted VFS on; refined dedup
  token flow; `kb_record_outcome` feedback loop; `kb_recent_context` session warm-up.
- **Later** — embeddings on (hosted API opt-in, or local ONNX at 2 GB/4 GB) + re-embed job; **optional** git-forge
  Markdown mirror if browsable diffs are wanted; space-scoping + OAuth *only if* a second party arrives;
  Postgres/pgvector *only if* write concurrency forces it.

---

## 9. Ideas beyond the brief (value-adds)

- **`kb_record_outcome` feedback loop** — the agent reports whether a fetched entry `helped | didn't-apply |
  was-wrong`; drives maturity `seed → battle-tested` on real evidence and auto-nominates wrong entries into the
  review queue. Retrieval becomes self-curating.
- **Negative knowledge as first-class entries** — rejected proposals and refuted versions become *searchable
  warnings* ("we tried `spring-session-hazelcast`, dropped in 4.x — don't"). Paid-for dead-ends are among the most
  valuable assets and today would be discarded.
- **`kb_recent_context` warm-up** — "what this KB learned in the last N sessions on project X" as a compact briefing,
  so a fresh session starts warm.
- **Confidence-gated auto-linking** — when dedup finds 0.78–0.90 overlap and the agent chooses "distinct," auto-create
  the `relates` edge; the `[[wikilink]]` graph densifies as a byproduct of normal use.
- **"Explain this ranking" debug view** — per-arm RRF contributions for a query; invaluable for tuning weights.
- **Weekly staleness digest** (a scheduled job) — a bounded 5-minute review; silent rot is what kills
  personal KBs.

---

## 10. Implementation status & open items

> **THE "BUILT" INVENTORY BELOW PREDATES 4.0.0.** It records `/comm/mcp` as COMM's endpoint, the
> pairing code as the way a channel is created, and an eight-tool `/mcp`. All three are now wrong:
> there is ONE machine surface carrying 41 tools, no pairing code, and a link created by the first
> message. See [CHANGELOG.md](../CHANGELOG.md) and [UPGRADING.md](UPGRADING.md) for what changed.

**Built (git `main`, tests green):** MVP (MCP `kb_search`/`kb_get` + write path + promotion) · token/user CLI · human web UI · backup + deploy · flat-memory importer · **semantic embeddings** · **first-run wizard** · **v1 tools** (`kb_diff`, `kb_record_outcome`, `kb_recent_context`) · **in-process TLS/ACME** (`internal/webtls`) · **per-IP/per-token rate limiting** (`internal/ratelimit`, `internal/clientip`) · **web token admin** (`/tokens`) + **live editable settings** (`/settings`, `internal/settings` — rate limits/login/session/trusted-proxies/ACME-domains applied without a restart via an atomically-swapped snapshot) · **home dashboard** (`/` — KB stats + review queue + recent activity) + dedicated **Browse page** (`/browse`) · **server-delivered MCP instructions** (the operating loop shipped to clients in the `initialize` response) · **OAuth 2.1 authorization server** (unconditional since 2.0.0; discovery + DCR + consent + token endpoints, connectors revocable from `/tokens`; migration `0008_oauth.sql`; see [`OAUTH.md`](OAUTH.md)) · **themeable + multilingual web UI** (dark/light
self-contained design system, zero external requests; reloadable i18n with English, Spanish + French embedded and
runtime drop-in translations — `internal/i18n`, see [`I18N.md`](I18N.md); decision **D8**). Knowledge-base MCP surface (`/mcp`) = 8 tools.

**Implementation note — embeddings (revises D3's mechanism, not its intent):** rather than `sqlite-vec`/`vec0`, Ken stores embeddings in a plain `entry_embedding` BLOB table ([`migrations/0002_embeddings.sql`](../migrations/0002_embeddings.sql)) and computes cosine KNN **brute-force in Go**, fused into the FTS RRF query via a Go-built `VALUES` CTE. This needs no SQLite extension and is fine at single-user scale (the design's "flat, never ANN" stance). The `internal/embed` SPI keeps a hosted OpenAI-compatible provider and an offline hash provider; `ken embed backfill` populates vectors; `KEN_EMBED_*` configures it (off by default).

**Resolved:** MCP tool prefix = `kb_*`; project renamed to `ken`; `git init` done; `Migrate()` applies all migrations (embeddings table always present, empty when unused).

**Built — inter-session communication** (decision **D9**), shipped in 1.2.0 and now **core and
unconditional** (`KEN_COMM_ENABLED` was REMOVED in 2.0.0; there is no switch): authenticated
session-to-session messaging between
AI sessions on the same or different machines. `internal/comm` (own SQLite file, own migrations) + `internal/commserver` (eight
`comm_*` tools on their own `/comm/mcp` endpoint, the `comm` and `comm-file` scopes, long-poll wakeups
with a shutdown drain), a human console at `/comm` where the operator mints the pairing codes that are
the only way a channel comes into existence, a live settings group, `ken_comm_*` metrics, and file
exchange (same-host rendezvous plus a one-time-grant HTTP relay). Curation is protected by the
`via_comm` hearsay marker (§7 of the contract), which surfaces on both the review queue and the
promote view. Full contract, including the isolation rules that make the in-process choice honest:
[`COMM.md`](COMM.md).

**Stations** ([`STATIONS.md`](STATIONS.md)): a durable, human-named working identity that AI sessions
staff, owning a notebook, a task list and a small file locker, and serving as COMM's durable address so
a peer relationship — and a mailbox — outlives the session that made it. Written contract-first for the
reason C-series decisions exist at all: the load-bearing choices (who owns a message inbox when several
sessions staff one identity, what a credential revocation actually severs, where durable state may
point) were cheap to argue on paper and would have been expensive to discover in code. That paid off
literally — implementing against the written contract is what surfaced a sequence-numbering defect that
would have made a cumulative acknowledge settle messages nobody had read. Core and unconditional, like COMM —
but resolved independently of it (`KEN_STATION_ENABLED` was REMOVED in 2.0.0 alongside COMM's;
neither can be switched off, and a COMM failure still leaves stations fully working): a notebook and a task list are worth having with no peers at all (S2).

**Still open / deferred:** at-rest whole-file encryption timing (VFS) · git/Markdown mirror (deferred by D5) · local ONNX embedder + background re-embed job · `kb_link`/`kb_related` graph tools · reaching an idle COMM session (COMM.md §12) · COMM's per-IP strike exemption and poll-interval advertisement (COMM.md §5.5) · COMM console re-labelling + endpoint identity (COMM.md §12) · **client-side sortable listing tables** (below). *(All §1 security-priority items are now implemented.)*

**Deferred UI enhancement — sortable listing tables (all web UI grids, not just COMM).** Every
data-listing table in the web UI (the proposal queue, Browse, the tokens list, the COMM channels and
endpoints panels, …) would be more useful with **click-to-sort column headers** — the immediate ask
was the COMM "Channels" panel, but the value is uniform across grids, so it should be one mechanism
applied everywhere rather than a per-table hack. Constraints that make this a real design item rather
than a drop-in: it must stay within **D8** — one small same-origin `app.js`, delegated `data-*`
handlers, **strict self-only CSP** (no inline scripts, no external library), and fully **progressive**
(the server already renders a sensible default order, so with JS off the table is exactly as it is
today). The Ken-native shape: a `data-sortable` opt-in on a `<table>`, headers annotated with a sort
key and type (text vs numeric vs date, since a display string like "120 B" or a `yyyy-MM-dd` stamp
must sort by its underlying value), and the sort done client-side on the already-rendered rows — no
new server round-trip, no persistence in v1. **Explicitly NOT** the house "listing-tables" component
(that assumes a JS framework and CSS toolkit Ken does not use); this is a from-scratch, dependency-free
implementation in Ken's own idiom. Multi-column sort, column reorder/resize, and per-user persisted
layout are a larger follow-on, out of scope for the first pass.
