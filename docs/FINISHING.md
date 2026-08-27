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

1. ~~**No new features until this list is finished.**~~ **LIFTED by Vlad, 2026-08-20**, after a
   cleanup pass and two adversarial audits established what was actually outstanding. The instruction
   was *"lift rule 1, let's finish the pending items"* — so the list is now work to be DONE rather
   than a gate holding other work back.

   **What it was for, kept because the reasoning did not expire:** adding to a half-finished
   migration is what produced the recurring defects, and bug fixes were never features. That hazard
   is unchanged; what changed is that the half-finished migrations named in Batches 1–6 are now
   finished or explicitly deferred, so the gate was protecting against a state the project has left.

   **What replaces it:** nothing automatic. An item here still ships with its documentation and its
   test, and a schema change still ships alone (Rule 4).
2. **Every item AND the status header are updated in the SAME COMMIT as the work.** This file can never be stale,
   because letting it go stale is the failure it exists to prevent.
3. **Release whenever it is convenient**, then ask ken-prod-ops to upgrade and verify, then
   **WAIT for their response before starting the next batch.** Their measurements have caught
   things no local check could see, repeatedly.
4. **A migration ships alone.** A rollback must never discard behaviour fixes along with a data
   rewrite.

> **Rules 2 and 4 are the two that have ever been broken, and only one of them now has a
> mechanism.** Rule 2 was broken nine releases running until the status header was put behind
> `TestFinishingHeaderIsNotStale`. Rule 4 has held every time, because a schema change is a file
> you have to create — the discipline is attached to an artefact rather than to a memory. That is
> the pattern worth copying: **when a rule keeps failing, look for the artefact it could hang
> off.**

## How to read the states

| Mark | Meaning |
|------|---------|
| `[ ]` | Not started |
| `[~]` | In progress right now |
| `[x]` | Done — the release that shipped it is named |
| `[-]` | Deliberately dropped — the reason is given |
| `[>]` | **Deferred to a named later conversation** — NOT dropped, and the obligation survives |

> `[>]` exists because `[-]` was being used for both. An item deferred under a mark that reads as
> *disposed of* is how `station_block` disappeared into Batch 5's `[x]` and had to be rediscovered,
> and Batch 6 reached `[x]` the same way. A batch may close over a `[-]`; it may **not** close over
> a `[>]` without saying so in its header.

---

## Where we are today

**Released: 3.40.0** (2026-08-27) — a channel send to a seat nobody can ever hold is refused at send time, scoped so a revoked BOUND peer still passes to its successor. Previously 3.39.0 (2026-08-27) — a connector row says LEGACY GRANT and shows its redirect host, so a short tool list names its own cause. Previously 3.38.0 (2026-08-26) — an abandoned workspace can be taken over from the console, mailbox included, and asset transfer stops leaving the vault behind. Previously 3.37.1 — the session_key guidance covers chat sessions, which cannot see their own conversation id (measured). Previously 3.37.0 — a conversation claims a comm endpoint and drives it with no secret, so a chat session with no disk can use messaging. Previously 3.36.0 — `/all/mcp`: all three surfaces from ONE connector, 45 tools on one endpoint, with the three existing ones unchanged. Previously 3.35.1 — station_me tells every session how to keep its workspace, in the result, because a session's schema cannot. Previously 3.35.0 — a conversation declares its own workspace in-band (`station_me{session_key}`), so onboarding costs the consent and nothing else. Previously 3.34.0 — the station surface is reachable through claude.ai connectors: `/station/mcp?workspace=<id>`, because the client refuses custom headers. Previously 3.33.1 — a transfer is audited but no longer counted as a retrieval, and STATIONS.md stops calling the vault unencrypted. Previously 3.33.0 — `station_vault_send`: a secret can pass to a session on another machine without entering a message, a file, or a transcript (migration 0022, ships with its code under Rule 4). Previously 3.32.0 — the station vault encrypts at rest, under a key outside the database and outside every backup (IDENTITY.md §11, decided and built the same day). Previously 3.31.0 — no surface is optional, on any path: the consent screen's per-surface checkboxes and `CheckScopeMix` are both deleted, so one credential can carry the knowledge base, messaging and a working identity at once. Previously 3.30.0 — IDENTITY.md §10 step 5, the LAST identity step: `space_id` and the `space` table are gone from both databases. **§10 is complete.** Previously 3.29.1 — the version stamp reaches station_me's workspace-creation path, which was the one call omitting it and the one call least able to notice. Previously 3.29.0 — IDENTITY.md §10 step 3: the binding-voucher chain is deleted. Previously 3.28.0 — the console can mint what the transports require. Previously 3.27.0 — the advertisement half of the identity work. Previously 3.26.0 — IDENTITY.md §10 steps 2 and 4: one identity across all three
surfaces, and a session that can get its own workspace. Previously 3.25.0 and 3.24.0 — the snapshot-encryption documentation defect, the
instruction re-fetch, and the outcome prompt. **Production is on 3.23.0**, verified by ken-prod-ops at 2026-08-25T17:05Z with both databases
row-identical and the whole `endpoint` table byte-identical; the fresh-session test passed on the
m600 machine, which named all three surfaces from its instructions alone before calling anything.
3.21.0 was verified at
2026-08-25T15:32Z against the live databases: healthz reports `Ken 3.21.0 linux/amd64`, both
`applied_at` timestamps unchanged, schema band clean on both, and — the strongest form this check
has taken — **the whole `endpoint` table byte-identical before and after, `last_seen_at` included**,
with row counts identical on both databases (2581 / 1624). They tested the "this release writes
nothing" claim by hashing table CONTENTS rather than counting rows, and this time no session polled
during the window, so there was no drift to attribute.

