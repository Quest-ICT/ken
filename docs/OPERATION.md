# Ken — operations manual

> **Authored by the production-operations session (`ken-prod-ops`)** against `v3.6.0` on
> 2026-08-14, and **partially re-verified against `v3.9.0` on 2026-08-17** by the development
> session before commit.
>
> **PARTIALLY. Read that word literally, because this document's whole subject is stamps that
> claim more than they cover.** Every claim was re-checked against the tree — 262 of them — and
> 50 were wrong by v3.9.0. The high-severity ones are corrected here, along with the sections
> an operator acts on under pressure: §2.3's vault defaults, §2.4(c) and (h), §3.2, §4.1, §5.1
> and §6. **A number of lower-severity corrections are NOT yet applied**, and the sections
> below still carry `v3.6.0` stamps where that is the honest statement of what was verified.
> Do not read an unqualified sentence in this document as re-verified at v3.9.0 unless its
> section says so.
>
> **What the verification stamp actually covers.** Every setting key, default and bound below
> was read from `internal/settings/settings.go` at tag `v3.6.0`. Every SQL recipe was checked
> against the migration files at that tag. Behavioural claims were re-derived from source by
> five independent readers with an adversarial verifier behind each, and the disagreements
> were resolved by reading the code rather than by majority. Where a claim comes from running
> a deployment rather than from the source, it says so.
>
> **The previous revision of this draft was written against v1.5.5 and was wrong in
> fifty-seven places by v3.6.0** — including four of the five subsections of §2.4, both SQL
> recipes in §6.2, and the meaning of the health check. That is recorded here rather than
> quietly fixed, because the failure mode is the point: none of those statements *looked*
> stale. A version stamp is the only thing that distinguishes a verified document from a
> confident one, which is why this one says exactly what it covers.

This document is about **running** Ken. Installing it is [INSTALL.md](INSTALL.md); this one
starts the moment the service is up and never stops. It covers the two surfaces an operator
has — the web console and the `ken` command — what every setting does, which combinations
are traps, and what to look at when something feels wrong.

---

## 1. Two surfaces, and which one to reach for

**The console is the main method for any operation. The CLI is a last resort.**

That is a deliberate ordering, not a description of which was built first. The console
validates what you type, shows you the current value beside the one you are changing, and
cannot be typo'd into starting a second server. The CLI can do none of that.

| Task | Surface |
|---|---|
| Every setting | Console → **Settings** |
| Review, promote, reject proposals | Console → **Proposals** |
| Browse, search, read entries | Console → **Browse** / **Search** |
| Stations, notebooks, tasks, **rooms**, station vault | Console → **Stations** |
| Endpoints, channels, secret rotation | Console → **Inter-session comms** |
| Mint a knowledge-base token (`read` / `write-draft` / `propose`) | Console → **Tokens** *(or CLI)* |
| Mint a **`comm`** token (add `comm-file` for file exchange) | **CLI only today** — `ken token add --actor comm-<name> --scopes comm,comm-file`. The console form offers only the three knowledge-base scopes and **silently discards anything else**, so a comm token cannot be minted there. *Being fixed: the console will mint these too.* |
| Mint a station key | Console → **Stations** *(or CLI)* |
| Create the *first* user | Browser wizard at `/setup`, or CLI |
| Take or verify a snapshot from cron | CLI — it is a scheduled job |
| Bulk import, embedding backfill | CLI — long-running batch work |

**Corrected in this revision:** earlier text said token and station-key minting were
CLI-only "because the secret is printed once, to a terminal". That has not been true for
some time — **the console mints both and renders the secret once on the page**, deliberately
rendering instead of redirecting so the secret survives to the response. The one-time
property is preserved either way; the *surface* claim was wrong.

**Rooms can only be created in the console.** There is no agent-facing create path. A
session can use a room it has been added to and can do nothing whatsoever to bring one into
existence. That is doing real containment work — see §2.4(f).

### 1.1 Console pages

```
/                 dashboard      counts, recent activity
/search           search         query the corpus
/browse           browse         the curated corpus
/entry/{slug}     entry          one entry, its versions and history
/proposals        review queue   what agents have proposed; promote or reject here
/stations         stations       stations, notebooks, tasks, keys, ROOMS, station vault
/comm             inter-session  endpoints, channels, pairing codes, secret rotation
/tokens           tokens         API tokens and their scopes
/settings         settings       everything in §2
/login  /lang  /oauth/authorize              supporting pages
/setup            first-run      REQUIRES a one-time token printed at startup
/healthz          liveness       plain text, no auth — see §4.1 for what it does NOT mean
/metrics          Prometheus     access-controlled; 404s rather than 403s when refused
```

**Three machine endpoints, each taking a differently-scoped credential:**

```
/mcp           knowledge base        agent token (read / write-draft / propose)
/comm/mcp      inter-session comms   a token carrying the `comm` scope
/station/mcp   station identity      a station key
```

Earlier text listed only `/mcp`. An operator debugging "the agent cannot reach X" needs all
three, because the failure is almost always a credential scoped for one of them being
pointed at another.

**`/setup` is not open.** While no admin exists every other path redirects to it, but it
requires a one-time token generated at startup and printed to the log. That token is what
stops a first-run window from being an open door.

---

## 2. The console

### 2.1 How a setting actually takes effect

Three layers, in order:

```
compiled defaults  (settings.DefaultsFromEnv — 13 of 47 read an env var; the rest are literals)
        ↓  overridden by
app_setting rows   (written only by the Settings form)
        ↓  folded into
a live Snapshot    (swapped atomically)
```

