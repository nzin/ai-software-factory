// Package buildgate compiles and tests a per-run workspace and turns any
// failure into routed factory.Finding values. Like internal/sast and
// internal/kodus it never returns a hard error: a missing toolchain becomes one
// informational finding so the pipeline still completes.
package buildgate

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// budget bounds the whole gate. npm install for a component library plus
// `go test ./...` is the slow part; a hang past this is itself a failure.
const budget = 8 * time.Minute

// maxDetail caps the toolchain output attached to a finding.
const maxDetail = 3000

// Check builds and tests everything under dir. The returned findings feed
// factory.VerdictFor / RouteRole in the calling agent.
func Check(ctx context.Context, dir string) ([]factory.Finding, string) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	goDir := goModuleDir(dir)
	nodeDirs := packageDirs(dir)

	haveGo := goDir != "" && lookPath("go")
	haveNode := len(nodeDirs) > 0 && lookPath("npm")

	if !haveGo && !haveNode {
		return []factory.Finding{{
			Source:   "build-gate",
			Severity: "info",
			Title:    "build gate skipped: no Go or Node toolchain available",
		}}, "build gate skipped (no toolchain)"
	}

	var findings []factory.Finding
	var ran []string

	if haveGo {
		ran = append(ran, "go")
		findings = append(findings, checkGo(ctx, goDir)...)
	}
	for _, nd := range nodeDirs {
		if !haveNode {
			break
		}
		ran = append(ran, "npm("+shortRel(dir, nd)+")")
		findings = append(findings, checkNode(ctx, nd)...)
	}

	summary := "build gate: " + strings.Join(ran, ", ")
	switch {
	case len(findings) == 0:
		summary += " — clean"
	default:
		summary += " — " + strconv.Itoa(len(findings)) + " problem(s)"
	}
	return findings, summary
}

// --- Go ---

// goErrLine matches `path/file.go:12:5: message` and `path/file.go:12: message`.
var goErrLine = regexp.MustCompile(`^(\S+\.go):(\d+)(?::\d+)?:\s+(.*)$`)

func checkGo(ctx context.Context, dir string) []factory.Finding {
	if out, ok := run(ctx, dir, nil, "go", "build", "./..."); !ok {
		if fs := parseGoErrors(out); len(fs) > 0 {
			return fs
		}
		return []factory.Finding{buildFinding("go build failed", out, factory.RoleBackendDeveloper)}
	}
	if out, ok := run(ctx, dir, nil, "go", "test", "./..."); !ok {
		if fs := parseGoErrors(out); len(fs) > 0 {
			return fs
		}
		return []factory.Finding{buildFinding("go test failed", out, factory.RoleBackendDeveloper)}
	}
	return nil
}

func parseGoErrors(out string) []factory.Finding {
	var findings []factory.Finding
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		m := goErrLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		file := strings.TrimPrefix(m[1], "./")
		key := file + ":" + m[2]
		if seen[key] {
			continue
		}
		seen[key] = true
		findings = append(findings, factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "compile",
			File:       file,
			Line:       atoi(m[2]),
			Title:      truncate(m[3], 200),
			Suggestion: "fix the compile error so `go build ./...` and `go test ./...` pass",
			TargetRole: routeByPath(file),
		})
		if len(findings) >= 20 {
			break
		}
	}
	return findings
}

// --- Node ---

func checkNode(ctx context.Context, dir string) []factory.Finding {
	scripts, rn := readPackage(dir)
	role := factory.RoleFrontendDev
	if rn {
		role = factory.RoleMobileDeveloper
	}

	if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "install", "--no-audit", "--no-fund"); !ok {
		return []factory.Finding{buildFinding("npm install failed", out, role)}
	}
	if _, has := scripts["build"]; has {
		if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "run", "build"); !ok {
			return []factory.Finding{buildFinding("npm run build failed", out, role)}
		}
	}
	if s, has := scripts["test"]; has && !isPlaceholderTest(s) {
		if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "test"); !ok {
			return []factory.Finding{buildFinding("npm test failed", out, role)}
		}
	}
	return nil
}

func readPackage(dir string) (scripts map[string]string, reactNative bool) {
	scripts = map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return scripts, false
	}
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return scripts, false
	}
	for k, v := range pkg.Scripts {
		scripts[k] = v
	}
	_, rn := pkg.Dependencies["react-native"]
	_, rnDev := pkg.DevDependencies["react-native"]
	return scripts, rn || rnDev
}

func isPlaceholderTest(s string) bool {
	return strings.Contains(s, "no test specified")
}

// --- shared ---

func buildFinding(title, output string, role string) factory.Finding {
	return factory.Finding{
		Source:     "build-gate",
		Severity:   "high",
		Category:   "build",
		Title:      title,
		Suggestion: tail(output, maxDetail),
		TargetRole: role,
	}
}

// run executes name+args in dir with extra env, returning combined output and
// whether it exited zero. GOTOOLCHAIN=local pins the image's Go rather than
// letting a generated go.mod trigger a toolchain download.
func run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (string, bool) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func lookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// goModuleDir returns the directory holding go.mod (root preferred), or "".
func goModuleDir(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return dir
	}
	found := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			found = filepath.Dir(p)
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// packageDirs lists every directory with a package.json outside node_modules.
func packageDirs(dir string) []string {
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

func routeByPath(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		return factory.RoleBackendDeveloper
	case ".vue", ".ts", ".tsx", ".jsx", ".css", ".scss":
		return factory.RoleFrontendDev
	}
	if strings.HasSuffix(file, "go.mod") || strings.HasSuffix(file, "go.sum") {
		return factory.RoleBackendDeveloper
	}
	if strings.HasSuffix(file, "package.json") {
		return factory.RoleFrontendDev
	}
	return ""
}

func shortRel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && r != "." {
		return r
	}
	return filepath.Base(p)
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…\n" + s[len(s)-n:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
