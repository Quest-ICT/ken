# Ken — inter-session communication (COMM)

> **Status: EXPERIMENTAL, off by default, feature-complete for text messaging.** This document is the
> design contract for a feature targeted at **1.2.0**. Being experimental and optional-and-off-by-default
> places it outside the SemVer contract (see [COMPATIBILITY.md](../COMPATIBILITY.md)) for at least one
> MINOR release.
>
> **Built and verified end to end:** the schema and store layer (`internal/comm`), the six `comm_*`
> tools on their own `/comm/mcp` endpoint (`internal/commserver`) with the `comm` scope and
> dedicated-token enforcement, long-poll wakeups with a shutdown drain, the instruction section, the
> human console at `/comm` (mint a pairing code, see endpoints and channels with pending counts, revoke
> either), English + Spanish translations, and the `ken serve` wiring behind `KEN_COMM_ENABLED` with a
> one-minute sweeper. Two sessions can register, be paired by a human, exchange a message and
> acknowledge it.
>
> **Not built yet:** the settings group (limits are compile-time defaults, not yet operator-tunable at
> runtime), COMM-specific Prometheus metrics, and the curation provenance marker (§7). File exchange
> (§11) remains deferred to a later MINOR.

Ken's knowledge base answers *"has this problem been solved before?"*. COMM answers a different
question that the same deployment is unusually well-placed to serve: **"how do two AI sessions,
possibly on different machines, hand work to each other?"**

A human running several agent sessions at once — one developing, one testing, one monitoring —
constantly moves context between them by hand: copying a file path, pasting a summary, re-explaining
a decision. COMM makes that a first-class transfer between sessions, with the same authenticated,
self-hosted, single-binary properties as the rest of Ken.

**Primary user: the AI session.** As with the `kb_*` tools, a human never consumes COMM directly.
The human's role is to *authorize* connections and to keep a brake within reach.

---

## 1. What COMM is not

Naming the non-goals first, because several of them are the reason the design looks the way it does.

- **Not a chat system.** There is no presence, no typing indicator, no history to scroll. Message
  bodies are deleted once the receiver has processed them.
- **Not a message broker.** No topics, no fan-out, no subscriptions, no durable log. A channel joins
  exactly two endpoints.
- **Not a file server.** File exchange (deferred past the first release, §11) exists to move a
  working document between two of the *same human's* sessions, not to host or distribute anything.
- **Not a second knowledge base.** COMM state is expendable by design: it is not backed up, not
  replicated, and not curated. Losing it costs an in-flight conversation, never knowledge.
- **Not a security boundary against a compromised session.** COMM authenticates *who* is sending and
  gives the human a structural gate on *which sessions may talk at all* — but it cannot make the
  content of a message safe. See §8.

---

## 2. Locked decisions

Recorded the same way as [DESIGN.md](DESIGN.md) §2: what was chosen, the **why**, and the trade-off
accepted.

### C1 — In-process module, separate database *(chosen: embed in the Ken binary)*
COMM ships inside the Ken binary as `internal/comm`, with its **own SQLite file**
(`data/comm/comm.db`), its own single-writer pool (the D6 discipline), and its own additive
migrations.

- **Why embed:** the operator story (D1 — one binary, one unit, one installer, a 1 GB VPS) is the
  thing a second service would damage most, and the one genuinely expensive thing to duplicate is
  token authentication. A separate daemon buys isolation the module can approximate, and costs a
  second unit, a second port, and a second upgrade path for every operator.
- **Why a separate database:** message traffic is high-churn and expendable; the KB is low-churn and
  durable. Sharing one file would put ephemeral WAL churn into the backed-up, replicated database
  and would put COMM's write rate behind the KB's single writer. Separate files keep the two
  lifetimes, and the two backup postures, honestly distinct.
- **Trade-off accepted:** the crash domain and the disk are still shared, and pretending otherwise
  would be the failure mode. §5 turns "COMM cannot take the KB down" into enforced rules rather than
  an aspiration.
