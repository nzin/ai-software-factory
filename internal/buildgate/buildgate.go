// Package buildgate compiles and tests a per-run workspace, validates its
// docker-compose deployment, and runs the component test suite over
// `docker compose up`. Every failure becomes a routed factory.Finding. Like
// internal/sast and internal/kodus it never returns a hard error: a missing
// toolchain becomes one informational finding so the pipeline still completes.
package buildgate

import (
	"context"
	"encoding/json"
	"fmt"
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

const (
	// budget bounds the whole gate. `docker compose up --build` of a Go+Vue
	// stack plus `go test ./...` is the slow part; a hang past this is a failure.
	budget = 20 * time.Minute
	// composeStep bounds the component-test compose run on its own.
	composeStep = 14 * time.Minute
	// maxDetail caps the toolchain output attached to a finding.
	maxDetail = 3000
	// maxFailBytes caps the raw output handed to the LLM per failure.
	maxFailBytes = 12_000
)

// Failure is one command that exited non-zero, kept so the agent's LLM pass can
// summarise it into tighter findings.
type Failure struct {
	Kind        string // "go build" | "go test" | "npm run build" | "compose config" | "component tests"
	Output      string
	DefaultRole string
}

// Result is what Check returns: the deterministic findings (file:line anchors),
// a one-line summary, the raw failures, and any Playwright screenshots pulled
// out of the tester container (paths relative to the workspace dir).
type Result struct {
	Findings    []factory.Finding
	Summary     string
	Failures    []Failure
	Screenshots []string
}

// Check builds, tests, validates deployment and runs component tests under dir.
// runID scopes the compose project name so concurrent runs stay isolated.
// changed is the run's changed-file list (vs the base branch); when it names
// only non-code files the deployment + component checks are skipped.
func Check(ctx context.Context, runID, dir string, changed []string) Result {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	goDir := goModuleDir(dir)
	nodeDirs := packageDirs(dir)
	haveGo := goDir != "" && lookPath("go")
	haveNode := len(nodeDirs) > 0 && lookPath("npm")

	if !haveGo && !haveNode {
		return Result{
			Findings: []factory.Finding{{
				Source:   "build-gate",
				Severity: "info",
				Title:    "build gate skipped: no Go or Node toolchain available",
			}},
			Summary: "build gate skipped (no toolchain)",
		}
	}

	c := &checker{dir: dir}
	var ran []string

	if haveGo {
		ran = append(ran, "go")
		c.checkGo(ctx, goDir)
	}
	for _, nd := range nodeDirs {
		if !haveNode {
			break
		}
		ran = append(ran, "npm("+shortRel(dir, nd)+")")
		c.checkNode(ctx, nd)
	}

	// Deployment + component tests only make sense for a runnable service that
	// the change actually touches, and only once the code compiles.
	if hasService(goDir, nodeDirs) && isCodeChange(changed) && lookPath("docker") {
		ran = append(ran, "compose")
		c.checkCompose(ctx, dir)
		if len(c.findings) == 0 && hasFile(dir, "docker-compose.test.yml") {
			ran = append(ran, "component")
			c.checkComponent(ctx, runID, dir)
		}
	}

	summary := "build gate: " + strings.Join(ran, ", ")
	if len(c.findings) == 0 {
		summary += " — clean"
	} else {
		summary += " — " + strconv.Itoa(len(c.findings)) + " problem(s)"
	}
	if c.testBreakdown != "" {
		summary += " (" + c.testBreakdown + ")"
	}
	return Result{Findings: c.findings, Summary: summary, Failures: c.failures, Screenshots: c.screenshots}
}

type checker struct {
	dir           string
	findings      []factory.Finding
	failures      []Failure
	screenshots   []string // paths relative to dir, pulled from the tester container
	testBreakdown string   // e.g. "api 8/8 passed, e2e 5/5 passed, 3 screenshot(s) captured"
}

func (c *checker) add(f factory.Finding) { c.findings = append(c.findings, f) }
func (c *checker) fail(kind, out, role string) {
	c.failures = append(c.failures, Failure{Kind: kind, Output: clip(out, maxFailBytes), DefaultRole: role})
}

// --- Go ---

// goErrLine matches `path/file.go:12:5: message` and `path/file.go:12: message`.
var goErrLine = regexp.MustCompile(`^(\S+\.go):(\d+)(?::\d+)?:\s+(.*)$`)

func (c *checker) checkGo(ctx context.Context, dir string) {
	if out, ok := run(ctx, dir, nil, "go", "build", "./..."); !ok {
		c.fail("go build", out, factory.RoleBackendDeveloper)
		c.goFindings("go build failed", out)
		return
	}
	if out, ok := run(ctx, dir, nil, "go", "test", "./..."); !ok {
		c.fail("go test", out, factory.RoleBackendDeveloper)
		c.goFindings("go test failed", out)
	}
}

func (c *checker) goFindings(title, out string) {
	if fs := parseGoErrors(out); len(fs) > 0 {
		c.findings = append(c.findings, fs...)
		return
	}
	c.add(buildFinding(title, out, factory.RoleBackendDeveloper))
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
			TargetRole: factory.RoleForPath(file),
		})
		if len(findings) >= 20 {
			break
		}
	}
	return findings
}

