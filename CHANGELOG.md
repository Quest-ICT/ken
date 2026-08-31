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

### Fixed

- **An empty briefing read as reassurance when a session's station was gone.** `station_me` knows
  when it has just created a station, and then reports `open: 0` tasks exactly as it would for one
  that genuinely has nothing outstanding. On a first run that is right; on a deployment where the
  conversation used to have a station, it is a session telling its human *"nothing is waiting on
  you"* about a post that no longer exists.

  collector-proxy-prod read precisely that after the estate was rebuilt from empty, having lost 34
  tasks and a notebook at rev 29, and named the shape better than we had: *"a consumer who had not
  exported would have discovered the loss by finding an empty briefing that said nothing was
  waiting on them — which reads as all clear, not as your data is gone."*

  A newly-created station on a deployment that already has others now carries a `relay_to_human`
  sentence saying so. **It is a sentence, not a flag** — the briefing reaches nobody unless the
  session says it out loud, which is the reason that field exists at all.

  **It claims only what Ken can see.** Ken does not know whether this conversation ever had a
  station or what became of it, only that others exist and this one is new — so the wording says
  that and hands the question to the human rather than asserting a loss it cannot verify. **The
  first station on a genuinely fresh deployment stays silent**, asserted as firmly as the warning
  itself: a warning on every first run is noise, and noise is how instructions get ignored.

## [5.0.2] — 2026-08-31

### Fixed

- **The upgrade procedure said `start` where it must say `restart`, and the difference leaves Ken
  running degraded while every liveness check reports healthy.** `ken-upgrade` starts the service
  as its last step — before the operator has run the SQL script — so `systemctl start` is a no-op
  on a running unit and the process keeps the pre-script database open.

  ken-prod-ops measured the window while upgrading to 5.0.1: unit **active**, `/healthz` **ok**,
  database **correctly upgraded**, messaging **entirely absent**. Both liveness checks passed, and
  the only signal was a log line nobody was required to read. An operator following the written
  steps stops there, because everything they were told to check says fine.

  That is this project's own defect class — a failure rendered identically to success — sitting in
  the instructions rather than the code. Corrected in `docs/UPGRADING-THE-DATABASE.md`,
  `docs/UPGRADING.md` and the upgrade script's own header.

- **The procedure assumed `sqlite3` was installed. It is not, on Rocky 10** — and not on the
  deployment that ran this upgrade. `docs/UPGRADING-THE-DATABASE.md` now carries a python3 fallback
  using the stdlib `sqlite3` module with `isolation_level=None`, which is required so the script's
  own `BEGIN`/`COMMIT` is not nested inside Python's implicit transaction.

  The claim that a database rewrite can be verified "with the `sqlite3` you already have" was the
  wrong half of a true statement: the plain SQLite **file format** is the guarantee worth leaning
  on, and the **client** is not.

## [5.0.1] — 2026-08-31

### Fixed

- **5.0.0's release bundle did not contain the upgrade script it tells you to run.** 5.0.0 made the
  database upgrade an operator's job — Ken refuses to start against a database at the wrong version
  and names `upgrade/comm-4.x-to-5.0.0.sql` — and `build-release.sh` staged `docs/`, `scripts/`,
  `deploy/` and `configs/`, because `upgrade/` did not exist when it was written. So the tarball
  refused to start and the fix was in a git checkout the operator might not have.

  The bundle now carries `upgrade/*.sql` and `docs/UPGRADING-THE-DATABASE.md`, and
  `verify-artifact.sh` **fails the release** if any script in the repo is missing from the bundle —
  counting files on disk in the unpacked tarball rather than trusting the build that produced it.
  Verified by building a bundle with the directory omitted and watching the gate refuse it.

  Nothing is wrong with a running 5.0.0. This only affects upgrading TO it from the tarball.

## [5.0.0] — 2026-08-31

**Every session reconnects again, and the database upgrade is now yours to run.** The second MAJOR
in two days, deliberately: with four sessions connected, one reconnect now costs less than two
later.

### Removed

- **The channel is retired (slice 7).** `comm_open_channel` is gone, with the `channel` table, the
  `ch:` scope namespace, the Comm page's channel card and its revoke control, the per-channel
  pending counter, the admin listing, and the channel arms of send, ack-scope and file-offer.
  4.0.0 had already removed its reason to exist — a link is created by the first message, so
  `comm_send{to_station}` reaches any station with nothing to open, join or expire.

  What the channel cost was a **second place for a conversation to live**: two stations could hold
  a pair scope and one or more channel scopes at once, each with its own sequence, backpressure and
  idea of what "the conversation" was, and every surface that counted, closed or listed traffic had
  to know about both.

  **`comm_channels` keeps its name** — it reports rooms and pairs, and renaming a tool costs a
  reconnect for nothing. **The `ken_comm_channels_open` metric is gone**, which matters to anything
  scraping `/metrics`.

- **Suspend is no longer retroactive**, and suspend/resume are now exact inverses. Suspending a
  link used to close its live channels, and resuming never reopened them — a reversible control
  that was partly irreversible. There is nothing left to half-restore.

- **The two MCP servers nothing served.** 4.0.0 deleted `/comm/mcp` and `/station/mcp` and left
  their servers standing: `main.go` stopped calling `stationserver.NewHTTPHandler` altogether, and
  commserver kept building a full MCP server, its middleware and its connect-time instructions on
  every boot, reachable by nothing. The station block had rotted exactly as unserved text does — it
  still told every session there were "THREE MCP SURFACES" and to ask their human for the others by
  name.

- **Dead mailbox columns:** `endpoint.secret_sha256` (creating a mailbox minted a random secret and
  hashed it purely to satisfy NOT NULL; nothing has verified one since 4.0.0), `secret_rotated_at`,
  `rotate_count`, `bound_by_station_key_id` — plus `LiveEndpointForStation` and
  `store.TokenIsRevoked`, whose last callers went with the channel.

### Changed

- **KEN NO LONGER MIGRATES DATABASES.** `schema/ken.sql` and `schema/comm.sql` create a whole
  database in one step, applied only when the file is empty; on an existing database Ken reads the
  recorded version and **refuses to start** if it is not the one the binary requires. Upgrading is
  a deliberate act performed with stock `sqlite3` — scripts in `upgrade/`, procedure in
  `docs/UPGRADING-THE-DATABASE.md`. ken.db is unchanged at 26; comm.db moves 21 → 22.

  **The refusal is the load-bearing half.** Without it this fails silently in the worst direction —
  a binary opening a database whose shape it does not know. Measured before 4.0.0 going the other
  way: the v3.42.0 binary booted against a 4.0.0 database with an ordinary startup log and then
  500ed on a table that no longer existed.

### Fixed

- **File-offer idempotency was unenforced for every room and pair offer.** `idx_attachment_idem`
  was keyed on `channel_id` while the lookup has used `scope_id` since comm 0017; SQLite treats
  NULLs as distinct, so those offers sat outside the unique index entirely and a repeated key
  created a second attachment. The upgrade script de-duplicates before building the correct index,
  because building it over existing duplicates aborts.

- **Creating a database left foreign keys OFF for the process.** The generated schema opened with
  `PRAGMA foreign_keys=OFF`, and a pragma is per CONNECTION — so the writer the server holds for
  its whole life had every `ON DELETE CASCADE` inert. Purging a message stopped taking its
  deliveries, SQLite reused the freed rowid, and the next insert collided with a delivery row that
  should not have existed.

- **Cumulative ack lost its subject** when the channel arm went: a two-station conversation, now
  the ordinary case, could be acked one message at a time and no other way. It gains a pair arm,
  scoped to stations comm knows — an earlier version accepted any id, which made acking a room you
  are not in resolve as a "pair" and turned the uniform refusal into an existence oracle.

- **`station_directory` listed stations it gave no way to reach.** The row carried name, purpose,
  `last_seen_at`, `linked` and `staffed` — and no `station_id`, which is the only thing
  `comm_send{to_station}` accepts and the only thing that identifies a station now the pairing code
  is gone. `st.StationID` sat two lines away in the loop, used for the staffing lookup. Its own
  comment claimed the row MIRRORED `comm_directory`'s, which carries both the id and an
  `address_with` hint; nothing compared them, so nothing said otherwise. Both fields are now built
  by a constructor rather than a struct literal, because both are strings and a literal that omits
  them marshals cleanly and lists a station nobody can reach.

- **`station_me` accepted a self-description and threw it away.** It is applied at the bottom of the
  handler, and BOTH `session_key` branches — station created, and station already claimed — return
  before that line. `session_key` is the recommended way to call the tool and now the only reliable
  one, so the fields were dead for exactly the population using it correctly. Applied on all three
  paths through one helper.

- **The boot that migrates said nothing.** ken-prod-ops measured the boot that took the live
  database from ken 24 → 26 and comm 19 → 21: thirteen lines, and
  `grep -icE "migrat|foreign|fk|integrity|schema"` returned **zero**. Two migrations ran, the
  foreign-key check ran and passed, and none of it was visible — so an operator could not tell "the
  integrity check passed" from "there is no integrity check". Both boot paths now report the
  version, what moved, and that the check ran; the same grep returns 4.

- **An abandoned `/authorize` left a permanent active grant.** The grant row is created at consent,
  not at token exchange, so a retry or a closed consent screen left one with no tokens and
  `revoked_at IS NULL` forever. ken-prod-ops measured two grants for one client 2.3 seconds apart,
  one live and one empty, both listed as connected applications. A successful exchange now revokes
  grants that have issued no token AND whose authorization code is gone — never one still mid-flow,
  which is the arm that matters: a sweep taking that grant would break the consent a human is
  completing.

### Changed

- **`TestBinaryRefusesAPartialCredential` did not test a partial credential** — only an unknown
  bearer and an absent one, both refused by the ordinary path since 1.x. The 4.0.0 claim that a
  credential holding *some* capabilities is refused at the transport was documented in UPGRADING.md
  and the release notes with nothing exercising it. Now minted and presented for real: a read-only
  token is refused.

## [4.0.1] — 2026-08-31

A patch release, cut for ONE reason: on 4.0.0 most of the station surface is unusable on any
client that re-initialises between messages, which is the common case rather than the corner.
The rest shipped with it because it was already on `main` and tested.

### Fixed

- **The console told operators to perform deleted actions.** 33 strings across three locales still
  named mechanisms 4.0.0 removed: *"Registered sessions"* on a console where nothing registers,
  *"This endpoint's secret has been replaced"* for a secret that no longer exists, *"Credentials
  these endpoints depend on"*, and — the one an operator would act on — `rooms.member_deaf_help`
  saying *"the session needs to bind"* when binding was deleted. A wrong label is cosmetic; an
  instruction to perform a deleted action sends the only person who can fix the problem looking
  for a control that is not there.

  All reworded in English, Spanish and French. Two dead things went with them: the *rotated* badge
  on the Comm page, and the *Bound by* column — nothing has written `secret_rotated_at`,
  `rotate_count` or `bound_by_station_key_id` since station keys and secret rotation were retired,
  so both could only ever render empty.

  **`TestNoOperatorStringNamesARetiredMechanism` replaces a discipline, not a test.** v3.42.0 had a
  prose gate over comm strings; the wave deleted it correctly, because the mechanism it policed was
  gone — and nothing replaced the habit, which is how these 33 shipped. The new gate matches
  PHRASES rather than words, so "registered" stays legal for OAuth client registration and "rotate"
  stays legal in the sentence that says there is nothing to rotate.

- **Most of the station surface was behind a coin flip.** `station_me`, `comm_poll`, `comm_send`
  and `comm_directory` accepted a `session_key`; the other nineteen station tools did not declare
  the field, so `additionalProperties:false` REJECTED it and they had nothing to identify the
  caller but a map keyed on the MCP session id. ken-prod-ops measured that map failing on a client
  which re-initialises between messages: `station_me` succeeded and a `station_note_write` seconds
  later, same conversation and same key, was refused *"this connection has not said which station
  it is"*. Notebook, tasks, locker and vault all depended on whether a connection happened to
  persist.

  Every station tool now takes `session_key`, and `requireStation` resolves through
  `station.Resolve` — which already preferred the key and fell back to the binding; the station
  surface simply never handed it one. Resolve also asks the liveness and archived questions
  `requireStation` did not, so an archived station now gets the refusal that names its remedy.

  The three listing tools took a bare `struct{}`, which is why they could not be told anything at
  all: with no field, `session_key` was not merely undeclared but unrepresentable.

- **The upgrade smoke test could pass by reading nothing.** `runFor` read the subprocess's output
  buffer while `os/exec` was still filling it, behind a 200ms "let the startup lines flush" sleep.
  Every assertion over that log is NEGATIVE — "the log must not say DEGRADED" — so a read that lost
  the race made the test green by having nothing to read. That is the exact failure mode
  `cmd/ken/smoke_test.go` was written to catch, reproduced inside the instrument, over the one
  migration three audit rounds found broken three separate times. `runFor` now kills the child and
  calls `cmd.Wait()` — which joins the copiers — before reading, so the log is complete and the read
  is race-free, with no sleep to tune. The upgrade test additionally anchors on a line HEAD always
  prints, so an empty log fails loudly instead of satisfying every negative check.

### Changed

- **A database is created in ONE step; the migration chains are collapsed.** `migrations/` held 26
  files and `internal/comm/migrations/` 21, and every fresh install replayed all of them — creating
  tables to drop them again, adding columns later files removed, rebuilding the same table several
  times. Ken is installed fresh, so that journey was pure cost, and nobody could read the schema
  without replaying it mentally. Both are now a single generated `*_init.sql`.

  The files keep the version numbers **26** and **21** deliberately: `dbmigrate` tracks applied
  migrations by the number in the filename, so a database that already recorded those finds nothing
  pending and is untouched. Numbering them `0001` would make every existing database try to create
  tables it already has.

  Correctness is not asserted, it is generated and proved: the files are dumped from a database
  built by the real chain, and the two schemas were diffed byte-for-byte before the chain was
  deleted. `TestUpgradeFromPreviousReleaseDoesNotDegradeComm` now builds **v4.0.0** and boots HEAD
  on its database, so the claim the running deployment depends on is exercised rather than reasoned
  about.

- **The reply-overdue rule no longer reads migration archaeology at runtime.** It carried a lower
  bound of `schema_migration.applied_at WHERE version = 9`, which existed to protect delivery rows
  written before comm 0009 began linking replies. Such a row cannot exist in a 4.0.0 database, and
  cannot be created at all now the chain is one file, so the clause could never exclude anything.
  Removing it changes no behaviour — the old guard was `COALESCE(..., '')`, which failed open.

- **CI runs `go test -race ./...`.** It never did, which is why the above shipped: the race is
  invisible to `go test ./...`, and that is all CI and the release workflow ran. A separate step,
  because the detector needs cgo and the rest of the build is deliberately `CGO_ENABLED=0`.

## [4.0.0] — 2026-08-30

### THE BREAKING WAVE

**MAJOR. SCHEMA CHANGE. Rule 4 is SUSPENDED for this release by decision** — a release carrying a
migration normally carries nothing else, and staging this one would have meant several reconnects,
which is exactly what it exists to avoid. `ken.db` **24 → 26**, `comm.db` **19 → 21**.

**Every session must reconnect. Nothing is migrated.** Vlad, ruling on both: *"I prefer breaking
them now that I can reconfigure every existing session rather than keep workspace laying around for
future users"*, and *"Do not waste time on migrating existing 'anything'. Anybody using Ken after
this upgrade — including you — either is able to use it after the upgrade or just gets off-board and
on-board again."* It extends to `comm.db`, which may start empty. Everything durable survives:
stations keep their ids, notebooks, tasks, lockers, vaults and links, and the knowledge base is
untouched.

Net **−6,377 lines** across 129 files. The operator briefing is
[docs/UPGRADING.md](docs/UPGRADING.md).

### Removed

- **`/comm/mcp` and `/station/mcp`.** `/mcp` carries all **41** tools (39 feature tools plus `ken_version` and `ken_instructions`; counted off `tools/list`, not off the registry that omits the meta pair). The three-way split existed
  because the surfaces took mutually exclusive credentials — a knowledge-base token could not send
  messages, a comm token could not write knowledge — and that stopped being true when one OAuth
  grant began carrying every scope family. What remained was three connectors, three consents and
  three UUID prefixes in every tool list. A credential reaching `/mcp` must now carry **every**
  capability and is refused at the transport otherwise, which turns a legacy connector into a loud
  401 rather than a short tool list nobody can explain.
- **Station keys**, their binding, the binding voucher and `ken station key`. A `kens_` credential
  reached nothing once `/mcp` required a full grant. **`kens_` is a contract prefix**, which is one
  of the reasons this release is a MAJOR rather than a carve-out.
- **`comm_register`, `comm_bind`, `comm_unbind`**, the `endpoint_id` / `endpoint_secret` pair,
  secret rotation, endpoint repointing, the console's reassign-a-mailbox form, and the idle-mailbox
  sweep with `comm_endpoint_idle_sec`.
- **The pairing code**, entirely: `comm_join`, `MintPairingCode`, `JoinChannel`, the console mint
  form, its one-time reveal, the pending-codes table, `comm_pairing_code_ttl_sec`, and the
  `pairing_code` table (comm migration 0020).
- **`station_link_request`** and the console approval, with the denial ladder and
  `station_link_denial`. A link is created by the first message.
- **`X-Ken-Workspace` and `?workspace=`**, with the word *workspace* itself.
- **The stations "switch"** — `stationsEnabled` was a constant `true` behind 19 dead guards, a nav
  gate and a dashboard gate. Vlad: *"IN KEN NOTHING IS OPTIONAL!"* A test asserted the console could
  be absent and passed against a 404 no deployment could produce.
- `docs/RUNBOOK-ENDPOINT-MIGRATION.md`, whose subject no longer exists.

### Changed

- **A mailbox belongs to a STATION, not a session.** Vlad: *"I own a home and I don't have to go to
  the post office to claim a mailbox — the mailbox resides in my home."* A successor session
  inherits its predecessor's unread mail with nothing to re-bind, and there is no credential to
  write to a `0600` file. The separation the endpoint secret provided survives on a better axis:
  two sessions on one machine still cannot read each other's mail, because each resolves to its own
  station.
- **Links are born active, and SUSPEND replaces REVOKE.** Vlad: *"'suspend' button instead of revoke
  button (I want to be able to 'resume' it). 'revoke' concept is out of the table."* The link is
  still recorded — it is the audit trail and it carries the off-switch. Auto-linking will **not**
  resurrect a suspended link, or the off-switch would be undone by the first thing it exists to
  stop. Scoped to links only: channels still have a `revoked` state.
- **The directory shows the whole estate.** `station_directory` and `comm_directory` list every live
  station rather than only the published and already-linked. **This shipped WITH the gate removal
  because it had to** — ken-prod-ops named the consequence of doing one without the other: a session
  goes from "cannot reach anyone because not linked" to "cannot reach anyone because it does not
  know who exists", the same outcome reached differently and harder to diagnose because nothing
  errors. `published` and `linked` are still returned; they stopped being a filter and stayed as
  facts.
- **Per-tool rules live in `ken_instructions{tool:"<name>"}`.** Each tool's list entry is one
  sentence plus a pointer. A tool description is captured when a conversation begins and never
  refreshes; a result is computed per call, so this moves the detail to the side of the freeze that
  stays fresh. One string per tool, not two — the brief is computed from the rules, so a summary
  cannot disagree with its own detail.
- **"Workspace" is retired; the word is "station".** No dual acceptance. Wire fields
  `how_to_keep_this_workspace`, `workspace_just_created` and `workspace_name` become
  `how_to_keep_this_station`, `station_just_created` and `station_label`. The default name a station
  received when nothing better was available was literally `workspace` — the one place the retired
  word would have kept appearing in front of a human, on the newest stations rather than the oldest.
- **One station resolver** (`internal/station.Resolve`), used by both surfaces. The `?station=`
  promotion used to live in the station middleware, and comm handlers saw its effect only because
  `allserver` wired one middleware inside the other — a wiring order nothing asserted.
- The console shows each station's **id**, which is what sessions address and what appears in a
  session's own account of what it did.

### Fixed

- **A test was green because its harness lacked wiring production has.** The HTTP send test asserted
  that a send to an unlinked station was refused; it kept passing after auto-linking shipped because
  the auto-link push went through an **optional** `SyncLinkMirror` hook that was nil in that
  harness. The link landed in `ken.db`, `comm.db` never heard, and the send refused with "no
  approved link joins you" — naming as missing a permission the handler had created microseconds
  earlier. The hook is deleted; the send path pushes the mirror itself.
- **`ken_instructions` returned a third of the answer.** Three packages each registered it, correct
  when there were three servers. `mcp.AddTool` *"adds a Tool to the server, or replaces one with the
  same name"*, so the last registration won and the tool returned one surface's block, correctly
  formatted, with nothing to suggest two thirds were missing. Every test stayed green: the
  per-package tests connect to their own handlers, and the audit test asserted all three packages
  registered it — which they did, and which **was** the defect. A wire test now reads the served
  surface.
- **The console rendered a "Mint key" form posting to a route deleted with the station keys**, four
  lines below a comment reading "THE KEYS TABLE IS GONE". `TestEveryFormActionHasARoute` now checks
  every template's `action` against the registered routes — the build cannot check a string in HTML,
  and no test drives a control nobody knows about.
- **A station's mailbox could be reaped with mail waiting for it.** The idle sweep spared any mailbox
  referenced by a message, an attachment or a channel seat — but a pair message is addressed to the
  post, so its delivery row carries no recipient mailbox by design. `MailboxFor` recreates one on the
  next call, so nothing would have errored while the station came back with a new id, no history, and
  the directory reporting it unstaffed.
- **`OpenLinkedChannel` had the self-peer hole the deleted join path had already closed.** It checked
  rowids and empty stations, never whether the two stations were the same, so a channel could end up
  with `station_a = station_b` and every message that station sent would come back as mail from a
  peer. Unreachable today — a station has one mailbox — and guarded anyway, with a test that builds
  the state by direct INSERT because a guard whose only proof is "nothing can reach it" is one
  nobody has watched refuse.
- **Retiring the station keys would have left `withCaller` accepting nothing, silently.** It
  re-derives the principal per call so a `kb_save` presented with token B on a session opened by
  token A is authored by B, it knew only station keys, and it returns the unmodified context on
  failure. It and the middleware had already drifted to different credential vocabularies; both now
  go through one `principalFromToken`.
- Three refusals were instructing sessions into dead ends: `ErrNotAStation` named `comm_bind` and
  the `X-Ken-Workspace` header, `ErrNotLinked` said to file `station_link_request` and wait for a
  human, and `ErrUnknownStation` claimed a station must appear in an approved link to be known.

### Fixed after the pre-release audit

An adversarial audit of the staged release (`docs/audits/2026-08-28-4.0.0-prerelease.md`) raised 103
findings; 82 survived three refutation lenses. These are the ones that were defects rather than
observations, listed because several were introduced BY this wave.

- **A suspended link told the session to check its id.** comm.db's mirror holds active links only,
  so a suspended peer vanished from it and the send path — which decided the REASON by asking
  whether the target appeared in any mirror row — answered "no station with that id is known here".
  comm_directory returns that exact id, so the session re-checks and retries: the one behaviour the
  SUSPENDED refusal exists to prevent. The reason now comes from ken.db, which is the authority.
  **The shipped test passed only because its fixture gave the target a second link.**
- **Every live setting reached a handler nobody served.** The per-surface handlers stopped being
  mounted when the endpoints collapsed, and `live.OnChange` was still wired to them: a station cap
  lowered to 1 KiB let a 4 KiB write through, and a curation language never reached `kb_save`.
  Both guarding tests were green against unmounted handlers.
- **Two console buttons reported success and did nothing** — Revoke endpoint (stamped a column
  nothing reads; the mailbox is recreated on the next call) and Reassign mailbox, which three
  documents already described as deleted.
- **The rate limit was charged three times per request**, so the shipped default of 120/min burst 60
  was really 40/20. Two extra unthrottled `UPDATE api_token` statements per request went with it.
- **`KEN_DEV_TOKEN` authenticated nothing**, including README's quickstart: the bypass was honoured
  by one middleware of three.
- **A binary older than the database booted normally.** The migration runner had no ceiling check,
  so a rollback to 3.42.0 produced an ordinary startup log and then failed on the first query
  touching a dropped table. It now refuses, names both schema versions and points at the snapshot.
- **`comm_file_offer{to_room|to_station, transfer:"upload"}` could never move a byte** — a nullable
  column scanned into an `int64`, surfacing as a 404 indistinguishable from "no such grant".
