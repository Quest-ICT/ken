# Ken — inter-session communication (COMM)

> **Status: supported, CORE and on by default; feature-complete for text messaging and file exchange.**
> Shipped in **1.2.0**, opt-in until the reversal recorded in C2. This document is its contract. Its
> interface still sits outside the byte-level SemVer contract (see
> [COMPATIBILITY.md](../COMPATIBILITY.md)) and evolves **additively** — no longer because it is
> optional, but because the surface is **mid-redesign** and COMM v2 retires the channel itself (C2).
> Supported, but not preview-frozen.
>
> **Built and verified end to end:** the schema and store layer (`internal/comm`), the twelve `comm_*`
> tools on their own `/comm/mcp` endpoint (`internal/commserver`) with the `comm` scope and
> dedicated-token enforcement, long-poll wakeups with a shutdown drain, the instruction section, the
> human console at `/comm` (mint a pairing code, see endpoints and channels with pending counts, revoke
> either), English, Spanish and French translations, and the `ken serve` wiring (core and
> unconditional; the `KEN_COMM_ENABLED` opt-out was removed in 2.0.0) with a one-minute sweeper. Two sessions can register, be paired by a human, exchange a message and
> acknowledge it.
>
> Also built: the live settings group (every limit is operator-tunable without a restart), the
> `ken_comm_*` Prometheus gauges, the curation provenance marker (§7), and **file exchange** (§11) —
> same-host rendezvous offers, and a one-time-grant HTTP relay for cross-host transfers, gated behind
> its own live setting (`comm_files_enabled`, default ON since 2026-08-24) and the `comm-file` scope. Verified end to
> end against the running binary. The whole COMM design is now implemented.

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
  bodies are deleted on a retention window after the message settles — not kept for browsing.
- **Not a message broker.** No topics, no fan-out, no subscriptions, no durable log. A channel joins
  exactly two endpoints. When stations are in use those endpoints may each belong to a *station*, and
  the station's other readers can claim its mail — but delivery is still **claim-once**, never
  fan-out, precisely so this line stays true.
- **Not a file server.** File exchange (§11) exists to move a working document between two of the
  *same human's* sessions, not to host or distribute anything: bytes are deleted once delivered or
  expired, grants are single-use, and nothing is ever served without authentication.
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

### C2 — ~~Off by default~~ → **core and unconditional** *(reversed twice: 1.6.0 inverted the switch, 2.0.0 removed it)*

**What was decided (1.2.0):** COMM is disabled unless the operator sets `KEN_COMM_ENABLED=1`. Its
tools are **not registered** and its instruction section is **not appended** when off.

- **The why, as recorded then:** a default Ken install must remain exactly the curated knowledge base
  the README promises — no second operating loop in every agent's connect-time instructions, no
  message-bus surface on a product whose value is curation. It also made the contract exclusion
  *mechanical* rather than documentary: COMPATIBILITY.md already excluded anything
  optional-and-off-by-default, so the interface was genuinely free to evolve additively.

> **SUPERSEDED, 2.0.0 — read this block as history.** The variables described below were REMOVED
> entirely: `KEN_COMM_ENABLED` and `KEN_STATION_ENABLED` do nothing today, and setting either has
> no effect. The reasoning is kept because the *runtime* "COMM off" state it describes is still
> real (an unopenable `comm.db` still degrades COMM rather than taking the knowledge base down) —
> only the operator-facing switch is gone. See docs/UPGRADING.md for the release that removed it.

**What changed (1.7.0):** COMM and [stations](STATIONS.md) became **core** — on by default. The
variable was *kept, inverted*, as an opt-*out*: `KEN_COMM_ENABLED=0` turned COMM off, and an
unrecognised value left it **on**, because a typo must never silently disable core functionality.
`KEN_STATION_ENABLED` read the same way, and the two were **independent** — turning COMM off left
stations fully working, because the station notebook and task list are valuable to a solo session
with no peers (STATIONS.md S2).

**What changed again (2.0.0), and this is the state today:** both variables were REMOVED. Nothing
reads them; setting either does nothing. The independence above is still true and is now structural
rather than configured — stations work whether or not COMM has a peer. What survives is the
*runtime* degradation: an unopenable `comm.db` still disables COMM rather than taking the durable
knowledge base down.

- **Why the reversal:** the premise stopped holding. COMM is not an extra bolted onto a knowledge
  base; it is part of what Ken is. And an opt-in transport is off on *both* sides by default — a
  two-sided feature (C7) buys nothing until the peer has switched it on too, so "a feature that fewer
  people will discover" was not a mild cost here, it was most of the feature.
- **Why the variable was KEPT rather than deleted:** Ken already has a runtime "COMM off" state — if
  `comm.db` cannot be opened, COMM degrades to disabled on purpose (§5.3), so an expendable database
  can never take the durable knowledge base down. Deleting the variable would not remove that state;
  it would only remove the operator's *control* of it, which is their one remedy if COMM misbehaves in
  production.
- **What did NOT change, deliberately:** the contract exclusion. COMPATIBILITY.md still keeps the
  `comm_*` and `station_*` surfaces outside the byte-level SemVer contract — but the justification is
  no longer "optional-and-off-by-default", which is now false. The reason is that the COMM surface is
  **mid-redesign**. Notice-messages are gone (§4.6, 3.4.0) and rooms have landed; the remaining work
  replaces pairing codes and channel-pair addressing with name-addressed send, and retires the
  **channel** — the central noun of §3, §4 and the tool table in §6. Promoting these surfaces into the contract now
  would make that redesign a MAJOR bump, or force deprecated v1 aliases through a release cycle, for
  no benefit. They are promoted when COMM v2 lands.
- **Trade-off accepted:** what the original decision protected is now spent — every install carries
  the COMM instruction section and tool surface whether or not there is a second session to talk to.
  An operator who does not want that sets `KEN_COMM_ENABLED=0`, which is exactly why the variable was
  kept.

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
A body survives for `comm_body_retention_sec` after the message **settles** — is acknowledged, or
expires — and is then deleted. A slim **metadata row** — id, sequence, sender, flags, reply linkage,
timestamps, size, content hash — outlives it and ages out on `comm_metadata_ttl_sec`.

