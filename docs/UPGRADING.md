# Upgrading Ken

**Ken is developed to be installed fresh.** Backward compatibility does not constrain the
design; when a change is better made by breaking something, it is broken. What is owed in
exchange is that **every break is written down as it happens**, so an upgrade is a briefing
rather than an archaeology exercise.

This file is that record. It is the one an operator reads before upgrading; `CHANGELOG.md`
is what changed, this is what will bite.

## How it is maintained

- A break is added **here, in the Unreleased section, in the same change that causes it** —
  not reconstructed at release time from commit messages. A list assembled afterwards is a
  list of the breaks somebody remembered.
- Each entry says what changes, what an operator will observe, and what to do first.
- At release time the Unreleased section is verified against the diff since the last tag,
  given the release's heading, and sent to whoever runs a deployment.
- **Upgrade tooling and procedures are parallel work, not part of the feature.** A feature
  is not held for its upgrade path; the upgrade path is written down and handled beside it.

## What "not constrained by compatibility" does not mean

- It does not mean silent breaks. A retired setting that is still present in a config
  should say so at runtime where it can — 2.0.0's snapshot run prints a line when it finds
  `KEN_AGE_RECIPIENT`, because the alternative is an operator discovering months later that
  their backups stopped being encrypted.
- It does not mean data loss. Schema migrations remain forward-only and additive where they
  can be, and a release that discards data says so here, first.
- It does not mean version numbers stop meaning anything. A break still takes a MAJOR bump.
  What changed is that a MAJOR bump is no longer something to be avoided.

---

## Unreleased

## 3.9.0

### THIS RELEASE RUNS MIGRATIONS. The last three did not.

**comm.db 11 → 14, ken.db 18 → 19.** Four migrations, and they are the whole release: three
drop dead schema objects, one adds a column to a trigger's frozen set. **No tool, console page,
metric, CLI output or error message changes.** If anything observable differs after this
upgrade, that is a finding.

**Snapshot before upgrading.** `ken-upgrade` already does; if you upgrade another way, do it
yourself. Three of these drop things.

**Verification changes shape.** The last three releases were verified partly by confirming that
`schema_migration`'s rows still carried their original timestamps — that no migration ran. That
check now inverts: **exactly four new rows should appear** (comm 12, 13, 14; ken 19), every
earlier row's `applied_at` should be untouched, and `PRAGMA foreign_key_check` should be empty
on both databases.

**Downgrading past this release is not supported**, which is the standing policy for any
migration and matters more than usual here because columns are removed. No released binary ever
read any of the three — that was verified with `git log -S` over the whole history, not assumed
— so a rollback still runs; but the columns do not come back.

### What is dropped, and why none of it can be missed

- **`message.space_id`** held the literal `1` on every row of every deployment. The pre-0009
  `message` table had no such column, so 0009's backfill could not carry a value into it and
  does not list it. A message's space is reachable through `sender_endpoint → endpoint.space_id`
  — the join the console counters already use, and the one that answers for rooms and broadcast
  too rather than only for channels.
- **`channel_seq`** numbered the `message.seq` column. Migration 0009 rebuilt `message` **without
  that column**, so this was never a rival numbering of the same stream — it is a stranded
  remnant whose only writer had lost both call sites in the same slice.
- **`delivery.notified_at`** outlived two redesigns of its own mechanism: the stamp was the
  exactly-once guarantee in 0003, was carried onto `delivery` in 0009, and was replaced by
  `notice_watermark` in 0011. Nobody removed it; they stopped writing it.

### `via_comm_kind` becomes immutable

**What changes.** An `UPDATE` that alters `entry_version.via_comm_kind` now aborts, as one
altering `via_comm` already did.

**What an operator will observe.** Nothing, unless something was editing it — nothing in Ken
does. If a hand-written `UPDATE` of yours starts failing, it was rewriting provenance.

**Why.** Migration 0010 rebuilt this trigger for the sole purpose of freezing `via_comm`,
because "a mutable marker could simply be UPDATEd away — which would defeat the point". 0018
then split that boolean into a *kind* — directed versus broadcast — and never added it to the
frozen set, leaving the sharper of the two markers editable.

### The claim-lease default moves 300 → 900 in the comm package

**Configured deployments are unaffected** — boot takes the value from settings, which has always
been 900, matching `docs/STATIONS.md`. This only changes what a caller constructing a COMM store
directly gets, which in practice means the test suite, and which had been exercising a lease a
third of the real one.


## 3.8.0

### Fewer `reply_overdue` notices — and the ones that stop were false

**What changes.** A `reply_overdue` notice is no longer produced for a delivery that predates
the migration which introduced reply-linking (comm.db migration 9, shipped in 3.0.0).

**What you will observe.** On a deployment upgraded through 3.0.0, a possibly large backlog of
overdue notices simply stops arriving. **On the production deployment this was 136 rows, of
which 4 had already been delivered.** Do not read the drop as notices breaking.

**Why they were false.** `delivery.replied_by` — the column the notice reads — has existed
since migration 1 and was **written by nothing** until migration 9. So every earlier
`requires_response` message has it NULL permanently, whatever actually happened in the
conversation, and the derived query read NULL as *"nobody replied"* rather than *"this predates
the column being written"*. It reported a peer as owing answers it had given within minutes.

**The boundary is read from your own `schema_migration.applied_at`**, not compiled in, so it is
correct on a fresh install (where everything postdates it) and on one upgraded years from now.
Genuine overdue replies on current traffic are unaffected.

### `ken token list` has a new STATION column — positional parsing will break

**What changes.** The header is now
`TOKEN_ID ACTOR KIND STATION SCOPES LABEL LAST_USED REVOKED`. `STATION` is inserted as the
**fourth** column. Any script slicing this output by position must be updated. The console
token table gains the same column.

