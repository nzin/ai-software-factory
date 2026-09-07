package buildgate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
}

func TestCheckCleanModule(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/clean\n\ngo 1.22\n")
	write(t, dir, "add.go", "package clean\n\nfunc Add(a, b int) int { return a + b }\n")
	write(t, dir, "add_test.go", "package clean\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n")

	findings, summary := Check(context.Background(), dir)
	for _, f := range findings {
		if f.Severity == "high" {
			t.Fatalf("clean module produced a high finding: %+v", f)
		}
	}
	if factory.VerdictFor(findings) != factory.VerdictApprove {
		t.Fatalf("verdict = %s, want approve; findings=%+v", factory.VerdictFor(findings), findings)
	}
	if !strings.Contains(summary, "clean") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCheckCompileError(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/broken\n\ngo 1.22\n")
	// references an undefined symbol
	write(t, dir, "bad.go", "package broken\n\nfunc Use() int { return missing() }\n")

	findings, _ := Check(context.Background(), dir)
	if factory.VerdictFor(findings) != factory.VerdictRequestChanges {
		t.Fatalf("verdict = %s, want request_changes", factory.VerdictFor(findings))
	}
	var hit *factory.Finding
	for i := range findings {
		if findings[i].Severity == "high" {
			hit = &findings[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("no high finding: %+v", findings)
	}
	if hit.Source != "build-gate" || hit.TargetRole != factory.RoleBackendDeveloper {
		t.Fatalf("finding not routed to backend: %+v", *hit)
	}
	if hit.File != "bad.go" || hit.Line == 0 {
		t.Fatalf("finding lost the file:line: %+v", *hit)
	}
}

func TestCheckFailingTest(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/failtest\n\ngo 1.22\n")
	write(t, dir, "x.go", "package failtest\n\nfunc One() int { return 1 }\n")
	write(t, dir, "x_test.go", "package failtest\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 2 {\n\t\tt.Fatal(\"expected 2\")\n\t}\n}\n")

	findings, _ := Check(context.Background(), dir)
	if factory.VerdictFor(findings) != factory.VerdictRequestChanges {
		t.Fatalf("a failing test should bounce the run: %+v", findings)
	}
}

func TestCheckNoToolchain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")
	// Empty PATH so neither `go` nor `npm` resolves.
	t.Setenv("PATH", "")

	findings, summary := Check(context.Background(), dir)
	if len(findings) != 1 || findings[0].Severity != "info" {
		t.Fatalf("want one info finding, got %+v", findings)
	}
	if factory.VerdictFor(findings) != factory.VerdictApprove {
		t.Fatal("a missing toolchain must not fail the run")
	}
	if !strings.Contains(summary, "skipped") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestRouteByPath(t *testing.T) {
	cases := map[string]string{
		"internal/api/handlers.go": factory.RoleBackendDeveloper,
		"go.mod":                   factory.RoleBackendDeveloper,
		"src/App.vue":              factory.RoleFrontendDev,
		"src/main.ts":              factory.RoleFrontendDev,
		"package.json":             factory.RoleFrontendDev,
		"README.md":                "",
	}
	for path, want := range cases {
		if got := routeByPath(path); got != want {
			t.Errorf("routeByPath(%q) = %q, want %q", path, got, want)
		}
	}
}
