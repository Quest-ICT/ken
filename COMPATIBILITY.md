# Ken — compatibility & versioning

Ken follows [Semantic Versioning](https://semver.org). This document states, as of
**1.0**, which surfaces are **stable** — covered by SemVer, so a breaking change to
one of them requires a new MAJOR version — and which are explicitly **not** part of
the compatibility contract.

## Stable (SemVer-governed at 1.0)

- **The MCP tool contract** — the `kb_*` tool names, their input/output JSON schemas,
  and their documented semantics ([docs/MCP-TOOLS.md](docs/MCP-TOOLS.md)). New
  *optional* fields or whole new tools may be **added** in a MINOR release (additive,
  non-breaking); removing or renaming a tool/field, or tightening validation on an
  existing one, is a MAJOR change. The controlled-vocabulary enums are part of the
  contract: entry `kind` (`user`/`feedback`/`project`/`reference`), version `state`,
  `staleness`, `lifecycle`, `link_type` (`relates`/`supersedes`/`refutes`/`depends_on`),
  and token `scopes` (`read`/`write-draft`/`propose`/`curate`).
- **The CLI surface** — the `ken` subcommands and their flags (`serve`, `token`,
  `user`, `backup`, `embed`, `import`). New subcommands/flags may be added in a MINOR;
  removing or repurposing one is MAJOR.
- **Environment configuration** — the `KEN_*` variables and their meanings. New vars
  may be added; removing one, or changing a default's effect incompatibly, is MAJOR.
  This includes **`KEN_SOURCE_URL`**, which sets the "Source" link a running instance
  shows: if you run a **modified** Ken, set it to your own repository — AGPL-3.0 §13
  requires a network service to offer *its own* Corresponding Source, not upstream's.
- **Bearer-token & cookie format** — the `ken_` / `kenc_` token prefixes and the auth
  scheme MCP clients present (`Authorization: Bearer …`). An issued token keeps working
  across MINOR/PATCH upgrades.
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
- Anything documented as **optional-and-off-by-default** or **"Planned"**. This includes
  the **inter-session communication surface** ([docs/COMM.md](docs/COMM.md)) — its `comm_*`
  tools, endpoint ids, MCP endpoint path, and settings. COMM is a **supported** feature, but
  because it is opt-in and off by default its interface is not part of the byte-level contract;
  it evolves **additively**. The `comm` and `comm-file` token scopes are **reserved** so that
  splitting them later is not a MAJOR.

## Deprecation policy

A stable surface slated for removal is first **deprecated** — kept working, with a note
in [CHANGELOG.md](CHANGELOG.md) — for at least one MINOR release before it is removed in
the next MAJOR. A security fix may make an immediate exception where remaining
backward-compatible would keep users exposed.
