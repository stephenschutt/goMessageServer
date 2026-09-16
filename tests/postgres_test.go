package tests

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	messageserver "goMessageServer"
)

// Production is Postgres and the tests above are SQLite, which leaves the gap
// this file covers: SQL that one engine accepts and the other does not, and
// behaviour that differs between them — how a concurrent writer is made to
// wait, and how a timestamp survives a round trip.
//
// It is skipped unless TEST_POSTGRES_URL names a database it may write to, so
// `go test ./...` still needs nothing installed:
//
//	TEST_POSTGRES_URL='postgres://postgres@127.0.0.1:5432/messages?sslmode=disable' go test -run Postgres ./tests/
//
// Every table is emptied first, so the database it points at must be a
// throwaway one.
const postgresURLVar = "TEST_POSTGRES_URL"

func newPostgresStore(t *testing.T) *messageserver.UserStore {
	t.Helper()
	connStr := os.Getenv(postgresURLVar)
	if connStr == "" {
		t.Skipf("set %s to run this against Postgres", postgresURLVar)
	}
	if !strings.HasPrefix(connStr, "postgres") {
		t.Fatalf("%s is %q, which is not a Postgres connection string", postgresURLVar, connStr)
	}

	users, err := messageserver.OpenUserStore(context.Background(), connStr, messageserver.ApplySchema)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(func() { users.Close() })

	// A shared database rather than a fresh temp file, so each test starts by
	// clearing what the last one left.
	for _, table := range []string{
		messageserver.RecipientsTable, messageserver.MessagesTable, messageserver.MembersTable, messageserver.UsernameTable,
		messageserver.AuthorizedTable, messageserver.UnauthorizedTable,
	} {
		if _, err := users.DB().ExecContext(context.Background(), `DELETE FROM "`+table+`"`); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	if _, err := users.DB().ExecContext(context.Background(),
		`UPDATE "`+messageserver.SequenceTable+`" SET nextId = 0 WHERE id = 1`); err != nil {
		t.Fatalf("reset %s: %v", messageserver.SequenceTable, err)
	}
	return users
}

// The whole of REQ-009 against the engine that actually runs it: the schema
// applies, a message goes in, and the delivery rule selects it back out.
func TestPostgresRoundTripsAMessage(t *testing.T) {
	users := newPostgresStore(t)
	store := messageserver.NewChatStore(users)
	ctx := context.Background()

	stephen := authorizedNamedKey(t, users, "stephen")
	sophia := authorizedNamedKey(t, users, "sophia")
	bystander := authorizedNamedKey(t, users, "nosy")
	if err := users.AddChatMember(ctx, stephen, sophia); err != nil {
		t.Fatalf("add member: %v", err)
	}

	sent, err := store.Add(ctx, stephen, "stephen", "hello postgres", []string{sophia})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	seen, err := store.Since(ctx, 0, sophia)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(seen) != 1 || seen[0].Text != "hello postgres" || seen[0].Sender != "stephen" {
		t.Fatalf("sophia sees %v, want the one message", seen)
	}
	// TEXT in, time.Time out: a timestamp that lost its precision or its zone
	// on the way through would show up here.
	if !seen[0].Timestamp.Equal(sent.Timestamp) {
		t.Fatalf("timestamp came back as %v, want %v", seen[0].Timestamp, sent.Timestamp)
	}

	outsider, err := store.Since(ctx, 0, bystander)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(outsider) != 0 {
		t.Fatalf("an outsider sees %v, want nothing", outsider)
	}
}

// The sequence table is there because the two engines spell auto-increment
// incompatibly. Postgres serialises on the row lock rather than failing the way
// SQLite would without its busy timeout, and this is what says so.
func TestPostgresConcurrentSendsGetDistinctIDs(t *testing.T) {
	users := newPostgresStore(t)
	store := messageserver.NewChatStore(users)
	ctx := context.Background()

	const senders = 4
	const each = 5

	keys := make([]string, senders)
	for i := range keys {
		keys[i] = authorizedNamedKey(t, users, fmt.Sprintf("sender%d", i))
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ids  []int
		errs []error
	)
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			for n := range each {
				msg, err := store.Add(ctx, key, fmt.Sprintf("sender%d", i), fmt.Sprintf("m%d", n), nil)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					ids = append(ids, msg.ID)
				}
				mu.Unlock()
			}
		}(i, key)
	}
	wg.Wait()

	for _, err := range errs {
		t.Fatalf("concurrent add: %v", err)
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("id %d was handed out twice", id)
		}
		seen[id] = true
	}
	if len(seen) != senders*each {
		t.Fatalf("got %d distinct ids, want %d", len(seen), senders*each)
	}
}

// Two UserStores over one Postgres database are two pods behind the ALB. This
// is the scenario REQ-008 could not support and REQ-009 is for.
func TestPostgresSharesTheLogAcrossInstances(t *testing.T) {
	podA := newPostgresStore(t)
	ctx := context.Background()

	stephen := authorizedNamedKey(t, podA, "stephen")
	sophia := authorizedNamedKey(t, podA, "sophia")
	if err := podA.AddChatMember(ctx, stephen, sophia); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := messageserver.NewChatStore(podA).Add(ctx, stephen, "stephen", "across pods", []string{sophia}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// A second connection pool, as a second replica would have.
	podB, err := messageserver.OpenUserStore(ctx, os.Getenv(postgresURLVar), messageserver.RequireSchema)
	if err != nil {
		t.Fatalf("open second instance: %v", err)
	}
	defer podB.Close()

	seen, err := messageserver.NewChatStore(podB).Since(ctx, 0, sophia)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(seen) != 1 || seen[0].Text != "across pods" {
		t.Fatalf("the second instance sees %v, want the message the first took", seen)
	}
}
