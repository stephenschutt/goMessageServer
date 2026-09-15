// The authorization database: two single-column tables of public keys.
//
// authorizedUsers is the allow list — a key in it may use the chat API, and the
// table holds nothing else, so the database never learns who anyone is. When a
// caller proves possession of a key that is not in that table, its public key is
// filed in unauthorizedUsers, an identically shaped table that doubles as the
// queue an administrator promotes keys from.
//
// Both tables are defined by the migrations in internal/database, which is also
// what `go run ./migrate` applies.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"goMessageServer/internal/database"
)

// Table names, quoted in every statement so the camel case survives Postgres,
// which folds unquoted identifiers to lower case.
const (
	authorizedTable   = "authorizedUsers"
	unauthorizedTable = "unauthorizedUsers"
)

// SchemaMode says what OpenUserStore should do about a database whose schema is
// out of date.
type SchemaMode int

const (
	// ApplySchema runs pending migrations at startup, which is what makes the
	// server work against an empty SQLite file with no setup.
	ApplySchema SchemaMode = iota
	// RequireSchema refuses to start instead. Use it where the server's
	// database user has no DDL rights and migrating is a separate deploy step.
	RequireSchema
)

// UserStore is the authorization database. It is safe for concurrent use:
// *sql.DB pools connections internally.
type UserStore struct {
	db      *sql.DB
	dialect database.Dialect
}

// OpenUserStore connects using a DATABASE_URL-style connection string and makes
// sure the schema is present, either by applying the pending migrations or by
// insisting they have already been applied.
func OpenUserStore(ctx context.Context, connStr string, mode SchemaMode) (*UserStore, error) {
	db, dialect, err := database.Open(ctx, connStr)
	if err != nil {
		return nil, err
	}

	if err := prepareSchema(ctx, db, dialect, mode); err != nil {
		db.Close()
		return nil, err
	}
	return &UserStore{db: db, dialect: dialect}, nil
}

func prepareSchema(ctx context.Context, db *sql.DB, dialect database.Dialect, mode SchemaMode) error {
	if mode == RequireSchema {
		pending, err := database.Pending(ctx, db)
		if err != nil {
			return err
		}
		if len(pending) > 0 {
			return fmt.Errorf("%d migration(s) pending; run: go run ./migrate", len(pending))
		}
		return nil
	}

	applied, err := database.Apply(ctx, db, dialect)
	if err != nil {
		return err
	}
	for _, migration := range applied {
		log.Printf("schema: applied %s", migration.Filename())
	}
	return nil
}

// IsAuthorized reports whether a public key appears in authorizedUsers.
func (s *UserStore) IsAuthorized(ctx context.Context, publicKey string) (bool, error) {
	stmt := fmt.Sprintf(`SELECT 1 FROM %s WHERE publicKey = %s`,
		database.Quote(authorizedTable), s.dialect.Placeholder(1))
	var found int
	err := s.db.QueryRowContext(ctx, stmt, publicKey).Scan(&found)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, fmt.Errorf("query %s: %w", authorizedTable, err)
	}
	return true, nil
}

// RecordUnauthorized files a rejected public key for an administrator to review.
// Repeat attempts by the same key collapse onto the one row.
func (s *UserStore) RecordUnauthorized(ctx context.Context, publicKey string) error {
	return s.insertIgnore(ctx, unauthorizedTable, publicKey)
}

// Authorize adds a public key to the allow list and drops any record of it from
// the rejected table, which is how a key graduates from one to the other.
func (s *UserStore) Authorize(ctx context.Context, publicKey string) error {
	if err := s.insertIgnore(ctx, authorizedTable, publicKey); err != nil {
		return err
	}
	stmt := fmt.Sprintf(`DELETE FROM %s WHERE publicKey = %s`,
		database.Quote(unauthorizedTable), s.dialect.Placeholder(1))
	if _, err := s.db.ExecContext(ctx, stmt, publicKey); err != nil {
		return fmt.Errorf("clear %s: %w", unauthorizedTable, err)
	}
	return nil
}

// PendingKeys lists the public keys waiting in unauthorizedUsers.
func (s *UserStore) PendingKeys(ctx context.Context) ([]string, error) {
	stmt := fmt.Sprintf(`SELECT publicKey FROM %s ORDER BY publicKey`, database.Quote(unauthorizedTable))
	rows, err := s.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", unauthorizedTable, err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *UserStore) insertIgnore(ctx context.Context, table, publicKey string) error {
	stmt := fmt.Sprintf(
		`INSERT INTO %s (publicKey) VALUES (%s) ON CONFLICT (publicKey) DO NOTHING`,
		database.Quote(table), s.dialect.Placeholder(1))
	if _, err := s.db.ExecContext(ctx, stmt, publicKey); err != nil {
		return fmt.Errorf("insert into %s: %w", table, err)
	}
	return nil
}

func (s *UserStore) Close() error { return s.db.Close() }
