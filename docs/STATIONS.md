# Ken — stations, notebooks and task lists

> **Status: BUILT, supported, and CORE — always on. There is no switch:** `KEN_STATION_ENABLED`
> was removed in 2.0.0 and setting it does nothing (S2 records the decision as it stood).** Written before the code, the way [`COMM.md`](COMM.md) was, so the decisions were argued
> while they were still cheap — then implemented against. The `station_*` surface still sits outside
> the byte-level compatibility contract ([`COMPATIBILITY.md`](../COMPATIBILITY.md)), exactly as COMM
> does — but **no longer because it is off by default**. It stays outside because the COMM surface
> this design is wired into (S4's station-owned inbox, S9's links materializing channels) is
> mid-redesign: notice-messages are gone (3.4.0) and rooms have landed; the remaining work replaces
> pairing codes and channel-pair addressing with name-addressed send, and retires the **channel** —
> the central noun of the current tool surface. Promoting either surface now would make that redesign a
> MAJOR bump, or push deprecated v1 aliases through a release cycle, for no benefit. Both are
> promoted into the contract when that redesign ("COMM v2") lands; until then they evolve additively.
>
> **Built:** the schema and `kens_` station keys with the three-way scope split (S5); the notebook
> with revisions (S10); the task list and its ordering contract (§11); the locker (S11); the
> `/station/mcp` surface (§6); the operator console at `/stations` (§10); `ken station
> add|list|key|requests`; peer links, denials and their mute (S9); endpoint binding by voucher (S5, S5a); `station_directory` and its published-or-linked visibility rule (S8);
> the station-owned inbox with claim-once delivery and its lease (S4); severing on key revocation
> (S6); and `comm_open_channel`, which opens a channel over an approved link with **no pairing code**.
>
> **Not built, and named here so no section reads as a promise it does not keep:** per-station
> connect-time instructions (§13); and station-specific metrics, which do not exist beyond the generic
> per-tool counters every MCP surface emits.
>
> Convention in this document: **S*n*** is a locked decision, **§*n*** is a section.

A **station** is a durable, human-named working identity — `prod-ops`, `promo`, `public-dev`. AI
sessions *staff* a station; the station outlives them. It owns three assets: a **notebook** (pages of
prose), a **task list**, and a **locker** (small files). It is also what COMM addresses, so a peer
relationship survives the session that created it.

---

## 1. What a station is not

- **Not a user account.** Nobody logs in as a station. It has no password and no web session. A
  station is a *post*, not a person.
- **Not a second knowledge base.** The notebook is working state — uncurated, private, mutable, never
  searched by `kb_search`. Knowledge goes through the curation gate or it is not knowledge. S10 gives
  the routing rule and the only path out.
- **Not a message broker.** A message is claimed by **one** reader and redelivered to that reader
  until acknowledged — COMM's at-least-once guarantee (C6), raised from the endpoint to the station.
  Stations do not fan out, do not multicast and do not forward. S4 says why.
- **Not a file store.** The locker holds a few small files that help a fresh session on a new machine
  reconstitute itself. It is bounded and carried in every snapshot; §9 states the numbers.
- **Not a credential store.** Never put a token, key or password in a notebook page or a locker blob.
  S11 says what Ken can and cannot do about that.
- **Not machine-bound — but keys are.** Moving a session to another machine means minting *that
  machine* its own key for the same station (S5), never copying an existing one. What follows the
  station and what does not is stated exactly in S1.

---

## 2. Locked decisions

### S1 — A station is durable identity; a session is not *(chosen: separate the two lifetimes)*

`comm_register` today mints an **endpoint**: ephemeral, swept after an idle period, labelled with a
string the *agent* chose. That is a connection, not an identity, and [`COMM.md`](COMM.md) §12 already
records the consequence. A station is the durable half: created and named by a human, never swept,
living in `ken.db` where the backup story already reaches.

- **Why:** every feature here needs continuity across conversations. A notebook that dies with the
  session is scratch paper the model already has; a directory of who-is-out-there is worthless if
  entries evaporate; a peer relationship re-authorized every morning is friction the human stops
  paying. All three are the same missing noun.
- **Second reason:** it fixes an existing hole rather than adding a surface beside it. An
  agent-asserted label is not an identity, and no console polish makes it one.
- **Trade-off accepted, and it is narrower than it looks.** Assets and links follow the station and
  survive a machine move. **Conversation history does not.** Messages expire on their own TTL, the
  endpoint sweep then reaps endpoints no message references any more, and channels cascade with them
  — `comm.db` is expendable by design and this document does not change that. Moving machines
  preserves *who you may talk to* and *everything you wrote down*; it does not preserve the
  transcript. The tool description must say so, or a session will plan around a guarantee it does not
  have.

### S2 — REVERSED: stations are CORE and ON by default *(was: opt-in, off unless `KEN_STATION_ENABLED=1`)*

> **SUPERSEDED IN PART, 2.0.0 — read the variable below as history.** `KEN_STATION_ENABLED` was
> REMOVED. Nothing reads it and setting it does nothing. Stations are core and there is no switch.
> The *reversal* this record describes stands; only the opt-out it kept is gone.

`KEN_STATION_ENABLED` survived **inverted**, as an opt-OUT: `0` / `false` / `off` / `no` turned the
surface off, and a value Ken did not recognise left it **ON** — a typo must not silently disable
core functionality. (Removed entirely in 2.0.0: a switch nobody used still cost a hedge in every
document, and the hedges rotted.)

- **Why it was off:** the same reasoning as COMM's C2. A default install must remain exactly the
  curated knowledge base the README promises — no extra operating loop in every agent's connect-time
  instructions, no extra credential family to reason about, no operator surface for a feature nobody
  asked for.
- **Why that reversed:** the reasoning expired rather than being shown wrong. A surface every
  deployment was expected to turn on is an option in name only, and the flag charged its cost to
  exactly the sessions this design exists for — a station that is off is a session with no durable
  memory and no task list, which is §11.1's decay with a switch in front of it.
- **Why the variable was KEPT rather than deleted — and here, too, the COMM analogy is only partial.**
  COMM already has a runtime "off" state that no edit can remove: an unopenable `comm.db` degrades
  into it on purpose, so that an expendable database can never take the durable knowledge base down.
  Deleting `KEN_COMM_ENABLED` would not delete that state, only the operator's *control* of it —
  their one remedy if COMM misbehaves in production. Stations have no such degraded mode; their state
  is in `ken.db`, which Ken cannot run without. The switch stays anyway, so an operator whose station
  surface misbehaves has a remedy that is not "downgrade Ken".
- **What the flag does and does not gate — stated precisely, because the COMM analogy does not
  transfer cleanly.** COMM creates `comm.db` lazily under its flag, so its tables genuinely do not
  exist when it is off. Stations put durable state in `ken.db`, whose migrations are unconditional —
  so **the tables ship and stay empty**. That is what `COMPATIBILITY.md`'s forward-compatible-schema
  rule permits, and an empty table costs a snapshot nothing. The flag gates the *tool surface*, the
  *connect-time instructions* and the *console page*.
- **Trade-off accepted:** two flags that now default the same way and therefore read as one switch.
  They are not one, and the independence is the older and more important half of this decision —
  stations work with COMM **off** (a solo session with no peers still gets durable memory and a task
  list), and COMM works with stations off, unchanged (§12). `KEN_COMM_ENABLED=0` must leave stations
  entirely working; anything that gates the notebook or the task list behind messaging contradicts
  this decision.

### S3 — Only a human creates, names, publishes, renames, archives or reassigns a station *(chosen: request → approve → name)*

A session may **request** a station: a purpose, and optionally a non-binding name *hint*. The human
approves in the console and **types the name**. No tool writes `name`.

