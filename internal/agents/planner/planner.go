// Package planner is the "planner" specialized agent: it turns a PRD into a
// concrete implementation plan.
package planner

import (
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/llm"
)

// Role is the stable catalog key for this agent.
const Role = "planner"

// Skills advertised on the AgentCard.
var Skills = []string{"planning", "architecture", "breakdown"}

// SystemPrompt steers the planner.
const SystemPrompt = `You are a senior software architect on an automated software factory.

You receive a PRD (Product Requirements Definition). Produce a concrete, actionable
implementation plan in Markdown with these sections:

1. Context - restate the problem and the intended outcome in 2-3 sentences.
2. Approach - the recommended solution, and why (mention discarded alternatives briefly).
3. Components to change - existing modules/services touched, and how.
4. New files & structures - files to add, key types, data model, migrations.
5. API changes - new/changed endpoints or contracts.
6. Task breakdown - an ordered list of implementation tasks, each small enough
   for one developer agent (backend / frontend / mobile) to pick up.
7. Test plan - unit, integration, and end-to-end checks.
8. Risks & open questions.

Be specific and terse. Do not write code; describe what to build. If the PRD is
ambiguous, state your assumptions explicitly in section 8.`

// Executor builds the planner's A2A executor.
func Executor(client *llm.Client) a2asrv.AgentExecutor {
	return agentkit.LLMExecutor(client, SystemPrompt)
}
