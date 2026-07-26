package mcpserver

import (
	"errors"
	"log"

	"github.com/Quest-ICT/ken/internal/store"
)

// mcpError maps a store error to a client-safe value: known sentinels (and
// ErrInvalid-wrapped validation errors) pass through with their message; any
// unexpected/internal error (e.g. raw SQLite/driver text) is logged server-side
// and replaced with a generic message so schema/internal details never leak.
func mcpError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrSlugConflict),
		errors.Is(err, store.ErrBadVersion),
		errors.Is(err, store.ErrInvalid):
		return err
	default:
		log.Printf("mcp: internal error: %v", err)
		return errors.New("internal error")
	}
}
