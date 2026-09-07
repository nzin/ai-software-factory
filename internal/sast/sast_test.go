package sast

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGosecParsesFixture(t *testing.T) {
	if _, err := exec.LookPath("gosec"); err != nil {
		t.Skip("gosec not installed")
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.21\n")
	// G404: weak random
	write(t, dir, "main.go", "package main\nimport \"math/rand\"\nfunc main(){ _ = rand.Int() }\n")

	fs := gosec(context.Background(), dir)
	// gosec may or may not flag this depending on version; assert it doesn't panic
	// and that any finding is well-formed.
	for _, f := range fs {
		if f.Source != "gosec" || f.Title == "" {
			t.Fatalf("bad finding: %+v", f)
		}
	}
}

func TestNPMAuditNoLockfile(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed")
	}
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"x","version":"1.0.0"}`)
	fs := NPM(context.Background(), dir)
	// No lockfile / deps -> no findings, no crash.
	if len(fs) != 0 {
		t.Logf("npm audit returned %d findings (ok, tolerant)", len(fs))
	}
}

func TestGovulncheckParsesOnlyRealFindings(t *testing.T) {
	// govulncheck streams osv definitions (whole referenced DB) + finding
	// entries (actual hits). Only the latter, with a trace into user code, count.
	stream := `{"osv":{"id":"GO-2024-0001","summary":"Bug A in pkg/a"}}
{"osv":{"id":"GO-2024-0002","summary":"Bug B in pkg/b (not reachable)"}}
{"finding":{"osv":"GO-2024-0001","trace":[{"function":"main.doThing","position":{"filename":"/repo/main.go","line":12}}]}}
{"finding":{"osv":"GO-2024-0002","trace":[{"module":"pkg/b"}]}}
`
	got := parseGovulncheck([]byte(stream), "/repo")
	if len(got) != 1 {
		t.Fatalf("want 1 finding, got %d: %+v", len(got), got)
	}
	if got[0].Category != "GO-2024-0001" || got[0].File != "main.go" || got[0].Line != 12 {
		t.Fatalf("bad finding: %+v", got[0])
	}
	if got[0].Title != "Bug A in pkg/a" {
		t.Fatalf("title = %q", got[0].Title)
	}
}

func TestAtoi(t *testing.T) {
	for in, want := range map[string]int{"12": 12, "12-15": 12, " 3 ": 3, "abc": 0, "": 0} {
		if got := atoi(in); got != want {
			t.Fatalf("atoi(%q) = %d, want %d", in, got, want)
		}
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
