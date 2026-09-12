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
// When the client supports it (see AgenticCompleter), the model makes its
// changes directly via write_file/edit_file tool calls as it goes — files
// land on disk incrementally, and its final answer is just a prose summary.
// Otherwise (the fallback path) files come back in an escaping-free block
// format ("=== FILE: <path> ===" … "=== END FILE: <path> ==="), not a JSON
// array: a model cannot corrupt raw file bytes with an unescaped newline or
// quote, and an unterminated final block is a detectable truncation rather
// than an unparseable blob. A legacy JSON array and a bare "NO CHANGES" are
// still accepted on that path.
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
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

const maxContextBytes = 40_000

// Completer is the slice of *llm.Client the executor needs — a seam for tests.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// AgenticCompleter is a Completer that can also run a bounded tool-use loop.
// The real *llm.Client satisfies this automatically (it has both methods); a
// test stub implementing only Completer does not, and is used exactly as
// before (the block-format fallback). Any pass — first build or fix pass
// alike — uses this when available, so the model can investigate the actual
// repo/history and make/verify its changes directly via tools, rather than
// only the flattened current-file snapshot and one final parsed text blob.
type AgenticCompleter interface {
	Completer
	CompleteAgentic(ctx context.Context, system, user string, tools []llm.Tool, exec llm.ToolExecFunc, maxTurns int) (string, error)
}

// maxDevToolTurns bounds an agentic pass's tool-use loop (0 would fall back
// to llm.Client's own default). A turn here can be "run_tests, read a
// failure, edit_file, run_build again" rather than a quick read, and a
// from-scratch build doing this needs more room than a fix pass checking one
// finding — hence a larger budget than the old read-only-investigation loop.
const maxDevToolTurns = 16

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

// outputContractAgentic replaces outputContract when the model has
// write_file/edit_file tools: changes land on disk as the model calls them,
// not as a final block dump, so the final answer is a short prose summary.
const outputContractAgentic = "\n" + `Make your changes directly using the write_file and edit_file tools as you
go — do not describe them in your final answer. Use write_file for a new
file or a full rewrite; prefer edit_file for a small, targeted change to an
existing file, so you don't clobber unrelated content.

- Include everything needed to build and run: source, config, go.mod /
  package.json, tests.
- If you create a runnable service, include a multi-stage Dockerfile for it
  that builds and runs cleanly, and a /healthz (or equivalent) endpoint.
- Before finishing, consider using run_build (and run_tests, if the change
  has tests) to verify your change actually compiles and passes.
- If (and only if) there is genuinely nothing to change, do not call
  write_file or edit_file at all.

When you are done, reply with a short prose summary (a few sentences) of what
you changed and why — no file blocks, no JSON, no markdown fences.`

// agenticToolsNote lists the tools available on an agentic pass, appended
// once regardless of attempt (a from-scratch build and a fix pass share the
// same tool set).
const agenticToolsNote = `You have tools available: read_file, git_log, git_diff, grep (read-only, to
investigate before changing anything), and write_file, edit_file, run_build,
run_tests (to make and verify your changes). Investigate only as much as you
need; prefer edit_file over write_file for existing files so you don't
clobber unrelated content.

`

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

// dispatchAndWrite gets the model's changes for one prompt and gets them onto
// disk: via write_file/edit_file tool calls as they happen (agentic — files
// are already written by the time this returns; this just drives the loop),
// or by parsing the model's final text into file blocks and writing them in
// one shot (the non-agentic fallback, unchanged). The returned *truncatedError
// is non-nil only on the non-agentic path — the agentic path has nothing to
// truncate, since writes land incrementally rather than in one final blob.
//
// touched is returned even when err != nil, so a caller can still commit
// whatever a mid-loop failure already wrote to the workspace directory
// (reused across a run's attempts) instead of leaving it dirty and
// uncommitted for the next attempt to trip over.
func dispatchAndWrite(ctx context.Context, client Completer, systemPrompt, user, stage string, repo *workspace.Repo, o options) (touched []string, out string, trunc *truncatedError, err error) {
	if ac, ok := client.(AgenticCompleter); ok {
		touched, out, err = completeFilesAgentic(ctx, ac, systemPrompt, user, stage, repo)
		if o.postWrite != nil && len(touched) > 0 {
			var pwErr error
			if touched, pwErr = postWrite(o.postWrite, repo.Dir, touched); pwErr != nil && err == nil {
				err = fmt.Errorf("devagent(%s): post-write: %w", stage, pwErr)
			}
		}
		return touched, out, nil, err
	}

	files, out, ferr := completeFiles(ctx, client, systemPrompt, user, stage)
	errors.As(ferr, &trunc)
	if trunc != nil {
		ferr = nil
	}
	if ferr != nil {
		return nil, "", nil, ferr
	}
	if len(files) == 0 {
		return nil, out, trunc, nil
	}
	written, werr := repo.WriteFiles(files)
	if werr != nil {
		return nil, "", nil, werr
	}
	if o.postWrite != nil {
		if written, werr = postWrite(o.postWrite, repo.Dir, written); werr != nil {
			return nil, "", nil, fmt.Errorf("devagent(%s): post-write: %w", stage, werr)
		}
	}
	return written, out, trunc, nil
}

