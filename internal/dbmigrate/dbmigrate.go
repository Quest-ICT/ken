// Package dbmigrate applies embedded SQL migrations to one SQLite database.
//
// Ken has two databases — ken.db (knowledge) and comm.db (messages) — and had two
// runners. Only comm's disabled foreign keys for the duration of the run, so
// ken.db's would have silently severed or cascade-deleted child rows the first time
// a ken.db migration rebuilt a table. This is the one runner both now use. The
// databases still version INDEPENDENTLY: each caller passes its own pools and its
// own migration set, and neither reads the other's schema_migration.
package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"
)

// Run applies every migration in fsys matching glob, in lexical order, skipping
// versions already recorded in schema_migration. It is idempotent and
// forward-only, and a run with nothing pending reads schema_migration and returns
// — it never touches the pragma and never pays for the check below.
//
// w must be the caller's WRITER pool and r a pool over the same file. The whole run
// happens on ONE connection pinned from w, because the pragma is per-connection.
//
// FOREIGN KEYS ARE DISABLED FOR THE DURATION, ON THAT PINNED CONNECTION, AND THE
// RESULT IS CHECKED. This is not caution for its own sake — it is what makes a
// table REBUILD possible at all, and the reason is worth writing down because the
// failure it prevents is silent.
//
// SQLite's DROP TABLE performs an implicit DELETE FROM when foreign keys are
// enforced, so dropping a table fires every ON DELETE action pointing at it: an
// ON DELETE SET NULL child is severed from its parent, an ON DELETE CASCADE child
// is deleted outright. Neither raises. The migration reports success and the data
// is gone. In comm.db that meant every attachment severed from its message; in
// ken.db a rebuild of station would take eight cascading child tables with it.
//
// The usual defence, `PRAGMA foreign_keys=OFF` written into the migration file,
// DOES NOT WORK: that pragma is a documented no-op inside a transaction, and every
// migration in this project is wrapped in BEGIN/COMMIT. Measured against this
// driver before the comm version of this code was written: parent rebuilt, 2 child
// rows inserted, 0 child rows afterwards.
//
// So the pragma is set OUTSIDE the transaction, on a connection pinned for the
// whole run (both writer pools are capped at one connection, but pinning makes it
// explicit rather than incidental), and `foreign_key_check` runs at the end.
// Disabling enforcement while rewriting is only safe if something afterwards proves
// the result is consistent; without that this trades a loud failure for a quiet one.
// Run applies pending migrations to one database and SAYS SO.
//
// *** IT USED TO DO ALL OF THIS SILENTLY. ***
//
// ken-prod-ops measured the boot that took the live database from ken 24 -> 26 and comm 19 -> 21:
// thirteen log lines, and `grep -icE "migrat|foreign|fk|integrity|schema"` returned ZERO. Two
// migrations ran, the foreign-key check ran and passed, and none of it was visible. They could
// only establish that the databases were sound by opening them and checking by hand.
//
// That is this project's own defect class aimed at its riskiest operation: a check whose success
// is INDISTINGUISHABLE FROM ITS ABSENCE. An operator reading that log cannot tell "the integrity
// check passed" from "no integrity check exists", and the second is what they will assume the day
// it matters. The label names WHICH database, because two of them migrate on one boot and a line
// that does not say which is barely better than no line.
func Run(ctx context.Context, w, r *sql.DB, fsys fs.FS, glob, label string) error {
	files, err := fs.Glob(fsys, glob)
	if err != nil {
		return err
	}
	sort.Strings(files)

	applied, err := Applied(ctx, r)
	if err != nil {
		return err
	}

	// *** A DATABASE FROM THE FUTURE IS REFUSED, LOUDLY, BEFORE ANYTHING TOUCHES IT. ***
	//
	// `pending` is "embedded files not yet applied", so a binary older than the database computes
	// an EMPTY pending set and reports success. Ken documents rollback as supported — INSTALL.md
	// says to point `current` at a previous release and restart, with data/ "preserved untouched" —
	// and measured against 4.0.0's databases the v3.42.0 binary booted with a completely ordinary
	// startup log and then 500ed on /comm with "no such table: pairing_code". On a populated
	// database it is worse than a 500: the old code writes columns that migration 0025 dropped and
	// inserts request kinds 0026's rebuilt CHECK rejects.
	//
	// FORWARD-ONLY IS THE RULE THIS ENFORCES, not a new one — the package comment has always said
	// upgrades only add migrations and downgrading after one has run is unsupported. What was
	// missing is that "unsupported" was indistinguishable from "fine" at startup, which is the
	// difference between an operator restoring their pre-upgrade snapshot and one discovering the
	// problem from a user.
	var highestEmbedded, highestApplied int
	for _, f := range files {
		if v := Version(f); v > highestEmbedded {
			highestEmbedded = v
		}
	}
	for v := range applied {
		if v > highestApplied {
			highestApplied = v
		}
	}
	if highestApplied > highestEmbedded {
		return fmt.Errorf("this database is at schema %d and this binary only knows %d: it was "+
			"written by a NEWER Ken and downgrading is not supported. Restore the snapshot taken "+
			"before the upgrade (see docs/BACKUP.md), or run the newer binary",
			highestApplied, highestEmbedded)
	}

	pending := make([]string, 0, len(files))
	for _, f := range files {
		if v := Version(f); v != 0 && !applied[v] {
			pending = append(pending, f)
		}
	}
	if len(pending) == 0 {
		log.Printf("schema: %s at version %d, up to date — %d migration(s) already applied",
			label, highestApplied, len(applied))
		// *** THE INTEGRITY CHECK RUNS ON EVERY BOOT, NOT ONLY WHEN SOMETHING IS APPLIED. ***
		//
		// Each migration file carries its own BEGIN/COMMIT, so a file COMMITS before the
		// foreign_key_check below ever runs. A migration that left a dangling reference therefore
		// recorded its version, failed the boot — and on the NEXT boot computed an empty pending
		// set and returned from here, reporting an ordinary healthy startup over a database whose
		// foreign_key_check still fails. Measured during the 4.0.0 pre-release work: one restart
		// apart, DEGRADED then healthy, with nothing repaired in between.
		//
		// A fault that heals itself by being restarted is the worst shape a fault can take, because
		// the operator's first instinct is exactly the thing that hides it. One PRAGMA per boot is
		// a small price for the failure staying visible until somebody fixes it.
		if err := checkForeignKeys(ctx, r); err != nil {
			return err
		}
		log.Printf("schema: %s foreign_key_check clean", label)
		return nil
	}

	conn, err := w.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable foreign keys for migration: %w", err)
	}

	log.Printf("schema: %s MIGRATING from version %d to %d — %d file(s): %s",
		label, highestApplied, highestEmbedded, len(pending), strings.Join(pending, ", "))

	for _, f := range pending {
		body, err := fs.ReadFile(fsys, f)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return fmt.Errorf("re-enable foreign keys after migration: %w", err)
	}
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check after migration: %w", err)
	}
	defer rows.Close()
	var broken []string
	for rows.Next() {
		var table, parent sql.NullString
		var rowid, fkid sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		broken = append(broken, fmt.Sprintf("%s row %d -> %s", table.String, rowid.Int64, parent.String))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(broken) > 0 {
		return fmt.Errorf("migration left %d dangling foreign key reference(s), first: %s — "+
			"foreign keys were off during the rewrite and the result does not hold together",
			len(broken), broken[0])
	}
	log.Printf("schema: %s now at version %d — %d migration(s) applied, foreign_key_check clean",
		label, highestEmbedded, len(pending))
	return nil
}

