# The parking lot — everything observed and not yet fixed

> **THIS IS AN INDEX, NOT A PLAN.** Nothing here is scheduled, prioritised, or promised. It exists
> because on **2026-08-18** Vlad asked that we *"save all the things we've observed that needs to be
> added or modified"* before the conversation about where Ken is going — and because an index built
> from evidence beats one built from anyone's recollection.
>
> The destination those observations feed into is `docs/TARGET-ARCHITECTURE.md`. The work actually
> agreed and in flight is `docs/FINISHING.md`. **This file changes neither.**

## How it was built, and what that is worth

Ten agents swept five sources — `FINISHING.md`, the audit documents and decision briefs, every
`internal/**` code comment, the `docs/` tree, and every migration in both databases — for anything
recorded as deferred, declined, accepted-as-a-cost, or observed-and-not-fixed. A second, adversarial
pass over each source dropped items that had since been fixed, on the standard that **a padded index
is actively harmful**: an index nobody trusts gets skimmed, and the real items go down with the
filler.

**168 raw findings → 152 entries.** Thirteen were the same item recorded in two or three separate
places; those are merged and labelled, and the fact that they were found independently is itself
signal.

**What this index is not.** The "still open" notes are the sweeping agents' verification, made
against the tree on 2026-08-18. I re-checked six of the sharpest claims by hand rather than
trusting them wholesale:

| claim | verdict |
|---|---|
| the `attachment.scope_id` seam was cut and never used | **holds** — `grep -c scope internal/comm/file.go` → `0` |
| nothing ever writes `message.audience_epoch` | **holds** — zero references anywhere in `internal/` |
| nothing ever writes `station_request.state='expired'` | **holds** — the only reads are `WHERE state='pending'` |
| `station_task_add`'s `merge_into?` exists in docs but not in code | **holds** — and the live tool text says "merge" too |
| the OAuth grant's scope is stored and has no effect | **holds** — `auth.go:200` hardcodes `read \| write-draft \| propose` and discards `op.Scope` |
| `stations.vault*` has 19 English keys and none in es/fr | **STALE** — all three bundles carry 19; translated in `0c0f687` (3.10.0) |

The last row is the useful one. It is a claim **in `FINISHING.md` itself**, in the very item whose
subject is text that asserts a control that does not exist — the checklist had become an instance of
the class it was tracking. Corrected in the same commit as this file.

One line number had drifted by one (`stationserver.go:528` → `:529`). Treat every line number here
as approximate and every file path as exact.

## The one thing to read if you read nothing else

The entries in **section A** are not a list of bugs. They are the reason the identity question is
open at all. Read together they say something the individual items do not: **Ken has three
credential systems, and OAuth — the one Vlad wants to be the only one — is the only one that cannot
express what Comm and Station need.** `oauth_grant.scope` is written on every grant, echoed back to
the client, listed in the console, and consulted by nothing; the principal it produces is a hardcoded
literal. Whatever the answer turns out to be, it goes through that line.

---
## A00. Added 2026-08-20 — ken-prod-ops' six provisioning and credential findings (E1–E6)

**These were promised into this file on 2026-08-19 and were not written.** I told ken-prod-ops in
seq 143 *"WHERE THEY GO: docs/PARKING-LOT.md"* and again in seq 147 *"Your E1, E2 and E3 are in
PARKING-LOT.md"* — and none of the six was here, under any wording, until now. Found by an audit on
2026-08-20 that swept the message log for promises made and not kept. **A claim made to a peer about
where their work was recorded, which was false when made.**

All six were found by ken-prod-ops reading production, and their evidence is in the message log at
`2026-08-19-seq142-prod.md` §5. Three are operator-safety.

**E1 — Revoking a COMM token silently kills every endpoint under it, and both consoles keep showing
them healthy.** `auth()`'s last check refuses when `ep.Owner.TokenID != p.TokenID`, and 11 of 13
comm tools call it — so revoking the token is sufficient on its own. Nothing tells the operator,
before or after: the eager sweep that exists to tidy up after a revoke matches on
`bound_by_station_key_id`, a **station-key** column, so a comm token id matches zero rows and it
does nothing, silently. `ListEndpoints` filters only on the endpoint's own `revoked_at` and the
template renders no token column. **On that deployment this would show 10 endpoints and 13 channels
reading normal while none of them can make a call.** Fix shape, smallest first: state the blast
radius in the revoke confirmation — exactly what 3.8.0 already did for station-key revoke.

**E2 — There is no comm-token rotation. Not a missing button: the operation does not exist.**
`endpoint.token_id` is written once at registration and no `UPDATE` anywhere re-points it. No console
route, no CLI verb, no migration; `ken token` is `add|list|revoke`. **So "rotate a compromised comm
token" decomposes into: revoke, re-register every session, and hand a human-minted pairing code to
every unbound seat.** For a credential that is per-MACHINE by convention, one leak takes out every
session on the host — there, one token, 10 endpoints, 8 projects. The inversion is the point:
`POST /comm/endpoints/{id}/rotate` does exactly the right thing for an endpoint secret, keeping the
id, the binding and every channel. **The endpoint has a rotation story and the token does not**,
which is backwards — the token is the credential more likely to leak, because it is shared and
long-lived. *This is the one I would want first if Rule 1 ever lifts.*

**E3 — Mail addressed to an unbound endpoint is unreadable forever by any successor, and binding
does not fix it. Live instance included.** `delivery.party_key` is stamped at SEND time and never
updated; `BindEndpointToStation` updates only the endpoint row. So an endpoint that receives mail
while unbound can still read it after binding — its poll predicate ORs both party forms — **but its
successor never can.** Live in `comm.db`: delivery 536, message `214lqWSIQUrOmoyTrsL82U`, party_key
`e:18`, `requires_response=1`, never polled, expires 2026-09-17. If endpoint 18 is replaced — which
is exactly what E2 forces after a token compromise — the message is lost to everyone including the
sender waiting on it. ken-prod-ops explicitly asked that it **not** be fixed for their sake.
Re-filing `e:<rowid>` deliveries to `s:<station>` at bind time would close it, and the transaction is
already open.

**E4 — `/tokens` reports a RETIRED key as Active.** `ListTokens` does not select `retired_at` and the
template renders `{{if .RevokedAt}}revoked{{else}}active{{end}}`. `/stations` shows retirement
correctly; `/tokens` cannot. Live instance: key `Hdtpy1PVb07G`, retired 2026-08-18T21:13:23Z, showing
green **Active** while being refused at every station call. **Fourth instance of the same shape** —
the correct value is stored and the surface does not use it.

**E5 — `retired_at` is honoured by exactly one authenticator, and the `kens_` prefix is cosmetic.**
Checked in `AuthenticateStationKey` and nowhere else; `mcpserver/auth.go` and `commserver/auth.go`
filter only `revoked_at`. Both prefixes resolve to the same `api_token` row and the prefix is a
property of the printed string, not the row — so a retired station key can be re-formed as
`ken_<id>_<secret>` and presented to the KB surface. **Reachable capability is negligible** (the row
carries neither `read` nor `comm`, so everything refuses on scope), which makes this a latent trap
rather than a hole. But "Retire" reads as "off", and it is off on one surface of three.

**E6 — An OAuth connector is invisible in the way an operator would search, and leaves no usage
trace.** Three independent reasons a search fails: wrong table (OAuth lives in
`oauth_grant`/`oauth_token`, nothing is written to `api_token`); wrong vocabulary
(`oauth_grant.scope` holds the wire scope `read write offline_access` while the real capability set
is hardcoded in Go as `read, write-draft, propose` — **those strings appear nowhere in the OAuth data
path**); and the console hides the actor (`ListOAuthGrants` selects `human_actor_id` and never
`actor_id`, so the page says "approved by admin" and never reveals which actor the connector
*writes* as). **And there is no usage trace at all** — `oauth_token` has no `last_used_at`, and the
middleware bumps `TouchToken` only when `p.APIToken` is true, which is false for OAuth. **A connector
can read the entire knowledge base and leave nothing behind.** 3.10.0 fixed precisely this for
station keys, with the reasoning that "no timestamp" reads as "unused" rather than "unmeasured".
This is the same sentence, and it pairs with the independently-found fact that the grant's scope is
discarded — see §A of this file and TARGET-ARCHITECTURE.md §7.

---

## A0. Added 2026-08-19 — the unreachable-refusal class, swept

Two defects of this class were found and fixed the same day (`ab7d68e`, `4ac7e6c`). A sweep of all
four surfaces for the rest found **thirteen more**, each verified by tracing the raise site through
its mapper to what a caller actually receives. Recorded, not fixed: `FINISHING.md` is one
confirmation from closing.

**The class:** an error whose author wrote a paragraph for a caller, where the text cannot reach
that caller. `commError` flattens by sentinel to keep refusals uniform, and anything its switch does
not name becomes `internal error`. The opt-in seam is `comm.CallerSafe`.

| where | sentinel | caller gets |
|---|---|---|
| `comm/room_send.go:36` | `ErrRoomEmpty` | `internal error` |
| `comm/room_send.go:45` | `ErrNotInRoom` | `internal error` |
| `comm/room_send.go:289` | `ErrNoAudience` | `internal error` |
| `comm/file.go:211,223,230` | three `OfferFile` validations | `internal error` ×3 |
| `comm/file.go:394` | "attachment has no downloadable bytes" | `internal error` on MCP — **the identical sentence is reachable over HTTP** (`commserver/files.go:197`, 409), which is evidence the text was meant for a caller |
| `comm/file.go:510` | "attachment is not awaiting an upload" | HTTP 500, so an uploader retries a request that can never succeed |
| `store/write.go:234` | "based_on_rev not found" (`kb_propose_enhancement`) | `internal error` |
| `store/write.go:449` | "could not allocate a unique slug" (`kb_save`) | `internal error` |

**`ErrNotInRoom` MUST NOT SIMPLY BE WRAPPED, and this is the finding worth keeping.** `room_send.go`
chooses it over `ErrRoomEmpty` when `len(members) > 0`, so a caller-visible `ErrNotInRoom` would
confirm *"this room exists and has at least one member"* to a non-member. The file's own comment
already records that the two cases are indistinguishable from the mirror; making the **errors**
distinguishable is what would create the oracle. The uniform answer is to give both non-member
branches `ErrRoomEmpty`'s wording — which is a small design decision, not a mechanical wrap, and is
why these were not swept up with the others.

**The three file-validation ones are the opposite case**: pure input validation, where the caller
supplied the bad value itself and uniformity buys nothing.

**Judged correct as-is, recorded so nobody "fixes" them:** the `/mcp` auth refusals
(`mcpserver/auth.go:208,226,229`) all collapse to HTTP 401 `invalid token`, and that flattening is
the deliberate anti-probing behaviour, not an instance of this class.

**And the testing lesson underneath all of it.** Every one of these has a passing test. The store
tests assert `errors.Is` at the raise site, which structurally cannot see what a caller receives —
the mapping happens a layer above. Twice in one day a fix was written, tested at the store layer,
and a mutation reverting the *wiring* survived. **A test that does not cross the mapper cannot
observe this class at all.**

---

## A. Bears on the destination in §2–§4

> **2026-08-19 — one entry outranks this index and is not in it.** Vlad named the recurring
> credential friction on "pull comm" as a thing the new-feature work must solve, and it was
> measured rather than asserted: **six occurrences across five sessions**, one of them costing
> **10 h 43 m** to a working channel, and **352 copies of a single endpoint secret on disk**,
> 323 of them tool-call arguments in one session transcript. It is written up as
> `TARGET-ARCHITECTURE.md` **§4b**. Several entries below are that problem in different clothes —
> the endpoint pair travelling as arguments, `api_token` having no expiry, the credential a
> session must store and re-read, and the reissue path that must stay console-only.

**50 entries.** Each of these is either a mechanism the target architecture removes, a control it needs and Ken does not have, or a fact that constrains how it could be reached.

### Observed defects — behaviour that is wrong, not merely unfinished

##### 1. The 'an endpoint cannot move between stations' invariant is one boolean on a column another tool clears — and the harm it names is the wrong harm

`internal/commserver/commserver.go:272 (bind refusal) vs :308 (unbind success note); internal/comm/endpoint.go:190-192; batch3 finding N1`

**Outstanding.** Enforce the invariant or delete the claim. Leaving both is how the next reader concludes the live join is safe.

**Evidence.** Verified in the tree: comm_bind refuses with "this endpoint is already bound to a station — an endpoint cannot move between stations, because it would carry the first station's unread mail into the second"; comm_unbind clears station_id and its success note ends "You can bind again later." So unbind-then-bind performs the move and the tool performing the bypass advertises it. The stated harm cannot happen — party keys are recorded at write time and no delivery row moves; what actually moves is channel MEMBERSHIP, because the seat is re-derived from the live binding.

**Deferred because.** The critic named it the single most important finding of the sweep and it was not fixed in 3.8.0; FINISHING files it as "belongs with the credential model" — i.e. parked behind the Batch 5/6 identity work.

**Still open.** Unticked at FINISHING.md:366, both strings unchanged. Two outstanding decisions in one: enforce or delete the invariant, and correct a stated harm that cannot occur. Explicitly parked with the credential model, which is now Batch 6 work.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 2. The frozen instruction block tells every session to attribute peer knowledge to the DISPOSABLE identity, and never mentions comm_bind at all

`docs/FINISHING.md:385 (raised by the Batch 3 sweep, unticked); internal/commserver/commserver.go:188`

**Outstanding.** Attribute to the station, not the endpoint (messageView already carries from_station_id and from_station_name). Adjacent and in the same block: the loop never mentions comm_bind or stations at all, so a successor reading only the instructions is told to re-pair.

**Evidence.** Line 188 still reads "attribute the sending endpoint". Endpoint rows are deleted by the idle sweep (7 d default), the knowledge base has no TTL, so the attribution names a row that no longer exists — and three sessions of one correspondent become three unrelated opaque ids. The durable name is already in the result being read: messageView carries FromStationID/FromStationName (types.go:214). The loop at :178-190 mentions comm_register and comm_join with a human-minted pairing code and never comm_bind, so a successor reading only the instructions is told to re-pair — the precise cost stations exist to abolish.

**Deferred because.** No reason recorded. Weight noted at source: MCP instructions pin at conversation start, so a wrong sentence here is unfixable for every session already running.

**Half shipped (Unreleased), half still open.** The hearsay attribution is fixed: the connect-time block now names the station (from_station_name + from_station_id) and says what to record, and how to qualify it, when the sender has no station — pinned by TestTheHearsayRuleNamesTheDurableIdentity, which reads the delivered initialize result rather than the const. Still open, and now tracked as its own FINISHING item: the loop never mentions comm_bind, so a session has no stated path from comm_register to a station. Note also that the Evidence above is now partly stale — the loop does discuss stations (to_station, comm_directory, station_link_request); comm_bind is the omission.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 3. Nothing can mint a station-less key, so station_request's documented entry path cannot be enacted

`internal/web/stations.go handleStationKey (always mints against the path station id); cmd/ken/cli_station.go:74-76 (`--station is required`); internal/stationserver/auth.go:62-71; migrations/0012_stations.sql:47-50`

**Outstanding.** Delete the branch and rewrite the four descriptions — minting an unbound key is a NEW operator control and forbidden under Rule 1.

**Evidence.** The empty-station branch is live in code (requireStation's refusal text tells the caller to "call station_request to ask your human to create one") and unreachable in practice: the console mints against r.PathValue("id"), the CLI dies without --station, and `ken token add` refuses station scopes. Four places describe it as the onboarding path — the migration comment, the tool description, auth.go, and STATIONS.md §6. Noted in passing and still true: handleStationKey never checks that the path id names a real station and api_token.station_id has no FK, so a key bound to a dangling station id is mintable.

**Deferred because.** Building the missing half is barred by the standing rule (no new features until the list is finished); only the deletion is available, and it has not been done.

**Still open.** Four places describe an onboarding path that no surface can produce, and closing the gap means a NEW operator control, which Rule 1 forbids — so this is a decision, not a fix.

*Recorded in: audits + briefs*

##### 4. station_request.from_token_id is NOT NULL, written by both request paths, and read only by a test fixture — so approving binds nothing

`migrations/0012_stations.sql:85; internal/store/stations.go:447; internal/store/station_console.go:450; only reader is internal/store/station_console_test.go:364`

**Outstanding.** Delete the column, or use it and say so in the approve flow.

**Evidence.** On both the console and CLI approval paths the operator must still run `ken station key` afterwards and the session must be reconfigured — which no surface says. The column that would let approval bind the asking key is written and never read.

**Deferred because.** No reason recorded. Deleting it is a migration, which ships alone.

**Still open.** The column that would let approval bind the asking key is collected and never used, so approving still leaves the operator to run `ken station key` and the session to be reconfigured — which no surface says.

*Recorded in: audits + briefs*

##### 5. The console mints station keys under the logged-in human, and its own badge tells the operator to do something the form cannot express

`internal/web/stations.go:487 (mint) and :495; internal/web/templates/stations.html:333-342 (form); internal/i18n/locales/messages.properties:645 (badge help)`

**Outstanding.** The mint form has one field (`label`) and passes `sess.ActorID` — the human curator's actor. The remedy Ken prints ("Mint a key under the actor that holds this machine's COMM token") is not performable on that form. The CLI already resolves the right actor (`mustStationActor`, cmd/ken/cli_station.go:156-175); the console has no equivalent.

**Evidence.** stations.go:487 — `IssueStationKey(r.Context(), sess.ActorID, r.PathValue("id"), label, scopes)`. Template form posts only `csrf` and `label`. messages.properties:645 — "This key was minted under an actor that holds no COMM token. … Mint a key under the actor that holds this machine's COMM token." TARGET-ARCHITECTURE.md §7 records the production confirmation: the first console-minted station key carries actor `human:admin`.

**Deferred because.** Not stated in code. The CLI path was corrected (cli_station.go:156-166 documents the same defect as fixed there); the console path was not carried over.

**Still open.** Confirmed outstanding and confirmed in production. The mint form carries `csrf` + `label` only, and stations.go:487 passes `sess.ActorID` — the curator's human actor. The console's other mint path is hardcoded to kind `ai` (web/app.go:1037, oauth_web.go:274 both `FindOrCreateActor(ctx, "ai", …)`), and `(kind, display_name)` is unique, so the two console mint paths sit on opposite sides of a partition. The CLI has the resolver (mustStationActor, cmd/ken/cli_station.go:156-175, which refuses to CREATE an actor precisely so a typo cannot mint a key that marks nothing); the console has no equivalent. Note the IssueStationKey contract comment overclaims here too: it says "the console offers a picker that marks them" — the console offers a badge in the key TABLE (stations.html:310), not a picker on the form.

*Recorded in: code comments*

##### 6. A station key minted under the wrong actor is still permitted and still silently kills the hearsay marker — only binding checks it

`docs/STATIONS.md:196-199 (S5); the operator requirement it depends on is docs/COMM.md:673-685 (§7)`

**Outstanding.** Minting is unenforced. `prompted_by_peer_traffic` goes permanently false with nothing said on either side, and the tooling only steers — `ken station key` defaults to the comm-token actor and the console picker marks holders — it does not refuse.

**Evidence.** docs/STATIONS.md:197-199 — "**Since 1.7.0 the mismatch is no longer silent everywhere: BINDING enforces it** (below). Minting under the wrong actor is still permitted and still silently defeats the hearsay marker — that half remains a papercut the tooling steers around. Do not read the binding check as covering both." docs/COMM.md:683 — "Mint them under *different* actor names and nothing is ever marked — a silent false negative."

**Deferred because.** Stated: refusing at mint time would break the supported case (a station is legitimately usable with COMM off, and there is then no comm token to match) to protect an unsupported one.

**Still open.** Confirmed unenforced in code. The document is explicit that only half the mismatch is caught, and warns against reading the binding check as covering both.

*Recorded in: docs tree*

##### 7. station_request.state='expired' has no writer — a pending request lives forever, and `ken station add` strands one deliberately

`migrations/0012_stations.sql:94-95; cmd/ken/cli_station.go:119-127`

**Outstanding.** Nothing expires a station or link request. The CHECK declares 'expired', the sweeper never sets it, and a request created by a session that has since died stays in the console's pending list and renders a live Approve form indefinitely.

**Evidence.** Every writer of `station_request.state` sets 'approved' (internal/store/station_console.go:89,512) or 'denied' (:129). Grep for `state='expired'` under internal/store returns nothing. cmd/ken/cli_station.go:119-127 documents the resulting split state in the other direction: "CreateStation never touches station_request, so the row is left pending forever while a station exists… The stranded row then renders a live Approve form: clicking it later creates a SECOND station or fails on the name, and the only other exit is Deny, which records a refusal for a request that was granted." The CLI works around it with print-text rather than fixing it.

**Still open.** Confirmed on both halves. The CHECK value has no writer, and the CLI ships a printed warning instead of a fix — a known trap carried on purpose with a live wrong-action path (Approve on an already-granted request).

*Recorded in: migrations + schema*

### Deferred work — decided, scoped, not built

##### 8. The appendix's prose class is NOT finished — two of its three hand-verified findings still stand (the third is now fixed and the item text is stale)

`docs/FINISHING.md:282 (Batch 2, unticked); docs/audits/batch2-stations-kb.md`

**Outstanding.** Work the rest of the prose section of the Batch 2 appendix. Still true today: (a) docs/STATIONS.md:878 documents a `merge_into?` parameter on station_task_add that appears zero times in internal/, and the FROZEN live surface says it too — internal/stationserver/stationserver.go:528 ("so you can close or merge instead of duplicating") and internal/stationserver/types.go:197 — so a doc-only strike leaves "merge" pinned into every session at connect; (b) the same table gives `station_task_defer(ids[], until, reason)` while internal/stationserver/types.go:214 takes a single task_id.

**Evidence.** Both re-verified today by grep. The item's THIRD example is now false and the checklist has not been updated: it says "stations.vault* has 19 keys in English and ZERO in Spanish and French", but all three bundles now carry 19 stations.vault* keys with real Spanish/French text, translated in 0c0f687 (3.10.0). The item's own rule is the part worth keeping: "text asserting a control that does not exist is the class that propagates" — the same class TARGET-ARCHITECTURE.md §7 points back at this file for.

**Deferred because.** The bullet records that this item was WRONGLY marked complete once: "that sentence stood here until 2026-08-17 and was wrong. Five findings are corrected … the rest of the appendix's prose section is still live."

**Still open.** Unticked at FINISHING.md:282. Two of its three named findings re-verified true today, and the third being stale is itself a fact the index should carry rather than a reason to drop the item. The live-surface half matters most: the doc strike alone leaves 'merge' pinned into every session at connect.

*Recorded in: FINISHING.md*

##### 9. Slice 7 is unscheduled, and two of the three blockers it claims are dissolved are still open items in this same file

`docs/FINISHING.md:518 (`[ ]`, unscheduled)`

**Outstanding.** Retire the channel. Five hard blockers: H1 (no agent-initiable private path), H2 (file exchange is channel-only), H3 (retroactive revocation), H4 (station_block is dead), H5 (unbound endpoints have no address).

**Evidence.** The section states "Batches 3 and 4 dissolve H2, H3 and H4. Batch 5 dissolves H1 and H5." But H2 is the attachment scope item (FINISHING.md:334, unticked, ships alone) and H4 is station_block (FINISHING.md:420, unticked) — so as written the dissolution claim is ahead of the tree. H1's replacement (P2) is also unticked and H5 is the endpoint migration, which is unticked operator work.

**Deferred because.** "Not scheduled. It is a program, not a slice, until Batches 3, 4 and 5 are done — and after them it may be a slice again." Plus the target-architecture note: three Batch 6 items "now point somewhere he has said he does not want to go. That tension is recorded there and resolved after this list, not during it."

**Still open.** FINISHING.md:518 is `[ ]` and 'Not scheduled'. This is the largest remaining architectural change — retiring the channel — and the item adds a verified discrepancy: the section's dissolution claim runs ahead of the tree. That matters directly to a rebuild-or-extend decision, because it changes how much of the destination is actually reachable by finishing the list.

*Recorded in: FINISHING.md*

##### 10. P2 — comm_send{to_station:"X"}, the send that makes comm_open_channel redundant

`docs/DECISIONS-BATCH5.md §Decision 2 recommendation; docs/FINISHING.md Batch 6`

**Outstanding.** A new scope prefix beside ch:/r:/b:, a membersOfScope arm, and reply/sequence numbering per pair. Its cheaper half P3 shipped in 3.11.0 (d593434); P2 has not started.

**Evidence.** Verified absent today: no station scope prefix anywhere in internal/comm, and membersOfScope (internal/comm/party.go:108) has only channel/room/broadcast arms. Decided as part of Batch 5 decision 2 ("P3, then P2"); P3 shipped in 3.11.0 (d593434), P2 did not.

**Deferred because.** Order: "The order is load-bearing" — P2 follows the endpoint migration, which has not started. TARGET-ARCHITECTURE.md §6 also flags it: "addressing between stations, which survives the concern but inherits its container from it."

**Still open.** Unticked at FINISHING.md:574, verified absent. It is the built half of Batch 5's second decision (P3 shipped, P2 did not) and the file names it as the change slice 7 actually needs.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 11. D — station key authenticates /comm/mcp: not started, and one of its two same-commit prerequisites is still unmet

`docs/FINISHING.md:577 (Batch 6, unticked); internal/store/stations.go:321-403; internal/mcpserver/server.go:91 and internal/stationserver/stationserver.go:909`

**Outstanding.** (1) retired_at is checked only by AuthenticateStationKey, so "Retire this key" would silently not sever messaging; (2) /comm/mcp never re-derives the caller per call, which is harmless only while the per-call secret pins identity. Both in the same commit as D, after the endpoint migration.

**Evidence.** internal/commserver/auth.go:145 and internal/mcpserver/auth.go:219 both SELECT revoked_at and never retired_at; internal/store/stations.go:403 (AuthenticateStationKey) is still the only query with `retired_at IS NULL`. Inert today because station keys carry no comm scope; under D the console's "Retire this key" would silently not sever messaging — the same class that left four releases of operator text promising the opposite of the code.

**Deferred because.** Ordered behind the endpoint migration and the keepalive verification — "the order is not a preference; each step removes a blocker for the next." Prod's caveat travels with it: no station has ever had two live endpoints, so D deletes something UNTESTED (S4's second reader), not something proven useless.

