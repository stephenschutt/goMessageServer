// Usernames and chat membership: the part of the database that is about people
// rather than keys.
//
// A key in authorizedUsers may use the API, but it starts out anonymous. The
// user picks a username for themselves once they are through the door, which is
// what puts them in the directory other users search; adding one of those
// results puts the two of them in each other's chat.
//
// Both tables are keyed by public key rather than by name, so renaming yourself
// changes what people see and nothing else.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"goMessageServer/internal/database"
)

const (
	usernameTable = "userNames"
	membersTable  = "chatMembers"
)

// Username length bounds. Long enough to be recognisable, short enough to fit
// the chat header on a phone.
const (
	minUsernameLen = 3
	maxUsernameLen = 20
)

// searchLimit caps how many names a directory search returns. The client shows
// them in a list it expects the user to read, not page through.
const searchLimit = 25

// ErrUsernameTaken is returned when a name already belongs to another key.
// Names are compared case-insensitively, so "Alice" collides with "alice".
var ErrUsernameTaken = errors.New("that username is already taken")

// ErrNoSuchUser is returned when a name matches nobody in the directory.
var ErrNoSuchUser = errors.New("no user by that name")

// ErrUsernameRequired is returned when a request that names a user names none.
var ErrUsernameRequired = errors.New("username is required")

// ErrSelfMember is returned by AddChatMember for a user adding themselves.
var ErrSelfMember = errors.New("you are already in your own chat")

// ValidateUsername reports whether a name is usable and returns it trimmed.
// The character set is deliberately narrow: names are typed by one person to
// find another, so anything that renders ambiguously (spaces, lookalike
// punctuation, control characters) is more trouble than it is worth.
func ValidateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case len(name) < minUsernameLen:
		return "", fmt.Errorf("a username needs at least %d characters", minUsernameLen)
	case len(name) > maxUsernameLen:
		return "", fmt.Errorf("a username can be at most %d characters", maxUsernameLen)
	}

	for i, r := range name {
		alphanumeric := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if alphanumeric {
			continue
		}
		// Separators are allowed inside the name but not at either end, which
		// keeps "..." and " -bob" out of the directory.
		if (r == '.' || r == '_' || r == '-') && i > 0 && i < len(name)-1 {
			continue
		}
		return "", errors.New("a username can use letters, digits, and . _ - between them")
	}
	return name, nil
}

// SetUsername claims a name for a public key, replacing any name that key
// already had. It returns ErrUsernameTaken if the name belongs to someone else.
func (s *UserStore) SetUsername(ctx context.Context, publicKey, username string) error {
	username, err := ValidateUsername(username)
	if err != nil {
		return err
	}

	// Checking first is what turns the common case into a clear error rather
	// than a constraint violation; the unique index is still what makes it
	// true, for the two clients that claim one name at the same moment.
	owner, err := s.KeyForUsername(ctx, username)
	if err != nil {
		return err
	}
	if owner != "" && owner != publicKey {
		return ErrUsernameTaken
	}

	stmt := fmt.Sprintf(
		`INSERT INTO %s (publicKey, username) VALUES (%s, %s)
		 ON CONFLICT (publicKey) DO UPDATE SET username = excluded.username`,
		database.Quote(usernameTable), s.dialect.Placeholder(1), s.dialect.Placeholder(2))
	if _, err := s.db.ExecContext(ctx, stmt, publicKey, username); err != nil {
		if isUniqueViolation(err) {
			return ErrUsernameTaken
		}
		return fmt.Errorf("insert into %s: %w", usernameTable, err)
	}
	return nil
}

// Username returns the name a key has chosen, or "" if it has not chosen one.
func (s *UserStore) Username(ctx context.Context, publicKey string) (string, error) {
	stmt := fmt.Sprintf(`SELECT username FROM %s WHERE publicKey = %s`,
		database.Quote(usernameTable), s.dialect.Placeholder(1))
	var username string
	switch err := s.db.QueryRowContext(ctx, stmt, publicKey).Scan(&username); {
	case err == sql.ErrNoRows:
		return "", nil
	case err != nil:
		return "", fmt.Errorf("query %s: %w", usernameTable, err)
	}
	return username, nil
}