**The database only holds what you have overridden.** If you query `app_setting` directly
you will see a handful of rows, not the full list. A setting absent from that table is not
unset — it is running on its compiled default. **The console is the only place that shows
the effective value.** The database can answer "have I changed it?" but never "what is this
set to?".

**There is no such thing as a restart-level setting at v3.6.0.** Of 47 fields, **45 are
`Live: true`** and the other two — `tls_mode` and `tls_email` — are `ReadOnly: true`,
display-only, set through `KEN_TLS` / `KEN_TLS_EMAIL` in the unit. The console form still
contains a "restart to apply" branch; at v3.6.0 it is unreachable dead code. If you are
waiting for a restart to make a saved setting take effect, you are waiting for nothing.

**Only 13 of the 47 fields read an environment variable at all.** Every COMM field, every
Station field, and `login_max_fails` / `login_lockout_sec` / `session_ttl_hours` are
literals in code — so there is no `KEN_*` variable to set them from the unit, and looking
for one is a dead end.

**Corrected in this revision:** earlier text said "whether COMM exists at all" was a
restart-level choice governed by `KEN_COMM_ENABLED`. **That switch was deleted in 2.0.0.**
COMM is core and unconditional. See §2.3.

*ACME domains are live, with an asymmetry that matters — see §2.3 (TLS).*

### 2.2 Saving

**The form writes every setting, not only the ones you edited.** The save handler walks the
entire field registry and, for each field, either writes the row or removes it — deciding by
comparison with the **compiled default**, never with the value currently stored. So every
setting that differs from its default is rewritten on every save, whether or not you touched
it. (First noticed on a running deployment, where changing four values left all thirteen
persisted rows carrying one identical timestamp; later confirmed in the handler.)

So `app_setting.updated_at` means **"last saved"**, not **"last changed"**. Do not use it to
reconstruct what you modified, and do not conclude from a fresh timestamp that a value moved.

### 2.2.1 A group name that is not on the render list disappears

The form renders groups in a **hard-coded order**, not registry order:

```
Rate limiting → Login → Session → Network → TLS → Curation → Inter-session comms → Stations
```

Within each group, fields follow the registry. The reference below matches.

**A field whose group is not on that list renders nothing at all, silently** — and the
consequence is worse than invisibility. The save handler builds its form map from
`settings.Fields` rather than from what was rendered, so an unrendered field is processed on
every save as though it had been submitted empty. It remains settable through other means,
but the console will actively fight you for it.

The group-heading key derivation collapses **both spaces and hyphens** to underscores. That
is a fix for a defect that was invisible until a group name contained a hyphen.

### 2.2.2 The label you see may not be the label in the code — and something now warns you

Each field's label and help text resolve **from the translation bundle first**, falling back
to the string compiled into the registry only when no translation key exists. The bundle
therefore **overrides** the code; it does not mirror it.

That asymmetry caused a real defect: **1.6.0 renamed settings and rewrote their help, nobody
touched the bundles, and the console went on rendering the old names and the old semantics**
— in three languages — long after they stopped being true.

**What changed since, and it changes what you should do about it:**

- **English `settings.field.*` is GENERATED and exact-diffed at build time.**
  `internal/i18n/settings_drift_test.go` exists for precisely this failure and names 1.6.0
  as the release that caused it. Do not hand-edit English labels; change the label in
  `internal/settings/settings.go` and run the `i18nsync` generator.
- **Spanish and French are stamp-checked**, so a shipped build cannot carry silently stale
  bundles either.
- **Cross-field validation messages no longer carry their own field names.** At v3.6.0 a
  validation failure carries the field *key* plus placeholders, and the web layer resolves
  every name through the same bundle lookup the form used — so an error can no longer
  instruct you to change a field by a name that appears nowhere on your screen.

**The identifier that never drifts is the setting key**, which is why this document lists it
beside every label.

### 2.3 Settings reference

**Every default and bound below was read from `internal/settings/settings.go` at v3.6.0.**
Between v1.5.5 and v3.6.0, **six keys were added and none were removed**, and not one default
or bound of a pre-existing field changed.

**Every value in a field labelled "(seconds)" is in seconds** — the form does not accept
`30d`. Convert deliberately: 1 h = `3600`, 1 d = `86400`, 7 d = `604800`, 30 d = `2592000`,
60 d = `5184000`, 90 d = `7776000`.

#### Rate limiting

| Label | Key | Default | Range |
|---|---|---|---|
| Enabled | `rl_enabled` | on | on/off |
| Per-IP requests / minute | `rl_ip_rpm` | 120 | 1–1000000 |
| Per-IP burst | `rl_ip_burst` | 120 | 1–1000000 |
| Per-token requests / minute | `rl_token_rpm` | 120 | 1–1000000 |
| Per-token burst | `rl_token_burst` | 60 | 1–1000000 |
| Auto-block after | `rl_block_after` | 100 | 0–1000000 (0 = never block) |
| Auto-block lockout (seconds) | `rl_lockout_sec` | 900 | 1–604800 |
| Always-allowed CIDRs | `rl_allow_cidrs` | *(empty)* | CIDR list |

**These "defaults" are compiled FALLBACKS, not fixed values.** Seven of the eight
rate-limiting rows, plus `trusted_proxies`, read an environment variable at startup — so the
value shown here is what you get *when the unit sets nothing*. Check the unit before
concluding the table is wrong.

**`/healthz` is exempt from rate limiting**, alongside loopback and the configured CIDRs. A
health prober cannot lock itself out.

