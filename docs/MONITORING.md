# Monitoring Ken — health & metrics

Ken ships a small, dependency-free observability surface: a liveness probe, a
readiness health report, and a Prometheus metrics endpoint. It is deliberately
lightweight — hand-rolled counters and an on-scrape encoder, **no
`client_golang`** — so it adds essentially nothing to the binary size or the
running footprint, and it writes **nothing to disk** (metrics are pull-based;
Prometheus keeps the history on its side).

Ready-to-use Grafana dashboard, Prometheus alert rules, and a scrape snippet live
in [`monitoring/`](../monitoring/).

## Endpoints

| Path | Purpose | Access |
|---|---|---|
| `GET /healthz` | Liveness — plain `ok`, no dependency checks | **public** |
| `GET /health` | Readiness — JSON `{"status":"UP","components":{…}}` | **public** (component details only for operators) |
| `GET /metrics` | Prometheus text exposition | **gated** (loopback / token / CIDR) |

### `/healthz` (liveness)
Always `200 ok`. Use it for a load-balancer or `systemd`/k8s liveness probe. It is
exempt from the rate limiter, so probes are never throttled.

> **Check it on the port your deployment actually listens on.** `localhost:8080` is the
> plain-HTTP default; a deployment that terminates TLS in-process listens on **:443**
> (and :80 only for the redirect and ACME), so the 8080 form fails with *"Failed to
> connect"* on a perfectly healthy server. That matters more than it sounds, because the
> moment an operator runs a health check is usually **seconds after a restart**, when
> "it's down" is the most believable explanation available — a connection refused there
> reads as an outage the change just caused. It cost a production operator a minute of
> exactly that. Derive the URL from your own configuration:
>
> ```bash
> curl -fsS "$(systemctl show ken -p Environment | grep -qi 'KEN_TLS=off\|KEN_TLS=$' && echo http://localhost:8080 || echo https://localhost)/healthz"
> ```
>
> or simply use the scheme, host and port this instance is configured with. A verification
> step that cries wolf after a restart trains people to stop running it.

### `/health` (readiness)
Returns the Actuator-style shape:

```json
{
  "status": "UP",
  "components": {
    "db":      { "status": "UP" },
    "storage": { "status": "UP", "details": { "path": "/opt/ken/data", "writable": true } }
  }
}
```

- `db` — the database answers a ping (reader pool).
- `storage` — the data directory exists and is writable (a temp file is created and removed).

Any component DOWN flips the overall status to `DOWN` and the response code to **503**
(so a proxy/orchestrator can act on it). Per-component **details** (the path, error
strings) are shown only to an operator — a loopback caller, an allowlisted CIDR, or a
request bearing `KEN_METRICS_TOKEN`. Anonymous/public callers see status only, so the
readiness probe never leaks the filesystem layout.

### `/metrics` (Prometheus)
Prometheus text format (`Content-Type: text/plain; version=0.0.4`). See
[`monitoring/README.md`](../monitoring/README.md) for the full metric reference. Highlights:

- **Traffic** — `ken_http_requests_total{surface,outcome}`, `ken_http_request_duration_seconds`
  (**histogram** — request latency for the web surface; use `histogram_quantile(0.95, …)` for p95).
- **MCP** — `ken_mcp_tool_calls_total{tool,outcome}` and `ken_mcp_tool_duration_seconds{tool}`
  (**histogram**, per-tool handler latency) cover **every** MCP surface that is mounted: `kb_*`,
  `comm_*` and `station_*` are all core and on by default, so all three appear on a stock install —
  a missing `comm_*` or `station_*` series means the surface was turned off, not that it is idle. `comm_poll` is deliberately excluded from the latency histogram — it is a
  long-poll, and a parked wait is not latency.
- **No station-specific gauges exist**, and their absence is stated rather than left to be inferred:
  there is no equivalent of the `ken_comm_*` series counting stations, notebook bytes or open tasks.
  Per-tool call counts are the only station signal today. The per-station usage an operator actually
  wants — assets against their caps — is on the `/stations` console instead.
- **Knowledge base** — `ken_kb_entries`, `ken_kb_versions`, `ken_kb_proposals_pending`,
  `ken_kb_embeddings` / `ken_kb_embeddable_versions`, `ken_users`, `ken_tokens_active`.
