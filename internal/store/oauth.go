package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// This file implements Ken's optional OAuth 2.1 authorization-server storage:
// clients (dynamic registration), human-approved grants, single-use PKCE codes,
// and rotating opaque access/refresh tokens. Only SHA-256 of any secret is
// persisted; the plaintext is returned to the caller exactly once. All writes go
// through the single-writer connection (s.W); the hot MCP-auth read path
// (ValidateOAuthAccessToken) uses the reader pool (s.R).
//
// Design notes captured here because they are load-bearing:
//   - A grant is the durable unit of consent; revoking it (revoked_at) is
//     re-checked on EVERY token use, so revocation is instant regardless of the
//     access-token TTL.
//   - Authorization codes are single-use: the atomic DELETE row-count is the
//     double-spend guard (a race can only let one exchange win).
//   - Refresh tokens ROTATE: a rotated token is kept with revoked_at set (not
//     deleted) so that re-presenting it is detectable as theft and revokes the
//     whole grant (OAuth 2.1 §4.14.2 / MCP spec).

const nowExpr = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`

// refreshReuseGrace: re-presenting a rotated refresh within this window is treated
// as a benign retry (the rotation's response was lost in transit), not theft — the
// grant survives. Beyond it, reuse revokes the whole grant (OAuth 2.1 §4.14.2).
const refreshReuseGrace = 60 * time.Second

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// secondsArg renders a TTL as a SQLite datetime modifier ("+3600 seconds"); the
// sign is always explicit so a (defensive) negative TTL stays valid SQL.
func secondsArg(ttl time.Duration) string {
	return fmt.Sprintf("%+d seconds", int(ttl.Seconds()))
}

// --- clients (RFC 7591 dynamic client registration) ---

// OAuthClient is a registered public client (PKCE; no secret).
type OAuthClient struct {
	ClientID     string
	Name         string
	RedirectURIs []string
}

// RegisterOAuthClient mints a new public client_id for the given name +
// redirect-URI allowlist and returns it. The caller validates the redirect URIs
// (https or loopback) before calling.
func (s *Store) RegisterOAuthClient(ctx context.Context, name string, redirectURIs []string) (string, error) {
	clientID, err := randBase62(32)
	if err != nil {
		return "", err
	}
	clientID = "kenc_" + clientID
	uris, _ := json.Marshal(redirectURIs)
	if _, err := s.W.ExecContext(ctx,
		`INSERT INTO oauth_client(client_id, client_name, redirect_uris) VALUES(?,?,?)`,
		clientID, nullStr(name), string(uris)); err != nil {
		return "", err
	}
	return clientID, nil
}

// OAuthClientByID returns the registered client or ErrOAuthNoClient.
func (s *Store) OAuthClientByID(ctx context.Context, clientID string) (*OAuthClient, error) {
	var name sql.NullString
	var uris string
	err := s.R.QueryRowContext(ctx,
		`SELECT client_name, redirect_uris FROM oauth_client WHERE client_id=?`, clientID).
		Scan(&name, &uris)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOAuthNoClient
	}
	if err != nil {
		return nil, err
	}
	c := &OAuthClient{ClientID: clientID, Name: name.String}
	_ = json.Unmarshal([]byte(uris), &c.RedirectURIs)
	return c, nil
}

// --- authorization: grant + single-use code ---

// NewAuthCode is the input to CreateOAuthGrantAndCode: everything captured at the
// consent step. ConnectorActorID is the 'ai' actor that will author MCP writes;
// HumanActorID is the curator who approved.
type NewAuthCode struct {
	ClientID            string
	ConnectorActorID    int64
	HumanActorID        int64
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	Resource            string
}

// RevokeAbandonedGrants revokes grants that can never produce a token again.
//
// *** A GRANT IS CREATED AT /authorize, NOT AT TOKEN EXCHANGE. *** So a retry, a double-submit, or
// a consent screen the human closes leaves a row with revoked_at IS NULL forever, and nothing ever
// cleaned it up. ken-prod-ops measured it on the live deployment: one human action produced grants
// g13 and g14 for the same client 2.3 seconds apart — g13 with an access and a refresh token, g14
// with none, both listed as connected applications, distinguishable only by an Active-tokens count
// of 0.
//
// Vlad's rule is that two active authorizations is a fault rather than a feature. This is the
// mechanism by which one silently becomes two, and it is invisible in the console.
//
// THE PREDICATE IS "CAN NEVER SUCCEED", NOT "LOOKS UNUSED", and the difference matters. A grant is
// abandoned only when it has issued NO token AND its authorization code is gone — consumed or
// expired. An authorization code is single-use and short-lived, so such a grant has no path left
// to a token. A grant whose code is still live is mid-flow and MUST NOT be touched: the human may
// be finishing the consent screen right now, and revoking it would break the very flow that
// created it.
//
// Returns how many it revoked, so a caller can log a number rather than a reassurance.
func (s *Store) RevokeAbandonedGrants(ctx context.Context) (int64, error) {
	res, err := s.W.ExecContext(ctx, `