**A CIDR field rejects `/0` outright.** Both `rl_allow_cidrs` and `trusted_proxies` refuse
it rather than accepting a value that would disable the protection entirely.

**On the allowlist.** Loopback is always exempt; you do not need to list it. An allowlist
entry is a *complete* bypass of abuse protection for every address inside it, and a `/16` is
sixty-five thousand hosts. If your agents sit behind an edge that source-NATs across a pool,
prefer listing the pool's individual addresses over the enclosing prefix.

#### Login

| Label | Key | Default | Range |
|---|---|---|---|
| Max failed logins | `login_max_fails` | 5 | 1–10000 |
| Login lockout (seconds) | `login_lockout_sec` | 300 | 1–604800 |

#### Session

| Label | Key | Default | Range |
|---|---|---|---|
| Session lifetime (hours) | `session_ttl_hours` | 12 | 1–720 |

Applies to **new sessions only** — raising it does not extend the one you are using, and
lowering it does not end it. Log out and back in to see the change.

#### Network

| Label | Key | Default |
|---|---|---|
| Trusted proxy CIDRs | `trusted_proxies` | *(empty)* |

`X-Forwarded-For` is honoured **only** from these peers. Blank means "trust nobody", correct
when Ken faces the internet directly. **An over-broad value lets a client forge its own IP
address**, defeating per-IP rate limiting, the auto-block list and the login lockout in one
move. If you terminate TLS at a proxy, list that proxy and nothing else.

#### TLS

| Label | Key | Editable |
|---|---|---|
| TLS mode | `tls_mode` | read-only (`KEN_TLS`) |
| ACME domains | `tls_domains` | live — **add-only, see below** |
| ACME account email | `tls_email` | read-only (`KEN_TLS_EMAIL`) |

**"Live" is only half true, and the missing half is the dangerous one.** The running host
policy is the **union** of the startup `KEN_TLS_DOMAINS` list and the live settings list. A
live edit can **add** a domain without a restart; it can **never remove a startup domain**.

So an operator who deletes a hostname from this field and watches the form save has changed
nothing for that host. Removing a startup domain needs a `KEN_TLS_DOMAINS` change **plus a
restart**. This is deliberate — it stops an accidental settings edit from locking you out of
the host you booted on — but nothing on the form says so.

#### Curation

| Label | Key | Default |
|---|---|---|
| Curation language(s) | `curation_langs` | *(empty)* |

Comma-separated codes, e.g. `en` or `fr,zh`. **Blank turns the feature off entirely** — the
default, so a fresh install has no language gating until you opt in.

The gate **fails open on an undetermined result**: an entry whose language could not be
determined promotes without a check, so `und` means *"did not look"*, never *"looked and
approved"*.

**Corrected in this revision — the earlier length claim was inverted.** The previous text
said long entries were more likely to come back undetermined, and concluded that "the long,
carefully-argued entry you most want to read is the one most likely to skip the check". The
only explicit length rule in Ken's detector does the opposite: **text with fewer than 12
letters returns `und`** before the detector is consulted at all. There is no upper bound and
no truncation. More text makes detection *easier*, not harder. **The entries that skip the
check are the shortest ones.**

#### Inter-session comms

**COMM is always on and cannot be switched off.** `KEN_COMM_ENABLED` was deleted in 2.0.0
and setting it has no effect. The only state in which these settings are inert is the
**degraded** case where `comm.db` could not be opened — a failure, logged as such, not a
configuration choice. All 18 fields are live.

| Label | Key | Default | Range |
|---|---|---|---|
| Max message size (bytes) | `comm_max_body_bytes` | 65536 | 256–1048576 |
| Max unacknowledged per channel | `comm_max_unacked` | 64 | 1–100000 |
| **Lifetime after delivery (seconds)** | `comm_message_ttl_sec` | 86400 | 60–2592000 |
| **Lifetime before delivery (seconds)** | `comm_undelivered_ttl_sec` | 2592000 | 3600–7776000 |
| **Body retention after settling (seconds)** | `comm_body_retention_sec` | 86400 | 0–7776000 |
| Metadata retention (seconds) | `comm_metadata_ttl_sec` | 604800 | 60–7776000 |
| Reply deadline (seconds) | `comm_reply_deadline_sec` | 3600 | 30–604800 |
| Pairing code lifetime (seconds) | `comm_pairing_code_ttl_sec` | 900 | 30–86400 |
| Max long-poll wait (seconds) | `comm_poll_wait_max_sec` | 15 | 1–30 |
| Hearsay window (seconds) | `comm_provenance_window_sec` | 3600 | 0–604800 |
| File exchange enabled | `comm_files_enabled` | off | on/off |
| Max file size (MB) | `comm_file_max_mb` | 16 | 1–1024 |
| Relay storage budget (MB) | `comm_file_budget_mb` | 256 | 1–100000 |
| Free-space floor (MB) | `comm_file_min_free_mb` | 512 | 0–1000000 |
| File lifetime (seconds) | `comm_file_ttl_sec` | 86400 | 60–2592000 |
| Transfer grant lifetime (seconds) | `comm_grant_ttl_sec` | 300 | 60–3600 |
| Idle session cleanup (seconds) | `comm_endpoint_idle_sec` | 604800 | 300–7776000 |
| Station claim lease (seconds) | `comm_claim_lease_sec` | 900 | 30–3600 |

