# Ken — compatibility & versioning

Ken follows [Semantic Versioning](https://semver.org). This document states, as of
**1.0**, which surfaces are **stable** — covered by SemVer, so a breaking change to
one of them requires a new MAJOR version — and which are explicitly **not** part of
the compatibility contract.

## Supported platforms

Ken is supported on **Linux** — `amd64` and `arm64`, the architectures it is built,
tested, and released for. It is a single static Go binary with a pure-Go SQLite
(`CGO_ENABLED=0`), so it may well compile and run on other operating systems, but none
is built, released, or supported: **macOS and Windows are not supported.** If that ever
changes, the honest order is a documented build/run path or a published artifact first,
and then the claim — not the reverse.

## Stable (SemVer-governed at 1.0)

- **The MCP tool contract** — the `kb_*` tool names, their input/output JSON schemas,
  and their documented semantics ([docs/MCP-TOOLS.md](docs/MCP-TOOLS.md)). New
  *optional* fields or whole new tools may be **added** in a MINOR release (additive,
  non-breaking); removing or renaming a tool/field, or tightening validation on an
  existing one, is a MAJOR change. The controlled-vocabulary enums are part of the
  contract: entry `kind` (`user`/`feedback`/`project`/`reference`), version `state`,
  `staleness`, `lifecycle`, `link_type` (`relates`/`supersedes`/`refutes`/`depends_on`),
  and token `scopes` (`read`/`write-draft`/`propose`/`curate`; the COMM and station surfaces add
  `comm`/`comm-file` and `station`/`station-locker`, which are outside this contract while
  those surfaces are).
- **The CLI surface** — the `ken` subcommands and their flags (`serve`, `token`,
  `user`, `backup`, `embed`, `import`). New subcommands/flags may be added in a MINOR;
  removing or repurposing one is MAJOR.
- **Environment configuration** — the `KEN_*` variables and their meanings. New vars
  may be added; removing one, or changing a default's effect incompatibly, is MAJOR.
  This includes **`KEN_SOURCE_URL`**, which sets the "Source" link a running instance
  shows: if you run a **modified** Ken, set it to your own repository — AGPL-3.0 §13
  requires a network service to offer *its own* Corresponding Source, not upstream's.
  **2.0.0 removed four of them** — `KEN_COMM_ENABLED`, `KEN_STATION_ENABLED`,
  `KEN_OAUTH_ENABLED` and `KEN_AGE_RECIPIENT` — which is exactly why 2.0.0 is a MAJOR
  release rather than a carve-out. An earlier version of this bullet excused the first two
  as "excluded with the surfaces they gate". That excuse is withdrawn: exempting variables
  one at a time until the rule covers nothing is how a compatibility promise stops meaning
  anything, and the honest move was to take the version bump the rule asks for.
- **Credential prefixes & cookie format** — and they are not all the same kind of thing, so the
  contract is stated per prefix rather than as one list:
  - `ken_` — an API token presented as `Authorization: Bearer …` on `/mcp` and, when it carries the
    `comm` scope, on `/comm/mcp`.
  - `kens_` — a **station key**, presented the same way on `/station/mcp`. Distinct from `ken_`
    because it additionally carries a station binding.
  - `kenc_` — an **OAuth client id**, not a bearer token and never presented as one.

  An issued credential of any of these shapes keeps working across MINOR/PATCH upgrades.
- **HTTP endpoints an external client depends on** — `/mcp`, the OAuth 2.1
  discovery / registration / authorize / token endpoints, and `/healthz`.
- **The database schema is forward-compatible.** Upgrades only **add** migrations and
  never require a schema change to downgrade; your `data/ken.db` is preserved across
  upgrades. Downgrading a release *after* a newer migration has run is not supported —
  the recovery path is the pre-upgrade snapshot (see [docs/BACKUP.md](docs/BACKUP.md)).

## NOT part of the compatibility contract

- **The web UI** — HTML/CSS/JS, page layout, DOM structure, CSS class names. It may
  change freely between releases; automate against the MCP tools or the HTTP API, never
  by scraping the pages.
- **Internal Go packages** (`internal/…`). Ken is an application, not a library — there
  is no importable public Go API, and the module path is not a support commitment.
- **Log formats, metric names**, and the exact wording of human-facing text and error
  messages.
- **On-disk layout** beyond "your knowledge lives in `data/ken.db`" (snapshot
  filenames, `releases/<v>/` internals, etc.).
- Anything documented as **optional-and-off-by-default** or **"Planned"**.
- **The inter-session communication surface** ([docs/COMM.md](docs/COMM.md)) — its `comm_*`
  tools, endpoint ids, MCP endpoint path, and settings. COMM is **core, on by default, and
  supported**. It is excluded not for being optional — it is not, any more — but because the
  surface is **mid-redesign**: the remaining planned work removes notice-messages, replaces
  pairing codes and channel-pair addressing with rooms and name-addressed send, and **retires
  the channel**, the central noun of the tool surface as it stands. Promoting it into the
  contract now would make that redesign a MAJOR bump, or force a release cycle of deprecated v1
  aliases for a shape nobody intends to keep, and buy a caller nothing in exchange. Until then
  it evolves **additively wherever it can**, and where it cannot the CHANGELOG says so plainly
  under **Changed**. "Additively" describes the habit, not a promise this document makes —
  reading it as a promise is how a surface gets frozen before its design has settled. A removed
  or renamed tool argument is **rejected by name** rather than ignored, so a caller working from
  an older flow is told rather than silently getting less than it asked for.
  The `comm` and `comm-file` token scopes are **reserved** so that
  splitting them later is not a MAJOR.

  The same applies to the **station surface** ([docs/STATIONS.md](docs/STATIONS.md)) — its
  `station_*` tools, station ids, MCP endpoint path, notebook/task/locker shapes and settings.
  `station` and `station-locker` are likewise reserved together, on the same reasoning: splitting
  a shipped scope is a MAJOR, merging two is free. The `kens_` prefix IS contract (above), because
  a credential's shape freezes the moment one is issued.

  **The trigger for promotion is stated rather than open-ended: both surfaces enter the
  byte-level contract when the COMM v2 redesign lands.** Stations are named here alongside COMM
  because that redesign changes how COMM addresses a station, not because either surface is
  optional.

## Deprecation policy

A stable surface slated for removal is first **deprecated** — kept working, with a note
in [CHANGELOG.md](CHANGELOG.md) — for at least one MINOR release before it is removed in
the next MAJOR. A security fix may make an immediate exception where remaining
backward-compatible would keep users exposed.
