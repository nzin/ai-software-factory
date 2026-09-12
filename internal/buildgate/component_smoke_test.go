package buildgate

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/testharness"
)

// TestComponentSmoke runs the whole deploy + component path against a real
// Docker daemon: a one-service Go stack that is its own `gateway`, the
// factory-owned harness, one API check and one Playwright spec. It proves the
// harness's Playwright pin matches its image's browsers, and that the gate
// builds, runs and reads the tester log. It pulls the ~2 GB Playwright image,
// so it only runs with ASF_DOCKER_TESTS=1.
func TestComponentSmoke(t *testing.T) {
	if os.Getenv("ASF_DOCKER_TESTS") != "1" {
		t.Skip("set ASF_DOCKER_TESTS=1 to run the Docker component smoke test")
	}
	requireGo(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}

	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/smoke\n\ngo 1.26\n")
	write(t, dir, "main.go", smokeServer)
	write(t, dir, ".dockerignore", "test/\n")
	write(t, dir, "Dockerfile", strings.Join([]string{
		"FROM golang:1.26 AS build",
		"WORKDIR /src",
		"COPY go.mod main.go ./",
		"RUN CGO_ENABLED=0 go build -o /out/app .",
		"FROM alpine:3",
		"COPY --from=build /out/app /app",
		"HEALTHCHECK --interval=2s --retries=15 CMD wget -qO- http://127.0.0.1/healthz || exit 1",
		`ENTRYPOINT ["/app"]`,
	}, "\n")+"\n")
	// The obsolete top-level `version:` key (still common in generated
	// compose files) makes every `docker compose` invocation print a
	// deprecation warning ahead of its real output — this fixture keeps that
	// warning in the loop so a regression in parsing `ps -q`'s output (see
	// containerID) fails here instead of only in the field.
	write(t, dir, "docker-compose.yml", "version: \"3.8\"\n\nservices:\n  gateway:\n    build: .\n    ports:\n      - \"${GATEWAY_PORT:-8080}:80\"\n")
	write(t, dir, "test/component/main.go", smokeAPI)
	write(t, dir, "test/e2e/home.spec.ts", smokeSpec)
	if _, _, err := testharness.Materialize(dir); err != nil {
		t.Fatal(err)
	}

	res := Check(context.Background(), "smoke", dir, nil)
	if len(res.Findings) != 0 {
		t.Fatalf("summary %q\nfindings: %+v\nfailures: %+v", res.Summary, res.Findings, res.Failures)
	}
	for _, want := range []string{"component", "api 1/1 passed", "e2e 1/1 passed"} {
		if !strings.Contains(res.Summary, want) {
			t.Fatalf("summary %q lacks %q", res.Summary, want)
		}
	}
	if len(res.Screenshots) == 0 {
		t.Fatal("no screenshot pulled out of the tester")
	}
}

const smokeServer = `package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!doctype html><title>smoke</title><h1>hello from smoke</h1>")
	})
	_ = http.ListenAndServe(":80", nil)
}
`

const smokeAPI = `package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	base := os.Getenv("APP_URL")
	if base == "" {
		base = "http://gateway"
	}
	resp, err := http.Get(base + "/")
	if err != nil {
		fmt.Printf("FAIL [api] home page: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "hello from smoke") {
		fmt.Printf("FAIL [api] home page: status %d\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("PASS [api] home page")
}
`

const smokeSpec = `import { test, expect } from '@playwright/test';

test('home page greets', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('h1')).toHaveText('hello from smoke');
  await page.screenshot({ path: '/output/screenshots/home.png' });
});
`