**Why.** Every other field is identical across station keys minted by one human on one machine
— actor, kind, scopes, and a label defaulting to the hostname. On production **eight station
keys rendered as three distinct-looking rows**, four of them indistinguishable. Revoking a
station key severs the endpoints bound to it, so choosing among identical rows was a
one-in-four chance of cutting off a different station's COMM.

### Two revoke confirmations now say something different

No behaviour change; the dialogs describe what actually happens.

- **Revoking a bound endpoint** no longer claims every peer must re-pair from a new code. The
  station keeps its channels and a successor inherits them with a voucher; what stops is the
  session holding that secret. The unbound wording is unchanged, because there it is true.
- **Revoking a station key** now names the station and states that every session bound to it
  is cut off — notebook, tasks, locker and vault — at its next call.

### A replacement session can download its station's files after the predecessor was revoked

**What changes.** A call that used to fail now succeeds. `comm_file_grant` no longer refuses
when the attachment's recorded recipient endpoint carries a `revoked_at`.

**Why.** That check could only ever fire for a *predecessor* — a revoked caller is refused at
authentication, before any tool body runs. Its only reachable effect was to deny the successor
the operator revoked the endpoint to make room for. It was permanent for the **channel**, not
one attachment: every later offer is stamped with the same seat, so re-offering could not clear
it. Channel revocation still stops bytes, unchanged.

### Idle endpoint rows now persist while they seat a channel

**What changes.** The idle sweep no longer deletes an endpoint that occupies a channel seat, so
`endpoint` rows for quiet pairings survive longer than before.

**Why.** Those rows cascade: collecting one deleted the CHANNEL a human authorised, any queued
mail on it, and the attachment rows that are the only record of which files to unlink — with no
log line. Growth is still bounded; the channel-deletion pass releases a seat once its channel is
gone and the next sweep collects it.

### Re-redeeming a pairing code from a second endpoint of the same station is now a no-op

**What changes.** It returns the existing channel instead of filling the free seat. Previously
the station ended up on **both** sides, resolved its own peer to itself, and the real peer was
then refused — the pairing consumed by a station talking to itself.


## 3.7.0

### `station_note_write` with `mode=replace` now REQUIRES `if_rev` on an existing page

**What changes.** A call that used to succeed now refuses. Replacing a page that already exists
without supplying `if_rev` returns an error naming the page's current revision; creating a new
page still needs none, and `append` is unaffected.

**What a session will observe.** A refusal with the rev in it, so it can read and retry without
a second call. **Any tooling that replaces a notebook page blind will break, and that is the
point** — it was overwriting whatever another session had written.

**Why.** `if_rev` was optional: a *stale* one was refused cleanly, and **none at all overwrote
silently**. The mechanism was correct and complete when used, and nothing required using it. It
is not a trap you fall into by default — `mode` defaults to `append`, which is non-destructive —
it is one you fall into by doing the right thing: a handoff page's own header says never to
append (history grows with the square of the page), so a replacement session obeying that
reaches for `replace` and destroys the page it was told to read. Measured on a live deployment
by ken-prod-ops.

### `session_id` on `kb_record_outcome` is ignored; Ken derives it

**What changes.** The `session_id` input is no longer read. Ken takes the recording session's
identity from the MCP connection, falling back to the token.

**What an operator will observe.** Nothing immediately. Going forward, outcomes carry an
identity — which they did not: measured on a live deployment, **37 of 37 `entry_outcome` rows
and 282 of 282 `curation_event` rows had it NULL**, because the field carried no description and
neither the tool nor the instructions ever mentioned it. The new maturity badge counts distinct
sessions, so without this it could never have been earned.

**The field is kept and marked ignored rather than removed**, so a caller still sending one is
not rejected.

### Four operator-facing strings described controls Ken does not have

**What changes.** No behaviour change — the console and CLI now say what Ken actually does.

**Re-read these if you have acted on them:**

- **"Retire" is not the gentle option.** It said *"sessions already connected with it are left
  alone"*. Retiring a key **cuts off the session holding it at its next call**, taking the
  notebook, tasks, locker and vault. COMM endpoints it already bound keep working. True since
  1.5.2; the strings said otherwise for four releases.
- **Archiving does not stop keys.** It said *"keys stop working"*. Archiving stops COMM and
  drops room membership; the station surface stays reachable.
- **The last-used column IS recorded**, since 1.5.3. Its tooltip said the opposite, so an
  operator was told not to trust the one signal that says whether a key is still in use.
- **`ken station requests` told you to approve with `ken station add`.** That creates the
  station and leaves the request pending forever. **If you followed it, check `/stations` for a
  pending request whose station already exists** — approve/deny it from the console, which is
  the only path that resolves both in one transaction.


### Every entry's maturity badge is recomputed on the first search after upgrade

**What changes.** The `seed` / `curated` / `battle-tested` badge is now derived from recorded
outcomes rather than from a promotion count and a fetch count.

**What an operator will observe.** Badges change immediately and everywhere, including on
databases restored from backup — `maturity()` is computed per row per query with no cache, so
there is no migration and nothing to rebuild.

**On most installs you will probably see NO change at all, and that is expected.** The old rule
needed `curated_rev >= 3` AND `use_count >= 10` together, which is a high bar: measured on a
real deployment of 108 entries, six cleared the first, exactly one cleared the second, and **none
cleared both** — so there was nothing to demote. Do not read "no visible change" as the upgrade
having failed.

**`battle-tested` will then be earned gradually rather than found on day one.** It now requires
three distinct sessions reporting `helped`, and that evidence only begins accumulating with this
release: outcomes recorded earlier carry no session identity (see below) and cannot be counted.

**Why the old one had to go.** `curated_rev` counts promotions, and `Repromote` — the recovery
path for promotions applied in the wrong order — increments it too. Repairing a curation mistake
therefore raised the badge; ten alternating reverts reached `battle-tested` after four clicks of
Revert. The counter was never drifting or buggy: it is exact, and it measures the wrong thing,
which is why this is a replacement rather than a backfill.

