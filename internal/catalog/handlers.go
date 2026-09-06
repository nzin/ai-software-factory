package catalog

import (
	"encoding/json"
	"errors"

	"github.com/go-openapi/runtime/middleware"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"

	"github.com/nzin/ai-software-factory/internal/catalog/gen/models"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations/agents"
	"github.com/nzin/ai-software-factory/internal/catalog/gen/restapi/operations/health"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// Setup wires every generated operation handler to the service.
func Setup(api *operations.CatalogAPI, svc *Service) {
	api.HealthHealthHandler = health.HealthHandlerFunc(func(health.HealthParams) middleware.Responder {
		return health.NewHealthOK().WithPayload(&models.Health{Status: swag.String("ok")})
	})

	api.AgentsPutAgentHandler = agents.PutAgentHandlerFunc(func(p agents.PutAgentParams) middleware.Responder {
		a := Agent{
			Role:        p.Role,
			Name:        swag.StringValue(p.Body.Name),
			Description: p.Body.Description,
			BaseURL:     swag.StringValue(p.Body.BaseURL),
			Transport:   swag.StringValue(p.Body.Transport),
			Skills:      p.Body.Skills,
			Concurrency: int(swag.Int64Value(p.Body.Concurrency)),
			Enabled:     p.Body.Enabled == nil || *p.Body.Enabled,
		}
		var mc modelext.Config
		if p.Body.ModelConfig != nil {
			mc = modelFromAPI(p.Body.ModelConfig)
		}
		force := swag.BoolValue(p.ForceModel)
		d, err := svc.Upsert(p.HTTPRequest.Context(), a, mc, force)
		if err != nil {
			return errResponder(err, agents.NewPutAgentDefault)
		}
		return agents.NewPutAgentOK().WithPayload(detailToAPI(d))
	})

	api.AgentsListAgentsHandler = agents.ListAgentsHandlerFunc(func(p agents.ListAgentsParams) middleware.Responder {
		var skill string
		if p.Skill != nil {
			skill = *p.Skill
		}
		list, err := svc.List(p.HTTPRequest.Context(), skill, p.Enabled)
		if err != nil {
			return errResponder(err, agents.NewListAgentsDefault)
		}
		out := make([]*models.AgentRegistration, 0, len(list))
		for _, a := range list {
			out = append(out, agentToAPI(a))
		}
		return agents.NewListAgentsOK().WithPayload(out)
	})

	api.AgentsGetAgentHandler = agents.GetAgentHandlerFunc(func(p agents.GetAgentParams) middleware.Responder {
		d, err := svc.Get(p.HTTPRequest.Context(), p.Role)
		if errors.Is(err, ErrNotFound) {
			return agents.NewGetAgentNotFound().WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		if err != nil {
			return errResponder(err, agents.NewGetAgentDefault)
		}
		return agents.NewGetAgentOK().WithPayload(detailToAPI(d))
	})

	api.AgentsGetAgentModelHandler = agents.GetAgentModelHandlerFunc(func(p agents.GetAgentModelParams) middleware.Responder {
		mc, err := svc.GetModel(p.HTTPRequest.Context(), p.Role)
		if errors.Is(err, ErrNotFound) {
			return agents.NewGetAgentModelNotFound().WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		if err != nil {
			return errResponder(err, agents.NewGetAgentModelDefault)
		}
		return agents.NewGetAgentModelOK().WithPayload(modelToAPI(mc))
	})

	api.AgentsPatchAgentModelHandler = agents.PatchAgentModelHandlerFunc(func(p agents.PatchAgentModelParams) middleware.Responder {
		patch := ModelPatch{}
		if b := p.Body; b != nil {
			if b.Provider != "" {
				patch.Provider = &b.Provider
			}
			if b.Model != "" {
				patch.Model = &b.Model
			}
			if b.MaxTokens != 0 {
				patch.MaxTokens = &b.MaxTokens
			}
			if b.Effort != "" {
				patch.Effort = &b.Effort
			}
			if b.Thinking != "" {
				patch.Thinking = &b.Thinking
			}
			patch.Params = anyToMap(b.Params)
		}
		mc, err := svc.PatchModel(p.HTTPRequest.Context(), p.Role, patch)
		if errors.Is(err, ErrNotFound) {
			return agents.NewPatchAgentModelNotFound().WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		if err != nil {
			return errResponder(err, agents.NewPatchAgentModelDefault)
		}
		return agents.NewPatchAgentModelOK().WithPayload(modelToAPI(mc))
	})

	api.AgentsDeleteAgentHandler = agents.DeleteAgentHandlerFunc(func(p agents.DeleteAgentParams) middleware.Responder {
		err := svc.Delete(p.HTTPRequest.Context(), p.Role)
		if errors.Is(err, ErrNotFound) {
			return agents.NewDeleteAgentNotFound().WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		if err != nil {
			return errResponder(err, agents.NewDeleteAgentDefault)
		}
		return agents.NewDeleteAgentNoContent()
	})
}

// --- mapping helpers ---

func modelFromAPI(m *models.ModelConfig) modelext.Config {
	if m == nil {
		return modelext.Config{}
	}
	return modelext.Config{
		Provider:  swag.StringValue(m.Provider),
		Model:     swag.StringValue(m.Model),
		MaxTokens: m.MaxTokens,
		Effort:    m.Effort,
		Thinking:  m.Thinking,
		Params:    anyToMap(m.Params),
	}
}

func modelToAPI(c modelext.Config) *models.ModelConfig {
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

func agentToAPI(a Agent) *models.AgentRegistration {
	return &models.AgentRegistration{
		Role:        swag.String(a.Role),
		Name:        swag.String(a.Name),
		Description: a.Description,
		BaseURL:     swag.String(a.BaseURL),
		Transport:   swag.String(a.Transport),
		Skills:      a.Skills,
		Concurrency: swag.Int64(int64(a.Concurrency)),
		Enabled:     swag.Bool(a.Enabled),
		CreatedAt:   strfmt.DateTime(a.CreatedAt),
		UpdatedAt:   strfmt.DateTime(a.UpdatedAt),
	}
}

func detailToAPI(d Detail) *models.AgentDetail {
	out := &models.AgentDetail{
		Registration: agentToAPI(d.Agent),
		ModelConfig:  modelToAPI(d.Model),
	}
	if d.Card != nil {
		// Marshal the a2a.AgentCard into a free-form object so we don't have
		// to re-model the whole A2A schema in Swagger.
		if b, err := json.Marshal(d.Card); err == nil {
			var generic any
			if json.Unmarshal(b, &generic) == nil {
				out.AgentCard = generic
			}
		}
	}
	return out
}

func anyToMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// errResponder builds a *Default error response (HTTP 500) for the given
// operation's NewXxxDefault constructor.
func errResponder[T interface {
	WithPayload(*models.Error) T
	middleware.Responder
}](err error, newDefault func(int) T) middleware.Responder {
	return newDefault(500).WithPayload(&models.Error{Message: swag.String(err.Error())})
}
