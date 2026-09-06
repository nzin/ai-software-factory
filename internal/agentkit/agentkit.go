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

	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// InvokePath is the A2A JSON-RPC endpoint each agent exposes.
const InvokePath = "/invoke"

// Options configures a single agent.
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

	// DefaultModel is sent to the catalog on first registration only.
	DefaultModel modelext.Config
}

// Bootstrapped is the result of Bootstrap.
type Bootstrapped struct {
	Card    *a2a.AgentCard
	LLM     *llm.Client
	Model   modelext.Config
	Catalog *CatalogClient
}

// Bootstrap registers the agent and returns everything needed to serve it.
func Bootstrap(ctx context.Context, opts Options) (*Bootstrapped, error) {
	if opts.DefaultModel.Model == "" {
		opts.DefaultModel = modelext.Defaults(opts.Role)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 1
	}

	cc, err := NewCatalogClient(opts.CatalogURL)
	if err != nil {
		return nil, err
	}

	if err := cc.Register(ctx, Registration{
		Role:         opts.Role,
		Name:         opts.Name,
		Description:  opts.Description,
		BaseURL:      opts.PublicURL,
		Transport:    "JSONRPC",
		Skills:       opts.Skills,
		Concurrency:  opts.Concurrency,
		DefaultModel: opts.DefaultModel,
	}); err != nil {
		return nil, err
	}

	model, err := cc.GetModel(ctx, opts.Role)
	if err != nil {
		return nil, err
	}

	card := buildCard(opts, model)
	return &Bootstrapped{
		Card:    card,
		LLM:     llm.New(model),
		Model:   model,
		Catalog: cc,
	}, nil
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

func buildCard(opts Options, model modelext.Config) *a2a.AgentCard {
	skill := a2a.AgentSkill{
		ID:          opts.Role,
		Name:        opts.Name,
		Description: opts.Description,
		Tags:        opts.Skills,
	}
	if len(skill.Tags) == 0 {
		skill.Tags = []string{opts.Role}
	}
	invoke := strings.TrimRight(opts.PublicURL, "/") + InvokePath
	return &a2a.AgentCard{
		Name:        opts.Name,
		Description: opts.Description,
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
