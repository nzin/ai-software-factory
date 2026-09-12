// Package uiux is the executor for the ui-ux-designer agent: PRD + plan in, a
// UI/UX spec (Markdown) plus optional SVG screen mockups out. The spec and
// mockups are emitted as "=== FILE: path ===" blocks (the same contract
// devagent uses) and committed to the run's workspace like any other agent's
// output, so later developer stages see the mockups in their repository
// snapshot for free.
package uiux

import (
	"context"
	"encoding/json"
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

// jsonBlockPaths are optional structured-data blocks that must be valid JSON
// to be useful to frontend/mobile's deterministic parsing — an LLM
// occasionally wraps JSON in prose or leaves a trailing comma. A block that
// fails validation is dropped rather than committed broken.
var jsonBlockPaths = []string{"design/tokens.json", "design/components.json"}

// dropInvalidJSON removes any jsonBlockPaths entry from files whose content
// isn't valid JSON, returning the paths dropped.
func dropInvalidJSON(files map[string]string) []string {
	var dropped []string
	for _, p := range jsonBlockPaths {
		if body, ok := files[p]; ok && !json.Valid([]byte(body)) {
			delete(files, p)
			dropped = append(dropped, p)
		}
	}
	return dropped
}

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
	dropped := dropInvalidJSON(files)

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
	summary := strings.TrimSpace(spec)
	if len(dropped) > 0 {
		summary += fmt.Sprintf("\n\n(dropped invalid JSON: %s)", strings.Join(dropped, ", "))
	}
	return factory.ResultEnvelope{
		Role:         env.Stage,
		Summary:      summary,
		CommitSHA:    sha,
		FilesWritten: written,
	}, nil
}
