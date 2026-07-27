# Changelog

All notable changes to **Ken** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A behavioral change, feature, or design decision lands with its CHANGELOG entry in the
same change — never "docs later".

> **Releases before 1.1.0 predate this public repository.** Ken was developed privately
> through the 0.x line; those entries are kept because the *why* behind each decision is
> the useful part, but they have no public tag or downloadable release. A `1.0.0-rc.1`
> release candidate was run internally and superseded before a 1.0.0 was finalized, so
> the first public release is **1.1.0**.

## [Unreleased]

### Added
- **Inter-session communication (COMM) — schema and store layer** (`internal/comm`, decision **D9**,
  contract in [docs/COMM.md](docs/COMM.md)). Compiled but **not mounted**: there are no MCP tools, no
  `comm` scope and no `cmd/ken` wiring yet, so the feature does **not** run in this build. Experimental
  and off by default when it does ship.
- Its own SQLite file with its own forward-only migrations, versioned **independently** of `ken.db` so
  a COMM schema change never touches the knowledge base. Ownership columns (`actor_id`, `space_id`,
  `token_id`) name rows in `ken.db` and are deliberately **not** foreign keys — SQLite FKs cannot span
  database files, so the caller that holds both handles validates them. The schema says so in place,
  because the tempting "fix" is to move the tables into `ken.db`, which would undo the separation the
  whole design rests on.
- **The content/metadata split is enforced, and it is load-bearing rather than an optimization.**
  Acking deletes the message *body*; the metadata row survives. Deleting the whole record — the
  intuitive storage win — is mutually exclusive with request/response: with two requests outstanding
  and both acked, a later reply would reference a row that no longer exists, and the server could
  neither validate it, route it, nor report which request was answered. A regression test pins it.
- **Ack means *processed*, not received, and delivery is at-least-once.** Polling never hides a message
  from the next poll; only acking advances state, and every message carries a delivery count so a
  receiver can spot a redelivery. This makes a lost poll response harmless — the ordinary failure on
  this transport — and means a turn truncated mid-processing gets its message back rather than losing
  it. Sends are idempotent per client-supplied key; acks succeed on unknown or already-acked ids.
- **Channel establishment is human-gated and two-sided.** An agent cannot conjure a channel: a human
  mints a pairing code and *both* endpoints redeem it. This is the same move that makes the curation
  gate trustworthy — withhold the capability rather than instruct the model not to use it. Re-redeeming
  from a member is idempotent, a third endpoint is refused, and an expired code is indistinguishable
  from an unknown one so codes cannot be probed.
- Per-endpoint secrets, because the operating convention is one token per *machine*: without them two
  sessions sharing a token could poll and ack each other's messages, most likely by accident when both
  register the same label. Registration therefore never reuses an endpoint. Sequence numbers are per
  channel *and direction*, which is the only ordering COMM promises.
- Quotas (body cap, per-channel un-acked depth, TTLs) are enforced **in SQL inside the writing
  transaction**, never by keying the shared rate-limiter bucket — that bucket fails *open* when
  saturated, which is right for IP and token keys an attacker cannot mint cheaply and wrong for
  identifiers a caller creates in a loop. A fail-open quota is a disk-full outage; a refused message is
  a retry.
- The sweeper expires **delivered-but-never-acked** messages as well as queued ones (a message polled
  by a session that then died must not live forever), drops the retained body of a request whose reply
  deadline passed, and purges settled metadata past its retention window.
- 21 tests, including three whose failure modes were verified by deliberately breaking the invariant
  they guard.

### Docs
- **New design contract: [docs/COMM.md](docs/COMM.md) — inter-session communication**, plus locked
  decision **D9** in [docs/DESIGN.md](docs/DESIGN.md). A specification only: **nothing is implemented**,
  and when it lands it will be **experimental and off by default** (so it sits inside
  [COMPATIBILITY.md](COMPATIBILITY.md)'s optional-and-off-by-default exclusion rather than the stable
  contract). Targeted at 1.2.0 as an additive MINOR — new tools, new scope, new settings, additive
  migrations, nothing removed or retyped.
- The feature: authenticated **session-to-session messaging** between AI sessions on the same or
  different machines, as an opt-in `internal/comm` subsystem with its **own** SQLite file, MCP endpoint
  and `comm` scope. Why it belongs in Ken: the deployment already provides the two things such a service
  needs and that are expensive to stand up twice — an authenticated endpoint every session reaches, and
  a host with spare capacity. Why it is walled off: message traffic is high-churn and **expendable**,
  knowledge is low-churn and **durable**, so separate files keep ephemeral WAL churn out of the
  replicated database and out of the KB's single writer.
