# Connecting claude.ai as a custom connector (OAuth)

Ken ships an **optional OAuth 2.1 authorization server** so you can add it to
**claude.ai** as a *custom connector* using the normal **"Connect" (OAuth)**
button — no beta feature, no request-headers field, no static token to paste.
This is the path Anthropic's personal (Pro/Max) connector UI expects: that UI is
**OAuth-only**, so before this existed the only way to connect Ken on a personal
account was Route B (per-machine `claude mcp add`; see [AI-INTEGRATION.md](AI-INTEGRATION.md)).

**It is ON and there is nothing to enable.** `KEN_OAUTH_ENABLED` was removed in **2.0.0** and
`cmd/ken/main.go` now hardcodes it; `TestNoFeatureCanBeSwitchedOff` fails the build if anything
reads the retired variable again. Ken's static bearer tokens keep working exactly as before — the
connector path is *additional*, never a replacement.

> **This paragraph said "It is off by default" until 2026-08-25**, four releases after the switch
> was removed. That is the class this project keeps finding: text asserting a control that does not
> exist. INSTALL.md carried both statements four lines apart — "off by default" and "there is
> nothing to enable" — and neither reader nor writer noticed.

---

## What it does (and does not) grant

- A connector you approve authenticates over MCP with the **same capability as an
  agent token: `read`, `write-draft`, `propose`** — it can search, fetch, create
  **drafts**, and propose **enhancements**. It **can never `curate`** (promote a
  draft/proposal to the curated head). Promotion stays a human act in the web UI.
  That invariant is enforced server-side, not by the connector's honesty.
- Every write from a connector is authored by a single shared `ai` actor named
  after the client (e.g. *Claude*), so its contributions are attributable.
- You can **revoke** a connection at any time on the **Tokens** page
  (*Connected apps (OAuth)* → Revoke). Revocation is instant: it kills the
  grant, and every access/refresh token under it dies on the next call regardless
  of token lifetime.

## What it needs

**Nothing to enable.** The authorization server is on by default and there is no
switch. `KEN_OAUTH_ENABLED` was removed in **2.0.0** and setting it does nothing —
this page instructed you to set it until 2026-08-20, which is why the removal is
stated here rather than quietly dropped.

What OAuth *does* require is a public **HTTPS** origin: claude.ai only talks to
https, and the issuer must match the URL you type.

```sh
journalctl -u ken -n5 | grep -i oauth   # -> "OAuth: authorization server ENABLED …"
```

When enabled, Ken serves the discovery + registration + token endpoints and the
`/mcp` endpoint returns the OAuth **discovery challenge** on an unauthenticated
request. When disabled, none of those endpoints exist and `/mcp` returns a plain
401 — behaviour identical to before this feature.

## Connect from claude.ai

1. In claude.ai: **Settings → Connectors → Add custom connector**.
2. Enter the MCP URL exactly: `https://<your-ken-host>/mcp`. The path **`/mcp` must
   be included** — the resource identifier must match byte-for-byte.
3. Click **Connect**. claude.ai discovers the authorization server, registers
   itself, and opens Ken's **login** page in your browser.
4. **Log in to Ken** with your curator credentials, then **Approve** the consent
   screen. claude.ai receives the authorization and the connector goes live —
   in claude.ai chat (web, desktop, mobile) **and** automatically in Claude Code
   on every machine signed in to that account.

To disconnect: revoke it on Ken's **Tokens** page, and/or remove the connector in
claude.ai.

## How it works (the security model)

- **PKCE (S256) is mandatory** on every authorization request; Ken rejects a
  missing or `plain` challenge.
- **Authorization codes are single-use and short-lived** (60 s), bound to the
  client and its exact redirect URI; the redeem is an atomic consume-or-fail.
- **Access tokens are opaque, short-lived (1 h), and only their SHA-256 is
  stored.** claude.ai refreshes them automatically.
- **Refresh tokens rotate on every use.** Re-presenting an already-rotated
  refresh token is treated as theft: the **entire grant is revoked** (OAuth 2.1
  §4.14.2). Ken's reference flow verifies this end-to-end.
- **Redirect URIs are exact-matched** against the client's registered set on both
  the authorize and token steps — no open-redirect / code-interception.
- Dynamic client registration is open (as the spec intends and as claude.ai
  requires) but bounded: redirect URIs must be https or loopback, and every OAuth
  endpoint sits behind Ken's per-IP abuse guard. A registered client is inert
  until a human approves it.

## Verify it (from any shell)

```sh
BASE=https://<your-ken-host>

# discovery documents resolve and name the right resource/issuer
curl -s $BASE/.well-known/oauth-protected-resource/mcp     # resource == $BASE/mcp
curl -s $BASE/.well-known/oauth-authorization-server       # issuer == $BASE, S256 advertised

# an unauthenticated MCP call returns the discovery challenge
curl -s -o /dev/null -D - -X POST $BASE/mcp -d '{}' | grep -i www-authenticate
#   WWW-Authenticate: Bearer resource_metadata="…/.well-known/oauth-protected-resource/mcp"
```

## Endpoints (for reference)

| Path | Purpose |
|------|---------|
| `/.well-known/oauth-protected-resource` (+ `/mcp`) | RFC 9728 resource metadata |
| `/.well-known/oauth-authorization-server` | RFC 8414 AS metadata |
| `/oauth/register` | RFC 7591 dynamic client registration (JSON) |
| `/oauth/authorize` | consent (human login required) |
| `/oauth/token` | authorization_code + refresh_token grants (form-encoded) |

All are mounted unconditionally. They were inert without `KEN_OAUTH_ENABLED` until **2.0.0** removed it.
