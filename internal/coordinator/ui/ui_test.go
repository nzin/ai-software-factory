package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apiHandler stands in for the generated go-swagger router.
func apiHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api":"` + r.URL.Path + `"}`))
	})
}

// distDir writes a minimal built SPA and points the package at it.
func distDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "index.html"), "<!doctype html><div id=app></div>")
	write(t, filepath.Join(dir, "assets", "index-abc123.js"), "console.log('spa')")
	t.Cleanup(func() { SetDir("") })
	SetDir(dir)
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, h http.Handler, method, path, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const html = "text/html,application/xhtml+xml"

func TestServesBuiltAssets(t *testing.T) {
	distDir(t)
	h := Middleware(apiHandler())

	res := get(t, h, http.MethodGet, "/assets/index-abc123.js", html)
	if res.Code != http.StatusOK {
		t.Fatalf("asset: code = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), "console.log") {
		t.Fatalf("asset body = %q", res.Body.String())
	}

	// "/" resolves to a directory, which negroni.Static answers with IndexFile.
	if res := get(t, h, http.MethodGet, "/", html); res.Code != http.StatusOK ||
		!strings.Contains(res.Body.String(), "id=app") {
		t.Fatalf("index: code=%d body=%q", res.Code, res.Body.String())
	}
}

func TestSPAFallbackForDeepLinks(t *testing.T) {
	distDir(t)
	h := Middleware(apiHandler())

	// vue-router history mode: no such file, must still get the shell back
	// rather than the API's 404.
	for _, path := range []string{"/runs/abc-123", "/agents/backend-developer", "/submit"} {
		res := get(t, h, http.MethodGet, path, html)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", path, res.Code)
		}
		if !strings.Contains(res.Body.String(), "id=app") {
			t.Fatalf("%s: did not serve index.html, got %q", path, res.Body.String())
		}
		if cc := res.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("%s: Cache-Control = %q, want no-cache", path, cc)
		}
	}
}

func TestAPIIsNeverShadowed(t *testing.T) {
	distDir(t)
	h := Middleware(apiHandler())

	for _, path := range []string{"/v1/runs", "/v1/runs/abc/approve", "/healthz", "/swagger.json"} {
		res := get(t, h, http.MethodGet, path, html)
		if !strings.Contains(res.Body.String(), `"api"`) {
			t.Fatalf("%s reached the SPA instead of the API: %q", path, res.Body.String())
		}
	}
}

func TestNonHTMLAndWritesFallThrough(t *testing.T) {
	distDir(t)
	h := Middleware(apiHandler())

	// An XHR wants a real 404 from the API, not an HTML shell.
	if res := get(t, h, http.MethodGet, "/runs/abc", "application/json"); !strings.Contains(res.Body.String(), `"api"`) {
		t.Fatalf("json Accept got the SPA shell: %q", res.Body.String())
	}
	// A POST must never be answered with index.html.
	if res := get(t, h, http.MethodPost, "/runs/abc", html); !strings.Contains(res.Body.String(), `"api"`) {
		t.Fatalf("POST got the SPA shell: %q", res.Body.String())
	}
}

func TestNoSPAConfigured(t *testing.T) {
	// http.Dir("") resolves to the process working directory, so an unusable dir
	// must disable static serving entirely rather than exposing the repo.
	cases := map[string]func(t *testing.T) string{
		"missing dir":     func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
		"empty string":    func(t *testing.T) string { return "" },
		"dir without SPA": func(t *testing.T) string { return t.TempDir() },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(func() { SetDir("") })
			SetDir(mk(t))
			if Dir() != "" {
				t.Fatalf("Dir() = %q, want empty", Dir())
			}
			h := Middleware(apiHandler())
			res := get(t, h, http.MethodGet, "/go.mod", html)
			if !strings.Contains(res.Body.String(), `"api"`) {
				t.Fatalf("served something other than the API: %q", res.Body.String())
			}
		})
	}
}

func TestRecoveryKeepsServing(t *testing.T) {
	distDir(t)
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := Middleware(panicky)

	res := get(t, h, http.MethodGet, "/v1/runs", "application/json")
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("panicking handler: code = %d, want 500", res.Code)
	}
}
