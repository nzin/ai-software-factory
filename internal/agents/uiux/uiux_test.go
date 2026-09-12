package uiux

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// fakeCompleter returns a canned reply and records the prompt it saw.
type fakeCompleter struct {
	reply string
	calls []string
}

func (f *fakeCompleter) Complete(_ context.Context, _, user string) (string, error) {
	f.calls = append(f.calls, user)
	return f.reply, nil
}

func newRepo(t *testing.T) *workspace.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := workspace.NewManager(t.TempDir())
	repo, err := m.Prepare(context.Background(), "run-uiux1", workspace.ParseRepoURL("", "main"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return repo
}

func TestRunTextOnlyReplyUnchanged(t *testing.T) {
	fc := &fakeCompleter{reply: "## Screens\n\n- Home: lists items.\n"}
	env := factory.DispatchEnvelope{Stage: factory.RoleUIUXDesigner, PRDText: "build a todo app"}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Summary != fc.reply {
		t.Fatalf("Summary = %q, want raw reply %q", res.Summary, fc.reply)
	}
	if res.CommitSHA != "" || len(res.FilesWritten) != 0 {
		t.Fatalf("expected no commit/files, got CommitSHA=%q FilesWritten=%v", res.CommitSHA, res.FilesWritten)
	}
}

func TestRunSpecAndMockupCommitted(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/spec.md ===\n## Screens\n\n- Home: lists items.\n=== END FILE: design/spec.md ===\n" +
		"=== FILE: design/mockups/home.svg ===\n<svg></svg>\n=== END FILE: design/mockups/home.svg ===\n",
	}
	env := factory.DispatchEnvelope{
		Stage:        factory.RoleUIUXDesigner,
		WorkspaceDir: repo.Dir,
		PRDText:      "build a todo app",
	}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(res.Summary, "=== FILE") {
		t.Fatalf("Summary leaked block markers: %q", res.Summary)
	}
	if res.Summary != "## Screens\n\n- Home: lists items." {
		t.Fatalf("Summary = %q", res.Summary)
	}
	if res.CommitSHA == "" {
		t.Fatal("CommitSHA empty")
	}
	if got := strings.Join(res.FilesWritten, ","); got != "design/mockups/home.svg,design/spec.md" {
		t.Fatalf("FilesWritten = %q", got)
	}
}

func TestRunBlocksWithoutSpecPathFallsBack(t *testing.T) {
	fc := &fakeCompleter{reply: "" +
		"=== FILE: design/mockups/home.svg ===\n<svg></svg>\n=== END FILE: design/mockups/home.svg ===\n",
	}
	env := factory.DispatchEnvelope{Stage: factory.RoleUIUXDesigner, PRDText: "build a todo app"}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Summary != fc.reply {
		t.Fatalf("Summary = %q, want raw reply %q", res.Summary, fc.reply)
	}
	if res.CommitSHA != "" || len(res.FilesWritten) != 0 {
		t.Fatalf("expected no commit/files, got CommitSHA=%q FilesWritten=%v", res.CommitSHA, res.FilesWritten)
	}
}
