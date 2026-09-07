// Package devagent is the shared executor for the code-writing agents
// (backend-developer, frontend-developer, mobile-developer). They differ only in
// their prompt file; the executor is identical: read the worktree, ask the model
// for a set of files, write and commit them.
package devagent

import (
	"context"
	"fmt"
	"log"
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
- Keep it minimal but complete for the assigned tasks.
- Answer with the JSON array and nothing else. If (and only if) there is genuinely
  nothing to change, answer with exactly [] — never with prose.`

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

		files, out, err := completeFiles(ctx, client, systemPrompt, user, env.Stage)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		if len(files) == 0 {
			// On a fix pass "nothing to change" is a legitimate answer — the
			// reviewer's findings may already be addressed, or judged noise. Let
			// the run advance to the reviewers instead of failing it. On the
			// first pass the agent was asked to build something, so producing
			// nothing is a real failure.
			if env.Attempt > 0 {
				return factory.ResultEnvelope{
					Role:    env.Stage,
					Summary: "no changes needed: " + head(out, 300),
				}, nil
			}
			return factory.ResultEnvelope{}, fmt.Errorf(
				"devagent(%s): model returned no files (said %q)", env.Stage, head(out, 200))
		}
		written, err := repo.WriteFiles(files)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		// Force-stage what we just wrote: a .gitignore the model authored in this
		// same pass must not be able to silently drop our own source files.
		if err := repo.AddPaths(ctx, written); err != nil {
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

// retryContract nudges a model that answered in prose back to the wire format.
const retryContract = `
Your previous answer was not a JSON array and could not be used.

Reply with ONLY the JSON array described above — no prose, no explanation, no
markdown outside the fenced block. If nothing needs to change, reply with
exactly: []`

// completeFiles asks the model for the file list, retrying once when the reply
// is not parseable. The models occasionally answer a fix-pass prompt in prose
// ("the findings are already addressed") instead of the agreed JSON; one strict
// retry is much cheaper than failing the whole run.
func completeFiles(ctx context.Context, client *llm.Client, system, user, stage string) (map[string]string, string, error) {
	out, err := client.Complete(ctx, system, user)
	if err != nil {
		return nil, "", err
	}
	files, parseErr := factory.ParseFileSpecs(out)
	if parseErr == nil {
		return files, out, nil
	}
	log.Printf("devagent(%s): unparseable reply (%d bytes, starts %q) — retrying once",
		stage, len(out), head(out, 120))

	retry, err := client.Complete(ctx, system, user+"\n\n"+retryContract)
	if err != nil {
		return nil, "", err
	}
	files, retryErr := factory.ParseFileSpecs(retry)
	if retryErr == nil {
		return files, retry, nil
	}
	if dbg := os.Getenv("ASF_DEBUG_DIR"); dbg != "" {
		_ = os.WriteFile(filepath.Join(dbg, "devagent-"+stage+"-raw.txt"),
			[]byte(out+"\n\n===== RETRY =====\n\n"+retry), 0o644)
	}
	return nil, retry, fmt.Errorf(
		"devagent(%s): could not parse file list after a retry (%d bytes, starts %q): %w",
		stage, len(retry), head(retry, 200), retryErr)
}

func head(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