**Four items remain open across Batches 1–4** — the list is in those batches and nothing rounds it
up. Batch 6 is CLOSED; its last item, the endpoint migration, closed at **ep 6 only** by Vlad's
scope decision on 2026-08-19.

> **THIS HEADER WENT NINE RELEASES STALE — 3.13.0 through 3.21.0 — and is now DERIVED rather than
> promised.** It said "Released: 3.12.1 … Fifteen items remain open" while 3.21.0 was live and
> seven boxes were unchecked. Every one of those nine release commits edited this file.
>
> The two earlier recurrences are recorded below and each produced a stronger rule: Rule 2 was
> extended to cover the header, then extended again to cover prose. **Neither worked, and the file
> had already written down why — "a rule is not a mechanism" — along with the remedy for a third
> recurrence: "derive the line rather than write it."**
>
> `TestFinishingHeaderIsNotStale` (`internal/audit`) now fails the build when this paragraph does
> not name the newest release in `CHANGELOG.md`, or when the count it states does not match the
> unchecked boxes. The prose stays hand-written, because the *reasoning* around a number is worth
> writing; the two NUMBERS are checked against the sources that cannot drift. Rule 2 stands, and no
> longer has to be remembered.

### Previously

**3.9.0** (2026-08-17), verified on production 2026-08-17T18:32Z — the first
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

**Batch 4 was closed here**, and at the time Batch 5 was next and contained no code. Both have
since happened; see *Where we are today* above.

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

> **This file has broken its own Rule 2 three times, failed in a fourth way once, and a FIFTH way
was found on 2026-08-19 by an audit that checked every item and every narrative sentence against
the tree.**

**The fifth is the one worth changing behaviour over, because it is the only one Rule 2 as written
cannot catch.** The release commits for 3.10.0, 3.11.0, 3.12.0 and 3.12.1 all edited this file and
all obeyed Rule 2 — they ticked their items in the same commit as the work. None touched *Where we
are today*, so for four consecutive releases the first thing a human read here was "Released:
3.9.0 … Batch 5 is next", while Batch 5 had been decided and Batch 6 had opened, shipped three
items and closed. **The habit protects checkboxes; every failure in that audit landed in the
prose.** Rule 2 now says so explicitly.

The audit also found this file wrong by UNDERSTATING completion for the first time (Batch 1's
header claimed two open items against seventeen ticks), and found `[-]` doing double duty for
"dropped" and "deferred" — which is how `station_block` vanished into a closed batch once already.
`[>]` was added so the two cannot be confused again.
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

## Batch 1 — ship what is already done  `[x]`  *(3.6.0, verified on production 2026-08-14T22:02Z; the last two closed in 3.10.0)*

> **This header said "two items still open" until 2026-08-19, and all seventeen were ticked.**
> Noted because it is the first time this file has been wrong by UNDERSTATING completion — every
> previous failure overstated it. A stale "not done" is cheaper than a stale "done", and it is
> still a human reading a false sentence.

No schema change. Everything named here shipped in 3.6.0 — but the last two items below were
never done. **Both closed in 3.10.0**, and this sentence outlived them — it said the batch was not closed while the header above said `[x]`, which is the same self-contradiction one screen apart.

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
- [x] **The retired-switch sweep is finished** — **3.10.0**, *with two escapes found on 2026-08-19 and one of them fixed the same day*. `docs/DESIGN.md` carried two live
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
- [x] **Cut and release** — **3.6.0**, verified on production 2026-08-14T22:02Z. Superseded twice since: **3.7.0** (verified 2026-08-15T04:00Z) and **3.8.0** (released 2026-08-17, **verified on production 2026-08-17T16:03Z** — this line said "verification outstanding" until 2026-08-20 while the narrative above recorded the verification the same day; the file disagreed with itself 111 lines apart).