> **Revised.** Acknowledging used to delete the body immediately unless the message required a
> response. That destroyed 97% of one live deployment's bodies (153 of 159) through the ordinary,
> instructed path — poll, act, acknowledge — because the un-acknowledged inbox was not a safety net,
> it was the only place a body ever existed. Setting `comm_body_retention_sec` to `0` restores the
> old behaviour exactly.

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

### C7b — Test expiry with a per-message `ttl_seconds`, NEVER by lowering the settings *(chosen: the parameter that already exists)*

**Do not lower `comm_message_ttl_sec` or `comm_undelivered_ttl_sec` to make something expire.**
`comm_send` takes a per-message `ttl_seconds`, and `clampTTL` honours any value from 1 up to
the configured maximum. One parameter, no console round-trip, no collateral.

This is written down because the obvious-looking alternative was proposed here and
`ken-prod-ops` refused it after five adversarial passes. Three reasons, and the second is
the one that would have destroyed other people's mail:

- **It cannot execute.** `comm_undelivered_ttl_sec` has a hard field minimum of 3600, so the
  console refuses anything smaller. Worse than refusing: the settings form saves field by
  field, so a first edit sticks while a second is rejected — leaving the deployment on a
  five-minute post-delivery TTL while the operator debugs a form error.
- **It is destructive to mail that has nothing to do with the test.** `comm_message_ttl_sec`
  re-stamps `expires_at` at FIRST DELIVERY, not at send. Every queued message polled during
  the window gets the tiny budget instead of the real one, then expires and has its body
  blanked. On the deployment where this was proposed, that would have taken undelivered mail
  to two dormant stations — one of it `requires_response` — plus a queued reply.
- **Restoring is not symmetrical.** Writing a value the operator had been leaving implicit
  pins it as an explicit row forever, so "put it back" leaves the deployment in a different
  state from the one it started in.

The general form is worth more than the instance: **a per-message parameter beats a global
setting for any test, because the blast radius is the message rather than the deployment.**

### C7a — The idempotency key outlives the body, so it is a naming decision *(chosen: tell senders, do not add a subject field)*

Retention blanks a message body and keeps its metadata row. The key goes with the row, so
**a descriptive key is the only part of a message guaranteed to survive its text.**

`ken-prod-ops` demonstrated it rather than argued it: three messages from 2026-08-06 whose
bodies the pre-1.6.0 ack rule had destroyed were identified months later from their keys
alone — `quest-infra-notebook-loss-warning-2026-08-06` and two siblings. That is how an
"unidentified message" got identified without reading anything.

**Why not a `subject` field instead:** because the key already is one, and a second field
would be a second thing to forget. What was missing was that nobody told senders — the
tool described it purely as a retry guard, which is true and incomplete. The description
now says both.

### C7 — Establishment is human-authorized *(chosen: pairing code, both sides join)*
An agent cannot conjure a channel. A human mints a short-lived **pairing code** in the web UI and
gives it to both sessions; each calls `comm_join` with it, and the channel exists once both have.

- **Why:** this is the one place COMM can borrow the property that makes the rest of Ken trustworthy.
  The curation gate works because a capability is *withheld* (no tool requires `curate`), not because
  the model is asked nicely. Channel establishment is where the same trick is available: agents talk
  freely inside a channel a human deliberately created.
- **Stations refine this and do not weaken it — but the literal sentence changed, so it is restated
  here rather than left to be discovered.** With [stations](STATIONS.md), a session staffing
  one can call `comm_open_channel` and get a channel with **no pairing code and no human present at
  that moment**. The human decision did not disappear; it moved from the conversation to the
  *relationship*, and it happened earlier: someone approved a **link** between those two stations
  (S9), which is a durable row in `ken.db` that the agent cannot write. So "an agent cannot conjure a
  channel" remains true — an unlinked pair is refused, and the only way to become linked is a human
  clicking approve. What is no longer true is "a human mints a code for every conversation".
- **ROOMS refine it a second time, in the same direction, and the sentence changes again.** A room is
  a set of stations a human named and filled in the console; a member addresses it with
  `comm_send{to_room}` and reaches everyone at once, and `to_room:"all"` reaches every station it
  shares a room with. No pairing code, no link, no human present at that moment. **The invariant
  holds and is worth restating precisely: an agent cannot enlarge its own audience.** There is no
  tool that creates a room, adds a member, or joins one — those are console-only, deliberately, and
  broadcast reaches exactly the union of rooms a human already put this station in. So the capability
  is still *withheld* rather than requested politely, which is the property C7 exists to preserve.
- **STATION ADDRESSING refines it a third time, and this is the one that retires the channel.**
  `comm_send{to_station:"<id>"}` writes to a station an approved **link** joins you to. There is no
  code, no channel row, no both-sides-join and no expiry — the conversation is the *relationship*,
  and it exists from the moment the human clicks approve. **The invariant is untouched, and by the
  same mechanism as rooms**: the only way to become linked is a human approving a request an agent
  filed, so the capability is still withheld rather than requested politely. What it removes is the
  last case where a human decision was recorded and then still cost a second human step to spend.
  - **Why this and not "materialise a channel on approval"** — which is what `comm_open_channel`
    and, since 3.11.0, link approval already do. A channel needs **both stations staffed** at the
    moment of creation. `proxmox-servers` held a station and no endpoint for five days, so an
    approval during that window materialised nothing and the granted permission had nothing to
    spend it on. A pair scope is derived from the two ids, so it needs neither side to be online.
  - **The scope is `p:<a>|<b>` with the ids SORTED**, so both directions name one place, one
    ascending sequence covers the exchange, and a cumulative ack means what it says. Members are
    the scope's own name; the mirrored link is consulted only for *permission*.
  - **Trade-off accepted:** comm.db carries a projection of `station_link` (`station_link_mirror`),
    with the same rule as the room mirror — **stale, never authoritative**. Lose comm.db and every
    link is still in `ken.db`; the next boot rebuilds it.
