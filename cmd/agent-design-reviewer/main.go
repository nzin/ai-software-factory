// Command agent-design-reviewer runs the design reviewer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/designreview"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleDesignReviewer,
		DefaultName:        "Design Reviewer",
		DefaultDescription: "Compares rendered e2e screenshots against the UI/UX mockups.",
		DefaultSkills:      []string{"design-review", "ui", "vision"},
		DefaultPort:        9110,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return designreview.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