---

      **THE SWEEP MISSED TWO, AND THE REASON IS THE REUSABLE PART.** It was greppable-by-variable-
      name, so it found every mention of `KEN_COMM_ENABLED` and `KEN_STATION_ENABLED` and nothing
      that describes the vanished switch WITHOUT NAMING IT. Both escapes are that shape:
      `docs/MONITORING.md:72` told an operator, in the present tense, that a missing metric series
      *"means the surface was turned off"* — on the page they open when something looks wrong, and
      in a document this very item lists among those already fixed. Fixed 2026-08-19. The second is
      comm migration 0012's *"unless KEN_STATION_ENABLED is set"*, which stands deliberately:
      SQLite stores a migration's comment verbatim and editing an applied one makes a fresh install
      differ from an upgraded deployment. It is recorded in `PARKING-LOT.md` instead.

      **The lesson, since this is the fourth time this class has been declared finished:** a sweep
      keyed on a SYMBOL cannot find the prose that describes the symbol's behaviour. Those need a
      reader, or a differently-shaped search.

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
- [x] **Render WHO read a vault secret, or stop carrying the actor id** — **Unreleased**. The
      console now names the reader as `kind:name`, resolved through a join rather than shown as a
      bare integer, with an explicit string in all three locales for a read whose actor was never
      recorded. *Its checkbox did not move with its own commit (`666bf56`) — the specification that
      produced the fix touched no checklist file, so the work shipped and the mark did not. Caught
      by counting open items afterwards, which is the check Rule 2 is supposed to make unnecessary.*
      Sharper than the audit
      had it: `by_actor_id` IS recorded, and `StationVaultReads` DOES select it into
      `StationVaultRead.ActorID` — and `stations.html:405` renders `{{.Name}} · {{.Via}} ·
      {{.ReadAt}}` and drops it. So the data is collected, carried to the view model, and thrown
      away at the last step. The description now promises only what is shown; either render the
      actor (resolved to a name — a bare integer is not an identity a human can read) or stop
      selecting it.
- [ ] **Work through the remaining confirmed findings** in the appendix. **The prose class is NOT
      done** — that sentence stood here until 2026-08-17 and was wrong. Five findings are corrected
      (the Retire strings, and the four in `ea4443c`); the rest of the appendix's prose section is
      still live. Three were verified by hand on 2026-08-17; the first two are FIXED in Unreleased (F04 — `merge_into?` struck from §11.9 along with the two frozen strings, and the defer row corrected to `task_id`), and are kept below as the record of what was found:
      `docs/STATIONS.md:878` documents a `merge_into?` parameter on `station_task_add` that appears
      **zero** times in `internal/` — and the FROZEN live surface says it too
      (`internal/stationserver/stationserver.go:529`, `types.go:197` both offer to "merge"), so a
      doc-only strike would leave the promise pinned into every session at connect; and the same
      table gives `station_task_defer(ids[], until, reason)` while `taskDeferIn` takes a single
      `task_id`. *(A third example stood here — `stations.vault*` having 19 English keys and none in
      Spanish or French — and it is **no longer true**: all three bundles carry 19, translated in
      `0c0f687` (3.10.0). It was left standing for a day. The checklist tracking "text that asserts a
      control that does not exist" had become an instance of its own class; found by the
      2026-08-18 sweep, not by re-reading.)* What is left is therefore prose AND one-sidedness, not one-sidedness. The rule
      this batch confirms: **text asserting a control that does not exist is the class that
      propagates** — it is what put "off by default" on the public site, and it is a third of
      what was found here.
- [x] **Age EVERY open task, not just the human-blocked ones.** 3.6.0 added
      `oldest_blocked_on_human_days`, which covers only `blocked_on='human'`. ken-promo audited
      their own station and found a third staleness category neither of us had: a task that is
      **overtaken rather than stale** — not wrong, just no longer the point. Their example was
      "read the 1.5.1 and 1.5.2 promo briefs", created 2026-07-30, still accurate and pointless
      because Ken was already at 3.6.0 when promo found it on 2026-08-14. It was `blocked_on='self'`, so the field I shipped misses it, and
      `briefed_count` could never have caught it. Age since creation would have. **Shipped** as
      `oldest_open_task_days` on `station_me`: one ungated `MAX` over `created_at` across every
      open task, deferred ones included. It SURFACES only — nothing defers, closes or reorders on
      age, per the separate decline, and the §11.5 head slots are untouched.
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
- [x] **`attachment` is channel-shaped and must become scope-shaped** — **done 2026-08-24**,
      migration `0017_attachment_scope.sql` with its code, shipping together because they cannot
      be separated: the migration makes `scope_id` NOT NULL and the code is what writes it.
      **THIS ITEM UNDERSTATED ITS OWN FINDING.** "A file cannot be offered to a room" was true and
      not the worst of it: `comm_send{to_station}` — the path Ken's instructions call the simplest
      way to reach a peer — has NO CHANNEL ROW by design, so two linked stations could not exchange
      a file **at all**. Files worked on 1 of the 4 ways COMM addresses. The workaround (mint a
      pairing code) re-created the very channel the pair model exists to eliminate and split the
      conversation: the file in `ch:…`, the talking in `p:…`.
      **NOT A TABLE REBUILD.** Measured at the pinned driver: `ALTER COLUMN DROP/SET NOT NULL`
      works in place at SQLite 3.53.3 and is a syntax error at 3.50.4. Four ALTERs, a re-backfill
      and an index swap; indexes, comments and foreign keys all survive. That hard-pins comm.db to
      a ≥3.53 driver, which nothing else in the repo asserted — so it is a test now, and the test
      exercises the capability rather than parsing a version string.
      **The re-backfill was not optional:** every attachment written since 0010 carries `scope_id`
      NULL, because the seam was cut and nothing ever wrote it. Tightening without re-backfilling
      aborts the upgrade.
      **`enqueueLocked` is deleted** — 84 lines, and COMM goes from three message-insert paths to
      two. Its own comment recorded why that matters: the paths "drifted before — the shipped
      AckUpTo and Ack carried different recipient predicates for months."
      **Three defects fixed on the way, each invisible until a scope-shaped row existed:**
      `attachmentByID` INNER JOINed `channel`, so a room attachment would have reported *not found*
      for a row sitting in the table; the two `/comm` file counters INNER JOINed it too, so an
      operator would have read `Files=0` while the relay held bytes; and the grant check keyed on a
      recipient rowid frozen at offer time, which cannot express a room and does not exist for a
      pair. Authorisation is now scope membership **as of now**, so removing a station from a room
      stops its outstanding grants without revoking them one by one.
      **One rule survived only because a test named it:** the sender must not mint a DOWNLOAD grant
      for its own file. On a channel the sender was never the recipient row, so the old comparison
      excluded it for free; in a room the sender IS a member, so membership alone let it through.
      `TestGrantDownloadIsRecipientOnly` caught it within a minute of the rewrite.
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

