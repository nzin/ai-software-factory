package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/prd"
)

func TestPlannerInputCarriesRevisionContext(t *testing.T) {
	e := &pipelineEngine{}
	run := &Run{
		PRD:          prd.PRD{Title: "Quotes API", Description: "a service that returns quotes"},
		Plan:         "1. Context — a quotes service\n2. Approach — one Go binary",
		PlanFeedback: "split the storage into its own task and add a healthcheck",
	}

	got := e.plannerInput(context.Background(), run)

	for _, want := range []string{
		"# Revise the previous plan",
		"one Go binary",
		"add a healthcheck",
		"a service that returns quotes", // the PRD is still there
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("plannerInput missing %q\n---\n%s", want, got)
		}
	}
}

func TestPlannerInputPlainWhenNoFeedback(t *testing.T) {
	e := &pipelineEngine{}
	run := &Run{PRD: prd.PRD{Title: "X", Description: "y"}, Plan: "old plan"}

	got := e.plannerInput(context.Background(), run)
	if strings.Contains(got, "Revise the previous plan") || strings.Contains(got, "old plan") {
		t.Fatalf("unexpected revision context with no feedback:\n%s", got)
	}
}