**Still open.** Unticked at FINISHING.md:577, and both prerequisites verified as still absent. The failure mode is stated precisely — 'Retire this key' would silently not sever messaging — which makes this a correctness constraint on a future commit rather than a preference.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 12. Migrate the unbound endpoints onto stations — operator work on a console only Vlad has, and the set is not closed

`docs/DECISIONS-BATCH5.md "What only production can answer" #1; docs/FINISHING.md Batch 6, first item`

**Outstanding.** Five endpoints: ep 6 (quest-infra) and ep 14 (proxmox-servers) BIND; ep 13 (rb5009-config) and ep 18 (runway-prod-admin) NEW STATION then bind; ep 10 REVOKE. None done. Nothing in this repository can do any of it.

**Evidence.** Six of thirteen live endpoints were unbound and two were load-bearing the same day — ep 6 holds seven channel seats, ep 13 holds three. quest-infra wrote 47 station tasks under its station identity and received 83 deliveries under `e:6` with ZERO to `s:JiJm1FZK9Afs08u0` — its two identities have never been joined, undetected for three weeks.

**Deferred because.** It gates D. And the design constraint it raised is permanent, not a caveat: ep 18 was created two minutes before the table was read, so anything premised on "every session holds a station" must define an answer for a session that registers mid-flight.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 13. The endpoint pair is still accepted as a tool argument — the compatibility window has no end date

`internal/commserver/types.go:40-41, :52-53, :131-132, :172-173, :283-284, :306-307, :327-328, :341 (eleven tools); docs/FINISHING.md Batch 6 item B`

**Outstanding.** Removing the argument form. B shipped the header in 3.11.0 and every field is now `omitempty` with a jsonschema saying the header is available — but the arguments are still read, so a session that keeps passing them keeps writing the secret into its own transcript.

**Evidence.** Every comm tool input still carries EndpointID/EndpointSecret; comm_register still RETURNS the secret as a tool result (types.go:35-36, "shown ONCE — keep it"), which lands in the same transcript the header change exists to keep it out of.

**Deferred because.** "Keep the arguments accepted-and-ignored for one release so running sessions are not broken" — MCP tool schemas pin at conversation start, so a session that connected before 3.11.0 can only send arguments. No release is named as the one that removes them.

**Still open.** Removal is the outstanding half of B. The arguments are not merely tolerated — they are read as a live fallback — and comm_register still returns the secret as a tool result, which lands in the transcript the header change exists to protect.

*Recorded in: audits + briefs*

##### 14. There is no graceful "I moved machines" path for a station key — only Retire (severs station surface) and Revoke (severs both)

`internal/store/stations.go:318-331 (statement at :328)`

**Outstanding.** The capability the comment says was promised does not exist. Both verbs cut a live session; neither hands a station over to a new machine without an interruption.

**Evidence.** stations.go:326-328 — "So the difference from revocation is narrower than the words suggest — Retire severs the STATION surface and spares COMM; Revoke severs both (RevokeToken plus the endpoint-severing pass, S6). Neither is the graceful 'I moved machines' path this comment used to promise."

**Deferred because.** Not stated. The 2026-08-14 change corrected the description rather than adding the behaviour.

**Still open.** Confirmed outstanding, and distinct from item 7: item 7 is deleting a false sentence, this is a capability that does not exist. Directly relevant to the provisioning work TARGET-ARCHITECTURE §7 is about — a station handover today interrupts the live session either way.

*Recorded in: code comments*

##### 15. The endpoint credential is still accepted as tool ARGUMENTS, which land in client transcripts; headers are only preferred

`internal/commserver/commserver.go:890-908 (statement at :900); argument fields declared throughout internal/commserver/types.go`

**Outstanding.** Removing the argument fields. Until then every session whose conversation began before 3.11.0 keeps writing the secret into its own transcript, and nothing on the server can stop it.

**Evidence.** commserver.go:900 — "THE ARGUMENTS STILL WORK, deliberately, and will for at least one release." :891 — "tool arguments are recorded by the CLIENT in its conversation transcript — on disk, in the clear, for as long as that transcript is kept. Ken cannot mitigate that by changing what Ken logs: the recording happens in software neither end ships."

**Deferred because.** Stated: "A tool's input schema is captured by a client when its conversation begins and never refreshes… Removing the fields would break every conversation already in flight."

**Still open.** Confirmed outstanding with an explicit time-boxed deferral ("will for at least one release") and a stated reason that is itself a hard constraint: a tool's input schema pins at conversation start, so removing the fields breaks every conversation in flight. Ken cannot mitigate the leak by changing its own logging — the recording happens in client software neither end ships. The removal is real work someone must schedule.

*Recorded in: code comments*

##### 16. AI-INTEGRATION.md — the document README points agents at — contains no station coverage at all

`docs/AI-INTEGRATION.md (whole file; section list ends at §"Inter-session communication", line 431)`

**Outstanding.** There is no agent-facing setup path for the station surface. COMM gets a full section (token minting, `claude mcp add` line, the two things worth knowing); stations get nothing — not the `kens_` header credential, not `/station/mcp`, not the briefing loop. The only written adoption procedure is docs/STATIONS.md §12, which is addressed to an operator, not to a session.

**Evidence.** `grep -in station docs/AI-INTEGRATION.md` returns zero hits. README.md:69 describes AI-INTEGRATION.md as "how to make your AI use Ken (token strategy + the operating loop)", and README.md:57 asserts stations are a core surface with no switch.

**Still open.** Confirmed: zero station coverage in the agent-facing document. Distinct from item 2 (that is a false sentence to correct; this is a missing section to write).

*Recorded in: docs tree*

##### 17. COMM v2 — retiring the channel and replacing pairing codes with name-addressed send — is the named precondition for two whole surfaces entering the compatibility contract

`COMPATIBILITY.md:71-96; corroborated at docs/STATIONS.md:8-13`

**Outstanding.** Replace pairing codes and channel-pair addressing with name-addressed send, and retire the channel — the central noun of the current comm_* tool surface. Until it lands, `comm_*` and `station_*` tools, endpoint ids, MCP endpoint paths, notebook/task/locker shapes and settings are all outside SemVer.

**Evidence.** COMPATIBILITY.md:74-77 — "what remains is replacing pairing codes and channel-pair addressing with name-addressed send, and **retiring the channel**, the central noun of the tool surface as it stands." COMPATIBILITY.md:93-95 — "**The trigger for promotion is stated rather than open-ended: both surfaces enter the byte-level contract when the COMM v2 redesign lands.**"

**Deferred because.** Stated: "Promoting it into the contract now would make that redesign a MAJOR bump, or force a release cycle of deprecated v1 aliases for a shape nobody intends to keep, and buy a caller nothing in exchange."

**Still open.** Confirmed and load-bearing: it is the stated trigger gating both comm_* and station_* out of SemVer. The single largest outstanding design item in the index.

*Recorded in: docs tree*

##### 18. Endpoint identity in the console is an agent-supplied label rendered as identity on the surface where a human decides what to revoke

`docs/COMM.md:866-884 (§12)`

**Outstanding.** The "fuller" half — a human-set, authoritative endpoint name visually distinct from the agent's self-chosen label — is not built. The "cheap" half has partly landed (the console now groups by station and shows channel membership), but first-seen / last-activity / host-hint columns and the human-controlled identifier are not there.

**Evidence.** docs/COMM.md:874-876 — "an **agent-supplied label is untrusted input rendered as identity** on the one surface where a human decides what to trust and what to revoke — nothing stops two endpoints claiming the same name." The code still says the same thing: internal/web/comm.go:87-95 — "Endpoint LABELS are agent-supplied — a session names itself — so after a few weeks the list is a column of similar strings an operator cannot tell apart, and rotating the wrong row is a self-inflicted outage."

**Deferred because.** Stated: "Surfaced from the production shakedown; recorded here rather than built, pending a decision on scope."

**Still open.** KEEP, but the evidence needs one correction before a human uses it. The 'fuller' half — a human-set authoritative endpoint name — is genuinely unbuilt and the code comment confirms the problem is still carried. However the item overstates the cheap half: first-seen and last-activity ARE columns today (Created / LastSeen), and channel membership is surfaced in the revoke confirm dialog. What is actually missing from the cheap half is only the host hint.

*Recorded in: docs tree*

##### 19. Per-station connect-time MCP instructions and station-specific metrics are named as not built

`docs/STATIONS.md:22-24 (header) and 969-973 (§13); corroborated at docs/MONITORING.md:74-77`

**Outstanding.** Per-station instructions need a server built and cached per station, selected from the authenticated principal, because MCP instructions are one string per server. Station metrics do not exist at all beyond the generic per-tool counters; there is no equivalent of the ken_comm_* series counting stations, notebook bytes or open tasks.

**Evidence.** docs/STATIONS.md:22 — "**Not built, and named here so no section reads as a promise it does not keep:** per-station connect-time instructions (§13); and station-specific metrics, which do not exist beyond the generic per-tool counters every MCP surface emits." docs/MONITORING.md:74-77 states the same absence from the operator side. Confirmed: internal/stationserver/stationserver.go:253 builds one `mcp.NewServer` for the whole surface.

**Deferred because.** Stated for the instructions: "Worth doing — identity arriving without a tool call is most of the ergonomic win — but not yet settled, so §4 falls back to the briefing."

**Still open.** Confirmed by document and code. Both absences are stated deliberately so no section reads as a promise, and both are still true.

*Recorded in: docs tree*

##### 20. The space/tenancy seam was cut in the 0001 baseline and there is still no way to create a second space

`migrations/0001_init.sql:34-44 ("isolation DEFERRED"), plus space_id on actor/entry and every station, room, channel, endpoint and request table`

**Outstanding.** `space` holds exactly one row, seeded by the migration. No Go code INSERTs into it, and the station principal hardcodes SpaceID 1. Every `space_id` column, its defaults and `idx_entry_space` are carried on every table for a second party that the target architecture now says will never exist.

**Evidence.** Migration comment: "Identity & tenancy seams — built now (cheap columns), isolation DEFERRED. Everything is space_id=1 until a second party exists (DESIGN §7). Carrying these columns from day 1 makes the collaborative future additive, not a rewrite." `INSERT INTO space(id, name) VALUES (1, 'personal')` at :44 is the only insert in the project — grep finds no `INTO space` or `FROM space` in any .go file. internal/stationserver/auth.go:129 and stationserver.go:949 both hardcode `SpaceID: 1`.

**Deferred because.** Stated: the columns are cheap now and make a collaborative future additive rather than a rewrite.

**Still open.** Confirmed. The stated justification ("makes the collaborative future additive") is now in direct tension with the target architecture, so this is exactly the kind of carried decision the index exists to surface. It is also the blocking precondition for item 12.

*Recorded in: migrations + schema*

##### 21. station_binding_voucher.issued_in_space is written on every voucher and enforced by nothing

`migrations/0014_voucher_holder.sql:25-28; internal/store/station_binding.go:122 vs :187-192`

**Outstanding.** The redemption predicate checks the endpoint and the actor; the space is recorded and never compared. The stated precondition for tightening it — that the station principal stop hardcoding SpaceID 1 — has not changed.

**Evidence.** Migration comment: "issued_in_space is recorded but NOT enforced, because it cannot discriminate yet: the station principal hardcodes SpaceID 1 (stationserver/auth.go). Writing it now means the check can tighten to (actor, space) without a second migration and without a backfill that would have nothing truthful to write." Confirmed at HEAD: `issued_in_space` appears in exactly one Go statement, the INSERT at internal/store/station_binding.go:122. internal/stationserver/auth.go:129 still reads `SpaceID: 1`.

**Deferred because.** Stated: the column cannot discriminate anything while the station principal hardcodes space 1; writing it now avoids a second migration and an untruthful backfill later.

**Still open.** Confirmed, and the stated precondition for tightening it has not moved. It is a deliberately-carried cost with an explicit unblock condition — precisely the shape a rebuild analysis needs. Downstream of item 6.

*Recorded in: migrations + schema*

##### 22. message.channel_id is still written for every 'ch:' message purely so slice 7 can retire it later — and slice 7 is unscheduled

`internal/comm/migrations/0009_delivery.sql:71-74; docs/FINISHING.md "Slice 7 — retire the channel"`

**Outstanding.** The channel column, the channel table, `channel.label`, `channel.station_a/b` and the pairing-code path all persist behind a retirement that is explicitly "not scheduled… a program, not a slice".

**Evidence.** 0009:71-74: "Nullable from here on: a room message belongs to no channel. Still written for every 'ch:' message so slice 7 can retire the column with evidence rather than hope." FINISHING.md: "## Slice 7 — retire the channel `[ ]` **Not scheduled.**… the five hard blockers are H1 (no agent-initiable private path), H2 (file exchange is channel-only), H3 (retroactive revocation), H4 (`station_block` is dead), H5 (unbound endpoints have no address)." H4 is still open (see the station_block item); H2 is still open (see attachment.scope_id).

**Deferred because.** Stated: retiring the column should be justified by evidence rather than hope, so it keeps being written until the retirement is taken.

**Still open.** Confirmed. This is the umbrella item for the single largest thing being carried on purpose — the whole channel generation — and it names the condition for retirement plus the blockers that are still open. Overlaps items 14 and 23 only in that they are its named blockers; the carried column, table and pairing-code path are distinct outstanding surface.

*Recorded in: migrations + schema*

##### 23. attachment.scope_id was added, backfilled and indexed by comm migration 0010 — and internal/comm/file.go contains the string "scope" zero times

`internal/comm/migrations/0010_rooms.sql:45-60; internal/comm/file.go`

**Outstanding.** The seam is entirely unwired: a file still cannot be offered to a room, and the one-attachment-per-offer economy the migration describes does not exist. `attachment.channel_id` is still NOT NULL and `recipient_endpoint` still holds one endpoint where a room needs a party set. Needs a migration plus code; FINISHING.md records that it must ship alone.

**Evidence.** 0010:53-60 adds the column, backfills every existing row to `'ch:' || channel_id`, and creates `idx_attachment_scope`. At HEAD `grep -c scope internal/comm/file.go` returns **0**, and grep for `attachment` + `scope_id` across all Go files returns nothing — no reader, no writer. FINISHING.md Batch 3, still `[ ]`: "Migration 0010 already added and backfilled `attachment.scope_id` — and `internal/comm/file.go` contains the string \"scope\" **zero times**. The seam was cut and never used, so a file cannot be offered to a room. *Needs a migration; ships alone.*" It is slice 7's blocker H2.

**Deferred because.** Stated in Vlad's 2026-08-17 ruling: it is "a restructure carrying code changes in file.go, so bundling it would destroy the property that makes bundling safe — that every change in the release is provably inert. It ships on its own, and it is the one case where Rule 4 and the code it needs are in genuine tension."

**Still open.** Confirmed unwired at HEAD and still `[ ]` in the plan. A migration cut the seam, backfilled it, indexed it, and no code followed — the file surface is still channel-only, which is slice 7 blocker H2.

*Recorded in: migrations + schema*

##### 24. mirror_state.roster_epoch exists so a stale room mirror can be recognised — and no production code ever compares it

`internal/comm/migrations/0010_rooms.sql:33-43; internal/comm/room_mirror.go:49-57`

**Outstanding.** The mirror records which ken.db roster generation it was built from, and nothing reads it back. A mirror that has fallen behind (a console write that failed after the ken.db commit, a restored comm.db) addresses yesterday's room silently — which is precisely the outcome the column was added to prevent.

**Evidence.** 0010:33-36: "What the mirror was built from, so a stale one can be RECOGNISED rather than trusted… if ken.db has moved past it, this projection is behind and the caller can say so instead of silently addressing yesterday's room." `Store.MirrorEpoch` (internal/comm/room_mirror.go:51) has exactly one caller in the whole tree and it is a test (internal/comm/room_send_test.go:209). The boot rebuild (cmd/ken/main.go:411-429) writes the epoch and never compares it; nothing on the send path or in the console reads it.

