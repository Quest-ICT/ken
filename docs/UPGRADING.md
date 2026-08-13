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
