package buildgate

import (
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
)

const gatewayOK = `  gateway:
    image: traefik:v3.1
    ports:
      - "${GATEWAY_PORT:-8080}:80"
    healthcheck:
      test: ["CMD", "traefik", "healthcheck", "--ping"]
`

func lint(t *testing.T, dir string) []factory.Finding {
	t.Helper()
	c := &checker{dir: dir}
	c.lintDeploy(dir)
	return c.findings
}

// The failure from the field: a tester Dockerfile that COPYs ../e2e from its
// own ./test/component build context. Docker reports it as "/e2e: not found"
// only after pulling the base image; the lint anchors it at the line.
func TestLintFlagsCopyOutsideTheBuildContext(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  app:\n    build: .\n"+gatewayOK)
	write(t, dir, "Dockerfile", "FROM golang:1.26 AS build\nCOPY . .\nFROM alpine:3\nCOPY --from=build /out/app /app\n")
	write(t, dir, "docker-compose.test.yml", "services:\n  tester:\n    build: ./test/component\n")
	write(t, dir, "test/component/Dockerfile", strings.Join([]string{
		"FROM mcr.microsoft.com/playwright:v1.45.0-jammy", // 1
		"WORKDIR /src",                               // 2
		"COPY go.mod main.go ./",                     // 3
		"COPY --chown=pwuser ../e2e/package.json \\", // 4
		"     /e2e/",                                 // 5
		`COPY ["../e2e", "/e2e"]`,                    // 6
	}, "\n")+"\n")

	fs := lint(t, dir)
	if len(fs) != 2 {
		t.Fatalf("want 2 findings (lines 4 and 6), got %+v", fs)
	}
	for i, wantLine := range []int{4, 6} {
		f := fs[i]
		if f.File != "test/component/Dockerfile" || f.Line != wantLine || f.Category != "deploy" || f.Severity != "high" {
			t.Fatalf("finding %d = %+v", i, f)
		}
		if !strings.Contains(f.Title, "../e2e") || !strings.Contains(f.Title, "test/component") {
			t.Fatalf("title should name the path and the build context: %q", f.Title)
		}
	}
}

func TestLintFlagsAMissingDockerfile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  app:\n    build:\n      context: ./backend\n      dockerfile: Dockerfile.prod\n")

	fs := lint(t, dir)
	if len(fs) != 1 || fs[0].File != "docker-compose.yml" || fs[0].Line != 2 || !strings.Contains(fs[0].Title, "backend/Dockerfile.prod") {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestLintRoutesAFrontendDockerfileToFrontend(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  frontend:\n    build: ./web\n")
	write(t, dir, "web/package.json", `{"scripts":{"build":"vite build"}}`)
	write(t, dir, "web/Dockerfile", "FROM node:22\nCOPY ../shared ./shared\n")

	fs := lint(t, dir)
	if len(fs) != 1 || fs[0].TargetRole != factory.RoleFrontendDev {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestLintGatewayContract(t *testing.T) {
	cases := []struct {
		name, gateway, dockerfile, want string
	}{
		{"ok", gatewayOK, "", ""},
		{"missing", "", "", "no `gateway` service"},
		{"no healthcheck", "  gateway:\n    image: traefik:v3.1\n    ports: [\"${GATEWAY_PORT:-8080}:80\"]\n", "", "no healthcheck"},
		{"healthcheck in its image", "  gateway:\n    build: ./gateway\n    ports: [\"${GATEWAY_PORT:-8080}:80\"]\n",
			"FROM traefik:v3.1\nHEALTHCHECK CMD traefik healthcheck --ping\n", ""},
		{"fixed port", "  gateway:\n    image: traefik:v3.1\n    ports: [\"8080:80\"]\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n", "", "fixed host port (8080:80)"},
		{"fixed published port", "  gateway:\n    image: traefik:v3.1\n    ports:\n      - target: 80\n        published: 8080\n    healthcheck:\n      test: [\"CMD\", \"true\"]\n", "", "fixed host port (8080)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "docker-compose.yml", "services:\n  app:\n    image: nginx:alpine\n"+tc.gateway)
			write(t, dir, "docker-compose.test.yml", "services:\n  tester:\n    image: alpine:3\n")
			if tc.dockerfile != "" {
				write(t, dir, "gateway/Dockerfile", tc.dockerfile)
			}
			fs := lint(t, dir)
			if tc.want == "" {
				if len(fs) != 0 {
					t.Fatalf("want no findings, got %+v", fs)
				}
				return
			}
			if len(fs) != 1 || !strings.Contains(fs[0].Title, tc.want) || fs[0].File != "docker-compose.yml" {
				t.Fatalf("want one finding containing %q, got %+v", tc.want, fs)
			}
		})
	}
}

func TestLintSkipsTheGatewayWithoutTheTestOverlay(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  app:\n    image: nginx:alpine\n")
	if fs := lint(t, dir); len(fs) != 0 {
		t.Fatalf("no test overlay, so no gateway requirement; got %+v", fs)
	}
}

func TestCopySourcesSkipsWhatIsNotTheBuildContext(t *testing.T) {
	got := copySources(strings.Join([]string{
		"COPY --from=build /out /x",
		"ADD https://example.com/a.tgz /a",
		"COPY $SRC /x",
		"COPY <<EOF /x",
		"hi",
		"EOF",
		"# COPY ../commented /x",
		"copy a b ./",
	}, "\n"))
	var paths []string
	for _, s := range got {
		paths = append(paths, s.path)
	}
	if strings.Join(paths, ",") != "a,b" {
		t.Fatalf("sources = %v, want [a b]", paths)
	}
	if escapes("/abs/in/context") || !escapes("./x/../../y") {
		t.Fatal("escapes: a leading / is the context root; a climbing path is not")
	}
}
