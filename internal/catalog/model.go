package catalog

import (
	"errors"
	"time"
)

// ErrNotFound is returned when an agent role is not registered.
var ErrNotFound = errors.New("catalog: agent not found")

// Transport values mirror a2a.TransportProtocol.
const (
	TransportJSONRPC = "JSONRPC"
	TransportGRPC    = "GRPC"
	TransportHTTPX   = "HTTP+JSON"
)

// Agent is the domain representation of a registered agent (no persistence or
// transport tags).
type Agent struct {
	Role        string
	Name        string
	Description string
	BaseURL     string
	Transport   string
	Skills      []string
	Concurrency int
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ModelPatch is a partial update to an agent's model configuration. A nil field
// means "leave unchanged".
type ModelPatch struct {
	Provider  *string
	Model     *string
	MaxTokens *int64
	Effort    *string
	Thinking  *string
	Params    map[string]any
}

// --- GORM rows (persistence only) ---

type agentRow struct {
	Role        string `gorm:"primaryKey"`
	Name        string
	Description string
	BaseURL     string
	Transport   string
	Enabled     bool
	Concurrency int
	Skills      []string `gorm:"serializer:json"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (agentRow) TableName() string { return "agents" }

type modelConfigRow struct {
	Role      string `gorm:"primaryKey"`
	Provider  string
	Model     string
	MaxTokens int64
	Effort    string
	Thinking  string
	Params    map[string]any `gorm:"serializer:json"`
	UpdatedAt time.Time
}

func (modelConfigRow) TableName() string { return "model_configs" }

func (r agentRow) toDomain() Agent {
	return Agent{
		Role:        r.Role,
		Name:        r.Name,
		Description: r.Description,
		BaseURL:     r.BaseURL,
		Transport:   r.Transport,
		Skills:      r.Skills,
		Concurrency: r.Concurrency,
		Enabled:     r.Enabled,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}
