# Finishing what is half-built

**This is the shared checklist. It is the answer to "what is done and what is pending".**

Ken began as a knowledge base. COMM, stations, rooms, the vault and OAuth were added to it.
Some of those additions replaced an older mechanism and **the replacement was never finished**,
so two generations of the same idea now coexist. That is where almost every defect found in
August 2026 came from — not from the design, and not from the new parts, but from the seam
between them.

This document exists because status reported in conversation is readable once and unfindable
afterwards. **A human should be able to open this file and know exactly where we are.**

---

## The rules we agreed

1. **No new features until this list is finished.** Adding to a half-finished migration is what
   produced the recurring defects. Bug fixes are not features.
2. **Every item is updated in the SAME COMMIT as the work.** This file can never be stale,
   because letting it go stale is the failure it exists to prevent.
3. **Release whenever it is convenient**, then ask ken-prod-ops to upgrade and verify, then
   **WAIT for their response before starting the next batch.** Their measurements have caught
   things no local check could see, repeatedly.
4. **A migration ships alone.** A rollback must never discard behaviour fixes along with a data
   rewrite.

## How to read the states

| Mark | Meaning |
|------|---------|
| `[ ]` | Not started |
| `[~]` | In progress right now |
| `[x]` | Done — the release that shipped it is named |
| `[-]` | Deliberately dropped — the reason is given |

---

## Where we are today

**Released: 3.9.0** (2026-08-17), **verified on production 2026-08-17T18:32Z** — the first
release in four to RUN migrations (comm.db 11→14, ken.db 18→19), and the check that carried the
previous three inverted: three releases were verified by proving no migration had run, this one
by proving exactly four had. Earlier `applied_at` rows byte-identical, `foreign_key_check` empty
and `integrity_check` ok on both databases.

> **The row accounting is the part worth keeping.** All three drops were claimed inert, and prod
> measured rather than assumed: `message` 417→417, `delivery` 466→466, `channel` 14→14 — delta
> zero on every one. The whole-database arithmetic closes too: comm.db 979→960, which is
> `channel_seq`'s 22 rows removed plus 3 new `schema_migration` rows. **Not one surviving table
> changed row count.** Their schema band went LOUD (R8/R2) and every violation reconciles to a
> declared change, while **R4 and R5 — the rules that need no declaration — stayed silent
> through a migration that removed a table and rewrote two others.** They ran it undeclared on
> purpose, because declaring first would have let them rationalise whatever appeared.

**Batch 4 is closed.** Batch 5 is next and contains no code.

Previously **3.8.0**, verified on production 2026-08-17T16:03Z — band clean on
both databases, migrations 18/11 with original timestamps intact, and the `reply_overdue` fix
confirmed in BOTH directions (31 historical notices eliminated; a genuinely unanswered request on
current traffic still fired one). Batch 3 shipped in it. **The Batch 4 gate is open.**

> **One item in that report is a VERIFICATION GAP, not a pass, and ken-prod-ops refused to
> record it as one.** The idle-sweep fix — an endpoint seating a channel must survive collection —
> could not be exercised on production at all: `comm_endpoint_idle_sec` is overridden there to
> **7776000 (90 days)** and the most idle endpoint is 14 days old, so nothing was ever eligible
> and the sweep had no opportunity to exhibit the bug in either direction. Twelve channels before
> and after is the same green a broken build would produce. **The evidence for that fix is the
> mutation-verified unit test and nothing else**, which is adequate but should not be mistaken for
> production confirmation. The earliest natural opportunity there is **~2026-11-01**; prod offered
> a deliberate window sooner and I did not take it, because the local test already pins the
> property and a staged window would mostly re-test the harness.

Previously **3.7.0**, verified on production 2026-08-15T04:00Z; before that **3.6.0**, verified
2026-08-14T22:02Z — band clean on both databases, and no migration ran at all in either
(`schema_migration`'s rows still carry their original timestamps, and the `migrations/` tree hash
is identical between the tags).

**Unreleased on `main`: documentation only.** For what is actually pending, read
`CHANGELOG.md`'s `## [Unreleased]` section — it is the machine-conventional home and this line is
prose that drifted from it within an hour of being written.