// commitTouched force-stages and commits whatever touched names — a
// .gitignore the model authored in this same pass must not be able to
// silently drop our own source files. It is a no-op when touched is empty.
func commitTouched(ctx context.Context, repo *workspace.Repo, role, msg string, touched []string) (string, error) {
	if len(touched) == 0 {
		return "", nil
	}
	if err := repo.AddPaths(ctx, touched); err != nil {
		return "", err
	}
	return repo.Commit(ctx, role, msg)
}

// runOnce asks the model for every file in one call and makes one commit,
// using the agentic tool-use path when the client supports it (see
// AgenticCompleter).
func runOnce(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, repo *workspace.Repo, o options) (factory.ResultEnvelope, error) {
	tree, _ := repo.Tree(ctx)
	_, agentic := client.(AgenticCompleter)
	user := buildPrompt(ctx, env, repo, tree, agentic)

	touched, out, trunc, err := dispatchAndWrite(ctx, client, systemPrompt, user, env.Stage, repo, o)
	// Commit whatever landed even if the call above ultimately errored (e.g. a
	// mid-loop transport failure) — see dispatchAndWrite's doc comment.
	var sha string
	if len(touched) > 0 {
		var cerr error
		if sha, cerr = commitTouched(ctx, repo, env.Stage, taskTitles(env.Tasks), touched); cerr != nil && err == nil {
			err = cerr
		}
	}
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	if len(touched) == 0 {
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
			"devagent(%s): model made no changes (said %q)", env.Stage, head(out, 200))
	}
	summary := fmt.Sprintf("wrote %d files (%s)", len(touched), strings.Join(shortList(touched), ", "))
	if agentic && strings.TrimSpace(out) != "" {
		summary += ": " + head(out, 300)
	}
	if trunc != nil {
		summary += fmt.Sprintf("; model output cut off before %s — expect a build-gate finding", trunc.lastPath)
	}
	return factory.ResultEnvelope{
		Role:         env.Stage,
		Summary:      summary,
		CommitSHA:    sha,
		FilesWritten: touched,
	}, nil
}

