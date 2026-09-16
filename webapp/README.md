# Messages — React web app

The third client for the `messageServer` in this repo, alongside the plain
browser client at `/` and the iPhone app in `mobile/iPhone`. The Go server
serves it at <http://localhost:8080/webapp>.

It does what the iOS app does: generates this browser's RSA key pair on the
first visit, signs every API request with it, lets you choose a username once
the key is authorized, and searches for other users to add to your chat.

## Building

```sh
cd webapp
npm install
npm run build          # or: node cli.js build
cd .. && go build -o messageServer ./cmd/messageServer
```

The build writes `dist/`, which the server embeds with `go:embed` — so a change
here only reaches a browser after both the webpack build and a rebuild of the Go
binary. That is also why `dist/` is committed: `go build` needs it to exist.

| Command | What it does |
| --- | --- |
| `node cli.js build` | minified production bundle into `dist/` |
| `node cli.js build --dev` | unminified, readable bundle |
| `node cli.js dev` | unminified build that rebuilds as you edit |
| `node cli.js clean` | delete `dist/` |
| `node cli.js help` | usage |

`npm run build`, `npm run dev` and `npm run clean` are wrappers around the same
CLI.

## The key, and the 404

On the first visit the app generates a 2048-bit RSA pair with WebCrypto and
stores both halves in `localStorage` (`messages.privateKey`, as PKCS#8, and
`messages.publicKey`, as SPKI — both base64). Only the public half is ever sent.
Every request carries `X-Public-Key`, `X-Timestamp`, `X-Nonce` and `X-Signature`
over the canonical string `auth.go` reconstructs.

Until that key is in `authorizedUsers`, every API call comes back 403 and the
app shows a **friendly 404** rather than the chat: to a browser the server has
not been told to trust, the app is not a page that exists. The 404 does show the
public key, because otherwise nobody could ever be let in:

```sh
go run ./cmd/messageServer -authorize '<the key from the 404 page>'
go run ./cmd/messageServer -pending    # or read it off the server instead
```

**Check again** on that page re-tries without a reload; the app also recovers on
its own within about five seconds of being authorized.

A private key in `localStorage` can be read by any script on this origin, which
is weaker than the non-extractable IndexedDB key the plain client at `/` uses.
That is the trade for storing the pair the way the iOS app stores its own (both
halves, re-importable, shown in the UI); the page is served from the same origin
as the API and loads no third-party script.

## Layout

| Path | What it is |
| --- | --- |
| `cli.js` | the build CLI described above |
| `webpack.config.js` | one config, production by default |
| `src/index.jsx` | mounts `<App/>` |
| `src/keys.js` | the RSA pair in `localStorage` |
| `src/api.js` | signed requests and the typed `ApiError` |
| `src/components/App.jsx` | key setup, polling, and which screen is showing |
| `src/components/Chat.jsx` | header, transcript, compose bar |
| `src/components/People.jsx` | username, directory search, who is in the chat |
| `src/components/NotFound.jsx` | the 404 an unauthorized browser sees |
| `src/styles.css` | the iPhone frame, shared in spirit with `templates/index.html` |
| `dist/` | build output, committed because the server embeds it |