> **This file has broken its own Rule 2 three times, and failed in a fourth way once.**
> The second time was worse: two Batch 2
> headline items sat open after being fixed in the same session that wrote them, and I only
> caught it by READING the list instead of recalling it. The rule needs a habit attached —
> **tick the item in the commit that closes it**, not when someone asks what is left.
>
> **First time:** Six items sat marked *unreleased* after
> shipping in 3.6.0, and prod found it, not me. The rule is right; I did not apply it in the release
> commit itself. Marking an item done and naming its release is part of cutting, not a follow-up.
>
> **Third time**, hours after the sentence above was written: `9219414` shipped the `dm` change and
> its tick landed separately in `a8221f3`, because the script that was meant to write it asserted,
> failed, and the `git commit` on the next line ran anyway with nothing chaining them. Knowing the
> rule was not enough; the shell has to enforce it.
>
> **And a fourth failure, of a different kind, in the 3.8.0 release commit** — the checklist WAS
> stamped in the release commit, which is what Rule 2 asks, but by a blanket find-and-replace of
> `*unreleased*`. It stamped 3.8.0 onto two items that had shipped in **3.7.0**, mangled two
> sentences mid-clause, and rewrote the narrative sentence directly above this one so that it
> described a past mistake in the wrong tense. Stamping blind is not stamping.

---

## Batch 1 — ship what is already done  `[~]`  *(3.6.0, verified on production 2026-08-14T22:02Z; **two items still open**)*

No schema change. Everything named here shipped in 3.6.0 — but the last two items below were
never done, so this batch is not closed.

- [x] `comm_ack` reports what it settled; cumulative ack takes a room id — **3.5.0**
- [x] Room sends wake parked polls; notice recipients are station names — **3.5.0**
- [x] `you_are` and `comm_endpoint_ids` — **3.5.0**
- [x] Archiving a station stops it on COMM — **3.5.0**
- [x] Code-paired channels record their authorising stations — **3.5.0**
- [x] Console and metrics count room mail — **3.5.1**
- [x] Attestation controls documented in INSTALL.md — **3.5.1**
- [x] `ken_comm_deliveries_unacked`; the unacked gauge states its unit — **3.6.0**
- [x] Two figures named *bytes* now count bytes — **3.6.0**
- [x] The briefing says how much of the list it is not showing — **3.6.0**
- [x] A replacement session can download its station's file — **3.6.0**
- [x] `install.sh`'s `_ken_unit_env` says what it is for now — **3.6.0**
- [x] Live claims that COMM and stations can be switched off, corrected — **3.6.0**
- [x] `docs/COMM.md`'s C2 record no longer contradicts its own SUPERSEDED banner six lines later — **3.6.0**,
      and `docs/STATIONS.md` — which had **no banner anywhere** and asserted the opt-out in its
      status line — is corrected. *(Both found by peers: promo hit the contradiction while fixing
      the public site, prod established that STATIONS.md was the worse half.)*
- [x] **The retired-switch sweep is finished** — **3.10.0**. `docs/DESIGN.md` carried two live
      assertions plus a decision record explaining "why the switch survives"; `docs/COMM.md`'s status
      block and C2 heading carried two more. Every remaining mention in the docs now states the
      removal, sits inside a block banner-marked as history, or is a released CHANGELOG entry.
      Original finding: `KEN_COMM_ENABLED` / `KEN_STATION_ENABLED` were removed
      in 2.0.0. The instructions that told an OPERATOR or an AGENT to use them are fixed
      (README, INSTALL, AI-INTEGRATION, MONITORING, MCP-TOOLS, COMM §6 — **but NOT `docs/DESIGN.md`,
      which still carries two unannotated present-tense assertions that the opt-out exists**, and that
      is the kind to do first), and COMM's C2
      decision record is marked superseded rather than rewritten. **About 20 further mentions remain.**
      Split them on promo's line, which prod confirmed is the right one: "wrong tense,
      keep the reasoning" is cosmetic; **"asserts a live capability that does not exist" is the kind
      that propagates** — it is what put "off by default" on the public site in three languages,
      four majors after it stopped being true. Do the second kind first.
- [x] **`docs/FINISHING.md` does not ship** — FIXED, **3.10.0**. It and `docs/OPERATION.md` are
      now staged into the release bundle. Original finding: `scripts/build-release.sh` stages only `INSTALL.md`,
      `BACKUP.md` and `configs/litestream.yml`, and no docs are embedded in the binary — so a file
      whose stated purpose is that a human can open it and know where things stand is absent from
      every artifact a human installs. Either add it to the bundle or say plainly that its audience
      is people with a git checkout. Found by prod, who verified it against the build script rather
      than assuming.
- [x] **Cut and release** — **3.6.0**, verified on production 2026-08-14T22:02Z. Superseded twice since: **3.7.0** (verified 2026-08-15T04:00Z) and **3.8.0** (released 2026-08-17, verification outstanding).

---

## Batch 2 — find out what is actually half-built  `[~]`  *(both surveys done; the findings they produced are still being worked)*

We knew COMM's seams because a survey mapped them, and stations had never been surveyed at all —
guessing would have repeated the mistake this whole plan exists to fix. **So this batch surveyed
stations and the knowledge base.** Both are done; what follows is what they found.

- [x] **Audit stations for half-built seams** — done. 91 candidates, **63 confirmed**, 4 rejected.
      Full evidence in [audits/batch2-stations-kb.md](audits/batch2-stations-kb.md).
