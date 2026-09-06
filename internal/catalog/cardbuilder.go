package catalog

import (
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/nzin/ai-software-factory/internal/modelext"
)

// InvokePath is the path each agent's A2A server exposes for its JSON-RPC handler.
const InvokePath = "/invoke"

// BuildCard assembles the A2A AgentCard for an agent from its registration and
// model configuration. The model configuration is surfaced as a capability
// extension (see package modelext) so both machines and humans can read "the
// model to use" straight off the card.
func BuildCard(a Agent, mc modelext.Config) *a2a.AgentCard {
	transport := a2a.TransportProtocol(a.Transport)
	if transport == "" {
		transport = a2a.TransportProtocolJSONRPC
	}

	skill := a2a.AgentSkill{
		ID:          a.Role,
		Name:        a.Name,
		Description: a.Description,
		Tags:        a.Skills,
	}
	if len(skill.Tags) == 0 {
		skill.Tags = []string{a.Role}
	}

	return &a2a.AgentCard{
		Name:        a.Name,
		Description: a.Description,
		Version:     "0.1.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(strings.TrimRight(a.BaseURL, "/")+InvokePath, transport),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Capabilities: a2a.AgentCapabilities{
			Streaming:  true,
			Extensions: []a2a.AgentExtension{mc.WithDefaults().ToExtension()},
		},
		Skills: []a2a.AgentSkill{skill},
	}
}
