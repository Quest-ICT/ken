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

### 9.3 Two migration landmines — move the data *before* retiring the credential

- **`endpoint.token_id` welds every live endpoint to the token that registered it, and there is no
  re-pointing path in the code.** Any transition that retires the current COMM tokens strands their
  endpoints and every queued message on them — permanently, and with no error. Re-point the rows or
  re-file the mail onto workspace parties **first**.
- **`api_token.station_id IS NULL` is a state, not data** — a key in it may call exactly one tool.
  §5 deletes the state deliberately. What must not go with it is the human-typed *name*: §5 keeps
  naming by moving the moment to the link-approval screen, which is the one place a bad name is in
  front of the human anyway.

### 9.4 Controls that survive with their key moved

The 46 in this class share a shape: they are keyed on a credential, and the replacement collapses
many credentials into one identity. Three cost something real, and the design should say so rather
than discover it:

- **Revocation granularity collapses.** Today the unit is a key per machine — *"revoke this
  laptop"*. One OAuth grant per instance makes the unit the whole instance. That is a capability
  loss hiding inside a simplification, and single user does not shrink the machine count.
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

1. **Re-point what is welded to a credential.** `endpoint.token_id` first — re-point the rows, or
   re-file queued mail onto workspace parties. Nothing else may start until this is reversible.
2. **Make one identity span `/comm` and `/station`.** This is the condition §9.2 names, and it is
   what unlocks the voucher chain. `auth.go:200` discarding the OAuth grant's scope is the blocker:
   until OAuth can express `comm` and `station`, no session can hold one identity across both.
3. **Then delete the voucher chain**, in one change, with its tests, saying why.
4. **Then the workspace id in config**, and auto-naming for unknown folders (§5). Existing stations
   keep their `station_id` and become workspaces; §8 covers the estate.
5. **`space_id` last**, with its test, as one deliberate change (§9.1) — it blocks nothing, and
   doing it early is churn that makes the remaining plumbing look intentional.

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
