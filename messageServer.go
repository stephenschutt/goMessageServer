// Command messageServer runs a tiny two-person chat: a JSON API backed by an
// in-memory message log, plus a browser UI styled like the iPhone Messages app.
package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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

// Participants is the fixed pair allowed to chat. Keeping it to two names
// (rather than open registration) matches the "two people" scope of this server.
var Participants = [2]string{"Alice", "Bob"}

type Message struct {
	ID        int       `json:"id"`
	Sender    string    `json:"sender"`
	Text      string    `json:"text"`
	Timestamp time.Time `json:"timestamp"`
}

type ChatStore struct {
	mu       sync.Mutex
	messages []Message
	nextID   int
}

func (s *ChatStore) Add(sender, text string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	msg := Message{ID: s.nextID, Sender: sender, Text: text, Timestamp: time.Now()}
	s.messages = append(s.messages, msg)
	return msg
}

func (s *ChatStore) Since(id int) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0)
	for _, m := range s.messages {
		if m.ID > id {
			out = append(out, m)
		}
	}
	return out
}

func isParticipant(name string) bool {
	return name == Participants[0] || name == Participants[1]
}

func main() {
	store := &ChatStore{}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/api/participants", handleParticipants)
	mux.HandleFunc("/api/messages", handleMessages(store))

	addr := ":8080"
	log.Printf("messageServer listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
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

func handleParticipants(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Participants)
}

// handleMessages serves GET /api/messages?since=<id>&user=<name> for polling
// new messages, and POST /api/messages with a JSON body {sender, text} to send one.
func handleMessages(store *ChatStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
			writeJSON(w, http.StatusOK, store.Since(since))

		case http.MethodPost:
			var req struct {
				Sender string `json:"sender"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			req.Sender = strings.TrimSpace(req.Sender)
			req.Text = strings.TrimSpace(req.Text)
			if !isParticipant(req.Sender) {
				http.Error(w, "unknown sender", http.StatusBadRequest)
				return
			}
			if req.Text == "" {
				http.Error(w, "text is required", http.StatusBadRequest)
				return
			}
			if len(req.Text) > 2000 {
				http.Error(w, "text too long", http.StatusBadRequest)
				return
			}
			msg := store.Add(req.Sender, req.Text)
			writeJSON(w, http.StatusCreated, msg)

		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
