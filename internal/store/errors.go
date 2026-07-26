package store

import "errors"

// Sentinel errors surfaced to callers (MCP tools, web handlers).
var (
	ErrSlugConflict = errors.New("an entry with that slug already exists")
	ErrNotFound     = errors.New("entry not found")
	ErrBadVersion   = errors.New("version not found or not in a promotable state")
	// ErrInvalid wraps user-facing validation failures (safe to surface to clients).
	ErrInvalid = errors.New("invalid input")
	// ErrForeignLang blocks promoting a version whose detected content language is
	// not one the curator declared they can read — the "can't promote what you
	// can't read" comprehension gate. It is enforced in the store (Promote /
	// Repromote), not just the UI, so no server-side path reaches the head with
	// unreadable content.
	ErrForeignLang = errors.New("proposal is not in a curation language")

	// OAuth authorization-server errors (see internal/store/oauth.go).
	ErrOAuthNoClient  = errors.New("oauth client not found")
	ErrOAuthBadCode   = errors.New("authorization code invalid, expired, or already used")
	ErrOAuthBadToken  = errors.New("oauth token invalid, expired, or revoked")
	ErrOAuthReuseKill = errors.New("refresh token reuse detected — grant revoked")
)