- **Auto-linking created an ACTIVE link to an ARCHIVED station**, and its mirror push was one-shot
  with its failure swallowed.
- **Four frozen input-schema strings asserted deleted gates**, plus `station_me`'s
  `comm_endpoint_ids`, which told sessions to check a credentials file that no longer exists.
- **The CLI taught the inverted scope rule** and printed `ken station key` as a next step after
  `ken station add`, a subcommand this release retired.
- **`afterEndpointAuth` was dead and holding up a gate**: it was the only producer of
  `store.ErrStationArchived`, and internal/audit's reachability check was green over
  `store.IsStationArchived` only because this uncalled function called it.

### Fixed after the second audit round

The first round's fixes were then re-verified by EXECUTION, and the areas that round admitted it had
never examined were covered. Several fixes turned out to be incomplete, and one — a migration
written during the fix round — was actively dangerous.

- **comm migration 0021 broke real upgraded databases three ways.** Nine columns reference
  `endpoint(id)` and it re-pointed four, leaving dangling references that failed the boot; a station
  whose endpoints were all revoked aborted it permanently; and rows pointing at a revoked endpoint
  were silently re-attributed. Rewritten to drive everything off an explicit list of the rows about
  to be deleted, and verified against databases built by the real v3.42.0 binary.
- **A failed migration hid itself on the next boot.** Each migration file commits before the
  foreign-key check runs, and a boot with nothing pending returned before reaching the check — so
  DEGRADED became healthy on restart with nothing repaired. The check now runs every boot.
- **The link mirror lost links under concurrency.** A snapshot read then whole-table replace meant
  an older snapshot could land last: 4–10% of concurrent first contacts had their link deleted and
  their sends refused permanently. Serialised. The test for it passed without the fix until it had
  writer contention, which is what stretches the window.
- **The dev token authenticated and then failed on the first call the server mandates**, with a raw
  FOREIGN KEY error, because the principal carried no actor.
- **A lowered station cap did not bind an already-open session.** comm reads its limits per call;
  stations baked them into tool closures at registration. Both read per call now.
- **A file offer could not make first contact** and refused with the SUSPENDED sentence — telling a
  session to stop and escalate over a relationship one comm_send would have created.
- **Auto-naming ran out after 50 stations**, so every new label-less conversation's first mandated
  call failed permanently with an error naming no remedy.
- **Seven console strings and four consent-screen sentences** described mechanisms this deployment
  does not have — including a consent screen offering per-surface choices on a page with no
  checkboxes, which is the narrowing action a cautious operator would reach for.
- Two gates were widened rather than left: the console-prose check read six lines out of two
  thousand, and nothing compared the consent prose against the consent markup.

### Documentation

- `IDENTITY.md` says what runs rather than what was planned, with the three places the build
  diverged from the design marked where they appear. **"Same folder, new conversation, same station"
  is recorded as genuinely unsolved and deliberately deferred** — the console's reassign step covers
  it, and the folder-key design waits until Vlad wants to think about it.
- `COMM.md`, `STATIONS.md` and `MCP-TOOLS.md` carry a dated box saying what changed, and the
  decision records that were superseded say what survived rather than only that something did.
- `IDENTITY-CONTROLS.md` is banner-marked as a **snapshot of the system before the replacement** and
  deliberately not rewritten: it is evidence gathered while the code still ran, and every verdict in
  it was handed to an adversary instructed to refute it. Editing it afterwards would turn evidence
  into a summary that happens to agree with the outcome.
- `UPGRADING.md` states, at the top, that **3.30.0–3.42.0 shipped with no entries** — thirteen
  releases, against a file whose own rule is that a break is recorded in the change that causes it.
- `COMPATIBILITY.md` records that the COMM v2 promotion trigger has **not** fired: the channel is
  still the noun the tool surface is built on, and one open item is enough to keep it from firing.

## [3.42.0] — 2026-08-27

**SCHEMA CHANGE — ships alone under Rule 4 with exactly its own code.** `ken.db` **23 → 24**;
`comm.db` unchanged at 19.

```
station_request.kind  CHECK IN ('station','link')  ->  ('station','link','room')
  one table rebuilt · no column added or removed · every row copied by NAME
  idx_station_request_pending recreated by hand · ON DELETE CASCADE preserved
  measured on a fully-migrated ken.db: 2.53 ms, foreign_key_check clean, integrity ok
```

### Added

- **A session can ask its human for a ROOM.** `station_room_request` on the station surface, and a
  room branch in the console's request queue.

  **This was Vlad's decision on 2026-08-06, and it was quietly not implemented.** His words, from
  station task `t-CtGY9i1q` the same day: *"ROOM CREATION: sessions may REQUEST, human approves —
  NOT the humans-only option I recommended … Same shape as the curation gate, which is the right
  instinct — the agent proposes, the human promotes."* He decided it **overriding the session's own
  recommendation**, and knowing the schema cost — it was in the same sentence. Six days later
  `0017_comm_rooms.sql` declined to build it, citing that cost. There is no record of a reversal.

  **The objection was defensible then and is measurably false now.** When 0017 was written, ken.db's
  migration runner had no foreign-key handling, which made any table rebuild genuinely dangerous;
  that precondition was removed 2026-08-20. Then 3.33.0's migration 0022 performed exactly this
  operation on a station table, in ~19 lines, as routine.

  **THE REQUEST NAMES NO STATION, and that is the design.** 0017's *second* reason is a principle
  and it survives untouched: *"a room is a set of stations a human decided should talk to each
  other. There is no version of that decision an agent should be making for itself."* That is an
  argument against an agent **creating** a room, not against one **asking** — and Ken already ships
  two request tools that coexist with human-only creation. A request carrying no members leaves
  membership wholly with the human, and leaves no station name to resolve, so the enumeration
  oracle `station_link_request` needed `StationByNameVisibleTo` to close cannot arise here.

  - `name_hint` is prefilled in the console and **never applied automatically**. An approval that
    used it would let a session choose what its human sees in the room list.
  - Approving creates the room **EMPTY**. The human adds members exactly as before.
  - **One pending ask per station**, and a denied station is **silently muted** on the link ladder
    (1h → 6h → 24h → 7d), computed from its own denied rows — `station_link_denial` is keyed on a
    pair a room request does not have, and storing `(x,x)` there would be a lie in the schema.
    Silent, because a caller that could tell a dropped ask from a filed one could probe its human's
    past refusals one request at a time. Asserted at the TOOL, not just the store: the byte-for-byte
    identical answer is what the session actually reads.
  - Hearsay is recorded, because a peer can talk a station into asking and the request then reaches
    the human looking like its own idea.

### Fixed

- **The console's approve handler had a latent fallthrough, found by the analysis that designed this
  feature rather than by an operator hitting it.** It dispatches on a `kind` form field, and the
  template's station branch emitted **none** — so a request of any kind the template did not
  recognise landed in the station branch and was refused by `ApproveStationRequest` with a message
  naming a *different* function. Latent while only two kinds existed; `room` is the third. Every
  branch now declares its kind, and an unrecognised one says so instead of being absorbed.

- **`ApproveStationRequest`'s refusal named one sibling when there are now two.** It said *"approve
  it with ApproveLinkRequest"* for every non-station kind, so a room request reaching it would have
  sent the operator to a function that also refuses. A refusal that misdirects is worse than a bare
  one.

- **The denial-policy comment described a two-case world.** It would have read as complete while
  silently omitting the room case.

### Notes

- **Sessions will not see the new tool until they reconnect.** MCP tool lists freeze at conversation
  start; nothing can make a running session discover it.
- **Verified by mutation, with compiling mutants only.** Ten killed, including: the kind guard
  removed, the mute never firing, the mute *erroring* instead of dropping silently, dedup removed,
  reason not required, the approval adding a member, the template dropping `kind=room`, the console
  branch removed, and — the one worth naming — **the agent's `name_hint` silently overriding the
  human's typed name.** Three earlier attempts were invalid and redone: two did not compile, and
  one added a member with a station id no station has, so the foreign key rejected it and the mutant
  was a silent no-op reading as SURVIVED.

## [3.41.0] — 2026-08-27

### Fixed

- **THE DEAD-SEAT GATE HAD A SECOND DOOR, AND IT WAS STANDING OPEN ON PRODUCTION.** Revoke an
  endpoint's **owner token** instead of the endpoint, and you get the identical black hole with
  `endpoint.revoked_at` still `NULL`, so 3.40.0's gate never fires.

  **Found by ken-prod-ops through an entirely ordinary operator action**, not an exotic one: Vlad
  revoked a machine's three per-machine API tokens during routine credential cleanup, and one of
  them owned a comm endpoint. `store.RevokeToken` writes `api_token` and **nothing else** — it never
  opens comm.db — while the endpoint becomes exactly as dead as a revoked one, because nobody can
  present the revoked token and any other token fails `auth`'s `ep.Owner.TokenID` comparison.

  ```
  revoke a STATION KEY  -> SeverEndpointsBoundBy sets revoked_at -> gate FIRES    correct
  revoke a COMM TOKEN   -> nothing sets revoked_at               -> gate BLIND    the hole
  revoke the ENDPOINT   -> revoked_at set                        -> gate FIRES    correct
  ```

  Reproduced in-process before anything was written: `ChannelFor -> nil`, **send accepted,
  `recipients: 1`**, filed under `e:<rowid>`, unretrievable forever.

  **Ken's own code had already argued this would happen.** `auth`'s station-key check carries the
  reason: *"the revoking end cannot be relied upon … failing closed at use covers every revocation
  path, including ones added later that forget stations exist."* A token revocation is precisely
  such a path, and `ChannelFor` inherited the assumption that comment warns against — that
  `revoked_at` is a trustworthy record of deadness.

  **The fix is at USE, and one layer up, because it cannot be anywhere else.** `api_token` lives in
  ken.db and comm.db has no handle on it; the comm package imports nothing from the store,
  deliberately. So `comm.PeerSeatOwner` reports the peer seat's owner token and binding, and the
  tool handler — which holds both databases — judges it. Prod's suggested one-column join was not
  available for exactly this reason, which is worth recording since it is the natural first idea.

  **Scoped to UNBOUND, like the first door.** A bound seat's mail files under `s:<station>` and a
  successor collects it, so a bound peer whose owner token was revoked must still accept mail —
  otherwise routine credential cleanup would silently destroy every station-bound channel a machine
  ever opened. A lookup failure does not refuse either: this is a deliverability warning, and
  turning a database hiccup into a refused send trades a rare silent loss for a common loud one.

### Added

- **The dead-seat refusal is now asserted at the layer a session actually reads it** — a real MCP
  client, a real HTTP transport, a real `comm_send` tools/call, checking the text that comes back.

  **ken-prod-ops named this gap after taking 3.40.0, and was right to.** It satisfied the migration
  declaration 15 of 15 and passed the idle-peer check with a delivery row — then reported plainly
  that the check I most wanted could not be run there: on that deployment zero revoked endpoints sit
  on a channel, and manufacturing one means revoking an endpoint on a LIVE channel to test a gate.
  It declined, correctly, and said so rather than reporting three checks as passed when one was.

  Three layers carry that refusal, and each has already hidden a defect of exactly this kind:

  ```
  comm.ChannelFor   raises it       — a store test here SURVIVED a CallerSafe-less mutant
  commError         maps it         — 3.3.0 shipped a correct string this layer discarded
  the MCP handler   serializes it   — UNTESTED, and the only layer the caller reads
  ```

  Demonstrated with two mutants rather than argued:

  ```
                            CallerSafe marker removed   commError ignores CallerSafe
    store test                    SURVIVED                      SURVIVED
    mapper test                   killed                        (n/a — is the mutant)
    MCP wire test                 killed                        killed
  ```

  The second mutant is the point: it lives *above* every store assertion, so nothing in
  `internal/comm` can see it, and it would have shipped the 3.3.0 defect again — a correct refusal
  in the binary that no session can read. Production could not have caught it either, for the reason
  prod gave. It is catchable here, in-process, with disposable endpoints and nothing at risk.

_Everything below this section is tagged, built and published._

<!-- KEEP THIS PLACEHOLDER LINE. An EMPTY "Unreleased" heading sitting directly above the newest
     release reads as though that release is unreleased — Vlad read it exactly that way on
     2026-08-27 and asked whether 3.40.0 still needed cutting when it had already shipped. The
     heading is a Keep a Changelog convention and stays; the line under it is what stops it
     being ambiguous. Replace this line with real entries when work is pending, and restore it
     when the next release is cut. -->

### Notes

- **The verification procedure for the FIRST door was itself a silent instrument, and prod caught
  it before anyone ran the check.** Both strings contain the word `revoked`:

  ```
  PASS (gate fired)      "…was revoked and was never bound to a station…"
  FAIL (gate flattened)  "channel is not open — … and it must not be revoked"
  ```

  So `grep revoked` — the obvious check, and the one this project's own hand-off text invited —
  **cannot distinguish the fix from the mutant that survived the store-level suite.** The
  discriminators are `was never bound to a station` and `to_station`, present only in the authored
  refusal; the reliable assertion is that the GENERIC string is ABSENT. Two further false passes on
  the same check: a channel not in `state='open'` returns before the gate is reached, and a revoked
  **bound** peer refusing is the over-scoping regression, not a pass. Both new tests here match on
  the discriminator and never on the shared word.

- **A store-level probe for this fix reported SEND ACCEPTED against the fixed build**, because the
  gate lives in the tool handler where both databases are in hand. It was written first, and it was
  structurally blind to its own subject — the same trap, one layer over. The shipped tests drive a
  real MCP client over a real HTTP transport.

- **Mutation, with compiling mutants only.** Four killed: the gate never firing, the gate ignoring
  the binding (caught by the bound-seat test, which is why that test exists), a revoked token
  reading as live, and an absent token reading as live. That last one **survived the whole suite**
  until a test was written for it — `TokenIsRevoked` treats an absent row as revoked, deliberately,
  and nothing asserted it. Two earlier attempts did not compile and were re-done: a mutant that
  fails to build reads as a passing check.

## [3.40.0] — 2026-08-27

### Fixed

- **A channel send to a peer that can NEVER read it succeeded, with `recipients: 1`.** Revoke a
  peer's endpoint and the send went through: `recipients: 1`, `comm_channels` still reading
  `state="open"`, and the delivery filed under `e:<rowid>` for a rowid that can never authenticate
  again — **nothing anywhere in the tree clears `revoked_at`.** A permanently undeliverable send
  rendered identically to a healthy one.

  **The silent instrument was `ChannelFor`, not the counter**, and that distinction shaped the fix.
  `recipients` is specified honestly as the number of addressed **parties** and never claimed to be
  a delivery check. `ChannelFor` *is* the check — deciding whether a channel can carry a message is
  its whole job, it already tested `Open()` and `peer != 0`, and it read `station_id` off both
  endpoint rows on every call while ignoring the adjacent column saying that seat was dead forever.
  It now reads two more columns from rows it was already joining and refuses, `CallerSafe`, naming
  what happened and what to do instead.

  **SCOPED TO UNBOUND, which is the load-bearing half.** A revoked **bound** peer keeps working: its
  mail is filed under `s:<station>` and a successor endpoint on that station collects it. Verified
  rather than assumed — the test polls as a successor and reads the message. Gating on revocation
  alone would have destroyed exactly the successor inheritance the station model exists for.

  **What it deliberately does not do is refuse because nobody is reading.** An unstaffed post is the
  designed normal state of this transport (median 11 min, p90 144 min); a check that fires on the
  common case trains senders to ignore it and puts the real case back in the dark. A separate test
  sends to an idle, never-polled peer and requires it to succeed.

  Why a refusal rather than a new result field: **MCP tool schemas freeze at conversation start**, so
  a new output field never reaches the sessions that already have the problem.

  *Provenance, because the shape of it matters.* ken-prod-ops reported this class and then retracted
  it — the incident it inferred from was ordinary poll latency, and its `reachable_via` evidence
  described station-addressed reachability rather than channel delivery. **It retracted the mechanism
  along with the incident, and only the incident was wrong.** Measured directly afterwards.

- **An idempotent channel replay reported `recipients: 0`** for a message that has a delivery row.
  The count is assigned after the insert; the replay branch returns before reaching it. A sender
  reading `0` as "it reached nobody" resends under a new key — **defeating the exact feature the key
  exists to provide**, so the bug attacked the mechanism it lived inside.

- **`comm_send`'s description overstated `recipients`** as "how many endpoints it actually went to".
  The pair path contradicts that by design: `to_station` mail to a station with nobody staffing it
  is filed with no endpoint attached and still counts 1, waiting for whoever arrives. It now says
  parties, and says what a party is. Reaches new conversations only — descriptions freeze at connect.

### Notes

- **Verified by mutation, and one mutant exposed a test that could not see it.** Five breakages of
  the gate (removed, ignoring the binding, firing on any peer, checking the wrong seat, the replay
  fix reverted) were killed by store-level tests. Dropping the `CallerSafe` marker **survived** —
  at the store layer `err.Error()` returns the full text either way, while a caller would have read
  the generic "both sessions must join the pairing code", which is advice to re-join a channel that
  is open. That is the 3.3.0 defect verbatim: a correct string in the binary, unreachable from every
  caller, with a test one layer below it passing. There is now an assertion at the `commError`
  boundary, and it kills that mutant while the store-level one still cannot.

## [3.39.0] — 2026-08-27

### Upgrading from 3.36.0 or earlier — ONE comm migration is applied, declared here

An operator coming from **3.36.0** crosses **comm migration 0019**, which shipped in 3.37.0. It is
additive and touches one table:

```
comm.db   18 -> 19      ken.db unchanged (23)
  ALTER TABLE endpoint ADD COLUMN session_key TEXT
  CREATE UNIQUE INDEX idx_endpoint_session_key ON endpoint(session_key) WHERE session_key IS NOT NULL
  INSERT INTO schema_migration(version) VALUES (19)
```

Expect exactly: the `endpoint` table redefined in `sqlite_master`, one added index, one new
`schema_migration` row, and **every other table byte-identical**. No row is rewritten and no
constraint tightens. Coming from 3.37.0 or later, **no migration runs at all** — 3.38.0 and 3.39.0
carry none in either database.

*Stated because it was got wrong once.* The 3.38.0 entry originally attributed comm 0019 to 3.36.0;
ken-prod-ops caught it before upgrading, since an undeclared migration is exactly what their band
check flags — and a wrong version here produces a false violation reported against a correct
release.

### Added

- **A connector row now names the cause, not just the symptom: LEGACY GRANT, and the registered
  redirect host.** Both reported by ken-prod-ops from the live estate.

  **The failure this fixes cost a whole debugging detour.** Vlad removed three connectors, re-added
  one, and saw only `kb_*` tools. Two causes stacked: he used `/mcp` instead of `/all/mcp`, and —
  the one that would have survived fixing the URL — **his reconnect silently reused a grant from
  2026-08-11** whose scope predates `ken:kb ken:comm ken:station`. He was KB-only **by grant**, not
  by URL, and pointing the same connector at `/all/mcp` would have returned a bare 401 and taught
  him nothing.

  The human's model is "I deleted the connector and made a new one." The system's model is "same
  grant, unchanged." **Deleting a connector revokes nothing**, and until now nothing anywhere said
  so. The tool list was the symptom, the grant was the cause, and `/tokens` is where the two now
  meet. `docs/OPERATION.md` gains the revocation section prod wrote to go with it.

  The legacy predicate is `store.IsLegacyGrant`, **one definition read by both the authenticator
  and the console**, because a badge that disagrees with what the server grants leaves an operator
  worse off than no badge. It is *no `ken:` scope recorded* — not *grants exactly the knowledge
  base*, which would also flag a grant a human deliberately narrowed to `ken:kb` and advise them to
  undo their own decision.

- **The registered redirect host on each connector row — the only field there that carries trust.**
  The application name is self-reported, and the consent screen already says so; anyone reachable
  can register a client under a reassuring one. ken-prod-ops found `ken-identity-verification`
  holding all three capabilities on the live estate: registered through open dynamic client
  registration, approved by a human, and shipped by nothing in Ken. Not a vulnerability —
  registration grants nothing and a human approved it — but it was **plausible only because its
  redirect was loopback**, and that field was visible at consent time and nowhere else: the one
  moment a human is least equipped to weigh it. An unparseable redirect renders as a dash rather
  than raw, since printing a non-host in the trust column invites reading it as one.

### Notes

- **The duplicated-scope defect prod reported on grant 9 is already fixed** — `oauth_web.go` dedupes
  request and default scopes, shipped in 3.31.0 with that exact symptom named in its comment
  (`"... ken:kb ken:comm ken:station ken:kb ken:comm ken:station"` on the first real consent ever
  performed). Grants 10–12 are clean, which is consistent with grant 9 predating the deployed fix.
- **`resource` is NULL on the `/all/mcp` grant because the client did not send one** — it is an
  RFC 8707 request parameter Ken records verbatim, not a value Ken derives, and the consent screen
  never used it (it shows the redirect host). Flagged by prod as an audit field that went empty;
  recorded here as *client behaviour*, not a regression, and not something the server can fill in
  honestly at consent time.

## [3.38.0] — 2026-08-26

### Fixed

- **Transferring a station's assets left its VAULT behind, and nothing said so.**
  `TransferStationAssets` is the console answer to "a session is gone and its work should not be".
  It moved notes, note revisions, tasks and locker files — and silently left the secrets. Its own
  doc comment explained at length why the message queue stays put (expendable, S7) and said nothing
  about credentials, so an operator reading it would have concluded the transfer was complete.

  Found while designing workspace takeover, which is exactly the path that would have hit it. Vlad's
  ruling and his reason: **the whole point is that work survives a session, and secrets are the part
  hardest to recreate** — an API key nobody has a second copy of is worse to lose than a note.

  - **Vault names are collision-checked** like page and locker names, and a clash refuses the whole
    transfer. A silent merge would overwrite one credential with another and the loser would be
    unrecoverable.
  - **Revision history follows its secret; the read trail does not.** Those rows record reads that
    happened at the SOURCE — relocating them would make the destination's log assert reads from
    before it held the value, and erase the source's record of ever holding it.
  - **The departure is audited**: one `station_vault_read` row per secret, `via='transfer'`, against
    the source — the same meaning that value already carries for `station_vault_send`. No value is
    decrypted to do it; the ciphertext is re-pointed and ownership changing is the event.
  - The console box is **ticked by default** and the receipt now **counts secrets**, because this is
    the action that used to leave them behind in silence.

  Verified by mutation: five separate breakages of the move, the collision check, the audit row and
  the trail rule are each caught by a named test. The history mutant **survived the first run** —
  nothing asserted that previous values travel — which is why there is now a test that says so.

### Added

- **Console reassignment: an abandoned workspace can be taken over, and so can its mailbox.**

  The sealed-workspace problem, which nobody had answered: a session may adopt a station only while
  its `session_key` is NULL — it must, because a key that could take a *claimed* workspace would be
  authorising something, and the key is documented as selecting and never authorising. So when a
  conversation dies, the workspace it claimed is sealed with its notes, tasks, locker and vault
  inside it, and **nothing can reach them**. Every abandoned conversation was a one-way door.

  Vlad: *"I think we can use the fact that a workspace can be re-assigned to tell a chat session to
  recover (take over) an (abandoned) workspace and it might even be used to re-establish comm
  channels."*

  - `POST /stations/{id}/reassign` points a workspace at a conversation key. **Console-only**, because
    reassignment is exactly the act the claim path refuses to infer from a session's say-so — behind
    an authenticated form the rule survives intact: the key still authorises nothing, a *person*
    decides who takes over a post.
  - `POST /comm/endpoints/{id}/reassign` does the same for a mailbox, because a workspace recovered
    without its mail is half a recovery. **Rotate was the only way in and it does not work for the
    sessions that need it**: it mints a secret for the human to relay and the session to write to
    disk, and a claude.ai chat has no disk — the exact ceremony 3.36.0 removed from `comm_register`.
    The endpoint's id, channels, station binding and **queued messages** are untouched; a
    reassignment that dropped the inbox would be a rotation with extra steps.
  - **It costs the human no credential work.** The session states a conversation key in its reply — a
    Claude Code session has a UUID, a chat session invents one — the human pastes that string into
    both forms, and the session's next `station_me` and `comm_poll` land in the recovered workspace
    and read the mail waiting in it. Nothing secret is displayed, typed or transported.
  - **The key is taken from whatever holds it, and the displacement is reported.** The first cut
    refused a key in use; the test written for the recovery flow hit it on the first run, because
    that is the MAIN path — a session asked for its key has usually already claimed a fresh empty
    workspace under it. Nothing is destroyed by taking it, so the safety is disclosure rather than
    refusal: the receipt names what was displaced and one click puts it back.
  - **An empty key releases**, which is the undo; **archived stations and revoked endpoints are
    refused**, or archive and revocation would be advisory.
  - The stations page marks each workspace **held** or **unclaimed**, and the comm page shows which
    conversation drives each mailbox — the operator's first question is which posts are abandoned.
  - The endpoint's **owner token is not touched**, and that is load-bearing: `auth` re-checks the
    bearer's token against the endpoint's owner on every call, so a conversation key alone can never
    drive a mailbox from another account. A taking-over session on a different Ken token also needs
    a **repoint**, which is the control next to it. Repointing silently here would move an estate
    boundary as a side effect of a convenience.
  - Every reassignment and release is logged with the operator's name, like a rotation: `comm.db` is
    expendable and not backed up, so the server log is the record that survives.

  No schema change in this release. The two columns it uses shipped earlier: `station.session_key`
  in **3.35.0** (migration 0023) and `endpoint.session_key` in **3.37.0** (comm migration 0019).

  *Corrected 2026-08-27:* this entry first attributed comm 0019 to **3.36.0**, which is wrong —
  3.36.0 (`/all/mcp`) carried no migration in either database. ken-prod-ops caught it from the
  live estate before running the upgrade, holding comm 18 after 3.36.0 and reasonably asking
  which of the two possible explanations applied. **An operator upgrading from 3.36.0 crosses
  3.37.0's comm migration**, and an undeclared migration is precisely what their band check
  exists to catch — so the wrong version here would have produced a false violation reported
  against a correct release. Verified against the tags, not from memory:
  `git cat-file -e v3.36.0:internal/comm/migrations/0019_endpoint_session_key.sql` fails and
  `v3.37.0` succeeds.