UPDATE oauth_grant SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
 WHERE revoked_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM oauth_token t WHERE t.grant_id = oauth_grant.id)
   AND NOT EXISTS (SELECT 1 FROM oauth_auth_code c
                    WHERE c.grant_id = oauth_grant.id
                      AND c.expires_at > strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CreateOAuthGrantAndCode records the human's approval as a durable grant and a
// single-use authorization code (hashed), returning the plaintext code once.
func (s *Store) CreateOAuthGrantAndCode(ctx context.Context, in NewAuthCode, codeTTL time.Duration) (string, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO oauth_grant(client_id, actor_id, human_actor_id, scope, resource) VALUES(?,?,?,?,?)`,
		in.ClientID, in.ConnectorActorID, in.HumanActorID, in.Scope, nullStr(in.Resource))
	if err != nil {
		return "", err
	}
	grantID, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	code, err := randBase62(48)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO oauth_auth_code(code_sha256, grant_id, client_id, redirect_uri, code_challenge, code_challenge_method, scope, resource, expires_at)
VALUES(?,?,?,?,?,?,?,?, strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		sha256hex(code), grantID, in.ClientID, in.RedirectURI, in.CodeChallenge, in.CodeChallengeMethod,
		in.Scope, nullStr(in.Resource), secondsArg(codeTTL)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return code, nil
}

// CodeData is the authorization-code record needed to validate a token exchange.
type CodeData struct {
	GrantID             int64
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	Resource            string
}

// PeekOAuthCode reads (without consuming) a non-expired authorization code so the
// token endpoint can verify client_id, redirect_uri, and PKCE before committing.
// ErrOAuthBadCode if missing or expired. Consumption happens in ExchangeOAuthCode.
func (s *Store) PeekOAuthCode(ctx context.Context, code string) (*CodeData, error) {
	var d CodeData
	var res sql.NullString
	err := s.R.QueryRowContext(ctx, `
SELECT grant_id, client_id, redirect_uri, code_challenge, code_challenge_method, scope, resource
FROM oauth_auth_code
WHERE code_sha256=? AND expires_at > `+nowExpr,
		sha256hex(code)).
		Scan(&d.GrantID, &d.ClientID, &d.RedirectURI, &d.CodeChallenge, &d.CodeChallengeMethod, &d.Scope, &res)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOAuthBadCode
	}
	if err != nil {
		return nil, err
	}
	d.Resource = res.String
	return &d, nil
}

// ExchangeOAuthCode atomically consumes the code (single-use) and issues a new
// access + refresh token pair under its grant, returning both plaintexts once.
// The DELETE row-count is the double-spend guard: if the code was already used
// (or expired) between Peek and here, RowsAffected is 0 and this returns
// ErrOAuthBadCode without issuing anything.
func (s *Store) ExchangeOAuthCode(ctx context.Context, code string, grantID int64, accessTTL, refreshTTL time.Duration) (access, refresh string, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`DELETE FROM oauth_auth_code WHERE code_sha256=? AND expires_at > `+nowExpr, sha256hex(code))
	if err != nil {
		return "", "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", "", ErrOAuthBadCode
	}
	if access, refresh, err = issueTokenPair(ctx, tx, grantID, accessTTL, refreshTTL); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

// issueTokenPair inserts a fresh access + refresh token under grantID (within an
// open tx) and returns the plaintexts. Only the SHA-256 of each is stored.
func issueTokenPair(ctx context.Context, tx *sql.Tx, grantID int64, accessTTL, refreshTTL time.Duration) (access, refresh string, err error) {
	if access, err = randBase62(48); err != nil {
		return "", "", err
	}
	if refresh, err = randBase62(48); err != nil {
		return "", "", err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO oauth_token(token_sha256, grant_id, kind, expires_at) VALUES(?,?,'access', strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		sha256hex(access), grantID, secondsArg(accessTTL)); err != nil {
		return "", "", err
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO oauth_token(token_sha256, grant_id, kind, expires_at) VALUES(?,?,'refresh', strftime('%Y-%m-%dT%H:%M:%fZ','now', ?))`,
		sha256hex(refresh), grantID, secondsArg(refreshTTL)); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

// RefreshResult reports the grant a rotated refresh belongs to.
type RefreshResult struct {
	GrantID  int64
	Scope    string
	Resource string
}

// RotateOAuthRefresh validates a refresh token and, on success, revokes it and
// issues a fresh access + refresh pair (rotation). If a refresh token that has
// ALREADY been rotated (revoked) is presented, that is a theft signal: the whole
// grant is revoked and ErrOAuthReuseKill is returned. ErrOAuthBadToken for a
// missing/expired token or a revoked grant.
func (s *Store) RotateOAuthRefresh(ctx context.Context, refresh string, accessTTL, refreshTTL time.Duration) (access, newRefresh string, rr *RefreshResult, err error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", "", nil, err
	}
	defer tx.Rollback()

	var (
		grantID       int64
		tokRevoked    sql.NullString
		recentlyRotad bool
		grantRevoked  sql.NullString
		expired       bool
		scope, res    string
		resNull       sql.NullString
	)
	// recentlyRotad flags a refresh revoked within the grace window — the tell-tale
	// of a benign client retry (the successful-rotation response was lost) rather
	// than a token-theft replay. Arg order matches ? appearance: grace, then hash.
	err = tx.QueryRowContext(ctx, `
SELECT t.grant_id, t.revoked_at,
       (t.revoked_at IS NOT NULL AND t.revoked_at > strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)),
       (t.expires_at <= `+nowExpr+`), g.revoked_at, g.scope, g.resource
FROM oauth_token t JOIN oauth_grant g ON g.id = t.grant_id
WHERE t.token_sha256=? AND t.kind='refresh'`, secondsArg(-refreshReuseGrace), sha256hex(refresh)).
		Scan(&grantID, &tokRevoked, &recentlyRotad, &expired, &grantRevoked, &scope, &resNull)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, ErrOAuthBadToken
	}
	if err != nil {
		return "", "", nil, err
	}
	res = resNull.String

	// Grant already revoked → dead token.
	if grantRevoked.Valid {
		return "", "", nil, ErrOAuthBadToken
	}
	// Reuse of an already-rotated refresh.
	if tokRevoked.Valid {
		// Within the grace window this is almost certainly a benign retry of a
		// rotation whose response was lost: reject THIS (spent) token but keep the
		// grant — the client's live tokens from that rotation still work. Only a
		// later replay is treated as theft.
		if recentlyRotad {
			return "", "", nil, ErrOAuthBadToken
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_grant SET revoked_at=`+nowExpr+` WHERE id=?`, grantID); err != nil {
			return "", "", nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_token SET revoked_at=`+nowExpr+` WHERE grant_id=? AND revoked_at IS NULL`, grantID); err != nil {
			return "", "", nil, err
		}
		if err := tx.Commit(); err != nil {
			return "", "", nil, err
		}
		return "", "", nil, ErrOAuthReuseKill
	}
	if expired {
		return "", "", nil, ErrOAuthBadToken
	}

	// Rotate: revoke the presented refresh, mint a fresh pair.
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_token SET revoked_at=`+nowExpr+` WHERE token_sha256=?`, sha256hex(refresh)); err != nil {
		return "", "", nil, err
	}
	if access, newRefresh, err = issueTokenPair(ctx, tx, grantID, accessTTL, refreshTTL); err != nil {
		return "", "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", "", nil, err
	}
	return access, newRefresh, &RefreshResult{GrantID: grantID, Scope: scope, Resource: res}, nil
}

// --- MCP access-token validation (hot read path) ---

// OAuthPrincipal is the resolved identity behind a valid OAuth access token.
type OAuthPrincipal struct {
	ActorID  int64
	GrantID  int64
	Scope    string
	Resource string
}

// ValidateOAuthAccessToken resolves an opaque access token to its principal, or
// ErrOAuthBadToken if it is unknown, expired, revoked, or its grant was revoked.
// Read-only (reader pool) so it never contends for the single writer.
func (s *Store) ValidateOAuthAccessToken(ctx context.Context, token string) (*OAuthPrincipal, error) {
	var p OAuthPrincipal
	var res sql.NullString
	err := s.R.QueryRowContext(ctx, `
