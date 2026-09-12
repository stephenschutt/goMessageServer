# messageServer.go — fixes

## Architecture
- [ ] Fix the repo root package: `SophiaTest.go` and `test.go` both declare
      `func main()` in the same `package main` as `messageServer.go`, so
      `go build ./...` / `go vet ./...` currently fail with "main
      redeclared in this block". Move or delete these — they're unrelated
      scratch files, not part of the chat server.
- [ ] Add a `.gitignore` for the compiled binaries sitting in the repo
      root (`messageServer`, `SophiaTest`, `test`) so build output doesn't
      get committed.
- [ ] Split `messageServer.go` (457 lines) by responsibility instead of
      one file doing wiring + data model + handlers + embedded frontend:
      - `messageServer.go` — just `main()`: build store, mux, `*http.Server`.
      - `store.go` — `Message`, `ChatStore`, `Add`/`Since`.
      - `handlers.go` — `handleIndex`, `handleParticipants`,
        `handleMessages`, `writeJSON`.
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
- [ ] Bound `ChatStore.messages` growth (e.g. cap history length or add
      eviction) — currently grows forever for the life of the process
      (messageServer.go:28).
- [ ] Switch `ChatStore.mu` from `sync.Mutex` to `sync.RWMutex` so
      concurrent `Since` reads (polled every 1.5s per client) don't block
      each other (messageServer.go:27, :41).
- [ ] `Since` does a full linear scan of all messages on every poll
      (messageServer.go:41-51) — fine at toy scale, revisit if history
      grows large.
- [ ] No real authentication — any client can POST as "Alice" or "Bob" by
      naming them in the request body; `isParticipant` only checks the
      name matches, it doesn't bind identity to a session
      (messageServer.go:53-55, :111). Acceptable for a private/local toy,
      but flag before this goes anywhere less trusted.
