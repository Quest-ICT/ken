# Workspace identity — the locked decisions

> **STATUS: DESIGN, NOT PLAN. Nothing here is built.** This records decisions Vlad took on
> **2026-08-21**, in the conversation `docs/TARGET-ARCHITECTURE.md` was written to make possible.
> It supersedes nothing and schedules nothing; `FINISHING.md` is unaffected.
>
> **What it is for.** Ken's identity and access layer is being replaced — that layer and no other.
> The delivery model, the knowledge base, the console and the migration discipline stay. This file
> is the contract the replacement is built against, written *before* any code, because the reasoning
> behind the current controls lives in scattered comments and in one session's context, and a
> replacement written without it would rebuild the same defects under new names.

---

## 1. The problem, in Vlad's words

> *"Ken has become an invaluable tool for me while I work with Claude. But it has also been a
> torture to keep it working, mainly because sessions experience many problems while trying to use
> Comm and Station services."*
>
> *"The killer feature is ease of usage for the human user. Few approval points, not having to
> generate numerous tokens for things to work, and definitively not having to spend a lot of time
> trying to get Ken working instead of working with the AI on the problems the human really wanted
> to fix."*

Measured rather than asserted, in `TARGET-ARCHITECTURE.md` §4b: **six occurrences across five
sessions**, the worst costing **10 h 43 m** to a working channel, and **352 copies of one endpoint
secret on disk** — with ken-prod-ops independently measuring **543** on theirs.

## 2. Settled facts

These are decisions, not proposals. Each was taken explicitly.

| | |
|---|---|
| **Single user, permanently** | One human per Ken instance. One Claude account per instance. Many Claude Code sessions — *the sessions are the actual users of the system.* |
| **Multi-user is federation, never tenancy** | If instances ever need to share, that is a separate **federation service between instances**. Nothing inside one instance is ever multi-tenant again. |
| **A workspace is what a station already is** | A durable identity owning notebook, tasks, vault and channels, which outlives every session. |
| **One workspace per folder** | Not a new rule — Claude Code already keys MCP config by the folder a session starts in, and the estate converged on one station per folder without anyone designing it. |
| **Identity is a stable opaque id; the name is decoration** | The id travels in the folder's MCP config. **It is not a secret** — knowing it authorises nothing. |
| **The human can rename a workspace at any time** | With no side effects anywhere. |
| **An unknown folder works immediately** | Auto-named, fully functional, no approval. |
| **The existing estate is migrated, not abandoned** | Stations, notebooks, tasks, vaults, channels and links all survive. |

## 3. The four words

"Project" is deliberately **not** used anywhere in this design: in Claude Code it means folder, and
using it loosely is what made the first explanation of this model unclear.

| Term | What it is | How many | Approved how |
|---|---|---|---|
| **Device** | a machine where the human has logged into claude.ai | any number | **the login is the approval.** No Ken step exists or is wanted |
| **Folder** | the directory a session starts in | many | never — it is a place, not an identity |
| **Workspace** | the durable identity: notebook, tasks, vault, channels | one per folder | **once, by name, in the console** — and even that is optional |
| **Session** | one Claude Code conversation | many, over time | **never.** It inherits its folder's workspace |

## 4. Identity: an id in config, a name in the console

The folder's MCP entry carries a **stable opaque workspace id**, written once by Ken:

```
X-Ken-Workspace: lhqBQKBpTSyJoZyu      ← permanent, meaningless, not a secret
name in console:  ken-public            ← the human's, renameable at will
```

**Why the id and not the name.** `COMM.md` §3 already states the rule, learned on endpoints:

> *"Display labels are non-unique decoration. Routing is always by `endpoint_id`. A human-chosen
> name is never an address, or the first release ships a global namespace one session can squat."*

The first sketch of this design put the **name** in the config, and Vlad's rename requirement
exposed it immediately: renaming would have invalidated every folder pointing at that workspace.
Recorded because the rule already existed and was nearly designed away.