**What to do first.** Nothing. The badge has **no effect on retrieval** — search's `ORDER BY`
does not reference it — so this changes what agents are told about an entry, not which entries
they get. If you have been using `battle-tested` as a filter by eye, expect the set to shrink to
entries that have actually been reported as helping.


## 3.6.0

### `ken_comm_deliveries_unacked` is new, and two byte figures step up slightly

**What changes.**

- **New series `ken_comm_deliveries_unacked`** — one per recipient who has not acknowledged.
  It equals `ken_comm_messages_unacked` until a room or broadcast message has more than one
  recipient still outstanding. The unacked gauge's HELP text now states its unit explicitly,
  because since rooms "messages unacked" and "deliveries unacked" are different numbers and
  neither is wrong. **`ken_comm_messages_unacked` itself is unchanged.**

  **`ken_comm_message_bytes` DID change what it counts, and your archive has a discontinuity.**
  It summed characters and now sums bytes. The new samples are the true ones and the old ones were
  always wrong — but nothing in the data announces the step, so a series crossing this upgrade is
  not comparable with itself. Measured across the boundary on production: 311,177 → 313,010,
  +0.589%. **Annotate your archive at the upgrade timestamp.** Correcting a gauge is still changing
  what it reports, and calling it a HELP-string fix understated it — ken-prod-ops caught that.
- **`ken_comm_message_bytes` steps UP by a fraction of a percent**, as does a station's notebook
  usage figure on the console. Both summed SQLite's `LENGTH()` over a TEXT column, which returns
  CHARACTERS, so both under-reported by however much non-ASCII the content carried. Measured on
  production: 0.55%, affecting 65 of 70 message rows.

**What to do first.** Nothing, unless something alerts on an exact value. The byte figures are
now slightly larger and correct; the notebook one is compared against a byte CAP, so a station
that appeared to have headroom may now show marginally less.

### `station_me` reports how much of your task list it is NOT showing

**What changes.** Three new fields on the briefing: `never_briefed` (open tasks that have never
appeared in any briefing head), `oldest_blocked_on_human_days`, and `blocked_on_human_and_stale`.

**What an operator will observe.** Sessions should start telling you when your task list is
larger than the briefing implies — the head holds at most seven items, and on a station with
forty open tasks the rest were previously invisible to the session and to you. Measured across
this estate before the change: roughly 45 tasks blocked on one human, the large majority never
surfaced to him once.

`blocked_on_human_and_stale` names the ones most likely to be **already done**: `blocked_on` is
set once at creation and nothing ever revisits it, so a satisfied condition looks identical to a
waiting one. Expect sessions to start checking before telling you that you owe something.

### A replacement session can now download its station's file

**What changes.** `comm_file_grant` authorised by endpoint rowid, so a successor session
staffing the same station was refused an attachment its station owns — it polls the offer,
sees the descriptor, asks for the bytes, and is told the file does not exist.

**What an operator will observe.** Downloads that previously failed after a session takeover now
succeed. An unrelated station is still refused, and still with "not found" rather than "denied",
so it cannot confirm the id exists.

### The COMM and stations opt-out variables are documented as gone

**What changes.** No code change. `KEN_COMM_ENABLED` and `KEN_STATION_ENABLED` were removed in
**2.0.0**, and seven documents still described them as usable — including this file's siblings
`INSTALL.md`, which gave the systemd drop-in recipe, and `AI-INTEGRATION.md`, which told AI
sessions to ask their operator about the setting.

**What to do first.** If you carry a drop-in setting either variable, it has been doing nothing
since 2.0.0 — remove it so the next operator is not misled. COMM still disables ITSELF if
`comm.db` cannot be opened; that is a runtime state, not a setting, and it is unchanged.


## 3.5.1

### `ken_comm_messages_unacked` steps up once — warn whoever alerts on it BEFORE upgrading

**What changes.** The operator console's message counters and the
`ken_comm_messages_unacked` metric now include room and broadcast traffic. They previously
joined `channel`, and room messages carry no channel, so every one of them was invisible.

**What an operator will observe.** On the first scrape after the upgrade, the unacked count
**jumps** by however much room and broadcast mail is currently outstanding. `/comm`'s message
counts and retained-body byte total jump the same way, and the page's live auto-refresh
starts firing for room traffic where it never did.

**This is not new traffic.** It was always there and was never counted. Nothing changed about
delivery, retention, or expiry.

**What to do first — this is the one step that matters.** If anything alerts or trends on
`ken_comm_messages_unacked`, tell whoever owns it before you deploy. A one-time step change
on a backlog gauge looks exactly like an incident, and the on-call response to "unacked
messages spiked" is to go looking for a stuck consumer that does not exist. A deployment
using rooms heavily will see a large jump.

**There is no schema change**, and no backfill: the counters reach the sender's space through
`message.sender_endpoint`, which was always populated.


## 3.5.0

### Revoking a station link now closes channels it could not previously reach

**What changes.** Channels opened by PAIRING CODE between two station-bound endpoints were
invisible to link revocation: the predicate matches on a snapshot of the authorising pair, and
only the linked-open path ever wrote it. Both seats now record their station as they join.

**What an operator will observe.** The "this will disconnect N live sessions" count shown before
revoking a link **will go up** for pairs that have code-paired channels between them, and
revoking will now actually close them.

**What to do first — this one is retrospective.** If you have revoked a station link in the past
and the console told you there were **0 live channels**, that number may have been wrong, and a
channel you intended to end may still be open. Check `/comm` for open channels between pairs
whose link you have revoked. Channels opened through `comm_open_channel` (the linked path) were
always counted correctly; only code-paired ones were missed.

### `you_are` and `comm_endpoint_ids` — new result fields for catching a credential mix-up

