// Package devagent is the shared executor for the code-writing agents
// (backend-developer, frontend-developer, mobile-developer). They differ only in
// their prompt file; the executor is identical: read the worktree, ask the model
// for a set of files, write and commit them.
package devagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

const maxContextBytes = 60_000

const outputContract = `
Return ONLY a JSON array of files to write, no prose:

[
  { "path": "relative/path.go", "content": "<full file contents>" },
  ...
]

Rules:
- Full file contents, not diffs. Paths are relative to the repo root.
- Include everything needed to build and run: source, config, go.mod / package.json, tests.
- Do not delete files. Overwrite by providing the same path.
- Keep it minimal but complete for the assigned tasks.`

// Executor builds the developer executor for a given role prompt.
func Executor(client *llm.Client, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		repo, err := workspace.Open(ctx, env.WorkspaceDir)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		repo.BaseBranch = env.BaseBranch
		tree, _ := repo.Tree(ctx)
		user := buildPrompt(env, repo.Dir, tree)

		out, err := client.Complete(ctx, systemPrompt, user)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		files, err := factory.ParseFileSpecs(out)
		if err != nil {
			return factory.ResultEnvelope{}, fmt.Errorf("devagent(%s): could not parse file list from model: %w", env.Stage, err)
		}
		if len(files) == 0 {
			return factory.ResultEnvelope{}, fmt.Errorf("devagent(%s): model returned no files", env.Stage)
		}
		written, err := repo.WriteFiles(files)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		sha, err := repo.Commit(ctx, env.Stage, taskTitles(env.Tasks))
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		return factory.ResultEnvelope{
			Role:         env.Stage,
			Summary:      fmt.Sprintf("wrote %d files (%s)", len(written), strings.Join(shortList(written), ", ")),
			CommitSHA:    sha,
			FilesWritten: written,
		}, nil
	})
}

func buildPrompt(env factory.DispatchEnvelope, dir string, tree []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PRD\n\n%s\n\n", env.PRDText)
	if env.Plan != "" {
		fmt.Fprintf(&b, "# Implementation plan\n\n%s\n\n", env.Plan)
	}
	if env.UISpec != "" {
		fmt.Fprintf(&b, "# UI/UX spec\n\n%s\n\n", env.UISpec)
	}
	if env.Attempt > 0 && len(env.Findings) > 0 {
		fmt.Fprintf(&b, "# Fix pass (attempt %d)\n\nThe previous version was reviewed. Fix these findings; keep everything else working:\n\n", env.Attempt+1)
		for _, f := range env.Findings {
			fmt.Fprintf(&b, "- [%s] %s:%d — %s\n", f.Severity, f.File, f.Line, f.Title)
			if f.Suggestion != "" {
				fmt.Fprintf(&b, "  fix: %s\n", f.Suggestion)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("# Your tasks\n\n")
	for _, t := range env.Tasks {
		fmt.Fprintf(&b, "- [%s] %s\n", t.ID, t.Title)
		if t.Details != "" {
			fmt.Fprintf(&b, "  %s\n", t.Details)
		}
	}
	b.WriteString("\n# Current repository\n\n")
	if len(tree) == 0 {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(strings.Join(tree, "\n"))
		b.WriteString("\n\n")
		b.WriteString(existingContent(dir, tree))
	}
	b.WriteString("\n")
	b.WriteString(outputContract)
	return b.String()
}

// existingContent inlines the current files up to a byte budget so the agent can
// extend rather than clobber prior work.
func existingContent(dir string, tree []string) string {
	sort.Strings(tree)
	var b strings.Builder
	used := 0
	for _, rel := range tree {
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		if used+len(data) > maxContextBytes {
			b.WriteString("\n(remaining files omitted for length)\n")
			break
		}
		used += len(data)
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", rel, data)
	}
	return b.String()
}

func taskTitles(tasks []factory.PlanTask) string {
	if len(tasks) == 0 {
		return "implement assigned work"
	}
	titles := make([]string, len(tasks))
	for i, t := range tasks {
		titles[i] = t.Title
	}
	return strings.Join(titles, "; ")
}

func shortList(xs []string) []string {
	if len(xs) <= 6 {
		return xs
	}
	return append(xs[:6:6], "…")
}
