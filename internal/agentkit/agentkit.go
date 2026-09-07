// Package agentkit is the shared bootstrap for every specialized A2A agent in
// the factory: it registers the agent with the catalog, fetches the effective
// model configuration back, builds the AgentCard (with the model extension), and
// stands up the A2A JSON-RPC server.
package agentkit

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentprompts"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// InvokePath is the A2A JSON-RPC endpoint each agent exposes.
const InvokePath = "/invoke"

// Options configures a single agent. Name / Description / Skills are the code
// defaults; a matching prompt file's front-matter overrides them.
type Options struct {
	Role        string
	Name        string
	Description string
	Skills      []string
	Concurrency int

	// Addr is the local listen address, e.g. ":9101".
	Addr string
	// PublicURL is how other services reach this agent, e.g. "http://127.0.0.1:9101".
	PublicURL string
	// CatalogURL is the catalog base URL, e.g. "http://127.0.0.1:8080".
	CatalogURL string
	// PromptsDir holds <role>.md prompt files (default "agent_prompts").
	PromptsDir string

	// DefaultModel is sent to the catalog on first registration only.
	DefaultModel modelext.Config
}

// Bootstrapped is the result of Bootstrap.
type Bootstrapped struct {
	Card         *a2a.AgentCard
	LLM          *llm.Client
	Model        modelext.Config
	Catalog      *CatalogClient
	SystemPrompt string
}

// LLMExecutor is the standard single-turn executor for this agent, wired to its
// loaded system prompt.
func (b *Bootstrapped) LLMExecutor() a2asrv.AgentExecutor {
	return LLMExecutor(b.LLM, b.SystemPrompt)
}

// Bootstrap loads the agent's prompt file, registers the agent with the catalog
// (using prompt front-matter to override the code defaults), fetches the
// effective model configuration, and returns everything needed to serve it.
func Bootstrap(ctx context.Context, opts Options) (*Bootstrapped, error) {
	if opts.DefaultModel.Model == "" {
		opts.DefaultModel = modelext.Defaults(opts.Role)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}
	if opts.PromptsDir == "" {
		opts.PromptsDir = "agent_prompts"
	}

	prompt, err := agentprompts.Load(opts.PromptsDir, opts.Role)
	if err != nil {
		return nil, err
	}
	meta := mergeMeta(opts, prompt)
	log.Printf("agentkit: %q prompt loaded from %s (%d bytes)",
		meta.Name, opts.PromptsDir+"/"+opts.Role+".md", len(prompt.System))

	cc, err := NewCatalogClient(opts.CatalogURL)
	if err != nil {
		return nil, err
	}

	if err := cc.Register(ctx, Registration{
		Role:         opts.Role,
		Name:         meta.Name,
		Description:  meta.Description,
		BaseURL:      opts.PublicURL,
		Transport:    "JSONRPC",
		Skills:       meta.Skills,
		Concurrency:  opts.Concurrency,
		DefaultModel: opts.DefaultModel,
	}); err != nil {
		return nil, err
	}

	model, err := cc.GetModel(ctx, opts.Role)
	if err != nil {
		return nil, err
	}

	card := buildCard(meta, opts.PublicURL, model)
	return &Bootstrapped{
		Card:         card,
		LLM:          llm.New(model),
		Model:        model,
		Catalog:      cc,
		SystemPrompt: prompt.System,
	}, nil
}

// meta is the resolved agent identity after merging code defaults with prompt
// front-matter.
type meta struct {
	Role        string
	Name        string
	Description string
	Skills      []string
}

// mergeMeta layers prompt front-matter over the Options code defaults
// (front-matter wins; empty front-matter fields fall back).
func mergeMeta(opts Options, p *agentprompts.Prompt) meta {
	m := meta{
		Role:        opts.Role,
		Name:        opts.Name,
		Description: opts.Description,
		Skills:      opts.Skills,
	}
	if p != nil {
		if p.Name != "" {
			m.Name = p.Name
		}
		if p.Description != "" {
			m.Description = p.Description
		}
		if len(p.Skills) > 0 {
			m.Skills = p.Skills
		}
	}
	if m.Name == "" {
		m.Name = opts.Role
	}
	return m
}

// Serve runs the agent's A2A JSON-RPC server until ctx is cancelled.
func Serve(ctx context.Context, addr string, card *a2a.AgentCard, exec a2asrv.AgentExecutor) error {
	handler := a2asrv.NewHandler(exec)

	mux := http.NewServeMux()
	mux.Handle(InvokePath, a2asrv.NewJSONRPCHandler(handler))
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("agentkit: listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	log.Printf("agentkit: %q serving A2A on %s%s", card.Name, addr, InvokePath)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func buildCard(m meta, publicURL string, model modelext.Config) *a2a.AgentCard {
	skill := a2a.AgentSkill{
		ID:          m.Role,
		Name:        m.Name,
		Description: m.Description,
		Tags:        m.Skills,
	}
	if len(skill.Tags) == 0 {
		skill.Tags = []string{m.Role}
	}
	invoke := strings.TrimRight(publicURL, "/") + InvokePath
	return &a2a.AgentCard{
		Name:        m.Name,
		Description: m.Description,
		Version:     "0.1.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(invoke, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities: a2a.AgentCapabilities{
			Streaming:  true,
			Extensions: []a2a.AgentExtension{model.WithDefaults().ToExtension()},
		},
		Skills: []a2a.AgentSkill{skill},
	}
}