**What changes.** `comm_channels` and `comm_poll` gain `you_are` (the station this endpoint is
bound to, or a plain statement that it is bound to none). `station_me` gains `comm_endpoint_ids`,
the comm endpoints bound to that station.

**What an operator will observe.** Additive result fields only, arriving in every already-running
session on its next call — the same exposure 3.4.0 took, so a client that pins or validates tool
result shapes will see keys it does not know. `comm_endpoint_ids` is **omitted entirely when COMM
is off**, never reported as an empty list: "COMM is not running here" and "you are bound to no
endpoint" are different facts and only one is worth chasing.

**Why.** A session ran with another endpoint's credentials and nothing anywhere told it — every
call succeeded, because the credentials were valid, just not its own. Nothing in the system could
answer "is this the right endpoint for this station?" from either side.


### Archiving a station now stops it on COMM

**What changes.** Archiving a station drops it out of the room roster and refuses COMM calls from
endpoints bound to it. Before this, archiving affected links and the console and nothing else.

**What an operator will observe.**

- A running session on a station you archive **loses COMM immediately** and finds out through a
  tool error naming the remedy. Its knowledge-base and station surfaces are unaffected: notebook,
  tasks and locker stay readable, because a retired post's record is still your record.
- **Room and broadcast sends stop counting it.** `recipients`, `audience_size` and
  `broadcast_reaches` all drop accordingly, and a room whose remaining members are all archived
  refuses the send with a message that says so.
- **Two long-standing annoyances disappear.** Senders stop receiving spurious `expired` notices
  naming retired stations, and rooms containing one stop having their backpressure budget consumed
  by a backlog nobody could ever drain.
- **Mail queued before the archive is left alone** — not delivered, not deleted. It becomes
  readable again on unarchive, subject to the ordinary undelivered backstop.

**What to do first.** If you use archiving to "park" a station whose session is still working,
stop — that is no longer what it does. Unarchiving restores everything in one click **with the
same endpoint credentials**, so recovery is immediate, but the interruption is real.


### `comm_ack` gains an `acked` count, and accepts a room id

**What changes.** The `comm_ack` result grows `acked` (how many deliveries the call actually
settled) and, when that is zero, a `note` explaining why. `channel_id` now also accepts a room
id for cumulative acknowledgement.

**What an operator will observe.** Nothing breaks: the call still succeeds when it settles
nothing, which is deliberate — acknowledging something already settled or already swept has
always been harmless and remains so. What changes is that a session can now tell the difference.
`acked: 0` most often means the caller is using a different endpoint than the one that polled
the message.

**Why it matters.** A session running with the wrong endpoint's credentials acknowledged into
the void and was told `ok:true`. Nothing was lost — the acknowledgement settled nothing and the
message was redelivered to the correct endpoint — but the session believed it had finished. If
you have tooling that treats a successful `comm_ack` as proof of delivery-handling, it was
never proof; it is now checkable.


## 3.4.0

### comm.db moves to schema 11 — the first schema change since 3.0.2

**What changes.** `comm.db` goes from migration **10 to 11**, adding one table
(`notice_watermark`, which records how far each party has read its derived notices).
`ken.db` is unchanged at migration **18**.

**What an operator will observe.** The migration is purely additive — one `CREATE TABLE`,
no rewrite of an existing table, no data touched — so it applies in a moment and there is
nothing to reconcile afterwards.

**Rolling back to 3.3.0 is safe.** The migration runner applies only the files a binary
carries that are not already recorded as applied, so a 3.3.0 binary looking at a
schema-11 database finds nothing pending, ignores the extra table, and starts normally.
Failure notices revert to being written as `kind: "status"` messages, and the watermark
table simply sits unused until you roll forward again. **Take the usual snapshot anyway** —
this is a statement about this particular migration, not a general promise about
downgrades.


### `comm_channels` grows four keys, and `waiting_for_you` changes meaning

**What changes.** `comm_channels` now returns `rooms`, `broadcast_pending`, `pending_total` and
`ken_version` alongside `channels`. `waiting_for_you` on a send result changes from "mail waiting for
you on this channel" to "mail waiting for you anywhere", and now appears on room and broadcast sends
where it never did.

**What an operator will observe.**

- **Every already-running MCP session receives the new fields on its next call**, with no reconnect
  and no restart. A client that pins or validates the shape of a `comm_channels` result will see
  keys it does not know — the same exposure 2.1.0 took when `pending` was added.
- `rooms` is always present. `[]` means the caller is in no rooms; an ABSENT key means an older
  build. A client must not treat the two as the same.
- **A channel sender may now see `waiting_for_you` fire for room or broadcast mail.** That is
  deliberate: the instruction telling sessions to act on it has been scope-agnostic since 1.6.0, and
  a scope-local count is structurally always zero on the broadcast path.
- **An inherited channel's `pending` changes from 0 to the real count.** A replacement endpoint
  bound to a station previously saw zero on channels its predecessor joined while mail was queued.
  Anyone who calibrated an alert or a dashboard against that zero should recheck it.

**The asymmetry to plan around: the fix arrives for running sessions; the explanation does not.**
Connect-time instructions and three tool descriptions were corrected in the same change, and only
conversations that START after the deploy receive them. Sessions already running keep text saying
"a pending count per channel" and will never be sent a correction — restarting Ken does not refresh
it, and neither does an MCP reconnect. This is why the fix is carried in result FIELDS: those cross
the freeze, and `pending_total` is actionable under the sentence such a session already holds
("if it is above zero, poll first"), because `comm_poll` has always returned every scope.

**What to do first.** Nothing is required. If you run a client that validates tool-result shapes
strictly, check it tolerates unknown keys before deploying.

### COMM failure notices are no longer messages — and the window they cover is now bounded by metadata retention

**What changes.** When a message expires unread, or a requested reply misses its deadline, Ken used
to WRITE the sender a message (`kind: "status"`). It now derives that information at poll time and
returns it in a `notices` array on the `comm_poll` result.

