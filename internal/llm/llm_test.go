package llm

import (
	"testing"

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

func TestHasToolIgnoresOtherKeys(t *testing.T) {
	c := New(modelext.Config{Model: "claude-sonnet-5", Params: map[string]any{"foo": "bar"}})
	if c.hasTool("web_search") {
		t.Fatal("hasTool(web_search) = true with no tools key")
	}
}
