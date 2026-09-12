package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/nzin/ai-software-factory/internal/modelext"
)

func TestNewClampsMaxTokens(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int64
	}{
		{"over the ceiling is capped", 256_000, maxOutputTokens},
		{"at the ceiling is kept", maxOutputTokens, maxOutputTokens},
		{"under the ceiling is kept", 64_000, 64_000},
		{"zero falls back to the default", 0, 16_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(modelext.Config{Model: "claude-sonnet-5", MaxTokens: tc.in})
			if got := c.Config().MaxTokens; got != tc.want {
				t.Fatalf("MaxTokens = %d, want %d", got, tc.want)
			}
			if got := c.maxTokens(); got != tc.want {
				t.Fatalf("maxTokens() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestParamsWebSearchTool(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   bool
	}{
		{"no params", nil, false},
		{"tools missing web_search", map[string]any{"tools": []any{"other"}}, false},
		{"tools as []any", map[string]any{"tools": []any{"web_search"}}, true},
		{"tools as []string", map[string]any{"tools": []string{"web_search"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New(modelext.Config{Model: "claude-sonnet-5", Params: tc.params})
			params := c.params("sys", "user")

			got := false
			for _, tool := range params.Tools {
				if tool.OfWebSearchTool20250305 != nil {
					got = true
				}
			}
			if got != tc.want {
				t.Fatalf("web_search tool present = %v, want %v", got, tc.want)
			}
			if !tc.want && len(params.Tools) != 0 {
				t.Fatalf("params.Tools = %v, want empty", params.Tools)
			}
		})
	}
}

func TestCompleteWithImagesBlockOrder(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	c := testAgenticClient(server)

	out, err := c.CompleteWithImages(context.Background(), "sys", "compare these",
		[]ImageInput{{MediaType: "image/png", Data: []byte("fakepngbytes")}})
	if err != nil {
		t.Fatalf("CompleteWithImages: %v", err)
	}
	if out != "ok" {
		t.Fatalf("out = %q, want %q", out, "ok")
	}

	messages, _ := gotBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v, want 1 user turn", messages)
	}
	content, _ := messages[0].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2 (image then text)", len(content))
	}
	if content[0].(map[string]any)["type"] != "image" {
		t.Fatalf("first block type = %v, want image", content[0].(map[string]any)["type"])
	}
	if content[1].(map[string]any)["type"] != "text" {
		t.Fatalf("last block type = %v, want text", content[1].(map[string]any)["type"])
	}
}

func TestHasToolIgnoresOtherKeys(t *testing.T) {
	c := New(modelext.Config{Model: "claude-sonnet-5", Params: map[string]any{"foo": "bar"}})
	if c.hasTool("web_search") {
		t.Fatal("hasTool(web_search) = true with no tools key")
	}
}

// scriptedMessagesServer scripts POST /v1/messages: each call in order returns
// the next body in bodies; calling past the end repeats the last one.
func scriptedMessagesServer(t *testing.T, bodies []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotImplemented)
			return
		}
		n := int(calls.Add(1)) - 1
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[n]))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func testAgenticClient(server *httptest.Server) *Client {
	return New(modelext.Config{Model: "claude-sonnet-5"},
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
}

var echoTool = Tool{
	Name:        "echo",
	Description: "echoes its input back",
	Properties:  map[string]any{"msg": map[string]any{"type": "string"}},
	Required:    []string{"msg"},
}

func TestCompleteAgenticRunsToolThenReturnsFinalText(t *testing.T) {
	server, calls := scriptedMessagesServer(t, []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"echo","input":{"msg":"hi"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
	})
	c := testAgenticClient(server)

	var gotName string
	var gotInput json.RawMessage
	exec := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		gotName, gotInput = name, input
		return "echoed: hi", nil
	}

	out, err := c.CompleteAgentic(context.Background(), "sys", "user", []Tool{echoTool}, exec, 4)
	if err != nil {
		t.Fatalf("CompleteAgentic: %v", err)
	}
	if out != "done" {
		t.Fatalf("out = %q, want %q", out, "done")
	}
	if gotName != "echo" {
		t.Fatalf("tool exec called with name %q, want %q", gotName, "echo")
	}
	if !strings.Contains(string(gotInput), `"msg":"hi"`) {
		t.Fatalf("tool exec input = %s, want it to contain msg:hi", gotInput)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls to /v1/messages (one tool turn + one final), got %d", calls.Load())
	}
}

// TestCompleteAgenticWithdrawsToolsOnFinalTurn verifies the last allowed turn
// sends no "tools" field — a real Claude API then cannot answer with
// stop_reason=tool_use, so a model still investigating on its last turn is
// forced to give a text answer instead of the whole dispatch hard-failing
// with "did not finish within N turns" (see CompleteAgentic's doc comment).
func TestCompleteAgenticWithdrawsToolsOnFinalTurn(t *testing.T) {
	bodies := []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"echo","input":{"msg":"hi"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"final answer"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	var reqBodies []map[string]any
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		reqBodies = append(reqBodies, body)
		n := int(calls.Add(1)) - 1
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodies[n]))
	}))
	t.Cleanup(server.Close)
	c := testAgenticClient(server)

	exec := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "ok", nil
	}

	out, err := c.CompleteAgentic(context.Background(), "sys", "user", []Tool{echoTool}, exec, 2)
	if err != nil {
		t.Fatalf("CompleteAgentic: %v", err)
	}
	if out != "final answer" {
		t.Fatalf("out = %q, want %q", out, "final answer")
	}
	if len(reqBodies) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(reqBodies))
	}
	if _, ok := reqBodies[0]["tools"]; !ok {
		t.Fatal("first request should still offer tools")
	}
	if _, ok := reqBodies[1]["tools"]; ok {
		t.Fatalf("last request should withdraw tools, got: %v", reqBodies[1]["tools"])
	}
}

func TestCompleteAgenticStopsAtMaxTurns(t *testing.T) {
	// Always answers tool_use — a model that never stops calling tools must
	// not loop forever; CompleteAgentic should give up after maxTurns.
	server, calls := scriptedMessagesServer(t, []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"echo","input":{"msg":"hi"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
	})
	c := testAgenticClient(server)

	exec := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "ok", nil
	}

	_, err := c.CompleteAgentic(context.Background(), "sys", "user", []Tool{echoTool}, exec, 3)
	if err == nil {
		t.Fatal("expected an error when the tool-use loop never finishes, got nil")
	}
	if !strings.Contains(err.Error(), "did not finish within 3 turns") {
		t.Fatalf("error = %v, want it to mention the turn cap", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected exactly 3 calls to /v1/messages (the turn cap), got %d", calls.Load())
	}
}
