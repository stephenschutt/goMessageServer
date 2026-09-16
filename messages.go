// The message log, in the database.
//
// This used to be a slice guarded by a mutex, which meant each server process
// had a transcript of its own. That is invisible on a laptop and wrong the
// moment there is more than one process: the EKS deployment in infra/terraform
// runs three replicas behind a load balancer, and two clients that landed on
// different pods could not see each other's messages. Every replica now reads
// and writes the same three tables — "Messages", "messageRecipients" and
// "messageSequence" — so which pod answers a request stops mattering.
//
// Delivery is still decided when a message is sent, not when it is read: the
// recipients are the keys in the sender's chat at that moment, written
// alongside the message. Joining a chat therefore does not reveal what was said
// before you were in it, and leaving does not retract what you were shown.
package messageserver

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"goMessageServer/internal/database"
)

const (
	MessagesTable   = "Messages"
	RecipientsTable = "messageRecipients"
	SequenceTable   = "messageSequence"
)

// MaxMessageBatch caps one poll. A client asks for everything after an id it
// already has, and a client that has been away for a week should not be handed
// the week in a single response; it will ask again with a higher id.
const MaxMessageBatch = 500

// Message is one line of chat. Sender is the username its author had when they
// sent it, so a later rename does not rewrite the transcript.
//
// Who else may read a message is deliberately not part of this struct: it is a
// column in "messageRecipients" that the queries below filter on, and a client
// is told who wrote a message, not who else can see it.
type Message struct {
	ID        int       `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

// ChatStore is the message log. It holds no messages itself — the database
// does — and is safe for concurrent use because *sql.DB pools connections.
type ChatStore struct {
	db      *sql.DB
	dialect database.Dialect
}

// NewChatStore reads and writes messages through the same connection the
// authorization tables use. One database, one pool, one transaction when
// something has to be written atomically.
func NewChatStore(users *UserStore) *ChatStore {
	return &ChatStore{db: users.db, dialect: users.dialect}
}

// Add records a message from senderKey (posting as sender) addressed to
// recipients, the keys in that sender's chat.
//
// The message, its delivery list and the id it was given are one transaction:
// a message is never half-delivered, and no two messages share an id.
func (s *ChatStore) Add(ctx context.Context, senderKey, sender, text string, recipients []string) (Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, fmt.Errorf("begin: %w", err)
	}
	// A rollback after a successful commit does nothing, so this covers every
	// early return below without a flag to track.
	defer tx.Rollback()

	id, err := nextMessageID(ctx, tx, s.dialect)
	if err != nil {
		return Message{}, err
	}

	sentAt := time.Now().UTC()
	insert := fmt.Sprintf(
		`INSERT INTO %s (id, senderKey, sender, body, sentAt) VALUES (%s, %s, %s, %s, %s)`,
		database.Quote(MessagesTable),
		s.dialect.Placeholder(1), s.dialect.Placeholder(2), s.dialect.Placeholder(3),
		s.dialect.Placeholder(4), s.dialect.Placeholder(5))
	if _, err := tx.ExecContext(ctx, insert, id, senderKey, sender, text, formatSentAt(sentAt)); err != nil {
		return Message{}, fmt.Errorf("insert into %s: %w", MessagesTable, err)
	}

	// The sender is not in their own delivery list: Since matches them by
	// senderKey, which is one row fewer per message and one less thing to keep
	// consistent.
	deliver := fmt.Sprintf(
		`INSERT INTO %s (messageId, recipientKey) VALUES (%s, %s)
		 ON CONFLICT (messageId, recipientKey) DO NOTHING`,
		database.Quote(RecipientsTable), s.dialect.Placeholder(1), s.dialect.Placeholder(2))
	for _, key := range recipients {
		if key == senderKey {
			continue
		}
		if _, err := tx.ExecContext(ctx, deliver, id, key); err != nil {
			return Message{}, fmt.Errorf("insert into %s: %w", RecipientsTable, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("commit: %w", err)
	}

	return Message{ID: id, Sender: sender, Text: text, Timestamp: sentAt}, nil
}

// nextMessageID takes the next id from "messageSequence".
//
// Two statements rather than one UPDATE ... RETURNING, because RETURNING is not
// something both drivers can be relied on for. Inside the transaction they are
// equivalent: the UPDATE takes a write lock on the single row, the SELECT reads
// the value this transaction just wrote, and any other sender waits rather than
// being handed the same number.
func nextMessageID(ctx context.Context, tx *sql.Tx, dialect database.Dialect) (int, error) {
	bump := fmt.Sprintf(`UPDATE %s SET nextId = nextId + 1 WHERE id = 1`, database.Quote(SequenceTable))
	result, err := tx.ExecContext(ctx, bump)
	if err != nil {
		return 0, fmt.Errorf("update %s: %w", SequenceTable, err)
	}
	// The row is created by migration 0003. Its absence means the schema is not
	// what this code expects, which is worth saying plainly rather than
	// discovering as a duplicate id later.
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return 0, fmt.Errorf("%s has no row 1; the schema is out of date", SequenceTable)
	}

	read := fmt.Sprintf(`SELECT nextId FROM %s WHERE id = 1`, database.Quote(SequenceTable))
	var id int
	if err := tx.QueryRowContext(ctx, read).Scan(&id); err != nil {
		return 0, fmt.Errorf("query %s: %w", SequenceTable, err)
	}
	return id, nil
}

// Since returns the messages after id that viewerKey is allowed to read, oldest
// first, at most MaxMessageBatch of them.
//
// "Allowed to read" is the same rule the in-memory version applied in Go: you
// wrote it, or you were on its delivery list when it was sent.
func (s *ChatStore) Since(ctx context.Context, id int, viewerKey string) ([]Message, error) {
	query := fmt.Sprintf(
		`SELECT m.id, m.sender, m.body, m.sentAt
		 FROM %s m
		 WHERE m.id > %s
		   AND (m.senderKey = %s
		        OR EXISTS (SELECT 1 FROM %s r
		                   WHERE r.messageId = m.id AND r.recipientKey = %s))
		 ORDER BY m.id
		 LIMIT %d`,
		database.Quote(MessagesTable),
		s.dialect.Placeholder(1), s.dialect.Placeholder(2),
		database.Quote(RecipientsTable), s.dialect.Placeholder(3),
		MaxMessageBatch)

	rows, err := s.db.QueryContext(ctx, query, id, viewerKey, viewerKey)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", MessagesTable, err)
	}
	defer rows.Close()

	// Never nil: the handler marshals this straight to JSON, and a client
	// polling an empty log should read [] rather than null.
	out := make([]Message, 0)
	for rows.Next() {
		var (
			msg    Message
			sentAt string
		)
		if err := rows.Scan(&msg.ID, &msg.Sender, &msg.Text, &sentAt); err != nil {
			return nil, err
		}
		msg.Timestamp, err = parseSentAt(sentAt)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", msg.ID, err)
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

// sentAtLayout is how a timestamp is spelled in the sentAt column: RFC3339 with
// nanoseconds, always UTC, so the text sorts in the order the messages happened.
const sentAtLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatSentAt(t time.Time) string { return t.UTC().Format(sentAtLayout) }

// parseSentAt reads that spelling back, and also accepts a plain RFC3339 one
// without a fractional part — which is what a row inserted by hand looks like.
func parseSentAt(value string) (time.Time, error) {
	t, err := time.Parse(sentAtLayout, strings.TrimSpace(value))
	if err == nil {
		return t.UTC(), nil
	}
	t, rfcErr := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if rfcErr != nil {
		return time.Time{}, fmt.Errorf("unreadable sentAt %q: %w", value, err)
	}
	return t.UTC(), nil
}