- [x] **The "an endpoint cannot move between stations" invariant is one boolean on a column
      another tool clears.** `comm_bind` refuses when `station_id` is set; `comm_unbind` clears it
      and its own success note says "You can bind again later". So unbind-then-bind moves an
      endpoint between stations, and the tool performing the bypass advertises it. **The stated
      harm is also the wrong harm**: the error says a moved endpoint would carry the first
      station's unread mail across, which cannot happen — party keys are recorded at write time
      and no delivery row moves. What actually moves is channel MEMBERSHIP, because the seat is
      re-derived from the live binding. Enforce the invariant or delete the claim; leaving both is
      how the next reader concludes the live join is safe. Belongs with the credential model.

      **DONE 2026-08-25 — the claim is deleted, because enforcing it needs history this schema does
      not keep.** `comm_unbind` clears BOTH `station_id` and `bound_by_station_key_id`, so after an
      unbind nothing records where the endpoint has been; a real invariant is a schema change, it
      ships alone under Rule 4, and the identity work may delete the mechanism first. The refusal
      now states what actually happens — a channel seat is re-derived from the live binding, so
      rebinding elsewhere hands the new station the old one's seats — says plainly that the mail
      does NOT travel, and stops asserting a prevention it cannot deliver. `comm_unbind`'s
      "You can bind again later" is qualified in the same change, since one tool forbidding what
      the other offers was half the defect. Pinned by `TestTheRebindRefusalDoesNotClaimAnInvariant`,
      which fails on either old sentence returning.
- [~] **Backfill `channel.station_a` / `station_b`, then make the snapshot authoritative** —
      **THE ITEM AS WRITTEN IS WRONG, and an analysis on 2026-08-21 established why.** Split into
      three; the first has shipped.

      **The second half would break a class silently.** Making the snapshot authoritative today
      strands the *adopt-after-join* station: its successor keeps RECEIVING mail, because `Poll` is
      party-keyed, and loses the ability to reply, offer a file or ack — the exact
      poll-but-cannot-answer half-feature the live arm was added to prevent. `ChannelFor`'s own
      comment already says the snapshot cannot be made authoritative yet, and **no test pinned it**:
      the adoption test asserts only `Poll`, and the unbind test binds both endpoints *before*
      joining, so its snapshot is non-NULL and it would pass under a snapshot-only rule.

      **And 0008 is right that a later backfill cannot be correct.** NULL is ambiguous between "a
      link authorised this and revocation can no longer see it" and "no link was ever involved",
      and the only available source — the current binding — is the value 0008 warns may already
      have drifted. That information was never written down and cannot be recovered.

      - [x] **(A) Adopt the seats at BIND time** — *Unreleased*. Closes the class at its source,
        permanently and for future occurrences, with no migration: `comm_bind` fills a NULL seat
        snapshot for channels the endpoint already sits on. Safe where a later backfill is not,
        because the binding is being established in that transaction and cannot have drifted, and
        because it only ever fills NULL.
      - [ ] **(B) A migration for the residue, if any remains** — and it must be measured first.
        Only rows provably attributable, never the ambiguous ones. **Ships alone.**
      - [ ] **(C) Authoritative, only after (A) and (B)** — gated on a production count of what is
        still NULL with a station-bound seat. That is the blast radius, and the console shows one
        before every other destructive act.

      **A live defect found while proving this**, now fixed by (A): `OpenLinkedChannel`'s reuse
      lookup uses the same snapshot-only predicate, so approving a link between two stations that
      already had a NULL-pair channel open **opened a second one** — fragmenting the conversation
      its own doc comment promises not to fragment.
      `bcd60dd` consults the snapshot IN ADDITION to the live binding because the column is NULL
      on every pairing-code channel opened before migration 0008 — six of seven on a real
      deployment — so making it authoritative today would strand them. *Migration; ships alone.*