- **Why:** it is the pairing-code trick again — the property that makes Ken trustworthy is a
  *withheld* capability, not a well-behaved model. An agent that could mint durable named identity
  could mint it in a loop, and every row lands in every backup.
- **Second reason:** the name is the one field a human reads to decide anything. A name an agent chose
  is a name an agent can choose to resemble another one.
- **The name is never an address.** Routing is always by the opaque server-minted `station_id`,
  exactly as COMM routes by `endpoint_id`, or the first release ships a squattable namespace.
- **Publication is human-only too.** A station appears in another station's directory only if a human
  published it (S8) — an agent cannot advertise itself into anyone's view.
- **Releasing a name makes an archive irreversible.** Names are unique per space, so a released name
  can be taken by a new station; the console says so at the release click, and unarchive is refused
  while the name is taken (offering rename-on-unarchive).
- **The chicken-and-egg is solved by a station-less key.** A `station` key may exist with
  `station_id` NULL. Such a key can call exactly one tool — `station_request` — and nothing else. That
  is how a session with no station asks for one.

### S4 — The station owns the inbox; endpoints are credentialed readers *(chosen: claim-once with a lease)*

You cannot simultaneously have (a) messages addressed to a station, (b) the endpoint as the only
inbox, and (c) several live endpoints per station. Resolved: **the station owns the logical inbox.** An
endpoint is a credentialed *reader*. Delivery is **claim-once** — the first poller claims the message,
the claim is recorded on the row, and one ack settles it for the station.