**`comm_message_ttl_sec` was renamed and its meaning changed with it.** It was "Message
lifetime"; it is now "Lifetime after delivery", and **the clock starts when the recipient is
first handed the message, not when it was sent.** This is the only label in the whole
registry that changed between v1.5.5 and v3.6.0, and anyone sizing it against a
send-anchored clock sizes it wrong. The send-to-first-poll window is now the separate
`comm_undelivered_ttl_sec`. See §2.4.

**One cross-field rule is enforced:** `comm_undelivered_ttl_sec` must be **≥**
`comm_message_ttl_sec`. Violating it is refused at save with a message naming the fields —
it does not clamp and it does not warn-and-accept.

**Max long-poll wait is capped at 30 in code no matter what you set**, and defaults to 15. A
session asking to wait two minutes gets fifteen seconds — but it is now *told*: `comm_poll`
returns `wait_seconds_granted` on every call and `wait_clamped_from` whenever the request
was reduced. Do not tune around the number an agent asked for.

#### Stations

| Label | Key | Default | Range |
|---|---|---|---|
| Notebook page size (KiB) | `station_note_page_kib` | 64 | 1–1024 |
| Revision history per page (KiB) | `station_note_revision_kib` | 256 | 0–4096 |
| Notebook per station (KiB) | `station_notebook_kib` | 4096 | 64–65536 |
| Locker file size (KiB) | `station_locker_blob_kib` | 256 | 1–4096 |
| Locker per station (KiB) | `station_locker_total_kib` | 2048 | 16–65536 |
| **Vault secret size (KiB)** | `station_vault_secret_kib` | 8 | 1–256 |
| **Secrets per station** | `station_vault_entries` | 64 | 1–1000 |
| **Vault versions kept per secret** | `station_vault_history_rev` | 16 | 0–200 |
| **Vault reads kept per station** | `station_vault_read_log` | 500 | 10–100000 |
| Open tasks per station | `station_max_open_tasks` | 500 | 10–5000 |
| Task line length (bytes) | `station_task_text_bytes` | 512 | 64–4096 |
| Task detail size (bytes) | `station_task_detail_bytes` | 4096 | 256–65536 |
| Task list page size | `station_task_list_limit` | 50 | 5–200 |

**Everything a station holds lands in your backups**, because station assets live in the
knowledge-base database. Raising a station bound is a backup decision before it is a storage
decision.

**The station vault stores values that can be read back**, which is a different security
posture from the rest of Ken — see §6.1.

**`station_vault_history_rev` is what makes a vault write reversible: at `0` an overwrite is
final.** And `station_vault_read_log` is a COUNT of retained read records, not a switch — the
per-secret read count stays exact regardless, so lowering it shortens the audit trail without
hiding how often a secret was read, and its minimum of 10 means read auditing cannot be turned
off from this form.

---

### 2.4 Interactions that bite

These are the combinations where each setting is individually sensible and the pair is not.

> **This entire section was rewritten for v3.6.0.** The v1.5.5 text described a
> retention model replaced in 1.6.0 and false for thirteen releases. If you are holding an
> older copy, discard §2.4 of it wholesale rather than reconciling it.

#### The spine: expiry is stamped TWICE

Everything below follows from one fact.

```
at INSERT          expires_at = now + comm_undelivered_ttl_sec   (default 30 days)
at FIRST DELIVERY  expires_at = now + comm_message_ttl_sec       (default 24 hours)
```

**Mail nobody has polled is bounded by the 30-day backstop, not by the 24-hour TTL.** The
24 hours only ever means *"the recipient has had it and done nothing"*. Absence is governed
by a different setting from neglect, and that separation is the whole point of the change.

#### (a) The weekend problem, and why it is fixed

The old advice was to size message lifetime against your longest absence, because the clock
ran from send. **That is no longer necessary and the setting no longer does it.** With
shipped defaults, mail sent on a Friday and polled the following Monday is fine:
64 hours of absence against a 720-hour undelivered backstop.

The incident that produced the old advice was real — a message requiring a response was sent
on a Sunday and expired on the Monday, four hours before anyone returned, and it was recorded
as neglect for days before the timestamps were checked. **It is quoted in the source as the
reason the anchor moved.** Sizing `comm_message_ttl_sec` against an absence today is sizing
the wrong setting.

**Size `comm_undelivered_ttl_sec` against your longest absence.** It accepts up to 90 days,
so a three-month absence is configurable — where the old model had a hard 30-day ceiling and
no answer.

#### (b) Metadata retention and message lifetime are now INDEPENDENT

The old rule — "metadata retention must exceed message lifetime, because the purge keys on
`created_at`" — is **retired**. At v3.6.0 the purge anchors at **settle time**: the last
recipient's ack for an acked message, `expires_at` for an expired one. A message that
expires on day 30 starts its retention window on day 30, not on day 1.

You no longer need to order these two against each other. **The one ordering rule that is
enforced is `comm_undelivered_ttl_sec ≥ comm_message_ttl_sec`** (§2.3), and the form refuses
a violation rather than accepting it.

#### (c) The quiet endpoint, and why it matters less than it did

An endpoint is removed by the idle sweep when **all** hold: `last_seen_at` is older than
`comm_endpoint_idle_sec`, no surviving message or attachment references it, **and it does not
occupy a channel seat**. The seat condition was added in 3.9.0's predecessor: seats cascade, so
collecting one used to delete the CHANNEL a human had authorised, along with any mail still
queued on it. Endpoint rows for live pairings therefore persist indefinitely, and that is
correct — the channel-deletion pass releases a seat once its channel is gone, and the next sweep
collects it.

