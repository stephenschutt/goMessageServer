# Migrations

One numbered SQL file per schema change, applied lowest version first by
`go run ./migrate` (and by the server at startup unless it is given
`-require-schema`).

```
<version>_<name>.sql        e.g. 0002_add_key_labels.sql
```

The files are embedded into the binaries, so both commands apply the same SQL
from any working directory. Once a migration has been applied anywhere, treat it
as frozen and write a new one rather than editing it — the runner records
versions, not contents, so an edit to an applied file silently never runs.

Each file is executed inside a transaction along with the row recording it, so a
migration either takes effect and is remembered or does neither.

## Writing one

- Double-quote **table** names. Postgres folds unquoted names to lower case,
  which would turn `authorizedUsers` into a second, empty `authorizedusers`.
- Leave **column** names unquoted. Postgres then folds `publicKey` to
  `publickey`, and because the queries in `userdb.go` are unquoted too, both
  sides fold the same way and agree. It also means ad-hoc SQL works whatever
  case you type. Quoting a column in a migration but not in the query that
  reads it is the way to break this.
- Stick to syntax both Postgres and SQLite accept, since `DATABASE_URL` selects
  between them. The suite in `tests/` exercises the SQLite path on every
  `go test ./...`; the Postgres one is only exercised if you ask for it:

  ```sh
  TEST_POSTGRES_URL='postgres://postgres@127.0.0.1:5432/messages?sslmode=disable' \
      go test -run Postgres ./tests/
  ```

  That matters more than it sounds. Auto-increment is the clearest example: the
  two engines spell it incompatibly, which is why `"Messages"` takes its ids
  from the `"messageSequence"` table in 0003 rather than from the column.
- Prefer `IF NOT EXISTS`, so a migration can also be run by hand against a
  database that was set up before the runner existed.

## Applying by hand

`go run ./migrate -print` writes the SQL to stdout and touches no database:

```sh
go run ./migrate -print | psql "$DATABASE_URL"
```

Note that this does not record anything in `schemaMigrations`, so the runner
will try the migration again later; the `IF NOT EXISTS` clauses are what make
that harmless.