## [3.37.1] — 2026-08-26

### Fixed

- **The `session_key` guidance was Claude-Code-specific and gave a chat session nothing.** It said
  *"in Claude Code, the UUID in your transcript or scratchpad path"* — useless to the population
  the whole mechanism was extended for.

  **Measured by asking a claude.ai chat session directly**, which is the experiment that should
  have been run first: *"I have no access to a conversation id. Nothing in my context — system
  prompt, metadata, or any tool result — contains an identifier for this conversation."* It also
  found the asymmetry: `recent_chats` returns `https://claude.ai/chat/<uuid>` for **past**
  conversations, so ids exist — but **not for the one it is in.** Whatever assigns that id happens
  outside what the session can see, so no transport change could have fixed it either.

  And it identified the one thing that does persist: *"Only the transcript itself… conversation-scoped
  and persistent."*

  So the guidance now tells a session with no natural id to **invent one and state it in its reply**
  — because the transcript is what survives a reload, and a key that lives only in a tool call is a
  key that is lost. No client support needed, and nothing else changed: `session_key` already
  accepted any stable string.

  `TestTheMergedInstructionsKeepWhatTheyExistToSay` now fails if either half is removed — inventing
  a key is useless without being told where to put it.

## [3.37.0] — 2026-08-26

### Added

- **A conversation can CLAIM a comm endpoint and drive it with no secret at all.**
  `comm_register{session_key}` ships with migration `0019` under Rule 4.

  **This is what locked chat sessions out of comm.** `comm_register` returned a one-time secret
  and told the session, in capitals, *"WRITE THEM TO A FILE ON DISK NOW."* **A claude.ai chat
  session has no disk** — it could register once and then lose the ability to poll forever,
  because the secret is shown once and nothing it controls survives a compaction. The same
  instruction is the per-machine credential tax `IDENTITY.md` §4b exists to remove.

  **And the secret only ever existed because Ken could not tell two sessions apart.**
  `0001_init.sql:38`, unchanged since the first release: *"the operating convention is one Ken
  token per MACHINE, so every session on a box shares a token. Without a per-endpoint secret, two
  sessions could poll and ack each other's messages."* That is a disambiguation problem, and a
  conversation key solves it — better, because it survives a client restart while a secret
  survives only a file.

  Claiming is **idempotent per conversation**: calling again after a restart returns the same
  endpoint with its channels and queued mail intact.

### Changed

- **`comm_register`'s description now leads with the secret-free path**, and the write-to-a-file
  instruction is the **fallback** for callers that send no key rather than the headline. It also
  says to send `session_key` **even if the tool schema does not list it**, for the same reason
  `station_me` does — a session's schema pins at connect time and cannot announce a parameter
  added later.

- **`auth` gained a secret-free path at the single chokepoint every comm tool passes through**, and
  the post-authentication checks were factored into `afterEndpointAuth` so the two paths **cannot
  diverge**. A second copy would be one edit away from a claimed endpoint outliving a station-key
  revocation that a secret-driven one respects.

### Security

- **The secret is NOT deleted, and that restraint is measured.** `station_me` returns
  `comm_endpoint_ids`, so endpoint ids are handed out rather than private. Removing the secret with
  nothing in its place would let any session that has seen an id poll and ack that endpoint's mail
  — exactly and only what the secret prevents. Production holds eight bound endpoints. So claimed
  endpoints need no secret, unclaimed ones keep theirs, and the secret can be retired for real once
  measurement shows nothing depends on it.

- **The conversation key AUTHORISES here, unlike the station key of the same name.** The station
  `session_key` selects a workspace and grants nothing (§4, §9.2). This one drives a mailbox: it is
  a credential, as sensitive as the secret it replaces, and must never be logged, put in a URL, or
  written to a notebook. **Ownership is re-checked on every use** — a key presented under a
  different bearer token is refused, so a leaked key cannot be replayed from another account.

## [3.36.0] — 2026-08-26

### Added

- **`/all/mcp` — all three surfaces from ONE connector.** Vlad, on the acceptance run: *"Under
  the, also absurd, current model, 3 connectors, one for each surface, while we could have only
  one for the 3 surfaces."*

  **The reason for the split had already been deleted.** The three endpoints existed because they
  took mutually exclusive credential families, enforced by `store.CheckScopeMix` — removed in
  3.31.0 — and a single OAuth grant has carried `ken:kb ken:comm ken:station` together since
  3.25.0. The three-way URL split was vestigial, and its whole remaining cost landed on the user
  as three connectors, three consents and three UUID prefixes in every tool list.

  Verified end to end: **45 tools on one endpoint** — 8 `kb_`, 12 `comm_`, 23 `station_`, plus
  `ken_version` and `ken_instructions` — matching the source count exactly.

  **The three existing endpoints are unchanged and keep working.** This is additive on purpose:
  production holds eight bound comm endpoints and other machines are configured against the
  specific URLs, so nobody migrates on Ken's schedule.

  **A credential reaching `/all/mcp` must carry every capability**, and is refused at the
  transport otherwise. That falls out of the mechanism — each surface's middleware runs and each
  fails closed — but it is also the right rule: this endpoint offers everything, and a narrower
  credential should use the endpoint it was minted for, where the refusal is precise. Measured:
  a comm-only token gets `401` here and `200` on `/comm/mcp`.

  It also preserves the property `IDENTITY-CONTROLS.md` records as surviving — transport-level
  scope enforcement fails closed, so an unusable surface is *unreachable* rather than
  *reachable-and-erroring*. A collapse that admitted partial credentials and refused per tool
  would have turned the tool list into a reconnaissance surface.

  **The instruction budget is why the merged block is short, and it was measured rather than
  assumed:** 2045 characters delivered on `/mcp` and 2046 on `/comm/mcp`, against a 2048 budget.
  Concatenating three blocks would have sent ~6100 characters into a 2048 window and silently
  destroyed two thirds of the guidance on every surface at once. The merged block is **1305
  characters** and orients rather than instructs — it can, because per-tool rules already live in
  tool descriptions (the 3.26.0 refit) and `ken_instructions` serves the full untruncated text of
  any surface on demand. **The collapse is only affordable because that tool shipped first.**

### Removed

- **`ken token add` no longer refuses station scopes**, and its reason had been false since
  3.28.0. It said *"/station/mcp requires a kens_ key BOUND to a station, and this command issues
  an unbound ken_ token"* — true when written, and untrue from the moment 3.27.0 taught
  `/station/mcp` to accept a plain `ken_` token carrying the station scope.

  **The console's identical refusal was fixed in 3.28.0 and this one was not** — the same half-fix
  the console had suffered, where `consoleCommScopes` excluded station scopes citing *this
  command* as its authority. The justification and the code parted company in two places and only
  one was repaired.

  Found by the unified endpoint's own first test, which could not mint a credential carrying all
  three families — the exact credential `/all/mcp` exists to serve.

### Changed

- **Tool registration is separable from server construction** on all three surfaces
  (`RegisterTools`). The unified endpoint registers the *same* tool definitions rather than
  copies, so a tool cannot say different things depending on which URL reached it.

## [3.35.1] — 2026-08-26

### Fixed

- **`station_me` now tells every session how to keep its workspace, in the RESULT.** 3.35.0's
  `session_key` was unreachable by any session reasoning carefully about its own tool schema.
  ken-prod-ops watched one conclude, correctly from what it could see: *"There is no `session_key`
  parameter… I called it with no arguments rather than passing an unsupported field, which would
  have failed validation."* Its schema was 3.33.0; the server was 3.35.0.

  **A tool description cannot fix that** — descriptions pin at connect time, so text saying "send
  `session_key`" is invisible to precisely the sessions that need telling. Only results cross the
  freeze. Every `station_me` result now carries `how_to_keep_this_workspace`, which says to send
  the parameter **even when the schema does not list it**, and `session_key_received` echoes what
  actually arrived so a session can confirm it landed.

- **A keyed call now ADOPTS the workspace the same connection just minted, instead of stranding
  it.** The pre-3.35.0 tool text told sessions to call `station_me` with no arguments, so the real
  sequence is no-arg then keyed — and on the clean VM that left an orphan station behind, because
  Ken could not tell the two calls were one conversation. The connection binding does know, and
  the `session_key IS NULL` guard makes adopting another conversation's workspace impossible
  rather than unlikely.

  The binding is now written at the tool's single exit, so **every** path leaves the connection
  knowing what it resolved. Doing it only inside the keyed branch is what made the first version
  of the adoption miss.

- **`workspace_just_created` is always present, never omitted when false.** A reader could not
  tell *"this workspace already existed"* from *"the server did not say"* — the third instance of
  that `omitempty` shape found in one day, after `comm_endpoint_ids` and the station briefing. A
  boolean that disappears when false answers a different question than the one it is named for.

## [3.35.0] — 2026-08-26

### Added

- **A conversation can tell Ken which workspace is its own — `station_me{session_key}`.** Ships
  with migration `0023` under Rule 4.

  **Identity was in the wrong layer.** It arrived from the connection — a header, briefly a URL —
  and a claude.ai connector is added **once per account**, so any value carried there had exactly
  one value for every machine and every session. The header could not be set at all (the client
  refuses custom header names); the URL could be set and identified nothing.

  Vlad: *"the communication between the Claude instances and the Ken instance is direct… so why
  each session cannot tell it's Ken instance 'I'm XXXXX'?"* There was no reason.

  A session now declares a stable id for **its own conversation** — in Claude Code, the UUID in
  its transcript and scratchpad paths. Ken returns that conversation's workspace if it has one, or
  mints and records it. The result binds to the MCP connection, so every other station tool works
  with no argument and no header. **Onboarding a machine now costs the consent and nothing else,
  ever.**

  **It survives a client restart**, which is the case that defines it: the key is durable and the
  connection binding is only a cache, so a restarted conversation comes back to its own workspace
  rather than minting an orphan. It cannot be keyed on the MCP session id, which is reborn on
  every reconnect.

  The **label** is auto-generated so a human can recognise it and stays renameable; the **key** is
  the identity, and renaming invalidates nothing. The key **selects and never authorises**, under
  the same §9.2 condition as the workspace id.

## [3.34.0] — 2026-08-26

### Fixed

- **The station surface was unreachable through claude.ai connectors — the onboarding path Ken
  recommends.** Found by the acceptance run on a clean Windows 11 VM.

  A session could call `station_me`, receive a workspace with zero approvals exactly as
  `IDENTITY.md` §6 promises, and then have **every other station tool refuse it one call later**.
  They all pass through `requireStation`, which needs the connection to declare its workspace, and
  `station_me` is the only tool that does not.

  The declaration was **header-only**, and claude.ai connectors enforce an allowlist of header
  names: *"Only approved header names are accepted."* An already-created connector has no headers
  field at all. So workspaces could be minted indefinitely and used never.

  **`/station/mcp?workspace=<id>` is now accepted**, normalised into the same header everything
  downstream already reads. A URL is the one thing a connector lets a user set freely, and
  connectors are unique per URL — so `?workspace=A` and `?workspace=B` are two connectors, giving
  per-workspace identity **and** account-level propagation, neither of which was possible before.
  An explicit header still wins when both are present.

  **Safe only because the workspace id authorises nothing** (§4). A URL is a worse place for a
  secret than a header, so §9.2's condition governs unchanged: if that id ever gains authority,
  this form goes with it.

- **The no-workspace refusal advised a loop.** It said *"call station_me"* — which mints a
  **second** workspace and returns the same refusal, leaving an orphan station behind each time.
  It now says the connection has not declared a workspace, names both remedies, and warns
  explicitly that re-calling `station_me` is not one of them.

  `TestASessionWithNoWorkspaceIsToldWhatItCanDoAlone` was rewritten rather than edited: it used to
  forbid the refusal from mentioning a human at all, which after this finding would only have
  stopped the message from disclosing a dependency that genuinely exists. It now forbids the
  **deleted request-and-wait path** and the **loop**, which is what actually protects a session.

## [3.33.1] — 2026-08-26

### Fixed

- **`station_vault_send` no longer increments the sender's `read_count`.** The console renders
  that number as *how often this credential was retrieved*, and a transfer is a different event —
  which is exactly why it has its own `via` and its own migration. Counting it in both places
  stated one act twice, once under a label meaning something else, so an operator auditing *"this
  key was read 4 times"* would have been counting sends.

  **The transfer is still fully audited**: the `station_vault_read` row is written for every
  provenance including `transfer`. Only the counter — the one number carrying a specific English
  meaning into the console — stays about retrievals. The two now answer different questions on
  purpose.

  Found by ken-prod-ops in the **first live transfer ever performed**: the sending session had
  never called `station_vault_get`, and its own copy still showed `read_count` 1.

### Changed

- **`docs/STATIONS.md` S13 no longer says the vault "does not pretend to be encrypted".** That
  heading, and its *(chosen: plaintext, audited, reversible)* subtitle, were correct until 3.32.0
  and false after it. The decision that changed is not the argument — a key beside its ciphertext
  is still theatre — but the key's **location**. The section now states what is protected (copies
  that leave the host), what is not (root on the host), and points at the restore failure mode.
  `station_vault_send` is added to the tool table, and the read-count row carries its exception.

## [3.33.0] — 2026-08-26

### Added

- **`station_vault_send` — hand a credential to a session on another machine without the value
  ever leaving the server.** Ships with migration `0022` under Rule 4; the code writes a `via`
  value the old CHECK constraint refuses, so the two cannot be separated.

  **Why it exists: every other route was wrong in a different way.** Pasting a secret into a COMM
  message body puts it in the message store under retention *and* in both sessions' transcripts.
  Relaying it as a file writes plaintext bytes to the server's disk until the sweeper runs. Asking
  the human to copy it by hand is exactly the credential tax the standing requirement exists to
  remove — *"it should not require numerous keys, tokens, vouchers, approvals, etc."*

  The value is decrypted from the sender's row and re-encrypted into the recipient's **inside the
  process**. It never enters a message, a file, `data/comm/`, or either transcript. The sender
  gets a receipt carrying the plaintext `sha256`, so both sides can confirm they hold the same
  secret without either repeating it.

  **Authorised by the existing station link** — the same approval that lets the two stations
  message each other, so there is no second ceremony. Unlinked stations are refused; so is a
  transfer to your own vault.

  **It is a COPY, not a move.** The sender keeps theirs, because a transfer that emptied the
  source would make a mistyped station id destructive, and every vault write is reversible.

  **The recipient is not notified** — the tool says so and tells the sender to announce it by
  name. Automatic notification would need a COMM handle the station surface deliberately does
  not have.

### Changed

- **`station_vault_read.via` accepts `'transfer'`** (migration `0022`, a table rebuild since
  SQLite cannot alter a CHECK in place). A session reading its own credential and a session
  handing that credential to another station are materially different events; filing the second
  as the first would make the log say a secret was *read* when it was *copied somewhere else*,
  and the read log exists to answer exactly one question — who saw this secret.

### Fixed

- **`station_vault_put`'s tool description said the value is "stored unencrypted" and "travels in
  EVERY backup".** Both became false in 3.32.0 and the description was not updated with them —
  a tool description lying about a security property, in the one place a session reads to decide
  whether a credential is safe to store.

## [3.32.0] — 2026-08-26

### Added

- **Station-vault secrets are encrypted at rest.** AES-256-GCM, a fresh nonce per write, under a
  32-byte key at `data/vault.key` (`0600`) created by `Open` — beside the database, next to the
  existing `dedup.key`. IDENTITY.md §11 option A, decided by Vlad and built the same day, while
  the vault still held zero rows on production so there was nothing to migrate or re-encrypt.

  **What it protects, exactly:** copies of `ken.db` that leave the host. The nightly snapshot
  copies only `ken.db`, so the key is outside every backup **by construction** rather than by an
  exclusion rule someone has to remember. Before this, every vault secret travelled off-box in
  plaintext.

  **What it does not protect against:** root on this host, who can read the key beside the
  database and the running process's memory regardless. Saying so is a condition of shipping it —
  migration 0016 refused encryption on the grounds that a key travelling with its ciphertext is
  theatre, and *"theatre in a security store is worse than an honest absence."* The only thing
  separating this from that is the key's location.

  **Residual weakness, stated because it is not obvious:** `sha256` and `size_bytes` are still
  computed over the plaintext, deliberately — the digest is what the console shows to identify a
  secret and what an operator compares against an external copy. So a **low-entropy** secret stays
  guessable from its digest, which travels in the same snapshot. The vault is for high-entropy
  credentials.

  **`docs/BACKUP.md` now leads with the failure mode:** restore `ken.db` without the matching
  `vault.key` and every secret is unreadable, while the restore looks entirely successful — the
  database opens, row counts match, and `station_vault` still lists every secret by name, size and
  digest. Only the values are gone. **Back the key up separately from the snapshots**, never into
  the same place.

  **No migration.** A stored value carries a `kv1:` prefix; anything without one predates this and
  is read back unchanged, so an existing deployment upgrades with no schema change. The honest cost
  is that pre-3.32.0 values stay plaintext until rewritten.

  A wrong key fails loudly and names `vault.key` rather than returning garbage that a restore could
  write over a real secret; a truncated key file is refused at startup rather than guessed at; and a
  `Store` with no key **refuses vault writes** rather than silently falling back to plaintext.

## [3.31.0] — 2026-08-26

### Removed

- **The consent screen no longer offers to withhold a surface.** Its per-surface checkboxes let a
  human approve a connection with no messaging, or with no knowledge base — building exactly the
  crippled session Ken's operating requirement forbids: *no Ken surface is optional; every session
  gets everything it can use.* The template's own comment stated that rule and then implemented
  the negotiation in the next clause.

  The surfaces are still **listed** — a consent screen that does not say what it grants is worse
  than one that does. Only the inputs are gone. `TestConsentStatesEverySurface` fails if either
  the checkboxes come back or the disclosure disappears with them.

- **`store.CheckScopeMix` is deleted**, with its tests, in the same commit.

  It forced every token to be dedicated to one surface family, so minting a station token
  **required unticking the knowledge base** — which is how a session onboarded on 2026-08-25
  ended up holding a station and a locker and unable to read the thing Ken exists to hold.

  **The property it claimed was already false.** Its rationale was *"a knowledge-base token cannot
  send messages and a comm token cannot write knowledge"*, but it was never applied to OAuth
  grants — its only callers were `ken token add` and the console's token-create — and
  `GrantedCapabilities` hands a single OAuth bearer kb **and** comm **and** station. Verified on
  the wire against 3.30.0: one grant, three surfaces, `200/200/200`. The same server already
  issued the credential this function refused to mint.

  **What it costs, stated rather than glossed:** a leaked token now reaches every surface rather
  than one family, and API tokens have no expiry, only revocation. That is real. It is accepted
  because the alternative was an asymmetry with no defensible line — OAuth already carried
  everything — that bought no containment while guaranteeing crippled sessions.

  The token form now ticks comm and station by default, like the knowledge-base scopes.
  `TestATokenMayCarryEverySurface` is the inverse assertion; `TestCurateIsNeverMintable` keeps the
  one exclusion that survives, which is not a surface but the curation gate.

### Fixed

- **OAuth grants recorded duplicated scopes.** The approval handler concatenated the client's
  requested scope string with the granted surfaces and never deduped, so a correct client — one
  that asks for what Ken advertises in `scopes_supported` — produced
  `… ken:kb ken:comm ken:station ken:kb ken:comm ken:station` on the grant. Authorization was
  never affected, which is why it survived: it was invisible everywhere except the one place a
  human reads it. Found on the first real end-to-end consent ever performed against production.

## [3.30.0] — 2026-08-26

### Removed

- **`space_id` is gone from both databases, and so is the `space` table.** IDENTITY.md §10
  step 5 — the last step of the identity work. Ships alone under Rule 4 with migrations
  `0021` (ken.db) and `0018` (comm.db).

  **It only ever held one value.** `0001_init.sql` inserts `space(1, 'personal')` and
  nothing anywhere else ever inserts a space — no `CreateSpace`, no console route, no CLI
  subcommand, no MCP tool — while every column was `NOT NULL DEFAULT 1`. It was the SHAPE
  of a tenancy model rather than one, and §9.1's decision is that a second user is another
  INSTANCE federated over COMM, not another row-set behind a predicate.

  What went: the column from 7 tables in ken.db (`actor`, `entry`, `comm_room`, `station`,
  `station_link`, `station_link_denial`, `station_request`) and 3 in comm.db (`channel`,
  `endpoint`, `pairing_code`); 5 indexes rebuilt without their dead leading column;
  `idx_entry_space` dropped outright; the `space` table; and 160 non-test Go references.
  `ListChannelsForSpace` is now `ListChannelsForConsole`, which is what it was always for.

  **The cost, stated plainly:** Ken can no longer become multi-tenant by filling in a
  column. That door is closed on purpose.

### Fixed