- **Why:** it collapses three cases into one mechanism. Zero readers (the unclaimed tail), one reader
  (today's behaviour) and several readers are the same code path, so "queued for an unstaffed station"
  stops being a feature and becomes a state.
- **A claim is a LEASE, not a transfer of ownership.** A claim not acknowledged within the
  claim-lease window returns the message to the unclaimed tail and increments `delivery_count` — per
  *station*, so a redelivered message may reach a **different** reader than first saw it. A claim is
  released immediately when its endpoint is revoked, severed or swept. Without the lease, a session
  that claims and then dies strands its messages permanently and COMM's C6 safety net — *a message
  delivered but never acted upon comes back* — would be false.
- **Fan-out is rejected explicitly:** delivering one message to every reader would make COMM the
  broker §1 says it is not, would multiply the per-channel unacknowledged count, and would re-create
  exactly the shared-inbox accident the per-endpoint secret was invented to prevent.
- **Trade-off accepted, and the tool description must state it:** COMM's ordering promise weakens from
  *per channel and direction* to *per channel and direction, across the station's readers*. Two
  sessions polling one station see a **partitioned** stream; neither sees the whole order. That is the
  price of letting a second session help without severing the first.
- **Sequence keying.** For a bound endpoint the per-channel sequence keys on `(channel, sender
  station)` rather than sender endpoint, or outbound numbering restarts every reconnect. Unbound
  endpoints keep the shipped `(channel, sender_endpoint, seq)` behaviour, so the two regimes coexist
  in one index by writing the station id when there is one and the endpoint id when there is not.

### S5 — The station key is a header credential; binding uses a voucher *(chosen: never a tool argument)*

A **station key** is an `api_token` row — same table, same hashing, same revocation, same `/tokens`
listing — carrying `actor_id`, `scopes` and a new nullable `station_id`, minted with a **`kens_`**
prefix. It is presented as an `Authorization` header on `/station`, never as a tool argument. To bind
an endpoint on `/comm/mcp` without moving the key onto that surface, `/station` issues a short-lived
single-use **binding voucher** that `comm_register` accepts.

- **Why a header:** tool arguments are model output. They land in transcripts, harness logs and
  scrollback, and — via the notebook — potentially in a backup. A long-lived credential must travel
  the way the Ken token already travels: read by the transport, never spoken by the model.
- **Why the existing token table:** it already gives `secret_sha256`, `revoked_at`, `last_used_at`,
  constant-time comparison, console listing and `ken token revoke`. A new credential type would
  re-implement all of it worse. The prefix is contract, so `kens_` lands in `COMPATIBILITY.md` in the
  same change.
- **Several named keys per station** ("laptop", "vps"), each independently revocable — key-per-machine
  is what makes targeted revocation mean anything, and it is why §1 says to mint rather than copy.
- **The key must carry the same `actor_id` as that machine's comm token.** The hearsay window is keyed
  on the actor, so a different actor silently defeats `prompted_by_peer_traffic` (S9), and a marker
  that fails open without saying so is worse than no marker.
  **This is NOT enforced at mint time, and an earlier version of this line claimed it was.** Nothing
  compares the two actors, because a station is legitimately usable with COMM off (S2) and there is
  then no comm token to match — refusing would break the supported case to protect an unsupported
  one. What the tooling does instead is make the correct actor the DEFAULT and say which it chose:
  `ken station key` resolves the actor holding this deployment's comm token, names it, and refuses to
  guess when several could apply; the console offers a picker that marks comm-token holders, and the
  `/stations` key table names each key's actor and badges the ones holding no comm token.
  *(The original claim was false in a way that mattered: station keys were minted under a HUMAN actor
  while comm tokens default to `ai`, and `(kind, display_name)` is unique — so on any deployment
  following the documented setup the marker was permanently false, and the only shipped remedy was to
  mislabel an AI session's token as human.)*
  **Since 1.7.0 the mismatch is no longer silent everywhere: BINDING enforces it** (below). Minting
  under the wrong actor is still permitted and still silently defeats the hearsay marker — that half
  remains a papercut the tooling steers around. Do not read the binding check as covering both.

- **The voucher names the ONE endpoint that may redeem it.** `station_binding_voucher` takes an
  `endpoint_id`; redemption requires that exact endpoint, so using a voucher demands that endpoint's
  own secret — a separate credential the voucher does not carry. A leaked voucher is inert in
  anyone else's hands. *(As first shipped it required nothing of the redeemer at all: hash,
  single-use, expiry and station state were checked, the holder was not. "Never send a voucher over
  COMM, never write it to a file" was load-bearing security enforced by a human remembering it.)*
  - **An interim version keyed on the ACTOR, and its central claim was false.** It said a leaked
    voucher then granted nothing the credential needed to use it already granted. A comm token alone
    registers an **unbound** endpoint, which reads no station's mail; binding is exactly the
    capability it does not confer. `ken-prod-ops` found the consequence by measuring their estate:
    **six of eight stations share one actor**, because the actor is per MACHINE — right for the
    hearsay marker, wrong for this. The voucher had a *weaker* binding than the per-station key that
    mints it.
  - **Both checks stay, and they are not redundant.** The endpoint is the security property; the
    actor is the SETUP guard, catching a key minted under a different actor than the machine's comm
    token — a misconfiguration with no symptom until it silently defeats the hearsay marker. Removing
    either because "the other covers it" removes a different guarantee than the remover intends.
  - **The endpoint id is safe as a tool argument precisely because it is not a credential.** It
    NARROWS the voucher: naming an endpoint you do not control mints a voucher you cannot use. There
    is nothing to gain by lying. This is the opposite of a `station_id` argument, which would widen —
    which is why the station still comes from the header key alone.
  - **Three refusals, deliberately distinct**, because each demands a different response: wrong
    endpoint (ask for a voucher naming yours — a retry that works), wrong actor (re-mint a key from
    the console — retrying never works), and unknown/used/expired (collapsed into one string, since
    *those* protect a secret an attacker might guess). Reaching either identity refusal requires
    already holding a live 32-character voucher, so neither can be reached by guessing.
  - **Vouchers issued before the columns existed refuse rather than being grandfathered.** They carry
    NULL where redemption authorises, and the predicate compares with `=`. Vouchers live five minutes
    and an upgrade takes longer, so one in flight across the restart is already dead by arithmetic.
  - **Space is recorded but not enforced**, because it cannot discriminate yet: the station principal
    hardcodes space 1. Writing it now lets the check tighten to `(endpoint, actor, space)` with no
    further migration.

### S5a — Binding is `comm_bind`, never `comm_register` *(chosen: one path, one guarantee)*

Registration mints a credential and stops. To bind: **`comm_register` → write the secret to disk →
`station_binding_voucher` naming that endpoint → `comm_bind`.**

- **A voucher passed to registration cannot name its redeemer**, because the endpoint does not exist
  yet. That path could only ever carry the weaker guarantee, and shipping two strengths under one
  name is worse than shipping one.
- **Registration had acquired a hazard from doing two jobs.** It mints a secret shown exactly once;
  the MCP SDK discards structured output when a handler returns an error; so a failed binding
  destroyed the credential the handler had just created. That was worked around with a
  `binding_error` field — a "succeeded but did not bind" result existing only because two unrelated
  operations shared one call. Splitting them deletes the hazard rather than guarding it.
- **The forced order is the safe order.** Register, *save your secret*, then bind. The old
  one-call flow encouraged binding before the secret was ever written down.
- **The retired argument is REFUSED, not ignored.** The MCP SDK rejects unknown arguments by name, so
  a session working from an older flow gets a hard error rather than an unbound endpoint it believes
  is bound. Verified against the SDK, not assumed.

### S6 — Revocation severs; retirement does not *(chosen: two verbs, severing default)*

Every endpoint records `bound_by_station_key_id`. **Revoke** stops further binds *and* severs every
endpoint that key bound. **Retire** stops further binds and leaves live endpoints alone. There is no
third verb: "rotate" is mint-new-then-retire-old, and the console composes it.

- **Why severing is the default:** you revoke because the key leaked. A revocation that leaves the
  leaked capability running until an idle sweep notices is theatre — and traffic keeps an endpoint
  alive indefinitely.
- **"Retire does not sever" is about ENDPOINTS, and is misleading about the KEY itself.** Station-key
  authentication requires `retired_at IS NULL`, so a retired key stops working *immediately*: the
  session holding it loses its notebook, task list and locker at once. What survives untouched are the
  COMM endpoints that key already bound. So the composed rotation — mint new, retire old — cuts the
  running session's station tools at the retire step if done in that order. **The safe order is: mint
  → install in the client config → restart the session → verify the new key works → only then
  retire.** Stated because the natural reading of "non-severing" is that rotation is seamless, and it
  is not.
- **Trade-off accepted:** revoke is destructive to live work, so the console states the count before
  the click — *"this will disconnect 2 live sessions"* — and a severed endpoint's next call returns a
  **distinguishable** error so the model reports "my station key was revoked" instead of retrying.
  That does not weaken §5's unprobeability: the distinguishing error is returned **only after the
  endpoint secret verifies**, so it informs a proven holder and tells a prober nothing.
- **`ken token revoke` must compose.** A station key is an ordinary token row, so revoking it from the
  CLI or `/tokens` must take the same severing path; otherwise the SemVer-stable surface silently
  bypasses this decision.

### S7 — Durable state and expendable state, split by lifetime *(chosen: pointers run expendable → durable)*

Durable, in `ken.db`: the station, its keys, links, denials, notebook pages and revisions, tasks,
locker blobs, and the small surfacing timestamps the briefing needs. Expendable, in `comm.db`: the
message claim record (which reader took it, when, lease expiry) and a per-reader last-poll timestamp
for the console's staffed-now display.

- **`last_briefed_at` lives in `ken.db`, and the lifetime argument is withdrawn for it deliberately.**
  It looks like churn but is not: it is touched only on the handful of rows a briefing actually
  displays, at most once per staffing session (§11.4) — not per task, not per message.
  The alternative, putting it in `comm.db`, is unimplementable, because that file is opened only when
  COMM is running — which an unopenable `comm.db` degrades out of on purpose (there is no operator
  switch; that was removed in 2.0.0) — while S2 promises the task list works with COMM
  off. Aging-first surfacing is the whole point of §11, so it cannot depend on the messaging
  subsystem.
- **The pointer rule:** every cross-database pointer this design adds runs from the **expendable** file
  to the **durable** one (`comm.endpoint.station_id → ken.station.station_id`), by opaque text id.
  Never the reverse: a dangling pointer in `comm.db` is a row to drop; one in `ken.db` would be
  corruption in the file we promise to restore.
- **Three pre-existing rowid pointers predate the rule** (`endpoint.actor_id`, `endpoint.space_id`,
  `channel.owner_actor_id`) and are left alone. Under restore skew — `ken.db` restored backwards while
  `comm.db` stays current, the only direction skew occurs — a `comm.db` row whose station pointer no
  longer resolves is treated as unbound rather than as an error.
- **No durable row points at an endpoint.** A notebook revision records
  `{station_key_id, actor_id, hearsay_at_write}` — all `ken.db` facts — never `endpoint_id`, which is
  guaranteed to dangle once the sweep runs and does not exist at all with COMM off.

### S8 — A self-description is a claim, and the field name says so *(chosen: name the untrustworthiness)*

A station advertises `self_described_about` and `self_described_tags`. Never `about` beside a
`verified: false` sibling.

- **Why:** a sibling flag does not survive a harness flattening a structured result into prose, and
  once flattened the claim reads as fact. The field *name* survives every transformation the value
  does.
- **Second reason:** it matches the hearsay discipline COMM already enforces rather than inventing a
  second vocabulary for the same idea.
- **What is not a claim:** `name` (a human typed it), stamped server-side with `name_source: "human"`,
  and `published` (a human set it) — so a reader can always tell the two apart.

### S9 — Approval is per relationship *(chosen: links, not per-conversation codes)*

`station_link_request(to_station, reason)` → pending → the human approves or denies **with a reason**.
An approved **link** lets either side materialize a channel when it needs one.

- **Why:** the pairing code authorizes one *conversation*; what the human decides is that two posts may
  talk. Making the durable object match the decision removes a step per conversation without removing
  the decision.
- **The reason is shown only to the human, never delivered to the target before approval.** Otherwise
  every request is a one-shot unauthorized message channel: A cannot talk to B, but A could put 280
  characters in front of B by asking to.
- **The transitive path is marked.** A cannot create a channel, but A can talk B into requesting one to
  C, and B's request reaches the human looking like B's own idea. Every request records
  `prompted_by_peer_traffic` — whether the requesting session had received COMM traffic inside the
  hearsay window — badged in the console as *"this session was in a conversation when it asked."*
  Computed exactly like the existing `via_comm` marking.
- **Denials are durable and unprobeable.** A denial lives in `ken.db` so a human's "no" survives a
  `comm.db` loss. A re-request against a denied **unordered** pair (the link's own shape — muting an
  ordered pair would let the same relationship be re-asked from the other side) returns the ordinary
  "submitted, pending review" result and is silently dropped, so the mute cannot be probed.
- **Trade-off the human should accept knowingly:** approval widens from per-conversation to
  per-relationship. Revocation is one click and kills the live channel.
- **Honest limit:** a human who reflexively approves has converted the gate into a rubber stamp. No
  server-side design fixes that, and this document will not pretend otherwise.

### S10 — The notebook is working state, not knowledge *(chosen: a routing rule and a human-converted promotion)*

- **The rule, repeated in every write tool's description:** *would a session on a **different** station,
  months from now, want this? Then it is `kb_save` / `kb_propose_enhancement`, not a notebook page.
  The notebook is for what only this post needs, only for now.*
- **Why a rule and not a schema:** nothing structural distinguishes a durable lesson from a working
  note. Left unstated, the notebook becomes an uncurated shadow knowledge base — the exact thing Ken
  exists to prevent, rebuilt inside Ken.
- **The path out is a pending promotion, not a `kb_save` call.** `station_note_promote(page)` writes a
  **pending promotion** row in `ken.db` that the human converts in the console. It deliberately does
  not call `kb_save`: that needs the `write-draft` scope which §6 forbids on a station token, and a
  `dedup_check_token` that is HMAC-bound to the *calling token* and therefore unobtainable from
  `/station`. Routing it through the console also carries the write-time hearsay marking server-side
  — a marking the model retypes is forgeable, which is precisely what COMM's provenance work refused
  to build.
- **Never searched by `kb_search`.** A notebook page must not surface as knowledge.

### S11 — The locker is not a credential store *(chosen: declare it, bound it, narrow the promise it touches)*

The locker holds the **non-secret half of a working identity**: memory and instruction files, tool
preferences, paths, conventions.

- **Ken cannot enforce this and must not imply it can.** It cannot inspect a blob and know it is a
  secret. The tool description states the rule, the console shows what is stored, and the human can
  look. That is a documented expectation, not a control.
- **Which means `BACKUP.md`'s promise narrows, in the same change.** It currently says no credential in
  a snapshot is replayable. The true statement is: *no credential **Ken itself stores** is replayable
  — passwords are Argon2id, token secrets (station keys included) are hashed, session ids are hashed.
  Notebook pages and locker blobs are opaque content Ken does not inspect; whatever an operator or an
  agent puts there is in the snapshot verbatim.* Both sentences land together or the guarantee is
  false the day this ships.
- **The rule now has an answer, which it did not when it was written.** "Never a credential here" left a
  station with nowhere to put one at all, and a prohibition with no alternative is advice a session
  eventually has to break. S13 is where credentials go.

### S12 — Caps are a backup decision; refuse, never evict *(chosen: bounded, fail-loud)*

Every byte in `ken.db` is carried by the live database **plus fourteen nightly snapshots plus the
retention-exempt pre-upgrade snapshots plus Litestream** — a cap is really cap × ~15, on disk and in
every off-box copy.

- **Why refuse:** silent eviction of a working note is data loss the session cannot see. A refusal is
  an error the model reads and reacts to.
- **Two carve-outs, stated here so §9 does not contradict them:** refuse-never-evict governs
  **content** — pages, open tasks, blobs — and the head revision is never evicted. *Revision history is
  an undo buffer, not content*, and is pruned oldest-first; so is the **terminal-task archive** beyond
  its bound, because a closed task is a record rather than working state. Those two are the only things
  this design deletes without asking, and both are stated in §9.
- **A refusal names the cap in its message.** `MCP-TOOLS.md` records that there is no machine-readable
  error vocabulary, so "typed error" would be a contract this surface cannot honour; the text is the
  contract.

---

### S13 — The vault is the credential store, and it does not pretend to be encrypted *(chosen: plaintext, audited, reversible)*

The locker's sibling, and the answer to what S11 forbids. A station may hold credentials — tokens, keys,
passwords, connection strings — in `station_vault_*`, which is a different thing from the locker in every
way that matters.

| | locker | vault |
|---|---|---|
| holds | opaque blobs | small text credentials |
| listing shows | metadata, bytes fetchable | metadata only, **never a value** |
| reads | unaudited | **every read logged**, station and console alike |
| delete | destructive | **tombstone**, value recoverable |
| overwrite | destructive | previous value kept |

**Why the values are stored in plaintext, which is the decision most worth arguing with.** Encrypting
them needs a key; the key would live in the same `ken.db`; lock and key would then travel together in
every backup. The encryption would protect nobody who can read the file, while inviting an operator to
relax a control that is not there. So the boundary is **stated instead of simulated**: confidentiality
comes from the host and the backup, and `BACKUP.md` says so in the same change — its "no credential Ken
stores is replayable" sentence is now split between what Ken **mints** (verifiers, not replayable) and
what a session **puts in a vault** (plaintext, replayable, by design).

This followed an explicit instruction, after age-encrypted snapshots cost real production pain: security
is not a functional concern of Ken, and *"a non-encrypted database up to the backup point"* is preferred
to a key-management problem that buys nothing.

**Why every write is reversible.** The condition attached to storing secrets at all was that doing so
"does not modify them or at least it is reversible". `station_locker_delete` destroys; the vault must
not. An update pushes the previous value into a bounded history and a delete is a tombstone, so a
session that overwrites the wrong name costs its human a click rather than the credential. **Restore is
console-only and has no station tool** — a session that has just destroyed something by mistake is not
the party to decide what goes back.

**Why reads are audited.** A secret store whose reads are invisible cannot answer the only question
worth asking after something leaks. The trail is bounded like everything else here, and the per-secret
read COUNT is kept exactly, so the console says *"the last 20 of 2,318"* rather than presenting 20 as
the whole story — S12's fail-loud rule applied to an audit log, and a deliberate refusal of the
notebook's silent revision pruning.

**What it is not.** Not a KMS, not multi-user, not private from the human who owns the instance — they
can read every value from `/stations`, and that read is logged like any other. The operating model this
sits in has exactly one human per instance (see `docs/DESIGN.md`); a second person with console access
is outside the threat model rather than defended against.

### S14 — A room is a human-filled set of stations; an agent cannot enlarge its own audience *(chosen: console-only membership, derived broadcast)*

Rooms give a station many-party addressing without giving it a way to widen who hears it.

- **Membership is console-only.** There is no tool that creates a room, adds a member or joins one.
  A session that wants a room asks its human in words. This is the same withheld-capability trick as
  the curation gate and as C7's pairing code: the property holds because the capability does not
  exist, not because the model is asked nicely.
- **Rooms live in `ken.db`.** A membership list is a human decision, and `comm.db` is expendable
  (S7). `comm.db` keeps a derived mirror so `Send` can check membership inside its own writer
  transaction — a check anywhere else is advisory, because a human can remove a station between an
  outer check and the insert. The mirror is replaced wholesale, never synced incrementally: a missed
  removal would leave a station able to send to a room it was taken out of, which is the failure that
  fails OPEN.
- **Broadcast is derived, not granted.** `to_room:"all"` reaches the union of the rooms this station
  is already in — exactly the set it could have addressed one room at a time. A station in three of
  those rooms receives ONE copy.
- **One body, N deliveries.** Each recipient owns its own state, redelivery count and reply deadline;
  the body is stored once, charged once against every bound, and survives until the LAST recipient
  has settled. Blanking when the first acks would rebuild the 97%-of-bodies defect from a new cause.
- **No scrollback, and it is stated in the console rather than left to be discovered.** A station
  added today sees nothing sent before it joined; one removed keeps what it was already sent. The
  audience is decided at send time, and rewriting it afterwards would mean an inbox changed because
  of something that happened later.

## 3. Model

**Station** — `{station_id (opaque text), space_id, name (human, unique per space), purpose (human),
self_described_about, self_described_tags[], published, created_by_actor, state: active|archived,
advertised_at}`.

**Station key** — an `api_token` row with `{actor_id, scopes, station_id (nullable), label,
secret_sha256, created_at, last_used_at, retired_at, revoked_at}` and a `kens_` prefix. `station_id`
NULL means the key can only call `station_request` (S3).

**Endpoint** — unchanged from COMM plus `station_id` and `bound_by_station_key_id`. Still ephemeral,
still swept, still the holder of a *reader's* credential.

**Message** — unchanged, plus `recipient_station_id` and a claim: `{claimed_by_endpoint (nullable),
claimed_at, claim_expires_at}`. **The unclaimed tail *is* the queue** — there is no separate queue
table, which is what makes S4's "one code path" true rather than aspirational. This is a `comm.db`
migration; that file is expendable and outside the contract, so it is additive and cheap.

**Link** — `{link_id, space_id, station_a, station_b, approved_by_actor, approved_at, state:
active|dormant|revoked}`. Undirected. Channels materialize from it.

**Assets**, per station, in `ken.db`:
- **Notebook page** — `{key, title, tags[], body, rev, updated_at, station_key_id, actor_id,
  hearsay_at_write}`, with bounded revision history.
- **Task** — a row, not prose; full shape in §11.3.
- **Locker blob** — `{name, bytes, sha256, content_type, updated_at}`.

**Ownership** is keyed on `space_id` plus the authorizing human actor, exactly as COMM keys it and for
the reason COMM.md §10 records: actors resolve by display name and collapse across machines, so an
actor-keyed check would reject nothing it was meant to reject.

**Concurrent writes.** Two sessions may staff one station (S4), so `station_note_write` takes an
optional `if_rev`: the write is refused if the page moved underneath it, naming the current revision.
Without a precondition the second writer silently destroys the first's page.

---

## 4. Staffing and the briefing

The station key is in project config, so the bearer is known before any tool call. The **briefing** is
returned on the first `/station` call — deliberately small, counts and the few rows that matter, never
whole assets:

```
You are staffing: prod-ops  ("production operations for the live instance")
  tasks     7 open — 2 waiting on the human, 1 overdue,
                     3 not briefed in the last 5 sessions, 1 briefed 5+ times unchanged
    t-3  due   verify the decrypt drill before deleting the plaintext archive
    t-7  human decide whether backups/ is ever synced off-box
    t-1  aging sweep the pre-1.4.1 plaintext pre-upgrade snapshots      (briefed 6x)
    …3 more
  notebook  9 pages; handoff last written 4 days and 12 activities ago
  links     promo (active), public-dev (active)
  inbox     2 unread
```

Everything else costs a second call. The briefing exists to make the *right next action* obvious, not
to page in the station's memory. **It names rows, it does not only count them** — the counts are the
remainder, and a task the human never hears named is a task that decayed (§11.4). The head's slot
allocation is fixed by §11.5, and only the rows it displays are stamped, at most once per staffing
session.

**Whether the connect-time instructions can be per-station is an open question (§13).** MCP
instructions are one string per server, so a per-station briefing in the instructions would require a
server built and cached per station. Until that is settled, identity arrives in this briefing on the
first call, which the instructions tell every session to make first.

**Handoff staleness is measured against station ACTIVITY, not the wall clock** — tasks touched, pages
edited, messages exchanged since the handoff was last written. An idle station is never stale; a busy
one goes stale fast. A handoff written only on the way out is never written, so maintaining it is a
duty of the *current* session. The page key `handoff` is a reserved convention: the briefing reads it,
transfer collides on it, and every station is expected to have one.

---

## 5. Isolation — what is enforced, and what is not

**Enforced by the server:**
- A station's assets are readable only by a key for *that* station.
- A session cannot create, name, publish, rename, archive, reassign or transfer a station, nor approve
  a link.
- A station sees in the directory only **published** stations plus those it has a link to.
- Requests and denials are unprobeable, extending COMM's house rule: expired and unknown identifiers
  are indistinguishable, denied and nonexistent targets are indistinguishable, and a muted re-request
  looks exactly like an accepted one.

**Not enforced, and said plainly so nobody assumes otherwise:**
- **"Private to the station" is per-credential, not structural.** A session holding two station keys
  bridges them and the server cannot see it. The boundary is the human's config file.
- **The human reads everything, always.** "Private" means station-to-station, never AI-to-human. The
  console ships notebook, task and locker views in the same release as the tools, and the tool
  descriptions say there is no expectation of privacy from the curator.
- **Locker contents are not inspected** (S11).
- **A reflexive approval is not a gate** (S9).

---

## 6. Tool surface (sketch)

| Tool | Purpose |
|---|---|
| `station_me` | Who am I, my links, my counts — the briefing on demand. Sets `self_described_*`. |
| `station_binding_voucher` | Exchange the header credential for a single-use voucher naming ONE endpoint, which `comm_bind` redeems. |
| `station_request` | Ask the human to create a station. The only tool a station-less key may call. |
| `station_directory` | Published stations plus those I have a link to. A session names a peer by the name its human uses; discovery is deferred until there are enough stations for a list to beat asking. |
| `station_link_request` | Ask the human to approve a relationship: `to_station`, `reason` (human-only). |
| `station_note_list` / `_read` / `_write` | Keys, titles, sizes; one page; `append`/`replace` with `if_rev`. |
| `station_note_promote` | Open a pending promotion for the human to convert (S10). |
| `station_task_add` / `_list` / `_close` / `_drop` / `_defer` | §11.8. Closing is the cheapest verb, deliberately. |
| `station_locker_*` | `list`, `put`, `get`, `delete`. Bounded; refuses over cap rather than evicting (S12). |
| `station_vault_*` | `list`, `put`, `get`, `delete` — credentials, which the locker forbids (S13). Listing never returns a value; every `get` is logged; `delete` is reversible from the console. |

### Scopes

`station` is its **own scope family**, with `station-locker` **reserved from the first release** — on
the reasoning that reserved `comm-file` beside `comm`: splitting a shipped scope later is a MAJOR,
merging two is free.

**That merge has now happened, and it is why the reservation was worth making.** The locker is gated
on `station` alone: every station key reaches it. It shipped withholdable so a key could keep notes
and tasks without storing files, and that turned out to make a station's capabilities depend on which
KEY a session happened to be handed — so "does this station have a locker" had no answer, only "does
this key". A session finding it absent could not tell a deliberately restricted key from a
misconfigured one, and the locker is precisely where a fresh session on a new machine finds what it
needs to reconstitute itself. `station-locker` stays in the vocabulary and is still written onto new
keys, so an existing key's scope list keeps describing what it can do and nothing has to migrate.

**The scope-mixing rule must be fixed in the same commit.** `station` is not in `validScopes` today, so
nothing mints yet; the hazard is what happens the moment it is added. `checkScopeMix` buckets every
non-comm scope as knowledge-base, so `station` would immediately be treated as a KB scope — allowing
`read,write-draft,propose,station` and refusing `comm,station`, which is exactly backwards. The rule
becomes: **a token holds scopes from exactly one family — knowledge-base, comm, or station — except
that `station` and `comm` may combine**, since one session staffs a post and talks from it.

`station` must **never** enter the OAuth grant set (a hosted connector must not hold "read my working
notes") nor the `KEN_DEV_TOKEN` set (it bypasses per-token rate accounting entirely).

---

## 7. Provenance — keeping stations out of the curation path

Provenance is stamped at **write** time, on COMM.md §7's precedent that provenance marking lands with
the feature rather than after it.

- Every notebook revision, task and locker blob records the writing **station key**, its **actor**, and
  whether that session was inside the hearsay window — all `ken.db` facts (S7).
- A pending promotion carries that marking to the curator, so a note written while a session was being
  told things by a peer arrives already marked as hearsay-influenced.
- Nothing on `/station` writes a curated row, promotes a version or sets staleness. `curate` remains
  required by no tool.

---

## 8. Trust and safety — the blast radius of a station key

A no-expiry credential deserves an explicit paragraph. **A leaked station key lets its holder:** bind
endpoints as that station; claim and read its queued messages; materialize channels on every
already-approved link with no human in the loop; read and rewrite the notebook, tasks and locker; and
file link and station requests in that station's name.

**It does not let its holder:** read another station's assets; approve anything; take over an existing
endpoint's secret; or re-read what another reader already acknowledged.

Mitigations: S5 (the key is never model output), S6 (revocation severs, and `ken token revoke`
composes), key-per-machine so revocation is targeted, and per-key `last_used_at` in the console so an
unused key is visible before it is a problem.

---

## 9. Bounds

Live settings; the reason is attached so nobody raises one by a hundred without meeting the ×15
multiplier from S12.

| bound | proposed | reason |
|---|---|---|
| notebook page | 64 KiB | a page larger than this is a document, not a note |
| revision history per page | 256 KiB, pruned oldest-first | an undo buffer, not an archive (S12's carve-out) |
| notebook per station | 4 MiB of **head revisions** | ~60 full pages; history is bounded separately above |
| locker blob / total | 256 KiB / 2 MiB | memory and instruction files, not payloads |
| vault secret | 8 KiB | a PEM private key fits; anything larger is a file, and files go in the locker |
| secrets per station | 64 | a vault this full is holding something that belongs elsewhere |
| vault versions per secret | 16, pruned oldest-first, **and the write reports what it dropped** | what makes an overwrite reversible; at 0 an overwrite is final |
| vault reads retained | 500 per station | the trail is bounded; the per-secret read COUNT is exact regardless, so the console can say "the last 20 of 2,318" |
| open tasks per station | 500 | a longer list is not being worked |
| task `text` / `resolution` | 512 B each | one line, by construction |
| task `detail` + `context` | 4 KiB | a task needing more than this is a notebook page with a plan |
| closed + dropped tasks retained | 2000 per station, then oldest-first pruning | the record of what was decided is worth keeping; it is not worth keeping ×15 forever (S12's second carve-out) |
| station queue (unclaimed) | 20 messages + byte cap; TTL **7 days** | "nobody is staffing it" is exactly when the 24 h message TTL is too short |
| claim lease | 15 minutes | long enough for a working session, short enough that a dead one does not strand mail |
| link requests | quota per station; exponential mute on a denied **unordered** pair (1h → 6h → 24h → 7d) | a denied relationship must not be re-asked in a loop |

**Backpressure composes rather than excludes.** The per-channel unacknowledged cap applies unchanged
to every message; the station-queue bound is an **additional** ceiling that fires first for a station
with no live reader. Both are checked in the same insert transaction, so a send is refused by exactly
one of them — and COMM's only bound on message accumulation is not weakened.

At every cap: **refuse, naming the cap in the message** (S12).

---

## 10. Operator surface

A `/stations` console page, always registered — stations are core and there is no flag to gate it
on, and it was never gated on COMM's state either, because stations work with COMM off:

- the **request queue** — station requests and link requests together, one approval model — each row
  showing requester, reason, and the `prompted_by_peer_traffic` badge;
- **name the station at approval**, and publish/unpublish;
- per-station **asset usage against the caps**, with notebook, task and locker views (locker with
  download);
- the **vault** (S13) — names, notes, sizes, revision and **read count**, never values. One reveal
  button per secret, which is a POST and shows the value once in the response rather than redirecting
  with it in a URL; the reveal lands in the same read trail a session's `station_vault_get` does,
  marked `console`. Deleted secrets stay listed as recoverable tombstones with a restore control, and
  the trail states how much of itself it is showing when it has been pruned, and each line NAMES WHO
  read the value — the actor resolved to `kind:name`, the same way the key list names an actor, because
  an audit line an operator cannot read identifies nobody. A row whose actor cannot be resolved says
  *actor not recorded* rather than showing an id that looks like an identity;
- the **cross-station task view** §11.8 requires — every open task in the space, ordered by the §11.5
  contract, `blocked_on` filtering to `human` by default, station name as a column, archived stations
  marked, and the `hearsay_at_write` badge shown exactly as S9 badges a peer-prompted request. This is
  the human's whole-pile view and the only surface where the pile is visible at once;
- the **key list** with retire / revoke and revoke's "this will disconnect N live sessions" count;
- **rename** — the one control here with no consequences anywhere else, and it is worth saying why
  rather than leaving the operator to wonder what a rename might break. **Nothing addresses a station
  by name.** Routing is by `station_id`, the `station_link_mirror` in comm.db carries ids only, a
  polled message's `from_station_name` is resolved at read time from `station`, and a task's station
  name is a JOIN rather than a stored copy — so the new name is in effect everywhere at once, no
  link, channel, key or queued message is touched, and no session has to reconnect. This is COMM.md
  §3 — *"a human-chosen name is never an address"* — paying out. The one refusal is a **collision**:
  names are unique per space, and the console reports the name that is already taken so the operator
  has something to act on. Also reachable as `ken station rename --station <name> --to <new>` for a
  headless box, where the console is the surface and the CLI is the fallback;
- **archive / unarchive** — reversible, and it means **no new mail is addressed to this post and no
  session may act as it on COMM**. Concretely: the station drops out of the room roster, so room and
  broadcast sends stop counting it as a recipient; a COMM call from an endpoint bound to it is
  refused at use with an error naming the remedy; keys stop binding; links go *dormant* rather than
  revoked; the name is held unless explicitly released (S3).

  **Everything it already holds stays.** Its notebook, tasks and locker remain readable — a retired
  post's record is still the human's record — and mail queued before the archive is left alone
  rather than tidied away. Unarchiving restores routing and sessions in one click, with the SAME
  endpoint credentials: refusal is at use rather than by revoking the endpoint, because revocation
  is one-way and would turn unarchiving into a re-registration (a new secret onto disk, a fresh
  voucher, channels re-opened).

  **What this costs, stated because it is a real loss:** parking a live conversation is no longer
  possible. Archiving cuts a running session's COMM immediately and it finds out through a tool
  error. Nothing is destroyed, and unarchive puts it back — but "archive it for now, it is still
  talking to someone" is not a thing this operation does. Before 3.5.0 archiving severed nothing
  on COMM at all: a retired post kept receiving room mail nobody could read or acknowledge, its
  permanent backlog consumed the live room's backpressure budget, and its sender got a spurious
  expiry notice naming it on every message;
- **asset transfer** — an atomic move, per asset class, in one writer transaction, **refused on any
  name collision**, returning the colliding page names so the human renames or drops them first. Since
  every station is expected to have a `handoff` page, a collision on it is the *common* case. The
  message queue never moves: it is expendable, the assets are not. (Distinct from **reassigning a
  station** to another owner or space, which moves the station itself and takes its assets with it.)

---

## 11. The task list

### 11.1 The failure being fixed is decay, not storage

Pending items live in a session's context. As a conversation grows they lose to newer material and to
compaction, so recall becomes a *memory harvest*: effortful, lossy, and biased toward the recent. The
observable symptom is that older commitments surface less and less often until they stop surfacing at
all — and nobody notices, because the thing that would have noticed is the thing that decayed.

Storage alone does not fix it. **A list I have to remember to read is the same failure one level
down.** Everything below follows from that: the list must surface itself, and it must actively
counteract recency rather than merely resist it.

### 11.2 The honest limit — stated first, because it bounds everything after it

Two links in this loop are **instruction-borne and unverifiable**, in the sense COMM.md §8 means when
it says instruction text "is not a control":

- **Capture.** Nothing makes a model call `station_task_add`. The faculty being asked to record the
  item is the same attention §11.1 says decays — the model that forgets to remind is the model that
  forgets to write it down.
- **Relay.** Ken can observe that it *generated* a briefing. It cannot observe that the model told the
  human. A tool result is not a message to a person.

This section is designed so that neither failure is silent, and so that no metric claims otherwise
(§11.4, §11.7). It cannot make either impossible, and does not pretend to — the same stance S9 takes
about a reflexive approval and S11 about a locker's contents.

### 11.3 The item

```
{ id                    short, stable, sayable out loud   — "close t-7"
  text                  one line, imperative               — what to do
  detail?               longer context: why, and what done looks like
  blocked_on            self | human | peer                — REQUIRED on add
  blocked_on_station?   when blocked_on = peer, which station
  remind_after?         a date; suppressed from the briefing head until it passes
  context?              what this arose from
  state                 open | done | dropped
  resolution?           one line, required to leave `open`
  resolution_link?      kb slug+rev · commit · URL · notebook page key · promotion id
                        — never a COMM message id (S7)
  created_at, created_by_station_key, actor_id, hearsay_at_write
  last_briefed_at, briefed_count
  deferred_until?, defer_count, last_defer_reason
  closed_at?, closed_by?, closed_reason? }
```

**The enum is defined, not just declared**, and the definition ships in the tool description because
two sessions must classify the same item the same way:

- `self` — I can act on this now; nothing external is required.
- `human` — it cannot move until the owner does or decides something.
- `peer` — another *station* owes something; name it in `blocked_on_station`.

**`blocked_on` is required on add.** It is a three-value enum and costs one token; making it optional
would put an unstated default into the human's only view (§11.8). It is also the field that earns its
place: most of a session's closing summary is this value computed by hand — *"two things waiting on
you"* — recalculated every time and therefore wrong as often as the memory it comes from.

**There is deliberately no priority field.** Priority is a number people assign once and never
maintain, and its staleness is invisible. Ordering is derived (§11.5) from facts that maintain
themselves.

**`dropped` is not `done`, and neither is final.** Abandoning a commitment is a real outcome, and
conflating it with completion destroys the list's ability to answer *"what did we decide not to do?"*.
Both require a reason, both remain readable (`station_task_list(state:)`), and
`station_task_reopen` exists because a decision to drop is sometimes wrong.

**`hearsay_at_write`** is required by §7 of every task, exactly as of every notebook revision — a
commitment created while the session was being told things by a peer is marked as such, and the badge
travels to the human's view.

### 11.4 What "surfaced" may honestly mean

The anti-decay ordering key is the most abusable thing in this design, because the obvious
implementation measures the wrong event.

**The field is `last_briefed_at`, named for what Ken can actually observe.** It is stamped when a
briefing *displays* that row — never on `station_task_list`, which is a pure query, and never on rows
the briefing merely counted. It is stamped **at most once per station key per staffing session**, so
that `station_me` ("the briefing on demand", §6) cannot re-stamp the aging clock every time a model
re-orients itself.

Why the pedantry: if a briefing stamped the whole open set on every connect, a regularly-staffed
station would show a perfect surfacing history while the human was told nothing — the aging nag would
read healthy and the feature would be inert. That is precisely the "healthy metrics, non-functional
feature" failure COMM.md §13 exists to prevent, and it would reproduce the owner's original symptom
with better plumbing and a number asserting it was fine.

So the metric is honest about its own weakness: **`briefed_count` counts briefings, not human
exposures**, and §11.7's nags say so in their wording.

### 11.5 Ordering is a contract, and the head has fixed slots

The point of the list is that its answer is *predictable*. A tunable heuristic reproduces the original
problem in a new place. So the order is stated in full:

1. **Due** — `remind_after` has passed.
2. **Blocked on the human.**
3. **Aging** — `last_briefed_at` oldest first. The clause that inverts the failure.
4. **Tie-break** — `created_at` oldest first.

**But rank order alone starves clause 3, so the briefing head has fixed slots.** Classes 1 and 2 are
*monotonic*: a passed date never un-passes, and the human-blocked pile is by definition the one not
being cleared. Ranked purely by class, they occupy the head forever and the aging clause never runs.
So the head is **up to 2 due + 2 human-blocked + 3 aging**, each slot filled by `last_briefed_at`
oldest first, with counts for the remainder. Silence — the cheapest possible human response — can
therefore no longer pin an item at rank 1 and freeze everything beneath it.

**Deferral is a surfacing rule, never a membership rule.** An item with a future `deferred_until` is
suppressed from the briefing *head* only. It stays in the open set, stays in `station_task_list`,
stays in the counts, and stays in `station_task_add`'s near-match check — otherwise the items that
have gone quiet, which are exactly the ones this feature exists for, would be invisible to the two
mechanisms meant to protect them.

### 11.6 Closing is cheapest; deferring is legitimate but leaves a trace

- **Close** takes ids and one line: `station_task_close(["t-7"], "shipped in 1.4.1")`. It accepts
  **several ids**, because closing a batch after a release is the common case and five calls is five
  chances not to bother.
- **Defer** requires a date *and* a reason, and increments `defer_count`. Deferring is a legitimate
  decision; deferring silently and repeatedly is the failure mode, so it leaves a record the briefing
  reads back.
- **Drop refuses `blocked_on: human` items** unless the call carries the human's own decision. Without
  that guard, §11.7's nag would aim the model's one destructive verb squarely at the pile the whole
  feature exists to preserve — the one that reliably accumulates and that the owner says disappears on
  him today.

### 11.7 The nags, worded honestly

Three, all computed, all in the briefing:

- **Aging** — *"3 not briefed in the last 5 sessions."* Expressed in **station activity**, not
  wall-clock days, for the same reason §4 measures handoff staleness that way: an idle station is not
  neglecting anything.
- **Briefed without progress** — *"2 briefed 5+ times, unchanged and never deferred."* The sharper
  signal. An item repeatedly put in front of a session and never actioned, never deferred and never
  closed is either not real or blocked on something nobody has named. The wording says *briefed*, not
  *raised*, because Ken does not know whether the human ever heard it.
- **Deferred repeatedly** — *"1 deferred 3 times."* The trace §11.6 promises, made visible.

None of the three recommends deletion of a human-blocked item (§11.6).

### 11.8 The human's view is cross-station, and ordered like the list

Per-station lists answer the AI's question. They do not answer the human's — *"what is everyone
waiting on me for?"* — which is the pain that motivated the feature. The console therefore carries a
**cross-station view of all open tasks**, with `blocked_on` as a filter that **defaults to `human`**
rather than as the query, so the whole pile remains reachable.

It is ordered by **the §11.5 contract applied across stations** — due, then aging, then created — with
the station name as a column. Ordering it by recent station activity would sink the old items on the
one surface built to stop exactly that.

Archived stations' tasks appear, marked, because a commitment does not stop existing when a post is
retired.

**The view needs a PULL, not just a page — added 2026-08-21.** Built for the human's question and
then left somewhere nothing pointed at, it answered that question only for a human who already
remembered to ask it. Two additions, both counts and neither a new capability:

- **A nav badge and a dashboard stat**, sourced from `CountCrossStationTasks` with the same
  predicate as the list. Rendered only when something is waiting: a permanent zero teaches the eye
  to skip the row it will one day need to notice.
- **"Showing the first 200 of N"**, when and only when the cap bit. A capped list rendered with no
  total is a silent sample — on the one page built so the human can see the *whole* pile. The vault
  trail on this same page already says "the last 20 of 2,318" for exactly this reason, and the two
  now behave alike.

**And the briefing crosses the station boundary, as a COUNT.** `station_me` gains
`waiting_on_your_human_elsewhere` — two integers and a note, never contents. A session staffs one
post and its briefing stops there, but the human does not have that boundary: a human staffing
several was told about a station only while a session for it happened to be running, and a session
whose own list was empty said *"nothing is waiting on you"* while another pile grew unmentioned.
That answer is worse than silence, because it is confidently wrong.

Three properties make it safe, and each is tested:

- **No contents.** No task text, no station names, no ids — §S6 says a station key does not let its
  holder read another station's assets, and two integers are not assets.
- **A pure read.** Nothing stamps `last_briefed_at`. The caller does not staff those stations and
  cannot relay their contents, so marking them briefed would record a briefing that never happened
  and suppress the item for the session that could actually give one.
- **It counts what is RECORDED, not what is owed.** `blocked_on` is written once at creation and
  nothing revisits it, so some of these are already done. The caller cannot check — the state
  belongs to stations it does not staff — so the note tells it to send the human to `/stations`
  rather than assert the debt. This is §11.5's warning applied to a figure the session cannot verify
  at all.

**Why a count and not the tasks.** A visibility-gated cross-station read — filtered to published or
linked stations — would return a partial pile indistinguishable from a complete one, which is this
project's named defect manufactured deliberately. A count is either right or absent.

### 11.9 Tool surface

| Tool | Notes |
|---|---|
| `station_task_add(text, blocked_on, remind_after?, detail?, context?)` | `blocked_on` required. Returns the id and any near-matches from the open set. There is no merge verb: a duplicate is closed with a resolution naming the id kept — normally the row just added, since the older one carries the age §11.5 orders by (§11.10). |
| `station_task_list(blocked_on?, due?, aging?, state?, limit?)` | §11.5 order, compact; 50 default and hard ceiling. A pure query: stamps nothing. |
| `station_task_close(ids[], resolution, resolution_link?)` | Batch. |
| `station_task_defer(task_id, until, reason)` | Deliberately the wordiest call here, and the only single-id verb: one date and one reason cannot be true of a batch, and a batch defer is how a whole list gets pushed out with "later". |
| `station_task_drop(ids[], reason)` | Refuses `blocked_on: human` without the human's decision. |
| `station_task_reopen(ids[], reason)` | Because dropping is sometimes wrong. |

The descriptions must carry **four** sentences, and the fourth is the one that makes the feature work
rather than merely exist:

1. *Add is cheap — add the moment you say "we should", not at the end.*
2. *Close the moment the thing is done, not at the end of the session.*
3. *If an item has been briefed repeatedly and nothing changed, say what is blocking it or defer it
   with a reason — do not silently leave it, and do not drop something the human owes.*
4. **In your first message of a session, tell the human in words every item blocked on them and
   everything past its date.** A briefing the model reads and does not relay is the original failure
   with extra steps.

### 11.10 What is deliberately absent

- **No priority field** (§11.3), **no subtasks** (a task needing decomposition is a notebook page with
  a plan, whose parts are tasks), **no assignee beyond `blocked_on`** (there is one human).
- **No merge verb, and no edit verb at all.** Near-matches (§11.9) are returned so a duplicate is
  *noticed*, not so it can be folded: nothing is forgotten by closing the duplicate with a resolution
  naming the row that survived, and `station_task_close` already requires that line. A merge would be
  the first verb to rewrite a commitment's own wording, and it would have to choose which `created_at`
  survives — keeping the newer one silently undoes the anti-recency ordering §11.5 exists to
  guarantee. **The residue is named rather than hidden:** a duplicate `blocked_on: human` can only
  leave `open` as **done**, because `station_task_drop` refuses it without the human's decision
  (§11.6) — so the record says *done* where *never was a separate thing* would be truer. If that
  proves to cost something the answer is a fourth terminal outcome on the existing close path, which
  is a `state` CHECK change and therefore ships on its own — not an edit verb.
- **No checklists.** They are a different shape: a *finite procedure*, instantiated per run, valued for
  **completeness**, and reset — a release gate is one. A task list is open-ended, actor-owned and
  valued for **not forgetting**. One thing that tries to be both is bad at each: steps that outlive
  their run, or commitments that get "reset". If checklists earn their place they arrive later as
  templates + runs, sharing no schema with this.

---

## 12. Migration — and the statements elsewhere that must change

### Adopting it where sessions are already running

The order matters, and only the last step involves the sessions themselves:

1. **Nothing to enable.** Stations are core and on by default (S2): the upgrade exposes
   `/station/mcp` and the `/stations` console, unconditionally — there is no opt-out. The
   schema was already there — migrations are unconditional — so that is all the upgrade adds.
2. **Create a station per working identity, naming each one yourself**: `ken station add --name
   prod-ops`. Or leave this until a session asks with `station_request` and approve it in the console;
   either way a human types the name (S3).
3. **Mint one key per MACHINE, never per person, and never copy one**: `ken station key --station
   prod-ops --label vps`. Key-per-machine is what makes revoking a single one mean anything (S5).
4. **Put the key in that machine's MCP client config as an `Authorization` header** — never in a
   prompt, never as a tool argument (S5). The session now has a notebook, a task list and a locker
   immediately; nothing further is required for those.
5. **Only if you want the messaging half:** the session calls `station_binding_voucher` on `/station`
   **naming its own `endpoint_id`**, and passes the voucher to **`comm_bind`** on `/comm`. It keeps its
   endpoint id, its secret and every channel it is in — adoption costs no re-pairing. A session with no
   endpoint yet calls `comm_register` first, writes the secret to disk, and then does the same.

**What each step buys, so none of it is done on faith:** after step 4 a session has durable memory
and a task list that survives it. After step 5 the STATION owns its inbox, so a replacement session
inherits unread mail — which is the only step that makes losing a session cheap. Approving a **link**
(S9) between two stations is what then lets either open a channel with no pairing code.

**An endpoint binds once and cannot move between stations.** Binding one that has no station carries
nothing across; re-pointing a bound one would carry the first station's unread mail into the second,
which is the shared-inbox accident in a new costume. Register a new endpoint if a session genuinely
needs a different station.

---



1. **`comm_register` with no station key stays valid indefinitely.** Binding is an *identity*, never a
   requirement. Existing endpoints keep working, unbound.
2. **Pairing codes and `comm_join` are not deprecated.** Links are an addition; a human who prefers to
   authorize one conversation at a time may keep doing so.
3. **An open channel between two *unbound* endpoints is untouched.** The moment an endpoint binds a
   station, that direction's sequence keys on the station and the ordering promise weakens as S4
   states — so this is not "nothing changes", and the upgrade note must say which.
4. **The console gains a page**, so the upgrade note says so rather than letting an operator discover
   it.

**Statements elsewhere that become false and must be edited in the same change:**

| document | statement | becomes |
|---|---|---|
| `BACKUP.md` | "no credential in it is replayable" | narrowed to credentials **Ken stores** (S11) |
| `COMPATIBILITY.md` | token prefix list `ken_` / `kenc_` | gains `kens_` |
| `COMPATIBILITY.md` | scope list | gains `station`, reserves `station-locker` |
| `COMM.md` §1/§3 | "a channel joins exactly two endpoints" | two endpoints, each optionally bound to a station; links materialize channels |
| `COMM.md` §4 | per-channel ordering and `delivery_count` are per endpoint | per station for bound endpoints (S4) |
| `COMM.md` §6 | a token holds comm scopes or knowledge-base scopes | three families; `station` + `comm` may combine (§6) |
| `MCP-TOOLS.md` | the two-surface description | gains `/station` |

---

## 13. Open questions

- **Can the connect-time instructions be per-station?** MCP instructions are one string per server, so
  this needs a server built and cached per station, selected from the authenticated principal. Worth
  doing — identity arriving without a tool call is most of the ergonomic win — but not yet settled, so
  §4 falls back to the briefing.
- **Does the claim need a reader hint?** Claim-once means whichever session polls first takes the item.
  If a long-running session starves a fresh one, the additive fix is an optional hint on send.
- **Should a station be able to have no key at all** — existing only to be addressed, staffed by
  nobody? Coherent, possibly useful as a shared inbox, deferred until wanted.
- **How does a session say it is finishing?** Archive is a human act; a session that knows it is ending
  has no way to say "handoff written, I am done" beyond writing the page.
- **Multi-human.** Ownership is keyed on `space_id` plus the authorizing actor from day one and both
  request flows are two-sided in shape, so invitations across humans stay additive — the same stance
  COMM.md §10 takes, for the same reason.