- **What a room is NOT:** there is no scrollback and no late-join replay. Membership is snapshotted
  at send, so a station added today sees nothing sent yesterday, and a station removed keeps the mail
  it was already sent — the audience was decided when each message was written, and rewriting it
  afterwards would mean an inbox changed because of something that happened later.
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
- **The secret can be ROTATED, and a lost one is not fatal.** §3.1 below is the whole story: what to
  do, what each remedy requires in advance, and what each costs when it is needed.

**Channel** — joins exactly two distinct endpoints, each of which may optionally be **bound to a
station** (see [STATIONS.md](STATIONS.md) S4). A channel is created either by the pairing flow in C7
or, when a human has approved a **link** between two stations, by `comm_open_channel` with no code at
all — the approval moved from the conversation to the relationship, it did not disappear. Carries an
owner identity (`space_id` plus the authorizing human actor) from day 1; a channel whose two
endpoints do not share an owner is rejected.

**Pair conversation** — the private exchange between two **linked** stations, addressed with
`comm_send{to_station}`. It has **no row and no id of its own**: the scope `p:<a>|<b>` is derived
from the two station ids, sorted. Nothing creates it, nothing expires it, and neither side needs to
be connected for the other to write. It is what a channel is for, minus the channel — and it is the
shape slice 7 retires the channel *into*.

**Message** — one atomic body plus an envelope: server-assigned per-scope sequence, sender
endpoint, `requires_response`, optional `reply_to`, delivery count, timestamps, size, content hash.

---

### 3.1 Losing the endpoint secret

`comm_register` returns the secret once and nothing will ever show it again. This is not an edge case
to design around later: **an AI client's memory is lossy by design.** Context compaction is routine,
silent, and gives the session no signal — a session does not know it has forgotten. Treat a lost
secret as an expected event with a known remedy rather than as an accident.

Three mechanisms answer it. They are not alternatives ranked by quality; they differ in **what each
requires in advance** and **what each costs at the moment of failure**.

**Prevention — write the pair to disk at registration.** Requires nothing but doing it. The
connect-time instructions tell every session to write `endpoint_id` and `endpoint_secret` to a `0600`
file outside any git repo before its next tool call, and to re-read that file after a compaction
rather than trusting its context. It costs nothing, needs no operator, and is the **only** mechanism
that helps a session running unattended. *What it does not solve:* it is useless to a session that
did not do it before the failure, and a file on disk is a secret on disk — no help at all for a
secret that has leaked, and one more place one can leak from.

**Rotation — a curator issues a new secret from the `/comm` console.** Requires a human at the
keyboard. It preserves the endpoint id and every channel the endpoint belongs to, so peers are
undisturbed and nothing is re-paired. It is also the **only** remedy for a secret that has *leaked*,
which neither of the others addresses. *What it does not solve:* the wait. Rotation shortens the work
to seconds; the session is still stalled until somebody is available.

> **Why rotation has no tool, and will not get one.** A Ken token covers a *machine*, so every session
> on that box presents the same credential — anything one session can trigger, every session can
> trigger, against any endpoint on the machine. A rotation tool would let a session seize a
> neighbour's endpoint, which is precisely the shared-inbox accident the per-endpoint secret exists to
> prevent. The defect is the **automation**, not the reissuing: behind curator authentication — a
> credential no session holds or can obtain from the machine — the same operation is safe. Each
> rotation is logged with the curator who performed it, because a rotation nobody remembers doing is
> the signal that matters.

**Replacement — bind a fresh endpoint to the same station.** [Stations](STATIONS.md) are core and on
by default, so what this still requires is the binding arranged in advance (S5): the lost session must
have bound its endpoint to the station. A new session staffing that workspace calls `comm_register`, writes the new secret to disk, and
calls `comm_bind` with the `X-Ken-Workspace` header set — inheriting the workspace's unread mail,
because the **station** owns the inbox (S4), and the dead endpoint's claims return to the unclaimed
tail rather than stranding.

> **The voucher step was deleted in 3.29.0** (`docs/IDENTITY.md` §10 step 3). It existed solely so
> a station key never crossed to the comm surface as a tool argument; one identity spans both
> surfaces now and the workspace id authorises nothing, so there was nothing left to hand across.
Where the two stations already hold an approved **link** (S9), it re-opens the channel with
`comm_open_channel`: no pairing code, and no human in the loop at that moment. This is the only path
that recovers without waiting for a person. *What it does not solve:* it recovers the **mailbox and
the relationships, not the conversation** — the transcript was never Ken's to keep (S1) — and it does
nothing for a session that was never bound: stations being core removed the operator's setup step, not
the binding step, which the session itself must still have performed.

**The honest summary.** A session that wrote its pair to disk recovers alone. A session bound to a
station recovers alone. A session that did neither waits for a human, and no mechanism here changes
that — which is why prevention is stated first in the instructions and costs nothing.

## 4. Delivery semantics

### 4.1 Lifecycle

```
queued ──poll──▶ delivered ──ack──▶ processed ──▶ (body kept for the retention
   │                  │                  │              window, then deleted;
   │                  │                  │              metadata outlives it)
   │                  │                  │
   │                  └── TTL from DELIVERY ──▶ expired (body deleted; sender learns
   │                                                        at its next poll — §4.6)
   │
   └── undelivered backstop ─────────────────▶ expired (BODY KEPT — nobody ever read it,
                                                        so "expired" must not also mean
                                                        "unknowable")
```

Every transition is stamped by the **server** clock. Clients supply *relative* lifetimes
(`ttl_seconds`), never absolute timestamps, so clock skew between agent machines cannot silently
shorten or extend anything.

### 4.2 Idempotency

