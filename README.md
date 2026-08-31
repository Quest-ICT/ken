# Ken

An **AI-first knowledge base**: a place for an AI coding agent to store and
retrieve *curated* knowledge — solutions, caveats, pitfalls, design decisions —
gathered while it works, so hard-won answers are neither reinvented nor missed.

- Primary user: the **AI**, over the Model Context Protocol (`/mcp`).
- Secondary user: the **human curator**, over a web UI (browse / search / promote).

Project site: **<https://ken.quest.mx>**

The load-bearing idea: **the AI authors, the human promotes.** Everything an agent
writes lands as a *proposed* revision it can use immediately — but the curated head
moves only when a human promotes it. An agent can never curate; that exclusion is the
whole point. Nothing is ever overwritten: enhancements append, and superseded versions
stay searchable, so the answer that was right on the old dependency version is still
there when you go back to it.

## Try it in 60 seconds

```sh
go run ./cmd/ken --demo-seed          # add KEN_DEV_TOKEN=dev-secret for MCP access
```

Open <http://localhost:8080> — the first visit runs a one-time setup wizard that
creates your curator account. A demo entry is already seeded, so search and the entry
view have something to show.

To let an agent use it, start with a dev token and register the endpoint:

```sh
KEN_DEV_TOKEN=dev-secret go run ./cmd/ken --demo-seed
```

```sh
claude mcp add --transport http ken http://localhost:8080/mcp --header "Authorization: Bearer dev-secret"
```

Ken sends the agent its own operating instructions on connect, so there is no prompt to
paste. Ask it to search for something and it will use `kb_search` unprompted.
(`KEN_DEV_TOKEN` is a static, unrevocable credential and is refused whenever TLS is on —
for anything real, issue a scoped token with `ken token add`.)

## Status

In production. All eight knowledge-base MCP tools — `kb_search`, `kb_get`, `kb_save`,
`kb_propose_enhancement`, `kb_flag_stale`, `kb_diff`, `kb_record_outcome`,
`kb_recent_context` — are implemented, and the MCP server delivers its own usage
instructions to connecting agents (no prompt-pasting needed). The web UI is
complete: a home dashboard, search (with an `all` scope), a filterable **Browse**
grid, entry + history, the proposal queue with promote/reject, agent-token and
OAuth-connector management, the COMM console at `/comm` and the stations console at
`/stations`, first-run setup wizard, and live settings. It is a
**themeable** (dark/light) and **multilingual** (English, Spanish + French, with
drop-in translations) design system that makes **zero external requests**. An optional
**OAuth 2.1 authorization server** lets claude.ai add Ken as a custom connector.
**Inter-session comms (COMM) and stations are core surfaces, and there is no
switch for either** — the opt-out variables were removed in 2.0.0, because a
switch nobody used still cost a hedge in every document and instruction, and the
hedges rotted. Stations work whether or not COMM has a peer to talk to, because a
notebook and a task list need no peers.
Embeddings, health/metrics, in-process ACME TLS, and self-extracting release
installers are built and tested.

## Docs

- [docs/DESIGN.md](docs/DESIGN.md) — architecture, locked decisions, rationale.
- [docs/MCP-TOOLS.md](docs/MCP-TOOLS.md) — the AI-facing MCP tool contracts.
- [docs/AI-INTEGRATION.md](docs/AI-INTEGRATION.md) — how to make your AI use Ken (token strategy + the operating loop).
- [docs/OAUTH.md](docs/OAUTH.md) — connect claude.ai as a custom connector (the optional OAuth server).
- [docs/COMM.md](docs/COMM.md) — inter-session communication: let two AI sessions hand work to each other (**core; there is no switch**).
- [docs/STATIONS.md](docs/STATIONS.md) — stations: durable, human-named AI working identities with a notebook, a task list and a small file locker, which also become what COMM addresses so a peer relationship outlives the session that made it (**core; there is no switch** — the notebook and task list work whether or not COMM has a peer to talk to).
- [docs/I18N.md](docs/I18N.md) — the multilingual UI: add a language or override any string at runtime (drop-in `.properties`).
- [docs/INSTALL.md](docs/INSTALL.md) — install / deploy (self-extracting `.bin`, systemd, TLS posture).
- [docs/FINISHING.md](docs/FINISHING.md) — **the working checklist**: what is half-built, what finishing it means, and exactly where we are. Read this before starting work.
- [docs/MONITORING.md](docs/MONITORING.md) — health, metrics, and the Grafana/Prometheus bundle.
- [docs/REMOTE-UPGRADE.md](docs/REMOTE-UPGRADE.md) — the scoped, least-privilege remote-upgrade tooling.
- [docs/BACKUP.md](docs/BACKUP.md) — backup & restore runbook.
- [docs/UPGRADING.md](docs/UPGRADING.md) — **read before upgrading**: every change that breaks an existing deployment, what you will observe, and what to do first.
- [CHANGELOG.md](CHANGELOG.md) — release history.
- [COMPATIBILITY.md](COMPATIBILITY.md) — what SemVer covers at 1.0 (stable MCP/CLI/env/token/schema surfaces); the `comm_*` and `station_*` tools stay outside it until the COMM v2 redesign lands.
- [schema/](schema/) — the SQL that CREATES each database, and nothing that changes one. [`ken.sql`](schema/ken.sql) and [`comm.sql`](schema/comm.sql) build a whole database in one step; Ken applies them only when the file is empty and otherwise checks the recorded version and refuses to start if it differs. Upgrading an existing database is a separate, deliberate act with stock `sqlite3` — the scripts are in [upgrade/](upgrade/) and the procedure is [docs/UPGRADING-THE-DATABASE.md](docs/UPGRADING-THE-DATABASE.md).

## Stack

Go single static binary · embedded SQLite (WAL) via `ncruces/go-sqlite3` ·
FTS5 + trigram keyword search (embeddings optional) · MCP via the official
`modelcontextprotocol/go-sdk` · optional OAuth 2.1 authorization server ·
server-rendered web UI (`html/template`, no JS framework) — themeable dark/light
and multilingual (reloadable i18n), zero external requests · in-process ACME TLS.

## Build & run (dev)

```sh
# requires Go 1.26.5+ on your PATH
go build ./...
go test ./...

# run with a demo entry seeded, and a dev token for the MCP endpoint
KEN_DEV_TOKEN=dev-secret go run ./cmd/ken --demo-seed
# then point an MCP client at http://localhost:8080/mcp with:
#   Authorization: Bearer dev-secret
```

## License

Ken is free and open source under the **GNU Affero General Public License v3.0**
(`AGPL-3.0-only`) — see [LICENSE](LICENSE). In short: use, study, modify and share it
freely; and because Ken is a network service, the AGPL's **§13** requires that if you
run a *modified* Ken as a service, you offer its users the corresponding source. A
running instance links to its source in the footer.

**If you run a modified Ken**, set `KEN_SOURCE_URL` to your own repository:

```sh
KEN_SOURCE_URL=https://example.org/you/ken-fork
```

That is what the footer link (and the login/setup pages) will point at, so your users
are offered *your* source — the code actually running — rather than this repository.
`ken version` prints the value in effect.

Copyright (C) 2026 Quest ICT.

**Trademark:** the AGPL grants rights to the *code*. It does not grant rights to the
name "Ken" or the compass mark. Forks are welcome — please use your own name and logo
so users can tell your build apart from this project.

Contributions are welcome under a Developer Certificate of Origin — see
[CONTRIBUTING.md](CONTRIBUTING.md).

`SPDX-License-Identifier: AGPL-3.0-only`
