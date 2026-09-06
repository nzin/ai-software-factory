// Package llm is a thin wrapper around the Anthropic SDK that drives a Claude
// model according to a modelext.Config (which the agent fetches from the
// catalog on startup).
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/nzin/ai-software-factory/internal/modelext"
)

// Client is a configured Claude client for a single agent.
type Client struct {
	anth anthropic.Client
	cfg  modelext.Config
}

// New builds a Client. It uses the ambient credentials resolved by the Anthropic
// SDK (ANTHROPIC_API_KEY, or an `ant auth login` profile). Extra options can be
// passed for tests.
func New(cfg modelext.Config, opts ...option.RequestOption) *Client {
	return &Client{
		anth: anthropic.NewClient(opts...),
		cfg:  cfg.WithDefaults(),
	}
}

// Config returns the effective configuration.
func (c *Client) Config() modelext.Config { return c.cfg }

// Complete runs a single non-streaming completion and returns the concatenated
// text of the response.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.cfg.Model),
		MaxTokens: c.maxTokens(),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	}
	if system != "" {
		params.System = []anthropic.TextBlockParam{{Text: system}}
	}
	if !strings.EqualFold(c.cfg.Thinking, "off") {
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		}
	}
	if effort := c.effort(); effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: effort}
	}

	resp, err := c.anth.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("llm: message create: %w", err)
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("llm: model refused the request (%s): %s",
			resp.StopDetails.Category, resp.StopDetails.Explanation)
	}

	var b strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("llm: empty response (stop_reason=%s)", resp.StopReason)
	}
	return out, nil
}

func (c *Client) maxTokens() int64 {
	if c.cfg.MaxTokens > 0 {
		return c.cfg.MaxTokens
	}
	return 16000
}

func (c *Client) effort() anthropic.OutputConfigEffort {
	switch strings.ToLower(c.cfg.Effort) {
	case "low":
		return anthropic.OutputConfigEffortLow
	case "medium":
		return anthropic.OutputConfigEffortMedium
	case "high":
		return anthropic.OutputConfigEffortHigh
	case "xhigh":
		return anthropic.OutputConfigEffortXhigh
	case "max":
		return anthropic.OutputConfigEffortMax
	default:
		return ""
	}
}
