// Package planner holds the identity of the "planner" specialized agent. Its
// system prompt lives in agent_prompts/planner.md, loaded at startup by
// internal/agentkit.
package planner

// Role is the stable catalog key for this agent.
const Role = "planner"

// Code defaults for the agent's catalog metadata. The front-matter of
// agent_prompts/planner.md overrides any of these that it sets.
const (
	DefaultName        = "Planner"
	DefaultDescription = "Turns a PRD into a concrete implementation plan."
)

// Skills is the default skill set (overridden by prompt front-matter).
var Skills = []string{"planning", "architecture", "breakdown"}
