// Package devagent is the shared executor for the code-writing agents
// (backend-developer, frontend-developer, mobile-developer, test-engineer). They
// differ only in their prompt file; the executor is identical: read the worktree,
// ask the model for a set of files, write and commit them.
//
// A developer's first pass is dispatched one planner task at a time — one model
// call and one git commit per task — so no single call has to emit a whole
// codebase (which truncates the JSON reply). Fix passes and the test-engineer
// run as a single call.
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
	"github.com/nzin/ai-software-factory/internal/workspace"
)

const maxContextBytes = 40_000

// Completer is the slice of *llm.Client the executor needs — a seam for tests.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

const outputContract = `
Return ONLY a JSON array of files to write, no prose:

[
  { "path": "relative/path.go", "content": "<full file contents>" },
  ...
]

Rules:
- Full file contents, not diffs. Paths are relative to the repo root.
- Include everything needed to build and run: source, config, go.mod / package.json, tests.
- If you create a runnable service, include a multi-stage Dockerfile for it that
  builds and runs cleanly, and a /healthz (or equivalent) endpoint.
- Do not delete files. Overwrite by providing the same path.
- Keep it minimal but complete for the assigned tasks.
- Answer with the JSON array and nothing else. If (and only if) there is genuinely
  nothing to change, answer with exactly [] — never with prose.`

// Executor builds the developer executor for a given role prompt.
func Executor(client Completer, systemPrompt string) a2asrv.AgentExecutor {
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		return run(ctx, client, systemPrompt, env)
	})
}

// run opens the worktree and dispatches the work: one model call per task on a
// first developer pass with more than one task, a single call otherwise (fix
// passes, the test-engineer, single/zero-task roles).
func run(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
	repo, err := workspace.Open(ctx, env.WorkspaceDir)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	repo.BaseBranch = env.BaseBranch

	if env.Attempt == 0 && factory.IsDeveloperRole(env.Stage) && len(env.Tasks) > 1 {
		return runBatched(ctx, client, systemPrompt, env, repo)
	}
	return runOnce(ctx, client, systemPrompt, env, repo)
}

