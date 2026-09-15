// Package database holds what the server and the migration tool have to agree
// on: where the connection string comes from, how it maps to a driver, and what
// the schema looks like. Keeping it in one place is what stops the two commands
// from quietly pointing at different databases or disagreeing about the shape
// of the tables.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"  // "postgres" driver
	_ "modernc.org/sqlite" // "sqlite" driver, pure Go so no cgo is needed
)

// Dialect is the SQL flavour behind an open connection. The two differ in
// little more than how bind parameters are spelled, but that little is enough
// that every statement has to know.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

func (d Dialect) String() string {
	if d == Postgres {
		return "postgres"
	}
	return "sqlite"
}

// Placeholder spells the nth bind parameter: Postgres numbers them, SQLite uses
// positional question marks.
func (d Dialect) Placeholder(n int) string {
	if d == Postgres {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// Quote wraps an identifier in double quotes, which both dialects read as "use
// this name exactly". Without it Postgres folds camelCase to lower case and
// "authorizedUsers" quietly becomes a different table.
func Quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// Open connects using a DATABASE_URL-style connection string and verifies the
// connection before returning it. It does not touch the schema; call Apply for
// that.
func Open(ctx context.Context, connStr string) (*sql.DB, Dialect, error) {
	driver, dsn, err := resolveDriver(connStr)
	if err != nil {
		return nil, SQLite, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, SQLite, fmt.Errorf("open %s database: %w", driver, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, SQLite, fmt.Errorf("connect to %s database: %w", driver, err)
	}

	dialect := SQLite
	if driver == "postgres" {
		dialect = Postgres
	}
	return db, dialect, nil
}

// resolveDriver maps a connection string to a registered driver name. The
// string is passed through untouched for Postgres, which understands its own
// URL form; a SQLite scheme prefix is stripped down to a plain file path.
func resolveDriver(connStr string) (driver, dsn string, err error) {
	trimmed := strings.TrimSpace(connStr)
	switch {
	case trimmed == "":
		return "", "", fmt.Errorf("connection string is empty")
	case strings.HasPrefix(trimmed, "postgres://"), strings.HasPrefix(trimmed, "postgresql://"):
		return "postgres", trimmed, nil
	case strings.HasPrefix(trimmed, "sqlite://"):
		return "sqlite", strings.TrimPrefix(trimmed, "sqlite://"), nil
	case strings.HasPrefix(trimmed, "sqlite:"):
		return "sqlite", strings.TrimPrefix(trimmed, "sqlite:"), nil
	case strings.HasPrefix(trimmed, "file:"):
		return "sqlite", trimmed, nil
	case strings.Contains(trimmed, "://"):
		scheme, _, _ := strings.Cut(trimmed, "://")
		return "", "", fmt.Errorf("unsupported database scheme %q: use postgres:// or sqlite:", scheme)
	default:
		// A bare path, e.g. DATABASE_URL=./users.db
		return "sqlite", trimmed, nil
	}
}

// ConnectionStringVar is the environment variable holding the connection string.
const ConnectionStringVar = "DATABASE_URL"

// ConnectionString returns the configured connection string, having first
// loaded envFile. The error names the file so a missing setting is actionable.
func ConnectionString(envFile string) (string, error) {
	if err := LoadEnv(envFile); err != nil {
		return "", fmt.Errorf("read %s: %w", envFile, err)
	}
	connStr := os.Getenv(ConnectionStringVar)
	if connStr == "" {
		return "", fmt.Errorf("%s is not set; put it in %s (see .env.example)",
			ConnectionStringVar, envFile)
	}
	return connStr, nil
}

// LoadEnv reads KEY=VALUE lines into the process environment without clobbering
// variables that are already set, so an explicit export still wins over the
// file. A missing file is not an error: the value may come from the environment.
func LoadEnv(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", filepath.Base(path), i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Quotes are how you keep a trailing comment or a space in a value.
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return nil
}