// runBatched dispatches env.Tasks one at a time: each task gets its own model
// call (scoped to that task, with the earlier tasks' commits visible in the
// repo snapshot) and its own commit.
func runBatched(ctx context.Context, client Completer, systemPrompt string, env factory.DispatchEnvelope, repo *workspace.Repo) (factory.ResultEnvelope, error) {
	_, agentic := client.(AgenticCompleter)
	seen := map[string]bool{}
	var allWritten []string
	var lastSHA string
	var truncNote string

	for i, t := range env.Tasks {
		tree, _ := repo.Tree(ctx)
		user := buildBatchPrompt(env, repo.Dir, tree, t, taskLabels(env.Tasks[:i]), taskLabels(env.Tasks[i+1:]), agentic)

		touched, _, trunc, err := dispatchAndWrite(ctx, client, systemPrompt, user, env.Stage, repo, options{})
		if trunc != nil {
			truncNote = fmt.Sprintf("; task %s cut off before %s — expect a build-gate finding", t.ID, trunc.lastPath)
		}
		if len(touched) > 0 {
			var sha string
			var cerr error
			if sha, cerr = commitTouched(ctx, repo, env.Stage, t.Title, touched); cerr != nil && err == nil {
				err = cerr
			} else if sha != "" {
				lastSHA = sha
			}
		}
		if err != nil {
			return factory.ResultEnvelope{}, err
		}
		if len(touched) == 0 {
			log.Printf("devagent(%s): task %s (%s) produced no changes — skipping", env.Stage, t.ID, t.Title)
			continue
		}
		for _, w := range touched {
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

func buildPrompt(ctx context.Context, env factory.DispatchEnvelope, repo *workspace.Repo, tree []string, agentic bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PRD\n\n%s\n\n", env.PRDText)
	if env.Plan != "" {
		fmt.Fprintf(&b, "# Implementation plan\n\n%s\n\n", env.Plan)
	}
	if env.UISpec != "" {
		fmt.Fprintf(&b, "# UI/UX spec\n\n%s\n\n", env.UISpec)
	}
	if env.Attempt > 0 && len(env.Findings) > 0 {
		fmt.Fprintf(&b, "# Fix pass (attempt %d)\n\nThe previous version was reviewed. A finding's title/suggestion is the\nreviewer's paraphrase, not ground truth — before changing anything, check it\nagainst the evidence quoted below and against the referenced file/line as they\nactually are right now. If the referenced code already does what a finding\nasks, that finding's premise is likely wrong or stale: the real cause is\nprobably elsewhere (a different component, an interaction/state bug) — trace\nit rather than re-editing code that's already correct. Once you're confident\nyou've found the real cause, fix it with the smallest change that does the\njob; do not rewrite files or components beyond what's needed, and keep\neverything else working.\nA `gosec` finding that is a genuine false positive for this PRD may be resolved\nwith a `// #nosec Gxxx -- <reason>` comment on the flagged line instead of a code\nchange — see your role prompt.\n\n", env.Attempt+1)
		for _, f := range env.Findings {
			fmt.Fprintf(&b, "- [%s]%s %s:%d — %s\n", f.Severity, findingTag(f), f.File, f.Line, f.Title)
			if f.Suggestion != "" {
				fmt.Fprintf(&b, "  fix: %s\n", f.Suggestion)
			}
			if f.Evidence != "" {
				fmt.Fprintf(&b, "  evidence (verbatim from the actual failure — check the title/fix above actually match this before acting on them):\n    %s\n",
					strings.ReplaceAll(strings.TrimSpace(f.Evidence), "\n", "\n    "))
			}
			if f.File != "" {
				if log, err := repo.LogPath(ctx, f.File, 5); err == nil && log != "" {
					fmt.Fprintf(&b, "  history of %s this run (newest first — has a previous attempt already touched this?):\n    %s\n",
						f.File, strings.ReplaceAll(log, "\n", "\n    "))
				}
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
	writeRepoSnapshot(&b, repo.Dir, tree)
	b.WriteString("\n")
	if agentic {
		b.WriteString(agenticToolsNote)
		b.WriteString(outputContractAgentic)
	} else {
		b.WriteString(outputContract)
	}
	return b.String()
}

// buildBatchPrompt is buildPrompt scoped to a single task: the current task is
// the only work to do this pass, the other tasks are listed only so the model
// neither repeats nor pre-empts them.
func buildBatchPrompt(env factory.DispatchEnvelope, dir string, tree []string, cur factory.PlanTask, earlier, later []string, agentic bool) string {
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
	if agentic {
		b.WriteString(agenticToolsNote)
		b.WriteString(outputContractAgentic)
		b.WriteString("\n- Only touch files this task needs. Files from earlier tasks are already\n  committed — change one only if this task needs to.")
	} else {
		b.WriteString(outputContract)
		b.WriteString("\n- Return only the files THIS task needs. Files from earlier tasks are\n  already committed — include one only if this task changes it.")
	}
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

// rawFailuresPrefix is build-gate's raw-failure-log directory (see
// factory.RawFailuresDir) plus a trailing separator, so existingContent can
// recognise paths under it with a plain prefix check.
var rawFailuresPrefix = factory.RawFailuresDir + "/"

// existingContent inlines the current files up to a byte budget so the agent can
// extend rather than clobber prior work. Build-gate's raw failure logs are
// deliberately excluded: they're for on-demand reading (a human browsing the
// branch, or the agentic fix-pass's read_file tool), not for being stuffed
// into every prompt regardless of relevance — they can be large and would
// otherwise crowd real source files out of the byte budget.
func existingContent(dir string, tree []string) string {
	sort.Strings(tree)
	var b strings.Builder
	used := 0
	for _, rel := range tree {
		if strings.HasPrefix(rel, rawFailuresPrefix) {
			continue
		}
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
	return completeFilesWith(stage, user, func(u string) (string, error) {
		return client.Complete(ctx, system, u)
	})
}

// completeFilesAgentic runs one bounded tool-use loop with the full developer
// tool set (see devTools/toolExecFunc): the model makes its changes directly
// via write_file/edit_file as it goes — not via file blocks — optionally
// verifying with run_build/run_tests, and its final text is a prose summary,
// not a parseable file list. Returns the repo-relative paths the tool calls
// actually wrote/edited (sorted, deduped) and that final text.
//
// touched reflects everything written so far even when err != nil (a
// genuine API/transport failure mid-loop) — see dispatchAndWrite, which
// relies on this to avoid discarding real work already on disk. Note that
// exhausting the turn budget is not such a failure: CompleteAgentic
// withdraws its tools on the last turn, forcing the model to answer in text
// instead of erroring out, so this essentially always succeeds by
// construction.
func completeFilesAgentic(ctx context.Context, client AgenticCompleter, system, user, stage string, repo *workspace.Repo) ([]string, string, error) {
	state := newToolState()
	out, err := client.CompleteAgentic(ctx, system, user, devTools(), toolExecFunc(repo, state), maxDevToolTurns)
	return state.touched(), out, err
}

// completeFilesWith drives one completion (plain or agentic — complete does
// the actual model call) and retries once when the reply is neither the block
// format, a JSON array, nor the "NO CHANGES" sentinel. Models occasionally
// answer a fix-pass prompt in prose ("the findings are already addressed");
// one strict retry is much cheaper than failing the run.
//
// A *truncatedError is returned alongside a non-empty file map when the reply
// was cut off mid-block — the caller commits what arrived and moves on.
func completeFilesWith(stage, user string, complete func(u string) (string, error)) (map[string]string, string, error) {
	out, err := complete(user)
	if err != nil {
		return nil, "", err
	}
	if files, txt, e := interpret(out, stage); files != nil || e != nil {
		return files, txt, e
	}
	log.Printf("devagent(%s): unusable reply (%d bytes, starts %q) — retrying once",
		stage, len(out), head(out, 120))

	retry, err := complete(user + "\n\n" + retryContract)
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
