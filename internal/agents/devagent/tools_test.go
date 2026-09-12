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

	dispatch := toolExecFunc(repo, newToolState())

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

// devTools' names must match exactly what the agentic-pass prompt tells the
// model is available (see agenticToolsNote) and what toolExecFunc's switch
// handles — a drift here would silently break tool use.
func TestDevToolsMatchDispatcher(t *testing.T) {
	tools := devTools()
	want := map[string]bool{
		"read_file": true, "git_log": true, "git_diff": true, "grep": true,
		"write_file": true, "edit_file": true, "run_build": true, "run_tests": true,
	}
	// run_build/run_tests legitimately have no required properties (path is
	// optional) — every other tool must require at least one.
	noRequiredOK := map[string]bool{"run_build": true, "run_tests": true}
	if len(tools) != len(want) {
		t.Fatalf("devTools() has %d tools, want %d", len(tools), len(want))
	}
	for _, tl := range tools {
		if !want[tl.Name] {
			t.Fatalf("unexpected tool %q", tl.Name)
		}
		if len(tl.Required) == 0 && !noRequiredOK[tl.Name] {
			t.Fatalf("tool %q has no required properties", tl.Name)
		}
		if tl.Description == "" {
			t.Fatalf("tool %q has no description", tl.Name)
		}
	}
}

func TestToolWriteFileCreatesAndOverwrites(t *testing.T) {
	repo := newRepo(t)
	state := newToolState()

	out, err := toolWriteFile(repo, state, "src/main.go", "package main\n")
	if err != nil {
		t.Fatalf("toolWriteFile: %v", err)
	}
	if !strings.Contains(out, "src/main.go") {
		t.Fatalf("confirmation = %q, want it to name the path", out)
	}
	got, err := toolReadFile(repo.Dir, "src/main.go")
	if err != nil || got != "package main\n" {
		t.Fatalf("file on disk = %q, %v", got, err)
	}
	if want := []string{"src/main.go"}; !equal(state.touched(), want) {
		t.Fatalf("state.touched() = %v, want %v", state.touched(), want)
	}

	if _, err := toolWriteFile(repo, state, "src/main.go", "package main\n\nfunc main() {}\n"); err != nil {
		t.Fatalf("toolWriteFile overwrite: %v", err)
	}
	got, err = toolReadFile(repo.Dir, "src/main.go")
	if err != nil || got != "package main\n\nfunc main() {}\n" {
		t.Fatalf("overwritten content = %q, %v", got, err)
	}
}

func TestToolWriteFileRejectsTraversal(t *testing.T) {
	repo := newRepo(t)
	state := newToolState()
	if _, err := toolWriteFile(repo, state, "../escape.go", "package x\n"); err == nil {
		t.Fatal("expected an error writing a path that escapes the repo, got nil")
	}
	if len(state.touched()) != 0 {
		t.Fatalf("state.touched() = %v, want none", state.touched())
	}
}

func TestToolEditFileReplacesUniqueMatch(t *testing.T) {
	repo := newRepo(t)
	state := newToolState()
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n\nfunc Hello() string { return \"hi\" }\n"}); err != nil {
		t.Fatal(err)
	}

	out, err := toolEditFile(repo, state, "main.go", `return "hi"`, `return "hello"`, false)
	if err != nil {
		t.Fatalf("toolEditFile: %v", err)
	}
	if !strings.Contains(out, "1 occurrence") {
		t.Fatalf("confirmation = %q", out)
	}
	got, _ := toolReadFile(repo.Dir, "main.go")
	if !strings.Contains(got, `return "hello"`) {
		t.Fatalf("file not updated: %q", got)
	}
	if want := []string{"main.go"}; !equal(state.touched(), want) {
		t.Fatalf("state.touched() = %v, want %v", state.touched(), want)
	}
}

func TestToolEditFileErrorsWhenNotFound(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolEditFile(repo, newToolState(), "main.go", "nope", "x", false); err == nil {
		t.Fatal("expected an error when old_string is not found, got nil")
	}
}

func TestToolEditFileErrorsWhenAmbiguousWithoutReplaceAll(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{"main.go": "x\nx\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolEditFile(repo, newToolState(), "main.go", "x", "y", false); err == nil {
		t.Fatal("expected an error when old_string matches more than once, got nil")
	}
}

func TestToolEditFileReplaceAllReplacesEveryOccurrence(t *testing.T) {
	repo := newRepo(t)
	state := newToolState()
	if _, err := repo.WriteFiles(map[string]string{"main.go": "x\nx\n"}); err != nil {
		t.Fatal(err)
	}
	out, err := toolEditFile(repo, state, "main.go", "x", "y", true)
	if err != nil {
		t.Fatalf("toolEditFile: %v", err)
	}
	if !strings.Contains(out, "2 occurrence") {
		t.Fatalf("confirmation = %q", out)
	}
	got, _ := toolReadFile(repo.Dir, "main.go")
	if got != "y\ny\n" {
		t.Fatalf("file = %q, want both replaced", got)
	}
}

func TestToolEditFileErrorsOnMissingFile(t *testing.T) {
	repo := newRepo(t)
	if _, err := toolEditFile(repo, newToolState(), "nope.go", "a", "b", false); err == nil {
		t.Fatal("expected an error editing a file that doesn't exist, got nil")
	}
}

func TestToolEditFileErrorsWhenOldEqualsNew(t *testing.T) {
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{"main.go": "x\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolEditFile(repo, newToolState(), "main.go", "x", "x", false); err == nil {
		t.Fatal("expected an error when old_string == new_string, got nil")
	}
}

func TestToolRunBuildAndRunTestsAgainstRealRepo(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repo := newRepo(t)
	if _, err := repo.WriteFiles(map[string]string{
		"go.mod":      "module example.com/x\n\ngo 1.22\n",
		"add.go":      "package x\n\nfunc Add(a, b int) int { return a + b }\n",
		"add_test.go": "package x\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := toolRunBuild(context.Background(), repo.Dir, ""); err != nil {
		t.Fatalf("toolRunBuild: %v", err)
	}
	if _, err := toolRunTests(context.Background(), repo.Dir, ""); err != nil {
		t.Fatalf("toolRunTests: %v", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