- **Rejected alternative — a separate `ken-comm` binary.** Genuinely attractive: `internal/` packages
  are importable within the module, SQLite WAL permits multi-process readers (so it could validate
  `ken_` tokens read-only against `ken.db` with no new auth infrastructure), and it would get its own
  crash domain, its own resource limits, and its own release cadence — dissolving three of §5's rules
  *structurally* instead of by discipline. It loses on the operator story, which is D1's whole point.
  **Tripwires that would revisit this:** a COMM defect takes the KB down in production; COMM needs a
  release the KB does not; or the §5 resource rules prove unenforceable in-process. `internal/comm`
  keeps its boundary clean enough (own store, own migrations, `Deps` injection, no reach into KB
  internals beyond auth) that extraction stays a mechanical refactor.

### C2 — Off by default *(chosen: opt-in)*
COMM is disabled unless the operator turns it on. Its tools are **not registered** and its
instruction section is **not appended** when off.

- **Why:** a default Ken install must remain exactly the curated knowledge base the README promises —
  no second operating loop in every agent's connect-time instructions, no message-bus surface on a
  product whose value is curation. It also makes "experimental" *mechanical* rather than
  documentary: COMPATIBILITY.md already excludes anything optional-and-off-by-default, so the
  contract is genuinely free to move during the shakedown period.
- **Trade-off accepted:** one more thing to switch on, and a feature that fewer people will discover.

### C3 — Pull with long-poll *(chosen: pull)*
Sessions retrieve messages by calling `comm_poll`, which may park up to a bounded interval before
returning.

- **Why not push:** the MCP transport genuinely supports server-initiated messages in Ken's
  configuration — but an agent harness only surfaces the results of tool calls *the model made*. An
  unsolicited server event has no path to the model's attention. Push would be correct plumbing that
  never arrives.
- **Why long-poll rather than fast polling:** it is the difference between one request per 25 seconds
  and a request every 2 seconds, and Ken's rate limiter makes that difference dangerous rather than
  merely wasteful (§5).
- **Trade-off accepted, and it must be documented honestly:** **an idle session receives nothing.**
  Delivery happens only when the receiver chooses to poll. COMM offers no presence and promises no
  latency bound.

### C4 — Full-duplex channels, correlation per message *(chosen: full-duplex)*
A channel joins two endpoints and carries traffic in both directions at once. One endpoint per
session — not a send point and a receive point. Request/response is a property of a *message*
(`requires_response`, `reply_to`), never a state of the channel.

- **Why not half-duplex turn-taking:** channel-level turn state is a distributed state machine, and
  it fails exactly where it matters — a session that dies mid-turn leaves the channel wedged, with no
  clean way for the peer or the operator to reason about whose turn it was. Correlation per message
  has no such state to corrupt: an abandoned request simply ages out and is reported to its sender.
- **Trade-off accepted:** without turn-taking there is no natural flow control, so backpressure must
  be explicit (§4.4).

### C5 — Content is ephemeral; metadata is not *(chosen: split the two lifetimes)*
Acknowledging a message deletes its **body**. A slim **metadata row** — id, sequence, sender, flags,
reply linkage, timestamps, size, content hash — survives until the exchange is complete or ages out.

- **Why:** deleting the whole record on ack (the original instinct, and the right instinct about
  *storage*) is mutually exclusive with request/response. With two requests outstanding and both
  acknowledged, a later reply refers to a record that no longer exists: the server cannot validate
  it, cannot route it, and cannot tell the sender which request was answered. Splitting the lifetimes
  keeps the storage win — bodies are what have size — while leaving the state machine intact.
- **Second reason:** a service that relays instructions between machines, reachable on a public IP,
  must leave the operator *something* to look at after an incident. Metadata-only retention is that,
  without keeping conversation content around by default.
- **Trade-off accepted:** COMM keeps a little more state than "nothing after ack", and the retention
  window is one more thing to tune.

### C6 — Acknowledge means *processed*, not *received* *(chosen: at-least-once)*
Messages are redelivered on every poll until acknowledged. `comm_ack` is the only state advance.