**Why it is not a secret, and why that is the whole point.** Today the per-folder value is a station
key — a bearer credential a human must generate, deliver and protect. That is the mechanism that has
been costing hours. Its protection is also largely theatre: on 2026-08-18 the only delivery path
offered was a prompt, which the same instruction forbade, and the key was burned on arrival. **A
credential whose delivery path is a prompt is not protecting anything.** A name tag cannot leak,
cannot be burned, never expires and never rotates.

**What authorises, then.** The human's OAuth grant proves *who*, and single-user makes that
sufficient: within one instance there is one human and one Claude account, so a session declaring a
workspace is that human's own session. There is no other tenant to protect against. **The isolation
that matters moved to between instances — federation — which is where Vlad put it.**

**The residual risk is confusion, not compromise** — a session adopting the wrong workspace and
reading another's mail. It is mitigated by visibility rather than by credentials: Ken shows which
workspace each session claimed, every session states its workspace in its first message, and two
live sessions claiming one workspace is a condition the console can surface.

## 5. Starting work in an unknown folder

Decided: **fully working, auto-named, no approval.**

1. Ken mints a workspace id and an **auto-name** from the folder's basename, disambiguated on
   collision — names are unique per space (`idx_station_name`).
2. The session works **immediately**: notebook, tasks, vault, knowledge base. Nothing withheld.
3. It says so in its first message: *"working as `ken-public` (auto-named) — rename it in the
   console if that is wrong."*
4. The **only** thing still needing the human is two workspaces talking, because that is a decision
   about who may reach whom and always was.

**Naming pays for itself at (4) rather than being a setup chore.** The channel approval screen reads
*"`ken-public` wants to talk to `ken-prod`"* — if the auto-name is wrong, the human fixes it at the
one moment a bad name is actually in front of them.

## 6. Every approval in the finished system

| Event | Approval |
|---|---|
| Log into claude.ai on a device | none — it *is* the approval |
| Start work in a new folder | **none.** Auto-named, fully working |
| Rename a workspace | none, and no side effects |
| Two workspaces talking | **one, once per pair** |

Against `TARGET-ARCHITECTURE.md` §3, where Vlad said he would accept one approval per device *and*
one per session: this is fewer. The only surviving approval is the one he explicitly wants.

## 7. What this deletes

Station keys. Binding vouchers. Pairing codes. Per-machine COMM tokens. Endpoint secrets a session
must store, protect and re-read after every compaction. The `comm_bind` dance. Most of it exists to
solve problems that do not exist under one human and one Claude account.

**§9 is the correction to that paragraph, and it is a real one.** Of 164 credential controls,
**three** dissolve outright. Most of the list above is deleted *conditionally* — safe only once one
identity spans `/comm` and `/station`, or once the thing in the MCP header genuinely stops carrying
authority. Read §9 before treating anything here as settled.

## 8. Migrating the existing estate

Decided: **migrate.** `quest-infra` alone holds 47 tasks; there are live channels, links and a
vault. Each existing station becomes a workspace, keeping its `station_id` — which is already the
stable identifier everything else is keyed to — and its id is written into the corresponding
folder's MCP config. Notebooks, tasks, vaults, channels and links follow unchanged because they
already reference the station rather than any credential.

*The detailed transition, including what is keyed to a token or endpoint id and therefore needs
re-pointing, is §10 and depends on the extraction in §9.*

## 9. What we must not lose

**164 credential controls were extracted and each was asked whether single user genuinely
dissolves it. Three do.** Every "dissolves" verdict went to an adversary whose only job was to
refute it, defaulting to "survives" when uncertain. Twenty-three claims went in; three came out.

| verdict | count |
|---|---:|
| survives — single user changes nothing about why it exists | 95 |
| survives, but the axis it is keyed on moves | 46 |
| dissolves **only once a stated condition holds** — claimed free, refuted | 20 |
| **dissolves outright** | **3** |