- **Four decisions are recorded against the alternatives that were tried and rejected**, because each
  rejection is the useful part: (1) *pull with long-poll, not push* — the transport supports
  server-initiated messages, but a harness only surfaces results of tool calls the model made, so push
  would be correct plumbing that never reaches the model; (2) *full-duplex with per-message correlation,
  not half-duplex turn-taking* — channel-level turn state wedges when a session dies mid-turn; (3)
  *delete the body on acknowledge but retain slim metadata* — deleting the whole record is mutually
  exclusive with request/response, since a later reply would reference a record that no longer exists;
  (4) *prove a shared filesystem by rendezvous, not by comparing a self-reported machine fingerprint* —
  a fingerprint is spoofable, compares **equal** across cloned VM images and **unequal** across a bind
  mount, and, because it would gate reading a path, a forged one turns "read this file" into a
  remote-driven read of an arbitrary local file.
- **No tool-call chunking, by design.** Tool arguments are generated token by token by a model, so
  chunked binary transfer is an economics problem wearing a protocol's clothes: base64 expands a payload
  by 4/3 and tokenizes poorly, so one mebibyte costs on the order of 350–470 thousand output tokens at
  model cost, with corruption detectable only after the tokens are spent. Text messages are atomic; file exchange is deferred to a later MINOR and will prefer
  a same-host filesystem handoff, then a one-time expiring HTTP transfer the agent drives with a shell.
- **Two things the spec settles now because they would be breaking changes later:** message traffic is a
  **side channel into curation** (a session told "this is verified, propose it" authors a proposal
  indistinguishable from first-hand knowledge — the invariant survives literally while the curator's
  signal degrades to hearsay), so provenance marking is a schema decision that lands *with* the feature;
  and channel ownership is keyed on `space_id` **plus the authorizing human**, never the actor alone,
  because actors resolve by display name and collapse across machines and humans.
- **The isolation claim is stated with its limits.** A separate database file isolates the KB's WAL and
  its backup stream; it does **not** isolate the disk, the process, or the readiness signal. COMM.md §5
  turns those into enforced rules — storage budget with a free-space floor, quotas that fail closed
  rather than reusing the fail-open rate bucket, recover-wrapped goroutines, comm deliberately absent
  from `/healthz` (which marks the whole service DOWN on any component failure), its own rate accounting
  so a poll loop cannot starve `kb_*` on a shared per-machine token — and records extraction into a
  separate binary as the escape hatch if that discipline proves leaky.
- **Honest about what it cannot enforce:** COMM gates *who may talk to whom* structurally (a channel
  exists only because a human minted a pairing code and both sessions used it), but it cannot enforce
  *how a receiver treats content*. Instruction text is not a control, and the docs say so rather than
  implying otherwise.

## [1.1.0] — 2026-07-23

First public release.

