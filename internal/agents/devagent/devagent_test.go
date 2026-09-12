package devagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// fakeCompleter returns a canned reply per call and records the prompts it saw.
// It implements only Completer (not AgenticCompleter) — a fix pass given one
// of these must fall back to the plain, non-tool-using path.
type fakeCompleter struct {
	replies []string
	calls   []string
}

func (f *fakeCompleter) Complete(_ context.Context, _, user string) (string, error) {
	f.calls = append(f.calls, user)
	i := len(f.calls) - 1
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	return "[]", nil
}

// fakeAgenticCompleter additionally implements CompleteAgentic: it calls the
// "read_file" tool once (recording what it got back), then — for each entry
// in writes — calls "write_file" via the exec func it was given, so a test
// can assert an agentic pass actually took the tool-use path, that its tool
// exec function works against the real repo, and that a real write_file call
// lands on disk and gets committed. Its final reply is plain prose (a
// summary), matching what a real agentic completion returns.
type fakeAgenticCompleter struct {
	fakeCompleter
	agenticCalls int
	gotTools     []llm.Tool
	toolResult   string
	toolErr      error
	writes       map[string]string   // path -> content, written via write_file on every call (unless writesSeq is set)
	writesSeq    []map[string]string // when set, call N (0-indexed) writes writesSeq[N] instead of writes — for a multi-call test where each call must touch something different
	afterWrite   error               // if set, CompleteAgentic returns this error after making the writes
}

func (f *fakeAgenticCompleter) CompleteAgentic(ctx context.Context, _, user string, tools []llm.Tool, exec llm.ToolExecFunc, _ int) (string, error) {
	f.agenticCalls++
	f.gotTools = tools
	writes := f.writes
	if f.writesSeq != nil {
		writes = nil
		if idx := f.agenticCalls - 1; idx < len(f.writesSeq) {
			writes = f.writesSeq[idx]
		}
	}
	if exec != nil {
		f.toolResult, f.toolErr = exec(ctx, "read_file", json.RawMessage(`{"path":"main.go"}`))
		for path, content := range writes {
			input, err := json.Marshal(map[string]string{"path": path, "content": content})
			if err != nil {
				return "", err
			}
			if _, err := exec(ctx, "write_file", input); err != nil {
				return "", err
			}
		}
	}
	if f.afterWrite != nil {
		return "", f.afterWrite
	}
	f.calls = append(f.calls, user)
	i := len(f.calls) - 1
	if i < len(f.replies) {
		return f.replies[i], nil
	}
	return "done", nil
}

func newRepo(t *testing.T) *workspace.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := workspace.NewManager(t.TempDir())
	repo, err := m.Prepare(context.Background(), "run-dev1", workspace.ParseRepoURL("", "main"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return repo
}

func commitSubjects(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestRunBatchedOneCommitPerTask(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage:   factory.RoleBackendDeveloper,
		PRDText: "build a quote service",
		Tasks: []factory.PlanTask{
			{ID: "T1", Title: "scaffold the module"},
			{ID: "T2", Title: "add quotes endpoint"},
			{ID: "T3", Title: "add health endpoint"},
		},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: go.mod ===\nmodule quotes\n\ngo 1.22\n=== END FILE: go.mod ===\n",
		// one legacy JSON reply among the blocks — the fallback must still parse it
		`[{"path":"quotes.go","content":"package main\n\nfunc quotes() {}\n"}]`,
		"=== FILE: health.go ===\npackage main\n\nfunc health() {}\n=== END FILE: health.go ===\n",
	}}

	res, err := runBatched(ctx, fc, "sys", env, repo)
	if err != nil {
		t.Fatalf("runBatched: %v", err)
	}

	if len(fc.calls) != 3 {
		t.Fatalf("model calls = %d, want 3", len(fc.calls))
	}
	if got := strings.Join(res.FilesWritten, ","); got != "go.mod,health.go,quotes.go" {
		t.Fatalf("FilesWritten = %q", got)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}

	subjects := commitSubjects(t, repo.Dir)
	want := []string{
		"backend-developer: add health endpoint",
		"backend-developer: add quotes endpoint",
		"backend-developer: scaffold the module",
		"chore: empty base",
	}
	if strings.Join(subjects, "|") != strings.Join(want, "|") {
		t.Fatalf("commit subjects = %v, want %v", subjects, want)
	}

	// The T2 call must see T1's committed file and the other tasks framed as
	// off-limits.
	c2 := fc.calls[1]
	if !strings.Contains(c2, "# Your task (this pass)") || !strings.Contains(c2, "[T2] add quotes endpoint") {
		t.Fatalf("T2 prompt missing its own task framing:\n%s", c2)
	}
	if !strings.Contains(c2, "go.mod") || !strings.Contains(c2, "module quotes") {
		t.Fatalf("T2 prompt does not show T1's committed go.mod:\n%s", c2)
	}
	if !strings.Contains(c2, "[T1] scaffold the module (already committed") ||
		!strings.Contains(c2, "[T3] add health endpoint (handled in a later pass)") {
		t.Fatalf("T2 prompt missing the other-tasks list:\n%s", c2)
	}
}

