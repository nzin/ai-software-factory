package modelext

import (
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	yaml "go.yaml.in/yaml/v3"
)

func TestExtensionRoundTrip(t *testing.T) {
	in := Config{
		Provider:  "anthropic",
		Model:     "claude-sonnet-5",
		MaxTokens: 20000,
		Effort:    "high",
		Thinking:  "adaptive",
		Params:    map[string]any{"foo": "bar"},
	}

	ext := in.ToExtension()
	if ext.URI != URI {
		t.Fatalf("URI = %q, want %q", ext.URI, URI)
	}

	got, err := FromExtension(ext)
	if err != nil {
		t.Fatalf("FromExtension: %v", err)
	}
	if got.Model != in.Model || got.MaxTokens != in.MaxTokens || got.Effort != in.Effort {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, in)
	}
	if got.Params["foo"] != "bar" {
		t.Fatalf("params lost: %+v", got.Params)
	}
}

func TestFromCard(t *testing.T) {
	cfg := Config{Provider: "anthropic", Model: "claude-sonnet-5"}
	card := &a2a.AgentCard{
		Capabilities: a2a.AgentCapabilities{
			Extensions: []a2a.AgentExtension{cfg.ToExtension()},
		},
	}
	got, ok := FromCard(card)
	if !ok {
		t.Fatal("FromCard: ok = false, want true")
	}
	if got.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q", got.Model)
	}

	if _, ok := FromCard(&a2a.AgentCard{}); ok {
		t.Fatal("FromCard on empty card: ok = true, want false")
	}
	if _, ok := FromCard(nil); ok {
		t.Fatal("FromCard(nil): ok = true, want false")
	}
}

func TestWithDefaults(t *testing.T) {
	got := Config{}.WithDefaults()
	if got.Provider != "anthropic" || got.Model != DefaultModel || got.MaxTokens != 16000 || got.Thinking != "adaptive" {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if DefaultModel != "claude-sonnet-5" {
		t.Fatalf("DefaultModel = %q, want claude-sonnet-5", DefaultModel)
	}
}

func TestYAMLTags(t *testing.T) {
	// Prompt-file front-matter uses these exact keys; yaml.v3 lowercases field
	// names by default, so the tags must carry the camelCase names.
	const block = `
provider: anthropic
model: claude-sonnet-5
maxTokens: 64000
effort: low
thinking: adaptive
`
	var c Config
	if err := yaml.Unmarshal([]byte(block), &c); err != nil {
		t.Fatal(err)
	}
	if c.Model != "claude-sonnet-5" || c.MaxTokens != 64000 || c.Effort != "low" || c.Thinking != "adaptive" {
		t.Fatalf("yaml parse: %+v", c)
	}
}
