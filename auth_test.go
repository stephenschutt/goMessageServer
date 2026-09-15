package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
	nonce := newNonce()

	signed := signingString(method, req.URL.Path, req.URL.RawQuery, timestamp, nonce, body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	req.Header.Set(headerPublicKey, c.publicKey)
	req.Header.Set(headerTimestamp, timestamp)
	req.Header.Set(headerNonce, nonce)
	req.Header.Set(headerSignature, base64.StdEncoding.EncodeToString(sig))
	return req
}

func newTestStore(t *testing.T) *UserStore {
	t.Helper()
	store, err := OpenUserStore(context.Background(), filepath.Join(t.TempDir(), "users.db"), ApplySchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newTestHandler(users *UserStore) http.Handler {
	return authenticate(users, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestUnsignedRequestIsRejected(t *testing.T) {
	handler := newTestHandler(newTestStore(t))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/messages?since=0", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestAuthorizedKeyIsAllowedThrough(t *testing.T) {
	users := newTestStore(t)
	client := newTestClient(t)
	if err := users.Authorize(context.Background(), client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	rec := httptest.NewRecorder()
	newTestHandler(users).ServeHTTP(rec, client.request(t, http.MethodGet, "/api/messages?since=0", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

// An unknown key that nonetheless proves it owns its private half is the case
// the requirement names: refuse it, and file its public key for review.
func TestUnauthorizedKeyIsRecorded(t *testing.T) {
	users := newTestStore(t)
	client := newTestClient(t)

	rec := httptest.NewRecorder()
	newTestHandler(users).ServeHTTP(rec, client.request(t, http.MethodGet, "/api/messages?since=0", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	pending, err := users.PendingKeys(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != client.publicKey {
		t.Fatalf("unauthorizedUsers holds %v, want the caller's key", pending)
	}
}

// Presenting someone else's public key without their private key must fail, or
// the public key would be a credential anyone could copy off the wire.
func TestStolenPublicKeyIsRejected(t *testing.T) {
	users := newTestStore(t)
	victim := newTestClient(t)
	if err := users.Authorize(context.Background(), victim.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	attacker := newTestClient(t)
	req := attacker.request(t, http.MethodGet, "/api/messages?since=0", nil)
	req.Header.Set(headerPublicKey, victim.publicKey) // signature is still the attacker's

	rec := httptest.NewRecorder()
	newTestHandler(users).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	// A failed signature must not put the victim's key on the rejected list.
	pending, err := users.PendingKeys(context.Background())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("unauthorizedUsers holds %v, want nothing", pending)
	}
}

// A signature covers the body, so a captured POST cannot be edited in flight.
func TestTamperedBodyIsRejected(t *testing.T) {
	users := newTestStore(t)
	client := newTestClient(t)
	if err := users.Authorize(context.Background(), client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	req := client.request(t, http.MethodPost, "/api/messages", []byte(`{"sender":"Alice","text":"hi"}`))
	swapped := []byte(`{"sender":"Alice","text":"transfer everything"}`)
	req.Body = io.NopCloser(bytes.NewReader(swapped))
	req.ContentLength = int64(len(swapped))

	rec := httptest.NewRecorder()
	newTestHandler(users).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestReplayedRequestIsRejected(t *testing.T) {
	users := newTestStore(t)
	client := newTestClient(t)
	if err := users.Authorize(context.Background(), client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	handler := newTestHandler(users)

	req := client.request(t, http.MethodGet, "/api/messages?since=0", nil)
	replay := httptest.NewRequest(http.MethodGet, "/api/messages?since=0", nil)
	replay.Header = req.Header.Clone()

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("first request got %d, want 200", first.Code)
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, replay)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("replay got %d, want 401", second.Code)
	}
}

// Authorizing a key that was previously turned away should clear it from the
// rejected table, so -pending only ever lists keys still waiting.
func TestAuthorizeClearsPending(t *testing.T) {
	users := newTestStore(t)
	client := newTestClient(t)
	ctx := context.Background()

	if err := users.RecordUnauthorized(ctx, client.publicKey); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := users.Authorize(ctx, client.publicKey); err != nil {
		t.Fatalf("authorize: %v", err)
	}

	ok, err := users.IsAuthorized(ctx, client.publicKey)
	if err != nil || !ok {
		t.Fatalf("IsAuthorized = %v, %v; want true, nil", ok, err)
	}
	pending, err := users.PendingKeys(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("unauthorizedUsers holds %v, want nothing", pending)
	}
}
