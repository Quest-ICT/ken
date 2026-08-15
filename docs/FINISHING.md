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

**Released: 3.7.0** — prod asked to upgrade and verify; **the Batch 3 gate is that report.**
Previously 3.6.0, verified on production 2026-08-14T22:02Z — band clean on both
databases, no migration ran at all (`schema_migration`'s rows still carry their original
timestamps, and the `migrations/` tree hash is identical between the two tags). **The Batch 2 gate
is open.**

**Unreleased on `main`: documentation only.** For what is actually pending, read
`CHANGELOG.md`'s `## [Unreleased]` section — it is the machine-conventional home and this line is
prose that drifted from it within an hour of being written.

> **This file has broken its own Rule 2 twice.** The second time was worse: two Batch 2
> headline items sat open after being fixed in the same session that wrote them, and I only
> caught it by READING the list instead of recalling it. The rule needs a habit attached —
> **tick the item in the commit that closes it**, not when someone asks what is left.
>
> **First time:** Six items sat marked *unreleased* after
> shipping in 3.6.0, and prod found it, not me. The rule is right; I did not apply it in the release
> commit itself. Marking an item done and naming its release is part of cutting, not a follow-up.

---

## Batch 1 — ship what is already done  `[x]`  *(3.6.0, awaiting prod's verification)*

No schema change. Everything here is written, tested and sitting on `main`.

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
- [x] `docs/COMM.md`'s C2 record no longer contradicts its own SUPERSEDED banner six lines later,
      and `docs/STATIONS.md` — which had **no banner anywhere** and asserted the opt-out in its
      status line — is corrected. *(Both found by peers: promo hit the contradiction while fixing
      the public site, prod established that STATIONS.md was the worse half.)*
- [ ] **Finish the retired-switch sweep.** `KEN_COMM_ENABLED` / `KEN_STATION_ENABLED` were removed
      in 2.0.0. The instructions that told an OPERATOR or an AGENT to use them are fixed
      (README, INSTALL, AI-INTEGRATION, MONITORING, MCP-TOOLS, COMM §6, DESIGN), and COMM's C2
      decision record is marked superseded rather than rewritten. **About 20 further mentions
      remain. **Split them on promo's line, which prod confirmed is the right one:** "wrong tense,
      keep the reasoning" is cosmetic; **"asserts a live capability that does not exist" is the kind
      that propagates** — it is what put "off by default" on the public site in three languages,
      four majors after it stopped being true. Do the second kind first.
- [ ] **`docs/FINISHING.md` does not ship.** `scripts/build-release.sh` stages only `INSTALL.md`,
      `BACKUP.md` and `configs/litestream.yml`, and no docs are embedded in the binary — so a file
      whose stated purpose is that a human can open it and know where things stand is absent from
      every artifact a human installs. Either add it to the bundle or say plainly that its audience
      is people with a git checkout. Found by prod, who verified it against the build script rather
      than assuming.
- [x] **Cut and release** — **3.6.0**. Prod asked to upgrade and verify; awaiting their report.

---

## Batch 2 — find out what is actually half-built  `[ ]`

We know COMM's seams because a survey mapped them. **We have not done the same for stations,**
and guessing would repeat the mistake this whole plan exists to fix.

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

- [x] **`kb_record_outcome` writes a table NOTHING READS** — FIXED, *unreleased*. The reader is
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
      instead of a promotion count. *Unreleased.* Vlad settled the three open numbers: dedup by
      distinct `session_id`, N=3, and a `was-wrong` since the last promotion blocks the top tier.
> **Record correction, 2026-08-14.** `9ec2e5c`'s commit message says *"neither of the two
> stations that maintain handoff pages had ever called `station_note_read`"*. ken-prod-ops has
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
again in three weeks of work. That is the whole locker plus `station_directory` (all five calls
inside 61 seconds on 2026-08-11). A bare count says "used"; usage in the course of work is zero.
It changes the CONFIDENCE of a verdict, not just its label: a capability that survives contact
with a curious operator and still finds no use is a stronger signal than one nobody opened.