- **Why:** nothing in the transport makes a lost response distinguishable from a request that never
  arrived. A tool result lost after the server committed is the *ordinary* failure here — a harness
  timeout, a connection reset, a restart inside the shutdown grace — so exactly-once delivery is not
  on offer. At-least-once with idempotent operations is, and it also matches what the feature
  actually wants: a message that was delivered but never acted upon (because the receiving turn was
  truncated) *should* come back.
- **Trade-off accepted:** receivers must tolerate seeing a message twice. The envelope carries a
  delivery count so they can tell.

### C7 — Establishment is human-authorized *(chosen: pairing code, both sides join)*
An agent cannot conjure a channel. A human mints a short-lived **pairing code** in the web UI and
gives it to both sessions; each calls `comm_join` with it, and the channel exists once both have.

- **Why:** this is the one place COMM can borrow the property that makes the rest of Ken trustworthy.
  The curation gate works because a capability is *withheld* (no tool requires `curate`), not because
  the model is asked nicely. Channel establishment is where the same trick is available: agents talk
  freely inside a channel a human deliberately created, and can never create one themselves.
- **Second reason:** both-sides-join is also what keeps the multi-user future additive (§10) — a
  unilateral "A opens a channel to B" would have to *tighten* into an accept flow later, which is a
  breaking change.
- **Trade-off accepted:** a human step before two sessions can talk. This is a feature, not friction:
  the human already knows which two sessions they intend to connect.

### C8 — Text is atomic; bytes never travel as tokens *(chosen: no tool-call chunking)*
A message body is a single string with a hard ceiling. There is **no** multi-part message assembly in
the tool surface.

- **Why:** the client is a language model, and tool-call arguments are *generated token by token*.
  Chunked binary transfer through tool calls looks like a protocol problem and is actually an
  economics problem: base64 expands a payload by 4/3 and tokenizes poorly (roughly 3–4 characters per
  token), so **one mebibyte costs on the order of 350–470 thousand output tokens** — produced at model
  speed and model cost, with a corruption rate the checksum can only detect *after* the tokens are
  spent. A 16 MiB attachment is several million. Even a 64 KiB text message is a five-figure token
  count. Multi-part framing would make the expensive thing look routine.
- **What replaces it** (§11, deferred): same-host filesystem handoff first, and for genuinely large
  cross-host payloads, a one-time expiring HTTP transfer the agent drives with a shell tool — because
  HTTP already does resumable chunked transfer correctly and the model never has to emit the bytes.
- **Trade-off accepted:** a session with no shell and no shared filesystem can only exchange small
  inline payloads. That is the honest capability, and the tool description says so.

### C9 — Prove a shared filesystem; never assert it *(chosen: rendezvous, not fingerprint)*
Two sessions establish that they are on the same machine by **demonstrating a shared readable
directory**, not by comparing a self-reported machine identifier.

- **Why the fingerprint was rejected:** for two independent sessions to compute an equal value, the
  input must be stable and shared per machine — which makes it a static string any peer learns during
  the comparison and can replay. It is also wrong in both directions: cloned VM images and container
  templates make *different* hosts compare equal, while a network or bind mount makes *the same*
  filesystem compare unequal. Two sessions genuinely on one host may run as different OS users and
  still not be able to read each other's files, so equality never proved readability in the first
  place. Remote-hosted sessions cannot compute one at all, so "absent" must never match "absent".
- **Why it matters more than an ordinary design flaw:** the fingerprint would gate *reading a path*.
  A forged one turns "read this file and follow up" into a remote-driven read of an arbitrary local
  file — with the contents relayed back through the channel, bypassing the size cap, the checksum,
  and every server-side counter at once.
- **What replaces it:** the sender writes a nonce into a dedicated exchange directory and sends the
  basename plus a hash; the receiver reads it and echoes the nonce back. Only relative names under
  that root ever travel, and Ken validates the string server-side (rejecting absolute paths, `..`,
  `~`, and control characters) so a convention becomes a checked contract. Unspoofable, needs no
  fingerprint, no salt distribution, and no story about hostname privacy.