The full register, with each control's stated reason quoted and what breaks if it goes, is
**[IDENTITY-CONTROLS.md](IDENTITY-CONTROLS.md)**. What follows is what changes the design.

### 9.1 The three that dissolve are one thing

`space_id` — the pairing code's space check, ownership keyed on space + authorizing human, and
the seam itself: 24 schema columns, 118 Go references, `UNIQUE idx_station_name(space_id, name)`.
Nothing can create a second space, and multi-user is now federation between instances. It goes.

**But not piecemeal, and not silently.** `TestPairingCodeIsSpaceScoped` fabricates a second space
and asserts the denial; the refuter deleted the three-line check and watched CI go red in seconds.
So the check *is* exercised — in the suite, never in production. Whoever removes it deletes that
test in the same commit, deliberately and reviewably, and says why. Removing the check first and
the test later leaves a live control nothing exercises, which is the state the whole exercise
exists to avoid.

### 9.2 §7 was too confident. The big deletions carry conditions

§7 above lists what this deletes. The extraction agrees about the *list* and disagrees about the
word "deletes" — twenty of those removals are safe only once something else is true, and the
condition is usually the thing being built:

- **The binding-voucher chain is the single largest safe deletion available** — the voucher, its
  5-minute TTL, single-use, endpoint pinning, actor matching, hash-at-rest and sweep, all of it.
  The condition: **one identity must span `/comm` and `/station`.** The voucher exists solely so a
  station key never crosses to the comm surface as a tool argument. Nothing to hand across, nothing
  to hand it with.
- **The header-only rule for station keys** dissolves *because the thing travelling stops being a
  secret.* A stable opaque workspace id in an MCP header authorises nothing, so there is nothing to
  keep out of a transcript. **If that id ever gains authority, this control comes straight back**,
  and §4's "it is not a secret, and that is the whole point" becomes load-bearing rather than
  descriptive.
- **The owner-token re-check** (`ep.Owner.TokenID != p.TokenID`, run on every tool call) becomes
  `x != x` under one OAuth identity and already costs more than it protects — it is why an endpoint
  cannot move between machines and dies with its registering token. But it is *the last line of
  `auth()`*, the only thing between "holds a valid endpoint id and secret" and "is the right
  principal". **Delete it and the replacement must state what binds a workspace to a credential, or
  the honest answer is "nothing".**
- **Both-sides-join** — the first redeem creates a pending channel, the second opens it — is the
  one control the settled facts actively retire, and Ken has already retired it twice in place. Its
  cost is measured: `proxmox-servers` held a station and no endpoint for five days, so an approval
  in that window materialised nothing. The successor is already written down in `COMM.md` — *"a
  pair scope is derived from the two ids, so it needs neither side to be online"*. **Build on the
  derived-scope shape; do not re-derive rendezvous.**

### 9.3 Three migration landmines — move the data *before* retiring the credential