Required on every operation, because §C6 makes redelivery normal:

- **`comm_send`** takes a client-supplied idempotency key, unique per sender and channel within a
  dedup window. A repeat returns the original message id rather than sending a second message.
- **`comm_poll`** is a pure read. It returns *all* unacknowledged messages for the endpoint across
  every scope it can receive in — channels, rooms and broadcast alike — with a delivery count, plus
  the `notices` array describing what became of messages it SENT (§4.6). Being polled is an informational timestamp, never a state
  that hides a message from the next poll.

  An optional **`scope`** narrows one call to a single conversation (`ch:<channel_id>`, `r:<room_id>`,
  `p:<a>|<b>` or `b:<party>`), so a hub holding several conversations can drain one without pulling
  the others into its context and without spending the 100-message ceiling on mail it did not ask for.
  It is a **window, not a count**: the other scopes are hidden from that call, not empty, and the
  result echoes **`scope_filter`** so a caller can tell a server that applied the filter from one that
  predates the argument and ignored it. A scope naming no namespace is **refused**, never matched
  against nothing — an unparseable filter returning an empty list would be indistinguishable from an
  empty inbox. A well-formed scope that matches no row is an ordinary empty result, not an error, so
  the filter cannot be used as an existence oracle for ids. `notices` are **not** filtered: they are
  what became of messages the caller SENT, they are shown once, and dropping them from a scoped poll
  would lose them.
- **`comm_ack`** succeeds on an already-acknowledged or unknown id.

Ordering is promised **per channel and direction** and nowhere else: polls return strictly ascending
sequence numbers, and a cumulative acknowledge (`ack_up_to`) is available because it collapses
chatter and is idempotent by construction.

**For a STATION-BOUND endpoint the promise weakens, and the weakening is deliberate.** A station owns
one logical inbox that several endpoints may read, so the guarantee becomes *per channel and
direction, across the station's readers*: two sessions polling one station see a **partitioned**
stream and neither sees the whole order. `delivery_count` likewise counts per **station** rather than
per endpoint, so a redelivered message may reach a different reader than first saw it. That is the
price of letting a second session help without severing the first, and of letting a replacement
session inherit mail its predecessor never read. An **unbound** endpoint is unaffected: it is the
sole reader of its own mail and the original promise holds exactly.

### 4.3 Request/response

A message with `requires_response` is given a server-computed reply deadline **when it is first
delivered** — not when it is sent, because a deadline that starts before the recipient can know the
message exists is a deadline against the transport rather than against the peer. Its body follows the
ordinary retention window like every other message; `requires_response` no longer governs retention
at all. When a deadline passes unanswered, the requester learns about it from the
`notices` array on its next `comm_poll` — `reason: "reply_overdue"` — so a dead peer surfaces as a
fact the requester can act on rather than an indefinite wait. A message that expires unread reports
the same way (`reason: "expired"`). Full-duplex removes turn deadlock; reply deadlines are what keep
the *requester* from hanging in its place.

Until 3.4.0 this was a **status message** the sweeper WROTE to the requester (`kind: "status"`), and
it deliberately bypassed the backpressure cap because a full channel is precisely when a failure
signal matters most. That exemption is gone with the mechanism: a derived notice is not a message, so
there is no cap for it to be exempt from — and no insert inside a pass whose job is deleting. §4.6
has the full reasoning.

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

### 4.6 Notices — a sender learns what became of what it sent

A **notice** tells a sender that a message they sent expired unread, or that a reply they asked for
never came. It rides the `comm_poll` result as a `notices` array. It is not mail: nothing is
addressed to anyone, nothing is queued, and there is nothing to acknowledge.

**It used to be mail, and that is the point of the design.** Until 3.4.0 the sweeper WROTE the
sender a real message — a row in `message` with `kind: "status"`, occupying the scope, counting
against backpressure, carrying its own TTL, and needing its own ack. Three consequences, in the
order they were learned rather than the order they matter:

- **A deleting pass was also an inserting one.** Sweep expires messages, blanks bodies, purges
  metadata, cleans attachment files and removes idle endpoints — all in one transaction. Because it
  also inserted, a failure in the *notice* rolled back every deletion. That is precisely how one
  unread ROOM message stopped all five in 3.0.0 and 3.0.1: the notice writer read two columns that
  are NULL for room mail.
- **The information was always derivable.** Everything a notice carries already exists in `message`
  and `delivery`. A written notice is a denormalised copy that can disagree with its source.
- **A notice could itself expire.** It was stamped with a TTL, so the signal reporting a failure to
  deliver was subject to the same failure it reported.

**What a notice says.** `message_id`, `scope`, `reason` (`expired` or `reply_overdue`), `at`, the
`idempotency_key` — echoed because retention blanks bodies and keeps metadata, so a notice naming
only an opaque id describes something the sender can no longer look up — and `recipients`, the
parties it concerns. For a room message the recipient list is most of the information: "nobody
engaged" and "one station is quiet" are different facts and only one of them is actionable.

A room message that **some** recipients read and others ignored still produces a notice, naming only
the parties that went quiet. Suppressing it when any recipient acked was a real defect: one ack hid
every other silence, and silence is what a sender reads as delivery.

**How it clears.** Each poll confirms what the PREVIOUS poll displayed, then records what it is
showing. A notice is therefore cleared by the caller coming back, not by the call that showed it, so
a fault between the query and the caller holding the result cannot drop it. A result lost in transit
*does* lose the notice — the server cannot tell a delivered result from a discarded one — and that
is accepted rather than fixed: the alternative is an explicit confirmation call, and a NEW TOOL
reaches no session that is already running, because MCP tool lists pin at conversation start. The
population that would never receive the fix is exactly the population with mail dying unread.

