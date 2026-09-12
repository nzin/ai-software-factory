package buildgate

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/nzin/ai-software-factory/internal/factory"
)

// composeService is the slice of a compose service the deploy lint reads.
type composeService struct {
	Build       yaml.Node `yaml:"build"`
	Healthcheck yaml.Node `yaml:"healthcheck"`
	Ports       yaml.Node `yaml:"ports"`
}

// buildSpec is where a compose service's image is built from, repo-relative.
type buildSpec struct {
	Service     string
	Context     string // slash-separated, "." for the repo root
	Dockerfile  string // slash-separated
	ComposeFile string // the compose file that declares the build
	Line        int    // the service's line in ComposeFile
}

// composeDoc is one parsed compose file.
type composeDoc struct {
	services map[string]composeService
	lines    map[string]int // service name -> line of its key
}

func loadCompose(dir, name string) (*composeDoc, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	doc := &composeDoc{lines: map[string]int{}}
	if len(root.Content) == 0 {
		return doc, nil
	}
	var raw struct {
		Services map[string]composeService `yaml:"services"`
	}
	if err := root.Decode(&raw); err != nil {
		return nil, err
	}
	doc.services = raw.Services
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "services" {
			continue
		}
		svcs := top.Content[i+1]
		for j := 0; j+1 < len(svcs.Content); j += 2 {
			doc.lines[svcs.Content[j].Value] = svcs.Content[j].Line
		}
	}
	return doc, nil
}

// build resolves a service's build section — the short form (a context path)
// or the long form ({context, dockerfile}). ok is false when there is none, or
// when it builds from something the lint can't read: a remote or interpolated
// context, an inline Dockerfile, a path outside the repo.
func (s composeService) build() (ctxDir, dockerfile string, ok bool) {
	switch s.Build.Kind {
	case yaml.ScalarNode:
		ctxDir = s.Build.Value
	case yaml.MappingNode:
		var long struct {
			Context          string `yaml:"context"`
			Dockerfile       string `yaml:"dockerfile"`
			DockerfileInline string `yaml:"dockerfile_inline"`
		}
		if s.Build.Decode(&long) != nil || long.DockerfileInline != "" {
			return "", "", false
		}
		ctxDir, dockerfile = long.Context, long.Dockerfile
	default:
		return "", "", false
	}
	if ctxDir == "" {
		ctxDir = "."
	}
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	for _, p := range []string{ctxDir, dockerfile} {
		if strings.Contains(p, "://") || strings.Contains(p, "$") || strings.HasPrefix(p, "git@") || filepath.IsAbs(p) {
			return "", "", false
		}
	}
	ctxDir = path.Clean(filepath.ToSlash(ctxDir))
	if ctxDir == ".." || strings.HasPrefix(ctxDir, "../") {
		return "", "", false
	}
	return ctxDir, path.Join(ctxDir, filepath.ToSlash(dockerfile)), true
}

// composeBuilds lists every service the root compose file and the test overlay
// build from a local Dockerfile, sorted by service; the overlay's build of a
// service wins over the root file's.
func composeBuilds(dir string) []buildSpec {
	byName := map[string]buildSpec{}
	for _, name := range []string{composeFile(dir), "docker-compose.test.yml"} {
		if name == "" {
			continue
		}
		doc, err := loadCompose(dir, name)
		if err != nil {
			continue
		}
		for svc, s := range doc.services {
			if ctxDir, df, ok := s.build(); ok {
				byName[svc] = buildSpec{Service: svc, Context: ctxDir, Dockerfile: df, ComposeFile: name, Line: doc.lines[svc]}
			}
		}
	}
	out := make([]buildSpec, 0, len(byName))
	for _, b := range byName {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

// lintDeploy checks, without Docker, what `docker compose config` can't: that
// every service's Dockerfile exists and never COPYs from outside its build
// context, and — when the test overlay is present — that the root compose file
// honours the gateway contract the tester depends on.
func (c *checker) lintDeploy(dir string) {
	base := composeFile(dir)
	if base == "" {
		return
	}
	for _, b := range composeBuilds(dir) {
		c.lintDockerfile(dir, b)
	}
	if hasFile(dir, "docker-compose.test.yml") {
		c.lintGateway(dir, base)
	}
}

func (c *checker) lintDockerfile(dir string, b buildSpec) {
	role := roleForContext(filepath.Join(dir, filepath.FromSlash(b.Context)))
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(b.Dockerfile)))
	if err != nil {
		c.add(factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "deploy",
			File:       b.ComposeFile,
			Line:       b.Line,
			Title:      fmt.Sprintf("service %q builds from %s, which does not exist", b.Service, b.Dockerfile),
			Suggestion: "add the Dockerfile, or point the service's build.context / build.dockerfile at the real one",
			TargetRole: role,
		})
		return
	}
	for _, src := range copySources(string(data)) {
		if !escapes(src.path) {
			continue
		}
		c.add(factory.Finding{
			Source:   "build-gate",
			Severity: "high",
			Category: "deploy",
			File:     b.Dockerfile,
			Line:     src.line,
			Title: truncate(fmt.Sprintf("%s %s reaches outside service %q's build context (%s), so the image build fails with \"not found\"",
				src.op, src.path, b.Service, b.Context), 200),
			Suggestion: fmt.Sprintf("Docker only sends the build context (%s) to the builder. Widen the service's build.context in %s to a directory that contains everything the image needs, and make every COPY/ADD path relative to it.",
				b.Context, b.ComposeFile),
			TargetRole: role,
		})
	}
}