- **`endpoint.token_id` welds every live endpoint to the token that registered it, and there is no
  re-pointing path in the code.** Re-point the rows **first**; nothing that retires a credential may
  run before that does.

  > **CORRECTED 2026-08-24. This entry said the weld strands "their endpoints and every queued
  > message on them", and the second half is wrong.** The poll predicate keys on the PARTY, and for
  > a bound endpoint it deliberately matches *both* the station party and the endpoint's own — so a
  > bound endpoint's mail is filed under `s:<station_id>` and a replacement endpoint bound to the
  > same station inherits it. **The mail survives. What dies is the CONNECTION**: the row, its
  > secret, and the session's ability to reach it without re-registering, taking a voucher and
  > re-binding.
  >
  > That is not a smaller problem, it is a different one — and it is the ceremony §4b exists to
  > remove, reached from the other end. Measured on production: of 16 endpoints, 8 are bound and
  > survive; of the 8 unbound, 2 are revoked and 6 live; and the mail genuinely at risk is **7
  > unacked deliveries across exactly two endpoints** (`rb5009-config` 3, `runway-prod-admin` 4).
  > Everything else unbound is empty or fully acked.
  >
  > **Bounded, and knowable per endpoint in advance** — which decides the shape: a pre-flight that
  > NAMES the endpoints holding unacked mail and refuses, not a caveat in a document. Check what the
  > value has to produce, never its form.

  **THE MAGNITUDE, measured on the only deployment there is (2026-08-24):**

  ```
  jMl4ZNH4q73E   endpoints=13   LIVE=11
  eNfNcVwXQ0Uj   endpoints=2    live=2
  qXykpIUyOLHg   endpoints=1    live=1
  ```

  **Eleven live endpoints hang off one token** — ken-prod-ops, ken-public-dev, both collector
  proxies, ken-promo, network-infrastructure, both runways, rb5009-config, proxmox-servers.
  Retiring that one credential today forces eleven re-onboardings at once, **including the channel
  the two stations would use to report that it had gone wrong.**

  **And there is an open rotation decision on that exact token**, carried as housekeeping. It is
  not housekeeping: until this step lands, "rotate `jMl4ZNH4q73E`" means "re-onboard the estate".
  That makes this step a live operational blocker rather than scaffolding for a transition — it
  would be worth doing if the rest of this document were abandoned tomorrow.

  Production had already reported this column as write-once on 2026-08-18, filed then as a
  *rotation* gap. **Same column, two different disasters, and the second happens on purpose.**

  > **RESOLVED IN 3.19.0.** `POST /comm/endpoints/{id}/repoint` moves one, `POST
  > /comm/tokens/{id}/repoint` moves every live endpoint of a token in one statement, and
  > `/comm` renders the owning token so the concentration is visible without a database query.
  > The step remains first in §10: the control exists, the estate has not moved.
- **`endpoint.bound_by_station_key_id` is a SECOND weld on the same row, and the entry above
  called its rows the safe ones.** Every BOUND endpoint carries the station key that authorised
  its binding, and that column is checked **at use, on every call** —
  `commserver.go` → `store.IsStationKeyRevoked`, which returns *revoked* for a **missing** row as
  well as a revoked one. So **revoking** a station key — or deleting its row, which the identity
  transition does — ends its bound sessions just as surely as revoking their comm token does.
  **Bound endpoints have two welds; unbound have one.** The
  correction above was right that a bound endpoint's mail survives the token weld and wrong to
  read that as safety.

  **THE MAGNITUDE, measured by ken-prod-ops on 2026-08-24, and it is the INVERSE of the first:**

  ```
  live bound endpoints = 8      distinct bound_by_station_key_id = 8      RATIO 1:1
  ```

  **No concentration at all — revoking one key costs exactly one session**, recoverable one at a
  time, against eleven at once for the token weld including the channel the report would travel
  on. That is why the token half shipped first, and it makes this a **correctness** fix rather
  than a blast-radius fix. It should not inherit the first one's alarm.

  **The 1:1 is an accident of provisioning, not a design property.** Those eight stations were
  set up one at a time; nothing prevents a future key from binding several endpoints. A guarantee
  that matters has to be enforced rather than observed — and this one is not worth enforcing (one
  key legitimately covers several sessions of one station), so what is built instead is the
  count before the click and the move that makes retirement survivable.

  > **RESOLVED IN 3.20.0.** `POST /comm/endpoints/{id}/rebind` and `POST /comm/keys/{id}/rebind`
  > move a binding to another key **of the same station** — the rule is in the `UPDATE`'s `WHERE`,
  > so another station's key can never become a lever over these sessions. Unlike an owner
  > re-point, the running session needs no config edit and no restart. `/comm` renders the binding
  > key and groups endpoints by both credentials.
  >
  > **One asymmetry to know: on the CLI revoke path this control is CURATIVE.** `ken token revoke`
  > has no `comm.db` handle and cannot sever, so its endpoints stay live-looking and are refused
  > one call at a time — re-binding them repairs a session that has already stopped answering. On
  > the console revoke path the sweep marks them revoked and a revoked endpoint is refused here,
  > so there the move must come **first**. There is no un-revoke path anywhere in the tree.