**What it recovered, which was not designed for and is worth recording.** A written notice was
itself a message with its own TTL, so a notice about a failure could expire before its sender came
back — the signal reporting a delivery failure was subject to the same failure it reported. Deriving
it from rows that already exist removes that: a notice is computed fresh on every poll, so it is
available for as long as its underlying rows are. Measured on the first production deployment to
upgrade: the first poll after 3.4.0 returned **ten notices, nine of which that station had never
seen** — `reply_overdue` events from 2026-08-04 to 08-06 whose written notices had expired unread
months of polls ago. The change was made to decouple the sweeper; it also retroactively surfaced
signals the old model had already lost.

**What this gives up.** A notice cannot outlive the rows it is derived from. The metadata purge
removes a settled message `metadata_ttl_seconds` after it settles (7 days by default), and the
notice goes with it; a written notice was an independent row and could outlive its subject. The
trade is deliberate — an independent row is what gave a failure signal its own expiry, its own
backpressure and its own ack — but operators should know the notice window is the metadata window.

---

## 5. Isolation — the rules that make C1 honest

A separate database file isolates the KB's WAL and its backup stream. It does **not** isolate the
disk, the process, or the readiness signal. These are enforced rules, each with a failure it prevents.

1. **Disk budget.** A global byte cap on relayed FILE storage, plus a free-space floor, both checked
   before accepting bytes and again as they arrive. Without them, ephemeral traffic can fill the
   volume and start failing KB writes — search, save, promotion, and the nightly snapshot all die
   because of chat. This is the coupling C1 must not have.
   Message *bodies* are bounded differently and deliberately: a per-message size cap, a per-channel
   un-acked depth cap, and TTL sweeping, rather than a global byte budget — the product of those caps
   is small and self-limiting, whereas files are large and arbitrary. There is no aggregate byte cap
   on messages; if that ever proves insufficient, it is the same fail-closed SQL check as the file
   budget.
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
5. **Its own rate accounting.** COMM gets its own per-token bucket, separate from the knowledge
   base's, so a poll loop cannot exhaust the budget that fronts `kb_*` calls. The default operating
   convention is one token per machine, so every session on a box shares one bucket. Long-poll is the
   primary defense: a parked call costs one unit regardless of how long it waits, which is why the
   tool descriptions push a long wait over frequent short polls.
   **Not yet done, and honestly a gap:** COMM requests still feed the shared per-IP strike counter, so
   a pathological poll loop can still trip the machine-wide auto-block; and poll results do not carry
   a server-advertised minimum interval. Both are on the list for the next COMM increment.
6. **Drain on shutdown.** The graceful-shutdown budget is shorter than a long poll, so parked waiters
   are woken with an empty success before shutdown begins. Otherwise every deploy produces a burst of
   agent-visible transport errors.
7. **Bounded waiters.** Concurrent parked polls are capped per endpoint and globally; past the cap a
   poll returns immediately and empty. The HTTP server deliberately sets no write timeout (the MCP
   transport holds long-lived responses) and the unit file sets no task or memory limits, so nothing
   outside the process bounds this. `TasksMax`, `LimitNOFILE` and `MemoryMax` are now set in
   [`deploy/ken.service`](../deploy/ken.service).
8. **Backups exclude it by construction — keep it that way.** ([BACKUP.md](BACKUP.md) now says so
   explicitly.) Litestream replicates one explicitly
   named path, and the snapshot script copies only the KB database, so `data/comm/` is already
   outside both tiers. Two traps to avoid: the snapshot retention prune matches `ken-*.db*` in the
   backup directory, so nothing COMM-related may ever be written there under that shape; and
   [BACKUP.md](BACKUP.md)'s "your knowledge lives in one SQLite file" becomes incomplete the day this
   ships and must be corrected in the same change to say COMM state is deliberately unreplicated.

---

## 6. Tool surface (sketch)

Eight tools, all `comm_*`, all requiring the `comm` scope (the two file tools additionally require `comm-file`), served from `/comm/mcp`
(`internal/commserver`). Every tool except `comm_register` carries `endpoint_id` + `endpoint_secret`:
the bearer token identifies a *machine*, so the endpoint pair is what identifies the *session* within
it.

Core, with no switch — the C2 opt-out variable was itself removed in 2.0.0. The message database defaults to
`<db dir>/comm/comm.db` (`KEN_COMM_DB`). Mint a **dedicated** token. There are **three** scope families — knowledge-base,
comm, and station — and the rule is not "one family per token" but which pairs may combine:
knowledge-base scopes may not be mixed with either of the others, while `station` and `comm` MAY be
held together, because a session legitimately staffs a post and talks from it. A token that could
both read working notes and write curated knowledge is the mixing this check exists to prevent.
Enforced at mint time:

```
ken token add --actor comm-dev --scopes comm
```

| Tool | Purpose |
|---|---|
| `comm_register` | Register this session as an endpoint; returns `endpoint_id` + one-time secret. Does NOT bind to a station — write the secret down, then use `comm_bind`. |
| `comm_join` | Join a channel using a human-minted pairing code. Both sides call it. |
| `comm_open_channel` | Open a channel with a station your human has already **linked** to yours — no pairing code. Refused without an approved link. |
| `comm_bind` | Bind an endpoint you already have to a station, keeping its id, secret and channels. For a session that registered before it began staffing a station. |
| `comm_channels` | List this endpoint's channels, rooms and **pairs** (the stations a link lets it address), each with what is waiting. |
| `comm_send` | Send one atomic message, addressed by `to_station` (a linked station — no code, no channel), `channel_id`, or `to_room`; optional `requires_response` / `reply_to` / idempotency key. |
| `comm_poll` | Long-poll for unacknowledged messages across every scope this endpoint receives in — channels, rooms, pairs and broadcast. Optional `scope` drains one conversation; optional `limit` (default 50, max 100). |
| `comm_ack` | Mark a message processed (or acknowledge cumulatively up to a sequence). |
| `comm_file_offer` | Offer a file: a same-host rendezvous, or a one-time upload grant (`comm-file`). |
| `comm_file_grant` | Mint a fresh single-use download URL for a file offered to you (`comm-file`). |

