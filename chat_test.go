package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// authorizedClient is a test client whose key is already in authorizedUsers,
// which is the state REQ-005 starts from: a device that can reach the API and
// has yet to choose a name.
func authorizedClient(t *testing.T, users *UserStore) *testClient {
	t.Helper()
	client := newTestClient(t)
	if err := users.Authorize(context.Background(), client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return client
}

// newAPI wires the real routes behind the real middleware, so these tests
// exercise what a device actually talks to.
func newAPI(users *UserStore, store *ChatStore) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/api/messages", handleMessages(store, users))
	api.HandleFunc("/api/identity", handleIdentity(users))
	api.HandleFunc("/api/username", handleUsername(users))
	api.HandleFunc("/api/users", handleUsers(users))
	api.HandleFunc("/api/chat", handleChat(users))
	return authenticate(users, api)
}

// call signs a request as client, runs it, and fails the test if the status is
// not what the caller expected. It returns the recorder for the body.
func call(t *testing.T, api http.Handler, client *testClient,
	method, target string, body []byte, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, client.request(t, method, target, body))
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: got %d (%s), want %d", method, target, rec.Code, rec.Body.String(), wantStatus)
	}
	return rec
}

func setUsername(t *testing.T, api http.Handler, client *testClient, name string) {
	t.Helper()
	call(t, api, client, http.MethodPut, "/api/username",
		[]byte(fmt.Sprintf(`{"username":%q}`, name)), http.StatusOK)
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rec.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return value
}

func TestUsernameIsChosenAndReadBack(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	client := authorizedClient(t, users)

	// A device that has not named itself is authorized but anonymous.
	before := decode[Profile](t, call(t, api, client, http.MethodGet, "/api/identity", nil, http.StatusOK))
	if before.Username != "" {
		t.Fatalf("new device already named %q", before.Username)
	}

	setUsername(t, api, client, "Stephen")

	after := decode[Profile](t, call(t, api, client, http.MethodGet, "/api/username", nil, http.StatusOK))
	if after.Username != "Stephen" {
		t.Fatalf("username is %q, want Stephen", after.Username)
	}
}

// A username is an identity other people rely on to find the right person, so
// two devices must not be able to hold the same one.
func TestUsernameCannotBeTakenTwice(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	first := authorizedClient(t, users)
	second := authorizedClient(t, users)

	setUsername(t, api, first, "Stephen")
	// Case is not a difference: the search matches case-insensitively, so
	// "stephen" would be indistinguishable from "Stephen" to anyone adding them.
	call(t, api, second, http.MethodPut, "/api/username", []byte(`{"username":"stephen"}`), http.StatusConflict)

	// Re-claiming your own name is not a conflict; it is how a client saves the
	// form again without changing anything.
	setUsername(t, api, first, "Stephen")
}

func TestInvalidUsernameIsRejected(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	client := authorizedClient(t, users)

	for _, name := range []string{"ab", "a name with spaces", ".leading", "trailing.", "twenty-one-chars-long"} {
		call(t, api, client, http.MethodPut, "/api/username",
			[]byte(fmt.Sprintf(`{"username":%q}`, name)), http.StatusBadRequest)
	}
}

// An unauthorized key must not reach the username API at all: choosing a name
// is something a device does once it is in authorizedUsers, not before.
func TestUnauthorizedKeyCannotChooseUsername(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	stranger := newTestClient(t) // signed, but never authorized

	call(t, api, stranger, http.MethodPut, "/api/username",
		[]byte(`{"username":"stranger"}`), http.StatusForbidden)
}

func TestSearchFindsOtherUsers(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	me := authorizedClient(t, users)
	her := authorizedClient(t, users)
	anonymous := authorizedClient(t, users)

	setUsername(t, api, me, "stephen")
	setUsername(t, api, her, "sophia")
	_ = anonymous // authorized, unnamed, and so not in the directory

	found := decode[[]DirectoryEntry](t, call(t, api, me, http.MethodGet, "/api/users?q=SOP", nil, http.StatusOK))
	if len(found) != 1 || found[0].Username != "sophia" {
		t.Fatalf("search found %v, want just sophia", found)
	}
	if found[0].InChat {
		t.Fatal("sophia is reported as already in the chat")
	}

	// An empty query lists the directory, minus the caller and the unnamed.
	all := decode[[]DirectoryEntry](t, call(t, api, me, http.MethodGet, "/api/users", nil, http.StatusOK))
	if len(all) != 1 || all[0].Username != "sophia" {
		t.Fatalf("directory is %v, want just sophia", all)
	}
}

