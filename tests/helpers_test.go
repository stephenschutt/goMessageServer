// Package tests holds the whole suite, per REQ-010.
//
// Go's own convention is a _test.go file beside the code it tests, because that
// is the only way a test can see a package's unexported parts. Keeping them
// here instead makes every test an outside caller: it sees exactly what the
// command in cmd/messageServer and the clients in mobile/ and webapp/ see. That
// is a real gain in one direction — nothing here can pass by reaching through
// the API — and a real cost in the other, which is why a handful of internals
// carry ForTest bridges (internal/database/testexports.go) instead of tests of
// their own.
//
// Everything in this file is shared setup. The tests themselves are in
// auth_test.go, chat_test.go, messages_test.go, migrate_test.go,
// postgres_test.go and webapp_test.go alongside it.
package tests

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	messageserver "goMessageServer"
)

// testClient is the client half of the signing scheme, mirroring what
// ChatClient.swift and templates/index.html do.
type testClient struct {
	key       *rsa.PrivateKey
	publicKey string
}

func newTestClient(t *testing.T) *testClient {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return &testClient{key: key, publicKey: base64.StdEncoding.EncodeToString(der)}
}

func (c *testClient) request(t *testing.T, method, target string, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := messageserver.NewNonce()

	signed := messageserver.SigningString(method, req.URL.Path, req.URL.RawQuery, timestamp, nonce, body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	req.Header.Set(messageserver.HeaderPublicKey, c.publicKey)
	req.Header.Set(messageserver.HeaderTimestamp, timestamp)
	req.Header.Set(messageserver.HeaderNonce, nonce)
	req.Header.Set(messageserver.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	return req
}

func newTestStore(t *testing.T) *messageserver.UserStore {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "users.db"))
}

// openStoreAt opens a UserStore on a given database file, so a test can have
// two of them over one database — which is what two server replicas are.
func openStoreAt(t *testing.T, path string) *messageserver.UserStore {
	t.Helper()
	store, err := messageserver.OpenUserStore(context.Background(), path, messageserver.ApplySchema)
	if err != nil {
		t.Fatalf("open store at %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// newTestHandler wraps a handler that always succeeds in the real middleware,
// so a test can see what the middleware decided without a route's own rules
// getting in the way.
func newTestHandler(users *messageserver.UserStore) http.Handler {
	return messageserver.Authenticate(users, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// newAPI wires the real routes behind the real middleware, so these tests
// exercise what a device actually talks to.
func newAPI(users *messageserver.UserStore, store *messageserver.ChatStore) http.Handler {
	return messageserver.NewAPIHandler(users, store)
}

// authorizedClient is a test client whose key is already in authorizedUsers,
// which is the state REQ-005 starts from: a device that can reach the API and
// has yet to choose a name.
func authorizedClient(t *testing.T, users *messageserver.UserStore) *testClient {
	t.Helper()
	client := newTestClient(t)
	if err := users.Authorize(context.Background(), client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return client
}

// authorizedNamedKey makes a key that is authorized and has chosen a name,
// which is the state a client has to be in before it can send anything.
func authorizedNamedKey(t *testing.T, users *messageserver.UserStore, name string) string {
	t.Helper()
	client := newTestClient(t)
	ctx := context.Background()
	if err := users.Authorize(ctx, client.publicKey); err != nil {
		t.Fatalf("authorize %s: %v", name, err)
	}
	if err := users.SetUsername(ctx, client.publicKey, name); err != nil {
		t.Fatalf("name %s: %v", name, err)
	}
	return client.publicKey
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