// --- Node ---

func (c *checker) checkNode(ctx context.Context, dir string) {
	scripts, rn := readPackage(dir)
	role := factory.RoleFrontendDev
	if rn {
		role = factory.RoleMobileDeveloper
	}

	if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "install", "--no-audit", "--no-fund"); !ok {
		c.fail("npm install", out, role)
		c.add(buildFinding("npm install failed", out, role))
		return
	}
	if _, has := scripts["build"]; has {
		if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "run", "build"); !ok {
			c.fail("npm run build", out, role)
			c.add(buildFinding("npm run build failed", out, role))
			return
		}
	}
	if s, has := scripts["test"]; has && !isPlaceholderTest(s) {
		if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "test"); !ok {
			c.fail("npm test", out, role)
			c.add(buildFinding("npm test failed", out, role))
		}
	}
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

// --- deployment ---

var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

func composeFile(dir string) string {
	for _, n := range composeNames {
		if hasFile(dir, n) {
			return n
		}
	}
	return ""
}

func (c *checker) checkCompose(ctx context.Context, dir string) {
	file := composeFile(dir)
	if file == "" {
		c.add(factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "deploy",
			Title:      "no root docker-compose.yml — the feature can't be deployed with `docker compose up`",
			Suggestion: "add a root docker-compose.yml wiring the service Dockerfile(s), with a healthcheck on the app service",
			TargetRole: factory.RoleBackendDeveloper,
		})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if out, ok := run(cctx, dir, nil, "docker", "compose", "-f", file, "config", "-q"); !ok {
		c.fail("compose config", out, factory.RoleBackendDeveloper)
		c.add(factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "deploy",
			File:       file,
			Title:      "docker-compose.yml is not valid",
			Suggestion: tail(out, maxDetail),
			TargetRole: factory.RoleBackendDeveloper,
		})
	}
}

// checkComponent runs the tester service in the compose stack and treats its
// exit code as the verdict. It always tears the stack down.
//
// The gateway's host port defaults to 8080 in the generated docker-compose.yml
// (${GATEWAY_PORT:-8080}) for a real deployment; here it's overridden to an
// ephemeral port (GATEWAY_PORT=0) so concurrent runs' stacks never collide on
// a fixed host port — see checkComponent's caller doc and test-engineer.md.
func (c *checker) checkComponent(ctx context.Context, runID, dir string) {
	base := composeFile(dir)
	proj := "asf-" + shortID(runID)
	files := []string{"-p", proj, "-f", base, "-f", "docker-compose.test.yml"}

	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer dcancel()
		down := append([]string{"compose"}, files...)
		down = append(down, "down", "-v", "--remove-orphans", "--rmi", "local")
		_, _ = run(dctx, dir, nil, "docker", down...)
	}()

	cctx, cancel := context.WithTimeout(ctx, composeStep)
	defer cancel()
	up := append([]string{"compose"}, files...)
	up = append(up, "up", "--build", "--quiet-pull", "--abort-on-container-exit", "--exit-code-from", "tester")
	out, ok := run(cctx, dir, []string{"GATEWAY_PORT=0"}, "docker", up...)
	if !ok {
		c.fail("component tests", out, factory.RoleBackendDeveloper)
		c.add(factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "component-test",
			Title:      "component tests failed against the running stack",
			Suggestion: tail(out, maxDetail),
			TargetRole: factory.RoleBackendDeveloper,
		})
	}

	// The tester container is stopped but not removed yet (the `down` above is
	// deferred) — pull out any Playwright screenshots and report what ran,
	// regardless of pass/fail.
	c.extractScreenshots(ctx, dir, files)
	c.testBreakdown = summarizeComponentTests(out, len(c.screenshots))
}