func TestRunBatchedSkipsEmptyTaskButCommitsRest(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{
			{ID: "T1", Title: "one"},
			{ID: "T2", Title: "two"},
			{ID: "T3", Title: "three"},
		},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: a.go ===\npackage main\n=== END FILE: a.go ===\n",
		"NO CHANGES",
		"=== FILE: c.go ===\npackage main\n=== END FILE: c.go ===\n",
	}}

	res, err := runBatched(ctx, fc, "sys", env, repo)
	if err != nil {
		t.Fatalf("runBatched: %v", err)
	}
	if got := strings.Join(res.FilesWritten, ","); got != "a.go,c.go" {
		t.Fatalf("FilesWritten = %q, want a.go,c.go", got)
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 3 { // 2 tasks + base
		t.Fatalf("commits = %v, want 2 task commits + base", subs)
	}
}

func TestRunBatchedAllEmptyIsAnError(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "one"}, {ID: "T2", Title: "two"}},
	}
	fc := &fakeCompleter{replies: []string{`NO CHANGES`, `[]`}}

	if _, err := runBatched(ctx, fc, "sys", env, repo); err == nil {
		t.Fatal("expected an error when every task produced no files")
	}
}

func TestRunOnceSingleCommit(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "do it all"}},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: main.go ===\npackage main\n=== END FILE: main.go ===\n" +
			"=== FILE: go.mod ===\nmodule x\n=== END FILE: go.mod ===\n",
	}}

	res, err := runOnce(ctx, fc, "sys", env, repo, options{})
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(fc.calls))
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 2 || subs[0] != "backend-developer: do it all" {
		t.Fatalf("commit subjects = %v", subs)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
}

func TestExecutorFixPassIsSingleCall(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	// Seed a first commit so the fix pass has something to amend.
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "seed"); err != nil {
		t.Fatal(err)
	}

	env := factory.DispatchEnvelope{
		Stage:        factory.RoleBackendDeveloper,
		WorkspaceDir: repo.Dir,
		BaseBranch:   "main",
		Attempt:      1,
		Findings: []factory.Finding{
			{Severity: "high", Source: "gosec", Category: "G404", File: "main.go", Line: 3, Title: "weak rng"},
		},
		Tasks: []factory.PlanTask{{ID: "T1", Title: "one"}, {ID: "T2", Title: "two"}},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: main.go ===\npackage main // #nosec G404 -- deliberate\n=== END FILE: main.go ===\n",
	}}

	got, err := run(ctx, fc, "sys", env, options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("fix pass made %d model calls, want 1", len(fc.calls))
	}
	if !strings.Contains(fc.calls[0], "# Fix pass") || !strings.Contains(fc.calls[0], "(gosec G404)") {
		t.Fatalf("fix-pass prompt missing finding framing:\n%s", fc.calls[0])
	}
	if got.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
}

// A fix pass given a client that implements AgenticCompleter must use the
// tool-use path (CompleteAgentic, not Complete), offer it the fixPassTools
// set, and its tool exec function must actually work against the real repo —
// this is the mechanism a fix pass relies on to check a finding against the
// current file/history instead of only the finding's own (possibly wrong)
// text.
func TestExecutorFixPassUsesAgenticPathWhenAvailable(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "seed"); err != nil {
		t.Fatal(err)
	}

	env := factory.DispatchEnvelope{
		Stage:        factory.RoleBackendDeveloper,
		WorkspaceDir: repo.Dir,
		BaseBranch:   "main",
		Attempt:      1,
		Findings: []factory.Finding{
			{Severity: "high", TargetRole: "backend", File: "main.go", Line: 1, Title: "some finding"},
		},
	}
	ac := &fakeAgenticCompleter{writes: map[string]string{"main.go": "package main // fixed\n"}}

	got, err := run(ctx, ac, "sys", env, options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ac.agenticCalls != 1 {
		t.Fatalf("CompleteAgentic calls = %d, want 1 (fix pass should use the agentic path)", ac.agenticCalls)
	}
	var toolNames []string
	for _, tl := range ac.gotTools {
		toolNames = append(toolNames, tl.Name)
	}
	for _, want := range []string{"read_file", "git_log", "git_diff", "grep", "write_file", "edit_file", "run_build", "run_tests"} {
		found := false
		for _, n := range toolNames {
			found = found || n == want
		}
		if !found {
			t.Fatalf("devTools() = %v, missing %q", toolNames, want)
		}
	}
	if ac.toolErr != nil {
		t.Fatalf("exec(read_file, main.go) failed: %v", ac.toolErr)
	}
	if !strings.Contains(ac.toolResult, "package main") {
		t.Fatalf("exec(read_file, main.go) = %q, want it to contain the seeded file's content", ac.toolResult)
	}
	if !strings.Contains(ac.calls[0], "read_file, git_log, git_diff, grep") ||
		!strings.Contains(ac.calls[0], "write_file, edit_file, run_build,\nrun_tests") {
		t.Fatalf("fix-pass prompt missing tool-availability note:\n%s", ac.calls[0])
	}
	if got.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
	if got := strings.Join(got.FilesWritten, ","); got != "main.go" {
		t.Fatalf("FilesWritten = %q, want main.go (written via the write_file tool call)", got)
	}
}