- [x] **Audit the knowledge base the same way** — done, **and my expectation was wrong.** I
      predicted it would be small because its open items are policy decisions rather than debt.
      It holds the single worst finding of the batch. "Expected is not checked" was exactly the
      assumption worth testing.

**THE HEADLINE: stations are NOT half-built in COMM's shape.** COMM had two live mechanisms
competing and every defect lived in the seam. Stations are internally consistent; what they have
instead is **one-sidedness** — a write path with no read path, an agent surface with no human
surface, a store function with no caller — plus **a large body of prose describing controls
nobody can reach**. Roughly a third of the findings are text that is false *today*.

### The two that matter most, both re-verified by hand before being written here

- [x] **`kb_record_outcome` writes a table NOTHING READS** — FIXED, **3.7.0**. The reader is
      the maturity badge (`396a4df`), and the identity it counts is server-derived (`811bdb8`)
      after prod measured 37/37 NULL session ids. Original finding: `entry_outcome` is INSERTed at `internal/store/v1tools.go:89`; there is not a
      single SELECT against it anywhere. The connect-time instruction every session captures
      says: *"Act, then close the loop EVERY time: kb_record_outcome … **This is how Ken
      self-curates — do not skip it.**"* Four migrations of collected evidence, a promise in the
      one text that reaches everyone, and no reader. **The decision on how to use it already
      exists** (station task `t-G3QnWfv6`, 2026-08-04: maturity = curation gate + deduped outcome
      evidence) and still needs three numbers from Vlad. *Build the reader or stop asking — the
      present state solicits work and discards it.*
- [x] **"Retire" told the operator connected sessions are safe; it cuts them off** — FIXED in
      `e965bb8`, seven sites, behaviour pinned by a test. Original finding:
      `AuthenticateStationKey` requires `retired_at IS NULL` (`internal/store/stations.go:391`)
      and the middleware re-authenticates every request, so retiring a key kills the holding
      session's notebook, tasks, locker **and vault** at its next call. The console says
      *"Sessions already connected with it are left alone."* Ten sites, three languages. The
      behaviour was corrected in code in 1.5.2 by a commit that touched no `.properties` and no
      template. *A destructive control with a tooltip promising it is safe.*

- [x] **`kb_record_outcome` has a reader** — the maturity badge now reads the outcome evidence
      instead of a promotion count — **3.7.0**. Vlad settled the three open numbers: dedup by
      distinct `session_id`, N=3, and a `was-wrong` since the last promotion blocks the top tier.
> **Record correction, 2026-08-14, itself corrected 2026-08-17.** The over-general claim is
> that **neither station maintaining a handoff page had ever called `station_note_read`**. I first
> recorded it here as living in `9ec2e5c`'s COMMIT MESSAGE. **It does not** — that message
> attributes the observation narrowly and correctly, to ken-promo about ken-promo. The sentence
> `9ec2e5c` actually added is in **`CHANGELOG.md`, inside the released `## [3.7.0]` entry**, which
> is a tracked, editable file that an ordinary commit can correct — and which is what people read.
> So the justification below for leaving it standing was pointed at the wrong artifact, and a
> claim I had already retracted went on shipping. It is annotated at the source now. The
> misattribution does survive unamendably in `981675f`'s own commit message, which is where the
> reasoning below genuinely applies. ken-prod-ops has
> since MEASURED their own usage and it is false for them — they called it on 2026-08-12 and used
> the locker on 2026-08-11; both of their earlier claims were recalled rather than measured.
> promo's statements about themselves stand. The commit is pushed and force-push is blocked on
> this branch, so the sentence stays in the log and the correction lives here. **The `if_rev` fix
> is unaffected**: it was built against the four behaviours prod measured on the live deployment,
> not against that framing — which is the whole argument for measuring.

- [x] **`kb_record_outcome`'s session identity is derived by the server**, not taken from the
      caller — **3.7.0**. ken-prod-ops measured the live deployment and falsified the reader
      before it shipped: **37 of 37 `entry_outcome` rows had `session_id` NULL, and 282 of 282
      `curation_event` rows.** With N=3 distinct sessions required, the top tier was not merely
      empty, it was **unreachable by accumulating more evidence** — the exact failure I had named
      as worse than the one being fixed. Root cause: the field carried no `jsonschema`, the tool
      description never mentioned it, and the instructions never mentioned it. Two AI actors,
      three weeks, not one identity recorded — which is what an undescribed optional field
      predicts. Fixed by reading `req.Session.ID()` (falling back to the token), because a
      caller-supplied identity is unreliable *and* unfalsifiable.