- [x] **"Retire" no longer promises that connected sessions are safe** — **3.7.0**. Seven
      sites corrected (six shipped strings across three locales, plus `RetireStationKey`'s own
      doc comment, which made the same claim). Behaviour pinned by a test, because the code has
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
- [ ] **Work through the remaining confirmed findings** in the appendix — the prose class is
      done; what is left is the one-sidedness. The rule
      this batch confirms: **text asserting a control that does not exist is the class that
      propagates** — it is what put "off by default" on the public site, and it is a third of
      what was found here.
- [ ] **Age EVERY open task, not just the human-blocked ones.** 3.6.0 added
      `oldest_blocked_on_human_days`, which covers only `blocked_on='human'`. ken-promo audited
      their own station and found a third staleness category neither of us had: a task that is
      **overtaken rather than stale** — not wrong, just no longer the point. Their example was
      "read the 1.5.1 and 1.5.2 promo briefs", created 2026-07-30, still accurate and pointless
      because Ken is at 3.6.0. It was `blocked_on='self'`, so the field I shipped misses it, and
      `briefed_count` could never have caught it. Age since creation would have.
- [ ] Fold the findings into Batch 3/4 below, then re-read this file with Vlad.

---

## Batch 3 — finish the party model  `[ ]`

**The recurring defect.** A message is addressed to a PARTY (`s:<station>` or `e:<endpoint>`)
so a replacement session inherits its station's inbox. Six places compared endpoint rowids
instead, each found and fixed separately: `Poll`, `Ack`, the pending counters,
`waiting_for_you`, the room mirror, and file downloads. Finishing means no seventh.

- [ ] **Sweep every comparison against an endpoint rowid** and classify each: correct (it really
      is about one connection) or wrong (it is about an inbox). This is a finite, greppable list
      — do it once rather than find the seventh in production.
- [ ] **`attachment` is channel-shaped and must become scope-shaped.** `channel_id` is
      `NOT NULL`; `recipient_endpoint` is `NOT NULL` and holds ONE endpoint where a room needs a
      party set. Migration 0010 already added and backfilled `attachment.scope_id` — and
      `internal/comm/file.go` contains the string "scope" **zero times**. The seam was cut and
      never used, so a file cannot be offered to a room. *Needs a migration; ships alone.*
- [ ] **`comm_room.kind='dm'`** — the CHECK constraint at `migrations/0017_comm_rooms.sql:36`
      permits a value `CreateRoom` cannot produce (`internal/store/rooms.go:57-59` hardcodes
      `'topic'`). *Moved here from Batch 4 on prod's correction: it is an unfinished migration, not
      a duplicated generation.* It is also the natural two-party container, so Batch 5's second
      decision may settle it.
- [x] **A regression test for retroactive revocation** — *unreleased*. Revoking a channel stops mail already
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

## Batch 4 — retire the duplicated generation  `[ ]`

Two generations of the same idea coexist. Each item is *delete one of them*, not build a third.

- [ ] **`message.space_id`** — written by nothing, read by nothing. Proven while fixing the
      console counters, which reach the sender's space through `sender_endpoint` instead. Drop
      the column. *Migration.*
- [ ] **`channel_seq` vs `scope_counter`** — establish which is authoritative, whether either is
      dead, and remove the loser.
- [ ] **`station_block`** — `BlockStationPair`, `UnblockStationPair` and `BlockedPairs` have
      **zero callers anywhere, including tests**. Its own migration describes it as the targeted
      deny that "beats the roster and beats a link". Either wire it to a console surface and a
      send-path check, or delete it. Leaving designed-and-unwired code is how a future session
      concludes the capability exists.


---

## Batch 5 — the two decisions that are not code  `[ ]`

These block slice 7 and are **Vlad's**, not a session's. Listed here so they are visible rather
than implicit.

- [ ] **The credential model.** ken-prod-ops proposes authenticating a bound endpoint with the
      STATION KEY and deriving the endpoint, which removes the loose credential file entirely.
      Vlad's posture — *every session must hold a station, always* — is what makes it possible.
- [ ] **How two sessions get a private conversation.** Today `comm_open_channel` is the only
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