- **Trade-off accepted:** one extra round-trip before the fast path is used. A machine-derived *hint*
  may still be reported to skip a doomed attempt, but it can never authorize one.

---

## 3. Model

**Endpoint** — one session's communication point. Created by `comm_register`, which returns an opaque
server-generated `endpoint_id` and a one-time `endpoint_secret`. Subsequent calls that act as this
endpoint present the secret.

- **Why a secret and not just the token:** the operating convention is one Ken token per *machine*,
  so every session on a box shares a token. Without a per-endpoint secret, two sessions could poll
  and acknowledge each other's messages — most likely by accident, when both register with the same
  friendly label. Sender identity is therefore honest about its own strength: **token-authenticated
  and endpoint-scoped** — trustworthy across machines and users, advisory between sessions that share
  a token.
- Display labels are non-unique decoration. **Routing is always by `endpoint_id`.** A human-chosen
  name is never an address, or the first release ships a global namespace one session can squat.

**Channel** — joins exactly two distinct endpoints, created by the pairing flow in C7. Carries an
owner identity (`space_id` plus the authorizing human actor) from day 1; a channel whose two
endpoints do not share an owner is rejected.

**Message** — one atomic body plus an envelope: server-assigned per-channel sequence, sender
endpoint, `requires_response`, optional `reply_to`, delivery count, timestamps, size, content hash.

---

## 4. Delivery semantics

### 4.1 Lifecycle

```
queued ──poll──▶ delivered ──ack──▶ processed ──▶ (body deleted; metadata retained)
   │                  │                  │
   └── TTL ───────────┴──────────────────┴──▶ expired  (sender is notified)
```

Every transition is stamped by the **server** clock. Clients supply *relative* lifetimes
(`ttl_seconds`), never absolute timestamps, so clock skew between agent machines cannot silently
shorten or extend anything.

### 4.2 Idempotency

Required on every operation, because §C6 makes redelivery normal:

- **`comm_send`** takes a client-supplied idempotency key, unique per sender and channel within a
  dedup window. A repeat returns the original message id rather than sending a second message.
- **`comm_poll`** is a pure read. It returns *all* unacknowledged messages for the endpoint across
  all its channels, with a delivery count. Being polled is an informational timestamp, never a state
  that hides a message from the next poll.
- **`comm_ack`** succeeds on an already-acknowledged or unknown id.

Ordering is promised **per channel and direction** and nowhere else: polls return strictly ascending
sequence numbers, and a cumulative acknowledge (`ack_up_to`) is available because it collapses
chatter and is idempotent by construction.

### 4.3 Request/response

A message with `requires_response` carries a server-computed reply deadline. Its body survives
acknowledgement until it is answered or the deadline passes — a responder that crashed and recovered
plausibly needs to re-read what it owes. When a deadline passes unanswered, the server delivers a
synthetic status message to the **requester** through the ordinary poll path, so a dead peer surfaces
as a normal arrival rather than an indefinite wait. Full-duplex removes turn deadlock; reply
deadlines are what keep the *requester* from hanging in its place.

### 4.4 Backpressure

Unacknowledged depth per channel is capped. `comm_send` past the cap returns a typed backpressure
error, and the instruction text tells the model to stop and wait rather than retry in a loop. Without
turn-taking (C4) and with auto-processing enabled on both ends, two sessions can otherwise enter a
reply loop that grows the database without bound.

### 4.5 What the tool descriptions must state plainly

- Messages arrive only when the receiver polls; an idle session receives nothing.
- A message may be delivered more than once; check the delivery count.
- Acknowledge after acting, not on receipt.
- A truncated poll is a normal empty result, not an error.

---

## 5. Isolation — the rules that make C1 honest

A separate database file isolates the KB's WAL and its backup stream. It does **not** isolate the
disk, the process, or the readiness signal. These are enforced rules, each with a failure it prevents.

1. **Disk budget.** A global byte cap on COMM storage, plus a free-space floor checked *before*
   accepting anything. Without both, ephemeral traffic can fill the volume and start failing KB
   writes — search, save, promotion, and the nightly snapshot all die because of chat. This is the
   coupling C1 must not have.