`last_seen_at` is refreshed by **any** authenticated COMM call, throttled to once a minute —
so a session that merely polls is protected regardless of whether anyone writes to it.

**Losing an endpoint no longer costs a station its inbox.** Mail is addressed to a *party*
(`s:<station_id>`), so a replacement endpoint bound to the same station inherits the queue.
The old warning — "come back from leave to a deleted endpoint and need a fresh pairing
code" — applies to an unbound endpoint, not to a staffed station.

#### (d) Body retention: what it measures from, and three traps

`comm_body_retention_sec` (default 24 h) blanks a message body **after the message settles**.
The retention pass requires: no delivery still `queued` or `delivered`, and at least one
delivery settled. **Acknowledging does not destroy the body.** There is an afterwards, and by
default it is a day long.

**Trap 1 — raising body retention above `comm_message_ttl_sec` does not lengthen retention
for delivered-but-never-acked mail.** That mail expires on the delivery clock first, and the
retention window starts from *that*. The lever you reached for is downstream of the one that
fires.

**Trap 2 — a body that was NEVER DELIVERED cannot be reclaimed by body retention at any
value above zero**, because retention measures from settlement and an undelivered message has
not settled. It survives until the metadata purge deletes the whole row.

**Trap 3, and it opposes Trap 2 — setting `comm_body_retention_sec` to `0` is the only lever
that restores blank-on-acknowledgement.** If you are trying to minimise text at rest, `0` is
the setting; any positive value keeps bodies longer than you may expect.

**A property worth knowing before you tune any of this: retention keeps bodies that nothing
in Ken can read.** At v3.6.0 no MCP tool and no console page returns a settled message body.
`comm_poll` returns unacked mail; once settled, the text is reachable only by reading the
database directly. **Retained-but-unreadable is the default state.** If a session needs to
re-read something, it must copy it out at the time — not because the text is destroyed, but
because nothing offers it back.

#### (e) The reply deadline is NOT a retention limit

The v1.5.5 text said a body was kept after acknowledgement only if the message asked for a
response, and only until its reply deadline passed. **Every clause of that is false at
v3.6.0.**

- `requires_response` governs **no retention at all**. Every settled body follows
  `comm_body_retention_sec` uniformly.
- The deadline is armed at **first delivery**, not at send.
- **Raising `comm_reply_deadline_sec` has zero retention effect.** The old text advised
  raising it to protect your audit trail; an operator who follows that advice changes nothing
  and believes they have fixed it — the worst class of remedy.

The measurement behind the old advice — **97% of all message bodies ever sent had been
deleted** — is real, but it is the **pre-1.6.0 result**. It is the justification for the
current behaviour, not a description of it.

#### (f) Rooms change the arithmetic

A room message is **one `message` row and N `delivery` rows**. The body is stored once; the
settle condition is *every* recipient, so one silent member holds the body for everyone.
Membership is console-only, which bounds the blast radius of anything that goes wrong in a
room to something a human did on purpose.

#### (g) Claim-once delivery

When several sessions staff one station, `comm_claim_lease_sec` (900 s) means **the first to
poll a message holds it** for the lease. A second session polling the same station in that
window sees nothing and may reasonably conclude the mail was lost. It was claimed, not lost.

#### (h) Notebook revision history is a cap, and it prunes silently

`station_note_revision_kib` (256 KiB) bounds the retained edit history of **one page**.
Exceeding it deletes the **oldest** revisions, oldest-first, with no log line and nothing in
the write result.

Each revision keeps a full copy of the page, so a page maintained by **appending** accumulates
history proportional to the square of its length. A conscientiously-maintained handoff page
reaches the cap far sooner than its size suggests.

Observed on a live deployment: one station's page had **already lost its first seventeen
revisions**, including the original context.

- **Raise the bound.** Up to 4096 KiB — sixteen times the default. `0` disables history.
- **Rewrite pages rather than append.** On the same deployment, a page with a *larger* body
  carried a **tenth** of the history cost, purely from being rewritten.
- **`station_note_list` now reports `revisions_lost` per page** — a station can answer this
  about itself without SQL. See §6.3 for the caveat.

**FIXED IN 3.7.0: `station_note_write` with `mode='replace'` on an EXISTING page now REQUIRES
`if_rev`**, and the refusal names the page's current revision so a session can read and retry
without a second call. Creating a new page needs none; `append` is unaffected.

Until 3.7.0 the check was opt-in and a blind replace overwrote silently. It was not a trap you
fell into by default — `append` is the default and is non-destructive — **it was one you fell
into by doing the right thing**, because a handoff page's own header says never to append.
Measured on a live 3.6.0 deployment, where one station had already lost seventeen revisions.

---

## 3. The CLI

### 3.1 Read this before you run anything

**An unrecognised subcommand starts a server.**

Dispatch matches a fixed list — `token`, `user`, `backup`, `import`, `embed`, `station`,
`serve`, `version`, `help` — and anything else falls through to "treat the arguments as
serve flags". There is no error for an unknown verb.

So a typo, or a command half-remembered from a newer release, does not fail. It launches a
**second Ken instance against the same database** and binds a port.

```bash
ken lang backfill      # not a command → starts a server
ken snapshot           # not a command (it is `backup snapshot`) → starts a server
```

**Check the verb before you run it.** `ken help` costs nothing and always works.

### 3.2 Commands

