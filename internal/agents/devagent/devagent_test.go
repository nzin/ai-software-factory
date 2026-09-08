package devagent

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// fakeCompleter returns a canned reply per call and records the prompts it saw.
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

	res, err := runOnce(ctx, fc, "sys", env, repo)
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

	got, err := run(ctx, fc, "sys", env)
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

	res, err := runOnce(ctx, fc, "sys", env, repo)
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
