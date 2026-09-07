// Command agent-backend-developer runs the backend developer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/devagent"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleBackendDeveloper,
		DefaultName:        "Backend Developer",
		DefaultDescription: "Writes the backend as a Go REST API with go-swagger.",
		DefaultSkills:      []string{"golang", "rest-api", "go-swagger"},
		DefaultPort:        9103,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return devagent.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
