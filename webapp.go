// The React client, served at /webapp.
//
// webapp/dist is produced by the app's own CLI (`node cli.js build` in webapp/,
// or `npm run build`) and compiled into this binary, so the server has no
// runtime dependency on Node and serves the same files from any working
// directory. A change to the React sources therefore reaches a browser only
// after both a webapp build and a rebuild of this binary.
//
// Like "/", the route is unauthenticated: the app has to load before it can
// generate a key to sign with, and the shell carries no messages. Everything it
// then asks for goes through the signed, authorized /api routes, and a key that
// is not in authorizedUsers gets the app's 404 page.
package messageserver

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

// WebappFS holds the built React client. The all: prefix keeps files webpack
// writes with a leading dot or underscore, which the default patterns skip.
//
//go:embed all:webapp/dist
var WebappFS embed.FS

// webappRoot is where the app is mounted. It appears in three places that have
// to agree: here, webpack's publicPath, and the URL a person types.
const webappRoot = "/webapp"

// WebappHandler serves the built client, or explains itself if the build is
// missing — which is what a checkout that has never run the webapp CLI looks
// like.
func WebappHandler() http.Handler {
	dist, err := fs.Sub(WebappFS, "webapp/dist")
	if err != nil {
		log.Printf("webapp: %v", err)
		return notBuilt(err.Error())
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		log.Printf("webapp: dist/index.html is missing; /webapp will not serve the app")
		return notBuilt("dist/index.html is missing")
	}

	files := http.StripPrefix(webappRoot+"/", cacheControl(http.FileServerFS(dist)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "/webapp" is what a person types; everything below it is the app's
		// own assets, which are absolute because webpack's publicPath is.
		if r.URL.Path == webappRoot {
			http.Redirect(w, r, webappRoot+"/", http.StatusMovedPermanently)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// cacheControl lets the hashed bundle be cached forever and the page never: the
// file name changes with every build, so a cached page would be the one thing
// that could still point at a bundle that no longer exists.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path := r.URL.Path; strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".css") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func notBuilt(detail string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w,
			"the webapp has not been built ("+detail+").\n"+
				"Build it and rebuild the server:\n"+
				"    cd webapp && npm install && npm run build\n"+
				"    go build -o messageServer ./cmd/messageServer\n",
			http.StatusServiceUnavailable)
	})
}