- [x] **N=3 is CONFIRMED by Vlad**, on evidence rather than deference. The "at most 10 entries"
      ceiling I recorded here was prod's own figure and they retracted it: it divided recorded
      outcomes by 3, which measures how rarely the loop is closed, not what N=3 can support. The
      honest ceiling is **39 of 108 entries (36%) have `use_count >= 3`**, so at full loop closure
      N=3 marks about a third of the knowledge base — slow, not unreachable. Lowering it would
      make the badge appear without making it truer, and misfire later: at healthy closure N=2
      marks half the KB and the tier stops distinguishing anything.
- [ ] **THE REAL CONSTRAINT: the loop is closed one time in seven.** 250 recorded uses, 37
      outcomes — **14.8%** — and only 22 of 108 entries have any outcome at all. The instructions
      say *"close the loop EVERY time … do not skip it"* and it is skipped 85% of the time, by
      every session including this one. **Verified from source that the denominator is honest:**
      `use_count` is bumped ONLY by `Store.Get`, whose sole caller is `kb_get` (`server.go:493`);
      the human console uses `GetEntry`, which deliberately does not bump, and `kb_search` does
      not bump either. So a "use" is exactly *an agent fetched the full entry to apply it* — the
      precise occasion an outcome is owed. If anything 14.8% is generous: one `kb_get` may carry
      several slugs and bumps each, while a session is likely to record at most one outcome for
      the batch. **This is upstream of every badge question and it is a defect, not a preference**
      — the same family as `session_id`: something the instructions request that nothing prompts
      for at the moment it matters.
- [ ] **`station_task_defer` is date-shaped and the problem is state-shaped.** Prod's
      observation, and it explains why neither station has ever called it: `defer` takes
      `remind_after`, modelling "not yet" as a TIME, while almost all real staleness is "the
      condition may already be satisfied and nobody rechecked". Consider whether the verb the
      list actually needs is a recheck rather than a postponement.

**A fifth verdict, from prod, that the audit's categories did not have: EXERCISED, NEVER USED.**
Count 1, in a burst, at the moment someone was systematically trying the surface — then never
again. That is the whole locker plus `station_directory` (all five calls
inside 61 seconds on 2026-08-11). A bare count says "used"; usage in the course of work is zero.
It changes the CONFIDENCE of a verdict, not just its label: a capability that survives contact
with a curious operator and still finds no use is a stronger signal than one nobody opened.

- [x] **"Retire" no longer promises that connected sessions are safe** — **3.7.0**, for every
      OPERATOR-VISIBLE site. Seven corrected: six shipped strings across three locales, plus
      `RetireStationKey`'s own doc comment, which made the same claim. **The finding named ten,
      and the three left are developer-facing comments** — `internal/web/stations.go`,
      `internal/store/stations.go` near the `retired_at` note, and `migrations/0012_stations.sql`,
      which still calls it "the \"I moved machines\" path" that the corrected comment in the same
      tree now explicitly disowns. The migration file is deliberately not edited (same reasoning as
      Batch 3's `dm` item: SQLite stores CREATE text verbatim, so editing applied migration prose
      drifts `.schema` between fresh and existing installs). The other two are ordinary fixes. Behaviour pinned by a test, because the code has
      been right since 1.5.2 and only the words were wrong — so nothing in the suite would have
      noticed the words coming back.
- [x] **The four remaining prose findings are corrected** — **3.7.0**.
      `stations.key_not_audited` said key use is never recorded; `TouchToken` has recorded it
      since 1.5.3 and the string shipped *inside the commit that made it false*, denying the one
      signal an operator needs before retiring a key. `stations.archive_help` said keys stop
      working; archiving never touches keys — it stops COMM. `ken station requests` told the
      operator to approve with `ken station add`, which creates a station and leaves the request
      pending forever, producing the split state `ApproveStationRequest`'s transaction exists to
      prevent. `station_vault_get` promised the caller's identity reaches the console. All in
      three locales where they were translated.
- [ ] **Render WHO read a vault secret, or stop carrying the actor id.** Sharper than the audit
      had it: `by_actor_id` IS recorded, and `StationVaultReads` DOES select it into
      `StationVaultRead.ActorID` — and `stations.html:405` renders `{{.Name}} · {{.Via}} ·
      {{.ReadAt}}` and drops it. So the data is collected, carried to the view model, and thrown
      away at the last step. The description now promises only what is shown; either render the
      actor (resolved to a name — a bare integer is not an identity a human can read) or stop
      selecting it.
