package store

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/Quest-ICT/ken/migrations"
)

// Migrate applies embedded migrations in lexical order, skipping versions already
// recorded in schema_migration. It is idempotent. All migrations are plain SQL
// (no loadable extensions), so all apply unconditionally; the embeddings table is
// created empty and only populated when a provider is configured.
func (s *Store) Migrate() error {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	applied, err := s.appliedVersions()
	if err != nil {
		return err
	}

	for _, f := range files {
		v := versionOf(f)
		if v == 0 || applied[v] {
			continue
		}
		body, err := migrations.FS.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := s.W.Exec(string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}

// appliedVersions reads the schema_migration table; a missing table (fresh db)
// yields an empty set rather than an error.
func (s *Store) appliedVersions() (map[int]bool, error) {
	out := map[int]bool{}
	rows, err := s.R.Query(`SELECT version FROM schema_migration`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil // fresh db — the table doesn't exist yet
		}
		return nil, err // don't swallow a real error as "nothing applied"
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// versionOf parses the leading integer of a migration filename ("0001_init.sql" -> 1).
func versionOf(filename string) int {
	base := filename
	if i := strings.IndexByte(base, '_'); i > 0 {
		base = base[:i]
	}
	n, _ := strconv.Atoi(base)
	return n
}
