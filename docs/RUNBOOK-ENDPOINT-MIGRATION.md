<!-- Operator runbook. Written 2026-08-19 for the last open item of docs/FINISHING.md Batch 6. -->

> **How this was produced, because it matters for how much you should trust it.** Every route,
> button label, tool argument and error string below was read out of the tree and then
> adversarially re-checked against it — 32 claims, of which **8 were corrected or thrown out**.
> The English labels come from `internal/i18n/locales/messages.properties`, not from documentation.
> Line numbers are approximate and file paths are exact.
>
> **One correction worth naming**, because the first draft got it backwards and it changes what you
> do: binding ep 6 does **not** strand its 83 existing deliveries. It does not re-file them either.
> See §6 — the operative instruction is about the *successor* session, not about the bind.

# Runbook — Batch 6, migrate the five unbound endpoints onto stations

Applies to a running deployment on 3.12.0 or later. Paths below are relative to the repository root; `<ken-host>` stands for your deployment's base URL.

---

## 0. What this actually is, and why the console can't just do it

Binding an endpoint to a station is **the one operation in this system that no console control performs.** It is not hidden, not permission-gated, not broken — it does not exist as a web route.

The entire COMM route table is six lines (`internal/web/app.go:199-206`, and only when `a.comm != nil`):

```
GET  /comm            GET  /comm/count       POST /comm/pair
POST /comm/channels/{id}/revoke   POST /comm/endpoints/{id}/revoke   POST /comm/endpoints/{id}/rotate
```

The only callers of the bind primitives in the whole tree are the MCP handlers: `RedeemBindingVoucher` at `internal/commserver/commserver.go:287` and `BindEndpointToStation` at `:291`, both inside `comm_bind`. `ken` has no `comm` subcommand at all (`cmd/ken/main.go:45-79`), and `ken station` is `add|list|key|requests` (`cmd/ken/cli_station.go`). Even the console's own copy tells you to delegate: `rooms.member_deaf_help` ends *"the session needs to bind"* (`internal/i18n/locales/messages.properties:701`).

