// Package testharness lays down the factory-owned half of a feature's
// component-test harness: the tester image (test/Dockerfile) and its
// entrypoint, the docker-compose.test.yml overlay, the component suite's
// go.mod, and — when there are Playwright specs — the e2e package.json pin,
// config and reporter.
//
// The test-engineer model writes only test code (test/component/main.go,
// test/e2e/*.spec.ts). Everything that is identical from one PRD to the next
// lives here so it cannot drift between runs: the build context that sees both
// suites, the base images, and the Playwright version that has to match the
// browsers baked into its image.
package testharness

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"text/template"

	yaml "go.yaml.in/yaml/v3"
)

const (
	// PlaywrightVersion is both the exact @playwright/test pin and the tester
	// image's base tag. The image's preinstalled browsers are locked to this
	// release; any other npm version fails browserType.launch at test time.
	PlaywrightVersion = "1.63.0"
	// PlaywrightImage is the e2e tester's base image.
	PlaywrightImage = "mcr.microsoft.com/playwright:v" + PlaywrightVersion + "-noble"
	// GoImage builds the component suite — the factory's own Go (Dockerfile.buildgate).
	GoImage = "golang:1.26"
	// RuntimeImage runs an API-only tester, which needs no browsers.
	RuntimeImage = "alpine:3"
)

// ComponentMain marks a workspace that has a component suite. Without it (a
// library or a docs-only change, where test-engineer wrote nothing) Materialize
// does nothing.
const ComponentMain = "test/component/main.go"

// componentGoMod is the component suite's module file: standard library only,
// on the factory's Go, so neither a go directive nor a toolchain line can drift.
const componentGoMod = "module example.com/component-test\n\ngo 1.26\n"

// obsolete lists files a model used to write that the harness now supersedes.
var obsolete = []string{
	"test/component/Dockerfile",
	"test/e2e/Dockerfile",
	"test/e2e/package-lock.json",
	"test/e2e/npm-shrinkwrap.json",
	"test/e2e/playwright.config.js",
	"test/e2e/playwright.config.mjs",
	"test/e2e/playwright.config.cjs",
	"test/e2e/playwright.config.mts",
	"test/e2e/playwright.config.cts",
}

// composeNames mirrors internal/buildgate's root compose lookup order.
var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

var specFile = regexp.MustCompile(`\.(spec|test)\.[cm]?[jt]sx?$`)

//go:embed templates
var templates embed.FS

var tmpl = template.Must(template.ParseFS(templates, "templates/*.tmpl"))

type file struct {
	path string
	body []byte
	mode os.FileMode
}

// Materialize writes the harness into the repo at dir, overwriting any copy a
// model wrote, and removes the files it supersedes. It returns the repo-relative
// paths it wrote (only those whose content changed) and removed, so a second
// call on an unchanged tree returns nothing.
func Materialize(dir string) (written, removed []string, err error) {
	if !isFile(filepath.Join(dir, filepath.FromSlash(ComponentMain))) {
		return nil, nil, nil
	}
	files, err := render(dir, hasSpecs(filepath.Join(dir, "test", "e2e")))
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		changed, err := writeIfChanged(filepath.Join(dir, filepath.FromSlash(f.path)), f.body, f.mode)
		if err != nil {
			return written, removed, err
		}
		if changed {
			written = append(written, f.path)
		}
	}
	for _, rel := range obsolete {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if !isFile(full) {
			continue
		}
		if err := os.Remove(full); err != nil {
			return written, removed, err
		}
		removed = append(removed, rel)
	}
	sort.Strings(written)
	return written, removed, nil
}