- **`api_token.station_id IS NULL` is a state, not data** — a key in it may call exactly one tool.
  §5 deletes the state deliberately. What must not go with it is the human-typed *name*: §5 keeps
  naming by moving the moment to the link-approval screen, which is the one place a bad name is in
  front of the human anyway.

### 9.4 Controls that survive with their key moved

The 46 in this class share a shape: they are keyed on a credential, and the replacement collapses
many credentials into one identity. Three cost something real, and the design should say so rather
than discover it:

- **Revocation granularity collapses, and this is no longer a hypothetical.** Today the unit is a
  key per machine — *"revoke this laptop"*. One OAuth grant per instance makes the unit the whole
  instance. Single user does not shrink the machine count.
  **ken-prod-ops supplied the instance on 2026-08-24, and it is an incident rather than a
  scenario:** that estate holds keys on three machines, and the credential incident of
  2026-08-18 was resolved *precisely* by killing one key without touching the others. Under one
  grant per instance the same incident becomes *"revoke everything, or accept the exposure"*.
  The trade may still be worth making — but it must be made knowing that the case it costs has
  already happened once, on the only deployment there is.
- **Rate limiting is per token, with a separate bucket per surface.** Collapse the tokens and every
  session on every machine shares one bucket, so one runaway loop starves the rest — which is the
  exact starvation the per-token split was introduced to prevent.
- **Provenance is recorded per credential and read by nobody.** `updated_by_token_id`,
  `created_by_token_id`, `by_token_id` and six siblings are written across notes, revisions, tasks,
  the locker and the vault, and **every one of them is write-only**. The design wanted per-write
  attribution; only the plumbing is missing. Under the new model the meaningful unit is the
  **session**, so the replacement records a session id **and renders it** — otherwise it rebuilds a
  write-only column under a new name, which is precisely what this extraction exists to prevent.

### 9.5 Lessons that outlive the code they came from

- **The `station-locker` precedent, and it is the closest thing here to a warning about the new
  design.** A capability withheld per credential produced a state no session could interpret —
  *restricted, or misconfigured?* — the fails-indistinguishably defect, arrived at by **adding** a
  control. It was fixed by merging the scope away. Fewer approval points is not only kinder; it
  removes states that cannot be told apart.
- **Station-to-station isolation is a config-file boundary and the docs already say so.** Under one
  human it protects one of his workspaces from another of his sessions. The new model's one
  approval per talking pair must be justified as **intent and blast radius**, never as isolation —
  the human holding both configs can bridge them at will.
- **A credential whose shape cannot satisfy any surface must be refused at issue**, because the
  failure at use is indistinguishable from a network problem.
- **Make single-use a conditional `UPDATE`, never check-then-act.** It survives the voucher.
- **Hash every stored credential, with no exception for short-lived ones.**
- **The actor check's failure mode does not dissolve even though the check does.** What it catches
  is "the write-time provenance identity is not the traffic-receiving identity" — a real, silent bug
  in any design that keys provenance on one identity and authorises on another. Delete the check
  only if the new layer makes the two the same object *by construction*, and say so where the
  hearsay marker is computed, or the next reader will assume it was merely dropped.

### 9.6 Findings about the code as it stands — not design input

Surfaced by the extraction, true of Ken today, and belonging in the parking lot rather than here:

- **The curation gate is enforcement by reachability, not by a check.** `scopeCurate` is referenced
  nowhere but its own declaration; what actually holds is that no token-reachable path writes
  `state='curated'`. **Nothing tests that absence.** Worse, `PromoteInput.ActorKind` is never
  validated — it always reads `human` only because the sole call site hardcodes the literal.
- **`VoucherTTL` is effectively untested.** No test in the voucher suite advances a clock; delete
  the `expires_at` predicate and the hourly janitor silently widens the window from five minutes to
  an hour with every test still green. Any TTL that survives into the new design needs a
  clock-advancing test, because the current suite cannot tell an enforced TTL from an unenforced one.
