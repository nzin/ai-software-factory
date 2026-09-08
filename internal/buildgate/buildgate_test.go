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

	res := Check(context.Background(), "run-test", dir, nil)
	findings, summary := res.Findings, res.Summary
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

	findings := Check(context.Background(), "run-test", dir, nil).Findings
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

	findings := Check(context.Background(), "run-test", dir, nil).Findings
	if factory.VerdictFor(findings) != factory.VerdictRequestChanges {
		t.Fatalf("a failing test should bounce the run: %+v", findings)
	}
}

func TestCheckNoToolchain(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/x\n\ngo 1.22\n")
	// Empty PATH so neither `go` nor `npm` resolves.
	t.Setenv("PATH", "")

	res := Check(context.Background(), "run-test", dir, nil)
	findings, summary := res.Findings, res.Summary
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

func TestCheckReportsFailuresForTheLLM(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/broken\n\ngo 1.22\n")
	write(t, dir, "bad.go", "package broken\n\nfunc Use() int { return missing() }\n")

	res := Check(context.Background(), "run-test", dir, nil)
	if len(res.Failures) == 0 {
		t.Fatal("Check should hand the raw failure output to the caller for the LLM pass")
	}
	if res.Failures[0].Kind != "go build" || !strings.Contains(res.Failures[0].Output, "missing") {
		t.Fatalf("failure = %+v", res.Failures[0])
	}

	clean := t.TempDir()
	write(t, clean, "go.mod", "module example.com/clean\n\ngo 1.22\n")
	write(t, clean, "a.go", "package clean\n\nfunc A() int { return 1 }\n")
	if fs := Check(context.Background(), "run-test", clean, nil).Failures; len(fs) != 0 {
		t.Fatalf("clean module reported failures: %+v", fs)
	}
}

func TestCheckMissingComposeForAService(t *testing.T) {
	requireGo(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/svc\n\ngo 1.22\n")
	write(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	res := Check(context.Background(), "run-test", dir, nil)
	var deploy *factory.Finding
	for i := range res.Findings {
		if res.Findings[i].Category == "deploy" {
			deploy = &res.Findings[i]
		}
	}
	if deploy == nil {
		t.Fatalf("a runnable service with no docker-compose.yml should be flagged; findings=%+v", res.Findings)
	}
	if factory.VerdictFor(res.Findings) != factory.VerdictRequestChanges {
		t.Fatal("missing compose should bounce the run")
	}
}

func TestCheckNoComposeCheckForALibrary(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	// a library: no main.go, no cmd/*/main.go
	write(t, dir, "go.mod", "module example.com/lib\n\ngo 1.22\n")
	write(t, dir, "lib.go", "package lib\n\nfunc F() int { return 1 }\n")

	for _, f := range Check(context.Background(), "run-test", dir, nil).Findings {
		if f.Category == "deploy" || f.Category == "component-test" {
			t.Fatalf("a library must not get a deploy/component finding: %+v", f)
		}
	}
}

func TestIsCodeChange(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		want    bool
	}{
		{"nil (undiffable) runs the checks", nil, true},
		{"docs only", []string{"README.md", "docs/spec.md"}, false},
		{"a source file", []string{"docs/x.md", "internal/api/handlers.go"}, true},
		{"a manifest", []string{"go.mod"}, true},
		{"a compose file", []string{"docker-compose.test.yml"}, true},
		{"empty slice", []string{}, false},
	}
	for _, tc := range cases {
		if got := isCodeChange(tc.changed); got != tc.want {
			t.Errorf("%s: isCodeChange(%v) = %v, want %v", tc.name, tc.changed, got, tc.want)
		}
	}
}