- [ ] **Work through the remaining confirmed findings** in the appendix. **The prose class is NOT
      done** — that sentence stood here until 2026-08-17 and was wrong. Five findings are corrected
      (the Retire strings, and the four in `ea4443c`); the rest of the appendix's prose section is
      still live. Three verified by hand today, all operator- or session-facing:
      `docs/STATIONS.md:878` documents a `merge_into?` parameter on `station_task_add` that appears
      **zero** times in `internal/`; the same table gives `station_task_defer(ids[], until, reason)`
      while `taskDeferIn` takes a single `task_id`; and **`stations.vault*` has 19 keys in English
      and ZERO in Spanish and French**, so a non-English operator sees raw keys where the vault's
      warnings should be. What is left is therefore prose AND one-sidedness, not one-sidedness. The rule
      this batch confirms: **text asserting a control that does not exist is the class that
      propagates** — it is what put "off by default" on the public site, and it is a third of
      what was found here.
- [ ] **Age EVERY open task, not just the human-blocked ones.** 3.6.0 added
      `oldest_blocked_on_human_days`, which covers only `blocked_on='human'`. ken-promo audited
      their own station and found a third staleness category neither of us had: a task that is
      **overtaken rather than stale** — not wrong, just no longer the point. Their example was
      "read the 1.5.1 and 1.5.2 promo briefs", created 2026-07-30, still accurate and pointless
      because Ken was already at 3.6.0 when promo found it on 2026-08-14. It was `blocked_on='self'`, so the field I shipped misses it, and
      `briefed_count` could never have caught it. Age since creation would have.
- [ ] Fold the findings into Batch 3/4 below, then re-read this file with Vlad.

---

## Batch 3 — finish the party model  `[~]`  *(shipped in **3.8.0**; only the `attachment` scope migration remains, and it ships alone)*

**The recurring defect.** A message is addressed to a PARTY (`s:<station>` or `e:<endpoint>`)
so a replacement session inherits its station's inbox. Six places compared endpoint rowids
instead, each found and fixed separately: `Poll`, `Ack`, the pending counters,
`waiting_for_you`, the room mirror, and file downloads. Finishing means no seventh.

- [x] **Sweep every comparison against an endpoint rowid** — **3.8.0**. Classifying each as correct (it really
      is about one connection) or wrong (it is about an inbox). This is a finite, greppable list
      — do it once rather than find the seventh in production. **109 sites classified across five
      independent lenses, every finding adversarially verified; evidence in
      [audits/batch3-party-model-sweep.md](audits/batch3-party-model-sweep.md).** There WAS a
      seventh, and it was five lines below the sixth fix: `GrantDownload` authorises by party and
      then re-checked the attachment's frozen recipient rowid for revocation — permanent for the
      CHANNEL, because every later offer is stamped with the same dead seat. Four of the five
      lenses landed on that one line.

      Fixed in this batch: the file grant (`a4e1f32`), the idle sweep deleting channels by
      cascade (`28c4c63`), a station able to take both seats of one channel and its own missing
      outstanding-request list (`b7c573b`), the authorisation check re-deriving authorisation
      from a column an agent tool can clear (`bcd60dd`), both stale wakeups (`6f58037`), and the
      revoke dialog's blast radius (`f8f2740`).

      **The root the critic named, which is worth more than any single fix:**
      `LiveEndpointForStation` is an explicitly APPROXIMATE heuristic — "whichever endpoint is
      chosen, the message lands in the STATION's inbox" — whose answer is written into
      `channel.endpoint_b` and never updated. That is true for ADDRESSING, which is what it was
      written for, and false for every other use the value was then put to. Three separately
      confirmed defects were one frozen approximation read as an identity.
- [ ] **`attachment` is channel-shaped and must become scope-shaped.** `channel_id` is
      `NOT NULL`; `recipient_endpoint` is `NOT NULL` and holds ONE endpoint where a room needs a
      party set. Migration 0010 already added and backfilled `attachment.scope_id` — and
      `internal/comm/file.go` contains the string "scope" **zero times**. The seam was cut and
      never used, so a file cannot be offered to a room. *Needs a migration; ships alone.*
- [x] **`comm_room.kind='dm'`** — resolved as DOCUMENTED, not built and not dropped, **3.8.0**. The CHECK constraint at `migrations/0017_comm_rooms.sql:36`
      permits a value `CreateRoom` cannot produce (`internal/store/rooms.go:57-59` hardcodes
      `'topic'`). *Moved here from Batch 4 on prod's correction: it is an unfinished migration, not
      a duplicated generation.* It is also the natural two-party container, so Batch 5's second
      decision may settle it. **Settled for this batch:** the value stays
      reserved and `internal/store/rooms.go` now says plainly that nothing produces it, because
      building `dm` is a new feature AND the shape it would take is Batch 5's decision. **Migration
      0017 is deliberately left unedited** even though its comment is the sentence that misleads:
      SQLite stores a table's CREATE statement verbatim, comments included — verified, not assumed
      — so editing an applied migration's prose makes `.schema` differ between a fresh install and
      an existing deployment while changing nothing about either, and prod runs a schema band over
      exactly that. Correcting the Go model reaches every reader at no drift.
