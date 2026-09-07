// Command agent-build-gate runs the build/test gate A2A agent. It compiles and
// tests the per-run workspace after the developers and bounces the run back on
// any failure. It makes no LLM calls.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/buildgate"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleBuildGate,
		DefaultName:        "Build Gate",
		DefaultDescription: "Compiles and tests the workspace; fails the run on a broken build.",
		DefaultSkills:      []string{"build", "test", "ci"},
		DefaultPort:        9108,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return buildgate.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
