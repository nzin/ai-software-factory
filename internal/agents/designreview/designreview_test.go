package designreview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/llm"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// fakeCompleter returns a canned reply and records the images it was given.
type fakeCompleter struct {
	reply    string
	calls    int
	lastUser string
	lastImgs []llm.ImageInput
}

func (f *fakeCompleter) CompleteWithImages(_ context.Context, _, user string, images []llm.ImageInput) (string, error) {
	f.calls++
	f.lastUser = user
	f.lastImgs = images
	return f.reply, nil
}

func newRepo(t *testing.T) *workspace.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m := workspace.NewManager(t.TempDir())
	repo, err := m.Prepare(context.Background(), "run-designreview1", workspace.ParseRepoURL("", "main"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return repo
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestRunNoMockupsApprovesWithoutCallingModel(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{}
	env := factory.DispatchEnvelope{Stage: factory.RoleDesignReviewer, WorkspaceDir: repo.Dir}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Verdict != factory.VerdictApprove {
		t.Fatalf("Verdict = %q, want approve", res.Verdict)
	}
	if fc.calls != 0 {
		t.Fatalf("model calls = %d, want 0", fc.calls)
	}
}

func TestRunMockupsButNoScreenshotsApprovesWithoutCallingModel(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo.Dir, "design/mockups/home.svg", "<svg></svg>")
	fc := &fakeCompleter{}
	env := factory.DispatchEnvelope{Stage: factory.RoleDesignReviewer, WorkspaceDir: repo.Dir}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Verdict != factory.VerdictApprove {
		t.Fatalf("Verdict = %q, want approve", res.Verdict)
	}
	if fc.calls != 0 {
		t.Fatalf("model calls = %d, want 0", fc.calls)
	}
}

func TestRunBothPresentCallsModelAndParsesFindings(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo.Dir, "design/mockups/home.svg", "<svg><rect/></svg>")
	writeFile(t, repo.Dir, "test/e2e/screenshots/home.png", "fakepngbytes")
	fc := &fakeCompleter{reply: `[{"severity":"high","title":"missing button","targetRole":"frontend"}]`}
	env := factory.DispatchEnvelope{Stage: factory.RoleDesignReviewer, WorkspaceDir: repo.Dir}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fc.calls != 1 {
		t.Fatalf("model calls = %d, want 1", fc.calls)
	}
	if len(fc.lastImgs) != 1 {
		t.Fatalf("images given to model = %d, want 1", len(fc.lastImgs))
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %v, want 1", res.Findings)
	}
	if res.Findings[0].Source != "design-reviewer" {
		t.Fatalf("Source = %q, want design-reviewer", res.Findings[0].Source)
	}
	if res.Findings[0].TargetRole != "frontend" {
		t.Fatalf("TargetRole = %q, want frontend", res.Findings[0].TargetRole)
	}
	if res.Verdict != factory.VerdictRequestChanges {
		t.Fatalf("Verdict = %q, want request_changes", res.Verdict)
	}
	if !strings.Contains(fc.lastUser, "design/mockups/home.svg") {
		t.Fatalf("prompt does not mention the mockup path: %q", fc.lastUser)
	}
	if !strings.Contains(fc.lastUser, "test/e2e/screenshots/home.png") {
		t.Fatalf("prompt does not mention the screenshot path: %q", fc.lastUser)
	}
}

func TestRunEmptyFindingsApproves(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo.Dir, "design/mockups/home.svg", "<svg></svg>")
	writeFile(t, repo.Dir, "test/e2e/screenshots/home.png", "fakepngbytes")
	fc := &fakeCompleter{reply: `[]`}
	env := factory.DispatchEnvelope{Stage: factory.RoleDesignReviewer, WorkspaceDir: repo.Dir}

	res, err := run(context.Background(), fc, "sys", env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Verdict != factory.VerdictApprove {
		t.Fatalf("Verdict = %q, want approve", res.Verdict)
	}
}