- **`ScopeCommFile`'s comment says it is "required by nothing yet".** It has two enforcement sites.
  A reader auditing the scope model from the const block is misled.
- **The console mints station keys under `sess.ActorID` unconditionally**, while the code and
  `STATIONS.md` both claim the console offers an actor picker that marks valid choices. It does not;
  the form's only field is a label.

## 10. Transition

**Ordering, and it is derived from §9.3 rather than from convenience.** Data moves before
credentials retire, or mail is stranded with no error.

1. **Re-point what is welded to a credential.** `endpoint.token_id` first. Nothing else may start
   until this is reversible. §9.3 carries the magnitude: **eleven live endpoints on one token**, and
   an open rotation decision on it that currently means "re-onboard the estate".

   **BOTH welds are in this step, and the controls now exist for both** — `token_id` in 3.19.0,
   `bound_by_station_key_id` in 3.20.0. A bound endpoint whose owner has been re-pointed is still
   welded to its station key; moving one and calling the step done leaves the estate half-moved in
   a way every count reconciles. The terms below were stated for the token half and apply to both.

   **THE TERMS FOR THIS ONE STEP, stated by ken-prod-ops before it is built and accepted here.**
   They apply to no other step, and the reason is specific: *"it is the one migration where my
   ability to tell you it went wrong is itself the thing at risk"* — the endpoint being re-pointed
   is the channel the report would travel on.

   1. **A rollback that has been EXERCISED**, against a fixture holding a bound endpoint — not a
      snapshot that exists. Being wrong here looks like silence, and silence is indistinguishable
      from nobody having written.
   2. **An out-of-band path agreed in advance, and it is Vlad** — the only actor with console access
      to both stations. Written into the plan rather than discovered during the failure.
   3. **The post-state declared as LITERAL VALUES**, the way migration 0017's scope string was: the
      actual expected `endpoint_id` and `token_id` for the specific row. A row that survives
      pointing at the wrong token passes every count and silently orphans an inbox.
   4. **State explicitly whether channel bindings and `station_link_mirror` ride along.** If
      re-pointing disturbs a binding, that is a second migration wearing the first one's clothes.
   5. **No change to a production endpoint without Vlad's approval in chat**, same as a restart. A
      clean pre-flight is not permission.

   And the pre-flight §9.3 names: **refuse by endpoint**, listing the ones holding unacked mail,
   rather than warning in prose about a population.
2. **Make one identity span `/comm` and `/station`.** This is the condition §9.2 names, and it is
   what unlocks the voucher chain. `auth.go:200` discarding the OAuth grant's scope is the blocker:
   until OAuth can express `comm` and `station`, no session can hold one identity across both.

   > **DONE 2026-08-25.** All three authenticators resolve an OAuth bearer through one function,
   > `store.GrantedCapabilities(grant.scope)`. `/mcp` no longer discards `op.Scope`; `/comm/mcp` no
   > longer answers *"comm requires a dedicated `ken_` API token"*; `/station/mcp` accepts an OAuth
   > principal — **with no station**, which is exactly the state `station_request` was written for
   > and the reason the next step is now reachable.
   >
   > **THE PRICE WAS SET IN ADVANCE AND IS PAID IN FULL.** Consolidating removes the control that
   > refused OAuth on the comm surface, and `IDENTITY-CONTROLS.md` calls that *"the highest-value
   > item for a design that intends OAuth as the only mechanism, because THIS CONTROL IS THE ONE
   > THAT SAYS NO TO EXACTLY THAT"* — warning that the removal would be invisible, every surface
   > still working, until the day a connector is compromised and the blast radius turns out to have
   > grown from the knowledge base to the message bus and the vault. Its condition was that the
   > withholding be **"re-expressed as an explicit per-surface capability decision at grant time,
   > not inherited from the fact that three files exist."**
   >
   > So the consent screen now asks, per surface, and the grant records the answer — which is what
   > makes `oauth_grant.scope` load-bearing rather than the cosmetic column its own schema comment
   > called it. **Everything is ticked by default** (no Ken feature is optional or off by default);
   > what changed is that a human *can* withhold, which the register's own complaint said they
   > could not. **No schema change**: OAuth scope is the standard mechanism for exactly this, so
   > `ken:kb`, `ken:comm` and `ken:station` live in the column that already existed.
   >
   > **A grant approved before today carries no `ken:` scope and resolves to the knowledge base
   > alone** — what its human actually agreed to. Widening those silently would have been the
   > invisible removal wearing a migration's clothes.
   >
   > And `curate` is on no path at all. Four mutations pin it: legacy widening, `curate` appended,
   > the consent form ignoring the untick, and the picker rendering unticked.
