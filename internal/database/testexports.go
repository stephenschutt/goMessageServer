// Bridges for the test suite in tests/.
//
// Go puts a package's tests beside it so that they can reach its unexported
// parts. REQ-010 puts them in tests/ instead, which makes them an ordinary
// importing package with no such access — so the four internals that have tests
// worth keeping are re-exported here under names that say what they are for.
//
// They are not part of this package's API. Nothing outside tests/ should call
// them, and a change to any of them is a change to an internal detail, not a
// breaking one.
package database

// ResolveDriverForTest maps a connection string to a driver name and DSN.
var ResolveDriverForTest = resolveDriver

// ParseMigrationNameForTest splits "0001_create_user_tables.sql" into its
// version and name.
var ParseMigrationNameForTest = parseMigrationName

// ApplyOneForTest runs a single migration in its own transaction, which is what
// lets a test check that a failing one records nothing.
var ApplyOneForTest = applyOne

// SQLitePragmasForTest is the query string appended to every SQLite DSN.
const SQLitePragmasForTest = sqlitePragmas

// MigrationsTableForTest is the table the runner records applied versions in.
const MigrationsTableForTest = migrationsTable