| Operation | Console? | Where it actually happens |
|---|---|---|
| Bind an endpoint to a workspace | **NO — and it no longer needs one** | the session: `comm_bind`, with `X-Ken-Workspace` set. No voucher since 3.29.0 |
| Create a station | **Only by approving a pending request** — there is no `POST /stations`, no from-scratch form | `/stations` → **Approve and name**, or `ken station add` |
| Mint a station key | Yes — but **no actor field** (`internal/web/stations.go:499` passes the logged-in curator's `sess.ActorID`) | `/stations` → **Mint key**, or `ken station key --actor` |
| Revoke an endpoint | Yes | `/comm` → **Revoke** |
| Move an endpoint to another comm token | Yes, since 3.19.0 — one, or every live endpoint of a token | `/comm` → **Re-point**, **Re-point all** |
| Move a binding to another key of the same station | Yes, since 3.20.0 — one, or every endpoint one key bound | `/comm` → **Re-bind**, **Re-bind all** |
| See whether an endpoint is bound | Yes, since 3.20.0 — the **Bound by** column names the station key | `/comm` (was: inferable only from the Revoke confirm dialog, §6) |
| See which token owns an endpoint | Yes, since 3.19.0 — the **Owned by** column | `/comm` (was: `sqlite3` on `comm.db`) |
| See which ACTOR owns an endpoint | **NO** — `comm.html` renders the token, never the actor | `sqlite3` on `comm.db` |

So: three of the five bind/create items are two MCP calls by the session and **zero** console clicks. Two need a station created first, and that is where the labour lives.

**Two rows of this table were falsified by 3.19.0 and 3.20.0 and are corrected above.** The
console now answers "which token owns this" and "which key bound this" directly, and can move
either — which removes the `sqlite3` step this runbook used to budget for. The remaining `NO` is
the actor, which is a different question and still needs the database.

---

## 1. Pre-flight

### 1.1 THE TRAP — the actor rule

`comm_bind` redeems the voucher with a conditional UPDATE (`internal/store/station_binding.go:186-193`):

```sql
WHERE voucher_sha256=? AND redeemed_at IS NULL AND expires_at > now
  AND issued_for_endpoint=?   -- the endpoint redeeming it
  AND issued_to_actor=?       -- <— ep.Owner.ActorID, passed at commserver.go:287
```

`issued_to_actor` is the actor recorded on the **station key** that minted the voucher. The bound parameter is **the endpoint's own registering actor**. They must be the *same actor row*.

Consequences:

- **A key minted from the console belongs to you, the human curator** (`stations.go:499`, `sess.ActorID`), while COMM tokens are minted as `ai` actors (`cmd/ken/cli_token.go:33`; `ken station key --kind` defaults to `"ai"`, `cli_station.go:71`). A console-minted key therefore mints vouchers that fail **forever**, with `ErrVoucherNotYours` — a setup error no retry ever fixes (`station_binding.go:81-84`).
- **The "no COMM token" badge on `/stations` is NOT the test.** `ActorHasComm` is `EXISTS(SELECT 1 FROM api_token WHERE actor_id = key.actor_id AND revoked_at IS NULL AND scopes LIKE '%"comm"%')` (`internal/store/stations.go:352-355`). It only asks whether that key's actor holds *some* comm token. A key minted under actor A while the endpoint registered under actor B shows **no badge** and still cannot bind. Absence of the badge means "this actor could bind something", never "this key can bind *this* endpoint".
- The real check is a string comparison: the Actor cell on `/stations` (`stations.html:310`, rendered `kind:name`) against the actor that owns the endpoint's COMM token. **The console cannot show you the second value** — `/comm` renders no actor. Read it from `comm.db` (§1.4) or accept the diagnosis from the failure.

### 1.2 Per-endpoint prerequisites (all four must hold before the session starts)

1. **The session's `/comm/mcp` entry carries the SAME `ken_` token that registered that endpoint.** `auth()` ends with `if ep.Owner.TokenID != p.TokenID { return nil, errors.New("endpoint does not belong to this token") }` (`commserver.go:1064-1065`). `token_id` is written at registration (`internal/comm/endpoint.go:56-71`); a different machine's token fails even with a correct secret and a correct voucher. **Since 3.19.0 there IS a console override** — **Re-point** on `/comm`, per endpoint or for every live endpoint of a token at once — which moves the whole owner tuple and leaves the id, the secret, the channels, the binding and the queued mail untouched. Before it existed, a revoked token meant the endpoint was dead and its queued mail unreadable by anyone; now the move is one click, and the session needs the new token in its config plus a restart.

   **A BOUND endpoint has a SECOND weld, and re-pointing the token does not touch it.** `bound_by_station_key_id` names the station key that authorised the binding and is checked on every call, so **revoking** that key — or deleting its row — ends the session just as surely. (Retiring it does not: `IsStationKeyRevoked` reads `revoked_at` alone, so a retired key stops the station surface and leaves the COMM session running.) **Re-bind** (3.20.0) moves it to another key of the same station and costs the session nothing — no config edit, no restart. Move both, or the endpoint is half-moved in a way every count reconciles.
2. **The session has the endpoint's `endpoint_id` (22-char base62) and `endpoint_secret`.** Preferred as headers `X-Ken-Endpoint-Id` / `X-Ken-Endpoint-Secret` on the `/comm/mcp` entry — these win over the tool arguments (`commserver.go:993-1003`) and keep the secret out of the transcript. **All-or-nothing:** `withEndpointCred` (`:981-991`) ignores the header pair unless *both* are non-empty. If the secret is lost, the only recovery is you clicking **Rotate secret** on `/comm` (§5).
3. **The session's `/station/mcp` entry carries a `kens_` key FOR THAT STATION.** One key = one station; there is deliberately no `station_id` argument (`internal/stationserver/types.go:306-318`) — the station comes from the Authorization header. Four different stations need four different keys, i.e. four sessions (or four `/station` entries).
4. **That `kens_` key's actor == the endpoint's COMM token actor** (§1.1).

**MCP config must exist before the conversation starts.** Tool schemas and server instructions are captured by the client when the conversation begins and never refresh (`commserver.go:960-967`). You cannot add `/station/mcp` mid-session and use it.

### 1.3 One stale string to pre-empt

`comm_bind`'s own shipped description ends: *"If you are registering for the first time, pass the voucher to comm_register instead."* That is **false as of this tree** — `registerIn` has only `label` and `host_hint` (`internal/commserver/types.go:17-20`), and the MCP SDK rejects unknown arguments. Tell the session to ignore it; `comm_bind` is the only binding path.

### 1.4 Mapping "ep 6" to something the console can act on

`e:6` is the endpoint **rowid** (`endpointPartyKey` = `"e:" + endpoint.id`, `internal/comm/party.go:140`). The console shows only the 22-char `endpoint_id` (`comm.html:178`), sorted newest-first (`endpoint.go:394`, `ORDER BY created_at DESC`), so ep 6 is near the bottom. Endpoint labels name the **project** (FINISHING.md:551).

Not a console operation — read the map once, on the prod host:

```
sqlite3 "$KEN_COMM_DB" \
  "select id, endpoint_id, label, actor_id, token_id, coalesce(station_id,'-') from endpoint where revoked_at is null order by id;"
```
`KEN_COMM_DB` defaults to `<db dir>/comm/comm.db` (`cmd/ken/main.go:334`). Cross-reference `actor_id` against `ken.db`'s `actor` table to settle §1.1 before you spend an attempt.

---

## 2. ep 6 → `quest-infra` (station `JiJm1FZK9Afs08u0`) — do this first

Zero console steps if the pre-flight holds. Two tool calls.

| # | Who | Action |
|---|---|---|
| 1 | You | Confirm ep 6's `endpoint_id` and `actor_id` (§1.4), and that the live `quest-infra` key on `/stations` shows the **same** actor in the Actor column (`stations.html:310`). |
| 2 | You | Open the project that owns ep 6. Its `/comm/mcp` entry must already carry ep 6's registering `ken_` token plus the endpoint headers; add a `/station/mcp` entry with the `quest-infra` `kens_` key **before starting the session**. |
| 3 | You → session, in words | *"You are staffing quest-infra. Your endpoint is already registered but not bound. Add `X-Ken-Workspace: <the workspace id>` to this folder's Ken MCP entry, then call `comm_bind` with no arguments."* **The voucher steps below are gone as of 3.29.0** — there is nothing to fetch, nothing to echo back, and nothing you must avoid writing down, because the workspace id is not a secret. |
| 5 | Session | `mcp__ken-comm__comm_bind { "binding_voucher": "<value>" }`, with `endpoint_id`/`endpoint_secret` supplied via the headers. → `{station_id, note}`. (`commserver.go:266-299`) |

**Voucher hygiene:** TTL is 5 minutes (`store.VoucherTTL`, `station_binding.go:36`), single-use, and it is a **bearer credential narrowed to one endpoint**. Step 5 follows step 4 in the very next call. It is never written to a file and never sent over COMM.

**Note the `for_endpoint` echo** (`types.go:326-330`) — it exists precisely so a typo is caught before `comm_bind` returns a refusal that reads like a leak.

---

## 3. ep 14 → `proxmox-servers`

Identical to §2, on the project that owns ep 14, with the `proxmox-servers` `kens_` key. Same actor check first.

---

## 4. ep 13 → `rb5009-config` and ep 18 → `runway-prod-admin` (station does not exist yet)

**State plainly:** the console can create a station **only by approving a pending station request**. There is no `POST /stations` and no unsolicited create form (`app.go:181-194`; `stations.html` form inventory). A request is filed by an agent calling `station_request` — which needs a `kens_` key. A key with `station_id` NULL is a supported credential (`internal/store/stations.go:270-272`), but **no shipped surface mints one**: `ken station key` dies on an empty `--station` (`cli_station.go:74-76`) and the console route is `POST /stations/{id}/key`, which needs an existing station id (`app.go:186`).

So:

- **If that project's session already holds any `kens_` key** (for some other station), use path B — console.
- **If it holds none** (the normal case for ep 13 / ep 18), path A is the *only* path, and it is shell, not console.

### Path A — CLI on the prod host (`KEN_DB` pointed at the live database)

| # | Action |
|---|---|
| 1 | `ken station add --name rb5009-config --purpose "<what it is for>"` (`cli_station.go:29-48`). Dies on a duplicate name. |
| 2 | `ken station key --station rb5009-config --label <machine> --actor <actor-name> --kind ai` (`cli_station.go:66-104`). **`--actor` is the whole point** — it must be the actor holding ep 13's COMM token. Omitting it makes the CLI resolve and announce a candidate; a wrong one bricks the bind per §1.1. `--kind` defaults to `ai`, which matches how COMM tokens are minted. The key prints **once**, with the `claude mcp add` line. |
| 3 | Paste that key into the project's `/station/mcp` MCP config, alongside its existing `/comm/mcp` entry. |
| 4 | Restart the session (schemas pin at conversation start). |
| 5–6 | The session does §2 steps 4–5, naming ep 13's `endpoint_id`. |

Repeat for `runway-prod-admin` / ep 18.

**Do not mix the paths.** `ken station add` inserts into `station` only and never touches `station_request` (`internal/store/stations.go:49-68`); only console approval does both in one transaction (`internal/store/station_console.go:50-96`). Creating by CLI a station that was also requested leaves the request pending forever, with a live **Approve and name** form that will create a *second* station or flash `A station is already called {0}`. The CLI warns about exactly this (`cli_station.go:125-127`).

### Path B — console, when a request is already pending

1. `/stations` → section **Requests waiting for you** (`stations.requests_heading`, messages.properties:504).
2. On the **station** request (not a link request — that one has a hidden `kind=link` and no name field, `stations.html:224-228`), type the real name into **Name this station** (`input name="name"`, placeholder `prod-ops`) and click **Approve and name** (`stations.html:230-239`; route `app.go:184`; handler `stations.go:427-444`). The agent's `name_hint` carries no weight — *"a suggestion only; what you type below is what is used"*.
3. Success flash: **`Station {0} created.`** Approval creates the station and **nothing else** — no key, no binding (`station_console.go:76-87` = one INSERT + one UPDATE).
4. Mint the key: same page, station card → **New key for** `<machine>` → **Mint key** (`stations.html:333-342`; route `app.go:186`). **Only if your curator actor is the one holding that machine's COMM token** — the form has no actor field. Otherwise use `ken station key --actor` (Path A step 2).
5. Then Path A steps 3–6.

---

## 5. ep 10 → revoke `collector-proxy-dev` (0 seats / 0 sent / 0 received)

Pure console, one click.

1. `/comm` → **Registered sessions** table → the row whose `endpoint_id` is ep 10's.
2. Click **Revoke** (red, `comm.revoke`, messages.properties:416; form `comm.html:193-196` → `POST /comm/endpoints/{id}/revoke`, `app.go:205`).
3. Confirm dialog. Because ep 10 is unbound you will see `comm.revoke_endpoint_confirm` — *"…every peer must re-pair from a new code…"*. Accept.
4. Flash: **`Session endpoint {0} revoked`** (messages.properties:70). The row disappears — `ListEndpoints` filters `revoked_at IS NULL` (`endpoint.go:394`).

**Do not click the adjacent Rotate secret button by mistake.** Revoke is irreversible.

---

## 6. The 83 deliveries already addressed to `e:6`

**Binding does not re-file them. Say it flat: they stay `e:6` forever.**

`BindEndpointToStation` (`internal/comm/endpoint.go:193-249`) executes exactly one statement of consequence — `UPDATE endpoint SET station_id=?, bound_by_station_key_id=?, bound_at=…` — and touches no `delivery` row. (The old sequence-counter carry-over is gone; sequences are per-scope since migration 0009, so binding mid-life no longer breaks a channel.)

What that means concretely:

| | Before bind | After bind |
|---|---|---|
| The 83 existing `e:6` deliveries | readable by ep 6 only | **still readable by ep 6 only** |
| Mail sent to quest-infra *after* the bind | impossible — nothing was filed under `s:JiJm1FZK9Afs08u0` | filed under `s:JiJm1FZK9Afs08u0`, inheritable |
| A successor session on a new endpoint bound to quest-infra | sees nothing | sees the `s:` rows, **never the 83** |

Ep 6 keeps reading its own backlog because the poll predicate ORs both forms for a bound endpoint (`internal/comm/message.go:494-499`: `(d.party_key = 's:<station>' OR d.party_key = 'e:<rowid>')`). Filing is decided at **write** time (`endpointParty`, `party.go:120-140`). So the fix is forward-looking only: **if you want any of those 83 to survive ep 6, that session must act on them before it dies.** Tell it so in the same message as the bind instruction.

---

## 7. If you see X, it means Y

All strings verbatim from source.

| Error the session reports | Where | What it means | Fix |
|---|---|---|---|
| `endpoint does not belong to this token` | `commserver.go:1065` | The `/comm/mcp` bearer token is not the one that registered this endpoint. | Use the right machine/project. No override exists. |
| `endpoint_id and endpoint_secret are required — call comm_register first. They may be sent as the X-Ken-Endpoint-Id and X-Ken-Endpoint-Secret headers…` | `commserver.go:1005-1007` | Neither headers nor arguments carried a full pair. Remember the headers are all-or-nothing. | Fix the MCP entry. |
| `this endpoint_id/endpoint_secret pair is not valid — the endpoint may never have existed, the secret may be wrong, or the endpoint may have been swept after going idle…` | `commserver.go:1053-1060` | Deliberately identical for unknown / wrong-secret / swept. | If the row is still on `/comm`: click **Rotate secret** and hand the new one over. If gone: re-register + a fresh voucher. |
| `this endpoint is already bound to a station — an endpoint cannot move between stations…` | `commserver.go:283-286` | Someone already bound it, possibly to the wrong station. | `comm_unbind` (MCP only — also not a console control), then re-bind. |
| `this binding voucher was issued to a different identity than the one presenting it…` (`ErrVoucherNotYours`) | `station_binding.go:81-84` | **The §1.1 trap.** Station key actor ≠ endpoint's COMM token actor. | **STOP. Do not retry — a fresh voucher fails identically.** Re-mint the key with `ken station key --actor <correct> --kind ai`. |
| `this binding voucher names a different endpoint than the one redeeming it…` | `station_binding.go:77-79` | Wrong `endpoint_id` in the voucher call — the `for_endpoint` echo would have caught it. | Ask for a voucher naming *this* `endpoint_id`. A retry works. |
| `binding voucher is not valid — it may be unknown, already used, or expired (they last a few minutes; ask /station for a fresh one)` | `station_binding.go:43` | Expired (>5 min), already redeemed, unknown — collapsed on purpose. Also fires if the station was archived between issue and redemption. | Ask for a fresh voucher and redeem it in the next call. |
| `station %q is archived, so it cannot bind new endpoints — tell your human; they can unarchive it from the /stations console` | `stationserver.go:288-290` | Refused at **issue**, not redemption. | `/stations` → unarchive → retry. |
| `this key is not bound to a station yet — call station_request to ask your human to create one…` | `internal/stationserver/auth.go:62-72` | The `/station/mcp` key has `station_id` NULL. | §4. |
| HTTP 401, body `invalid token` | `commserver/auth.go:96,168-169` | Transport-level: bad or wrong-scope bearer on `/comm/mcp`. | Check the token and its `comm` scope. |
| `not found` (bare) | `endpoint.go:193-250` → `commserver.go:291,1096` | `BindEndpointToStation` found no row with `station_id IS NULL AND revoked_at IS NULL` — endpoint revoked, or bound in the split second since. | Re-check on `/comm`. |

---

## 8. Verification, from the console

There is **no station column on `/comm`** and no per-station staffing display outside rooms. These are the checks that actually exist.

**Per endpoint (ep 6, 14, 13, 18) — the confirm-dialog tell.**
`/comm` → the endpoint's row → click **Revoke** to raise the dialog, read the first sentence, then **Cancel**. The template picks the sentence from `Bound` (`comm.html:193`, computed at `internal/web/comm.go:144` as `ep.StationID != ""`):

| Dialog opens with | Meaning |
|---|---|
| *"…This cannot be undone, and it cuts off the session holding this secret at its next call. **The STATION keeps its channels**: a replacement session binds with a voucher and inherits them…"* (`comm.revoke_bound_endpoint_confirm`, messages.properties:621) | **BOUND — the migration landed.** |
| *"…This cannot be undone: **every peer must re-pair from a new code** before they can reach it again…"* (`comm.revoke_endpoint_confirm`, messages.properties:420) | Still unbound. |

Ugly, but it is a genuine console read of the bound flag. Cancel — do not confirm.

**Room-level, and the reason ep 6 mattered.** If the station is a member of a room, `/stations` renders a `not bound` amber badge per member (`stations.html:73`, `rooms.member_deaf`, messages.properties:700-701) and an `N not bound` count on the room header (`stations.html:38`), both driven by `staffing[station].Endpoints > 0` (`internal/web/stations.go:260-263`). **After binding, quest-infra's badge clears and the room's count drops by one.** That is the cleanest positive confirmation available — put quest-infra in a room if it is not already, and the check becomes a glance.

**ep 10.** Its row is gone from **Registered sessions** on `/comm`, and the flash read `Session endpoint <id> revoked`.

**Authoritative check, not a console one** (state it as such in any note you leave):

```
sqlite3 "$KEN_COMM_DB" \
  "select id, endpoint_id, label, coalesce(station_id,'UNBOUND'), bound_at from endpoint where revoked_at is null order by id;"
```
Expect ep 6 → `JiJm1FZK9Afs08u0`, ep 13/14/18 → their station ids with a non-null `bound_at`, ep 10 absent.

**Session-side confirmation, free:** `comm_bind` returns `note: "Bound. Your endpoint_id, secret and channels are unchanged — nothing to re-pair. Your mail now belongs to the station…"` (`commserver.go:295-297`). Have the session report `station_id` back to you and match it against `/stations`.

---

## 9. Step count, honestly

| Item | Console clicks | Shell | Session calls |
|---|---|---|---|
| ep 6 → quest-infra | 0 (2 to verify) | 0 | 2 |
| ep 14 → proxmox-servers | 0 (2 to verify) | 0 | 2 |
| ep 13 → rb5009-config | 0 (Path A) or 2 (Path B) | 2 commands | 2 + a session restart |
| ep 18 → runway-prod-admin | 0 (Path A) or 2 (Path B) | 2 commands | 2 + a session restart |
| ep 10 revoke | 2 | 0 | 0 |

The irreducible cost for a new station is: create → mint key → paste into MCP config → restart the session → voucher → bind. Steps 3 and 4 are outside Ken entirely, which is why this has felt laborious; it is structural, not cosmetic, and closing Batch 6 does not fix it.