- [x] **The four pending counters do not agree, in the file that says they must not.** —
      **fixed; the release that ships it is named here at release time.** The predicate is now
      `pendingNotExpiredSQL` in `internal/comm/pending.go`, spliced by `pendingSQL(...)` into all
      SIX counts over queued deliveries: the four here plus `queuedForEndpoint` (message.go) and
      `PairsFor` (pair_send.go), which each held a byte-identical copy. In `PendingForEndpoint`
      the clause sits in the delivery JOIN, never the WHERE — in the WHERE it drops the channel
      row entirely, turning "0 waiting" into a missing row, and a mutant that moves it there
      fails. Three tests: the four counters must agree about an expired-but-unswept message
      (positive control asserts all four see it while it is live); a source-reading test fails
      when a new COUNT over queued deliveries does not ask for the clause; and the contradiction
      is asserted at `comm_channels` itself, because it is a property of the assembled result.
- [x] **The hearsay rule tells every session to record the disposable identity** — **Unreleased**.
      The frozen instruction block said to attribute knowledge from a peer to "the sending
      endpoint". Endpoint rows are deleted by the idle sweep (`7 d` by default); the knowledge base
      has no TTL, so the attribution named a row that no longer exists — and three sessions of one
      correspondent became three unrelated opaque ids. The block now names the STATION
      (`from_station_name` + `from_station_id`, both already on `messageView`), and says what to
      record — and how to qualify it — when the sender holds no station. *Reaches NEW conversations
      only: instructions pin at connect, so every session already running keeps the old rule until
      it ends.*
- [x] **The loop in that same block never mentions `comm_bind`** — **done 2026-08-25**, as part of
  a much larger finding than this item described. The block did not merely omit `comm_bind`: **it
  was never delivered.** The MCP client truncates the instructions field at 2048 characters, and
  COMM was sending 8095 — 25% arrived. This item was recorded weeks before ken-prod-ops reported
  the same wall from the other side, and it was filed as an omission because nobody measured the
  block against what a client accepts. All three surfaces now deliver in full, and per-tool rules
  live in the descriptions of the tools they govern, which are not truncated.
- [x] **Keys missing from both `messages_es` and `messages_fr`** — **done 2026-08-24. Both locales
      are now at parity: 702 keys each, zero missing.**
      **The figure in this item was stale.** It said 89; the real count when I measured was **67**,
      because keys had landed in all three locales since it was written. Recording that because an
      unchecked count in a to-do item ages exactly like the `blocked_on` flag on a task does.
      **63 had content and were translated. The other 4 were EMPTY IN ENGLISH** —
      `rl_enabled.help`, `rl_token_burst.help`, `rl_lockout_sec.help`, `login_lockout_sec.help` —
      so the gap there was on the English side, not the translators'. Written in the Go registry
      and generated out from it.
      **AND I DID IT THE WRONG WAY FIRST, which is the part worth keeping.** I hand-edited
      `messages.properties`, not knowing `internal/i18n/i18nsync` existed: English settings text is
      GENERATED from `settings.Fields`, and each translation carries a `#@src` hash of the English
      it was made from. The drift test caught me on the first run, naming both the wording I had
      let diverge and every unstamped entry. Redone through the tool.
      **On stamping in bulk:** the tool deliberately has no `-stamp-all`, and its own comment says
      the friction IS the feature — a stamp claims someone read the new English and confirmed the
      translation still says it. I stamped all 64 in a loop, and the claim is true for each only
      because I authored every one of them from that English in the same session. **A session
      facing stamp failures it did not author must not do this**; that is precisely the wall of
      failures the missing flag exists to stop someone waving through.

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
- [x] **`station_block` — DECIDED 2026-08-24: DELETE.** Vlad's call, taken on the evidence below
      plus one fact these passes did not have. The three functions are gone; the `DROP TABLE`
      ships as its own release under Rule 4.
      **The fact that decided it:** the deny is not merely unwired, it is **unenforceable from
      where the sends happen**. `comm.Store` holds handles to comm.db alone — no ken.db handle —
      and comm.db has no block mirror, while links and rooms each have one. So "wire it" is a
      cross-database projection change: a new comm.db table, wholesale-replace, boot rebuild, and
      recipient filtering in two send paths, plus a console surface. A slice, not an afternoon.
      **And Vlad's own rule already answered it**, written in `DECISIONS-BATCH5.md:176` and
      unnoticed since: *"If P2 is chosen with the link requirement intact, the default is not
      permissive and `station_block` stays optional."* P2 shipped with the link requirement
      intact — `pair_send.go` calls `areLinked` and refuses with `ErrNotLinked`.
      **What the deletion costs, stated rather than glossed:** the capability is NOT superseded.
      Revoking a link kills only `to_station`; rooms and `to_room:"all"` carry no link predicate,
      and `AddRoomMember` requires no link to exist. To stop one pair an operator must still
      remove a station from a room, costing it that room's other relationships. That gap stays
      open and is recorded in `PARKING-LOT.md` rather than lost with the code.
      **Original finding, kept for the record:** Verified empirically
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
      `BlockStationPair`, `UnblockStationPair` and `BlockedPairs` had
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
- **`station_block` was DEFERRED TO BATCH 5** — *and Batch 5 closed without deciding it, which
  is exactly the failure the deferral was worded to prevent.* Recorded here unchanged because
  the note said "with a date on it" and the date passed unremarked; the item then had to be
  rediscovered by a sweep looking for something else entirely. **Decided 2026-08-24: delete.**
  The deferral reasoning was right that the real question was about the addressing default —
  and Vlad had already answered that question in the same document (`DECISIONS-BATCH5.md:176`),
  which nobody connected back to this item for a week.

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
- [x] **`Store.MarkNoticesSeen` has zero production callers** — **done 2026-08-24**. It was the
      explicit "I have read my notices" call that migration 0011's own comment argues at length is
      unusable here, because MCP tool lists pin at conversation start; `NoticesForPoll` supersedes it
      by promoting the previous poll's mark automatically. Deleted with no migration, and the two
      tests were REWRITTEN through `NoticesForPoll` rather than having the call removed — which
      raised coverage rather than lowering it, because they had been asserting "notices do not repeat
      forever" against a mechanism production never invoked. Suppression now takes two polls, which
      is what a session actually does.
