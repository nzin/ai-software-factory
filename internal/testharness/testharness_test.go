package testharness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

const mainGo = "package main\n\nfunc main() {}\n"

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

func TestMaterializeWithoutAComponentSuiteIsANoOp(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", mainGo)

	written, removed, err := Materialize(dir)
	if err != nil || len(written)+len(removed) != 0 {
		t.Fatalf("Materialize = %v, %v, %v; want nothing", written, removed, err)
	}
	if exists(dir, "test/Dockerfile") || exists(dir, "docker-compose.test.yml") {
		t.Fatal("wrote a harness for a repo with no component suite")
	}
}

func TestMaterializeAPIOnly(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ComponentMain, mainGo)
	write(t, dir, "test/component/go.mod", "module x\n\ngo 1.22\n\ntoolchain go1.27.0\n")

	written, _, err := Materialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "docker-compose.test.yml,test/.dockerignore,test/.gitignore,test/Dockerfile,test/component/go.mod,test/run.sh"
	if got := strings.Join(written, ","); got != want {
		t.Fatalf("written = %s, want %s", got, want)
	}
	df := read(t, dir, "test/Dockerfile")
	if !strings.Contains(df, "FROM "+RuntimeImage) || strings.Contains(df, "playwright") || strings.Contains(df, "e2e/") {
		t.Fatalf("an API-only tester must not involve Playwright:\n%s", df)
	}
	if got := read(t, dir, "test/component/go.mod"); got != componentGoMod {
		t.Fatalf("go.mod = %q, want the factory's", got)
	}
	if st, err := os.Stat(filepath.Join(dir, "test", "run.sh")); err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("run.sh must be executable: %v %v", st.Mode(), err)
	}
	if exists(dir, "test/e2e/package.json") {
		t.Fatal("no specs, yet an e2e package.json was written")
	}
}

// The failure this package exists for: an npm Playwright that doesn't match
// the tester image's browsers. Whatever the model pinned, both sides must end
// up on PlaywrightVersion, and the stale Dockerfile that COPYed ../e2e from
// outside its build context must be gone.
func TestMaterializeE2EPinsPlaywrightInLockStep(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ComponentMain, mainGo)
	write(t, dir, "test/e2e/home.spec.ts", "import { test } from '@playwright/test';\n")
	write(t, dir, "test/e2e/package.json",
		`{"name":"e2e","dependencies":{"@playwright/test":"^1.45.0","dayjs":"1.11.0"},"devDependencies":{"playwright":"1.48.0"}}`)
	write(t, dir, "test/e2e/package-lock.json", "{}")
	write(t, dir, "test/e2e/playwright.config.js", "module.exports = {};\n")
	write(t, dir, "test/component/Dockerfile", "FROM mcr.microsoft.com/playwright:v1.45.0-jammy\nCOPY ../e2e /e2e\n")

	_, removed, err := Materialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(removed, ","), "test/component/Dockerfile,test/e2e/package-lock.json,test/e2e/playwright.config.js"; got != want {
		t.Fatalf("removed = %s, want %s", got, want)
	}
	for _, p := range removed {
		if exists(dir, p) {
			t.Fatalf("%s still on disk", p)
		}
	}

	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(read(t, dir, "test/e2e/package.json")), &pkg); err != nil {
		t.Fatal(err)
	}
	pin := pkg.DevDependencies["@playwright/test"]
	if pin != PlaywrightVersion {
		t.Fatalf("@playwright/test = %q, want exactly %s", pin, PlaywrightVersion)
	}
	if _, ok := pkg.Dependencies["@playwright/test"]; ok {
		t.Fatal("@playwright/test left in dependencies")
	}
	if _, ok := pkg.DevDependencies["playwright"]; ok {
		t.Fatal("a second Playwright package would pull its own playwright-core")
	}
	if pkg.Dependencies["dayjs"] != "1.11.0" || pkg.Scripts["test"] != "playwright test" {
		t.Fatalf("the model's other declarations should survive: %+v", pkg)
	}

	df := read(t, dir, "test/Dockerfile")
	if !strings.Contains(df, "FROM mcr.microsoft.com/playwright:v"+pin+"-") {
		t.Fatalf("the image tag and the npm pin must be the same version (%s):\n%s", pin, df)
	}
	if strings.Contains(df, "../") {
		t.Fatalf("the tester Dockerfile must stay inside its build context:\n%s", df)
	}
	for _, p := range []string{"test/e2e/playwright.config.ts", "test/e2e/asf-reporter.cjs"} {
		if !exists(dir, p) {
			t.Fatalf("%s not written", p)
		}
	}
}

