package agentkit

import (
	"context"
	"fmt"
	"net/url"

	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"

	catalogclient "github.com/nzin/ai-software-factory/internal/catalog/gen/client"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/client/agents"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/models"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// CatalogClient is a small typed wrapper over the generated catalog REST client.
type CatalogClient struct {
	api *catalogclient.Catalog
}

// NewCatalogClient builds a client for the catalog service at baseURL
// (e.g. "http://127.0.0.1:8080").
func NewCatalogClient(baseURL string) (*CatalogClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("agentkit: bad catalog URL %q: %w", baseURL, err)
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	transport := httptransport.New(u.Host, "/", []string{scheme})
	return &CatalogClient{api: catalogclient.New(transport, strfmt.Default)}, nil
}

// Registration is the payload for registering an agent with the catalog.
type Registration struct {
	Role         string
	Name         string
	Description  string
	BaseURL      string
	Transport    string
	Skills       []string
	Concurrency  int
	DefaultModel modelext.Config
	// ForceModel overwrites the catalog's stored model config with DefaultModel
	// even when the agent already exists (the prompt file is authoritative).
	ForceModel bool
}

// Register performs an idempotent upsert of the agent in the catalog.
func (c *CatalogClient) Register(ctx context.Context, r Registration) error {
	transport := r.Transport
	if transport == "" {
		transport = "JSONRPC"
	}
	body := &models.PutAgentRequest{
		Name:        swag.String(r.Name),
		Description: r.Description,
		BaseURL:     swag.String(r.BaseURL),
		Transport:   swag.String(transport),
		Skills:      r.Skills,
		Concurrency: swag.Int64(int64(r.Concurrency)),
		Enabled:     swag.Bool(true),
		ModelConfig: modelConfigToAPI(r.DefaultModel),
	}
	params := agents.NewPutAgentParams().WithContext(ctx).WithRole(r.Role).WithBody(body)
	if r.ForceModel {
		params = params.WithForceModel(swag.Bool(true))
	}
	_, err := c.api.Agents.PutAgent(params)
	if err != nil {
		return fmt.Errorf("agentkit: register %q: %w", r.Role, err)
	}
	return nil
}

// GetModel returns the effective model configuration the catalog holds for role.
func (c *CatalogClient) GetModel(ctx context.Context, role string) (modelext.Config, error) {
	params := agents.NewGetAgentModelParams().WithContext(ctx).WithRole(role)
	ok, err := c.api.Agents.GetAgentModel(params)
	if err != nil {
		return modelext.Config{}, fmt.Errorf("agentkit: get model for %q: %w", role, err)
	}
	return modelConfigFromAPI(ok.Payload), nil
}

// GetAgentBaseURL returns the registered base URL for role.
func (c *CatalogClient) GetAgentBaseURL(ctx context.Context, role string) (string, error) {
	params := agents.NewGetAgentParams().WithContext(ctx).WithRole(role)
	ok, err := c.api.Agents.GetAgent(params)
	if err != nil {
		return "", fmt.Errorf("agentkit: get agent %q: %w", role, err)
	}
	if ok.Payload == nil || ok.Payload.Registration == nil {
		return "", fmt.Errorf("agentkit: agent %q has no registration", role)
	}
	return swag.StringValue(ok.Payload.Registration.BaseURL), nil
}

func modelConfigToAPI(c modelext.Config) *models.ModelConfig {
	c = c.WithDefaults()
	return &models.ModelConfig{
		Provider:  swag.String(c.Provider),
		Model:     swag.String(c.Model),
		MaxTokens: c.MaxTokens,
		Effort:    c.Effort,
		Thinking:  c.Thinking,
		Params:    c.Params,
	}
}

func modelConfigFromAPI(m *models.ModelConfig) modelext.Config {
	if m == nil {
		return modelext.Config{}.WithDefaults()
	}
	cfg := modelext.Config{
		Provider:  swag.StringValue(m.Provider),
		Model:     swag.StringValue(m.Model),
		MaxTokens: m.MaxTokens,
		Effort:    m.Effort,
		Thinking:  m.Thinking,
	}
	if mm, ok := m.Params.(map[string]any); ok {
		cfg.Params = mm
	}
	return cfg.WithDefaults()
}