- **Four space-scoping tests and a fifth assertion went with the column, deliberately**
  (§9.1: *"deletes that test in the same commit, deliberately and reviewably, and says
  why"*). §9.1 named one; there were five. They were REAL checks — a refuter deleted the
  predicate and CI went red in seconds — but each asserted a state no deployment could
  reach, exercised only by a fixture that wrote a second space row by hand.

  **`TestStationNameUniquePerSpace` was KEPT** and renamed
  `TestStationNameIsUniqueAndCollisionIsNamed`: name uniqueness is real, and
  `CreateStationAutoNamed`'s collision retry depends on `ErrStationNameTaken`. Deleting it
  by keyword would have removed a live control on a live feature.

## [3.29.1] — 2026-08-25

### Fixed

- **`station_me` returned an empty `ken_version` and `ken_version_note` on the
  workspace-CREATION path**, and the correct values on every established workspace.
  `meOut` was constructed at two sites and only the briefing one stamped the version.

  **It was missing from the one call that needs it most.** That field exists so a session
  can discover that the instructions it is holding are older than the server answering it
  — and the session most likely to hold stale text, and least equipped to suspect it, is a
  brand-new one calling `station_me` as its first act. The signal was present on every
  call except the first.

  The stamp now happens **once, after the handler returns**, so a future path through this
  tool cannot omit it. Found by ken-prod-ops on the first real onboarding auto-provisioning
  ever served, and located before it was reported: same tool, new workspace versus
  established, both on the same version.

### Changed

- **`TestTheRunningVersionRidesInResultsOnEverySurface` no longer overclaims.** It was
  green on 3.29.0 while the defect above was live, because it greps a source file for a
  stamp and one stamped path satisfies it on behalf of every unstamped one. It now accepts
  both stamp forms and states its real claim — *it catches wholesale removal of a surface's
  stamp and nothing finer*. Per-path proof lives over the transport instead, where the
  paths do.

  The other two surfaces were audited the same way, per construction site rather than per
  file: comm's `pollOut`/`channelsOut` and the knowledge base's `searchOut` stamp at every
  site. Stations was the only instance.

## [3.29.0] — 2026-08-25

### Removed

- **The binding-voucher chain is deleted — `docs/IDENTITY.md` §10 step 3, and §9.2 called it "the
  single largest safe deletion available".** Gone: `IssueBindingVoucher`, `RedeemBindingVoucher`,
  `SweepBindingVouchers`, the 5-minute TTL, single-use redemption, endpoint pinning, actor matching,
  hash-at-rest, the hourly janitor sweep, the `station_binding_voucher` tool, the `binding_voucher`
  argument, and four sentinel errors whose wording existed only to say which way a voucher failed.

  **Its condition was met before a line was deleted.** §9.2: *"The voucher exists SOLELY so a
  station key never crosses to the comm surface as a tool argument. Nothing to hand across, nothing
  to hand it with."* 3.25.0 gave one identity all three surfaces; 3.26.0 replaced the per-folder
  station KEY with a workspace id that authorises nothing. There was no key left to keep off that
  surface.

  **Binding now:** `comm_bind` with the `X-Ken-Workspace` header set. Nothing to fetch, nothing to
  redeem, nothing that expires in five minutes.

  **The endpoint binds with an EMPTY `bound_by_station_key_id`, deliberately.** That column is the
  second weld — checked at use on every call, with a *missing* row treated as revoked. Bound this
  way an endpoint names no key, so the check skips it: nothing authorised it, nothing can sever it
  through that column, and revocation moves to the credential that **owns** the endpoint, which has
  been re-pointable since 3.19.0. **One credential, one revocation, instead of two welds on one
  row.** Writing anything into that column would look tidy and would cut the endpoint off on its
  very next call, for a key that never existed — a test asserts it stays empty.

  **Existing bindings are untouched.** Endpoints bound the old way keep their key id and keep being
  severed by it; only the ability to mint a voucher is gone. ken-prod-ops holds eight of them and
  verified 3.27.0 on the wire before this shipped.

  **The tests went with it**, which is what the step asked for: `station_voucher_test.go`, ten cases.
  That also settles item (2) of an open task recording that `VoucherTTL` was effectively untested —
  *"either write the clock test or delete the mechanism, but do not leave it in this state."*
  `TestTheVoucherChainStaysDeleted` now fails if any of it returns, because deleting tests is how a
  removed mechanism quietly comes back.

  **The table survives** until its migration ships alone under Rule 4. Nothing reads it.

### Changed

- **Every text that routed a session to a voucher was corrected**, including four agent-facing error
  messages, `comm_register`'s and `comm_bind`'s descriptions, `docs/COMM.md`, the endpoint-migration
  runbook, and `STATIONS.md`'s S5 — which is marked SUPERSEDED with its reasoning preserved, because
  that reasoning is exactly *why* the deletion was safe.

## [3.28.0] — 2026-08-25

### Fixed

- **The console could not mint a station-scoped token — and the reason it gave was removed by the
  release that shipped one line above it.** `consoleCommScopes` excluded the station family,
  justified by *"/station/mcp requires a `kens_` key BOUND to a station, and this path issues an
  unbound `ken_` token, so it would authenticate nowhere while looking exactly like a working
  credential."*

  **True this morning; false as of 3.27.0**, whose fourth wall-fix is precisely that a plain `ken_`
  token carrying `station` reaches `/station/mcp` — the only door left for a client that cannot run
  an OAuth flow. **The justification was deleted by the same commit that left the exclusion
  standing**, so the operator could not mint the one credential the fix exists to serve. Found by
  ken-prod-ops within the hour of it shipping.

  **This omission has now happened twice on the same list.** The comment four lines above it
  records the first: until 3.10.0 the console could not mint comm scopes either, and *"an operator
  following it minted a token, handed it to a session, and watched `comm_register` refuse it for a
  missing scope."* **A comment describing a past instance of a defect is not a guard against the
  next one** — it sat directly above the line that reproduced it.

  `TestConsoleCanMintEveryAgentScope` now derives the requirement from the transports themselves:
  every scope `/comm/mcp` or `/station/mcp` requires must be mintable from the console, so a family
  added to a surface cannot be silently unmintable a third time. Verified against both historical
  instances. `curate` stays unmintable by every path.

  `CheckScopeMix` already permitted `{station, comm}` — its own test calls it *"THE PERMITTED PAIR:
  a station that talks"* — so offering both families together loosens nothing.

## [3.27.0] — 2026-08-25

### Fixed

- **3.25.0 and 3.26.0 were unreachable by the clients they were built for. Four walls, each
  sufficient alone, measured against the live deployment by ken-prod-ops.**

  ```
  POST /mcp          401  www-authenticate: Bearer resource_metadata="…"
  POST /comm/mcp     401  (no www-authenticate at all)
  POST /station/mcp  401  (no www-authenticate at all)

  /.well-known/oauth-protected-resource/mcp          200
  /.well-known/oauth-protected-resource/station/mcp  404
  scopes_supported: ["read","write","offline_access"]     ← no ken: scope anywhere
  ```

  1. **No discovery challenge on the two new surfaces.** `/mcp` answered its 401 with the RFC 9728
     `WWW-Authenticate` header; the others answered with three headers and none of them that. A
     client had nothing to follow.
  2. **No per-resource metadata for either.** The path-suffixed document was served for `/mcp` and
     404'd for the rest, so a client that followed the spec found nothing.
  3. **`scopes_supported` never named `ken:kb`, `ken:comm` or `ken:station`** — so a client asking
     for exactly what was advertised landed in the legacy branch **by construction**, and was
     refused, correctly, at the end of a flow that could never have produced anything else. Their
     estate proves it: **8 grants, every one `read write offline_access`.**
  4. **And the client that reported the deadlock cannot run an OAuth flow at all.** Claude Code
     inside the desktop app is non-interactive: *"This session is non-interactive, so Claude cannot
     run the OAuth flow here."* Every fix above unlocks an OAuth client and unlocks nothing for it.
     A plain `ken_` API token carrying the `station` scope now reaches `/station/mcp` — the
     credential such a session already holds, through a door that needs no browser. **The scope
     still gates it: a comm-only token gains nothing.**

  **The comment on `scopesSupported` said the strings were "cosmetic to Ken", and survived the
  release that made them load-bearing.** That is the same defect one layer out: a capability fully
  implemented, fully tested, and never announced. As prod put it — *"there is no button to add and
  no text to shorten. The advertisement IS the interface."*

  **A session can never diagnose this class.** The reporting session's own words: *"a
  401-without-`WWW-Authenticate` is indistinguishable from a 401 with one — both render as the same
  'needs authorization' notice. The diagnostic detail that would let someone fix the server is not
  propagated to me at all."* So all four are pinned by tests that assert over HTTP rather than on
  the helpers, which is exactly where the third one hid: the URL builder was correct and the header
  was never sent.

## [3.26.0] — 2026-08-25

### Added

- **A session with no workspace now gets one immediately — the station-registration deadlock is
  gone.** `docs/IDENTITY.md` §5: *"fully working, auto-named, no approval."*

  What it replaces, and Vlad's words for it at the console — *"It is absurd the way it works now"*:

  ```
  station_request lives on /station/mcp
  /station/mcp    required a station-scoped token
  a station key   is minted per-station, for an EXISTING station
  a station       is created by approving a station_request
  ```

  Every arrow points back one step. `station_request`'s own description called it *"the only tool a
  key with no station may call"* — true, and it hid the real constraint: **a key with no station
  could call it; a session with no KEY could not, and that is every session being onboarded.** There
  was no `POST /stations` either, so a deployment with zero stations could only get its first from
  the CLI — and a console-minted key was issued under the OPERATOR's actor, so it could never bind
  the session it was minted for. Console-first and binding were mutually exclusive.

  Now: call `station_me` — the call every session is already told to make first — and you have a
  workspace, named after your folder, working from the next call. The id comes back in the result
  along with what to tell your human, because a result is the channel that always arrives.

- **`X-Ken-Workspace` on the folder's MCP entry selects which workspace a session works as**
  (§4). **It authorises nothing** — the credential does that — which is exactly why it can live in
  a config file forever: *"a name tag cannot leak, cannot be burned, never expires and never
  rotates."* A credential that carries its own station ignores the header, because that binding is
  a fact about the credential rather than a preference.

  **Existing stations need no migration.** The header value IS the `station_id` they already have.

### Changed

- **`station_request` is now for the case that is still a decision** — a human who wants to name and
  approve a particular workspace — and its description says so, instead of advertising itself as
  the only door for a session that had no way to reach it.

### Fixed

- **A second folder with the same name was refused a workspace.** The collision retry matched the
  error *message* for `"unique"`, while the sentinel's text reads *"already in use in this space"*.
  **The deadlock rebuilt in miniature, by a check reading human-readable prose** — which is the
  failure mode that stops working silently the day someone improves the wording. It matches the
  sentinel now.

- **The workspace header was resolved somewhere the tool handler could never see it.** It was read
  in the auth middleware and written onto the principal; the middleware ran, the header was present,
  the log line fired, the principal was built correctly, and the handler still saw an empty station.
  The SDK does not hand a tool handler the request's context — it hands it the request, with
  `Extra.Header` on it, **exactly as `internal/commserver` has lifted its endpoint credential since
  it shipped.** The mechanism already existed in this repository and a second one was built that
  could not work.

## [3.25.0] — 2026-08-25

### Added

- **One identity now spans all three MCP surfaces — `docs/IDENTITY.md` §10 step 2.** A single
  human approval reaches the knowledge base, messaging and a working identity. Before this, a
  connector reached `/mcp` and nothing else: `/comm/mcp` answered *"comm requires a dedicated
  `ken_` API token"* and `/station/mcp` took `kens_` keys only, so a session wanting all three held
  three credentials minted three different ways.

  That is the blocker §9.2 names for everything after it — **the binding-voucher chain exists
  solely so a station key never crosses to the comm surface as a tool argument**, and with one
  identity there is nothing to hand across.

- **The consent screen asks which surfaces the approval covers, and the grant records the answer.**
  All three are ticked by default, because no Ken feature is optional or off by default. Untick one
  to withhold it.

  **This is not decoration — it is the control that replaces the one consolidation removed.**
  `IDENTITY-CONTROLS.md` calls the old refusal *"the highest-value item for a design that intends
  OAuth as the only mechanism, because THIS CONTROL IS THE ONE THAT SAYS NO TO EXACTLY THAT"*, and
  warns the removal would be invisible — every surface still working, until the day a connector is
  compromised and the blast radius has grown from the knowledge base to the message bus and the
  vault. Its stated condition: the withholding must be *"re-expressed as an explicit per-surface
  capability decision at grant time, not inherited from the fact that three files exist."*

  So `oauth_grant.scope` became load-bearing rather than the cosmetic column its own schema comment
  called it. **No schema change** — OAuth scope is the standard mechanism for precisely this, so
  `ken:kb`, `ken:comm` and `ken:station` live in the column that already existed. And a human can
  now withhold messaging from a cloud connector, which the register's own complaint said they
  could not.

### Changed

- **Connectors approved before this release keep the knowledge base alone.** They carry no `ken:`
  scope, which is exactly what their human agreed to. **Re-approve a connector to widen it.**
  Widening them silently would have been the invisible removal wearing a migration's clothes.

- **`curate` is on no path, on any surface.** The capability mapping is a whitelist rather than a
  pass-through, so a client cannot name its way past the curation gate. A human promotes.

### Fixed

- Four failure modes that would each have been silent are now pinned by tests, all of them
  survivors before the tests existed: legacy grants widening themselves, `curate` reaching the
  returned set, the consent handler ignoring what the human unticked, and the picker rendering
  unticked (withholding everything while looking like it granted it).

## [3.24.0] — 2026-08-25

### Fixed

- **SECURITY-RELEVANT DOCUMENTATION DEFECT: four places told operators they could encrypt their
  nightly snapshots. Ken removed that in 2.0.0 and there is no setting that turns it on.**

  | where | what it said |
  |---|---|
  | `docs/INSTALL.md` | a numbered procedure — install `age`, escrow a keypair, set a recipient |
  | `scripts/install.sh` | the same procedure, printed on the terminal after every install |
  | `deploy/ken-snapshot.service` | `Description=Ken nightly **encrypted** snapshot`, plus a recommendation to set a recipient |
  | `docs/BACKUP.md` | *"Encryption is opt-in and OFF by default — on both off-box tiers"* |

  `scripts/ken-snapshot-lib.sh` states the truth in its own comment — *"IT NO LONGER ENCRYPTS"* — and
  `docs/OPERATION.md` has said so since 2.0.0. The INSTALL procedure also warned that the step
  *"fails closed"* and keeps no snapshot when `age` is missing: **false in the other direction**, the
  plaintext is written and kept. And it linked to `BACKUP.md#encryption-turning-it-on`, a section
  that does not exist.

  **An operator who followed it installed `age`, generated and escrowed a keypair, set a recipient,
  and believed every off-box snapshot was ciphertext. Every one was plaintext** — of the whole
  knowledge base, the curator accounts, and every station vault secret. `0600` protects the file on
  that host and nowhere else, and tier 3 of the backup design is an off-box copy by definition.

  All four corrected. **The only encryption in this system belongs to Litestream** — the `age:` block
  in `configs/litestream.yml`, which covers the continuous replica and not the nightly snapshot.
  Anything else is the operator's to add in whatever moves the file.

  `TestNothingPromisesSnapshotEncryption` now fails the build when operator-facing text offers the
  control, verified by reintroducing the exact text that shipped, at each of the three sites.

  Found by an audit for the class `FINISHING.md` names — *"text asserting a control that does not
  exist is the class that propagates"* — **in a lens that died on a server error and had to be
  re-run.** Its absence would have been reported as "found nothing": a dead agent returns null and
  the harness filtered it out. The instrument had the defect it was hunting.

- **Three console strings told operators a feature was "switched off" that nothing can switch
  off — and the worst was read at an irreversible act.** After revoking a station link, an operator
  was told *"COMM is switched off in this server … Re-check from the Comm console once COMM is
  on."* There is no switch: `cmd/ken/main.go` says *"THERE IS NO SWITCH … both are gone"*, and a
  test fails the build if anything reads a retired gate. When COMM is unavailable it is because
  `comm.db` would not open — logged as **`COMM: DEGRADED … This is a failure, not a setting`** — and
  `/comm` is not routed in that state, so **the console the message sent them to 404s.** The real
  cause and the real remedy appeared nowhere.

  All three strings corrected in all three locales, and they now name the fault and point at the
  log line. `TestNoStringClaimsAFeatureIsSwitchedOff` reads the prose rather than grepping for
  retired variable names — which is why the 3.10.0 sweep missed these, as `FINISHING.md` predicted
  it would: *"text that describes the vanished switch WITHOUT NAMING IT."*

- **"Off by default" was wrong about two features, in five documents and the settings console.**
  `comm_files_enabled` has defaulted **ON** since 2026-08-24 — the code comment explains it flipped
  under the ruling that no Ken feature is optional or off by default — while the settings help, the
  operator manual and `COMM.md` all still said off. And the **OAuth server** has been unconditional
  since 2.0.0, yet `OAUTH.md` and `INSTALL.md` both said "off by default"; `INSTALL.md` carried both
  claims **four lines apart**, "off by default" and "there is nothing to enable", and neither reader
  nor writer noticed. This is the exact phrase `FINISHING.md` cites as what *"put 'off by default'
  on the public site"*.

- **`include_instructions` was unusable by the sessions it exists for.** ken-prod-ops ran it from
  inside that population and it failed: their client, having no schema for the property, serialized
  `true` as the string `"true"`, and validation refused the call before any handler ran. **A client
  with no schema for a property cannot type that property correctly** — that is the definition of
  the case, so a boolean-only contract excluded exactly the callers the argument serves. It accepts
  what a schema-less client sends now.

  The test that would have caught it did not exist because **every test written inside this
  repository has the schema available**, and therefore types the argument correctly. There is now
  one that sends the raw JSON a schema-less client sends, over the real transport, and it
  reproduces prod's exact failure as a mutation.

- **Twenty-one tool descriptions had no space at the join**, and one rule was duplicated — both
  introduced by the scripted edit that moved per-tool rules into descriptions in 3.22.0.

### Added

- **`kb_get` asks for the outcome at the moment one is owed**, naming every slug it just handed
  over. The instructions have always said *"close the loop EVERY time"* and the live deployment
  measures **14.8%** — 250 uses, 37 outcomes, and only 22 of 108 entries with any outcome at all.
  `FINISHING.md`'s diagnosis is what this acts on: *"something the instructions request that nothing
  prompts for at the moment it matters."* A rule delivered once at connect competes with the whole
  conversation; a rule in a result arrives at the occasion.

### Changed

- **`station_task_defer` asks for the reason as state, not as feeling.** Prod's observation was that
  the verb is date-shaped while the problem is state-shaped — *"the condition may already be
  satisfied and nobody rechecked."* The date stays a reminder; the reason is now documented as where
  a recheck is recorded, since `blocked_on` is set once at creation and nothing ever revisits it.

## [3.23.0] — 2026-08-25

### Added

- **`ken_instructions` — a session can re-read its own instructions, current and in full.**
  Vlad's suggestion, made hours after 3.22.0 shipped, and it closes the half of the problem that
  release could not: **fitting the instructions under the client's cap protects a session that
  connects today, and reaches nothing that connected before.** A tool RESULT is neither pinned nor
  truncated, so it is the only channel that can carry the current text to a session that already
  exists. Registered on all three surfaces; the answer also names every surface the deployment
  serves, so a session holding one endpoint learns what the others do and can ask for them by name.

- **`ken_version` gains an optional `include_instructions`, and it is not a duplicate of that
  tool.** **Whole tools do not travel across the freeze** — `ken_instructions` will never appear in
  a conversation that began before it existed, which is exactly the population that needs it.
  **Parameters do travel**: the server validates what ARRIVES, not the client's captured schema, as
  ken-prod-ops proved by passing `to_room` to a `comm_send` schema that has no such property. So a
  frozen session can ask through a tool it already holds. Two doors, two populations, neither
  covering both — a test pins that reasoning so the argument is not later deleted as redundant.

  **Found the hard way the same day.** An MCP registration on two separate machines was serving
  pre-3.22.0 instructions *and* pre-3.22.0 tool descriptions against a fully patched 3.22.0 server,
  in conversations that began AFTER the upgrade — while its `ken_version` result came back
  completely current. No Ken release can reach that captured text. A result can.

### Changed

- **The version stamp now says the field is truncated and names the way out.** It is the one piece
  of text every session reads first, and it was telling sessions their manual might be *old* while
  saying nothing about it being *short*. The three blocks each gave up a clause to pay for it.

### Fixed

- **`comm_bind`'s refusal claimed an invariant Ken does not hold, and named a harm that cannot
  happen.** It said *"an endpoint cannot move between stations, because it would carry the first
  station's unread mail into the second."* Both halves were false, in opposite directions:
  `comm_unbind` clears `station_id` and its own success note ends *"You can bind again later"*, so
  unbind-then-bind performs the move and the tool doing the bypass advertises it; and the mail
  cannot travel, because `delivery.party_key` is stamped at write time and no delivery row is ever
  moved.

  What actually moves is **channel membership** — a seat is re-derived from the live binding, so
  rebinding elsewhere hands the new station the old one's seats. The refusal says that now, says
  plainly that the mail stays behind, and stops asserting a prevention it cannot deliver. A false
  invariant is worse than an honest guard: it is what persuades the next reader the bypass is
  blessed. `comm_unbind`'s note is qualified in the same change, since one tool forbidding what the
  other offers was half the defect.

  Enforcing it properly needs history this schema does not keep — unbind clears both `station_id`
  and `bound_by_station_key_id`, so nothing afterwards records where an endpoint has been. That is
  a schema change, it ships alone under Rule 4, and the identity work may retire the mechanism
  first.

## [3.22.0] — 2026-08-25

### Fixed

- **Most of Ken's connect-time instructions had never reached any session.** The MCP client
  truncates the `instructions` field at **2048 characters**. Ken was sending 3807, 8095 and 5767:

  | surface | sent | delivered |
  |---|---:|---:|
  | `/mcp` knowledge base | 3807 | **53%** |
  | `/comm/mcp` | 8095 | **25%** |
  | `/station/mcp` | 5767 | **35%** |

  Reported by a Claude Code session that could not find Station, verified by ken-prod-ops against
  the v3.21.0 source, and confirmed here three ways: two independently observed cut points on two
  machines land exactly where character 2048 falls in the source, and computing that offset
  predicted both fragments verbatim. Ken contains no truncation code; the cap is downstream.

  **All three now deliver in full** — 2047, 2048 and 2037 characters — and a test per surface fails
  the build when the string that faces the cap grows past it.

- **The instruction-drift stamp had never been delivered either, on any surface, since 3.1.0.** It
  was 1053 characters **appended** to blocks of 2754, 7042 and 4714, so it began past the cut every
  time. The mechanism survived only because it was built with two channels: `instructions_may_be_stale`
  and `how_to_check` ride in every `ken_version` RESULT, and results are not truncated. **The design
  was saved by redundancy nobody knew was load-bearing.**

  The stamp is now 240 characters and **prepended**: under a cap, position is delivery. The long
  explanation of what the freeze does and does not block moved into `ken_version`'s description,
  which arrives intact and is read at the moment a session has the discrepancy in front of it.

- **A session holding only `/mcp` had no way to learn that COMM and stations exist.** The knowledge
  base block mentioned neither, and the two blocks that would have said so are on endpoints it
  cannot reach. A session spent a conversation probing four paths and reading three 404s to find
  `/station/mcp`, then correctly refused to assert Station did not exist because it could not tell
  *absent* from *undocumented*. **Every block now names all three surfaces in its opening lines**,
  and `ken_version` reports them in its result — the one channel that reaches a running session.

- **The curation-language rule was appended last, so the deployment that turned the feature ON was
  the only one guaranteed never to be told about it.** It rides on `kb_save` and
  `kb_propose_enhancement` now, the two calls it can change.

### Changed

- **Per-tool rules moved from the instruction blocks into the descriptions of the tools they
  govern**, which the client delivers intact — all 43 are under the budget, the largest being
  `comm_poll`. A rule is read at the moment it applies rather than in a wall of text at connect,
  and a second gate fails the build if a description grows past the cap in turn.

  Several tests had pinned exact sentences against the truncated consts — green about text no
  session had ever read. They now assert against the UNION of what a client receives, and against
  the property rather than the wording, so a rewrite that keeps the rule passes and one that drops
  it does not. Pinning the sentence would have forced the text back into the field that cuts it.

- **`FINISHING.md`'s status header is derived rather than promised.** It went nine releases stale
  saying "Released: 3.12.1 … Fifteen items remain open" while 3.21.0 was live and seven boxes were
  unchecked — the third recurrence of a failure the file had already diagnosed
  (*"a rule is not a mechanism"*) and already prescribed the remedy for. A test now fails the build
  when the header does not name the newest release, or when its count disagrees with the boxes.

## [3.21.0] — 2026-08-25

### Added

- **A bulk move's confirm now NAMES every session it will move, instead of counting them.**
  ken-prod-ops put the objection on 2026-08-25, against a page that had just passed its own count
  check: *"an operator reads 11, inspects 6, clicks a bulk verb that moves 11 — and the five they
  never saw include `runway-prod-admin` and `rb5009-config`, both in use this week."*

  A number is a claim about a population; a confirm that names the population is a claim the
  operator can check. That is the standard S6 already sets for revocation — *"this will disconnect
  2 live sessions"* — carried one step further, to which two.

  `EndpointsOwnedBy` and `EndpointsBoundBy` list exactly what the matching verb moves, using the
  verb's own predicate — no `space_id`, because the verbs have none either. **The count beside the
  button is now `len(list)`**, so the number and the list cannot disagree; a test pins both against
  the `Count…` pair `/tokens` still uses, and pins that pair against the data, because
  equal-and-wrong is the failure an agreement check cannot see.

  The re-bind confirm deliberately omits unbound endpoints that share the token: its verb cannot
  move them, and an over-stated blast radius is a false alarm an operator learns to click through.

### Fixed

- **`/comm` was reported as omitting unbound endpoints from the sessions table. It does not** —
  verified on the live deployment (14 rows, six showing `—` under **Bound by**) and reproduced
  locally. The reading behind the report was about the credentials block above it, where an
  unbound endpoint correctly has no *binding key* row. Recorded because the underlying worry was
  right even though the mechanism was not, and the fix above is what it earned.

## [3.20.1] — 2026-08-24

Corrections to 3.20.0, found by an adversarial review of its own diff — five independent lenses
over the change, every candidate finding handed to two skeptics told to refute it. Sixteen
survived. No behaviour of the application changed; what changed is operator text that was false,
tests that were not holding what they claimed, and a gate that was weaker than advertised.

### Fixed

- **RETIRING a station key does not end the COMM sessions it bound — REVOKING does, and 3.20.0
  used the two words interchangeably.** `store.IsStationKeyRevoked` reads `revoked_at` and nothing
  else, so a retired key stops the *station* surface at the holder's next call and leaves every
  bound COMM endpoint running. That is what `RetireStationKey`'s own comment says and what
  `stations.key_retire_help` has told operators all along — *"COMM endpoints it already bound keep
  working. Use Revoke instead only when you also want those severed."*

  3.20.0 shipped the opposite claim in `comm.credentials_help` and `comm.rebind_all_help`, **in all
  three locales**, on a card whose neighbouring string got it right — plus in five documents and
  the CHANGELOG. An operator reading it would either avoid Retire (the harmless verb) or reach for
  Revoke (the destructive one, with no un-revoke path anywhere) believing they were equivalent.

  **This is the second time the project has got this wrong, in opposite directions.** Six shipped
  strings once promised that retiring left live sessions alone, for four releases after the code
  stopped doing that, because the fix touched no `.properties` file — `internal/store/stations.go`
  carries that story in a comment. `TestNoCommStringClaimsRetireSeversComm` now fails when a COMM
  string asserts that retiring ends something, in any of the three locales. Verified by
  reintroducing the exact strings 3.20.0 shipped.

- **`TestEveryPostRouteHasAConsoleSurface` matched the route path anywhere on the page, so a nav
  link counted as a form.** `href="/settings"` in the shared layout satisfied `POST /settings`.
  Verified by deleting `settings.html`'s only form action: the gate stayed green. Every route whose
  surface is a page-level form — `/settings`, `/setup`, `/tokens`, `/rooms` — was effectively
  unwatched. It now requires `action="…"`, and catches all four.

  The gate added in 3.20.0 to catch a control with no button was itself an instance of the class
  it hunts.

- **Four assertions in the new tests were answered by a different part of the page.** Both bulk
  pickers could render with **zero options** and the suite stayed green (the per-endpoint pickers
  below satisfied the check); the **Bound by** column could be deleted entirely (the re-bind form's
  hidden `from_key` carried the same id); and the **live-endpoint count** — the number an operator
  weighs before an irreversible bulk move — was never read off the page at all. The tests now scope
  to the section they are about, and the fixtures give an endpoint a *different* owning token from
  its binding key, as production does.

- **Two guards in the new statements were held by nothing.** Dropping `bound_by_station_key_id=?`
  from the single-endpoint WHERE (so a stale console page silently overwrites someone else's move)
  and `revoked_at IS NULL` from the bulk WHERE (so the flash says *"the sessions keep running"*
  about revoked ones) both left the whole repository green.

  The first read as *held* because the mutation that tested it removed the clause and left its
  bound argument, so the statement failed on placeholder arity and the test died of a SQL error.
  **A mutant that dies of its own malformation is indistinguishable from one the tests killed** —
  this project's defect class, committed inside the harness built to find it.

- **The Re-bind picker had no test that it excludes the key the endpoint is already bound by** —
  the no-op-that-flashes-success defect 3.20.0 fixed on Re-point could have been re-created on the
  control added in the same commit.

- **`RUNBOOK-ENDPOINT-MIGRATION.md`'s capability table still said the console cannot show which
  token owns an endpoint or whether it is bound, and sent the reader to `sqlite3`.** Both have been
  console columns since 3.19.0 and 3.20.0 — the two releases the runbook's own migration step is
  about. The refusal message now names all four states it collapses, including the swept-away one
  the CHANGELOG claimed and the string omitted.

## [3.20.0] — 2026-08-24

### Added

- **The second weld can be moved too. A bound endpoint answers to TWO credentials, not one.**
  `token_id` says which token may drive an endpoint; `bound_by_station_key_id` says which station
  key authorised its binding, and that column is checked at **use, on every single call**
  (`store.IsStationKeyRevoked`), with a **missing** row treated as revoked. So **revoking** a station
  key — or deleting its row — ends its bound sessions exactly as revoking their comm token does,
  through a column nothing rendered and `docs/IDENTITY.md` §9.3 did not name. 3.19.0 moved one weld
  and left the other. *(Corrected in 3.20.1: this entry originally said **retiring**, which severs
  nothing here.)*

  `/comm` gains **Re-bind** beside Re-point, and `POST /comm/keys/{id}/rebind` moves every live
  endpoint one key bound in a single statement. The endpoint keeps its id, its **secret**, its
  channels and seats, its **station**, and everything queued for it — and unlike an owner
  re-point, **the running session needs no config edit and no restart**: nothing it holds changes,
  only which key could sever it.

  **A binding only ever moves to another key of the SAME station**, and that rule lives in the
  `UPDATE`'s `WHERE` rather than in a check above it. Moving a binding onto another station's key
  would hand that station's operator a lever that disconnects these sessions and take it away from
  the station that owns them — an authority laundered, with every count on both pages still
  reconciling.

  **Smaller than the first weld, and measured rather than assumed.** ken-prod-ops counted 8 live
  bound endpoints against 8 distinct binding keys on 2026-08-24 — 1:1, so revoking one key today
  costs exactly one session, against eleven for the token weld. **That ratio is an accident of how
  those stations were provisioned one at a time, not a property**: nothing stops one key from
  binding several endpoints, which is why the bulk verb exists and why `/tokens` states the count
  before the click.

  **It is also the only repair for a session `ken token revoke` has already killed.** The CLI runs
  in a separate process with no `comm.db` handle, so it cannot sever the endpoints its key bound:
  they stay live in `endpoint`, listed and counted and apparently healthy, and are refused one call
  at a time with nothing on any page saying why. Re-binding them to a working key restores them
  without a re-registration. On the console revoke path the sweep does mark them, and a revoked
  endpoint is refused here — so **there, this is a move to make first.** There is no un-revoke path
  anywhere in the tree.

- **`/comm` groups endpoints by the credentials that would end them**, with the live count and the
  bulk move beside it — owning tokens in one table, binding station keys in the other. The
  concentration this makes visible had to be queried from the database by hand.

### Fixed

- **The bulk re-point shipped in 3.19.0 with no way to reach it.** Route, handler,
  `RepointEndpointsOfToken`, a console test through the mux, flash strings in three locales — and
  no form anywhere posted to `/comm/tokens/{id}/repoint`. The verb that exists precisely for the
  eleven-endpoints-on-one-token case could be used only with `curl`, which breaks the standing rule
  that the console is the main method for any operation.

  **`TestEveryPostRouteHasAConsoleSurface` now fails when a POST route has no form.** It reads the
  templates rather than the mux, because a test that POSTs a path proves the handler works and
  never that anything offers it. It failed on its first run naming exactly this route — and named
  its own allowlist entry as wrong in the same run, which is the both-directions check doing its
  job.

- **The Re-point picker no longer defaults to a no-op.** It listed every comm token, the
  endpoint's own included — and tokens are listed newest first, so whenever the endpoint's own
  token was also the most recent one it sat at the top and became the default. Clicking flashed *"…the session needs the new token in its config
  and a restart"* over a row nothing had touched. A success message for a no-op, with instructions
  attached. The store is right to accept the write; the console is what must not offer it.

- **A refused re-bind now says which rule refused it.** The same-station guard lives in the
  statement, so a key from another station simply moves no rows and the bare answer is
  `ErrNotFound` — telling the operator that an endpoint visible on the page does not exist. The
  explanation is derived *after* the refusal, so it cannot re-open a check-then-act window, and it
  names all four possible states honestly — wrong station, already moved, revoked, or swept away
  while the page sat open — rather than guessing which one applies.

## [3.19.0] — 2026-08-24

### Added

- **A COMM token can be rotated. Until now the operation did not exist.** `endpoint.token_id` was
  written once at registration and no `UPDATE` anywhere re-pointed it — so "rotate a compromised
  comm token" decomposed into *revoke it, re-register every session, and hand a human-minted
  pairing code to every unbound seat.* On the live deployment that is **eleven live endpoints on
  one token**, including the channel the two stations would use to report that it had gone wrong.

  `/comm` gains **Re-point** beside Rotate, and `POST /comm/tokens/{id}/repoint` moves every live
  endpoint of one token in a single statement — a half-moved estate is the state nobody has a
  recovery story for. The endpoint keeps its id, its **secret**, its channels and seats, its
  station binding and everything queued for it. Only which credential may drive it changes, so a
  session needs one line edited in its MCP config and a restart: **one edit per machine, not one
  per endpoint.**

  **Console only** — `requireAuth` + CSRF, no MCP tool in any form. An `endpoint_id` is not a
  secret; it is the routing address, rendered on `/comm` and printed through the runbooks. A
  self-service re-point would let any session on a shared machine seize any endpoint on it with
  nothing to steal first.

  **No migration.** `idx_endpoint_token` has existed since `0001_init` and nothing had ever used
  it — the schema anticipated this operation and nobody wrote it.

- **`/comm` shows which token owns each endpoint.** It never did, which is why a credential
  carrying eleven endpoints was invisible until someone queried the database by hand.

### Fixed

- **The `/tokens` revoke confirm now states a blast radius for a plain COMM token.** The count
  added in 3.17.0 was gated on the row being a *station key* and used `CountEndpointsBoundBy`,
  which counts by `bound_by_station_key_id` — the exact column `PARKING-LOT.md` E1 names as the
  one that "matches zero rows and does nothing, silently" for a comm token. So the fix for E1
  shipped keyed on the column E1 identifies as wrong, and the credential that actually carries the
  endpoints showed no number at all. A station key severs what it **bound**; a comm token kills
  what it **owns**. Both are wired now.

- **The endpoint-ownership check had no test.** `if ep.Owner.TokenID != p.TokenID` is the last
  line of `auth()` and the only thing between "holds a valid endpoint id and secret" and "is the
  right principal" — and deleting it left the entire suite green. The one grep hit for its error
  string across every test file is a *comment* explaining how another test avoids triggering it.
  Pinned now, verified by deleting the line.


## [3.18.0] — 2026-08-24

### Added

- **A file can be offered to a ROOM or to a LINKED STATION.** `comm_file_offer` takes exactly
  one of `channel_id`, `to_room` or `to_station`. A room offer is **one** attachment against the
  file budget however many members receive it.

  The item this closes understated itself. "A file cannot be offered to a room" was true and not
  the worst of it: `comm_send{to_station}` — the path Ken's own instructions call the simplest
  way to reach a peer — has no channel row *by design*, so **two linked stations could not
  exchange a file at all**. Files worked on 1 of the 4 ways COMM addresses.

  Authorisation delegates to the rule that already governs a send of that kind: a room offer is
  legal exactly when a room send is, a pair offer exactly when a pair send is. Two rules for one
  relationship is how they drift.

### Changed

- **`attachment` is scope-shaped** (migration `0017`, ken-comm 16 → 17). `channel_id` and
  `recipient_endpoint` become optional; `scope_id` — the seam migration 0010 cut and nothing
  ever used — becomes the address and is `NOT NULL`.

  **Not a table rebuild.** `ALTER COLUMN DROP/SET NOT NULL` works in place at the pinned driver
  (SQLite 3.53.3) and is a syntax error at 3.50.4, so comm.db is now hard-pinned to a ≥3.53
  driver. Nothing in the repo asserted that floor; a test does now, and it exercises the
  capability rather than parsing a version string.

- **`enqueueLocked` is gone** — 84 lines, and COMM drops from three message-insert paths to two.

### Fixed

- **Three defects that could not surface until a scope-shaped row existed.** `attachmentByID`
  INNER JOINed `channel`, so a room attachment would have reported *not found* for a row sitting
  in the table. The two `/comm` file counters did the same, so an operator would have read
  `Files=0` while the relay held bytes. And download grants keyed on a recipient rowid frozen at
  offer time — which cannot express a room and does not exist for a pair. Authorisation is now
  scope membership **as of now**, so removing a station from a room stops its outstanding grants
  without revoking them one at a time.


## [3.17.0] — 2026-08-24

### Changed

- **File exchange is ON by default.** `FilesEnabled` shipped defaulting off so an operator could
  opt into the relay's risk separately; under the ruling that no Ken feature is optional or off by
  default, that default was itself the defect. The per-operation toggle stays and is now a **kill
  switch**, not an opt-in — the same distinction `main.go:337` already drew for COMM: *"a feature
  an operator can be missing is a feature every doc, every instruction and every session has to
  hedge about."*

  **This silently re-enables the relay for anyone who deliberately turned it off**, and
  `UPGRADING.md` says so prominently. `settings.Apply` *deletes* a key equal to the compiled
  default, so "off by choice" and "off by default" are stored identically — as nothing. That is
  true of every default Ken has ever changed, and it is written down now.

### Fixed

- **One station-addressed message permanently disabled the revoked-channel purge.** The sweep
  filtered `id NOT IN (SELECT channel_id FROM message)`, and `message.channel_id` became nullable
  in migration 0009 — a pair or room message belongs to no channel and writes NULL. `NOT IN` over
  a set containing NULL is never true, so the first `to_station` send a deployment ever made turned
  that purge into a permanent no-op, with **no error and no log line**. The rule is stated verbatim
  twelve lines above, in the endpoint purge, which got the guard when 0009 landed.

  **The existing sweep tests could not see it**: their fixtures leave `message` empty, and `NOT IN`
  over an empty set is true — so the purge worked perfectly in tests and never in production. The
  new test seeds a pair message first, because the poison *is* the fixture, and carries a control
  proving the purge works without it.

  **What it has actually cost, measured rather than implied: nothing.** ken-prod-ops evaluated
  both predicates on the live database — the unguarded form returns 0 channels, the guarded form
  returns 3, and 52 messages carry the NULL that poisons it — and then read the TTL that decides
  whether any of the three were due: `comm_metadata_ttl_sec = 5184000`, sixty days. **None is
  past it; the first would have been due 2026-10-06.** The mechanism is as bad as described and
  the realized leak on the only deployment there is is zero rows. Said plainly because "a
  retention leak that only grows" is true of the mechanism and would misdescribe the damage.

- **Offering a file to a room named a parameter that does not exist.** The refusal came from
  `ChannelFor`, written for `comm_send` where `to_room` is real; `comm_file_offer` has no such
  argument, so following the advice returned `unexpected additional properties ["to_room"]`. Each
  error blamed the other and a session could not tell *"I called it wrong"* from *"the feature does
  not exist."* It now says files are channel-only today — a limitation that is about to stop being
  true, which is what makes it the right thing to say rather than a workaround.

- **`ScopeCommFile`'s comment said it was "required by nothing yet"** while it was enforced in two
  places. A reader auditing the scope model from that block would have deleted live checks with the
  comment as justification.


## [3.16.0] — 2026-08-24

### Removed

- **`DROP TABLE station_block`.** ken.db 19 → 20. The three store functions went in 3.15.0;
  this drops the table they wrote to, and it **ships alone** because it is a schema change that
  destroys data rather than an additive index.

  It shipped in every deployment's schema since 3.0.0, could be written through an exported
  method, and **bumped the roster epoch so the write looked consequential** — while no send path
  ever consulted it, and none could: `comm.Store` holds handles to comm.db alone and comm.db has
  no block mirror.

  **Verified on the upgrade path, not just a fresh install** — a 3.15.0 database at schema 19
  with the table populated, migrated with the new binary: schema 20, both objects gone,
  `foreign_key_check` and `integrity_check` clean, exactly one table dropped, only
  `schema_migration` moved, and every station row survived. `station` and `actor` are referenced
  *by* it and not the reverse, so dropping it removes two inbound references and breaks nothing.

  **What it costs is recorded rather than glossed:** the capability is not superseded. To stop
  one pair talking, an operator must still remove a station from a room and cost it that room's
  other relationships. `PARKING-LOT.md` #25 keeps the gap and the shape a future fix would take.

## [3.15.0] — 2026-08-24

### Added

- **Pending notebook promotions now reach the human.** `CountPendingPromotions` calls itself
  *"the console's badge source"* in its own doc, and there was no badge. A session promoted a
  page and nothing in the chrome moved.

  Two halves, and they had to land together. The **badge** — promotions join open
  human-blocked tasks in the `/stations` nav and dashboard counts, because both are things
  waiting on the human on the same page and two adjacent numbers would ask them to add up.
  And the **live-refresh count**: `/stations/count` returned pending *requests* only, so a
  promotion filed at any hour left the page re-stamping *"last checked &lt;now&gt;"* on a timer
  while the item sat invisible below it — the live-refresh contract half-wired against the
  surface it exists for.

  **The marker and the endpoint now come from one function**, `stationsLiveCount`. That is the
  point rather than tidiness: `app.js` reloads whenever the endpoint disagrees with the value
  the page was rendered with, so computing them separately makes a drift an **infinite reload
  loop** — worse than the silence it replaces. Summing them in the template would have been
  the same hazard with an arithmetic slip as the trigger. A test asserts the two surfaces agree
  after each kind of arrival; `/stations/count` had no tests at all before this.


- **Revoking a station key now states its blast radius before the click.** S6 has required this
  since stations shipped and `STATIONS.md` asserted it shipped; `CountEndpointsBoundBy` was
  written for it and had exactly one caller — a test whose failure message reads *"the console
  states this number before the click"*, standing in for a surface that did not exist.

  The `/tokens` revoke confirm now says how many live sessions revoking would sever. It stays on
  `/tokens` rather than `/stations` deliberately: revoking goes through the ordinary token path
  so it cannot diverge from `ken token revoke`, which is S6's own reasoning.

  **With COMM off it says UNKNOWN, not zero** — comm.db and every endpoint in it outlive the
  server flag, so a zero there would assert a fact nobody measured. That is the two-field shape
  repaired one page over in the same release, applied here from the start rather than after.


- **A test that fails when an exported store method has no production caller.** Thirteen had
  accumulated, and nothing would ever have caught them: an unused *exported* method is never a
  compile error, `go vet` does not look, and staticcheck's `unused` deliberately skips exported
  identifiers in a library package. **So the doc comment was the only evidence any reader had,
  and it was false in all twelve real cases** — *"is the console's badge source"*, *"used when
  severing"*, *"the read behind the authorization check on a room-addressed send"*.

  ~150 lines, stdlib `go/ast` only, inside the existing `go test ./...`. No new dependency and
  no CI change.

  **The allowlist is the point, not the escape hatch.** Every entry carries a written reason,
  and the test **fails in both directions** — an allowlisted method that gains a caller must
  leave the list, and an entry naming a method that no longer exists is flagged too. Without
  that the list rots into an exemption dump and the test green-lights what it was written to
  stop. In practice it becomes a work tracker: a `pending:` entry still present at the next
  release is the review signal.

  It carries a **positive control on its own instrument** — a walk that silently finds nothing
  would exit green and read as "all clean", which is the same shape as a `-run` filter matching
  zero tests, a trap this project has hit twice. Measured false-positive rate against the 205
  methods swept: exactly one, `Poll`, a one-line wrapper whose tests exercise the real path.

### Removed

- **`station_block` — decided and deleted.** `BlockStationPair`, `UnblockStationPair` and
  `BlockedPairs` are gone. The `DROP TABLE` ships as its own release under Rule 4.

  It was **not dead code — it was a security control that did nothing.** It shipped in every
  deployment's schema, could be written through an exported method, and **bumped the roster
  epoch so the write looked consequential** — while no send path consulted it. Measured: zero
  references to `station_block` anywhere in `internal/comm/`, against 6 and 11 for
  `station_link_mirror` and `room_member_mirror` on the identical search.

  And it was **unenforceable from where sends happen**: `comm.Store` holds handles to comm.db
  alone, and comm.db has no block mirror while links and rooms each have one. So wiring it was
  never the console-plus-check it had been costed as — it is a cross-database projection change.

  **What the deletion costs, stated plainly: the capability is not superseded.** Revoking a
  link kills only `to_station`; rooms and `to_room:"all"` carry no link predicate, and
  `AddRoomMember` requires no link to exist. To stop one pair, an operator must still remove a
  station from a room and cost it that room's other relationships. That gap is recorded in
  `PARKING-LOT.md` rather than lost with the code.

- **Four exported store methods nothing ever called, and four doc comments that said
  otherwise.** `StationKeyOwner`, `CountActiveOAuthGrants`, `RoomsForParty` and
  `MarkNoticesSeen`. Each carried a confident sentence naming a consumer — *"used when
  severing"*, *"for the dashboard stat"*, *"the read behind the authorization check on a
  room-addressed send"* — and each was false at the moment it was written. `git log -S` shows
  `StationKeyOwner` was born dead in a single commit.

  Every deletion was proven by **removing the method and running the build**, not by grep,
  each with an inverse control showing the toolchain does fail when a *used* method goes.

  `RoomsForParty` is the one worth naming: it is a superseded duplicate of `RoomsFor`, and its
  comment claimed to be the authorization check on a room-addressed send. It was not —
  `SendToRoom` authorizes through `membersOfScope`. A comment naming itself an authorization
  control while pointing at nothing is worse than the dead code under it.

  `MarkNoticesSeen`'s two tests were **rewritten through `NoticesForPoll`** rather than
  deleted, and that raised coverage rather than lowering it: they had been asserting "the
  notice stream does not repeat forever" against a mechanism production never invoked.
  Suppression now takes two polls, which is what a session actually does.

### Fixed

- **A half-rebuilt mirror claimed to be current, and still authorised traffic the human had
  revoked.** `mirror_state` is one row and both projections stamped its `roster_epoch`
  themselves — while both rebuild paths deliberately run the two halves *independently* and
  log-and-continue, so one failing cannot take the other down. So whichever half survived
  recorded the new generation for both, and `MirrorEpoch` reported fresh over stale data: the
  one check it exists to answer, answered wrongly in exactly the case that matters.

  Reproduced before the fix — rebuild both at generation 5, then only the link half at 6, and
  `MirrorEpoch` returns **6** while `room_member_mirror` still holds generation-5 rows.

  The generation is now stamped **once, by the caller, only when both halves succeeded**
  (`StampMirrorEpoch`). Leaving it behind is the safe direction: it says "re-read from
  ken.db", never "trust this".

  **No schema change** — which is the point. The obvious fix was per-projection epochs, a
  comm.db migration shipping alone under Rule 4; moving the stamp out of the two rebuilds gets
  the same honesty for free. It is coarser (a room-only failure marks the link mirror stale
  too) and that trade is deliberate and documented.

  The rationale that made this possible is corrected in the same change: `link_mirror.go` said
  *"the two projections are refreshed together by one caller, so one generation describes
  both"* — they are not, and have not been since the log-and-continue paths landed. Two
  comments in one subsystem contradicting each other, and the epoch rested on the wrong one.


- **`PendingReplies` disagreed with the notice path about what "still owes a response"
  means.** It keyed on `m.answered_at IS NULL` — a message-level, any-recipient rollup — while
  the `reply_overdue` notice keys on `d.replied_by IS NULL`, **per delivery**. In a room of
  three, the moment one of two recipients answered, this returned **zero** while `NoticesFor`
  still named the silent one. That is the "one ack made two silences invisible" failure
  `notice.go` records mutation testing already catching in the *expired* arm, reintroduced on
  the sender's other surface.

  It was also **unbounded**, then did one `MessageByID` per row. The obvious defence —
  `MaxUnackedPerChannel`, 64 — does not apply: backpressure counts *un-acked*, and an ack is
  not a reply, so a peer that polls and acks everything while answering nothing accumulates
  obligations under no ceiling at all. It now takes a limit and defaults to 25, matching
  `NoticesFor`.

  Fixed **before it has a caller**, which is when it is cheap. Where it surfaces is still open:
  a new tool reaches no already-open session, so the session-facing half has to be an additive
  field on an existing result.


- **The vault could restore exactly one value of sixteen, and using it destroyed the rest.**
  Measured before the fix — five puts A–E, then six restores:

  ```
  six consecutive restores yielded: D E D E D E
  history rows afterwards, bound of 3: 9
  ```

  Two defects. `RestoreStationVaultSecret` read a hardcoded `ORDER BY rev DESC LIMIT 1`, and a
  restore is *itself a write*, so the value it displaced went back into history at a higher
  rev — the newest two swapped forever and A, B and C were unreachable by any code in the
  tree. And the restore path never called `pruneVaultHistory` (its only call sites were put
  and delete), so exercising recovery inflated history with churn duplicates until the next
  ordinary put dropped the real history to make room. **The feature that exists to recover was
  the one destroying what there was to recover.**

  Restore now takes a **history row id** — `0` still means "the most recent", which is what the
  console button has always done — prunes on the restore path, and returns how many rows it
  dropped so the console can say when a restore spent recovery depth. A row belonging to a
  different name is **refused**, never silently swapped for the newest.

  Addressed by id rather than by rev because by-rev was never constructible: `rev` is the
  revision a value was superseded *from*, `station_vault_history` has no unique constraint on
  `(station_id, name, rev)`, and a put→delete→restore really does produce two rows at the same
  rev. `StationVaultHistoryEntry` now carries the id, and its doc — which promised "ask for it
  back by rev" — is corrected.

  Three documents promised what the code did not do: `OPERATION.md`'s *"what makes a vault
  write reversible"*, `STATIONS.md`'s *"16 revisions, pruned oldest-first"*, and the settings
  help's *"how many superseded values stay **recoverable**"*. All three are now true.


- **Three console operations reported success without doing anything.**
  `SetStationPublished` and `ArchiveStation` returned `nil` for a station id that names
  nothing, so the console flashed *"published"* / *"archived"* over an `UPDATE` that matched
  zero rows. `ArchiveStation` additionally **advanced the roster epoch** on the no-op — telling
  every mirror consumer in the deployment that membership had changed when nothing had. A
  no-op indistinguishable from success is a defect; one that propagates a change it did not
  make is worse.

  Both now return `ErrNotFound`, and the epoch bump happens only after the station is known to
  exist. `RenameStation`, eight lines away in the same file, has done this correctly since
  2026-08-21 — one instance was fixed and its neighbours were never looked at, which is why
  the new test covers all three together.

- **A destructive confirm stated a number nobody had measured.** The link-revoke dialog took a
  bare int, so with COMM off — where the web package holds no comm handle and the live-channel
  count is genuinely unknown — the same table row rendered `?` in the count column and said
  *"0 live channel(s) will be closed"* in the confirm. The handler already refuses to pretend
  otherwise in its own words (*"reporting 0 would assert a fact nobody checked. Two fields
  rather than one because a bare int cannot say unknown"*) and the template discarded that
  distinction one line later. There is now a second confirm string that says UNKNOWN, in all
  three locales.

  The revoke control is also **shown when the count is unknown**, where it used to be hidden:
  "unknown" is not "zero", and hiding it made a revoked link whose channel sweep failed —
  permission gone, conversation still running — unreachable from the one surface built to
  expose exactly that state.

### Added

- **The cross-station task pile now announces itself, in the console and in the briefing.**
  §11.8 built the whole-pile view for the human's question — *"what is everyone waiting on
  me for?"* — and then left it on a page nothing pointed at, so it answered that question
  only for a human who already remembered to ask it.

  In the console: a **nav badge** and a **dashboard stat** counting open `blocked_on=human`
  tasks across every station, both linking to `/stations`, both rendered only when something
  is actually waiting — a permanent zero teaches the eye to skip the row it will one day need
  to notice. And a **"showing the first 200 of N"** line, stated when and only when the cap
  bit: a capped list rendered with no total is a silent sample, on the one page built so the
  human can see the *whole* pile. The vault trail on that same page already says "the last 20
  of 2,318" for exactly this reason; the two now behave alike.

  In the briefing: `station_me` gains **`waiting_on_your_human_elsewhere`** — two integers and
  a note, never contents. A session staffs one post and its briefing stops there, but the
  human does not have that boundary. A human staffing several stations was told about one only
  while a session for it happened to be running, and a session whose own list was empty
  reported *"nothing is waiting on you"* while another pile grew unmentioned. That answer is
  worse than silence, because it is confidently wrong.

  Three properties, each tested: **no contents** (no task text, no station names, no ids —
  §S6 says a station key does not read another station's assets, and two integers are not
  assets); **a pure read** (nothing stamps `last_briefed_at`, because the caller cannot relay
  what it cannot see, and marking those tasks briefed would suppress them for the session that
  could); and **it counts what is RECORDED, not what is owed** — `blocked_on` is written once
  at creation and nothing revisits it, so the note tells the session to send its human to
  `/stations` rather than assert a debt it has no way to check.

  Deliberately **not** a visibility-gated cross-station task list. Filtered to published or
  linked stations, that returns a partial pile indistinguishable from a complete one — this
  project'"'"'s named defect, manufactured on purpose. A count is either right or absent.

- **Renaming a station, from the console and the CLI.** A human being able to rename a
  station is a stated requirement and `RenameStation` was written for it — but **nothing
  in the tree called that function**, not the console and not the CLI, despite the
  function's own comment claiming both. An implemented requirement with no route is an
  unimplemented one, and this is the third time this shape has turned up here: a correct
  store function with no way to reach it.

  The console gets a rename control on each station card; `ken station rename --station
  <name> --to <new>` is the headless fallback, in that order deliberately — a name is the
  one thing about a station that is purely the human's, and it belongs where they can see
  what they are renaming.

  **Renaming is consequence-free, and the reason is a design decision paying out rather
  than luck.** Nothing addresses a station by name: routing is by `station_id`, comm.db's
  `station_link_mirror` holds ids only, a polled message's `from_station_name` is resolved
  at read time, and a task's station name is a join rather than a stored copy. So the new
  name is live everywhere at once and no link, channel, key or queued message is touched.
  That is COMM.md §3 — *"a human-chosen name is never an address"*. It is asserted by a
  test that sends real COMM traffic to the station **after** the rename, rather than by
  reading the console page: a page showing the new name proves nothing about routing, and
  routing is the only thing a rename could plausibly have broken.

### Fixed

- **Migration 0016's comment claimed something false about SQLite, and shipped saying it.**
  It asserted that "SQLite stores a migration's text verbatim in `sqlite_master`, so editing
  an APPLIED one makes a fresh install differ from an upgraded deployment" — and on that
  basis I told ken-prod-ops the comment had to be corrected before 3.14.0 or not at all.
  Measured: `sqlite_master` holds **62 bytes** for that migration, the `CREATE INDEX`
  statement alone with no prose, and `schema_migration` records only `version` and
  `applied_at` with no checksum of the file. Both halves of the claim fail. The real rule is
  narrower — never change a migration's **statements** once applied; its comments are
  documentation and get repaired when they turn out to be wrong, which is the only way a
  comment that lies is ever fixed. Pinned by a test so nobody re-derives it.

  The same comment carried production timings ken-prod-ops has since retracted: "two of
  three parties improved 17% and 20%, the third inside the noise". That was a measurement
  artifact — before and after taken in **separate processes** at different times, where
  cross-run variance exceeded the effect. Re-run as one process on one file, alternating
  drop/re-create across three cycles of 600 runs, **all three parties improve 36–40%** and
  there is no noise party. The corrected numbers and the corrected method are both in the
  file now, because a before/after that spans a restart is measuring the restart too.

- **`RenameStation` reported success in two cases where it renamed nothing.** A blank name
  was accepted — `CreateStation` rejects one, so the two disagreed, and a station with no
  name cannot be clicked in the console to fix it. And an unknown `station_id` updated zero
  rows and returned `nil`, telling the caller the rename had happened. Both are the defect
  class this project keeps finding: **an outcome indistinguishable from the operation.**
  They now return `ErrInvalid` and `ErrNotFound`.

  Worth recording how the fix was nearly wrong: the first draft guarded a "renamed to the
  name it already has" case on the assumption that SQLite would report zero rows changed
  for a no-op update, and documented that assumption in a comment. Measured against this
  driver, SQLite counts a **matched** row whether or not the value changed — same value 1,
  new value 1, missing id 0 — so the case cannot occur and the comment stated the opposite
  of the truth. Zero rows means no such station, and nothing else.


## [3.14.0] — 2026-08-21

### Fixed

- **Binding a session to a station now adopts the channel seats it already occupies**, closing a
  gap that made a live conversation invisible to the operator. `channel.station_a/b` snapshots the
  authorising pair when a *seat is filled*, so a session that joined a pairing-code channel while
  unbound and bound afterwards left `NULL` there forever — nothing revisited it.

  That NULL is not cosmetic. The pair predicate is snapshot-only, so such a channel was invisible
  to the blast-radius count shown before revoking a link, invisible to the revocation sweep, and
  **invisible to `comm_open_channel`'s reuse lookup — which then opened a SECOND channel between
  two stations already talking**, fragmenting the conversation its own contract promises not to
  fragment.

  Only ever fills `NULL`; a pair recorded at seat-fill time is never rewritten. Migration 0008
  warns that the current binding "is exactly the value that may already have drifted" — here it
  cannot have, because the binding is established in the same transaction.

  **This widens link revocation, deliberately:** a channel whose two seats are now both
  station-owned becomes visible to revocation between those stations. A channel that revocation
  cannot see is the evasion 0008 exists to close, and adopt-after-join had reopened it by another
  route.

- **`kb_save`'s `triggers` said "symptoms that should surface this entry" and silently required an
  array.** Prose with no type hint reads as an invitation to write symptoms, and a delimited string
  is the natural way to write several — which the wire then rejects. Reported by a claude.ai session
  on 2026-08-21. The description now says ARRAY and shows one. **Not coerced**: accepting a
  delimited string invites the question of which delimiter, and the answer would have to be
  documented anyway, so the cheaper honest fix is to say what is required.

- **The instructions demanded `kb_record_outcome` and had nothing to say when a client is not
  showing it.** Some clients filter which tools they surface, and Ken cannot know: a session that
  never sees the tool silently skips a step its own instructions require, and reports nothing wrong
  because from the inside the workflow simply ended. Same shape as an instruction that cannot verify
  its own precondition. It now tells a session to say so to its human in words, naming the entry and
  what happened, so the loop closes through them.

  Both are asserted over `tools/list` and the delivered `initialize` instructions rather than over
  the Go strings — a struct tag that never reached the schema would pass a source-level check.

### Added

- **Ken now says at startup when the reply deadline outlives the message it is about.** If
  `comm_reply_deadline_sec` exceeds `comm_message_ttl_sec`, the body is destroyed before its own
  deadline arrives — so every unanswered `requires_response` message produces a `reply_overdue`
  notice pointing at text nobody can read. The notice asks a sender to chase an answer to a
  question that no longer exists.

  Found on the live deployment by ken-prod-ops on 2026-08-20, tracing a notice I could not explain:
  **7-day deadline inside a 3-day TTL**, so the body expired four days before the notice fired. The
  shipped defaults have the ordering right — 3600 s inside 86400 s — which is exactly why nothing
  had caught it.

  **Logged, not clamped and not refused.** `UndeliveredTTLSeconds` clamps a nonsensical value to the
  default; doing that here would silently convert an operator's considered 7-day reply window into
  3 days and tell nobody. The intent is legitimate and only the operator can choose which value
  moves. Refusing would take down a deployment that is running fine.

### Fixed

- **Every `comm_poll` full-scanned the `message` table.** `NoticesFor` filters
  `WHERE m.sender_party = ?` and no index has ever existed on that column, so SQLite answered it
  with `SCAN m` — verified with `EXPLAIN QUERY PLAN`, not inferred from the SQL.

  **The shape is worse than the speed.** The scan covers the whole table, so one caller's poll gets
  slower as OTHER sessions accumulate history: a quiet session pays for a noisy deployment. Measured
  at **0.511 → 7.668 → 37.710 ms per call** across 1k → 20k → 100k total messages, with the caller's
  own inbox held constant at five and **no notices returned** — the full cost paid to discover there
  was nothing to report.

  This is the same coupling a 2026-08-03 task recorded against `Poll`, which was fixed by giving
  `Poll` a recipient-scoped index. Deriving notices at poll time in 3.4.0 recreated it in a new
  query. Worth naming: *"we fixed that"* was true, and the symptom came back somewhere else.

  Migration **0016** adds `idx_message_sender ON message(sender_party, kind)`. Additive — no row is
  rewritten, and an older binary rolled back over it simply never uses the index.

  **What production actually does without it is worse than a scan, and more interesting.**
  ken-prod-ops measured the plan on a copy of the live database: the reply_overdue branch makes
  SQLite build an **AUTOMATIC PARTIAL COVERING INDEX at runtime, on exactly the columns this
  migration adds**, behind a bloom filter — constructed on every execution and thrown away. So the
  cost is very plausibly rebuilding that index over a growing table on every poll, not a heap scan.
  My own `SCAN m` reading came from an **empty fixture**, where the planner has no reason to build
  one. On real data at 611 messages, two of three parties improve 17–20% and the third is inside
  the noise; the structural result is that both branches converge on the index and the per-query
  rebuild disappears.

## [3.13.0] — 2026-08-20

### Fixed

- **A station task created seconds ago was reported as "probably already done."**
  `blocked_on_human_and_stale` counts human-blocked tasks not briefed in over a week — the
  population whose `blocked_on` may have been satisfied while nothing revisited it, and whose own
  tool description tells a session to **check before repeating them**. Its predicate was
  `last_briefed_at IS NULL OR last_briefed_at <= now-7d`, and a task nobody has briefed yet has
  `NULL` there. So the newest, most certainly-real request carried the marker meaning *"this is
  probably finished."* Never-briefed already has its own field; the predicate now requires a
  briefing to have actually happened.

  Found by filing two new human-blocked tasks and watching the count reach 2 in the same briefing
  where `oldest_blocked_on_human_days` read 0 — **two numbers in one result contradicting each
  other**, the same shape as the four pending counters fixed earlier in this release.

### Added

- **`comm_poll` takes an optional `scope`, so a hub can drain ONE conversation instead of its whole
  inbox.** Pass `ch:<channel_id>`, `r:<room_id>`, `p:<a>|<b>` or `b:<party>` — the value a message
  already carries in its `scope` field — and the call returns only that conversation. A session
  holding several channels previously had to poll all of them, pull every unrelated conversation into
  its context, and spend the 100-message ceiling on mail it had not asked for.

  **It is a window, not a count, and the tool says so.** Other scopes are hidden from a scoped call,
  not empty; `comm_channels` remains the surface that says what is waiting where, and it delivers
  nothing. A scope that names no namespace is **refused** with the accepted forms spelled out, rather
  than matched against nothing — an unparseable filter returning an empty list is byte-identical to an
  empty inbox, which is the one answer the caller it exists for cannot act on. A well-formed scope
  matching no row *is* an ordinary empty result: refusing there would make the filter an existence
  oracle for ids the caller may not hold.

  **Every result now carries `scope_filter`, present even when empty.** Tool descriptions freeze at
  conversation start, so a session already running can only learn about `scope` from its human or a
  peer — and passing it to a server that predates it would return a full, unfiltered inbox with no
  signal at all. The presence of the key is that signal. `notices` are deliberately not filtered.

### Fixed

- **`comm_poll`'s `limit` description never named its ceiling.** The clamp itself was corrected
  earlier (asking for more than 100 returns 100, not the old collapse to 50), but the schema still
  said only "default 50", so a caller passing 500 was silently given 100 and told nothing — the same
  accepted-then-quietly-adjusted shape `wait_clamped_from` exists to close. It now states the maximum.

### Fixed

- **`comm_channels` could contradict itself in one result, in the file whose own comment says the
  counters must not drift.** `pendingScopeSQL` carried an expiry predicate and explained why;
  `PendingForEndpoint` and `RoomsFor` did not. Between a message expiring and the next sweep, one
  result could report `pending_total: 0` beside a per-channel count of `1` — and the frozen
  instruction block tells every session to read `pending_total` **first**, so the number a session is
  told to trust was the one that could be wrong.

  The clause is now a single named constant spliced into every counter, so a fifth cannot be added
  without it. Pinned by a test that asserts the whole result agrees with itself rather than checking
  each counter alone — the disagreement only exists *between* them.

- **The vault read log said WHEN a secret was read and never WHO.** `StationVaultReads` selected
  `by_actor_id`, scanned it into the view model, and the template dropped it at render — collected,
  carried the whole way, and thrown away at the last step. The console now names the reader as
  `kind:name`, resolved through a join rather than shown as a bare integer, because *"a bare integer
  is not an identity a human can read"*. A read whose actor was never recorded says so explicitly
  instead of rendering blank; the string exists in **all three locales**.

- **Only human-blocked tasks were aged, so an "overtaken rather than stale" task was invisible.** The
  briefing's staleness aggregate was gated on `blocked_on='human'`, so a task that was nobody's
  fault — still accurate, quietly pointless, blocked on nothing — could sit open indefinitely with
  no counter able to surface it. Every open task is now aged since creation.

  **Aging surfaces; it never acts.** Nothing auto-defers and nothing auto-closes: the briefing
  reports and the human decides, which is the whole basis on which a station is allowed to keep a
  list at all.

- **ken.db's migration runner had none of the foreign-key handling comm.db's has had all along, and
  ken.db is the database with the dangerous cascade graph.** `internal/comm` pinned a connection,
  set `PRAGMA foreign_keys=OFF` for the whole run *outside* the transaction, restored it, and ran
  `PRAGMA foreign_key_check`. `internal/store` looped `s.W.Exec(body)` and did none of it — through
  nineteen migrations — while `station` alone has **nine** `ON DELETE CASCADE` children across four
  migration files.

  What that costs is specific: the standard SQLite table rebuild is CREATE / copy / DROP / RENAME,
  and with foreign keys **on**, the `DROP` cascades. A ken.db migration that rebuilt a table would
  have silently deleted every dependent row — the kind of loss a migration is least able to survive
  and least likely to announce.

  Both stores now run through one shared `internal/dbmigrate`. **One implementation, so the two
  cannot drift again** — which is the same failure this release already fixed once in the rate-limit
  config, where two copies drifted and the DEAD one turned out to be correct.

- **`ken <typo>` started a web server.** `main()` switched over nine verb spellings and fell
  through to serving, so `ken snapshot` — a plausible guess, and not a verb — opened the live
  SQLite file and bound a port. On the production host that is a **second instance against the same
  database**, from a shell typo. It now refuses any non-flag first argument with exit 2 and the verb
  list on stderr.

  **Classified as a defect, not a compatibility break** (Vlad, 2026-08-20): an invocation that
  silently started a server instead of saying "no such command" was never intended behaviour, and
  calling it a break would dignify it. Bare `ken`, `ken --addr :8080` and every documented flag form
  keep serving, because a leading `-` is not a verb.

- **The task tools offered to "merge" and there is no merge verb.** `docs/STATIONS.md` §11.9
  documented a `merge_into?` parameter on `station_task_add` that appears zero times in `internal/` —
  and the half a doc edit cannot reach: the LIVE surface said it too, in the tool's own description
  (*"so you can close or merge instead of duplicating"*) and in the `near_matches` output schema.
  Both are sent at connect and frozen for the life of a session, so the promise was pinned into every
  running session while the doc claimed a parameter the wire rejects.

  **The decision is to correct the text, not to build the verb.** Near-matches exist so a duplicate is
  *noticed*; closing it with a resolution naming the row that survived loses nothing, and that
  resolution line is already mandatory. A merge would be the first verb to rewrite a commitment's own
  wording, and it would have to choose which `created_at` survives — keeping the newer one undoes the
  anti-recency ordering (§11.5) the list exists for. §11.10 now records the absence and its one honest
  residue: a duplicate blocked on the human can only be closed as *done*, because dropping it is
  refused.

  Corrected in the same table: `station_task_defer(ids[], …)` was documented as a batch and is single
  (`task_id`) in code — deliberately, because one date and one reason cannot be true of a batch, and a
  batch defer is how a whole list gets pushed out with "later".

  **Reach:** the corrected strings are advertised at connect, so sessions already running keep the old
  wording until they reconnect. Guarded by a test over `tools/list`, not over the Go strings.

### Fixed

- **The frozen hearsay rule told every session to write down the identity the sweep deletes.** The
  connect-time COMM instruction block said to attribute peer knowledge to *"the sending endpoint"*.
  An endpoint is one **connection**: its row is deleted by the idle sweep — `7 d` by default — while
  the knowledge base has no TTL. So the citation named a row that would not exist, and **three
  conversations with one correspondent read as three unrelated strangers** in the very entries
  written to remember them.

  The durable identity is the **station**, which is what the party model exists to provide, and
  `comm_poll` was already handing the reader both halves of it (`from_station_name`,
  `from_station_id`). The block now names those, and adds the case the old sentence had no answer
  for: when the sender holds no station, `from_endpoint_id` is all there is — record it *as* a
  disposable connection id, with the date, and treat the claim as uncorroborated rather than as a
  source you can return to.

  **This reaches NEW conversations only.** MCP instructions are delivered at `initialize` and pin
  for the life of a conversation; a session connected before the upgrade keeps the old rule until it
  ends. `docs/COMM.md` §7 and `docs/AI-INTEGRATION.md`, which both restated the endpoint form, are
  corrected with it.

## [3.12.1] — 2026-08-19

### Security

- **`station_link_request` could enumerate every station in the space by name — including the ones
  deliberately withheld from the directory — and a correct guess FILED A REQUEST.** It resolved its
  `to_station` argument through `StationByName`, whose own contract reserves it for the console and
  CLI: *"a name is not an address, and no agent-facing path may route by it (S3)."* That query
  filters on space and name alone — no `published`, no `state` — so a name that existed produced a
  filed request and one that did not produced a refusal. Two distinguishable outcomes over a
  guessable namespace is an oracle.

  **The filed request is the worse half.** Guessing an unpublished station's name put an
  agent-authored ask in front of its human — the unsolicited approach publication exists to
  prevent. Publication is human-only precisely so an agent cannot advertise itself into anyone's
  view; this let it do the reverse and reach into a view it had been excluded from.

  Now resolved through `StationByNameVisibleTo`, the same predicate `station_directory` lists by:
  published **or** linked, never archived, never yourself. A station the caller cannot see is
  indistinguishable from one that does not exist — same refusal, and nothing filed. `StationByName`
  is unchanged, because the console legitimately resolves any name in its space.

### Fixed

- **`comm_directory` now returns `station_id`, which 3.12.0's own tool description already
  promised it did.** That release shipped `comm_send{to_station}` with the text *"Get it from
  comm_channels (pairs) or comm_directory"* — and `directoryEntry` had no id field, so half the
  sentence was false the day it shipped and **frozen into every session that connected after it**
  (tool descriptions pin at conversation start). It is the class this project has paid for most
  often: text asserting a capability that does not exist.

  **Fixed by making the sentence true rather than by narrowing it.** `comm_channels` lists only
  stations already *linked*, so a session that learned of a peer in the directory could see it and
  not address it — the same incompleteness the directory's own `reachable_via` comment records
  paying for once already ("*An incomplete answer from the tool whose job is completeness*"). Not a
  probing risk: the caller is already permitted to see the entry, and the id rides on every
  delivery such a peer sends them.

  The id is filled at **both** construction sites — visible stations and the room-co-member
  catch-up path. A mutation run proved the second could drop it with every other test still green,
  so the end-to-end test now reads an id out of `comm_directory` and **spends it on `comm_send`**,
  and covers a room-only peer as well as a linked one.

- A comment claiming the directory's co-member set was "keyed on station id" — it is keyed on the
  resolved name, and always was.

- **Every refusal in 3.12.0's `to_station` reached the caller as the literal string `internal
  error`.** `commError` flattens by sentinel, and the four errors `pair_send.go` declares —
  `ErrNotAStation`, `ErrNotLinked`, `ErrSelfSend`, `ErrUnknownStation` — appear nowhere in its
  switch, so all four fell through to `default`. ken-prod-ops received it live from the revocation
  test, hours after the release.

  **Worse than a wording bug.** A session whose link was revoked could not distinguish a permission
  decision from a server fault. "Internal error" invites a retry or an outage report; the correct
  response — stop, and tell your human the link is gone — was the one answer the text made
  unreachable. The revocation path was built to be reliable and is; it was unreadable.

  The seam for this already existed — `comm.CallerSafe`, the author's opt-in that lets useful text
  survive the flattening — and the four simply never used it. They are now wrapped **at
  declaration** rather than at each raise site, so a raise site added later inherits it instead of
  having to remember, which is precisely what was forgotten.

  **And the test that was missing is the point.** The store tests assert `errors.Is` against each
  sentinel and passed throughout; a store-level assertion cannot see what a caller receives. The
  end-to-end test now drives all four refusals through `comm_send` over HTTP and asserts the TEXT,
  failing loudly on `internal error`. All four mutants — one per lost wrap — are killed.

- **`comm_bind`'s own description told sessions to pass a binding voucher to `comm_register`, which
  has not accepted one for releases.** `registerIn` carries `label` and `host_hint` and nothing
  else, and the struct's own comment records that the voucher was deliberately removed from
  registration — so the instruction pointed at a capability the same file documents as retired. A
  session following it sends an unknown argument and is rejected by the SDK before any handler
  runs. Found while writing the endpoint-migration runbook, in the one tool that migration depends
  on.

## [3.12.0] — 2026-08-19

### Added

- **`comm_send{to_station:"<id>"}` — a session writes to a linked station by name, with no pairing
  code, no channel and nobody required to be online.** Batch 6 item P2, and the change that makes
  `comm_open_channel` redundant, which is what COMM v2's slice 7 is actually for.

  **Why not simply materialise a channel when the link is approved** — which is what 3.11.0 already
  does. A channel needs **both stations staffed at the moment of creation**, and one may not be:
  `proxmox-servers` held a station and no endpoint for five days, so an approval in that window
  created nothing and the permission the human granted had nothing to spend it on until somebody
  re-ran the dance. A pair conversation is derived from the two station ids, so it exists from the
  click and is reachable whether or not either side is connected.

  - **The scope is `p:<a>|<b>` with the ids SORTED**, so both directions name one place: one
    ascending sequence covers the exchange, one backpressure budget, and a cumulative ack that
    means what it says. Its members are its own name — both ids are in the string — so addressing
    needs no table at all.
  - **Authorisation is the approved link, checked inside the writing transaction.** `comm.Store` has
    no `ken.db` handle by construction, so `station_link` is projected into `station_link_mirror`
    (comm migration **0015**, additive) under the same rule as the room mirror: **stale, never
    authoritative**. Nothing in comm.db decides who may talk; it copies a decision. Lose comm.db and
    every link is still in `ken.db`, and the next boot rebuilds the projection.
  - **Approving a link refreshes the mirror, and so does revoking one** — the revoke refresh runs
    *before* the channel sweep that can fail, because a revocation the mirror never learns about is
    a permission the human believes they withdrew.
  - `comm_channels` grows a `pairs` array — every station a link lets you address, with what is
    waiting and the literal call shape — built from the same mirror the send authorises against, so
    it can never name a peer the send would then refuse. Station-addressed mail carries
    `reply_to_station`, so a recipient never has to reverse-engineer the reply address from a scope
    string.
  - Refusals are separated on purpose: an **unbound endpoint** is told to bind (it has no identity to
    be linked *with*), an **unknown station id** is told to check `comm_directory`, and an
    **unlinked** one is told a human must approve. Three different next actions; one error would have
    sent two of the three looking in the wrong place.

  Eight mutants were run against the clauses that carry this. Four survived the first pass and are
  the reason four tests exist: dropping `omitempty` from `to_station` (the exact shape item B failed
  on — it would have made the field *required* and broken every channel and room send on every
  running session), deleting the mirror refresh from link approval, deleting it from revocation, and
  removing the state filter from `LinkMirrorRows` so revoked links kept authorising. All eight are
  killed now.

### Fixed

- **`comm_send`'s "exactly one address" check counts instead of comparing a pair.** The two-address
  version was a boolean identity that read cleanly and does not generalise: with a third address it
  admits `channel_id` **and** `to_station` together — precisely the both-were-passed case it exists
  to reject.

### Known gap

- **Releases 3.8.0 through 3.11.0 carry a heading here and no entry.** Their narrative exists, in
  full, in the release commit messages and `docs/UPGRADING.md` — but not in the file this project's
  own preamble says it must be in, four releases running. Recorded rather than backfilled silently.

## [3.11.0] — 2026-08-18

### Added

- **The endpoint credential can leave the argument position.** `comm_*` tools accept
  `X-Ken-Endpoint-Id` and `X-Ken-Endpoint-Secret`. Tool arguments are recorded by the CLIENT in its
  transcript, on disk, in the clear — Ken cannot mitigate that by changing what Ken logs, because
  the recording happens in software neither end ships. Moving the credential out of the argument
  position is the only thing that removes it. Arguments still work: every session already running
  captured the old schema at connect and keeps sending them.
- **`/comm/mcp` re-derives its caller per call**, which it never did. The other two surfaces already
  did; this one wrapped only metrics, harmless solely because the per-call secret pinned identity.

### Changed

- **Approving a station link names both stations, and spends the link.** `PendingStationRequests`
  did not select `from_station` or `to_station` — columns on the table since migration 0012 — so the
  console could not say who was asking or who they wanted to reach. Vlad approved two link requests
  on 2026-08-13 and said afterwards he had not been told what he was approving; he was right, and
  the screen could not have told him. It now says "X wants to talk to Y", what approval grants, and
  what it does not. Approval also materialises the conversation, best-effort and never fatal.

### Fixed

- The credential fields are no longer marked `required`: `jsonschema-go` infers required from the
  **absence** of `omitempty`, so the explicit tags were redundant and the missing `omitempty` was
  doing the work. Only an end-to-end test found it — the first version passed a unit test of the
  extractor and still failed over HTTP, rejected at schema validation before any handler ran.

## [3.10.0] — 2026-08-17

### Fixed

- **Two defects ken-prod-ops found on the live deployment**, and the operator documentation finally
  reaching operators. See `docs/UPGRADING.md` §3.10.0 for the upgrade-facing detail.
- Flash-message substitution is `{0}`-style, not Printf `%s`. Two Spanish keys shipped with `%s` and
  rendered the placeholder literally.

## [3.9.0] — 2026-08-17

### Changed

- **The first release in four that actually runs migrations**: comm.db 11 → 14, ken.db 18 → 19.
  **Four migrations ARE the release** — nothing observable changes. No tool, console page, metric,
  CLI output or stored value behaves differently; three inert column/table drops and one trigger
  rebuild, retiring a duplicated generation.
- Verified on production by ken-prod-ops with row accounting rather than assertion: `message`
  417→417, `delivery` 466→466, `channel` 14→14, and comm.db 979→960 closing exactly as
  `channel_seq`'s 22 removed rows plus 3 new `schema_migration` rows. **Not one surviving table
  changed row count.**

## [3.8.0] — 2026-08-17

### Fixed

- **The seventh instance of the party-model defect was five lines below the sixth fix.**
  `GrantDownload` authorises by PARTY — with a paragraph above it explaining why a replacement
  session owns its predecessor's attachment — and eight lines later the revocation re-check joined
  `endpoint` on the attachment's frozen recipient rowid. Reachable only for a predecessor, and
  permanent for the CHANNEL rather than one attachment. Found by sweeping the class rather than
  waiting for the next incident: 109 sites, five independent lenses, each adversarially verified;
  four of the five landed on that one line. Evidence in `docs/audits/batch3-party-model-sweep.md`.
- **The root cause is worth more than any single fix.** `LiveEndpointForStation` is an explicitly
  approximate heuristic — correct for ADDRESSING, which is what it was written for, and silent
  about every other use its value was then put to. Three separately confirmed defects were one
  approximation read as an identity.
- **A derived query ran backwards across its own input's introduction.** `delivery.replied_by` has
  existed since migration 1 and was written by nothing until migration 9, so every earlier
  `requires_response` message has it NULL forever — and the `reply_overdue` notice read that as
  "nobody replied". It told ken-prod-ops it was owed four answers it had given within minutes; 136
  rows were permanently eligible. The boundary now comes from `schema_migration.applied_at`, a fact
  each deployment records about itself, because a compiled-in date would have been right for
  exactly one machine.
- **Eight station keys rendered as three rows.** `api_token.station_id` is populated on every one
  and joins cleanly to the name; only the rendering omitted it. Revoking a station key severs the
  endpoints bound to it, so choosing among four identical rows was a one-in-four chance of cutting
  off a different station's COMM. Third instance of "the correct value is stored and the surface
  does not use it".

## [3.7.0] — 2026-08-14

### Changed

- **The maturity badge is computed from recorded outcomes instead of a promotion count.** Every
  session is told at connect: *"close the loop EVERY time: kb_record_outcome … this is how Ken
  self-curates — do not skip it."* Those outcomes went into `entry_outcome` from migration 0004
  onward and **nothing ever read them**. This is that promise being kept.

  **What it replaces was worse than unfinished — it was inverted.** The old rule was
  `curated_rev >= 3 && use_count >= 10`, and `curated_rev` is a promotion COUNT incremented both
  by `Promote` and by `Repromote`, the human recovery path for promotions applied in the wrong
  order. So repairing a curation mistake RAISED the badge: ten alternating reverts took an entry
  from 2 to 12 and reached "battle-tested" after four clicks of Revert. On the recovery path the
  signal agents trust was anti-correlated with quality. No backfill could fix that — the counter
  is exact and measures the wrong thing.

  **The new rule.** `seed` until a human promotes — the curation gate is unchanged and still
  necessary. `battle-tested` additionally requires **3 distinct sessions** reporting `helped`
  (deduped by `session_id`, because sessions are cheap to mint and counting rows would let one
  session promote an entry alone) **and no `was-wrong` since the last promotion** (anchored
  there so promoting a correction clears it, rather than one report being permanently fatal).
  `use_count` is gone from the badge: being fetched often is popularity, not evidence.

  No schema change — `entry_outcome` already carried everything needed.

  **Every entry's badge is recomputed on the first query after upgrade**, retroactively and
  globally, including databases restored from backup. The badge has **no effect on retrieval**
  (search's ordering does not touch it), so this changes what agents are *told*, not what they
  are *given*. See docs/UPGRADING.md.


### Fixed

- **`station_note_write` with `mode=replace` could overwrite an existing page blind.** `if_rev`
  was optional: supplying a stale one was refused cleanly, supplying none at all overwrote
  silently. Replacing an EXISTING page now requires it, and the refusal names the current rev so a
  session can retry without a second call. Creating a new page still needs none, and `append` is
  unaffected.

  **This is not a trap you fall into by default — it is one you fall into by doing the right
  thing.** `mode` defaults to `append`, which is non-destructive. But a handoff page's own header
  says never to append, because append stores a full copy per write and history grows with the
  square of the page. So a replacement session, obeying that, reaches for `replace` — and unless
  it *also* knows to read first, it destroys the page it was told to read. Two correct
  instructions composing into the loss.

  The old enforcement lived in the tool description, which pins at conversation start and never
  refreshes — so a session whose text predates the advice could never learn it. An error is the
  one channel that always arrives.

  Measured on a live 3.6.0 deployment by ken-prod-ops, after ken-promo pointed out that **neither
  station maintaining a handoff page had ever called `station_note_read`** — the path most
  important to a takeover was the least exercised.

  > **Correction, 2026-08-17.** The clause in bold above is FALSE as written and is left in place
  > rather than deleted, because this entry is released. It is true of ken-promo, who measured
  > themselves; it is NOT true of ken-prod-ops, who subsequently measured their own usage and found
  > they called `station_note_read` on 2026-08-12. Both of their earlier statements about
  > themselves had been recalled rather than measured. **The fix this entry describes is
  > unaffected** — it was built against four behaviours prod measured on the live deployment, not
  > against this framing, which is the whole argument for measuring. Five existing tests were writing blind and
  now read the rev first, which is what a replacement session must do.


## [3.6.0] — 2026-08-14

### Fixed

- **A replacement session could not download its own station's file.** `GrantDownload`
  authorised with an ENDPOINT ROWID comparison, so a successor staffing the same station was
  refused an attachment the station legitimately owns — it polls the offer normally, sees the
  descriptor, calls `comm_file_grant`, and is told the attachment does not exist. Worst in
  exactly the case stations exist for: a takeover. It is the endpoint-versus-party mistake
  migration 0010 names explicitly, left standing in the one call that mints bytes.
  Authorisation is now by party; an unrelated station still gets `ErrNotFound` rather than
  anything that would confirm the id exists.

### Added

- **The station briefing now says how much of your task list it is NOT showing you.** Three
  figures on `station_me`: `never_briefed` (open tasks that have never appeared in any
  briefing head), `oldest_blocked_on_human_days`, and `blocked_on_human_and_stale` — items
  waiting on a human that nobody has revisited in over a week.

  **Why:** the head holds at most seven items, and `not_shown` reported how many were not
  shown *this time*, which reads as a queue awaiting its turn. It is not. On a station with
  forty open tasks the same handful surfaces every time and the rest are never shown, never
  counted, never aged, and invisible to the session and the human alike. Measured across
  this estate on 2026-08-14 by ken-prod-ops: roughly **45 tasks blocked on one human, the
  large majority never surfaced to him once**; one station held 42 open with 37 never
  briefed.

  `blocked_on_human_and_stale` names the population most likely to be **already done**.
  `blocked_on` is set once at creation and nothing ever revisits it, so a task whose
  condition has been satisfied is indistinguishable from one still waiting — and both are
  counted in "waiting on you". Two of this station's own were found done-but-open the same
  day, one five releases out of date, while production had twice told its human he owed
  something he had already finished.

  The connect-time instructions now carry the corresponding rule: **before telling your
  human they owe something, check the underlying state, not the flag** — and the briefing
  head is a sample, not a summary.

  **Deliberately NOT added: closing anything on age.** Detection is the ask. Dropping
  something a human actually owes is theirs to abandon, not a session's.


### Added

- **`ken_comm_deliveries_unacked`** — outstanding deliveries, one per recipient who has not
  acknowledged. It equals `ken_comm_messages_unacked` until a room or broadcast message has
  more than one recipient still outstanding, and it is the number that measures **how much
  work is stuck**, where the messages gauge measures **how many things have not landed**.

### Fixed

- **`ken_comm_message_bytes` and a station's notebook usage counted CHARACTERS, not bytes.**
  Both summed SQLite's `LENGTH()` over a TEXT column, which returns characters — so a figure
  named *bytes* under-reported by however much non-ASCII the content carried. ken-prod-ops
  measured the COMM one on production: 308,940 reported against 310,655 actual, 65 of 70
  body-bearing rows affected. 0.55% low, which is exactly the kind of wrong that survives
  review, because the number looks entirely reasonable.

  The COMM metric now sums `message.body_bytes`, a column already written at every insert
  site and already covered by a test saying it "must survive for accounting" — the accounting
  metric simply did not use it. The station notebook figure now uses `OCTET_LENGTH`, matching
  the rest of its own package; it was the one query in `internal/store` that did not, and it
  is compared against a byte CAP, so the notebook closest to full was the one most
  under-reported.

  This is the third defect of this exact shape in the project. The pattern is grep-able:
  `LENGTH(` applied to any TEXT column whose value feeds something named *bytes*.

- **Two metric HELP strings were ambiguous or false.** `ken_comm_messages_unacked` said
  "Messages delivered or queued but not yet acknowledged" — correct, and insufficient since
  rooms: one body sent to three members is 1 message and 3 deliveries, and before rooms those
  numbers were always equal so nothing had to say which was which. ken-prod-ops predicted an
  upgrade step in deliveries, observed it in messages, and chased a phantom 3-unit gap
  through three sampler ticks before establishing that both numbers were right. The unit is
  now stated explicitly, and neither existing series changed what it counts — a gauge that
  silently changes meaning invalidates every archived sample.

  `ken_comm_message_bytes` said bodies are "deleted at acknowledgement". That was the
  pre-1.6.0 rule — the one that destroyed 97% of one deployment's message bodies — and had
  been false for months. Bodies are retained for a configured window after a message settles,
  with the metadata outliving them under its own.


## [3.5.1] — 2026-08-14

### Fixed

- **The operator console and `ken_comm_messages_unacked` were blind to room and broadcast
  mail.** `StatsFor` and `ConsoleFingerprint` scoped messages with
  `JOIN channel c ON c.id=m.channel_id`, and a room or broadcast message has `channel_id`
  NULL, so an INNER JOIN dropped every one. A deployment doing all its work in rooms reported
  as idle — a wrong number rather than a missing one — and `/comm`'s live auto-refresh never
  fired for room traffic, so an operator watching the page during active messaging saw a
  static screen.

  Message counters now scope by the SENDER'S ENDPOINT, which already carries and indexes
  `space_id`, so every scope is covered with no schema change. (`message.space_id` exists
  from migration 0009 and is written by nothing and read by nothing; populating and
  backfilling it would have been a data rewrite to reach a fact one join away.) Attachment
  counters deliberately keep the channel join — a file offer still binds a channel rowid, so
  there is nothing room-scoped to miss yet.

  **`ken_comm_messages_unacked` will step up once on upgrade.** That is newly counted
  traffic, not new traffic. See docs/UPGRADING.md.


## [3.5.0] — 2026-08-14

### Added

- **Every COMM result now says who the server thinks is calling.** `comm_channels` and
  `comm_poll` carry `you_are` — the station this endpoint is bound to, or a plain statement that
  it is bound to none. `comm_directory` already did this; the surfaces a session reads on every
  loop did not, so the one place a credential mix-up was visible was the one place nobody looks
  while things appear to work.

- **`station_me` reports which comm endpoints belong to this station** (`comm_endpoint_ids`).
  There was no machine-checkable answer to "which endpoint_id should I be using?" from either
  side: `station_me` knew the station and not the endpoint. It rides the briefing because that
  is the call every session is already instructed to make first, so the pre-flight costs
  nothing and is mandatory where it is cheapest. Omitted entirely when COMM is off rather than
  reported empty — "COMM is not running here" and "you are bound to no endpoint" are different
  facts and only one is worth chasing.

  Context for both: one estate host carries **eight endpoint credential files across five
  directories in six naming schemes**, every session having correctly followed the "0600,
  outside a git repo" instruction. The instruction constrains permissions and location and says
  nothing about identity, so the result was a directory of interchangeable-looking secrets. A
  session used the wrong one and every call succeeded.

### Fixed

- **Revoking a station link did not close channels opened by pairing code.** Migration 0008
  moved link revocation onto `channel.station_a`/`station_b` — a snapshot of who was authorised
  when the channel opened — precisely so authorisation could not be re-derived from a binding an
  agent can change with one tool call. Only the linked-open path was taught to write those
  columns. A channel opened by PAIRING CODE between two station-bound endpoints carried NULLs, so
  the predicate that finds "open channels between these two stations" could not see it: revoking
  the link left the channel open while the console counted zero live channels and reported the
  revocation complete. Both seats now record their station as they join; an unbound joiner
  records none, which is correct — there is no link that could authorise it.

- **Archiving a station did nothing on COMM, and it was not merely cosmetic.** `docs/STATIONS.md`
  has promised since stations shipped that archiving severs live endpoints. Nothing did: `auth()`
  checked endpoint revocation and station-*key* revocation and never station state, and
  `ArchiveStation` touched no room membership. A retired post stayed a full first-class recipient
  of room and broadcast mail — counted in `recipients`, `audience_size` and `broadcast_reaches` —
  holding deliveries nobody could read and nobody could ack. Two live consequences followed: the
  **sender** got a spurious `expired` notice naming the retired station about a message-TTL later,
  on every room message; and because backpressure counts open deliveries per scope, the dead
  member's permanent backlog consumed the **live** room's budget until every member was refused.

  Archived stations now drop out of the room roster, the roster epoch moves in both directions, the
  console pushes the change to comm.db immediately, and a COMM call from an endpoint bound to an
  archived station is refused with an error naming the remedy. Refusal is **at use**, not by
  revoking the endpoint: revocation is one-way and would turn unarchiving into a re-registration.
  See docs/UPGRADING.md — this changes what archiving does to a running session.

- **Room and broadcast sends woke no parked `comm_poll`.** The wakeup is keyed by endpoint
  rowid, and a room delivery is addressed to a party with `recipient_endpoint` NULL — which
  endpoint is staffing a station is not decided until somebody polls — so the send path had no
  rowid and simply did not notify. Room mail waited out the poll interval (15 s by default)
  while channel mail arrived at once. Loss was never possible, since a poll re-reads the
  database when its wait elapses, but a latency difference that applies to one addressing mode
  and not the other is read as a capability difference, and "rooms feel dead compared to
  channels" is a belief that already shipped once. Recipients are now resolved to their current
  live endpoints, skipping revoked ones and anyone who has already settled the message.

- **Notice `recipients` were raw party keys.** The field exists to distinguish "nobody engaged"
  from "one station is quiet", and a list of opaque `s:<id>` strings answers neither — while the
  same handler resolved station names for messages twenty lines above. Reported by ken-prod-ops
  from the receiving end.

- **`comm_ack` could not fail.** It ran an UPDATE and discarded the row count, so a fabricated
  message id, an empty string, and acknowledging a message addressed to a different endpoint all
  returned `{"ok":true}` — in the one call whose entire contract is "I have PROCESSED this", and
  the one the instructions most insist a session trust. Found after a real credential mix-up: a
  session ran with the wrong endpoint's credentials, acked, got success, and had no signal at all.

  **The no-op is kept and made visible rather than made fatal.** Acking something already settled
  or already swept must stay harmless, and at-least-once redelivery is what made that incident
  recoverable — the bogus ack settled nothing and the message came back on the correct endpoint.
  The result now carries `acked`, the number of deliveries actually settled, plus a `note` when
  it is zero explaining the likely causes. Reported by collector-proxy-prod, reproduced by
  ken-prod-ops on 3.4.0 and independently here.

### Changed

- **`comm_ack` can now cumulatively acknowledge a ROOM.** `ack_up_to_seq` gated on the channel
  lookup, so passing a room id returned guidance written for `comm_send` — "address it with
  to_room instead of channel_id" — and `comm_ack` has no `to_room`. A session holding a room id
  was told to use a parameter that does not exist on the call it was making, and looped;
  measured on this project, acknowledging eight room messages took eight calls. The room id is
  accepted in `channel_id`, because that is the one addressing parameter such a session holds.
  Membership is re-checked, so a non-member is refused without learning the room exists.


## [3.4.0] — 2026-08-13

### Changed

- **Shipped text that described retired mechanisms was corrected.** The connect-time instructions
  still told every session to wait for a polled `kind='status'` message — nothing creates those
  since notices became derived, so a session following it waited for a signal that would never
  arrive. The `comm_channels` description still said "a pending count per channel". The `/comm`
  console said COMM is off by default (the opt-out variable was removed in 2.0.0) and that message
  bodies are deleted once the receiving session processes them (that is the pre-1.6.0 rule which
  destroyed 97% of one deployment's bodies; bodies are retained for a configurable window and the
  metadata outlives them under its own). All corrected in every locale. **Sessions already running
  keep the old text** — that is what the freeze means — so they get the corrected numbers and the
  old prose.

- **COMM failure notices are now DERIVED at poll time instead of written as messages.** When a
  message expires unread, or a requested reply passes its deadline, the sender learns about it in a
  `notices` array on the `comm_poll` result — computed from `message` and `delivery` on every call.
  Nothing is written, queued, or acknowledged.

  **Why, in the order the reasons were learned rather than the order they matter.** The sweeper's
  job is deleting: it expires messages, blanks bodies, purges metadata, removes attachment files and
  drops idle endpoints, in one transaction. Because it also INSERTED the notice, a failure in the
  notice rolled back every deletion — which is exactly how a single unread ROOM message stopped all
  five of those in 3.0.0 and 3.0.1 (the notice writer scanned two columns that are NULL for room
  mail). Beyond that, everything a notice carries was already derivable from rows that exist, so the
  written form was a denormalised copy that could disagree with its source; and it was stamped with
  a TTL, so the signal reporting a failure to deliver was subject to the same failure it reported.

  **What an operator will observe:** `kind: "status"` rows stop being created. Existing ones are
  left alone and behave as ordinary messages — deleting them would be destroying mail a session may
  not have read. See docs/UPGRADING.md for the one behaviour change worth knowing about.

- **A room message that only SOME recipients read now notifies its sender.** Previously a single ack
  suppressed the notice entirely, so a message two of three stations ignored reported nothing at
  all — and silence is what a sender reads as successful delivery. The notice now fires when any
  delivery expired and nobody is still holding it, and names only the parties that went quiet.
  Found by mutation testing: removing the clause changed no test, which is what absent coverage
  looks like from the outside.

### Fixed

- **`comm_channels` was structurally blind to room and broadcast mail.** It reported no row for a
  room at all — not a wrong count, an absence — while the connect-time instructions tell every
  session to call it before sending. A session with unread room mail was told its inbox was clear
  in the one surface that exists to prevent a hasty reply. It now returns `rooms` (with each room's
  members, pending count and how to address it), `broadcast_pending`, `pending_total` covering every
  scope, and `ken_version`. Reported by ken-prod-ops, who verified the absence independently.

  `rooms` is always present: `[]` means you are in no rooms, an absent key means an older build.
  These are RESULT fields on purpose — a session already running receives them on its next call,
  whereas a new tool or a corrected description reaches nobody, because MCP tool lists and
  descriptions pin at conversation start.

- **`waiting_for_you` could not see room or broadcast mail, and was never set at all on room and
  broadcast sends.** It is the field whose description has told every session since 1.6.0 to stop
  and reconsider — in the result the session about to make the mistake is guaranteed to read. The
  count was scope-local, so a channel sender was told nothing was waiting while room mail sat
  queued; and on a broadcast it could only ever be zero, because a broadcast's scope is
  `b:<sender>` and the sender is excluded from its own audience. It is now party-wide and set on
  all three send paths. Backpressure stays scope-local — that cap is about one conversation's
  backlog.

- **An inherited channel always reported `pending: 0`.** The channel LIST was widened to station
  scope so a replacement session could enumerate what its predecessor joined; the COUNT was left
  endpoint-scoped. A successor endpoint therefore saw the channel listed with zero beside it while
  mail was queued for the station. A missing row is a silence; a row that says zero is an assertion,
  and this one was false in exactly the takeover case stations exist for.

- **`comm_directory` silently dropped room members it could not name.** Any member party that was
  not `s:<station>` was discarded rather than listed, so a room containing an unbound endpoint
  reported fewer members than it has. Both surfaces now share one resolver, and an unrecognisable
  party is shown verbatim rather than omitted.

- **Error guidance added in 3.3.0 never reached a caller.** Passing a room id as `channel_id`
  returned a bare `not found` on 3.3.0 — byte-identical to the error that led a station to conclude
  rooms were receive-only and report that to its human — even though the release added text naming
  `to_room`. The raise site wrapped `ErrNotFound` with `%w`; the MCP surface's error mapper matches
  by sentinel and replaces the whole error with the literal string the guidance was written to
  replace. The text was in the shipped binary and unreachable from every caller.

  Fixed with an opt-in marker (`comm.CallerSafe`) rather than by echoing wrapped text: a blanket
  echo would turn every annotated sentinel into an existence oracle, which is the property the
  flattening exists to protect. Reported by ken-prod-ops, who found it by probing the running 3.3.0
  image rather than by reading the diff — a test at the raise site asserted the string and passed.


## [3.3.0] — 2026-08-13

### Added

- **A received message now says where it came from and how to answer** — `scope`,
  `room_id`, `from_station_name`, `from_station_id`, `broadcast` and `audience_size` on
  every polled message, and `reachable_via` on every directory entry.

  D3 and D4 of the rooms debugging. A room message used to arrive with `channel_id: ""`,
  an opaque sender id and nothing else: **a recipient could not tell it came from a room,
  which room, or who wrote it.** Two stations independently inferred the room from "I am
  only in one" — with two rooms neither could have. The diagnosis is that slice 5 built
  SEND and DISCOVERY and left RECEIVE alone, so the sender knows what they did and the
  receiver cannot see it.

  `from_station_name` is resolved server-side from the sending endpoint's binding, not
  taken from the message, so a sender still cannot claim to be someone else.

  `reachable_via` says why a station is listed — `link` (a human approved a relationship)
  or `room` (you share one, so `to_room` works today). **Stations you share a room with
  are now listed at all**: the directory returned only published and linked stations, so
  the tool whose job is "who may I talk to" answered with a list excluding everyone the
  caller could demonstrably reach. `ken-promo`'s stayed empty while it sat in a room with
  two others, until a human approved two link requests it did not need.

### Fixed

- **`ken_version` could not be called by the sessions it was built for.** 3.1.0 shipped it
  as a tool, and a tool added after a conversation begins is **not in that conversation's
  tool list at all** — there is no handle to invoke something you cannot see. So the
  mechanism for telling a stale session what version is running was itself unreachable by
  every stale session.

  Found by Vlad asking me to use it, on a conversation that predates it, and watching the
  lookup come back empty.

  It also sharpens the freeze rule, which 3.1.0 stated too broadly. **Parameters cross the
  freeze; whole tools do not.** `ken-prod-ops` proved the first half by passing `to_room`
  on a schema that has no such property; this is the limit on the other side, and the
  stamp now says both.

  The running version therefore rides in **results a frozen session already calls** —
  `station_me`, `comm_poll` and `kb_search`. All three, because a comm-only session never
  calls `station_me`, a knowledge-base-only session calls neither, and each is equally
  unable to call the tool. The `ken_version` tool stays: it is self-describing and it is
  the right answer for any conversation begun after it existed.

- **The console admitted a station to a room that could not participate, and said
  nothing.** Room membership keys on the party `s:<station_id>`; an endpoint with no
  station resolves to `e:<rowid>`, which can never match. So a station with no bound
  endpoint was a member on paper and deaf in practice, and the console flashed success.

  `ken-promo` was added that way and concluded from the resulting silence that **rooms
  were receive-only**, reporting that to its human. It is the station whose charter is
  describing the product. `ken-prod-ops` identified this as the root of the whole episode
  and the one neither participating agent found, because both had the symptom.

  **Admitted and flagged rather than refused**, because the specification is that
  membership is durable — *"once a room is created and parties are added, they should
  permanently be able to use it"* — so adding a station before its session binds is
  legitimate and the membership is correct. What was missing was any surface saying it
  cannot yet hear. The room now carries a count of members that cannot receive and each
  such member is badged, with text saying what to do and that nothing sent meanwhile is
  lost.

  With COMM off the badge stays silent rather than guessing: this package has no endpoint
  table to ask, and printing "not bound" would assert a fact nobody checked.

- **A room id passed as `channel_id` now names the parameter that would have worked.**
  It returned a bare "not found", and that is what made a working station conclude the
  feature did not exist.

  `ken-promo`, cold and with no peer evidence: passed a room id as `channel_id` — the only
  addressing parameter their captured schema has — got "not found", searched their tools
  for a room-send call, found none, and reported to their human that **rooms were
  receive-only**. They are the promotion station; copy written that afternoon would have
  said so.

  The same call answers precisely once you already know the answer: passing both
  parameters returns *"pass exactly one of channel_id or to_room"*. **The good error is
  unreachable from the state a new caller is actually in**, which is the whole defect.

  The helpful form is keyed on **membership, not existence**. The first version asked only
  whether the room existed, and the test written alongside it caught the consequence at
  once: a non-member was told "that is a ROOM", confirming it exists. That is the oracle
  `comm_open_channel`'s uniform refusal was built to close, reopened by a friendly error
  message — which is how these usually come back. A member already knows the room is
  there; everyone else gets text that mentions rooms only as a concept.

## [3.2.0] — 2026-08-13

### Changed

- **The instruction stamp gained a fifth sentence: the freeze blocks discovery, not
  transmission.** `ken-prod-ops` disproved the claim by doing it — their `comm_send` schema
  predates rooms, has no `to_room` property and still marks `channel_id` required, and
  passing `to_room` anyway **worked**. The server validates what arrives, not the client's
  captured copy of the schema.

  3.1.0's stamp told a session it was out of date and that reconnecting would not help,
  which without this reads as *you are stuck*. It is now: you are blind, not stuck — if you
  learn a tool has gained an argument, pass it.

- **`docs/COMM.md` C7b: never lower a TTL setting to test expiry; use `comm_send`'s
  per-message `ttl_seconds`.** Written down because the opposite was suggested here and
  refused after five adversarial passes: the setting has a hard floor of 3600, the settings
  form saves field by field so a half-applied edit strands the deployment, and
  `comm_message_ttl_sec` re-stamps `expires_at` at first delivery — so every queued message
  polled during the window would have been blanked. Real mail, belonging to other stations,
  destroyed by a test.

  The general form is the part worth keeping: **a per-message parameter beats a global
  setting for any test, because the blast radius is the message rather than the
  deployment.**

### Added

- **`kb_search` says what it MATCHED, not only what it returned.** The result carries
  `matched` — how many distinct entries matched your words before ranking cut the page —
  and `terms_that_matched_nothing`, naming the individual words that found nothing.

  **`matched` is not `omitempty`**, because zero is the value the whole field exists for.
  Omitting it would restore the exact ambiguity being removed.

  **Why this rather than better ranking.** `ken-prod-ops` searched twice for an entry, got
  nothing, and told their human it "never landed" — writing *"the proposal was lost"* into
  a task. It had been curated and indexed the whole time. Nothing in the result could have
  told them otherwise: **a search that matched forty and showed ten, and a search that
  matched nothing, return the same shape.** Tuning the ranking would trade one silent
  failure for another; this makes the ranking's effect visible, so a thin result becomes a
  reason to ask differently instead of a conclusion that the knowledge does not exist.

  The tool description now also warns that **long, specific queries are not reliably
  better** — ranking penalises long documents, so an entry can be missed by a query built
  from its own title while one distinctive word finds it. Measured here on a 111-entry
  corpus: `clock` returned the target at rank 0 (absent) while its full title returned it
  at rank 1. Ken's own guidance had been telling sessions to search in exactly the style
  that fails.

## [3.1.0] — 2026-08-13

### Added

- **`ken_version` on all three MCP surfaces, and every instruction set now says which
  version wrote it.** Together these let a session detect the one thing it otherwise
  cannot: that the manual it is reading is older than the server it is talking to.

  `ken-prod-ops` measured that problem on a live deployment — their session held 1.7.0's
  instructions while the process serving it had been the 2.1.0 image for hours, and
  nothing said so. An MCP client captures `instructions` **and every tool description**
  when the CONVERSATION begins; neither refreshes on an upgrade or on a reconnect. Only
  tool RESULTS are computed per call.

  **This is deliberately not self-detection**, which prod rejected and I agreed to drop:
  a session cannot check the thing it is made of. It is two statements from the same
  authority at two different times — the instructions state the version that wrote them,
  `ken_version` states the version running now — so the session compares two strings it
  was handed rather than introspecting anything.

  The stamp says what to do, not just a number: that reconnecting does **not** help
  (the counter-intuitive part), that tool descriptions are pinned too, that results are
  still trustworthy, and that stale text is **normal** — a session told only that it is
  out of date will treat an ordinary condition as a fault. Tests assert each of those
  sentences is present, and that all three surfaces register the tool: the session most
  likely to need it is one bound to a single endpoint, so a surface that forgot would
  fail exactly the caller it was for.

- **`station_note_read` takes a `rev`**, so a station can read the revisions it still
  has. 3.0.0 told it how many were lost and gave it no way to look at what survived;
  `ken-prod-ops` had to query the database, which is not a route a station has.

  **The evidence that this is not hypothetical, measured on a live deployment:** they
  extracted a page's surviving revisions at `2026-08-12T23:11:36Z`, and an ordinary write
  destroyed revision 18 at `2026-08-13T01:28:28Z` — **2 h 16 m 52 s later**. The station
  could not have saved itself, because no released version had the argument. The next one
  in that position rescues itself instead of needing an operator with SQL.

  The lowest readable revision is `revisions_lost + 1`; that arithmetic is in the tool
  description, so a test asserts it. A pruned revision says it was pruned. An old
  revision returns the old body under the CURRENT title, because the title was never
  versioned and pretending otherwise would invent history.

- **`station_note_read` takes a `rev`.** 3.0.0 told a station how many revisions it had
  lost and gave it no way to look at the ones that survived; `ken-prod-ops` had to query
  the database directly, which is not a route a station has. **A measurement without a
  read path leaves a session knowing something is wrong and unable to act on it.**

  The lowest readable revision is `revisions_lost + 1`, and that arithmetic is in the
  tool description, so a test asserts it. Asking for a pruned revision says it was
  pruned rather than returning nothing — "gone for good" and "you typed the wrong key"
  send a session to different places.

  An old revision returns the old body under the CURRENT title, because the title was
  never versioned. Presenting one as if it had been would invent history.

### Changed

- **`comm_send` tells senders that the idempotency key outlives the body.** Retention
  blanks the text and keeps the metadata row, so a descriptive key is the only part of a
  message guaranteed to survive it.

  `ken-prod-ops` demonstrated this rather than arguing it: three messages from 2026-08-06
  whose bodies the pre-1.6.0 ack rule destroyed were identified months later from their
  keys alone. The tool had described the key purely as a retry guard — true and
  incomplete. Recorded as a decision in `docs/COMM.md` C7a, including why this is not a
  new `subject` field: the key already is one, and a second field is a second thing to
  forget.

## [3.0.2] — 2026-08-12

### Fixed

- **An expired room message stopped the sweep — all of it.** `Sweep` carries message
  expiry, body retention, the metadata purge, file cleanup and idle-endpoint removal.
  From the first room or broadcast message that expired unread, every one of them
  stopped, and nothing said so: the error went to a log line and retention simply
  ceased.

  The cause is two columns in one scan. A room message belongs to no channel and a room
  delivery names no endpoint, so `message.channel_id` and `delivery.recipient_endpoint`
  are both NULL — and the notice collector scanned both as `int64`. Rooms shipped in
  3.0.0; the sweep was written for a world with one recipient and always a channel, and
  the trigger is the ordinary case rather than an edge one: **a room message nobody
  reads.**

  The sender is now told through the message's own scope, so a room message that dies
  unread reaches its author exactly as a channel message does — and is stamped, so the
  notice is sent once rather than on every sweep forever.


## [3.0.1] — 2026-08-12

### Fixed

- **`station_note_list`'s `revisions_lost` reported the revisions that SURVIVED, not the
  ones that were pruned.** `ken-prod-ops` measured it on a live station within the hour:
  head revision 6, revisions 1–5 all present, nothing ever pruned — and the field
  returned 5.

  **The inversion points at the wrong station**, which is why this is a release rather
  than a note. A healthy page with a long history reports a big number; the one page in
  their estate that genuinely lost seventeen revisions would have reported **8** — less
  than the intact pages around it. The field was added in 3.0.0 so that station would
  finally see its own damage, and as shipped it would have sent a curator to the wrong
  three.

  History holds revisions 1 … head−1, so what is missing is everything below the oldest
  still held. The arithmetic now lives in the store beside the query that produces it,
  rather than in the caller that got it backwards, and the test asserts the ordering
  that actually failed: a pruned page must report MORE loss than an intact one.

  `history_bytes` was correct and is unchanged.


## [3.0.0] — 2026-08-12

### Added

- **Rooms and broadcast — many-party messaging.** `comm_send` takes `to_room` (a room
  your human put you in) or `to_room:"all"` (every station you share a room with), with
  no pairing code for either. A room message is **one body delivered to each member
  separately**: each acks for itself, none settles it for the others, and the text
  survives until the last of them is done with it.

  **Rooms live in `ken.db`, not `comm.db`**, because a membership list is a human
  decision and `comm.db` is expendable by design. `comm.db` keeps a derived mirror,
  rebuilt at boot and on every console write, so `Send` can check membership inside its
  own writer transaction — a check anywhere else is advisory, since a human can remove a
  station between an outer check and the insert.

  **A room is created only by a human, in the console.** There is no agent path, and
  that is not a gap in the tool surface: a room decides which posts may talk to each
  other. A session that wants one asks in words. The console at `/stations` creates,
  fills, empties and archives them; archiving stops sends at once and keeps the history.

  **Broadcast adds reach, never permission** — its audience is the union of the rooms
  you are already in, so it can address exactly the set you could have reached one room
  at a time. A station in three of your rooms receives **one** copy, not three.

  Refusals are distinct where the remedy differs: not a member, room empty, and no
  audience at all are three different sentences, because "join a room" and "add someone
  to yours" send an operator to different places. A send that would reach nobody is
  refused rather than returned with a message id — an audience of zero is the outcome
  hardest to notice.

  `comm_directory` reports the rooms you are in, each with its members by NAME and a
  pending count that delivers nothing, plus how far a broadcast would reach right now
  and the `roster_epoch` the answer describes. Without that a session could be *in* a
  room with no way to learn its id, and the feature would work only when a human pasted
  one into the conversation.

  Not included, deliberately: **no scrollback**. A station added today sees nothing sent
  before it joined; one removed keeps what it was already sent. Membership is
  snapshotted at send, and rewriting an audience afterwards would mean an inbox changed
  because of something that happened later.

- **The station vault — somewhere for a credential to live.** `station_vault_list` / `_put` / `_get` /
  `_delete`, plus a console surface at `/stations`. The locker's own text says *"NEVER put a token, key
  or password here"*, which was correct and left a station with **nowhere to put one at all** — and a
  prohibition with no alternative is advice a session eventually has to break.

  It is the locker's sibling and differs everywhere it matters: a listing **never** returns a value,
  every read is **logged** (a session's `_get` and a human's console reveal land in the same trail,
  distinguished by which one it was), and **nothing is destroyed** — an overwrite keeps the previous
  value and a delete is a tombstone the console can restore.

  **Values are stored unencrypted, and that is the design rather than a gap.** Encrypting them needs a
  key, the key would live in the same `ken.db`, and lock and key would travel together in every backup —
  protecting nobody who can read the file while inviting an operator to relax a control that is not
  there. So the boundary is stated instead of simulated: confidentiality is the host and the backup.
  `docs/BACKUP.md` is corrected in the same change, because its promise that "no credential Ken stores
  is replayable" becomes false the day this ships, and an operator who designed a backup chain around
  that sentence has to be told rather than left to discover it.

  Restore is **console-only with no station tool**: a session that has just destroyed something by
  mistake is not the party to decide what goes back.

  Four settings under Stations, with the audit trail bounded like everything else — but the per-secret
  read COUNT is kept exactly, so the console says "the last 20 of 2,318" rather than presenting 20 as
  the whole story. That is S12's fail-loud rule applied to an audit log, and a deliberate refusal of
  the notebook's silent revision pruning.

- **A test that every template string exists in English.** `T` returns the KEY when a lookup misses,
  which is right at runtime and invisible to every other test: the page renders, the handler returns
  200, the suite is green, and the console shows `stations.vault_help` to the operator. The settings
  registry has had a drift test since 2.0.0; the templates, where most visible text lives, had none.
  Written after this release's own console section would have shipped twenty-two raw keys with a clean
  test run.

### Changed

- **The hearsay badge tells a directed message from a broadcast one.** `ReceivedSince`
  becomes `ReceivedFrom`, returning the traffic behind the marker with **directed
  sources ranked first**, and the console badge reads *"possibly second-hand"* or
  *"heard in a room?"* with tooltips that say which was seen.

  This was a companion to rooms rather than an improvement on them. `ken-prod-ops`
  measured the badge as **nearly always on** before rooms existed — three sessions
  exchanging eleven messages kept the window continuously open — and named the
  consequence: *"a badge that is almost always present carries less information than one
  that is sometimes absent."* One broadcast to nine stations marks nine actors from a
  single send, so shipping rooms without this made an already-weak signal weaker.

  Versions written before this ships carry no kind, and that is deliberate: they were
  marked without the distinction being recorded, and inventing one now would be
  fabricating provenance — the one thing the whole mechanism exists to avoid.

- **A message has RECIPIENTS now, not a recipient.** `message` keeps what is true of the
  message; a new `delivery` table holds one row per recipient with everything that varies
  between them — state, redelivery count, claim, reply deadline, ack. Addressing moves to
  a **scope** (`ch:<channel>`, and `r:<room>` when rooms land) and a **party**
  (`s:<station>` or `e:<endpoint>`).

  The party is the change that matters. The poll query already asked "addressed to my
  endpoint, or to any endpoint of my station" at READ time; storing the party answers it
  at WRITE time. That is what makes a third participant possible at all, and it makes
  `STATIONS.md` S4 — the station owns the inbox — true where the data lives rather than
  only in one query.

  **Sequence numbers are per conversation**, one ascending stream instead of one per
  sender. `ack_up_to_seq` is a range, and two interleaved sequences reusing the same low
  numbers meant a cumulative ack could settle mail nobody had read.

  Body lifetime is the part where a careless port would rebuild the 97%-of-bodies defect
  from a new cause: a body is one object with one lifetime, so it is never blanked while
  any recipient is still owed it, and the retention window runs from the **last**
  recipient to settle. With one recipient every one of these is byte-for-byte the old
  behaviour.

- **Migrations run with foreign keys disabled outside the transaction, and the result is
  checked.** `PRAGMA foreign_keys=OFF` written inside a migration file is a documented
  no-op, so a table rebuild silently fired every `ON DELETE` aimed at the table it
  dropped. Measured against the driver before the fix: a child table populated in the
  same transaction came out empty, with no error. `Migrate()` now pins a connection,
  disables enforcement around the run, and fails loudly on `PRAGMA foreign_key_check`.

### Fixed

- **Five defects `ken-prod-ops` found on a running deployment.** None was introduced by
  a recent release; all five had been shipping quietly.

  **A rollback point named `.db.gz` that is uncompressed SQLite.** `BACKUP.md` documented
  restore as `gunzip -c`, which fails on it — on a live file that will shortly be one of
  the few rollback points still held. The data is intact and `ken backup verify` never
  cared, because it reads magic bytes rather than the name. The documented recipe now
  does the same: it asks the file what it is.

  **The notebook bounds counted characters, not bytes.** SQLite's `LENGTH()` on TEXT
  returns characters, so every non-ASCII page under-reported — 934,305 against 943,072
  estate-wide on their corpus, and worse on anything not mostly English. These bounds are
  backup decisions, so under-counting lets a station carry more disk into every snapshot
  than its setting promised.

  **`station_note_list` now reports `revisions_lost` and `history_bytes`.** Revision
  pruning is silent by design, and one of their stations sits at head revision 26 holding
  only 18 and up — **seventeen revisions destroyed, including its original context, with
  nothing anywhere reporting it.** A tool RESULT is also the only channel measured to
  reach a conversation already in progress, so this is the one place the disclosure can
  actually arrive.

  **The installer re-widened `litestream.yml` from 0640 to 0644 on every upgrade**,
  undoing the mode the packager set for the file that carries replication credentials.

  **`ken-snapshot.service` shipped a truncated comment** — three coherent lines, then a
  sentence with no subject. The missing words were "DO NOT EDIT THIS FILE TO SET IT",
  which is the line telling an operator how not to lose their configuration on the next
  upgrade.

- **The hearsay marker was blind to room mail, and would have shipped that way.** It
  joined `delivery.recipient_endpoint` to find the actor, and a room delivery names no
  endpoint at all — rooms hold stations, and which connection reads the mail is decided
  at poll time. So every room message was invisible to the check, the badge would simply
  never have fired for it, and **an absent badge is indistinguishable from a
  checked-and-clean one**. Found by a test written for the feature above; the query now
  also resolves a party's station back to the actors staffing it, which is the same
  widening the poll predicate already does.

## [2.2.0] — 2026-08-11

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

- **The station instructions steered every session onto the expensive path.** "Keep the
  handoff page current as you go" reads naturally as `mode:append`, and append stores a
  full copy of the page as history **every time** — so history grows with the square of
  the page's length. A measured station reached **96.4% of its cap with 252,759 bytes of
  history behind an 8,083-byte head**, while a *larger* page maintained with `replace`
  and `if_rev` cost a tenth of that.

  All three places that gave the advice now name the pattern rather than only the
  cadence: the connect-time block, `station_note_write`'s description, and the
  empty-handoff nudge in the briefing.

  **Correction, from ken-prod-ops measuring their own deployment after this shipped:**
  the sentence originally here said the tool description is "what a session reads at the
  moment it decides", implying it reaches a running conversation. It does not. Their
  process has served the 2.2.0 image since `2026-08-12T16:59:05Z` while their station
  instructions AND that tool description are verbatim the 2.1.0 text. **Both are captured
  at CONVERSATION START.** Only tool RESULTS are computed per call — and the one result
  changed here fires solely on the empty-handoff arm, which is false for all eight of
  their stations. So on an existing deployment this fix reaches nobody until each session
  restarts, and `comm_poll`'s new fields are the only 2.2.0 change that reaches a running
  session at all.

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

[Unreleased]: https://github.com/Quest-ICT/ken/compare/v3.3.0...HEAD
[3.14.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.14.0
[3.13.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.13.0
[3.12.1]: https://github.com/Quest-ICT/ken/releases/tag/v3.12.1
[3.12.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.12.0
[3.11.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.11.0
[3.10.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.10.0
[3.9.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.9.0
[3.8.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.8.0
[3.7.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.7.0
[3.6.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.6.0
[3.5.1]: https://github.com/Quest-ICT/ken/releases/tag/v3.5.1
[3.5.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.5.0
[3.4.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.4.0
[3.3.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.3.0
[3.2.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.2.0
[3.1.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.1.0
[3.0.2]: https://github.com/Quest-ICT/ken/releases/tag/v3.0.2
[3.0.1]: https://github.com/Quest-ICT/ken/releases/tag/v3.0.1
[3.0.0]: https://github.com/Quest-ICT/ken/releases/tag/v3.0.0
[2.2.0]: https://github.com/Quest-ICT/ken/releases/tag/v2.2.0
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
