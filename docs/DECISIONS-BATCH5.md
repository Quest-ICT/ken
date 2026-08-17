# Batch 5 — the two decisions that are not code

> **These are Vlad's, not a session's**, and they block COMM v2 slice 7. This document exists so
> they can be settled from evidence rather than from whoever describes them last.
>
> **Assembled 2026-08-17** against `v3.9.0`, by four independent readers with an adversarial
> verifier behind each, plus direct verification of the load-bearing facts. **Where a claim is
> unverified or unverifiable from this repository, it says so** — several of the numbers that
> would settle these questions live only on the production deployment, and they are listed at
> the end as things to measure rather than guessed at here.

---

## Decision 1 — the credential model

**The proposal on the table** (ken-prod-ops): authenticate a bound endpoint with the STATION KEY
and derive the endpoint from it, removing the loose credential file entirely. Vlad's posture —
*every session must hold a station, always* — is what makes it conceivable.

### Why it is being asked now

Every `comm_*` tool takes `endpoint_id` + `endpoint_secret` **as tool arguments** — 11 of the 13
tools. Tool arguments are recorded by the CLIENT, in conversation transcripts, on disk, in the
clear. **Ken cannot fix that by changing what Ken logs**: no redaction, log level or retention
setting on the server reaches a file written by software neither party ships. Only moving the
credential out of the argument position removes it.

The other two MCP surfaces already re-authenticate per call from `req.Extra.Header`.

### THE FACT THAT DECIDES IT

**A station key is per MACHINE. An endpoint is per SESSION.**

- `ken station key --label` is documented as "which machine this key is for", and the CLI tells
  operators to mint a separate key per machine.
- A repeat `comm_register` deliberately mints a NEW endpoint; a station may legitimately have
  several live ones at once (`SELECT endpoint_id FROM endpoint WHERE station_id=? AND
  revoked_at IS NULL ORDER BY id` — plural by construction, because that is the successor model).

So **"derive the endpoint from the station key" is not a function.** Two sessions on one machine
present the same key. And the per-endpoint secret is exactly what stops those two sessions
polling and acking each other's mail — `endpoint.go`'s own comment says so.

The proposal therefore needs a **second, server-derived per-session discriminator**, or it
reintroduces the shared-inbox accident it was never meant to touch.

### Two things that must change in the same commit, or the fix ships a lie

1. **`retired_at` is checked ONLY by `AuthenticateStationKey`.** Neither `/mcp`'s nor
   `/comm/mcp`'s authenticator selects it. That is inert today — station keys carry no comm
   scope, so they never reach `/comm/mcp`. **Under the proposal it becomes live**: the console's
   "Retire this key" would silently NOT sever messaging. This is the same class that already
   left four releases of operator text promising the opposite of what the code did.
2. **`/comm/mcp` never re-derives the caller per call.** `commserver`'s `addTool` wraps metrics
   only; the other two surfaces wrap `withCaller`. Today the per-call endpoint secret pins
   identity, so the connection-time token going stale is harmless. **If the header credential
   becomes the sole identity, that staleness stops being harmless** — the missing wrap is a
   prerequisite, not a cleanup.

### The options

| | what it does | what it costs |
|---|---|---|
| **A. Do nothing, document it** | States plainly that the pair travels as a tool argument and Ken cannot mitigate it | The exposure is permanent. And `docs/STATIONS.md` asserts "a long-lived credential must never be a tool argument" — a principle the sibling surface visibly violates |
| **B. Move the pair to a header** | Minimum change that closes the transcript exposure; endpoint model, claim-once, waiters and seats untouched | Keeps the loose credential file, the loss path and rotation exactly as they are. A session cannot write its own MCP config, so `comm_register` would hand back something a HUMAN pastes — a worse first run than today |
| **C. Station key + endpoint per (station, MCP session id)** | Removes the file AND the exposure; nothing is ever minted into a tool result | The session id is per CONNECTION: a reconnect mints a NEW endpoint. **Endpoint churn replaces credential loss as the failure mode** — old claims held, unacked mail stranded until the idle sweep. Needs the `withCaller` wrap first |
| **D. Station key + ONE endpoint per key** | Simplest derivation, no SDK dependency | Two concurrent sessions on one machine collapse to one reader. Claim-once stops partitioning them. **Deletes S4's "a second session helps without severing the first"** |
| **E. OAuth-enable `/comm/mcp`** | — | **The worst option, stated plainly.** An OAuth grant identifies a CONNECTOR, not a session, so it supplies no session identity and cannot remove the pair at all |

### Recommendation

**B now, C later — and only if a measurement supports C.**

B is small, closes the exposure that was actually measured, breaks nothing at deploy (keep the
arguments optional for a release), and does not require settling the per-session identity
question. C is the better end state *if* endpoint churn on reconnect turns out to be rare — and
that is a production measurement, not a judgement.

What would change this recommendation: if the live deployment turns out to have **no** station
with two concurrent live endpoints, D becomes viable and is much simpler than C.

---

## Decision 2 — how two sessions get a private conversation

