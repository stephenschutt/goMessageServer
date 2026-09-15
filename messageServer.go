// Command messageServer runs a small group chat: a JSON API backed by an
// in-memory message log, plus two browser clients styled like the iPhone
// Messages app — a plain one at "/" and a React one at "/webapp".
//
// An authorized client chooses a username for itself, searches the directory of
// other named users, and adds them to its chat; a message is then delivered to
// whoever is in the sender's chat when it is sent. See chat.go for those
// handlers and directory.go for the tables behind them.
//
// Every API request must be signed by a client's RSA key and that key must
// appear in the authorizedUsers table; see auth.go for the scheme and userdb.go
// for the database. The connection string comes from DATABASE_URL in .env, and
// the tables themselves come from the migrations in internal/database.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"goMessageServer/internal/database"
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

// Message is one line of chat. Sender is the username its author had when they
// sent it, so a later rename does not rewrite the transcript.
type Message struct {
	ID        int       `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`

	// senderKey and recipients are the delivery list: the public keys allowed
	// to read this message. They are unexported, and so never marshalled — a
	// client is told who wrote a message, not who else can see it.
	senderKey  string
	recipients []string
}

// visibleTo reports whether a public key may read this message. The recipients
// were fixed when the message was sent, so joining a chat does not hand someone
// the history from before they were in it, and leaving one does not erase what
// they were already shown.
func (m Message) visibleTo(publicKey string) bool {
	if m.senderKey == publicKey {
		return true
	}
	for _, key := range m.recipients {
		if key == publicKey {
			return true
		}
	}
	return false
}

type ChatStore struct {
	mu       sync.Mutex
	messages []Message
	nextID   int
}

// Add records a message from senderKey (posting as sender) addressed to
// recipients, the keys in that sender's chat.
func (s *ChatStore) Add(senderKey, sender, text string, recipients []string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	msg := Message{
		ID:         s.nextID,
		Sender:     sender,
		Text:       text,
		Timestamp:  time.Now(),
		senderKey:  senderKey,
		recipients: recipients,
	}
	s.messages = append(s.messages, msg)
	return msg
}

// Since returns the messages after id that viewerKey is allowed to read.
func (s *ChatStore) Since(id int, viewerKey string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0)
	for _, m := range s.messages {
		if m.ID > id && m.visibleTo(viewerKey) {
			out = append(out, m)
		}
	}
	return out
}

func main() {
	var (
		addr      = flag.String("addr", ":8080", "address to listen on")
		envFile   = flag.String("env", ".env", "file holding "+database.ConnectionStringVar)
		authorize = flag.String("authorize", "", "add a base64 public key to authorizedUsers and exit")
		pending   = flag.Bool("pending", false, "list the keys in unauthorizedUsers and exit")
		strict    = flag.Bool("require-schema", false,
			"fail instead of migrating if the schema is out of date (run ./migrate separately)")
	)
	flag.Parse()

	connStr, err := database.ConnectionString(*envFile)
	if err != nil {
		log.Fatal(err)
	}

	mode := ApplySchema
	if *strict {
		mode = RequireSchema
	}

	ctx := context.Background()
	users, err := OpenUserStore(ctx, connStr, mode)
	if err != nil {
		log.Fatalf("authorization database: %v", err)
	}
	defer users.Close()

	// Both administrative flags are one-shot: they touch the allow list and
	// exit rather than starting a server.
	if *authorize != "" {
		if err := authorizeKey(ctx, users, *authorize); err != nil {
			log.Fatalf("authorize: %v", err)
		}
		return
	}
	if *pending {
		if err := printPending(ctx, users); err != nil {
			log.Fatalf("pending: %v", err)
		}
		return
	}

	store := &ChatStore{}

	// The API is signed and authorized; the two client shells are not, because
	// each has to load before it can generate a key to sign with. Neither shell
	// carries messages or names — everything they show comes from the API.
	api := http.NewServeMux()
	api.HandleFunc("/api/messages", handleMessages(store, users))
	api.HandleFunc("/api/identity", handleIdentity(users))
	api.HandleFunc("/api/username", handleUsername(users))
	api.HandleFunc("/api/users", handleUsers(users))
	api.HandleFunc("/api/chat", handleChat(users))

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	// One handler on both patterns: "/webapp" alone, and everything under it.
	webapp := webappHandler()
	mux.Handle(webappRoot, webapp)
	mux.Handle(webappRoot+"/", webapp)
	mux.Handle("/api/", authenticate(users, api))

	log.Printf("messageServer listening on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

// authorizeKey promotes a key an administrator has decided to trust. It accepts
// the same base64 SPKI spelling the clients display and send.
func authorizeKey(ctx context.Context, users *UserStore, encoded string) error {
	cred, err := parseEncodedKey(encoded)
	if err != nil {
		return err
	}
	if err := users.Authorize(ctx, cred.PublicKey); err != nil {
		return err
	}
	log.Printf("authorized key %s", cred.Fingerprint())
	return nil
}

// printPending shows the keys that tried to connect and were turned away, so a
// new client can be approved by copying one into -authorize.
func printPending(ctx context.Context, users *UserStore) error {
	keys, err := users.PendingKeys(ctx)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		fmt.Println("no keys waiting in unauthorizedUsers")
		return nil
	}
	for _, key := range keys {
		cred, err := parseEncodedKey(key)
		if err != nil {
			fmt.Printf("(unparseable)\t%s\n", key)
			continue
		}
		fmt.Printf("%s\t%s\n", cred.Fingerprint(), key)
	}
	return nil
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
			writeJSON(w, http.StatusOK, store.Since(since, cred.PublicKey))

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

			msg := store.Add(cred.PublicKey, sender, req.Text, recipients)
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