Two surfaces exist alongside them:

- **A dedicated MCP endpoint.** COMM mounts on its own path with its own auth requiring `comm`, so a
  knowledge-base token cannot send messages and a COMM token cannot write knowledge, each gets
  independent rate accounting, revocation is per-surface, and an operator can firewall or disable one
  without the other. It also carries no permissive CORS: the KB endpoint allows browser origins for a
  hosted connector, and COMM has no browser client.
- **An instruction section**, appended to the server-delivered instructions whenever COMM is enabled
  — which is by default (C2) — describing the loop (register → join → poll → act → acknowledge → reply) and the handling
  rules in §8.

### Scopes

`comm` is a new token scope; `comm-file` additionally gates the file tools (`comm_file_offer`,
`comm_file_grant`) and the byte-relay HTTP surface at `/comm/files/{grant}`. They were reserved
together from the first release because splitting a shipped scope later would be a MAJOR, merging two
is free. A file-capable token is minted as `--scopes comm,comm-file` (both are comm-family, so the
dedicated-token rule is satisfied).

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

1. **A provenance marker** on the authored version (`entry_version.via_comm`, migration `0010`), set
   when the authoring token received COMM traffic within a bounded recent window. The two databases
   stay decoupled — the MCP layer holds both handles and passes a boolean.
2. **A badge in the curator UI** on such proposals, so a human can demand a first-hand citation
   before promoting.
3. **Two lines in the COMM instruction section:** knowledge received from another session is
   *hearsay* — attribute the sending **station** in the rationale (`from_station_name` and
   `from_station_id`, both already on the polled message), lower the confidence, and never record an
   outcome or assert verification on another session's behalf. **The endpoint is the wrong name to
   write down.** An endpoint is one connection: its row is deleted by the idle sweep (`7 d` by
   default, `EndpointIdleTTLSeconds`) while the knowledge base has no TTL, so an endpoint-attributed
   entry cites a row that no longer exists and three sessions of one correspondent read as three
   unrelated strangers. When the sender holds no station, `from_endpoint_id` plus the date is all
   there is — record it as that, and mark the claim uncorroborated.

**Operator requirement — mint both tokens under the same actor.** A COMM token must be dedicated
(§6), so the token that receives messages is never the token that authors an entry. The marker
therefore keys on the **actor**, which is the identity the two tokens can legitimately share:

```
ken token add --actor agent-x --scopes comm
ken token add --actor agent-x --scopes read,write-draft,propose
```

Mint them under *different* actor names and nothing is ever marked — a silent false negative, which
is why it is stated here rather than left to be discovered. Actors resolve by display name and so
collapse across machines; that would be wrong for an ownership check, but here over-matching is the
safe direction.

Three properties of the marker are deliberate:

- **It keys on delivery, not arrival.** A message sitting un-polled in the queue has influenced
  nothing; `first_delivered_at` is when the receiving session actually read it.
- **It is frozen**, like every other provenance column, and unlike `content_lang` (which was left
  mutable so a backfill could re-derive it). This one cannot be re-derived — the COMM metadata it was
  computed from is swept — and a mutable marker could simply be updated away.
- **NULL means "no signal", never "known clean."** Every pre-existing row is NULL, an error while
  checking leaves it NULL, and COMM being off leaves it NULL. It is biased toward over-reporting,
  because a false positive costs the curator one extra glance while a false negative silently
  launders hearsay into the knowledge base.

What it cannot do: it does not know whether the received message had anything to do with what is
being saved. It answers "was this author in a conversation?", not "is this claim second-hand" — a
prompt for the curator's judgement, not a verdict.

---

## 8. Trust and safety — what is enforced, and what is not

Ken's reputation rests on a gate that is **structural**: the AI cannot promote because no tool grants
the capability. COMM must be equally honest about where it does and does not have that property.

**Enforced by the server:**

- A channel exists only because a human authorized it: a pairing code both sessions used, or an
  approved station **link** (C7). Stations are core, so the link path exists in a default install.
- Sender identity is stamped server-side into every delivered envelope; a message cannot claim to be
  from another endpoint.
- Message bodies are returned in a dedicated structured field, never spliced into prose, so content
  cannot position itself as though it were part of Ken's own instructions. (There is deliberately no
  claim here about defeating a determined in-band forgery: the transport is JSON, the boundary is the
  field, and a receiving harness that flattens structured results into a prompt is beyond Ken's reach.)
- **Ken writes no messages at all.** Every row in `message` came from a peer. Failure notices used
  to be server-authored `kind: "status"` rows; since 3.4.0 they are DERIVED at poll time (§4.6), so
  the forgery question is closed by construction rather than by a field a reader must check. Rows
  written before the change are still present and still carry `kind: "status"`; nothing new joins
  them.
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

**Settings** (a new group, all live): maximum message size · poll-wait ceiling · message and metadata
TTLs · reply-deadline default · pairing-code TTL · per-channel unacknowledged cap · hearsay window ·
file exchange on/off · file size cap · relay storage budget · free-space floor · file TTL · transfer
grant TTL. (Per-owner quotas are **not** implemented — there is one owner; see §10.)

**A Comm page** in the web UI: mint a pairing code; list endpoints (label, owner, last seen) and
channels (state, counters, queue depth); **rotate an endpoint's secret** (§3 — the endpoint and its
channels survive, so it is the cheap remedy for both a leak and a session that lost its secret, and it
is deliberately reachable *only* here); revoke a channel or an endpoint; disable the subsystem live.
Rotation and revocation are visually distinct on purpose — rotation is recoverable, revocation is not,
and two identical red buttons would invite the wrong one.

**Reassign an endpoint to a conversation** (3.38.0) — the comm half of workspace recovery, and the
answer to a mailbox whose conversation is gone. Rotation *was* the only way back into one, and it
does not work for the sessions that need it most: it mints a secret for the human to relay and the
session to write to disk (`0600`, outside any git repo), and a claude.ai chat has no disk. That is
precisely the ceremony migration 0019 removed from `comm_register`; leaving it on the recovery path
would have put it straight back.