2. **Quotas fail closed.** COMM quotas are enforced in SQL inside the insert transaction, never by
   keying the shared rate-limiter bucket. That bucket deliberately *fails open* when full, which is
   right for IP and token keys — an attacker cannot mint those cheaply — and wrong for identifiers a
   caller can create in a loop. A fail-open quota is a disk-full outage; a refused message is a retry.
3. **No panic escapes.** Every COMM goroutine is wrapped in recover-and-log, and the subsystem
   degrades to disabled rather than crashing. Shipping the newest, highest-churn code in-process with
   the durable product otherwise inverts the risk profile: the component most likely to have a bug
   becomes the one that can take down the component users trust with their knowledge.
4. **COMM is not in `/healthz`.** The health checker marks the *whole service* DOWN on any component
   failure and the endpoint returns 503, so registering COMM there would let a wedged sweeper pull a
   perfectly healthy knowledge base out of load-balancer rotation — and, with `Restart=on-failure`,
   potentially loop it. COMM reports through metrics only. **The rule: COMM may fail; the KB stays
   UP.**
5. **Its own rate accounting.** COMM gets its own bucket, and polls rejected for rate must not feed
   the per-IP strike counter. The default operating convention is one token per machine, so every
   session on a box shares one bucket that *also* fronts all `kb_*` traffic; a naive poll loop could
   otherwise starve a machine's knowledge-base access and then trip the auto-block, locking that
   machine out entirely. Long-poll is the primary defense — a parked call costs one unit regardless
   of how long it waits — and results carry a server-advertised minimum interval.
6. **Drain on shutdown.** The graceful-shutdown budget is shorter than a long poll, so parked waiters
   are woken with an empty success before shutdown begins. Otherwise every deploy produces a burst of
   agent-visible transport errors.
7. **Bounded waiters.** Concurrent parked polls are capped per endpoint and globally; past the cap a
   poll returns immediately and empty. The HTTP server deliberately sets no write timeout (the MCP
   transport holds long-lived responses) and the unit file sets no task or memory limits, so nothing
   outside the process bounds this. Adding `TasksMax`, `LimitNOFILE` and `MemoryMax` to the unit
   belongs in the same change.
8. **Backups exclude it by construction — keep it that way.** Litestream replicates one explicitly
   named path, and the snapshot script copies only the KB database, so `data/comm/` is already
   outside both tiers. Two traps to avoid: the snapshot retention prune matches `ken-*.db*` in the
   backup directory, so nothing COMM-related may ever be written there under that shape; and
   [BACKUP.md](BACKUP.md)'s "your knowledge lives in one SQLite file" becomes incomplete the day this
   ships and must be corrected in the same change to say COMM state is deliberately unreplicated.

---

## 6. Tool surface (sketch)

Six tools, all `comm_*`, all requiring the `comm` scope, served from `/comm/mcp`
(`internal/commserver`). Every tool except `comm_register` carries `endpoint_id` + `endpoint_secret`:
the bearer token identifies a *machine*, so the endpoint pair is what identifies the *session* within
it.

Enable with `KEN_COMM_ENABLED=1`; the message database defaults to `<db dir>/comm/comm.db`
(`KEN_COMM_DB`). Mint a **dedicated** token — a token may hold comm scopes or knowledge-base scopes,
never both, enforced at mint time:

```
ken token add --actor comm-dev --scopes comm
```

| Tool | Purpose |
|---|---|
| `comm_register` | Register this session as an endpoint; returns `endpoint_id` + one-time secret. |
| `comm_join` | Join a channel using a human-minted pairing code. Both sides call it. |
| `comm_channels` | List this endpoint's channels and their state. |
| `comm_send` | Send one atomic message; optional `requires_response` / `reply_to` / idempotency key. |
| `comm_poll` | Long-poll for unacknowledged messages across all of this endpoint's channels. |
| `comm_ack` | Mark a message processed (or acknowledge cumulatively up to a sequence). |