- [x] **A regression test for retroactive revocation** — **3.8.0**. Revoking a channel stops mail already
      queued; that property is held by three hand-mirrored SQL predicates and the only test
      asserts a *send* fails afterwards. Nothing pins the retroactive half. **The three are
      `message.go:583`, `message.go:813` and `pending.go:56`** — prod's own first pass found two and
      nearly corrected me; the third is the predicate behind `pending_total`, so a test that misses
      it lets the console counter drift from the poll. **Five mutants, all killed**, each gated on the
      edit actually applying and the tree still building — an inert mutant reads as SURVIVED and
      has fooled this project four times. The room arm exists because a revocation test between
      two endpoints in no room cannot tell a correct `LEFT JOIN` from an `INNER` one: both return
      zero.

---

### Raised by the Batch 3 sweep, not fixed in it

- [ ] **The "an endpoint cannot move between stations" invariant is one boolean on a column
      another tool clears.** `comm_bind` refuses when `station_id` is set; `comm_unbind` clears it
      and its own success note says "You can bind again later". So unbind-then-bind moves an
      endpoint between stations, and the tool performing the bypass advertises it. **The stated
      harm is also the wrong harm**: the error says a moved endpoint would carry the first
      station's unread mail across, which cannot happen — party keys are recorded at write time
      and no delivery row moves. What actually moves is channel MEMBERSHIP, because the seat is
      re-derived from the live binding. Enforce the invariant or delete the claim; leaving both is
      how the next reader concludes the live join is safe. Belongs with the credential model.
- [ ] **Backfill `channel.station_a` / `station_b`, then make the snapshot authoritative.**
      `bcd60dd` consults the snapshot IN ADDITION to the live binding because the column is NULL
      on every pairing-code channel opened before migration 0008 — six of seven on a real
      deployment — so making it authoritative today would strand them. *Migration; ships alone.*
- [ ] **The four pending counters do not agree, in the file that says they must not.**
      `pendingScopeSQL` carries an expiry predicate and states why; `RoomsFor` and
      `PendingForEndpoint` do not. Between a message expiring and the next sweep, one
      `comm_channels` result can report `pending_total=0` beside a per-channel count of 1 — and
      the frozen instruction block tells sessions to read `pending_total` FIRST. Bounded by the
      sweep interval; over-reports, never under.
- [ ] **The hearsay rule tells every session to record the disposable identity.** The frozen
      instruction block says to attribute knowledge from a peer to "the sending endpoint". Endpoint
      rows are deleted by the idle sweep; the knowledge base has no TTL, so the attribution names a
      row that no longer exists — and three sessions of one correspondent become three unrelated
      opaque ids. `messageView` already carries `from_station_id` and `from_station_name`. Adjacent:
      the loop in that same block never mentions `comm_bind` or stations at all, so a successor
      reading only the instructions is told to re-pair — the precise cost stations abolish.
- [ ] **89 keys are missing from both `messages_es` and `messages_fr`.** Pre-existing and unchanged
      by this batch; noticed while checking that new keys landed in all three. A missing key renders
      raw, so this is visible to any non-English operator.

---

## Batch 4 — retire the duplicated generation  `[x]`  *(**3.9.0**, verified on production 2026-08-17T18:32Z; `station_block` deferred to Batch 5 by Vlad)*

Two generations of the same idea coexist. Each item is *delete one of them*, not build a third.

- [x] **`message.space_id`** — **3.9.0**. Written by nothing, read by nothing. Proven while fixing the
      console counters, which reach the sender's space through `sender_endpoint` instead. Drop
      the column. *Migration.*
- [x] **`channel_seq` vs `scope_counter`** — settled; Go half and table both gone — **3.9.0**.
      `scope_counter` won in 3.0.0. Go half **3.8.0**+, table dropped **3.9.0**. `channel_seq` numbered the `message.seq` column, and migration
      0009 rebuilt `message` **without that column** — so it was never a rival numbering of the
      same stream, it is a stranded remnant. `nextSeq` lost both call sites in the same slice and
      nobody deleted it: zero callers, still writing a table nothing read.
      **`ErrSequenceCollision` was worse than dead code.** Its detector matched error text naming
      `message.seq` and `message.sender_endpoint` — a UNIQUE index 0009 dropped — so it could
      never fire, while the branch consuming it told a session to call `comm_unbind`: the one
      remediation the party-model sweep found costs a station its channel, recommended for a
      condition impossible since 3.0.0, in an ERROR STRING, which is the one channel that DOES
      reach an already-running session. Gone in `2478ef2`.
      - [x] **Drop the `channel_seq` table** — **3.9.0**. Nothing writes it now, so
            it is inert until then. Note the one enumeration error an adversarial verifier caught:
            `DELETE FROM channel` cascades into it via FK, so it is not true that "no write path
            exists" — the cascade simply has one fewer target after the drop.
