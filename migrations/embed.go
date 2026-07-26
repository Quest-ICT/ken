// Package migrations embeds ken's SQL schema migrations.
package migrations

import "embed"

// FS holds the ordered migration files, applied in lexical order. All are plain
// SQL applied unconditionally; 0002_embeddings.sql creates an entry_embedding
// table that stays empty until an embedding provider is configured (no SQLite
// extension is required).
//
//go:embed *.sql
var FS embed.FS
