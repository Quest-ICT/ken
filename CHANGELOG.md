# Changelog

All notable changes to **Ken** are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A behavioral change, feature, or design decision lands with its CHANGELOG entry in the
same change — never "docs later".

> **Releases before 1.1.0 predate this public repository.** Ken was developed privately
> through the 0.x line; those entries are kept because the *why* behind each decision is
> the useful part, but they have no public tag or downloadable release. A `1.0.0-rc.1`
> release candidate was run internally and superseded before a 1.0.0 was finalized, so
> the first public release is **1.1.0**.

## [Unreleased]

### Added

- **`comm_poll` reports the wait it actually granted.** `wait_seconds_granted` is what
  the call was prepared to block for after the server's cap; `wait_clamped_from` appears
  only when yours was shortened, carrying the value you asked for.

  The tool description told sessions to prefer one long wait over frequent short polls,
  the value was capped server-side, and the result never mentioned it. `ken-prod-ops`
  passed `120` for a week believing they were asking for two minutes. A parameter that is
  accepted, silently ignored and never spoken of again is the same shape as a remedy that
  is inert — nothing distinguishes it from one that worked. The description now names the
  two fields that carry the truth.

  Reported unconditionally, including when messages were already waiting and no blocking
  happened, so a caller checking whether `wait_seconds` means anything does not have to
  arrange an empty inbox to find out.


### Fixed

- **The hearsay badge claimed more than it detects.** Its tooltip said "the session that
  wrote this had recently received a message" — but `ReceivedSince` is keyed on the
  ACTOR, and an actor covers every session on a machine. `ken-prod-ops` measured eight
  endpoints under one actor on the live deployment, so the badge means *an agent under
  this identity was talking recently*, not *this writer relayed something*. The wording
  now says what was observed and names the gap; the label keeps its question mark, which
  was always doing the hedging the tooltip failed to.

  It also means the badge is nearly always on where several sessions share a machine —
  and a badge that is never absent carries no information. Narrowing it to the writing
  session needs the knowledge-base and messaging identities to be the same one, which is
  the one-identity work.

- **The consent picker defaulted to the identity that cannot be marked.** Actors holding
  a messaging token now come first and the first is pre-selected; "a new identity named
  after this application" moved to the bottom.

  Prod's grant chain is the evidence: `id=7` was approved with the picker on its first
  option, resolving straight back to the dead actor, and `id=8` was the corrected retry.
  The option text was accurate and its help said plainly that marking would never happen
  — it was simply first, on a screen someone was clicking through to restore access they
  had just cut off. **A default that must be argued against on every use is a defect in
  the default, not in the reader.**


### Fixed

- **`ken_prune_pre_upgrade` did nothing on a standard install.** It shipped in 2.0.0,
  ran nightly, logged success, exited 0, and deleted no files at all — because `find`
  does not descend into a **symlinked** starting point, and the default layout is exactly
  that: `KEN_HOME` is `/opt/ken/current`, and `current/backups` is a symlink to
  `/opt/ken/backups`.

  The `ls -1t` line two lines below it worked throughout, because shell globs *do* follow
  symlinks. The two lines look equivalent and are not, which is why reading the function
  never showed it.

  Found by `ken-prod-ops` **counting rollback points across an upgrade** — 9 → 10 → 10,
  expected 3 — rather than by inspection. A prune that deletes nothing is byte-identical
  to a prune with nothing to delete.

  The fix is `-H`. The lesson is the test: all three original fixtures pass against the
  broken code, because they built a plain temp directory. The new one reproduces the
  **layout** — a symlinked backup dir — and it is the only test that fails when `-H` is
  removed. It carries a control asserting the fixture really is a symlink, so it cannot
  quietly decay into the plain case it was written to replace.


## [2.1.0] — 2026-08-11

### Added

- **`comm_channels` reports how many messages are waiting for you**, counted without
  delivering any of them. This is what makes "check before you send" a look rather than
  a delivery: `comm_poll` was the only other way to find out, and polling stamps
  `first_delivered_at`, arms the expiry and reply clocks, and leaves the session on the
  hook for messages it was only trying to peek at. An instruction that costs a delivery
  every time you send gets skipped, and a skipped instruction is worse than none because
  it still looks like a control.

  Counts `queued` only. A delivered-but-unacked message has already been shown to you,
  so counting it would say "go and read" something you have — the mistake
  `waiting_for_you` made before it was narrowed.

- **Two connect-time instructions for COMM.** Check what is waiting before you send, and
  adjust or drop what you were about to write. And **write what you poll to a file before
  anything else** — before acting, replying or deciding — because a file survives context
  compaction, a body swept by retention, and Ken being unreachable, none of which are
  rare and none of which announce themselves.

  **`comm_ack` still means PROCESSED.** An earlier draft had sessions ack on receipt,
  which would have hijacked the word rather than used the state that already exists:
  `delivered` *is* received, set automatically on first poll. Keeping ack late also keeps
  redelivery as the self-announcing signal for unfinished work, and leaves the hearsay
  window — which keys on `COALESCE(acked_at, first_delivered_at)` — exactly where it was.


### Changed

- **Backward compatibility no longer constrains development, and the deprecation cycle is
  withdrawn.** Ken is developed to be installed fresh. When a change is better made by
  breaking something it is broken, and the version takes the MAJOR bump the rules already
  required — a MAJOR bump is ordinary here, not a failure.

  What is owed instead is [`docs/UPGRADING.md`](docs/UPGRADING.md): every break recorded
  **in the change that causes it**, saying what an operator will observe and what to do
  first, verified against the diff at release time and sent to whoever runs a deployment.
  A list assembled afterwards from commit messages is a list of the breaks somebody
  remembered.

  What this does not license is a **silent** break. A retired setting still present in a
  config should say so at runtime where it can, and a release that discards data says so
  before it is installed.


## [2.0.0] — 2026-08-11

**MAJOR because four `KEN_*` variables were removed and the snapshot artifact was
renamed.** `COMPATIBILITY.md` says removing one is MAJOR; an earlier draft excused two of
them as exceptions, and that excuse is withdrawn here rather than extended. Exempting
variables one at a time until the rule covers nothing is how a compatibility promise
stops meaning anything.

**Read this before upgrading:**

- **`KEN_AGE_RECIPIENT` is retired and snapshots are no longer encrypted.** If you set
  it, your snapshots become compressed plaintext at `0600`. The snapshot run now says so
  on every run rather than letting you find out from a file listing. Encryption,
  transport and destination are yours; see `BACKUP.md`.
- **The snapshot artifact is renamed** from `ken-<stamp>.db[.age]` to
  `ken-<stamp>.db.gz`, and the pre-upgrade rollback point likewise. **Anything that
  selects backups by name, decrypts them, or restores them needs updating first.**
- **`KEN_COMM_ENABLED`, `KEN_STATION_ENABLED` and `KEN_OAUTH_ENABLED` are gone.** All
  three surfaces are always on. Setting them has no effect.

### Fixed

- **`station_note_promote` asked a human who could never be asked.** The tool has
  written `station_promotion` rows since stations shipped, and its description told
  every session it "asks your human to convert a page" — while nothing read the table.
  No store function, no route, no template. Every request a session filed went into a
  drawer nobody could open, and the session was told it had asked.

  `/stations` now shows pending requests with the page's own text, since the decision
  cannot be made without it, and closes each as **converted** or **discarded**. Two
  warnings are surfaced that the schema had always recorded and nobody could see: the
  page was written while its station was receiving peer traffic, and the page has
  changed since the request was made — a human converting stale material into durable
  knowledge is exactly what the curation gate exists to prevent.

  **The console records the decision and never performs it.** Converting a page is a
  `kb_save`; routing it through a button would let this page write curated content,
  which is the one capability the whole design withholds. An optional entry slug is
  recorded so the trail runs from the note to the knowledge.

  This is the third instance of one shape in a week — a flag with no reader
  (`published`), a store function with no caller (`RevokeStationLink`), and now a table
  with neither.


### Changed

- **Snapshots are gzip-compressed and no longer encrypted.** `ken backup snapshot --out
  X.gz` writes a compressed artifact; `ken backup verify` reads it directly, detecting
  compression from the file's own magic bytes so a snapshot that was renamed — or handed
  over without an extension — still verifies. Compression is selected by the extension
  rather than a flag, because two scripts pass a path and then operate on it and a
  snapshot that landed somewhere else would break both silently.

  **68% off every artifact**, measured on the live deployment by `ken-prod-ops`:
  4,521,984 bytes raw against 1,484,578 gzipped. Nothing in this path compressed before,
  and `age` does not — ciphertext is incompressible, which is also why the archive could
  never deduplicate. Over two weeks their database grew 3.2 MB while the archive grew
  **46.9 MB**: the multiplier the backup applied was always the dominant term.

  **The `age` layer is retired**, along with `KEN_AGE_RECIPIENT`. Ken writes a compressed
  snapshot at `0600` and stops; transport, destination and at-rest protection belong to
  whoever moves the file. Removing it is what let compression exist at all.

- **Pre-upgrade rollback points are pruned, and until now nothing pruned them.** The
  nightly retention globs `ken-*`, which cannot match `pre-upgrade-*` — they share no
  prefix — so every upgrade left one behind permanently. `ken-prod-ops` measured nine:
  19.6 MB, **30% of the entire archive**, the oldest from the day the box was built.

  A rollback point now survives if it is among the newest `KEEP_PRE_UPGRADE` (default 3)
  **or** younger than `KEEP_PRE_UPGRADE_DAYS` (default 7) — whichever keeps more. Both
  floors are required and both failure modes were measured inside one thirteen-day
  window: a count alone fails during a **burst** (four upgrades in a day evicts the point
  taken before that day's work began, which is the one you want when that day is what
  broke things), and an age bound alone fails during a **drought** (255 hours with no
  upgrade would have left none at all).


### Changed

- **Nothing in Ken is optional any more.** `KEN_COMM_ENABLED`, `KEN_STATION_ENABLED`
  and `KEN_OAUTH_ENABLED` are gone. The first two were opt-in, then briefly opt-outs;
  the third defaulted to **off**, which meant a fresh install could not be connected
  the documented way — one registration on the account, reachable from every client —
  until the operator found a variable nothing pointed them at.

  A switch nobody is expected to use still costs a hedge in every document, every tool
  description and every connect-time instruction, and hedges rot: 1.7.0 shipped a COMM
  instruction opening "opt-in; off by default", false the moment it was released, in
  the one place read by a machine on every connection rather than by a human once.

  **The degraded state is not a switch and it stays.** An unopenable `comm.db` still
  leaves messaging unavailable while the knowledge base runs — that is what makes "COMM
  may fail; the KB stays UP" true rather than aspirational. What is removed is the
  operator's ability to *choose* it. `nil` now means exactly one thing, which is a
  small win: the console no longer has two indistinguishable causes to report.

### Added

- **Every session is now told it has no clock.** Delivered in the knowledge-base
  instruction block, so it reaches sessions that never touch messaging or stations —
  a session writing "this was fixed a few weeks ago" into an entry commits a drifting
  number to the durable record, in the same confident voice as the measured figures
  beside it, and the human promoting it cannot see which is which.

  The wording was chosen by trial rather than by taste. Three drafts, each handed to
  fresh agents as their connect-time instruction along with tasks that invite an
  unmeasured time claim; every draft that ran produced an agent that went and read a
  clock. One reported what it would otherwise have written — *"a couple of hours and
  the logs go back about two weeks"* — and added that "about two weeks" would have been
  roughly right **by luck**, with no way to know that from the inside.

  Two failure modes the trials exposed are answered explicitly, because drafts that
  only forbade unmeasured *durations* did not catch them: a claim with the number
  hidden ("recently", "long-standing"), and a real timestamp welded to an unverified
  assumption about the interval.


## [1.7.0] — 2026-08-10

### Changed

- **The locker belongs to every station; it is no longer a withholdable scope.** The
  server gates it on `station` alone. It shipped behind its own `station-locker` scope
  so a key could keep notes and tasks without storing files — which made a station's
  capabilities depend on which KEY a session happened to be handed, so "does this
  station have a locker" had no answer, only "does this key". A session finding it
  absent could not distinguish a deliberately restricted key from a misconfigured one,
  and the locker is exactly where a fresh session on a new machine finds what it needs
  to reconstitute itself.

  `station-locker` stays in the vocabulary and is still written onto new keys, so an
  existing key's scope list keeps describing what it can do and nothing migrates. This
  is the merge `COMPATIBILITY.md` reserved the pair for — "splitting a shipped scope is
  a MAJOR, merging two is free". The console's per-key locker checkbox is gone, and
  `ken station key --locker` is accepted as a no-op that says so rather than failing a
  script over a flag that now grants what is granted anyway.

### Changed

- **COMM and stations are CORE, on by default.** `KEN_COMM_ENABLED` and
  `KEN_STATION_ENABLED` no longer have to be set to `1` to switch a surface on; both
  default to **on** and the variables survive **inverted**, as opt-*outs*. `=0` (also
  `false`/`off`/`no`) turns a surface off; `=1` still means on, so a deployment that
  opted in under the old meaning keeps working across the upgrade; an unrecognised
  value leaves the surface **on**, because a typo must not silently disable core
  functionality. The two remain independent — `KEN_COMM_ENABLED=0` leaves stations
  fully working, since a notebook and a task list need no peers (STATIONS.md S2).

  The opt-outs were kept rather than deleted. Ken already has a runtime "COMM off"
  state: an unopenable `comm.db` degrades into it deliberately, so an expendable
  database can never take the durable knowledge base down. Removing the variable would
  not remove that state, only the operator's control of it — their one remedy if COMM
  misbehaves in production.

  The reversed decisions are recorded as reversals, not deleted: `COMM.md` C2 and
  `STATIONS.md` S2 now state what was decided, what changed, and why. Both reasons
  expired rather than being shown wrong — a surface every deployment was expected to
  turn on is an option in name only.

- **The `comm_*` and `station_*` surfaces stay OUTSIDE the byte-level compatibility
  contract, for a new reason.** `COMPATIBILITY.md` excluded them for being
  optional-and-off-by-default; that justification is gone, and the exclusion is not.
  It now rests on the surface being **mid-redesign**: remaining work removes
  notice-messages, replaces pairing codes and channel-pair addressing with rooms and
  name-addressed send, and retires the channel — the central noun of the current tool
  surface. Freezing now would make that redesign a MAJOR bump or force a release cycle
  of deprecated aliases. **They are promoted into the contract when COMM v2 lands**,
  which is stated in the document as the trigger rather than left implicit.

### Security