// Attempt 0 (a from-scratch build) has nothing to investigate against a prior
// attempt, but it still uses the agentic tool-use path when the client
// supports it — the model can read/write/verify from the very first pass.
func TestExecutorAttemptZeroUsesAgenticPathWhenAvailable(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage:        factory.RoleBackendDeveloper,
		WorkspaceDir: repo.Dir,
		BaseBranch:   "main",
		PRDText:      "build a quote service",
		Tasks:        []factory.PlanTask{{ID: "T1", Title: "only task"}},
	}
	ac := &fakeAgenticCompleter{writes: map[string]string{"main.go": "package main\n"}}

	got, err := run(ctx, ac, "sys", env, options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ac.agenticCalls != 1 {
		t.Fatalf("CompleteAgentic calls = %d, want 1 on attempt 0 when the client supports it", ac.agenticCalls)
	}
	if got.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
}

// A client that only implements Completer (not AgenticCompleter) must still
// work on attempt 0 via the plain, block-parsing fallback.
func TestExecutorAttemptZeroUsesPlainPathWithoutAgenticSupport(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage:        factory.RoleBackendDeveloper,
		WorkspaceDir: repo.Dir,
		BaseBranch:   "main",
		PRDText:      "build a quote service",
		Tasks:        []factory.PlanTask{{ID: "T1", Title: "only task"}},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: main.go ===\npackage main\n=== END FILE: main.go ===\n",
	}}

	got, err := run(ctx, fc, "sys", env, options{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("Complete calls = %d, want 1", len(fc.calls))
	}
	if got.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
}

// A reply cut off mid-block still commits the files that fully arrived; the
// missing one is left for the build gate to catch.
func TestRunOnceTruncatedCommitsCompleteFiles(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "everything"}},
	}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: main.go ===\npackage main\n=== END FILE: main.go ===\n" +
			"=== FILE: go.mod ===\nmodule x\n=== END FILE: go.mod ===\n" +
			"=== FILE: store.go ===\npackage main\n// cut off before the closing marker",
	}}

	res, err := runOnce(ctx, fc, "sys", env, repo, options{})
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("model calls = %d, want 1 (no retry on a usable partial)", len(fc.calls))
	}
	if got := strings.Join(res.FilesWritten, ","); got != "go.mod,main.go" {
		t.Fatalf("FilesWritten = %q, want the two complete files", got)
	}
	if !strings.Contains(res.Summary, "cut off before store.go") {
		t.Fatalf("Summary should flag the truncation: %q", res.Summary)
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 2 {
		t.Fatalf("commits = %v, want 1 task commit + base", subs)
	}
}

// On the agentic path, changes land on disk via write_file tool calls, not a
// parsed final text blob — runOnce must commit exactly what those calls
// touched.
func TestRunOnceAgenticWritesViaTools(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "everything"}},
	}
	ac := &fakeAgenticCompleter{
		writes:        map[string]string{"main.go": "package main\n"},
		fakeCompleter: fakeCompleter{replies: []string{"wrote main.go to get things going"}},
	}

	res, err := runOnce(ctx, ac, "sys", env, repo, options{})
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := strings.Join(res.FilesWritten, ","); got != "main.go" {
		t.Fatalf("FilesWritten = %q, want main.go", got)
	}
	if !strings.Contains(res.Summary, "wrote main.go to get things going") {
		t.Fatalf("Summary should carry the model's prose: %q", res.Summary)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
	got, err := toolReadFile(repo.Dir, "main.go")
	if err != nil || got != "package main\n" {
		t.Fatalf("file on disk = %q, %v", got, err)
	}
}

