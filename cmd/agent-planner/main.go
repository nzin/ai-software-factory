// Command agent-planner runs the planner A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/planner"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               planner.Role,
		DefaultName:        planner.DefaultName,
		DefaultDescription: planner.DefaultDescription,
		DefaultSkills:      planner.Skills,
		DefaultPort:        9101,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return b.LLMExecutor()
		},
	})
}
