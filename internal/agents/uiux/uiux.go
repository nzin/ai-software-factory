// Package uiux is the executor for the ui-ux-designer agent: PRD + plan in, a
// UI/UX spec (text) out. No code.
package uiux

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
)

// Executor builds the ui-ux-designer executor.
func Executor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		var b strings.Builder
		fmt.Fprintf(&b, "# PRD\n\n%s\n\n", env.PRDText)
		if env.Plan != "" {
			fmt.Fprintf(&b, "# Implementation plan\n\n%s\n", env.Plan)
		}
		spec, err := client.Complete(ctx, systemPrompt, b.String())
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		return factory.ResultEnvelope{Role: env.Stage, Summary: spec}, nil
	})
}
