// Package ui serves the built Vue SPA (browser/asf-ui/dist) alongside the
// coordinator's generated REST API, from the same origin — which is what lets
// the browser reach both the runs API and the catalog read-through without CORS.
//
// negroni.Static does the file serving, but its IndexFile only applies when the
// request path resolves to a *directory*: a deep link like /runs/abc-123 misses,
// falls through to the go-swagger router and 404s. SPAFallback closes that gap
// for vue-router's history mode.
package ui

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/urfave/negroni/v3"
)

// apiPrefixes are owned by the generated REST API and must never be shadowed by
// the SPA fallback.
var apiPrefixes = []string{"/v1/", "/healthz", "/docs", "/swagger.json"}

var (
	mu  sync.RWMutex
	dir string
)

// SetDir points the static handler at a built SPA. It is a no-op unless the
// directory exists and actually holds an index.html, so a coordinator built and
// run without the Node toolchain simply serves the API.
//
// Call it before restapi.Server.ConfigureAPI().
func SetDir(path string) {
	mu.Lock()
	defer mu.Unlock()
	dir = ""
	if path == "" {
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		log.Printf("coordinator ui: bad --ui-dir %q: %v — serving the API only", path, err)
		return
	}
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
		log.Printf("coordinator ui: no index.html in %s — serving the API only (run `make build_ui`)", abs)
		return
	}
	dir = abs
	log.Printf("coordinator ui: serving %s at /", abs)
}

// Dir returns the configured SPA directory, or "" when none is usable.
//
// The empty case matters: http.Dir("") resolves relative to the process working
// directory, so installing negroni.Static with it would happily serve go.mod and
// friends. Callers must skip the static middleware entirely when this is "".
func Dir() string {
	mu.RLock()
	defer mu.RUnlock()
	return dir
}

// SPAFallback serves index.html for client-side routes that reached the end of
// the static handler without matching a file. Everything else falls through to
// the API.
func SPAFallback(root string) negroni.Handler {
	index := filepath.Join(root, "index.html")
	return negroni.HandlerFunc(func(w http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
		if !isSPARequest(r) {
			next(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, index)
	})
}

// isSPARequest reports whether a request that missed every static file should be
// answered with the SPA shell rather than handed to the API.
func isSPARequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	for _, p := range apiPrefixes {
		if r.URL.Path == p || strings.HasPrefix(r.URL.Path, p) {
			return false
		}
	}
	// XHR/JSON callers want a real 404, not an HTML shell.
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// Middleware wraps the API handler with the recovery, static-file and SPA
// fallback stack. With no built SPA it still adds panic recovery.
func Middleware(api http.Handler) http.Handler {
	n := negroni.New()
	n.Use(negroni.NewRecovery())
	if root := Dir(); root != "" {
		n.Use(&negroni.Static{
			Dir:       http.Dir(root),
			IndexFile: "index.html",
		})
		n.Use(SPAFallback(root))
	}
	n.UseHandler(api)
	return n
}
