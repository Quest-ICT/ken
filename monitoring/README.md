# Ken monitoring bundle

Drop-in Prometheus + Grafana assets for Ken. Ken exposes Prometheus metrics at
`/metrics` (loopback-only by default) and health at `/health` + `/healthz`. The
whole stack is hand-rolled and dependency-free — no `client_golang`, no separate
exporter, and metrics are pull-based (nothing is written to disk).

## Scrape config (`prometheus.yml`)

Local Prometheus on the same host (default — no token needed, loopback is always allowed):

```yaml
scrape_configs:
  - job_name: ken
    metrics_path: /metrics
    static_configs:
      - targets: ['127.0.0.1:8080']
```

Remote Prometheus — set `KEN_METRICS_TOKEN` on Ken (or add the scraper's network to
`KEN_METRICS_CIDRS`) and present the token as a bearer:

```yaml
scrape_configs:
  - job_name: ken
    metrics_path: /metrics
    scheme: https
    authorization:
      type: Bearer
      credentials: '<KEN_METRICS_TOKEN>'
    static_configs:
      - targets: ['kb.example.com:443']
```

## Files

- `ken-prometheus-alerts.yml` — alerting rules; reference from `rule_files:` in `prometheus.yml`.
- `ken-grafana-dashboard.json` — import into Grafana (Dashboards → Import → Upload JSON),
  then pick your Prometheus datasource. A starting point; tune panels to taste.

## Metrics reference

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `ken_build_info` | gauge | `version` | Constant 1; build version in the label |
| `ken_uptime_seconds` | gauge | | Seconds since process start |
| `ken_goroutines` | gauge | | Live goroutines |
| `ken_memory_heap_bytes` | gauge | | Heap memory in use (bytes) |
| `ken_http_requests_total` | counter | `surface`, `outcome` | Web requests by outcome class |
| `ken_http_request_duration_seconds` | histogram | `surface` | Request latency, web surface (percentiles via `histogram_quantile`; `_sum`/`_count` give the mean) |
| `ken_mcp_tool_calls_total` | counter | `tool`, `outcome` | MCP `kb_*` (and `comm_*`) calls (success/error) |
| `ken_mcp_tool_duration_seconds` | histogram | `tool` | Per-tool handler latency (work time; the blocking `comm_poll` is excluded) |
| `ken_auth_failures_total` | counter | `surface` | Rejected authentications (e.g. bad token) |
| `ken_ratelimit_rejected_total` | counter | | 429 throttles |
| `ken_ratelimit_blocked_total` | counter | | 403s from auto-blocked IPs |
| `ken_kb_entries` | gauge | | Entries (curated or draft) |
| `ken_kb_versions` | gauge | | Versions (append-only history) |
| `ken_kb_proposals_pending` | gauge | | Proposals awaiting human promotion |
| `ken_kb_embeddings` | gauge | | Versions with an embedding |
| `ken_kb_embeddable_versions` | gauge | | Total versions (embedding denominator) |
| `ken_users` | gauge | | Human users |
| `ken_tokens_active` | gauge | | Active (non-revoked) agent tokens |
| `ken_comm_endpoints` | gauge | | Registered inter-session endpoints (sessions) — COMM only |
| `ken_comm_channels_open` | gauge | | Open inter-session channels — COMM only |
| `ken_comm_messages_unacked` | gauge | | Messages delivered/queued but not acknowledged — COMM only |
| `ken_comm_message_bytes` | gauge | | Bytes of retained message bodies — COMM only |
| `ken_comm_poll_waiters` | gauge | | Long-poll receive calls currently parked — COMM only |
| `ken_comm_files` | gauge | | Live file attachments (offered or awaiting delivery) — COMM only |
| `ken_comm_file_bytes` | gauge | | Relay bytes currently held on disk — COMM only |
| `ken_db_connections_open` / `ken_db_connections_in_use` | gauge | `pool` | DB pool (reader/writer) |
| `ken_db_wait_total` | counter | `pool` | DB pool waits |

The `ken_comm_*` gauges appear **only when `KEN_COMM_ENABLED=1`** — absent series, not zeros,
on a default install. COMM is deliberately absent from `/health` (which marks the whole service
DOWN on any component failure), so these gauges are the only place a runaway channel shows up.

Naming follows Prometheus conventions: counters end in `_total`; gauges carry no reserved
suffix (a gauge named `_total`/`_info`/`_count` is silently renamed or dropped at exposition).

## Health

- `GET /healthz` — liveness, plain `ok` (public; the k8s-style liveness probe).
- `GET /health` — readiness JSON `{"status":"UP","components":{…}}` (public). Per-component
  **details** (paths, error strings) appear only for loopback/token/CIDR callers — the
  "show-details: when-authorized" posture. Returns **503** when any component is DOWN.

## Security

`/metrics` is **not** public: loopback + `KEN_METRICS_CIDRS` are always allowed; any other
scraper must present `KEN_METRICS_TOKEN` as a bearer token. The gate authorizes on the direct
peer (`RemoteAddr`), never `X-Forwarded-For`, so a forwarded header can't forge access. Behind
a reverse proxy, scrape Ken directly on its internal address, or set a token. `KEN_METRICS=off`
removes `/metrics` entirely.

Behind a declared trusted proxy (`KEN_TRUSTED_PROXIES`), the gate resolves the real client IP
(validated `X-Forwarded-For`) rather than the proxy's loopback address, so a co-located proxy
does not make every scraper look local. Only the trivial `/healthz` liveness probe is exempt
from the rate limiter; `/health` and `/metrics` are throttled like any path (loopback and
allow-CIDR callers bypass the limiter anyway).