3. **Then delete the voucher chain**, in one change, with its tests, saying why.

   > **DONE 2026-08-25.** The chain is gone: `IssueBindingVoucher`, `RedeemBindingVoucher`,
   > `SweepBindingVouchers`, the 5-minute TTL, single-use redemption, endpoint pinning, actor
   > matching, hash-at-rest, the hourly janitor sweep, the `station_binding_voucher` tool, the
   > `binding_voucher` argument, and four sentinel errors whose careful wording existed only to say
   > which way a voucher had failed. `comm_bind` reads `X-Ken-Workspace` and binds.
   >
   > **The condition §9.2 set was met before a line was deleted**: *"the voucher exists SOLELY so a
   > station key never crosses to the comm surface as a tool argument."* Step 2 gave one identity
   > both surfaces; step 4 replaced the per-folder key with an id that authorises nothing. There was
   > no key left to keep off that surface.
   >
   > **The endpoint binds with an EMPTY `bound_by_station_key_id`** — no key authorised it, so the
   > at-use severing check skips it and nothing can cut it off through that column. Revocation moved
   > to the credential that OWNS the endpoint, re-pointable since 3.19.0: **one credential, one
   > revocation, instead of two welds on one row.** Putting anything in that column would sever the
   > endpoint on its next call, for a key that never existed; a test asserts it stays empty.
   >
   > **The tests went with it, which is what the step asked for** — `station_voucher_test.go`, ten
   > cases over wrong-endpoint, wrong-actor, single-use, no-nomination, pre-migration NULLs and
   > archived-station redemption. That also settles item (2) of task `t-laLaMYzb`, which recorded
   > that `VoucherTTL` was effectively untested and instructed: *"either write the clock test or
   > delete the mechanism, but do not leave it in this state."* The mechanism is deleted.
   > `TestTheVoucherChainStaysDeleted` fails if any of it returns, because deleting tests is how a
   > removed mechanism quietly comes back.
   >
   > **The TABLE survives** until its migration ships alone under Rule 4. Nothing reads it; the rows
   > are inert. Existing bindings are untouched — eight on the live estate keep their key id and
   > keep being severed by it.

4. **Then the workspace id in config**, and auto-naming for unknown folders (§5). Existing stations
   keep their `station_id` and become workspaces; §8 covers the estate.

   > **DONE 2026-08-25, and it took the station-registration deadlock with it.** `X-Ken-Workspace`
   > on the folder's MCP entry selects the workspace; the credential authorises. A session with no
   > workspace calls `station_me` — the call it is already told to make first — and gets one, named
   > after its folder, working from the next call, **with nothing to approve**. The id comes back in
   > the RESULT, which is the channel that always arrives, together with what to tell the human.
   >
   > **Existing stations needed no migration at all**: the header value IS the `station_id` they
   > already have, so an estate becomes an estate of workspaces by having the header written into
   > each folder's config. §8's concern turned out to be a config edit per folder, not a data move.
   >
   > `station_request` survives for the one case that is still a decision — *"my human wants to name
   > and approve this one"* — and its description now says so instead of claiming to be "the only
   > tool a key with no station may call", which was true and was the deadlock's disguise.
   >
   > **Two failures worth keeping.** A name collision was detected by grepping the error MESSAGE for
   > "unique" while the sentinel's text says "already in use in this space", so a second folder of
   > the same name was refused a workspace — the deadlock rebuilt in miniature by a check reading
   > prose. And the header was first resolved in the auth middleware, which the SDK never hands to a
   > tool handler: it hands the request, with `Extra.Header` on it, exactly as `internal/commserver`
   > has lifted its endpoint credential since it shipped. The mechanism existed; a second one was
   > built that could not work.
