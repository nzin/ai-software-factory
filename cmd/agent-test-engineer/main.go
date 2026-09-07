// Command agent-test-engineer runs the test-engineer A2A agent: after the
// developers, it writes a component test suite and the deployment glue
// (docker-compose.yml, docker-compose.test.yml) into the per-run workspace.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/devagent"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleTestEngineer,
		DefaultName:        "Test Engineer",
		DefaultDescription: "Writes the component test suite and the docker-compose deployment for the feature.",
		DefaultSkills:      []string{"testing", "component-test", "docker-compose", "qa"},
		DefaultPort:        9109,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return devagent.Executor(b.LLM, b.SystemPrompt)
		},
	})
}