- [ ] **`station_block` — VLAD'S DECISION, and the evidence is now in.** Verified empirically
      rather than by grep: the table holds **0 rows** in the live `ken.db`, and the only objects in
      `sqlite_master` naming it are the table and its own index — no FK, trigger or view in either
      direction. The three functions and the table arrived together in `2458b02` (v3.0.0) and
      nothing has referenced either since except two doc commits. So it is provably unused in every
      deployment that ever ran Ken code.
      **What it would cost to keep:** its migration calls it "a targeted deny that beats the roster
      and beats a link… what makes a broad addressing default safe to offer". Wiring it honestly
      means a send-path check on all three paths (channel, room, broadcast) plus a console surface,
      and a decision about which database owns it, since COMM keeps only a derived mirror of room
      membership. **What it costs to delete:** that capability, and a migration.
      Original finding: `BlockStationPair`, `UnblockStationPair` and `BlockedPairs` have
      **zero callers anywhere, including tests**. Its own migration describes it as the targeted
      deny that "beats the roster and beats a link". Either wire it to a console surface and a
      send-path check, or delete it. Leaving designed-and-unwired code is how a future session
      concludes the capability exists.


---

### Vlad's rulings, 2026-08-17

- **Rule 4 means "a release containing schema change carries nothing else", NOT one migration
  per release.** So the four schema-only changes below ship TOGETHER in one solo release —
  three inert column/table drops and one trigger rebuild — costing prod one upgrade and one
  verification instead of four. **Batch 3's `attachment` scope-shaping is deliberately NOT in
  it**: that one is a restructure carrying code changes in `file.go`, so bundling it would
  destroy the property that makes bundling safe — that every change in the release is provably
  inert. It ships on its own, and it is the one case where Rule 4 and the code it needs are in
  genuine tension.
- **`station_block` is DEFERRED TO BATCH 5**, neither wired nor deleted. It is the safety
  mechanism that makes a permissive addressing default defensible, so the real question is
  about that default, not about this table. Left in place, unwired, and recorded as pending —
  which is the one state this plan normally forbids, taken deliberately and with a date on it.

### Found by the Batch 4 sweep, not listed in it

Four lenses over "two generations coexist", each adversarially verified. Two of the four analyses
were refuted on details — one would not have compiled — which is the argument for the verify pass.

- [x] **The claim-lease default had genuinely DRIFTED, not merely been duplicated** — **3.9.0**.
      `comm.DefaultLimits()` said 300 while `internal/settings` said 900, and settings' own comment
      names comm as "the source of truth" that it mirrors — so the declared authority was the one
      that was wrong. Production was never affected (boot takes the settings value, and 900 matches
      `docs/STATIONS.md`), but **the test suite was exercising a lease production has never used.**
- [x] **`delivery.notified_at` is dead** — **3.9.0**. 0003 added `message.notified_at` so a repeating sweep
      notified exactly once; 0009 carried it onto `delivery`; 0011 replaced written notices with a
      derived query and moved exactly-once into `notice_watermark`. The column survived all three.
      *Migration; ships alone.*
- [ ] **`Store.MarkNoticesSeen` has zero production callers** — only two tests. It is the explicit
      "I have read my notices" call that migration 0011's own comment argues at length is unusable
      here, because MCP tool lists pin at conversation start; `NoticesForPoll` supersedes it by
      promoting the previous poll's mark automatically. Deleting it needs NO migration, but the two
      tests must be rewritten through `NoticesForPoll` rather than having the call deleted.
- [x] **The second generation did not inherit the first's invariant** — **3.9.0**. Migration 0010 rebuilt the
      `entry_version_immutable` trigger for the sole purpose of freezing `via_comm`, stating that
      "a mutable marker could simply be UPDATEd away — which would defeat the point". Migration 0018
      then added `via_comm_kind`, which is written and read like its sibling and is **not in the
      frozen set**. *Migration; ships alone.*
- [ ] **One function, two copies, only one hardened.** `internal/comm`'s `Migrate` pins a
      connection, sets `foreign_keys=OFF` outside the transaction for the whole run and runs
      `foreign_key_check` afterwards — with the measurement that bought it in the comment. The
      `internal/store` runner does not.
- [ ] **The nightly backup does not cover `comm.db` at all.** `cli_backup.go` and
      `scripts/ken-snapshot.sh` both target `ken.db` only. Not a duplicated generation and not in
      this batch's scope — but comm.db holds the delivery ledger, and finding it while proving that
      nothing enumerates columns is exactly the kind of thing that gets lost if it is not written
      down.
- [-] **`message.response_mode`** — not a duplicate. Its string occurs exactly ONCE in the repo,
      in its own column definition: an unbuilt seam, not a superseded generation. Leave it.

