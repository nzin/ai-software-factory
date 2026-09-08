// Package sast runs static analysers over a per-run worktree and normalises
// their output to factory.Finding. Missing tools are skipped, not fatal.
package sast

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// All runs every analyser appropriate for the tree at dir.
func All(ctx context.Context, dir string) []factory.Finding {
	var out []factory.Finding
	if hasGoModule(dir) {
		out = append(out, Go(ctx, dir)...)
	}
	for _, pkgDir := range npmDirs(dir) {
		out = append(out, NPM(ctx, dir, pkgDir)...)
	}
	return out
}

// Go runs gosec and govulncheck.
func Go(ctx context.Context, dir string) []factory.Finding {
	var out []factory.Finding
	out = append(out, gosec(ctx, dir)...)
	out = append(out, govulncheck(ctx, dir)...)
	return out
}

func gosec(ctx context.Context, dir string) []factory.Finding {
	if _, err := exec.LookPath("gosec"); err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "gosec", "-quiet", "-fmt=json", "./...")
	cmd.Dir = dir
	stdout, _ := cmd.Output() // gosec exits non-zero when it finds issues
	var report struct {
		Issues []struct {
			Severity string `json:"severity"`
			RuleID   string `json:"rule_id"`
			Details  string `json:"details"`
			File     string `json:"file"`
			Line     string `json:"line"`
		} `json:"Issues"`
	}
	if json.Unmarshal(stdout, &report) != nil {
		return nil
	}
	out := make([]factory.Finding, 0, len(report.Issues))
	for _, is := range report.Issues {
		file := rel(dir, is.File)
		out = append(out, factory.Finding{
			Source:     "gosec",
			Severity:   strings.ToLower(is.Severity),
			Category:   is.RuleID,
			File:       file,
			Line:       atoi(strings.SplitN(is.Line, "-", 2)[0]),
			Title:      is.Details,
			TargetRole: factory.RoleForPath(file),
		})
	}
	return out
}

func govulncheck(ctx context.Context, dir string) []factory.Finding {
	if _, err := exec.LookPath("govulncheck"); err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "govulncheck", "-format", "json", "./...")
	cmd.Dir = dir
	stdout, _ := cmd.Output()
	return parseGovulncheck(stdout, dir)
}

// parseGovulncheck turns govulncheck -format json output into findings.
// `osv` entries are vulnerability *definitions* (the whole referenced DB); the
// ones that actually affect this code are `finding` entries with a call `trace`.
func parseGovulncheck(stdout []byte, dir string) []factory.Finding {
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	osvSummary := map[string]string{}
	seen := map[string]bool{}
	var out []factory.Finding
	for dec.More() {
		var msg struct {
			OSV *struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"osv"`
			Finding *struct {
				OSV   string `json:"osv"`
				Trace []struct {
					Function string `json:"function"`
					Position *struct {
						Filename string `json:"filename"`
						Line     int    `json:"line"`
					} `json:"position"`
				} `json:"trace"`
			} `json:"finding"`
		}
		if dec.Decode(&msg) != nil {
			break
		}
		if msg.OSV != nil {
			osvSummary[msg.OSV.ID] = msg.OSV.Summary
		}
		f := msg.Finding
		// Only a finding whose trace reaches into the user's code is a real hit.
		if f == nil || len(f.Trace) == 0 || f.Trace[0].Function == "" || seen[f.OSV] {
			continue
		}
		seen[f.OSV] = true
		fnd := factory.Finding{Source: "govulncheck", Severity: "high", Category: f.OSV, Title: osvSummary[f.OSV]}
		if p := f.Trace[0].Position; p != nil {
			fnd.File, fnd.Line = rel(dir, p.Filename), p.Line
			fnd.TargetRole = factory.RoleForPath(fnd.File)
		}
		if fnd.Title == "" {
			fnd.Title = "Vulnerable dependency: " + f.OSV
		}
		out = append(out, fnd)
	}
	return out
}

// NPM runs `npm audit --omit=dev --json` in pkgDir (a directory containing a
// package.json). --omit=dev keeps devDependency advisories (build/test tooling
// that never ships) out of the results. root is the repo root, used to make the
// finding path repo-relative so it can be routed to the owning developer.
func NPM(ctx context.Context, root, pkgDir string) []factory.Finding {
	if _, err := exec.LookPath("npm"); err != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "npm", "audit", "--omit=dev", "--json")
	cmd.Dir = pkgDir
	stdout, _ := cmd.Output()
	return parseNPMAudit(stdout, root, pkgDir)
}

func parseNPMAudit(stdout []byte, root, pkgDir string) []factory.Finding {
	var report struct {
		Vulnerabilities map[string]struct {
			Severity string `json:"severity"`
			Via      []any  `json:"via"`
			Name     string `json:"name"`
		} `json:"vulnerabilities"`
	}
	if json.Unmarshal(stdout, &report) != nil {
		return nil
	}
	file := rel(root, filepath.Join(pkgDir, "package.json"))
	owner := factory.RoleForPath(file)
	var out []factory.Finding
	for name, v := range report.Vulnerabilities {
		title := "Vulnerable dependency: " + name
		for _, via := range v.Via {
			if m, ok := via.(map[string]any); ok {
				if t, ok := m["title"].(string); ok && t != "" {
					title = t
					break
				}
			}
		}
		out = append(out, factory.Finding{
			Source:     "npm-audit",
			Severity:   strings.ToLower(v.Severity),
			Category:   "dependency",
			File:       file,
			Title:      title,
			TargetRole: owner,
		})
	}
	return out
}

func hasGoModule(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	if err == nil {
		return true
	}
	// nested module?
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "go.mod" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func npmDirs(dir string) []string {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "package.json" {
			dirs = append(dirs, filepath.Dir(p))
		}
		return nil
	})
	return dirs
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}

func atoi(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