SELECT g.actor_id, t.grant_id, g.scope, g.resource
FROM oauth_token t JOIN oauth_grant g ON g.id = t.grant_id
WHERE t.token_sha256=? AND t.kind='access'
  AND t.revoked_at IS NULL AND t.expires_at > `+nowExpr+`
  AND g.revoked_at IS NULL`, sha256hex(token)).
		Scan(&p.ActorID, &p.GrantID, &p.Scope, &res)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOAuthBadToken
	}
	if err != nil {
		return nil, err
	}
	p.Resource = res.String
	return &p, nil
}

// --- human management (the Connectors section of the Tokens page) ---

// OAuthGrantRow is one live connector grant, for the curator UI.
type OAuthGrantRow struct {
	ID           int64
	ClientName   string
	ApprovedBy   string
	Scope        string
	CreatedAt    string
	ActiveTokens int

	// RedirectHost is the host of the client's FIRST registered redirect URI, and it is the only
	// field on this row that carries trust. The name is self-reported — the consent screen says
	// so — and anyone reachable can register a client under any reassuring one. ken-prod-ops found
	// `ken-identity-verification` holding all three capabilities on the live estate: registered
	// through open dynamic client registration, approved by a human, shipped by nothing in Ken.
	// It was plausible ONLY because its redirect was loopback. That field was visible at consent
	// time and nowhere else — the one moment a human is least equipped to weigh it.
	RedirectHost string

	// Legacy is true when the grant carries no `ken:` scope and therefore reaches the knowledge
	// base only, whatever URL the connector is pointed at.
	//
	// SHOWN BECAUSE THE SYMPTOM AND THE CAUSE WERE UNCONNECTED. Vlad removed three connectors,
	// re-added one, and saw only kb_* tools. His reconnect silently reused a grant from
	// 2026-08-11 — deleting a connector revokes nothing — so he was KB-only BY GRANT while
	// debugging it as a URL mistake, and pointing the same connector at /all/mcp would have
	// returned a bare 401 and taught him nothing. The tool list is the symptom, the grant is the
	// cause, and until now nothing anywhere connected them.
	Legacy bool
}

// ListOAuthGrants returns live (non-revoked) grants, newest first.
func (s *Store) ListOAuthGrants(ctx context.Context) ([]OAuthGrantRow, error) {
	rows, err := s.R.QueryContext(ctx, `