// A fix pass (attempt > 0) that makes no tool calls is a legitimate "nothing
// to do" outcome, not an error.
func TestRunOnceAgenticNoChangesOnFixPass(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "seed"); err != nil {
		t.Fatal(err)
	}

	env := factory.DispatchEnvelope{
		Stage:   factory.RoleBackendDeveloper,
		Attempt: 1,
		Findings: []factory.Finding{
			{Severity: "low", File: "main.go", Line: 1, Title: "already fixed"},
		},
	}
	ac := &fakeAgenticCompleter{fakeCompleter: fakeCompleter{replies: []string{"the finding is already addressed"}}}

	res, err := runOnce(ctx, ac, "sys", env, repo, options{})
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if !strings.Contains(res.Summary, "no changes needed") {
		t.Fatalf("Summary = %q, want a no-changes-needed summary", res.Summary)
	}
	if res.CommitSHA != "" {
		t.Fatal("CommitSHA should be empty when nothing changed")
	}
}

// A developer's first pass (attempt 0) making no changes at all is a real
// failure — it was asked to build something.
func TestRunOnceAgenticNoChangesOnAttemptZeroIsAnError(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "only task"}},
	}
	ac := &fakeAgenticCompleter{fakeCompleter: fakeCompleter{replies: []string{"there's nothing to build here"}}}

	if _, err := runOnce(ctx, ac, "sys", env, repo, options{}); err == nil {
		t.Fatal("expected an error when a first pass makes no changes")
	}
}

// A mid-loop failure (e.g. a transport error after some tool calls already
// landed) must not leave dirty, uncommitted writes in the workspace — whatever
// was touched before the failure gets committed, and the error still
// propagates so the caller knows the pass didn't finish cleanly.
func TestRunOnceAgenticCommitsPartialWorkOnMidLoopError(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "everything"}},
	}
	ac := &fakeAgenticCompleter{
		writes:     map[string]string{"main.go": "package main\n"},
		afterWrite: fmt.Errorf("simulated transport failure"),
	}

	if _, err := runOnce(ctx, ac, "sys", env, repo, options{}); err == nil {
		t.Fatal("expected the mid-loop error to propagate")
	}
	got, err := toolReadFile(repo.Dir, "main.go")
	if err != nil || got != "package main\n" {
		t.Fatalf("file on disk = %q, %v — the partial write should survive the error", got, err)
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 2 { // the partial commit + base
		t.Fatalf("commits = %v, want the partial write committed despite the error", subs)
	}
}

// runBatched must use the agentic path per task when the client supports it,
// each task getting its own commit.
func TestRunBatchedUsesAgenticPathPerTask(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleBackendDeveloper,
		Tasks: []factory.PlanTask{
			{ID: "T1", Title: "scaffold"},
			{ID: "T2", Title: "add endpoint"},
		},
	}
	ac := &fakeAgenticCompleter{writesSeq: []map[string]string{
		{"go.mod": "module x\n\ngo 1.22\n"},
		{"main.go": "package main\n"},
	}}

	res, err := runBatched(ctx, ac, "sys", env, repo)
	if err != nil {
		t.Fatalf("runBatched: %v", err)
	}
	if ac.agenticCalls != 2 {
		t.Fatalf("CompleteAgentic calls = %d, want 2 (one per task)", ac.agenticCalls)
	}
	if got := strings.Join(res.FilesWritten, ","); got != "go.mod,main.go" {
		t.Fatalf("FilesWritten = %q, want go.mod,main.go", got)
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 3 { // 2 task commits + base
		t.Fatalf("commits = %v, want 2 task commits + base", subs)
	}
}

// WithPostWrite's harness-materialization hook must still fold its writes in
// on the agentic path, exactly as it does on the non-agentic path.
func TestRunOnceAgenticFoldsPostWrite(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{
		Stage: factory.RoleTestEngineer,
		Tasks: []factory.PlanTask{{ID: "T1", Title: "write tests"}},
	}
	ac := &fakeAgenticCompleter{writes: map[string]string{"main.go": "package main\n"}}
	postWriteFn := func(dir string) (written, removed []string, err error) {
		if err := os.WriteFile(filepath.Join(dir, "harness.go"), []byte("package main\n"), 0o644); err != nil {
			return nil, nil, err
		}
		return []string{"harness.go"}, nil, nil
	}

	var o options
	WithPostWrite(postWriteFn)(&o)

	res, err := runOnce(ctx, ac, "sys", env, repo, o)
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := strings.Join(res.FilesWritten, ","); got != "harness.go,main.go" {
		t.Fatalf("FilesWritten = %q, want both the model's and postWrite's files", got)
	}
}
