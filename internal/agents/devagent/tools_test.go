package devagent

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestToolReadFileRejectsPathTraversal(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(context.Background(), "backend-developer", "seed"); err != nil {
		t.Fatal(err)
	}

	if _, err := toolReadFile(repo.Dir, "../../etc/passwd"); err == nil {
		t.Fatal("expected an error reading a path that escapes the repo, got nil")
	}
	if _, err := toolReadFile(repo.Dir, ""); err == nil {
		t.Fatal("expected an error for an empty path, got nil")
	}

	got, err := toolReadFile(repo.Dir, "main.go")
	if err != nil {
		t.Fatalf("toolReadFile(main.go): %v", err)
	}
	if got != "package main\n" {
		t.Fatalf("toolReadFile(main.go) = %q, want the seeded content", got)
	}
}

func TestToolExecFuncDispatchesGitLogAndDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo := newRepo(t)

	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "seed main.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n\nfunc main() {}\n"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddPaths(ctx, []string{"main.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Commit(ctx, "backend-developer", "add main func"); err != nil {
		t.Fatal(err)
	}

	dispatch := toolExecFunc(repo)

	log, err := dispatch(ctx, "git_log", json.RawMessage(`{"path":"main.go"}`))
	if err != nil {
		t.Fatalf("git_log: %v", err)
	}
	if !strings.Contains(log, "add main func") || !strings.Contains(log, "seed main.go") {
		t.Fatalf("git_log output missing expected commits:\n%s", log)
	}

	diff, err := dispatch(ctx, "git_diff", json.RawMessage(`{"path":"main.go"}`))
	if err != nil {
		t.Fatalf("git_diff: %v", err)
	}
	if !strings.Contains(diff, "func main()") {
		t.Fatalf("git_diff output missing the actual change:\n%s", diff)
	}

	grepOut, err := dispatch(ctx, "grep", json.RawMessage(`{"pattern":"func main"}`))
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(grepOut, "main.go") {
		t.Fatalf("grep output missing the match:\n%s", grepOut)
	}

	if _, err := dispatch(ctx, "not_a_real_tool", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for an unknown tool name, got nil")
	}
}

// fixPassTools' names must match exactly what the fix-pass prompt tells the
// model is available (see buildPrompt's tool-availability note) and what
// toolExecFunc's switch handles — a drift here would silently break tool use.
func TestFixPassToolsMatchDispatcher(t *testing.T) {
	tools := fixPassTools()
	want := map[string]bool{"read_file": true, "git_log": true, "git_diff": true, "grep": true}
	if len(tools) != len(want) {
		t.Fatalf("fixPassTools() has %d tools, want %d", len(tools), len(want))
	}
	for _, tl := range tools {
		if !want[tl.Name] {
			t.Fatalf("unexpected tool %q", tl.Name)
		}
		if len(tl.Required) == 0 {
			t.Fatalf("tool %q has no required properties", tl.Name)
		}
		if tl.Description == "" {
			t.Fatalf("tool %q has no description", tl.Name)
		}
	}
}
