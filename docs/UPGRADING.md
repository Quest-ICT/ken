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

*Nothing yet.*

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