// lintGateway checks the root compose file against what the test overlay relies
// on: a `gateway` service (the tester's only way in) with a healthcheck (the
// tester waits for service_healthy) whose host port is left to GATEWAY_PORT
// (the gate sets it per run so concurrent stacks don't collide).
func (c *checker) lintGateway(dir, base string) {
	doc, err := loadCompose(dir, base)
	if err != nil {
		return
	}
	add := func(line int, title, suggestion string) {
		c.add(factory.Finding{
			Source:     "build-gate",
			Severity:   "high",
			Category:   "deploy",
			File:       base,
			Line:       line,
			Title:      title,
			Suggestion: suggestion,
			TargetRole: factory.RoleBackendDeveloper,
		})
	}
	gw, ok := doc.services["gateway"]
	if !ok {
		add(0, "no `gateway` service in "+base+" — the component tests reach the stack only through it",
			"add the Traefik `gateway` service in front of the app, with a healthcheck on its ping endpoint and ports \"${GATEWAY_PORT:-8080}:80\"")
		return
	}
	line := doc.lines["gateway"]
	if gw.Healthcheck.Kind == 0 && !builtWithHealthcheck(dir, gw) {
		add(line, "`gateway` has no healthcheck — the tester waits for it to be healthy, so it never starts",
			"add a healthcheck against Traefik's ping endpoint, e.g. test: [\"CMD\", \"traefik\", \"healthcheck\", \"--ping\"]")
	}
	if p := hardcodedPort(gw.Ports); p != "" {
		add(line, fmt.Sprintf("`gateway` publishes a fixed host port (%s) — concurrent runs collide on it", p),
			"publish it as \"${GATEWAY_PORT:-8080}:80\"; the build gate sets GATEWAY_PORT per run")
	}
}

// builtWithHealthcheck reports whether a service's image declares its own
// HEALTHCHECK, which compose honours without a healthcheck: section.
func builtWithHealthcheck(dir string, s composeService) bool {
	_, df, ok := s.build()
	if !ok {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(df)))
	if err != nil {
		return false
	}
	for _, ln := range strings.Split(string(data), "\n") {
		if f := strings.Fields(ln); len(f) >= 2 && strings.EqualFold(f[0], "HEALTHCHECK") && !strings.EqualFold(f[1], "NONE") {
			return true
		}
	}
	return false
}

// hardcodedPort returns the first port mapping that pins a literal host port
// ("8080:80", "127.0.0.1:8080:80", {published: 8080}), or "".
func hardcodedPort(ports yaml.Node) string {
	if ports.Kind != yaml.SequenceNode {
		return ""
	}
	for _, p := range ports.Content {
		switch p.Kind {
		case yaml.ScalarNode:
			if strings.Contains(p.Value, "${") {
				continue
			}
			parts := strings.Split(strings.SplitN(p.Value, "/", 2)[0], ":")
			if len(parts) >= 2 && isDigits(parts[len(parts)-2]) {
				return p.Value
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(p.Content); i += 2 {
				if v := p.Content[i+1].Value; p.Content[i].Value == "published" && v != "" && !strings.Contains(v, "${") {
					return v
				}
			}
		}
	}
	return ""
}

// copySource is one local source path of a COPY/ADD instruction.
type copySource struct {
	op   string // COPY or ADD
	path string
	line int // where the instruction starts
}

// copySources lists the local source paths of every COPY/ADD instruction.
// COPY --from=<stage|image>, heredocs, URLs and paths with variable references
// are skipped: they don't read the build context, or can't be resolved without
// building.
func copySources(dockerfile string) []copySource {
	var out []copySource
	lines := strings.Split(dockerfile, "\n")
	for i := 0; i < len(lines); i++ {
		start := i
		ln := strings.TrimSpace(lines[i])
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		for strings.HasSuffix(ln, "\\") && i+1 < len(lines) {
			i++
			ln = strings.TrimSuffix(ln, "\\") + " " + strings.TrimSpace(lines[i])
		}
		op, rest, _ := strings.Cut(ln, " ")
		op = strings.ToUpper(op)
		if op != "COPY" && op != "ADD" {
			continue
		}
		rest = strings.TrimSpace(rest)
		fromStage := false
		for strings.HasPrefix(rest, "--") {
			var flag string
			flag, rest, _ = strings.Cut(rest, " ")
			fromStage = fromStage || strings.HasPrefix(flag, "--from")
			rest = strings.TrimSpace(rest)
		}
		if fromStage || strings.HasPrefix(rest, "<<") {
			continue
		}
		var parts []string
		if strings.HasPrefix(rest, "[") {
			if json.Unmarshal([]byte(rest), &parts) != nil {
				continue
			}
		} else {
			parts = strings.Fields(rest)
		}
		if len(parts) < 2 {
			continue
		}
		for _, src := range parts[:len(parts)-1] {
			if strings.Contains(src, "://") || strings.Contains(src, "$") {
				continue
			}
			out = append(out, copySource{op: op, path: src, line: start + 1})
		}
	}
	return out
}

// escapes reports whether a COPY/ADD source climbs out of the build context. A
// leading "/" does not: Docker resolves it against the context root.
func escapes(src string) bool {
	p := path.Clean(strings.TrimPrefix(filepath.ToSlash(src), "/"))
	return p == ".." || strings.HasPrefix(p, "../")
}

// roleForContext names the developer who owns a build context: a Node project
// belongs to frontend (mobile for React Native), anything else to backend.
func roleForContext(ctxDir string) string {
	if !hasFile(ctxDir, "package.json") {
		return factory.RoleBackendDeveloper
	}
	if _, rn, _ := readPackage(ctxDir); rn {
		return factory.RoleMobileDeveloper
	}
	return factory.RoleFrontendDev
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