// extractScreenshots best-effort docker-cp's /output/screenshots out of the
// tester container (the fixed path test-engineer.md's Playwright suite writes
// to) into <dir>/test/e2e/screenshots, and records what landed there. A
// service with no frontend has no such path — that's not an error.
func (c *checker) extractScreenshots(ctx context.Context, dir string, files []string) {
	xctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ps := append([]string{"compose"}, files...)
	ps = append(ps, "ps", "-a", "-q", "tester")
	idOut, ok := run(xctx, dir, nil, "docker", ps...)
	id := strings.TrimSpace(strings.SplitN(idOut, "\n", 2)[0])
	if !ok || id == "" {
		return
	}

	dest := filepath.Join(dir, "test", "e2e", "screenshots")
	_ = os.RemoveAll(dest)
	if _, ok := run(xctx, dir, nil, "docker", "cp", id+":/output/screenshots", dest); !ok {
		return
	}

	_ = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			c.screenshots = append(c.screenshots, filepath.ToSlash(rel))
		}
		return nil
	})
}

// summarizeComponentTests reports how many [api] and [e2e] tests passed, from
// the PASS [api]/FAIL [api]/PASS [e2e]/FAIL [e2e] lines test-engineer.md's two
// suites print, plus how many screenshots were captured.
func summarizeComponentTests(out string, screenshots int) string {
	var parts []string
	if apiPass, apiFail := strings.Count(out, "PASS [api]"), strings.Count(out, "FAIL [api]"); apiPass+apiFail > 0 {
		parts = append(parts, fmt.Sprintf("api %d/%d passed", apiPass, apiPass+apiFail))
	}
	if e2ePass, e2eFail := strings.Count(out, "PASS [e2e]"), strings.Count(out, "FAIL [e2e]"); e2ePass+e2eFail > 0 {
		parts = append(parts, fmt.Sprintf("e2e %d/%d passed", e2ePass, e2ePass+e2eFail))
	}
	if screenshots > 0 {
		parts = append(parts, fmt.Sprintf("%d screenshot(s) captured", screenshots))
	}
	return strings.Join(parts, ", ")
}

var codeExt = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".vue": true,
	".py": true, ".rb": true, ".java": true, ".rs": true, ".mjs": true, ".cjs": true,
}

// isCodeChange reports whether the change touches source or deployment config
// (vs. a docs-only change). A nil list (couldn't diff) is treated as a code
// change so the checks still run.
func isCodeChange(changed []string) bool {
	if changed == nil {
		return true
	}
	for _, f := range changed {
		b := filepath.Base(f)
		if codeExt[strings.ToLower(filepath.Ext(f))] ||
			b == "go.mod" || b == "go.sum" || b == "package.json" ||
			b == "Dockerfile" || strings.HasPrefix(b, "Dockerfile.") ||
			strings.HasPrefix(b, "docker-compose") || strings.HasPrefix(b, "compose.") {
			return true
		}
	}
	return false
}

// hasService reports whether the repo builds a runnable service worth deploying
// and component-testing (vs. a library or a docs-only change).
func hasService(goDir string, nodeDirs []string) bool {
	if goDir != "" {
		if hasFile(goDir, "main.go") {
			return true
		}
		if m, _ := filepath.Glob(filepath.Join(goDir, "cmd", "*", "main.go")); len(m) > 0 {
			return true
		}
	}
	for _, nd := range nodeDirs {
		if s, _ := readPackage(nd); s["build"] != "" || s["start"] != "" {
			return true
		}
	}
	return false
}

// --- shared ---

func buildFinding(title, output, role string) factory.Finding {
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

func hasFile(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

// goModuleDir returns the directory holding go.mod (root preferred), or "".
func goModuleDir(dir string) string {
	if hasFile(dir, "go.mod") {
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

func shortRel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil && r != "." {
		return r
	}
	return filepath.Base(p)
}

// shortID sanitises a run id into a compose-project-safe slug.
func shortID(s string) string {
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(s))
	if len(s) > 8 {
		s = s[:8]
	}
	if s == "" {
		return "run"
	}
	return s
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…\n" + s[len(s)-n:]
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (truncated)"
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