So the recovery is **one string used twice**: the session states its conversation key, the human
pastes it into the station form and this one, and the next `comm_poll` reads the mail that was
already waiting. The endpoint's id, channels, station binding and **queued messages** are untouched
— a reassignment that dropped the inbox would be a rotation with extra steps.

- **The owner token is NOT touched, and that is load-bearing.** `auth` re-checks on every call that
  the bearer's token matches the endpoint's owner (§3), so a conversation key alone can never drive
  a mailbox from another account. If the taking-over session bears a *different* Ken token, the
  operator must also **repoint** the endpoint to that token — the control is right next to this one.
  Repointing silently here would move an estate boundary as a side effect of a convenience.
- **The key is taken from whatever holds it and the displacement is reported** — same ruling as the
  station form, same reason: a session asked for its key has usually already claimed a fresh empty
  mailbox under it, so refusing would fail the main path. Nothing is destroyed; the displaced
  endpoint keeps its channels and can be reassigned back.
- **An empty key releases** the mailbox — after which only its secret can drive it, so the flash
  says to rotate if nobody holds one.
- **A revoked endpoint is refused**, or revocation would be advisory.
- Every reassignment and release is **logged to the server log** with the operator's name, like a
  rotation and for the same reason: `comm.db` is expendable and not backed up, so the log is the
  record that survives, and "who repointed this mailbox" is exactly the question that follows mail
  turning up somewhere unexpected.
The page **auto-refreshes** the way the Proposals page does — a small poller hits `GET /comm/count`
(a cheap per-space "console fingerprint": counts of endpoints, channels, open channels, live pairing
codes, and in-flight messages, prime-weighted so offsetting changes rarely collide) and reloads when
the number diverges from the rendered one, so an operator watching for a peer to join, a channel to
open, or a code to be consumed sees it without a manual refresh. A "last checked" stamp, re-written on
every poll from the browser's own clock, makes the liveness visible rather than assumed; it is a
change *detector*, not a checksum, and it is hidden without JavaScript, where the freshness it claims
could only ever be stale.

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
reject. Establishment is two-sided from day 1; and every listing is scoped to the owner from day 1.

**Deferred:** invitation flows between different humans, per-user quotas with real values, cross-space
policy. All additive on top of the above — which is the entire point of settling ownership keying and
two-sided establishment before the tools freeze, since both would otherwise be MAJOR surgery.

---

## 11. File exchange

Built, and gated twice: the `comm-file` scope on the token, and the live `comm_files_enabled`
setting (**default ON** since 2026-08-24 — a KILL SWITCH rather than an opt-in). It shipped
defaulting OFF because the relay is the bulk of the subsystem's risk, and that default was reversed
under Vlad's ruling that no Ken feature is optional or off by default: withholding the SWITCH was
never the point, withholding the DEFAULT was. Turning it off still stops bytes immediately, checked
per operation and with no restart, which is what makes it worth having in an incident.

Three tiers, in the preference order the instructions teach:

1. **Same-host filesystem handoff** (`transfer: "path"`) — the primary path for anything large; zero
   bytes move through the server and zero model tokens are spent on payload. The offer carries a
   **server-validated bare basename** (no separators, no dot-dot, no control bytes — the C9 contract)
   plus the file's sha256 and the sha256 of a rendezvous **nonce** the sender wrote into the shared
   exchange directory. The receiver reads the nonce, echoes it back in a reply — proving the shared
   filesystem — then reads the file and verifies its checksum. The attachment row exists purely as
   audit and envelope.
2. **Small inline payloads** — an ordinary message body, for genuinely small text. The tool
   descriptions state the token cost so the expense is visible before it is incurred.
3. **One-time-grant HTTP relay** (`transfer: "upload"`) — for cross-host transfers.
   `comm_file_offer` mints a single-use, minutes-lived upload grant; the agent PUTs the bytes with a
   shell tool (`curl -T FILE -H "Authorization: Bearer $TOKEN" <base>/comm/files/<grant>`). The
   message referencing the attachment is enqueued **only when the upload completes and its sha256
   matches the offer**, so the receiver never observes partial state. The receiver's poll carries the
   file descriptor; `comm_file_grant` mints a fresh single-use download URL as often as needed.

Enforced properties of the relay:

- **Two credentials per byte-moving request**: the bearer token (must carry `comm-file` and must own
  the endpoint the grant was minted for) and the grant itself. A leaked URL is useless without the
  token; a leaked token cannot touch bytes it was never granted.
- **Quotas fail closed, checked twice** (at offer and again as the bytes arrive): a per-file size cap,
  a global storage budget, and a free-space floor so the knowledge base's writer always has headroom.
  In-flight uploads are capped per sender, which closes the accounting window between "grant minted"
  and "bytes counted".
- **Grants are single-use and kind-bound**, consumed even when the transfer then fails; an unknown,
  expired, consumed, or wrong-kind grant are all indistinguishable, so grants cannot be probed.
- **Verification before visibility**: bytes stream into a `.part` file (0600, 0700 directory, never
  executable) and are renamed into place only when size and checksum match the offer; a mismatch fails
  the attachment and deletes the partial.
- **Serving posture**: `application/octet-stream`, `nosniff`, `Content-Disposition: attachment` —
  relayed bytes are downloads, never rendered.
- **The sweeper deletes delivered and expired bytes** (an acked message settles its attachment) and
  removes abandoned `.part` files; the attachment *row* — name, size, sha256, endpoints, timestamps —
  survives as the audit record, on the same reasoning as message metadata.
- Files live under `data/comm/files/` so an operator can exclude or separately mount exactly one path;
  nothing under `data/comm/` is backed up.

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

- **Re-labelling a channel.** A channel's human name is fixed at pairing time by the code's label
  (§ the 1.2.2 change), so a channel named in haste, or one whose purpose drifts, is stuck with it —
  and a channel paired before that change is stuck at "(no label)" permanently. A console edit action
  that sets `channel.label` after creation is small and self-contained; the only question is whether
  it belongs on its own or bundled with the endpoint work below.