- **The station binding voucher is now usable only by the endpoint it names.** As
  shipped, redemption checked the voucher hash, the single-use flag, the expiry and
  the station's state — and nothing at all about who presented it. The string alone
  bound any endpoint to the station's inbox, where it reads the station's mail and
  takes messages out of another reader's poll. The only control was a human
  remembering "never send a voucher over COMM, never write it to a file"; that rule
  was load-bearing security.

  `station_binding_voucher` now takes the `endpoint_id` that will redeem it, and
  redemption requires that exact endpoint (migration `0015_voucher_nominates_endpoint`).
  Redeeming therefore needs that endpoint's own secret — a separate credential the
  voucher does not carry — so a leaked voucher is inert in anyone else's hands.

  **An interim fix keyed on the ACTOR instead, and the claim that accompanied it was
  wrong.** It said a leaked voucher then granted nothing that the credential needed to
  use it already granted. That is false: a comm token alone registers an *unbound*
  endpoint, which can read no station's mail — binding is precisely the capability it
  does not confer. `ken-prod-ops` found the consequence by measuring rather than
  reading, on an estate where **six of eight stations share one actor**, because the
  actor is per machine. The voucher had ended up with a *weaker* binding than the
  per-station key that mints it. The actor check remains, as a setup guard rather than
  the security property — it catches a station key minted under a different actor than
  the machine's comm token, a misconfiguration with no other symptom until it silently
  defeats the hearsay marker.

  Vouchers minted before the upgrade carry no issuing identity and are **refused**
  rather than grandfathered. They live five minutes and an upgrade takes longer, so one
  in flight across the restart is already dead by arithmetic.

  **There were no tests over vouchers at all** — not the bearer defect, not single-use,
  expiry, or the archived-station refusal. There are now, each two-sided, and the two
  identity checks are mutation-verified independently so neither can be silently
  covering for the other.

### Fixed

- **The hearsay marker could never fire for anything written through an OAuth
  connector.** `viaComm` asks whether *this actor* recently received inter-session
  traffic. An OAuth grant's authoring actor was created here from the connector's
  self-reported display name, while COMM traffic arrives under the actor a `comm`
  token was minted with — different rows, by construction, always. So a session that
  read a peer's message and then saved what it learned through the connector produced
  `via_comm=NULL`, and an absent badge is indistinguishable from a checked-and-clean
  one.

  This is the surface a human uses from mobile and from Claude chat, and — confirmed
  from inside a Claude Code session — the connector reaches Claude Code too, alongside
  any locally-added registration. The marker was not merely unreliable there; it was
  structurally incapable of firing.

  The consent screen now lets the approving human choose **which identity the
  connector authors as**, offering live actors with the ones holding a messaging token
  marked — the same question, and now the same candidate list, as "which actor is this
  station key minted under". Leaving it on the default reproduces the old behaviour
  exactly. A chosen id is validated against the actor table rather than trusted, since
  authorship is what a human reads when deciding whether to promote.

  Pointing several machines at one actor makes the marker **over**-report, and that is
  the correct bias, stated where the marker was designed: a false negative silently
  launders hearsay into the knowledge base, a false positive only asks a human to
  check a source.

  **Entries already written through a connector keep `via_comm=NULL` and cannot be
  repaired** — nothing recorded what those sessions had been reading. The fix is
  forward-looking, and that limit is stated rather than implied.

- **A tool handler acted as whoever opened the MCP session, not as the caller.** The
  go-sdk binds a session to the *initialize* request's context, so anything a handler
  read from context was frozen at connect. Demonstrated through the real HTTP handler:
  a `kb_save` presented with token B, on a session opened by token A, was written with
  **A** as `author_actor_id` and returned 200. Ken records that field on every version
  and a human reads it when promoting, so the durable record carried false provenance
  with nothing on the page to say so. Fixed at the tool-registration wrapper, the only
  place the SDK exposes a per-call header.

  **Now fixed on `/station/mcp` too**, where it is worse in kind: a station key *is*
  the station, so a stale principal meant one post writing into another's notebook,
  closing another's tasks, and reading another's locker — the three things stations
  exist to keep separate. `/comm/mcp` never had it: its identity arrives as tool
  arguments, which is per-call by construction.

- **MCP sessions never expired.** Every handler passed nil options, and the SDK's zero
  `SessionTimeout` means idle sessions are never closed. Now 30 minutes on all three
  surfaces — comfortably longer than the longest parked `comm_poll`, so a session
  waiting on mail is never what times out.

### Fixed

- **The settings console was teaching a data model the release had removed.** The form
  resolves each field's label and help through the translation bundle FIRST, with the
  Go registry text only as a fallback — so the bundle *overrides* the registry rather
  than mirroring it. 1.6.0 renamed and rewrote registry entries and left the bundles
  alone. Five fields drifted, seven entries; the worst was
  `comm_metadata_ttl_sec.help`, which stated "message bodies are deleted at
  acknowledgement regardless" — the exact behaviour that release existed to stop — as
  current fact, in three languages. The fields *added* in 1.6.0 rendered correctly
  precisely because nobody had translated them yet.

  English `settings.field.*` entries are now **generated** from the registry
  (`go run ./internal/i18n/i18nsync`), with a test that regenerates and diffs, so
  English drift cannot be merged. Non-English entries carry a `#@src` fingerprint of
  the English they were translated from; when the registry changes, the test fails
  naming the key, the language and both texts. Nothing else can see this — a
  translation is *supposed* to differ from its source, so no string comparison
  distinguishes a good one from a stale one.

- **Validation errors named fields as the code calls them, not as the form shows
  them.** The refusal for an incoherent TTL pair said "Lower Lifetime after delivery
  first" while the field on screen was labelled "Message lifetime", so the operator
  was told to change something that appeared nowhere on their page. Errors now carry
  the field *key* and are resolved at render time exactly as the form resolves it.
  This misnamed 2 of 43 fields for an English operator and **31 of 43 for every
  Spanish or French one**, since those bundles translate the labels and could never
  match a literal English string. The reason text is still English — a known gap,
  recorded in `docs/I18N.md` rather than implied away.

- **Two settings group headings never resolved,** both invisible in English because the
  lookup falls back to the group's display name. `Stations` had no key in any bundle;
  `Inter-session comms` had one in all three that could not be reached, because the key
  derivation collapsed spaces but not hyphens. Their Spanish and French headings had
  been sitting in the files unused since they were written.

### Changed

- **`comm_register` no longer binds to a station; use `comm_bind`.** Passing
  `binding_voucher` to `comm_register` is now **refused with an error naming the
  argument**, never ignored — a session working from an older flow is told, rather than
  left holding an unbound endpoint it believes is bound. The COMM surface is outside
  the byte-level compatibility contract (see `COMPATIBILITY.md`), so this is not a
  MAJOR change; it is called out here because it is the one change in this release a
  running session can notice.

  Two reasons, and the second is the stronger. **A voucher passed to registration can
  never name its redeemer**, because the endpoint does not exist yet — that path could
  only ever have had the weaker guarantee, and shipping two strengths under one name is
  worse than shipping one. And **registration had acquired a hazard from doing two
  jobs**: it mints a secret shown exactly once, the MCP SDK discards structured output
  when a handler returns an error, so a failed binding could destroy the credential it
  had just created. That was worked around with a `binding_error` field reporting
  failure without failing. Splitting the operations deletes the hazard instead of
  guarding it, and gives the safer order: register, **write your secret down**, bind.

### Added

- **The `/stations` key table names each key's actor** and badges the ones whose actor
  holds no COMM token. That property decides whether a key can bind an endpoint at all,
  and until now it was invisible: a key under the wrong actor authenticates perfectly,
  drives every station tool, and refuses only at binding — in a different surface,
  possibly months later. `ActorsForStationKey` had existed since stations shipped with
  no console caller, only a CLI one.

### Fixed

- **`waiting_for_you` counted mail the sender had already read.** The predicate counted
  `queued` and `delivered`; `delivered` means the sender has been handed the message, so
  the prompt's own advice — "poll it and reconsider what you just sent" — was advice
  they had already taken. It fired on exactly the behaviour it exists to encourage: a
  session that reads its mail before replying. Found in production, by the field firing
  on this project's own dev session mid-reply.

- **An actor mismatch on binding now reports as itself** rather than as "unknown,
  already used, or expired". A mismatch is a setup error a deployment can sit in for
  months with no symptom; reported as an expiry race, the operator mints fresh vouchers
  forever, each failing identically.

## [1.6.0] — 2026-08-09

### Changed
- **Message lifetimes are anchored at DELIVERY, not at send.** A human works roughly eight hours a
  day, so a session goes 16 h between polls on a weeknight, 64 h over a weekend and weeks over annual
  leave. Against the shipped 24 h TTL — which ran from the moment a message was *sent* — that made
  every message sent during a Friday shift dead before Monday, 2.67x the window, and it is what
  killed a real 4 661-byte message sent on a Sunday. The clock ran during exactly the period in which
  nobody could poll. An undelivered message now waits on a separate, longer backstop
  (`comm_undelivered_ttl_sec`, 30 d); `comm_message_ttl_sec` measures the window *after* someone
  picks it up. **The two compose**, so an operator who inflated the message TTL to survive the old
  anchor should lower it before upgrading.
- **Acknowledging a message no longer destroys its body.** The old rule kept a body only when the
  message required a response, which destroyed 97% of one live deployment's bodies (153 of 159)
  through the ordinary, instructed path: poll, act, acknowledge. The un-acknowledged inbox was not a
  safety net — it was the only place a body ever existed. Retention is now uniform and governed by
  `comm_body_retention_sec`; setting it to `0` restores the previous behaviour exactly.
- **A message nobody ever read keeps its text when it expires.** The sender is told it expired;
  keeping the words makes that a fact they can act on rather than a hole. A *delivered* message that
  expires follows the ordinary retention rule — the recipient had it and did nothing.
- **`ack_up_to_seq` no longer settles undelivered mail.** It matched `queued` as well as `delivered`,
  so a cumulative acknowledgement could mark a message never shown to anyone. You cannot have
  processed what you were never handed.
- **`comm_poll` with a limit above the ceiling returns the ceiling** (100), not 50 — asking for
  everything used to yield less than asking for exactly 100.
- **`comm_send` reports what it overruled and what you have not read.** `ttl_clamped_from` appears
  when the server shortened a requested lifetime; `waiting_for_you` appears when mail was already
  waiting for the sender, as a prompt to poll and *reconsider* before the reply lands.
- **The metadata purge is measured from when a message settled**, not from when it was created. Keyed
  on creation the window was already spent before a long-lived message ever settled, which made
  "expiry keeps the body of a message nobody read" unreachable under the shipped defaults. This also
  retires the ordering rule an operator otherwise had to know: metadata TTL and message TTL are now
  independent.

### Added
- **A station directory**, on both surfaces: `comm_directory` (`/comm`) and `station_directory`
  (`/station`). Answers who you can see and who you can talk to right now. The `/station` mirror
  closes a real gap — `station_link_request` needs a name and nothing on that surface would tell a
  session one exists. `published` finally has a reader and now means exactly one thing: listed in the
  directory.
- **Link revocation from the console**, with the blast radius shown before the click and a new *Live
  channels* column. Revoking a link now also ends the channels it authorised — `RevokeStationLink`
  had no callers at all, while the UI promised "one click revokes it later".
- Two settings: `comm_undelivered_ttl_sec` and `comm_body_retention_sec`. Setting the backstop below
  the post-delivery TTL is **refused with a message naming the other field**, rather than silently
  replaced.

### Fixed
- **`comm_open_channel` no longer leaks which stations exist.** Three refusals — unknown name, no
  link, nobody staffing — were distinguishable, and the third echoed the *resolved* name, so guessing
  `PROD` confirmed a station really called `prod`. All three now return one string.
- **An agent could hide its channel from the operator's revoke.** The station pair was derived from
  each endpoint's *current* binding, so a single `comm_unbind` — the path that tool's own description
  recommends, needing no voucher and no human — made a channel invisible: the console showed 0 live
  channels and the sweep closed none while both sides kept talking. Migration `0008` snapshots the
  authorising pair on the channel row. A rebind can no longer move a channel under an unrelated link
  either, which previously severed traffic the operator had not aimed at.
- **`comm_channels` is station-scoped**, matching `comm_poll` and the membership check. A replacement
  session could poll a predecessor's mail and reply to it while `comm_channels` reported zero
  channels — able to act on a conversation it could not enumerate, which is worst for the takeover
  case stations exist for.
- **A half-finished link revocation is visible and retryable.** The count was skipped for revoked
  links, the button was hidden, and a retry short-circuited before the channel sweep — so the one
  state the new column exists to expose was invisible and permanently unrecoverable.
- **A comm.db failure no longer takes down the whole stations console.** comm.db is the expendable
  database and the page is gated on the stations flag alone; the channel count degrades to *unknown*
  rather than a 500, and with COMM switched off it reports unknown instead of asserting that no
  channels were open.
- **The one agent-writable station field is capped** at 4 KiB. The directory tools carry it verbatim
  into every peer's context, so an unbounded field was an unbounded write into other agents' working
  memory; a 700 KiB self-description was accepted.
- **Live settings changes reach the running COMM store.** The change detector that gates them omitted
  both new settings, so the console would save, validate and report success while the process ignored
  the value until restart.

## [1.5.5] — 2026-07-30

### Fixed
- **The sequence collision now names its own cause and its remedy.** When an endpoint that had
  adopted a station could not number a new message, the caller got a bare `internal error` — and an
  operator has no path from that string to a sequence counter. They will suspect the network, the
  token, the peer, or whatever they last restarted. The production operator hit exactly this and
  said they only knew where to look because a report happened to arrive first. The error now says
  what happened, that `comm_unbind` restores sending immediately, and that nothing was lost.
- **`comm_unbind` is pinned as the remediation path**, with a test that drives it *from inside* the
  collided state rather than from a healthy one. Anyone who adopted a station on 1.5.2 or 1.5.3 has
  an endpoint that cannot send, and unbind is their only way back — so if a later change ever makes
  unbind depend on sending, or on the sequence table being consistent, that operator is stranded.
  The test simulates the broken state directly rather than relying on current code to produce it,
  because the code that produced it has been fixed.


## [1.5.4] — 2026-07-30

### Fixed
- **Adopting a station broke every channel the endpoint had already used.** `comm_bind` on an
  endpoint with existing traffic left it unable to send: the next message failed with
  `UNIQUE constraint failed: message.channel_id, message.sender_endpoint, message.seq`, and it
  stayed broken until the endpoint unbound.
  Two correct pieces collided. The per-channel counter keys on the sending **station** once bound
  and on the endpoint rowid otherwise — which is what stops a *replacement* session restarting at 1
  (1.5.2). `comm_bind` lets a *running* session adopt in place (1.5.1). Together, adoption moved an
  endpoint to a fresh counter beginning at 1 while its own messages 1, 2, 3 were already on the
  channel. Binding and unbinding now carry the counter across, merging with `MAX` so it only ever
  moves forward and can never reissue a number either the endpoint or a station sibling has used.
  Affects **1.5.2 and 1.5.3**; a *fresh* endpoint binding at `comm_register` time was never affected,
  since it has no history to collide with.
  Found by binding this project's own session to its own station and watching its channel stop
  working — the feature used exactly as its handout documented.