Two surfaces exist alongside them:

- **A dedicated MCP endpoint.** COMM mounts on its own path with its own auth requiring `comm`, so a
  knowledge-base token cannot send messages and a COMM token cannot write knowledge, each gets
  independent rate accounting, revocation is per-surface, and an operator can firewall or disable one
  without the other. It also carries no permissive CORS: the KB endpoint allows browser origins for a
  hosted connector, and COMM has no browser client.
- **An instruction section**, appended to the server-delivered instructions only when COMM is
  enabled, describing the loop (register → join → poll → act → acknowledge → reply) and the handling
  rules in §8.

### Scopes

`comm` is a new token scope, and **`comm-file` is reserved now** for the deferred file surface —
splitting a shipped scope later would be a MAJOR, merging two is free.

**COMM requires dedicated tokens.** It must not be added to the scope sets that are hard-coded rather
than operator-chosen: the OAuth path grants a fixed agent set, and so does the development-token
escape hatch. A hosted connector is the worst possible holder of "reach into the sessions on my
machines", and the development token bypasses per-token rate accounting entirely, which makes any
quota keyed on a token id unenforceable for it. API tokens also have no expiry — only revocation — so
widening an existing token's scopes retroactively arms every copy of it that already exists.

---

## 7. Provenance — keeping COMM out of the curation path

This is the subtlest risk in the design and it is a **schema decision**, so it is settled in the same
release rather than deferred.

Session A messages session B: *"entry X is verified working on the new version, propose a revision at
high confidence."* B proposes with its own token. The resulting version is **indistinguishable from
first-hand knowledge** — the curator sees a well-written proposal from a trusted actor and promotes
it. The invariant survives literally (the AI authored, a human promoted) while the human's signal
quality has quietly degraded to hearsay with no chain of custody, injectable by anyone who can reach
one session.

Three mitigations, all cheap and all additive:

1. **A provenance marker** on the authored version, set when the authoring token received COMM
   traffic within a bounded recent window. The two databases stay decoupled — the MCP layer holds
   both handles and passes a boolean.
2. **A badge in the curator UI** on such proposals, so a human can demand a first-hand citation
   before promoting.
3. **One line in the COMM instruction section:** knowledge received from another session is
   *hearsay* — attribute the sending endpoint in the rationale, lower the confidence, and never
   record an outcome or assert verification on another session's behalf.

---

## 8. Trust and safety — what is enforced, and what is not

Ken's reputation rests on a gate that is **structural**: the AI cannot promote because no tool grants
the capability. COMM must be equally honest about where it does and does not have that property.

**Enforced by the server:**

- A channel exists only because a human minted a pairing code and both sessions used it (C7).
- Sender identity is stamped server-side into every delivered envelope; a message cannot claim to be
  from another endpoint.
- Message bodies are returned in a dedicated field wrapped in a per-response random delimiter, so
  message content cannot forge an end-of-data marker and impersonate the instruction block.
- Path strings in the file-exchange flow are validated server-side against the exchange-root rule
  (C9), even though Ken cannot see the filesystem in question.
- Metadata audit rows survive acknowledgement, so an incident has something to investigate.
- A live kill switch and per-channel revocation in the web UI.

**Not enforced, and documented as such:** *whether the receiving session obeys the instruction to
confirm with its human before acting on a message.* That mandate lives in the server-delivered
instruction text — a single string delivered once at initialization. Ken cannot verify a client
surfaced it to the model, cannot scope it per tool, and receives no signal that a human confirmed
anything. It is worth stating and it will change behavior in the common case; it is **not** a control.

The design's answer is not to pretend otherwise but to put the structural gate one level up: the
human decides *which sessions may talk at all*, and after that, content handling is the receiving
harness's responsibility. **The documentation must never imply the instruction text is a security
boundary.**

Auto-processing follows from this. A human may pre-authorize their own session to act on messages
from a given channel without asking each time — that authorization is given by the human to their own
agent, client-side, where it belongs. Ken neither grants nor sees it; Ken's contribution is
authenticated sender identity, so a client-side policy has something trustworthy to hang on.