func TestMaterializeIsIdempotentAndRestoresClobberedFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ComponentMain, mainGo)
	write(t, dir, "test/e2e/a.spec.ts", "")
	if _, _, err := Materialize(dir); err != nil {
		t.Fatal(err)
	}

	written, removed, err := Materialize(dir)
	if err != nil || len(written)+len(removed) != 0 {
		t.Fatalf("second Materialize = %v, %v, %v; want nothing", written, removed, err)
	}

	write(t, dir, "test/Dockerfile", "FROM golang:1.22\n") // a fix pass "helping"
	written, _, _ = Materialize(dir)
	if got := strings.Join(written, ","); got != "test/Dockerfile" {
		t.Fatalf("written = %s, want only the restored test/Dockerfile", got)
	}
}

func TestOverlayTargetsTheGateway(t *testing.T) {
	cases := []struct {
		name, compose, networks string
	}{
		{"default network", "services:\n  gateway:\n    image: traefik:v3\n", ""},
		{"network list", "services:\n  gateway:\n    image: traefik:v3\n    networks: [internal, edge]\n", "edge,internal"},
		{"network map", "services:\n  gateway:\n    image: traefik:v3\n    networks:\n      edge: {}\n      internal:\n        aliases: [gw]\n", "edge,internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, ComponentMain, mainGo)
			write(t, dir, "docker-compose.yml", tc.compose)
			if _, _, err := Materialize(dir); err != nil {
				t.Fatal(err)
			}

			var doc struct {
				Services map[string]struct {
					Build struct {
						Context string `yaml:"context"`
					} `yaml:"build"`
					Environment map[string]string `yaml:"environment"`
					DependsOn   map[string]struct {
						Condition string `yaml:"condition"`
					} `yaml:"depends_on"`
					Networks []string `yaml:"networks"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal([]byte(read(t, dir, "docker-compose.test.yml")), &doc); err != nil {
				t.Fatal(err)
			}
			tester := doc.Services["tester"]
			if tester.Build.Context != "./test" {
				t.Fatalf("build context = %q, want ./test (so the image sees both suites)", tester.Build.Context)
			}
			if tester.DependsOn["gateway"].Condition != "service_healthy" {
				t.Fatalf("tester must wait for a healthy gateway: %+v", tester.DependsOn)
			}
			if tester.Environment["APP_URL"] != "http://gateway" || tester.Environment["BASE_URL"] != "http://gateway" {
				t.Fatalf("tester must go through the gateway: %+v", tester.Environment)
			}
			if got := strings.Join(tester.Networks, ","); got != tc.networks {
				t.Fatalf("networks = %q, want %q", got, tc.networks)
			}
		})
	}
}

func TestNormalizePackageJSONFromNothing(t *testing.T) {
	for _, old := range []string{"", "{not json"} {
		b, err := normalizePackageJSON([]byte(old))
		if err != nil {
			t.Fatal(err)
		}
		var pkg map[string]any
		if err := json.Unmarshal(b, &pkg); err != nil {
			t.Fatalf("%q -> invalid JSON %s", old, b)
		}
		dev, _ := pkg["devDependencies"].(map[string]any)
		if pkg["name"] != "e2e" || pkg["private"] != true || dev["@playwright/test"] != PlaywrightVersion {
			t.Fatalf("%q -> %s", old, b)
		}
	}
}
