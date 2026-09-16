# messageServer.go — fixes

## Architecture
- [x] ~~Fix the repo root package: two files declaring `func main()`~~ —
      gone. REQ-010 went further: the root is now the importable library
      `messageserver`, the command lives in `cmd/messageServer/`, and the
      whole suite is in `tests/`.
- [ ] Add a `.gitignore` for the compiled binaries sitting in the repo
      root (`messageServer`, `SophiaTest`, `test`) so build output doesn't
      get committed.
- [x] ~~Split `messageServer.go` by responsibility~~ — done across
      `auth.go`, `chat.go`, `directory.go`, `messages.go`, `userdb.go`,
      `webapp.go`, with `messageServer.go` left holding `NewHandler` and the
      message routes.
- [ ] Move the embedded `indexHTML` string (messageServer.go:141-457, ~2/3
      of the file) out into real `web/index.html` + `web/style.css` +
      `web/app.js` files served via `go:embed` — gets syntax
      highlighting/linting on the frontend and free content-type handling
      via `http.FileServer(http.FS(...))` instead of the manual header set
      in `handleIndex` (messageServer.go:75).
- [ ] Once split, `loadtest/` is already a good model to mirror (own
      package, own focused `main`, doc comment) — no need for a full
      `cmd/`/`internal/` layout at this project's size.

## Should fix
- [ ] Add server timeouts: build an explicit `*http.Server` with `ReadTimeout`,
      `WriteTimeout`, `IdleTimeout`, `ReadHeaderTimeout` instead of bare
      `http.ListenAndServe` (messageServer.go:67) — avoids Slowloris-style
      hangs from slow/stalled clients.
- [ ] Add graceful shutdown: listen for SIGINT/SIGTERM and call
      `server.Shutdown(ctx)` instead of `log.Fatal(http.ListenAndServe(...))`
      (messageServer.go:67).
- [ ] Cap request body size on POST /api/messages with
      `http.MaxBytesReader` before `json.NewDecoder(r.Body).Decode(...)`
      (messageServer.go:105) — currently an arbitrarily large body is read
      into memory before the 2000-char text check runs.
- [ ] Check/log errors from `w.Write([]byte(indexHTML))`
      (messageServer.go:76) and `json.NewEncoder(w).Encode(v)`
      (messageServer.go:136) instead of discarding them.
- [ ] Make the listen address configurable (flag or env var) instead of
      hardcoded `":8080"` (messageServer.go:65).

## Worth considering
- [x] ~~Switch `ChatStore.mu` from `sync.Mutex` to `sync.RWMutex`~~ — moot:
      REQ-009 moved the log into the database (`messages.go`), so there is
      no mutex left. Concurrency is the connection pool's problem now.
- [x] ~~`Since` does a full linear scan of all messages on every poll~~ —
      it is an indexed query against `"Messages"` and `"messageRecipients"`,
      capped at `maxMessageBatch` rows per poll.
- [ ] Bound the growth of `"Messages"`. The old slice grew for the life of a
      process and was emptied by a restart; the table grows for the life of
      the database and is not. Wants a retention policy — delete rows past
      some age, or past some count per conversation — plus the matching
      cleanup of `"messageRecipients"`.
- [ ] No real authentication — any client can POST as "Alice" or "Bob" by
      naming them in the request body; `isParticipant` only checks the
      name matches, it doesn't bind identity to a session
      (messageServer.go:53-55, :111). Acceptable for a private/local toy,
      but flag before this goes anywhere less trusted.