All of 1.5.3 came from the first real production setup of stations. This one came from the first real
*use* of adoption. Both were invisible to tests written by the person who built them.


## [1.5.3] — 2026-07-30

### Fixed
- **Station-key and comm-token use was never recorded, so a leaked key left no trace.**
  `TouchToken` had exactly one caller — the knowledge-base authenticator — which meant
  `last_used_at` was permanently `NULL` for every `kens_` station key and every comm token.
  Production measured three comm tokens still blank after 102 acknowledged messages. Both
  authenticators now record use.
  The consequence was larger than a blank column: a stolen station key could read an entire
  notebook, task list and briefing with **no trace at all**, leaving a leak undetectable
  afterwards and un-scopable during response. It is a coarse signal — throttled to about
  once a minute, no per-read record — but the difference between "no timestamp" and "used
  four minutes ago" is the difference between an unanswerable incident and a scoped one.
- **The `/stations` key list could not tell two keys apart, at the exact moment the
  documented rotation says to retire one.** Label, last-used and state were identical for
  two keys minted for the same machine, and the only discriminator was a token id invisible
  inside a form action. An operator following "mint → install → restart → verify → retire the
  old one" reached the last step with no way to identify which row was old, and retiring the
  wrong one immediately cuts the session just set up. The table now shows the **token id**
  (which is also what `ken token revoke` takes, so console and CLI agree on how a key is
  named) and **created_at** — both were already fetched and discarded.
- The blank last-used cell now reads **"not measured"** with a tooltip saying station-key use
  is not recorded, rather than a dash that invites reading it as "unused".

### Changed
- **`station_binding_voucher`'s description now says the voucher is itself a credential** —
  that anyone holding it plus any comm-scoped token can join the station's inbox and *take*
  messages out of your poll, and that it must never be sent to a peer or written to a file
  or a notebook page. The tool text is where an agent actually reads a warning.
  **This is wording, not enforcement.** Redemption still checks no actor, no token and no
  endpoint, so a voucher remains a bearer capability; binding it to the redeeming identity
  at mint time is a design change that has not been made. A production operator has
  `comm_bind` on hold until it is, which is the correct call.

All of the above came from the first real production setup of stations, reported with source
citations by the operator who did it.

## [1.5.2] — 2026-07-29

### Fixed
- **The hearsay marker could never fire on a deployment that followed the documented setup.**
  `ken station key` hardcoded a **human** actor and the console used the logged-in curator's, while
  `ken token add` defaults to **ai** and `(kind, display_name)` is unique — so a machine's station key
  and its comm token were different actors, and the hearsay window (which joins on the actor) never
  matched. `hearsay_at_write` was permanently false, silently, and the only remedy the shipped
  commands offered was to deliberately mislabel an AI session's token as human: repairing one
  provenance signal by corrupting the one the curation model rests on. `ken station key` now resolves
  the actor that holds this deployment's comm token, says which it chose, and refuses to guess when
  several could apply; `--kind` defaults to `ai`, matching `ken token add`. Found by the production
  operator on the first real setup.
- **`IssueStationKey`'s doc comment named an enforcer that does not exist** ("the caller enforces
  that") — no caller ever compared the two actors. A contract comment asserting a guarantee is worse
  than silence: it stops the reader looking. `STATIONS.md` S5 carried the same false claim, that the
  console "refuses the mismatch at mint time". Both now state what is actually true: nothing enforces
  it, because a station is legitimately usable with COMM off and there is then no token to match; the
  tooling steers to the right actor instead.
- **`retire` is documented as non-severing, and that is only true of endpoints.** A retired station
  key stops authenticating immediately, so the session holding it loses its notebook, task list and
  locker at once — which makes the documented rotation (mint-new-then-retire-old) cut a running
  session at the retire step. S6 now states the safe order: mint → install → restart → verify → retire.
- **`docs/MONITORING.md` gave a health check on `localhost:8080` with no caveat.** A TLS-terminating
  deployment listens on :443, so the line fails with "connection refused" on a healthy server — and
  an operator runs it seconds after a restart, when "it's down" is the most believable explanation
  available. It read as an outage the change had just caused.

### Added
- **`comm_unbind` — binding is no longer a one-way door.** An endpoint can return to standing alone,
  keeping its id, its secret and every channel; only the station association goes. Asked for by the
  production operator before adopting a station, which was the right question: revoking a station key
  *severs* the endpoints it bound, so binding without an exit meant risking four live channels on a
  step meant to make things cheaper.

## [1.5.1] — 2026-07-29

### Added
- **`comm_bind` — adopt a station without re-registering.** Binding could only happen at
  `comm_register`, so a session that was already running when its human enabled stations had to
  register again: a new endpoint, a new secret, and every channel abandoned. That is the exact cost
  stations exist to remove, charged at the moment of adopting them. A running session now takes a
  voucher and calls `comm_bind`, keeping its endpoint id, its secret, its channels and its unread
  mail — which then belongs to the station, so a later session inherits it.
  An endpoint still binds **once**: re-pointing a bound one would carry the first station's unread
  mail into the second, while binding one that has no station carries nothing across.
- `docs/STATIONS.md` §12 now carries the ordered turn-it-on procedure for a deployment with sessions
  already running, and says what each step actually buys — durable memory and a task list after the
  key is in place, a station-owned inbox only after binding.

## [1.5.0] — 2026-07-29

### Added
- **Rotate a COMM endpoint's secret from the console — the incident-response primitive COMM was
  missing.** Until now the only thing a human could do to a live endpoint was *revoke* it, so a
  **leaked** endpoint secret could only be contained by revoking and then re-pairing every channel
  that endpoint belonged to, with every peer, from scratch. Rotation replaces the secret and keeps
  the endpoint id **and every channel membership**, so peers are unaffected and nothing is re-paired.
  It also covers the case that prompted it: a session whose context was compacted loses its secret
  irrecoverably, and recovery previously cost one fresh pairing code *per channel*.
  **There is deliberately no tool for this and there will not be one.** One bearer token covers a
  machine, so anything a session could trigger, every session on that machine could trigger — and
  seizing a neighbour's endpoint is exactly the shared-inbox failure the per-endpoint secret exists to
  prevent. The flaw in "let the session reissue" is the *automation*, not the reissuing: behind
  curator authentication, which no session holds, the same operation is safe. Each rotation is logged
  with the curator who performed it. A revoked endpoint cannot be rotated — that would resurrect a
  capability an operator deliberately destroyed.
- The connect-time COMM instructions, `comm_register`, and the authentication-failure text now tell a
  session to **ask its human to rotate** rather than repeating that the secret can never be reset —
  which rotation made untrue in the same change. (`comm_register`'s description was claimed here
  before it was actually edited; the audit that closed out this release caught it.)
- **Stations are complete and supported — still opt-in and off by default.** 1.4.2 shipped the
  foundation dark and said plainly that the console, peer links and the COMM binding were missing.
  They are built:
  - **The `/stations` operator console.** The request queue where a human approves a session's ask and
    **types the station's name** — the capability every tool is denied. Publish/unpublish, archive and
    unarchive, per-station asset usage against the caps, key list with retire, and an atomic asset
    **transfer** that is refused outright on a name collision and reports which names clashed (a
    `handoff` page collides in the common case, and silently merging would destroy exactly the page a
    human reaches for). Leading the page is the **cross-station task view**: every task waiting on the
    human, across every station, ordered by §11.5. That view is the answer to the problem stations
    exist for — a person should not have to ask each session in turn what it is still waiting on.
  - **Endpoint binding.** A session takes a short-lived single-use voucher from `/station` and passes
    it to `comm_register`; the station key itself never becomes a tool argument, because tool
    arguments are model output and land in transcripts and logs. A bound endpoint reads the
    **station's** inbox rather than its own, with claim-once delivery and a lease — so a replacement
    session inherits the mail its predecessor never read, with no pairing code and nobody waiting at a
    keyboard. An **unbound** endpoint behaves exactly as before, and a test says so.
  - **Peer links.** A human approves a *relationship* once instead of a conversation each time;
    `comm_open_channel` then opens channels with no code. A denied pair is muted on the **unordered**
    pair with escalating windows, and a re-request is silently dropped while returning the ordinary
    "pending" answer — telling the caller otherwise would let it probe past refusals one request at a
    time. Requests record whether the asking session was mid-conversation, which is the only signal a
    human gets that a peer may have talked it into asking.
  - **Revoking a station key severs what it bound.** Enforced at *use* rather than at revocation,
    because `ken token revoke` runs in a separate process with no message-database handle and could
    never have marked those endpoints however it was wired.
  - The station bounds from `STATIONS.md` §9 are now **live settings** in their own group, applied
    without a restart.

### Fixed
- **A cumulative acknowledge could settle messages nobody had read.** The per-channel sequence keyed
  on the sending *endpoint*, so a replacement session began numbering at 1 while its predecessor had
  reached 20 — two messages in one channel and direction sharing a sequence number. Since
  `ack_up_to_seq` is a **range**, acking after a takeover settled the predecessor's unread mail too.
  Found by checking a promise the design document made against what the code did; the counter now
  keys on the sending station, as `STATIONS.md` S4 always said it must.
- **`comm_register` could destroy the one-time secret it had just minted.** When a binding voucher
  failed to redeem — a stale one, the ordinary case — the handler returned an error, and the MCP SDK
  discards structured output when a handler does that: the caller received the error text and nothing
  else, while the endpoint existed in the database with a secret nobody would ever see. A binding
  failure is now reported *in* the result.
- The binding voucher was stored in **plaintext** — the only credential in Ken that was, in the one
  database the backup story copies off-box. Hashed like every other secret.
- Documentation corrected against the code across `STATIONS.md`, `COMM.md`, `README.md`, `DESIGN.md`,
  `MONITORING.md`, `MCP-TOOLS.md` and `COMPATIBILITY.md` — including a status banner that still told
  readers to disregard sections describing shipped behaviour, a missing ninth `comm_*` tool, three
  station tool names that never shipped under those names, and `kenc_` described as a token prefix
  when it is an OAuth client id and never a bearer token.

## [1.4.2] — 2026-07-28

### Added
- **Stations: the foundation ships DARK and is NOT yet ready to enable.** `KEN_STATION_ENABLED`
  exposes a `/station/mcp` endpoint where a session staffs a durable, human-named identity and uses
  its notebook, task list and locker. What is implemented: the schema, `kens_` station keys (a new
  scope family — see below), the notebook with revisions, the task list with its ordering contract,
  the locker, the MCP surface, and `ken station add|list|key|requests` — which is the **only** way to
  create and name a station, because that capability is deliberately withheld from every tool.
  **What is NOT implemented yet: the operator console, peer links, and the COMM binding**, so an
  operator who turns the flag on today gets a working notebook and task list but no web UI for them
  and no relationship between stations. Left off, it is completely inert: the tables ship empty and
  nothing is mounted. Design contract: [docs/STATIONS.md](docs/STATIONS.md).
- **A third token scope family, and a fix that had to land with it.** `station` and `station-locker`
  join `comm`/`comm-file`, minted as `kens_` credentials bound to one station. The scope-mixing check
  bucketed every non-comm scope as knowledge-base, so the moment `station` became valid it would have
  minted `read,write-draft,propose,station` **silently** while refusing `comm,station` — exactly
  backwards, since a session legitimately staffs a post and talks from it, while a token that both
  reads working notes and writes knowledge is the mixing that check exists to prevent. It is now a
  three-family partition with `station`+`comm` as the one permitted pair.
- **[docs/STATIONS.md](docs/STATIONS.md) — a design contract for stations**, written before any code in
  the style of `COMM.md`. A *station* is a durable, human-created and human-**named** working identity
  that AI sessions staff and outlive: it owns a notebook, a task list and a small file locker, and it is
  what COMM addresses, so a peer relationship survives the session that created it. Twelve locked
  decisions, each with its trade-off; nothing is implemented yet. The document exists because the
  expensive questions here are not features but invariants — who owns a message inbox when several
  sessions staff one identity, what a credential revocation actually severs, which database a durable
  row may point into, and what a snapshot then carries. It also names the statements in `BACKUP.md`,
  `COMPATIBILITY.md`, `COMM.md` and `MCP-TOOLS.md` that must change on the day it ships.

### Changed
- **`ken backup snapshot`/`verify` now name the real cause when `KEN_DB` is unset.** The command fell
  back to a *relative* `./data` and failed with `create data dir: mkdir data: permission denied` — which
  sends the reader to check permissions on a directory they never meant to use, instead of setting the
  variable. It now says which variable is missing and what it fell back to, and only when the path
  really is the relative default (a genuine permission problem still reports plainly). The docs that
  showed the command without `KEN_DB` are fixed too. Reported from production, which hit it running the
  verification step from a handout.
- **Documented that upgrading to 1.4.1 *defuses* an archive of older snapshots** — so do it in that
  order. Pre-1.4.1 snapshots contain session cookies that were replayable against the live server;
  migration `0011` clears the session table, which makes every one of those embedded cookies point at a
  row that no longer exists. Re-encrypting or shipping an old archive off-box *before* upgrading means
  carefully protecting a live credential. Found in production while preparing exactly that migration.

## [1.4.1] — 2026-07-28

> **Upgrading logs every curator out once.** Session ids are now stored hashed, and existing rows
> cannot be converted (the stored value *was* the credential), so migration `0011` clears them. Sign in
> again; nothing else is affected. Agent/MCP tokens are untouched.

### Security
- **Web-session ids are now stored hashed, so a database copy no longer contains replayable logins.**
  `web_session.id` was the cookie value itself — the raw bearer credential, in the clear — while
  `api_token` has always stored `secret_sha256`. Presenting that value *is* being logged in, and the
  database is copied by design: every snapshot is a byte-complete copy, and `KEN_BACKUP_GROUP` (1.4.0)
  deliberately lets an unprivileged account read snapshots. A snapshot taken while a curator was logged
  in therefore handed its reader that curator's session for the remainder of its life. Ken now stores
  the SHA-256 of the cookie and looks up by hash; the raw value never touches disk. Plain SHA-256 is
  the right primitive here — the input is 32 bytes of CSPRNG output, so there is nothing to
  brute-force. **Migration `0011` clears `web_session`, so every curator is logged out once** and signs
  in again; existing rows cannot be converted, because the stored value *is* the credential.
- **The first-run setup token no longer lands in a world-readable log.** `ken.sh`'s detached mode
  created `logs/ken.out` at the ambient umask, and that log carries the one-time `/setup` token — a
  credential whose reader can complete setup and become the curator. It now sets `umask 077` first.
  (Unused under systemd, which logs to the journal.)
