package devagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// The test-engineer's post-write hook (the factory-owned test harness) lands in
// the model's commit: its files are added, and a file it removes — even one the
// model just wrote — is neither committed nor reported.
func TestRunOncePostWriteJoinsTheCommit(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	env := factory.DispatchEnvelope{Stage: factory.RoleTestEngineer}
	fc := &fakeCompleter{replies: []string{
		"=== FILE: test/component/main.go ===\npackage main\n=== END FILE: test/component/main.go ===\n" +
			"=== FILE: test/component/Dockerfile ===\nCOPY ../e2e /e2e\n=== END FILE: test/component/Dockerfile ===\n",
	}}
	hook := func(dir string) ([]string, []string, error) {
		if err := os.WriteFile(filepath.Join(dir, "test", "Dockerfile"), []byte("FROM alpine:3\n"), 0o644); err != nil {
			return nil, nil, err
		}
		if err := os.Remove(filepath.Join(dir, "test", "component", "Dockerfile")); err != nil {
			return nil, nil, err
		}
		return []string{"test/Dockerfile"}, []string{"test/component/Dockerfile"}, nil
	}

	res, err := runOnce(ctx, fc, "sys", env, repo, options{postWrite: hook})
	if err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := strings.Join(res.FilesWritten, ","); got != "test/Dockerfile,test/component/main.go" {
		t.Fatalf("FilesWritten = %q", got)
	}
	if subs := commitSubjects(t, repo.Dir); len(subs) != 2 {
		t.Fatalf("commits = %v, want the one test-engineer commit + base", subs)
	}
	tracked, _ := repo.Tree(ctx)
	if got := strings.Join(tracked, ","); got != "test/Dockerfile,test/component/main.go" {
		t.Fatalf("tracked = %q, want the hook's file in and the removed one out", got)
	}
}

func TestRunOncePostWriteErrorFailsThePass(t *testing.T) {
	repo := newRepo(t)
	fc := &fakeCompleter{replies: []string{"=== FILE: a.go ===\npackage a\n=== END FILE: a.go ===\n"}}
	hook := func(string) ([]string, []string, error) { return nil, nil, errors.New("disk full") }

	_, err := runOnce(context.Background(), fc, "sys", factory.DispatchEnvelope{Stage: factory.RoleTestEngineer}, repo, options{postWrite: hook})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v, want the hook's error", err)
	}
}
