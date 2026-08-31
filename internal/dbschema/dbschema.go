// Package dbschema creates a database from one schema file, and REFUSES to migrate one.
//
// See package schema for why there is no migration runner. This package is the whole of what the
// server does about schema at boot:
//
//   - an EMPTY database gets the schema applied, once;
//   - a database at the version this binary expects is used as-is;
//   - anything else STOPS THE BOOT with the upgrade procedure named.
//
// *** THE REFUSAL IS THE LOAD-BEARING PART. *** Without it the scheme fails silently in the worst
// direction: a binary opening a database whose shape it does not know, writing columns that are
// not there or reading ones that moved. Measured before 4.0.0 in the other direction — the v3.42.0
// binary booted against a 4.0.0 database with a completely ordinary startup log and then 500ed on
// "no such table: pairing_code". "Unsupported" was indistinguishable from "fine" at startup, and
// that is the difference between an operator restoring a snapshot and a user finding the problem.
package dbschema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// Apply brings one database to the schema this binary requires, or refuses to continue.
//
// label names the database in the log and in the refusal ("ken.db", "comm.db"), because two of
// them are opened on one boot and a line that does not say which is barely better than no line.
func Apply(ctx context.Context, w, r *sql.DB, schemaSQL string, want int, label, upgradeDoc string) error {
	have, err := Version(ctx, r)
	if err != nil {
		return fmt.Errorf("%s: reading schema version: %w", label, err)
	}

	switch {
	case have == 0:
		// A FRESH DATABASE. Nothing to preserve, so this is the only case that writes.
		log.Printf("schema: %s is empty — creating it at version %d", label, want)
		if _, err := w.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("%s: creating schema: %w", label, err)
		}
		got, err := Version(ctx, r)
		if err != nil {
			return fmt.Errorf("%s: re-reading schema version after create: %w", label, err)
		}
		// THE FILE MUST RECORD THE VERSION THE BINARY EXPECTS. A schema that creates every table
		// and records the wrong number — or none — leaves the next boot refusing a database this
		// one just made, which reads as corruption rather than as a mistake in the file.
		if got != want {
			return fmt.Errorf("%s: schema file created version %d but this binary requires %d — "+
				"the embedded schema and the constant beside it disagree, which is a build fault, "+
				"not an operator one", label, got, want)
		}

	case have == want:
		log.Printf("schema: %s at version %d, as required", label, have)

	default:
		// *** REFUSE, LOUDLY, BEFORE ANYTHING TOUCHES IT. ***
		//
		// Both directions are named because the remedy differs and an operator reading this is
		// deciding which one they are in. Older than the binary: run the upgrade. Newer: this is a
		// downgrade, and the answer is the snapshot, never an "upgrade" that would move it further.
		direction := "OLDER than this binary"
		remedy := "Stop Ken, run the upgrade for this database (see " + upgradeDoc + "), and start it again."
		if have > want {
			direction = "NEWER than this binary"
			remedy = "This is a DOWNGRADE. Restore the snapshot taken before the upgrade, or run the newer Ken."
		}
		return fmt.Errorf("%s is at schema version %d and this binary requires %d — the database is %s. %s "+
			"Ken does not migrate databases: it creates one and checks the rest, so nothing here will "+
			"change on a restart", label, have, want, direction, remedy)
	}

	// *** THE INTEGRITY CHECK RUNS ON EVERY BOOT, NOT ONLY WHEN SOMETHING WAS WRITTEN. ***
	//
	// It is cheap, and the failure it catches is one that does not heal: a database with a dangling
	// reference reports an ordinary healthy startup on every subsequent boot unless something asks.
	// A fault that appears to fix itself when restarted is the worst shape a fault can take,
	// because the operator's first instinct is exactly the thing that hides it.
	if err := checkForeignKeys(ctx, r, label); err != nil {
		return err
	}
	log.Printf("schema: %s foreign_key_check clean", label)
	return nil
}

// Version reads the recorded schema version. A missing table means an EMPTY database and answers
// 0 — but a real error is never swallowed as "empty", because that would create a schema over the
// top of a database that already had one.
func Version(ctx context.Context, r *sql.DB) (int, error) {
	var v sql.NullInt64
	err := r.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&v)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return int(v.Int64), nil
}

func checkForeignKeys(ctx context.Context, db *sql.DB, label string) error {
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("%s: foreign key check: %w", label, err)
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
		// THE REMEDY MUST BE ONE THE OPERATOR CAN ACTUALLY PERFORM, and it differs by database:
		// comm.db is deliberately in no backup tier, so "restore the snapshot" is advice that
		// cannot be followed for the database this check most often fires on.
		return fmt.Errorf("%s has %d or more dangling foreign key reference(s), first: %s — this is "+
			"not repaired by restarting. For ken.db restore the pre-upgrade snapshot (docs/BACKUP.md). "+
			"For comm.db there is no snapshot by design: it is expendable, so stop Ken, delete it and "+
			"its -wal/-shm, and restart — messaging rebuilds empty and the knowledge base and stations "+
			"are unaffected", label, len(broken), strings.Join(broken, "; "))
	}
	return nil
}