```
ken [serve] [flags]         run the MCP + web server (the default with no subcommand)
ken token add|list|revoke   API tokens for agent (MCP) access
ken user  add|list          human users for web login
ken backup snapshot|verify  make or verify a consistent database snapshot
ken import --dir DIR        import flat .md memory files as curated entries
ken embed backfill|status   compute embeddings for semantic search
ken station add|list|key    create stations and mint their keys
ken version                 build version and source location
ken help                    this list
```

**The backup commands do not run as bare verbs.**

```bash
ken backup snapshot --out /path/to/file.db.gz     # --out is REQUIRED
ken backup verify /path/to/file.db.gz             # the file is a positional arg
```

`ken backup snapshot` with no `--out` exits with `--out is required`; `ken backup verify`
with no argument exits with its usage line. Earlier text quoted both as bare verbs.

**`snapshot` reads `KEN_DB`; `verify` never opens a store at all.** `verify` checks only the
file you name, which is what makes it safe to run against an archived snapshot on a machine
with no Ken database of its own.

**⚠ A missing `KEN_DB` does not stop `snapshot` — and that is the trap.** It falls back to the
RELATIVE default `./data/ken.db`, creates that directory and database if they are absent, and
then reports success:

```
snapshot: /path/to/file.db.gz (0 entries, integrity ok)
```

**So a snapshot taken from the wrong working directory succeeds AND verifies**, because it
faithfully snapshots an empty database it just created. Set `KEN_DB` explicitly on every
snapshot — the systemd unit and any cron entry included — and **reconcile the printed entry
count against the live instance** rather than reading "integrity ok" as proof you backed
anything up.

**`ken version` is the one command that always works.** Dependency-free by design, and the
only surface that reveals a build whose version metadata failed to inject — which otherwise
ships a binary quietly reporting the wrong version. Run it after every upgrade, first.

**Commands that print secrets belong to the person who owns them.** `token add` and
`station key` print a credential once and it cannot be retrieved afterwards. Run them
yourself; do not have an agent run them for you, and do not paste the output anywhere it
will be recorded.

**Token scopes** are the access-control boundary that makes the review queue meaningful:
`read`, `write-draft`, `propose` let an agent search, draft and propose. `curate` is the
promotion right. **An agent token should never carry `curate`** — that exclusion *is* the
curation gate, and there is nothing else enforcing it.

---

## 4. Health and monitoring

### 4.1 The health check — read this before you trust it

```bash
curl -fsS https://<your-ken-host>/healthz
```

**`/healthz` is LIVENESS ONLY.** At v3.6.0 the handler writes the literal bytes `ok\n` and
touches nothing else. **It returns 200 from a process whose database is unreachable and whose
data directory is read-only.**

This is the single most important correction in this revision. A green health check means the
process is running and can serve a request. It does not mean Ken works. Anyone treating it as
a service check — including this document's previous revision, and including the operators who
wrote it — is reading a signal that cannot fail for the reasons they care about.

**`/health` IS THE ONE THAT ANSWERS THE QUESTION `/healthz` CANNOT.** It is public, returns
JSON, and returns **503 when a component is DOWN** — it pings the database AND proves the data
directory is writable by creating and removing a temp file, which are exactly the two failures
`/healthz` sails through. Component detail (paths, error strings) is shown only to loopback or
to a caller carrying `KEN_METRICS_TOKEN` / inside `KEN_METRICS_CIDRS`; everyone else gets the
verdict without the internals.

**Point your monitoring at `/health`, and use `/healthz` only for "is the process alive".**

**COMM is deliberately excluded from BOTH**, and the reason is sound: a failure in an ephemeral
subsystem should not mark the whole service DOWN and take a healthy knowledge base with it.
COMM's state belongs in `/metrics`.

**Use the real hostname.** Two plausible-looking variants fail indistinguishably from an
outage:

- **`localhost:8080`** — correct for a plain-HTTP deployment, wrong for one terminating TLS
  in-process, which listens on 443 and 80 with nothing on 8080. Connection refused.
- **`https://localhost/healthz`** — with ACME, the certificate manager refuses a non-FQDN
  server name and **aborts the handshake**, so `-k` does not help.

Both fail hardest seconds after a restart, when you are most inclined to believe them. Confirm
with the service manager's status, the listening sockets, and the log before concluding an
outage from one failed check.

### 4.2 Metrics

`/metrics` is access-controlled and **404s rather than 403s** when refused — so a wrong
source address looks like a missing endpoint, not a permission error.

Series worth knowing:

- **`ken_comm_deliveries_unacked` is new in 3.6.0** — outstanding deliveries, one per
  recipient. This is the one that measures how much work is stuck.
- **`ken_comm_messages_unacked`** counts MESSAGES; a room message counts once regardless of
  how many members have not acknowledged it.
- **`ken_comm_message_bytes` changed what it counts in 3.6.0** — it summed characters and now
  sums bytes. **Samples either side of that upgrade are not comparable**, and nothing in the
  data announces it. Annotate your archive at the upgrade boundary.

### 4.3 What "slow" usually is

Ken's own work is measured in **single-digit milliseconds** for tool calls and about a
millisecond for web requests, on an idle small VPS. Decompose before blaming the server:

```bash
curl -sS -o /dev/null -w \
  'tls=%{time_appconnect} ttfb=%{time_starttransfer} total=%{time_total}\n' \
  https://<your-ken-host>/healthz
```

Run it **from the server itself** as well as from your workstation. The TLS handshake is
typically the largest single component — far larger than Ken's work. A slow call is almost
always the path, the handshake, or a client not reusing connections.

### 4.4 Long polls are not slowness