### Public-release preparation
- **Go module path is now `github.com/Quest-ICT/ken`** (was the internal mirror's path).
  Ken ships as a compiled binary, so this changes nothing for operators — but it is the
  import path every contributor sees, and `-trimpath` bakes it into the binary. The
  `-X` linker symbol in `scripts/build-release.sh` is now **derived** from `go list -m`
  rather than hardcoded: `go build` does *not* error on an unknown `-X` symbol, so a
  stale literal fails **silently** and ships binaries with the wrong version/Source link.
  `build-release.sh` also now refuses a non-public `KEN_SOURCE_URL` unless
  `KEN_ALLOW_ALT_SOURCE=1`, so a stray value can't bake the wrong AGPL §13 link.

### Curation language — convention + live instructions (targets 1.1.0)
- **New live setting `curation_langs`** (Settings → Curation): a comma-separated list of the
  language code(s) the human curator can read (e.g. `fr,zh`; also seedable via
  `KEN_CURATION_LANGS`). Blank = off, so an English-only KB is unchanged. Stored verbatim;
  the normalized primary-subtag set (`en-US`→`en`) is what consumers read.
- **Agents are told to author in the curation language.** When `curation_langs` is set, the
  MCP `initialize` instructions gain a paragraph naming the language(s) and directing the
  agent to write every human-readable field (title/summary/problem/solution/rationale/caveats)
  in one of them — while keeping triggers, code, identifiers and verbatim error text in their
  original form as language-neutral retrieval keys. This is what makes a non-English curator's
  KB reviewable going forward: new proposals land in a language they can promote.
- **MCP instructions are now live.** The server is rebuilt (behind an atomic pointer) when
  `curation_langs` changes, so a settings edit reaches new connections without a restart —
  fixing the prior behavior where the instruction string was fixed at process start.
- **Auto-detected content language + comprehension guardrail.** Migration `0009` adds a
  nullable `entry_version.content_lang`, auto-set by a new `internal/lang` detector (whatlanggo)
  over the PROSE fields at write time — on the **delta** for an enhancement, so a small foreign
  addition to an otherwise-in-language entry is still caught. It feeds two surfaces only (never
  retrieval): the `/proposals` review queue shows a **Language** column and flags out-of-language
  proposals, and a **server-side gate in `store.Promote`/`Repromote`** refuses to promote a version
  whose language isn't a curation language ("can't promote what you can't read"). Fails **open**:
  with the feature off, or an undetected/legacy version (NULL/`und`), nothing is flagged or blocked —
  so turning it on never walls off the existing corpus. `kb_search` results now include a `language`
  field so a polyglot agent can spot and re-author a stranded entry.

### v1.0 readiness hardening (from the pre-1.0 audit — 0 blockers, should-fix items)
- **Write-path error contract:** `kb_save` validates `kind` and each `link_type` and
  returns an actionable message — no more opaque "internal error" (bad kind) or
  silently-dropped link (bad link_type). New regression test.
- **OAuth consent CSP:** the `redirect_uri` host is validated before it is reflected
  into the consent page's `form-action`, so CSP metacharacters can no longer reach the
  header. New regression test; corrected a misleading "can't inject CSP syntax" comment.
- **Entry prose** preserves author line breaks (`white-space: pre-wrap`) instead of
  collapsing multi-paragraph content into a wall of text.
- **Upgrades take a pre-upgrade DB snapshot** (`backups/pre-upgrade-<stamp>.db`, via the
  currently-installed binary, before the new release's migrations run) — a clean rollback point.
- **All web-UI flash messages are now translated** (en + es) via an arg-carrying
  `flashRedirect` helper; nothing user-facing in the UI is hardcoded English anymore.
- **[COMPATIBILITY.md](COMPATIBILITY.md)** documents what SemVer covers at 1.0 (MCP tool
  contract, CLI, `KEN_*` env, token format, forward-only DB schema).
- **Embedding config (`KEN_EMBED_*`) is documented** (usage help + INSTALL.md).
- **Distribution bundle ships `LICENSE` + a generated `THIRD-PARTY-NOTICES`**, and
  `version.SourceURL` is injectable at build time (`KEN_SOURCE_URL` → `-ldflags`).
- **Migration idempotency** regression test; **first-run setup gate fails closed** on an
  empty setup token.
- **Public-repo cutover:** Ken's source is **github.com/Quest-ICT/ken** (AGPL-3.0).
  `version.SourceURL`, the install/download docs, and `ken-upgrade` now use the **public
  GitHub Releases**, so upgrading needs no download token or private registry credentials.
  Removed the now-dead authenticated-download config example.

## [0.6.4] — 2026-07-21

### Added
- **Ken is now licensed under the GNU Affero General Public License v3.0** (`AGPL-3.0-only`; see
  `LICENSE`). Because Ken is a network service, the web UI footer links to the source repository so a
  running instance offers remote users its Corresponding Source, as the AGPL **§13** network-interaction
  clause requires (`version.SourceURL`, shown on every page including the public login and setup screens).
  Added `CONTRIBUTING.md` (Developer Certificate of Origin sign-off; the CLA path for any future
  dual-licensing is noted), an SPDX identifier, and a `footer.source` string (en + es). **Before going
  public, point `version.SourceURL` at the public repository.**

### Changed
- **Favicon is now the compass mark** from the web UI — the azure compass on a dark rounded badge —
  replacing the old amber logo. `ken-logo.svg` is redrawn as a favicon-optimized, self-contained badge (real
  colors, no `currentColor`), and `favicon-32.png` / `favicon-180.png` / `favicon.ico` are regenerated from
  it (librsvg). The favicon `<link>`s are version-busted (`?v=<version>`) so returning browsers pick up the
  new icon.

## [0.6.3] — 2026-07-21

### Fixed
- **Layout shifted sideways between pages.** A tall page (dashboard) shows a vertical scrollbar and a short
  one (search, proposals) does not, so on systems with classic space-taking scrollbars the centered layout
  jumped right by the scrollbar width when navigating between them. Added `scrollbar-gutter: stable` on
  `html` to always reserve the gutter, so the layout stays put across pages.

### Changed
- **Cache-busting for static assets.** `app.css` and `app.js` are now referenced with a `?v=<version>`
  query, so a UI change reaches a returning browser immediately after an upgrade instead of being masked by
  the 1-hour asset cache (`cache-control: max-age=3600`). No hard-refresh needed on future releases.

## [0.6.2] — 2026-07-21

### Fixed
- **Login / auth card was left-aligned instead of centered.** The `.auth-card` sat at the left edge of the
  page container (which spans the full width); `place-items:center` on `.auth-main` only centered the wide
  container, not the card inside it. Added `margin-inline:auto` so the card is centered at every viewport
  width (verified desktop + mobile).
- **Top-bar language selector looked like a form field.** It rendered with a border and a surface
  background — a "white rectangle" in the light theme. The top-bar `.lang .select` is now borderless and
  transparent (just the globe, the language name, and the dropdown caret); the caret is retained. Other
  selects (search scope, browse filters, settings) keep their field styling — only the top-bar language
  control changed.

## [0.6.0] — 2026-07-21

### Added
- **Redesigned, themeable web UI** across every page (login, setup, consent, dashboard,
  search, browse, entry, proposals, tokens, settings): a dark-default + light theme built
  on CSS custom-property tokens, resolved **server-side** from the `ken_theme` cookie so
  there is no first-paint flash. IBM Plex type with a system fallback, inline-SVG compass
  logo and icons, and the "Lineage Rail" version history.
  It makes **zero external requests** — every stylesheet, script, font and icon is
  same-origin — so it is clean under the strict self-only CSP and works fully offline.
  One small `app.js` with delegated `data-*` handlers; no inline scripts or handlers
  (the CSP forbids them). Replaces `ken.js`.
- **Multilingual UI.** Every page renders through the reloadable i18n catalog: English and
  Spanish embedded, with drop-in `messages_<lang>.properties` via `KEN_I18N_DIR` to
  override or add a language at runtime (no rebuild, no restart); resolution falls back
  language → English → key. Count-aware plurals (`TN`, `.one`/`.other`) so it reads
  "1 proposal is waiting", not "1 proposals". Settings field labels, help text and group
  names are translatable too, with the Go registry's English as the fallback.

## [0.6.1] — 2026-07-21

### Fixed
- **Brand logo rendered as literal `<svg …>` text instead of the compass mark.** In `base.html` the logo
  SVG was assigned to a Go template **string** variable and printed with `{{$brand}}`; `html/template`
  auto-escapes string values, so the markup showed as escaped text in the top bar on every page. Fixed by
  inlining the SVG as literal template markup (the same way the consent/setup marks already were).

### Changed
- **Multilingual UI completeness.** Following an audit of the 0.6.0 port, the remaining English-only strings
  on non-English pages are now translated: the promote/reject/revert **confirmation dialogs** on an entry,
  the page **`<meta name="description">`** and the brand/primary-nav **`aria-label`s**, and all
  **controlled-vocabulary domain values** — entry *kind*, version *state*, *staleness*, *lifecycle*,
  activity *event*, browse *sort*, and agent *kind* — now render through a new `Enum` view helper
  (`{{$.Enum "state" .State}}`) backed by `enum.<class>.<value>` catalog keys. English values are identity
  (the UI is unchanged); Spanish is translated; a missing key falls back to the raw value, so no unmapped
  value ever shows as a key. Raw values are still used for CSS hooks, `<option>` values and conditionals.

### Added
- **Redesigned web UI — a themeable, self-contained design system.** Every page (login, setup, consent,
  dashboard, search, browse, entry, proposals, tokens, settings) is rebuilt on a shared visual identity:
  **dark by default with a light theme**, driven by CSS custom-property tokens (`prefers-color-scheme`
  plus a persisted `ken_theme` toggle resolved server-side, so there is no first-paint flash), an IBM Plex
  type pairing (system-stack fallback), an inline-SVG compass logo + icon set, and the **"Lineage Rail"**
  append-only version history. **Zero external requests**: all CSS, JS, fonts and icons are served
  same-origin, so the UI stays clean under the strict self-only Content-Security-Policy and renders fully
  offline. Interactivity (theme + language switch, copy-to-clipboard, confirm-guarded destructive actions,
  password reveal, mobile nav) is one small same-origin `app.js` using **delegated `data-*` handlers** — no
  inline scripts or event attributes, which the CSP forbids.
- **Multilingual web UI (every page).** The UI resolves a per-request language and translates through a
  **reloadable message catalog**. **English and Spanish ship embedded**; drop a
  `messages_<lang>.properties` file into the external i18n dir (`KEN_I18N_DIR`, default `<data-dir>/i18n`)
  to **add a language or override any string at runtime — no recompile, no restart** (picked up within a
  couple of seconds). A missing key falls back to English, then to the key itself. Language is chosen from a
  header **selector** (a `ken_lang` cookie), seeded from `Accept-Language`; the selector auto-lists whatever
  languages are present (each file declares its own endonym via `lang.self_name`). Includes **count-aware
  pluralization** (`.one`/`.other` keys, so a count of 1 reads "1 proposal is waiting", not "1 proposals"),
  and the **settings page's field labels, help text and group names are translatable too** (mirrored in the
  catalog; the Go settings registry stays the English fallback). New package `internal/i18n`. Scope: the
  human web UI only — the AI/MCP surface and the logs stay English. See [docs/I18N.md](docs/I18N.md).

## [0.5.3] — 2026-07-20

### Changed
- `kb_search` now advertises the **`all`** scope in its MCP tool schema
  (`curated | proposals | history | all`). The scope was already accepted; this only makes the tool's
  self-description match its behaviour (and the docs).

### Docs
- Brought the README and docs (DESIGN, INSTALL, MCP-TOOLS, AI-INTEGRATION) up to date with the OAuth
  connector, the Browse page, and server-delivered instructions; capitalized "Ken" in the copy-paste
  prompt blocks. MONITORING / REMOTE-UPGRADE / BACKUP verified already current.

## [0.5.2] — 2026-07-20

### Added
- **The MCP server teaches connected agents how to use Ken.** The server now ships usage instructions (the
  warm-up → search-first → record-outcome → save/enhance loop, and the human-only curation model) in its
  MCP `initialize` response, so an AI that connects over the connector learns the workflow **without a human
  pasting a prompt**. Distilled from `docs/AI-INTEGRATION.md`.

### Changed
- **"Ken" is now capitalized as the product name** across the web UI, log output, and documentation (prose
  references). Identifiers are deliberately unchanged — the `ken` binary/CLI, `KEN_*` env vars, `ken_*`
  cookies/tokens, the Go module path, filesystem paths, and hostnames all stay lowercase.

## [0.5.1] — 2026-07-20

### Fixed
- **OAuth consent "Approve" did nothing in the browser.** The strict `form-action 'self'` CSP also governs
  the redirect that *results from* a form submission (browsers enforce `form-action` on form-submission
  redirects), so approving created the grant server-side but the browser then silently refused to navigate
  to the client's callback (e.g. `claude.ai`). The consent page now widens `form-action` to include the
  **server-validated** client redirect origin. (curl-based end-to-end tests can't catch this — it's
  browser-only CSP enforcement; added a header regression test.)

## [0.5.0] — 2026-07-20

### Added
- **Optional OAuth 2.1 authorization server — connect claude.ai as a custom connector.** With
  `KEN_OAUTH_ENABLED` set, Ken serves OAuth discovery (RFC 8414 + RFC 9728), dynamic client registration
  (RFC 7591), and an authorization-code + refresh-token flow with **mandatory PKCE-S256**, so a claude.ai
  personal-account *custom connector* (whose UI is OAuth-only) can authenticate with the normal **Connect**
  button — no beta request-headers field, no pasted token. A connector you approve gets the standard agent
  capability set (`read`/`write-draft`/`propose`) and **never `curate`**; every write is attributed to a
  *Claude* connector actor, and you revoke it from the Tokens page (*Connected apps*). Security: single-use
  short-lived codes, exact-match redirect URIs, rotating refresh tokens with **reuse-detection that revokes
  the whole grant**, opaque hash-only-stored tokens, and instant grant revocation re-checked on every MCP
  call. **Off by default** — static bearer tokens are unchanged whether or not it is enabled. See
  [docs/OAUTH.md](docs/OAUTH.md). New migration `0008_oauth.sql`.
- **Browse all entries.** A new **Browse** page (top-nav) lists the knowledge base as a grid independent of
  search, with filters (kind, category, staleness, lifecycle), sorting (updated / title / uses / created /
  kind), pagination, a sticky header, and pending-proposal / lifecycle badges. Backed by a single-table
  query over the denormalized `entry` row (no version join).
- **A real home page.** `/` is now a landing dashboard — knowledge-base stats (curated entries, pending
  review, versions, active tokens), a highlighted "pending review" tile plus a **Review N pending
  proposal(s) →** shortcut when the queue is non-empty, and a **Recent activity** feed (curation events in
  the last 30 days). Search moved off the home page and is reached via the **Search** link in the top nav.
- **Search: an `all` scope + single-page results.** The Scope selector gains **all**, which drops the
  per-state filter so a query reaches every version in every state (curated + proposed + history) — one
  entry may appear once per matching version, the deliberate "show me everything" view. Results now render
  on the **same** page as the search form (the form and scope stay put, pre-filled), so refining a search
  no longer needs the old "← new search" round-trip.
- **Back-to-top button on every page.** A floating control appears bottom-right once a
  page is scrolled past ~300px and smooth-scrolls to the top on click. Implemented CSP-clean (external
  `/static/ken.js`, no inline handler), honours `prefers-reduced-motion`, and is inert without JS.

## [0.4.2] — 2026-07-20

### Added
- **Curated-head recovery for mis-ordered promotions.** A **"Revert to this"** action on the entry page's
  history rows re-promotes a superseded/rejected version back to the curated head (`store.Repromote`,
  `POST /entry/{slug}/revert/{vid}`), and the Promote button now shows a **downgrade confirm** when the
  pending revision is older than the current head. Promoting stacked proposals in the wrong order (newest
  first) could silently regress an entry to older content with no way back via the UI; now it warns, and is
  recoverable.

### Fixed
- The proposal **Reject** and token **Revoke** confirm dialogs never fired — inline `onsubmit` handlers are
  blocked by the strict `default-src 'self'` CSP, so the forms submitted immediately. Replaced with a
  CSP-clean `data-confirm` attribute handled by one delegated listener in `/static/ken.js`.

## [0.4.1] — 2026-07-20

### Added
- **Scoped remote-upgrade tooling** (`scripts/ken-upgrade`, `deploy/ken-upgrade.sudoers`,
  `docs/REMOTE-UPGRADE.md`) — a root-owned wrapper that lets an
  unprivileged deploy user run *only* the Ken installer via one `NOPASSWD` sudoers rule (no full
  root, no editing `/opt/ken`). It fetches the installer from the release host, **checksum-verifies**
  it, keeps any download credential in a root-only `curl -K` config (never on argv), and **validates
  every argument against a strict allowlist** (`--version`, `--tls acme|off`, `--domain`, `--email`,
  `--port`, `--open-firewall`/`--no-firewall`, `--dry-run`) — refusing path/prefix/user flags that
  could escalate.

### Changed
- **Review UI consolidated onto the entry page** — the proposal queue's separate `Review →` page is
  gone; the promote/reject actions now live on `/entry/{slug}` itself. For a pending **enhancement**
  the entry shows the curated head, then a highlighted *Pending proposal* panel with the proposed
  content **full-width** (replacing the cramped side-by-side diff); for a **brand-new draft** the entry
  body already *is* the proposal. The queue drops its Review column — click an entry to review it.

### Docs
- `docs/INSTALL.md` — document the always-latest download links
  (`…/releases/latest/download/ken-latest-linux-<arch>.bin`), enabled by the
  stable-named release aliases added in 0.4.0.

## [0.4.0] — 2026-07-19

### Added
- **Health & metrics** (`internal/metrics`, `internal/health`) — a small, dependency-free
  observability surface modeled on the usual Actuator/Micrometer status-class buckets, but hand-rolled
  so the footprint stays flat (no `client_golang`; kilobytes of RAM; nothing written to disk):
  - `GET /health` — readiness JSON `{"status":"UP","components":{db,storage}}` (public; per-component
    details only for loopback/token/CIDR callers; **503** when a component is DOWN). `GET /healthz`
    stays the plain liveness probe.
  - `GET /metrics` — Prometheus text exposition: web request counts/latency by outcome, MCP
    `kb_*` tool calls, knowledge-base gauges (entries, versions, **proposals pending**, embedding
    coverage, users, active tokens), rate-limit rejects/blocks, auth failures, and process/DB-pool
    stats. **Gated**: loopback + `KEN_METRICS_CIDRS` always allowed, any other scraper needs
    `KEN_METRICS_TOKEN` (constant-time bearer, authorized on the direct peer — never `X-Forwarded-For`).
    On by default (loopback-only); `KEN_METRICS=off` removes it. The gate resolves the client IP
    through the trusted-proxy machinery, so a co-located reverse proxy can't make every scraper look
    local. Only `/healthz` is exempt from the rate limiter; `/health` and `/metrics` are throttleable
    (loopback / allow-CIDR callers bypass it anyway).
  - A drop-in [`monitoring/`](monitoring/) bundle: Grafana dashboard, Prometheus alert rules
    (incl. a `KenProposalsBacklog` curation nudge), and a scrape/metric-reference README; operator
    guide in [`docs/MONITORING.md`](docs/MONITORING.md).

### Docs
- `docs/AI-INTEGRATION.md` — add a third, **recommended-for-capable-agents** integration prompt: a
  **hybrid** that leads with the three load-bearing ideas (sole author, append-only, human-only promotion)
  then gives the §3 terse mechanics, plus a chooser positioning all three prompts (terse / why / hybrid).

## [0.3.2] — 2026-07-19

### Added
- **Favicon & brand logo** — the web UI ships Ken's compass/quill mark: an SVG favicon with PNG (32px and a
  180px apple-touch icon) and a multi-size `.ico` fallback, and the logo now sits in the header brand.
  Assets are committed and served from the embedded `/static/` tree, so the release build needs no image
  tooling.
