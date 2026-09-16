package tests

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	messageserver "goMessageServer"
)

// openStoreAt opens a messageserver.UserStore on a given database file, so a test can have
// The reason REQ-009 exists. Two ChatStores over one database are two replicas
// behind the load balancer: a message sent through one has to be readable
// through the other, which was exactly what the in-memory log could not do.
func TestMessagesAreVisibleAcrossServerInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")
	podA := openStoreAt(t, path)
	podB := openStoreAt(t, path)

	ctx := context.Background()
	stephen := authorizedNamedKey(t, podA, "stephen")
	sophia := authorizedNamedKey(t, podA, "sophia")
	if err := podA.AddChatMember(ctx, stephen, sophia); err != nil {
		t.Fatalf("add member: %v", err)
	}

	// Sent to one replica...
	sent, err := messageserver.NewChatStore(podA).Add(ctx, stephen, "stephen", "hello from pod A", []string{sophia})
	if err != nil {
		t.Fatalf("add message: %v", err)
	}

	// ...read from the other.
	seen, err := messageserver.NewChatStore(podB).Since(ctx, 0, sophia)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(seen) != 1 || seen[0].Text != "hello from pod A" || seen[0].Sender != "stephen" {
		t.Fatalf("pod B sees %v, want the message pod A took", seen)
	}
	if seen[0].ID != sent.ID {
		t.Fatalf("pod B calls it id %d, pod A called it %d", seen[0].ID, sent.ID)
	}
	if !seen[0].Timestamp.Equal(sent.Timestamp) {
		t.Fatalf("timestamp came back as %v, want %v", seen[0].Timestamp, sent.Timestamp)
	}
}

// A restart used to empty the chat. It should not any more.
func TestMessagesSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.db")

	ctx := context.Background()
	before := openStoreAt(t, path)
	stephen := authorizedNamedKey(t, before, "stephen")
	if _, err := messageserver.NewChatStore(before).Add(ctx, stephen, "stephen", "still here?", nil); err != nil {
		t.Fatalf("add message: %v", err)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	after, err := messageserver.NewChatStore(openStoreAt(t, path)).Since(ctx, 0, stephen)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(after) != 1 || after[0].Text != "still here?" {
		t.Fatalf("after restart the log is %v, want the one message", after)
	}
}

// Delivery is decided when a message is sent, so joining a chat does not hand
// someone the history from before they were in it. The in-memory version fixed
// the recipient list at send time for this reason; the table has to as well.
func TestJoiningAChatDoesNotRevealEarlierMessages(t *testing.T) {
	users := newTestStore(t)
	store := messageserver.NewChatStore(users)
	ctx := context.Background()

	stephen := authorizedNamedKey(t, users, "stephen")
	sophia := authorizedNamedKey(t, users, "sophia")

	// Said before sophia was in the chat, and addressed to nobody.
	if _, err := store.Add(ctx, stephen, "stephen", "before", nil); err != nil {
		t.Fatalf("add message: %v", err)
	}

	if err := users.AddChatMember(ctx, stephen, sophia); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := store.Add(ctx, stephen, "stephen", "after", []string{sophia}); err != nil {
		t.Fatalf("add message: %v", err)
	}

	seen, err := store.Since(ctx, 0, sophia)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(seen) != 1 || seen[0].Text != "after" {
		t.Fatalf("sophia sees %v, want only the message sent after she joined", seen)
	}

	// The sender reads their own transcript whole, delivery list or not.
	mine, err := store.Since(ctx, 0, stephen)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("stephen sees %v, want both of his own messages", mine)
	}
}

// "?since=<id>" is only a usable cursor if ids never repeat and never go
// backwards. With the sequence table doing the work rather than a mutex, that
// has to hold when several senders are writing at once.
func TestConcurrentSendsGetDistinctIncreasingIDs(t *testing.T) {
	users := newTestStore(t)
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
	if len(ids) != senders*each {
		t.Fatalf("got %d ids, want %d", len(ids), senders*each)
	}

	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("id %d was handed out twice", id)
		}
		seen[id] = true
		if id < 1 {
			t.Fatalf("id %d is not positive", id)
		}
	}
}

// A poll asks for everything after an id it already has; it should not be
// handed an unbounded response.
func TestSinceIsBounded(t *testing.T) {
	users := newTestStore(t)
	store := messageserver.NewChatStore(users)
	ctx := context.Background()
	stephen := authorizedNamedKey(t, users, "stephen")

	for n := range messageserver.MaxMessageBatch + 10 {
		if _, err := store.Add(ctx, stephen, "stephen", fmt.Sprintf("m%d", n), nil); err != nil {
			t.Fatalf("add message %d: %v", n, err)
		}
	}

	first, err := store.Since(ctx, 0, stephen)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(first) != messageserver.MaxMessageBatch {
		t.Fatalf("one poll returned %d messages, want the %d cap", len(first), messageserver.MaxMessageBatch)
	}

	// The rest arrives on the next poll, which is what makes the cap harmless.
	rest, err := store.Since(ctx, first[len(first)-1].ID, stephen)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(rest) != 10 {
		t.Fatalf("the next poll returned %d messages, want the remaining 10", len(rest))
	}
}
