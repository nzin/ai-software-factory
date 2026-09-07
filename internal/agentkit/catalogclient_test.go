package agentkit_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/catalog"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// liveCatalog stands up the real catalog service over an in-memory store, so the
// client is exercised against the same handlers production uses.
func liveCatalog(t *testing.T) *agentkit.CatalogClient {
	t.Helper()
	store, err := catalog.Open("file:catalogclient_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	spec, err := loads.Analyzed(restapi.SwaggerJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	api := operations.NewCatalogAPI(spec)
	catalog.Setup(api, catalog.NewService(store))

	srv := httptest.NewServer(api.Serve(nil))
	t.Cleanup(srv.Close)

	cc, err := agentkit.NewCatalogClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return cc
}

func TestListAndGetAgentDetail(t *testing.T) {
	ctx := context.Background()
	cc := liveCatalog(t)

	if agents, err := cc.ListAgents(ctx); err != nil || len(agents) != 0 {
		t.Fatalf("empty catalog: agents=%v err=%v", agents, err)
	}

	err := cc.Register(ctx, agentkit.Registration{
		Role:        "backend-developer",
		Name:        "Backend Developer",
		Description: "Writes Go services",
		BaseURL:     "http://127.0.0.1:9103",
		Skills:      []string{"golang", "rest-api"},
		Concurrency: 4,
		DefaultModel: modelext.Config{
			Provider: "anthropic", Model: "claude-sonnet-5", MaxTokens: 64000, Effort: "low",
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	agents, err := cc.ListAgents(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents", len(agents))
	}
	got := agents[0]
	if got.Role != "backend-developer" || got.Name != "Backend Developer" {
		t.Fatalf("registration lost: %+v", got)
	}
	if len(got.Skills) != 2 || !got.Enabled || got.Concurrency != 4 {
		t.Fatalf("registration fields lost: %+v", got)
	}
	if got.BaseURL != "http://127.0.0.1:9103" || got.Transport != "JSONRPC" {
		t.Fatalf("transport/baseURL lost: %+v", got)
	}

	// listAgents on the catalog returns registrations only; the model and the
	// assembled card come from the per-role detail the UI fans out to.
	detail, err := cc.GetAgentDetail(ctx, "backend-developer")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Model.Model != "claude-sonnet-5" || detail.Model.MaxTokens != 64000 || detail.Model.Effort != "low" {
		t.Fatalf("model config lost: %+v", detail.Model)
	}
	if detail.Card == nil {
		t.Fatal("no AgentCard in the detail")
	}
	if detail.Card["name"] != "Backend Developer" {
		t.Fatalf("card = %+v", detail.Card)
	}
	caps, _ := detail.Card["capabilities"].(map[string]any)
	exts, _ := caps["extensions"].([]any)
	if len(exts) == 0 {
		t.Fatalf("card carries no model extension: %+v", caps)
	}
	ext, _ := exts[0].(map[string]any)
	if ext["uri"] != modelext.URI {
		t.Fatalf("extension uri = %v, want %v", ext["uri"], modelext.URI)
	}
}

func TestGetAgentBaseURLAndMissingAgent(t *testing.T) {
	ctx := context.Background()
	cc := liveCatalog(t)

	if err := cc.Register(ctx, agentkit.Registration{
		Role: "planner", Name: "Planner", BaseURL: "http://127.0.0.1:9101",
	}); err != nil {
		t.Fatal(err)
	}

	url, err := cc.GetAgentBaseURL(ctx, "planner")
	if err != nil {
		t.Fatalf("base url: %v", err)
	}
	if url != "http://127.0.0.1:9101" {
		t.Fatalf("base url = %q", url)
	}

	if _, err := cc.GetAgentDetail(ctx, "nobody"); err == nil {
		t.Fatal("detail for an unknown role should fail")
	}
}