- [x] **The second generation did not inherit the first's invariant** — **3.9.0**. Migration 0010 rebuilt the
      `entry_version_immutable` trigger for the sole purpose of freezing `via_comm`, stating that
      "a mutable marker could simply be UPDATEd away — which would defeat the point". Migration 0018
      then added `via_comm_kind`, which is written and read like its sibling and is **not in the
      frozen set**. *Migration; ships alone.*
- [x] **One function, two copies, only one hardened** — *unreleased*. `internal/comm`'s `Migrate`
      pinned a connection, set `foreign_keys=OFF` outside the transaction for the whole run and ran
      `foreign_key_check` afterwards — with the measurement that bought it in the comment. The
      `internal/store` runner did not, through nineteen migrations. Both now call
      `internal/dbmigrate.Run`; ken.db needed it MORE, not less (`station` has eight cascading
      children, `entry` three). Verified that applying all nineteen with enforcement off yields an
      identical schema and a clean `foreign_key_check`. No migration; nothing under `migrations/`
      was touched.
- [-] **The nightly backup does not cover `comm.db` at all.** **DECIDED by Vlad, 2026-08-24: not
      doing it** — *"probably we don't need this given the restructuring of the login we are trying
      to finalize."* Recorded rather than closed, because the reasoning is conditional: comm.db is
      the expendable database by design (`BACKUP.md` says outright **"Do not add `data/comm/` to
      either tier"**), and if the identity work ever makes a comm.db row unrecoverable rather than
      re-derivable, this comes back. Left visible for that reason.

      *Original finding:* `cli_backup.go` and
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

**THAT SENTENCE IS A FORECAST AND FOUR OF ITS FIVE CLAIMS HAVE NOT HELD. Read it as intent, never
as status.** Corrected 2026-08-19, both halves this time — an earlier correction fixed only the
Batch 3/4 half and left this one standing, which is the same mistake one paragraph apart.

| blocker | forecast | actual, 2026-08-19 |
|---|---|---|
| H1 no agent-initiable private path | Batch 5 | **DISSOLVED** — P2 shipped in 3.12.0 (`internal/comm/pair_send.go`). It is still listed as a live blocker above; that list is what is stale, not this row. |
| H2 file exchange is channel-only | Batch 3 | **open** — `grep -c scope internal/comm/file.go` is still `0` |
| H3 retroactive revocation | Batch 4 | dissolved |
| H4 `station_block` is dead | Batch 4 | **DISSOLVED 2026-08-24** — deleted. Deferred into Batch 5, which closed without deciding it; decided on the finding that the deny is unenforceable from comm.db, which has no ken.db handle and no block mirror |
| H5 unbound endpoints have no address | Batch 5 | **open, and it will not be dissolved by this list.** Batch 6 closed at ep 6 only; five endpoints stay unbound by Vlad's decision. |

**That sentence is a forecast, and as of 2026-08-18 two of the three have not happened.** **H2** is
still open — `attachment` is still channel-shaped, and `grep -c scope internal/comm/file.go`
returns `0`, so the `scope_id` seam migration 0010 cut and backfilled has never been used; the item
sits unticked in Batch 3 because Vlad ruled on 2026-08-17 that it ships alone. **H4** is closed as of 2026-08-24 —
`station_block` is deleted; it had been deferred into Batch 5, and Batch 5 closed without
deciding it, so it survived two batches by being neither wired nor dropped. Read the sentence as *what these batches are for*, not as a status. (Found by
the 2026-08-18 sweep; indexed in `PARKING-LOT.md`.)

> **Vlad recorded a target architecture on 2026-08-18 — see
> [TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md).** It changes NOTHING here, deliberately: the
> agreement is still no new features until this list is closed. It is written down so that
> finishing does not lose it, and because three items below now point somewhere he has said he
> does not want to go. **That tension is recorded there and resolved after this list, not during
> it.**

> **Migration 0016 shipped separately, 2026-08-20** — `idx_message_sender` on
> `message(sender_party, kind)`. `NoticesFor` runs on every `comm_poll` and filtered an unindexed
> column, so each poll scanned the whole `message` table: measured 0.511 → 37.710 ms/call between
> 1k and 100k deployment messages with the caller's own inbox constant and no notices returned.
> It is the coupling the 2026-08-03 poll-cost task recorded, recreated in a new query when 3.4.0
> made notices derived. **Ships alone under Rule 4.**

## Batch 6 — what the Batch 5 decisions make into work  `[x]`  *(closed 2026-08-19)*

The decisions are made; this is what they cost. **The order is load-bearing.**

- [x] **Migrate the unbound endpoints onto stations** — **DONE TO THE SCOPE VLAD SET, 2026-08-19.**
      **ep 6 only.** His words: *"The others I can live without them until Ken is redesigned (which
      should happen soon enough)."* OPERATOR work, not code — the voucher is
      redeemed by the session itself and the console is Vlad's. **Step-by-step procedure:
      [RUNBOOK-ENDPOINT-MIGRATION.md](RUNBOOK-ENDPOINT-MIGRATION.md)** (2026-08-19), verified
      against the tree rather than the docs. Its headline finding is the answer to why this has
      felt laborious: **no console control binds an endpoint to a station** — there is no route,
      no form and no i18n string for it, so every one of the five is a two-party dance between the
      console and the session on that machine. **Shape settled 2026-08-18**, and
      it is smaller than the original six suggested: two were revoked on 2026-08-17, and
      `endpoint.label` turned out to name the PROJECT, which neither of us had been reading.

      | endpoint | station | action |
      |---|---|---|
      | ep 6 | `quest-infra` | **BIND** — key exists, no console step |
      | ep 14 | `proxmox-servers` | **BIND** — key exists, no console step |
      | ep 13 | `rb5009-config` | **NEW STATION** (Vlad, 2026-08-18) then bind |
      | ep 18 | `runway-prod-admin` | **NEW STATION** (Vlad, 2026-08-18) then bind |
      | ep 10 | `collector-proxy-dev` | **REVOKE** — duplicate, 0 seats / 0 sent / 0 received |

      **ep 6 is first and it is not a tidy-up.** `quest-infra` has 47 station tasks and a live
      key, 83 deliveries addressed to `e:6` and **zero to `s:JiJm1FZK9Afs08u0`** — its station
      identity and its messaging identity have never been joined. Zero is not "nobody wrote to
      it", it is "nobody CAN": a live instance of the 2026-08-13 rooms defect, unnoticed in
      production for three weeks because nobody happened to put it in a room. Found by
      ken-prod-ops reading the label column.

      **The set is NOT closed, and that is a design constraint rather than a caveat.** `ep 18`
      was created two minutes before ken-prod-ops read the table. Anything built on "every
      session holds a station" must handle a session that registers MID-FLIGHT — which lands
      directly on D: an authenticated station key with no endpoint yet needs a defined answer,
      not an assumption that the world was migrated first.

      Also settled: one per-machine comm token, one endpoint per project, so the posture means
      **one station per PROJECT**, not per machine.

      **WHAT LANDED**, verified by ken-prod-ops against the live database:

          endpoint_id   pCKgl1bYYLtJSdN5VhCfFS
          station_id    JiJm1FZK9Afs08u0  (quest-infra)
          bound_by_key  ey1RghLoiRsZ      — the ai-actor key, not a console-minted one
          bound_at      2026-08-19T22:19:07.310Z
          control       ep 13 and ep 14 still station_id NULL

      It bound first try because the actor check was verified BEFORE the instruction was written
      rather than after it failed. At bind time ep 6 held 87 deliveries received, 94 sent and 6
      open channels — the 2026-08-18 figures in the table above had already moved.

      **THE COST OF THE SCOPE, recorded once so it is not rediscovered as a surprise.** ep 10, 13,
      14, 18 and 19 stay unbound. They remain in the shape where, if the exposed comm token
      `jMl4ZNH4q73E` is ever revoked, their channels ORPHAN PERMANENTLY instead of passing to a
      successor — binding is what converts an orphan into an inheritance. Nothing about it is
      urgent and it needs no code; it is a consequence of a decision, sitting in the open.

      **And the set grew again while this was being planned**: `ep 19 runway-dev` registered after
      the table above was written — 1 open seat, 7 sent, 6 received. Third time this month. **"The
      set is not closed" is not a caveat about this migration, it is a property of the system**,
      and any future plan shaped like "migrate the N endpoints" inherits it.

      **A trap for anyone following [RUNBOOK-ENDPOINT-MIGRATION.md](RUNBOOK-ENDPOINT-MIGRATION.md)**,
      which was not Ken's: the session's first attempt was refused by its own client-side
      permission classifier before `station_binding_voucher` ran, and it correctly declined to work
      around it. **Allow `station_binding_voucher` AND `comm_bind` together, before starting** — a
      block between the two calls burns the five-minute voucher.
