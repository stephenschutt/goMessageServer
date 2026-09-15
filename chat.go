// The people half of the API: who the caller is, who else there is, and who is
// in the caller's chat.
//
// Every handler here runs behind the authenticate middleware, so the caller's
// public key is already known and already authorized — that is the requirement
// for setting a username at all. The key, not anything the client sends, is
// what these handlers act on; a client can rename itself but it cannot act as
// anybody else.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// Profile is what a client is told about itself: its key, and the name it has
// chosen, which is empty until it chooses one.
type Profile struct {
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	Username    string `json:"username"`
}

// DirectoryEntry is one search result. inChat saves the client from having to
// diff the results against its own roster to know which button to show.
type DirectoryEntry struct {
	Username string `json:"username"`
	InChat   bool   `json:"inChat"`
}

// Chat is the caller's conversation: who they are and who else is in it.
type Chat struct {
	Username string   `json:"username"`
	Members  []string `json:"members"`
}

// handleIdentity lets a client confirm its key was accepted without sending a
// message first. Reaching this handler at all means the request was signed and
// the key is authorized, so the answer is simply who the caller is.
func handleIdentity(users *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, ok := caller(w, r)
		if !ok {
			return
		}
		username, err := users.Username(r.Context(), cred.PublicKey)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, Profile{
			PublicKey:   cred.PublicKey,
			Fingerprint: cred.Fingerprint(),
			Username:    username,
		})
	}
}

// handleUsername reads and sets the caller's own username: GET returns it,
// PUT (or POST) with {"username": "..."} claims it. A name already taken comes
// back as 409 so a client can tell "pick another" from "that was malformed".
func handleUsername(users *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, ok := caller(w, r)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			username, err := users.Username(r.Context(), cred.PublicKey)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, Profile{
				PublicKey:   cred.PublicKey,
				Fingerprint: cred.Fingerprint(),
				Username:    username,
			})

		case http.MethodPut, http.MethodPost:
			var req struct {
				Username string `json:"username"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			username, err := ValidateUsername(req.Username)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			switch err := users.SetUsername(r.Context(), cred.PublicKey, username); {
			case errors.Is(err, ErrUsernameTaken):
				http.Error(w, err.Error(), http.StatusConflict)
				return
			case err != nil:
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, Profile{
				PublicKey:   cred.PublicKey,
				Fingerprint: cred.Fingerprint(),
				Username:    username,
			})

		default:
			methodNotAllowed(w, "GET, PUT")
		}
	}
}

// handleUsers searches the directory of named users: GET /api/users?q=<text>.
// An empty query lists everyone, which is what a client shows before anything
// has been typed. Only users who have chosen a name are listed — a key with no
// name has not asked to be findable.
func handleUsers(users *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, ok := caller(w, r)
		if !ok {
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return
		}

		found, err := users.SearchUsernames(r.Context(), r.URL.Query().Get("q"), cred.PublicKey)
		if err != nil {
			serverError(w, err)
			return
		}
		members, err := users.ChatMembers(r.Context(), cred.PublicKey)
		if err != nil {
			serverError(w, err)
			return
		}

		inChat := make(map[string]bool, len(members))
		for _, member := range members {
			inChat[strings.ToLower(member)] = true
		}

		entries := make([]DirectoryEntry, 0, len(found))
		for _, username := range found {
			entries = append(entries, DirectoryEntry{
				Username: username,
				InChat:   inChat[strings.ToLower(username)],
			})
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

// handleChat reads and edits the caller's chat: GET lists it, POST with
// {"username": "..."} adds someone found through /api/users, and
// DELETE /api/chat?username=<name> removes them. Adding is mutual, so both
// sides can talk the moment either one adds the other.
func handleChat(users *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, ok := caller(w, r)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			writeChat(r.Context(), w, users, cred)

		case http.MethodPost:
			var req struct {
				Username string `json:"username"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			if err := editChat(r.Context(), users, cred, req.Username, users.AddChatMember); err != nil {
				chatError(w, err)
				return
			}
			writeChat(r.Context(), w, users, cred)

		case http.MethodDelete:
			if err := editChat(r.Context(), users, cred, r.URL.Query().Get("username"),
				users.RemoveChatMember); err != nil {
				chatError(w, err)
				return
			}
			writeChat(r.Context(), w, users, cred)

		default:
			methodNotAllowed(w, "GET, POST, DELETE")
		}
	}
}

// editChat resolves a name to the key behind it and applies add or remove to
// it. Working in keys rather than names is what keeps a membership pointed at
// the same person after they rename themselves.
func editChat(ctx context.Context, users *UserStore, cred Credential, username string,
	edit func(context.Context, string, string) error) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return ErrUsernameRequired
	}
	memberKey, err := users.KeyForUsername(ctx, username)
	if err != nil {
		return err
	}
	if memberKey == "" {
		return ErrNoSuchUser
	}
	return edit(ctx, cred.PublicKey, memberKey)
}

func writeChat(ctx context.Context, w http.ResponseWriter, users *UserStore, cred Credential) {
	username, err := users.Username(ctx, cred.PublicKey)
	if err != nil {
		serverError(w, err)
		return
	}
	members, err := users.ChatMembers(ctx, cred.PublicKey)
	if err != nil {
		serverError(w, err)
		return
	}
	if members == nil {
		members = []string{}
	}
	writeJSON(w, http.StatusOK, Chat{Username: username, Members: members})
}

// chatError maps the ways editing a chat can fail onto status codes a client
// can act on: an unknown name is the user's typo, a malformed one is their
// mistake to correct, and anything else is the server's own failure — which is
// reported as such rather than quoting a database error back at the client.
func chatError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoSuchUser):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrUsernameRequired), errors.Is(err, ErrSelfMember):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		serverError(w, err)
	}
}

// serverError reports a failure the client cannot do anything about. The detail
// goes to the log, not to the response: it would be a database error verbatim.
func serverError(w http.ResponseWriter, err error) {
	log.Printf("api: %v", err)
	http.Error(w, "the server could not complete that request", http.StatusInternalServerError)
}

// caller returns the identity the middleware verified. A handler can only miss
// it by being mounted outside that middleware, which is a wiring bug.
func caller(w http.ResponseWriter, r *http.Request) (Credential, bool) {
	cred, ok := credentialFrom(r.Context())
	if !ok {
		http.Error(w, "no credential", http.StatusUnauthorized)
	}
	return cred, ok
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