- `ken.service` no longer holds a write grant on the backups directory: nothing in the server writes
  there, and it is a directory `KEN_BACKUP_GROUP` may read.

### Fixed
- **`ken backup snapshot` wrote a world-readable copy of the knowledge base.** The mode came from the
  ambient umask (`0644` under the usual `0022`) and was only corrected by the two *shell* callers — so
  an operator running the command by hand, exactly as `BACKUP.md` and `INSTALL.md` instruct, got a
  byte-complete dump of every entry, curation history, curator account and token record at `0644` —
  and both runbooks point at `/tmp`, which is world-writable and world-readable. `Snapshot` now chmods
  `0600` itself and the `backup` subcommand narrows its umask, so the file is owner-only from creation
  regardless of caller or umask; the shell guards become belt-and-braces. Found by auditing the
  assumption that only `ken` and root ever read Ken's data — an assumption a backup group makes false.
- **The live database was `0644`.** `ken.service` set no `UMask`, so `ken.db`, its WAL sidecars and the
  COMM database were created group- and world-readable, contained only by the `0750` directory around
  them. Nothing could reach them on-box, but the protection was the *directory*, not the file — and a
  mode travels with a copy while a directory mode does not. The unit now sets `UMask=0077`.
- The installer's `umask 077` now also covers the securing step, so an encrypted pre-upgrade snapshot
  is `0600` from creation rather than `0644`-then-fixed; and the bundled `configs/litestream.yml`
  template ships `0640` with a header warning **not** to fill credentials into that copy — it lives in
  the world-readable release tree and is replaced on every upgrade.

### Changed
- **Documented that enabling `KEN_BACKUP_GROUP` / `KEN_BACKUP_DIR` is a one-time root action**, and
  recorded in [`scripts/ken-upgrade`](scripts/ken-upgrade) why they are deliberately **not** in the
  scoped upgrade wrapper's allowlist. The property that makes that allowlist safe is that no accepted
  argument can change **who can read what** — version, TLS mode, domain, port and firewall all change
  what the service *does*, none changes who can read its data. `--backup-group` would break it: a
  caller could nominate its own group as one allowed to read every database snapshot. Because the
  installer re-discovers both settings from the installed unit, one root run is enough — every later
  scoped upgrade preserves them without the flag. (Raised by the production operations session, which
  argued *against* being granted the convenience.)

## [1.4.0] — 2026-07-28

### Changed
- **Inter-session communication (COMM) is no longer labelled "experimental."** It has been in use
  since 1.2.0, and the "experimental" wording undersold a feature that is stable in practice — it read
  as a preview that might vanish. COMM is now described as a **supported, opt-in** feature that is
  **off by default**, everywhere the label appeared: the `/comm` web console, the startup banner and
  CLI help, the MCP connect-time instructions agents receive, and the docs. Nothing about its behaviour
  changes — it is still off unless `KEN_COMM_ENABLED=1`, still needs a dedicated `comm`-scope token, and
  file exchange is still separately gated. Its interface stays **outside the byte-level compatibility
  contract**, but now for the honest reason — it is optional and off by default (which
  [COMPATIBILITY.md](COMPATIBILITY.md) already excludes), not because it is unstable — so it continues
  to evolve additively (the open channel-relabelling and endpoint-identity items can still land).

### Added
- **`KEN_BACKUP_GROUP` (installer: `--backup-group`) — pull snapshots off the box without root.**
  Snapshots are `0600` owned by the `ken` service account, which is `nologin`, inside a `0750`
  directory: **only root could read one.** So the documented tier-3 off-box copy required either a
  root-authorized SSH key on the Ken host or a root cron job staging copies elsewhere — both a worse
  grant than the thing they enable, and POSIX ACLs are no way around it (`chmod 0600` zeroes the group
  bits, setting the ACL mask to `---`). Naming a group now makes snapshots `0640` owned by it, with the
  backups directory setgid so new snapshots inherit it, letting a dedicated unprivileged account be
  pulled from by an archive host. **Unset, nothing changes** — the strict owner-only default is
  byte-identical to prior releases — and it **fails safe**: a group that does not exist or cannot be
  applied leaves `0600` with a warning, never a file readable by the wrong group. Reported from
  production, where it was blocking the first off-box backup of a Ken instance.

### Fixed
- **A snapshot was created world-readable for the length of the dump.** The securing step chmods a
  snapshot to `0600`, but that lands only *after* `VACUUM INTO` has written the entire file — so under
  systemd's default `0022` umask (and root's, for the pre-upgrade snapshot) a multi-gigabyte dump sat
  at `0644` for as long as it took to write, and in a setgid backups directory it already carried the
  backup group while doing so. The snapshot unit now sets `UMask=0077` and the installer wraps its dump
  in `umask 077`, so a snapshot is `0600` from its first byte; the explicit `0640` still applies
  afterwards when `KEN_BACKUP_GROUP` is set.
- **`KEN_BACKUP_DIR` split the archive in half.** The nightly script honoured it; `install.sh`
  hardcoded `<prefix>/backups` and never read it. Setting it therefore moved the nightlies while the
  **pre-upgrade rollback snapshots stayed behind** — an archive silently living in two places, with the
  rollback points in the half that moved nowhere. The installer now honours it too (`--backup-dir`),
  writes it into `ken-snapshot.service` so both paths resolve the same directory, and points the
  versioned `backups` symlink at it. This was the third instance of the same shape — the installer path
  and the nightly path disagreeing about policy (after the file mode in 1.2.1 and the timestamp in
  1.3.0) — so the settings are now **re-discovered from the installed unit on upgrade**: re-running the
  installer without the flags never relocates an existing archive or drops a configured group.

### Changed
- **The web UI now shows timestamps in the reader's own timezone, and deadlines as "in 8 minutes."**
  Every human-facing timestamp was rendered by trimming the stored UTC string to `2026-07-20 17:53` —
  which also **cut the trailing `Z`**, so the result no longer said which timezone it was and read as
  local time. A curator six hours from UTC read a `17:53Z` deadline as late afternoon; it was 11:53
  their time. (Same root cause as the snapshot-filename divergence fixed in 1.3.0, one surface over —
  and reported by the same production instance, which had by then been caught by it twice in two days.)
  The rule this settles: **machine-facing artifacts stay UTC and self-describing** (filenames, API
  fields, logs — unchanged); **human-facing surfaces render in the reader's timezone.** The server now
  emits `<time datetime="…Z">` and the browser converts it, so there is no timezone setting to
  configure and it stays correct when two people in different timezones share one instance. Deadlines
  and expiries render **relative** — a deadline is about how much time remains, so the absolute form
  was a lossy encoding of what the reader wanted — with the exact local time on hover. Relative
  wording and date formatting come from the browser in the page's language, so no new translations
  were needed. Without JavaScript the server-side fallback still renders, now explicitly suffixed
  **`UTC`** rather than passing as an unmarked local-looking time. Applies to `/comm`, `/dashboard`,
  `/browse`, `/entry` and `/tokens`; the COMM API's own `…Z` fields are untouched.

### Fixed
- **The backup docs said snapshots were encrypted; the shipped default is plaintext.** `BACKUP.md`
  asserted "Everything that leaves the box is age-encrypted" and titled tier 2 "Nightly **encrypted**
  snapshot"; `INSTALL.md` said "Nightly **encrypted** snapshots … enabled by default"; `DESIGN.md`
  stated it as implemented policy. All false as shipped — encryption is **opt-in** and turns on only
  when the operator sets `KEN_AGE_RECIPIENT`. A reader who skimmed concluded there was nothing to turn
  on, which is exactly how an instance ends up with plaintext backups nobody chose. Every one of those
  claims now states the real default.
- **The fail-closed ordering trap is now documented where an operator will see it.** If
  `KEN_AGE_RECIPIENT` is set but the `age` binary is missing, the snapshot step deletes the plaintext
  and **keeps nothing for that run** — correct behaviour (never silently substitute plaintext for the
  encryption you asked for), but previously written down only in a source comment. Setting the
  recipient before installing `age` therefore did not downgrade you to plaintext backups; it silently
  gave you *no* backups. Install-`age`-first, the journal lines that distinguish each outcome, and a
  "prove it worked" step are now in `BACKUP.md`, `INSTALL.md`, the installer's post-install banner, and
  the `ken-snapshot.service` comment.
- **`BACKUP.md` now answers the question an operator actually has** — what a snapshot contains (a full
  dump of the knowledge base plus the list of who can reach it), why mode `0600` protects the file on
  the box but not any copy of it, and the deciding question ("do your backups ever leave this
  machine?"). It also documents key **escrow**, a **decrypt drill** (a key you have never decrypted
  with is not a backup), **recipient rotation**, and the sweep for plaintext `pre-upgrade-*.db` files
  left by 1.2.x — which retention never prunes.
- **The documented restore recipe was unsafe.** It decrypted straight onto the live `data/ken.db`, so
  a wrong key or truncated transfer destroyed the very file being fallen back on; it ran as root while
  the service runs as `ken`, leaving a root-owned database the service could not open; and it cleared
  the stale WAL sidecars *after* verifying rather than before. It now restores via a scratch copy,
  verifies before going live, keeps the outgoing file until the new one passes, and sets ownership.
- **A failed snapshot run was invisible.** `MONITORING.md` covered the running server but never
  mentioned backups, and the shipped units carry no `OnFailure=`. It now names the blind spot and gives
  two ways to close it — the recommended one being a freshness check on the backups directory, which
  catches every failure mode at once.
- Documented that the recipient must be a real `Environment=` line **on `ken-snapshot.service`**: a
  recipient supplied via `EnvironmentFile=`, or set on `ken.service`, still encrypts the nightlies but
  leaves every **pre-upgrade** snapshot in plaintext, because the installer reads the unit's
  `Environment=` and does not expand `EnvironmentFile=`.

### Removed
- **Windows packaging is removed; support is now Linux-only, stated explicitly.** Ken was only ever
  built, tested, and released for **Linux** (`amd64`/`arm64`) — every release ships only Linux
  artifacts — but the repo carried a Windows `deploy/ken.nsi` (NSIS + WinSW) template and an
  INSTALL.md Windows section that documented a path with **no published artifact behind it**,
  overstating what is supported. Both are removed. [COMPATIBILITY.md](COMPATIBILITY.md) now states
  the platform boundary outright (**Linux `amd64`/`arm64`; macOS and Windows not supported**) instead
  of leaving it implicit in what gets released. The binary is pure Go (`CGO_ENABLED=0`) and may still
  compile elsewhere, but that is not support. macOS was never documented or shipped.

## [1.3.0] — 2026-07-27

### Added
- **French translation of the human web UI** (`messages_fr.properties`, embedded alongside English and
  Spanish). The language selector now offers **Français**; a French reader gets a French curator UI,
  with the AI/MCP surface and logs staying English-only by design. **AI-translated from the English
  source, community corrections welcome** — the strings were machine-generated against a supplied
  French glossary (so the product UI and the marketing site use the same terms) and review-weighted
  toward the high-risk strings: destructive-action confirmations, action buttons, and error/flash
  text, where a mistranslation is a usability or data-loss problem rather than a matter of style.
  Partial by construction is safe: any key not present falls back to English, so nothing breaks. Key
  parity with the English source is exact and enforced by the same tooling as Spanish.
- **Request and tool latency histograms** (`ken_http_request_duration_seconds` is now a **histogram**;
  new `ken_mcp_tool_duration_seconds{tool}`). Ken previously exposed only a duration *summary* — a mean
  per surface, with no percentiles — so an operator could see how much Ken was used and how much memory
  it took, but not how fast it *feels*. The histograms give real p50/p95/p99 via
  `histogram_quantile(…)`, while `_sum`/`_count` still yield the mean, so this is a strict superset of
  what the summary provided. Hand-rolled to match the metrics package (no `client_golang`).
- **A blocking tool's wait is never counted as latency.** `ken_mcp_tool_duration_seconds` measures each
  tool handler's *work* time, and the intentionally-blocking `comm_poll` (a long-poll that parks up to
  the poll-wait ceiling) is deliberately excluded — bucketing a 30-second parked wait would drown every
  other tool's real latency. The HTTP histogram covers the web surface only; the streaming MCP surface
  is measured per tool, as it was for the counters. A regression test pins the exclusion.
- **A live "last checked" stamp on the Proposals and Comm pages, and auto-refresh for
  the Comm console.** Both pages now show a "Last checked: <local time>" line that the
  page's poller re-stamps on every check (every 20s, using the browser's own clock and
  timezone) — so a curator who leaves the page open can see it is genuinely still
  checking, not just sitting on stale data. The Comm console gains the same
  reload-on-change auto-refresh the Proposals page already had: it reloads when a peer
  session registers, a channel opens, or a pairing code is consumed, driven by a cheap
  per-space "console fingerprint" endpoint (`GET /comm/count`). One generic poller in
  `app.js` now serves both pages (the count URL is derived from the path), and the stamp
  is hidden without JavaScript, where the freshness it claims could only ever be stale.

### Changed
- `ken_http_request_duration_seconds` changed type from **summary** to **histogram**. The `_sum` and
  `_count` series are unchanged (mean queries keep working); the addition is the `_bucket{le=…}` series
  that make percentiles possible. Metric names and types are outside the compatibility contract
  ([COMPATIBILITY.md](COMPATIBILITY.md)); no dashboard query over `_sum`/`_count` breaks.

### Fixed
- **The installer's pre-upgrade snapshot now uses the same UTC, self-describing filenames as the
  nightly snapshot — and is age-encrypted when the nightlies are.** The two snapshot paths had
  drifted: the nightly stamped `ken-<UTC>T…Z.db`, while the installer's pre-upgrade snapshot stamped
  `pre-upgrade-<LOCAL, unmarked>.db` and never encrypted. Two consequences, both reported from
  production: (1) one `backups/` directory carried two time conventions six hours apart, which sent an
  operator investigating a **non-existent clock fault** (the local, unmarked name read six hours off a
  UTC one; both clocks were fine); and (2) the pre-upgrade snapshot — a full copy of the database taken
  seconds before an upgrade — was written in **plaintext** even on hosts where the operator had
  configured encrypted nightlies. Both are fixed: the pre-upgrade snapshot is now
  `pre-upgrade-<UTC>T…Z.db`, and it is age-encrypted (`.db.age`) whenever a `KEN_AGE_RECIPIENT` is set
  for the nightly timer. Everything sorts by mtime, so no retention or restore path changes. The
  unmarked local stamp was also a latent portability hazard on DST hosts (names run backwards for an
  hour each autumn); the UTC stamp removes it.
- **The naming and securing policy now lives in one shared library** (`scripts/ken-snapshot-lib.sh`,
  sourced by both `ken-snapshot.sh` and `install.sh`) instead of being re-implemented in each path.
  This is the root-cause fix behind the two symptoms above and the `0600`-mode fix in 1.2.1 — all three
  were the pre-upgrade path failing to do something the nightly path already did. The library
  age-encrypts **fail-closed**: if a recipient is set but encryption cannot happen, the plaintext is
  removed rather than left behind. The pre-upgrade snapshot stays best-effort — a failure never aborts
  an upgrade.