- [x] **B — move the endpoint pair to a request header** — **3.11.0** (`85538ec`). Closes the transcript exposure
      ken-prod-ops measured. Needs the per-call `withCaller` wrap that `/comm/mcp` lacks; the
      other two surfaces already have it. Keep the arguments accepted-and-ignored for one release
      so running sessions are not broken.
- [x] **P3 — approving a link creates the pair conversation** — **3.11.0** (`d593434`). Nearly free, no new agent verb.
      **Ship the better approval surface with it**: ken-prod-ops recorded that Vlad approved two
      link requests on 2026-08-13 *without being told what he was approving*. The consent gate
      works; the consent was uninformed, and that is the half worth fixing.
- [x] **P2 — `comm_send{to_station:"X"}`, authorised by the existing link** — **3.12.0** (`98579f1`). A new scope
      prefix beside `ch:` / `r:` / `b:`, a `membersOfScope` arm, and reply/sequence numbering per pair.
      This is the one that makes `comm_open_channel` redundant, which is slice 7's actual goal.
      Shipped as specified: `p:<a>|<b>` with the ids sorted, members derived from the scope's own
      name, one ascending sequence per pair.

      **It needed a migration the plan did not anticipate** — comm **0015**, `station_link_mirror`.
      `comm.Store` has no `ken.db` handle by construction, so the approved link has to be projected
      to be checkable inside the writing transaction, exactly as room membership is.
      **Rule 4 was read against its stated reason** — *"a rollback must never discard behaviour
      fixes along with a data rewrite"* — and this migration performs no data rewrite: it creates one
      empty table no prior binary references, so a rollback over it discards nothing. That is a
      different case from Batch 3's `attachment` restructure, which rewrites a live table. **Flagged
      for Vlad rather than assumed silently**; if he wants it split, the schema half ships alone
      first and the code follows.

      Authorising off the CHANNEL row was considered and rejected: 0008 already snapshots the
      authorising pair, and 3.11.0 opens a channel at approval — but that channel needs both
      stations staffed, and `proxmox-servers` proves that case is real. The link is the decision;
      the channel is one way of spending it.
