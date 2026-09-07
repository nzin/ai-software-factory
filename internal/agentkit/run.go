package agentkit

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// AgentSpec is the static identity of an agent binary.
type AgentSpec struct {
	Role               string
	DefaultName        string
	DefaultDescription string
	DefaultSkills      []string
	DefaultPort        int
	// Executor builds the agent's executor from the bootstrap result.
	Executor func(*Bootstrapped) a2asrv.AgentExecutor
}

// Run is the standard main() body for an agent binary: parse the common flags,
// Bootstrap, then Serve until SIGINT/SIGTERM.
func Run(spec AgentSpec) {
	log.SetFlags(0)

	addr := flag.String("addr", fmt.Sprintf(":%d", spec.DefaultPort), "local listen address")
	publicURL := flag.String("public-url", fmt.Sprintf("http://127.0.0.1:%d", spec.DefaultPort), "URL other services use to reach this agent")
	catalogURL := flag.String("catalog-url", envOr("ASF_CATALOG_URL", "http://127.0.0.1:8080"), "catalog service base URL")
	concurrency := flag.Int("concurrency", 4, "max concurrent tasks advertised to the coordinator")
	promptsDir := flag.String("prompts-dir", envOr("ASF_PROMPTS_DIR", "agent_prompts"), "directory holding <role>.md prompt files")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	boot, err := Bootstrap(ctx, Options{
		Role:        spec.Role,
		Name:        spec.DefaultName,
		Description: spec.DefaultDescription,
		Skills:      spec.DefaultSkills,
		Concurrency: *concurrency,
		Addr:        *addr,
		PublicURL:   *publicURL,
		CatalogURL:  *catalogURL,
		PromptsDir:  *promptsDir,
	})
	if err != nil {
		log.Fatalf("agent-%s: bootstrap: %v", spec.Role, err)
	}
	log.Printf("agent-%s: registered with catalog, model=%s", spec.Role, boot.Model.Model)

	if err := Serve(ctx, *addr, boot.Card, spec.Executor(boot)); err != nil {
		log.Fatalf("agent-%s: serve: %v", spec.Role, err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
