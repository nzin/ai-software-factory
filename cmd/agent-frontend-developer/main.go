// Command agent-frontend-developer runs the frontend developer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/devagent"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleFrontendDev,
		DefaultName:        "Frontend Developer",
		DefaultDescription: "Writes the web frontend as a Vue 3.x SPA.",
		DefaultSkills:      []string{"vuejs", "vue3", "frontend"},
		DefaultPort:        9104,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return devagent.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
