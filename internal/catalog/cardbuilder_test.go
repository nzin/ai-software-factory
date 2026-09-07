package catalog

import (
	"testing"

	"github.com/nzin/ai-software-factory/internal/modelext"
)

func TestBuildCard(t *testing.T) {
	a := Agent{
		Role:        "planner",
		Name:        "Planner",
		Description: "plans things",
		BaseURL:     "http://127.0.0.1:9101/",
		Transport:   "JSONRPC",
		Skills:      []string{"planning", "architecture"},
	}
	card := BuildCard(a, modelext.Config{Model: "claude-sonnet-5"})

	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("interfaces: %+v", card.SupportedInterfaces)
	}
	if got := card.SupportedInterfaces[0].URL; got != "http://127.0.0.1:9101/invoke" {
		t.Fatalf("invoke URL = %q", got)
	}

	cfg, ok := modelext.FromCard(card)
	if !ok {
		t.Fatal("card has no model extension")
	}
	if cfg.Model != "claude-sonnet-5" {
		t.Fatalf("model on card = %q", cfg.Model)
	}
	if len(card.Skills) != 1 || len(card.Skills[0].Tags) != 2 {
		t.Fatalf("skills: %+v", card.Skills)
	}
}
