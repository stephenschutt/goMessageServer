// Authentication for messageServer.
//
// Every client owns an RSA key pair and keeps the private half to itself: the
// only key material that ever crosses the wire is the public key. A bare public
// key is not a secret, though — anyone who observed one could replay it — so a
// request proves possession of the matching private key by signing a canonical
// summary of itself. The server verifies that signature with the public key the
// request presented, then asks the database whether that key is authorized.
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
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Header names carrying the credential. They travel on every authenticated
// request; there is no session or cookie, so nothing is left behind to steal.
const (
	headerPublicKey = "X-Public-Key"
	headerTimestamp = "X-Timestamp"
	headerNonce     = "X-Nonce"
	headerSignature = "X-Signature"
)

// maxSkew bounds how stale a signed request may be. A signature is only valid
// for this long, which keeps a captured request from being replayed forever.
const maxSkew = 5 * time.Minute

// maxBody caps the request body the server will read and hash. The chat API's
// largest legitimate body is a 2000-character message.
const maxBody = 64 << 10

// minKeyBits rejects keys too small to be worth verifying.
const minKeyBits = 2048

// Credential is a verified request identity: the public key the caller
// presented, in both the encoded form stored in the database and the parsed
// form used to check signatures.
type Credential struct {
	// PublicKey is the base64 (standard encoding) of the DER SPKI form of the
	// key. This exact string is what lives in authorizedUsers.publicKey, so
	// clients and server must agree on the encoding down to the byte.
	PublicKey string
	key       *rsa.PublicKey
}

// Fingerprint is a short, log-safe name for the key: the first bytes of its
// SHA-256, hex encoded. Full keys are long enough to make log lines unreadable.
func (c Credential) Fingerprint() string {
	sum := sha256.Sum256([]byte(c.PublicKey))
	return hex.EncodeToString(sum[:8])
}

// credentialKey is the context key under which the middleware stashes the
// verified Credential for handlers that want to know who is calling.
type credentialKey struct{}

// withCredential attaches a verified identity to a request context.
func withCredential(ctx context.Context, cred Credential) context.Context {
	return context.WithValue(ctx, credentialKey{}, cred)
}

// credentialFrom returns the identity authenticate established for a request.
// It is only absent if a handler is mounted outside the middleware.
func credentialFrom(ctx context.Context) (Credential, bool) {
	cred, ok := ctx.Value(credentialKey{}).(Credential)
	return cred, ok
}

// sweepInterval is how often expired nonces are cleared out. Sweeping on every
// request would make each one cost a full pass over the cache, which under load
// is the difference between a constant and a quadratic amount of work.
const sweepInterval = 30 * time.Second

// nonceCache remembers recently used nonces so a signed request cannot be
// replayed inside the skew window. Entries older than maxSkew are dropped,
// because a signature that old fails the freshness check anyway; the cache
// therefore holds only what one maxSkew-plus-sweepInterval span of traffic
// produces, however long the server runs.
type nonceCache struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	lastSweep time.Time
}

func newNonceCache() *nonceCache {
	return &nonceCache{seen: make(map[string]time.Time), lastSweep: time.Now()}
}

// use records a nonce and reports whether it was fresh. A false return means
// the nonce has already been spent and the request must be rejected.
func (n *nonceCache) use(nonce string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	if now.Sub(n.lastSweep) >= sweepInterval {
		for k, t := range n.seen {
			if now.Sub(t) > maxSkew {
				delete(n.seen, k)
			}
		}
		n.lastSweep = now
	}

	if _, dup := n.seen[nonce]; dup {
		return false
	}
	n.seen[nonce] = now
	return true
}

var errUnsigned = errors.New("request is not signed")

// parseCredential pulls the public key out of a request and rejects anything
// that is not a usable RSA key. It does no signature checking; verifySignature
// does that once the body has been read.
func parseCredential(r *http.Request) (Credential, error) {
	encoded := r.Header.Get(headerPublicKey)
	if encoded == "" {
		return Credential{}, errUnsigned
	}
	return parseEncodedKey(encoded)
}

// parseEncodedKey validates one base64 DER SPKI RSA public key — the spelling
// clients put on the wire and administrators paste into -authorize — and
// returns it in the canonical form the database stores.
func parseEncodedKey(encoded string) (Credential, error) {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return Credential{}, errors.New("public key is not base64")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return Credential{}, errors.New("public key is not a DER SPKI key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return Credential{}, errors.New("public key is not RSA")
	}
	if key.N.BitLen() < minKeyBits {
		return Credential{}, errors.New("RSA key is too small")
	}
	// Re-encode rather than trusting the header's spelling: base64 has slack
	// (padding, stray whitespace) that would otherwise let one key take on
	// several database identities.
	canonical := base64.StdEncoding.EncodeToString(der)
	return Credential{PublicKey: canonical, key: key}, nil
}

