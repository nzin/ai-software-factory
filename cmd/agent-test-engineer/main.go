// Command agent-test-engineer runs the test-engineer A2A agent: after the
// developers, it writes the component + e2e test code, the gateway and the root
// docker-compose.yml into the per-run workspace. The tester image and the
// docker-compose.test.yml overlay are not the model's to write: they are the
// factory-owned harness (internal/testharness), laid down over the model's
// files in the same commit.
package main

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/devagent"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/testharness"
)

func main() {
	agentkit.Run(agentkit.AgentSpec{
		Role:               factory.RoleTestEngineer,
		DefaultName:        "Test Engineer",
		DefaultDescription: "Writes the component test suite and the docker-compose deployment for the feature.",
		DefaultSkills:      []string{"testing", "component-test", "docker-compose", "qa"},
		DefaultPort:        9109,
		Executor: func(b *agentkit.Bootstrapped) a2asrv.AgentExecutor {
			return devagent.Executor(b.LLM, b.SystemPrompt, devagent.WithPostWrite(testharness.Materialize))
		},
	})
}