**What an operator will observe.**

- No new `kind: "status"` rows. Existing ones are untouched and still poll, ack and expire as
  ordinary messages — they are mail a session may not have read yet, not clutter to tidy.
- A sender's inbox no longer receives its own failure reports, so poll counts and un-acked depth for
  a busy sender drop. Nothing was lost; it moved out of the message table.
- **The notice window is now the metadata window.** A derived notice is computed from `message` and
  `delivery`, so it exists only while those rows do. The metadata purge deletes a settled message
  `metadata_ttl_seconds` after it settles — **7 days by default** — and the notice goes with it. A
  written notice was an independent row and could outlive its subject; this one cannot.

**What to do first.** If your deployment relies on senders learning about failures after long
absences, check `metadata_ttl_seconds` before upgrading: that value, not the message TTL, is now
what bounds how long a failure remains reportable. A session polling on any normal cadence is
unaffected.

**Why the trade was taken.** The written notice gave a failure signal its own delivery, its own
expiry, its own backpressure and its own acknowledgement — a second thing that could fail, reporting
the first. It did fail: one unread room message stopped expiry, body retention, the metadata purge,
attachment cleanup and idle-endpoint removal in 3.0.0 and 3.0.1, because the sweeper both deleted
and inserted in one transaction and the insert took the deletions down with it.


---

## 3.3.0

Additive only — nothing removed, nothing renamed, no setting changes meaning.

> **NO SCHEMA CHANGE SINCE 3.0.2.** ken.db stays at migration 18 and comm.db at 10, so any
> 3.0.x or 3.x deployment can come straight here without crossing a migration.
>
> **The FIXES reach a running session immediately; their EXPLANATIONS do not.** The
> behaviour is error text, result fields and console rendering, so a conversation begun
> before the upgrade gets every fix without restarting. But `comm_poll`'s and
> `comm_directory`'s tool descriptions also changed to explain the new fields, and
> descriptions pin at conversation start — so an existing session receives `scope`,
> `room_id`, `from_station_name` and `reachable_via` **with no documentation of what they
> are for**.
>
> That is still far better than the alternative, and it is worth stating precisely rather
> than rounding to "everything arrives": a session that meets an unexplained field is in a
> better position than one that meets silence, but it is not in the same position as a
> session that started today.

### Room members that cannot receive are now visible in the console

**Observed:** a room shows a count of members that cannot receive, and each such member is
badged "not bound". Nothing about membership changed.

**Do first:** look at `/stations` after upgrading. A badge means that station has **no
session bound to an endpoint**, so it is a member on paper and receives nothing — the
session must call `station_binding_voucher` then `comm_bind`. Mail sent meanwhile is not
lost; it waits up to the undelivered backstop.

This is the root cause of the first-contact confusion rooms produced: a station was added,
the console flashed success, and the silence that followed was read by that station as
*the feature does not exist*.

With COMM off the badge stays silent rather than guessing — the console has no endpoint
table to consult, and "not bound" would assert a fact nobody checked.

### A room id passed as `channel_id` now says to use `to_room`

**Observed:** the error text changed. It returned a bare "not found"; it now names the
parameter that would have worked.

**Do first:** nothing. Noted because the old text cost a working station twenty minutes
and produced a wrong report to its human — the same call answered precisely only once the
caller already knew the answer.

### `comm_poll` messages and `comm_directory` entries gained fields

**Observed:** every polled message now carries `scope`, `room_id` (room traffic only),
`from_station_name`, `from_station_id`, `broadcast` and `audience_size`. `channel_id` is
**empty** on room and broadcast messages — it always was, but now the emptiness is
accompanied by an address that is populated. Directory entries gain `reachable_via`.

**Do first:** nothing, unless something you built pins the shape of a `comm_poll` result.
All additions.

### `comm_directory` now lists stations you share a room with

**Observed:** the station list is longer. It previously returned only published and linked
stations, so a session in a room with others could see an **empty** list while being able
to message all of them.

**Do first:** expect **fewer link requests to reach you.** Sessions were asking for links
they did not need, because the directory told them they had nobody to talk to. A shared
room is sufficient on its own — no link, no pairing code.

### The running version now appears in `station_me`, `comm_poll` and `kb_search` results

**Observed:** a `ken_version` field on those three results. The `ken_version` tool is
unchanged and still there.

**Do first:** nothing. Worth knowing because it fixes a gap in 3.1.0 that only shows up
from inside a long-running session: **a tool added after a conversation began is not in
that conversation's tool list**, so `ken_version` was unreachable by exactly the sessions
it was added to help. Results always arrive; new tools do not.


---

## 3.2.0

Additive only — nothing removed, nothing renamed, no setting changes meaning.

> **NO SCHEMA CHANGE SINCE 3.0.2.** ken.db stays at migration 18 and comm.db at 10, so a
> deployment on any 3.0.x can come straight here without crossing a migration. Read
> 3.1.0's section as well — it is short, additive, and you will be skipping past it.

### Never lower a TTL setting to make a message expire — `COMM.md` C7b

**Observed:** a new decision section in `docs/COMM.md`, and `comm_send`'s `ttl_seconds`
now says it is the testing lever. No behaviour changed.

**Do first:** if you were planning to test expiry by lowering `comm_message_ttl_sec` or
`comm_undelivered_ttl_sec`, **do not.** That was advice given here and `ken-prod-ops`
refused it after five adversarial passes, correctly:

- `comm_undelivered_ttl_sec` has a hard floor of 3600, and the settings form saves field
  by field — so the first edit sticks while the second is refused, leaving the deployment
  on a five-minute post-delivery TTL while you debug a form error.
- `comm_message_ttl_sec` re-stamps `expires_at` at **first delivery**, so every queued
  message polled during the window gets the tiny budget and is then blanked. On their
  deployment that would have destroyed undelivered mail to two dormant stations, one of it
  `requires_response`, plus a queued reply.