// signingString is the exact text a client signs and the server reconstructs.
// It pins the request's method, target and body, so a valid signature cannot be
// lifted off one request and pasted onto another.
func signingString(method, path, query, timestamp, nonce string, body []byte) string {
	sum := sha256.Sum256(body)
	var b bytes.Buffer
	for _, part := range []string{method, path, query, timestamp, nonce, hex.EncodeToString(sum[:])} {
		b.WriteString(part)
		b.WriteByte('\n')
	}
	return b.String()
}

// verifySignature checks freshness, replay and the signature itself. body is
// the request body the caller has already buffered.
func verifySignature(cred Credential, r *http.Request, body []byte, nonces *nonceCache) error {
	rawTimestamp := r.Header.Get(headerTimestamp)
	nonce := r.Header.Get(headerNonce)
	encodedSig := r.Header.Get(headerSignature)
	if rawTimestamp == "" || nonce == "" || encodedSig == "" {
		return errUnsigned
	}

	seconds, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return errors.New("timestamp is not unix seconds")
	}
	now := time.Now()
	if skew := now.Sub(time.Unix(seconds, 0)); skew > maxSkew || skew < -maxSkew {
		return errors.New("timestamp is outside the allowed window")
	}
	if !nonces.use(nonce, now) {
		return errors.New("nonce has already been used")
	}

	sig, err := base64.StdEncoding.DecodeString(encodedSig)
	if err != nil {
		return errors.New("signature is not base64")
	}
	signed := signingString(r.Method, r.URL.Path, r.URL.RawQuery, rawTimestamp, nonce, body)
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(cred.key, crypto.SHA256, digest[:], sig); err != nil {
		return errors.New("signature does not match the public key")
	}
	return nil
}

// authenticate wraps a handler so it only runs for a client that both proved
// possession of its private key and appears in authorizedUsers. A caller whose
// signature checks out but whose key is unknown is recorded in
// unauthorizedUsers, which is how an administrator learns a key to approve.
func authenticate(users *UserStore, next http.Handler) http.Handler {
	nonces := newNonceCache()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred, err := parseCredential(r)
		if err != nil {
			// Nothing identifies this caller, so there is no key to file away.
			unauthenticated(w, err)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil {
			http.Error(w, "could not read body", http.StatusBadRequest)
			return
		}
		if len(body) > maxBody {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		// The body was consumed to hash it; hand the handler a fresh reader.
		r.Body = io.NopCloser(bytes.NewReader(body))

		if err := verifySignature(cred, r, body, nonces); err != nil {
			// The signature failed, so possession of the private key is
			// unproven and the public key may well be someone else's. Filing
			// it under unauthorizedUsers would let anyone pollute that table
			// with keys they merely copied, so only log it.
			log.Printf("auth: rejected %s (%s): %v", cred.Fingerprint(), r.URL.Path, err)
			unauthenticated(w, err)
			return
		}

		authorized, err := users.IsAuthorized(r.Context(), cred.PublicKey)
		if err != nil {
			log.Printf("auth: authorization lookup failed for %s: %v", cred.Fingerprint(), err)
			http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
			return
		}
		if !authorized {
			if err := users.RecordUnauthorized(r.Context(), cred.PublicKey); err != nil {
				log.Printf("auth: could not record unauthorized key %s: %v", cred.Fingerprint(), err)
			}
			log.Printf("auth: unauthorized key %s attempted %s %s", cred.Fingerprint(), r.Method, r.URL.Path)
			http.Error(w, "this key is not authorized", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r.WithContext(withCredential(r.Context(), cred)))
	})
}

// unauthenticated answers a caller that never established an identity. The
// header tells a client what scheme to use rather than leaving it guessing.
func unauthenticated(w http.ResponseWriter, err error) {
	w.Header().Set("WWW-Authenticate", `Signature realm="messageServer"`)
	http.Error(w, "not authenticated: "+err.Error(), http.StatusUnauthorized)
}

// newNonce mints the per-request nonce that keeps a signature single-use.
func newNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail in practice; a chat server has nothing
		// useful to do if it does.
		panic("messageServer: out of randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
