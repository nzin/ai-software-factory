package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/nzin/ai-software-factory/internal/factory"
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

func TestFindingsForRoleFiltersByTarget(t *testing.T) {
	// build-gate's own outputContract asks for shorthand targetRole values
	// ("backend"/"frontend"/"mobile"), while the dispatch role passed in here
	// is always the full stage name ("frontend-developer") — findingsForRole
	// must normalize both sides, or a backend-targeted finding falls through
	// to "not a recognized developer role, so untargeted" and gets handed to
	// the frontend dispatch too.
	all := []Finding{
		{Title: "backend thing", TargetRole: "backend"},
		{Title: "frontend thing", TargetRole: "frontend"},
		{Title: "untargeted", TargetRole: ""},
		{Title: "reviewer-targeted", TargetRole: "code-reviewer"}, // not a developer role
	}

	got := findingsForRole(all, factory.RoleFrontendDev)

	var titles []string
	for _, f := range got {
		titles = append(titles, f.Title)
	}
	want := []string{"frontend thing", "untargeted", "reviewer-targeted"}
	if len(titles) != len(want) {
		t.Fatalf("findingsForRole(frontend-developer) = %v, want %v", titles, want)
	}
	for i, w := range want {
		if titles[i] != w {
			t.Fatalf("findingsForRole(frontend-developer)[%d] = %q, want %q", i, titles[i], w)
		}
	}
}

func TestFindingsForRoleScopedToLastRoundNotWholeHistory(t *testing.T) {
	// A finding from an earlier round (accumulated in run.Findings) must not
	// resurface in a later fix-pass dispatch once a newer round's findings have
	// superseded it in run.LastRoundFindings — a retry should see what just
	// failed, not the run's entire finding history.
	run := &Run{
		Findings: []Finding{
			{Title: "round 1 finding", TargetRole: "frontend"},
			{Title: "round 2 finding", TargetRole: "frontend"},
		},
		LastRoundFindings: []Finding{
			{Title: "round 2 finding", TargetRole: "frontend"},
		},
	}

	got := findingsForRole(run.LastRoundFindings, factory.RoleFrontendDev)
	if len(got) != 1 || got[0].Title != "round 2 finding" {
		t.Fatalf("findingsForRole(run.LastRoundFindings) = %v, want only the latest round's finding", got)
	}
}
