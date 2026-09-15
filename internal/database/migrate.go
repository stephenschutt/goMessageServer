package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFS carries the schema into the binary, so the server and the
// migration tool apply the same SQL from any working directory.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationsTable records what has already run. It is created by the runner
// rather than by a migration, because it has to exist before the first one.
const migrationsTable = "schemaMigrations"

// Migration is one numbered step, read from migrations/<version>_<name>.sql.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Filename reproduces the file this migration was read from.
func (m Migration) Filename() string {
	return fmt.Sprintf("%04d_%s.sql", m.Version, m.Name)
}

// Migrations returns every migration, lowest version first.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		// Two files claiming one version would apply in an order that depends
		// on the file system, and only one of them would ever be recorded.
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, entry.Name(), version)
		}
		seen[version] = entry.Name()

		body, err := migrationFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

// parseMigrationName splits "0001_create_user_tables.sql" into 1 and
// "create_user_tables".
func parseMigrationName(filename string) (version int, name string, err error) {
	trimmed := strings.TrimSuffix(filename, ".sql")
	prefix, rest, found := strings.Cut(trimmed, "_")
	if !found {
		return 0, "", fmt.Errorf("migration %q: expected <version>_<name>.sql", filename)
	}
	version, err = strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("migration %q: %q is not a version number", filename, prefix)
	}
	if version < 1 {
		return 0, "", fmt.Errorf("migration %q: versions start at 1", filename)
	}
	return version, rest, nil
}

// Applied is the record of one migration that has already run.
type Applied struct {
	Version   int
	Name      string
	AppliedAt time.Time
}

// Apply runs every migration that has not run yet, lowest version first, and
// returns the ones it applied. It is safe to call on an up-to-date database:
// the result is then empty.
//
// Each migration runs inside a transaction together with the row recording it,
// so a migration either takes effect and is remembered or does neither. Both
// Postgres and SQLite roll DDL back, so a failure part-way through leaves no
// half-built table behind.
func Apply(ctx context.Context, db *sql.DB, dialect Dialect) ([]Migration, error) {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return nil, err
	}

	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}
	done, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	var applied []Migration
	for _, migration := range migrations {
		if done[migration.Version] {
			continue
		}
		if err := applyOne(ctx, db, dialect, migration); err != nil {
			return applied, err
		}
		applied = append(applied, migration)
	}
	return applied, nil
}

func applyOne(ctx context.Context, db *sql.DB, dialect Dialect, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%s: begin: %w", migration.Filename(), err)
	}
	// Rollback after a successful commit is a no-op, so this is safe to defer
	// unconditionally and covers every early return below.
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("%s: %w", migration.Filename(), err)
	}

	record := fmt.Sprintf(
		`INSERT INTO %s (version, name, appliedAt) VALUES (%s, %s, %s)`,
		Quote(migrationsTable),
		dialect.Placeholder(1), dialect.Placeholder(2), dialect.Placeholder(3))
	if _, err := tx.ExecContext(ctx, record,
		migration.Version, migration.Name, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: record: %w", migration.Filename(), err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: commit: %w", migration.Filename(), err)
	}
	return nil
}

// Pending returns the migrations that Apply would run.
func Pending(ctx context.Context, db *sql.DB) ([]Migration, error) {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return nil, err
	}
	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}
	done, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	var pending []Migration
	for _, migration := range migrations {
		if !done[migration.Version] {
			pending = append(pending, migration)
		}
	}
	return pending, nil
}

// AppliedMigrations lists what has run, oldest first.
func AppliedMigrations(ctx context.Context, db *sql.DB) ([]Applied, error) {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return nil, err
	}

	query := fmt.Sprintf(`SELECT version, name, appliedAt FROM %s ORDER BY version`, Quote(migrationsTable))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", migrationsTable, err)
	}
	defer rows.Close()

	var applied []Applied
	for rows.Next() {
		var record Applied
		var stamp string
		if err := rows.Scan(&record.Version, &record.Name, &stamp); err != nil {
			return nil, err
		}
		// Stored as RFC 3339 text so the column means the same thing in both
		// dialects; an unreadable stamp should not hide the migration itself.
		record.AppliedAt, _ = time.Parse(time.RFC3339, stamp)
		applied = append(applied, record)
	}
	return applied, rows.Err()
}

func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	stmt := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		appliedAt TEXT NOT NULL
	)`, Quote(migrationsTable))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create table %s: %w", migrationsTable, err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int]bool, error) {
	query := fmt.Sprintf(`SELECT version FROM %s`, Quote(migrationsTable))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", migrationsTable, err)
	}
	defer rows.Close()

	done := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		done[version] = true
	}
	return done, rows.Err()
}
