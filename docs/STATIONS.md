# Ken — stations, notebooks and task lists

> **Status: DESIGN CONTRACT — not yet built.** Written before the code, the way [`COMM.md`](COMM.md)
> was, so the decisions are argued while they are still cheap. When it ships it will be **opt-in and
> off by default** (`KEN_STATION_ENABLED`), which places it outside the byte-level compatibility
> contract ([`COMPATIBILITY.md`](../COMPATIBILITY.md)) exactly as COMM is — supported, but free to
> evolve additively.
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

### S2 — Off by default *(chosen: opt-in, `KEN_STATION_ENABLED`)*

- **Why:** the same reasoning as COMM's C2. A default install must remain exactly the curated
  knowledge base the README promises — no extra operating loop in every agent's connect-time
  instructions, no extra credential family to reason about, no operator surface for a feature nobody
  asked for.
- **What the flag does and does not gate — stated precisely, because the COMM analogy does not
  transfer cleanly.** COMM creates `comm.db` lazily under its flag, so its tables genuinely do not
  exist when it is off. Stations put durable state in `ken.db`, whose migrations are unconditional —
  so **the tables ship and stay empty**. That is what `COMPATIBILITY.md`'s forward-compatible-schema
  rule permits, and an empty table costs a snapshot nothing. The flag gates the *tool surface*, the
  *connect-time instructions* and the *console page*.
- **Trade-off accepted:** two opt-in subsystems. They are independent on purpose — stations work with
  COMM **off** (a solo session with no peers still gets durable memory and a task list), and COMM
  works with stations off, unchanged (§12).

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
  on the actor, so a different actor silently defeats `prompted_by_peer_traffic` (S9). A marker that
  fails open without saying so is worse than no marker, so the console refuses the mismatch at mint
  time.

### S6 — Revocation severs; retirement does not *(chosen: two verbs, severing default)*

Every endpoint records `bound_by_station_key_id`. **Revoke** stops further binds *and* severs every
endpoint that key bound. **Retire** stops further binds and leaves live endpoints alone. There is no
third verb: "rotate" is mint-new-then-retire-old, and the console composes it.

- **Why severing is the default:** you revoke because the key leaked. A revocation that leaves the
  leaked capability running until an idle sweep notices is theatre — and traffic keeps an endpoint
  alive indefinitely.
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

- **`last_surfaced_at` lives in `ken.db`, and the lifetime argument is withdrawn for it deliberately.**
  It looks like churn but is not: one timestamp per task, touched once per briefing — not per message.
  The alternative, putting it in `comm.db`, is unimplementable, because that file is opened only under
  `KEN_COMM_ENABLED` — and S2 promises the task list works with COMM off. Aging-first surfacing is the
  whole point of §11, so it cannot depend on the messaging subsystem.
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

### S12 — Caps are a backup decision; refuse, never evict *(chosen: bounded, fail-loud)*

Every byte in `ken.db` is carried by the live database **plus fourteen nightly snapshots plus the
retention-exempt pre-upgrade snapshots plus Litestream** — a cap is really cap × ~15, on disk and in
every off-box copy.

- **Why refuse:** silent eviction of a working note is data loss the session cannot see. A refusal is
  an error the model reads and reacts to.
- **The one carve-out, stated here so §9 does not contradict it:** refuse-never-evict governs
  **content** — pages, tasks, blobs — and the head revision is never evicted. *Revision history is an
  undo buffer, not content*, and is pruned oldest-first. It is the only thing this design deletes
  without asking.
- **A refusal names the cap in its message.** `MCP-TOOLS.md` records that there is no machine-readable
  error vocabulary, so "typed error" would be a contract this surface cannot honour; the text is the
  contract.

---

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
- **Task** — a row; shape delegated (§11), fixed points there.
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
You are staffing: prod-ops  ("production operations for kb.quest.mx")
  tasks     7 open — 2 waiting on the human, 1 due today, 3 not surfaced in 30+ days
  notebook  9 pages; handoff last written 4 days and 12 activities ago
  links     promo (active), public-dev (active)
  inbox     2 unread
