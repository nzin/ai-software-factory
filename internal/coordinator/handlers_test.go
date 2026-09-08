package coordinator

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/nzin/ai-software-factory/internal/factory"
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

func TestResumeFailedRunWithCommentOverHTTP(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil), WithDefaults(100, 0))
	srv := serve(t, o)

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitTerminal(t, o, run.ID)
	if failed.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", failed.Status)
	}

	var got map[string]any
	code := doJSON(t, http.MethodPost, srv.URL+"/v1/runs/"+run.ID+"/resume",
		map[string]any{"comment": "add // #nosec G404 to the RNG line"}, &got)
	if code != http.StatusOK {
		t.Fatalf("resume code = %d, want 200", code)
	}
	findings, _ := got["findings"].([]any)
	found := false
	for _, f := range findings {
		m, _ := f.(map[string]any)
		if m["source"] == "human" && m["title"] == "add // #nosec G404 to the RNG line" {
			found = true
		}
	}
	if !found {
		t.Fatalf("human finding not in response: %+v", findings)
	}
	waitTerminal(t, o, run.ID)
}

func TestDeleteRunOverHTTP(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil), WithDefaults(100, 0))
	srv := serve(t, o)

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s := waitTerminal(t, o, run.ID).Status; s != StatusFailed {
		t.Fatalf("status = %s, want failed", s)
	}

	if code := doJSON(t, http.MethodDelete, srv.URL+"/v1/runs/"+run.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete code = %d, want 204", code)
	}
	if code := doJSON(t, http.MethodGet, srv.URL+"/v1/runs/"+run.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("get-after-delete code = %d, want 404", code)
	}
	var list []any
	doJSON(t, http.MethodGet, srv.URL+"/v1/runs", nil, &list)
	if len(list) != 0 {
		t.Fatalf("run still listed: %+v", list)
	}
	if code := doJSON(t, http.MethodDelete, srv.URL+"/v1/runs/"+run.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("second delete code = %d, want 404", code)
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

func ghSign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func postWebhook(t *testing.T, url, event, sig string, body []byte) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestGithubWebhookAuth(t *testing.T) {
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil))
	srv := serve(t, o)
	body := []byte(`{"action":"submitted"}`)

	t.Setenv("GITHUB_WEBHOOK_SECRET", "")
	if code := postWebhook(t, srv.URL, "pull_request_review", "", body); code != http.StatusServiceUnavailable {
		t.Fatalf("no secret: code = %d, want 503", code)
	}

	t.Setenv("GITHUB_WEBHOOK_SECRET", "topsecret")
	if code := postWebhook(t, srv.URL, "pull_request_review", "sha256=deadbeef", body); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: code = %d, want 401", code)
	}
	if code := postWebhook(t, srv.URL, "ping", ghSign("topsecret", body), body); code != http.StatusAccepted {
		t.Fatalf("valid ping: code = %d, want 202", code)
	}
}

func TestGithubWebhookRoutesAChangeRequest(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "topsecret")
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil))
	srv := serve(t, o)

	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	driveToPRReady(t, o, run.ID)
	// give it a PR URL so the webhook can match it
	stored, _, _ := o.store.Get(run.ID)
	stored.Status = StatusPROpen
	stored.PRURL = "https://github.com/me/repo/pull/9"
	_ = o.store.Put(stored)

	payload := []byte(`{
	  "action":"submitted",
	  "review":{"body":"handle the empty case","state":"changes_requested","user":{"login":"octo"}},
	  "pull_request":{"html_url":"https://github.com/me/repo/pull/9","head":{"ref":"asf/run-x"}},
	  "repository":{"full_name":"me/repo"}
	}`)
	if code := postWebhook(t, srv.URL, "pull_request_review", ghSign("topsecret", payload), payload); code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", code)
	}

	// The prReady fake approves every stage, so the re-entered run races back
	// through the pipeline; assert on the outcome, not a transient stage.
	got := waitTerminal(t, o, run.ID)
	var human bool
	var reentered bool
	for _, f := range got.Findings {
		if f.Source == "human" && f.Title == "handle the empty case" {
			human = true
		}
	}
	for _, e := range got.Events {
		if e.Kind == EventReviewChanges && e.Actor == ActorHuman {
			reentered = true
		}
	}
	if !human {
		t.Fatalf("no human finding from the webhook: %+v", got.Findings)
	}
	if !reentered {
		t.Fatalf("webhook did not drive a review re-entry: %v", kinds(got))
	}
	if got.Attempts[factory.RoleBackendDeveloper] != 1 {
		t.Fatalf("expected one backend fix attempt, got %v", got.Attempts)
	}
}