func TestAddingAUserIsMutualAndDeliversMessages(t *testing.T) {
	users := newTestStore(t)
	store := &ChatStore{}
	api := newAPI(users, store)
	me := authorizedClient(t, users)
	her := authorizedClient(t, users)
	bystander := authorizedClient(t, users)

	setUsername(t, api, me, "stephen")
	setUsername(t, api, her, "sophia")
	setUsername(t, api, bystander, "nosy")

	chat := decode[Chat](t, call(t, api, me, http.MethodPost, "/api/chat",
		[]byte(`{"username":"sophia"}`), http.StatusOK))
	if len(chat.Members) != 1 || chat.Members[0] != "sophia" {
		t.Fatalf("my chat is %v, want [sophia]", chat.Members)
	}

	// Mutual: sophia can answer without having to add stephen back.
	hers := decode[Chat](t, call(t, api, her, http.MethodGet, "/api/chat", nil, http.StatusOK))
	if len(hers.Members) != 1 || hers.Members[0] != "stephen" {
		t.Fatalf("sophia's chat is %v, want [stephen]", hers.Members)
	}

	call(t, api, me, http.MethodPost, "/api/messages", []byte(`{"text":"hello"}`), http.StatusCreated)

	delivered := decode[[]Message](t, call(t, api, her, http.MethodGet, "/api/messages?since=0", nil, http.StatusOK))
	if len(delivered) != 1 || delivered[0].Sender != "stephen" || delivered[0].Text != "hello" {
		t.Fatalf("sophia sees %v, want one message from stephen", delivered)
	}

	// Nobody outside the chat sees it, however authorized they are.
	seen := decode[[]Message](t, call(t, api, bystander, http.MethodGet, "/api/messages?since=0", nil, http.StatusOK))
	if len(seen) != 0 {
		t.Fatalf("an outsider sees %v, want nothing", seen)
	}
}

func TestRemovingAUserStopsDelivery(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	me := authorizedClient(t, users)
	her := authorizedClient(t, users)

	setUsername(t, api, me, "stephen")
	setUsername(t, api, her, "sophia")
	call(t, api, me, http.MethodPost, "/api/chat", []byte(`{"username":"sophia"}`), http.StatusOK)

	chat := decode[Chat](t, call(t, api, her, http.MethodDelete, "/api/chat?username=stephen", nil, http.StatusOK))
	if len(chat.Members) != 0 {
		t.Fatalf("sophia's chat is %v after leaving, want empty", chat.Members)
	}

	call(t, api, me, http.MethodPost, "/api/messages", []byte(`{"text":"still there?"}`), http.StatusCreated)
	seen := decode[[]Message](t, call(t, api, her, http.MethodGet, "/api/messages?since=0", nil, http.StatusOK))
	if len(seen) != 0 {
		t.Fatalf("sophia still sees %v after leaving the chat", seen)
	}
}

func TestAddingAnUnknownUserIsNotFound(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	me := authorizedClient(t, users)
	setUsername(t, api, me, "stephen")

	call(t, api, me, http.MethodPost, "/api/chat", []byte(`{"username":"nobody"}`), http.StatusNotFound)
}

// A message is labelled with the sender the server knows, not the one the body
// claims, so a client cannot post as somebody else.
func TestSenderComesFromTheKeyNotTheBody(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	me := authorizedClient(t, users)
	setUsername(t, api, me, "stephen")

	rec := call(t, api, me, http.MethodPost, "/api/messages",
		[]byte(`{"sender":"sophia","text":"not me"}`), http.StatusCreated)
	if msg := decode[Message](t, rec); msg.Sender != "stephen" {
		t.Fatalf("message is from %q, want stephen", msg.Sender)
	}
}

// Until a device has a name there is nothing to sign a message with, and the
// client is told to fix that rather than left guessing.
func TestSendingWithoutAUsernameIsRefused(t *testing.T) {
	users := newTestStore(t)
	api := newAPI(users, &ChatStore{})
	me := authorizedClient(t, users)

	call(t, api, me, http.MethodPost, "/api/messages", []byte(`{"text":"hi"}`), http.StatusConflict)
}