A receive call with no waiting mail **blocks on purpose**, up to the long-poll ceiling
(§2.3). That is the feature, not latency, and it is excluded from the latency histogram for
exactly that reason.

---

## 5. Routine operations

### 5.1 Upgrading

1. `ken version` — record what you are on.
2. Take a snapshot — **`KEN_DB` must be in the environment**, or the command silently
   snapshots a brand-new empty `./data/ken.db` and reports it healthy:
   ```bash
   KEN_DB=/opt/ken/data/ken.db ken backup snapshot \
     --out /opt/ken/backups/pre-upgrade-$(date -u +%Y%m%dT%H%M%SZ).db.gz
   ```
   **Read the entry count it prints** — `snapshot: … (N entries, integrity ok)` — and check N
   against the size of your knowledge base. A `0` there is the empty-database failure, not an
   empty knowledge base. `snapshot` already verifies what it wrote and fails loudly rather than
   returning a bad file, so the check that is NOT automatic is this one: that it snapshotted the
   RIGHT database. Keep `ken backup verify FILE` for a file you did not just write — a nightly,
   an older rollback point, a copy pulled off the box.
3. Upgrade.
4. `ken version` again — confirm it moved.
5. `curl -fsS https://<host>/healthz` — remembering §4.1: this proves liveness, nothing more.
6. **Compare the migration count before and after**, from the database. See below.

**There is no migration log to check.** `Store.Migrate()` applies each pending `NNNN_*.sql`
and emits nothing — no line per migration, no summary, no count. Earlier text said to "check
the log for a migration you were not expecting"; there is nothing there to see. Read the
`schema_migration` table instead:

```sql
SELECT COUNT(*), MAX(version), MAX(applied_at) FROM schema_migration;
```

**An unchanged count with unchanged timestamps proves no migration ran** — a much stronger
statement than the absence of a log line, and available in one query on each database.

**But read the release note first, because this test inverts.** For a release that ships no
migration, unchanged is the pass. For one that does, unchanged is the FAILURE, and the pass is
*exactly the expected new rows, with every earlier `applied_at` untouched*. 3.9.0 was the first
release in four to run any: comm.db 11→14 and ken.db 18→19. Run `PRAGMA foreign_key_check` on
both afterwards as well — it should return nothing.

**Service drop-in files survive upgrades; the main unit file does not.** An installer
regenerates the unit but leaves an override directory alone, so anything that must persist
across upgrades — environment variables, resource limits — belongs in a drop-in, never edited
into the unit.

### 5.2 Backups

Whatever schedule you run, three properties matter more than frequency:

- **Verify restores, not just that files exist.** A backup job never restored from is an
  untested code path.
- **A size band is the wrong alarm for growing data.** A fixed maximum will eventually be
  crossed by ordinary growth and then fire every night, training everyone to ignore it. Alarm
  on **change** — a snapshot that *shrank*, or grew far more than usual — and keep an absolute
  ceiling only as a sanity bound.
- **A check with no stored baseline must say so.** "No baseline" is not a pass. A check that
  cannot look and reports success is worse than no check.

---

## 6. Investigating

The console answers most questions. These are for the ones it does not, and they are all
**read-only**.

### 6.1 Reading the database safely

`sqlite3` is frequently **not installed** on a minimal server image. Python's bundled module
is, and opening read-only through a URI cannot write even by accident:

```bash
python3 - <<'PY'
import sqlite3
c = sqlite3.connect("file:/path/to/ken.db?mode=ro", uri=True)
for row in c.execute("SELECT COUNT(*) FROM entry"):
    print(row)
c.close()
PY
```

**Never `SELECT *` on a table you have not inspected.** This is now literally and
deliberately true of a named table: **the station vault stores values that can be read
back** — that is what a vault is for — so `SELECT *` there prints secrets into whatever log
or transcript is capturing your session. Name your columns, and name them especially there.

### 6.2 Has a message been lost?

> **The v1.5.5 recipes in this section no longer execute.** Migration 0009 moved `state` and
> `delivery_count` off `message` into a per-recipient `delivery` table. The old queries fail
> with `no such column: state` — mid-incident, which is exactly when they would be run.

**These run against `comm.db`, not `ken.db`.** §6.1's worked example opens `ken.db`, which is
the right file for entries, settings and stations — and the wrong one for every query in this
subsection. Messages, deliveries and channels live in `comm.db`; pointed at `ken.db` these fail
with `no such table: message`.

```sql
-- died before anyone saw it: expired without ever being delivered
SELECT m.message_id, m.scope_id, m.created_at, m.expires_at, m.body_bytes
FROM message m
JOIN delivery d ON d.message_row = m.id
WHERE d.state = 'expired' AND d.delivery_count = 0;

-- consumed without ever being delivered (should be empty)
SELECT m.message_id, m.scope_id, m.created_at
FROM message m
JOIN delivery d ON d.message_row = m.id
WHERE d.state = 'acked' AND d.delivery_count = 0;
```

**Read `scope_id`, not `channel_id`.** `channel_id` still exists on `message` but is now
**NULL for every room and broadcast message** — so the column an operator reaches for to
locate a lost message is blank for exactly the traffic most likely to go missing. `scope_id`
carries `ch:<channel_id>`, `r:<room_id>` or `b:<sender>` and is always populated.

**`body_bytes` records how much text used to be there** even after the body is gone — and at
v3.6.0 it is a true byte count, not a character count.

**A message that expired undelivered still has its body**, deliberately (§2.4(d), Trap 2).
Earlier text implied expiry blanked it. The text survives until the metadata purge removes
the whole row, which makes this recipe more useful than it used to be, not less.