**Still open.** Confirmed: the guard is written but never read, so the exact failure it was added to prevent (a mirror behind ken.db silently addressing yesterday's room) is unguarded. Distinct from item 15, which is the message-side epoch in comm.db's message table.

*Recorded in: migrations + schema*

### Open questions that need a human decision

##### 25. station_block — the table 0017 calls "what makes a broad addressing default safe to offer" has zero callers, and Batch 5 closed without deciding it

`docs/FINISHING.md:420 (unticked, inside a Batch 4 header marked [x]) and docs/FINISHING.md:450 (Vlad's ruling); internal/store/rooms.go:232-270; migrations/0017_comm_rooms.sql:62`

**Outstanding.** Vlad's decision: wire it honestly (a send-path check on all three paths — channel, room, broadcast — plus a console surface, plus a decision about which database owns it, since COMM keeps only a derived mirror of room membership) or delete it (a migration). Verified still zero callers today: BlockStationPair, UnblockStationPair and BlockedPairs are referenced nowhere, including tests.

**Evidence.** `BlockStationPair`, `UnblockStationPair` and `BlockedPairs` (internal/store/rooms.go:232-280) have zero callers at HEAD — grep finds only their own definitions, not one call site and not one test. FINISHING.md Batch 4 records it verified empirically: "the table holds 0 rows in the live ken.db, and the only objects in sqlite_master naming it are the table and its own index — no FK, trigger or view in either direction… provably unused in every deployment that ever ran Ken code." Vlad's ruling: "station_block is DEFERRED TO BATCH 5, neither wired nor deleted… Left in place, unwired, and recorded as pending — which is the one state this plan normally forbids, taken deliberately and with a date on it." It is also slice 7's blocker H4.

**Deferred because.** Explicitly: "it is the safety mechanism that makes a permissive addressing default defensible, so the real question is about that default, not about this table" — left unwired and recorded as pending, "the one state this plan normally forbids, taken deliberately". DECISIONS-BATCH5 adds: if P2 keeps the link requirement, the default is not permissive and station_block stays optional.

**Still open.** Unticked at FINISHING.md:420 and the deferral has lapsed: Batch 5 is marked [x] 'both DECIDED 2026-08-18' and this was not one of the two decisions. Zero callers confirmed today. It is Slice 7's H4 blocker and the safety mechanism that makes a permissive addressing default defensible, so the decision is still owed and is now unscheduled.

*Recorded in: audits + briefs, FINISHING.md, migrations + schema — 3 separate places*

##### 26. The unbound-endpoint migration is unstarted operator work, and one endpoint has had two disjoint identities for three weeks

`docs/FINISHING.md:538 (Batch 6, unticked, first in a load-bearing order)`

**Outstanding.** Five operator actions in Vlad's console: BIND ep 6 (quest-infra) and ep 14 (proxmox-servers) — keys exist, no console step; NEW STATION then bind for ep 13 (rb5009-config) and ep 18 (runway-prod-admin); REVOKE ep 10 (collector-proxy-dev, duplicate, 0 seats / 0 sent / 0 received). ep 6 is first.

**Evidence.** "quest-infra has 47 station tasks and a live key, 83 deliveries addressed to e:6 and zero to s:JiJm1FZK9Afs08u0 — its station identity and its messaging identity have never been joined. Zero is not 'nobody wrote to it', it is 'nobody CAN': a live instance of the 2026-08-13 rooms defect, unnoticed in production for three weeks." Also recorded as a design constraint: "The set is NOT closed … ep 18 was created two minutes before ken-prod-ops read the table. Anything built on 'every session holds a station' must handle a session that registers MID-FLIGHT."

**Deferred because.** Blocked on a human — "OPERATOR work, not code — the voucher is redeemed by the session itself and the console is Vlad's." And now in recorded tension with the destination: TARGET-ARCHITECTURE.md §6 says it "binds endpoints to stations on an estate whose owner has stopped using stations."

**Still open.** Unticked at FINISHING.md:538 and first in an order the file calls load-bearing — D cannot land before it. It is also the only item here that is a live production defect rather than latent debt, and it carries a design constraint (the set of sessions is never closed) that binds anything built on 'every session holds a station'.

*Recorded in: FINISHING.md*

##### 27. Multi-space / multi-human machinery is deferred with the seams already built and load-bearing

`internal/web/comm.go:24-31 (`spaceForSession = 1`); internal/comm/endpoint.go:386-389; internal/comm/admin.go:37-41; internal/comm/channel.go:91`

**Outstanding.** Deferred until a second human exists: space isolation (`WHERE space_id=?` or a separate DB), per-space RBAC / control points / sub-users, Postgres/pgvector; and on the COMM side invitation flows between different humans, per-user quotas with real values, and cross-space policy. Per-owner quotas are stated as not implemented ("there is one owner").

**Evidence.** web/comm.go:26 — "Hardcoded to the single space that exists today, matching DESIGN.md §7's stance: build the seams, defer the machinery." endpoint.go:388 — "an unscoped listing would be the enumeration surface in a multi-human future, and scoping it later would be a behavioural break." admin.go:41 — "an unscoped listing becomes the enumeration surface the moment a second human exists, and narrowing it later would be a behavioural break."

**Deferred because.** Stated: the cheap seams (actors, space_id on every row, ownership keyed on space_id plus the authorizing human, two-sided establishment from day 1) were built now precisely so the rest stays additive — "both would otherwise be MAJOR surgery."

**Still open.** Confirmed outstanding and it is the single item most directly in tension with the recorded destination — this is a decision the analysis must make, not inherit. The seams were built on a stated rule (scoping a listing later is a behavioural break, so scope from day 1), and they are load-bearing today: every COMM query is space-scoped and the console narrows to a hardcoded space 1. TARGET-ARCHITECTURE §2 settles "One Ken user — one human — per Ken instance", which means the future those seams protect against may never arrive. Note the coupling: item 14's assumption breaks precisely if this machinery is ever activated.

*Recorded in: code comments, docs tree — 2 separate places*

##### 28. COMM cannot wake an idle session, and whether Ken should ship a CLI verb for a harness hook is still undecided

`docs/COMM.md:855-858 (§12)`

**Outstanding.** Decide whether Ken ships a surface (a CLI verb usable from a harness hook or background loop) that surfaces arrivals into a session that would otherwise never poll. Nothing has been built.

**Evidence.** docs/COMM.md:855-858 — "**Reaching an idle session.** COMM cannot wake a session that is not polling. Whether Ken should ship a CLI verb usable from a harness hook or background loop — surfacing arrivals into a session that would otherwise never look — is a real question and deliberately out of scope for 1.2." Still listed as open at docs/DESIGN.md:464.

**Deferred because.** Stated: "deliberately out of scope for 1.2" — and never re-opened since.

**Still open.** Confirmed open in two places. A genuine undecided design question, not just unbuilt work.

*Recorded in: docs tree*

##### 29. comm_room.kind='dm' is declared, unreachable, and migration 0017 describes it in the present tense as though it worked

`migrations/0017_comm_rooms.sql:35-36 and :43; internal/store/rooms.go:28-38; docs/FINISHING.md Batch 6`

**Outstanding.** The value stays reserved, dm rooms were declined on 2026-08-18, and the decline has a technical precondition that is still open: `room_member_mirror` carries no `kind` column. Revisiting requires that mirror change first.

**Evidence.** 0017:36 `kind TEXT NOT NULL DEFAULT 'topic' CHECK (kind IN ('topic','dm'))` with the comment "'dm' rooms are created implicitly for a pair" — present tense. `CreateRoom` (internal/store/rooms.go) hardcodes 'topic'; internal/store/rooms.go:28-38 states plainly "nothing creates one, so no 'dm' row has ever existed… that sentence describes an intention, not a behaviour, and this is the correction." FINISHING.md Batch 6: "[-] dm rooms — declined 2026-08-18. Not because they are wrong, but because `room_member_mirror` carries no `kind` and a missed audience filter widens broadcast invisibly. Revisit only if the mirror gains a kind for another reason."

**Deferred because.** Stated twice: building dm is a new feature and its shape was Batch 5's decision; and the mirror's missing `kind` means a missed audience filter would widen broadcast invisibly.

**Still open.** Confirmed. Recently declined (2026-08-18) with an explicit technical precondition that is still open — room_member_mirror has no kind column — so revisiting has a named prerequisite. That is a decision with a reason, which is what this index is for.

*Recorded in: migrations + schema*

### Declined, with the reason — so a later session does not silently re-open it

##### 30. DECLINED: dm rooms — the mirror carries no kind, so a missed audience filter would widen broadcast invisibly

`docs/FINISHING.md:581 (Batch 6, `[-]`)`

**Outstanding.** Nothing to build. The decision itself is the artifact, and it leaves comm_room.kind='dm' permanently reserved-but-unproducible (see the related known-cost item).

**Evidence.** "declined 2026-08-18. Not because they are wrong, but because room_member_mirror carries no kind and a missed audience filter widens broadcast invisibly. Revisit only if the mirror gains a kind for another reason."

**Deferred because.** Stated in full above: the risk is an invisible widening of broadcast reach, which is the same property Batch 5 decision 2 was constrained by ("an agent must not be able to enlarge its own audience").

**Still open.** A deliberate decline with its reason and its revisit condition stated — exactly the category that belongs in this index. Nothing to build, but the decision is the artifact: it is why the private-conversation path is P2 rather than a two-party room, and it converts the reserved schema value into a permanent carry.

*Recorded in: FINISHING.md*

##### 31. DECLINED: scoping pairing codes to a station — it hardens a mechanism slice 7 removes

`docs/FINISHING.md:591 (Deliberately not doing, `[-]`)`

**Outstanding.** Nothing. Note that pairing codes are exactly what TARGET-ARCHITECTURE.md §3 forbids ("no keys, tokens or other codes generated by the human"), so the decline is consistent with the destination but leaves the human-minted code path live until slice 7.

**Evidence.** "it hardens a mechanism that slice 7 removes, and comm_open_channel already covers the station-to-station case. The regression it was hiding (link revocation could not close code-paired channels) was fixed, in 3.5.0."

**Deferred because.** Effort against a mechanism scheduled for removal.

**Still open.** A decline with a stated reason, and the item adds a verified consequence worth carrying into the rebuild-or-extend conversation: the human-minted code path stays live until slice 7, and slice 7 is unscheduled — while the target architecture forbids human-generated codes outright.

*Recorded in: FINISHING.md*

##### 32. DECLINED FOR NOW: a full re-implementation — and its stated revisit point (after Batch 5) has arrived

`docs/FINISHING.md:594 (Deliberately not doing, `[-]`)`

**Outstanding.** The revisit. The condition attached to the decline is now met: Batch 5 closed 2026-08-18, and TARGET-ARCHITECTURE.md §8 names this exact question as the post-finishing conversation — "do we get there by adding and modifying, or was the re-implementation right?"

**Evidence.** "The defects found have been shallow — a wrong predicate, a missing filter, an unwired call — and none demanded a design change. The pain is localised to one unfinished migration, and the tests and comments encode failures already paid for. Revisit after Batch 5, when the identity model is settled and there is more information than there is today."

**Deferred because.** Insufficient information at the time, plus the value already sunk into tests and comments that encode paid-for failures.

**Still open.** The central item of the whole index. The decline was explicitly conditional and the condition is now met, and the destination document names this exact question as the post-finishing conversation. The reasoning attached to the decline is the evidence a re-decision has to argue against.

*Recorded in: FINISHING.md*

##### 33. station_directory discovery is deferred; a session names a peer by the name its human uses

`docs/STATIONS.md:571`

**Outstanding.** No discovery mechanism beyond the published-or-linked list. A session that does not already know a peer's name must ask its human.

**Evidence.** docs/STATIONS.md:571 — "A session names a peer by the name its human uses; discovery is deferred until there are enough stations for a list to beat asking."

**Deferred because.** Stated: "deferred until there are enough stations for a list to beat asking."

**Still open.** Confirmed as an explicit deferral with a stated threshold ("until there are enough stations for a list to beat asking"), which is a decision rather than silent debt.

*Recorded in: docs tree*

### Known costs accepted on purpose — the price of a decision already taken

##### 34. channel.station_a / station_b are NULL on every pre-0008 pairing-code channel, so the snapshot cannot be made authoritative

`internal/comm/channel.go, ChannelFor's membership switch (the four-arm `stnA || snapA` form) and its comment "THE SNAPSHOT IS NOT MADE AUTHORITATIVE HERE, deliberately"; docs/FINISHING.md Batch 3 `[ ]``

**Outstanding.** Link revocation still consults the snapshot IN ADDITION to the live binding, because making the snapshot authoritative today would strand the NULL rows. Fixing it needs a backfill that cannot be computed from inside comm.db — the authorising binding was never written down.

**Evidence.** 0008:29-52 states the limit and forbids the obvious cleanup: "DO NOT TREAT A NULL PAIR AS A DEFECT TO CLEAN UP. Two completely different histories produce it and NOTHING DISTINGUISHES THEM… On the deployment this was written against, seven of nine open channels had a NULL pair and SIX of the seven were kind (2) — including the operator's own working channels. An earlier version of this comment told operators to close them, which would have severed the live estate." FINISHING.md, still `[ ]`: "Backfill `channel.station_a` / `station_b`, then make the snapshot authoritative. `bcd60dd` consults the snapshot IN ADDITION to the live binding because the column is NULL on every pairing-code channel opened before migration 0008 — six of seven on a real deployment. *Migration; ships alone.*"

**Deferred because.** Stated: the distinction between "a link authorised this and revocation can no longer see it" and "no link was ever involved" is unrecoverable, because the authorising binding was never recorded.

**Still open.** Confirmed outstanding, and the deferral reason is explicit and good: the snapshot is NULL on every channel opened by a pairing code before migration 0008 (most of them on a real deployment), so dropping the live-binding arm today would strand those channels' successors. The stated blocker is that a backfill is a migration and "a migration ships alone". Migrations 0009-0014 have shipped since; no backfill among them, so the two-source read is still live in the authorisation switch.

*Recorded in: audits + briefs, code comments, FINISHING.md, migrations + schema — 4 separate places*

##### 35. comm_room.kind='dm' is a CHECK-permitted value nothing can produce, now carried indefinitely

`docs/FINISHING.md:339 (Batch 3, ticked as DOCUMENTED not built); migrations/0017_comm_rooms.sql:36; internal/store/rooms.go:28-32 and :57-70`

**Outstanding.** The reservation itself. It was resolved by documenting, not by building or dropping, and Batch 6 then declined dm rooms outright — so the schema keeps a value with no producer and no scheduled producer.

**Evidence.** Verified: `kind TEXT NOT NULL DEFAULT 'topic' CHECK (kind IN ('topic','dm'))` with a partial unique index that excludes dm, while CreateRoom hardcodes 'topic'. internal/store/rooms.go:28 now states plainly that no 'dm' row has ever existed and that migration 0017's comment describes them in the present tense.

**Deferred because.** "building dm is a new feature AND the shape it would take is Batch 5's decision" — and Batch 5's decision declined it (FINISHING.md:581).

**Still open.** Ticked as resolved-by-documenting, but the premise of that tick has since changed: it was closed on the basis that 'the shape it would take is Batch 5's decision', and Batch 6 then declined dm rooms outright. So the schema now carries a value with no producer and no scheduled producer, permanently — a carried cost, not a closed item.

*Recorded in: FINISHING.md*

##### 36. Two production measurements that would decide option C are unavailable, one of them permanently

`docs/DECISIONS-BATCH5.md "What only production can answer" #3 and #4`

**Outstanding.** #3 (do two sessions ever run on one machine against one station simultaneously) is UNANSWERABLE with today's instruments. #4 (how often an MCP transport reconnects within one conversation — the cost of C expressed as endpoint churn) was never measured.

**Evidence.** "The question is about SESSIONS and Ken records ENDPOINTS: last_seen_at moves and does not record overlap, so two sessions sharing one endpoint pair are indistinguishable from one session polling twice. Claims name the endpoint, not the session. This needs an instrument that does not exist, not a query." The partial substitute measured instead: ~31 comm teardowns/day, of which only about a quarter cluster.

**Deferred because.** Recorded explicitly as questions for ken-prod-ops rather than guesses to be made in the repository. #3 is flagged UNANSWERABLE ≠ NO, which is the trap it exists to name.

**Still open.** The #3 half is still load-bearing for the decision that IS outstanding: D collapses concurrent sessions on one machine to one reader, and Ken cannot tell whether that ever happens. It names an instrument that does not exist, which is a decision to take, not a query to run.

*Recorded in: audits + briefs*

##### 37. `IssueStationKey` still does not enforce the actor rule its own contract states, and the hearsay consequence stays silent

`internal/store/stations.go:270-297 (statement at :279)`

**Outstanding.** A key minted under the wrong actor authenticates perfectly and marks nothing, forever, with no signal on either side. Only BINDING is enforced (RedeemBindingVoucher, internal/store/station_binding.go:144-155); the hearsay consequence is unenforced.

**Evidence.** stations.go:279 — "THIS FUNCTION STILL DOES NOT ENFORCE THAT. It records what it is told. An earlier version of this comment said 'the caller enforces that', naming an enforcer that never existed — which cost a production operator real time." :291 — "So the hearsay consequence above remains unenforced and silent — a mismatched key authenticates perfectly and marks nothing — while the binding consequence is now loud. Do not read the new check as covering both."

**Deferred because.** Stated: "refusing at mint time would block the legitimate case of a deployment that has no comm token yet, and stations run with COMM off by design." The mitigation chosen instead was to make the right actor the DEFAULT in each caller — which the console caller does not do (see the previous item).

**Still open.** Confirmed outstanding, and the non-enforcement is deliberate with a stated reason worth preserving: refusing at mint time would block the legitimate case of a deployment with no comm token yet, and stations run with COMM off by design. What is carried is the asymmetry — binding is now loudly enforced (RedeemBindingVoucher), hearsay marking is not, and the comment explicitly warns the reader not to read the new check as covering both.

*Recorded in: code comments*

##### 38. Hearsay marking is a silent false negative whenever the comm token and station key are minted under different actor names

`internal/comm/provenance.go:25-33`

**Outstanding.** Nothing detects or reports the condition; it is documented in docs/COMM.md §7 instead of being made impossible or visible at runtime.

**Evidence.** provenance.go:30 — "Consequence the operator must know: if the two tokens are minted under DIFFERENT actor names, nothing is ever marked. That is a silent false negative, so it is called out in docs/COMM.md §7 rather than left to be discovered."

**Deferred because.** Structural: the token that receives messages may not be the token that authors an entry (scope dedication forbids it), so the ACTOR is the only shared identity available to join on.

**Still open.** Confirmed outstanding. Overlaps item 4 but is not a duplicate: item 4 is the station-key mint site in store/stations.go, this is the marker itself in internal/comm and it holds for the comm-token↔knowledge-base-token pair as well, i.e. on a deployment with stations off entirely. The chosen mitigation is documentation (docs/COMM.md §7) rather than detection — nothing at runtime reports the condition. Worth merging with item 4 only if the human decides one fix covers both mint paths.

*Recorded in: code comments*

##### 39. The hearsay badge is keyed on the actor, so on a shared machine it is nearly always on and therefore carries no information

`internal/comm/provenance.go:55-59; wording corrected in commit 5788040`

**Outstanding.** Not fixable within the current identity model. The commit that corrected the tooltip says so explicitly and defers the real fix to "the one-identity work".

**Evidence.** provenance.go:58 — "the badge prod already measured as nearly always on, made worse by the feature that just shipped." Commit 5788040 body: "Their second consequence is real and not fixable here: where several sessions share a machine the badge is nearly always on, and one that is never absent carries no information. Narrowing it to the writing session requires the knowledge-base and messaging identities to be the same identity, which is exactly what the one-identity work does. Noted in the CHANGELOG rather than papered over." Production measurement quoted in the same commit: eight endpoints under one actor.

**Deferred because.** Requires unifying the knowledge-base and messaging identities — precisely the change TARGET-ARCHITECTURE.md §2-§4 describes.

**Still open.** Confirmed outstanding and explicitly declined at the point it was found, with the reason recorded: it is not fixable inside the current identity model. The wording fix landed (all three locales); the substance was deferred to the one-identity work, which is exactly the destination TARGET-ARCHITECTURE.md describes. This is the highest-value item in the identity cluster for a rebuild-vs-extend decision, because the fix is a change of identity model rather than a patch.

*Recorded in: code comments*

##### 40. Whole tools cannot reach a running session — the freeze that bounds every provisioning change

`internal/version/stamp.go:5-25 and InstructionStamp(); internal/version/toolinfo.go:19-27`

**Outstanding.** Nothing to fix in Ken; it is a constraint any migration plan must obey. A new tool added by a redesign is invisible to every conversation already open; only tool RESULTS and existing tools' PARAMETERS reach them.

**Evidence.** stamp.go InstructionStamp() — "WHOLE TOOLS DO NOT travel: a tool added after this conversation began is not in your list and you have no handle to call it, however much you know about it." toolinfo.go:19 — "The server cannot see which text a client captured — that is the whole shape of the problem." Measured case in stamp.go:7-12: a session held 1.7.0's text while the process serving it had been the 2.1.0 image for hours.

**Deferred because.** Not deferrable — it is a property of MCP clients. Recorded here because it is the reason two other items on this list (the argument-position credential, and the un-confirmable notice) were declined rather than fixed.

**Still open.** Keep, with the caveat that it is a CONSTRAINT rather than a defect — there is nothing to fix in Ken. It belongs in this index because it invalidates the obvious shape of any migration plan: a redesign that introduces a new tool is invisible to every conversation already open, and only tool RESULTS and existing tools' PARAMETERS reach them. Item 10 and item 19 are both downstream consequences of it, which is the argument for stating it once at the top of the index.

*Recorded in: code comments*

##### 41. Station-key use is recorded only as a throttled `last_used_at`; there is no per-read record and no source address

`internal/stationserver/auth.go:110-122`

**Outstanding.** "Was this credential used by someone else" is unanswerable. A stolen station key reading an entire notebook, task list and briefing leaves one coarse timestamp, at minute granularity.

**Evidence.** auth.go:117 — "It also means a stolen station key could read an entire notebook, task list and briefing with no trace at all. This is a coarse signal — throttled to about once a minute, no per-read record — but the difference between 'no timestamp' and 'used four minutes ago' is the difference between an unanswerable incident and a scoped one." TARGET-ARCHITECTURE.md §7 records the same gap for `api_token`: no column in either database records a request source address.

**Deferred because.** The throttled touch was added as the cheap first step (a poll loop must not amplify into a write per request); the per-read record was not attempted.

**Still open.** Confirmed outstanding. `TouchToken` now runs on the station path (it previously never did, so the console's last-used column was permanently blank and an operator read blank as "unused"), but the signal is deliberately coarse: throttled to about once a minute, no per-read record, no request source. "Was this credential used by someone else" remains unanswerable, and the same gap is recorded independently for `api_token` at the schema level.

*Recorded in: code comments*

##### 42. A station with no bound endpoint can be a room member and is deaf; the console flags it rather than preventing it

`internal/web/stations.go:206-236 (D1)`

**Outstanding.** The condition itself is permitted by design. The mitigation is a `Deaf` count and a per-member badge on the console — a human has to look. Nothing tells the station, and nothing tells the sender.

**Evidence.** stations.go:206-215 — "A station with NO BOUND ENDPOINT can be added to a room, the console flashes success, and the station is a member on paper and deaf in practice… ken-promo was added at 2026-08-13T15:39:59Z with zero bound endpoints and concluded from the resulting silence that rooms were RECEIVE-ONLY — a wrong belief about the product, reported upward, by the station whose charter is describing the product."

**Deferred because.** Explicit: "ADMITTED AND FLAGGED rather than refused, because Vlad's specification is that membership is durable: 'once a room is created and parties are added, they should permanently be able to use it'. Adding a station before its session binds is legitimate… Refusing would make the console wrong about a legitimate order of operations; staying silent makes it wrong about the outcome."

**Still open.** Confirmed outstanding, and admitted-not-refused for a stated specification reason: Vlad's requirement is that membership is durable ("once a room is created and parties are added, they should permanently be able to use it"), so adding a station before its session binds is legitimate. What is carried is that the only surface saying the station cannot yet hear is a console badge a human must look at — nothing tells the station, nothing tells the sender. The cost is measured and severe.

*Recorded in: code comments*

##### 43. oauth_grant.scope is stored, echoed to the client, and has no effect — capability is hardcoded

`migrations/0008_oauth.sql:38; internal/mcpserver/auth.go:190-201`

**Outstanding.** An OAuth connector's capability is a compile-time constant, independent of the scope the human approved and independent of the scope string returned in the token response. Any move toward OAuth as THE mechanism has to decide whether the scope column becomes load-bearing or is removed.

**Evidence.** 0008:38 declares `scope TEXT NOT NULL, -- granted OAuth scope string (cosmetic; capability is fixed)`. internal/mcpserver/auth.go:196-200 confirms it: after `ValidateOAuthAccessToken` the principal is built with a literal `scopeSet([]string{scopeRead, scopeWriteDraft, scopePropose})`, discarding `op.Scope`. The value is still selected (internal/store/oauth.go:264,334) and returned to the client (:314), so a connector is told about a scope that decides nothing. No comm or station scope is reachable through OAuth at all.

**Still open.** Confirmed at HEAD. A connector is told about a scope that decides nothing, and the target architecture pushes toward OAuth — so whether the column becomes load-bearing or is removed is a live decision, not a cosmetic one.

*Recorded in: migrations + schema*

### Text that describes a control Ken does not have

##### 44. The degraded state is called "COMM off" everywhere, and that configuration no longer exists

`internal/stationserver/stationserver.go:369 (station_directory's description) and :47/:52/:794; internal/i18n/locales/messages.properties:602 + es:680 + fr:685 (flash.station_link_revoked_comm_off); cmd/ken/main.go:516-542`

**Outstanding.** Relabel, and carry a degraded marker on the briefing. Rewiring is not available — the degraded state is deliberate.

**Evidence.** Hearsay, Staffing and CommEndpoints are decided once at boot inside `if commStore != nil`, and the only remaining route to nil is comm.db failing to open — which main.go already calls DEGRADED. Two sites are the propagating kind: station_directory tells sessions "'staffed' is absent when this deployment has COMM off", and the flash tells the operator that channels "may still be open" and to "Re-check from the Comm console once COMM is on" — in exactly the state where /comm 404s and its nav entry is hidden. Meanwhile the real fault is named nowhere: the briefing drops comm_endpoint_ids with no reason for two different causes a session cannot tell apart.

**Deferred because.** No reason recorded. Related switch-removal sweep was closed in 3.10.0; this is the residue it did not cover, because these strings describe a STATE rather than a switch.

**Still open.** Two of the sites propagate — one to sessions, one to the operator — and the flash tells the human to re-check from a console that 404s in exactly the state it fires. The real fault is named nowhere.

*Recorded in: audits + briefs*

##### 45. Six documents still sell OAuth as an optional feature gated by KEN_OAUTH_ENABLED, a variable deleted in 2.0.0

`docs/OAUTH.md:3,10,36,45,115 · docs/INSTALL.md:252,258,262,268 · docs/MCP-TOOLS.md:54 · docs/AI-INTEGRATION.md:267 · docs/DESIGN.md:237,382,433 · README.md:70,88`

**Outstanding.** Rewrite all six documents to say OAuth is unconditional. OAUTH.md needs the most work — its title framing ("optional"), its "Enable it" section (a systemd drop-in setting a variable that does nothing), and its closing line "All are inert unless KEN_OAUTH_ENABLED is set" are all false; an operator following it will add a dead Environment= line and conclude OAuth is off. INSTALL.md's whole section is headed "Optional: OAuth connector for claude.ai".

**Evidence.** cmd/ken/main.go:272 is `oauthEnabled := true`, with a comment reading "OAuth is not optional and never should have been… Defaulting it OFF meant a fresh install could not be connected the documented way until the operator found a variable nothing pointed them at." cmd/ken/surfaces_core_test.go:34 lists KEN_OAUTH_ENABLED among retired gates and fails if any code reads it. COMPATIBILITY.md:37-38 records the removal in 2.0.0. Commit 0c0f687 ("stop three documents asserting a switch that is gone") swept DESIGN.md for KEN_COMM_ENABLED and KEN_STATION_ENABLED only — the OAuth assertions in the same file at lines 237, 382 and 433 were left standing, and OAUTH.md has not been touched since commit c0d4d34 (Ken 1.1.0).

**Deferred because.** None stated. The 2.0.0 sweep and the 3.10.0 sweep both fixed COMM/STATION wording and did not look for the third variable removed in the same release.

**Still open.** Confirmed unfixed. All six documents still assert optionality; the code is unconditional and a source test forbids reintroducing the gate. Operator-harmful: OAUTH.md instructs adding a systemd Environment= line that does nothing.

*Recorded in: docs tree*

##### 46. AI-INTEGRATION.md tells every AI "You cannot open a channel" — comm_open_channel has existed since stations shipped

`docs/AI-INTEGRATION.md:455-457`

**Outstanding.** Correct the one document whose stated purpose is to be handed to an agent. It currently teaches only the pairing-code path and states the link path does not exist, which steers every session that reads it onto the exact mechanism the target architecture wants removed.

**Evidence.** docs/AI-INTEGRATION.md:455 — "**A human must pair the sessions.** You cannot open a channel; your human mints a pairing code in Ken's web UI and gives it to both sessions." But internal/commserver/commserver.go:439 registers `comm_open_channel`, described as "Open a channel with another STATION your human has already linked to yours — no pairing code", and docs/STATIONS.md:20 lists it under Built. Commit d593434 further made link approval open the conversation itself.

**Deferred because.** None stated — the section predates stations and was never revisited.

**Still open.** Confirmed false as written. The document is the one handed to agents, and the sentence steers sessions onto the pairing-code path the target architecture wants retired.

*Recorded in: docs tree*

##### 47. OPERATION.md still says a comm token can only be minted from the CLI — the console gained it in 3.10.0

`docs/OPERATION.md:60`

**Outstanding.** Correct the surface table, which is the first thing the manual shows an operator and which currently contradicts the manual's own stated posture ("The console is the main method for any operation"). The trailing "*Being fixed: the console will mint these too.*" is the tell — it was fixed after the manual was committed.

**Evidence.** docs/OPERATION.md:60 — "**CLI only today** … The console form offers only the three knowledge-base scopes and **silently discards anything else**, so a comm token cannot be minted there." internal/web/app.go:838-854 now defines `consoleCommScopes = []string{"comm", "comm-file"}` with the comment "UNTIL 3.10.0 THE CONSOLE COULD NOT MINT THESE AT ALL", and `agentScopeOK` (internal/web/app.go:856-868) accepts them. The manual is stamped "partially re-verified against v3.9.0" (docs/OPERATION.md:3-4), i.e. one release before the fix; commit ba661d9 landed it.

**Still open.** Confirmed stale. The manual's own stamp explains it (re-verified against v3.9.0, one release before the fix), and the trailing "Being fixed" is the tell. It contradicts the manual's stated console-first posture on the first table an operator reads.

*Recorded in: docs tree*

##### 48. Migration 0001's scope comment lists 4 scopes; Ken now mints 8, and one of them is not checked by anything

`migrations/0001_init.sql:66-67; internal/store/scopes.go:14-23; internal/stationserver/auth.go:32`

**Outstanding.** The `api_token.scopes` comment reads `-- subset of: read | write-draft | propose | curate`. The vocabulary is now read, write-draft, propose, curate, comm, comm-file, station, station-locker. Separately, `station-locker` is minted on every station key and enforced by nothing.

**Evidence.** internal/store/scopes.go:20-23 declares eight valid scopes; scopes.go:14 states "the vocabulary is deliberately larger than what any tool checks today: `comm-file` and `station-locker` are RESERVED." That is now half-stale too: `comm-file` IS checked (internal/commserver/commserver.go:871-877 `requireFileScope`), while `station-locker` is not — cmd/ken/cli_station.go:86-88 and internal/web/stations.go:484-486 both write it "only so the key's recorded scope matches" while "the server now gates it on `station` alone".

**Deferred because.** Stated in scopes.go: "Splitting a shipped scope into two later is a MAJOR change; merging two is free — so the cheap direction is to declare early and merge if it turns out one was enough."

**Still open.** Two separate live inaccuracies, both confirmed. The 0001 comment is stale on its face, and scopes.go's own claim that comm-file and station-locker are both RESERVED is now half-false — comm-file IS enforced. station-locker remains minted-and-unenforced.

*Recorded in: migrations + schema*

##### 49. Migration 0012 says the station tables ship empty "unless KEN_STATION_ENABLED is set" — that flag no longer exists

`migrations/0012_stations.sql:11-14; cmd/ken/main.go:503`

**Outstanding.** The migration's stated gating model is false on every current deployment. A reader reaching for the flag to disable stations will not find one, and the comment is the file the design points at as authoritative for the station data model.

**Evidence.** Migration: "These tables SHIP EMPTY unless KEN_STATION_ENABLED is set. ken.db migrations are unconditional, so the flag gates the tool surface, the instructions and the console — not the schema." cmd/ken/main.go:503: "There is no KEN_STATION_ENABLED any more, as there is no switch for…". cmd/ken/surfaces_core_test.go:13 records the transition: the surfaces "were then made core". Per the 0012 precedent the migration prose should not be edited in place (SQLite stores CREATE statements verbatim) — so the correction has to land somewhere a reader will reach.

**Still open.** Confirmed false at HEAD, and it is embedded verbatim in every deployment's sqlite_master. Also already logged as an open sweep item, and the 0012 precedent forbids editing the migration prose in place — so where the correction lands is an open question, not a one-line fix.

*Recorded in: migrations + schema*

##### 50. 0017 claims the roster epoch is "carried on every delivered message" — nothing has ever written message.audience_epoch

`migrations/0017_comm_rooms.sql:75-87; internal/comm/migrations/0009_delivery.sql:93-97`

**Outstanding.** The receiver-side half of the roster-epoch design does not exist. A session given a standing instruction to auto-process a room cannot tell from a message that the membership moved; the epoch is only available by separately calling comm_directory.

**Evidence.** 0017:76-81: "Carried on every delivered message so a receiver can tell that the set it was addressed WITH has changed since… When the roster moves, the epoch moves, and the grant it was given no longer describes the room it is in." `audience_epoch` has ZERO Go references anywhere in the tree. Both send paths insert the column list `(… audience_size, expires_at)` (internal/comm/message.go:261) and `(… audience_size, kind, expires_at)` (internal/comm/room_send.go:220) — `audience_epoch` is in neither, so every row holds 0009's literal default 0. The epoch IS surfaced, but only on the directory result (internal/commserver/commserver.go:377-378, types.go:386-389).

**Deferred because.** 0009's own comment: "slice 5 gives them meaning, and slice 6 uses the epoch to lapse a standing auto-process grant when membership moves." Slice 5 shipped; slice 6 does not appear in FINISHING.md at all.

**Still open.** Confirmed: the receiver-side half of the roster-epoch design does not exist, and the migration asserts it does. A session with a standing auto-process grant cannot detect that the room moved. Distinct from item 24 (that is comm.db's mirror_state.roster_epoch).

*Recorded in: migrations + schema*

---

## B. Everything else

**102 entries.** Real, recorded, and independent of which way the identity question goes.

### Observed defects — behaviour that is wrong, not merely unfinished

##### 51. kb_record_outcome's loop is closed one time in seven, and nothing prompts for it at the moment it is owed

`docs/FINISHING.md:230 (Batch 2, unticked)`

**Outstanding.** Either build something that asks for an outcome at the occasion one is owed, or stop instructing every session to close a loop it skips 85% of the time. No remedy is proposed in the file — only the diagnosis and the assertion that it is upstream of every badge question.

**Evidence.** "250 recorded uses, 37 outcomes — 14.8% — and only 22 of 108 entries have any outcome at all." Denominator verified from source in the item itself: use_count is bumped only by Store.Get, whose sole caller is kb_get (server.go:493); the console's GetEntry and kb_search deliberately do not bump. So a "use" is exactly "an agent fetched the full entry to apply it". Named "a defect, not a preference — the same family as session_id: something the instructions request that nothing prompts for at the moment it matters."

**Deferred because.** None stated. It sits unticked among Batch 2's findings, which the batch header says are "still being worked".

**Still open.** Unticked at FINISHING.md:230 and nothing in the tree has changed. The instruction that solicits the outcome is still frozen into every session, and there is still no prompt at the occasion. This is an open design question (build a prompt, or stop asking), not a bug someone forgot to close.

*Recorded in: FINISHING.md*

##### 52. The vault read log collects the actor id, carries it to the view model, and drops it at render

`docs/FINISHING.md:275 (Batch 2, unticked); internal/store/station_vault.go:394-403; internal/web/templates/stations.html:407`

**Outstanding.** Either render the reader (resolved to a name — "a bare integer is not an identity a human can read") or stop selecting it.

**Evidence.** Verified in the tree today: StationVaultReads does `SELECT name, via, read_at, COALESCE(by_actor_id,0)` and scans into r.ActorID; the template emits `{{.Name}} · {{.Via}} · {{localtime .ReadAt}}` and never uses ActorID. The tool description has already been narrowed to promise only what is shown, so the data is collected for nobody. (The file cites stations.html:405; it is now :407.)

**Still open.** Unticked at FINISHING.md:275 and re-verified end to end today. Data is collected, scanned into the view model, and discarded at the last step — either render it or stop selecting it. Small, concrete, still open.

*Recorded in: FINISHING.md*

##### 53. Only human-blocked tasks are aged; an 'overtaken rather than stale' task is invisible

`docs/FINISHING.md:294 (Batch 2, unticked); internal/store/station_tasks.go:430-436`

**Outstanding.** Age EVERY open task since creation, not just blocked_on='human'.

**Evidence.** Verified: the briefing aggregate is `COALESCE(MAX(CASE WHEN blocked_on='human' THEN CAST(julianday('now') - julianday(created_at) AS INTEGER) END), 0)` — gated on human. 3.6.0 shipped oldest_blocked_on_human_days (internal/stationserver/types.go:60) and nothing else. ken-promo's example: "read the 1.5.1 and 1.5.2 promo briefs", created 2026-07-30, still accurate and pointless at 3.6.0 on 2026-08-14, blocked_on='self', so briefed_count could never have caught it and age since creation would have.

**Still open.** Unticked at FINISHING.md:294, aggregate unchanged. Real gap with a concrete production instance, and it is constrained by a separate decline (age must surface, never act).

*Recorded in: FINISHING.md*

##### 54. The four pending counters still disagree, in the file that says they must not

`docs/FINISHING.md:379 (raised by the Batch 3 sweep, unticked); internal/comm/pending.go:49-56 vs internal/comm/room_mirror.go:96-101 and internal/comm/channel.go:679-688`

**Outstanding.** Give RoomsFor and PendingForEndpoint the same expiry predicate pendingScopeSQL carries, or state why they may differ.

**Evidence.** PendingForEndpoint's count clause is `AND d.state = 'queued'` with no expiry test; RoomsFor's subselect is `AND d.party_key = ? AND d.state = 'queued'` — single party, no expiry. Both PendingTotalFor and BroadcastPendingFor go through pendingScopeSQL and DO carry it. Effect visible in one comm_channels result: pending_total=0 beside a per-channel count of 1 between a message expiring and the next ~60 s sweep — and the frozen instruction block tells sessions to read pending_total FIRST. Bounded by the sweep interval; over-reports, never under. RoomsForParty still has zero callers while its doc claims two roles.

**Deferred because.** No reason recorded. Raised by the sweep's completeness critic, listed under "Raised by the Batch 3 sweep, not fixed in it", and left.

**Still open.** Two of the four still lack the expiry predicate the shared fragment carries, and the one instruction every session is told to read first (pending_total) can disagree with the per-channel number beside it.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 55. 70 keys are missing from messages_es and 70 from messages_fr — every rooms string, every station storage setting, three room flashes

`docs/FINISHING.md:392 (raised by the Batch 3 sweep, unticked); internal/i18n/locales/`

**Outstanding.** Translate. Measured now rather than recalled: 70 keys absent from each, down from the 89 recorded in Batch 3 — the 19 stations.vault* keys (including the unencrypted-storage warning) HAVE since been translated.

**Evidence.** Measured today: messages.properties has 686 keys, messages_es and messages_fr 616 each, and the two missing sets are IDENTICAL at 70 keys — the whole rooms.* surface (rooms.add, rooms.archive, rooms.archived_help …), flash.room_*, flash.station_vault_restore*/reveal_failed, proposals.via_comm_*. The checklist's "89" is stale: 0c0f687 (3.10.0) translated the 19 stations.vault* keys.

**Deferred because.** "Pre-existing and unchanged by this batch; noticed while checking that new keys landed in all three."

**Still open.** Unticked at FINISHING.md:392 and measurably still true. A missing key renders raw, so this is operator-visible today; only the count in the checklist is stale, and that is worth recording rather than a reason to drop.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 56. Two migration runners, only one hardened — internal/store's has no FK pin and no foreign_key_check

`docs/FINISHING.md:479 (found by the Batch 4 sweep, unticked); internal/comm/comm.go:357-395 vs internal/store/migrate.go:17`

**Outstanding.** Give the internal/store runner the same treatment, or record why ken.db does not need it.

**Evidence.** Verified: internal/comm's Migrate pins a connection, sets `PRAGMA foreign_keys=OFF` outside the transaction for the whole run, restores it, and runs `PRAGMA foreign_key_check` afterwards — "with the measurement that bought it in the comment". internal/store/migrate.go does none of these; the only foreign_key_check in internal/store is in snapshot.go:209.

**Still open.** Unticked at FINISHING.md:479, gap confirmed by reading both runners. The hardened one carries the measurement that bought it; the other has none of it, and either it needs the same treatment or the reason it does not must be written down.

*Recorded in: FINISHING.md*

##### 57. Rule 2 has failed four times and its named remedy — shell enforcement — was never built

`docs/FINISHING.md:83-102 (Where we are today, narrative — no checkbox)`

**Outstanding.** Make the tick mechanically inseparable from the commit that closes an item. The file names the requirement and does not implement it.

**Evidence.** Four recorded failures, the most instructive being the third: "9219414 shipped the dm change and its tick landed separately in a8221f3, because the script that was meant to write it asserted, failed, and the git commit on the next line ran anyway with nothing chaining them. Knowing the rule was not enough; the shell has to enforce it." And a fourth of a different kind — the 3.8.0 release commit stamped by blanket find-and-replace of *unreleased*, which "stamped 3.8.0 onto two items that had shipped in 3.7.0, mangled two sentences mid-clause" and rewrote the sentence above it into the wrong tense.

**Deferred because.** None stated — each failure is recorded, none produced a mechanism.

**Still open.** The remedy is named in the file and does not exist in the tree. It is also load-bearing for this index specifically: if ticks drift from commits, the checklist's own state cannot be trusted, and two items here exist only because of that drift. The fourth failure (blind find-and-replace) shows the failure mode also corrupts evidence, not just status.

*Recorded in: FINISHING.md*

##### 58. An explicitly approximate heuristic is still frozen into a durable channel seat and read as an identity

`internal/comm/channel.go LiveEndpointForStation (`ORDER BY last_seen_at DESC LIMIT 1`) → the endpoint_b write in OpenLinkedChannel; batch3 finding N3`

**Outstanding.** The three downstream defects were fixed individually in 3.8.0; the root was not. The chosen rowid is still written into channel.endpoint_b and never updated, and OpenLinkedChannel still reuses a channel by STATION PAIR — so the seat chosen at the first open outlives every session on both sides, permanently.

**Evidence.** LiveEndpointForStation is unchanged and its doc still justifies the approximation for ADDRESSING only ("whichever endpoint is chosen, the message lands in the STATION's inbox"). The critic: "this is the one place where 'which endpoint of a station' is decided by an ORDER BY … LIMIT 1, and the answer is durable" — and it should be fixed once rather than three times.

**Deferred because.** Reported as ROOT CAUSE with an explicit "do not double-count", so it was recorded rather than opened as a seventh item; FINISHING absorbed it into the closed sweep entry as commentary.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 59. station_me returns the station's endpoints predecessor-first, under a field doc that asks a singular question

`internal/comm/endpoint.go EndpointIDsForStation (`ORDER BY id`); internal/stationserver/types.go CommEndpointIDs doc; batch3 finding N5`

**Outstanding.** Wording plus ordering — newest first would at least make the likely-correct id first.

**Evidence.** The query is still `SELECT endpoint_id FROM endpoint WHERE station_id=? AND revoked_at IS NULL ORDER BY id`, i.e. registration order, and E1 dying does not set revoked_at. The doc still frames it singularly — "the answer to 'which endpoint_id should I be calling comm_* with?'" — while the jsonschema frames it as the membership test, which is the only answerable form (station_me has no comm identity). A session that takes the first id adopts its predecessor's, has no secret for it, and lands on an auth error whose text lists three causes, none of which is what happened.

**Deferred because.** No reason recorded. Found by the completeness critic; never carried into FINISHING's "raised but not fixed" list, so it is currently tracked nowhere but the audit.

**Still open.** Wording and ordering both still wrong, and the failure mode is a session adopting an id it holds no secret for.

*Recorded in: audits + briefs*

##### 60. The sending station is still re-derived from the live binding at two sites, so an unbind blanks the sender's name retroactively

`internal/comm/provenance.go:97 (`COALESCE(se.station_id,'')`); internal/comm/message.go:1182 (same, in MessageByID, feeding messageView.FromStationID)`

**Outstanding.** Read the recorded `m.sender_party` written in the sending transaction instead of joining to the endpoint row.

**Evidence.** Both queries still LEFT/INNER JOIN endpoint se ON se.id = m.sender_endpoint and take station_id off the LIVE row. `grep -n "se.station_id" internal/comm/*.go` returns exactly these two. Confirmed defects in the sweep (provenance.go:97 and message.go:1205, schema-shape and inverse-bug lenses): a sender's comm_unbind blanks from_station_id on every message it ever sent, and the same value is what N2's hearsay fix would point sessions at.

**Deferred because.** No reason recorded. The sweep's six named fixes did not include these; FINISHING ticked the sweep item as shipped in 3.8.0 without listing the residue.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 61. Attachment idempotency is still keyed on sender_endpoint, against a rule 0009 decided in writing

`internal/comm/file.go:248 (`WHERE channel_id=? AND sender_endpoint=? AND idempotency_key=?`); internal/comm/migrations/0002_files.sql:63 (the matching UNIQUE index)`

**Outstanding.** Re-key to the party, or record why file transfer keeps the connection form.

**Evidence.** 0009 re-keyed message idempotency to (scope, sender_party) with the stated reason that "a session that reconnects under a new endpoint must not be treated as a new sender"; the attachment path never followed. Four lenses reached this line; the verifier's correction is that the index does not permit the duplicate — the READ at file.go:248 does.

**Deferred because.** Downgraded to low in the sweep (nothing E2 inherits is denied), and left. No decision recorded either way.

**Still open.** Migration 0009 re-keyed message idempotency to the party with a stated reason; the attachment path never followed, so a session reconnecting under a new endpoint is treated as a new sender for file offers.

*Recorded in: audits + briefs*

##### 62. Cumulative ack has no claim clause, so a sibling reader settles a row another reader holds a live lease on

`internal/comm/message.go, AckUpTo's scope select (`WHERE m.scope_id=? AND m.scope_seq<=? AND d.state='delivered' AND <partyPredicate>`)`

**Outstanding.** Decide whether cumulative ack should respect the claim lease, or state that it deliberately does not.

**Evidence.** The SELECT still carries no claim predicate. With E1 holding a live lease on scope_seq 5, E2 (same station) polls 6 — 5 is hidden from its poll by the claim arm — and comm_ack up-to-6 settles 5 anyway. Confirmed reachable and real by the sweep's inverse-bug lens.

**Deferred because.** No reason recorded; classified low and not carried into any batch.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 63. station_task.blocked_on_station has no writer, is advertised in every task tool's schema, and the docs tell sessions to fill it

`internal/stationserver/types.go taskAddIn (no such field) vs taskView:BlockedOnStation; internal/store/station_tasks.go:120-124 (bind exists); docs/STATIONS.md:751 and :769`

**Outstanding.** Three parts, not one: the input field, the existing bind, and a cell in stations.html — which renders it nowhere today, so wiring only the writer leaves the human's `peer` filter unable to say which peer. Or drop the column, the field, the JSON key and the two doc lines.

**Evidence.** Column with a real FK, both column lists, both scanners, the MCP view and the JSON tag all exist; taskAddIn has Text/BlockedOn/Detail/Context/RemindAfter and nothing sets the struct field. Because addTool derives the output schema from the Out type, it is advertised in the declared schema of every task tool while appearing in no result. STATIONS.md:769: "`peer` — another *station* owes something; name it in `blocked_on_station`."

**Deferred because.** Dropping it needs a migration, which ships alone.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 64. station_task.context is stored and returned by no surface; the same view drops LastDeferReason, which the console DOES show

`internal/stationserver/stationserver.go taskViewOf; internal/web/templates/stations.html:183`

**Outstanding.** Finish the view + the template + widen the regression test to the whole field set; or drop the input, the column and the cap arithmetic.

**Evidence.** taskViewOf enumerates fields by hand and emits neither Context nor LastDeferReason. context shares the 4 KiB cap with detail, so provenance a session writes comes out of the only field the human can see. taskViewOf's own comment says it exists "because the store can grow a field the view silently drops", and the regression test written for exactly that enumerates fields by hand too and asserts neither Context nor BlockedOnStation. Asymmetry worth naming: the human sees why a task was deferred (stations.html:183) and the session staffing the station cannot.

**Deferred because.** No reason recorded.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 65. The vault read trail cannot name the key that read the secret, and the actor it does record is never rendered

`internal/store/station_vault.go:243 (INSERT by_token_id) vs :394 (SELECT name, via, read_at, by_actor_id); internal/web/templates/stations.html:407`

**Outstanding.** Select and render both. Adjacent and same shape: internal/store/station_locker.go:105-110 writes updated_by_token_id/updated_by_actor_id and neither of that table's SELECTs (:54, :124) reads them.

**Evidence.** by_token_id is written and selected by nothing. by_actor_id IS selected into StationVaultRead.ActorID and the template renders `{{.Name}} · {{.Via}} · {{localtime .ReadAt}}` — the data is collected, carried to the view model, and thrown away at the last step. Migration 0016's header states the trail's whole purpose is "who saw it, and when"; 0015 established by measurement that the actor is per machine — six of production's eight stations share one — so the KEY is the only column that can answer.

**Deferred because.** No reason recorded. FINISHING carries the actor half as an open item; the token half and the locker columns are only in the audit.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 66. §10's "notebook, task and locker views" — one of the three exists, and the sentence is load-bearing for the privacy stance

`docs/STATIONS.md §10 (:557) and §5 (:556-558); docs/BACKUP.md:32-33; internal/web/app.go:183; internal/store/station_tasks.go CrossStationHumanTasks`

**Outstanding.** Build the views or strike all three claim sites — and if the claim goes, handleStationLocker and its route become dead code to remove with it.

**Evidence.** No notebook view: ListStationNotes/ReadStationNote have zero web callers (only internal/stationserver/stationserver.go:441). Locker: GET /stations/{id}/locker exists with a full handler and nothing links to it, no file NAMES are listed anywhere, and there is no console delete — so the check the handler's own comment calls "the only control the design actually has" needs a name only a session can supply. Task view: CrossStationHumanTasks hardcodes `t.state='open'`, so the closed record §9 budgets 2000 rows to retain has no reader. BACKUP.md:32-33 repeats "read a station's locker in the console if you ever suspect one" as security guidance.

**Deferred because.** No reason recorded.

**Still open.** Three claim sites rest on views that are not built, and one of them is security guidance in BACKUP.md. If the claim goes, handleStationLocker becomes dead code to remove with it.

*Recorded in: audits + briefs*

##### 67. The promotion decision is write-only: entry_slug is never selected, and the badge its counter feeds does not exist

`internal/store/station_notes.go:442 (ResolvePromotion writes entry_slug), :453-459 (CountPendingPromotions), :409/:456 (both queries filter state='pending'); internal/web/stations.go handleStationsCount`

**Outstanding.** A decided list or a converted marker linking /entries/<slug>, and promotions in the console count. Or drop entry_slug, the input and the dead counter.

**Evidence.** `grep entry_slug` over Go finds the UPDATE and one test SELECT — nothing in the product reads it, so the operator is invited to type the slug answering "which knowledge came from which station page" into a field nothing displays. CountPendingPromotions has zero callers while its doc calls it "the console's badge source"; handleStationsCount counts pending REQUESTS only, so a promotion filed while the operator is watching moves nothing until a manual reload.

**Deferred because.** No reason recorded.

**Still open.** The operator is asked to type the answer to 'which knowledge came from which station page' into a field nothing displays, and the counter documented as the badge source feeds no badge.

*Recorded in: audits + briefs*

##### 68. A promotion decision never reaches the station that asked, and the comment on the handler says it does

`internal/stationserver/* (nothing reads station_promotion); internal/web/stations.go handlePromotionResolve doc comment; internal/store/station_notes.go:333-334`

**Outstanding.** Report recent outcomes in the briefing (or a state on station_note_list). Correct the comment either way.

**Evidence.** `grep -rn station_promotion internal/stationserver` returns nothing; meOut has no promotion field; station_note_promote returns pending_human_review and that is the last the station hears. PromoteStationNote inserts unconditionally with UNIQUE only on promotion_id, so a session that cannot see its request was DISCARDED re-promotes and piles duplicate pending rows on the human. The handler comment still ends "…this only closes the loop so the request stops waiting and the station can see it was answered."

**Deferred because.** No reason recorded. Noted at source: the comment was born false, in the commit that gave the table its first reader.

**Still open.** The station cannot see a DISCARDED verdict, and PromoteStationNote has no guard against re-asking — so duplicates pile on the human by design of the gap.

*Recorded in: audits + briefs*

##### 69. Station-key revocation still has no console control and no blast-radius count, while two doc sections say it ships

`internal/web/templates/stations.html:320-322 (Retire only); internal/web/templates/tokens.html:91; internal/store/station_binding.go:271 (StationKeyOwner) and internal/comm/endpoint.go:344 (CountEndpointsBoundBy); docs/STATIONS.md:271 and :687`

**Outstanding.** The pre-click count. The /tokens confirm now NAMES the station (fixed in 3.8.0, 81f59f1) but still states no number, and /stations offers Retire with no revoke at all.

**Evidence.** Both halves of the count were written and wired to nothing: StationKeyOwner has zero callers anywhere; CountEndpointsBoundBy has one, a test. Each carries S6's requirement in its own doc. Every neighbouring destructive control already does this — link revoke states the live-channel count (stations.html:490). STATIONS.md:271 promises the console "states the count before the click — 'this will disconnect 2 live sessions'" and :687 lists "the key list with retire / revoke and revoke's 'this will disconnect N live sessions' count".

**Deferred because.** Scope is recorded: finish on the console ONLY — `ken token revoke` runs with no comm.db handle by construction and cannot produce the count.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 70. A human cannot close, defer, drop, reopen or add a task from /stations — and an archived station's human-blocked tasks are unclosable from any human surface

`internal/web/app.go:182-194 (no task-state route exists); internal/store/station_tasks.go terminateTasks; docs/STATIONS.md §11.8`

**Outstanding.** A close control, or a sentence in §11.8 stating that closing is a session-only verb so the gap is a decision rather than an omission.

**Evidence.** The cross-station pile — the part the page comment calls "the part that earns the page" — defaults to blocked_on='human' and is strictly read-only for state; every verb's only production caller is the MCP tool. terminateTasks scopes by station_id and archived stations' tasks stay in the pile by design, so the only escape for an archived station's human-blocked task is the transfer checkbox followed by a session on the receiving station.

**Deferred because.** No reason recorded. Distinguished at source from the other seams: this is an asymmetric surface, not an unfinished one — no orphan route, no orphan i18n key, and §11.8 promises only a view.

**Still open.** The cross-station pile — the part the page exists for — is read-only for state, and an archived station's human-blocked task has no human-side escape at all.

*Recorded in: audits + briefs*

##### 71. An undecided station request never lapses, cannot be withdrawn, and station-kind rows accumulate with no dedupe

`migrations/0012_stations.sql:94-95 (CHECK admits 'expired'); cmd/ken/main.go:585-605 (janitor touches sessions, OAuth and binding vouchers only); internal/store/station_console.go:432-439; internal/store/stations.go:447`

**Outstanding.** Expire past an age in the existing janitor and let the dedupe ignore expired rows. Deleting the state instead means a table rebuild plus a dismiss action.

**Evidence.** `grep "'expired'" internal/store/*.go` returns nothing — no producer. CreateStationLinkRequest returns the existing pending id for the pair in either direction, so a station cannot replace a pending request with a better reason; CreateStationRequest has no dedupe at all. ArchiveStation leaves pending requests behind. The denial ledger covers the human saying no; nothing covers the human saying nothing.

**Deferred because.** No reason recorded.

**Still open.** The schema anticipates the state and nothing produces it; the janitor that would expire it already exists and does not look. The denial ledger covers the human saying no; nothing covers the human saying nothing.

*Recorded in: audits + briefs*

##### 72. decision_reason is mandatory to type and impossible to read

`internal/store/station_console.go:107-109 (refuses without it), :131 (stores it); no SELECT anywhere; internal/i18n/locales/messages.properties:513 and :576 (+ es/fr), rendered at internal/web/templates/stations.html:243-246`

**Outstanding.** A decided-requests read, or surface the last denial on the request being shown.

**Evidence.** `grep -rn decision_reason` over Go finds the UPDATE and nothing else. DenyStationRequest's own doc states the purpose — "the next request from the same station arrives to a human who can see what was already said no to" — and the promise is made to the operator at the moment of typing, in three locales. The task equivalents this pattern was copied from (resolution, resolution_link, last_defer_reason) are all read back. For `link` the escalating mute partly covers the window; for `station` there is no mute, so an immediate re-ask reaches the human with zero record.

**Deferred because.** No reason recorded.

**Still open.** A promise made to the operator at the moment of typing, in three locales, that nothing keeps. For station-kind requests there is no mute either, so an immediate re-ask reaches the human with zero record.

*Recorded in: audits + briefs*

##### 73. A curator sees a "stale" badge and cannot find out why — curation_event.note is written by nine sites and selected by none

`internal/store/write.go:373 (INSERT with note, from_state, to_state, actor_id, actor_kind, session_id, version_id); internal/store/v1tools.go:129-130 (the only read, event_type only)`

**Outstanding.** An events strip on /entry (type + note + actor + time) against the existing index — it would make flag_stale, promote and reject notes visible at once. Deleting instead means stopping asking agents for a reason.

**Evidence.** The only reader is RecentContext, which selects slug, title, summary, kind and the last event_type. kb_flag_stale REQUIRES a reason and folds suspected_applies_to into it; it lands in curation_event.note and is unreachable from every surface.

**Deferred because.** No reason recorded.

**Still open.** kb_flag_stale REQUIRES a reason and the reason reaches no surface, which is also what makes the irreversible-flag item unresolvable by the curator.

*Recorded in: audits + briefs*

##### 74. The whole wikilink graph is write-only, agents are instructed to write into it, and DESIGN.md lists it as a shipped surface

`internal/store/write.go:151 and internal/store/import.go:65 (the only two touches); docs/DESIGN.md:366`

**Outstanding.** One Related block on /entry (outbound + inbound) — the data, the indexes and the dangling resolution already work. Or delete the table, the trigger, links[] and the instruction.

**Evidence.** `grep -rn entry_link --include=*.go` returns exactly two INSERTs and no SELECT, in code or tests. link_type is validated up front so a bad one is a clean error. kb_get returns no links, search does not rank on them, /entry does not render them. DESIGN.md:366 lists a "[[link]] graph" among the shipped Human-UI surfaces, contradicting its own roadmap lines.

**Deferred because.** No reason recorded.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 75. parent_version_id is write-only, and the drift warning is delivered to the party that cannot act on it

`internal/store/write.go:334 (the only touch); docs/MCP-TOOLS.md ~:243-246; docs/DESIGN.md:200; internal/web/templates/entry.html:84-135`

**Outstanding.** Select it into ReviewData and show "based on rev N (head is M)" next to Promote — every version ever written is already populated.

**Evidence.** Written at write.go:334 and selected nowhere. The rebase warning is computed in memory at propose time and returned to the AI, which cannot rebase; ProposalReview loads the proposal and the current head with no base field, so a proposal based on rev 2 against a rev-5 head is reviewed as a rev-5 delta. write.go says "review will show the drift", MCP-TOOLS renders that as "a 3-way diff" explained "from parent_version_id lineage" — and the console review renders no diff at all, so the claim is wrong twice.

**Deferred because.** No reason recorded.

**Still open.** Every version ever written is already populated; the review that would use it loads no base field, and two documents describe a diff the console does not render.

*Recorded in: audits + briefs*

##### 76. curated_rev is a promotion counter presented to humans and agents as a revision number

`internal/store/promote.go:131 and :265 (both increment); internal/web/templates/entry.html:70 ("Curated head"), browse.html:56 ("Rev"); internal/i18n/locales/messages_es.properties:209 ("Versión curada"); internal/model/model.go:65 (kb_get)`

**Outstanding.** Rename to promotion_count and label it honestly in both templates and kb_get. The maturity half WAS fixed in 3.7.0 — maturity() now reads deduped outcome evidence, and search.go:227-231 records the ten-alternating-reverts measurement that killed the old rule — but the label was not touched.

**Evidence.** `curated_rev = curated_rev + 1` appears in Promote AND Repromote, so it counts promotions, not revisions. /entry labels it "Curated head" directly above a rail labelling the same head "rev N"; kb_get returns it beside curated_head.rev_no with no hint they differ. Note store.ReviewData.CuratedRev is a genuinely different quantity sharing the name (promote.go:440 reads rev_no).

**Deferred because.** No reason recorded for the remaining half.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 77. Browse offers three filters no code path can populate

`internal/web/app.go:700-701 (Stalenesses aging/refuted, Lifecycles deprecated)`

**Outstanding.** Delete the unproducible options; the CHECK constraints and the archived guards can stay.

**Evidence.** Every writer produces only fresh/stale (internal/store/write.go:304, v1tools.go:96 set 'stale'; promote/revert set 'fresh') and draft/active. Three filters return empty forever and a curator cannot tell "nothing is aging" from "this filter does nothing". Supporting dead weight: four `lifecycle != 'archived'` guards, the field comments declaring the vocabulary as contract, and translations for all four dead labels in three locales.

**Deferred because.** No reason recorded.

**Still open.** A curator cannot tell 'nothing is aging' from 'this filter does nothing', and three translated labels in three locales back options that return empty forever.

*Recorded in: audits + briefs*

##### 78. An agent can flag an entry stale and no human surface can clear it

`internal/store/write.go:304 and internal/store/v1tools.go:96 (the only staleness writers, both to 'stale'); internal/store/promote.go:132 and :266 (the only paths back to fresh); migrations/0001_init.sql:206 (reserved 'reverified')`

**Outstanding.** A curate-scoped "still holds" action, or a sentence in the flag_stale section saying a flag is effectively irreversible.

**Evidence.** No route and no CLI sets staleness. The only path back to fresh is the side effect of a promotion or a revert, both of which move the curated head and bump curated_rev — and for a single-version entry there is nothing to revert to, so clearing a wrong flag requires authoring a new version. The reason for the flag is unreadable (curation_event.note is never selected), so the curator cannot evaluate the flag they are stuck with. Credit where due: MCP-TOOLS.md:298-306 says plainly that kb_reverify is not built.

**Deferred because.** No reason recorded.

**Still open.** Clearing a wrong flag requires authoring a new version, and the reason for the flag is unreadable — so the curator is stuck with a judgement they cannot evaluate.

*Recorded in: audits + briefs*

##### 79. The room mirror's staleness detector is written and never compared, and sessions are told to make a check that is impossible

`internal/comm/room_mirror.go:43 (writes roster_epoch + refreshed_at), :49-51 (MirrorEpoch, one test caller); internal/commserver/types.go:386-389; internal/web/rooms.go and cmd/ken/main.go (both rebuild paths log and continue)`

**Outstanding.** Report the mirror's generation (or both, marked on divergence) and log the rebuild failure loudly.

**Evidence.** MirrorEpoch's only caller is internal/comm/room_send_test.go:209; refreshed_at is never read. Both rebuild paths log and continue, so a failed rebuild leaves a removed member still receiving room mail and an added one still refused, with a log line as the only trace. comm_directory publishes ken.db's AUTHORITATIVE epoch beside mirror-derived membership, so the number can never flag the list beside it — and types.go:386-389 still tells sessions the epoch "appears on delivered messages too", which no message view carries.

**Deferred because.** No reason recorded. One forward note attached: BlockStationPair/Unblock bump the roster epoch and blocks are not mirrored, so wiring station_block would move the epoch for something the mirror cannot reflect.

**Still open.** A failed rebuild leaves a removed member receiving room mail with a log line as the only trace, and sessions are told to make a comparison the surface makes impossible.

*Recorded in: audits + briefs*

##### 80. message.audience_epoch is NOT NULL, written by none of the three insert paths, and read nowhere — while three prose sites describe it as live

`internal/comm/migrations/0009_delivery.sql:97; internal/comm/message.go:261 and :486, internal/comm/room_send.go:218 (the three inserts, none of which name it); internal/commserver/types.go:386-388; internal/comm/migrations/0017_comm_rooms.sql:75-81`

**Outstanding.** Drop the column, or stamp it; either way fix the three sentences. Migration; ships alone.

**Evidence.** `grep -rn audience_epoch` finds exactly one hit repo-wide, the column definition. Every row is 0. The agent-facing sentence is the propagating one: 0017's comment says it is "carried on every delivered message so a receiver can tell that the set it was addressed WITH has changed". The epoch already reaches comm.db — mirror_state.roster_epoch is one SELECT away inside room_send's own transaction.

**Deferred because.** Rule 4: dropping or stamping the column is a migration and ships alone.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 81. Cumulative ack's settled-count has no test for the case stations make ordinary

`internal/comm/message.go:835-843`

**Outstanding.** A test-coverage gap named in the code and left open. A mutation that swaps the correct expression for `len(ids)` survives the suite.

**Evidence.** message.go:838 — "The two differ only when a row selected as a candidate is settled by somebody else before this loop reaches it — another endpoint of the SAME station racing the same inbox, which is the one case stations make ordinary. NO TEST COVERS THAT: the select and the acks happen inside this function, so a single-threaded test cannot interleave anything between them, and the two expressions are indistinguishable from outside. Stated rather than papered over, because a mutation swapping them survives the suite and someone will eventually 'simplify' it."

**Deferred because.** Stated: the interleaving cannot be produced from a single-threaded test against this function's current shape.

**Still open.** Confirmed outstanding: a test-coverage gap named in the code and left open, with the reason it cannot be closed by an ordinary test (select and acks happen inside one function, so a single-threaded test cannot interleave). The comment predicts the exact regression — someone "simplifying" the sum to len(ids) — and notes the mutation survives the suite.

*Recorded in: code comments*

##### 82. An unrecognised CLI verb starts a second Ken server against the same database

`docs/OPERATION.md:597-606 (§3.1) and trap 3 at docs/OPERATION.md:1015; code at cmd/ken/main.go:45-77`

**Outstanding.** Dispatch has no default case. `ken lang backfill` or `ken snapshot` does not error — it falls through to "treat the arguments as serve flags", launches a second instance against the same SQLite file and binds a port. Documented, workaround given ("check the verb before you run it"), not fixed.

**Evidence.** docs/OPERATION.md:602 — "There is no error for an unknown verb." Confirmed at cmd/ken/main.go:77: the switch over args[0] covers eight verbs and falls out to `runServe(args) // no subcommand: treat args as serve flags`.

**Still open.** Confirmed in code exactly as documented. Documented with a workaround ("check the verb before you run it") rather than fixed — the dispatch switch still has no default.

*Recorded in: docs tree*

##### 83. `ken backup snapshot` without KEN_DB snapshots a brand-new empty database and reports it healthy

`docs/OPERATION.md:14-16 (stamp), §3.2, and trap 4 at docs/OPERATION.md:1017-1018`

**Outstanding.** A pre-upgrade backup taken from the wrong directory succeeds, verifies, and protects nothing. Documented with a workaround (always pass KEN_DB explicitly) rather than made to fail.

**Evidence.** docs/OPERATION.md:15-17 — "`ken backup snapshot` without `KEN_DB` does not fail — it snapshots a brand-new empty database and reports it healthy, so a pre-upgrade backup taken from the wrong directory succeeds and verifies while protecting nothing. Both are fixed below" — meaning the *document* was fixed. Found by running it, not reading it.

**Still open.** Confirmed in code. The behaviour was documented, not fixed — a pre-upgrade backup from the wrong directory still succeeds, verifies, and protects nothing. Distinct from item 23 despite sharing a section.

*Recorded in: docs tree*

##### 84. A settings field whose group is not on the console's hard-coded render list is invisible AND is processed as empty on every save

`docs/OPERATION.md:182-188 (§2.2.1) and trap 13 at docs/OPERATION.md:1039; code at internal/web/app.go:1124`

**Outstanding.** The render order is a hard-coded string slice while the save handler builds its form map from `settings.Fields`. Adding a field in a new group silently hides it and then blanks it on the next save. No test or startup check couples the two lists.

**Evidence.** docs/OPERATION.md:182-186 — "**A field whose group is not on that list renders nothing at all, silently** — and the consequence is worse than invisibility. The save handler builds its form map from `settings.Fields` rather than from what was rendered, so an unrendered field is processed on every save as though it had been submitted empty." Confirmed at internal/web/app.go:1124: `order := []string{"Rate limiting", "Login", "Session", "Network", "TLS", "Curation", "Inter-session comms", "Stations"}`.

**Still open.** Confirmed: the two lists are still uncoupled, with no test or startup check. The code's own comment admits it is "Silent, and easy to miss" — a latent data-loss trap for the next field added.

*Recorded in: docs tree*

##### 85. The settings console still renders a "restart to apply" branch that no field can reach

`docs/OPERATION.md:143-148 (§2.1); code at internal/web/templates/settings.html:38,52`

**Outstanding.** Dead code in a human-facing template. Every field is `Live: true` or `ReadOnly: true`, so the `{{if and (not .Live) (not .ReadOnly)}}` branches are unreachable — but their existence is what makes an operator believe a restart-level setting exists.

**Evidence.** docs/OPERATION.md:145-148 — "The console form still contains a \"restart to apply\" branch; it is unreachable dead code. If you are waiting for a restart to make a saved setting take effect, you are waiting for nothing." Confirmed: internal/web/templates/settings.html:38 and :52 both guard on `(not .Live) (not .ReadOnly)`.

**Still open.** Confirmed unreachable. Every field literal in the registry carries Live:true or ReadOnly:true, so both guarded branches are dead — and their existence is what makes an operator believe a restart-level setting exists.

*Recorded in: docs tree*

##### 86. entry.lifecycle values 'deprecated' and 'archived' are unreachable, yet the console offers 'deprecated' as a browse filter

`migrations/0001_init.sql:94-95; internal/web/app.go:701; internal/store/browse.go:15,59`

**Outstanding.** No code path can set lifecycle to anything but 'draft' (Save) or 'active' (Promote). There is no deprecate and no archive operation anywhere. Either build them or remove the values, the browse filter option and the i18n enum keys.

**Evidence.** The only INSERTs are `VALUES(...,'draft','fresh',...)` (internal/store/write.go:125) and import/seed; the only UPDATEs are `lifecycle = 'active'` (internal/store/promote.go:133,267). Grep for `lifecycle=` finds no other writer. Meanwhile internal/web/app.go:701 publishes `"Lifecycles": []string{"draft", "active", "deprecated"}` as a filter dropdown, and internal/store/browse.go:59 documents that archived rows are "always excluded" — excluding a state nothing can enter. The matching curation_event types 'deprecated' and 'archived' are likewise never emitted.

**Still open.** Confirmed and independently harmful from item 2: the console publishes a filter for a state no code path can produce, so a curator filters and gets nothing forever. Build deprecate/archive or remove the values, the dropdown and the i18n keys.

*Recorded in: migrations + schema*

##### 87. The curation_event reflog never records a supersession it performs, and 'withdrawn' is unreachable in both tables

`migrations/0001_init.sql:131 (entry_version.state CHECK), :196-206 (curation_event, "append-only reflog… the in-app changelog")`

**Outstanding.** Promotion sets `entry_version.state='superseded'` but emits no 'superseded' curation_event, so the reflog that replaces the git commit log is missing the transition. 'withdrawn' is never written to either table, though search's `history` scope filters on it.

**Evidence.** internal/store/promote.go:114 and :246 run `UPDATE entry_version SET state='superseded'…`; the adjacent `insertEvent` calls (promote.go:146, :281) write event_type 'promoted' only. All 8 `insertEvent` call sites emit only proposed|promoted|rejected|flagged_stale — 'superseded', 'withdrawn', 'deprecated', 'archived', 'reverified', 'refuted' are 6 of the 11 declared CHECK values with no writer. `scopeStatePredicate` (internal/store/search.go:191) returns `ev.state IN ('superseded','rejected','withdrawn')` for the history scope, so the search surface queries a state nothing produces.

**Still open.** Confirmed. The reflog is DESIGN D5's explicit replacement for a git commit log, and it is silently missing the supersession transition it causes. Independent of item 3 (that one is about entry.lifecycle; this is about entry_version.state and the event stream).

*Recorded in: migrations + schema*

##### 88. endpoint.host_hint is write-only — no peer and no console can ever see it, yet the tool text tells sessions to compare hints

`internal/comm/migrations/0001_init.sql:44-51 and :60; internal/commserver/commserver.go:194`

**Outstanding.** The column's stated purpose — deciding whether a same-host handoff is worth a round-trip — cannot be served, because a session has no way to observe another endpoint's hint. Either surface it (directory / channel listing) or drop it and the guidance that references it.

**Evidence.** Migration: "host_hint is an OPTIONAL, opaque, client-supplied string used only to decide whether attempting a same-host filesystem handoff is worth a round-trip." It is written at registration (internal/comm/endpoint.go:68) and loaded into the Go struct (:123,:404,:434) — and never compared, never returned by any MCP tool result, and absent from the web console entirely (grep for host_hint under internal/web/ and configs/ returns nothing). Meanwhile the frozen instruction block at internal/commserver/commserver.go:194 tells sessions "A matching host_hint only suggests trying this; the echoed nonce is the proof" — describing a match no surface makes observable.

**Still open.** Confirmed: the guidance frozen into the MCP instruction block references a comparison no surface makes possible. Either surface the hint or drop it and the sentence — a real add-or-delete with a user-visible symptom.

*Recorded in: migrations + schema*

### Deferred work — decided, scoped, not built

##### 89. `attachment` is channel-shaped and must become scope-shaped — a file still cannot be offered to a room

`internal/comm/file.go (the string "scope" occurs ZERO times); internal/comm/migrations/0002_files.sql:31 and 0010; docs/FINISHING.md Batch 3 `[ ]``

**Outstanding.** Rebuild attachment around scope: channel_id is NOT NULL and recipient_endpoint is NOT NULL holding ONE endpoint where a room needs a party set. Migration 0010 already added and backfilled attachment.scope_id and it has never been used.

**Evidence.** `grep -c scope internal/comm/file.go` → 0. channel_id is NOT NULL and recipient_endpoint is NOT NULL, holding ONE endpoint where a room needs a party set — the last recipient column in comm.db that stores a connection where a party belongs, after 0009 deliberately made the delivery equivalent nullable/SET NULL. "The seam was cut and never used."

**Deferred because.** Rule 4 ("a migration ships alone"). Vlad's 2026-08-17 ruling deliberately excluded it from the bundled schema-only release: "that one is a restructure carrying code changes in file.go, so bundling it would destroy the property that makes bundling safe — that every change in the release is provably inert. It ships on its own, and it is the one case where Rule 4 and the code it needs are in genuine tension."

**Still open.** Unticked at FINISHING.md:334, and the seam is still unused. This is the single largest carried restructure: a migration plus code, explicitly excluded from the bundled schema release because it is not inert, and named as Slice 7's H2 blocker.

*Recorded in: audits + briefs, FINISHING.md — 2 separate places*

##### 90. Store.MarkNoticesSeen has zero production callers and the migration that superseded it argues it is unusable

`docs/FINISHING.md:469 (found by the Batch 4 sweep, unticked); internal/comm/notice.go:247`

**Outstanding.** Delete it — no migration needed — but the two tests must be REWRITTEN through NoticesForPoll rather than having the call deleted.

**Evidence.** Verified today: the only references are the definition and two tests (internal/comm/audit_fixes_test.go:165, internal/comm/notice_test.go:128). It is the explicit "I have read my notices" call that migration 0011's own comment argues at length is unusable here, because MCP tool lists pin at conversation start; NoticesForPoll supersedes it by promoting the previous poll's mark automatically.

**Deferred because.** None stated beyond the test-rewrite cost; it was found by the sweep rather than listed in the batch.

**Still open.** Unticked at FINISHING.md:469, still exactly two test references. Small and unambiguous, and the item carries the one instruction that keeps the deletion from silently reducing coverage.

*Recorded in: FINISHING.md*

##### 91. The nightly backup does not cover comm.db at all — the delivery ledger is unbacked

`docs/FINISHING.md:483 (found by the Batch 4 sweep, unticked); cmd/ken/cli_backup.go:34; scripts/ken-snapshot.sh:28,55,72`

**Outstanding.** Extend the backup path to comm.db, or state that it is deliberately excluded.

**Evidence.** Verified: cli_backup.go opens `envOr("KEN_DB", "./data/ken.db")` and nothing else; ken-snapshot.sh exports KEN_DB only, writes `ken-<stamp>.db.gz` and prunes `ken-*.db*`. comm.db holds the delivery ledger.

**Deferred because.** Explicitly out of scope where it was found: "Not a duplicated generation and not in this batch's scope — but comm.db holds the delivery ledger, and finding it while proving that nothing enumerates columns is exactly the kind of thing that gets lost if it is not written down."

**Still open.** Unticked at FINISHING.md:483 and confirmed. An operational gap with a two-way disposition (extend the path, or state the exclusion), and the thing not covered is the delivery ledger.

*Recorded in: FINISHING.md*

##### 92. Attachment counters keep the channel JOIN and will become the room-blindness bug the moment file exchange learns about scopes

`internal/comm/admin.go:172-175`

**Outstanding.** A named latent defect with a stated trigger. The message counters were already fixed for this exact cause (an INNER JOIN on channel_id dropped every room and broadcast message and reported a busy deployment as idle); the attachment counters were deliberately left in the old shape.

**Evidence.** admin.go:172 — "ATTACHMENT COUNTERS KEEP THE CHANNEL JOIN, deliberately. A file offer still binds a channel rowid, so there are no room-scoped attachment rows to miss. They become the same bug the moment file exchange learns about scopes, and not before."

**Deferred because.** Stated: today a file offer always binds a channel rowid, so there is nothing to miss yet.

**Still open.** Confirmed outstanding: a named latent defect with a stated trigger and a precedent that already fired. The identical shape in the message counters dropped every room and broadcast message and reported a busy deployment as idle (fixed in a364be9); the attachment counters were deliberately left in the old shape because a file offer still binds a channel rowid. Correct today, wrong the day file exchange gains room scope.

*Recorded in: code comments*

##### 93. Settings validation messages are half-translated: field names localise, the reason text stays English

`internal/settings/settings.go:379-383`

**Outstanding.** Field names in validation errors resolve through the translation bundle so they match the form, but the reason clause does not. A non-English operator gets a localized field name attached to an English explanation.

**Evidence.** settings.go:379 — "KNOWN LIMIT, stated rather than implied: the REASON text is still English. It comes from Go errors returned by each field's Set, and translating those is a separate piece of work. So a Spanish operator gets Spanish field names inside an English sentence — which is strictly better than an English name they cannot find on the page, and worse than the finished thing."

**Deferred because.** Stated: it is a separate piece of work, and the partial state is strictly better than the state it replaced.

**Still open.** Confirmed outstanding and self-labelled: "KNOWN LIMIT, stated rather than implied", with the work named ("a separate piece of work") and not scheduled. The current state is honestly characterised in the comment as better than the previous bug and worse than finished.

*Recorded in: code comments, docs tree — 2 separate places*

##### 94. COMM's failure-visibility increment is specified in full and none of it is built

`docs/COMM.md:889-949 (§13)`

**Outstanding.** Three named deliverables: `ken_comm_tool_errors_total{tool,reason}` plus `ken_comm_endpoints_swept_total` / `ken_comm_messages_expired_total`; a rate-limited janitor log line when a sweep removes an endpoint or channel (currently silent); and a per-gauge decision on the racing-snapshot problem. The §5.5 gaps below were scoped into the same increment.

**Evidence.** No such counters exist. Every ken_comm_* series in the binary is a gauge added at cmd/ken/main.go:992-1015 (endpoints, channels_open, messages_unacked, deliveries_unacked, message_bytes, files, file_bytes, poll_waiters); internal/metrics/metrics.go:183-196 defines the only counters in the process and none is COMM-specific. The document's own acceptance test is unmet: "an operator watching only /metrics should be able to see that something is wrong."

**Deferred because.** Stated target: "a single, coherent unit; target: a later MINOR."

**Still open.** Confirmed: no COMM counter exists anywhere in the process. A fully specified increment with a written acceptance test, entirely unbuilt.

*Recorded in: docs tree*

##### 95. A channel's human name is fixed at pairing time and cannot be edited; channels paired before 1.2.2 are stuck at "(no label)" permanently

`docs/COMM.md:860-865 (§12)`

**Outstanding.** A console edit action that sets `channel.label` after creation. The document calls it "small and self-contained" and says the only open question is whether it ships alone or bundled with the endpoint-identity work.

**Evidence.** docs/COMM.md:860-864. Confirmed absent: internal/web/comm.go handles `label` only at internal/web/comm.go:190-195, where it is read from the pairing-code mint form and passed to `MintPairingCode`; there is no update path anywhere in internal/web/comm.go.

**Deferred because.** Stated: pending a decision on whether to bundle it with the endpoint-identity increment.

**Still open.** Confirmed absent. The only remaining question is scoping (alone vs bundled with the endpoint work), which is exactly the kind of decision this index is for.

*Recorded in: docs tree*

##### 96. kb_link / kb_related, kb_get history+version selection, and kb_reverify are all specified and unbuilt

`docs/MCP-TOOLS.md:206-208, 301-307, 327; docs/DESIGN.md:251 and 464`

**Outstanding.** Three separate gaps in the knowledge-base tool surface: (a) `kb_link(from, to_slug, type)` and `kb_related(slug)` — the explicit link-graph tools, deferred since the original roadmap; (b) `kb_get`'s `include_history` flag and `version` selector; (c) `kb_reverify` — asserting an entry still holds. Today staleness returns to `fresh` only as a side effect of a human promotion, and the `reverified` event type sits reserved in the schema.

**Evidence.** docs/MCP-TOOLS.md:206 — "**Planned (not yet implemented):** an `include_history` flag (git-log rev list) and a `version` selector." docs/MCP-TOOLS.md:301-306 — "`kb_reverify` — *reserved, NOT yet implemented* … **It is not built yet.**" docs/MCP-TOOLS.md:327 — "## Still roadmap — `kb_link(from, to_slug, type)` · `kb_related(slug)`." Confirmed: internal/mcpserver/server.go registers exactly nine tools (kb_search, kb_get, kb_save, kb_propose_enhancement, kb_flag_stale, kb_diff, kb_record_outcome, kb_recent_context, ken_version) — none of the three.

**Deferred because.** Stated for kb_reverify: it is a curate-scoped act and "would belong in the human web UI, never exposed to agent tokens." The link-graph tools carry no stated reason beyond roadmap ordering.

**Still open.** All three confirmed unbuilt against the registered tool set. kb_reverify carries the most weight: staleness can only return to fresh as a side effect of a human promotion, and the `reverified` event type sits reserved in the schema waiting for it.

*Recorded in: docs tree*

##### 97. Client-side sortable listing tables — specified in detail as a deferred UI increment, and absent

`docs/DESIGN.md:466-480`

**Outstanding.** A dependency-free, progressive `data-sortable` opt-in for every web-UI grid (proposals, Browse, tokens, COMM channels and endpoints). The design is written down — sort key + type annotations on headers, client-side sort of already-rendered rows, no persistence in v1 — and none of it exists. Multi-column sort, reorder/resize and persisted per-user layout are explicitly a larger follow-on.

**Evidence.** docs/DESIGN.md:466-480, and listed among "Still open / deferred" at docs/DESIGN.md:464. Confirmed absent: no `data-sortable` attribute or sort handler in internal/web/static/app.js or any template under internal/web/templates/.

**Deferred because.** Stated: it must stay inside decision D8 (one small same-origin app.js, delegated data-* handlers, strict self-only CSP, fully progressive), and it is "**Explicitly NOT** the house 'listing-tables' component (that assumes a JS framework and CSS toolkit Ken does not use)" — so it is a from-scratch design item rather than a drop-in.

**Still open.** Confirmed absent. The design is written to implementation detail (opt-in attribute, sort key + type annotations, progressive, no persistence in v1) and constrained by D8, and none of it exists — a fully specified increment sitting unbuilt.

*Recorded in: docs tree*

##### 98. The whole re-verification / aging mechanism is declared and unbuilt: verified_at, verify_ttl_days, staleness 'aging' and 'refuted', curation_event 'reverified' and 'refuted'

`migrations/0001_init.sql:96-97 (staleness CHECK), :158-159 (verified_at, verify_ttl_days), :204-206 (event_type CHECK); docs/DESIGN.md:90-93`

**Outstanding.** Nothing ages an entry. Decide whether time-based / TTL-based staleness is being built or dropped, then either wire it or remove the four declared objects and the DESIGN.md paragraph.

**Evidence.** `verified_at` has ZERO Go references. `verify_ttl_days` has two, both in one test poking it to prove the immutability trigger permits it (internal/store/via_comm_test.go:100,191). Every write to `entry.staleness` in production code is the literal `'stale'` (internal/store/write.go:304, internal/store/v1tools.go:96) or `'fresh'` (promote.go:132,266 and the INSERT default) — `'aging'` and `'refuted'` have no writer. `insertEvent` is called from exactly 8 sites and emits only 'proposed', 'promoted', 'rejected', 'flagged_stale'. DESIGN.md:90-93 states each version carries `verified_at`, `verify_ttl_days` and that "Time / explicit flag / recorded dependency-bump can age an entry to stale" — only the explicit flag exists.

**Deferred because.** None stated in the migration; the columns were declared in the 0001 baseline as part of the model and never revisited.

**Still open.** Confirmed: nothing ages an entry. Four declared schema objects with no writer, and DESIGN.md asserts the time/TTL path exists. Open decision — build or delete.

*Recorded in: migrations + schema*

##### 99. entry_link is a write-only table: wikilinks are stored and resolved, and nothing ever reads them

`migrations/0001_init.sql:218-242 (table, both indexes, entry_resolve_links trigger); docs/DESIGN.md:215`

**Outstanding.** `kb_save` accepts `links`, they are inserted, and the AFTER INSERT trigger back-resolves dangling `to_slug` → `to_entry_id`. No surface reads them: no related-entries panel, no backlink graph, no field on kb_get. Decide whether links get a consumer or the capture is dropped.

**Evidence.** The only two occurrences of `entry_link` in Go are both INSERTs (internal/store/write.go:151, internal/store/import.go:65). There is no SELECT against the table anywhere, including tests. `idx_link_to_id ON entry_link(to_entry_id)` (0001:234) therefore has no query that can use it — only `idx_link_to_slug` is exercised, by the trigger. The model type `Links` appears only on the mcpserver INPUT side (internal/mcpserver/server.go:263,544); nothing returns them. DESIGN.md:215 lists entry_link as the thing that "resolves [[wikilinks]]" without saying nothing consumes the resolution.

**Still open.** Confirmed write-only. A capture path plus a resolution trigger plus two indexes, feeding no surface — a clear add-or-delete decision, and one of the few items where the cost is paid on the KB's write path.

*Recorded in: migrations + schema*

##### 100. `ken lang backfill` — the command 0009 deliberately weakened the immutability trigger for was never built

`migrations/0009_content_lang.sql:6-9; docs/OPERATION.md:608`

**Outstanding.** `content_lang` is excluded from the frozen set of `entry_version_immutable` specifically so an offline backfill could re-derive it. That backfill does not exist, so a provenance-adjacent column on an otherwise-immutable table is editable for a capability nobody built.

**Evidence.** Migration comment: "It is detection METADATA, deliberately NOT listed in the entry_version_immutable trigger's frozen set, so a future offline `ken lang backfill` can (re)derive it without fighting immutability." `cmd/ken/main.go:49-73` dispatches token|user|backup|import|embed|station|serve|version|help — there is no `lang` verb. docs/OPERATION.md:608 uses `ken lang backfill` as its worked EXAMPLE of a non-command that silently starts a second server instead of erroring. Compare 0019, which re-froze `via_comm_kind` precisely because "a provenance field an author can edit after the fact records nothing."

**Deferred because.** Stated: to let a future offline re-derivation run without fighting immutability.

**Still open.** Confirmed. A provenance-adjacent column on an otherwise-immutable table is deliberately left editable for a capability that does not exist, and 0019 later re-froze a sibling column for exactly the opposite reason. That inconsistency is a decision to re-take.

*Recorded in: migrations + schema*

### Open questions that need a human decision

##### 101. station_task_defer is date-shaped while the staleness it must handle is state-shaped

`docs/FINISHING.md:242 (Batch 2, unticked); internal/stationserver/types.go:214`

**Outstanding.** Decide whether the verb the task list needs is a RECHECK rather than a postponement. Nothing has changed in code: taskDeferIn is still {task_id, until, reason}.

**Evidence.** Prod's observation, recorded as the explanation for why neither station has ever called defer: it takes remind_after, "modelling 'not yet' as a TIME, while almost all real staleness is 'the condition may already be satisfied and nobody rechecked'."

**Deferred because.** Phrased as a question to consider, not a decision taken — no owner, no batch.

**Still open.** Unticked at FINISHING.md:242, code unchanged. A genuine open verb-design decision (recheck vs postponement) with a stated cause for why the existing verb is unused.

*Recorded in: FINISHING.md*

##### 102. EXERCISED, NEVER USED: the whole vault/locker plus station_directory saw five calls in 61 seconds and nothing since

`docs/FINISHING.md:248-253 (Batch 2 verdict, no checkbox and no action attached)`

**Outstanding.** No item was ever created for this. It is a fifth verdict class prod invented mid-audit, applied to a whole surface, with no decision recorded about what to do with a capability that survived contact with a curious operator and still found no use.

**Evidence.** "Count 1, in a burst, at the moment someone was systematically trying the surface — then never again. That is the whole locker plus station_directory (all five calls inside 61 seconds on 2026-08-11). A bare count says 'used'; usage in the course of work is zero. It changes the CONFIDENCE of a verdict, not just its label." Directly relevant to TARGET-ARCHITECTURE.md's premise that Vlad has stopped trying to use Station.

**Deferred because.** None — it was recorded as a methodological note rather than turned into an item.

**Still open.** A verdict applied to an entire surface with no checkbox and no item ever created — the only finding in the file with a conclusion and no disposition. It bears directly on the rebuild-or-extend question, because it is measured evidence about whether a shipped surface is wanted, not whether it works.

*Recorded in: FINISHING.md*

##### 103. Four station open questions carried unresolved, including "how does a session say it is finishing?"

`docs/STATIONS.md:968-981 (§13)`

**Outstanding.** (a) whether claim-once needs an optional reader hint on send when a long-running session starves a fresh one; (b) whether a station may exist with no key at all, addressable but staffed by nobody; (c) a session that knows it is ending has no way to say "handoff written, I am done" beyond writing the page — archive is a human act; (d) multi-human invitations.

**Evidence.** docs/STATIONS.md:974-981. (b) carries its own disposition — "Coherent, possibly useful as a shared inbox, deferred until wanted" (docs/STATIONS.md:977). (c) is stated flatly with no proposal attached.

**Deferred because.** Only (b) and (d) carry reasons; (a) and (c) are recorded without one.

**Still open.** Confirmed, all four still open. Two carry dispositions (reader hint = additive fix; no-key station = deferred until wanted); the finishing question is stated flatly with no proposal, which makes it the one with real design weight.

*Recorded in: docs tree*

### Declined, with the reason — so a later session does not silently re-open it

##### 104. DECLINED: message.response_mode is left in the schema as an unbuilt seam

`docs/FINISHING.md:488 (found by the Batch 4 sweep, `[-]`)`

**Outstanding.** Nothing — but the column stays, unwritten and unread, and a future reader can mistake it for a capability. Recorded so the question is not reopened by grep.

**Evidence.** "not a duplicate. Its string occurs exactly ONCE in the repo, in its own column definition: an unbuilt seam, not a superseded generation. Leave it."

**Deferred because.** It failed the batch's own test — Batch 4 is "delete one of two generations", and there is only one generation here.

**Still open.** A decline recorded specifically so a future grep does not reopen it, and it names a schema seam a reader can mistake for a capability. Verified: the string occurs exactly once outside the checklist.

*Recorded in: FINISHING.md*

##### 105. DECLINED: closing stale tasks on age — detection is the ask, abandonment is the human's

`docs/FINISHING.md:599 (Deliberately not doing, `[-]`)`

**Outstanding.** Nothing to build. It constrains the still-open aging item (FINISHING.md:294): age must surface, never act.

**Evidence.** "detection is the ask. What a human owes is theirs to abandon, not a session's."

**Deferred because.** A policy boundary, not a cost: a session must not retire a human's obligation.

**Still open.** A decline with its reason, and it is load-bearing rather than inert: it is the constraint on the still-open aging item, fixing its shape to surface-never-act. Cheap to carry, and a rebuild that dropped it would repeat a decided question.

*Recorded in: FINISHING.md*

##### 106. dm rooms DECLINED — the container cannot distinguish itself from the one that feeds broadcast

`docs/DECISIONS-BATCH5.md §Decision 2, option P4; docs/FINISHING.md Batch 6 `[-]``

**Outstanding.** Nothing to build. The decision is recorded with a revisit condition: reconsider only if room_member_mirror gains a `kind` column for some other reason.

**Evidence.** `room_member_mirror` is (room_id, party_key) with no kind column and ReplaceRoomMirror takes a bare map[string][]string, so nothing between ken.db and BroadcastAudience carries the kind — an agent-initiable dm room would silently enlarge both parties' broadcast audience. The CHECK at migrations/0017_comm_rooms.sql:36 still admits 'dm'; internal/store/rooms.go says plainly nothing produces it (settled that way in 3.8.0).

**Deferred because.** Declined for the failure MODE, not the feature: the fix costs two migrations, a kind-aware console and a filter every future audience query must remember — "and it fails OPEN: a missed filter widens broadcast invisibly, discoverable only by someone counting an audience."

**Still open.** A deliberately declined decision with its reason and an explicit revisit condition — exactly the class the index is for. Without it, the obvious answer gets re-proposed by the next reader, because the schema still invites it.

*Recorded in: audits + briefs*

##### 107. A notice cannot outlive its subject: notices vanish with the 7-day metadata purge

`internal/comm/notice.go:43-54`

**Outstanding.** A sender who has not polled within MetadataTTLSeconds (7 days by default) never learns what became of their message. Accepted, not fixed.

**Evidence.** notice.go:43 — "BOUNDED ALSO BY METADATA RETENTION, which is the one thing a derived notice gives up against a written one and is stated here because it is invisible from the call site… The metadata purge removes a settled message MetadataTTLSeconds after it settled (7 days by default), and the notice goes with it. A written notice was an independent row and could outlive its subject; this cannot."

**Deferred because.** Stated: "the alternative is what shipped: an independent row is exactly what made a failure signal into a second failure-prone delivery, with its own expiry, its own backpressure and its own ack — and it took the sweep down twice."

**Still open.** Confirmed outstanding as an accepted limitation, with the reason recorded and load-bearing: the alternative (an independent notice row) is what shipped before, and it turned a failure signal into a second failure-prone delivery with its own expiry, backpressure and ack — and took the sweep down twice. Keep as a decision the rebuild analysis should re-derive rather than re-discover, not as a bug to fix.

*Recorded in: code comments*

##### 108. A notice lost in transit is lost, and the fix was declined because running sessions could not use it

`internal/comm/notice.go:202-215 (statement at :207)`

**Outstanding.** Notices are cleared by the caller's next poll regardless of whether the result reached it. Closing the gap needs receipt confirmation, which needs a new tool or a new parameter.

**Evidence.** notice.go:207 — "WHAT IT DOES NOT BUY, and this is the accepted trade: a result lost in transit is a notice lost. The next poll promotes it regardless, because the server cannot tell a delivered result from a discarded one. Closing that would need the caller to confirm receipt — and the only mechanisms for that are a new tool or a new parameter, neither of which a session running today can use: tool lists and descriptions pin at conversation start."

**Deferred because.** Explicit: "A design that clears only on confirmation would repeat every notice forever for exactly the sessions least able to fix it."

**Still open.** Confirmed outstanding as a declined design with an explicit reason — and the reason is item 11, which is why both belong in the index: receipt confirmation needs a new tool or a new parameter, and neither reaches a conversation already open. The trade is stated as chosen: a rare loss versus a permanent repeat for every frozen session.

*Recorded in: code comments*

##### 109. message.response_mode: a CHECK-constrained column whose string occurs exactly once in the repo — its own definition

`internal/comm/migrations/0009_delivery.sql:85-86; docs/FINISHING.md Batch 4 sweep`

**Outstanding.** Explicitly left in place. 'any' vs 'all' response semantics for room messages were never built, and rooms now exist, so the comment's premise ("Inert until rooms") has expired without the column gaining a reader.

**Evidence.** 0009:85-86: `response_mode TEXT NOT NULL DEFAULT 'any' CHECK (response_mode IN ('any','all'))` with "Inert until rooms: with one recipient, 'any' and 'all' are the same question." Grep for `response_mode` / `responseMode` / `ResponseMode` over the tree: ZERO hits outside the migration. FINISHING.md records the ruling: "[-] `message.response_mode` — not a duplicate. Its string occurs exactly ONCE in the repo, in its own column definition: an unbuilt seam, not a superseded generation. Leave it."

**Deferred because.** Stated: it was classified as an unbuilt seam rather than a duplicated generation, so it fell outside Batch 4's "delete one of the two" scope.

**Still open.** Confirmed, and the reason for leaving it has expired: the comment's premise was "Inert until rooms", rooms shipped, and the column still has no reader. That converts a parked seam into an open question — build 'any' vs 'all' for room messages, or drop it.

*Recorded in: migrations + schema*

##### 110. Migration 0009's "NOT INCLUDED" list: retain_body_until and party_load were declined, each with a reason worth keeping

`internal/comm/migrations/0009_delivery.sql:54-61`

**Outstanding.** Nothing to do unless the reasons stop holding. Recorded because both are named in the source addressing plan and a future reader of that plan will look for them in the schema and not find them.

**Evidence.** 0009:54-61 verbatim: "NOT INCLUDED, deliberately, though the source plan lists them: retain_body_until — 1.6.0 made body retention a live setting evaluated at sweep time. Materialising a per-message deadline at send would freeze the value an operator is most likely to change DURING an incident, which is the one case live settings exist for. party_load — slice 3 of the plan; a budget table nothing maintains is worse than none, because the next reader trusts it." The third entry, notice_watermark, was subsequently built in 0011.

**Deferred because.** Both reasons stated in full: a materialised deadline would freeze a setting operators change during incidents; an unmaintained budget table is worse than no table because the next reader trusts it.

**Still open.** Keep — this is squarely the "deliberately declined decision, with the reason" category. Both are named in the source addressing plan and absent from the schema, so a reader working that plan will look for them and find nothing; the stated reasons are the only record of why. The third listed item (notice_watermark) was subsequently built in 0011, which proves the list is a live decision record rather than dead prose.

*Recorded in: migrations + schema*

### Known costs accepted on purpose — the price of a decision already taken

##### 111. The idle-sweep fix is a VERIFICATION GAP carried on purpose — production could not exercise it and the next natural window is ~2026-11-01

`docs/FINISHING.md:63-72 (Where we are today, narrative — no checkbox)`

**Outstanding.** Production confirmation that an endpoint seating a channel survives collection. Today the only evidence is a mutation-verified unit test.

**Evidence.** "comm_endpoint_idle_sec is overridden there to 7776000 (90 days) and the most idle endpoint is 14 days old, so nothing was ever eligible and the sweep had no opportunity to exhibit the bug in either direction. Twelve channels before and after is the same green a broken build would produce." Recorded because ken-prod-ops "refused to record it as" a pass. Context from TARGET-ARCHITECTURE.md §7: this bug "had already destroyed a live channel twice on a machine nobody was watching, each repair costing a human-minted pairing code."

**Deferred because.** A deliberate declined offer: "prod offered a deliberate window sooner and I did not take it, because the local test already pins the property and a staged window would mostly re-test the harness."

**Still open.** Narrative with no checkbox, and deliberately recorded as a gap rather than a pass. Something is still owed (production confirmation) and there is a dated window for it. The bug it covers is the most expensive one in this repo's history, which is why the distinction between 'green' and 'exercised' was worth writing down.

*Recorded in: FINISHING.md*

##### 112. Two applied migrations keep prose the project has since disowned, and will not be corrected — by policy

`docs/FINISHING.md:261-263 and 347-350; migrations/0012_stations.sql:53; migrations/0017_comm_rooms.sql:34-36`

**Outstanding.** Nothing planned. The false text stays in the migration files permanently; the correction lives only in the Go comments beside them.

**Evidence.** Verified today: 0012_stations.sql:53 still reads `-- retired = stop binding NEW endpoints, leave live ones alone (the "I moved machines" …)` while internal/store/stations.go:328 now says "Neither is the graceful 'I moved machines' path this comment used to promise." 0017_comm_rooms.sql:34-36 still describes dm rooms in the present tense ("'dm' rooms are created implicitly for a pair") though nothing can create one. The retire item ticks "seven of ten" sites and names 0012 as one of the three left.

**Deferred because.** Stated identically in both places, and verified rather than assumed: "SQLite stores a table's CREATE statement verbatim, comments included — so editing an applied migration's prose makes .schema differ between a fresh install and an existing deployment while changing nothing about either, and prod runs a schema band over exactly that. Correcting the Go model reaches every reader at no drift."

**Still open.** A known cost carried deliberately, with a technical reason (SQLite stores CREATE text verbatim; editing applied migration prose drifts .schema between fresh and existing installs, which production runs a band over). The false text is permanent and the correction lives only beside it in Go — a future reader of the migration alone gets the wrong answer. That is precisely the class this project says propagates.

*Recorded in: FINISHING.md*

##### 113. Migration 0018 differentiated the hearsay marker on entry_version only; the five station tables still carry the undifferentiated flag

`migrations/0018_provenance_kind.sql:37-38 and 0019_freeze_via_comm_kind.sql; migrations/0012_stations.sql:133, :148, :173, :221 (hearsay_at_write)`

**Outstanding.** Decide whether the station tables should gain via_comm_kind, or record that the coarse flag is deliberate there.

**Evidence.** `grep -rn via_comm_kind` finds it on entry_version only (plus its freeze trigger and one read in promote.go). The station notebook revisions, tasks, link requests and promotions all still store a bare hearsay_at_write integer, so "directed" and "broadcast" traffic are indistinguishable on everything a station writes.

**Deferred because.** Recorded explicitly as "one open question recorded here rather than opened" in the audit's checked-and-clean section — so it is carried on purpose, but by no batch.

**Still open.** An open question recorded rather than opened: on everything a station writes, directed and broadcast traffic remain indistinguishable. Either the tables gain the kind or the coarse flag is recorded as deliberate.

*Recorded in: audits + briefs*

##### 114. The message-counter space attribution rests on one unguarded assumption, flagged for recheck if endpoints ever move between spaces

`internal/comm/admin.go:150-155`

**Outstanding.** No test or constraint enforces it. The comment asks a future change to come back here; nothing will make it.

**Evidence.** admin.go:150 — "THE ONE ASSUMPTION, stated so it can be rechecked: nothing moves an endpoint between spaces. Verified — every `UPDATE endpoint SET` in this package touches only last_seen_at, station binding, or revocation. If a space-move is ever added, a message's attributed space would follow its sender, where `channel.space_id` was fixed at creation; that is the moment to revisit this."

**Deferred because.** Conditional on multi-space work that has not happened (see the deferred-machinery item).

**Still open.** Keep, though it is the weakest item on its own — it is a conditional, trigger-fired trap rather than present work. It earns its place because its trigger is item 22: if the multi-space machinery is ever activated (or a space-move is added), every message's attributed space silently follows its sender while `channel.space_id` was fixed at creation. Nothing — no test, no constraint — will route a future author back to this comment.

*Recorded in: code comments*

##### 115. The BodyBytes gauge is correct only because retention blanks bodies to NULL rather than to an empty string, and nothing enforces that

`internal/comm/admin.go:166-171 (depends on a choice made in internal/comm/message.go)`

**Outstanding.** A cross-file invariant with no guard. If retention ever blanks to `''`, the gauge silently reports several times the truth.

**Evidence.** admin.go:166 — "THIS IS ONLY CORRECT BECAUSE BLANKING WRITES NULL, NOT ''. The predicate below excludes blanked rows by `body IS NOT NULL`, and those rows keep their body_bytes forever — on production that is 1.27 MB of accounting for text that no longer exists. If retention ever blanks to an empty string instead, this line silently reports several times the truth. The dependency is on a choice made in message.go, so it is written down here."

**Deferred because.** Written down rather than enforced — the comment is explicit that documenting it was the chosen mitigation.

**Still open.** Confirmed outstanding: a cross-file invariant (admin.go's predicate depends on message.go's blanking choice) with no guard on either side. The failure mode is silent over-reporting by several multiples, and the same metric already shipped one silent wrong answer for a different reason (LENGTH() counting characters, 0.55% low on production 3.5.1).

*Recorded in: code comments*

##### 116. COMM requests still feed the shared per-IP strike counter, so a poll loop can auto-block a machine's knowledge-base access

`docs/COMM.md:568-570 (§5, rule 5)`

**Outstanding.** Exempt COMM from the shared per-IP strike/auto-block accounting, and advertise a server-side minimum poll interval in poll results. The second half is partly addressed — `wait_seconds_granted` / `wait_clamped_from` now report the wait actually granted — but there is still no advertised minimum interval.

**Evidence.** docs/COMM.md:568 — "**Not yet done, and honestly a gap:** COMM requests still feed the shared per-IP strike counter, so a pathological poll loop can still trip the machine-wide auto-block; and poll results do not carry a server-advertised minimum interval. Both are on the list for the next COMM increment." Confirmed in code: cmd/ken/main.go:565 is `handler := rlGuard.Wrap(mux)` — the guard wraps the whole mux, so /comm/mcp strikes the same IP key as /mcp. internal/commserver/types.go:245-253 shows what did ship.

**Deferred because.** Stated: scoped into the §13 increment because "COMM should fail *safely and visibly* under abuse" is the same theme as the telemetry work.

**Still open.** Both halves confirmed. The guard wraps the whole mux so /comm/mcp strikes the same IP key as /mcp, and no advertised minimum poll interval exists. The item correctly notes the partial mitigation that did ship.

*Recorded in: docs tree*

##### 117. Multi-instance COMM silently degrades to poll-interval latency, and the proxy/timeout guidance the document owes itself is unwritten

`docs/COMM.md:847-855 (§12)`

**Outstanding.** Two things: COMM assumes a single instance (long-poll wakeups are in-process and do not cross a load balancer — correctness survives, latency degrades silently), with a database-poll tick named as the escape hatch if that changes; and the reverse-proxy read-timeout guidance the document says "belongs in this document next to the existing no-write-timeout rationale" has not been written.

**Evidence.** docs/COMM.md:847-855. The escape hatch is named but not built; the proxy guidance is named as owed and is absent from §5 rule 7 (docs/COMM.md:573-577), where the no-write-timeout rationale actually sits.

**Deferred because.** Stated: "COMM assumes a single instance; if that ever changes, the escape hatch is a short database-poll tick rather than a redesign."

**Still open.** Both halves confirmed. The multi-instance limitation is a carried constraint with a named escape hatch; the proxy guidance is a debt the document explicitly books against itself and has not paid.

*Recorded in: docs tree*

##### 118. Station space is recorded but not enforced — the station principal hardcodes space 1

`docs/STATIONS.md:250-252 (S5)`

**Outstanding.** The voucher redemption predicate is `(endpoint, actor)`; the space column is written but cannot discriminate. Tightening to `(endpoint, actor, space)` is the stated next step and needs no further migration.

**Evidence.** docs/STATIONS.md:250-252 — "**Space is recorded but not enforced**, because it cannot discriminate yet: the station principal hardcodes space 1. Writing it now lets the check tighten to `(endpoint, actor, space)` with no further migration."

**Deferred because.** Stated: one human, one space — the column is a cheap seam laid now so the tightening is additive later.

**Still open.** Confirmed in code at two sites. A deliberate forward-compatibility choice with the tightening step already named — a decision to record, not debt.

*Recorded in: docs tree*

##### 119. The station vault stores every credential in plaintext, and every snapshot carries them

`docs/STATIONS.md:398-441 (S13); docs/BACKUP.md:15-20 and 264-281`

**Outstanding.** Nothing to build — this is a ruling being carried. But it means a snapshot is "equivalent to a full dump of the instance **plus its credentials**", and the only stated remedy is not to use the vault.

**Evidence.** docs/BACKUP.md:15-17 — "**Every credential a session PUTS IN THE VAULT is stored as plaintext and is replayable by anyone holding the file.**" docs/STATIONS.md:426-431 gives the reasoning: encrypting needs a key, the key would live in the same ken.db, "lock and key would then travel together in every backup."

**Deferred because.** Stated, and it followed an explicit instruction from Vlad after age-encrypted snapshots cost real production pain: security is not a functional concern of Ken, and "a non-encrypted database up to the backup point" is preferred to a key-management problem that buys nothing.

**Still open.** Confirmed. Nothing to build — this is a ruling carried on purpose with its reasoning written down, which is exactly what a rebuild-vs-modify analysis must not rediscover. The blast radius (snapshot = dump + credentials) is the reason it belongs in the index.

*Recorded in: docs tree*

##### 120. The shipped systemd units carry no OnFailure=, so a nightly snapshot that kept nothing pages nobody

`docs/MONITORING.md:140-166 ("The backup blind spot")`

**Outstanding.** Ken ships no failure notification for the backup timer. The document offers the operator two workarounds (a drop-in OnFailure=, or a freshness check from their own monitoring) and recommends the second.

**Evidence.** docs/MONITORING.md:144-145 — "The run exits non-zero and systemd marks `ken-snapshot.service` failed, but nothing pages you: the shipped units carry no `OnFailure=`." Confirmed: `grep -rn OnFailure deploy/ scripts/` returns nothing across ken.service, ken-snapshot.service and ken-snapshot.timer.

**Deferred because.** Implied rather than stated — the alert target is operator-specific, and COMM.md §13 takes the same stance for Prometheus ("the Grafana/Prometheus bundle stays structurally neutral… alert thresholds remain the operator's to set").

**Still open.** Confirmed absent from every shipped unit. Documented with two operator workarounds and a recommendation, i.e. a known gap deliberately pushed onto the operator.

*Recorded in: docs tree*

##### 121. Retained message bodies are unreadable — nothing in Ken returns a settled body

`docs/OPERATION.md:493-499 (§2.4(d)) and trap 8 at docs/OPERATION.md:1027`

**Outstanding.** `comm_body_retention_sec` keeps text for a day by default that no MCP tool and no console page will return. "Retained-but-unreadable is the default state." A session that needs to re-read something must copy it out at the time.

**Evidence.** docs/OPERATION.md:493-496 — "At v3.6.0 no MCP tool and no console page returns a settled message body. `comm_poll` returns unacked mail; once settled, the text is reachable only by reading the database directly."

**Still open.** Confirmed: no tool on the COMM surface reads a settled body, and no console page does either. A setting that retains data for a day that the product cannot show back is a real product gap, not just a doc note.

*Recorded in: docs tree*

##### 122. Notebook revision history prunes silently — oldest-first, no log line, nothing in the write result

`docs/OPERATION.md:530-539 (§2.4(h)) and trap 10 at docs/OPERATION.md:1032`

**Outstanding.** Exceeding `station_note_revision_kib` deletes the oldest revisions with no signal at write time. Partly mitigated — `station_note_list` now reports `revisions_lost` — but the deletion itself is still silent, and a page can lose its history entirely (§6.3 notes such a page has no rows in `station_note_revision` at all, "and the worst case is the one it cannot see").

**Evidence.** docs/OPERATION.md:533 — "Exceeding it deletes the **oldest** revisions, oldest-first, with no log line and nothing in the write result." Observed on a live deployment: "one station's page had **already lost its first seventeen revisions**, including the original context" (docs/OPERATION.md:541-542).

**Still open.** Confirmed still silent at write time. The partial mitigation the item names is real (station_note_list reports revisions_lost) and correctly does not close the item: the deletion itself signals nothing, and it has already destroyed data on a live deployment.

*Recorded in: docs tree*

##### 123. Store.Migrate() emits nothing — there is no migration log on either database

`docs/OPERATION.md:825-833 (§5.1)`

**Outstanding.** No line per migration, no summary, no count. The upgrade procedure had to be rewritten around a `schema_migration` query because there is nothing in the journal to read. Compounded by the asymmetry immediately below it: a ken.db migration failure is fatal, a comm.db failure is not — it logs `COMM: DEGRADED` and serves on, so a moved version and a green /healthz prove nothing about comm.db.

**Evidence.** docs/OPERATION.md:825-827 — "**There is no migration log to check.** `Store.Migrate()` applies each pending `NNNN_*.sql` and emits nothing — no line per migration, no summary, no count. Earlier text said to 'check the log for a migration you were not expecting'; on a clean run there is nothing there to see."

**Still open.** Confirmed: no logging call in the migration path. Compounded by the ken.db-fatal / comm.db-degraded asymmetry, which makes a moved version plus a green /healthz prove nothing about comm.db.

*Recorded in: docs tree*

##### 124. A task's blocked_on is set once at creation and nothing ever revisits it

`docs/UPGRADING.md:414-416`

**Outstanding.** A satisfied condition looks identical to a waiting one, so a station keeps telling its human they owe something that is already done. 3.11.0 added `blocked_on_human_and_stale` to flag the likely-stale ones; the underlying field is still never re-derived.

**Evidence.** docs/UPGRADING.md:414-416 — "`blocked_on_human_and_stale` names the ones most likely to be **already done**: `blocked_on` is set once at creation and nothing ever revisits it, so a satisfied condition looks identical to a waiting one." The scale is recorded in the same section: "roughly 45 tasks blocked on one human, the large majority never surfaced to him once."

**Still open.** Confirmed: 3.11.0 added a heuristic flag, not a re-derivation. The underlying field is still write-once, so a satisfied condition is still indistinguishable from a waiting one — and the recorded scale (≈45 tasks on one human) shows the cost is not theoretical.

*Recorded in: docs tree*

##### 125. Release trust root is "whoever can publish a Ken release"; GPG signing is named as the stronger guarantee and not done

`docs/REMOTE-UPGRADE.md:76-81`

**Outstanding.** The remote-upgrade wrapper checksum-verifies the installer against SHA256SUMS from the same GitHub release, which guards a corrupted or MITM'd download and nothing else. The document names the remedy — GPG-sign releases and have the wrapper verify the signature — and does not take it.

**Evidence.** docs/REMOTE-UPGRADE.md:78-81 — "The checksum guards against a corrupted or MITM'd download, **not** a malicious release (which you control anyway). For a stronger guarantee, GPG-sign releases and have the wrapper verify the signature."

**Deferred because.** Stated: the malicious-release case is one the operator controls anyway, on a single-owner deployment.

**Still open.** Confirmed: the document names the remedy and does not take it, with the reason stated (a malicious release is one you control anyway). A limitation carried knowingly — worth an index line so the next person does not rediscover that the checksum is not a signature.

*Recorded in: docs tree*

##### 126. Two indexes on entry_version exist for filters no query performs: idx_ev_content_lang and idx_ev_via_comm

`migrations/0009_content_lang.sql:21; migrations/0010_comm_provenance.sql:59-61`

**Outstanding.** Both indexes cost a write on the knowledge base's hottest insert path and serve no read. Either add the filtering query they were built for or drop them.

**Evidence.** Grep for any `WHERE` clause mentioning `content_lang` or `via_comm` across the tree returns nothing. Both columns are only ever SELECTed by id or as part of a review-queue projection: internal/store/promote.go:97,227 (`SELECT … content_lang FROM entry_version WHERE id=?`), promote.go:327-328 and :424 (select-list only), internal/store/search.go:75. 0010's comment claims "The review queue's only question is 'which pending proposals carry the mark', so a partial index over the marked rows is enough and stays tiny" — the review queue joins the latest version and reads the column, it never filters on it.

**Still open.** Confirmed: both columns are only ever read by id or as select-list projections, never filtered. Two write-path costs on the KB's hottest insert for zero reads — small, but a concrete add-or-drop.

*Recorded in: migrations + schema*

### Text that describes a control Ken does not have

##### 127. ~~The file's own status lines are stale: 'Released: 3.9.0' at HEAD v3.11.0, and Batch 1 still marked in-progress after both its items closed~~ — **FIXED 2026-08-19/20**

`docs/FINISHING.md:41, :79-81, :106, :149`

**Outstanding.** Restate where the project actually is. This is the one file whose stated purpose is that "a human should be able to open this file and know exactly where we are", so its own drift is load-bearing.

**Evidence.** Verified: `git describe` = v3.11.0-1-ga0ed9ec, and 3.10.0 and 3.11.0 are both tagged and released, while line 41 still opens "Released: 3.9.0 (2026-08-17)". Batch 1's header (line 106) still reads `[~]` "two items still open" although both of those items are ticked 3.10.0 immediately below it. Line 149 still says 3.8.0's "verification outstanding" while line 58 records it verified 2026-08-17T16:03Z. Line 79-81 concedes the pattern in place: "this line is prose that drifted from it within an hour of being written."

**Deferred because.** Rule 2 (update in the same commit as the work) is the intended mechanism and it has no enforcement — see the Rule 2 item.

**Still open.** Concrete, currently-wrong statements in the one document whose stated purpose is that a human can open it and know exactly where things stand. Distinct from the enforcement item: that one is a mechanism to build, this is a correction to apply, and the corrections are what a cold reader of the index would otherwise inherit as fact.

*Recorded in: FINISHING.md*

##### 128. comm_unbind promises "mail addressed to you stays yours", and the doc it rests on has been false since migration 0009

`internal/commserver/commserver.go:290-292 (tool description) and :306-308 (success note); internal/comm/endpoint.go:262-263 ("Messages are addressed to an ENDPOINT rowid; the station merely widens which endpoint may read them")`

**Outstanding.** Correct the two agent-facing strings and the doc comment, or change what unbind does with mail received while bound.

**Evidence.** Since 0009 mail is filed under the party key recorded at write time, so everything received while bound is filed `s:<station>`; unbinding narrows the poll party to `e:<rowid>` and that mail stops being visible to the endpoint that received it. The tool description and the success note both assert the opposite, and the endpoint.go comment is the premise both are built on.

**Deferred because.** Recorded in batch2's "Checked and clean" section for Batch 3's party-model sweep to decide, explicitly "not opened here". Batch 3 did not decide it.

**Still open.** Two agent-facing strings and the doc comment they rest on assert the opposite of what the party model does. Distinct from the bind-invariant item: this is about what unbind does to mail, not about whether the move is allowed.

*Recorded in: audits + briefs*

##### 129. The task tools advertise a merge verb that does not exist, and two of the three strings are frozen at connect

`internal/stationserver/stationserver.go:529 and internal/stationserver/types.go:197 ("close or merge instead of duplicating"); docs/STATIONS.md:878 (`merge_into?` on station_task_add)`

**Outstanding.** Strike merge_into? from the doc and decide what the two frozen strings should say. Nothing has changed since the finding.

**Evidence.** `grep -rn merge_into internal/` → zero hits; the parameter exists only in STATIONS.md:878. The live surface says it too, in the NearMatches jsonschema and in station_task_add's own description — both pin at conversation start, so a doc-only fix leaves "merge" in every running session.

**Deferred because.** No reason recorded. FINISHING re-verified it by hand on 2026-08-17 and it is still open.

**Still open.** Two of the three strings pin at connect, so a doc-only fix leaves 'merge' in every running session — which makes this a decision about wording, not a one-line edit.

*Recorded in: audits + briefs*

##### 130. §11.9's tool table still gives three wrong signatures, and the SDK hard-errors on an unknown argument

`docs/STATIONS.md:879 and :881 vs internal/stationserver/types.go taskListIn and taskDeferIn`

**Outstanding.** Doc edit; unchanged since the finding.

**Evidence.** STATIONS.md:879 documents `station_task_list(blocked_on?, due?, aging?, state?, limit?)`; taskListIn has State, BlockedOn, Limit — no due, no aging. STATIONS.md:881 documents `station_task_defer(ids[], until, reason)`; taskDeferIn takes a single `task_id`. And the wire name is `task_ids`, not `ids`, for all four batch verbs (types.go:210, :220, :225). A session working from the table gets a hard error.

**Deferred because.** No reason recorded.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 131. The aging nag is wall-clock and three places say it is station activity

`internal/store/station_tasks.go:496-498 (comment) vs :502 (query); docs/STATIONS.md:849`

**Outstanding.** Correct all three, or implement it. Unchanged.

**Evidence.** The comment still reads "Aging is counted in station activity, not wall-clock days: an idle station is not neglecting anything (§11.7)" directly above `last_briefed_at <= strftime(…,'now','-30 days')`, which never touches station.last_activity_at. HandoffStaleness genuinely IS activity-based, which is what makes the parallel convincing. A station idle five weeks reports its whole open list as neglected — the false positive §11.7 says the design avoids. STATIONS.md:849 repeats it: "Expressed in station activity, not…".

**Deferred because.** No reason recorded; the audit names correcting the text as "the honest cheap half".

**Still open.** An idle station reports its whole open list as neglected — the exact false positive §11.7 claims the design avoids — and the parallel to HandoffStaleness (which genuinely is activity-based) is what makes the wrong claim convincing.

*Recorded in: audits + briefs*

##### 132. §7 says every locker blob records the hearsay marking; the locker and the vault have no such column

`docs/STATIONS.md §7 ("Every notebook revision, task and locker blob records … whether that session was inside the hearsay window"); migrations/0012_stations.sql:194-206 (station_locker)`

**Outstanding.** One-line correction plus a sentence naming the exception. Unchanged.

**Evidence.** station_locker's columns are id, station_id, name, bytes, size_bytes, sha256, content_type, updated_at, updated_by_token_id, updated_by_actor_id — no hearsay column; hearsayFor is computed for notes, tasks and link requests only (stationserver.go:329, :497, :538). docs/STATIONS.md:492 already states the correct locker shape, so the document contradicts itself.

**Deferred because.** No reason recorded.

**Still open.** The document contradicts itself — §7 asserts it, and STATIONS.md:492 already states the correct locker shape. One-line correction plus a sentence naming the exception.

*Recorded in: audits + briefs*

##### 133. §3's Message model is stale four ways, and S4's sequence-keying bullet describes the pre-0009 regime

`docs/STATIONS.md §3 ("**Message** — unchanged, plus recipient_station_id and a claim: {claimed_by_endpoint, claimed_at, claim_expires_at}") and §S4 (:161-164)`

**Outstanding.** Replace with the party key as shipped and cross-reference Batches 3 and 4. Unchanged.

**Evidence.** recipient_station_id exists nowhere; claimed_at exists nowhere; the two real claim columns are on `delivery`, not `message`, since migration 0009 — which rebuilt the table, so "Message — unchanged" is false as well. S4's bullet still describes per-(channel, sender) numbering, which 0009 replaced with per-scope. Named at source as "the section that keeps producing the seventh endpoint-rowid comparison".

**Deferred because.** No reason recorded.

**Still open.** Named at source as the section that keeps producing endpoint-rowid comparisons — a reader building on §3 reasons about columns that do not exist.

*Recorded in: audits + briefs*

##### 134. §4's sample briefing shows three lines station_me does not return, and §6 lists "my links" as part of the tool

`docs/STATIONS.md §4 sample (notebook 9 pages / links promo (active) / inbox 2 unread) and §6:568; internal/stationserver/types.go meOut`

**Outstanding.** Rewrite the sample to meOut's fields; drop "my links". Unchanged.

**Evidence.** meOut is StationID, Name, NameSource, Purpose, SelfDescribedAbout/Tags, Tasks, Handoff, Relay, CommEndpointIDs, KenVersion, VersionNote — no notebook, links or inbox, and buildBriefing queries neither links nor mail. The sample's "last written 4 days" also contradicts §4's own activity rule sixteen lines below it. All three facts are obtainable at one extra call each; a session reads the sample as the briefing's contents.

**Deferred because.** No reason recorded.

**Still open.** A session reads the sample as the briefing's contents; all three facts cost an extra call each. §6 compounds it by listing 'my links' as part of the tool.

*Recorded in: audits + briefs*

##### 135. Rename, name-release, rename-on-unarchive and reassignment are documented; none is built, and RenameStation has zero callers

`docs/STATIONS.md:117, :131-133, :546, :711; internal/store/stations.go:183-191`

**Outstanding.** Wire RenameStation to a form beside publish/archive, or delete it — and fix S3's release paragraph and the comment either way.

**Evidence.** RenameStation has no route, no form, no CLI verb and no test; the comment above it claims "RenameStation / SetStationPublished / ArchiveStation are HUMAN-only operations, reachable from the console **and the CLI**", which is false for the CLI for all three (`ken station` is add|list|key|requests). There is no released-name state anywhere and ArchiveStation never touches name, so S3's "the console says so at the release click, and unarchive is refused while the name is taken (offering rename-on-unarchive)" describes a control that does not exist.

**Deferred because.** No reason recorded.

**Still open.** RenameStation exists with no way to reach it, and its own comment makes a CLI claim that is false for all three functions it names. S3's release paragraph describes a control that does not exist at all.

*Recorded in: audits + briefs*

##### 136. §9 publishes three bounds nothing enforces, under a heading that calls them live settings

`docs/STATIONS.md §9 ("Live settings; the reason is attached…") rows for closed/dropped task retention, station queue, and link-request quota`

**Outstanding.** Delete the three rows and the composing paragraph; building any of them is a feature. Unchanged.

**Evidence.** (a) 2000 closed+dropped tasks per station with oldest-first pruning — internal/store/station_tasks.go contains no DELETE and no retention setting exists. (b) station queue 20 messages + byte cap + 7-day TTL, and the "both checked in the same insert transaction" paragraph — there is exactly one cap on every insert path, MaxUnackedPerChannel (internal/comm/comm.go:218 default 64; internal/comm/message.go:208, room_send.go:187). (c) per-station link-request quota — CreateStationLinkRequest checks the mute, dedupes the pair and inserts; the mute half IS built and correct.

**Deferred because.** No reason recorded. Rule 1 blocks building them, which leaves deletion as the only available half.

**Still open.** Building any of the three is a feature, not a fix — so the honest move is deleting the rows, which makes this a decision. The composing paragraph asserts a two-cap insert transaction that has exactly one cap.

*Recorded in: audits + briefs*

##### 137. auth.go says ScopeStationLocker gates the locker tools; requireLocker is a straight alias and also gates the vault

`internal/stationserver/auth.go:27-29; internal/stationserver/stationserver.go requireLocker`

**Outstanding.** Comment edit — keep the constant, since old scope lists must still parse. Unchanged.

**Evidence.** `func requireLocker(ctx context.Context) (*principal, error) { return requireStation(ctx) }`, and it gates all four vault tools as well. A reader auditing what a restricted key reaches concludes a locker-less station key is possible; the merge was deliberate (§6:585-592) and is nowhere reflected here.

**Deferred because.** The merge was deliberate: "a station whose capabilities depended on which key a session happened to be handed — so 'does this station have a locker' had no answer, only 'does this key'." The constant is kept so old keys' scope lists still parse.

**Still open.** Confirmed outstanding, and it is a same-package self-contradiction. stationserver/auth.go:27-28 says the scope "gates the locker tools"; stationserver.go:769-786 documents the deliberate merge ("requireLocker gates the locker on the STATION scope alone") and implements `func requireLocker(ctx) { return requireStation(ctx) }`. The merge itself is a settled decision with a stated reason (a station's capabilities must not depend on which key a session was handed); what is outstanding is only the stale comment.

*Recorded in: audits + briefs, code comments — 2 separate places*

##### 138. STATIONS.md's status banner certifies the operator console as built and names only two unbuilt things

`docs/STATIONS.md:15-24`

**Outstanding.** Rewrite once the items above are decided, and maintain it in the same commit as any §-numbered claim.

**Evidence.** The Built list still includes "the operator console at /stations (§10)" — which is where the missing notebook view, the missing key revoke, the missing task controls and the unrendered vault actor all live — while the Not-built list names only per-station connect-time instructions and station-specific metrics. It is the sentence that licenses trust in the rest of the document.

**Deferred because.** Deliberately sequenced last: the banner cannot be honest until the items it would have to list are decided.

**Still open.** It is the sentence that licenses trust in the rest of the document, and /stations is exactly where the missing notebook view, key revoke, task controls and unrendered vault actor live.

*Recorded in: audits + briefs*

##### 139. STATIONS.md calls if_rev optional; mode=replace on an existing page requires it

`docs/STATIONS.md:497-499 vs internal/store/station_notes.go:90-96 (ErrNoteRevRequired) and :192-205`

**Outstanding.** One sentence. The rest of the replace-vs-append framing WAS corrected (b3ace0d, three sites: the connect-time block, station_note_write's description, and the empty-handoff nudge); this line was not.

**Evidence.** "`station_note_write` takes an optional `if_rev`: the write is refused if the page moved underneath it" — while ErrNoteRevRequired is returned when mode=replace would overwrite an EXISTING page with no if_rev at all, and the store comment at :192 says "It used to be optional, and optional was…".

**Still open.** The surrounding replace-vs-append framing WAS corrected in b3ace0d at three sites; this line was missed, and it is the one that tells a session it may skip the precondition.

*Recorded in: audits + briefs*

##### 140. entry.trust_policy is dead on both sides while DESIGN's D4 states the three-mode chain in the present tense — and the hardcoded behaviour is not the documented default

`migrations/0001_init.sql:104; docs/DESIGN.md:86-89 and :199; docs/MCP-TOOLS.md:203; internal/store/get.go:61-66`

**Outstanding.** Delete the column (migration, ships alone) and past-tense D4; the doc fix is the urgent half.

**Evidence.** The identifier appears in the schema and in DESIGN.md and in no Go file; there is no global default setting either. Worth recording before anyone builds it: get.go falls back to the PROVISIONAL body for a never-promoted entry ("Body = the curated head, or (for a draft with no curated version yet) the best provisional"), which is exactly what the documented default `curated_only` says is withheld.

**Deferred because.** No reason recorded.

**Still open.** NOT FILTERED

*Recorded in: audits + briefs*

##### 141. verified_at and verify_ttl_days are dead, and two documents claim a ranking tie-break that reads them

`migrations/0001_init.sql:158-159; docs/MCP-TOOLS.md:185 and :303; docs/DESIGN.md:91 and :255`

**Outstanding.** Delete the columns; correct four sentences, prose first. Do NOT drop blind: internal/store/via_comm_test.go:100 and :191 write verify_ttl_days as a mutable-status stand-in and pin the immutability trigger.

**Evidence.** No product code writes either column. MCP-TOOLS.md:185 and DESIGN.md:255 assert "a tiny post-RRF tie-break toward battle-tested / fresher verified_at"; the ranking is ORDER BY f.score DESC and maturity() is a display string computed after the rows return — MCP-TOOLS prints the ORDER BY three lines above the claim. DESIGN.md:91 names time and dependency-bump as aging mechanisms, neither of which exists, which is where the unreachable `aging` state came from.

**Deferred because.** No reason recorded.

**Still open.** Four sentences assert a ranking effect that does not exist, one of them three lines below the ORDER BY that disproves it — and the dead columns are also where the unreachable `aging` state came from. The drop-blind warning is real.

*Recorded in: audits + briefs*

##### 142. The save-vs-enhance rubric is documented as returned with every search result; searchOut has no such field — and this claim has already escaped the repository

`docs/AI-INTEGRATION.md:76, :153, :230, :374; docs/MCP-TOOLS.md:258; docs/DESIGN.md:257; internal/mcpserver/server.go searchOut`

**Outstanding.** Delete the per-hit rubric and the cosine numbers; keep the qualitative rule of thumb in the connect-time block. Whatever is chosen, the downstream copies are corrected in the same change.

**Evidence.** searchOut carries Results, Matched, DeadTerms, HasMore, NextOffset, KenVersion, DedupCheckToken — no rubric, no per-hit similarity, no verdict. The cosine thresholds cannot be applied even by hand: the only number is the fused RRF score, and AI-INTEGRATION.md:83 tells the agent to disregard it in favour of "Ken's returned guidance". It is verbatim in this machine's own global agent instructions (~/.claude/CLAUDE.md: "returns a save-vs-enhance rubric with each search result — follow those"), so the propagation is confirmed outside the repo, not merely predicted.

**Deferred because.** No reason recorded. Named at source as the strongest instance of the class this project pays for most often.

**Still open.** Six doc sites, and the claim has confirmably escaped the repository into an operator's global agent instructions — so a fix here has a downstream leg.

*Recorded in: audits + briefs*

##### 143. `comm-file` scope is documented as reserved and unimplemented, but file exchange shipped and the scope is enforced

`internal/commserver/auth.go:22-28; internal/store/scopes.go:14`

**Outstanding.** Correct both comments. `comm-file` is no longer reserved: `requireFileScope` (internal/commserver/commserver.go:871-878) rejects a token without it, and `internal/commserver/files.go:58` authenticates the byte relay against `ScopeCommFile`. A reader deciding whether a token needs the scope is told it needs nothing.

**Evidence.** auth.go:22 — "ScopeCommFile is RESERVED and required by nothing yet: file exchange is deferred to a later MINOR (docs/COMM.md §11)." scopes.go:14 — "`comm-file` and `station-locker` are RESERVED." File exchange shipped in commit 38df77f ("COMM: file exchange — rendezvous offers and a one-time-grant HTTP relay"); comm_file_offer/comm_file_grant are registered at commserver.go:811.

**Deferred because.** Original reason for declaring the scope early is still valid and worth keeping: "splitting a shipped `comm` scope into two later would be a MAJOR under COMPATIBILITY.md, while merging two into one is free."

**Still open.** Confirmed outstanding. commserver/auth.go:22 still reads "ScopeCommFile is RESERVED and required by nothing yet: file exchange is deferred to a later MINOR" while requireFileScope (commserver.go:871-878) rejects tokens lacking it and files.go:58 authenticates the byte relay against ScopeCommFile. A doc-vs-reality inversion of the exact class FINISHING.md tracks. One narrowing for the human: in store/scopes.go:14 only the `comm-file` half is wrong — `station-locker` really is still reserved (see item 2), so that line needs a split, not a deletion.

*Recorded in: code comments*

##### 144. `handleStationKeyRetire`'s doc comment still promises the behaviour the store documents as false

`internal/web/stations.go:495`

**Outstanding.** Correct the comment. The operator-facing i18n string was fixed; this Go comment was not, and it is the copy the next maintainer reads.

**Evidence.** stations.go:495 — "handleStationKeyRetire stops a key binding new endpoints without touching live ones." internal/store/stations.go:318-321 — "RetireStationKey stops the key working — INCLUDING for a session holding it right now. IT DOES NOT 'LEAVE LIVE ONES ALONE', and six shipped strings said it did until 2026-08-14." messages.properties:546 now reads "Stops this key working, including for sessions holding it right now". The comment dates from the original console commit 53bd615 and has never been touched.

**Deferred because.** Not stated — this is the residue of the 2026-08-14 correction pass, which reached the bundles and missed the Go comment. Exactly the "text that described controls Ken did not have" class named in TARGET-ARCHITECTURE.md §7.

**Still open.** Confirmed outstanding. web/stations.go:495 is unchanged and still reads "stops a key binding new endpoints without touching live ones"; store/stations.go:318-321 states the opposite in capitals and dates the correction (six shipped strings said it until 2026-08-14). The operator-facing string was fixed; this Go comment is what the next maintainer reads, and this project's most expensive recurring defect class is text describing controls Ken does not have.

*Recorded in: code comments*

##### 145. internal/comm/admin.go still describes message.space_id as an existing column; comm migration 0012 dropped it

`internal/comm/admin.go:145-147 vs internal/comm/migrations/0012_drop_message_space_id.sql:34`

**Outstanding.** One-line correction. The comment is the file's own justification for how message counters attribute a space, so a reader checking it against the schema finds the schema disagreeing with the reasoning.

**Evidence.** admin.go:145-147 at HEAD: "(There is a `message.space_id` column, added by migration 0009; it is written by nothing and read by nothing, so populating and backfilling it would be a data rewrite to reach a fact already one join away.)" Migration 0012 (shipped in 3.9.0, `0ba0811`) runs `ALTER TABLE message DROP COLUMN space_id;`. The surrounding reasoning — attribute through `sender_endpoint -> endpoint.space_id` — is still correct; only the parenthetical is now false.

**Deferred because.** Not stated; the migration landed (Batch 4 of docs/FINISHING.md) and the comment that motivated it was not updated with it.

**Still open.** Confirmed false at HEAD. Small, but it is the file's own justification for how message counters attribute a space, so a reader checking the reasoning against the schema finds them disagreeing — a stale statement invalidated by a shipped change, which the house rule says gets corrected on the spot.

*Recorded in: code comments, migrations + schema — 2 separate places*

##### 146. COMM.md §9 lists a set of operator metrics that does not exist

`docs/COMM.md:772-773`

**Outstanding.** Either build the counters or correct the sentence. As written it promises an operator six signals, of which two exist.

**Evidence.** docs/COMM.md:772-773 — "**Metrics** (never health, per §5.4): messages sent/delivered/acknowledged/expired, queue depth, parked waiters, storage bytes, sweeper lag." Of these, only queue depth (`ken_comm_messages_unacked` / `ken_comm_deliveries_unacked`), parked waiters and storage bytes exist (cmd/ken/main.go:992-1015). There is no sent/delivered/acknowledged/expired counter and no sweeper-lag metric anywhere in the tree — which §13 of the same document independently confirms when it proposes adding `ken_comm_messages_expired_total` as new work.

**Still open.** Confirmed false as written, and distinct enough to keep — its remedy branches to a documentation fix, which item 6 does not cover. Flag for the human: the BUILD half is entirely subsumed by item 6, so merge them if the resolution is to build rather than to correct the sentence.

*Recorded in: docs tree*

##### 147. BACKUP.md still tells the operator to encrypt snapshots, and points at machinery retired in 2.0.0

`docs/BACKUP.md:63, 77, 110, 255`

**Outstanding.** Four statements to fix. Line 77 sends the reader to an encryption section "(below)" that does not exist; line 110 describes the shared secure step as "removing the plaintext only after a confirmed encrypt"; line 255 offers the backup group as pairing "naturally with encryption — with a recipient set"; line 63 frames encryption as "opt-in and OFF by default" on both tiers when tier 2 has no on switch at all.

**Evidence.** scripts/ken-snapshot-lib.sh:37 — "IT NO LONGER ENCRYPTS. Ken used to age-encrypt here when KEN_AGE_RECIPIENT was set, and that is retired." scripts/ken-snapshot.sh:49-50 warns if the retired variable is set. COMPATIBILITY.md:37-38 and docs/UPGRADING.md:1084 record the removal. The same file contradicts itself at docs/BACKUP.md:267 — "Ken makes no attempt to protect the file once it leaves the box, and does not encrypt it. That is a deliberate scope, not an omission."

**Still open.** Confirmed on all four lines, and the dangling "(below)" is the worst of them — there is no encryption section anywhere in the file. Same root cause as item 19 but a different file and different wrong sentences; both need fixing.

*Recorded in: docs tree*

##### 148. DESIGN.md still records age-encrypted backups as the intent and as shipped-opt-in

`docs/DESIGN.md:319-321 (§6); the roadmap entry at docs/DESIGN.md:401-402 repeats it`

**Outstanding.** Mark the decision reversed the way COMM.md's C2 and DESIGN.md's own COMM switch record were marked SUPERSEDED, rather than leaving it reading as current intent.

**Evidence.** docs/DESIGN.md:319-321 — "**Intent: age-encrypt every backup** … *Shipped as opt-in:* encryption turns on when the operator sets a recipient." The recipient variable (`KEN_AGE_RECIPIENT`) was removed in 2.0.0 and the reversal is documented in scripts/ken-snapshot-lib.sh:37-48 and docs/UPGRADING.md:1084.

**Still open.** Confirmed. The reversal is recorded elsewhere in the tree but not here, so the architecture document reads as current intent for a decision that was reversed under an explicit instruction. Same root cause as item 18, different file.

*Recorded in: docs tree*

##### 149. DESIGN.md carries at-rest whole-file encryption (VFS) as "still open / deferred" after the project ruled encryption out of scope

`docs/DESIGN.md:464 (list head) and 328-330`

**Outstanding.** Decide whether this is deferred or declined, and say so in one place. DESIGN.md:328-330 still instructs "enable `ncruces` encrypted VFS … once the shared $5 host is deemed outside the trust boundary. Verify Litestream + encrypted-VFS on the real VPS before committing" — a live instruction pointing the opposite way from S13.

**Evidence.** docs/DESIGN.md:464 lists "at-rest whole-file encryption timing (VFS)" as still open. docs/STATIONS.md:435-438 records the countervailing ruling: "security is not a functional concern of Ken, and *'a non-encrypted database up to the backup point'* is preferred to a key-management problem that buys nothing."

**Still open.** Confirmed contradiction, and it is a live instruction pointing the wrong way rather than stale narrative. Distinct from item 19: that is backup encryption, this is live-database encryption. The outstanding act is a decision (deferred vs declined), not a build.

*Recorded in: docs tree*

##### 150. MONITORING.md tells an operator that a missing comm_* or station_* metric series means the surface was turned off — nothing can turn either off

`docs/MONITORING.md:72`

**Outstanding.** Correct the diagnostic. For stations the stated cause is impossible; for COMM the only real cause is the degraded state (comm.db unopenable), which the same document states correctly ten lines later at MONITORING.md:80-83.

**Evidence.** docs/MONITORING.md:72 — "a missing `comm_*` or `station_*` series means the surface was turned off, not that it is idle." cmd/ken/main.go:509 is `stationsEnabled := true`, preceded by the comment "no flag exists that could couple them"; cmd/ken/surfaces_core_test.go asserts nothing reads KEN_COMM_ENABLED or KEN_STATION_ENABLED. An operator following this line would hunt for a switch that does not exist while the real cause (a degraded comm.db, logged as `COMM: DEGRADED`) goes unexamined.

**FIXED 2026-08-19.** It was confirmed false and actively misdirecting — sending an operator hunting for a switch that cannot exist while the real cause went unexamined, in a document that stated the correct cause ten lines later. `docs/MONITORING.md` now says a missing series means nothing has called that surface since the process started, and names the removal explicitly. *This entry stayed marked open for a day after the fix, which is the index having the same disease as the checklist.*

*Recorded in: docs tree*

##### 151. README's migrations pointer stops at 0009; the tree has 19

`README.md:82`

**Outstanding.** One-line fix, but it is the source-of-truth pointer: it hides ten migrations including every station, vault, room and provenance change (0010_comm_provenance through 0019_freeze_via_comm_kind).

**Evidence.** README.md:82 — "[migrations/](migrations/) — the SQLite schema (source of truth; `0001_init.sql` … `0009_content_lang.sql`)." `ls migrations/` shows 0001 through 0019.

**Still open.** Confirmed and trivially fixable, but it is the pointer README calls the source of truth, and it hides every station, vault, room and provenance migration. Keep, flagged as one-line work — do not let its size suggest the surrounding items are comparable.

*Recorded in: docs tree*

##### 152. entry.trust_policy is a dead column with a dead CHECK vocabulary — and DESIGN.md documents the mechanism as working

`migrations/0001_init.sql:104-106 (declared); docs/DESIGN.md:86-89 (documented as behaviour)`

**Outstanding.** Either build the trust-policy resolution (explicit param ▸ entry ▸ global default) or drop the column, its CHECK and the DESIGN.md paragraph. Today a curator reading DESIGN.md believes un-curated proposals are served under a per-entry policy; they are not.

**Evidence.** `trust_policy TEXT CHECK (trust_policy IN ('curated_only','high_confidence','all_proposals'))` with the comment `NULL = inherit global default ('curated_only')`. Case-insensitive grep for `trust_policy`/`trustPolicy` over the whole tree returns ZERO Go hits — no writer, no reader, no test. There is no global-default setting either (`internal/settings/settings.go` has only `TrustedProxies`). What actually exists is `scopeStatePredicate` (internal/store/search.go:186-196), a caller-supplied scope of curated|proposals|history|all — one of the three resolution levels DESIGN.md describes, with no `0.75` threshold and no `NOT_YET_CURATED` label anywhere in the tree.

**Still open.** Confirmed unbuilt at HEAD, and the doc/schema disagreement is live. This is not just dead schema: DESIGN.md describes a three-mode resolution chain as current behaviour, so a curator reading it holds a false belief about what is served. Either build it or delete column + CHECK + paragraph — a real open decision.

*Recorded in: migrations + schema*
---

*Swept 2026-08-18 from the tree at a0ed9ec. Spot-checks in the header were run by hand; everything else is the sweep's own verification.*
