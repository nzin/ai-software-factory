// Command agent-planner runs the planner A2A agent.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/planner"
)

func main() {
	addr := flag.String("addr", ":9101", "local listen address")
	publicURL := flag.String("public-url", "http://127.0.0.1:9101", "URL other services use to reach this agent")
	catalogURL := flag.String("catalog-url", "http://127.0.0.1:8080", "catalog service base URL")
	concurrency := flag.Int("concurrency", 4, "max concurrent tasks advertised to the coordinator")
	promptsDir := flag.String("prompts-dir", envOr("ASF_PROMPTS_DIR", "agent_prompts"), "directory holding <role>.md prompt files")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	boot, err := agentkit.Bootstrap(ctx, agentkit.Options{
		Role:        planner.Role,
		Name:        planner.DefaultName,
		Description: planner.DefaultDescription,
		Skills:      planner.Skills,
		Concurrency: *concurrency,
		Addr:        *addr,
		PublicURL:   *publicURL,
		CatalogURL:  *catalogURL,
		PromptsDir:  *promptsDir,
	})
	if err != nil {
		log.Fatalf("agent-planner: bootstrap: %v", err)
	}
	log.Printf("agent-planner: registered with catalog, model=%s", boot.Model.Model)

	if err := agentkit.Serve(ctx, *addr, boot.Card, boot.LLMExecutor()); err != nil {
		log.Fatalf("agent-planner: serve: %v", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