## [1.2.2] — 2026-07-27

### Changed
- **The inter-session communication console is easier to find and its channels are easier to
  recognize** — from production feedback.
  - The nav entry for the console was labelled **"Sessions"**, which reads as login sessions and was
    missed entirely; it is now **"Comm"**, matching how the feature is named everywhere else (`/comm`,
    `comm_*`, `KEN_COMM_ENABLED`). The link itself was always present — it was just unrecognizable.
  - **A channel now shows the human name you gave it.** The pairing code's optional label
    (e.g. "Ken dev &lt;-&gt; prod") is carried onto the channel at creation (comm migration
    `0004_channel_label.sql`, nullable/additive) and the console leads with it, demoting the opaque
    channel id to a secondary line — mirroring how entries show a title over a slug. Once channels
    accumulate, "Ken dev ↔ prod" is what an operator recognizes; the opaque id and the drifting
    endpoint labels are not. The create-code form now says plainly that the label names the channel.
    An unlabelled code falls back to the endpoint labels, unchanged.

### Docs
- **Scoped the open COMM observability thread** (docs/COMM.md §13). The 1.2.0 shakedown surfaced two
  live-instance observations — gauges that read zero for racing quantities (`ken_comm_endpoints`
  during the sweep bug, `ken_comm_poll_waiters` during an active poll), and tool-level failures that
  register in no counter or log — which together define one coherent increment for a later
  experimental MINOR: per-tool error counters, a janitor that announces non-trivial deletions, an
  explicit per-gauge decision (document-as-instantaneous vs pair-with-a-counter, never a reflexive
  moving average), and the two §5.5 abuse-safety gaps folded in. Its acceptance bar: an operator
  watching only `/metrics` could have seen the 1.2.0 failure without reproducing it by hand.

## [1.2.1] — 2026-07-27

### Fixed
- **COMM was unusable wherever it was enabled: freshly-registered session endpoints were deleted
  within a minute, so a channel could never carry its first message.** Caught by dogfooding 1.2.0 —
  the first real inter-session connection failed with `not found` on the first poll.
  Root cause: the idle-endpoint sweep added in 1.2.0 reads `EndpointIdleTTLSeconds` from
  `comm.Limits`, but the production path that builds those limits from live settings
  (`commLimits()` in `cmd/ken`) never mapped that field. It was therefore the zero value, and the
  sweep computed its cutoff as *now minus zero* — matching **every** endpoint that had no message
  traffic yet, which the once-a-minute sweeper then deleted. The store's own `DefaultLimits()` had
  the right value (7 days), which is why every test passed: none exercised the settings→limits
  mapping. Same class as the `viewOf` bug — a hand-written cross-layer copy that silently dropped a
  field.
- Three-part fix. (1) **The sweep now fails safe:** a non-positive idle window *disables* endpoint
  cleanup instead of meaning "sweep everything now" — a retention threshold of zero must never be
  read as "delete immediately". (2) The mapping is completed, backed by a new live setting
  **`comm_endpoint_idle_sec`** (Settings → Inter-session comms; default 7 days, minimum 5 minutes) so
  the window is operator-tunable like every other COMM limit. (3) A reflection-based regression test
  asserts `commLimits()` maps **every** `comm.Limits` field to a non-zero value, so the next dropped
  mapping fails a test rather than production; verified by confirming it fails when the new mapping is
  removed. Plus a store-level test that a zero window leaves a fresh endpoint intact.
- Note: an actively polling session was never at risk in principle (a poll refreshes the endpoint's
  last-seen), but the *handshake* window — register, join, wait for the peer — has no traffic yet, so
  the sweep destroyed endpoints before they could be used. That is the window every connection starts
  in, which is why the feature was effectively broken.
- The `ken_comm_endpoints` gauge reading zero while endpoints existed (reported from production) was a
  *symptom* of the same bug, not a separate one: endpoints were being deleted between an operator's
  `comm_channels` call and the next `/metrics` scrape. With endpoints no longer vanishing, the gauge
  reflects reality — verified: two registered endpoints read `ken_comm_endpoints 2`.
- **Installer: the automatic pre-upgrade snapshot now `chmod 0600` immediately after it is written**
  (`scripts/install.sh`), mirroring `scripts/ken-snapshot.sh`. Unrelated to COMM; found and
  re-confirmed by the production session during the 1.2.0 upgrade. `ken backup snapshot` writes at
  root's umask (`0644`), and the installer's later `chown -R` fixed *ownership* but never *mode*, so
  every upgrade left one world-readable full copy of the database in the backups directory. The
  containing directory (`0750`) blocks traversal on the box, so this was a defense-in-depth gap rather
  than a live exposure — but a file mode travels with copies, so an off-box backup of that directory
  carried the loose mode with it. The nightly path already did this correctly; the two paths now agree.

## [1.2.0] — 2026-07-27

### Security
- **`dedup_check_token` is now bound to the token holder that ran the search.** It was signed over its
  expiry alone, which made it a *transferable bearer capability*: any holder could satisfy `kb_save`,
  so handing the string to another session would have reduced the structural search-before-save gate
  to a convention. That was inert while a token could only reach the session that minted it — and
  stops being inert the moment sessions can exchange strings, which is exactly what COMM introduces.
  Fixed before that becomes possible rather than after. The binding is the calling **token**, not the
  actor: several sessions can share one actor, and actors collapse by display name, so the token is
  the narrower handle.
- The wire shape is unchanged — still an opaque `dct_v1.<exp>.<sig>` string — so no client changes
  anything. Tokens minted by an older build stop verifying at upgrade, which the 10-minute TTL makes
  cosmetic. New error message: `invalid dedup_check_token — run kb_search yourself before saving`,
  which deliberately does not distinguish a tampered token from one issued to a different holder: the
  remedy is identical, and separating them would confirm to a caller that a stolen token was otherwise
  valid. Regression tests cover the cross-principal case, tampering, a wrong server secret, expiry, and
  the empty-subject (dev-token) round trip.

### Added
- **Inter-session communication (COMM)** — authenticated session-to-session messaging between AI
  sessions on the same or different machines (`internal/comm` + `internal/commserver`, decision **D9**,
  contract in [docs/COMM.md](docs/COMM.md)). **Experimental and off by default**, which places it
  outside the compatibility contract for at least one MINOR. Messaging, live-tunable limits, metrics,
  the curator-side hearsay marking, and file exchange — each verified end to end against the running
  binary (see the entries below).
- Its own SQLite file with its own forward-only migrations, versioned **independently** of `ken.db` so
  a COMM schema change never touches the knowledge base. Ownership columns (`actor_id`, `space_id`,
  `token_id`) name rows in `ken.db` and are deliberately **not** foreign keys — SQLite FKs cannot span
  database files, so the caller that holds both handles validates them. The schema says so in place,
  because the tempting "fix" is to move the tables into `ken.db`, which would undo the separation the
  whole design rests on.
- **The content/metadata split is enforced, and it is load-bearing rather than an optimization.**
  Acking deletes the message *body*; the metadata row survives. Deleting the whole record — the
  intuitive storage win — is mutually exclusive with request/response: with two requests outstanding
  and both acked, a later reply would reference a row that no longer exists, and the server could
  neither validate it, route it, nor report which request was answered. A regression test pins it.
- **Ack means *processed*, not received, and delivery is at-least-once.** Polling never hides a message
  from the next poll; only acking advances state, and every message carries a delivery count so a
  receiver can spot a redelivery. This makes a lost poll response harmless — the ordinary failure on
  this transport — and means a turn truncated mid-processing gets its message back rather than losing
  it. Sends are idempotent per client-supplied key; acks succeed on unknown or already-acked ids.
- **Channel establishment is human-gated and two-sided.** An agent cannot conjure a channel: a human
  mints a pairing code and *both* endpoints redeem it. This is the same move that makes the curation
  gate trustworthy — withhold the capability rather than instruct the model not to use it. Re-redeeming
  from a member is idempotent, a third endpoint is refused, and an expired code is indistinguishable
  from an unknown one so codes cannot be probed.
- Per-endpoint secrets, because the operating convention is one token per *machine*: without them two
  sessions sharing a token could poll and ack each other's messages, most likely by accident when both
  register the same label. Registration therefore never reuses an endpoint. Sequence numbers are per
  channel *and direction*, which is the only ordering COMM promises.
- Quotas (body cap, per-channel un-acked depth, TTLs) are enforced **in SQL inside the writing
  transaction**, never by keying the shared rate-limiter bucket — that bucket fails *open* when
  saturated, which is right for IP and token keys an attacker cannot mint cheaply and wrong for
  identifiers a caller creates in a loop. A fail-open quota is a disk-full outage; a refused message is
  a retry.
- The sweeper expires **delivered-but-never-acked** messages as well as queued ones (a message polled
  by a session that then died must not live forever), drops the retained body of a request whose reply
  deadline passed, and purges settled metadata past its retention window.
- 21 tests, including three whose failure modes were verified by deliberately breaking the invariant
  they guard.
- **The COMM MCP layer** (`internal/commserver`): six `comm_*` tools — register, join, channels, send,
  poll, ack — served from a **separate `/comm/mcp` endpoint**, wired into `ken serve` behind
  `KEN_COMM_ENABLED` (off by default).
- **A separate endpoint is a security property, not packaging taste.** A client registers Ken twice: a
  knowledge-base token cannot send messages, a comm token cannot write knowledge, each surface gets
  independent rate accounting so a poll loop cannot starve `kb_*` calls, and revocation is per-surface.
  It also lets `/comm/mcp` refuse the permissive CORS `/mcp` needs for a browser-based connector, since
  nothing here has a browser client.
- **COMM authentication deliberately does not reuse the knowledge base's, and the duplication is the
  point.** That path accepts three token shapes; this one accepts exactly one — a `ken_` API token
  carrying `comm`. The OAuth path is excluded because a cloud-hosted connector is the worst possible
  holder of "reach into the sessions on my machines" *and* its scope set is hard-coded, so an operator
  could not withhold comm from it; the dev-token bypass is excluded because its empty token id escapes
  per-token rate accounting entirely. Sharing the other package's `authenticate()` would mean a future
  token shape added there silently gains comm access.
- **Comm tokens must be dedicated, enforced at mint time.** `ken token add` refuses to combine comm
  scopes with knowledge-base scopes. Without this the design's claim would be aspirational — and since
  API tokens have no expiry, only revocation, widening an existing token's scopes would retroactively
  arm every already-copied instance of it. `comm-file` gates the file surface (below).
- **Long-poll wakeups are an optimization, never the correctness mechanism**: a poll re-reads the
  database before returning either way, so a missed signal costs latency rather than a message. Waits
  are clamped server-side to 30s regardless of configuration, because a wait that ties the client's
  tool timeout turns a successful empty poll into a tool *error*, which models handle badly — and
  proxies commonly read-timeout at 60s. Waiters are capped per endpoint and globally, since nothing
  outside the process bounds them (no write timeout by design, no task limit in the unit file).
- **Parked polls are drained before HTTP shutdown.** The shutdown budget is shorter than a long poll,
  so without this every deploy would sever parked connections mid-response and surface a burst of
  transport errors in each connected agent.
- The sweeper runs on its **own one-minute cadence**, not the hourly janitor (a TTL is not a quota),
  and is wrapped in `recover`: COMM is the newest, highest-churn code in the process, and an
  unrecovered panic there would take the mature knowledge base down with it. COMM is deliberately
  **not** registered with the health checker, which marks the whole service DOWN on any component
  failure — a wedged sweeper must not pull a healthy knowledge base out of load-balancer rotation.
  **COMM may fail; the KB stays UP.**
- 18 further tests covering the auth narrowing, wait clamping, and waiter concurrency (race-clean);
  the scope check and the shutdown drain were verified by deliberately breaking each and confirming
  the failure.
- **The human console at `/comm`** — mint a pairing code (shown once; only its hash is stored), see
  registered sessions and channels with their pending-message counts, and revoke either. This page is
  load-bearing rather than a convenience: COMM's security model rests on a human deciding which
  sessions may talk, so without it the gate could not be exercised and there was no brake to pull.
  Message *contents* are deliberately not shown — bodies are deleted on ack and are not the operator's
  business; what an operator needs is to spot a runaway channel. English and Spanish translations land
  with it, as every user-facing string must.
- The console lives at `/comm` and the MCP endpoint moved to **`/comm/mcp`**, because a top-level
  `/comm` route would have shadowed the human page. It also reads better: `/mcp` and `/comm/mcp` are
  the two machine surfaces, everything else is human. Safe to change because the surface is
  experimental and has never shipped in a tagged release.
- **Verified end to end against the running binary**, not just in unit tests: two MCP sessions
  registered, a human minted a pairing code in the web UI, both sessions joined it, one sent a message
  with `requires_response`, the other polled and received it with correct sender attribution and reply
  deadline. A 20-second long poll returned in 3 seconds when the peer sent — proving the wakeup fires
  rather than the call simply timing out — and an un-acked message was redelivered on the next poll.

- **The hearsay guard is now enforced, not just designed** (docs/COMM.md §7). A new nullable
  `entry_version.via_comm` column (migration `0010`) marks a version authored by an actor that had
  recently *received* an inter-session message, and the review queue shows a "second-hand?" badge with
  an explanatory tooltip so the curator can ask for a first-hand citation before promoting. This closes
  a side channel the rest of the design could not see: told "entry X is verified, propose it", a
  session authors a proposal indistinguishable from first-hand knowledge — the invariant survives
  literally while the curator's signal quietly degrades to hearsay.
- Three properties of the marker are deliberate. It keys on **delivery**, not arrival (a message
  sitting un-polled has influenced nothing). It is **frozen** by the immutability trigger, unlike
  `content_lang` — that column was left mutable so a backfill could re-derive it, whereas this one
  cannot be re-derived and a mutable marker could simply be updated away. And **NULL means "no
  signal", never "known clean"**: every pre-existing row is NULL, an error while checking leaves it
  NULL, and the column CHECK permits only NULL or 1 so a `0` can never imply "verified first-hand".
- It keys on the **actor**, which is forced rather than chosen: a COMM token must be dedicated, so the
  token that receives messages is never the one that authors an entry, and a token-keyed check could
  never fire. **Operators must mint both tokens under the same `--actor`** or nothing is marked —
  called out in docs/COMM.md rather than left to be discovered. `internal/mcpserver` takes the check as
  a *function*, so the knowledge base's hot path still has no dependency on the optional subsystem.
