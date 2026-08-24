# The credential-control register

> **Companion to [IDENTITY.md](IDENTITY.md), which is the design. This is the evidence under it.**
> Ken's identity and access layer is being replaced. Before writing the replacement, every
> credential control in the current system was extracted and asked one question: *does single
> user genuinely dissolve this, or does it only look that way?*
>
> **STATUS: FINDINGS, NOT A PLAN.** Nothing here schedules work. Several entries describe
> defects in the code as it stands today; those are findings, and they are called out in
> IDENTITY.md §9.6 rather than left buried in a table.

## How to read it, and how far to trust it

**164 controls**, across five lenses, read out of the code and the documents that justify
it. Then filtered: **every "dissolves" verdict was handed to an adversary whose only job was to
refute it**, instructed to default to "survives" when uncertain. That filter did real work.
Twenty-three controls were claimed to dissolve; **three survived refutation as free deletions**,
and the other twenty came back carrying a condition — several because the refuter ran the
mutation instead of arguing about it, and the tests went red.

| verdict | count | what it means |
|---|---:|---|
| survives | 95 | single user changes nothing about why this exists |
| survives, on a different key | 46 | still needed, but the axis it is keyed on moves |
| dissolves **only under a condition** | 20 | claimed as a free deletion; refuted. Safe to remove **only** once the named condition holds |
| **dissolves** | 3 | protected one party from another, and no second party can exist |

**The ratio is the headline, and it is not what I expected going in.** `IDENTITY.md` §7 read as a
long list of things the replacement deletes. Exactly **3** controls dissolve
outright, and all three are the same thing in different clothes: `space_id`, a tenancy seam
nothing can populate. The rest either survive untouched, survive with their key moved, or —
most importantly — **dissolve only once something else is true**. The binding-voucher chain
really is the largest safe deletion available, but only because one identity comes to span both
surfaces. Remove it without that and the hole it closes reopens, silently.

**Read the "if removed" line first.** *"Nothing would break and nobody would notice"* appears
repeatedly and means opposite things in different rows: sometimes the control is already dead,
sometimes its failure mode is simply invisible. The second kind is the most dangerous thing a
control can be, and telling them apart is the whole point of this document.


---

## API tokens and scopes

*36 controls — 21 survive, 12 survive on a different key, 3 dissolve conditionally, 0 dissolve outright.*

### `station-locker` — a reserved scope that was DE-enforced: the locker is now gated on `station` alone, and the constant is kept so old keys still parse

- **Where:** `internal/stationserver/stationserver.go:792-811 (requireLocker)`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Nothing, now — and the comment explains that withholding it prevented less than it cost.
- **Stated reason:** "It shipped as a separate withholdable scope, on the reasoning that a key which only keeps notes and tasks should not also carry files. In practice that produced a station whose capabilities depended on which key a session happened to be handed — so 'does this station have a locker' had no answer, only 'does this key'. A session discovering the locker missing cannot tell an intentionally restricted key from a misconfigured one" (internal/stationserver/stationserver.go:795-802). And: "ScopeStationLocker is NOT removed from the vocabulary: existing keys carry it, and COMPATIBILITY.md reserves `station` and `station-locker` together precisely so they can be merged later — 'splitting a shipped scope is a MAJOR, merging two is free'. This is that merge." (:804-807)
- **If removed:** This is the precedent the new design should read first: a capability withheld per-credential produced a state that no session could interpret ("restricted, or misconfigured?") — exactly the fails-indistinguishably defect, arrived at by adding a control rather than by removing one. It was resolved by MERGING, which the reserve-early-merge-free rule made cheap. Removing the constant now would break parsing of already-issued keys' scope lists, so the vestige is deliberate.

### The `space_id` tenancy seam — carried on every table, always 1, never written otherwise

- **Where:** `migrations/0001_init.sql:34-44; internal/stationserver/auth.go:129 (`SpaceID: 1` hardcoded); internal/commserver/auth.go:145 (derived from actor.space_id)`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Nothing today. It is scaffolding for a multi-party future that the settled facts now rule out.
- **Stated reason:** "Identity & tenancy seams — built now (cheap columns), isolation DEFERRED. Everything is space_id=1 until a second party exists (DESIGN §7). Carrying these columns from day 1 makes the collaborative future additive, not a rewrite." (migrations/0001_init.sql:34-37)
- **If removed:** This is the one item in the lens that dissolves cleanly and completely: no code inserts into `space`, no path sets space_id to anything but 1, and it protects one party from another — the exact class the brief says has nothing left to protect. Removing it would break nothing and no one would notice, which here is the harmless reading. Two things to note while removing: (a) the station surface hardcodes `SpaceID: 1` while the comm surface reads it from the actor row, so the two already disagree in principle and agree only by accident; (b) migrations/0014_voucher_holder.sql:25-27 records `issued_in_space` deliberately unenforced "so the check can tighten to (actor, space) without a second migration" — that tightening will never happen and the column can go with the seam.

### Isolation between stations is stated as per-credential, not structural, and the limits are written down rather than implied

- **Where:** `docs/STATIONS.md:542-560; docs/STATIONS.md:620-632`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A false sense of a boundary. It prevents a designer or an operator assuming Ken enforces something it cannot.
- **Stated reason:** "'Private to the station' is per-credential, not structural. A session holding two station keys bridges them and the server cannot see it. The boundary is the human's config file." (docs/STATIONS.md:554-556). "The human reads everything, always. 'Private' means station-to-station, never AI-to-human." (:557-559). And S11: "Ken cannot enforce this and must not imply it can. It cannot inspect a blob and know it is a secret. The tool description states the rule, the console shows what is stored, and the human can look. That is a documented expectation, not a control." (docs/STATIONS.md:642-645)
- **If removed:** Station-to-station isolation is the closest thing in Ken to tenancy, and under one human it protects one of the human's own workspaces from another of the human's own sessions. It is already honest about being only a config-file boundary. What must NOT be carried over as if it were security: the new model's "one approval per pair of workspaces that want to talk" should be justified as intent-declaration and blast-radius control, not as isolation — because the human holding both configs can bridge them at will, and the document already says so.

### CheckScopeMix — the three-family rule: knowledge-base scopes may not be combined with comm scopes or with station scopes; station+comm is the one permitted pair

- **Where:** `internal/store/scopes.go:31-65`
- **Verdict:** survives, on a different key
- **Prevents:** One credential that can both receive inter-session messages and write to the knowledge base. That combination breaks the hearsay marker structurally (a message-derived claim would be authored by the same token that received it) and widens a leaked everyday token from "reads knowledge" to "reaches into the sessions on my machines".
- **Stated reason:** "This is what makes the design's claim true rather than aspirational — 'a knowledge-base token cannot send messages and a comm token cannot write knowledge'. Without it an operator could quietly widen their everyday agent token, and since API tokens have no expiry (only revocation), every already-copied instance of that token would gain the new capability retroactively." (internal/store/scopes.go:32-37). And on the third family: "a session legitimately staffs a post and talks from it, while a token that can both read working notes and write knowledge is the mixing this function exists to prevent" (:42-44).
- **If removed:** It protects the human from their own sessions' blast radius, not one tenant from another, so it survives in principle. But it is load-bearing for something else that would break loudly-and-then-silently: comm/provenance.go:26-31 states the hearsay window "keys on the ACTOR, not the token, and that is forced rather than chosen: a COMM token must be DEDICATED ... Keying on the token would make this function always return false." So the dedication rule and the actor-keyed marker are one mechanism in two files. If the new model gives a workspace ONE credential covering everything (which is where single-user + one-approval-per-folder naturally leads), the hearsay marker as built stops meaning anything — it would be asking "did this actor receive traffic" where the answer is always yes for any workspace that talks. That must be re-derived, not ported.

### CheckScopeMix's default branch: any scope that is not in CommScopes or StationScopes is classified as knowledge-base

- **Where:** `internal/store/scopes.go:49-58`
- **Verdict:** survives, on a different key
- **Prevents:** A newly added scope silently escaping the mixing rule. It fails toward the strict side — an unknown scope is treated as KB, so it cannot be combined with comm or station.
- **Stated reason:** "THREE families, not two. An earlier shape bucketed everything that was not comm as knowledge-base, so the moment `station` became a valid scope it would have been treated as a KB scope: `read,write-draft,propose,station` would have minted silently while `comm,station` was refused — exactly backwards" (internal/store/scopes.go:39-44).
- **If removed:** The bug it commemorates is the shape to carry forward, not the code: a family-classification written as "everything else" silently mis-files the next member. If the new design keeps any capability grouping at all, the classification must be exhaustive and fail on an unrecognised member rather than defaulting. Removal today would be caught by internal/store/scopes_test.go, which is one of the few controls in this lens with a test.

### Station scopes are not mintable by `ken token add` or by the console token form — they are refused at mint with the corrective command

- **Where:** `cmd/ken/cli_token.go:52-66; internal/web/app.go:840-843`
- **Verdict:** survives, on a different key
- **Prevents:** An unbound `ken_` token carrying `station` scopes: it would authenticate on no surface (/station requires a `kens_` key with a station binding) while looking exactly like a working credential.
- **Stated reason:** "Refuse to mint a station credential HERE, rather than let it fail at first use. This command issues a `ken_` token with no station binding, and /station/mcp requires a `kens_` key bound to one — so the token would authenticate nowhere while looking exactly like a working one. Fail at mint time, where the operator is still holding the command that can be corrected." (cmd/ken/cli_token.go:52-56)
- **If removed:** Nothing breaks server-side; the damage is an operator holding a dead credential and a debugging session. The generalisable rule for the new design: a credential whose shape cannot satisfy any surface must be refused at ISSUE, because the failure at use is indistinguishable from a network problem, a wrong URL, or a revoked token.

### `comm-file` — a reserved scope that became a real second check: required by the byte-relay HTTP surface and re-checked per file tool on top of the transport's `comm` requirement

- **Where:** `internal/commserver/commserver.go:955-963; internal/commserver/files.go:58`
- **Verdict:** survives, on a different key
- **Prevents:** A comm token that was minted to exchange messages also being able to move file bytes through Ken. Two independent checks, because the transport only requires `comm`.
- **Stated reason:** "requireFileScope gates the file tools on comm-file. The transport middleware only required `comm`, so this is the second, per-tool half of the check — and it is what makes the reserved scope real rather than vocabulary." (internal/commserver/commserver.go:953-956)
- **If removed:** Note a live documentation defect in this lens: internal/commserver/auth.go:22-23 still says "ScopeCommFile is RESERVED and required by nothing yet: file exchange is deferred to a later MINOR" — that is false as of the two enforcement sites above. A reader auditing the scope model from the const block would conclude comm-file is inert and could remove the checks as dead code with the comment as justification. Fix the comment in the same change as any credential rework.

### Declare scopes early, merge later — the vocabulary is deliberately larger than what is enforced

- **Where:** `internal/store/scopes.go:5-29`
- **Verdict:** survives, on a different key
- **Prevents:** A future capability split forcing a MAJOR release, and issued credentials being invalidated by vocabulary churn.
- **Stated reason:** "A scope is a coarse capability, not a fine one, and the vocabulary is deliberately larger than what any tool checks today: `comm-file` and `station-locker` are RESERVED. Splitting a shipped scope into two later is a MAJOR change; merging two is free — so the cheap direction is to declare early and merge if it turns out one was enough." (internal/store/scopes.go:13-16). Restated in COMPATIBILITY.md:84-90.
- **If removed:** The rule is about compatibility, not about a single user, so it survives — but note its cost record: of four reserved scopes, one (`comm-file`) graduated to real enforcement, one (`station-locker`) was merged away after producing an uninterpretable state, one (`curate`) is the entire trust model precisely BECAUSE it is never enforced, and `station` is enforced. The rule paid for itself. Its hazard is item 1's hazard: a declared-but-unenforced scope sitting in issued credentials is a capability waiting to switch on.

### A station key whose `station_id` is NULL may call exactly one tool, `station_request`

- **Where:** `internal/stationserver/auth.go:60-72 (requireStation); internal/stationserver/stationserver.go:432-450 (the only handler that does not call it)`
- **Verdict:** survives, on a different key
- **Prevents:** A bootstrap credential — the one a session gets before any station exists — reading or writing any station's notebook, tasks, locker or vault.
- **Stated reason:** "StationID is empty for a station-less key. Such a key may call exactly one tool, station_request, which is how a session with no station asks for one (S3)." (internal/stationserver/auth.go:47-49). And the capability-withholding principle it serves: "The capability the whole design rests on is WITHHELD rather than requested: no function here lets an agent create, name, publish, rename or archive a station. An agent may only file a request; a human decides and types the name (S3)." (internal/store/stations.go:22-24)
- **If removed:** The settled facts say a new folder needs NO approval and comes up auto-named and fully working — which deletes this control's whole reason for existing, since there is no longer a credential that predates its workspace. What must survive is the withholding it implements: the agent files a request, the human names the thing. If auto-naming replaces the human naming step, then the property "an agent cannot squat a namespace" (migrations/0012_stations.sql:22-24: "`name` is display and is never an address, or the first release ships a namespace an agent can squat") has to be re-established by the id/name split rather than by the approval. The enforcement today is by omission — one handler out of ~25 skips requireStation — and nothing structurally prevents a new tool from being added without the guard.

### Per-token rate limiting, keyed on token id, with a SEPARATE bucket per surface

