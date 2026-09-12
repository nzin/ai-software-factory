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
	// the change actually touches, and only once the code compiles. The deploy
	// lint is pure Go; validating the compose file and running the stack need
	// Docker.
	if hasService(goDir, nodeDirs) && isCodeChange(changed) {
		docker := lookPath("docker")
		if docker {
			ran = append(ran, "compose")
			c.checkCompose(ctx, dir)
		}
		c.lintDeploy(dir)
		if docker && len(c.findings) == 0 && hasFile(dir, "docker-compose.test.yml") {
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
			Evidence:   strings.TrimSpace(line),
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
	scripts, rn, playwright := readPackage(dir)
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
	// A Playwright suite only has real browsers inside the docker-compose
	// `tester` service (see checkComponent) — running `npm test` here, on the
	// build gate's own bare filesystem, always fails with a missing browser
	// binary regardless of what the generated project did right.
	if s, has := scripts["test"]; has && !isPlaceholderTest(s) && !playwright {
		if out, ok := run(ctx, dir, []string{"CI=1"}, "npm", "test"); !ok {
			c.fail("npm test", out, role)
			c.add(buildFinding("npm test failed", out, role))
		}
	}
}

func readPackage(dir string) (scripts map[string]string, reactNative, playwright bool) {
	scripts = map[string]string{}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return scripts, false, false
	}
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return scripts, false, false
	}
	for k, v := range pkg.Scripts {
		scripts[k] = v
	}
	_, rn := pkg.Dependencies["react-native"]
	_, rnDev := pkg.DevDependencies["react-native"]
	_, pw := pkg.Dependencies["@playwright/test"]
	_, pwDev := pkg.DevDependencies["@playwright/test"]
	return scripts, rn || rnDev, pw || pwDev
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
			Evidence:   tail(out, maxDetail),
			TargetRole: factory.RoleBackendDeveloper,
		})
	}
}

// checkComponent builds the stack's images, runs the tester service in the
// compose stack and treats its exit code as the verdict. It always tears the
// stack down.
//
// Building is its own step so a failure says which step broke: an image that
// doesn't build is a Dockerfile problem, reported from the build log; a failed
// run is reported from the tester's own log — never from `up`'s build and pull
// progress, which is all an `up --build` failure used to show.
//
// The gateway's host port defaults to 8080 in the generated docker-compose.yml
// (${GATEWAY_PORT:-8080}) for a real deployment; here it's overridden to an
// ephemeral port (GATEWAY_PORT=0) so concurrent runs' stacks never collide on
// a fixed host port — see checkComponent's caller doc and test-engineer.md.
func (c *checker) checkComponent(ctx context.Context, runID, dir string) {
	proj := "asf-" + shortID(runID)
	files := []string{"-p", proj, "-f", composeFile(dir), "-f", "docker-compose.test.yml"}
	compose := func(args ...string) []string {
		return append(append([]string{"compose"}, files...), args...)
	}
	env := []string{"GATEWAY_PORT=0"}

	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer dcancel()
		_, _ = run(dctx, dir, env, "docker", compose("down", "-v", "--remove-orphans", "--rmi", "local")...)
	}()

	cctx, cancel := context.WithTimeout(ctx, composeStep)
	defer cancel()

	// --progress is a global compose flag: it goes before the subcommand.
	build := append([]string{"compose", "--progress", "plain"}, files...)
	if out, ok := run(cctx, dir, env, "docker", append(build, "build")...); !ok {
		c.fail("stack build", out, factory.RoleBackendDeveloper)
		c.add(stackBuildFinding(dir, out))
		return
	}

	out, ok := run(cctx, dir, env, "docker", compose("up", "--no-build", "--quiet-pull", "--abort-on-container-exit", "--exit-code-from", "tester")...)
	testerLog := composeLogs(ctx, dir, env, compose("logs", "--no-color", "--no-log-prefix", "tester"))
	if !ok {
		report := componentReport(ctx, dir, env, compose, out, testerLog, cctx.Err() != nil)
		c.fail("component tests", report, factory.RoleBackendDeveloper)
		c.add(componentFinding(testerLog, report))
	}

	// The tester container is stopped but not removed yet (the `down` above is
	// deferred) — pull out any Playwright screenshots and report what ran,
	// regardless of pass/fail.
	c.extractScreenshots(ctx, dir, files)
	if strings.TrimSpace(testerLog) == "" {
		testerLog = out
	}
	c.testBreakdown = summarizeComponentTests(testerLog, len(c.screenshots))
}

// composeLogs best-effort reads one `docker compose logs` invocation.
func composeLogs(ctx context.Context, dir string, env, args []string) string {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := run(lctx, dir, env, "docker", args...)
	return out
}

// componentReport is what the LLM pass gets for a failed run: the tester's own
// log; every service's log tail when the tester never reported a result (a
// dependency that never got healthy, a crash on start); then the tail of `up`'s
// interleaved stream for anything else.
func componentReport(ctx context.Context, dir string, env []string, compose func(...string) []string, upOut, testerLog string, timedOut bool) string {
	var b strings.Builder
	if timedOut {
		fmt.Fprintf(&b, "The component run was killed after %s without finishing.\n\n", composeStep)
	}
	fmt.Fprintf(&b, "## tester log\n\n%s\n\n", orNone(testerLog))
	if !strings.Contains(testerLog, "[api]") && !strings.Contains(testerLog, "[e2e]") {
		all := composeLogs(ctx, dir, env, compose("logs", "--no-color", "--tail", "80"))
		fmt.Fprintf(&b, "## every service's log (the tester never reported a result)\n\n%s\n\n", orNone(all))
	}
	fmt.Fprintf(&b, "## docker compose up (tail)\n\n%s\n", tail(upOut, maxDetail))
	return b.String()
}

