// Package modelext defines the custom A2A protocol extension that carries an
// agent's model configuration (which Claude model and parameters the agent must
// use) inside its AgentCard.
//
// The A2A AgentCard has no field for "the model to use", but
// AgentCard.Capabilities.Extensions is the sanctioned place for custom metadata.
// The catalog service is the source of truth for this configuration; it injects
// this extension into every card it assembles, and agents read their effective
// configuration back from the catalog on startup.
package modelext

import (
	"encoding/json"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// URI uniquely identifies this extension.
const URI = "https://ai-software-factory.dev/ext/model/v1"

// Description is the human-readable blurb attached to the extension on the card.
const Description = "Claude model and parameters this agent runs on"

// Config is the model configuration for a single agent.
//
// There is deliberately no temperature / top_p: current Claude models reject
// sampling parameters. Extra forward-compatible knobs go in Params.
type Config struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	MaxTokens int64          `json:"maxTokens,omitempty"`
	Effort    string         `json:"effort,omitempty"`   // "" | low | medium | high | xhigh | max
	Thinking  string         `json:"thinking,omitempty"` // "" (=adaptive) | adaptive | off
	Params    map[string]any `json:"params,omitempty"`
}

// WithDefaults returns a copy of c with empty fields filled in.
func (c Config) WithDefaults() Config {
	out := c
	if out.Provider == "" {
		out.Provider = "anthropic"
	}
	if out.Model == "" {
		out.Model = "claude-opus-5"
	}
	if out.MaxTokens == 0 {
		out.MaxTokens = 16000
	}
	if out.Thinking == "" {
		out.Thinking = "adaptive"
	}
	return out
}

// ToExtension renders the config as an a2a.AgentExtension suitable for
// AgentCard.Capabilities.Extensions.
func (c Config) ToExtension() a2a.AgentExtension {
	return a2a.AgentExtension{
		URI:         URI,
		Description: Description,
		Params:      c.toParams(),
	}
}

func (c Config) toParams() map[string]any {
	// Round-trip through JSON so the map matches the json tags exactly.
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// FromExtension decodes a Config from a single a2a.AgentExtension.
func FromExtension(ext a2a.AgentExtension) (Config, error) {
	if ext.URI != URI {
		return Config{}, fmt.Errorf("modelext: extension URI %q is not %q", ext.URI, URI)
	}
	b, err := json.Marshal(ext.Params)
	if err != nil {
		return Config{}, fmt.Errorf("modelext: marshal params: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("modelext: decode params: %w", err)
	}
	return c, nil
}

// FromCard extracts the model config from a card's capability extensions.
// The bool is false when the card carries no such extension.
func FromCard(card *a2a.AgentCard) (Config, bool) {
	if card == nil {
		return Config{}, false
	}
	for _, ext := range card.Capabilities.Extensions {
		if ext.URI != URI {
			continue
		}
		c, err := FromExtension(ext)
		if err != nil {
			return Config{}, false
		}
		return c, true
	}
	return Config{}, false
}

// Defaults returns a sensible default configuration for a given agent role.
// Used only the first time an agent registers with the catalog.
func Defaults(role string) Config {
	switch role {
	case "planner", "security-reviewer", "code-reviewer", "ui-ux-designer":
		return Config{Provider: "anthropic", Model: "claude-opus-5", MaxTokens: 16000, Effort: "high", Thinking: "adaptive"}
	default:
		return Config{Provider: "anthropic", Model: "claude-opus-5", MaxTokens: 16000, Thinking: "adaptive"}
	}
}