- **Running version in the page footer** — every page shows `ken v<version>` in the footer (from
  `internal/version`, injected at release build), so it's obvious which build is live.

### Changed
- **Token page** (`/tokens`) — the one-time-secret reveal is now readable in **dark mode** (it used the
  theme foreground on a fixed light-green box, so text went near-white-on-pale) and the registration
  example is **copy-paste-ready**: it carries the deployment's **real host** (derived from the request —
  `Host`, or a trusted proxy's validated `X-Forwarded-Host`, with an `https` scheme when TLS terminates
  here), single-quoted so an IPv6-literal host can't trip shell globbing, instead of a `<ken-host>`
  placeholder. The token and the command each get a one-click **Copy** button — a small same-origin
  `/static/ken.js`, so the strict `default-src 'self'` CSP is unchanged — with an `aria-live` announcement
  for screen readers. The host is run through an allowlist sanitizer before it reaches the shown command.

### Docs
- `docs/AI-INTEGRATION.md` — a verified **connector reach/restart matrix**: adding Ken once as a claude.ai
  Connector needs no app restart and reaches Chat, Cowork and Claude Code (each with its own per-surface
  activation step) but **not** Design; plus the reminder that the connector must carry the
  `Authorization: Bearer` token or it 401s.

## [0.3.1] — 2026-07-19

