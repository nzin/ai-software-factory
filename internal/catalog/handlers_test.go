package catalog_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/catalog"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := catalog.Open("file:handlers_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	spec, err := loads.Analyzed(restapi.SwaggerJSON, "")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	api := operations.NewCatalogAPI(spec)
	catalog.Setup(api, catalog.NewService(store))

	srv := httptest.NewServer(api.Serve(nil))
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPFlow(t *testing.T) {
	srv := newTestServer(t)

	// health
	if body := get(t, srv.URL+"/healthz"); !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("health: %s", body)
	}

	// register
	put := do(t, http.MethodPut, srv.URL+"/v1/agents/planner",
		`{"name":"Planner","baseURL":"http://127.0.0.1:9101","skills":["planning"],"concurrency":2}`)
	if put != http.StatusOK {
		t.Fatalf("put status = %d", put)
	}

	// get -> card carries the model extension
	body := get(t, srv.URL+"/v1/agents/planner")
	var detail struct {
		AgentCard struct {
			Capabilities struct {
				Extensions []struct {
					URI    string         `json:"uri"`
					Params map[string]any `json:"params"`
				} `json:"extensions"`
			} `json:"capabilities"`
		} `json:"agentCard"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	exts := detail.AgentCard.Capabilities.Extensions
	if len(exts) != 1 || !strings.Contains(exts[0].URI, "ext/model") {
		t.Fatalf("model extension missing: %s", body)
	}
	if exts[0].Params["model"] != "claude-sonnet-5" { // modelext.Defaults fallback
		t.Fatalf("model = %v", exts[0].Params["model"])
	}

	// override the model
	do(t, http.MethodPatch, srv.URL+"/v1/agents/planner/model", `{"model":"claude-opus-5"}`)
	if m := get(t, srv.URL+"/v1/agents/planner/model"); !strings.Contains(m, "claude-opus-5") {
		t.Fatalf("override not applied: %s", m)
	}

	// 404
	if code := do(t, http.MethodGet, srv.URL+"/v1/agents/ghost", ""); code != http.StatusNotFound {
		t.Fatalf("ghost status = %d, want 404", code)
	}
}

func get(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func do(t *testing.T, method, url, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}