---

## Batch 5 — the two decisions that are not code  `[x]`  *(both DECIDED 2026-08-18; see [DECISIONS-BATCH5.md](DECISIONS-BATCH5.md))*

These block slice 7 and are **Vlad's**, not a session's. Listed here so they are visible rather
than implicit.

> **The evidence for both is assembled in
> [DECISIONS-BATCH5.md](DECISIONS-BATCH5.md)** — options with costs, the facts that decide each,
> a recommendation, and the five numbers only production can supply. Written 2026-08-17 because
> listing a decision as pending is not the same as making it decidable, and these had been
> pending while a week of relevant evidence accumulated elsewhere.

- [x] **The credential model** — DECIDED 2026-08-18: **B now, D later.** The endpoint pair moves
      out of tool arguments into a request header immediately; the station-key model follows once
      the unbound endpoints are resolved. Original framing: ken-prod-ops proposes authenticating a bound endpoint with the
      STATION KEY and deriving the endpoint, which removes the loose credential file entirely.
      Vlad's posture — *every session must hold a station, always* — is what makes it possible.
- [x] **How two sessions get a private conversation** — DECIDED 2026-08-18: **P3, then P2; dm
      rooms declined.** Approving a link creates the pair conversation; `comm_send{to_station}`
      is authorised by that link. Original framing: Today `comm_open_channel` is the only
      agent-initiable path; rooms need a human at the console. Whatever replaces it must not
      widen broadcast reach, because room membership feeds `to_room:"all"` and an agent must not
      be able to enlarge its own audience.

---

## Slice 7 — retire the channel  `[ ]`

**Not scheduled.** It is a program, not a slice, until Batches 3, 4 and 5 are done — and after
them it may be a slice again. The full dependency map is in the survey; the five hard blockers
are H1 (no agent-initiable private path), H2 (file exchange is channel-only), H3 (retroactive
revocation), H4 (`station_block` is dead), H5 (unbound endpoints have no address).

Batches 3 and 4 dissolve H2, H3 and H4. Batch 5 dissolves H1 and H5.

## Batch 6 — what the Batch 5 decisions make into work  `[ ]`

The decisions are made; this is what they cost. **The order is load-bearing.**

- [ ] **Migrate the six unbound endpoints onto stations.** OPERATOR work, not code: three are
      idle (0 seats, last seen 5–14 days ago) and can be revoked; three are live and need
      stations plus a rebind, one of them holding **seven channel seats**. Until this is done,
      "every session holds a station, always" is an aspiration and no station-key option is safe.
- [ ] **B — move the endpoint pair to a request header.** Closes the transcript exposure
      ken-prod-ops measured. Needs the per-call `withCaller` wrap that `/comm/mcp` lacks; the
      other two surfaces already have it. Keep the arguments accepted-and-ignored for one release
      so running sessions are not broken.
- [ ] **P3 — approving a link creates the pair conversation.** Nearly free, no new agent verb.
      **Ship the better approval surface with it**: ken-prod-ops recorded that Vlad approved two
      link requests on 2026-08-13 *without being told what he was approving*. The consent gate
      works; the consent was uninformed, and that is the half worth fixing.
- [ ] **P2 — `comm_send{to_station:"X"}`, authorised by the existing link.** A new scope prefix
      beside `ch:` / `r:` / `b:`, a `membersOfScope` arm, and reply/sequence numbering per pair.
      This is the one that makes `comm_open_channel` redundant, which is slice 7's actual goal.
- [ ] **D — station key authenticates `/comm/mcp`.** AFTER the migration. Two things must land in
      the same commit or it ships a lie: `retired_at` is checked only by `AuthenticateStationKey`,
      so "Retire this key" would silently not sever messaging; and `/comm/mcp` never re-derives
      the caller per call, which is harmless only while the per-call secret pins identity.
- [-] **dm rooms** — declined 2026-08-18. Not because they are wrong, but because
      `room_member_mirror` carries no `kind` and a missed audience filter widens broadcast
      invisibly. Revisit only if the mirror gains a kind for another reason.

---

---

## Deliberately not doing

- [-] **Scoping pairing codes to a station** — it hardens a mechanism that slice 7 removes, and
      `comm_open_channel` already covers the station-to-station case. The regression it was
      hiding (link revocation could not close code-paired channels) *was* fixed, in 3.5.0.
- [-] **A full re-implementation, for now.** The defects found have been shallow — a wrong
      predicate, a missing filter, an unwired call — and none demanded a design change. The
      pain is localised to one unfinished migration, and the tests and comments encode failures
      already paid for. Revisit after Batch 5, when the identity model is settled and there is
      more information than there is today.
- [-] **Closing stale tasks on age** — detection is the ask. What a human owes is theirs to
      abandon, not a session's.