SELECT g.id, COALESCE(c.client_name,''), h.display_name, g.scope, g.created_at,
       (SELECT COUNT(*) FROM oauth_token t
          WHERE t.grant_id=g.id AND t.kind='access' AND t.revoked_at IS NULL AND t.expires_at > `+nowExpr+`),
       -- The FIRST registered redirect. redirect_uris is a JSON array and json_extract on a
       -- malformed or empty one yields NULL, which COALESCE turns into "" — an unknown host
       -- renders as a dash rather than failing the whole page.
       COALESCE(json_extract(c.redirect_uris, '$[0]'), '')
FROM oauth_grant g
JOIN oauth_client c ON c.client_id = g.client_id
JOIN actor h ON h.id = g.human_actor_id
WHERE g.revoked_at IS NULL
ORDER BY g.created_at DESC, g.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OAuthGrantRow
	for rows.Next() {
		var r OAuthGrantRow
		var redirect string
		if err := rows.Scan(&r.ID, &r.ClientName, &r.ApprovedBy, &r.Scope, &r.CreatedAt, &r.ActiveTokens,
			&redirect); err != nil {
			return nil, err
		}
		r.RedirectHost = redirectHostOf(redirect)
		// THE SAME PREDICATE THE AUTHENTICATOR USES — see IsLegacyGrant on why it is not computed
		// a second time here.
		r.Legacy = IsLegacyGrant(r.Scope)
		out = append(out, r)
	}
	return out, rows.Err()
}

// redirectHostOf reduces a registered redirect URI to the host a human can judge. A URI that will
// not parse, or carries no host, comes back EMPTY rather than raw: printing an unparseable string
// in the column that is supposed to carry trust invites reading it as a host.
func redirectHostOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// RevokeOAuthGrant revokes a grant and all of its outstanding tokens in one tx.
// Idempotent: revoking an already-revoked grant is a no-op success.
func (s *Store) RevokeOAuthGrant(ctx context.Context, id int64) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_grant SET revoked_at=`+nowExpr+` WHERE id=? AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE oauth_token SET revoked_at=`+nowExpr+` WHERE grant_id=? AND revoked_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeExpiredOAuth deletes spent authorization codes and long-expired tokens.
// Best-effort housekeeping; safe to call periodically.
func (s *Store) PurgeExpiredOAuth(ctx context.Context) error {
	if _, err := s.W.ExecContext(ctx, `DELETE FROM oauth_auth_code WHERE expires_at <= `+nowExpr); err != nil {
		return err
	}
	// Drop tokens that have been expired/revoked for over a day (keep recent
	// revoked refresh tokens so reuse-detection still fires within the window).
	_, err := s.W.ExecContext(ctx,
		`DELETE FROM oauth_token WHERE expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 days')`)
	return err
}

