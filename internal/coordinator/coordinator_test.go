package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/nzin/ai-software-factory/internal/prd"
)

// planThenDone is a fake engine: the planner stage succeeds once, then the run
// is complete.
type planThenDone struct{}

func (planThenDone) Run(context.Context, *Run) (StageResult, error) {
	return StageResult{Task: Task{Role: "planner", State: "completed", Output: "# Plan\n\ndo the thing"}}, nil
}
func (planThenDone) Next(*Run) string { return "" }

func TestSubmitHappyPath(t *testing.T) {
	o := New(nil, WithEngine(planThenDone{}))
	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusDone {
		t.Fatalf("status = %s, reason = %s", run.Status, run.Reason)
	}
	if run.Plan == "" {
		t.Fatal("no plan captured")
	}
}

// alwaysRequestChanges never lets a run finish: it always adds a finding and
// keeps the run on the same stage. Without a TTL this would loop forever.
type alwaysRequestChanges struct{ calls int }

func (a *alwaysRequestChanges) Run(context.Context, *Run) (StageResult, error) {
	a.calls++
	return StageResult{
		Task:     Task{Role: "planner", State: "completed", Output: "attempt"},
		Findings: []Finding{{Source: "code-reviewer", TargetRole: "planner", Note: "nope"}},
	}, nil
}
func (a *alwaysRequestChanges) Next(*Run) string { return "planner" }

func TestBudgetStopsInfiniteLoop(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithDefaults(5, time.Hour))

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review", run.Status)
	}
	if run.Reason != "iteration budget exhausted" {
		t.Fatalf("reason = %q", run.Reason)
	}
	if eng.calls != 5 {
		t.Fatalf("engine called %d times, want 5", eng.calls)
	}
	if len(run.Findings) != 5 {
		t.Fatalf("findings = %d, want 5", len(run.Findings))
	}
}

func TestZeroBudgetDispatchesNothing(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithDefaults(0, 0))

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review", run.Status)
	}
	if eng.calls != 0 {
		t.Fatalf("engine called %d times with zero budget, want 0", eng.calls)
	}
}