// KeyForUsername resolves a name to the public key that owns it, or "" if the
// name is unclaimed. The match ignores case, the way the search does.
func (s *UserStore) KeyForUsername(ctx context.Context, username string) (string, error) {
	stmt := fmt.Sprintf(`SELECT publicKey FROM %s WHERE lower(username) = lower(%s)`,
		database.Quote(usernameTable), s.dialect.Placeholder(1))
	var key string
	switch err := s.db.QueryRowContext(ctx, stmt, username).Scan(&key); {
	case err == sql.ErrNoRows:
		return "", nil
	case err != nil:
		return "", fmt.Errorf("query %s: %w", usernameTable, err)
	}
	return key, nil
}

// SearchUsernames lists named users whose name contains query, ignoring case,
// leaving out the caller's own key. An empty query lists the directory, which
// is how a client shows something useful before anything has been typed.
func (s *UserStore) SearchUsernames(ctx context.Context, query, excludeKey string) ([]string, error) {
	stmt := fmt.Sprintf(
		`SELECT username FROM %s
		 WHERE lower(username) LIKE %s ESCAPE '\' AND publicKey <> %s
		 ORDER BY lower(username) LIMIT %d`,
		database.Quote(usernameTable), s.dialect.Placeholder(1), s.dialect.Placeholder(2), searchLimit)

	rows, err := s.db.QueryContext(ctx, stmt, "%"+likePattern(query)+"%", excludeKey)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", usernameTable, err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// likePattern lower-cases a search term and defuses the wildcards in it, so a
// user typing "%" searches for a percent sign instead of matching everyone.
func likePattern(query string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(strings.ToLower(strings.TrimSpace(query)))
}

// AddChatMember puts two users in each other's chat. Membership is symmetric on
// purpose: the person you added can answer you straight away, and neither side
// ends up talking into a chat the other is not in.
func (s *UserStore) AddChatMember(ctx context.Context, ownerKey, memberKey string) error {
	if ownerKey == memberKey {
		return ErrSelfMember
	}
	for _, pair := range [][2]string{{ownerKey, memberKey}, {memberKey, ownerKey}} {
		stmt := fmt.Sprintf(
			`INSERT INTO %s (ownerKey, memberKey) VALUES (%s, %s)
			 ON CONFLICT (ownerKey, memberKey) DO NOTHING`,
			database.Quote(membersTable), s.dialect.Placeholder(1), s.dialect.Placeholder(2))
		if _, err := s.db.ExecContext(ctx, stmt, pair[0], pair[1]); err != nil {
			return fmt.Errorf("insert into %s: %w", membersTable, err)
		}
	}
	return nil
}

// RemoveChatMember undoes AddChatMember from both sides, for the same reason it
// added both: a one-sided removal would leave one person sending into a chat
// the other had left.
func (s *UserStore) RemoveChatMember(ctx context.Context, ownerKey, memberKey string) error {
	stmt := fmt.Sprintf(
		`DELETE FROM %s WHERE (ownerKey = %s AND memberKey = %s) OR (ownerKey = %s AND memberKey = %s)`,
		database.Quote(membersTable),
		s.dialect.Placeholder(1), s.dialect.Placeholder(2),
		s.dialect.Placeholder(3), s.dialect.Placeholder(4))
	if _, err := s.db.ExecContext(ctx, stmt, ownerKey, memberKey, memberKey, ownerKey); err != nil {
		return fmt.Errorf("delete from %s: %w", membersTable, err)
	}
	return nil
}

// ChatMembers lists the names of everyone in a user's chat.
func (s *UserStore) ChatMembers(ctx context.Context, ownerKey string) ([]string, error) {
	stmt := fmt.Sprintf(
		`SELECT u.username FROM %s m JOIN %s u ON u.publicKey = m.memberKey
		 WHERE m.ownerKey = %s ORDER BY lower(u.username)`,
		database.Quote(membersTable), database.Quote(usernameTable), s.dialect.Placeholder(1))
	rows, err := s.db.QueryContext(ctx, stmt, ownerKey)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", membersTable, err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// ChatMemberKeys lists the public keys in a user's chat. It is what a new
// message is addressed to, because keys are what identify a reader later even
// if they have renamed themselves since.
func (s *UserStore) ChatMemberKeys(ctx context.Context, ownerKey string) ([]string, error) {
	stmt := fmt.Sprintf(`SELECT memberKey FROM %s WHERE ownerKey = %s`,
		database.Quote(membersTable), s.dialect.Placeholder(1))
	rows, err := s.db.QueryContext(ctx, stmt, ownerKey)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", membersTable, err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// isUniqueViolation recognises a broken unique constraint without depending on
// either driver's error type: SQLite says "UNIQUE constraint failed" and
// Postgres "duplicate key value violates unique constraint".
func isUniqueViolation(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate key")
}
