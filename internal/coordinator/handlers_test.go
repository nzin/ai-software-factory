package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-openapi/loads"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/modelext"
	"github.com/nzin/ai-software-factory/internal/prd"
)

// fakeCatalog stands in for the real catalog service.
type fakeCatalog struct {
	agents  []agentkit.AgentInfo
	listErr error
	getErr  error
	gets    int
}

func (f *fakeCatalog) ListAgents(context.Context) ([]agentkit.AgentInfo, error) {
	return f.agents, f.listErr
}

func (f *fakeCatalog) GetAgentDetail(_ context.Context, role string) (*agentkit.AgentDetail, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, a := range f.agents {
		if a.Role == role {
			return &agentkit.AgentDetail{
				Info:  a,
				Model: modelext.Config{Provider: "anthropic", Model: "claude-sonnet-5", MaxTokens: 16000, Effort: "high"},
				Card:  map[string]any{"name": a.Name, "capabilities": map[string]any{"streaming": true}},
			}, nil
		}
	}
	return nil, fmt.Errorf("no agent %q", role)
}

// serve wires the real generated API around an orchestrator, so these tests
// exercise routing, binding and the handlers together.
func serve(t *testing.T, o *Orchestrator) *httptest.Server {
	t.Helper()
	spec, err := loads.Analyzed(restapi.SwaggerJSON, "")
	if err != nil {
		t.Fatal(err)
	}
	api := operations.NewCoordinatorAPI(spec)
	Setup(api, o)
	srv := httptest.NewServer(api.Serve(nil))
	t.Cleanup(srv.Close)
	return srv
}

func doJSON(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestListAgentsReadsThroughToTheCatalog(t *testing.T) {
	cat := &fakeCatalog{agents: []agentkit.AgentInfo{
		{Role: "planner", Name: "Planner", BaseURL: "http://127.0.0.1:9101", Transport: "JSONRPC", Skills: []string{"planning"}, Concurrency: 4, Enabled: true},
		{Role: "backend-developer", Name: "Backend Developer", BaseURL: "http://127.0.0.1:9103", Transport: "JSONRPC", Enabled: true},
	}}
	srv := serve(t, New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil), WithCatalog(cat)))

	var got []map[string]any
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents", nil, &got); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents", len(got))
	}
	// The fan-out must fill the model in, otherwise the table has an empty column.
	model, _ := got[0]["model"].(map[string]any)
	if model["model"] != "claude-sonnet-5" {
		t.Fatalf("model not filled in by the fan-out: %+v", got[0])
	}
	if cat.gets != 2 {
		t.Fatalf("fan-out made %d detail calls, want 2", cat.gets)
	}
}

func TestListAgentsDegradesWhenDetailFails(t *testing.T) {
	cat := &fakeCatalog{
		agents: []agentkit.AgentInfo{{Role: "planner", Name: "Planner", Enabled: true}},
		getErr: fmt.Errorf("catalog is busy"),
	}
	srv := serve(t, New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil), WithCatalog(cat)))

	var got []map[string]any
	// The roster still renders; only the model column goes blank.
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents", nil, &got); code != http.StatusOK {
		t.Fatalf("code = %d, want 200 despite the detail failure", code)
	}
	if len(got) != 1 || got[0]["role"] != "planner" {
		t.Fatalf("roster lost: %+v", got)
	}
}

func TestAgentEndpointsWithoutACatalog(t *testing.T) {
	srv := serve(t, New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil)))

	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("list code = %d, want 503", code)
	}
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents/planner", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("detail code = %d, want 503", code)
	}
}

func TestGetAgentDetailReturnsTheCard(t *testing.T) {
	cat := &fakeCatalog{agents: []agentkit.AgentInfo{{Role: "planner", Name: "Planner", Enabled: true}}}
	srv := serve(t, New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil), WithCatalog(cat)))

	var got map[string]any
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents/planner", nil, &got); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	card, _ := got["agentCard"].(map[string]any)
	if card["name"] != "Planner" {
		t.Fatalf("agentCard missing: %+v", got)
	}
	info, _ := got["info"].(map[string]any)
	if info["role"] != "planner" {
		t.Fatalf("info missing: %+v", got)
	}

	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/agents/nope", nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown role code = %d, want 404", code)
	}
}

func TestResumeAndReviewReject409FromTheWrongStatus(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	srv := serve(t, o)

	var run map[string]any
	code := doJSON(t, http.MethodPost, srv.URL+"/v1/prd",
		map[string]any{"prd": map[string]any{"title": "X"}}, &run)
	if code != http.StatusAccepted {
		t.Fatalf("submit code = %d, want 202", code)
	}
	id, _ := run["id"].(string)
	waitTerminal(t, o, id) // ends `done`

	if code := doJSON(t, http.MethodPost, srv.URL+"/v1/runs/"+id+"/resume",
		map[string]any{"iterationBudget": 5}, nil); code != http.StatusConflict {
		t.Fatalf("resume code = %d, want 409", code)
	}
	if code := doJSON(t, http.MethodPost, srv.URL+"/v1/runs/"+id+"/review",
		map[string]any{"decision": "accept"}, nil); code != http.StatusConflict {
		t.Fatalf("review code = %d, want 409", code)
	}
}

func TestGetRunExposesEventsAndSteps(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	srv := serve(t, o)

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitTerminal(t, o, run.ID)

	var got map[string]any
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/runs/"+run.ID, nil, &got); code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	events, _ := got["events"].([]any)
	if len(events) == 0 {
		t.Fatalf("run detail carries no events: %+v", got)
	}
	first, _ := events[0].(map[string]any)
	if first["kind"] != EventSubmitted {
		t.Fatalf("first event = %v, want submitted", first["kind"])
	}
	tasks, _ := got["tasks"].([]any)
	if len(tasks) == 0 {
		t.Fatal("run detail carries no tasks")
	}
}
