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

**Released: 3.5.1.** Production is on it.

**Unreleased on `main`: 5 commits** — the unacked-unit metric split, the install.sh comment
correction, the bytes-not-characters fixes, the task-staleness figures, and the file-download
party fix.

---

## Batch 1 — ship what is already done  `[~]`

No schema change. Everything here is written, tested and sitting on `main`.

- [x] `comm_ack` reports what it settled; cumulative ack takes a room id — **3.5.0**
- [x] Room sends wake parked polls; notice recipients are station names — **3.5.0**
- [x] `you_are` and `comm_endpoint_ids` — **3.5.0**
- [x] Archiving a station stops it on COMM — **3.5.0**
- [x] Code-paired channels record their authorising stations — **3.5.0**
- [x] Console and metrics count room mail — **3.5.1**
- [x] Attestation controls documented in INSTALL.md — **3.5.1**
- [x] `ken_comm_deliveries_unacked`; the unacked gauge states its unit — *unreleased*
- [x] Two figures named *bytes* now count bytes — *unreleased*
- [x] The briefing says how much of the list it is not showing — *unreleased*
- [x] A replacement session can download its station's file — *unreleased*
- [x] `install.sh`'s `_ken_unit_env` says what it is for now — *unreleased*
- [x] Live claims that COMM and stations can be switched off, corrected — *unreleased*
- [ ] **Finish the retired-switch sweep.** `KEN_COMM_ENABLED` / `KEN_STATION_ENABLED` were removed
      in 2.0.0. The instructions that told an OPERATOR or an AGENT to use them are fixed
      (README, INSTALL, AI-INTEGRATION, MONITORING, MCP-TOOLS, COMM §6, DESIGN), and COMM's C2
      decision record is marked superseded rather than rewritten. **About 20 further mentions
      remain** — mostly inside decision records and rationale, where the reasoning is still worth
      keeping and only the tense is wrong. Go through them once; do not delete the reasoning.
- [ ] **Cut and release.** Then ask prod to upgrade and verify. **Then wait.**

---

## Batch 2 — find out what is actually half-built  `[ ]`

We know COMM's seams because a survey mapped them. **We have not done the same for stations,**
and guessing would repeat the mistake this whole plan exists to fix.

- [ ] **Audit stations for half-built seams**, the same way COMM was audited: what was replaced
      and left coexisting, what is written and never read, what is admitted by a constraint and
      never built. Produces new checklist items, not code.
- [ ] **Audit the knowledge base the same way.** Expected to be small — its open questions are
      policy decisions waiting on a human, not debt — but *expected* is not *checked*.
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
- [ ] **A regression test for retroactive revocation.** Revoking a channel stops mail already
      queued; that property is held by three hand-mirrored SQL predicates and the only test
      asserts a *send* fails afterwards. Nothing pins the retroactive half.

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
- [ ] **`comm_room.kind='dm'`** — admitted by a CHECK constraint; `CreateRoom` hardcodes
      `'topic'`. Anticipated, never built. Decide: build it (it is the natural two-party
      container) or remove it from the constraint.

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