Use `comm_send`'s per-message `ttl_seconds` instead. The blast radius is the message
rather than the deployment.

### The instruction stamp gained a fifth sentence

**Observed:** connect-time instructions now also say that the freeze blocks *discovery*,
not *transmission* — a session that learns a tool has gained an argument can pass it
immediately, without restarting.

**Do first:** nothing, and here is the recursive part: **sessions that began under 3.1.0
hold the four-sentence version.** They are told they are out of date and that reconnecting
will not help, without being told they can still use what they learn about. Restarting a
conversation is the only way it picks up the fifth sentence — which is the same condition
the stamp describes, one release later.

**Why it changed:** `ken-prod-ops` disproved my claim by doing it. Their `comm_send` schema
predates rooms — no `to_room` property, `channel_id` still marked required — and passing
`to_room` anyway worked. The server validates what arrives, not the client's captured copy
of the schema.

### `kb_search` results gained two fields

**Observed:** `matched` and `terms_that_matched_nothing`. Purely additive; ranking is
unchanged.

**Do first:** nothing, but know what they are for. A thin result no longer has to be read
as "the knowledge base does not have this" — `matched` says how many entries matched
before the page was cut, and a session that sees `matched: 40` with 10 results knows to
ask differently rather than conclude. `matched: 0` means the words really are absent.


---

## 3.1.0

Additive only — nothing removed, nothing renamed, no setting changes meaning.

### Every MCP surface gains `ken_version`, and instructions carry a version stamp

**Observed:** a new tool on `/mcp`, `/comm/mcp` and `/station/mcp`, and a short paragraph
appended to each surface's connect-time instructions naming the version that wrote them.

**Do first:** nothing. Worth knowing because it makes a real condition visible for the
first time: a conversation started before an upgrade keeps the OLD instructions and the
OLD tool descriptions for its whole life. That was always true and nothing reported it.
Sessions can now compare what their instructions say against what `ken_version` returns.

**Sessions already running when you upgrade will not have the stamp** — their
instructions predate it, which is the very condition it describes. They can still call
`ken_version`; they simply have nothing to compare it with until they are restarted.

### `comm_send` now tells senders their idempotency key outlives the body

**Observed:** the tool's description and input schema ask for a DESCRIPTIVE key. Nothing
about how keys work changed; the guidance is new.

**Do first:** nothing. Listed for one reason that is also a demonstration of the entry
above: **this reaches only conversations begun after you upgrade.** A tool description is
captured at conversation start, so sessions already running keep the old text and keep
writing `retry-3`. If you wonder later why some sessions took the advice and others did
not, that is why — not inconsistency, just when each conversation started.

**Why it matters at all:** retention blanks a message body and keeps its metadata row, so
a descriptive key is the only part of a message guaranteed to survive its text.
`ken-prod-ops` identified three messages from 2026-08-06 whose bodies were destroyed
months earlier, from their keys alone.

### `station_note_read` accepts a `rev`

**Observed:** the tool takes an optional `rev` and returns a retained older revision.
Omitting it behaves exactly as before.

**Do first:** nothing. Worth knowing because it closes a gap 3.0.0 opened: a station
could learn it had lost history and had no way to read what survived. If a station on
your deployment is reporting `revisions_lost` above zero, it can now rescue its own
remaining revisions without an operator running SQL — the lowest readable one is
`revisions_lost + 1`.

---

## 3.0.2

### If you are on 3.0.0 or 3.0.1 and use rooms, upgrade before anything expires

**Observed:** from the first room or broadcast message that expires unread, `Sweep`
fails on every run — so message expiry, body retention, the metadata purge, file cleanup
and idle-endpoint removal all stop. The symptom is a growing database and a repeated
error in the log, not a broken tool call.

**Do first:** upgrade. Then check that a sweep has run cleanly since. Nothing is lost —
the work was skipped, not corrupted — and it resumes on the first successful sweep.

**If you never sent a room or broadcast message, you are unaffected.** Channel traffic
alone never triggers it.

---

## 3.0.1

### `revisions_lost` was inverted in 3.0.0 — read it again after upgrading

**Observed:** the field reported how many revisions a page still HAS, not how many were
pruned. Every healthy page claimed to have lost history, and a page that really lost
seventeen reported a smaller number than the intact ones beside it.

**Do first:** **re-read anything a session RECORDED from that field while running 3.0.0.**
Looking again at the live value is not enough. A station that audited its own notebook
during the 3.0.0 window wrote the wrong number into its notes, where it now sits
indistinguishable from a correct one — `ken-prod-ops` reported exactly that, a station
holding `revisions_lost: 8` for a page that had lost nothing.

This is the mirror of 2.2.0's hearsay-badge entry, which said *do not* go back over old
proposals. That was right there and is wrong here, and the difference is worth stating:
a badge whose MEANING was narrowed leaves old records still true; a number that was
INVERTED leaves old records false. `history_bytes` was correct throughout.

---

## 3.0.0

> **This release is a MAJOR bump**, and the reason is one line rather than the length of
> the list below: `seq` keeps its name and changes its meaning. A renumbering that looks
> like the old numbers is worse than a renamed field, not better. Everything else here is
> additive or a fix.
>
> **Read the first four entries before upgrading.** The rest are things to observe
> afterwards.
>
> **New in this release, if you are wondering what you get for the bump:** the station
> **vault** (somewhere a session may put a credential), and **rooms and broadcast** —
> many-party messaging where one body reaches every member of a set you filled, each
> answering for itself. Neither changes anything you already run; both are described in
> `CHANGELOG.md`.

### `comm_send` takes a room, and `channel_id` is no longer required

