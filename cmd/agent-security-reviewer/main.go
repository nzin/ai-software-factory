// Command agent-security-reviewer runs the security reviewer A2A agent.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/secreview"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleSecurityReviewer,
		DefaultName:        "Security Reviewer",
		DefaultDescription: "Reviews the diff for security issues, backed by SAST tooling.",
		DefaultSkills:      []string{"security", "sast", "appsec"},
		DefaultPort:        9106,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return secreview.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