// runOnce asks the model for every file in one call and makes one commit.
func runOnce(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, repo *workspace.Repo) (factory.ResultEnvelope, error) {
	tree, _ := repo.Tree(ctx)
	user := buildPrompt(env, repo.Dir, tree)

	files, out, err := completeFiles(ctx, client, systemPrompt, user, env.Stage)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	if len(files) == 0 {
		// "Nothing to do" is legitimate for a fix pass (findings already
		// addressed or judged noise) and for the test-engineer against a
		// library / docs-only change (there is no runnable service to test).
		// A developer's first pass, though, was asked to build something —
		// producing nothing there is a real failure.
		if env.Attempt > 0 || !factory.IsDeveloperRole(env.Stage) {
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
}

// runBatched dispatches env.Tasks one at a time: each task gets its own model
// call (scoped to that task, with the earlier tasks' commits visible in the
// repo snapshot) and its own commit.
func runBatched(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, repo *workspace.Repo) (factory.ResultEnvelope, error) {
	seen := map[string]bool{}
	var allWritten []string
	var lastSHA string

	for i, t := range env.Tasks {
		tree, _ := repo.Tree(ctx)
		user := buildBatchPrompt(env, repo.Dir, tree, t, taskLabels(env.Tasks[:i]), taskLabels(env.Tasks[i+1:]))

		files, _, err := completeFiles(ctx, client, systemPrompt, user, env.Stage)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		if len(files) == 0 {
			log.Printf("devagent(%s): task %s (%s) produced no files — skipping", env.Stage, t.ID, t.Title)
			continue
		}
		written, err := repo.WriteFiles(files)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		if err := repo.AddPaths(ctx, written); err != nil {
			return factory.ResultEnvelope{}, err
		}
		sha, err := repo.Commit(ctx, env.Stage, t.Title)
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		if sha != "" {
			lastSHA = sha
		}
		for _, w := range written {
			if !seen[w] {
				seen[w] = true
				allWritten = append(allWritten, w)
			}
		}
	}

	if len(allWritten) == 0 {
		return factory.ResultEnvelope{}, fmt.Errorf(
			"devagent(%s): model returned no files across %d tasks", env.Stage, len(env.Tasks))
	}
	sort.Strings(allWritten)
	return factory.ResultEnvelope{
		Role: env.Stage,
		Summary: fmt.Sprintf("%d tasks, wrote %d files (%s)",
			len(env.Tasks), len(allWritten), strings.Join(shortList(allWritten), ", ")),
		CommitSHA:    lastSHA,
		FilesWritten: allWritten,
	}, nil
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
		fmt.Fprintf(&b, "# Fix pass (attempt %d)\n\nThe previous version was reviewed. Fix these findings; keep everything else working.\nA `gosec` finding that is a genuine false positive for this PRD may be resolved\nwith a `// #nosec Gxxx -- <reason>` comment on the flagged line instead of a code\nchange — see your role prompt.\n\n", env.Attempt+1)
		for _, f := range env.Findings {
			fmt.Fprintf(&b, "- [%s]%s %s:%d — %s\n", f.Severity, findingTag(f), f.File, f.Line, f.Title)
			if f.Suggestion != "" {
				fmt.Fprintf(&b, "  fix: %s\n", f.Suggestion)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("# Your tasks\n\n")
	if len(env.Tasks) == 0 {
		b.WriteString("(no explicit task list — derive your work from the PRD, the plan, and the current repository as your role prompt describes)\n")
	}
	for _, t := range env.Tasks {
		fmt.Fprintf(&b, "- [%s] %s\n", t.ID, t.Title)
		if t.Details != "" {
			fmt.Fprintf(&b, "  %s\n", t.Details)
		}
	}
	b.WriteString("\n# Current repository\n\n")
	writeRepoSnapshot(&b, dir, tree)
	b.WriteString("\n")
	b.WriteString(outputContract)
	return b.String()
}

// buildBatchPrompt is buildPrompt scoped to a single task: the current task is
// the only work to do this pass, the other tasks are listed only so the model
// neither repeats nor pre-empts them.
func buildBatchPrompt(env factory.DispatchEnvelope, dir string, tree []string, cur factory.PlanTask, earlier, later []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PRD\n\n%s\n\n", env.PRDText)
	if env.Plan != "" {
		fmt.Fprintf(&b, "# Implementation plan\n\n%s\n\n", env.Plan)
	}
	if env.UISpec != "" {
		fmt.Fprintf(&b, "# UI/UX spec\n\n%s\n\n", env.UISpec)
	}
	fmt.Fprintf(&b, "# Your task (this pass)\n\n- [%s] %s\n", cur.ID, cur.Title)
	if cur.Details != "" {
		fmt.Fprintf(&b, "  %s\n", cur.Details)
	}
	if len(earlier) > 0 || len(later) > 0 {
		b.WriteString("\n# Other tasks — do NOT implement now\n\n")
		for _, s := range earlier {
			fmt.Fprintf(&b, "- %s (already committed; visible in the repository below)\n", s)
		}
		for _, s := range later {
			fmt.Fprintf(&b, "- %s (handled in a later pass)\n", s)
		}
	}
	b.WriteString("\n# Current repository\n\n")
	writeRepoSnapshot(&b, dir, tree)
	b.WriteString("\n")
	b.WriteString(outputContract)
	b.WriteString("\n- Return only the files THIS task needs. Files from earlier tasks are\n  already committed — include one only if this task changes it.")
	return b.String()
}

func writeRepoSnapshot(b *strings.Builder, dir string, tree []string) {
	if len(tree) == 0 {
		b.WriteString("(empty)\n")
		return
	}
	b.WriteString(strings.Join(tree, "\n"))
	b.WriteString("\n\n")
	b.WriteString(existingContent(dir, tree))
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
func completeFiles(ctx context.Context, client Completer, system, user, stage string) (map[string]string, string, error) {
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

// findingTag renders the source and rule/category of a finding as " (gosec G404)"
// so the developer can tell a tool finding (and its rule ID, needed for a
// `// #nosec Gxxx` suppression) from a hand-written review comment. Empty when
// neither is set.
func findingTag(f factory.Finding) string {
	switch {
	case f.Source != "" && f.Category != "":
		return " (" + f.Source + " " + f.Category + ")"
	case f.Source != "":
		return " (" + f.Source + ")"
	case f.Category != "":
		return " (" + f.Category + ")"
	default:
		return ""
	}
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

// taskLabels renders "[T2] title" for each task, for the "other tasks" list in a
// batched prompt.
func taskLabels(tasks []factory.PlanTask) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = fmt.Sprintf("[%s] %s", t.ID, t.Title)
	}
	return out
}

func shortList(xs []string) []string {
	if len(xs) <= 6 {
		return xs
	}
	return append(xs[:6:6], "…")
}