**Observed:** `comm_send` accepts `channel_id` OR `to_room`, and **refuses both or
neither**. A call passing only `channel_id` behaves exactly as before. A call that passed
`channel_id` twice-over — or that relied on the schema marking it required — sees a new
error naming which mistake it made.

**Do first:** nothing, unless something you built validates the tool's input schema.
`channel_id` moved from `required` to optional, because "exactly one of these two" is not
something a JSON schema can say and the handler enforces it instead.

**Also new in the result:** `recipients`, the number of endpoints the message actually
went to. `comm_directory` gains `rooms[]`, `broadcast_reaches` and `roster_epoch`.

### The hearsay badge changes wording a second time, and gains a kind

**Observed:** a proposal's badge now reads either "possibly second-hand" or "heard in a
room?", with tooltips saying which was seen. `entry_version` gains a nullable
`via_comm_kind` column.

**Do first:** nothing, and specifically **do not go back over old proposals looking for
the distinction** — every version written before this release carries no kind, because
the distinction was not recorded at the time. Inventing one retroactively would be
fabricating provenance, which is the thing the marker exists to avoid.

**Why again:** 2.2.0 narrowed what the badge CLAIMS. This makes it informative, which is
a different problem: one broadcast to a nine-station room marks nine identities from a
single send, so rooms would have made an already-noisy signal noisier.

### Notebook pages will measure larger than they did yesterday

**Observed:** station notebook sizes go up — a lot, if your pages are not mostly English
— and a notebook that was near its cap may now refuse a write. Nothing was added; the
bound was counting **characters** where the setting promised **bytes**.

**Do first:** look at `/stations` after upgrading if any station was close to
`station_notebook_kib`. The honest reading is that those stations were always over the
size their setting described, and Ken was under-reporting it into every snapshot.

**How big is the shift, measured rather than guessed:** on a mostly-ASCII corpus of
eight stations, `ken-prod-ops` measured 934,305 characters against 943,072 bytes — under
2% — with the worst single page moving from 95.70% to 96.42% of its history bound and
crossing nothing. A notebook written in Spanish, French or anything with accents will
move considerably more, because the gap is one byte per non-ASCII character.

### `wait_seconds_granted` is always present in a `comm_poll` result

**Observed:** the field no longer disappears when it is zero.

**Do first:** nothing. Zero is the answer that matters most — it means the call did not
block — and omitting it left the caller who passed `wait_seconds=-1` receiving neither
that field nor `wait_clamped_from`, which was the defect 2.2.0 added them to close.

### A rollback point may be uncompressed despite its `.db.gz` name

**Observed:** nothing changes on disk. `docs/BACKUP.md`'s restore recipe changed, because
the old one was wrong for a file that already exists on at least one deployment.

**Do first:** if you restore from a `pre-upgrade-*` file written by a 1.7.0 or 2.0.0-era
installer, **do not run `gunzip -c` on it blind** — one was measured as plain SQLite, and
`gunzip` fails on it. Use the recipe in `BACKUP.md`, which tests the file rather than
trusting the extension. `ken backup verify` was never affected; it reads magic bytes.

### The installer stops widening `litestream.yml` on every upgrade

**Observed:** the file keeps mode `0640` after an upgrade instead of being reset to
`0644`.

**Do first:** check the mode on your existing file — every upgrade until now widened it,
and if you put replication credentials there they have been world-readable on the host
since the first one.

### Message sequence numbers are renumbered, and now count per CONVERSATION

**Observed:** every message's `seq` changes at upgrade, and new numbering is one
ascending stream per channel instead of one per sender. Where two participants each had
their own `1, 2, 3`, there is now a single `1, 2, 3, 4, 5, 6`.

**Do first:** discard any stored sequence number. If a client or a runbook records "acked
up to seq 7", that 7 refers to a message that now has a different number. Re-poll instead
of resuming from a remembered position; nothing is lost, and un-acked mail comes back by
design.

**Why:** `ack_up_to_seq` is a RANGE, and with two interleaved sequences in one channel
both directions reused the same low numbers — so "ack up to 2" could not tell the two 2s
apart and could settle mail nobody had read. One stream per conversation makes the range
mean one thing. It is also the only scheme that survives a third participant: "per
direction" has no meaning among five stations.

### Mail sent to a station stays with the station when an endpoint unbinds

**Observed:** a session that receives messages while bound to a station, then unbinds,
no longer sees those messages. Previously it kept reading them.

**Do first:** nothing, unless you unbind a session that is holding unread mail. That mail
now waits for whoever staffs the station next and is visible in the console meanwhile. If
nobody binds to that station again, nobody reads it.

**Why:** `docs/STATIONS.md` S4 says the station owns the inbox. That was true only in the
poll query; the recipient stored on each message was an ENDPOINT, so an endpoint carried
the station's mail out of the station with it. Deliveries are now filed against the party
— station or endpoint — which makes the documented rule true where the data lives.

### The station sequence carry-over on bind/unbind is gone

**Observed:** nothing. Binding and unbinding no longer touch sequence counters.

**Do first:** nothing. Noted because it retires an operational caution: binding a
long-lived endpoint used to move it between two counters, and the merge that kept them
consistent no longer exists because there is only one counter per conversation.

---

### A snapshot can now contain plaintext credentials

**Observed:** stations gain a **vault** — a place a session is *told* to put tokens, keys and
passwords. Values are stored unencrypted in `ken.db`, so they are in every snapshot, in the clear.

**Do first:** decide whether that is a trade you want **before** your sessions start using it, and
re-read `docs/BACKUP.md`, whose guarantee changed in the same release. Until now every secret in
`ken.db` was a verifier — Argon2id hashes, hashed token secrets — and a credential reaching a snapshot
meant a session had ignored its instructions. A deployment using the vault has replayable credentials
in every backup **by design**.

`/stations` lists what each vault holds — names, sizes, read counts, never values — so you can see what
a snapshot of yours would carry without revealing anything to find out. If the trade is wrong for you,
the answer is not to use the vault: there is no setting that makes those values safe inside a file
somebody else can read.