- **Endpoint identity in the console — the same disease the channel panel was cured of, without the
  same remedy.** The "Registered sessions" panel labels endpoints from the string an *agent* passed to
  `comm_register`. Two consequences the channel fix does not share: (1) the 1.2.2 cure sourced the
  name from a **human-minted** pairing code, and that lever does not exist here — no human is in the
  loop at registration, so the only label that exists is one the agent invented for itself; and (2)
  endpoints accumulate **faster** than channels, because creating a channel is gated by a human
  minting a code while registration is gated by nothing (an afternoon of two sessions testing already
  left 4 endpoints against 2 open channels). The sharper concern: an **agent-supplied label is
  untrusted input rendered as identity** on the one surface where a human decides what to trust and
  what to revoke — nothing stops two endpoints claiming the same name. The blast radius is small
  (pairing still requires a human code, so a label cannot get anyone into a channel), but the fix
  should still put a **human-controlled** identifier in that column. Two increments, cheap-first:
  - **Cheap, high-value:** show intrinsic metadata the agent cannot choose — first seen, last
    activity, channel membership, host hint — so an unlabelled endpoint is still actionable ("last
    active 3 weeks ago, member of no channels" is most of what the panel is for).
  - **Fuller:** a human-set endpoint name that is authoritative and **visually distinct** from the
    agent's label (which stays as a hint about what the thing claims to be, never silently
    overwritten). This shares a shape with the channel re-labelling above; do them together.
  *(Surfaced from the production shakedown; recorded here rather than built, pending a decision on
  scope.)*

---

## 13. Scoped increment — failure visibility and honest gauges

The most expensive property of the 1.2.0 endpoint-sweep bug was not the deletion; it was that an
operator watching logs and metrics saw a perfectly healthy service while the feature was completely
non-functional. Two live-instance observations from the production shakedown sharpen the point, and
together they define one coherent increment. Nothing here is a defect in the shipped code — it is
about making COMM's failures *legible*, which for a background subsystem is as important as being
correct.

**The two observations.**

1. **The gauges are scrape-instant snapshots of racing quantities.** `ken_comm_endpoints` read `0`
   while an endpoint existed — a symptom of the sweep deleting it between an operator's tool call and
   the scrape (fixed in 1.2.1). `ken_comm_poll_waiters` read `0` while a 180 s poll was actively
   blocking — because the waiter registers a hair *after* the poll's initial database read, so a
   scrape dispatched with the poll samples before it parks. Both are the same shape: a gauge that
   reports the value at the instant of scrape, of a quantity that changes on a sub-second timescale.
   Neither is wrong; both are *misleading* to a human who reads a single value as "the state".
2. **A tool-level failure does not register anywhere.** A `not found` from a vanished endpoint (or any
   store-level COMM error surfaced as a tool result) is not a transport auth failure, so it correctly
   does **not** increment `ken_auth_failures_total{surface="comm"}`. There is no counter that does
   move. So a burst of failing tool calls is invisible in metrics, and — because those errors are tool
   results, not HTTP errors or log lines — invisible in logs too.

**The increment (a single, coherent unit; target: a later MINOR).**

- **Counters, not just gauges, for the things that fail.** Add monotonic counters that a rate query
  can see even when the instantaneous gauge is zero: `ken_comm_tool_errors_total{tool,reason}` (every
  `comm_*` tool result that is an error, bucketed by a small reason vocabulary — `not_found`,
  `denied`, `backpressure`, `too_large`, `channel_closed`, `quota`), and `ken_comm_endpoints_swept_total`
  / `ken_comm_messages_expired_total` (the janitor's own actions, so a sweep that deletes anything is
  observable rather than silent). Counters are the right instrument for "did failures happen recently",
  which is the question an operator actually has; the existing gauges answer "what exists now", which
  is a different question and fine as-is once paired.
- **Make the janitor announce non-trivial work.** The sweep already logs when it expires or purges
  rows; extend that to endpoint and channel removal (currently silent) with a rate-limited line, so a
  mass deletion — the shape of a misconfiguration like the one 1.2.1 fixed — leaves a trail. One line
  per sweep that changed something, never per row.
- **Decide the gauge question explicitly, do not just smooth reflexively.** Two honest options, and
  the increment should pick per gauge rather than blanket-apply: (a) leave a racing gauge as an
  instantaneous snapshot but *document* it as such in `monitoring/README.md` (the cheapest fix, and
  correct for `poll_waiters`, which is inherently a right-now quantity); or (b) pair it with a
  counter/high-water companion where "did this ever climb" is the real question (`endpoints` is better
  served by the tool-error counter above than by smoothing). Resist a moving-average on a gauge — it
  hides exactly the spikes an operator needs to see.
- **Close the two §5.5 gaps in the same increment, because they are the same theme.** COMM requests
  still feed the shared per-IP strike counter (a pathological poll loop can auto-block a machine's KB
  access), and poll results do not advertise a minimum interval. Both are "COMM should fail *safely and
  visibly* under abuse" — the natural companions to the telemetry above.

**Explicitly deferred within this increment.** No per-message tracing, no content-level audit surface
(the metadata audit row already exists for incident response), and no alerting rules shipped in the
bundle — the Grafana/Prometheus bundle stays structurally neutral (no pinned instance selector), so
alert thresholds remain the operator's to set. The deliverable is *signals an operator can build an
alert on*, not the alerts themselves.

**Acceptance test of the increment.** Re-run the 1.2.0 failure with the fix reverted behind a flag:
an operator watching only `/metrics` should be able to see that something is wrong — a climbing
`ken_comm_tool_errors_total{reason="not_found"}` and a janitor log line — without reproducing the
tool calls by hand. That is the bar this increment has to clear, and the bar the 1.2.0 experience
failed.
