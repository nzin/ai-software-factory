// Command agent-ui-ux-designer runs the UI/UX designer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/uiux"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleUIUXDesigner,
		DefaultName:        "UI/UX Designer",
		DefaultDescription: "Produces a UI/UX spec the frontend agents build from.",
		DefaultSkills:      []string{"ui", "ux", "design"},
		DefaultPort:        9102,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return uiux.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