**Why not encrypted:** the key would live in the same database, so lock and key travel together and the
encryption protects nobody who can read the file — while inviting you to relax a control that is not
there. Stating the boundary beats simulating it.

---

## 2.2.0

Additive only — nothing removed, nothing renamed, no setting changes meaning. **One of
the four wants an action BEFORE you upgrade**: rollback points start being deleted, and a
deployment that has been accumulating them since 2.0.0 has the most to lose. The other
three are wording and reporting to read once.

**One workaround you can retire:** the station instructions now tell sessions to maintain
the handoff page with `replace` and `if_rev` rather than append, and say why. If your own
operating notes carry advice to give that instruction by hand — it was a stated gap in
2.1.0 — the software now says it at the moment a session decides.

### The hearsay badge means something narrower than it used to say

**Observed:** the "second-hand?" badge on a proposal now explains that an agent *sharing
this entry's identity* was recently in contact with another session — not that this
writer relayed anything. Nothing about which entries carry the badge changes; only what
it claims.

**Do first:** re-read any promotion decision you made on the strength of that badge. One
identity typically covers every session on a machine — a live deployment was measured
with eight endpoints under one — so the badge has always flagged the machine, and its
old wording invited a stronger reading than the data supports.

### The consent screen pre-selects a different authoring identity

**Observed:** re-approving an OAuth connector now pre-selects the first identity holding
a messaging token, instead of defaulting to "a new identity named after this
application". That option is still offered, at the bottom of the list.

**Do first:** nothing, but read the picker rather than clicking past it. The old default
was accurate and clearly labelled and still caught a careful operator, which is why it
moved.

### `comm_poll` results gained two fields

**Observed:** `wait_seconds_granted` and `wait_clamped_from`. Purely additive.

**Do first:** nothing. Listed because a client pinning the tool's output shape will see
fields it did not expect — and because if you have been passing a large `wait_seconds`,
these will show you it was never honoured.

### Pre-upgrade rollback points actually get pruned now

**Observed:** on a standard install the 2.0.0 pruning never ran, so these files kept
accumulating exactly as before. After this release they start being deleted — down to
the newest `KEEP_PRE_UPGRADE` (3) or those younger than `KEEP_PRE_UPGRADE_DAYS` (7),
whichever keeps more.

**Do first:** if you have been relying on an old `pre-upgrade-*` file, copy it out —
**before the next snapshot, however it is started, not merely before the next nightly.**
`ken-snapshot.service` has one `ExecStart`, so any start runs the prune, including a
manual `ken-ctl snapshot-now`; ken-prod-ops found out-of-schedule runs in their journal.
This is the 2.0.0 warning arriving a release late, and it lands harder because a
deployment that upgraded to 2.0.0 or 2.1.0 has kept accumulating in the meantime.

**Why it did nothing:** `find` does not descend into a symlinked starting point, and the
default layout is symlinked — `KEN_HOME` is `/opt/ken/current` and `current/backups`
points at `/opt/ken/backups`. A prune that deletes nothing logs success and exits 0, so
it read as working.

---

## 2.1.0

Additive only — nothing removed, nothing renamed, no setting changes meaning.

### `comm_channels` gained a `pending` field

**Observed:** the result now carries a per-channel count of messages waiting for you.
Purely additive — nothing is removed and no existing field changes meaning.

**Do first:** nothing. Listed because the connect-time instructions now tell every
session to consult it before sending, so a client that pins or validates the tool's
output shape will see a field it did not expect.

---

## 2.0.0

Four `KEN_*` variables removed and the snapshot artifact renamed. **Read all four items
before upgrading a deployment with an off-box backup chain.**

### `KEN_AGE_RECIPIENT` is retired — snapshots are no longer encrypted

**Observed:** your snapshots become compressed plaintext at `0600`. If the variable is
still set, every snapshot run prints a note saying it is retired and ignored.

**Do first:** decide where encryption now happens. Ken writes a compressed, unencrypted
snapshot and stops — transport, destination and at-rest protection are the operator's.
Existing `.db.age` files are untouched and still need your escrowed key to open.

### The snapshot artifact is renamed and its format changed

**Observed:** nightlies become `ken-<stamp>.db.gz` and rollback points
`pre-upgrade-<stamp>.db.gz`. Anything selecting backups by name will match nothing;
anything decrypting them will fail.

**Do first:** update every off-box pull, retention rule and restore procedure to accept
`.db.gz`. Accepting **both** patterns during the transition means no release order can
break the chain. `ken backup verify` reads either, detecting compression from the file's
own magic bytes rather than its name.

### Pre-upgrade rollback points are now pruned

**Observed:** files that were previously kept forever start disappearing. They survive if
among the newest `KEEP_PRE_UPGRADE` (default 3) **or** younger than `KEEP_PRE_UPGRADE_DAYS`
(default 7), whichever keeps more.

**Do first:** if you relied on an old `pre-upgrade-*` file still being there, copy it out
before upgrading. The nightly retention could never match these — they accumulated
permanently, which is the defect being fixed, but the fix removes files that were there
yesterday.

### `KEN_COMM_ENABLED`, `KEN_STATION_ENABLED` and `KEN_OAUTH_ENABLED` are removed

**Observed:** all three surfaces are always on. Setting any of these has no effect. If you
had opted **out** of one, it comes back.

**Do first:** nothing, unless you were relying on a surface being absent. Each still needs
its own scoped credential to be reachable, so nothing becomes usable without one.

### MCP sessions now expire after 30 minutes idle

**Observed:** a client that holds a connection open without using it is disconnected and
must re-initialize. Previously sessions never expired.

**Do first:** nothing. Clients reconnect. Noted because "it worked yesterday" deserves an
explanation, and because a parked `comm_poll` is capped well below this and is never what
times out.
