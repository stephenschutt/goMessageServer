// Command messageServer serves the chat in package messageserver: the JSON API,
// the plain browser client at "/", and the React one at "/webapp".
//
// It is deliberately thin. Everything it does beyond parsing flags is a call
// into the library at the repository root, which is what lets the suite in
// tests/ exercise the same code without going through a binary.
//
//	messageServer                          # serve on :8080
//	messageServer -addr 127.0.0.1:9000     # somewhere else
//	messageServer -pending                 # list keys waiting for approval
//	messageServer -authorize '<key>'       # admit one of them
//
// The connection string comes from DATABASE_URL, read from the file named by
// -env if that file exists and from the environment otherwise — which is how it
// runs both on a laptop with a .env and in a pod with a Secret.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	messageserver "goMessageServer"
	"goMessageServer/internal/database"
)

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

	mode := messageserver.ApplySchema
	if *strict {
		mode = messageserver.RequireSchema
	}

	ctx := context.Background()
	users, err := messageserver.OpenUserStore(ctx, connStr, mode)
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

	// The message log is three tables in the same database as the allow list,
	// so every replica of this server serves the same transcript.
	handler := messageserver.NewHandler(users, messageserver.NewChatStore(users))

	log.Printf("messageServer listening on http://localhost%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, handler))
}

// authorizeKey promotes a key an administrator has decided to trust. It accepts
// the same base64 SPKI spelling the clients display and send.
func authorizeKey(ctx context.Context, users *messageserver.UserStore, encoded string) error {
	cred, err := messageserver.ParseEncodedKey(encoded)
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
func printPending(ctx context.Context, users *messageserver.UserStore) error {
	keys, err := users.PendingKeys(ctx)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		fmt.Println("no keys waiting in unauthorizedUsers")
		return nil
	}
	for _, key := range keys {
		cred, err := messageserver.ParseEncodedKey(key)
		if err != nil {
			fmt.Printf("(unparseable)\t%s\n", key)
			continue
		}
		fmt.Printf("%s\t%s\n", cred.Fingerprint(), key)
	}
	return nil
}
