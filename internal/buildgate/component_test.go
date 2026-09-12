package buildgate

import (
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// `up --build` used to hand the LLM the first 12 KB of its output — all build
// and pull progress — and cut the test result that came at the end.
func TestFailKeepsTheEndOfLongOutput(t *testing.T) {
	out := strings.Repeat("#7 [app 2/5] RUN go mod download\n", 2000) +
		"tester-1  | FAIL [api] create roll: got 500, want 201\n"
	c := &checker{}
	c.fail("component tests", out, factory.RoleBackendDeveloper)

	got := c.failures[0].Output
	if !strings.Contains(got, "FAIL [api] create roll") {
		t.Fatalf("the failure's tail was clipped away: …%s", got[len(got)-200:])
	}
	if !strings.HasPrefix(got, "#7 [app 2/5]") {
		t.Fatal("the head should be kept too")
	}
	if len(got) > maxFailBytes+100 {
		t.Fatalf("output not bounded: %d bytes", len(got))
	}
}

func TestComponentFindingRoutesByFailingSuite(t *testing.T) {
	cases := []struct {
		log, role, title string
	}{
		{"PASS [api] list\nFAIL [e2e] board renders: timeout\n", factory.RoleFrontendDev, "1 component test(s) failed"},
		{"FAIL [api] create: 500\nFAIL [e2e] board renders: timeout\n", factory.RoleBackendDeveloper, "2 component test(s) failed"},
		{"", factory.RoleBackendDeveloper, "component tests failed"},
	}
	for _, tc := range cases {
		f := componentFinding(tc.log, "the report")
		if f.TargetRole != tc.role || !strings.HasPrefix(f.Title, tc.title) {
			t.Errorf("log %q: got role %s, title %q", tc.log, f.TargetRole, f.Title)
		}
		if tc.log != "" && !strings.Contains(f.Suggestion, "FAIL [") {
			t.Errorf("log %q: suggestion should quote the FAIL lines, got %q", tc.log, f.Suggestion)
		}
	}
}

// Compose v5's plain-progress log for a COPY from outside the build context,
// in a stack that builds more than one image.
func TestStackBuildFindingNamesTheServiceDockerfile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  app:\n    build: .\n  frontend:\n    build: ./web\n")
	write(t, dir, "web/package.json", `{"scripts":{"build":"vite build"}}`)
	out := strings.Join([]string{
		"#9 [frontend 3/6] COPY ../shared ./shared",
		`#9 ERROR: failed to calculate checksum of ref 557f815a-fc26-416b-8947-4b6b17c00997::pw6qsqxaoo5ji1tnremsbtzl0: "/shared": not found`,
		"------",
		" > [frontend 3/6] COPY ../shared ./shared:",
		"------",
		"Dockerfile:3",
		"--------------------",
		"   3 | >>> COPY ../shared ./shared",
		"--------------------",
		`target frontend: failed to solve: failed to compute cache key: failed to calculate checksum of ref 557f815a-fc26-416b-8947-4b6b17c00997::pw6qsqxaoo5ji1tnremsbtzl0: "/shared": not found`,
	}, "\n")

	f := stackBuildFinding(dir, out)
	if f.File != "web/Dockerfile" || f.Line != 3 || f.TargetRole != factory.RoleFrontendDev || f.Category != "deploy" {
		t.Fatalf("finding = %+v", f)
	}
	want := "image for service \"frontend\" failed to build at `COPY ../shared ./shared`: \"/shared\": not found"
	if f.Title != want {
		t.Fatalf("title = %q\nwant    %q", f.Title, want)
	}
}

// A stack that builds a single image names no service; it is the only one.
func TestStackBuildFindingSingleService(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  app:\n    build: ./svc\n")
	out := " > [2/2] RUN go build ./...:\nDockerfile:2\n" +
		"failed to solve: process \"/bin/sh -c go build ./...\" did not complete successfully: exit code: 1\n"

	f := stackBuildFinding(dir, out)
	if f.File != "svc/Dockerfile" || f.Line != 2 || !strings.Contains(f.Title, `"app"`) || !strings.Contains(f.Title, "exit code: 1") {
		t.Fatalf("finding = %+v", f)
	}
}

func TestReadPackageSpotsPlaywright(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"scripts":{"test":"playwright test"},"devDependencies":{"@playwright/test":"1.63.0"}}`)
	if _, _, pw := readPackage(dir); !pw {
		t.Fatal("a Playwright package must be recognised so its npm test is left to the tester")
	}
}