func render(dir string, e2e bool) ([]file, error) {
	data := struct {
		GoImage, RuntimeImage, PlaywrightImage string
		E2E                                    bool
		Networks                               []string
	}{GoImage, RuntimeImage, PlaywrightImage, e2e, gatewayNetworks(dir)}

	dockerfile, err := execute("Dockerfile.tmpl", data)
	if err != nil {
		return nil, err
	}
	overlay, err := execute("docker-compose.test.yml.tmpl", data)
	if err != nil {
		return nil, err
	}
	files := []file{
		{"docker-compose.test.yml", overlay, 0o644},
		{"test/Dockerfile", dockerfile, 0o644},
		{"test/.dockerignore", static("dockerignore"), 0o644},
		{"test/.gitignore", static("gitignore"), 0o644},
		{"test/run.sh", static("run.sh"), 0o755},
		{"test/component/go.mod", []byte(componentGoMod), 0o644},
	}
	if !e2e {
		return files, nil
	}
	old, _ := os.ReadFile(filepath.Join(dir, "test", "e2e", "package.json"))
	pkg, err := normalizePackageJSON(old)
	if err != nil {
		return nil, err
	}
	return append(files,
		file{"test/e2e/package.json", pkg, 0o644},
		file{"test/e2e/playwright.config.ts", static("playwright.config.ts"), 0o644},
		file{"test/e2e/asf-reporter.cjs", static("asf-reporter.cjs"), 0o644},
	), nil
}

func execute(name string, data any) ([]byte, error) {
	var b bytes.Buffer
	if err := tmpl.ExecuteTemplate(&b, name, data); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func static(name string) []byte {
	b, err := templates.ReadFile("templates/" + name)
	if err != nil {
		panic("testharness: missing embedded template " + name)
	}
	return b
}

// normalizePackageJSON pins @playwright/test to PlaywrightVersion — exact, as a
// devDependency — in the model's package.json, keeping anything else it
// declared, and drops the other Playwright packages, which would pull in a
// second, differently-versioned playwright-core.
func normalizePackageJSON(old []byte) ([]byte, error) {
	pkg := map[string]any{}
	if len(bytes.TrimSpace(old)) > 0 && json.Unmarshal(old, &pkg) != nil {
		pkg = map[string]any{} // unparseable: start over rather than fail the run
	}
	deps, dev, scripts := section(pkg, "dependencies"), section(pkg, "devDependencies"), section(pkg, "scripts")
	for _, name := range []string{"@playwright/test", "playwright", "playwright-core"} {
		delete(deps, name)
		delete(dev, name)
	}
	dev["@playwright/test"] = PlaywrightVersion
	scripts["test"] = "playwright test"
	pkg["devDependencies"], pkg["scripts"], pkg["private"] = dev, scripts, true
	if len(deps) == 0 {
		delete(pkg, "dependencies")
	} else {
		pkg["dependencies"] = deps
	}
	if _, ok := pkg["name"]; !ok {
		pkg["name"] = "e2e"
	}

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pkg); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func section(pkg map[string]any, key string) map[string]any {
	if m, ok := pkg[key].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// gatewayNetworks returns the networks the root compose file attaches the
// gateway to, so the tester joins the same ones. nil means compose's default
// network, which the tester joins on its own.
func gatewayNetworks(dir string) []string {
	for _, name := range composeNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var doc struct {
			Services map[string]struct {
				Networks yaml.Node `yaml:"networks"`
			} `yaml:"services"`
		}
		if yaml.Unmarshal(b, &doc) != nil {
			return nil
		}
		n := doc.Services["gateway"].Networks
		var out []string
		switch n.Kind {
		case yaml.SequenceNode:
			for _, c := range n.Content {
				out = append(out, c.Value)
			}
		case yaml.MappingNode:
			for i := 0; i < len(n.Content); i += 2 {
				out = append(out, n.Content[i].Value)
			}
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// hasSpecs reports whether dir holds at least one Playwright spec file.
func hasSpecs(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "screenshots") {
			return filepath.SkipDir
		}
		if !d.IsDir() && specFile.MatchString(d.Name()) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func writeIfChanged(full string, body []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(full); err == nil && bytes.Equal(old, body) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(full, body, mode); err != nil {
		return false, err
	}
	return true, os.Chmod(full, mode)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
