// Command agent-mobile-developer runs the mobile developer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/devagent"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleMobileDeveloper,
		DefaultName:        "Mobile Developer",
		DefaultDescription: "Writes the mobile client when the plan calls for one.",
		DefaultSkills:      []string{"mobile", "react-native"},
		DefaultPort:        9105,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return devagent.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
