package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) (*sql.DB, Dialect) {
	t.Helper()
	db, dialect, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dialect
}

// tableExists asks the database itself, rather than trusting the migration's
// return value, that the table is really there.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found string
	err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	return true
}

func TestMigrationsAreWellFormed(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations were embedded")
	}
	for i, migration := range migrations {
		if i > 0 && migration.Version <= migrations[i-1].Version {
			t.Fatalf("migrations are not in ascending order: %v", migrations)
		}
		if migration.SQL == "" {
			t.Fatalf("%s is empty", migration.Filename())
		}
	}
}

func TestApplyCreatesBothTables(t *testing.T) {
	db, dialect := openTestDB(t)

	applied, err := Apply(context.Background(), db, dialect)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("Apply reported no migrations on an empty database")
	}

	for _, table := range []string{"authorizedUsers", "unauthorizedUsers"} {
		if !tableExists(t, db, table) {
			t.Errorf("table %q was not created", table)
		}
	}
}

// The tables must hold the public key and nothing else — the requirement is
// that the allow list cannot become a user profile.
func TestUserTablesHoldOnlyThePublicKey(t *testing.T) {
	db, dialect := openTestDB(t)
	if _, err := Apply(context.Background(), db, dialect); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, table := range []string{"authorizedUsers", "unauthorizedUsers"} {
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		var columns []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		rows.Close()

		if len(columns) != 1 || columns[0] != "publicKey" {
			t.Errorf("%s has columns %v, want [publicKey]", table, columns)
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	db, dialect := openTestDB(t)
	ctx := context.Background()

	first, err := Apply(ctx, db, dialect)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	second, err := Apply(ctx, db, dialect)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second Apply re-ran %d migration(s)", len(second))
	}

	applied, err := AppliedMigrations(ctx, db)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied) != len(first) {
		t.Fatalf("recorded %d migrations, want %d", len(applied), len(first))
	}
	if applied[0].AppliedAt.IsZero() {
		t.Error("applied timestamp did not round-trip")
	}
}

// Data must survive a re-run: a migration that dropped and recreated the tables
// would silently revoke everyone's authorization.
func TestApplyPreservesExistingRows(t *testing.T) {
	db, dialect := openTestDB(t)
	ctx := context.Background()

	if _, err := Apply(ctx, db, dialect); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "authorizedUsers" (publicKey) VALUES ('a-key')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := Apply(ctx, db, dialect); err != nil {
		t.Fatalf("re-Apply: %v", err)
	}

	var key string
	if err := db.QueryRow(`SELECT publicKey FROM "authorizedUsers"`).Scan(&key); err != nil {
		t.Fatalf("row did not survive: %v", err)
	}
	if key != "a-key" {
		t.Fatalf("got %q, want %q", key, "a-key")
	}
}

// A migration that fails must leave nothing behind — neither a half-built table
// nor a row claiming it succeeded, which would make the runner skip it forever.
func TestFailedMigrationIsNotRecorded(t *testing.T) {
	db, dialect := openTestDB(t)
	ctx := context.Background()

	if _, err := Apply(ctx, db, dialect); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	broken := Migration{
		Version: 999,
		Name:    "broken",
		SQL:     `CREATE TABLE "halfBuilt" (a TEXT); THIS IS NOT SQL;`,
	}
	if err := applyOne(ctx, db, dialect, broken); err == nil {
		t.Fatal("expected the broken migration to fail")
	}

	applied, err := AppliedMigrations(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range applied {
		if record.Version == broken.Version {
			t.Error("the failed migration was recorded as applied")
		}
	}
	if tableExists(t, db, "halfBuilt") {
		t.Error("the failed migration left its table behind")
	}
}

func TestPendingGoesEmptyAfterApply(t *testing.T) {
	db, dialect := openTestDB(t)
	ctx := context.Background()

	before, err := Pending(ctx, db)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("nothing pending on an empty database")
	}

	if _, err := Apply(ctx, db, dialect); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after, err := Pending(ctx, db)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("%d still pending after Apply", len(after))
	}
}

func TestParseMigrationName(t *testing.T) {
	cases := []struct {
		filename string
		version  int
		name     string
		wantErr  bool
	}{
		{filename: "0001_create_user_tables.sql", version: 1, name: "create_user_tables"},
		{filename: "0012_add_thing.sql", version: 12, name: "add_thing"},
		{filename: "no_version.sql", wantErr: true},
		{filename: "0000_too_low.sql", wantErr: true},
		{filename: "nounderscore.sql", wantErr: true},
	}
	for _, tc := range cases {
		version, name, err := parseMigrationName(tc.filename)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error", tc.filename)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.filename, err)
			continue
		}
		if version != tc.version || name != tc.name {
			t.Errorf("%s: got (%d, %q), want (%d, %q)", tc.filename, version, name, tc.version, tc.name)
		}
	}
}

func TestResolveDriver(t *testing.T) {
	cases := []struct {
		connStr string
		driver  string
		dsn     string
		wantErr bool
	}{
		{connStr: "postgres://u:p@h/db", driver: "postgres", dsn: "postgres://u:p@h/db"},
		{connStr: "postgresql://u@h/db", driver: "postgres", dsn: "postgresql://u@h/db"},
		{connStr: "sqlite:./users.db", driver: "sqlite", dsn: "./users.db"},
		{connStr: "sqlite:///tmp/users.db", driver: "sqlite", dsn: "/tmp/users.db"},
		{connStr: "./users.db", driver: "sqlite", dsn: "./users.db"},
		{connStr: "", wantErr: true},
		{connStr: "mysql://u@h/db", wantErr: true},
	}
	for _, tc := range cases {
		driver, dsn, err := resolveDriver(tc.connStr)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected an error", tc.connStr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", tc.connStr, err)
			continue
		}
		if driver != tc.driver || dsn != tc.dsn {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", tc.connStr, driver, dsn, tc.driver, tc.dsn)
		}
	}
}