// Applied returns the versions already recorded in schema_migration. A missing
// table (a fresh database) yields an empty set rather than an error, but a real
// error is never swallowed as "nothing applied".
func Applied(ctx context.Context, r *sql.DB) (map[int]bool, error) {
	out := map[int]bool{}
	rows, err := r.QueryContext(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return out, nil
		}
		return nil, err
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

// Version parses the leading integer of a migration path, with or without a
// directory ("migrations/0001_init.sql" -> 1, "0001_init.sql" -> 1). A name that
// does not start with digits yields 0, which Run skips.
func Version(path string) int {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.IndexByte(base, '_'); i > 0 {
		base = base[:i]
	}
	n, _ := strconv.Atoi(base)
	return n
}

// checkForeignKeys reports every dangling reference in the database, as a single error naming the
// first few. Used both after a migration run and on a boot with nothing pending — see Run for why
// the second call matters more than it looks.
func checkForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign key check: %w", err)
	}
	defer rows.Close()
	var broken []string
	for rows.Next() {
		var table, parent sql.NullString
		var rowid, fkid sql.NullInt64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return err
		}
		if len(broken) < 5 {
			broken = append(broken, fmt.Sprintf("%s row %d -> %s", table.String, rowid.Int64, parent.String))
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(broken) > 0 {
		// *** THE REMEDY MUST BE ONE THE OPERATOR CAN ACTUALLY PERFORM. ***
		//
		// This said "restore the pre-upgrade snapshot" for BOTH databases — and comm.db is
		// deliberately in no backup tier (BACKUP.md, main.go and install.sh all say so, because it
		// is expendable by design). So the only remedy printed for the database this check most
		// often fires on was one that cannot exist: a recovery whose failure renders identically
		// to not recovering.
		//
		// Both remedies are named, and which applies is decided by the caller — it knows which
		// database it opened, and it is the only thing that does.
		return fmt.Errorf("database has %d or more dangling foreign key reference(s), first: %s — "+
			"this is not repaired by restarting. For ken.db restore the pre-upgrade snapshot "+
			"(docs/BACKUP.md). For comm.db there is no snapshot by design: it is expendable, so "+
			"stop Ken, delete it (data/comm/comm.db and its -wal/-shm), and restart — messaging "+
			"rebuilds empty and the knowledge base and stations are unaffected",
			len(broken), strings.Join(broken, "; "))
	}
	return nil
}
