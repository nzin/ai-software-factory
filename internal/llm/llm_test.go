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
