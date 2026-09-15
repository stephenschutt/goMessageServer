package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// identity is the load tester's client key. The server authenticates every API
// request, so the tool needs a key pair of its own and the key has to be in
// authorizedUsers before a run will measure anything but 403s.
//
// The key is kept in a file between runs so it only has to be authorized once.
type identity struct {
	key       *rsa.PrivateKey
	publicKey string
}

// loadIdentity reuses the key at path, or creates and saves one if there is
// none. The file holds the private key, so it is written owner-only.
func loadIdentity(path string) (*identity, error) {
	if der, err := os.ReadFile(path); err == nil {
		key, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return newIdentity(key)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(path, x509.MarshalPKCS1PrivateKey(key), 0o600); err != nil {
		return nil, fmt.Errorf("save %s: %w", path, err)
	}
	return newIdentity(key)
}

func newIdentity(key *rsa.PrivateKey) (*identity, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return &identity{key: key, publicKey: base64.StdEncoding.EncodeToString(der)}, nil
}

// sign adds the credential headers messageServer checks. body must be the exact
// bytes the request will send, because the signature covers their hash.
func (id *identity) sign(req *http.Request, body []byte) error {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	bodyHash := sha256.Sum256(body)
	var canonical strings.Builder
	for _, part := range []string{
		req.Method, req.URL.Path, req.URL.RawQuery, timestamp, nonce,
		hex.EncodeToString(bodyHash[:]),
	} {
		canonical.WriteString(part)
		canonical.WriteByte('\n')
	}

	digest := sha256.Sum256([]byte(canonical.String()))
	sig, err := rsa.SignPKCS1v15(rand.Reader, id.key, crypto.SHA256, digest[:])
	if err != nil {
		return err
	}

	req.Header.Set("X-Public-Key", id.publicKey)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", base64.StdEncoding.EncodeToString(sig))
	return nil
}

// checkAuthorized makes one signed request before the run starts, so an
// unauthorized key fails with an explanation rather than as a wall of 403s in
// the results table.
func checkAuthorized(client *http.Client, addr string, id *identity) error {
	req, err := http.NewRequest(http.MethodGet, addr+"/api/identity", nil)
	if err != nil {
		return err
	}
	if err := id.sign(req, nil); err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", addr, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("this load tester's key is not authorized. Run:\n\n"+
			"    messageServer -authorize '%s'\n", id.publicKey)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server answered HTTP %d to a signed request", resp.StatusCode)
	}
	return nil
}

// ensureUsername claims username for this tool's key. The server labels a
// message with the sender's username and refuses a message from a key that has
// not chosen one, so without this every POST in the run would be a 409.
func ensureUsername(client *http.Client, addr string, id *identity, username string) error {
	payload, err := json.Marshal(map[string]string{"username": username})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, addr+"/api/username", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := id.sign(req, payload); err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("reach %s: %w", addr, err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return fmt.Errorf("the username %q belongs to another key; run with -username <other>", username)
	default:
		return fmt.Errorf("claiming the username %q: HTTP %d: %s",
			username, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
}
