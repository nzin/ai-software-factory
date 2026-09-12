// Package llm is a thin wrapper around the Anthropic SDK that drives a Claude
// model according to a modelext.Config (which the agent fetches from the
// catalog on startup).
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	cfg = cfg.WithDefaults()
	if cfg.MaxTokens > maxOutputTokens {
		log.Printf("llm: maxTokens %d exceeds the model output ceiling — capping at %d",
			cfg.MaxTokens, maxOutputTokens)
		cfg.MaxTokens = maxOutputTokens
	}
	return &Client{
		anth: anthropic.NewClient(opts...),
		cfg:  cfg,
	}
}

// Config returns the effective configuration.
func (c *Client) Config() modelext.Config { return c.cfg }

const (
	// maxOutputTokens is the hard output-token ceiling for the current Claude
	// models (Sonnet 5 / Opus 5). A larger max_tokens is a 400 from the API, and
	// no beta header lifts it — so New() clamps to this rather than letting a
	// misconfigured prompt file fail every dispatch.
	maxOutputTokens = 128_000
	// streamThreshold is the MaxTokens above which Complete switches to streaming
	// to avoid HTTP timeouts on long generations.
	streamThreshold = 24000
	// callTimeout bounds a single model call so a stalled stream fails fast. A
	// high-effort generation near the output ceiling can run 20+ minutes.
	callTimeout = 30 * time.Minute
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

// Tool is a client-executed, read-only tool the model can call mid-conversation
// via CompleteAgentic — a name, a description, and a JSON schema for its input.
// Unlike the built-in server tools (web_search, wired in params() via
// hasTool), the tool itself runs locally: CompleteAgentic hands each call to
// the ToolExecFunc it's given and feeds the result back to the model.
type Tool struct {
	Name        string
	Description string
	// Properties and Required together form the tool's JSON input schema
	// (an object schema — the only shape Claude's tool_use supports). e.g.
	// Properties: map[string]any{"path": map[string]any{"type": "string"}},
	// Required: []string{"path"}.
	Properties map[string]any
	Required   []string
}

// ToolExecFunc executes one tool call and returns the text to feed back to the
// model. A returned error is reported to the model as a tool error (so it can
// adjust and retry) rather than aborting the loop.
type ToolExecFunc func(ctx context.Context, name string, input json.RawMessage) (string, error)

const (
	// defaultMaxToolTurns bounds a CompleteAgentic loop when the caller passes
	// maxTurns <= 0. Each turn is a full model round-trip; the 90-minute
	// coordinator dispatch timeout comfortably absorbs this many.
	defaultMaxToolTurns = 8
	// maxToolResultBytes caps what one tool call feeds back to the model, so a
	// large file read or diff can't blow up the running conversation across
	// several turns.
	maxToolResultBytes = 8_000
)

// CompleteAgentic is Complete with a bounded tool-use loop: the model may call
// any of tools (executed via exec, capped at maxTurns round-trips — 0 uses
// defaultMaxToolTurns) before giving its final text answer, which is returned
// exactly as Complete would return it. Tool calls are not streamed; each
// round-trip is bounded by callTimeout like a plain Complete call.
func (c *Client) CompleteAgentic(ctx context.Context, system, user string, tools []Tool, exec ToolExecFunc, maxTurns int) (string, error) {
	if maxTurns <= 0 {
		maxTurns = defaultMaxToolTurns
	}
	params := c.params(system, user)
	for _, t := range tools {
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: t.Properties,
					Required:   t.Required,
				},
			},
		})
	}

	for turn := 0; turn < maxTurns; turn++ {
		resp, err := c.callOnce(ctx, params)
		if err != nil {
			return "", fmt.Errorf("llm: tool-use turn %d: %w", turn, err)
		}
		if resp.StopReason == anthropic.StopReasonRefusal {
			return "", fmt.Errorf("llm: model refused the request (%s): %s",
				resp.StopDetails.Category, resp.StopDetails.Explanation)
		}
		if resp.StopReason != anthropic.StopReasonToolUse {
			return textOf(resp)
		}

		var assistantBlocks, toolResults []anthropic.ContentBlockParamUnion
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.TextBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(b.Text))
			case anthropic.ThinkingBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewThinkingBlock(b.Signature, b.Thinking))
			case anthropic.RedactedThinkingBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewRedactedThinkingBlock(b.Data))
			case anthropic.ToolUseBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewToolUseBlock(b.ID, json.RawMessage(b.Input), b.Name))
				out, execErr := exec(ctx, b.Name, b.Input)
				isErr := execErr != nil
				if isErr {
					out = execErr.Error()
				}
				toolResults = append(toolResults, anthropic.NewToolResultBlock(b.ID, clip(out, maxToolResultBytes), isErr))
			}
		}
		if len(toolResults) == 0 {
			// stop_reason said tool_use but no ToolUseBlock was found — return
			// whatever text there was rather than looping forever.
			return textOf(resp)
		}
		params.Messages = append(params.Messages, anthropic.NewAssistantMessage(assistantBlocks...))
		params.Messages = append(params.Messages, anthropic.NewUserMessage(toolResults...))
	}
	return "", fmt.Errorf("llm: tool-use loop did not finish within %d turns", maxTurns)
}

// callOnce is a single non-streaming Messages.New call bounded by callTimeout
// — the building block CompleteAgentic repeats per tool-use turn.
func (c *Client) callOnce(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.anth.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("llm: message create: %w", err)
	}
	return resp, nil
}

// clip bounds s to n bytes, keeping the end (a tool result's most useful part
// — a file's tail, a diff's actual changes, a grep's last matches — usually
// isn't the start).
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…\n" + s[len(s)-n:]
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
	if c.hasTool("web_search") {
		params.Tools = append(params.Tools, anthropic.ToolUnionParam{
			OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{},
		})
	}
	return params
}

// hasTool reports whether the agent's prompt front-matter enabled the named
// server tool via `params: {tools: [...]}`. Config has no first-class Tools
// field: it round-trips through the catalog service, whose ModelConfig schema
// whitelists only provider/model/maxTokens/effort/thinking/params, so a new
// named field would be silently dropped — Params is the only knob that
// survives that round trip untouched. The value is typically []any after a
// YAML->JSON round trip, but []string is accepted too for direct construction
// (e.g. in tests).
func (c *Client) hasTool(name string) bool {
	v, ok := c.cfg.Params["tools"]
	if !ok {
		return false
	}
	switch list := v.(type) {
	case []string:
		for _, s := range list {
			if s == name {
				return true
			}
		}
	case []any:
		for _, s := range list {
			if str, ok := s.(string); ok && str == name {
				return true
			}
		}
	}
	return false
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