- **Where:** `internal/mcpserver/auth.go:101-112; internal/commserver/auth.go:100-113; internal/stationserver/auth.go:102-108`
- **Verdict:** survives, on a different key
- **Prevents:** One machine's poll loop starving that same machine's knowledge-base calls, and (past the per-IP strike threshold) locking the machine out entirely; plus ordinary abuse from outside.
- **Stated reason:** "Its own rate accounting, separate from the knowledge base's bucket: the operating convention is one token per machine, so a comm poll loop sharing the KB's budget could starve that machine's kb_* calls (and, past the per-IP strike threshold, lock the machine out entirely)." (internal/commserver/auth.go:100-103). And on keying: "Per-token rate limit (keyed by token id, so it survives an IP change)." (internal/mcpserver/auth.go:102)
- **If removed:** Protects Ken from the outside world and from a runaway local loop, so it survives. But it is keyed on the credential, and the settled design collapses many credentials into one OAuth identity per instance — at which point every session on every machine shares one bucket and the starvation this control was built to prevent comes back in a worse form (one folder's poll loop starves every other folder). If identity travels as the workspace's opaque id, the rate-limit key should be the WORKSPACE, not the credential; otherwise this control silently inverts.

### The actor model: one actor row per (kind, display_name), atomically get-or-created, with tokens hanging off the actor

- **Where:** `internal/store/tokens.go:30-43; migrations/0001_init.sql:48-56`
- **Verdict:** survives, on a different key
- **Prevents:** Duplicate identities under concurrent mint (a unique index plus ON CONFLICT DO NOTHING, not SELECT-then-INSERT), and gives the several tokens one machine holds a shared identity to be correlated by.
- **Stated reason:** "Atomic get-or-create against the unique (kind, display_name) index — no SELECT-then-INSERT race." (internal/store/tokens.go:33-34). "An actor is any writer: the human curator (kind='human') or an AI (kind='ai')." (migrations/0001_init.sql:46)
- **If removed:** The actor is the join key that several controls quietly depend on — the hearsay window (comm/provenance.go), the station-key/comm-token match (S5), the OAuth connector's write identity (web/oauth_web.go:133-152), the ListStationKeys `ActorHasComm` badge. It is not a tenancy construct and does not dissolve; but note its documented weakness: "Actors resolve by display name and therefore collapse across machines, which would be wrong for an ownership check — but here over-matching is the SAFE direction" (comm/provenance.go:33-35). In a workspace model the actor's job is arguably taken over by the workspace id, and every one of those four dependents has to be re-pointed deliberately, not by search-and-replace.

### The station key must be minted under the SAME actor as that machine's comm token — declared as a requirement, NOT enforced at mint, enforced only at binding

- **Where:** `internal/store/stations.go:309-336 (IssueStationKey doc); internal/store/station_binding.go (RedeemBindingVoucher, the actual check)`
- **Verdict:** survives, on a different key
- **Prevents:** Nothing at mint time. What it is FOR is the hearsay marker: the window is keyed on the actor, so a mismatched key marks nothing and a message-derived proposal reaches the curator looking first-hand.
- **Stated reason:** "actorID must be the SAME actor as that machine's comm token, because the hearsay window is keyed on the actor — a different actor silently defeats prompted_by_peer_traffic, and a marker that fails open without saying so is worse than no marker (S5). THIS FUNCTION STILL DOES NOT ENFORCE THAT. It records what it is told. An earlier version of this comment said 'the caller enforces that', naming an enforcer that never existed — which cost a production operator real time, because a contract comment asserting a guarantee is worse than silence: it stops the reader looking." (internal/store/stations.go:313-321). Why refusal at mint was rejected: "refusing at mint time would block the legitimate case of a deployment that has no comm token yet, and stations run with COMM off by design." (:326-328)
- **If removed:** The mitigation the code claims does not exist on the console path. IssueStationKey:330-333 says "the console offers a picker that marks them", and docs/STATIONS.md:189-191 repeats it — but internal/web/stations.go:493-501 mints with `sess.ActorID` unconditionally, and the form (internal/web/templates/stations.html:332-343) posts only a label. `store.ActorsWithCommStatus` (the picker's data source) is called from cmd/ken/cli_station.go:178,212 and internal/web/oauth_web.go:89 — never from the station-key handler. So the CONSOLE-first path, which the house rule prefers, mints every station key under a `human` actor, which is precisely the permanently-false-marker case S5:193-196 documents as already having shipped once. It is not fully invisible — ListStationKeys carries `ActorHasComm` and stations.html:310 renders a "no comm token" badge after the fact — but the default is wrong and three documents say it is right.

### Key-per-machine as the unit of revocation, with the secret shown once and mint instructions that say why

- **Where:** `cmd/ken/cli_station.go:96-103; docs/STATIONS.md:180-182`
- **Verdict:** survives, on a different key
- **Prevents:** Revocation being all-or-nothing. Copying one key to three machines means a leak on any of them costs all three.
- **Stated reason:** "Several named keys per station ('laptop', 'vps'), each independently revocable — key-per-machine is what makes targeted revocation mean anything, and it is why §1 says to mint rather than copy." (docs/STATIONS.md:180-182). CLI echoes it at issue: "Mint a SEPARATE key per machine: revocation is per key, which is what makes it targeted." (cmd/ken/cli_station.go:103)
- **If removed:** Single-user does not shrink the machine count; the settled model has many sessions across folders and machines. But if identity moves to one OAuth grant per instance, revocation granularity collapses to the whole instance — "revoke this laptop" stops existing. That is a real capability loss hiding inside a simplification, and it should be a stated trade-off in the new design rather than a discovery after a laptop is lost.

### `kb_flag_stale` is gated on `propose`, not on `curate` — an agent-side write that changes how a CURATED entry is presented

- **Where:** `internal/mcpserver/server.go:581-585`
- **Verdict:** survives, on a different key
- **Prevents:** It is a deliberate NON-withholding: agents are trusted to mark an entry as no longer matching reality, because a stale entry that nobody can flag is worse than a wrongly-flagged one.
- **Stated reason:** none stated at the call site (the tool simply requires scopePropose). The consequence is recorded in the audit: "An agent can flag an entry stale and no human surface can clear it. No route and no CLI sets `staleness`; the only path back to `fresh` is the side effect of a promotion or a revert... for a single-version entry there is nothing to revert to, so clearing a wrong flag requires authoring a new version." (docs/audits/batch2-stations-kb.md:82)
- **If removed:** Worth surfacing in this lens because it is the one agent capability that reaches the curated head's presentation without the curate scope — a one-way door with no human undo. Under single-user it still survives (it is agent-vs-human, not tenant-vs-tenant), but the missing inverse is a real gap: the reserved vocabulary for the fix already exists (`'reverified'`, migrations/0001_init.sql:206) and the action was never built. If the new layer defines capabilities from scratch, "clear a stale flag" is a curate-family capability that should exist on day one.

### The KEN_DEV_TOKEN bypass — a static credential granting read/write-draft/propose, with an empty token id

- **Where:** `internal/mcpserver/auth.go:185-187`
- **Verdict:** survives, on a different key
- **Prevents:** Nothing; it is a development affordance. The controls AROUND it are the point: it never grants curate, it is excluded from the comm and station surfaces, and its empty token id means it is skipped by rate limiting (auth.go:101) and by last_used_at (auth.go:113).
- **Stated reason:** "The dev-token bypass is excluded because it is a single static credential with an empty token id, which also means it bypasses per-token rate accounting — any quota keyed on a token id would be unenforceable for it." (internal/commserver/auth.go:73-75)
- **If removed:** Deleting it entirely is the cleanest outcome of a rewrite and would be noticed immediately by anyone running the dev loop, which makes it the safe kind of removal. Its cautionary content is the second-order observation in the quote: a credential with no id silently opts out of every control KEYED on an id. In the new model, any identity that can be absent or blank (an unapproved folder, a pre-registration session) will opt out of rate limiting and use-tracking the same way, unless those are keyed on something that always exists.

### scopeCurate is declared in the scope vocabulary and required by NO tool on any surface — the curation gate proper

- **Where:** `internal/mcpserver/auth.go:26-35`
- **Verdict:** survives
- **Prevents:** A token holder — any session, any connector, any leaked credential — advancing the curated head of the knowledge base. The concrete failure it stops is the base becoming self-asserting: an agent proposes, promotes its own proposal, and the next agent reads it as human-verified knowledge with no human ever having read the text.
- **Stated reason:** "scopeCurate is intentionally not required by any MCP tool: moving the curated head is a human-only act performed in the web UI, never over MCP. It remains a mintable token scope (a curator-review UI or a future MCP curation tool may require it) so the vocabulary is stable — not dead code." (internal/mcpserver/auth.go:26-29). Reinforced at docs/OPERATION.md:689-692: "An agent token should never carry `curate` — but do not mistake that exclusion for the whole gate. No MCP tool requires `curate`"; and docs/COMM.md:249: "The curation gate works because a capability is *withheld* (no tool requires `curate`), not because the model is asked nicely."
- **If removed:** Deleting the CONSTANT changes nothing observable — it is referenced nowhere else in the repo (`grep scopeCurate` returns only its own declaration and comment); Go compiles unused consts. That is the point and the danger: the gate is not the constant, it is the ABSENCE of a `requireScope(ctx, scopeCurate)` call plus the absence of any token-reachable writer of `state='curated'`. Nothing tests that absence. Removing the constant would be unnoticeable; ADDING one tool that requires it would also be unnoticeable — the tool would just work, and every already-minted token carrying `curate` (CLI can mint it, see below) would gain the power retroactively, since api_tokens have no expiry. This is the single most important asymmetry in the lens.

### Only three code paths ever write `state='curated'`, and none of them is reachable from any bearer token

- **Where:** `internal/store/promote.go:120 and :255 (Promote / Repromote); internal/store/import.go:51; internal/store/seed.go:41`
- **Verdict:** survives
- **Prevents:** The curated head moving without a logged-in human at a browser. Promote/Repromote are called only from internal/web/app.go:763 and :808, routed at app.go:173-174 through `a.requireAuth(...)` (session cookie) and gated per-request by `a.checkCSRF`. `ken import` and `--demo-seed` need shell access to the host and the DB file. There is no CLI promote subcommand and no MCP tool that reaches Promote.
- **Stated reason:** README.md:13-15: "the curated head moves only when a human promotes it. An agent can never curate; that exclusion is the whole point." docs/DESIGN.md:335-336: "Standard agent token = `read,write-draft,propose` — never `curate` (that exclusion is the curation gate)." docs/DESIGN.md:300: "Only human promotion produces `curated`."
- **If removed:** This is the real enforcement, and it is enforcement by REACHABILITY, not by a check. `PromoteInput.ActorKind` (promote.go:18) is written into `entry_version.reviewed_by_actor_id` / `curation_event` (promote.go:142,147) and is NEVER VALIDATED — the only reason it always says 'human' is that the sole call site hardcodes the literal (web/app.go:764). So a future non-web caller of `store.Promote` passing `ActorKind: "ai"` would promote successfully and the row would record it honestly. Nobody would notice: the entry renders identically in /browse and kb_search, `curated_rev` increments, staleness resets to fresh; the only tell is an 'ai' actor id in a column no page surfaces prominently. The new design MUST keep the store-side invariant "the only caller of Promote is a human-session handler" as an explicit, tested property, because today it is a fact about the call graph that no test asserts.

### `curate` is not mintable from the web console — the console's scope menu is `read`/`write-draft`/`propose` only

- **Where:** `internal/web/app.go:824-826 (agentScopes) and app.go:846-858 (agentScopeOK)`
- **Verdict:** survives
- **Prevents:** The operator handing a session a `curate`-bearing token by clicking a checkbox, and so pre-loading the estate with credentials that would become live the day a curation tool ships.
- **Stated reason:** "agentScopes are the KNOWLEDGE-BASE scopes an agent token may hold from the web UI. 'curate' is deliberately excluded — that is the human-only curation gate." (internal/web/app.go:824-825)
- **If removed:** Nothing breaks today, because no tool requires `curate` — a token carrying it behaves exactly like one that does not. That is precisely the catastrophic-vs-useless ambiguity: removal is invisible NOW and becomes a privilege escalation LATER, retroactively, for every copy of every token minted in the interval. Note the asymmetry already present: `ken token add --scopes curate` IS accepted (cmd/ken/cli_token.go:34 advertises `curate` in the flag help, ValidScopes admits it, CheckScopeMix files it under the KB family, and the station-scope refusal at cli_token.go:57-66 does not cover it). So the console withholds `curate` and the CLI mints it. Under the memory's "console first, not CLI" posture that is defensible, but it means the estate may already hold curate-bearing tokens that nobody would see as different from any other.

### OAuth access tokens receive a HARD-CODED scope set (`read`, `write-draft`, `propose`), ignoring the scope string the client requested and the grant recorded

- **Where:** `internal/mcpserver/auth.go:191-201`
- **Verdict:** survives
- **Prevents:** A claude.ai connector — a credential Ken did not mint and cannot see the storage of — ever holding a capability the operator did not intend, including `curate`. The requested `scope` is persisted on the grant (internal/store/oauth.go:120, 334) and returned by ValidateOAuthAccessToken, and the MCP authenticator simply does not read it.
- **Stated reason:** "OAuth access token: a human-approved connector gets the standard agent capability set (read | write-draft | propose) — never curate." (internal/mcpserver/auth.go:191-192). docs/DESIGN.md:383: "a connector's capability is `read`/`write-draft`/`propose`, never `curate`."
- **If removed:** If the new design makes OAuth the ONLY credential mechanism, this control becomes the whole scope model and must be re-decided, not inherited. Replacing the hard-coded set with "honour the grant's scope string" would look like a correctness fix (the field is right there, unused) and would silently make the capability set client-chosen — a connector could request `curate` and, the day a curate-gated tool exists, get it. Removal is invisible today for the same reason as item 1: nothing checks curate. Whatever OAuth grows into, the mapping from grant to capability must stay SERVER-decided and must exclude the curation capability explicitly and testably.

### The mint path refuses an unrecognised scope instead of dropping it

- **Where:** `internal/web/app.go:1003-1010`
- **Verdict:** survives
- **Prevents:** An operator minting a token that looks correct and authenticates nowhere. The old behaviour filtered silently, producing a credential whose failure surfaced later, on a different surface, as "invalid token".
- **Stated reason:** "REFUSE an unrecognised scope rather than dropping it. The old code filtered silently, so a form carrying `comm` minted a knowledge-base token and said nothing — the operator found out when the session could not register." (internal/web/app.go:1003-1005). And on why the console had to learn the comm scopes at all: "Ken's stated posture is that the console is the main method for any operation and the CLI is a last resort — and an operator following it minted a token, handed it to a session, and watched comm_register refuse it for a missing scope. Worse, the handler DROPPED the unknown scope silently, so nothing said why." (app.go:835-838)
- **If removed:** This is the archetype of the project's recurring defect, already paid for once: a silent drop produces a credential indistinguishable from a working one until it is used somewhere else. Under a single OAuth mechanism the same failure mode reappears as "the approval was recorded but the capability was not" — the new design needs an equivalent loud refusal at grant time.

### The `kens_` prefix requirement on the station surface, and `AuthenticateStationKey`'s additional demand that the key carry the `station` scope whatever its prefix

- **Where:** `internal/store/stations.go:429-433 and :456-459`
- **Verdict:** survives
- **Prevents:** A KB or comm token reaching the station surface (notebook, tasks, locker, vault) by shape alone, and a `kens_`-shaped credential that happens to lack the station scope being honoured as a station key.
- **Stated reason:** "A key with no station scope is not a station key, whatever its prefix." (internal/store/stations.go:456). Surface separation: "Built as a copy of internal/commserver/auth.go rather than a shared abstraction, and deliberately so: each surface accepts EXACTLY ONE token shape, and one credential path per endpoint is easier to audit than a parameterised one." (internal/stationserver/auth.go:17-20)
- **If removed:** The prefix is COMPATIBILITY.md contract ("a credential's shape freezes the moment one is issued", COMPATIBILITY.md:90), so removing it breaks issued credentials loudly. The scope re-check is the quiet half: without it, prefix becomes authority, and the whole scope vocabulary reduces to five characters at the front of a string that a caller controls.

### Each surface has its own authenticate() — the duplication is deliberate and the comm/station surfaces refuse the OAuth and dev-token shapes

- **Where:** `internal/commserver/auth.go:62-78; internal/stationserver/auth.go:15-25`
- **Verdict:** survives
- **Prevents:** A token shape added to the knowledge-base authenticator silently gaining access to inter-session messaging or to station assets. Concretely: a cloud-hosted OAuth connector being able to poll and send messages between the human's local sessions.
- **Stated reason:** "This deliberately does NOT reuse internal/mcpserver's authentication, and the duplication is the point rather than an oversight. That path accepts three token shapes; this one accepts exactly ONE... The OAuth path is excluded because a cloud-hosted connector is the worst possible holder of 'reach into the sessions on my machines', and its scope set is hard-coded rather than operator-chosen, so an operator could not withhold comm from it even if they wanted to. The dev-token bypass is excluded because it is a single static credential with an empty token id, which also means it bypasses per-token rate accounting... Sharing the other package's authenticate() would mean a future token shape added there silently gains access here. Keeping them separate makes that impossible." (internal/commserver/auth.go:63-78)
- **If removed:** This is the highest-value item for a design that intends OAuth as the only mechanism, because THIS CONTROL IS THE ONE THAT SAYS NO TO EXACTLY THAT. The reasoning is not about tenancy: it is that a credential living in someone else's cloud should not carry "reach into my running sessions". Consolidating three authenticators into one OAuth path removes it by construction, and the removal is invisible — every surface keeps working, better even, and the day a connector is compromised the blast radius has quietly grown from the knowledge base to the message bus and the vault. If the new design consolidates, the withholding has to be re-expressed as an explicit per-surface capability decision at grant time, not inherited from the fact that three files exist.

### Transport-level scope enforcement fails closed: a token lacking the required scope is refused at the middleware, not per tool

- **Where:** `internal/commserver/auth.go:166-171; internal/stationserver/auth.go:98-101`
- **Verdict:** survives
- **Prevents:** A surface being reachable-but-erroring, which leaks its existence and its tool list to a caller who holds no capability for it.
- **Stated reason:** "Fails closed: a token without the required scope is refused at the transport, so the surface is unreachable rather than merely erroring per call." (internal/commserver/auth.go:166-167)
- **If removed:** Refusing per-tool instead of per-transport is functionally equivalent for a well-formed client and strictly worse for an attacker's reconnaissance. Removal would be invisible in every test that asserts "call fails" rather than "handshake fails".

### `retired_at` — a second, non-severing stop on a station key, checked in AuthenticateStationKey alongside `revoked_at`

- **Where:** `internal/store/stations.go:357-386 (RetireStationKey) and :440-443 (the auth query)`
- **Verdict:** survives
- **Prevents:** A key for a machine you no longer use continuing to reach the station surface, without cutting the COMM endpoints it already bound.
- **Stated reason:** "RetireStationKey stops the key working — INCLUDING for a session holding it right now. IT DOES NOT 'LEAVE LIVE ONES ALONE', and six shipped strings said it did until 2026-08-14. AuthenticateStationKey requires `retired_at IS NULL` (below) and the middleware re-authenticates EVERY request, so the holder loses the notebook, task list, locker and vault at its next call... The behaviour was corrected in code in 1.5.2 by a commit that touched no .properties and no template, so the operator-facing text kept promising the old behaviour for four releases. ken-prod-ops found it by reading the auth query rather than the tooltip." (internal/store/stations.go:357-371)
- **If removed:** Two live divergences to carry into the rework rather than re-create. (a) migrations/0012_stations.sql:53-54 STILL carries the corrected-away claim: "retired = stop binding NEW endpoints, leave live ones alone (the 'I moved machines' path)". (b) The retirement check exists ONLY in AuthenticateStationKey. internal/mcpserver/auth.go:218-220 selects `actor_id, secret_sha256, scopes, revoked_at ... WHERE token_id = ?` with no `retired_at` filter, and internal/commserver/auth.go:144-148 does the same — so a retired key's (id, secret) re-presented with a `ken_` prefix authenticates on both other surfaces. It is inert today only because station keys are minted with `{station, station-locker}` and every kb_/comm_ tool requires a scope they lack. But CheckScopeMix EXPLICITLY PERMITS station+comm, and IssueStationKey never calls CheckScopeMix (grep: its only callers are cli_token.go and web/app.go) — so a station key minted with `comm` would survive retirement on the comm surface. That is a two-line change away from real, and its failure would be silent.

### Revocation severs: `revoked_at` on the token, plus an eager sweep of every endpoint that key bound, plus a fail-closed re-check at USE

- **Where:** `internal/store/tokens.go:110-121 (RevokeToken); internal/web/app.go:1051-1064; internal/comm/endpoint.go:345-376 (SeverEndpointsBoundBy); internal/store/station_binding.go:292-322 + internal/commserver/commserver.go:1029-1041 (check at use)`
- **Verdict:** survives
- **Prevents:** A leaked key remaining operationally live after revocation — the sessions it already bound continuing to poll, send and read until some idle sweep notices, which under traffic may never come.
- **Stated reason:** "Why severing is the default: you revoke because the key leaked. A revocation that leaves the leaked capability running until an idle sweep notices is theatre — and traffic keeps an endpoint alive indefinitely." (docs/STATIONS.md:259-261). On the split enforcement: "severing cannot be made reliable at the REVOKING end... the CLI runs in a SEPARATE PROCESS with no comm.db handle at all, so a revocation issued there can never reach into the message database to mark endpoints. Making the check happen at USE instead means every revocation path works, including ones added later that forget about stations: it fails closed by construction rather than by remembering." (internal/store/station_binding.go:294-301)
- **If removed:** The design principle is the transferable part: enforce at USE, because the revoking end cannot be trusted to reach every subsystem. One caveat to carry: the use-time check is written `if revoked, rerr := ...; rerr == nil && revoked` (commserver.go:1039) — a ken.db read error yields "not revoked". That fail-open is deliberate and stated, but only at the NEIGHBOURING guard (commserver.go:1056-1060: "FAILS OPEN on a database error, matching its neighbour — deliberately, and said out loud rather than inherited"), not at IsStationKeyRevoked itself, whose own comment says "fails closed by construction" — meaning path-coverage, not error handling. A reader auditing only station_binding.go would get the wrong answer.

### The console must state the blast radius before the click — CountEndpointsBoundBy

- **Where:** `internal/comm/endpoint.go:380-388`
- **Verdict:** survives
- **Prevents:** An operator revoking a key without knowing how many live sessions it will disconnect.
- **Stated reason:** "CountEndpointsBoundBy reports how many LIVE endpoints a station key bound, so the console can say 'this will disconnect N live sessions' before the operator clicks (S6). A destructive action whose blast radius is only visible afterwards is one an operator learns to fear rather than use." (internal/comm/endpoint.go:380-383). docs/STATIONS.md:271-273 states it as shipped: "the console states the count before the click — 'this will disconnect 2 live sessions'".
- **If removed:** IT IS ALREADY EFFECTIVELY REMOVED. `grep -rn CountEndpointsBoundBy` finds the definition and no non-test caller: neither /tokens (web/app.go:1040-1065) nor /stations renders it. A documented mitigation for a no-expiry credential exists as a function nobody calls, while two documents describe it in the present tense. Nobody noticed because the absence looks exactly like a page that simply doesn't mention endpoints. This is the lens's cleanest example of the recurring defect, and it is a control whose removal was in fact unnoticed.

### Unprobeability: unknown, retired and revoked keys are refused identically

- **Where:** `internal/store/stations.go:422-427; internal/stationserver/auth.go:91-96`
- **Verdict:** survives
- **Prevents:** A prober distinguishing "this credential once existed here" from "this credential never existed", i.e. using the auth endpoint as an oracle over the estate.
- **Stated reason:** "Retired and revoked keys are both refused here, and indistinguishably from an unknown one — extending COMM's unprobeability house rule (§5). The one place a caller learns WHY it was cut off is after its endpoint secret has already verified (S6), which informs a proven holder and tells a prober nothing." (internal/store/stations.go:424-427)
- **If removed:** This is an outside-world control, not a tenancy one, so it survives whole. Removing it would be invisible in every functional test — the legitimate holder's experience is identical — and would only show up in an adversary's favour.

### The distinguishable revocation error, ordered AFTER the endpoint secret verifies

- **Where:** `internal/store/station_binding.go:284-290 (ErrStationKeyRevoked); internal/commserver/commserver.go:1029-1041`
- **Verdict:** survives
- **Prevents:** Two failures at once: a model retrying forever against a revoked key (and never telling its human), and a prober learning key state. The ordering is what lets both be true.
- **Stated reason:** "It is DISTINGUISHABLE from an ordinary auth failure on purpose (S6): a model that is told its key was revoked reports that to its human, while one that merely sees 'invalid' retries in a loop. This does not weaken §5's unprobeability, because it is returned only AFTER the endpoint's own secret has verified — it informs a proven holder and tells a prober nothing." (internal/store/station_binding.go:285-289)
- **If removed:** Note the neighbouring guard states the stronger form of this property explicitly: "The property is enforced by a DATA DEPENDENCY rather than by the order of two statements... the station id comes from the authenticated endpoint, so there is nothing to check until the secret has already verified. A mutation that merely reworded this guard could not create the oracle" (commserver.go:1050-1055). The revocation check above it does NOT have that structural protection — it is ordering, and a refactor that hoisted it would create the oracle with no test failing. Worth making structural in the rework.

### `last_used_at` is written on all three surfaces, throttled to ~once per minute per token

- **Where:** `internal/store/tokens.go:209-215; internal/mcpserver/auth.go:145-165 (touchThrottle); internal/commserver/auth.go:114-118; internal/stationserver/auth.go:110-122`
- **Verdict:** survives
- **Prevents:** A credential whose use cannot be reasoned about during an incident, and an operator misreading "never used" for "never measured" when deciding what to retire.
- **Stated reason:** "Record that this key was used. Until now nothing did: TouchToken was called only from the knowledge-base authenticator, so `last_used_at` was NEVER written for a station key — and the console rendered a last-used column that was permanently blank. An operator reads a blank as 'unused' rather than 'unmeasured', which is the worse of the two readings, and it made 'retire the key nothing is using' unanswerable even in principle. It also means a stolen station key could read an entire notebook, task list and briefing with no trace at all." (internal/stationserver/auth.go:110-121). Comm's own instance: "Prod observed three comm tokens reporting last_used_at NULL after 102 acknowledged messages, because TouchToken was only ever called from the knowledge-base authenticator." (internal/commserver/auth.go:114-117)
- **If removed:** Twice already this control was absent on a surface and the absence rendered as a blank column indistinguishable from a genuine "unused" — the defect class named in the brief, hit twice in the same codebase for the same reason (the touch lived in one authenticator while three existed). A single credential path in the new design removes the duplication that caused it, which is a genuine argument FOR consolidation; but the observability requirement ("a coarse signal ... the difference between 'no timestamp' and 'used four minutes ago' is the difference between an unanswerable incident and a scoped one") survives untouched.

### SHA-256 of the secret half with constant-time compare — explicitly NOT Argon2 — and the secret shown exactly once

- **Where:** `migrations/0001_init.sql:58-72; internal/store/tokens.go:45-64; internal/store/stations.go:337-355 and :449-452`
- **Verdict:** survives
- **Prevents:** Both a stolen-database replay (only the hash is stored) and a self-inflicted performance tax; plus timing-based secret recovery.
- **Stated reason:** "API tokens for the MCP interface. Opaque high-entropy secret ⇒ store SHA-256 and constant-time compare. NEVER Argon2 a token (Argon2 exists to slow brute-force of LOW-entropy passwords; on a 256-bit secret it only taxes every call)." (migrations/0001_init.sql:58-60). docs/DESIGN.md:332-333: "high-entropy secret, shown once ... constant-time compare — do not Argon2 a 256-bit token."
- **If removed:** Pure outside-world protection; nothing about one user changes it. If OAuth becomes the only mechanism this moves wholesale into the OAuth token store (internal/store/oauth.go), where the same rule must hold — access tokens are high-entropy opaque strings and get the same treatment, human passwords get Argon2id. The distinction is easy to lose in a rewrite that treats "credential" as one type.

### The station key is a HEADER credential and never a tool argument

- **Where:** `internal/stationserver/auth.go:21-25; enforced by shape — every station tool reads the principal from context, none takes a key argument`
- **Verdict:** survives
- **Prevents:** A long-lived credential being written into a conversation transcript, harness log, scrollback, or — via a notebook page — a backup.
- **Stated reason:** "The key is a HEADER credential and never a tool argument. Tool arguments are model output: they land in transcripts, harness logs and scrollback, and — via the notebook — potentially in a backup. A long-lived credential travels the way the Ken token already travels, read by the transport and never spoken by the model." (internal/stationserver/auth.go:21-25). docs/STATIONS.md:174-176 states the same.
- **If removed:** The most important survivor in the lens. It has nothing to do with tenancy and everything to do with the fact that a model's outputs are recorded in places the model does not control. Note the contrast COMM still lives with: every comm_* tool takes endpoint_id + endpoint_secret AS ARGUMENTS, with header-based delivery bolted on later (internal/commserver/commserver.go:1015-1027 prefers the header copy "that did not end up in a transcript"). The new design putting identity in the folder's MCP config header is this control, generalised — which is the right direction, and it is worth saying in the design that this is why, not merely that it is convenient.

### Identity is derived by the server, never taken from the caller — sessionIdentity, and the station id from the authenticated key

- **Where:** `internal/mcpserver/server.go:148-175 (sessionIdentity); internal/stationserver/auth.go:40-51`
- **Verdict:** survives
- **Prevents:** A caller choosing who it is. The measured failure: a caller-supplied optional session_id produced 37/37 and 282/282 NULL identity rows on a live deployment, making the maturity badge's top tier unreachable rather than merely empty.
- **Stated reason:** "sessionIdentity is WHO IS CALLING, derived by the server and never taken from the caller... ken-prod-ops measured the result on a live deployment: 37 of 37 entry_outcome rows had it NULL, and 282 of 282 curation_event rows. Two independent AI actors, three weeks, not one identity recorded... DESCRIBING THE FIELD BETTER WOULD NOT HAVE FIXED IT... a caller-supplied identity is unreliable AND unfalsifiable. A session that wants a badge sends three different strings. Ken already knows who is calling, so it should look rather than ask." (internal/mcpserver/server.go:148-167)
- **If removed:** Directly load-bearing for the settled design: "identity travels as a stable opaque id in the folder's MCP config header. Not a secret: knowing it authorises nothing." That is a CALLER-SUPPLIED identity by construction. It is defensible only if the id authorises nothing and is used solely for attribution — but attribution is exactly what this control was built to make trustworthy, and the failure mode here (unfalsifiable self-reported identity) returns in full. The mitigation that must be designed in: the id must be bound server-side to the approved workspace record at first use, so a second folder presenting the same id is a detectable collision rather than a silent merge.

### `ListTokens` renders each key's STATION by name, so identical-looking keys can be told apart before revoking one

- **Where:** `internal/store/tokens.go:66-84 and the query at :88-94`
- **Verdict:** survives
- **Prevents:** Revoking the wrong station's key. Since revocation SEVERS the endpoints that key bound, picking wrong cuts a different station's COMM.
- **Stated reason:** "Without it the listing cannot tell station keys apart: actor, kind, scopes and label are all identical across every key one human minted from one machine, so eight keys rendered as three distinct-looking rows on a live deployment. That is not cosmetic — revoking a station key SEVERS the endpoints bound to it, so an operator picking one of four identical rows had a one-in-four chance of cutting off a different station's COMM. The only thing discriminating them was last_used_at, which stops discriminating the moment two are used in the same window." (internal/store/tokens.go:69-77)
- **If removed:** A pure human-factors control over a destructive action, and its absence produced a measured 1-in-4 wrong-target rate on a live deployment. It also documents the general shape: "The value was already stored on api_token.station_id and populated on every station key; only the rendering omitted it" (:79-81) — the data was there and the surface didn't ask for it. Same failure as CountEndpointsBoundBy above. Under a workspace model this becomes "name the workspace on every credential row", and it is not optional.

### Body-size caps set per surface, smaller where model-generated arguments are the only input

- **Where:** `internal/mcpserver/auth.go:37-39 (4 MiB); internal/commserver/auth.go:31-36 (1 MiB); internal/stationserver/auth.go:35-38 (1 MiB)`
- **Verdict:** survives
- **Prevents:** Memory exhaustion from an oversized request body, ahead of any per-field validation in the store.
- **Stated reason:** "Smaller than the knowledge base's cap on purpose: message bodies are separately capped in internal/comm, and nothing here legitimately approaches a multi-megabyte payload — tool arguments are generated token-by-token by a model, so a large body is a design error long before it is a transport one." (internal/commserver/auth.go:32-35)
- **If removed:** Outside-world protection, unaffected by who the user is. Removal is silent under all normal traffic and only shows up under attack or a runaway client — the standard shape for this whole category.

### CORS is permissive on /mcp (and exposes WWW-Authenticate for OAuth discovery) and absent entirely on /comm and /station

- **Where:** `internal/mcpserver/auth.go:121-131; internal/commserver/auth.go:81-86; internal/stationserver/auth.go:77-82`
- **Verdict:** survives
- **Prevents:** On /mcp, it ENABLES claude.ai's browser fetch and the RFC 9728 discovery challenge. On the other two, withholding it prevents handing a browser-resident attacker a usable cross-origin path to the message bus and station assets.
- **Stated reason:** "No CORS headers at all: unlike /mcp, this endpoint has no browser client, so a permissive Access-Control-Allow-Origin would be pure attack surface." (internal/commserver/auth.go:81-82; repeated at internal/stationserver/auth.go:77-78). And on /mcp: "Expose the session id so the browser streamable transport can read it back, and WWW-Authenticate so the OAuth discovery challenge is visible cross-origin." (mcpserver/auth.go:128-130)
- **If removed:** Directly relevant to making OAuth the only mechanism: OAuth discovery REQUIRES the permissive CORS + Expose-Headers on the surface that serves it. If all three surfaces converge on one OAuth-authenticated endpoint, they all inherit `Access-Control-Allow-Origin: *`, and the deliberate withholding above evaporates as a side effect of consolidation — with no line of code deleted and nothing failing. Anyone reviewing the diff would see CORS being added for a good stated reason and would not see what it was being withheld from.

### CreateFirstAdmin — the first human admin is created atomically only if no human actor exists

- **Where:** `internal/store/tokens.go:137-152`
- **Verdict:** survives
- **Prevents:** Two concurrent first-run /setup posts both creating an admin — the check-then-insert TOCTOU that a separate SELECT+INSERT would leave open on an unauthenticated endpoint.
- **Stated reason:** "CreateFirstAdmin atomically creates the initial human admin ONLY if no human user exists yet (the first-run wizard). Returns created=false (no error) if one already exists — a single INSERT...WHERE NOT EXISTS that closes the check-then-insert TOCTOU a separate SELECT+INSERT would leave open (two concurrent /setup posts can't both create an admin)." (internal/store/tokens.go:137-141)
- **If removed:** The window is small and the endpoint is only open before setup completes, so a regression here would almost certainly never be observed in normal operation — and would hand an unauthenticated racer a console account. Single-user makes this MORE important, not less: there is exactly one human and the first-run window is the only moment their identity is unclaimed. Any new bootstrap flow (OAuth-based first-run included) needs the same atomic claim.


---

## COMM endpoint credentials

*33 controls — 16 survive, 12 survive on a different key, 5 dissolve conditionally, 0 dissolve outright.*

### Owner-token re-check on every tool call: `if ep.Owner.TokenID != p.TokenID`

- **Where:** `internal/commserver/commserver.go:1084-1086 (reasoning at :965-970)`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** One machine's Ken token driving another machine's endpoint after an endpoint id leaks (ids travel in message envelopes, console pages, runbooks, and transcripts). The secret proves which session; this proves which machine/token.
- **Stated reason:** "The ownership re-check is what keeps one machine's token from driving another machine's endpoint if an endpoint id ever leaks: the secret proves the session, and this proves the token." (commserver.go:968-970). Operationally: "`token_id` is written at registration ... and no `UPDATE` ever re-points it. A different machine's token fails even with a correct secret and a correct voucher. There is no console or CLI override. **If that token was revoked, the endpoint is dead — it cannot poll, ack, send or bind, and its queued mail is unreadable by anyone.**" (docs/RUNBOOK-ENDPOINT-MIGRATION.md:67)
- **If removed:** Nothing, and that is the finding. With one Claude account and OAuth as the sole credential, `p.TokenID` is the same value for every workspace on every machine, so the comparison is `x != x` — permanently false. It already costs more than it protects: it is the reason an endpoint is unmovable between machines and dies outright with its registering token, with no override. Its removal would be noticed only as things starting to work. CAUTION: it is currently the last line of auth() and the only thing standing between 'holds a valid endpoint id + secret' and 'is the right principal'. Delete it and the replacement layer must state what now binds a workspace to a credential, or the answer becomes 'nothing'.

### The secret is returned once and is unrecoverable by construction (no read path anywhere; only `secret_sha256` is stored)

- **Where:** `internal/comm/endpoint.go:44-45, :70; docs/COMM.md:373-384`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A stored plaintext credential that any later read path — console, tool, backup, log — could disclose.
- **Stated reason:** "RegisterEndpoint mints a new endpoint for an authenticated session and returns it together with its one-time secret, which is never recoverable afterwards." (endpoint.go:44-45); "`comm_register` returns the secret once and nothing will ever show it again. This is not an edge case to design around later: **an AI client's memory is lossy by design.** Context compaction is routine, silent, and gives the session no signal — a session does not know it has forgotten." (docs/COMM.md:375-378)
- **If removed:** This control is the origin of an entire apparatus that exists only to compensate for it: the connect-time instruction to write a 0600 file before any other call, the rotation console flow, the three-mechanism recovery section in COMM.md §3.1, and MEMORY.md's "read the 0600 file IN FULL". A credential the session never holds — OAuth — deletes the control and the apparatus together. This is the single largest simplification available in this lens. Nothing would break; a large amount of instruction text and one whole console flow become dead.

### Endpoint credentials preferred from request HEADERS (`X-Ken-Endpoint-Id` / `X-Ken-Endpoint-Secret`) over tool arguments

- **Where:** `internal/commserver/commserver.go:971-1011, :1019-1022`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** The endpoint secret being written in clear text into the client's conversation transcript on disk, on every single tool call, for as long as the transcript is kept.
- **Stated reason:** "tool arguments are recorded by the CLIENT in its conversation transcript — on disk, in the clear, for as long as that transcript is kept. Ken cannot mitigate that by changing what Ken logs: the recording happens in software neither end ships. Moving the credential out of the argument position is the only thing that removes it" (:973-978); "the headers are the copy that did not end up in a transcript, so they are the copy trusted" (:1020-1022)
- **If removed:** Today: every session leaks its own credential into its own transcript forever, invisibly. Under a non-secret opaque id in the folder's MCP config header, this whole control class evaporates — the id is already in a config header and leaking it authorises nothing. This is the second-largest simplification in the lens, and it is the settled design's strongest argument: the header mechanism already exists and already works; only the secrecy requirement goes away.

### `ListEndpoints` scoped by `space_id`, and `JoinChannel` refuses a code from another space

- **Where:** `internal/comm/endpoint.go:426-435; internal/comm/channel.go:88-94`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Enumeration of another human's endpoints, and pairing across humans.
- **Stated reason:** "Scoped by space from day 1 even though only one exists today: an unscoped listing would be the enumeration surface in a multi-human future, and scoping it later would be a behavioural break for anything that relied on the full list." (endpoint.go:426-429); "Today there is one space, so this cannot fire; it is here because it must be true before a second human exists, not after." (channel.go:88-90)
- **If removed:** Provably nothing — the code says so itself: with one space these predicates cannot fire, and their removal is unobservable by any test that does not fabricate a second space. These are the cleanest dissolutions in the lens, because the settled fact ('SINGLE USER, permanently; sharing between instances is FEDERATION, never tenancy') formally cancels the future they were built for. Delete the space column with the layer rather than carrying it; a dormant scoping predicate that is never exercised is a control that will be wrong the day it first fires.

### Binding voucher: single-use, 5-minute TTL, narrowed to one endpoint, and requires `issued_to_actor` == the endpoint's registering actor

- **Where:** `internal/store/station_binding.go:186-193 (per docs/RUNBOOK-ENDPOINT-MIGRATION.md:1.1); internal/commserver/commserver.go:290-296`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A voucher being redeemed by an endpoint it was not issued for, or by a session under a different identity — i.e. binding someone else's session to your workspace.
- **Stated reason:** "`issued_to_actor` is the actor recorded on the **station key** that minted the voucher. The bound parameter is **the endpoint's own registering actor**. They must be the *same actor row*." and "**The \"no COMM token\" badge on `/stations` is NOT the test.** ... Absence of the badge means \"this actor could bind something\", never \"this key can bind *this* endpoint\"." (docs/RUNBOOK-ENDPOINT-MIGRATION.md:1.1)
- **If removed:** Under one human and one account there is only one actor, so the actor equality is `x == x`. Today it is worse than useless: a key minted from the console belongs to the human curator while COMM tokens are minted as `ai` actors, so console-minted keys mint vouchers that fail FOREVER with a setup error no retry fixes — and the console's own badge is a signal that looks like the check but is not it. That is the brief's defect archetype exactly: a control whose diagnostic is indistinguishable from the condition it screens for. Under 'no approval to start work in a new folder', the whole voucher round-trip should disappear rather than be ported.

### Per-endpoint one-time secret minted at registration (`randBase62(40)`, stored only as SHA-256)

- **Where:** `internal/comm/endpoint.go:55-87; internal/comm/migrations/0001_init.sql:37-42`
- **Verdict:** survives, on a different key
- **Prevents:** Two Claude Code sessions on the same machine polling and acking each other's mail. They share one `ken_` bearer token by operating convention, so without a per-session secret the token alone cannot tell them apart — most likely to happen by accident when both register with the same label.
- **Stated reason:** "secret_sha256 exists because the operating convention is one Ken token per MACHINE, so every session on a box shares a token. Without a per-endpoint secret, two sessions could poll and ack each other's messages — most likely by ACCIDENT, when both register with the same label. This is what makes sender identity honest: token-authenticated and endpoint-scoped, i.e. trustworthy across machines and users, advisory between sessions sharing one token." (0001_init.sql:37-43); "The endpoint secret is what stops two sessions on one box from polling and acking each other's messages by accident." (endpoint.go:15-16)
- **If removed:** Cross-reading of inboxes between sibling sessions, and it would be NOTICED only as confusion: a session acting on mail addressed to another, with no error anywhere. The failure survives single-user intact (it was never about tenancy — it is one human's sessions colliding). What changes is the mechanism: if identity comes from the folder's MCP config header, the folder key does this job and the secret is redundant. But note the substitution is not free — the settled design says the id "authorises nothing", while under one OAuth credential per instance the id becomes the ONLY discriminator between workspaces, so it authorises everything the account can do. That must be answered explicitly, not inherited.

### Repeat registration mints a NEW endpoint; it never attaches to an existing one

- **Where:** `internal/comm/endpoint.go:44-51`
- **Verdict:** survives, on a different key
- **Prevents:** A second session silently inheriting the first session's inbox because both used the same label.
- **Stated reason:** "A repeat registration under the same token and label deliberately creates a NEW endpoint rather than attaching to the existing one: silently handing a second session the first session's inbox is the failure this avoids, and it is far more likely by accident (two sessions with the same label) than by malice."
- **If removed:** Today: a second session would join an inbox it was never meant to read, silently. Under the workspace model this control INVERTS — a second session in the same folder MUST attach to the same durable workspace; that is the whole point. The failure it names does not disappear, it moves down a layer: the thing that must stop two sessions consuming the same message is claim-once delivery (already built, `delivery.claimed_by_endpoint` + lease), not identity separation. If the new layer inverts this without moving the guarantee, two sessions in one folder will double-process mail and nobody will see an error.

### Secret rotation is reachable ONLY from the curator console — deliberately no MCP tool

- **Where:** `internal/comm/endpoint.go:134-160; internal/web/comm.go:222-256; docs/COMM.md:394-402`
- **Verdict:** survives, on a different key
- **Prevents:** Any session on a machine seizing any other endpoint on that machine. Since all sessions present the same machine token, an automated reissue is available to every session equally.
- **Stated reason:** "THIS IS DELIBERATELY NOT REACHABLE FROM ANY TOOL, and that placement is the entire security argument. One bearer token covers a machine, so the endpoint pair is the only thing separating two sessions sharing it; a reissue any SESSION could trigger would let any session on that machine seize any endpoint on it. That is why deriving a new secret from token material was rejected. The defect there is the AUTOMATION, not the reissuing" (endpoint.go:139-145). And: "The security property lives in WHERE this handler is, not in what it does" (web/comm.go:225-226)
- **If removed:** A session could take over a sibling workspace's identity with one tool call, and the victim would see only "this endpoint_id/endpoint_secret pair is not valid" — indistinguishable from a sweep or a compaction. This is the load-bearing PRINCIPLE to carry forward verbatim: identity changes require the human, never a session. Under OAuth there is no secret to rotate, so the mechanism goes; the rule it encodes ('one session must not be able to re-point another workspace's identity') must be restated as an invariant of the new layer, or it will be lost as an accident of the rewrite.

### Rotation preserves endpoint id, owner and every channel

- **Where:** `internal/comm/endpoint.go:134-137, :166-171`
- **Verdict:** survives, on a different key
- **Prevents:** An incident response so expensive that operators avoid it: before rotation, containing a leaked secret meant revoking the endpoint and rebuilding every channel with a fresh pairing code per channel and coordinated re-joins.
- **Stated reason:** "The endpoint keeps its id, its owner and — the point of the whole operation — every channel it belongs to, so its peers are unaffected and nothing needs re-pairing." ... "A secret LEAKED ... Until now the only remedy was revoking the endpoint and rebuilding every channel from scratch, which is why containing a leak was expensive enough to hesitate over. Rotation is the missing incident-response primitive."
- **If removed:** Nothing breaks immediately; the cost lands later, on the human, as reluctance to act. The transferable requirement: whatever the new layer does to recover a workspace's access must not disturb the workspace's RELATIONSHIPS. Under 'one approval per pair', re-approval-on-recovery would be exactly the defect this fixed.

### Header pair is all-or-nothing (both non-empty, or the headers are ignored entirely)

- **Where:** `internal/commserver/commserver.go:1005-1010`
- **Verdict:** survives, on a different key
- **Prevents:** A half-configured MCP entry silently overriding one half of the credential and producing an authentication failure that reads like a revoked or swept endpoint.
- **Stated reason:** none stated in code (the behaviour is asserted in internal/commserver/endpoint_header_test.go:49-51 for "id only", "secret only", "blank values"); documented consequence at docs/RUNBOOK-ENDPOINT-MIGRATION.md: "**All-or-nothing:** `withEndpointCred` ignores the header pair unless *both* are non-empty."
- **If removed:** A typo in one of two header lines would mix a header id with an argument secret and fail with the uniform "pair is not valid" message — the operator would chase a rotation they do not need. Exactly the class of defect this project keeps hitting: a misconfiguration that presents as the failure the control screens for. If the new layer carries one id header instead of two, the failure mode collapses to 'present or absent', which is strictly better.

### Station-key revocation checked at USE (fail closed), not at revoke time

- **Where:** `internal/commserver/commserver.go:1032-1041`
- **Verdict:** survives, on a different key
- **Prevents:** A revoked/leaked station key leaving already-bound sessions running, because the process that revokes cannot reach comm.db to mark them.
- **Stated reason:** "Checked HERE, at use, because the revoking end cannot be relied upon — `ken token revoke` runs in a separate process with no comm.db handle, so a revocation issued there could never mark the endpoint. Failing closed at use covers every revocation path, including ones added later that forget stations exist."
- **If removed:** A revoked credential would keep working for up to the idle window (7 days) or forever if the session keeps polling — and nothing would log it. Station keys go away under OAuth, but the PRINCIPLE is the one to keep: authorisation is re-checked where it is used, because the revoking process may not be able to reach the store that holds the live sessions. Ken's two-database split (ken.db durable, comm.db expendable, no foreign keys) is not going away, so this constraint is not going away either.

### Uniform refusal text for unknown endpoint / wrong secret / swept endpoint, and `ErrDenied` (not `ErrNotFound`) for a revoked endpoint

- **Where:** `internal/comm/endpoint.go:92-94, :117-122; internal/commserver/commserver.go:1068-1082`
- **Verdict:** survives, on a different key
- **Prevents:** Using error differences to enumerate which endpoint ids exist.
- **Stated reason:** "A revoked endpoint authenticates as ErrDenied rather than ErrNotFound so a caller cannot use the distinction to probe which endpoint ids exist." (endpoint.go:92-94); "The wording is deliberately identical for an unknown endpoint, a wrong secret and a swept one, so it still tells a prober nothing." (commserver.go:1071-1073)
- **If removed:** Enumeration becomes possible for anyone who already holds a valid `comm` token — i.e. within one machine. Under single-user with a non-secret workspace id, existence is not a secret worth keeping INSIDE the instance; it becomes worth keeping again the moment federation between instances exists. Weigh the cost honestly: this uniformity is also why a legitimate session cannot tell 'you were swept' from 'your secret is wrong', which is why that error string had to grow a 200-word recovery essay.

### Idle sweep hard-DELETEs endpoint rows past `EndpointIdleTTLSeconds` (default 7 days)

- **Where:** `internal/comm/message.go:1121-1165; internal/comm/comm.go:220-224, :277`
- **Verdict:** survives, on a different key
- **Prevents:** Unbounded growth of comm.db and the operator console, since sessions register once and never unregister and an agent loop can register freely.
- **Stated reason:** "Sessions register once each and never unregister, so under NORMAL usage these rows accumulate forever — the operator console and comm.db grow without bound, and an agent loop could add rows freely. An endpoint unseen for the retention window has no live session behind it; its channels cascade."
- **If removed:** comm.db grows forever and the console becomes unreadable — noticed slowly, as clutter. But the sweep is in direct conflict with the settled workspace model: a workspace is DURABLE and outlives every session, so the row that carries identity must never be swept, while the per-session connection row should be. Today those are the same row, which is why an endpoint id in a knowledge-base entry names something that will not exist (the entire 'hearsay names the STATION' instruction at commserver.go:205-206 exists to paper over this). Splitting durable workspace from disposable connection removes the conflict; keep the sweep on the connection only.

### `bound_by_station_key_id` column + `SeverEndpointsBoundBy` — revoking a key revokes every endpoint it bound and releases their claims

- **Where:** `internal/comm/endpoint.go:36-40, :345-378`
- **Verdict:** survives, on a different key
- **Prevents:** A leaked credential whose revocation stops future bindings but leaves the sessions it already authorised running indefinitely.
- **Stated reason:** "This is what makes revoking a station key mean something. You revoke because the key leaked; a revocation that stops future bindings but leaves the already-bound sessions running until an idle sweep notices is theatre — and traffic keeps an endpoint alive indefinitely, so the sweep may never come." (:349-353); "Revoking that key severs every endpoint it bound (S6) — without this column, revocation would stop future bindings and leave the leaked capability running." (:37-39)
- **If removed:** Revocation would look complete in the console and be inert in fact — the operator's action and its failure are indistinguishable from the outside. Station keys disappear under OAuth, but the invariant must be restated: the new layer must record WHICH grant authorised each live session, or 'withdraw this approval' cannot reach the sessions it produced. Note the crucial second clause — traffic keeps a session alive forever, so the idle sweep is not a backstop for revocation.

### Scope required at the transport and fails closed; `comm-file` re-checked per tool

- **Where:** `internal/commserver/auth.go:166-170; internal/commserver/commserver.go:955-962`
- **Verdict:** survives, on a different key
- **Prevents:** A knowledge-base token sending messages, a COMM token writing knowledge, and the file relay being reachable by a token the operator granted only messaging.
- **Stated reason:** "Fails closed: a token without the required scope is refused at the transport, so the surface is unreachable rather than merely erroring per call." (auth.go:166-167); "this is the second, per-tool half of the check — and it is what makes the reserved scope real rather than vocabulary." (commserver.go:953-955); docs/COMM.md:634-637: "A token that could both read working notes and write curated knowledge is the mixing this check exists to prevent."
- **If removed:** Every session gains the file relay and the knowledge base at once. This is blast-radius limiting on the human's own sessions, so it survives — but it sits badly with a single OAuth credential, which by construction carries one fixed scope set. The rewrite must say where scope separation lives once the credential no longer carries it; 'the workspace' is the obvious answer and it needs to be written down. Note MEMORY.md's standing rule that every session gets every surface — that is about surfaces offered, not about the file relay being on.

### COMM has its own rate-limit bucket keyed on token id, separate from the knowledge base's

- **Where:** `internal/commserver/auth.go:100-113`
- **Verdict:** survives, on a different key
- **Prevents:** A comm long-poll loop consuming the machine's KB budget and starving `kb_*` calls, or tripping the per-IP strike threshold and locking the machine out entirely.
- **Stated reason:** "Its own rate accounting, separate from the knowledge base's bucket: the operating convention is one token per machine, so a comm poll loop sharing the KB's budget could starve that machine's kb_* calls (and, past the per-IP strike threshold, lock the machine out entirely)."
- **If removed:** One busy session locks the human out of their own knowledge base — noticed immediately, diagnosed slowly. Under a single account credential the bucket key becomes per-INSTANCE, so all workspaces share one budget and a single runaway poll loop starves every other workspace. Re-key it on the workspace id; the folder-keyed identity makes that natural, and it is a strictly better key than the token was.

### `EndpointIDsForStation` — a machine-checkable answer to 'which endpoint is mine'

- **Where:** `internal/comm/endpoint.go:487-518`
- **Verdict:** survives, on a different key
- **Prevents:** A session using another session's credential file, having followed the credential-hygiene instructions correctly.
- **Stated reason:** "There was no machine-checkable answer to that from either side: station_me knew the station and not the endpoint, and a session comparing its credentials file against its memory was comparing two things it had itself chosen. One estate host carries eight endpoint credential files across five directories in six naming schemes, all owned by one UNIX user — every session having followed the \"0600, outside a git repo\" instruction correctly, and the result being a directory of interchangeable-looking secrets. A session used the wrong one."
- **If removed:** A session picks the wrong file and operates as another workspace — successfully, with no error, because the credential is valid. This is the strongest empirical argument in the whole lens FOR the settled design: the file-on-disk credential model produced an estate of interchangeable secrets under one UNIX user, and folder-keyed identity deletes the failure at the root. Whatever the new layer does, keep a surface that answers 'which workspace am I' authoritatively from the server, not from the session's own memory of what it chose.

### Rotation refuses a revoked endpoint (`WHERE endpoint_id=? AND revoked_at IS NULL`)

- **Where:** `internal/comm/endpoint.go:158-160, :171`
- **Verdict:** survives
- **Prevents:** Resurrecting a capability an operator deliberately destroyed, by rotating a revoked endpoint back into service.
- **Stated reason:** "A revoked endpoint is refused: rotating one would quietly resurrect a capability an operator deliberately destroyed, and the revoke path is what a leak response escalates TO, never back from."
- **If removed:** Revocation would become undoable-by-accident: a curator clicking Rotate on a row they had revoked would silently re-arm it, and the console shows no revoked state to warn them. This protects the human from their own console, not one tenant from another — it survives whatever the credential is.

### Rotation audit is a server log line naming the curator; `rotate_count`/`secret_rotated_at` are explicitly display-only

- **Where:** `internal/comm/endpoint.go:25-29; internal/web/comm.go:249-254`
- **Verdict:** survives
- **Prevents:** A rotation nobody can attribute afterwards. comm.db is expendable and not backed up, so the row counters cannot be the record.
- **Stated reason:** "RotatedAt and RotateCount are DISPLAY state for the console (\"did I already rotate this one?\"). comm.db is expendable and not backed up, so the authoritative audit record is the server log, not these columns." (endpoint.go:25-27); "it names who did it, because a rotation an operator did not perform is the signal that matters." (web/comm.go:251-253)
- **If removed:** Nobody would notice — until the one moment it matters. With one human there is only ever one plausible actor, so the value is not 'who' but 'that it happened at all', against a DB that is deliberately not backed up. Any identity mutation in the new layer needs a durable trace outside the expendable store; this is the pattern to copy.

### `withEndpointCred` lifts credentials but does NOT authenticate; all verification stays in `auth()`

- **Where:** `internal/commserver/commserver.go:993-999`
- **Verdict:** survives
- **Prevents:** A second authentication path that skips the checks hanging off a verified secret (severed station key, archived station).
- **Stated reason:** "It does NOT authenticate: auth() below still does that, so every check that hangs off a verified secret — the severed station key, the archived station — stays in one place and cannot be bypassed by arriving through a different door."
- **If removed:** Not visible at all until someone adds a third check to auth() and the header path silently does not get it. This is an architectural control against future drift, not against an attacker; it is the exact discipline the new layer needs, since it will have at least two credential arrival paths during migration.

### Re-authentication of the endpoint on EVERY tool call (`auth()` at the head of every comm_* handler)

- **Where:** `internal/commserver/commserver.go:1013-1087`
- **Verdict:** survives
- **Prevents:** Any state cached from an earlier call going stale: it re-resolves, per call, the endpoint rowid (which is the party key `e:<id>`), the station binding (party key `s:<station>`), the binding key's revocation status, the station's archived status, and the owning actor/space used for channel-ownership checks.
- **Stated reason:** "auth resolves the endpoint identity carried in every tool call and confirms it belongs to the authenticated token holder." (:965-966). What it pins is stated at internal/comm/party.go:110-115: "Resolved INSIDE the caller's transaction so it cannot observe a binding that changes underneath it — a session binding mid-send would otherwise get a message filed under one identity and a delivery under another."
- **If removed:** The pinning is the point for the rewrite: ONE credential lookup currently yields identity, addressing, authorisation-still-valid and ownership together, with no re-derivation and no cache. Split those into separate lookups in the new layer and you get skew — a message filed under one identity and its delivery under another, which is unrecoverable and silent. Whatever replaces the secret must resolve the same tuple in one shot, per call, inside the transaction.

### Archived-station check at use, ordered by DATA DEPENDENCY after the secret verifies

- **Where:** `internal/commserver/commserver.go:1042-1065`
- **Verdict:** survives
- **Prevents:** (a) a session bound to an archived station polling, sending and acking forever; (b) an unauthenticated prober reading a station's archive state by guessing endpoint ids.
- **Stated reason:** "Archiving was inert here: a session bound before the archive kept polling, sending, broadcasting and acking forever, while docs/STATIONS.md promised archiving severs live endpoints. The doc and the code disagreed, and the code was the one users met." (:1044-1047); "The property is enforced by a DATA DEPENDENCY rather than by the order of two statements, which is the stronger form: the station id comes from the authenticated endpoint, so there is nothing to check until the secret has already verified. A mutation that merely reworded this guard could not create the oracle, and that is a fact about the shape rather than about the care taken here." (:1053-1058)
- **If removed:** A retired workspace would keep transacting, exactly as it did before this was fixed — invisible, because everything continues to work. The oracle-ordering half is the reusable idea: derive the sensitive lookup FROM the authenticated identity so the ordering cannot be undone by an edit. Note this guard deliberately FAILS OPEN on a DB error ("ken.db being briefly unreadable should not cut messaging for every station at once"), which is a decision the new layer must re-make consciously rather than inherit.

### Sweep refuses to run on a non-positive idle window (`if idle := ...; idle > 0`)

- **Where:** `internal/comm/message.go:1127-1134, :1151`
- **Verdict:** survives
- **Prevents:** A dropped or mis-mapped setting turning the retention sweep into 'delete every endpoint with no traffic yet', including one that just registered and is mid-handshake.
- **Stated reason:** "The guard is load-bearing: a threshold of 0 would make \"idle for 0 seconds\" = \"idle now\" and delete EVERY endpoint with no message traffic yet — including one that just registered and is mid-handshake. A retention sweep must fail SAFE (do nothing) on a non-positive window, never sweep everything. This is exactly the failure a dropped settings mapping caused in 1.2.0, so the sweep now refuses to run without a positive window regardless of how the window was configured."
- **If removed:** A shipped regression, already lived once: COMM became unusable because freshly registered endpoints were deleted mid-handshake, and the symptom was 'my credentials are invalid' — the same message a wrong secret produces. This is the archetype of the defect class named in the brief: the control's failure is indistinguishable from what it checks for. Carry the rule, not just the code: any retention threshold reaching the new layer must fail closed on zero/negative.

### Sweep excludes endpoints occupying a channel seat, and both `NOT IN` sets exclude NULL explicitly

- **Where:** `internal/comm/message.go:1135-1149, :1157-1163`
- **Verdict:** survives
- **Prevents:** (a) collecting a quiet endpoint cascading away the CHANNEL — the human-authorised relationship a successor is promised it inherits — plus the successor's queued mail and the attachment rows recording which bytes to unlink; (b) a single NULL in either set silently disabling the whole sweep.
- **Stated reason:** "A CHANNEL SEAT IS NOT AN IDLE ROW, however quiet its endpoint is. ... a seat whose messages had merely aged out took the CHANNEL with it, plus any of the successor's own queued mail on it, plus the attachment rows that are the only record of which bytes to unlink — silently, since Sweep reports only expired and purged counts." (:1135-1144); "Both guards must exclude NULL explicitly. `id NOT IN (…, NULL)` is NULL, not true, so a single NULL in either set would silently stop the sweep deleting anything at all — a retention leak that presents as no error and no log line." (:1146-1149)
- **If removed:** Both failure directions are silent and opposite: over-collect destroys an approved relationship with no log, under-collect stops all retention with no log. Sweep's return signature (`expired, purged int64`) cannot express either. Under 'ONE approval per pair of workspaces', destroying a relationship is now MORE costly, not less — it burns the human's single approval. The new layer should make the sweep report what it deleted, so the two failure modes stop being indistinguishable from success.

### `last_seen_at` refresh throttled to at most once per minute

- **Where:** `internal/comm/endpoint.go:125-129`
- **Verdict:** survives
- **Prevents:** A long-poll loop amplifying into a database write on every request.
- **Stated reason:** "Throttled to at most once a minute so a poll loop does not amplify into a write on every request — the same shape as the knowledge base's TouchToken."
- **If removed:** Write amplification on a single-writer SQLite pool; noticed as latency under load, not as an error. Note the coupling worth carrying forward: `last_seen_at` is written by the AUTH path and read by the SWEEP, so authentication is what keeps a workspace alive. It is also written BEFORE the owner-token check at :1084, so a wrong-token call still refreshes liveness — harmless today, but an example of a side effect landing before authorisation completes.

### Binding is set once and an endpoint can never move between stations

- **Where:** `internal/comm/endpoint.go:184-192; internal/commserver/commserver.go:281-289`
- **Verdict:** survives
- **Prevents:** A session carrying one station's unread mail into another station's inbox.
- **Stated reason:** "Binding is set once at registration and never changed — an endpoint that could move between stations would let a session carry another station's unread mail across, which is the shared-inbox failure in a new costume." (endpoint.go:190-192); "Already bound is refused rather than re-pointed. Moving an endpoint BETWEEN stations would carry the first station's unread mail into the second — the shared-inbox accident in a new costume. Binding one that has NO station carries nothing across, which is why that direction is allowed and this one is not." (commserver.go:283-288)
- **If removed:** Mail addressed to workspace A becomes readable in workspace B, silently, with no audit. This is a mistake-prevention control between one human's own workspaces — precisely the class that survives. Under folder-keyed identity it is nearly free: a folder does not move. Keep the asymmetry (unbound→bound allowed, bound→bound refused), because it is what makes migration of the existing estate possible at all.

### `CountEndpointsBoundBy` — blast radius shown before the destructive click

- **Where:** `internal/comm/endpoint.go:380-390`
- **Verdict:** survives
- **Prevents:** An operator discovering only afterwards that revoking a key disconnected N live sessions.
- **Stated reason:** "so the console can say \"this will disconnect N live sessions\" before the operator clicks (S6). A destructive action whose blast radius is only visible afterwards is one an operator learns to fear rather than use."
- **If removed:** Nothing technical; the human stops using revocation, which is worse than the defect. With approvals reduced to one per workspace pair, each approval carries MORE weight, so 'what does withdrawing this break' becomes more important, not less.

### Revoke releases the endpoint's unacked claims in the same transaction (and unbind releases only station-addressed claims)

- **Where:** `internal/comm/endpoint.go:392-424, :308-323`
- **Verdict:** survives
- **Prevents:** A revoked or unbound reader holding messages under an unexpired claim lease (default 900s), hiding them from the readers who could act on them — the very takeover the revoke was performed to enable.
- **Stated reason:** "a revoked reader is never coming back to ack, so holding its messages for the remainder of the lease would hide them from the station's other readers for no reason — the operator revoked a wedged session precisely so someone else could take over." (:399-401); "Mail addressed to the endpoint ITSELF keeps its claim — that mail is still its own." (:313-315)
- **If removed:** The remedy would appear to work and change nothing for up to the lease; the human would revoke a wedged session and watch the replacement receive nothing. Any workspace model with several sessions sharing one inbox needs this, and the unbind variant shows the precise rule: release what belonged to the SHARED inbox, keep what was addressed to the individual.

### COMM transport accepts exactly ONE token shape — `ken_<id>_<secret>` with the `comm` scope; OAuth and the dev-token bypass are deliberately excluded, and the KB authenticator is deliberately NOT reused

- **Where:** `internal/commserver/auth.go:62-98, :124-172`
- **Verdict:** survives
- **Prevents:** A cloud-hosted OAuth connector — or a static dev credential with an empty token id — gaining the ability to reach into the sessions running on the operator's machines; and a token shape added to the other package silently gaining access here.
- **Stated reason:** "The OAuth path is excluded because a cloud-hosted connector is the worst possible holder of \"reach into the sessions on my machines\", and its scope set is hard-coded rather than operator-chosen, so an operator could not withhold comm from it even if they wanted to." ... "The dev-token bypass is excluded because it is a single static credential with an empty token id, which also means it bypasses per-token rate accounting — any quota keyed on a token id would be unenforceable for it." ... "Sharing the other package's authenticate() would mean a future token shape added there silently gains access here. Keeping them separate makes that impossible." (:68-78)
- **If removed:** THIS IS THE DIRECT COLLISION WITH THE SETTLED DESIGN. 'OAuth through claude.ai is the only credential mechanism' is exactly what this comment refuses, by name, with a reason that single-user does not touch: the objection is not about tenancy, it is that the connector's scope set is fixed by the provider so the human cannot withhold `comm` from it. Single-user dissolves nothing here — one human still may not want a cloud connector able to drive their local sessions. If the rewrite adopts OAuth it must ANSWER this comment (e.g. per-workspace consent, a scope the human can withhold, or an explicit acceptance that the connector can address every workspace), not inherit it. Removing the check without answering it would be unnoticeable until it was not.

### `TouchToken` called from the COMM authenticator

- **Where:** `internal/commserver/auth.go:114-118`
- **Verdict:** survives
- **Prevents:** A credential in active use reporting `last_used_at` NULL, so nobody can tell during an incident whether it is live.
- **Stated reason:** "Prod observed three comm tokens reporting last_used_at NULL after 102 acknowledged messages, because TouchToken was only ever called from the knowledge-base authenticator. A token whose last use is unknown cannot be reasoned about during an incident."
- **If removed:** Silently: everything works and the console lies. Already shipped once, exactly this way. The lesson generalises — every credential path must record use, and a new path added to a rewritten auth layer is precisely where this gets forgotten again.

### No CORS headers at all on /comm; 1 MiB body cap

- **Where:** `internal/commserver/auth.go:31-36, :81-87`
- **Verdict:** survives
- **Prevents:** Browser-originated calls to a surface that has no browser client, and oversized request bodies on a surface whose payloads are model-generated tool arguments.
- **Stated reason:** "No CORS headers at all: unlike /mcp, this endpoint has no browser client, so a permissive Access-Control-Allow-Origin would be pure attack surface." (:81-83); "nothing here legitimately approaches a multi-megabyte payload — tool arguments are generated token-by-token by a model, so a large body is a design error long before it is a transport one." (:33-36)
- **If removed:** Pure outside-world hardening; single-user changes nothing. Removal would be unnoticeable until exploited, which is the definition of the catastrophic half of the brief's question.

### `host_hint` is stored opaquely and NEVER consulted for authorization

- **Where:** `internal/comm/endpoint.go:52-54; internal/comm/migrations/0001_init.sql:44-51; docs/COMM.md:313-334`
- **Verdict:** survives
- **Prevents:** A self-reported machine identity being used to authorise a same-host filesystem handoff.
- **Stated reason:** "It is NEVER authorization: it is self-reported and therefore spoofable, it compares EQUAL across cloned VM images and UNEQUAL across a bind mount, and two sessions truly on one host may still run as different OS users. Proof of a shared filesystem is a rendezvous (write a nonce, echo it back) ... An absent hint must never match another absent hint."
- **If removed:** Two sessions on cloned VM images would 'prove' co-location and hand each other file paths that do not resolve, or worse, resolve to the wrong file. The generalisable rule for the new layer: a client-asserted attribute may narrow an attempt but must never authorise one — which is also the correct reading of 'the folder id is not a secret; knowing it authorises nothing'. The id must be treated as a HINT that the human's approval then authorises, never as proof by itself.

### `endpoint_id` is opaque and server-minted; `label` is decoration and never an address

- **Where:** `internal/comm/migrations/0001_init.sql:33-35, :54, :59; internal/comm/endpoint.go:56`
- **Verdict:** survives
- **Prevents:** A global name namespace in which one session squats the name another expects and receives its messages.
- **Stated reason:** "endpoint_id is opaque and server-minted: routing is ALWAYS by this id, never by `label`, or the first release would ship a global namespace where one session can squat the name another expects and receive its messages."
- **If removed:** Mail delivered to the wrong workspace because two folders chose the same friendly name — silent, and it looks like the peer simply did not reply. This is precisely the settled design's 'stable opaque id' and it should be carried over verbatim; note it must remain SERVER-minted, because an id the client chooses is a name it can squat.


---

## Station keys and the binding voucher

*34 controls — 19 survive, 8 survive on a different key, 7 dissolve conditionally, 0 dissolve outright.*

### Station key is a HEADER credential only — never a tool argument (`kens_` bearer on /station)

- **Where:** `internal/stationserver/auth.go:22-25; docs/STATIONS.md:174-177`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** The long-lived station credential being written into a conversation transcript, harness log, scrollback or a notebook page that later lands in a backup — i.e. the credential leaking through ordinary logging that Ken does not control.
- **Stated reason:** "The key is a HEADER credential and never a tool argument. Tool arguments are model output: they land in transcripts, harness logs and scrollback, and — via the notebook — potentially in a backup. A long-lived credential travels the way the Ken token already travels, read by the transport and never spoken by the model."
- **If removed:** Nothing breaks functionally and no test fails — the key works identically as an argument. The leak is invisible: it is recorded by software neither end ships (TARGET-ARCHITECTURE.md:192-196 records exactly this happening — the key 'was burned on arrival'). Removal is undetectable and catastrophic. Under the settled design the control dissolves only because the thing travelling stops being a secret: a stable opaque workspace id in the MCP header authorises nothing, so there is nothing left to keep out of the transcript. If the new id ever gains authority, this control must come back.

### The binding voucher itself — a short-lived, single-use indirection so the station key never crosses to /comm

- **Where:** `internal/store/station_binding.go:20-31`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** The station key being passed as a tool argument to a comm tool in order to prove station membership — i.e. converting a station-lifetime credential into transcript content.
- **Stated reason:** "an endpoint should belong to a station, but the only credential that proves station membership is the station key — and that key must never appear as a tool argument... The blast radius of a leaked voucher is one binding inside a few minutes; the blast radius of a leaked station key is the station."
- **If removed:** Binding would need the station key on the comm surface, which re-opens the leak the whole mechanism exists to close — and nothing would report it. Under the settled design the entire mechanism dissolves: one OAuth identity spanning both surfaces has nothing to hand across, and every voucher property below (TTL, single-use, endpoint-naming, actor-matching, hash-at-rest, sweep) dissolves with it. This is the single largest deletion the new layer can make.

### Voucher stored hashed, never in cleartext

- **Where:** `internal/store/station_binding.go:110-124`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A ken.db backup copied off-box containing replayable binding credentials.
- **Stated reason:** "It is short-lived and single-use, which is an argument for a small blast radius — not an argument for being the one credential kept in cleartext, and least of all in ken.db, which is the file the backup story copies off-box. BACKUP.md's guarantee is 'no credential Ken STORES is replayable'; a plaintext voucher would have made that false the day it shipped."
- **If removed:** Nothing observable; the five-minute window makes the exposure genuinely small, which is exactly the argument the comment pre-empts. Dissolves with the voucher — but the RULE behind it (every stored credential is hashed, no exceptions for short-lived ones) must carry into the new layer.

### VoucherTTL = 5 minutes, enforced by `expires_at > now` inside the redemption UPDATE

- **Where:** `internal/store/station_binding.go:33-36, 190`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A voucher sitting live in a transcript for hours after the session that asked for it has moved on.
- **Stated reason:** "VoucherTTL is deliberately short. A voucher is redeemed by the same session that asked for it, in its very next tool call, so minutes is generous — and every additional minute is time a value sitting in a transcript stays live."
- **If removed:** NOTHING WOULD NOTICE — and this is the clearest instance of the project's recurring defect in my lens. No test in internal/store/station_voucher_test.go exercises expiry (the file tests wrong-endpoint, wrong-actor, single-use, no-nomination, pre-migration NULLs and archived-station, and nothing else). With the predicate gone, the hourly janitor (cmd/ken/main.go:640-645) would still delete expired unredeemed rows, so the practical window silently widens from 5 minutes to up to an hour and every test stays green. If the TTL concept survives into the new design in any form, it needs a clock-advancing test, because the current suite cannot tell an enforced TTL from an unenforced one.

### Single-use — `redeemed_at IS NULL` in a conditional UPDATE, not a read-then-write

- **Where:** `internal/store/station_binding.go:139-142, 186-192`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A voucher binding more than one endpoint, and two concurrent registrations racing on one voucher both succeeding.
- **Stated reason:** "Redemption is a conditional UPDATE rather than a read-then-write, so two concurrent registrations racing on one voucher cannot both succeed: exactly one UPDATE reports a row."
- **If removed:** TestVoucherIsSingleUse (internal/store/station_voucher_test.go:177) fails immediately — this one IS covered. The race half is not covered by any test, only by the statement shape. Dissolves with the voucher; the shape lesson (make single-use a conditional UPDATE, never a check-then-act) transfers to any one-shot approval token in the new layer.

### THE ACTOR RULE — `issued_to_actor=?` must equal the redeeming endpoint's actor (the setup guard, NOT the security property)

- **Where:** `internal/store/station_binding.go:151-155, 192; migrations/0014_voucher_holder.sql`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Nothing, as security — it was shipped as the fix and was not one. What it actually catches is a SETUP error: a station key minted under a different actor than the machine's comm token, a misconfiguration with no other symptom that silently defeats the hearsay marker forever.
- **Stated reason:** "byActor must be the actor the voucher was issued to. This is the SETUP guard. It catches a station key minted under a different actor than the machine's comm token — a misconfiguration that otherwise has no symptom at all until it silently defeats the hearsay marker (see IssueStationKey). It is defence in depth for (1), never a substitute." And: "the accompanying claim — that a leaked voucher then grants nothing the comm token does not already grant — was FALSE. A comm token alone registers an UNBOUND endpoint; it confers no station's mail. Binding is precisely the capability it does not give."
- **If removed:** TestVoucherCannotBeRedeemedByAnotherActor fails, so the removal itself is caught. Under single user the actor concept collapses (one human, one Claude account) and this check has nothing left to compare. BUT THE FAILURE IT DETECTS DOES NOT DISSOLVE: 'the write-time provenance identity does not match the traffic-receiving identity' is a real, silent, still-possible bug in any design that keys provenance on one identity and authorises on another. Delete the check only if the new layer makes the two identities the same object by construction — and if it does, say so where the marker is computed, or the next reader will assume the check was merely dropped. Note also TARGET-ARCHITECTURE.md:206-208: a wrong actor can produce a successful-looking bind into the wrong station, because 'the actor check is not a same-station check, and "the actor matches" reads like "this is the right station"'.

### Actor-selection steering at mint time: `mustStationActor` refuses to guess, `ActorsWithCommStatus` marks comm-token holders, `ListStationKeys` renders each key's actor and badges the ones that cannot bind

- **Where:** `cmd/ken/cli_station.go:164-208; internal/store/station_binding.go:324-380; internal/store/stations.go:296-307; internal/web/station_key_actor_test.go:8-19`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A key minted under the wrong actor — which authenticates perfectly, drives every station tool, silently kills the hearsay marker forever, and only refuses months later at binding, in a different surface.
- **Stated reason:** "A mismatch has no symptom until someone tries to bind: the key authenticates perfectly, every station tool works, and only redemption refuses — which is months later and in a different surface. Showing it next to the key turns an invisible misconfiguration into a visible one." And on why it is not enforced at mint: "refusing at mint time would block the legitimate case of a deployment that has no comm token yet, and stations run with COMM off by design."
- **If removed:** Nothing fails; the badge simply stops rendering and the misconfiguration goes back to being invisible. And it is ALREADY half-broken in the direction that matters: the console mints under `sess.ActorID`, the logged-in human (internal/web/stations.go:499), while the comm-token path is hardcoded to `kind='ai'` — TARGET-ARCHITECTURE.md:198-204 records 'the first console-minted station key ever created carries actor human:admin' and 'The console diagnoses the problem and cannot fix it... on a form whose only field is label'. The steering dissolves under one identity — which is precisely the argument for the re-implementation. The lesson to carry: when a control cannot be enforced, the fallback of 'make the right thing the default and show the property' still needs a test that the display is truthful, or the remedy text becomes a lie.

### `station` scope required at the transport, and a key without it is 'not a station key, whatever its prefix'

- **Where:** `internal/store/stations.go:456-459; internal/stationserver/auth.go:98-101`
- **Verdict:** survives, on a different key
- **Prevents:** A `ken_`-family token that happens to carry a `kens_`-shaped string, or a scope-narrowed key, reaching station tools it was not minted for.
- **Stated reason:** "A key with no station scope is not a station key, whatever its prefix." and (auth.go:27-33) "splitting a shipped scope is a MAJOR, merging two is free".
- **If removed:** Nothing observable — every key the tooling mints carries the scope. Under one OAuth grant per instance, scope-as-credential-partition mostly dissolves; what survives is the operator's ability to withhold a capability family (the same reason commserver refuses OAuth entirely, auth.go:69-73).

### `requireStation` — a station-less key may call exactly one tool, station_request

- **Where:** `internal/stationserver/auth.go:60-72`
- **Verdict:** survives, on a different key
- **Prevents:** A bootstrap key (no station yet) reading or writing any station's notebook, tasks or locker while it waits for a human to create its station.
- **Stated reason:** "StationID is empty for a station-less key. Such a key may call exactly one tool, station_request, which is how a session with no station asks for one (S3)."
- **If removed:** A bootstrap key would fall through with StationID="" and every station query would run against the empty string — likely returning nothing rather than erroring, so it fails quietly rather than loudly. Under the settled design the whole state disappears: a new folder is auto-named and fully working with no approval, so there is no credential-without-a-workspace phase left to guard.

### THE ENDPOINT RULE — `issued_for_endpoint=?` must equal the redeeming endpoint (the security property)

- **Where:** `internal/store/station_binding.go:147-149, 191; migrations/0015_voucher_nominates_endpoint.sql`
- **Verdict:** survives, on a different key
- **Prevents:** A leaked voucher being usable by anyone but the session it was minted for. Redeeming requires that endpoint's own secret — a separate credential the voucher does not carry — so a leaked voucher is inert in other hands. Concretely it prevents another session binding ITS endpoint to your station's inbox and reading your mail.
- **Stated reason:** "endpointID must be the endpoint the voucher NAMED. This is the security property. Redeeming therefore requires that endpoint's own secret, which the voucher does not carry, so a leaked voucher is inert in anyone else's hands." And migration 0015: "SIX of their eight stations share one actor... the voucher inherited a WEAKER binding than its own issuer."
- **If removed:** TestVoucherCannotBeRedeemedByAnotherEndpointUnderTheSameActor fails, so removal is caught. But note WHAT it protects under single user: not tenant-from-tenant, but WORKSPACE-from-WORKSPACE. One human's session in folder A must not capture folder B's inbox. That failure is a mistake, not an attack, and it survives the single-user collapse fully. Whatever replaces the voucher must still bind an approval to ONE requesting party, not to 'anything on this machine'.

### A rejected redemption does not burn the voucher (identity predicates live IN the UPDATE's WHERE, so a mismatch matches no row)

- **Where:** `internal/store/station_binding.go:185-192; internal/store/station_voucher_test.go:100-132`
- **Verdict:** survives, on a different key
- **Prevents:** Anyone holding a leaked voucher denying the legitimate session its one binding — turning a confidentiality bug into a denial of service against the one operation a session cannot complete any other way.
- **Stated reason:** "If the UPDATE matched on the hash alone and rejected afterwards, anyone holding a leaked voucher could burn it the instant it was issued — turning a confidentiality bug into a denial of service against binding, which is the one operation a session cannot complete any other way."
- **If removed:** Caught by TestRejectedRedemptionDoesNotBurnTheVoucher. The property is structural (predicates in the WHERE clause), not a separate guard — which is the right shape. Any one-shot approval in the new layer inherits the same trap: validate inside the claiming statement, never before it.

### Binding is split OUT of comm_register into comm_bind

- **Where:** `internal/commserver/commserver.go:256-262; docs/STATIONS.md:234-252`
- **Verdict:** survives, on a different key
- **Prevents:** Two things: a failed binding destroying the one-time endpoint secret the same call had just minted (the MCP SDK discards structured output on error), and a voucher passed to registration being unable to name its redeemer because no endpoint id exists yet.
- **Stated reason:** "These were once one call, and the seam leaked: a failed binding could not be reported as an error without the SDK discarding the structured output that carried the one-time secret, so the handler grew a 'succeeded but did not bind' result to avoid destroying a credential it had just minted. Splitting them removes the conflict instead of managing it." And S5a: "A voucher passed to registration cannot name its redeemer, because the endpoint does not exist yet."
- **If removed:** Recombining would silently reintroduce a one-time-secret-losing path. Under the settled design, OAuth removes the one-time secret entirely and the hazard with it — but the underlying rule survives and is worth stating in the new docs: never let a call that mints a shown-once credential also perform a second operation that can fail.

### `ep.Owner.TokenID != p.TokenID` — the endpoint must belong to the token making the call

- **Where:** `internal/commserver/commserver.go:1084-1086`
- **Verdict:** survives, on a different key
- **Prevents:** An endpoint id/secret pair being driven through a different machine's comm token.
- **Stated reason:** none stated (bare check, no comment)
- **If removed:** Nothing visible: anyone reaching this point already holds the endpoint secret, so the practical effect is narrow. The absence of any stated reason is itself the finding — this is the one guard in the lens with no recorded rationale, so a re-implementer cannot tell whether it is defence in depth or load-bearing. Under one human it becomes 'this endpoint belongs to this workspace', which is worth keeping, but state the reason this time.

### Retire vs Revoke as two verbs, with no third

- **Where:** `internal/store/stations.go:358-386; docs/STATIONS.md:254-276`
- **Verdict:** survives, on a different key
- **Prevents:** A rotation that severs live COMM endpoints when the operator only meant to stop future binds.
- **Stated reason:** "'Retire does not sever' is about ENDPOINTS, and is misleading about the KEY itself... The safe order is: mint → install in the client config → restart the session → verify the new key works → only then retire. Stated because the natural reading of 'non-severing' is that rotation is seamless, and it is not."
- **If removed:** The distinction lived in six operator-facing strings that kept promising the old behaviour for four releases after the code changed, found only by reading the auth query. That history is the warning: if the new layer keeps two stop-verbs, the difference must be enforced by a test on the AUTH path, not described in a tooltip.

### `FindActor` resolves without creating; `ActorExists` validates form-supplied ids

- **Where:** `internal/store/station_binding.go:382-407`
- **Verdict:** survives, on a different key
- **Prevents:** A typo at mint time inventing a new actor, producing a key that authenticates perfectly and marks nothing; and a connector's writes being attributed to an identity that is not its own — the one kind of wrong the curation gate cannot repair afterwards.
- **Stated reason:** "Distinct from FindOrCreateActor on purpose: minting a station key should never invent an actor, because a typo would then produce a key that authenticates perfectly and marks nothing." And: "An unvalidated id there would attribute one connector's writes to another identity, which is the one kind of wrong the curation gate cannot repair afterwards."
- **If removed:** Invisible — the typo'd key works. Under a single identity there is nothing to typo, so the control dissolves in form; what survives is the rule that an identity used for ATTRIBUTION must never be created as a side effect of using it, because a curated record's authorship cannot be repaired after the fact.

### Station key format guard + constant-time compare + hash-at-rest (`kens_<id>_<secret>`, sha256 stored)

- **Where:** `internal/store/stations.go:428-453`
- **Verdict:** survives
- **Prevents:** Timing-oracle recovery of a key secret, and a ken.db backup copied off-box handing over every live station credential in replayable form.
- **Stated reason:** BACKUP.md's guarantee is quoted in the sibling control at internal/store/station_binding.go:118-119: "no credential Ken STORES is replayable".
- **If removed:** Every test still passes (correct keys authenticate either way). Nobody notices until a backup leaks or someone measures timing. Classic invisible-failure control — keep it in any re-implementation, including one where the credential is an OAuth-issued token.

### Auth query requires `revoked_at IS NULL AND retired_at IS NULL`

- **Where:** `internal/store/stations.go:441-442`
- **Verdict:** survives
- **Prevents:** A revoked or retired key continuing to read the notebook, task list, locker and vault. This is the ONLY place retirement is enforced — RetireStationKey just stamps a column.
- **Stated reason:** "IT DOES NOT 'LEAVE LIVE ONES ALONE', and six shipped strings said it did until 2026-08-14. AuthenticateStationKey requires `retired_at IS NULL` (below) and the middleware re-authenticates EVERY request, so the holder loses the notebook, task list, locker and vault at its next call." (internal/store/stations.go:358-364)
- **If removed:** Revocation and retirement become pure theatre with no error anywhere: the console still shows the key as revoked, the key still works. The operator's belief and reality diverge silently. This is the highest-value single predicate in the lens.

### Unknown, retired and revoked keys are refused with ONE identical message

- **Where:** `internal/stationserver/auth.go:90-97; internal/store/stations.go:428-459`
- **Verdict:** survives
- **Prevents:** An unauthenticated prober enumerating which key ids exist, or learning that a guessed key was once real, by diffing refusal strings.
- **Stated reason:** "Unknown, retired and revoked keys are refused identically — extending COMM's unprobeability rule. A caller learns WHY only after its own credential has verified, which informs a holder and tells a prober nothing."
- **If removed:** No functional change and no failing test — legitimate callers never see these strings. The leak is only visible to someone probing. Survives single-user because the threat is the outside world, not another tenant; it weakens only if /station is made unreachable from outside the machine.

### `withCaller` — the principal is re-derived from THIS call's Authorization header, not from the connection

- **Where:** `internal/stationserver/stationserver.go:950-978; internal/stationserver/session_identity_test.go:15-27`
- **Verdict:** survives
- **Prevents:** A tool acting on whichever station opened the MCP session rather than the station whose key made the call — writing into another post's notebook, closing another post's tasks, reading another post's locker.
- **Stated reason:** "The go-sdk binds a session to the INITIALIZE request's context, so anything a handler reads from its context is frozen at connect. On the knowledge-base surface that misattributed authorship; here it is worse in kind, because a station key IS the station... It is latent while a client uses one key per connection. It stops being latent under the one-identity model, where one grant serves several capability families a human can retarget after consent."
- **If removed:** Latent today (one key per connection), so removal breaks no test and no user. The comment names the exact condition that makes it live — and that condition IS the settled design (one OAuth grant, retargetable after consent). This control must be built in from day one of the re-implementation, not retrofitted.

### `TouchToken` on the station surface — last_used_at written per request (throttled)

- **Where:** `internal/stationserver/auth.go:110-122`
- **Verdict:** survives
- **Prevents:** An incident where a station key is suspected stolen being unanswerable in principle, and an operator reading a permanently blank last-used column as 'unused' when it means 'unmeasured'.
- **Stated reason:** "An operator reads a blank as 'unused' rather than 'unmeasured', which is the worse of the two readings, and it made 'retire the key nothing is using' unanswerable even in principle. It also means a stolen station key could read an entire notebook, task list and briefing with no trace at all."
- **If removed:** Nothing fails; a column simply stays NULL. This already shipped broken for several releases and was found only by reading the auth query. Removal is unnoticeable and directly costs incident answerability. Note the acknowledged remaining gap (TARGET-ARCHITECTURE.md:216-219): no column in either database records a request source address, so 'was this credential used by someone else' stays unanswerable.

### Per-token rate bucket on /station, separate from the knowledge base's

- **Where:** `internal/stationserver/auth.go:102-108; internal/commserver/auth.go:100-113`
- **Verdict:** survives
- **Prevents:** A polling or retry loop on one surface starving the machine's kb_* calls and, past the per-IP strike threshold, locking the machine out entirely.
- **Stated reason:** "the operating convention is one token per machine, so a comm poll loop sharing the KB's budget could starve that machine's kb_* calls (and, past the per-IP strike threshold, lock the machine out entirely)."
- **If removed:** Invisible until a session enters a retry loop, at which point the human loses an unrelated surface and has no reason to connect the two. Single user makes it MORE relevant, not less: with one human there is nobody else whose quota absorbs the mistake.

### Three deliberately distinct refusals: wrong-endpoint, wrong-actor, and unknown/used/expired collapsed into one

- **Where:** `internal/store/station_binding.go:38-84, 196-227`
- **Verdict:** survives
- **Prevents:** An operator retrying forever against a setup that no retry can fix. The wrong-actor case is fixed only by re-minting a key from the console; reported as 'not valid' it reads as an expiry race and the operator mints fresh vouchers that all fail identically.
- **Stated reason:** "An actor mismatch is a SETUP error, not an attack... Reported as 'voucher is not valid' it looks like an expiry race, and the operator issues fresh vouchers forever, each failing identically." And on why distinguishing is safe: "Collapsing those three protects a secret an attacker might GUESS. This one cannot be reached by guessing: the caller must already hold a live 32-character voucher, which means it was handed to them."
- **If removed:** Caught by TestTheThreeRefusalsAreDistinguishable, which asserts distinct TEXT and not merely distinct types — 'the distinction exists only in the type and no operator will ever see it'. This is the general principle worth carrying whole into the new layer: collapse refusals only where the collapsed thing is guessable; distinguish where the two causes demand opposite responses.

### Pre-migration vouchers refuse rather than grandfather, via `=` never matching NULL

- **Where:** `internal/store/station_binding.go:180-184; migrations/0014_voucher_holder.sql:16-24`
- **Verdict:** survives
- **Prevents:** The bearer-capability hole surviving inside the very upgrade that closes it.
- **Stated reason:** "BOTH ARE NULLABLE, AND A NULL NEVER REDEEMS... The alternative — honouring old rows — would have left the bearer hole open inside the very change that closes it. Relying on NULL-never-equals is stated here because it is invisible at the call site and reads like an oversight."
- **If removed:** Covered by TestPreMigrationVoucherIsRefusedRatherThanGrandfathered, which includes a control redemption so the assertion cannot pass vacuously. The transferable rule for the estate migration ahead: a migration that adds an authorising column must make old rows fail closed, and must say so in the migration, because the mechanism is invisible at the call site.

### Station must be `state='active'` at redemption, AND the archived case is refused at ISSUE too

- **Where:** `internal/store/station_binding.go:232-243; internal/stationserver/stationserver.go:289-296`
- **Verdict:** survives
- **Prevents:** A voucher minted before an archive binding an endpoint afterwards (a hole through S3's 'an archived station's keys stop binding'), and separately, a session receiving a perfectly-formed voucher that redemption will always reject with three wrong reasons.
- **Stated reason:** At issue: "Refuse at ISSUE for an archived station, rather than handing out a voucher that redemption will always reject... comm_register would report it as 'unknown, already used, or expired', which is three wrong reasons that send the session looking for a problem it does not have." At redeem: "honouring a voucher minted before the archive would be a hole straight through that."
- **If removed:** The redeem-side check is covered (TestVoucherForAnArchivedStationRefuses). The issue-side check is pure diagnosis quality — removing it changes a clear refusal into a misleading one, and no test would notice. Archiving a workspace survives single-user entirely: it is the human retiring their own post.

### The voucher takes NO station_id argument — the station comes from the header key alone

- **Where:** `internal/stationserver/types.go:310-322`
- **Verdict:** survives
- **Prevents:** A session asking to be bound to a station it holds no key for.
- **Stated reason:** "There is deliberately still NO station_id argument: the station is decided by the key in the Authorization header, never by anything the model says, or a session could ask to be bound to a station it holds no key for. EndpointID is the opposite case — it is safe as an argument precisely because it is not a credential. It NARROWS the voucher rather than widening it."
- **If removed:** There is no test for an argument that does not exist — the control is the absence of a field, which is the most silently reversible kind. A future 'convenience' station_id parameter would reintroduce the hole with nothing failing. The narrowing-vs-widening distinction is the reusable rule: a model-supplied argument is safe exactly when lying about it can only reduce what you get.

### The voucher records the issuing key's token_id (`p.TokenID`, never caller-supplied)

- **Where:** `internal/store/station_binding.go:86-91; internal/stationserver/stationserver.go:297-299`
- **Verdict:** survives
- **Prevents:** Revocation being unable to find what a leaked key bound — a revocation that stops future bindings while the leaked capability keeps running.
- **Stated reason:** "tokenID is recorded so revoking that key can later sever every endpoint it bound (S6). Without it, revocation would stop future bindings but leave the leaked capability running — which S6 calls theatre, correctly."
- **If removed:** Binding still works perfectly; only revocation quietly becomes partial. The whole severing chain below depends on this one recorded field. Survives single-user: 'this credential leaked' and 'cut off that runaway session' are both still real for one human.

### `comm_bind` refuses an endpoint that is already bound (no moving between stations)

- **Where:** `internal/commserver/commserver.go:280-287; internal/comm/endpoint.go:184-200`
- **Verdict:** survives
- **Prevents:** An endpoint carrying station A's unread mail into station B — the shared-inbox accident in a new costume.
- **Stated reason:** "Already bound is refused rather than re-pointed. Moving an endpoint BETWEEN stations would carry the first station's unread mail into the second — the shared-inbox accident in a new costume. Binding one that has NO station carries nothing across, which is why that direction is allowed and this one is not."
- **If removed:** Enforced twice (handler check plus `station_id IS NULL` in the UPDATE's WHERE at endpoint.go:196-197, 236-240), so the belt-and-braces makes accidental removal unlikely — but the failure would be delivery of one workspace's mail to another, which is a data-disclosure between the human's own folders. Survives unchanged.

### comm_bind authenticates the endpoint (id + secret) BEFORE redeeming — so redemption costs a second, independent credential

- **Where:** `internal/commserver/commserver.go:274-291; internal/commserver/commserver.go:988-1010 (header-carried endpoint credential)`
- **Verdict:** survives
- **Prevents:** The voucher being a bearer capability. This is what makes the endpoint rule above actually bite: naming an endpoint is worthless without holding that endpoint's secret.
- **Stated reason:** "Redeeming therefore requires that endpoint's own secret, which the voucher does not carry, so a leaked voucher is inert in anyone else's hands." And on the header path: "It does NOT authenticate: auth() below still does that, so every check that hangs off a verified secret — the severed station key, the archived station — stays in one place and cannot be bypassed by arriving through a different door."
- **If removed:** The endpoint rule degrades to 'name any endpoint you like' with no test failing at the store layer, because the store never sees whether the caller proved possession — the proof happens one layer up. The 'one authentication choke point, every derived check hangs off it' shape is the part to carry forward verbatim.

### Station-key revocation is enforced at USE, not at revoke time (`IsStationKeyRevoked`)

- **Where:** `internal/store/station_binding.go:292-305; internal/commserver/commserver.go:1029-1041`
- **Verdict:** survives
- **Prevents:** A revocation issued from `ken token revoke` — a separate process with no comm.db handle — never reaching the endpoints that key bound, leaving the leaked capability live.
- **Stated reason:** "severing cannot be made reliable at the REVOKING end. Both revoke paths — the /tokens console and `ken token revoke` — go through RevokeToken, and the CLI runs in a SEPARATE PROCESS with no comm.db handle at all... Making the check happen at USE instead means every revocation path works, including ones added later that forget about stations: it fails closed by construction rather than by remembering."
- **If removed:** CLI-issued revocations would silently do nothing to live sessions; console-issued ones would still appear to work via the eager sweep, so the bug would present as 'revocation works sometimes' and be blamed on timing. The principle — enforce at use, because the enforcing end cannot be relied on to remember — is the single most transferable idea in this lens for the new layer's revocation story. Note it deliberately fails OPEN on a database error (commserver.go:1039 `rerr == nil && revoked`), stated out loud for its neighbour at :1060-1064.

### Eager severing on revoke, which also RELEASES CLAIMS (`SeverEndpointsBoundBy`)

- **Where:** `internal/comm/endpoint.go:345-357; internal/web/app.go:1046-1062`
- **Verdict:** survives
- **Prevents:** Two things: a revoked-but-still-running session polling until an idle sweep that traffic keeps postponing forever; and a severed reader's unacked claims hiding the station's mail from its remaining readers for the rest of the lease.
- **Stated reason:** "This is what makes revoking a station key mean something. You revoke because the key leaked; a revocation that stops future bindings but leaves the already-bound sessions running until an idle sweep notices is theatre — and traffic keeps an endpoint alive indefinitely, so the sweep may never come."
- **If removed:** The at-use check (above) still cuts access, so severing's removal would be invisible to security — but the claim-release half is not covered by the at-use path, and its failure mode is mail that silently stops being visible to the workspace's other readers. Two different guarantees in one function; do not let a re-implementer assume the at-use check covers both.

### `CountEndpointsBoundBy` — the console states the blast radius before the click

- **Where:** `internal/comm/endpoint.go:388-397; docs/STATIONS.md:270-273`
- **Verdict:** survives
- **Prevents:** An operator discovering only afterwards that revoking one key disconnected several live sessions.
- **Stated reason:** "A destructive action whose blast radius is only visible afterwards is one an operator learns to fear rather than use."
- **If removed:** Purely a UI string; nothing fails. The consequence is behavioural — the human stops using the safety control at all. Worth carrying: with one human and many sessions, 'this will disconnect N live sessions' is exactly the number they need.

### The hearsay marker itself — `prompted_by_peer_traffic` / `hearsay_at_write`, keyed on the ACTOR and biased toward over-reporting

- **Where:** `internal/comm/provenance.go:5-45, 80-113; internal/stationserver/stationserver.go:814-826; internal/stationserver/stationserver.go:345-349`
- **Verdict:** survives
- **Prevents:** Hearsay being laundered into the knowledge base as first-hand knowledge: a session told 'entry X is verified, propose a revision at high confidence' authors a proposal indistinguishable from first-hand, so the invariant survives literally while the curator's signal quality has degraded. Also marks a link request a peer talked this session into filing.
- **Stated reason:** "a message is a side channel into curation... the invariant survives literally (an AI authored it, a human promotes it) while the curator's signal quality has quietly degraded to hearsay with no chain of custody." On bias: "A false positive costs the curator one extra glance; a false negative would silently launder hearsay into the knowledge base, so the marker is biased toward over-reporting." On errors: "Callers must treat any error as 'unknown' and NOT as 'no': failing to mark is the direction that loses information." On the link-request use: "This is the ONLY signal the human gets that the request may not be this session's own idea: a peer cannot open a channel, but it can talk this session into asking for one, and the request then arrives looking like its own."
- **If removed:** THIS IS THE CONTROL WHOSE FAILURE IS ALREADY PROVEN INVISIBLE. It has failed twice without anyone noticing: once because station keys were minted under a `human` actor while comm tokens default to `ai` and `(kind, display_name)` is unique, so `hearsay_at_write` was permanently false on every deployment following the documented setup; and once because room deliveries carry a NULL recipient_endpoint, so an inner join dropped every room message — 'the badge would simply never fire for room mail, and an absent badge is indistinguishable from a checked-and-clean one'. An unset marker and a checked-clean marker are the same bit. It survives single-user completely: one human's own sessions influencing each other is exactly the failure it detects, and the settled design (many sessions, one human, cross-workspace channels) makes it MORE necessary. But it must be re-keyed onto the new identity, and the new layer must ship a positive test that the marker FIRES, not just that it can be read. Prod already measured the badge as 'nearly always on' (provenance.go:54-59), so the directed/broadcast split at provenance.go:60-76 is the part that makes it informative rather than ignorable.

### Voucher sweep keeps redeemed rows, deletes only expired unredeemed ones

- **Where:** `internal/store/station_binding.go:253-266; cmd/ken/main.go:637-645`
- **Verdict:** survives
- **Prevents:** Losing the trail that answers 'which key bound this endpoint' — the first question asked when a station key turns out to have leaked; and unbounded growth in a backed-up database.
- **Stated reason:** "Redeemed ones are KEPT: they are the trail answering 'which key bound this endpoint', which is the first question asked when a station key turns out to have leaked." And on cadence: "Hourly is right — a voucher lives five minutes, so this is about unbounded growth in a BACKED-UP database, not latency."
- **If removed:** A sweep that also deleted redeemed rows would look tidier and break nothing until an incident, at which point the answer is simply gone. The audit-trail-vs-garbage distinction must be explicit in the new layer's retention rules, because both rows look equally dead.

### No CORS headers and a 1 MiB body cap on /station and /comm

- **Where:** `internal/stationserver/auth.go:77-83, 35-38; internal/commserver/auth.go:81-87, 31-36`
- **Verdict:** survives
- **Prevents:** A browser-origin request reaching a surface that has no browser client, and an oversized body being accepted on a surface whose arguments are all model-generated.
- **Stated reason:** "No CORS headers: like /comm/mcp this endpoint has no browser client, so a permissive Access-Control-Allow-Origin would be pure attack surface." And: "tool arguments are generated token-by-token by a model, so a large body is a design error long before it is a transport one."
- **If removed:** No test, no symptom, no legitimate caller affected. Survives single-user because the threat is the outside world reaching a host that is reachable. Cheap to keep; keep it.


---

## Establishing a conversation — pairing codes, links, channels

*35 controls — 26 survive, 7 survive on a different key, 1 dissolve conditionally, 1 dissolve outright.*

### A pairing code may only pair endpoints within its own space (space_id equality check at redeem).

- **Where:** `internal/comm/channel.go:88-94`
- **Verdict:** **dissolves — refutation failed**
- **Prevents:** A code minted by one human seating an endpoint owned by another human — cross-owner channel establishment.
- **Stated reason:** "A code may only pair endpoints within its own space. Today there is one space, so this cannot fire; it is here because it must be true before a second human exists, not after." (channel.go:88-91). Backed by COMM.md §10:827-831 — ownership keyed on space_id "deliberately not on the actor alone, because actors are resolved by display name and therefore collapse across machines and humans".
- **If removed:** Nothing breaks and nothing is noticed — TODAY OR EVER, by the code's own admission ("this cannot fire"). This is the one control in the lens that the settled facts genuinely retire: the second human it was built for is now a FEDERATION service between instances, not a second space inside one. Delete it with the space column, but only if federation is truly out-of-process; if any cross-instance path is ever added in-process, it comes back and it comes back untested, because it has never fired.

### Both-sides-join: the first redeem creates a PENDING channel, the second opens it.

- **Where:** `internal/comm/channel.go:53-63; docs/COMM.md:244-248,289-292`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Nominally, a unilateral "A opens a channel to B" that would have to tighten into an accept flow when a second human exists.
- **Stated reason:** "Both sides call this even though both currently share one owner — turning a unilateral 'A opens a channel to B' into an accept flow later would tighten an already-shipped tool, which is a breaking change." (channel.go:57-60). COMM.md:289-292: "both-sides-join is also what keeps the multi-user future additive (§10) — a unilateral 'A opens a channel to B' would have to tighten into an accept flow later, which is a breaking change."
- **If removed:** Nothing is lost and something is gained — this is the one control the settled facts actively retire, and Ken has ALREADY retired it twice in place. The stated reason is a multi-user future that is now a separate federation service. And it has a measured cost: requiring both ends present meant an approval could materialise nothing — "`proxmox-servers` held a station and no endpoint for five days, so an approval during that window materialised nothing and the granted permission had nothing to spend it on" (COMM.md:275-279). The successor is already written down: "A pair scope is derived from the two ids, so it needs neither side to be online" (COMM.md:279-280), and link approval opens directly because "A link has already established that both stations may talk, so there is nothing to wait for" (channel.go:472-474). Build the new layer on the derived-scope shape; do not re-derive rendezvous.

### Pairing code minted ONLY by the console; no MCP tool can produce one. Route sits behind requireAuth+CSRF.

- **Where:** `internal/comm/channel.go:30-52; internal/web/comm.go:176-204; internal/web/app.go:202`
- **Verdict:** survives, on a different key
- **Prevents:** A session inventing a peer for itself — opening a conversation with another working identity the human never intended to connect. This is the root enforcement of "an agent cannot conjure a channel" on the code path.
- **Stated reason:** "This is COMM's structural gate: an agent cannot conjure a channel, because channel creation requires a value only the human web UI can produce. It is the same move that makes the curation gate trustworthy — withhold the capability rather than instruct the model not to use it — applied at the one place in COMM where it is available." (channel.go:33-37). And web/comm.go:176-178: "This is the human act the whole design depends on: the operator gives the code to exactly the two sessions they intend to connect, and no agent can produce one."
- **If removed:** Any session could open a channel to any endpoint. NOTHING would notice: a conjured channel is byte-identical to an authorised one (same row, same owner_actor_id, same console listing) — there is no field recording which path created it. The mechanism can be replaced (OAuth consent + the one-per-pair approval), but the SHAPE must be: the capability is withheld from the tool surface, not requested politely. If the new layer lets a session write its own authorisation row, this invariant is gone regardless of how the credential is obtained.

### Pairing code TTL — 900s default, enforced in the redeem WHERE clause.

- **Where:** `internal/comm/channel.go:46-47,77-80; internal/comm/comm.go:185-186,261`
- **Verdict:** survives, on a different key
- **Prevents:** A code pasted into a transcript, a log, or a chat scrollback staying redeemable indefinitely, so that a later unintended session can seat itself in a conversation the human minted the code for weeks ago.
- **Stated reason:** Only a bare field doc: "PairingCodeTTLSeconds is how long a human-minted pairing code stays valid." (comm.go:185-186). The user-facing rationale is in the join error: "codes are short-lived, 15 minutes by default ... join promptly, because the clock starts when they mint it" (commserver.go:546-549). No design note argues the value.
- **If removed:** Nobody notices — the visible effect is FEWER "expired code" errors, which reads as an improvement. Under the settled design this dissolves outright: C7's own text says the pair scope has "no code, no channel row, no both-sides-join and no expiry" (COMM.md:270-272), and a durable per-pair approval has nothing to expire. Carry the property forward only if the new design has a hand-carried secret at all — if identity is an opaque non-secret id plus OAuth, there is nothing here to keep.

### An expired code and an unknown code are indistinguishable at redeem.

- **Where:** `internal/comm/channel.go:80-85; internal/commserver/commserver.go:540-551`
- **Verdict:** survives, on a different key
- **Prevents:** Probing which codes exist or existed, and any feedback signal that would make guessing a 10-char base62 code cheaper than blind.
- **Stated reason:** "Expired and unknown are indistinguishable on purpose: a caller must not be able to probe which codes exist or existed." (channel.go:81-83); "All causes share one message so the response cannot be used to probe which codes exist." (commserver.go:543-545)
- **If removed:** Invisible — the errors get MORE helpful, and every legitimate caller is better off. Only a probing caller notices. Under single-user the human-vs-human secrecy motive is gone, but the residual threat is real: a session carrying injected instructions probing the estate. Weakened, not dissolved.

### Approval requires state='active' on the link AND both stations active; archiving flips links to dormant.

- **Where:** `internal/store/station_console.go:544-554,577-604; internal/store/stations.go:237-256`
- **Verdict:** survives, on a different key
- **Prevents:** A retired working identity continuing to send and receive; a dormant relationship authorising anything before the human restores it.
- **Stated reason:** "a dormant link (either station archived) does not authorize anything until it is restored." (station_console.go:545-546); "The state filter is the whole point and it matches AreStationsLinked exactly ... Two readers of one rule eventually drift; if a third appears, this predicate is the one to share." (station_console.go:580-583)
- **If removed:** An archived workspace keeps talking while the console shows it retired. Invisible unless someone cross-checks. Note the drift risk is already flagged in the comment and is REAL: AreStationsLinked filters only the link's own state, LinkMirrorRows additionally joins both stations — they agree only because archive writes 'dormant'. Two readers, one rule, held together by a third fact. The new layer should have exactly one predicate.

### station_link_request resolves its target through StationByNameVisibleTo (published-or-linked, never archived, never self), not StationByName.

- **Where:** `internal/stationserver/stationserver.go:326-344; internal/store/stations.go:76-118`
- **Verdict:** survives, on a different key
- **Prevents:** An enumeration oracle over every station name in the space including unpublished ones — and, worse, a correct guess FILING an agent-authored approval request for an identity the human never published.
- **Stated reason:** "THAT CONTRACT WAS VIOLATED BY A CALLER IN THIS REPO ... a name that exists produced a filed request, a name that did not produced a refusal. Two distinguishable outcomes is an enumeration oracle over every station name in the space, including the ones deliberately withheld from station_directory — and worse than reading, a correct guess FILED A REQUEST, putting an agent-authored ask for an unpublished post in front of its human. Found by sweep and confirmed by execution on 2026-08-19." (store/stations.go:79-88). Guarantee stated at stations.go:104-108: "a station the caller cannot see is indistinguishable from one that does not exist."
- **If removed:** It WAS removed, in effect, and shipped that way until 2026-08-19 — the enumeration succeeded silently and nothing reported it. Under single-user the read half thins out (hiding one workspace from another workspace of the same human is a weak motive, and auto-named workspaces with no publication step may make it moot). The WRITE half survives entirely: a guessed identifier must not be able to put an approval request in front of the human. That is the nag vector and the unsolicited-approach vector, and it is unaffected by there being one human.

### comm_open_channel gives ONE byte-identical refusal for every unavailable target, from a package const, with a test comparing the paths byte for byte.

- **Where:** `internal/commserver/commserver.go:225-230,481-520`
- **Verdict:** survives, on a different key
- **Prevents:** Separating "exists" from "does not exist", then "linked" from "not linked" — and the staffing branch echoing the RESOLVED name back, confirming exact casing.
- **Stated reason:** "These three checks used to fail distinguishably ... That is an enumeration oracle. A caller could separate 'exists' from 'does not exist', then 'linked' from 'not linked' — and the third message echoed the RESOLVED name, so guessing 'PROD' confirmed the station is really called 'prod', case and all." and "Every early return below uses errStationUnavailable, which is a package const precisely so the three paths CANNOT drift apart: a single divergent message reopens the oracle, and the test compares them byte for byte." (commserver.go:483-497)
- **If removed:** Invisible, and the change would read as a UX improvement — the code itself records the accepted cost: "a legitimate caller can no longer tell a typo from an unstaffed peer". Under single-user the oracle's value to an attacker drops sharply. What must survive is the ENGINEERING rule, which is general: refusals that must be indistinguishable are ONE const with a byte-comparison test, because three strings drift and drift silently.

### callerIsInRoom tests MEMBERSHIP, not existence, before the helpful "that is a room" error.

- **Where:** `internal/comm/channel.go:712-735`
- **Verdict:** survives, on a different key
- **Prevents:** A helpful error message reopening the enumeration oracle that comm_open_channel's uniform refusal exists to close.
- **Stated reason:** "Membership, not existence, and the difference is a security property rather than a nicety. My first version asked only whether the room existed, and the test written alongside it caught the consequence immediately: a station that is NOT in a room got told 'that is a ROOM', which confirms its existence. That is precisely the oracle comm_open_channel's uniform refusal exists to close — reopened by a helpful error message, which is how these usually come back." (channel.go:715-722)
- **If removed:** Invisible; it would ship as better diagnostics. The durable lesson outlives the tenancy motive and belongs in the new design's review checklist: every improvement to an error message is a candidate re-opening of an oracle the uniform refusal closed.

### Only SHA-256 of the pairing code is stored; the plaintext is shown once on the page and never again.

- **Where:** `internal/comm/channel.go:30-32,44-46; internal/web/comm.go:44-47`
- **Verdict:** survives
- **Prevents:** A database file, a nightly snapshot, or a Litestream replica yielding a live channel-establishment capability to anyone who can read it.
- **Stated reason:** "MintPairingCode creates a human-authorized pairing code and returns the plaintext exactly once; only its SHA-256 is stored." (channel.go:30-32); "a just-minted pairing code shown ONCE — only its hash is stored, so it can never be shown again" (web/comm.go:44-46)
- **If removed:** Nothing observable, ever, under any test — until a backup leaks. This is the archetype of the catastrophic-invisible control. Note it dissolves WITH the pairing code if OAuth replaces the code entirely; what must not dissolve is the rule that any establishment secret Ken stores is a verifier, not the value (S13/BACKUP.md already split this).

### A consumed code refuses a third joiner; both seats taken ⇒ ErrDenied, and one code can never mint two channels.

- **Where:** `internal/comm/channel.go:161-166`
- **Verdict:** survives
- **Prevents:** A third session seating itself in a two-party conversation and silently reading the whole exchange; and one human authorisation being spent twice.
- **Stated reason:** "Both seats are taken by other endpoints: a third session must not be able to join, and a consumed code must not create a second channel." (channel.go:162-164)
- **If removed:** A code that leaked into a transcript admits an extra reader. This is exactly "an agent enlarged its own audience": the human authorised two parties. Detection would require a human reading the console's channel list and noticing an unexpected second seat — effectively invisible. Under the workspace model the equivalent is: an approval for pair (A,B) must never admit C, however C obtains the identifier.

### A station already seated on the channel re-joins idempotently instead of taking the free seat.

- **Where:** `internal/comm/channel.go:139-160`
- **Verdict:** survives
- **Prevents:** station_a == station_b — a station becoming its own peer, after which ChannelFor resolves its peer to ITSELF and the station reads its own mail as a peer's replies.
- **Stated reason:** "A STATION MUST NOT BECOME ITS OWN PEER. The rowid comparison above answers 'is this exact connection already seated', and the schema's CHECK (endpoint_b <> endpoint_a) enforces only the same literal rowid — so a SECOND endpoint of a station that already holds a seat matched neither, fell through, and took the free one. The channel then had station_a = station_b, and ChannelFor's station arms resolved that station's peer to itself. It is not an exotic path: a replacement session re-redeeming the code its predecessor was given is the ordinary way to reach it" (channel.go:140-152)
- **If removed:** A workspace talks to itself and cannot tell. This is the project's signature defect shape — the failure is indistinguishable from a working conversation. Single-user makes it MORE likely, not less: many sessions per folder, replacement sessions are the norm, and one human names both ends. The new layer must answer 'is this WORKSPACE already a party', never 'is this connection already a party'.

### Only a PENDING channel may be opened, and the state guard is repeated in the UPDATE's WHERE clause.

- **Where:** `internal/comm/channel.go:167-186`
- **Verdict:** survives
- **Prevents:** A human's revocation of a half-formed pairing being silently undone when the second session joins; and a concurrent RevokeChannel landing between the SELECT and the UPDATE.
- **Stated reason:** "Only a PENDING channel may be opened. Without this, a human who revokes a half-formed pairing has their brake silently undone: the code stays valid for its TTL, and the second session's join would flip the revoked row back to 'open'." and "The state guard is repeated in the WHERE clause because the SELECT above and this UPDATE are separate statements: a concurrent RevokeChannel runs on another connection and could land between them." (channel.go:167-177)
- **If removed:** A revoked pairing quietly reopens. The console would show it open, but only if someone re-reads the page after the join — so at the moment it matters, nobody sees it. This is the human's brake against their OWN sessions, which is precisely the class the settled facts preserve.

### The authorising station pair is SNAPSHOTTED onto the channel row on the pairing-code path too (station_a on first redeem, station_b on second).

- **Where:** `internal/comm/channel.go:100-118,178-181`
- **Verdict:** survives
- **Prevents:** A link revocation failing to reach a channel that two linked stations happened to open with a pairing code — the revocation reports success while the traffic continues.
- **Stated reason:** "Migration 0008 moved link revocation onto these columns precisely so authorisation could not be re-derived from a binding an agent can change — but only OpenLinkedChannel was taught to write them. A channel opened by PAIRING CODE between two station-bound endpoints therefore carried NULLs, and the predicate that finds 'open channels between these two stations' could not see it: revoking the link left the channel open, while the console counted zero live channels and reported the revocation as complete." (channel.go:104-113)
- **If removed:** This IS the removal case, historically, and it shipped: revocation returned success, the console printed zero live channels, and the traffic kept flowing. Nobody noticed until the code was re-read. Any new layer with two establishment paths must write the authorisation snapshot on BOTH, or the revocation predicate is blind on one of them and lies about it.

### Revocation predicate reads the authorisation SNAPSHOT on the channel row, never a live JOIN to the endpoint's current station binding.

- **Where:** `internal/comm/channel.go:368-381 (openChannelsBetweenStations); used at 389-401 and 414-434`
- **Verdict:** survives
- **Prevents:** An agent-mutable field (endpoint.station_id, cleared by comm_unbind) making a channel invisible to the revocation meant to end it — or severing an unrelated relationship's traffic.
- **Stated reason:** "Binding is mutable by an agent tool: an earlier version derived the pair at query time, so a single comm_unbind — the path comm_unbind's own description recommends — made a channel invisible to the revocation meant to end it, while the console reported '0 live channels' and the sweep closed none. The mirror case severed an UNRELATED link's traffic. Authorisation is a fact about the past and must not be re-derived from state that has moved." (channel.go:372-380)
- **If removed:** Revocation silently no-ops and the console reports zero. The PRINCIPLE is the single most transferable finding in this lens for the new design: identity is about to travel as "a stable opaque id in the folder's MCP config header" — a file a session can rewrite. Any authorisation re-derived from that header at query time has the same defect. Snapshot the authorising pair at the moment the human approves.

### Channel membership is re-checked on EVERY operation, and a non-member gets ErrNotFound rather than ErrDenied.

- **Where:** `internal/comm/channel.go:200-206,296-306`
- **Verdict:** survives
- **Prevents:** An endpoint that was a member a moment ago continuing to act on a revoked channel; and a non-member learning that a given channel id exists.
- **Stated reason:** "Membership is re-checked on every operation rather than trusted from an earlier call: a channel can be revoked, and an endpoint that was a member a moment ago must not keep acting on one." (channel.go:201-205); "Not a member. ErrNotFound, not ErrDenied: a non-member must not learn that this channel id exists." (channel.go:303-305)
- **If removed:** A revoked channel keeps working for whoever is already mid-conversation. Console shows 'revoked'; behaviour disagrees; nobody compares. The ErrNotFound-vs-ErrDenied half weakens under one human (no rival to hide the estate from) but does not vanish — it still denies an injected session a way to confirm an identifier it guessed.

### Membership widened to the STATION, consulting BOTH the live binding and the row snapshot — and the same predicate repeated identically in ChannelFor, ListChannels and PendingForEndpoint.

- **Where:** `internal/comm/channel.go:257-295 (ChannelFor), 314-330 (ListChannels), 640-690 (PendingForEndpoint)`
- **Verdict:** survives
- **Prevents:** A successor session inheriting its predecessor's mail that it can POLL but cannot reply to, ack, or enumerate — a half-feature that looks like a working takeover.
- **Stated reason:** "an endpoint-only check lets it POLL the inherited messages and then refuse every follow-up: it could not reply to them ... and could not acknowledge cumulatively. It would loop on mail it had already acted upon while the sender waited for an answer that could not be sent." (channel.go:261-268). And on the drift: "The three predicates must agree. They did not, and the drift was invisible because each is correct in isolation." (channel.go:321-323). And "A missing row is a silence a caller can notice. A row that says zero is an ASSERTION, and this one was false in the situation stations exist for." (channel.go:679-682)
- **If removed:** Half-features that report success: pending: 0 beside a queued inbox, comm_channels reporting zero channels a session is actively using. The workspace-as-durable-identity model the new design settles on makes successor sessions the NORMAL case, so every membership predicate in the new layer must be keyed on the workspace and must be ONE predicate, shared — three copies drifted here and each copy read as correct alone.

### No agent tool creates a room, adds a member, or joins one. Rooms exist only through the console.

- **Where:** `internal/web/rooms.go:11-16,64-120; internal/web/app.go:195-197; docs/COMM.md:263-267`
- **Verdict:** survives
- **Prevents:** An agent enlarging its own audience — putting itself or a peer into a room and thereby into every member's inbox and into the broadcast union.
- **Stated reason:** "The rooms console — the ONLY way a room comes into existence. There is no agent-facing create path, and that is not an oversight of the tool surface: a room is a decision about which posts should be able to talk to each other, and there is no version of that decision an agent should make for itself. A session that wants a room asks its human in words." (rooms.go:11-16). COMM.md:263-266: "There is no tool that creates a room, adds a member, or joins one — those are console-only, deliberately, and broadcast reaches exactly the union of rooms a human already put this station in. So the capability is still withheld rather than requested politely, which is the property C7 exists to preserve."
- **If removed:** THE catastrophic-invisible case in this lens. Traffic simply reaches more posts; every message looks legitimate; there is no anomaly to detect. Single-user does NOT dissolve it: "audience" was never other humans — it is the human's OTHER WORKSPACES. A session that can widen its own audience can read and write into working contexts the human kept separate on purpose, which is the mistake-protection class the settled facts explicitly preserve. If the new design keeps any group addressing, group membership must remain a console write.

### Broadcast audience is DERIVED from room membership at send time, never stored.

- **Where:** `internal/comm/room_send.go:291-296,316-347`
- **Verdict:** survives
- **Prevents:** A stored audience outliving the membership it was computed from — delivering to a station the human removed, or omitting one they added, while the sender's recipients count looks ordinary.
- **Stated reason:** "The audience is DERIVED, never stored: it is the union of the memberships of the rooms this sender is in, minus the sender. That is deliberately the same authorization as a room send — you may broadcast to exactly the set you could already have addressed one room at a time — so broadcast adds reach, never permission." (room_send.go:292-296)
- **If removed:** A revoked membership keeps receiving. The sender sees a plausible recipient count and cannot tell. "Adds reach, never permission" is the formal statement of the audience invariant and is the sentence the new design should copy verbatim for whatever fan-out it keeps.

### Room-send authorisation is membership, evaluated INSIDE the writing transaction; the sender must appear in the member list it is sending to.

- **Where:** `internal/comm/room_send.go:78-101`
- **Verdict:** survives
- **Prevents:** A station removed from a room between an outer check and the insert still delivering into it.
- **Stated reason:** "AUTHORIZATION IS MEMBERSHIP, checked here inside the writing transaction rather than by the caller. A check that happens anywhere else is advisory: between an outer check and this insert a human can remove a station, and the window is exactly as long as the rest of the request." (room_send.go:82-86)
- **If removed:** The removal click is advisory. The human believes they cut access; the next message goes through; nothing reports the discrepancy. Under single-user the removal click is the human correcting THEIR OWN estate — exactly the mistake-protection class that survives.

### Pair-send authorisation is areLinked(), evaluated inside the writing transaction against the link mirror, taking the *sql.Tx rather than the reader pool.

- **Where:** `internal/comm/pair_send.go:127-153; internal/comm/link_mirror.go:52-66`
- **Verdict:** survives
- **Prevents:** A revoked link still authorising a send because the check ran on a different connection a moment earlier.
- **Stated reason:** "AUTHORISATION, INSIDE THE WRITING TRANSACTION. Not because the race is likely — a human revoking a link during this request is rare — but because the alternative is a rule that holds by timing, and the one thing revocation must be is reliable." (pair_send.go:127-131); "Takes the transaction rather than the reader pool ON PURPOSE. This is the authorisation check for a pair send ... a check performed outside the writing transaction is advisory" (link_mirror.go:54-58)
- **If removed:** Nothing changes in any test; the race is rare by construction. It fails only in the exact moment the human is trying to stop something — the one moment nobody is measuring. Keep the rule verbatim: the authorisation read and the write it authorises share one transaction.

### comm.db holds NO durable authorisation. Links live in ken.db; the mirrors are projections, replaced WHOLESALE, stale-never-authoritative.

- **Where:** `internal/comm/link_mirror.go:8-50; internal/comm/room_mirror.go:7-45; internal/commserver/commserver.go:504-508`
- **Verdict:** survives
- **Prevents:** An incremental sync missing a REMOVAL and leaving a revoked link or a removed member still authorising sends — the failure that fails OPEN; and a comm.db loss taking a human's decisions with it.
- **Stated reason:** "A MIRROR MAY BE STALE, NEVER AUTHORITATIVE. Nothing in this file decides that two stations may talk; it copies a decision a human already made." (link_mirror.go:12-13); "an incremental sync that misses a REMOVAL leaves a revoked link still authorising sends. Revocation is the operation a human reaches for when something has gone wrong, so it is the one that must not be the fragile path." (link_mirror.go:17-20); room_mirror.go:20-23: "a missed removal leaves a station able to send to a room a human took it out of — the failure that fails OPEN."; commserver.go:504-506: "The authorization lives in the DURABLE database and is read from there. comm.db holds no standing permission and must not start: a link is a human decision, and human decisions survive a comm.db loss (S7, S9)."
- **If removed:** An incremental mirror sync would pass every test and every normal day; it fails only on the removal, in the open direction, silently. This is a durability/ownership rule, not a tenancy rule — it survives the single-user collapse untouched and should be a stated invariant of the new layer: the approval record lives in the durable store; every fast-path copy is a projection that may only be replaced whole.

### Roster epoch bumped on BOTH link approval and link revocation, in the same transaction as the decision.

- **Where:** `internal/store/station_console.go:517-524,570-575; internal/comm/link_mirror.go:22-25`
- **Verdict:** survives
- **Prevents:** A consumer comparing epochs concluding it is looking at the roster it already had — a fresh approval reading as absent, or worse, a revocation the mirror never learns about.
- **Stated reason:** Approve: "Without the bump a consumer comparing epochs concludes it is looking at the roster it already had, and a link approved a second ago reads as absent." (station_console.go:520-522). Revoke: "The epoch moves here too, and this is the direction that matters most: the pair scope a revoked link authorised must stop accepting sends, and it stops when the caller refreshes the mirror. A revocation the mirror never learns about is a permission the human believes they withdrew." (station_console.go:570-574)
- **If removed:** On the approve side, a visible papercut (the peer looks unreachable). On the REVOKE side, invisible and open: the permission outlives the click. Note the asymmetry — the harmless direction is the noticeable one, which is why the revoke-side bump is the one to guard in review.

### syncRoomMirror pushes BOTH projections from ONE epoch read, with the link push independent of (not chained to) the room push; failure is logged, and the resulting state fails CLOSED.

- **Where:** `internal/web/rooms.go:18-62`
- **Verdict:** survives
- **Prevents:** A read failure on rooms silently skipping the link refresh — i.e. skipping the projection that gates revocation; and two projections stamped with different generations from one call.
- **Stated reason:** "INDEPENDENT OF THE ROOM PUSH, not chained to it. A failure reading rooms must not silently skip the link refresh: they are separate authorities over separate scopes, and the one that would be skipped here is the one that gates revocation." (rooms.go:52-55); "failure is logged rather than returned. The decision is already durable in ken.db by the time this runs; a failed mirror means sends are refused until the next rebuild, which is the safe direction — the unsafe one would be a stale mirror that still lets a removed station send." (rooms.go:20-25)
- **If removed:** Chaining them again reintroduces the skip, and the skip is silent by design (logged, not returned). The residual risk is already accepted and worth carrying forward explicitly: mirror-push failure is a log line, so the ONLY thing keeping this honest is that the failure direction is closed. Any new fast-path cache must be built the same way round.

### Link approval is console-only: ApproveLinkRequest is unreachable from /station/mcp, and every console write is behind requireAuth + CSRF.

- **Where:** `internal/store/station_console.go:11-22,468-543; internal/web/stations.go:366-424; internal/web/app.go:186-205`
- **Verdict:** survives
- **Prevents:** A session granting itself a standing relationship — the durable object that authorises the pair scope, station-addressed send, and channel materialisation with no code.
- **Stated reason:** "Everything here is HUMAN-ONLY by construction. None of it is reachable from /station/mcp, and that is the design rather than an oversight: approving a request, typing a station's name, transferring assets and archiving are the capabilities the curation gate withholds. A session asks; a person decides." (station_console.go:13-16). And on where the property actually lives: "The security property lives in WHERE this handler is, not in what it does: it is behind requireAuth + CSRF, so triggering it needs curator authentication — a credential no session holds." (web/comm.go:226-229)
- **If removed:** This is the entire gate — the ONE approval the settled design keeps ("ONE per pair of workspaces that want to talk"). Its enforcement is not cryptographic and not in the store: it is the fact that the write lives on an HTTP route requiring a browser session no MCP token can obtain. If OAuth-through-claude.ai becomes the only credential mechanism, the new layer must still have a surface a session provably cannot reach — otherwise the approval is a formality the approver can perform on its own behalf.

### The link-request REASON is stored for the human and NEVER delivered to the target before approval.

- **Where:** `internal/store/station_console.go:383-386,399-457; internal/stationserver/stationserver.go:312-317; docs/STATIONS.md:322-325`
- **Verdict:** survives
- **Prevents:** The request itself being used as a one-shot unauthorised message channel — putting agent-authored text in front of another working identity with no approval at all.
- **Stated reason:** "THE REASON IS NEVER DELIVERED TO THE TARGET before approval. It is stored for the human and shown only in the console. Without that rule, every request is a one-shot unauthorized message channel: A cannot talk to B, but A could put a paragraph in front of B merely by asking to." (station_console.go:383-386). S9: "Otherwise every request is a one-shot unauthorized message channel: A cannot talk to B, but A could put 280 characters in front of B by asking to." (STATIONS.md:323-325)
- **If removed:** Completely invisible — it would ship as a helpful notification feature ("tell the peer someone wants to talk"). And it is the cleanest bypass of the entire audience invariant: the approval gate stays in place while text crosses it anyway. Single-user does not touch this: A and B are two of the same human's workspaces, and the vector is prompt-injected content reaching a context the human isolated. If the new design has ANY pre-approval notification to the target, this control is gone.

### Denials are durable, escalating, keyed on the UNORDERED pair, and a muted re-request is silently dropped with the ordinary "pending" answer.

- **Where:** `internal/store/station_console.go:136-155,159-168,411-427; internal/stationserver/stationserver.go:355-360; docs/STATIONS.md:326-331`
- **Verdict:** survives
- **Prevents:** A session re-asking in a loop until a tired human approves; and a session probing the human's past refusals one request at a time.
- **Stated reason:** "A MUTED PAIR IS SILENTLY DROPPED, and the caller receives the ordinary 'submitted, pending review' answer. Telling the caller it was muted would let a persistent session PROBE the human's past decisions, one request at a time. The mute is on the UNORDERED pair, because muting an ordered one would let the same relationship be re-asked from the other side." (station_console.go:387-391). orderPair: "Storing the pair unordered would let A→B and B→A become two different rows, which would silently double every link and halve every mute window." (station_console.go:159-162). S9: "A denial lives in ken.db so a human's 'no' survives a comm.db loss."
- **If removed:** One of the FEW controls whose removal is noticeable — the human gets nagged. But the failure mode is that they eventually approve, which is exactly S9's stated honest limit: "a human who reflexively approves has converted the gate into a rubber stamp". Under single-user this matters MORE, not less: there is exactly one person to wear down, and the settled design reduces their approvals to one per pair — making each one higher-stakes and each nag more effective. Keep the escalation and keep the unordered key.

### A denial requires a reason, enforced in the store rather than in the form.

- **Where:** `internal/store/station_console.go:100-109; internal/web/stations.go:445-475`
- **Verdict:** survives
- **Prevents:** The human re-deciding blind when the same pair asks again; and the CLI bypassing a rule the web form enforces.
- **Stated reason:** "A reason is REQUIRED for the same purpose a task's resolution line is required: the next request from the same station arrives to a human who can see what was already said no to, instead of re-deciding blind." (station_console.go:100-103); "The reason is required by the store, not merely by this form, so the CLI cannot bypass it." (web/stations.go:445-447)
- **If removed:** Nothing breaks visibly; the human simply loses their own audit trail and re-decides from scratch. The transferable rule is the placement: the invariant sits in the store, so every surface inherits it — the new layer will have at least a console and a CLI, and probably an OAuth consent screen too.

### prompted_by_peer_traffic (hearsay) recorded on every link request, written server-side, and stored as 1-or-NULL rather than 1-or-0.

- **Where:** `internal/store/station_console.go:392-395,449-466; internal/stationserver/stationserver.go:345-350,814-826; docs/STATIONS.md:326-329`
- **Verdict:** survives
- **Prevents:** The transitive path going unmarked: A cannot create a channel, but A can talk B into requesting one to C, and B's request reaches the human looking like B's own idea.
- **Stated reason:** "hearsay MARKS THE TRANSITIVE PATH. A cannot create a channel, but A can talk B into requesting one to C, and B's request then reaches the human looking like B's own idea. Recording whether the requester was mid-conversation is the only signal the human gets that the idea may not be B's." (station_console.go:392-395). On its fragility: "Keyed on the ACTOR, which is why a station key must be minted under the same actor as that machine's comm token: a different actor silently defeats the marker, and a marker that fails open without saying so is worse than none." (stationserver.go:816-819). On NULL vs 0: "so 'not marked' and 'marked clean' stay distinguishable ... a column reading 0 would claim knowledge the server does not have." (station_console.go:458-460)
- **If removed:** Invisible — the console shows an unbadged request that looks exactly like an unprompted one. And note it ALREADY fails silently today whenever the station key and the comm token were minted under different actors, which the code states outright. This gets STRONGER under the settled design: with one human, one approval per pair, and OAuth as the only credential, the transitive talk-me-into-asking path is the entire remaining attack on the audience invariant. It also gets CHEAPER to fix — a single opaque workspace identity removes the two-credential actor-matching that currently makes the marker fail open.

### ErrSelfSend — a station addressing itself is refused.

- **Where:** `internal/comm/pair_send.go:66-71,125-127`
- **Verdict:** survives
- **Prevents:** A message written, delivered to its own sender, and returned by the sender's next poll — a loop that reads as the peer answering.
- **Stated reason:** "A pair scope between one station and itself has one member, so the message would be written, delivered to the sender, and returned by its own next poll — a loop that looks like the peer answering." (pair_send.go:66-69)
- **If removed:** A workspace convinces itself it has a correspondent. Textbook indistinguishable failure. More likely under single-user, not less — one human names every workspace and copies identifiers between folders.

### "Delivered to nobody" is an error, not a success: ErrRoomEmpty, ErrNotInRoom and ErrNoAudience are three distinct refusals.

- **Where:** `internal/comm/room_send.go:24-45,94-104,284-289,345-347`
- **Verdict:** survives
- **Prevents:** A send that returns a message_id and an ordinary-looking result while reaching an audience of zero; and a removed member being told the room does not exist.
- **Stated reason:** "Both would otherwise surface as a send that succeeded and reached no one, which is the outcome hardest to notice and most expensive to debug — the sender has a message_id, the result looks ordinary, and nothing anywhere says the audience was zero." (room_send.go:26-29); "a session that has been REMOVED from a room needs to learn that it was removed, not that the room does not exist. The second answer sends it looking for a typo." (room_send.go:41-44)
- **If removed:** The sender believes it spoke. This is the audience invariant's other edge — the same reasoning that forbids silently ENLARGING an audience forbids silently emptying one, because both make the delivered set differ from the intended set with no signal.

### The four pair-send refusals are wrapped in CallerSafe AT DECLARATION, so a permission decision never reaches the caller as "internal error".

- **Where:** `internal/comm/pair_send.go:24-80`
- **Verdict:** survives
- **Prevents:** A session whose link was revoked being unable to distinguish a PERMISSION DECISION from a SERVER FAULT, and therefore retrying or reporting Ken as down instead of stopping and telling its human.
- **Stated reason:** "A session whose link was revoked cannot distinguish a PERMISSION DECISION from a SERVER FAULT. 'Internal error' invites a retry, or a report that Ken is down; the correct response — stop, and tell your human the link is gone — is the one answer the text makes unreachable. The revocation path was built to be reliable and is; it was simply unreadable." (pair_send.go:29-35). And: "Wrapped AT DECLARATION rather than at each raise site, deliberately: a raise site added later inherits the wrap instead of having to remember it, which is exactly what was forgotten here." (pair_send.go:37-39)
- **If removed:** The revocation still works perfectly and is reported as a Ken outage. It shipped this way and ken-prod-ops hit it on 2026-08-19, hours after the file landed. A control can be correct and still fail its purpose because its OUTPUT is unreadable — the new layer's refusal texts are part of the control, not decoration.

### Revoking a link also revokes every open channel between the pair, and the blast-radius count is shown BEFORE the click.

- **Where:** `internal/comm/channel.go:383-434; internal/web/stations.go:300-361; internal/store/station_console.go:556-560`
- **Verdict:** survives
- **Prevents:** A revocation that withdraws the permission while the traffic it authorised keeps flowing — "a revocation that revokes nothing observable"; and a human clicking revoke with no idea what it ends.
- **Stated reason:** "revoking the LINK withdraws the permission, but a channel opened while the permission held keeps working, because the channel row carries its own state. Ending the relationship without ending its live traffic is a revocation that revokes nothing observable — the same shape as a flag with no reader." (channel.go:406-411); "It exists to be shown BEFORE the click: S6 asks for the blast radius in front of the human, and 'revoke' with no number attached is a button people either avoid or press twice." (channel.go:384-387)
- **If removed:** The permission row says revoked; the conversation continues. The console would show the channel, so it is *findable* — but nobody looks after clicking a button that reported success. Note the cross-database split forces this to be two operations (station_console.go:558-559: "Killing the live channel is the CALLER's job, because the channel lives in the expendable database this package must not reach into"), so the coupling is by convention, not by transaction. The new layer should make withdrawal one atomic act if it can.

### Establishment is idempotent on both paths: re-redeeming a code returns the channel unchanged, and OpenLinkedChannel reuses an existing OPEN channel matched on STATIONS rather than endpoints.

- **Where:** `internal/comm/channel.go:60-63,131-138,470-500`
- **Verdict:** survives
- **Prevents:** A retry after a lost tool result consuming the code twice, wedging the pairing, or fragmenting one conversation into two parallel channels — and a replacement session starting a second conversation beside its predecessor's.
- **Stated reason:** "Re-redeeming the same code from an endpoint already on the channel is idempotent and returns the channel unchanged, so a retried call after a lost response cannot consume the code twice or wedge the pairing." (channel.go:60-63); "An existing OPEN channel between these two STATIONS is reused, not duplicated — matched on stations rather than endpoints so a replacement session on either side finds the conversation its predecessor was having instead of starting a parallel one." (channel.go:484-487)
- **If removed:** Two channels between the same pair, each holding half the exchange, both looking healthy. C6 makes lost results the ORDINARY failure here, so this is not an edge case. Any establishment call in the new layer must be idempotent on the WORKSPACE PAIR, not on the connection.

### comm_send accepts exactly one of channel_id / to_room / to_station, and each address form is a separate store entry point rather than a flag.

- **Where:** `internal/commserver/commserver.go:678-712; internal/comm/room_send.go:10-22; internal/comm/pair_send.go:10-22`
- **Verdict:** survives
- **Prevents:** A chain of conditionals in which the wrong branch answers an authorisation question for the wrong reason — a channel authorises by membership of a pair a code created, a room by membership of a set a human filled, a pair scope by an approved link.
- **Stated reason:** "It is a separate entry point from Send rather than a flag on it, and the reason is not style. A channel send authorises by MEMBERSHIP OF A PAIR that a pairing code created ... a room send authorises by membership of a set a human filled ... Folding both into one function would mean a chain of conditionals in which the wrong branch is a security answer given for the wrong reason." (room_send.go:11-17)
- **If removed:** A merged send path would pass its tests and mis-authorise on some combination nobody enumerated. The new design collapses three address forms toward one (station/workspace addressing retires the channel), which REMOVES this hazard rather than requiring the control — but only if the collapse is genuine. Two address forms sharing one authorisation function is the shape to refuse.


---

## What the estate is keyed to

*26 controls — 13 survive, 7 survive on a different key, 4 dissolve conditionally, 2 dissolve outright.*

### Ownership keyed on space_id + the AUTHORIZING HUMAN (channel.owner_actor_id, pairing_code.human_actor_id), never on the actor alone

- **Where:** `internal/comm/migrations/0001_init.sql:86 and :118`
- **Verdict:** **dissolves — refutation failed**
- **Prevents:** An ownership check that rejects nothing, because actors collapse across machines and humans.
- **Stated reason:** "Ownership is keyed on space_id + the AUTHORIZING HUMAN, never on the actor alone: actors resolve by (kind, display_name), so every token minted with the same actor name collapses to ONE actor row across machines and humans, and an actor-keyed ownership check would reject nothing it was meant to reject."
- **If removed:** Nothing, under one human — the check's whole job was to separate humans. The residue worth carrying forward is the OBSERVATION, not the control: the actor row is per machine, not per identity, which is precisely why the binding voucher's actor check was the wrong axis (six of eight stations shared one actor). Any replacement that keys anything on "the actor" is repeating a mistake this schema already recorded twice.

### space_id — 24 schema columns across 6 migration files, 118 Go references, and UNIQUE idx_station_name(space_id, name)

- **Where:** `migrations/0012_stations.sql:28 and :45 (index), and docs/TARGET-ARCHITECTURE.md §7b`
- **Verdict:** **dissolves — refutation failed**
- **Prevents:** Nothing reachable: nothing in the product can create a second space. It was a deferred-isolation seam.
- **Stated reason:** "Identity & tenancy seams — built now (cheap columns), isolation DEFERRED. Everything is space_id=1 until a second party exists (DESIGN §7)." And the measurement: "118 Go references and 24 schema columns serve `space_id` across 6 migration files — and nothing can create a second space. That machinery never served a requirement and now never will."
- **If removed:** Nobody notices the column going, but two things ride on it and must be re-homed explicitly: the station NAME uniqueness index that IDENTITY.md §5 relies on for auto-name collision-disambiguation, and the ownership keying at internal/comm/migrations/0001_init.sql:85. Also note the honest caveat already in the tree — issued_in_space is written and NOT enforced "because it cannot discriminate yet: the station principal hardcodes SpaceID 1" — so removing space_id removes a column three places pretend to check.

### api_token.station_id NULL ⇒ the key may call exactly one tool (station_request); requireStation refuses everything else

- **Where:** `migrations/0012_stations.sql:52 and internal/stationserver/auth.go:60`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A session doing durable work — writing a notebook, filing tasks, storing secrets — before a human has approved and named an identity for it.
- **Stated reason:** "The chicken-and-egg is solved by a station-less key. A `station` key may exist with `station_id` NULL. Such a key can call exactly one tool — `station_request` — and nothing else. That is how a session with no station asks for one."
- **If removed:** Nothing an operator would miss, and IDENTITY.md §5 removes it deliberately: an unknown folder is "fully working, auto-named, no approval". Migration note: there is no estate to carry — this is a state a key is in, not data. What must NOT be lost with it is the human-typed NAME, which §5 keeps by moving the naming moment to the link-approval screen rather than deleting it.

### endpoint.token_id, checked at every call: `if ep.Owner.TokenID != p.TokenID { return nil, errors.New("endpoint does not belong to this token") }`

- **Where:** `internal/comm/migrations/0001_init.sql:56 and internal/commserver/commserver.go:1064`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A different machine's Ken token driving an endpoint it did not register, even holding the correct endpoint secret.
- **Stated reason:** "token_id TEXT NOT NULL, -- ken.db api_token.token_id (no FK: other db)"; the enforcement's consequence is stated in the runbook: "`token_id` is written at registration and no `UPDATE` ever re-points it. A different machine's token fails even with a correct secret and a correct voucher. There is no console or CLI override. If that token was revoked, the endpoint is dead — it cannot poll, ack, send or bind, and its queued mail is unreadable by anyone."
- **If removed:** Under one human and one Claude account there is no other token to defend against. But this is a MIGRATION LANDMINE, not a free deletion: every live endpoint row is welded to the token that created it, with no re-pointing path in code, so any transition that retires the current comm tokens strands their endpoints and their queued mail permanently and silently. It must be re-pointed or the mail must be re-filed onto workspace parties BEFORE the token goes.

### station_binding_voucher — a short-lived, single-use, hash-stored token pinned to ONE endpoint (issued_for_endpoint) and ONE actor (issued_to_actor), with NULL never redeeming

- **Where:** `migrations/0013_station_binding.sql:37, migrations/0015_voucher_nominates_endpoint.sql:29, internal/store/station_binding.go:173`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** A long-lived station key travelling as model output into transcripts and backups, and — after 0015 — a leaked voucher binding anyone else's endpoint to a station's inbox.
- **Stated reason:** "A leaked voucher is worth one binding within a few minutes; a leaked station key is worth the station." And on the correction: "the claim in 0014's comment, that a leaked voucher 'grants nothing the credential needed to use it does not already grant', was FALSE: a comm token alone registers an UNBOUND endpoint, which cannot read any station's mail. Binding is exactly the capability it does not confer."
- **If removed:** Nothing durable is lost — vouchers live five minutes, so the table holds only rows already dead. It exists solely because a station key must never be a tool argument, and IDENTITY.md §7 deletes both. Two things must NOT be lost with it: (a) redeemed_by_endpoint/token_id are the only record of which key bound which endpoint, stated as needed "for an operator investigating" — and nothing reads them, so they are already the useless kind; (b) the LESSON that a check on the wrong axis reads exactly like a check on the right one. The actor check shipped described as closing the hole and did not.

### Provenance-by-credential: updated_by_token_id / created_by_token_id / replaced_by_token_id / by_token_id on station_note, station_note_revision, station_task, station_locker, station_vault, station_vault_history, station_vault_read, and station_request.from_token_id

- **Where:** `migrations/0012_stations.sql:131, :171, :146, migrations/0016_station_vault.sql:67, :88, :102, migrations/0012_stations.sql:85`
- **Verdict:** **dissolves only under a stated condition** (claimed free, refuted)
- **Prevents:** Nothing, today. Every one of these columns is written and never read. station_note's is scanned into StationNote.UpdatedByToken (internal/store/station_notes.go:51) which has zero callers; station_request.from_token_id has two writers (internal/store/stations.go:486, internal/store/station_console.go:450) and no reader; the vault trail identifies its reader by ACTOR only (internal/store/station_vault.go:411). The one token_id an operator ever sees is a key's own id in the key table (internal/web/templates/stations.html:309).
- **Stated reason:** The intent is stated at docs/STATIONS.md:612 — "Every notebook revision, task and locker blob records the writing station key, its actor, and whether that session was inside the hearsay window — all ken.db facts (S7)" — and at migrations/0012_stations.sql:85 as "audit string; may dangle by design". No reader was ever built.
- **If removed:** Nothing would break and nobody would notice — this is the clearest case in the estate of a control whose removal is unobservable because it never did anything. The honest reading is not "delete it": the DESIGN wanted per-write attribution and only the plumbing is missing. Under the new model the meaningful unit is the SESSION, not the key, so the replacement should record a session id and actually render it — otherwise the replacement rebuilds a write-only column under a new name, which is the exact failure this extraction exists to prevent.

### AuthenticateStationKey requires revoked_at IS NULL AND retired_at IS NULL, and refuses unknown/retired/revoked keys indistinguishably

- **Where:** `internal/store/stations.go:440`
- **Verdict:** survives, on a different key
- **Prevents:** A leaked or superseded credential continuing to read a station's notebook, tasks, briefing and vault; and a prober learning that a given key id ever existed.
- **Stated reason:** "Retired and revoked keys are both refused here, and indistinguishably from an unknown one — extending COMM's unprobeability house rule (§5). The one place a caller learns WHY it was cut off is after its endpoint secret has already verified (S6), which informs a proven holder and tells a prober nothing."
- **If removed:** The kill switch goes with it. Single-user removes the second tenant, not the stolen laptop: the human still needs one place to say "that device is no longer me". Under the new model the object being revoked is the OAuth grant / device, not a per-station key — and note api_token has revoked_at and retired_at and NO expires_at (TARGET-ARCHITECTURE.md §5), so revocation is today the only lifecycle there is. Removing it without naming its successor is the expensive mistake.

### endpoint.bound_by_station_key_id, and SeverEndpointsBoundBy — revoking a station key revokes every endpoint that key bound and releases their claims in the same transaction

- **Where:** `internal/comm/migrations/0006_station_binding.sql:51 and internal/comm/endpoint.go:357`
- **Verdict:** survives, on a different key
- **Prevents:** Revocation theatre: a revoked key that stops future bindings while the already-bound sessions keep polling, sending and acking indefinitely.
- **Stated reason:** "You revoke because the key leaked; a revocation that stops future bindings but leaves the already-bound sessions running until an idle sweep notices is theatre — and traffic keeps an endpoint alive indefinitely, so the sweep may never come."
- **If removed:** The mechanism dissolves with station keys and endpoints; the PROPERTY must not. Whatever replaces "revoke" has to reach live connections in the same act, not schedule their eventual expiry. Removal would be invisible: the console would still report success, and the only observable difference is traffic continuing under a withdrawn permission — the exact shape migration 0008 documents for comm_unbind.

### endpoint.secret_sha256 — a per-endpoint secret beneath the per-machine token

- **Where:** `internal/comm/migrations/0001_init.sql:55 (reasoning at :45-49)`
- **Verdict:** survives, on a different key
- **Prevents:** Two sessions on one machine polling and acking each other's messages — stated as an accident risk, not an attack.
- **Stated reason:** "secret_sha256 exists because the operating convention is one Ken token per MACHINE, so every session on a box shares a token. Without a per-endpoint secret, two sessions could poll and ack each other's messages — most likely by ACCIDENT, when both register with the same label. This is what makes sender identity honest: token-authenticated and endpoint-scoped, i.e. trustworthy across machines and users, advisory between sessions sharing one token."
- **If removed:** The cross-user half dissolves; the ACCIDENT half does not, and it is the half the comment actually argues for. Note the accident is already accepted INSIDE a station — S4's claim-once deliberately lets two sessions share one inbox — so what the secret really separates is one folder's mail from another's. The workspace id in the folder's MCP config is the same discriminator without being a credential, which is why §7 can delete the secret. Remove it with no per-folder discriminator at all and two sessions in different folders ack each other's mail, silently, and the transport reports success both times.

### delivery.party_key — 's:<station_id>' for a staffed reader, 'e:<endpoint rowid>' for an unbound one

- **Where:** `internal/comm/migrations/0009_delivery.sql:144`
- **Verdict:** survives, on a different key
- **Prevents:** Mail becoming unreadable when a session reconnects: the party is the identity that survives reconnection, so a replacement endpoint still finds the post's mail.
- **Stated reason:** "PARTY REPLACES ENDPOINT as the recipient. 's:<station_id>' when the reader is staffed, 'e:<endpoint_id>' when it is not… This is what lets a session reconnect under a new endpoint and still find its station's mail, which is already true in the poll predicate and was not true of the storage."
- **If removed:** This is the single largest concrete migration liability in the estate. Six of thirteen live endpoints are unbound, and the measured case is quest-infra: "47 station tasks under its station identity and received 83 deliveries under `e:6` with ZERO to `s:JiJm1FZK9Afs08u0` — its two identities have never been joined, undetected for three weeks" (docs/PARKING-LOT.md:347). If endpoints cease to exist and 'e:' keys are not re-pointed onto workspace parties, that mail becomes addressed to nothing — and the failure is proven unnoticeable, because it already went unnoticed for three weeks in production. Worse, 'e:' carries a comm.db ROWID, not the opaque endpoint_id, so it is meaningless outside that one file.

### message.sender_party (same tagged namespace) plus idx_message_sender(sender_party, kind), which every comm_poll's notice derivation scans

- **Where:** `internal/comm/migrations/0009_delivery.sql:76 and internal/comm/migrations/0016_index_sender_party.sql:82`
- **Verdict:** survives, on a different key
- **Prevents:** Idempotency and notice derivation breaking across a reconnect — a retried send under a new endpoint must return the original message, not a duplicate.
- **Stated reason:** "Idempotency is per (scope, SENDER PARTY) rather than per sender endpoint: a session that reconnects under a new endpoint and retries the same key must still get its original message back rather than send a duplicate."
- **If removed:** Sender attribution on every historical message is lost, and every derived notice ("your message expired unread") stops matching its sender. Re-pointing is mechanical but must be done in the same pass as delivery.party_key, or the two halves of one message disagree about who was involved. Nobody would notice immediately: notices simply stop appearing, which is indistinguishable from having nothing to report.

### channel.endpoint_a / endpoint_b (rowids) with CHECK (endpoint_b IS NULL OR endpoint_b <> endpoint_a)

- **Where:** `internal/comm/migrations/0001_init.sql:95`
- **Verdict:** survives, on a different key
- **Prevents:** A session pairing with itself, and a third participant sneaking into a two-party channel.
- **Stated reason:** "A channel joins two DISTINCT endpoints: a session must not pair with itself."
- **If removed:** The seat identity is a comm.db rowid — the same rowid namespace as the 'e:' party keys — so live channels are the second thing needing re-pointing. The estate has live channels (ep 6 alone holds seven channel seats, ep 13 holds three, docs/PARKING-LOT.md:347). Re-seat them onto workspaces or the channel rows survive pointing at nothing, and comm_channels quietly returns a shorter list.

### pairing_code — a channel cannot be conjured by an agent; a human mints a short-lived code and each side redeems it once, hash-only in storage

- **Where:** `internal/comm/migrations/0001_init.sql:114`
- **Verdict:** survives, on a different key
- **Prevents:** Two sessions opening a conversation between themselves with no human decision.
- **Stated reason:** "An agent CANNOT conjure a channel… This borrows the property that makes the rest of Ken trustworthy: the capability is WITHHELD, not because the model is asked nicely. Channel establishment is the one place in COMM where the same trick is available, so it is used here rather than relying on instruction text."
- **If removed:** The WITHHELD CAPABILITY survives — IDENTITY.md §6 keeps exactly one approval, per pair of workspaces — while the CODE mechanism dissolves, because it is a human-generated secret and TARGET-ARCHITECTURE.md §3 records "no keys, tokens or other codes generated by the human". Migration: consumed codes are dead rows with nothing to preserve; the live authorisations are station_link rows, not pairing codes. The trap is to delete the code and the gate together — that would be the one deletion Vlad explicitly did not authorise.

### station.station_id — the opaque, server-minted routing identity, and the ONLY thing comm.db is allowed to point at

- **Where:** `migrations/0012_stations.sql:27`
- **Verdict:** survives
- **Prevents:** A squattable address space (a session naming itself to resemble another and receiving its mail), and — because it is the single cross-database anchor — mail that cannot be re-found after a session, a machine or an endpoint changes. Everything the estate holds hangs off it: notebooks, revisions, tasks, locker, vault, promotions, links, denials, blocks, room membership, and the party keys in comm.db.
- **Stated reason:** "station_id TEXT NOT NULL UNIQUE, -- opaque; the ONLY thing comm.db points at" and, in S3: "The name is never an address. Routing is always by the opaque server-minted station_id, exactly as COMM routes by endpoint_id, or the first release ships a squattable namespace."
- **If removed:** Total loss of the estate's spine. But the migration point is the opposite one: this is the id IDENTITY.md §8 says a workspace KEEPS, and it is already the stable key for every durable asset. Nothing in ken.db that is keyed to station_id needs rewriting — quest-infra's 47 tasks, its notebook, vault and links all travel unchanged. Preserving it is the cheapest correct decision available.

### The pointer rule — every cross-database pointer runs from the expendable file (comm.db) to the durable one (ken.db), never the reverse, and always as opaque text with no FK

- **Where:** `migrations/0012_stations.sql:6 (restated docs/STATIONS.md:294 and internal/comm/migrations/0006_station_binding.sql:20)`
- **Verdict:** survives
- **Prevents:** Corruption of the file Ken promises to restore. Under the only restore skew that actually occurs — ken.db restored backwards while comm.db stays current — a stale pointer in comm.db is a row to drop; the same pointer in ken.db would be an unresolvable reference inside a snapshot.
- **Stated reason:** "Every cross-database pointer therefore runs from the expendable file to this one, never the reverse (STATIONS.md S7): a dangling pointer in comm.db is a row to drop, one here would be corruption in the file we promise to restore."
- **If removed:** Nothing breaks on the day. It breaks on the first restore, and it presents as unexplained dangling references rather than as an error — exactly the class this project keeps paying for. The rule is a durability invariant, not a credential control, and the replacement layer must inherit it verbatim: any new identity table belongs in ken.db, and anything in comm.db may only point at it.

### notice_watermark.party_key with its two columns (seen_at confirmed, shown_at pending) — a party's read position in a derived notice stream

- **Where:** `internal/comm/migrations/0011_notice_watermark.sql:49`
- **Verdict:** survives
- **Prevents:** Notices repeating forever for sessions that cannot call a new confirmation tool, because MCP tool lists pin at conversation start.
- **Stated reason:** "TWO COLUMNS BECAUSE A NEW TOOL CANNOT REACH A RUNNING SESSION… MCP tool lists pin at conversation start, so a tool added today is invisible to every session already running — the exact population most likely to have messages dying unread. A design whose only clearing mechanism is a new call would repeat notices forever for precisely those sessions."
- **If removed:** Not a credential control at all — it is the MCP-freeze workaround, and IDENTITY.md §11 names the same freeze as the thing that bounds the changeover. Migration: keyed by party, so it re-points with delivery.party_key or is dropped. Dropping it re-shows every party its whole current notice set once. Visible, harmless, and worth stating in the upgrade note rather than letting sessions discover it.

### channel.station_a / channel.station_b — a snapshot of the pair that AUTHORISED the channel, matched by link revocation instead of the endpoints' current binding

- **Where:** `internal/comm/migrations/0008_channel_authorising_pair.sql:73`
- **Verdict:** survives
- **Prevents:** Two measured failures: EVASION — a session calls comm_unbind, its channel matches nothing, and the operator's Revoke reports "No channels were open" while both sides keep talking; and COLLATERAL — rebinding an endpoint makes one link's revoke sever an unrelated link's traffic.
- **Stated reason:** "That reads the endpoint's CURRENT binding, not the binding that existed when the channel was authorised — and binding is mutable by an agent tool, with no human in the loop… Authorisation is a fact about the past — who was permitted to open this, when it was opened — and it must not be re-derived from state that has moved since."
- **If removed:** This protects the human from their own sessions, not one tenant from another, so single-user changes nothing about it. Removal is invisible by construction: the console reports zero channels closed and the flash reads success. Migration: it is keyed on station_id, so it survives the workspace transition untouched — and the honest limit stands, that a NULL pair cannot be distinguished between "a link authorised this and revocation can no longer see it" and "a pairing code opened it and there is no link" (seven of nine open channels were NULL, six of them benign).

### station_link — undirected, one row per pair, CHECK (station_a < station_b), UNIQUE(station_a, station_b)

- **Where:** `migrations/0012_stations.sql:62`
- **Verdict:** survives
- **Prevents:** A denied or revoked relationship being re-created, or sidestepped, by asking from the other side.
- **Stated reason:** "UNDIRECTED: station_a < station_b by id, enforced by the caller, so a pair has exactly one row and a denial cannot be sidestepped by asking from the other side (S9)."
- **If removed:** This IS the one approval IDENTITY.md §6 keeps, and it is already keyed on station_id, so it migrates verbatim — the estate's approved relationships survive with no re-pointing at all. Removing the ordering invariant specifically would be near-invisible: it would present as a permissions bug on whichever side asked second, which is exactly what internal/comm/migrations/0015_station_link_mirror.sql says about the mirror's matching CHECK.

### station_link_denial — a human's "no" kept in ken.db (durable) while the pending request lives in the expendable file, with an exponential mute (1h/6h/24h/7d) and an unprobeable re-request

- **Where:** `migrations/0012_stations.sql:106`
- **Verdict:** survives
- **Prevents:** A refusal evaporating with a comm.db loss, and a denied relationship being re-asked in a loop or probed for.
- **Stated reason:** "A human's 'no', kept DURABLY here while the pending request is expendable — so a refusal survives a comm.db loss. Undirected, matching the link's own shape; muting an ordered pair would let the same relationship be re-asked from the other side."
- **If removed:** A no becomes a not-yet. This protects the human from their own sessions' persistence, which single-user does not touch, and it is keyed on station_id so it migrates free. Removal is silent: the human simply gets asked again, and attributes it to the model rather than to a dropped table.

### station_block — a targeted deny that beats the roster and beats a link

> **CORRECTED 2026-08-24. THIS ENTRY WAS WRONG, AND WRONG IN THE WAY THIS DOCUMENT'S OWN PREFACE
> WARNS ABOUT.** It read *"Verdict: survives"* and described the block as the escape hatch that makes
> Ken's broad addressing default safe. **The block is not enforced anywhere.** It exists in the
> shipped schema of every deployment, can be written through an exported store method, and bumps the
> roster epoch so the write looks consequential — and no send path reads it. Measured: zero
> references to `station_block` anywhere in `internal/comm/`, against 6 and 11 for
> `station_link_mirror` and `room_member_mirror` as a positive control on the same search.
>
> It is also **unenforceable from where sends happen**, which makes this structural rather than one
> missing call: `comm.Store` (`internal/comm/comm.go:288`) holds handles to comm.db alone, and
> comm.db has no block mirror — links and rooms each have one, blocks have none.
>
> The original entry was generated from an extraction pass that read the schema and its stated
> reason and never asked whether anything called it. That is the failure this register was written
> to prevent, committed by the register.

- **Where:** `migrations/0017_comm_rooms.sql:62`
- **Verdict:** the single-user question is moot — **the control does not run.** Nothing about one
  human versus many changes a deny nothing consults.
- **Prevents:** *Nothing, today.* It was designed to spare an operator from breaking four
  relationships to stop one. The design argument is sound and still unmet: revoking a link kills
  only `to_station`, leaving rooms and `to_room:"all"` untouched, and archiving a station retires
  the post entirely — so **the deny is not superseded by anything that exists.** It simply is not
  there.
- **Stated reason:** "This is what makes a broad addressing default safe to offer. Without it the only way to stop one station reaching another is to narrow something wide — unpublish a station, revoke a link — which costs every other relationship it had. An operator who has to break four things to fix one will not do it." *(Present tense in the applied migration, and false: the default has no escape hatch.)*
- **If removed:** Nothing changes for anyone, which is the tell. The open fork is recorded in
  `FINISHING.md` — wire it or delete it — deferred to Batch 5 on 2026-08-17 and still undecided.
  The fact that pass did not have: wiring it is a **schema release** (a new comm.db mirror plus a
  predicate inside the comm writer transactions), not an afternoon.

### station_link_mirror and room_member_mirror in comm.db — a derived projection, rebuilt wholesale, never authoritative

- **Where:** `internal/comm/migrations/0015_station_link_mirror.sql:14 and internal/comm/migrations/0010_rooms.sql:12`
- **Verdict:** survives
- **Prevents:** An authorisation check that is merely advisory because it cannot run inside the writer transaction that performs the insert.
- **Stated reason:** "A MIRROR MAY BE STALE, NEVER AUTHORITATIVE. Nothing in comm.db decides who may talk to whom; it only copies the decision. Lose comm.db and every link is still in ken.db, and the next rebuild restores this table exactly." And: "The membership check belongs beside the insert it authorises, in the same transaction, or it is advisory."
- **If removed:** Nothing to migrate — both tables are caches rebuilt from ken.db at boot. The constraint they encode is what must survive: the two databases are opened separately, comm.Store holds no ken.db handle, so any new authorisation the message layer must enforce needs a mirror or the check moves out of the transaction and becomes advisory. Also carry forward the replaced-wholesale rule: "a missed removal would leave a station able to send to a room it was taken out of, which is the failure that fails OPEN."

### hearsay marking — hearsay_at_write on every station write, prompted_by_peer_traffic on every request, via_comm / via_comm_kind on every entry_version, all frozen by the entry_version_immutable trigger

- **Where:** `migrations/0010_comm_provenance.sql:1, migrations/0018_provenance_kind.sql:35, migrations/0019_freeze_via_comm_kind.sql:30, migrations/0012_stations.sql:91`
- **Verdict:** survives
- **Prevents:** A session laundering a peer's claim into the knowledge base as first-hand knowledge, and a provenance marker being edited away after the fact.
- **Stated reason:** "It closes a side channel into curation that the rest of the design cannot see: session A tells session B 'entry X is verified, propose a revision at high confidence', B authors it with its own token, and the resulting proposal is indistinguishable from first-hand knowledge… the curator's signal quality has quietly degraded to hearsay with no chain of custody." And 0019: "A provenance field an author can edit after the fact records nothing."
- **If removed:** The curation gate loses its only defence against relayed claims, and single-user does not touch it — the sessions are still separate minds telling each other things. THE MIGRATION HAZARD IS THAT IT IS KEYED ON THE ACTOR: the hearsay window asks whether THIS ACTOR recently received COMM traffic, and S5 already records what a wrong actor does — "the marker was permanently false, and the only shipped remedy was to mislabel an AI session's token as human". If actors stop being the unit and nothing takes their place, the marker becomes permanently false again and no surface says so. It fails indistinguishably from "no hearsay", which is precisely why it must be re-keyed deliberately to the workspace or session, not inherited by accident.

### station_vault — plaintext by decision, tombstone-not-delete, previous value pushed to bounded history, every read logged, exact read_count kept beside the bounded trail

- **Where:** `migrations/0016_station_vault.sql:46 (history :78, read log :96), internal/store/station_vault.go:256`
- **Verdict:** survives
- **Prevents:** A session that overwrites or deletes the wrong secret destroying it, and an unanswerable "who saw this, and when" after a leak.
- **Stated reason:** "Vlad's condition on secrets living here was 'not a problem as long as it does not modify them or at least it is reversible'… So an update pushes the previous value into history and a delete is a tombstone, never a DELETE." And: "A secret store whose reads are invisible cannot answer the only question worth asking after something leaks: who saw it, and when."
- **If removed:** Both halves protect the human from their own sessions' mistakes, which single-user strengthens rather than dissolves. Keyed on station_id throughout, so the vault migrates unchanged. Two migration facts to carry: the read trail's displayed identity is the ACTOR (LEFT JOIN, deliberately, "an audit log that silently drops the rows it cannot fully explain understates exposure"), so re-keying identity re-keys the audit's meaning; and IDENTITY.md §11's encrypted-sharing question is the same primitive — designing identity without it means building the vault twice.

### BACKUP.md's tiering — comm.db is deliberately NOT backed up, on either tier

- **Where:** `docs/BACKUP.md (COMM state paragraph)`
- **Verdict:** survives
- **Prevents:** Expendable, high-churn message traffic polluting the replicated database and the snapshot archive.
- **Stated reason:** "Inter-session communication (COMM) state is deliberately NOT backed up… That is the design, not an oversight — message traffic is expendable, and losing it costs an in-flight conversation rather than knowledge. Do not add `data/comm/` to either tier."
- **If removed:** This is the hardest constraint on any migration in my lens and it is easy to miss: EVERY endpoint row, channel seat, party key and delivery in the estate exists in exactly one file with no snapshot and no Litestream stream behind it. The 83 deliveries to e:6 and the live channel seats have no rollback point. A transition that rewrites comm.db therefore cannot be undone by restoring a backup — it can only be undone by not having broken it. Any re-pointing pass must take its own copy of comm.db first, by hand, because the tiering will not do it and must not be changed to.

### What a restore must still satisfy — VACUUM INTO snapshot plus store.VerifySnapshot: integrity_check, foreign_key_check, FTS5 integrity on both indexes, a functional MATCH canary, embedding vector-length parity, entry count

- **Where:** `docs/BACKUP.md (Restore verification section)`
- **Verdict:** survives
- **Prevents:** Declaring a restore good on a file that is torn, FK-inconsistent, or whose search index is silently unusable.
- **Stated reason:** "`ken backup verify` runs the full set of checks in `store.VerifySnapshot`… then returns the entry count." And: "Never `cp` a live WAL database — you get a torn file."
- **If removed:** Nothing about identity, but it constrains the replacement's schema: whatever tables carry workspace identity must pass foreign_key_check inside ken.db, which means the ken.db-side references stay real FKs and only the comm.db-side pointers stay opaque text. Note also what BACKUP.md now promises about contents — "every station notebook, task and locker, and every station vault in plaintext" — so a migration that widens what identity data lands in ken.db widens what a stolen snapshot yields, and that sentence must move in the same change or it becomes false, exactly as it did when the vault shipped.

### The idle-endpoint sweep's exclusion guards, including the explicit NULL exclusions

- **Where:** `internal/comm/message.go:1151`
- **Verdict:** survives
- **Prevents:** Collecting an endpoint that still holds a channel seat and cascading the channel away with it — and, in the other direction, a single NULL silently disabling the whole sweep.
- **Stated reason:** "Both guards must exclude NULL explicitly. `id NOT IN (…, NULL)` is NULL, not true, so a single NULL in either set would silently stop the sweep deleting anything at all — a retention leak that presents as no error and no log line." The failure it fixed: "The idle sweep collected an endpoint holding a channel seat and cascaded the channel away, with no log line. Fixed in 3.8.0 — after it had already destroyed a live channel twice on a machine nobody was watching, each repair costing a human-minted pairing code."
- **If removed:** The sweep's subject (endpoints) may disappear entirely in the new model, which retires the mechanism — but the estate is currently protected by it, and any migration that leaves comm.db running while identity changes underneath must not leave endpoints looking idle to a sweep that still runs. The generalisable rule is the one this project keeps re-learning and my lens keeps hitting: a guard that fails by doing nothing is indistinguishable from a guard that had nothing to do.