5. **`space_id` last**, with its test, as one deliberate change (§9.1) — it blocks nothing, and
   doing it early is churn that makes the remaining plumbing look intentional.

   > **DEFERRED by Vlad, 2026-08-25, after the scope was measured rather than estimated.** Steps
   > 1–4 shipped the same day; this one did not, on purpose.
   >
   > **What it actually touches — 11 tables across BOTH databases, 5 indexes, 160 non-test Go
   > references:**
   >
   > ```
   > ken.db   actor · entry · station · station_link · station_link_denial ·
   >          station_request · comm_room
   >          idx_station_name(space_id,name) UNIQUE · idx_comm_room_name(space_id,name) UNIQUE
   >          idx_station_state(space_id,state) · idx_station_request_pending(space_id,state,created_at)
   > comm.db  channel · endpoint · message_new · pairing_code
   >          idx_endpoint_owner(space_id,actor_id)
   > ```
   >
   > **Why it waited, and none of the reasons is timidity:** it is the only step that delivers no
   > capability — §9.1 says so itself — while being the only one that rewrites schema, in two live
   > databases, under Rule 4. It would have stacked a migration on top of a same-day deletion
   > (step 3) that ken-prod-ops had not yet verified, which is exactly what Rule 3 forbids. And the
   > decision in front of Vlad is whether Ken is worth keeping: steps 1–4 each gave him something
   > he can use, and this one gives him tidier internals he will never see.
   >
   > **When it is taken up, §9.1's instruction is the load-bearing part:** `TestPairingCodeIsSpaceScoped`
   > (`internal/comm/comm_test.go`) fabricates a second space and asserts the denial — the refuter
   > deleted the three-line check and watched CI go red in seconds. So the check IS exercised, in
   > the suite and never in production. **Whoever removes it deletes that test in the same commit,
   > deliberately and reviewably, and says why.** Removing the check first and the test later leaves
   > a live control that nothing exercises, which is the state this whole document exists to avoid.

**Two constraints on every step.** The **MCP freeze** (§11) means a running session holds the old
story and cannot be told otherwise, so each step must work for a session connecting fresh with no
prior state. And **Rule 4** — a release carrying schema change carries nothing else — applies to
steps 1 and 5, which are the two that rewrite rows.

**What this does not schedule.** Nothing above is a commitment to build. `FINISHING.md` is
unaffected, and this document remains design until Vlad says otherwise.

## 11. Open questions

- **The MCP freeze bounds the changeover.** Tool lists and instructions pin at conversation start,
  so during the transition a running session holds the old story and cannot be told otherwise. The
  design must work for a session that connects fresh with no prior state, every time.
- **Encrypted secret sharing between sessions** — Vlad wants it, and it is the same primitive as
  workspace identity rather than a separate feature. The vault deliberately does not encrypt today,
  and its reason is sound: *"A key stored beside the ciphertext protects nobody who can read the
  file."* A key that is **not** beside the ciphertext needs a source, and the only candidate is
  something an authenticated session can derive and an attacker with the file cannot. **Design it
  with identity or build the vault twice.**
- **The 30-minute MCP session timeout** — `TARGET-ARCHITECTURE.md` §9.0, deferred here deliberately.
  Not "what number", but *which workload Ken is for*, now that a human-cadence client exists.
- **Free disk space as a metric** — §9.2. Small, and it interacts with where shared code lives.