- [>] **D — station key authenticates `/comm/mcp`** — **DEFERRED into the design analysis,
      2026-08-19 (Vlad's ruling).** Not dropped and not built. Its stated precondition was *every
      session holds a station*, which stopped being true when he stopped using Station; and it adds
      a SECOND static-credential path to `/comm/mcp` in the same week §4b of
      [TARGET-ARCHITECTURE.md](TARGET-ARCHITECTURE.md) recorded that a session should hold no secret
      at all. It is the analysis's to settle alongside the rest of the credential story.

      **One of its two prerequisites landed without being noticed**: `/comm/mcp` now re-derives its
      caller per call — item B's `withEndpointCred` wrap did it in 3.11.0
      (`internal/commserver/commserver.go:1113`).

      **The other prerequisite was never a live defect, and this checklist's own wording caused a
      false alarm on 2026-08-19.** The sentence read *"`retired_at` is checked only by
      `AuthenticateStationKey`, so 'Retire this key' **would** silently not sever messaging"* — a
      CONDITIONAL about D's world. It was read as present tense, reported to ken-prod-ops as a
      console control that lies, and retracted the same hour. What is actually true, verified in
      three places: `RetireStationKey`'s contract states *"Retire severs the STATION surface and
      spares COMM; Revoke severs both"*; `IsStationKeyRevoked` tests `revoked_at` alone, matching
      it; and `stations.key_retire_help` says *"COMM endpoints it already bound keep working. Use
      Revoke instead only when you also want those severed"* — **in English, Spanish AND French**.
      Doc, code and UI agree. The prerequisite is real only IF D ships, because D makes the station
      key the messaging credential, at which point sparing COMM stops being a clean split and
      becomes a lie.
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
