package catalog

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// Service is the business layer sitting between the generated HTTP handlers and
// the store. It owns AgentCard assembly.
type Service struct {
	store *Store
}

// NewService wraps a store.
func NewService(store *Store) *Service { return &Service{store: store} }

// Detail bundles everything the API returns for a single agent.
type Detail struct {
	Agent Agent
	Model modelext.Config
	Card  *a2a.AgentCard
}

// Upsert registers or updates an agent.
func (s *Service) Upsert(ctx context.Context, a Agent, mc modelext.Config, forceModel bool) (Detail, error) {
	stored, model, err := s.store.Upsert(ctx, a, mc, forceModel)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Agent: stored, Model: model, Card: BuildCard(stored, model)}, nil
}

// Get returns one agent's detail, including its assembled card.
func (s *Service) Get(ctx context.Context, role string) (Detail, error) {
	a, mc, err := s.store.Get(ctx, role)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Agent: a, Model: mc, Card: BuildCard(a, mc)}, nil
}

// GetModel returns just the model configuration for a role.
func (s *Service) GetModel(ctx context.Context, role string) (modelext.Config, error) {
	return s.store.GetModel(ctx, role)
}

// List returns registered agents.
func (s *Service) List(ctx context.Context, skill string, enabled *bool) ([]Agent, error) {
	return s.store.List(ctx, skill, enabled)
}

// PatchModel applies an operator override to an agent's model configuration.
func (s *Service) PatchModel(ctx context.Context, role string, p ModelPatch) (modelext.Config, error) {
	return s.store.PatchModel(ctx, role, p)
}

// Delete deregisters an agent.
func (s *Service) Delete(ctx context.Context, role string) error {
	return s.store.Delete(ctx, role)
}