---

## 9. Operator surface

Minimal, but not absent: a security model whose enforcement point is the human needs the human to
have an instrument panel and a brake.

**Settings** (a new group, live where possible): enable/disable · maximum message size · poll wait
ceiling · message and metadata TTLs · reply-deadline default · per-channel unacknowledged cap · global
storage budget · per-owner quotas.

**A Comm page** in the web UI: mint a pairing code; list endpoints (label, owner, last seen) and
channels (state, counters, queue depth); revoke a channel or an endpoint; disable the subsystem live.

**Metrics** (never health, per §5.4): messages sent/delivered/acknowledged/expired, queue depth,
parked waiters, storage bytes, sweeper lag.

**A sweeper** on a cadence of a minute or less — not folded into the hourly housekeeping loop. At a
sustained send rate a single sender can write hundreds of megabytes before an hourly sweep first
runs, which means a TTL is not a quota and must not be mistaken for one.

---

## 10. Growing to multiple humans

The seams are built now; the machinery is not (matching [DESIGN.md](DESIGN.md) §7's stance).

**Now:** every endpoint row carries `token_id`, `actor_id` **and `space_id`**. Ownership is keyed on
`space_id` plus the authorizing human — deliberately **not** on the actor alone, because actors are
resolved by display name and therefore collapse across machines and humans: every token minted with
the same actor name is *one* actor row, so an actor-keyed check would reject nothing it was meant to
reject. Establishment is two-sided from day 1; every listing is scoped to the owner from day 1; a
quota row exists from day 1 with an unlimited value.

**Deferred:** invitation flows between different humans, per-user quotas with real values, cross-space
policy. All additive on top of the above — which is the entire point of settling ownership keying and
two-sided establishment before the tools freeze, since both would otherwise be MAJOR surgery.

---

## 11. Deferred to a later MINOR — file exchange

The first release is **text-only**. File exchange carries most of the design's risk — disk exhaustion,
orphan sweeping, global budgets, checksum bookkeeping, and the whole same-host protocol — and it is
cleanly severable, so it lands once channel semantics have survived their experimental period.

The shape it will take, in preference order:

1. **Same-host filesystem handoff** — the primary path for anything large, via the C9 rendezvous.
   Costs zero model tokens.
2. **Small inline payloads** — a single message, with the token cost documented in the tool
   description so the expense is visible before it is incurred.
3. **A one-time expiring HTTP transfer** for large cross-host payloads, minted by a tool and driven by
   the agent with a shell tool. HTTP does resumable chunked transfer correctly; the model never emits
   the bytes.

When it lands it needs: the `comm-file` scope (reserved now), off by default independently of COMM
itself, a global byte budget and free-space precondition, per-owner concurrent-upload caps, idempotent
chunk writes keyed by offset, a janitor sweeping abandoned uploads, checksum verification at finalize,
`0700`/`0600` permissions with no execute bit, and no HTTP path that serves an attachment without
authentication. Files live under `data/comm/` so an operator can exclude or separately mount one path.

---

## 12. Open questions

- **Multi-instance.** Wakeups between a parked poll and an incoming send are in-process, so they do
  not cross a load balancer. Correctness survives (a poll re-reads the database before returning) but
  latency silently degrades to the poll interval. COMM assumes a single instance; if that ever
  changes, the escape hatch is a short database-poll tick rather than a redesign.
- **Poll-wait ceiling.** The default must sit comfortably below both harness tool timeouts and typical
  reverse-proxy read timeouts, and the operator-tunable maximum must be clamped server-side — a wait
  that ties the client timeout converts a successful empty poll into a tool *error*, which models
  handle badly. Proxy guidance belongs in this document next to the existing no-write-timeout
  rationale.
- **Reaching an idle session.** COMM cannot wake a session that is not polling. Whether Ken should
  ship a CLI verb usable from a harness hook or background loop — surfacing arrivals into a session
  that would otherwise never look — is a real question and deliberately out of scope for 1.2.
