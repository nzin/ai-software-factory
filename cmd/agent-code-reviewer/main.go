// Command agent-code-reviewer runs the code reviewer A2A agent (Kodus).
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/codereview"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleCodeReviewer,
		DefaultName:        "Code Reviewer",
		DefaultDescription: "Runs Kodus over the change and summarises the review.",
		DefaultSkills:      []string{"code-review", "kodus"},
		DefaultPort:        9107,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return codereview.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