### Docs
- `docs/AI-INTEGRATION.md` — how to connect Ken across a fleet: **token strategy** with two routes (Route A:
  one token added as a claude.ai Connector via its beta *Request headers* field, then inherited by Claude
  Code everywhere; Route B: `claude mcp add --header` per machine, one token each), when to pick which, how
  to **switch Route B → Route A** later, that "Claude Code in the desktop app" is Claude Code (not the chat
  connector), a **"set up once, not per session"** clarification, and a copy-paste **recipe to harvest an
  existing session's knowledge into Ken**.

## [0.3.0] — 2026-07-19

### Added
- **Rate limiting** (`internal/ratelimit`): a per-IP token-bucket guard as the outermost
  handler — loopback + `KEN_RATELIMIT_ALLOW_CIDRS` + `/healthz` are exempt — returning
  `429` + `Retry-After` over the limit and auto-blocking repeat offenders with `403` for a
  lockout window, plus a per-token limit on MCP (keyed by token id). On by default; tunable
  via `KEN_RATELIMIT*` (see `ken --help`). Client-IP resolution (trusted-proxy aware) was
  extracted to `internal/clientip` and is now shared with the login guard. IP-keyed limits
  and lockouts key IPv6 clients by their `/64` so address rotation can't evade them.
- **Agent-token management in the web UI** (`/tokens`, superadmin): issue, list and revoke agent
  MCP tokens from the browser — scopes limited to read/write-draft/propose (never curate); the
  secret is shown once. The same credentials as `ken token add`.
