# Messages — iPhone client

A native SwiftUI app for the `messageServer` in this repo. It is a thin front end
over the server's JSON API: each install names itself, adds the people it wants
to talk to, and any number of installs pointed at the same server can then chat.

There is no device-to-device channel: every message goes through the Go server's
message log in the database, and each client polls `GET /api/messages?since=<id>`
every 1.5s for anything newer than the highest id it has seen — the same protocol the browser UI
in `templates/index.html` uses. The two clients can therefore be any mix of apps
and browsers.

Every request is authenticated. The app generates an RSA key pair the first time
it launches and keeps both halves in a local SQLite database; only the public
half is ever sent, and the server will not talk to a key it has not been told to
trust. See [Authorizing a device](#authorizing-a-device).

## Requirements

Xcode 15 or later (iOS 17 deployment target, iPhone only).

## Running two clients

1. Tell the server where its authorization database lives, then start it from
   the repo root:

   ```sh
   cp .env.example .env          # DATABASE_URL, SQLite by default
   go run ./migrate              # create the authorization tables
   go run ./cmd/messageServer    # listens on :8080
   ```

   `go run ./migrate -status` shows what has been applied. Running it is
   optional for a quick start — the server applies pending migrations itself
   unless you pass `-require-schema` — but it is the way to set up a database
   whose user is not allowed to create tables.

2. Open `MessagesApp.xcodeproj` and run the app on a simulator. On first launch
   it generates this device's key, then shows "This device isn't authorized yet"
   until you approve it — see [Authorizing a device](#authorizing-a-device).

3. Run a second client, any of:
   - another simulator (`Product ▸ Destination`, pick a different iPhone, run again),
   - a browser at <http://localhost:8080>, or
   - the React app at <http://localhost:8080/webapp> (see `webapp/README.md`).

4. Give each client a username and put them in each other's chat — see
   [Usernames and people](#usernames-and-people).

Messages sent from either client show up on the other within about a second.

## Usernames and people

Tap the **person.2 icon in the top left** for the People screen. It has the two
things a new client needs, in the order it needs them:

- **Your username** — the name everyone else searches for. It belongs to this
  device's key rather than to the app, so it is the same name on any server that
  has authorized that key, and the server refuses a name another key already
  holds (case ignored: `alice` and `Alice` are one name).
- **Add people** — searches the directory of everyone who has chosen a username.
  **Add** puts the two of you in each other's chat, so the person added can
  answer without having to add you back. Swipe a name in **In your chat** to
  remove them, which removes you from their chat as well.

A message goes to everyone in the sender's chat as it stands when they send it,
and the transcript shows who wrote each one once there is more than one other
person in the chat. Joining later does not reveal what was said before, and
leaving does not take back what has already been delivered.

Choosing a username needs an authorized key, so a device that has not been
approved yet is told that instead of being offered a form that could only fail —
see [Authorizing a device](#authorizing-a-device).

## Authorizing a device

The server only serves `/api/...` to a key listed in `authorizedUsers`, so each
client has to be approved once.

1. In the app, tap the person icon ▸ **This device** ▸ **Copy public key**.
   (A device that has not been approved also shows a banner linking straight
   there.)

2. Add it on the server:

   ```sh
   go run ./cmd/messageServer -authorize '<paste the key>'
   ```

   Anything that tried and failed is already recorded, so you can skip the copy
   and read the key off the server instead:

   ```sh
   go run ./cmd/messageServer -pending
   ```

   which prints a short fingerprint and the full key for every device waiting in
   `unauthorizedUsers`.

The app picks the change up on its next poll — no restart. **Create a new key**
in Settings discards the device's identity and starts this over: the new key is
unauthorized and unnamed, so it has to be approved again and given a username
again.

### How it works

The private key never leaves the device. Because a public key is not a secret,
presenting one alone would prove nothing, so each request also carries a
signature over its own method, path, query, timestamp, nonce and body hash:

| Header | What it carries |
| --- | --- |
| `X-Public-Key` | base64 DER SPKI of the device's RSA public key |
| `X-Timestamp` | unix seconds; more than 5 minutes out and the request is refused |
| `X-Nonce` | random per request, so a captured one cannot be replayed |
| `X-Signature` | RSA PKCS#1 v1.5 over SHA-256 of the canonical string |

The server checks the signature first and the allow list second, which is why a
key can only land in `unauthorizedUsers` if it really is the caller's own.

`/` is the one unauthenticated route: it serves the browser shell, which has to
load before it can generate a key to sign with, and it carries no messages.

## Which server it talks to

Out of the box the app points at the deployed one:

```
https://messages.schuttsm.com
```

That is the EKS cluster in `infra/terraform`, behind an ALB holding a
certificate ACM issued for the name — so App Transport Security accepts it with
no exemption, and nothing needs configuring to run the app on a real phone.

The address is still editable at Settings (the person icon, top right) ▸
**Server**, which is how you point it at a server on your own machine. A device
upgrading from an older build has `http://localhost:8080` saved in
UserDefaults; `ChatClient` treats that specific value as "never chosen" and
moves it to the new default, so only an address you actually typed survives.

The key still has to be authorized, wherever the server runs:

```sh
pod=$(kubectl -n messageserver get po -l app.kubernetes.io/name=messageserver -o name | head -1)
kubectl -n messageserver exec "$pod" -- messageServer -pending
kubectl -n messageserver exec "$pod" -- messageServer -authorize '<key>'
```

## Running against a server on your Mac

`localhost` means the phone itself, so set the server address to your Mac's
address on the same Wi-Fi network — Settings (the person icon, top right) ▸
**Server**:

```sh
ipconfig getifaddr en0     # e.g. 192.168.1.20  ->  http://192.168.1.20:8080
```

The server binds all interfaces on `:8080`, so no server change is needed. iOS
will ask for local network permission the first time.

The app signs with the Security framework, so plain HTTP over the LAN works.
The *browser* client is the exception: it signs with WebCrypto, which browsers
only expose in a secure context, so `http://192.168.x.x:8080` shows "Signing
needs a secure context" where `http://localhost:8080` works. Use the app, or
localhost, on anything but https.

App Transport Security is configured in `Resources/Info.plist` to allow plain
HTTP on the local network (`NSAllowsLocalNetworking`). If your Mac is reachable
only by an address that ATS still rejects, set `NSAllowsArbitraryLoads` to
`true` there — it is a development-only toy server, not something to ship.

## Before running on device

Set your signing team on the `MessagesApp` target (Signing & Capabilities) and
change `PRODUCT_BUNDLE_IDENTIFIER` from the placeholder
`com.example.goMessageServer.MessagesApp` to something you own.

## Layout

| Path | What it is |
| --- | --- |
| `Sources/MessagesApp.swift` | `@main` entry point; owns the shared `ChatClient` |
| `Sources/ChatClient.swift` | All server I/O: profile, chat roster, polling, sending |
| `Sources/Message.swift` | The `Message` model and Go RFC 3339 timestamp parsing |
| `Sources/Directory.swift` | The profile, search-result and chat models |
| `Sources/ChatView.swift` | Conversation screen: header, transcript, compose bar |
| `Sources/MessageBubble.swift` | Bubble and time-separator views |
| `Sources/PeopleView.swift` | Username, directory search, and who is in the chat |
| `Sources/SettingsView.swift` | Server address, status, and this device's key |
| `Sources/KeyStore.swift` | The RSA key pair and the `settings` SQLite table |
| `Resources/Info.plist` | Bundle config and the ATS exemption |