### 6.3 Has a notebook page been pruned?

**Ask the station first.** `station_note_list` reports `revisions_lost` and `history_bytes`
per page, so a session can answer this about itself without SQL.

⚠ **This field was inverted in 3.0.0 and fixed in 3.0.1** (2026-08-12). It reported the
revisions that *survive* rather than the number lost, so healthy stations reported losses and
the one station with real loss reported a smaller number than its neighbours. **On 3.0.1 and
later it is correct**: `revisions_lost` = oldest retained revision − 1, the lowest revision you
can still read is `revisions_lost + 1`, and `station_note_read` takes a `rev` to fetch it. Only
a deployment still on 3.0.0 needs the SQL below as a cross-check.

From SQL:

```sql
SELECT n.station_id, n.key, n.rev AS head,
       COALESCE(MIN(r.rev), n.rev) AS lowest,
       COUNT(r.id)                 AS kept,
       COALESCE(SUM(LENGTH(CAST(r.body AS BLOB))), 0) AS history_bytes
FROM station_note n
LEFT JOIN station_note_revision r
       ON r.station_id = n.station_id AND r.key = n.key
GROUP BY n.station_id, n.key
HAVING lowest > 1;
```

**The join to `station_note` is load-bearing.** Pruning deletes oldest-first and stops only when
the total fits or nothing is left, so a page can lose its history ENTIRELY. Such a page has no
rows in `station_note_revision` at all — so a query reading only that table returns nothing for
it, and the worst case is the one it cannot see. Starting from `station_note` reports it with
`kept = 0`.

**A lowest retained revision greater than 1 is proof of loss, with a count attached.**

**`kept` counts HISTORY rows only.** The head revision lives on `station_note`, not in this
table, so the number of readable revisions is **`kept + 1`**.

### 6.4 What is actually configured

```sql
SELECT key, value, updated_at FROM app_setting ORDER BY key;
```

Remember §2.1: this shows **overrides only**. Anything absent is on its compiled default, and
`updated_at` means "last saved" (§2.2). For effective values, use the console.

---

## 7. Traps, collected

Every one of these was paid for on a running deployment.

1. **`/healthz` returns 200 from a process whose database is unreachable.** It is liveness,
   not health. §4.1
2. **A health check aimed at the wrong listener fails exactly like an outage** — seconds
   after a restart, when you are most inclined to believe it. §4.1
3. **An unknown CLI verb starts a server** instead of erroring. §3.1
4. **`ken backup snapshot` needs `--out` and `KEN_DB`**; the bare verb exits without making a
   backup. §3.2
5. **`comm_message_ttl_sec` no longer means what its old name said.** It is the
   post-delivery clock; absence is governed by `comm_undelivered_ttl_sec`. §2.3, §2.4
6. **Raising `comm_reply_deadline_sec` has no retention effect** — the old advice to raise it
   to protect your audit trail changes nothing. §2.4(e)
7. **A body never delivered cannot be reclaimed by body retention at any positive value.**
   §2.4(d)
8. **Retention keeps bodies nothing in Ken can read back.** §2.4(d)
9. **FIXED in 3.7.0** — `station_note_write` with `replace` and no `if_rev` was the trap you
   reached by following the correct advice to replace rather than append. It now refuses and
   names the current revision. Listed because a deployment older than 3.7.0 still has it. §2.4(h)
10. **Notebook history prunes silently**, oldest-first, and append-style editing reaches the
    cap far sooner than page size suggests. §2.4(h)
11. **The language gate fails open on `und`, and `und` correlates with SHORT text** — the
    opposite of what this document said until v3.6.0. §2.3
12. **`tls_domains` is add-only.** Deleting a startup domain from the form changes nothing.
    §2.3
13. **A field whose group is not on the render list is invisible AND is processed as empty on
    every save.** §2.2.1
14. **Over-broad trusted proxies let clients forge their IP**, defeating three protections at
    once. §2.3
15. **An allowlist entry is a total bypass** for everything inside it. §2.3
16. **`updated_at` on settings means "last saved", not "last changed".** §2.2
17. **A long-poll wait is a request, not a promise** — capped in code, though now reported
    back via `wait_clamped_from`. §2.3
18. **A room message is claimed by the first session to poll it** for the claim lease; a
    second session sees nothing and may call it lost. §2.4(g)
19. **`channel_id` is NULL for room and broadcast traffic** — the column you reach for is
    blank for the traffic most likely to be missing. §6.2
20. **A fixed size band on growing data becomes a nightly false alarm**, and false alarms
    train people to ignore the channel that carries the real one. §5.2

---

## 8. A note on measurement

Several sections above replace a rule of thumb with a number, because the rules of thumb were
wrong in the same direction: a reply deadline chosen because an hour "felt reasonable", a size
band chosen because the file "was about that big", a watch window opened after the event it
was meant to observe.

**When something is time-dependent — a TTL, a deadline, a retry interval, a staleness
threshold — derive it from a measurement of the system it governs.** How long does delivery
actually take here? How long is this deployment actually unattended? Both are one query away,
and both are routinely off by an order of magnitude from what anyone would guess.

**And measure the instrument too.** This revision exists because fifty-seven statements in the
previous one were false while reading exactly like the true ones around them. The document did
not decay visibly; it decayed silently, and it was only caught because someone re-derived every
claim against source rather than re-reading the prose. A check that cannot fail differently
from a pass is not a check — and that applies to documentation as much as to monitoring.
