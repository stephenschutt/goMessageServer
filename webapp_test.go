package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The route a person types has to answer, and it has to answer with the app
// rather than a directory listing.
func TestWebappServesTheClient(t *testing.T) {
	handler := webappHandler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webapp", nil))
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/webapp/" {
		t.Fatalf("/webapp got %d to %q, want 301 to /webapp/", rec.Code, rec.Header().Get("Location"))
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webapp/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/webapp/ got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="root"`) && !strings.Contains(rec.Body.String(), "id=root") {
		t.Fatalf("/webapp/ did not serve the React shell:\n%s", rec.Body.String())
	}
}

// The bundle name is content-hashed, so a page built from one build and a
// bundle from another would leave the app dead in the browser with nothing in
// the server log. Checking that the embedded page's script is embedded too is
// what catches a half-copied dist/.
func TestWebappBundleIsEmbedded(t *testing.T) {
	rec := httptest.NewRecorder()
	webappHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webapp/", nil))

	script := regexp.MustCompile(`src=["']?/webapp/([^"' >]+)`).FindStringSubmatch(rec.Body.String())
	if script == nil {
		t.Fatalf("no bundle referenced by the page:\n%s", rec.Body.String())
	}
	if _, err := fs.Stat(webappFS, "webapp/dist/"+script[1]); err != nil {
		t.Fatalf("page asks for %s, which is not embedded: %v", script[1], err)
	}

	asset := httptest.NewRecorder()
	webappHandler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/webapp/"+script[1], nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("serving %s got %d, want 200", script[1], asset.Code)
	}
	if cache := asset.Header().Get("Cache-Control"); !strings.Contains(cache, "immutable") {
		t.Fatalf("hashed bundle is served with Cache-Control %q", cache)
	}
}
