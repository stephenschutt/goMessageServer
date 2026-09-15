// Command migrate creates and updates messageServer's schema — the
// authorizedUsers and unauthorizedUsers tables, plus the userNames and
// chatMembers tables behind usernames and chats — against the database named by
// DATABASE_URL in .env.
//
// Usage:
//
//	go run ./migrate                 # apply everything not yet applied
//	go run ./migrate -status         # show what has run and what is pending
//	go run ./migrate -dry-run        # name what would run, change nothing
//	go run ./migrate -print          # write the SQL to stdout, touch no database
//	go run ./migrate -env deploy.env # read the connection string elsewhere
//
// Applying twice is harmless: each migration is recorded when it runs and
// skipped from then on.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"goMessageServer/internal/database"
)

func main() {
	var (
		envFile = flag.String("env", ".env", "file holding "+database.ConnectionStringVar)
		status  = flag.Bool("status", false, "show applied and pending migrations, then exit")
		dryRun  = flag.Bool("dry-run", false, "report what would be applied without applying it")
		print   = flag.Bool("print", false, "write the migration SQL to stdout and exit")
	)
	flag.Parse()

	if err := run(*envFile, *status, *dryRun, *print); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(envFile string, status, dryRun, print bool) error {
	// -print is about the migrations themselves, so it works with no database
	// configured at all — handy for piping into psql or reviewing in a diff.
	if print {
		return printSQL()
	}

	connStr, err := database.ConnectionString(envFile)
	if err != nil {
		return err
	}

	ctx := context.Background()
	db, dialect, err := database.Open(ctx, connStr)
	if err != nil {
		return err
	}
	defer db.Close()

	switch {
	case status:
		return showStatus(ctx, db)
	case dryRun:
		return showPending(ctx, db)
	default:
		return apply(ctx, db, dialect)
	}
}

func apply(ctx context.Context, db *sql.DB, dialect database.Dialect) error {
	applied, err := database.Apply(ctx, db, dialect)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("already up to date")
		return nil
	}
	for _, migration := range applied {
		fmt.Println("applied", migration.Filename())
	}
	return nil
}

func showPending(ctx context.Context, db *sql.DB) error {
	pending, err := database.Pending(ctx, db)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Println("already up to date")
		return nil
	}
	for _, migration := range pending {
		fmt.Println("would apply", migration.Filename())
	}
	return nil
}

func showStatus(ctx context.Context, db *sql.DB) error {
	applied, err := database.AppliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	pending, err := database.Pending(ctx, db)
	if err != nil {
		return err
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "VERSION\tSTATUS\tAPPLIED AT\tNAME")
	for _, record := range applied {
		fmt.Fprintf(out, "%04d\tapplied\t%s\t%s\n",
			record.Version, record.AppliedAt.Local().Format("2006-01-02 15:04:05"), record.Name)
	}
	for _, migration := range pending {
		fmt.Fprintf(out, "%04d\tpending\t-\t%s\n", migration.Version, migration.Name)
	}
	if err := out.Flush(); err != nil {
		return err
	}

	if len(pending) > 0 {
		fmt.Printf("\n%d migration(s) pending. Run: go run ./migrate\n", len(pending))
	}
	return nil
}

func printSQL() error {
	migrations, err := database.Migrations()
	if err != nil {
		return err
	}
	for i, migration := range migrations {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("-- %s\n%s", migration.Filename(), migration.SQL)
	}
	return nil
}
