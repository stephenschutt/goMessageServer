// Package messageserver is a small group chat: a JSON API backed by a message
// log in the database, plus two browser clients styled like the iPhone Messages
// app — a plain one at "/" and a React one at "/webapp".
//
// An authorized client chooses a username for itself, searches the directory of
// other named users, and adds them to its chat; a message is then delivered to
// whoever is in the sender's chat when it is sent. See chat.go for those
// handlers and directory.go for the tables behind them.
//
// Every API request must be signed by a client's RSA key and that key must
// appear in the authorizedUsers table; see auth.go for the scheme and userdb.go
// for the database, and messages.go for the transcript itself. The tables
// themselves come from the migrations in internal/database.
//
// The command that wires this up and serves it is cmd/messageServer; the tests
// are in tests/, which is why the handlers below are reachable through
// NewHandler and NewAPIHandler rather than only from main.
package messageserver

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// templateFS holds the browser UI, compiled into the binary so the server can
// run from any working directory.
//
//go:embed templates/*.html
var templateFS embed.FS

// templates are parsed once at startup so a broken template fails fast rather
// than on the first request. index.html is the whole client: an iPhone-style
// Messages UI that polls the JSON API for new messages and posts new ones on send.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// maxMessageLen bounds one message, which is also what bounds the body the
// signing middleware has to buffer and hash.
const maxMessageLen = 2000

// NewHandler returns everything the server serves: the plain browser client at
// "/", the React one under "/webapp", and the signed JSON API under "/api/".
//
// The API is authorized; the two client shells are not, because each has to
// load before it can generate a key to sign with. Neither shell carries
// messages or names — everything they show comes from the API.
func NewHandler(users *UserStore, store *ChatStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	// One handler on both patterns: "/webapp" alone, and everything under it.
	webapp := WebappHandler()
	mux.Handle(webappRoot, webapp)
	mux.Handle(webappRoot+"/", webapp)
	mux.Handle("/api/", NewAPIHandler(users, store))
	return mux
}

// NewAPIHandler returns the JSON API alone, already behind Authenticate. It is
// separate from NewHandler so that a caller — the suite in tests/, or anything
// mounting this API somewhere else — can exercise the API without the two
// browser shells in front of it.
func NewAPIHandler(users *UserStore, store *ChatStore) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("/api/messages", handleMessages(store, users))
	api.HandleFunc("/api/identity", handleIdentity(users))
	api.HandleFunc("/api/username", handleUsername(users))
	api.HandleFunc("/api/users", handleUsers(users))
	api.HandleFunc("/api/chat", handleChat(users))
	return Authenticate(users, api)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "index.html", nil); err != nil {
		log.Printf("render index: %v", err)
	}
}

// handleMessages serves GET /api/messages?since=<id> for polling the messages
// the caller may read, and POST /api/messages with a JSON body {text} to send
// one to everybody in the caller's chat.
//
// The sender is the caller's username, taken from the verified key rather than
// from the body: a client can say what it likes, but it can only post as itself.
func handleMessages(store *ChatStore, users *UserStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, ok := caller(w, r)
		if !ok {
			return
		}

		switch r.Method {
		case http.MethodGet:
			since := 0
			if v := r.URL.Query().Get("since"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					http.Error(w, "invalid since", http.StatusBadRequest)
					return
				}
				since = n
			}
			messages, err := store.Since(r.Context(), since, cred.PublicKey)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, messages)

		case http.MethodPost:
			var req struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			req.Text = strings.TrimSpace(req.Text)
			if req.Text == "" {
				http.Error(w, "text is required", http.StatusBadRequest)
				return
			}
			if len(req.Text) > maxMessageLen {
				http.Error(w, "text too long", http.StatusBadRequest)
				return
			}

			sender, err := users.Username(r.Context(), cred.PublicKey)
			if err != nil {
				serverError(w, err)
				return
			}
			// Without a name there is nothing to label the message with, and
			// nobody could have added this key to their chat either.
			if sender == "" {
				http.Error(w, "choose a username before sending messages", http.StatusConflict)
				return
			}
			recipients, err := users.ChatMemberKeys(r.Context(), cred.PublicKey)
			if err != nil {
				serverError(w, err)
				return
			}

			msg, err := store.Add(r.Context(), cred.PublicKey, sender, req.Text, recipients)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, msg)

		default:
			methodNotAllowed(w, "GET, POST")
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
