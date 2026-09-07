// Package llm is a thin wrapper around the Anthropic SDK that drives a Claude
// model according to a modelext.Config (which the agent fetches from the
// catalog on startup).
package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

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

const (
	// streamThreshold is the MaxTokens above which Complete switches to streaming
	// to avoid HTTP timeouts on long generations.
	streamThreshold = 24000
	// callTimeout bounds a single model call so a stalled stream fails fast.
	callTimeout = 15 * time.Minute
	// idleTimeout fails a stream that goes quiet mid-generation.
	idleTimeout = 3 * time.Minute
)

// Complete runs a single completion and returns the concatenated text of the
// response. It streams automatically for large MaxTokens.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	if c.maxTokens() > streamThreshold {
		return c.CompleteStream(ctx, system, user)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	params := c.params(system, user)
	resp, err := c.anth.Messages.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("llm: message create: %w", err)
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("llm: model refused the request (%s): %s",
			resp.StopDetails.Category, resp.StopDetails.Explanation)
	}
	return textOf(resp)
}

// CompleteStream is Complete over a streaming request; used for large outputs.
// It bounds the whole call and fails if the stream goes idle mid-generation.
func (c *Client) CompleteStream(ctx context.Context, system, user string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	stream := c.anth.Messages.NewStreaming(ctx, c.params(system, user))
	var msg anthropic.Message

	idle := time.AfterFunc(idleTimeout, cancel)
	for stream.Next() {
		idle.Reset(idleTimeout)
		if err := msg.Accumulate(stream.Current()); err != nil {
			return "", fmt.Errorf("llm: accumulate: %w", err)
		}
	}
	idle.Stop()
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("llm: stream stalled or timed out after streaming %d tokens: %w", msg.Usage.OutputTokens, err)
		}
		return "", fmt.Errorf("llm: stream: %w", err)
	}
	if msg.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("llm: model refused the request (%s): %s",
			msg.StopDetails.Category, msg.StopDetails.Explanation)
	}
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		txt, _ := textOf(&msg)
		if txt == "" {
			return "", fmt.Errorf("llm: hit max_tokens (%d) before producing any output — lower Effort or raise MaxTokens", c.maxTokens())
		}
		return txt, nil // partial; caller may still be able to use it
	}
	return textOf(&msg)
}

func (c *Client) params(system, user string) anthropic.MessageNewParams {
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
	return params
}

func textOf(m *anthropic.Message) (string, error) {
	var b strings.Builder
	for _, block := range m.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("llm: empty response (stop_reason=%s)", m.StopReason)
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