// componentFinding is the deterministic finding for a failed run: the FAIL
// blocks when the suites reported any — routed to frontend when every failure
// is an [e2e] one — else the tail of the report. Each block keeps its
// asf-reporter.cjs detail (the `at file:line` and the first lines of the real
// error/call log), not just the one-line "FAIL [...]:" summary, so the
// location and cause survive into the finding even without an LLM pass.
func componentFinding(testerLog, report string) factory.Finding {
	var fails []string
	apiFailed := false
	lines := strings.Split(testerLog, "\n")
	for i := 0; i < len(lines); i++ {
		idx := strings.Index(lines[i], "FAIL [")
		if idx < 0 {
			continue
		}
		block := []string{strings.TrimSpace(lines[i][idx:])}
		apiFailed = apiFailed || strings.HasPrefix(block[0], "FAIL [api]")
		for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "    ") {
			i++
			block = append(block, strings.TrimSpace(lines[i]))
		}
		fails = append(fails, strings.Join(block, "\n"))
	}
	f := factory.Finding{
		Source:     "build-gate",
		Severity:   "high",
		Category:   "component-test",
		Title:      "component tests failed against the running stack",
		Suggestion: tail(report, maxDetail),
		Evidence:   tail(report, maxDetail),
		TargetRole: factory.RoleBackendDeveloper,
	}
	if len(fails) > 0 {
		f.Title = fmt.Sprintf("%d component test(s) failed against the running stack", len(fails))
		joined := strings.Join(fails, "\n\n")
		f.Suggestion = tail(joined, maxDetail)
		f.Evidence = tail(joined, maxDetail)
		if !apiFailed {
			f.TargetRole = factory.RoleFrontendDev
		}
	}
	return f
}

// Compose's plain build log names the failing service in bake's
// "target <svc>: failed to solve" and in the failed step's " > [<svc> 3/7] …"
// (a stack that builds a single image names none), the failing Dockerfile line
// as "Dockerfile:3", and wraps the cause in buildkit's own prefixes.
var (
	buildTarget = regexp.MustCompile(`target ([A-Za-z0-9_.-]+): failed to solve|> \[([A-Za-z0-9_.-]+) \d+/\d+\]`)
	buildStep   = regexp.MustCompile(`(?m)^\s*> \[(?:[A-Za-z0-9_.-]+ )?\d+/\d+\] (.*?):?\s*$`)
	buildLine   = regexp.MustCompile(`(?m)^\S*Dockerfile\S*:(\d+)\s*$`)
	buildNoise  = regexp.MustCompile(`target [A-Za-z0-9_.-]+: |failed to solve: |failed to compute cache key: |failed to calculate checksum of ref \S+: `)
)

// stackBuildFinding turns a failed `docker compose build` into a finding on the
// failing service's Dockerfile line, owned by whoever owns its build context.
func stackBuildFinding(dir, out string) factory.Finding {
	cause := buildCause(out)
	f := factory.Finding{
		Source:     "build-gate",
		Severity:   "high",
		Category:   "deploy",
		Title:      truncate("docker compose build failed: "+cause, 200),
		Suggestion: tail(out, maxDetail),
		Evidence:   tail(out, maxDetail),
		TargetRole: factory.RoleBackendDeveloper,
	}
	builds := composeBuilds(dir)
	svc := ""
	if m := buildTarget.FindStringSubmatch(out); m != nil {
		svc = m[1] + m[2]
	} else if len(builds) == 1 {
		svc = builds[0].Service
	}
	if svc == "" {
		return f
	}
	at := ""
	if m := buildStep.FindStringSubmatch(out); m != nil {
		at = " at `" + truncate(m[1], 80) + "`"
	}
	f.Title = truncate(fmt.Sprintf("image for service %q failed to build%s: %s", svc, at, cause), 200)
	for _, b := range builds {
		if b.Service != svc {
			continue
		}
		f.File = b.Dockerfile
		f.TargetRole = roleForContext(filepath.Join(dir, filepath.FromSlash(b.Context)))
		if m := buildLine.FindStringSubmatch(out); m != nil {
			f.Line = atoi(m[1])
		}
	}
	return f
}

// buildCause is the most specific error line in a build log — the last
// "failed to solve", else the last ERROR — without buildkit's wrapping.
func buildCause(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, marker := range []string{"failed to solve", "ERROR"} {
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], marker) {
				return strings.TrimSpace(buildNoise.ReplaceAllString(lines[i], ""))
			}
		}
	}
	return "see the build output"
}

func orNone(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "(no output)"
	}
	return s
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
	id := containerID(idOut)
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

// containerIDLine matches a `docker compose ps -q`-style container ID: hex,
// on its own line. run()'s CombinedOutput can put a compose deprecation
// warning (e.g. the obsolete top-level `version:` key) on stdout/stderr
// ahead of the ID, so the ID isn't always the first line of output.
var containerIDLine = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func containerID(out string) string {
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimSpace(ln); containerIDLine.MatchString(ln) {
			return ln
		}
	}
	return ""
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
		if s, _, _ := readPackage(nd); s["build"] != "" || s["start"] != "" {
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
		Evidence:   tail(output, maxDetail),
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

// clip bounds s to about n bytes, keeping its start and — most of the budget —
// its end: a command's cause usually comes last (a build's error, a test run's
// verdict), after a long run of progress output.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	head := min(2_000, n/2) // enough to show what was running
	return s[:head] + fmt.Sprintf("\n… (%d bytes elided) …\n", len(s)-n) + s[len(s)-(n-head):]
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
