package kodus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseEnvelope(t *testing.T) {
	out := []byte(`some progress line
{"ok":true,"command":"review","data":{"issues":[
  {"file":"main.go","line":10,"severity":"HIGH","category":"bug","title":"nil deref","suggestion":"guard it"},
  {"path":"web/app.vue","startLine":3,"severity":"low","message":"unused var"}
]},"error":null,"meta":{"schemaVersion":"1.0"}}`)

	findings, err := parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 2 {
		t.Fatalf("findings = %d", len(findings))
	}
	if findings[0].File != "main.go" || findings[0].Line != 10 || findings[0].Severity != "high" {
		t.Fatalf("f0 = %+v", findings[0])
	}
	if findings[1].File != "web/app.vue" || findings[1].Line != 3 || findings[1].Title != "unused var" {
		t.Fatalf("f1 = %+v", findings[1])
	}
}

func TestParseError(t *testing.T) {
	f, err := parse([]byte(`{"ok":false,"data":null,"error":{"message":"rate limited"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Severity != "info" {
		t.Fatalf("got %+v", f)
	}
}

func TestReviewUnconfigured(t *testing.T) {
	t.Setenv("KODUS_TEAM_KEY", "")
	f, err := Review(t.Context(), t.TempDir(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Source != "kodus" {
		t.Fatalf("got %+v", f)
	}
}

// TestReviewLiveSmoke exercises Review against a real, running Kodus instance
// instead of just the skip paths above. It only runs when both preconditions
// Review itself checks are met (kodus CLI on PATH, KODUS_TEAM_KEY set) —
// otherwise the standalone Kodus stack isn't up and there's nothing to hit.
func TestReviewLiveSmoke(t *testing.T) {
	if _, err := exec.LookPath("kodus"); err != nil {
		t.Skip("kodus CLI not on PATH")
	}
	if os.Getenv("KODUS_TEAM_KEY") == "" {
		t.Skip("KODUS_TEAM_KEY not set")
	}

	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeKodus(t, dir, "add.go", "package add\n\nfunc Divide(a, b int) int {\n\treturn a / b\n}\n")
	runGit(t, dir, "add", "add.go")
	runGit(t, dir, "commit", "-m", "init")
	runGit(t, dir, "checkout", "-b", "feature")
	writeKodus(t, dir, "add.go", "package add\n\nfunc Divide(a, b int) int {\n\treturn a / b\n}\n\nfunc Unused(x int) int {\n\ty := x + 1\n\treturn x\n}\n")
	runGit(t, dir, "add", "add.go")
	runGit(t, dir, "commit", "-m", "introduce a bug")

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	findings, err := Review(ctx, dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Fatal("expected at least one finding from a real review")
	}
	for _, f := range findings {
		if f.Source != "kodus" {
			t.Fatalf("unexpected source: %+v", f)
		}
		if strings.Contains(f.Title, "not installed") ||
			strings.Contains(f.Title, "not set") ||
			strings.Contains(f.Title, "did not return parseable JSON") {
			t.Fatalf("got a graceful-degradation finding instead of a real review: %+v", f)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeKodus(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
