// Package uiux is the executor for the ui-ux-designer agent: PRD + plan in, a
// UI/UX spec (Markdown) plus optional SVG screen mockups out. The spec and
// mockups are emitted as "=== FILE: path ===" blocks (the same contract
// devagent uses) and committed to the run's workspace like any other agent's
// output, so later developer stages see the mockups in their repository
// snapshot for free.
package uiux

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// specPath is the mandatory block the model's Markdown spec must land in — see
// the output-format contract in agent_prompts/ui-ux-designer.md.
const specPath = "design/spec.md"

// Completer is the slice of *llm.Client the executor needs — a seam for tests.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Executor builds the ui-ux-designer executor.
func Executor(client Completer, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		return run(ctx, client, systemPrompt, env)
	})
}

func run(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# PRD\n\n%s\n\n", env.PRDText)
	if env.Plan != "" {
		fmt.Fprintf(&b, "# Implementation plan\n\n%s\n", env.Plan)
	}
	raw, err := client.Complete(ctx, systemPrompt, b.String())
	if err != nil {
		return factory.ResultEnvelope{}, err
	}

	files, _ := factory.ParseFileBlocks(raw)
	spec, ok := files[specPath]
	if !ok {
		// No file blocks (or an old-style prose reply) — nothing to commit;
		// the raw reply is the spec, same as before this feature existed.
		return factory.ResultEnvelope{Role: env.Stage, Summary: raw}, nil
	}

	repo, err := workspace.Open(ctx, env.WorkspaceDir)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	written, err := repo.WriteFiles(files)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	if err := repo.AddPaths(ctx, written); err != nil {
		return factory.ResultEnvelope{}, err
	}
	sha, err := repo.Commit(ctx, env.Stage, "UI/UX spec and mockups")
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	return factory.ResultEnvelope{
		Role:         env.Stage,
		Summary:      strings.TrimSpace(spec),
		CommitSHA:    sha,
		FilesWritten: written,
	}, nil
}
