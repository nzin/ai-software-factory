// Package devagent is the shared executor for the code-writing agents
// (backend-developer, frontend-developer, mobile-developer, test-engineer). They
// differ only in their prompt file; the executor is identical: read the worktree,
// ask the model for a set of files, write and commit them.
//
// A developer's first pass is dispatched one planner task at a time — one model
// call and one git commit per task — so no single call has to emit a whole
// codebase (which truncates the reply). Fix passes and the test-engineer run as
// a single call.
//
// Files come back in an escaping-free block format ("=== FILE: <path> ===" …
// "=== END FILE: <path> ==="), not a JSON array: a model cannot corrupt raw file
// bytes with an unescaped newline or quote, and an unterminated final block is a
// detectable truncation rather than an unparseable blob. A legacy JSON array and
// a bare "NO CHANGES" are still accepted.
package devagent

import (
	"context"
	"errors"
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

const outputContract = "\n" + `Return ONLY the files to write, each as a block. No prose, no JSON, no markdown
fences around the blocks:

=== FILE: relative/path.go ===
<full file contents, verbatim>
=== END FILE: relative/path.go ===
=== FILE: relative/other.go ===
<full file contents, verbatim>
=== END FILE: relative/other.go ===

Rules:
- The content between the markers is copied to disk exactly as written — do NOT
  escape newlines or quotes, do NOT wrap it in backticks. Just paste the file.
- The "=== FILE: <path> ===" and "=== END FILE: <path> ===" lines must each be on
  their own line and name the same path.
- Full file contents, not diffs. Paths are relative to the repo root.
- Include everything needed to build and run: source, config, go.mod / package.json, tests.
- If you create a runnable service, include a multi-stage Dockerfile for it that
  builds and runs cleanly, and a /healthz (or equivalent) endpoint.
- Do not delete files. Overwrite by providing the same path.
- Keep it minimal but complete for the assigned tasks.
- If (and only if) there is genuinely nothing to change, answer with exactly the
  line: NO CHANGES — never with prose.`

// PostWriteFunc runs over the worktree after a single-call pass has written the
// model's files and before they are committed. What it writes lands in the same
// commit; what it removes is committed as a deletion. The test-engineer uses it
// to lay down the factory-owned test harness (testharness.Materialize).
type PostWriteFunc func(dir string) (written, removed []string, err error)

// Option customises an Executor.
type Option func(*options)

type options struct {
	postWrite PostWriteFunc
}

// WithPostWrite runs f after the model's files are written (see PostWriteFunc).
func WithPostWrite(f PostWriteFunc) Option {
	return func(o *options) { o.postWrite = f }
}

// postWrite runs f and folds its changes into the model's written list: its
// writes join the list and its removals leave it (force-adding a path that no
// longer exists, and was never tracked, is a git error).
func postWrite(f PostWriteFunc, dir string, written []string) ([]string, error) {
	extra, removed, err := f(dir)
	if err != nil {
		return written, err
	}
	gone := map[string]bool{}
	for _, p := range removed {
		gone[p] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range append(written, extra...) {
		if !gone[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Executor builds the developer executor for a given role prompt.
func Executor(client Completer, systemPrompt string, opts ...Option) a2asrv.AgentExecutor {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return agentkit.DispatchExecutor(func(ctx context.Context, env factory.DispatchEnvelope) (factory.ResultEnvelope, error) {
		return run(ctx, client, systemPrompt, env, o)
	})
}

// run opens the worktree and dispatches the work: one model call per task on a
// first developer pass with more than one task, a single call otherwise (fix
// passes, the test-engineer, single/zero-task roles).
func run(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, o options) (factory.ResultEnvelope, error) {
	repo, err := workspace.Open(ctx, env.WorkspaceDir)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	repo.BaseBranch = env.BaseBranch

	if env.Attempt == 0 && factory.IsDeveloperRole(env.Stage) && len(env.Tasks) > 1 {
		return runBatched(ctx, client, systemPrompt, env, repo)
	}
	return runOnce(ctx, client, systemPrompt, env, repo, o)
}

// runOnce asks the model for every file in one call and makes one commit.
func runOnce(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, repo *workspace.Repo, o options) (factory.ResultEnvelope, error) {
	tree, _ := repo.Tree(ctx)
	user := buildPrompt(env, repo.Dir, tree)

	files, out, err := completeFiles(ctx, client, systemPrompt, user, env.Stage)
	var trunc *truncatedError
	if errors.As(err, &trunc) {
		err = nil // usable partial: commit what arrived, note it below
	}
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
	if o.postWrite != nil {
		if written, err = postWrite(o.postWrite, repo.Dir, written); err != nil {
			return factory.ResultEnvelope{}, fmt.Errorf("devagent(%s): post-write: %w", env.Stage, err)
		}
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
	summary := fmt.Sprintf("wrote %d files (%s)", len(written), strings.Join(shortList(written), ", "))
	if trunc != nil {
		summary += fmt.Sprintf("; model output cut off before %s — expect a build-gate finding", trunc.lastPath)
	}
	return factory.ResultEnvelope{
		Role:         env.Stage,
		Summary:      summary,
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
	var truncNote string

	for i, t := range env.Tasks {
		tree, _ := repo.Tree(ctx)
		user := buildBatchPrompt(env, repo.Dir, tree, t, taskLabels(env.Tasks[:i]), taskLabels(env.Tasks[i+1:]))

		files, _, err := completeFiles(ctx, client, systemPrompt, user, env.Stage)
		var trunc *truncatedError
		if errors.As(err, &trunc) {
			truncNote = fmt.Sprintf("; task %s cut off before %s — expect a build-gate finding", t.ID, trunc.lastPath)
			err = nil
		}
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
		Summary: fmt.Sprintf("%d tasks, wrote %d files (%s)%s",
			len(env.Tasks), len(allWritten), strings.Join(shortList(allWritten), ", "), truncNote),
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
Your previous answer was not in the "=== FILE: <path> ===" block format and
could not be used.

Reply with ONLY the file blocks described above — no prose, no JSON, no markdown
fences. If nothing needs to change, reply with exactly the line: NO CHANGES`

// truncatedError marks a model reply that opened a file block but was cut off
// before closing it. It is not a hard failure: the complete files that arrived
// before the cut-off are still usable, and the missing one surfaces at the
// build gate as a normal fix-pass finding.
type truncatedError struct {
	stage    string
	lastPath string
	n        int
}

func (e *truncatedError) Error() string {
	return fmt.Sprintf("devagent(%s): model output cut off before %s (%d complete files kept)",
		e.stage, e.lastPath, e.n)
}

// completeFiles asks the model for the file list, retrying once when the reply
// is neither the block format, a JSON array, nor the "NO CHANGES" sentinel. The
// models occasionally answer a fix-pass prompt in prose ("the findings are
// already addressed"); one strict retry is much cheaper than failing the run.
//
// A *truncatedError is returned alongside a non-empty file map when the reply
// was cut off mid-block — the caller commits what arrived and moves on.
func completeFiles(ctx context.Context, client Completer, system, user, stage string) (map[string]string, string, error) {
	out, err := client.Complete(ctx, system, user)
	if err != nil {
		return nil, "", err
	}
	if files, txt, e := interpret(out, stage); files != nil || e != nil {
		return files, txt, e
	}
	log.Printf("devagent(%s): unusable reply (%d bytes, starts %q) — retrying once",
		stage, len(out), head(out, 120))

	retry, err := client.Complete(ctx, system, user+"\n\n"+retryContract)
	if err != nil {
		return nil, "", err
	}
	if files, txt, e := interpret(retry, stage); files != nil || e != nil {
		return files, txt, e
	}
	if dbg := os.Getenv("ASF_DEBUG_DIR"); dbg != "" {
		_ = os.WriteFile(filepath.Join(dbg, "devagent-"+stage+"-raw.txt"),
			[]byte(out+"\n\n===== RETRY =====\n\n"+retry), 0o644)
	}
	return nil, retry, fmt.Errorf(
		"devagent(%s): could not parse file list after a retry (%d bytes, starts %q)",
		stage, len(retry), head(retry, 200))
}

// interpret turns one model reply into a file set:
//   - files != nil, err == nil  -> usable (an empty map means "nothing to do")
//   - files != nil, err != nil  -> usable partial (*truncatedError)
//   - files == nil, err == nil  -> unusable; the caller should retry
func interpret(out, stage string) (map[string]string, string, error) {
	if blocks, res := factory.ParseFileBlocks(out); res.Count > 0 || res.Truncated {
		if res.Truncated {
			return blocks, out, &truncatedError{stage: stage, lastPath: res.LastPath, n: res.Count}
		}
		return blocks, out, nil
	}
	if isNoChanges(out) {
		return map[string]string{}, out, nil
	}
	if specs, err := factory.ParseFileSpecs(out); err == nil { // legacy JSON array
		return specs, out, nil
	}
	return nil, out, nil
}

func isNoChanges(s string) bool {
	return strings.EqualFold(strings.Trim(strings.TrimSpace(s), "`*_ \n\t"), "NO CHANGES")
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