- **Inter-session comms** (core, always registered; these series are absent only if `comm.db`
  could not be opened and COMM degraded to disabled — a runtime state, not a setting, and
  deliberately so: an expendable database must never take the durable knowledge base down; see
  [`COMM.md`](COMM.md)) —
  `ken_comm_endpoints`, `ken_comm_channels_open`, `ken_comm_messages_unacked`,
  `ken_comm_message_bytes`, `ken_comm_poll_waiters`, `ken_comm_files`, `ken_comm_file_bytes`. COMM is deliberately **absent from
  `/health`**: that endpoint marks the whole service DOWN on any component failure, and an
  ephemeral messaging subsystem must not pull a healthy knowledge base out of rotation. Watch it
  here instead — a climbing `ken_comm_messages_unacked` or `ken_comm_message_bytes` is the signal
  that a channel has run away.
- **Abuse guard** — `ken_ratelimit_rejected_total`, `ken_ratelimit_blocked_total`, `ken_auth_failures_total{surface}`.
- **Process** — `ken_build_info{version}`, `ken_uptime_seconds`, `ken_goroutines`, `ken_memory_heap_bytes`, `ken_db_*`.

## Configuration

| Env | Default | Effect |
|---|---|---|
| `KEN_METRICS` | `on` | `off` removes `/metrics` entirely |
| `KEN_METRICS_TOKEN` | *(unset)* | Bearer token a remote Prometheus presents to scrape |
| `KEN_METRICS_CIDRS` | *(unset)* | Extra CIDRs allowed to scrape `/metrics` and see `/health` details |

`/health` and `/healthz` have no configuration — they are always on.

## Security

`/metrics` exposes internal counts (entry totals, request/error volumes, rate-limit
state), so it is **not public**:

- **Loopback** (`127.0.0.1`, `::1`) and any network in `KEN_METRICS_CIDRS` are always allowed.
- Any other peer must present `KEN_METRICS_TOKEN` as `Authorization: Bearer <token>` (compared
  in constant time). Without a token configured, only loopback/CIDR callers can scrape.
- The gate authorizes on the **client IP**, resolved through the same trusted-proxy machinery
  as the rate limiter: a raw `X-Forwarded-For` is never believed, but behind a **declared**
  proxy (`KEN_TRUSTED_PROXIES`) the gate uses the real, validated client address — so a
  co-located reverse proxy does not make every scraper appear as loopback. If a proxy fronts
  Ken but isn't declared trusted, set `KEN_METRICS_TOKEN` (Ken logs a startup note about this).
- A denied scrape gets `404` (the endpoint isn't advertised), not `403`.
- Only `/healthz` is exempt from the rate limiter; `/health` and `/metrics` are throttled like
  any path (loopback / allow-CIDR callers bypass the limiter regardless).

## Footprint

The metrics registry is a handful of `atomic.Int64` counters plus gauge collectors that run
cheap `COUNT` queries at scrape time; the encoder is ~150 lines. No new dependency, kilobytes
of memory, nanosecond increments on the hot path, and one small `runtime.ReadMemStats` per
scrape. Choose your scrape interval (15–60s) and the cost is negligible.

## Quick start

```sh
# Local Prometheus, same host — no token needed:
curl -s http://127.0.0.1:8080/metrics | head
curl -s http://127.0.0.1:8080/health

# Then point Prometheus at it and import the dashboard:
#   scrape:  monitoring/README.md
#   alerts:  monitoring/ken-prometheus-alerts.yml
#   grafana: monitoring/ken-grafana-dashboard.json
```

## The backup blind spot (watch systemd, not `/metrics`)

Ken's metrics cover the **running server**; they say nothing about whether last night's **snapshot**
happened. That gap matters because a snapshot run that fails leaves **no snapshot for that run** (see
[BACKUP.md](BACKUP.md)). The run exits non-zero and systemd marks
`ken-snapshot.service` failed, but nothing pages you: the shipped units carry no `OnFailure=`.

So watch it at the systemd layer:

```sh
systemctl is-failed ken-snapshot.service          # "failed" = a run kept nothing
systemctl list-timers ken-snapshot.timer          # last run / next run
journalctl -u ken-snapshot.service -n 20 --no-pager
ls -lt /opt/ken/backups | head                    # newest snapshot: is it recent, and .db.gz?
```

Two low-effort ways to make a failure loud, either is enough:

- **A systemd failure handler** — add `OnFailure=<your-alert-unit>.service` to a drop-in on
  `ken-snapshot.service` (`systemctl edit ken-snapshot.service`), pointing at whatever already
  notifies you.
- **A freshness check from your existing monitoring** — alert if the newest file in the backups
  directory is older than ~26 hours. This catches every failure mode at once (timer stopped, disk
  full, fail-closed encrypt, unit removed by an upgrade) and needs nothing from Ken.

The second is the one to pick if you only do one: it asserts the property you actually care about — *a
recent restorable snapshot exists* — rather than that a particular unit ran.
