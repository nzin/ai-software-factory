package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newRef(url string) RepoRef { return ParseRepoURL(url, "main") }

func TestPrepareNewRepoLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	m := NewManager(t.TempDir())

	repo, err := m.Prepare(ctx, "run-1", newRef(""))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if repo.Kind != KindNew {
		t.Fatalf("kind = %s, want new", repo.Kind)
	}
	if repo.WorkBranch != "asf/run-run-1" {
		t.Fatalf("work branch = %q", repo.WorkBranch)
	}

	// nothing to commit yet
	if sha, err := repo.Commit(ctx, "backend-developer", "noop"); err != nil || sha != "" {
		t.Fatalf("empty commit: sha=%q err=%v", sha, err)
	}

	written, err := repo.WriteFiles(map[string]string{
		"cmd/main.go":     "package main\n",
		"../escape.txt":   "nope",
		"internal/x/y.go": "package x\n",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("written = %v (path traversal should be dropped)", written)
	}

	sha, err := repo.Commit(ctx, "backend-developer", "add files")
	if err != nil || sha == "" {
		t.Fatalf("commit: sha=%q err=%v", sha, err)
	}

	if !repo.HasCommits(ctx) {
		t.Fatal("expected commits beyond base")
	}

	files, err := repo.ChangedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("changed files = %v", files)
	}

	diff, err := repo.Diff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "cmd/main.go") || !strings.Contains(diff, "package main") {
		t.Fatalf("diff missing content:\n%s", diff)
	}

	reopened, err := Open(ctx, repo.Dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if tree, _ := reopened.Tree(ctx); len(tree) != 2 {
		t.Fatalf("tree = %v", tree)
	}
}

// fixtureRepo builds a real local git repo with a few commits of history on main.
func fixtureRepo(t *testing.T, ctx context.Context) string {
	t.Helper()
	dir := t.TempDir()
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Fixture"},
		{"config", "user.email", "fixture@test.local"},
		{"config", "commit.gpgsign", "false"},
	}
	for _, s := range steps {
		if out, err := git(ctx, dir, s...); err != nil {
			t.Fatalf("git %v: %v: %s", s, err, out)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := git(ctx, dir, "add", "-A"); err != nil {
			t.Fatalf("add: %v: %s", err, out)
		}
		if out, err := git(ctx, dir, "commit", "-q", "-m", "add "+name); err != nil {
			t.Fatalf("commit: %v: %s", err, out)
		}
	}
	return dir
}

func TestPrepareLocalRepoUsesWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	src := fixtureRepo(t, ctx)
	m := NewManager(t.TempDir())

	repo, err := m.Prepare(ctx, "wt-1", newRef(src))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if repo.Kind != KindLocal {
		t.Fatalf("kind = %s, want local", repo.Kind)
	}
	if repo.Source != src {
		t.Fatalf("source = %q, want %q", repo.Source, src)
	}

	// The work branch lives in the user's own repo.
	if out, _ := git(ctx, src, "branch", "--list", "asf/run-wt-1"); !strings.Contains(out, "asf/run-wt-1") {
		t.Fatalf("work branch not in source repo: %q", out)
	}

	if _, err := repo.WriteFiles(map[string]string{"new.txt": "new\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "add new.txt"); err != nil {
		t.Fatal(err)
	}

	// Diff must show only the run's work, not the fixture's history.
	files, err := repo.ChangedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "new.txt" {
		t.Fatalf("changed files = %v, want [new.txt] only", files)
	}

	// Cleanup unregisters the worktree; the branch survives in the source repo.
	if err := m.Remove(ctx, "wt-1", repo); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(repo.Dir); !os.IsNotExist(err) {
		t.Fatalf("workspace dir still present: %v", err)
	}
	if out, _ := git(ctx, src, "branch", "--list", "asf/run-wt-1"); !strings.Contains(out, "asf/run-wt-1") {
		t.Fatalf("branch should survive cleanup: %q", out)
	}
}

// A developer agent writes its own .gitignore, and a rule meant for a build
// artifact can match a source directory of the same name. The files the agent
// explicitly wrote must still be committed.
func TestAddPathsBeatsAModelAuthoredGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	m := NewManager(t.TempDir())
	repo, err := m.Prepare(ctx, "ign-1", newRef(""))
	if err != nil {
		t.Fatal(err)
	}

	written, err := repo.WriteFiles(map[string]string{
		// "/notes" is meant to ignore the compiled binary, but it also matches
		// the notes/ package the agent is writing right now.
		".gitignore":          "/notes\n*.test\n",
		"main.go":             "package main\n",
		"notes/store.go":      "package notes\n",
		"notes/store_test.go": "package notes\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 4 {
		t.Fatalf("written = %v", written)
	}
	if err := repo.AddPaths(ctx, written); err != nil {
		t.Fatalf("add paths: %v", err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "add notes package"); err != nil {
		t.Fatal(err)
	}

	tracked, err := repo.Tree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range tracked {
		got[f] = true
	}
	for _, want := range []string{".gitignore", "main.go", "notes/store.go", "notes/store_test.go"} {
		if !got[want] {
			t.Fatalf("%s was not committed; tracked = %v", want, tracked)
		}
	}

	// The .gitignore still governs files nobody asked us to write.
	if _, err := repo.WriteFiles(map[string]string{"notes/junk.test": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "noop"); err != nil {
		t.Fatal(err)
	}
	tracked, _ = repo.Tree(ctx)
	for _, f := range tracked {
		if f == "notes/junk.test" {
			t.Fatal("an ignored build artifact was committed")
		}
	}
}

func TestPrune(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	m := NewManager(t.TempDir())
	for _, id := range []string{"a", "b", "c"} {
		if _, err := m.Prepare(ctx, id, newRef("")); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Prune(1); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, m.RepoDir("c")); err != nil {
		t.Fatalf("newest run pruned: %v", err)
	}
}