```

Everything else costs a second call. The briefing exists to make the *right next action* obvious, not
to page in the station's memory.

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
| `station_bind` | Exchange the header credential for a single-use binding voucher for `comm_register`. |
| `station_request` | Ask the human to create a station. The only tool a station-less key may call. |
| `station_directory` | Published stations plus those I have a link to. |
| `station_link_request` | Ask the human to approve a relationship: `to_station`, `reason` (human-only). |
| `station_note_list` / `_read` / `_write` | Keys, titles, sizes; one page; `append`/`replace` with `if_rev`. |
| `station_note_promote` | Open a pending promotion for the human to convert (S10). |
| `station_task_*` | Delegated shape (§11). |
| `station_locker_*` | `list`, `put`, `get`, `delete`. Bounded; refuses over cap. |

### Scopes

`station` is its **own scope family**, with `station-locker` **reserved from the first release** — on
the reasoning that reserved `comm-file` beside `comm`: splitting a shipped scope later is a MAJOR,
merging two is free.

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
| open tasks per station | 500 | a longer list is not being worked |
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

A `/stations` console page, registered whenever `KEN_STATION_ENABLED` is set and gated on that flag
alone — never on `KEN_COMM_ENABLED`, because stations work with COMM off:

- the **request queue** — station requests and link requests together, one approval model — each row
  showing requester, reason, and the `prompted_by_peer_traffic` badge;
- **name the station at approval**, and publish/unpublish;
- per-station **asset usage against the caps**, with notebook, task and locker views (locker with
  download);
- the **key list** with retire / revoke and revoke's "this will disconnect N live sessions" count;
- **archive / unarchive** — reversible: keys stop binding, live endpoints are severed, links go
  *dormant* rather than revoked so unarchiving restores them, and the name is held unless explicitly
  released (S3);
- **asset transfer** — an atomic move, per asset class, in one writer transaction, **refused on any
  name collision**, returning the colliding page names so the human renames or drops them first. Since
  every station is expected to have a `handoff` page, a collision on it is the *common* case. The
  message queue never moves: it is expendable, the assets are not. (Distinct from **reassigning a
  station** to another owner or space, which moves the station itself and takes its assets with it.)

---

## 11. The task list — brief for the delegated design

The delegated design owns the *shape* of a good task list for an AI. These points are settled:

1. **The failure being fixed is decay, not storage.** Pending items live in a session's context; older
   ones lose to newer material and to compaction, so recall is effortful, lossy and recency-biased.
   Surfacing is therefore **aging-first** — items not raised recently outrank new ones — and happens in
   the briefing, unasked.
2. **`blocked_on` is an enum of slugs: `self | human | peer`** — no spaces, since the value freezes as
   contract — plus a client rule for unknown values so the vocabulary can grow additively.
3. **A resolution link may point at a knowledge-base slug + revision, a commit, or a URL — never a COMM
   message id.** That would be a `ken.db` row pointing into the expendable file, violating S7 in the
   direction that matters.
4. **`last_surfaced_at` lives in `ken.db`** (S7): one timestamp per task, touched once per briefing,
   and it must work with COMM off.
5. **Closing must be cheaper than snoozing.** If deferring is the low-effort path, everything is
   deferred and the list rots into the thing it replaced.

To decide: item shape and required fields, what "done" records, how duplicates are avoided without a
search, how ordering is expressed as a contract rather than a heuristic, and what the tool descriptions
must say so a model closes items without being told twice.

**Checklists are out of scope and are a different shape** — a finite procedure, instantiated per run,
valued for completeness, and reset. A task list is open-ended, actor-owned and valued for not
forgetting. One thing that tries to be both is bad at each; if checklists earn their place they arrive
later as templates + runs.

---

## 12. Migration — and the statements elsewhere that must change

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
