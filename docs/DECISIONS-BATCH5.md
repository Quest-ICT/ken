# Batch 5 — the two decisions that are not code

> **DECIDED 2026-08-18.** Both were Vlad's and both are now made; the reasoning below is kept as
> the record of what they were decided ON. They blocked COMM v2 slice 7, which is now unblocked.
>
> | | decision |
> |---|---|
> | **Credential model** | **B now, D later.** Move the endpoint pair out of tool arguments into a request header immediately. Adopt the station-key model afterwards, once the unbound endpoints are resolved. |
> | **The six unbound endpoints** | **Migrate them onto stations first.** "Every session holds a station, always" becomes an enforced fact rather than a posture before anything depends on it. |
> | **Private conversation** | **P3, then P2. dm rooms DECLINED.** Approving a link creates the pair conversation; then `comm_send{to_station}` authorised by that link. The dm-room container is refused because a missed audience filter widens broadcast invisibly. |
>
> **The order is not a preference — each step removes a blocker for the next.** The endpoint
> migration gates D; D does not gate B; P3 does not gate P2 but makes it cheaper to justify.
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

**B now. Then D — but not until two things are true that are not true today.**

*Updated 2026-08-17, after production answered.* The measurements moved this, and in both
directions at once.

**D became available.** No station has ever had two live endpoints — not once, live or revoked,
across every row that has ever existed. So the simplest derivation (one endpoint per station key,
no session-id dependency, no SDK coupling) is not blocked by anything in the data. Carry
ken-prod-ops' caveat with it: *"never used" and "unnecessary" are not the same finding, and from
inside the repository they look identical.* Choosing D deletes something untested, not something
proven useless.

**C became less attractive.** The teardown baseline that was gathered to measure the keepalive fix
is also an endpoint-churn measurement: ~31 comm teardowns per day, each of which would mint a new
endpoint under C — and since the idle sweep no longer collects an endpoint seating a channel,
those accumulate rather than age out. **The keepalive fix is therefore a prerequisite for C, not
a neighbour of it**, and even then the clustering is only about a quarter of the teardowns, so the
question is what the churn FLOOR is, not whether it reaches zero.

**And the worst break is real, not hypothetical.** Six of thirteen live endpoints are unbound, and
two were active the same day: one holds **seven channel seats**. So "every session holds a
station, always" is a posture the deployment does not yet satisfy, and any station-key option
strands those six until they are migrated onto stations.

**Therefore: B now** — it closes the measured exposure, needs none of the above settled, and
breaks nothing. **D after** the six unbound endpoints have somewhere to go and the keepalive fix
has been verified. The order is not a preference; each step removes a blocker for the next.

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

1. **How many endpoints are unbound?** **ANSWERED: six of thirteen, and two are load-bearing
   TODAY.** `ep 6` holds **seven channel seats** and was active at 19:42Z; `ep 13` holds three.
   Three of the six are genuinely idle. **So the worst break does NOT disappear** — "every
   session holds a station" would strand six endpoints, and one of them takes seven channels.
2. **Has any station ever had two live endpoints at once?** **ANSWERED: never, not once, live or
   revoked** — verified across every endpoint row that has ever existed. **So option D is
   available on the evidence.** ken-prod-ops attach a caveat that belongs with it: *"never used"
   and "unnecessary" are not the same finding, and from inside the repository they look
   identical.* S4's multi-reader has never been exercised on this deployment — a fact about its
   shape, not a verdict on the feature. Choosing D deletes something untested, not something
   proven useless.
3. **Do two sessions ever run on one machine against one station simultaneously?**
   **UNANSWERABLE, WHICH IS NOT THE SAME AS NO.** The question is about SESSIONS and Ken records
   ENDPOINTS: `last_seen_at` moves and does not record overlap, so two sessions sharing one
   endpoint pair are indistinguishable from one session polling twice. Claims name the endpoint,
   not the session. This needs an instrument that does not exist, not a query.
4. **How often does an MCP transport reconnect within one conversation?** This is the cost of
   option C, expressed as endpoint churn.
5. **Does `station_block` have rows?** **Answered 2026-08-17: zero rows.** *(Correction: the
   figure previously recorded here credited ken-prod-ops with a row count they had not taken —
   what they measured on the 17th was zero CALLERS, by grep. Both are zero, so nothing downstream
   changes, but only one of the two claims was theirs.)*

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