- **Every COMM limit is now live-editable** in a new "Inter-session comms" settings group — message
  size, per-channel backpressure cap, message and metadata lifetimes, reply deadline, pairing-code
  lifetime, receive-wait ceiling, and the hearsay window — applied without a restart so a limit can be
  tightened *during* a runaway rather than after it. Enabling COMM itself stays restart-level: it opens
  a second database. The store's limits are now swapped atomically, since a live edit while requests
  are in flight would otherwise be a data race.
- **`ken_comm_*` Prometheus gauges** (endpoints, open channels, unacknowledged messages, retained
  message bytes, parked long-poll waiters) — absent series rather than zeros on a default install. The
  collector is recover-wrapped because collectors run inline in the scrape handler with no panic
  recovery: a COMM bug must not take down the operator's view of a healthy knowledge base. COMM remains
  deliberately absent from `/health`, which marks the whole service DOWN on any component failure.

- **COMM file exchange** (docs/COMM.md §11) — the last designed-but-deferred piece; the COMM design is
  now fully implemented. Three tiers, in the order the instructions teach: a **same-host filesystem
  handoff** (`transfer='path'` — zero bytes through the server, zero model tokens on payload, with the
  C9 nonce rendezvous as proof of the shared filesystem and server-validated bare basenames so an offer
  can never steer a session toward an arbitrary local path), ordinary message bodies for genuinely
  small text, and a **one-time-grant HTTP relay** for cross-host transfers driven with curl — because
  tool arguments are model output, so payload bytes must never travel as tool-call tokens.
- **Gated twice, off by default**: the `comm-file` scope (now real, not just reserved — enforced per
  tool and at the relay) and a live `comm_files_enabled` setting that doubles as a mid-incident kill
  switch. Two credentials on every byte-moving request: the bearer token must carry `comm-file` AND own
  the endpoint the single-use, minutes-lived grant was minted for.
