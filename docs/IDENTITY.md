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

**What must NOT be deleted with it is recorded in §9** — the controls that survive single-user
because they protect the human from their own sessions' mistakes, protect Ken from the world outside
the instance, or are load-bearing for the curation gate.

## 8. Migrating the existing estate

Decided: **migrate.** `quest-infra` alone holds 47 tasks; there are live channels, links and a
vault. Each existing station becomes a workspace, keeping its `station_id` — which is already the
stable identifier everything else is keyed to — and its id is written into the corresponding
folder's MCP config. Notebooks, tasks, vaults, channels and links follow unchanged because they
already reference the station rather than any credential.

*The detailed transition, including what is keyed to a token or endpoint id and therefore needs
re-pointing, is §10 and depends on the extraction in §9.*

## 9. What we must not lose *(pending)*

*Being extracted now: every credential control in the current system, what concrete failure it
prevents, its stated reason quoted from the code, and whether single-user genuinely dissolves it —
with every "dissolves" verdict adversarially refuted, because removing a real control on a guess is
the expensive mistake available here.*

## 10. Transition *(pending §9)*

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
