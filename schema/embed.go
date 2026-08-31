// Package schema holds the SQL that CREATES each of Ken's databases, and nothing that changes one.
//
// *** THERE IS NO MIGRATION RUNNER, AND THAT IS THE DESIGN. ***
//
// Ken applies one of these files to an EMPTY database and otherwise reads the recorded schema
// version and refuses to start if it does not match. Upgrading an existing database is a separate,
// deliberate act an operator performs with stock sqlite3 — the scripts are in upgrade/ and the
// procedure is docs/UPGRADING-THE-DATABASE.md.
//
// WHY. A migration runner is code that rewrites data nobody is watching, on a schedule set by
// whoever restarts the service, and its failures are the expensive kind: three audit rounds before
// 4.0.0 found the same migration broken three separate times, each in a way a green test suite
// could not see, because every unit test opens a FRESH database and the data-moving arms of a
// migration copy zero rows. Ken is installed fresh, so the runner existed almost entirely for a
// case that does not arise here — and when it did arise, it was the least trustworthy code in the
// tree.
//
// Moving the rewrite OUT of the server makes it a thing an operator runs on purpose, reads the
// output of, and verifies with the same sqlite3 they already use. That last part matters: ken.db
// and comm.db are plain SQLite files readable from outside the process, which is how every
// upgrade verification ken-prod-ops has ever run works.
package schema

import _ "embed"

// Ken is the durable database: the knowledge base, stations, tokens, OAuth. There is no
// "delete it and start again" for this one.
//
//go:embed ken.sql
var Ken string

// KenVersion is the schema version Ken.sql records and the server requires.
const KenVersion = 26

// Comm is the expendable message database. If it is ever unrecoverable the supported answer is to
// delete it and restart: messaging rebuilds empty and nothing durable is affected.
//
//go:embed comm.sql
var Comm string

// CommVersion is the schema version Comm.sql records and the server requires.
const CommVersion = 22