- **Verification before visibility**: relayed bytes stream into a 0600 `.part` file and are renamed
  into place only when size and sha256 match the offer; the message the receiver polls is enqueued only
  at that point, so partial state is never observable. Quotas fail closed and are checked at offer time
  and again as bytes arrive: per-file cap, global storage budget, free-space floor (so the knowledge
  base's writer always has headroom), and a per-sender in-flight-upload cap that closes the accounting
  window between "grant minted" and "bytes counted".
- The sweeper deletes delivered and expired bytes and abandoned `.part` files; the attachment row
  (name, size, sha256, endpoints, timestamps) survives as the audit record, on the same reasoning as
  message metadata. New comm migration `0002_files.sql`; new gauges `ken_comm_files` and
  `ken_comm_file_bytes`; six new live settings; en+es strings for all of it.
- **Two tools** (`comm_file_offer`, `comm_file_grant`), 15 new tests including an HTTP-level round trip
  against the real handler, three deliberately-broken-invariant checks (checksum verification, name
  validation, scope narrowing — each confirmed to fail its test), and a full end-to-end run over the
  running binary: 200 KB uploaded with curl, polled, downloaded, and verified byte-identical.
- The end-to-end run caught a cross-layer bug unit tests could not: the store carried the file
  descriptor but the poll tool's view-copy dropped it, so a completed upload was delivered as a message
  with no file. Fixed, and the store→view copy is now a named, tested function (`viewOf`) so a field
  added on one side can no longer silently vanish on the other.

### Added
- **The Proposals page auto-refreshes.** A curator who keeps `/proposals` open now sees new proposals
  appear without a manual reload: the page polls a lightweight `GET /proposals/count` endpoint (one
  `COUNT`, behind the same auth as the page) every 20 seconds and reloads when the server's pending
  count diverges from what the page was rendered with — which also keeps it in sync when a proposal is
  promoted from another tab. Same-origin `fetch`, so it satisfies the strict `default-src 'self'` CSP;
  the poll pauses while the tab is hidden and fires immediately when it regains focus, so returning to
  a backgrounded tab is snappy. Fully progressive: with JavaScript disabled the page behaves exactly as
  before.

### Fixed
- **Pre-release audit of the whole 1.2 line.** A multi-agent audit (seven dimensions, each finding then
  handed to an adversarial verifier) surfaced defects across COMM that the feature's own tests did not
  cover. Everything below was confirmed by tracing the concrete failure, and each fix carries a
  regression test.
- **A revoked pairing could be silently un-revoked.** `JoinChannel`'s second-redeem path never checked
  the channel's state, so if a human revoked a half-formed pairing while the code was still valid, the
  second session's join flipped the revoked channel back to `open` — an agent action undoing the
  operator's brake. The state is now checked, and repeated in the UPDATE's `WHERE` because the read and
  the write are separate statements and a concurrent revoke can land between them.
- **A corrupt or unwritable COMM database took the whole service down.** Boot called `log.Fatal` on
  open/migrate failure, making "COMM may fail; the KB stays UP" false at the first failure an operator
  would actually hit. COMM now degrades: it logs, disables itself, and the knowledge base serves.
- **Client-supplied TTLs were unbounded**, so a session could mint effectively immortal messages and
  attachments that no sweep could settle, silently defeating the operator's live settings and pinning
  the file budget. A caller may now ask for a *shorter* lifetime, never a longer one.
- **Sequence numbers reset after the metadata sweep.** They were derived as `MAX(seq)+1` over surviving
  rows, so purging a direction's history restarted numbering at 1 — breaking the strictly-ascending
  promise and, the real damage, letting a retried cumulative acknowledgement computed against the old
  numbering settle brand-new messages. The high-water mark now lives in its own table (`channel_seq`,
  comm migration `0003`) and cannot go backwards.
- **The sender was never told when a message died.** The contract promised a status notice when a
  message expires unread or a required reply misses its deadline — the entire reason reply deadlines
  exist — and nothing implemented it, so a session whose peer died waited forever. The sweeper now
  delivers a server-authored `kind: "status"` message through the ordinary poll path
  (`{"status":"reply_overdue"|"expired","message_id":"…"}`), exactly once, deliberately bypassing the
  backpressure cap because a full channel is when a failure signal matters most. A peer cannot forge
  one: every peer send is `kind: "message"`.
- **Channel revocation did not stop bytes.** `GrantDownload` never re-checked that the channel was still
  open, so a recipient could keep minting download URLs for an already-offered file after the human
  revoked the channel.
- **A duplicate upload destroyed the winning one.** A second PUT hitting the `.part` lock marked the
  attachment failed while the first upload was still streaming. It now refuses only itself.
- **A verified upload could be thrown away.** If the peer's channel was momentarily at its backpressure
  cap, `CompleteUpload` failed and the handler deleted bytes that had already been fully streamed and
  checksum-verified. The bytes are kept and the sender retries the offer.
- **The documented recovery path was a dead end.** After a failed upload, re-offering with the same
  idempotency key returned the original attachment with no grant and no error — while the relay's own
  errors say "re-offer to retry". A failed attachment is now revived with a fresh grant.
- **Concurrent uploads could overshoot the storage budget.** The quota summed only bytes already on
  disk, so every in-flight upload was invisible to it; the declared size is now reserved at offer time.
- **A failed unlink leaked a file *and* its budget forever.** The sweep zeroed byte accounting inside
  the transaction but unlinked afterwards, and the collect query keys on non-zero accounting — so a
  file that could not be removed was never selected again. Accounting is now cleared only for bytes
  actually gone, and a row that still owns bytes is never purged.
- **Endpoints and settled channels grew without bound** under entirely normal use (sessions register
  once and never unregister). Idle endpoints with no traffic and no live attachment are now swept, and
  their channels cascade.
- **An answered request kept its body for days.** A request acked *before* its reply arrived retained
  its body (deliberately, so a recovered responder can re-read it) but nothing dropped it when the
  reply landed. It is dropped now.
- **The hearsay marker had a systematic blind spot.** It keyed on first delivery only, so under
  at-least-once semantics a message first delivered before the window but re-read and acted on inside
  it left no mark — a false negative in the system's normal operating mode. It now considers when the
  receiver actually acted.
- **The hearsay badge was missing from the only view that can promote.** It rendered on the review-queue
  listing but not on the entry page's review panel, where the Promote button lives — a warning that
  never reaches the moment of promotion is not a mitigation.
- `deploy/ken.service` gains `TasksMax`, `LimitNOFILE` and `MemoryMax`. Nothing inside the process
  bounds these: the HTTP server deliberately sets no write timeout, and COMM parks long polls and
  streams uploads.

### Docs
- **Corrected several claims that were aspirational rather than true.** COMM.md described a per-response
  random delimiter around message bodies (never implemented — the honest boundary is the structured
  field, and the doc now says so, along with what Ken genuinely cannot promise about a harness that
  flattens results into a prompt); claimed comm polls were exempt from the per-IP strike counter and
  that poll results advertised a minimum interval (neither is true — both are now named as open gaps);
  described the global byte cap as covering all COMM storage when it covers relayed files, with
  messages bounded by different means; and asserted a per-owner quota row that does not exist.
- **Removed stale "not yet implemented" framing** from README, DESIGN.md (D9 and §10), the CHANGELOG's
  own Unreleased section, and a comment in `internal/commserver/auth.go` — all written while COMM was
  a specification and left behind as it shipped.
- **COMM is now discoverable from where an integrator actually looks.** MCP-TOOLS.md notes the separate
  `/comm/mcp` endpoint and dedicated-token rule; AI-INTEGRATION.md gains a section covering the second
  registration, the same-actor requirement that makes hearsay marking work, and the two things an agent
  must know before using it. BACKUP.md now states explicitly that COMM state is deliberately
  unreplicated — a commitment COMM.md had made "in the same change" and that had not been kept.

- **New design contract: [docs/COMM.md](docs/COMM.md) — inter-session communication**, plus locked
  decision **D9** in [docs/DESIGN.md](docs/DESIGN.md). A specification only: **nothing is implemented**,
  and when it lands it will be **experimental and off by default** (so it sits inside
  [COMPATIBILITY.md](COMPATIBILITY.md)'s optional-and-off-by-default exclusion rather than the stable
  contract). Targeted at 1.2.0 as an additive MINOR — new tools, new scope, new settings, additive
  migrations, nothing removed or retyped.
- The feature: authenticated **session-to-session messaging** between AI sessions on the same or
  different machines, as an opt-in `internal/comm` subsystem with its **own** SQLite file, MCP endpoint
  and `comm` scope. Why it belongs in Ken: the deployment already provides the two things such a service
  needs and that are expensive to stand up twice — an authenticated endpoint every session reaches, and
  a host with spare capacity. Why it is walled off: message traffic is high-churn and **expendable**,
  knowledge is low-churn and **durable**, so separate files keep ephemeral WAL churn out of the
  replicated database and out of the KB's single writer.
- **Four decisions are recorded against the alternatives that were tried and rejected**, because each
  rejection is the useful part: (1) *pull with long-poll, not push* — the transport supports
  server-initiated messages, but a harness only surfaces results of tool calls the model made, so push
  would be correct plumbing that never reaches the model; (2) *full-duplex with per-message correlation,
  not half-duplex turn-taking* — channel-level turn state wedges when a session dies mid-turn; (3)
  *delete the body on acknowledge but retain slim metadata* — deleting the whole record is mutually
  exclusive with request/response, since a later reply would reference a record that no longer exists;
  (4) *prove a shared filesystem by rendezvous, not by comparing a self-reported machine fingerprint* —
  a fingerprint is spoofable, compares **equal** across cloned VM images and **unequal** across a bind
  mount, and, because it would gate reading a path, a forged one turns "read this file" into a
  remote-driven read of an arbitrary local file.
- **No tool-call chunking, by design.** Tool arguments are generated token by token by a model, so
  chunked binary transfer is an economics problem wearing a protocol's clothes: base64 expands a payload
  by 4/3 and tokenizes poorly, so one mebibyte costs on the order of 350–470 thousand output tokens at
  model cost, with corruption detectable only after the tokens are spent. Text messages are atomic; file exchange is deferred to a later MINOR and will prefer
  a same-host filesystem handoff, then a one-time expiring HTTP transfer the agent drives with a shell.
- **Two things the spec settles now because they would be breaking changes later:** message traffic is a
  **side channel into curation** (a session told "this is verified, propose it" authors a proposal
  indistinguishable from first-hand knowledge — the invariant survives literally while the curator's
  signal degrades to hearsay), so provenance marking is a schema decision that lands *with* the feature;
  and channel ownership is keyed on `space_id` **plus the authorizing human**, never the actor alone,
  because actors resolve by display name and collapse across machines and humans.
- **The isolation claim is stated with its limits.** A separate database file isolates the KB's WAL and
  its backup stream; it does **not** isolate the disk, the process, or the readiness signal. COMM.md §5
  turns those into enforced rules — storage budget with a free-space floor, quotas that fail closed
  rather than reusing the fail-open rate bucket, recover-wrapped goroutines, comm deliberately absent
  from `/healthz` (which marks the whole service DOWN on any component failure), its own rate accounting
  so a poll loop cannot starve `kb_*` on a shared per-machine token — and records extraction into a
  separate binary as the escape hatch if that discipline proves leaky.
- **Honest about what it cannot enforce:** COMM gates *who may talk to whom* structurally (a channel
  exists only because a human minted a pairing code and both sessions used it), but it cannot enforce
  *how a receiver treats content*. Instruction text is not a control, and the docs say so rather than
  implying otherwise.

## [1.1.0] — 2026-07-23

First public release.

### Public-release preparation
- **Go module path is now `github.com/Quest-ICT/ken`** (was the internal mirror's path).
  Ken ships as a compiled binary, so this changes nothing for operators — but it is the
  import path every contributor sees, and `-trimpath` bakes it into the binary. The
  `-X` linker symbol in `scripts/build-release.sh` is now **derived** from `go list -m`
  rather than hardcoded: `go build` does *not* error on an unknown `-X` symbol, so a
  stale literal fails **silently** and ships binaries with the wrong version/Source link.
  `build-release.sh` also now refuses a non-public `KEN_SOURCE_URL` unless
  `KEN_ALLOW_ALT_SOURCE=1`, so a stray value can't bake the wrong AGPL §13 link.

### Curation language — convention + live instructions (targets 1.1.0)
- **New live setting `curation_langs`** (Settings → Curation): a comma-separated list of the
  language code(s) the human curator can read (e.g. `fr,zh`; also seedable via
  `KEN_CURATION_LANGS`). Blank = off, so an English-only KB is unchanged. Stored verbatim;
  the normalized primary-subtag set (`en-US`→`en`) is what consumers read.
- **Agents are told to author in the curation language.** When `curation_langs` is set, the
  MCP `initialize` instructions gain a paragraph naming the language(s) and directing the
  agent to write every human-readable field (title/summary/problem/solution/rationale/caveats)
  in one of them — while keeping triggers, code, identifiers and verbatim error text in their
  original form as language-neutral retrieval keys. This is what makes a non-English curator's
  KB reviewable going forward: new proposals land in a language they can promote.
- **MCP instructions are now live.** The server is rebuilt (behind an atomic pointer) when
  `curation_langs` changes, so a settings edit reaches new connections without a restart —
  fixing the prior behavior where the instruction string was fixed at process start.
- **Auto-detected content language + comprehension guardrail.** Migration `0009` adds a
  nullable `entry_version.content_lang`, auto-set by a new `internal/lang` detector (whatlanggo)
  over the PROSE fields at write time — on the **delta** for an enhancement, so a small foreign
  addition to an otherwise-in-language entry is still caught. It feeds two surfaces only (never
  retrieval): the `/proposals` review queue shows a **Language** column and flags out-of-language
  proposals, and a **server-side gate in `store.Promote`/`Repromote`** refuses to promote a version
  whose language isn't a curation language ("can't promote what you can't read"). Fails **open**:
  with the feature off, or an undetected/legacy version (NULL/`und`), nothing is flagged or blocked —
  so turning it on never walls off the existing corpus. `kb_search` results now include a `language`
  field so a polyglot agent can spot and re-author a stranded entry.

### v1.0 readiness hardening (from the pre-1.0 audit — 0 blockers, should-fix items)
- **Write-path error contract:** `kb_save` validates `kind` and each `link_type` and
  returns an actionable message — no more opaque "internal error" (bad kind) or
  silently-dropped link (bad link_type). New regression test.
- **OAuth consent CSP:** the `redirect_uri` host is validated before it is reflected
  into the consent page's `form-action`, so CSP metacharacters can no longer reach the
  header. New regression test; corrected a misleading "can't inject CSP syntax" comment.
- **Entry prose** preserves author line breaks (`white-space: pre-wrap`) instead of
  collapsing multi-paragraph content into a wall of text.
- **Upgrades take a pre-upgrade DB snapshot** (`backups/pre-upgrade-<stamp>.db`, via the
  currently-installed binary, before the new release's migrations run) — a clean rollback point.
- **All web-UI flash messages are now translated** (en + es) via an arg-carrying
  `flashRedirect` helper; nothing user-facing in the UI is hardcoded English anymore.
- **[COMPATIBILITY.md](COMPATIBILITY.md)** documents what SemVer covers at 1.0 (MCP tool
  contract, CLI, `KEN_*` env, token format, forward-only DB schema).
- **Embedding config (`KEN_EMBED_*`) is documented** (usage help + INSTALL.md).
- **Distribution bundle ships `LICENSE` + a generated `THIRD-PARTY-NOTICES`**, and
  `version.SourceURL` is injectable at build time (`KEN_SOURCE_URL` → `-ldflags`).
- **Migration idempotency** regression test; **first-run setup gate fails closed** on an
  empty setup token.
- **Public-repo cutover:** Ken's source is **github.com/Quest-ICT/ken** (AGPL-3.0).
  `version.SourceURL`, the install/download docs, and `ken-upgrade` now use the **public
  GitHub Releases**, so upgrading needs no download token or private registry credentials.
  Removed the now-dead authenticated-download config example.

## [0.6.4] — 2026-07-21

### Added
- **Ken is now licensed under the GNU Affero General Public License v3.0** (`AGPL-3.0-only`; see
  `LICENSE`). Because Ken is a network service, the web UI footer links to the source repository so a
  running instance offers remote users its Corresponding Source, as the AGPL **§13** network-interaction
  clause requires (`version.SourceURL`, shown on every page including the public login and setup screens).
  Added `CONTRIBUTING.md` (Developer Certificate of Origin sign-off; the CLA path for any future
  dual-licensing is noted), an SPDX identifier, and a `footer.source` string (en + es). **Before going
  public, point `version.SourceURL` at the public repository.**

### Changed
- **Favicon is now the compass mark** from the web UI — the azure compass on a dark rounded badge —
  replacing the old amber logo. `ken-logo.svg` is redrawn as a favicon-optimized, self-contained badge (real
  colors, no `currentColor`), and `favicon-32.png` / `favicon-180.png` / `favicon.ico` are regenerated from
  it (librsvg). The favicon `<link>`s are version-busted (`?v=<version>`) so returning browsers pick up the
  new icon.

## [0.6.3] — 2026-07-21

### Fixed
- **Layout shifted sideways between pages.** A tall page (dashboard) shows a vertical scrollbar and a short
  one (search, proposals) does not, so on systems with classic space-taking scrollbars the centered layout
  jumped right by the scrollbar width when navigating between them. Added `scrollbar-gutter: stable` on
  `html` to always reserve the gutter, so the layout stays put across pages.

### Changed
- **Cache-busting for static assets.** `app.css` and `app.js` are now referenced with a `?v=<version>`
  query, so a UI change reaches a returning browser immediately after an upgrade instead of being masked by
  the 1-hour asset cache (`cache-control: max-age=3600`). No hard-refresh needed on future releases.

## [0.6.2] — 2026-07-21

### Fixed
- **Login / auth card was left-aligned instead of centered.** The `.auth-card` sat at the left edge of the
  page container (which spans the full width); `place-items:center` on `.auth-main` only centered the wide
  container, not the card inside it. Added `margin-inline:auto` so the card is centered at every viewport
  width (verified desktop + mobile).
- **Top-bar language selector looked like a form field.** It rendered with a border and a surface
  background — a "white rectangle" in the light theme. The top-bar `.lang .select` is now borderless and
  transparent (just the globe, the language name, and the dropdown caret); the caret is retained. Other
  selects (search scope, browse filters, settings) keep their field styling — only the top-bar language
  control changed.

## [0.6.0] — 2026-07-21

### Added
- **Redesigned, themeable web UI** across every page (login, setup, consent, dashboard,
  search, browse, entry, proposals, tokens, settings): a dark-default + light theme built
  on CSS custom-property tokens, resolved **server-side** from the `ken_theme` cookie so
  there is no first-paint flash. IBM Plex type with a system fallback, inline-SVG compass
  logo and icons, and the "Lineage Rail" version history.
  It makes **zero external requests** — every stylesheet, script, font and icon is
  same-origin — so it is clean under the strict self-only CSP and works fully offline.
  One small `app.js` with delegated `data-*` handlers; no inline scripts or handlers
  (the CSP forbids them). Replaces `ken.js`.
- **Multilingual UI.** Every page renders through the reloadable i18n catalog: English and
  Spanish embedded, with drop-in `messages_<lang>.properties` via `KEN_I18N_DIR` to
  override or add a language at runtime (no rebuild, no restart); resolution falls back
  language → English → key. Count-aware plurals (`TN`, `.one`/`.other`) so it reads
  "1 proposal is waiting", not "1 proposals". Settings field labels, help text and group
  names are translatable too, with the Go registry's English as the fallback.

## [0.6.1] — 2026-07-21

### Fixed
- **Brand logo rendered as literal `<svg …>` text instead of the compass mark.** In `base.html` the logo
  SVG was assigned to a Go template **string** variable and printed with `{{$brand}}`; `html/template`
  auto-escapes string values, so the markup showed as escaped text in the top bar on every page. Fixed by
  inlining the SVG as literal template markup (the same way the consent/setup marks already were).

### Changed
- **Multilingual UI completeness.** Following an audit of the 0.6.0 port, the remaining English-only strings
  on non-English pages are now translated: the promote/reject/revert **confirmation dialogs** on an entry,
  the page **`<meta name="description">`** and the brand/primary-nav **`aria-label`s**, and all
  **controlled-vocabulary domain values** — entry *kind*, version *state*, *staleness*, *lifecycle*,
  activity *event*, browse *sort*, and agent *kind* — now render through a new `Enum` view helper
  (`{{$.Enum "state" .State}}`) backed by `enum.<class>.<value>` catalog keys. English values are identity
  (the UI is unchanged); Spanish is translated; a missing key falls back to the raw value, so no unmapped
  value ever shows as a key. Raw values are still used for CSS hooks, `<option>` values and conditionals.

### Added
- **Redesigned web UI — a themeable, self-contained design system.** Every page (login, setup, consent,
  dashboard, search, browse, entry, proposals, tokens, settings) is rebuilt on a shared visual identity:
  **dark by default with a light theme**, driven by CSS custom-property tokens (`prefers-color-scheme`
  plus a persisted `ken_theme` toggle resolved server-side, so there is no first-paint flash), an IBM Plex
  type pairing (system-stack fallback), an inline-SVG compass logo + icon set, and the **"Lineage Rail"**
  append-only version history. **Zero external requests**: all CSS, JS, fonts and icons are served
  same-origin, so the UI stays clean under the strict self-only Content-Security-Policy and renders fully
  offline. Interactivity (theme + language switch, copy-to-clipboard, confirm-guarded destructive actions,
  password reveal, mobile nav) is one small same-origin `app.js` using **delegated `data-*` handlers** — no
  inline scripts or event attributes, which the CSP forbids.
- **Multilingual web UI (every page).** The UI resolves a per-request language and translates through a
  **reloadable message catalog**. **English and Spanish ship embedded**; drop a
  `messages_<lang>.properties` file into the external i18n dir (`KEN_I18N_DIR`, default `<data-dir>/i18n`)
  to **add a language or override any string at runtime — no recompile, no restart** (picked up within a
  couple of seconds). A missing key falls back to English, then to the key itself. Language is chosen from a
  header **selector** (a `ken_lang` cookie), seeded from `Accept-Language`; the selector auto-lists whatever
  languages are present (each file declares its own endonym via `lang.self_name`). Includes **count-aware
  pluralization** (`.one`/`.other` keys, so a count of 1 reads "1 proposal is waiting", not "1 proposals"),
  and the **settings page's field labels, help text and group names are translatable too** (mirrored in the
  catalog; the Go settings registry stays the English fallback). New package `internal/i18n`. Scope: the
  human web UI only — the AI/MCP surface and the logs stay English. See [docs/I18N.md](docs/I18N.md).

## [0.5.3] — 2026-07-20

### Changed
- `kb_search` now advertises the **`all`** scope in its MCP tool schema
  (`curated | proposals | history | all`). The scope was already accepted; this only makes the tool's
  self-description match its behaviour (and the docs).

### Docs
- Brought the README and docs (DESIGN, INSTALL, MCP-TOOLS, AI-INTEGRATION) up to date with the OAuth
  connector, the Browse page, and server-delivered instructions; capitalized "Ken" in the copy-paste
  prompt blocks. MONITORING / REMOTE-UPGRADE / BACKUP verified already current.

## [0.5.2] — 2026-07-20

### Added
- **The MCP server teaches connected agents how to use Ken.** The server now ships usage instructions (the
  warm-up → search-first → record-outcome → save/enhance loop, and the human-only curation model) in its
  MCP `initialize` response, so an AI that connects over the connector learns the workflow **without a human
  pasting a prompt**. Distilled from `docs/AI-INTEGRATION.md`.

### Changed
- **"Ken" is now capitalized as the product name** across the web UI, log output, and documentation (prose
  references). Identifiers are deliberately unchanged — the `ken` binary/CLI, `KEN_*` env vars, `ken_*`
  cookies/tokens, the Go module path, filesystem paths, and hostnames all stay lowercase.

## [0.5.1] — 2026-07-20

### Fixed
- **OAuth consent "Approve" did nothing in the browser.** The strict `form-action 'self'` CSP also governs
  the redirect that *results from* a form submission (browsers enforce `form-action` on form-submission
  redirects), so approving created the grant server-side but the browser then silently refused to navigate
  to the client's callback (e.g. `claude.ai`). The consent page now widens `form-action` to include the
  **server-validated** client redirect origin. (curl-based end-to-end tests can't catch this — it's
  browser-only CSP enforcement; added a header regression test.)

## [0.5.0] — 2026-07-20

### Added
- **Optional OAuth 2.1 authorization server — connect claude.ai as a custom connector.** With
  `KEN_OAUTH_ENABLED` set, Ken serves OAuth discovery (RFC 8414 + RFC 9728), dynamic client registration
  (RFC 7591), and an authorization-code + refresh-token flow with **mandatory PKCE-S256**, so a claude.ai
  personal-account *custom connector* (whose UI is OAuth-only) can authenticate with the normal **Connect**
  button — no beta request-headers field, no pasted token. A connector you approve gets the standard agent
  capability set (`read`/`write-draft`/`propose`) and **never `curate`**; every write is attributed to a
  *Claude* connector actor, and you revoke it from the Tokens page (*Connected apps*). Security: single-use
  short-lived codes, exact-match redirect URIs, rotating refresh tokens with **reuse-detection that revokes
  the whole grant**, opaque hash-only-stored tokens, and instant grant revocation re-checked on every MCP
  call. **Off by default** — static bearer tokens are unchanged whether or not it is enabled. See
  [docs/OAUTH.md](docs/OAUTH.md). New migration `0008_oauth.sql`.
- **Browse all entries.** A new **Browse** page (top-nav) lists the knowledge base as a grid independent of
  search, with filters (kind, category, staleness, lifecycle), sorting (updated / title / uses / created /
  kind), pagination, a sticky header, and pending-proposal / lifecycle badges. Backed by a single-table
  query over the denormalized `entry` row (no version join).
- **A real home page.** `/` is now a landing dashboard — knowledge-base stats (curated entries, pending
  review, versions, active tokens), a highlighted "pending review" tile plus a **Review N pending
  proposal(s) →** shortcut when the queue is non-empty, and a **Recent activity** feed (curation events in
  the last 30 days). Search moved off the home page and is reached via the **Search** link in the top nav.
- **Search: an `all` scope + single-page results.** The Scope selector gains **all**, which drops the
  per-state filter so a query reaches every version in every state (curated + proposed + history) — one
  entry may appear once per matching version, the deliberate "show me everything" view. Results now render
  on the **same** page as the search form (the form and scope stay put, pre-filled), so refining a search
  no longer needs the old "← new search" round-trip.
- **Back-to-top button on every page.** A floating control appears bottom-right once a
  page is scrolled past ~300px and smooth-scrolls to the top on click. Implemented CSP-clean (external
  `/static/ken.js`, no inline handler), honours `prefers-reduced-motion`, and is inert without JS.

## [0.4.2] — 2026-07-20

### Added
- **Curated-head recovery for mis-ordered promotions.** A **"Revert to this"** action on the entry page's
  history rows re-promotes a superseded/rejected version back to the curated head (`store.Repromote`,
  `POST /entry/{slug}/revert/{vid}`), and the Promote button now shows a **downgrade confirm** when the
  pending revision is older than the current head. Promoting stacked proposals in the wrong order (newest
  first) could silently regress an entry to older content with no way back via the UI; now it warns, and is
  recoverable.

### Fixed
- The proposal **Reject** and token **Revoke** confirm dialogs never fired — inline `onsubmit` handlers are
  blocked by the strict `default-src 'self'` CSP, so the forms submitted immediately. Replaced with a
  CSP-clean `data-confirm` attribute handled by one delegated listener in `/static/ken.js`.

## [0.4.1] — 2026-07-20

### Added
- **Scoped remote-upgrade tooling** (`scripts/ken-upgrade`, `deploy/ken-upgrade.sudoers`,
  `docs/REMOTE-UPGRADE.md`) — a root-owned wrapper that lets an
  unprivileged deploy user run *only* the Ken installer via one `NOPASSWD` sudoers rule (no full
  root, no editing `/opt/ken`). It fetches the installer from the release host, **checksum-verifies**
  it, keeps any download credential in a root-only `curl -K` config (never on argv), and **validates
  every argument against a strict allowlist** (`--version`, `--tls acme|off`, `--domain`, `--email`,
  `--port`, `--open-firewall`/`--no-firewall`, `--dry-run`) — refusing path/prefix/user flags that
  could escalate.

### Changed
- **Review UI consolidated onto the entry page** — the proposal queue's separate `Review →` page is
  gone; the promote/reject actions now live on `/entry/{slug}` itself. For a pending **enhancement**
  the entry shows the curated head, then a highlighted *Pending proposal* panel with the proposed
  content **full-width** (replacing the cramped side-by-side diff); for a **brand-new draft** the entry
  body already *is* the proposal. The queue drops its Review column — click an entry to review it.

### Docs
- `docs/INSTALL.md` — document the always-latest download links
  (`…/releases/latest/download/ken-latest-linux-<arch>.bin`), enabled by the
  stable-named release aliases added in 0.4.0.

## [0.4.0] — 2026-07-19

### Added
- **Health & metrics** (`internal/metrics`, `internal/health`) — a small, dependency-free
  observability surface modeled on the usual Actuator/Micrometer status-class buckets, but hand-rolled
  so the footprint stays flat (no `client_golang`; kilobytes of RAM; nothing written to disk):
  - `GET /health` — readiness JSON `{"status":"UP","components":{db,storage}}` (public; per-component
    details only for loopback/token/CIDR callers; **503** when a component is DOWN). `GET /healthz`
    stays the plain liveness probe.
  - `GET /metrics` — Prometheus text exposition: web request counts/latency by outcome, MCP
    `kb_*` tool calls, knowledge-base gauges (entries, versions, **proposals pending**, embedding
    coverage, users, active tokens), rate-limit rejects/blocks, auth failures, and process/DB-pool
    stats. **Gated**: loopback + `KEN_METRICS_CIDRS` always allowed, any other scraper needs
    `KEN_METRICS_TOKEN` (constant-time bearer, authorized on the direct peer — never `X-Forwarded-For`).
    On by default (loopback-only); `KEN_METRICS=off` removes it. The gate resolves the client IP
    through the trusted-proxy machinery, so a co-located reverse proxy can't make every scraper look
    local. Only `/healthz` is exempt from the rate limiter; `/health` and `/metrics` are throttleable
    (loopback / allow-CIDR callers bypass it anyway).
  - A drop-in [`monitoring/`](monitoring/) bundle: Grafana dashboard, Prometheus alert rules
    (incl. a `KenProposalsBacklog` curation nudge), and a scrape/metric-reference README; operator
    guide in [`docs/MONITORING.md`](docs/MONITORING.md).

### Docs
- `docs/AI-INTEGRATION.md` — add a third, **recommended-for-capable-agents** integration prompt: a
  **hybrid** that leads with the three load-bearing ideas (sole author, append-only, human-only promotion)
  then gives the §3 terse mechanics, plus a chooser positioning all three prompts (terse / why / hybrid).

## [0.3.2] — 2026-07-19

### Added
- **Favicon & brand logo** — the web UI ships Ken's compass/quill mark: an SVG favicon with PNG (32px and a
  180px apple-touch icon) and a multi-size `.ico` fallback, and the logo now sits in the header brand.
  Assets are committed and served from the embedded `/static/` tree, so the release build needs no image
  tooling.
- **Running version in the page footer** — every page shows `ken v<version>` in the footer (from
  `internal/version`, injected at release build), so it's obvious which build is live.

### Changed
- **Token page** (`/tokens`) — the one-time-secret reveal is now readable in **dark mode** (it used the
  theme foreground on a fixed light-green box, so text went near-white-on-pale) and the registration
  example is **copy-paste-ready**: it carries the deployment's **real host** (derived from the request —
  `Host`, or a trusted proxy's validated `X-Forwarded-Host`, with an `https` scheme when TLS terminates
  here), single-quoted so an IPv6-literal host can't trip shell globbing, instead of a `<ken-host>`
  placeholder. The token and the command each get a one-click **Copy** button — a small same-origin
  `/static/ken.js`, so the strict `default-src 'self'` CSP is unchanged — with an `aria-live` announcement
  for screen readers. The host is run through an allowlist sanitizer before it reaches the shown command.