- **Editable runtime settings in the web UI** (`/settings`, superadmin, `internal/settings`): retune
  the rate limits, login brute-force lockout, session TTL, trusted-proxy CIDRs and ACME domains.
  Changes are validated, persisted to `app_setting` (overriding env/defaults; a value equal to the
  default removes the override) and applied **live via an atomically-swapped snapshot — no restart**.
  ACME domains are additive-live (a settings edit can't remove the boot domain). TLS mode/cert remain
  config/restart-managed (a listener rebind can't be done live).
- **`KEN_TLS_QUIET_HANDSHAKE`** — drop the benign "TLS handshake error" scanner noise (no-SNI probes,
  EOF/reset, ALPN/cipher fingerprinting) from the log via a filtered `http.Server` ErrorLog, so real
  errors stand out. Off by default; the public `:443` gets this background scanning constantly.

### Changed
- The installer wizard now defaults HTTPS mode to **acme** (Let's Encrypt) instead of
  `off`, so a standalone install is HTTPS-first. An upgrade still inherits the installed
  unit's mode, and unattended `-y` without `--tls` stays plain HTTP (acme needs a domain).
- Secure/`__Host-` cookies are no longer inferred from `KEN_TRUSTED_PROXIES` (trusting a
  proxy's `X-Forwarded-For` does not mean it terminates TLS). Behind a TLS-terminating proxy,
  set `--secure-cookies` or `KEN_SECURE_COOKIES=on` explicitly. In-process TLS still forces it.

### Fixed
- The bounded-map idle sweep in the rate limiter and the login brute-force guard was
  unreachable (off-by-one), so the maps could fill and then silently fail open / stop locking;
  the sweep now runs and reclaims idle entries. Auto-block now counts *consecutive* over-limit
  rejections (reset on an allowed request), so a busy shared/CGNAT IP isn't blocked for bursts.

## [0.2.0] — 2026-07-19

### Added
- **In-process TLS / HTTPS** (`internal/webtls`). `KEN_TLS=acme` terminates TLS with
  Let's Encrypt (automatic issuance + renewal via ACME, HTTP-01/TLS-ALPN-01);
  `KEN_TLS=file` serves an operator-supplied PEM cert/key (hot-reloaded on renewal).
  Both run a `:80` listener that answers the ACME HTTP-01 challenge and 301-redirects
  everything else to HTTPS. Installer flags `--tls/--domain/--email/--cert/--key`
  (defaults the port to 443 and offers to open 80+443); the systemd unit and
  `docs/INSTALL.md` gained a standalone-HTTPS walkthrough. Secure/`__Host-` cookies
  are forced whenever TLS is on (or `KEN_TRUSTED_PROXIES` marks a TLS-terminating
  proxy in front of ken), and `KEN_DEV_TOKEN` is refused in any TLS posture. The
  `:80` redirect is host-checked against the domain allowlist (no open redirect),
  and file-mode certs hot-reload on any change; hardened per an adversarial review.

### Changed
- Plain HTTP (`KEN_TLS=off`, still the default) is now explicitly the
  *behind-a-reverse-proxy* option; standalone deployments should use `KEN_TLS=acme`.

### Docs
- `docs/AI-INTEGRATION.md` — the standard copy-paste prompt for integrating Ken into an AI agent's
  workflow (Claude Code or any streamable-HTTP MCP client): token + MCP-registration steps, a tight
  drop-in prompt and a longer explanatory variant, and per-project tuning notes. Linked from the README.

## [0.1.0] — 2026-07-19

First release: Ken is an AI-first personal knowledge base — a Go single binary that is
both a remote-MCP server (an AI coding agent stores and retrieves *curated* knowledge)
and a human curator web UI. SQLite is the single source of truth; enhancements are
append-only and the curated head moves only on human promotion.

### Added
- **MCP server** (streamable HTTP) with scoped bearer-token auth and 8 tools:
  `kb_search`, `kb_get`, `kb_save`, `kb_propose_enhancement`, `kb_flag_stale`,
  `kb_diff`, `kb_record_outcome`, `kb_recent_context`. A `kb_search`-issued dedup token
  structurally enforces search-before-save.
- **Storage & versioning** — embedded SQLite (WAL, pure-Go `ncruces/go-sqlite3`,
  FTS5 via `ext/fts5`); append-only model (`entry` + immutable `entry_version` +
  `curation_event` reflog); curated head advances only by human promotion.
- **Search** — keyword-first hybrid: FTS5 BM25 (prose) + trigram FTS (code), fused by
  Reciprocal Rank Fusion.
- **Semantic embeddings (optional, off by default)** — pluggable `internal/embed` SPI
  (HTTP OpenAI-compatible + offline hash provider); brute-force Go cosine over a plain
  `entry_embedding` BLOB table, RRF-fused into search; multiple models can coexist per
  version. `ken embed backfill|status`; enabled via `KEN_EMBED_*`.
- **Human curator web UI** — Argon2id (High) login, server-side sessions, per-session
  CSRF, login brute-force lockout; search/browse, entry + version history, and the
  proposal review queue (diff + promote/reject). The human curates; never authors.
- **First-run setup wizard** — mode derived from "no human users yet"; the setup page is
  gated by a one-time setup token logged at startup.
- **CLI** — `serve`, `token add|list|revoke`, `user add|list`, `backup snapshot|verify`,
  `import --dir` (flat-memory importer), `embed backfill|status`.
- **Backup & restore** — Litestream config (continuous) + nightly age-encrypted
  `VACUUM INTO` snapshots (`scripts/ken-snapshot.sh`); `backup verify` runs
  `integrity_check` + `foreign_key_check` + FTS5 integrity-check on both indexes + a
  MATCH canary + embedding vector-length parity. Runbook in `docs/BACKUP.md`.
- **Deployment** — self-extracting `.bin` installer (idempotent `install.sh` with
  SELinux relabel and opt-in firewall), `scripts/ken.sh` launcher, systemd units
  (`ken.service`, `ken-snapshot.service`/`.timer`), and an NSIS template for Windows.

### Security
- SHA-256 lookup for high-entropy bearer tokens (not Argon2); Argon2id (High profile,
  BouncyCastle-free, `golang.org/x/crypto`) for human passwords.
- Request-body caps (`MaxBytesReader`) on MCP and web; HTTP server read/idle timeouts
  (no write timeout, for SSE); trusted-proxy client-IP resolution (`KEN_TRUSTED_PROXIES`).
- Security headers (HSTS, nosniff, `SAMEORIGIN`, strict self-only CSP).
- `.gitignore` shields runtime secrets (`dedup.key`, `*.key`, `*.age`) regardless of path.

### Not yet implemented (planned)
- In-process TLS / ACME — Ken serves plain HTTP; terminate TLS at a reverse proxy
  (pass `--secure-cookies`).
- A general per-IP / per-token rate limiter — only a login brute-force lockout exists.
- git/Markdown mirror (deferred by design decision D5), local ONNX embedder + background
  re-embed job, and the `kb_link`/`kb_related` graph tools.

### Notes
- Built via multi-agent workflows and hardened before release: three adversarial reviews
  plus a ~60-agent full audit (0 critical/high) fixed every medium and every unambiguous
  low, each with a regression test. All documentation was reconciled against the code
  prior to this release.
- Windows installer: the NSIS `.exe` is not attached to this release (built separately
  where `makensis` is available); the Linux self-extracting `.bin` installers are.

[Unreleased]: https://github.com/Quest-ICT/ken/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.1.0