**As stated:** today `comm_open_channel` is the only agent-initiable path; rooms need a human at
the console. Whatever replaces it must not widen broadcast reach, because room membership feeds
`to_room:"all"`.

### The constraint, traced rather than assumed

`BroadcastAudience` counts **every party that shares any room with you**:

```sql
SELECT COUNT(DISTINCT other.party_key)
  FROM room_member_mirror mine
  JOIN room_member_mirror other ON other.room_id = mine.room_id
 WHERE mine.party_key = ? AND other.party_key <> ?
```

So the risk is precise: **adding any station to any room I am in makes them reachable by my
`to_room:"all"`.** It applies to ROOMS. It does not apply to channels, which feed neither the
broadcast union nor directory visibility.

### The finding that should decide against the obvious answer

The obvious answer is a `kind='dm'` room — the CHECK constraint already admits the value and the
name-uniqueness index is already partial on `kind='topic'`.

**But `room_member_mirror` is `(room_id, party_key)` with no `kind` column**, and
`ReplaceRoomMirror` takes a bare `map[string][]string`. Nothing between ken.db and the broadcast
union carries the kind. So **an agent-initiable dm room would silently enlarge both parties'
broadcast audience.**

Fixing that costs two migrations (dm-pair uniqueness on ken.db, `kind` on comm.db's mirror), a
console that understands kind, and a filter that every future audience query must remember. **And
it fails OPEN**: a missed filter widens broadcast invisibly, discoverable only by someone
counting an audience.

### What already exists, and was not on the table

The path is built end to end: `CreateStationLinkRequest` (agent-initiable) → `ApproveLinkRequest`
(human, at the console) → `OpenLinkedChannel`, reachable from the comm surface. It is
agent-initiable, human-authorised, and produces a **channel** — so it cannot widen broadcast
reach by construction.

### The options

| | what it does | what it costs |
|---|---|---|
| **P1. Do nothing** | Slice 7 keeps the two-party container a link authorises | Slice 7's stated goal — retiring the channel as the central noun — is not met, and pairing codes survive alongside their replacement |
| **P2. Name-addressed private send, authorised by the existing link** — `comm_send{to_station:"X"}` | Additive field; authorisation is the ACTIVE link a human already approved; **makes `comm_open_channel` redundant, which is slice 7's point** | A new scope prefix beside `ch:` / `r:` / `b:`, a `membersOfScope` arm, and reply/sequence numbering per pair. Does not dissolve unbound endpoints |
| **P3. Auto-create the pair on link APPROVAL** | Nearly free; no new agent verb at all | Does not answer "how does an agent reach a station it has no link to" — the answer stays "ask a human and wait" |
| **P4. Agent-initiable dm ROOM** | Uses the container the schema already anticipates | Two migrations, a kind-aware console, and a broadcast filter that fails open if ever missed |
| **P5. Harden `comm_open_channel` and keep it** | Keeps the mechanism, adds the missing enforcement | Preserves "channel" as a concept slice 7 exists to remove; and making the binding immutable removes `comm_unbind`, which exists for a stated reason |

### Recommendation

**P2, with P3 as its cheaper half.**

P2 is the only option that satisfies the constraint *by construction* rather than by a filter
someone must remember, and it is the one that actually advances slice 7. P3 costs almost nothing
and can ship first: it makes an approved link immediately useful without any new agent verb.

**P4 should be declined**, and the reason recorded: not because dm rooms are wrong, but because
the container that would carry them cannot currently distinguish itself from the container that
feeds broadcast — and the failure mode is silent.

`station_block` is a prerequisite for neither, but it is the targeted deny that would make any
*permissive* default defensible. If P2 is chosen with the link requirement intact, the default is
not permissive and `station_block` stays optional.

---

## What only production can answer

These decide between options and cannot be measured from this repository. They are questions for
ken-prod-ops, not guesses to be made here.

1. **How many endpoints are unbound?** This is the real size of the "every session holds a
   station" posture. If it is zero, options C/D lose their worst break.
2. **Has any station ever had two live endpoints at once?** If never, option D becomes viable and
   is far simpler than C.
3. **Do two sessions ever run on one machine against one station simultaneously?** This is the
   exact case option D collapses.
4. **How often does an MCP transport reconnect within one conversation?** This is the cost of
   option C, expressed as endpoint churn.
5. **Does `station_block` have rows?** Measured as 0 on 2026-08-17; worth re-checking before
   anything is built on the assumption.

---

## Verification honesty

262-claim-scale audits were run for the other batches; this one is smaller and its coverage is
uneven. **102 findings survived refutation and 2 were refuted, but many carry no verifier verdict
at all** — the adversarial pass did not return a matching verdict for every claim, so "survived"
sometimes means "unattacked" rather than "attacked and held". The readers also had no Go
toolchain, so every claim here is from reading source, not from compiling it.

The facts this document leans hardest on — the per-machine/per-session mismatch, the missing
`kind` on the mirror, the `retired_at` asymmetry, the existing link path, and the
`BroadcastAudience` query — were each verified directly rather than taken from a report.