// --- what a connector may do, decided by the human at grant time ---

// Ken-namespaced OAuth scopes. These are the per-surface capability decision the human makes on
// the consent screen, carried in the grant's `scope` column — which is why that column stopped
// being cosmetic.
const (
	ScopeKB      = "ken:kb"      // the knowledge base: read, write-draft, propose
	ScopeCommSet = "ken:comm"    // inter-session messaging, including the file relay
	ScopeStation = "ken:station" // a durable working identity, including its locker
)

// GrantedCapabilities maps a grant's OAuth scope string to the Ken capabilities it carries.
//
// *** THIS IS THE CONDITION docs/IDENTITY-CONTROLS.md PUT ON §10 STEP 2, AND IT IS NOT OPTIONAL. ***
//
// Step 2 consolidates three authenticators into one identity. The control it removes is the one
// that said `/comm/mcp` accepts a `ken_` token and NOTHING else — and the register's verdict on
// that control is unusually pointed:
//
//	"This is the highest-value item for a design that intends OAuth as the only mechanism, because
//	 THIS CONTROL IS THE ONE THAT SAYS NO TO EXACTLY THAT... Consolidating three authenticators
//	 into one OAuth path removes it by construction, and the removal is INVISIBLE — every surface
//	 keeps working, better even, and the day a connector is compromised the blast radius has
//	 quietly grown from the knowledge base to the message bus and the vault. If the new design
//	 consolidates, the withholding has to be RE-EXPRESSED AS AN EXPLICIT PER-SURFACE CAPABILITY
//	 DECISION AT GRANT TIME, not inherited from the fact that three files exist."
//
// So a global "connectors get everything" constant is exactly the invisible removal it names. The
// decision lives on the grant instead, where a human made it and where it can be read back.
//
// DEFAULTS ARE ALL THREE, because Vlad's ruling is that no Ken feature is optional or off by
// default and that a session needs "just an actor registration and ONE approval". The consent
// screen grants everything unless the human narrows it. What changed is that the grant now RECORDS
// what was granted, so narrowing is possible and revocation is legible — not that anything is
// withheld by default.
//
// A GRANT WITH NO `ken:` SCOPE IS LEGACY AND GETS THE KNOWLEDGE BASE ONLY. Every grant approved
// before this shipped was approved when a connector could reach `/mcp` and nothing else; that is
// what its human agreed to. Widening it silently would be the invisible removal wearing a
// migration's clothes. They keep what they had until a human approves anew.
// IsLegacyGrant reports whether a grant predates per-surface scopes and therefore reaches the
// knowledge base only, whatever URL its connector is pointed at.
//
// ONE DEFINITION, TWO READERS. The authenticator below decides what a grant may do; the console
// tells a human which grants are legacy. Computing that twice is how a badge comes to disagree
// with the server — and a badge that lies about capability is worse than no badge, because the
// operator debugging a short tool list would now have a WRONG answer instead of no answer.
//
// It is not "grants exactly the knowledge base": a grant scoped `ken:kb` alone does that too, and
// is a current grant a human narrowed, not a stale one. The distinction is whether ANY ken: scope
// was recorded.
func IsLegacyGrant(scope string) bool {
	for _, f := range strings.Fields(scope) {
		switch f {
		case ScopeKB, ScopeCommSet, ScopeStation:
			return false
		}
	}
	return true
}

func GrantedCapabilities(scope string) []string {
	var kb, comm, station bool
	for _, f := range strings.Fields(scope) {
		switch f {
		case ScopeKB:
			kb = true
		case ScopeCommSet:
			comm = true
		case ScopeStation:
			station = true
		}
	}
	if !kb && !comm && !station {
		// Legacy grant: exactly what a connector could do before step 2.
		return []string{"read", "write-draft", "propose"}
	}
	var out []string
	if kb {
		out = append(out, "read", "write-draft", "propose")
	}
	if comm {
		out = append(out, "comm", "comm-file")
	}
	if station {
		out = append(out, "station", "station-locker")
	}
	// NEVER "curate", on any path. A human promotes; an agent never advances the curated head or
	// asserts freshness. That exclusion is the curation gate, so it is stated as a deliberate
	// omission rather than left for a reader to notice by its absence.
	return out
}

// DefaultGrantScopes is what the consent screen offers, and grants when the human does not narrow
// it: everything, because no Ken feature is optional or off by default.
func DefaultGrantScopes() []string { return []string{ScopeKB, ScopeCommSet, ScopeStation} }