### Docs
- `docs/AI-INTEGRATION.md` — a verified **connector reach/restart matrix**: adding Ken once as a claude.ai
  Connector needs no app restart and reaches Chat, Cowork and Claude Code (each with its own per-surface
  activation step) but **not** Design; plus the reminder that the connector must carry the
  `Authorization: Bearer` token or it 401s.

## [0.3.1] — 2026-07-19

### Docs
- `docs/AI-INTEGRATION.md` — how to connect Ken across a fleet: **token strategy** with two routes (Route A:
  one token added as a claude.ai Connector via its beta *Request headers* field, then inherited by Claude
  Code everywhere; Route B: `claude mcp add --header` per machine, one token each), when to pick which, how
  to **switch Route B → Route A** later, that "Claude Code in the desktop app" is Claude Code (not the chat
  connector), a **"set up once, not per session"** clarification, and a copy-paste **recipe to harvest an
  existing session's knowledge into Ken**.

## [0.3.0] — 2026-07-19

### Added
- **Rate limiting** (`internal/ratelimit`): a per-IP token-bucket guard as the outermost
  handler — loopback + `KEN_RATELIMIT_ALLOW_CIDRS` + `/healthz` are exempt — returning
  `429` + `Retry-After` over the limit and auto-blocking repeat offenders with `403` for a
  lockout window, plus a per-token limit on MCP (keyed by token id). On by default; tunable
  via `KEN_RATELIMIT*` (see `ken --help`). Client-IP resolution (trusted-proxy aware) was
  extracted to `internal/clientip` and is now shared with the login guard. IP-keyed limits
  and lockouts key IPv6 clients by their `/64` so address rotation can't evade them.
- **Agent-token management in the web UI** (`/tokens`, superadmin): issue, list and revoke agent
  MCP tokens from the browser — scopes limited to read/write-draft/propose (never curate); the
  secret is shown once. The same credentials as `ken token add`.
- **Editable runtime settings in the web UI** (`/settings`, superadmin, `internal/settings`): retune
  the rate limits, login brute-force lockout, session TTL, trusted-proxy CIDRs and ACME domains.
  Changes are validated, persisted to `app_setting` (overriding env/defaults; a value equal to the
  default removes the override) and applied **live via an atomically-swapped snapshot — no restart**.
  ACME domains are additive-live (a settings edit can't remove the boot domain). TLS mode/cert remain
  config/restart-managed (a listener rebind can't be done live).
- **`KEN_TLS_QUIET_HANDSHAKE`** — drop the benign "TLS handshake error" scanner noise (no-SNI probes,
  EOF/reset, ALPN/cipher fingerprinting) from the log via a filtered `http.Server` ErrorLog, so real
  errors stand out. Off by default; the public `:443` gets this background scanning constantly.

### Changed
- The installer wizard now defaults HTTPS mode to **acme** (Let's Encrypt) instead of
  `off`, so a standalone install is HTTPS-first. An upgrade still inherits the installed
  unit's mode, and unattended `-y` without `--tls` stays plain HTTP (acme needs a domain).
- Secure/`__Host-` cookies are no longer inferred from `KEN_TRUSTED_PROXIES` (trusting a
  proxy's `X-Forwarded-For` does not mean it terminates TLS). Behind a TLS-terminating proxy,
  set `--secure-cookies` or `KEN_SECURE_COOKIES=on` explicitly. In-process TLS still forces it.

### Fixed
- The bounded-map idle sweep in the rate limiter and the login brute-force guard was
  unreachable (off-by-one), so the maps could fill and then silently fail open / stop locking;
  the sweep now runs and reclaims idle entries. Auto-block now counts *consecutive* over-limit
  rejections (reset on an allowed request), so a busy shared/CGNAT IP isn't blocked for bursts.

## [0.2.0] — 2026-07-19

### Added
- **In-process TLS / HTTPS** (`internal/webtls`). `KEN_TLS=acme` terminates TLS with
  Let's Encrypt (automatic issuance + renewal via ACME, HTTP-01/TLS-ALPN-01);
  `KEN_TLS=file` serves an operator-supplied PEM cert/key (hot-reloaded on renewal).
  Both run a `:80` listener that answers the ACME HTTP-01 challenge and 301-redirects
  everything else to HTTPS. Installer flags `--tls/--domain/--email/--cert/--key`
  (defaults the port to 443 and offers to open 80+443); the systemd unit and
  `docs/INSTALL.md` gained a standalone-HTTPS walkthrough. Secure/`__Host-` cookies
  are forced whenever TLS is on (or `KEN_TRUSTED_PROXIES` marks a TLS-terminating
  proxy in front of ken), and `KEN_DEV_TOKEN` is refused in any TLS posture. The
  `:80` redirect is host-checked against the domain allowlist (no open redirect),
  and file-mode certs hot-reload on any change; hardened per an adversarial review.

### Changed
- Plain HTTP (`KEN_TLS=off`, still the default) is now explicitly the
  *behind-a-reverse-proxy* option; standalone deployments should use `KEN_TLS=acme`.

### Docs
- `docs/AI-INTEGRATION.md` — the standard copy-paste prompt for integrating Ken into an AI agent's
  workflow (Claude Code or any streamable-HTTP MCP client): token + MCP-registration steps, a tight
  drop-in prompt and a longer explanatory variant, and per-project tuning notes. Linked from the README.

## [0.1.0] — 2026-07-19

First release: Ken is an AI-first personal knowledge base — a Go single binary that is
both a remote-MCP server (an AI coding agent stores and retrieves *curated* knowledge)
and a human curator web UI. SQLite is the single source of truth; enhancements are
append-only and the curated head moves only on human promotion.

### Added
- **MCP server** (streamable HTTP) with scoped bearer-token auth and 8 tools:
  `kb_search`, `kb_get`, `kb_save`, `kb_propose_enhancement`, `kb_flag_stale`,
  `kb_diff`, `kb_record_outcome`, `kb_recent_context`. A `kb_search`-issued dedup token
  structurally enforces search-before-save.
- **Storage & versioning** — embedded SQLite (WAL, pure-Go `ncruces/go-sqlite3`,
  FTS5 via `ext/fts5`); append-only model (`entry` + immutable `entry_version` +
  `curation_event` reflog); curated head advances only by human promotion.
- **Search** — keyword-first hybrid: FTS5 BM25 (prose) + trigram FTS (code), fused by
  Reciprocal Rank Fusion.
- **Semantic embeddings (optional, off by default)** — pluggable `internal/embed` SPI
  (HTTP OpenAI-compatible + offline hash provider); brute-force Go cosine over a plain
  `entry_embedding` BLOB table, RRF-fused into search; multiple models can coexist per
  version. `ken embed backfill|status`; enabled via `KEN_EMBED_*`.
- **Human curator web UI** — Argon2id (High) login, server-side sessions, per-session
  CSRF, login brute-force lockout; search/browse, entry + version history, and the
  proposal review queue (diff + promote/reject). The human curates; never authors.
- **First-run setup wizard** — mode derived from "no human users yet"; the setup page is
  gated by a one-time setup token logged at startup.
- **CLI** — `serve`, `token add|list|revoke`, `user add|list`, `backup snapshot|verify`,
  `import --dir` (flat-memory importer), `embed backfill|status`.
- **Backup & restore** — Litestream config (continuous) + nightly age-encrypted
  `VACUUM INTO` snapshots (`scripts/ken-snapshot.sh`); `backup verify` runs
  `integrity_check` + `foreign_key_check` + FTS5 integrity-check on both indexes + a
  MATCH canary + embedding vector-length parity. Runbook in `docs/BACKUP.md`.
- **Deployment** — self-extracting `.bin` installer (idempotent `install.sh` with
  SELinux relabel and opt-in firewall), `scripts/ken.sh` launcher, systemd units
  (`ken.service`, `ken-snapshot.service`/`.timer`), and an NSIS template for Windows.

### Security
- SHA-256 lookup for high-entropy bearer tokens (not Argon2); Argon2id (High profile,
  BouncyCastle-free, `golang.org/x/crypto`) for human passwords.
- Request-body caps (`MaxBytesReader`) on MCP and web; HTTP server read/idle timeouts
  (no write timeout, for SSE); trusted-proxy client-IP resolution (`KEN_TRUSTED_PROXIES`).
- Security headers (HSTS, nosniff, `SAMEORIGIN`, strict self-only CSP).
- `.gitignore` shields runtime secrets (`dedup.key`, `*.key`, `*.age`) regardless of path.

### Not yet implemented (planned)
- In-process TLS / ACME — Ken serves plain HTTP; terminate TLS at a reverse proxy
  (pass `--secure-cookies`).
- A general per-IP / per-token rate limiter — only a login brute-force lockout exists.
- git/Markdown mirror (deferred by design decision D5), local ONNX embedder + background
  re-embed job, and the `kb_link`/`kb_related` graph tools.

### Notes
- Built via multi-agent workflows and hardened before release: three adversarial reviews
  plus a ~60-agent full audit (0 critical/high) fixed every medium and every unambiguous
  low, each with a regression test. All documentation was reconciled against the code
  prior to this release.
- Windows installer: the NSIS `.exe` is not attached to this release (built separately
  where `makensis` is available); the Linux self-extracting `.bin` installers are.

[Unreleased]: https://github.com/Quest-ICT/ken/compare/v2.1.0...HEAD
[2.1.0]: https://github.com/Quest-ICT/ken/releases/tag/v2.1.0
[2.0.0]: https://github.com/Quest-ICT/ken/releases/tag/v2.0.0
[1.7.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.7.0
[1.6.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.6.0
[1.5.5]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.5
[1.5.4]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.4
[1.5.3]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.3
[1.5.2]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.2
[1.5.1]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.1
[1.5.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.5.0
[1.4.2]: https://github.com/Quest-ICT/ken/releases/tag/v1.4.2
[1.4.1]: https://github.com/Quest-ICT/ken/releases/tag/v1.4.1
[1.4.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.4.0
[1.3.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.3.0
[1.2.2]: https://github.com/Quest-ICT/ken/releases/tag/v1.2.2
[1.2.1]: https://github.com/Quest-ICT/ken/releases/tag/v1.2.1
[1.2.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.2.0
[1.1.0]: https://github.com/Quest-ICT/ken/releases/tag/v1.1.0
