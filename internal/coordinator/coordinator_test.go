package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/prd"
)

// --- helpers ---

func waitStatus(t *testing.T, o *Orchestrator, id string, want Status) *Run {
	t.Helper()
	for i := 0; i < 400; i++ {
		if r, ok := o.Get(id); ok && r.Status == want {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, _ := o.Get(id)
	t.Fatalf("run %s never reached %s (last: %s / %q)", id, want, r.Status, r.Reason)
	return nil
}

func waitTerminal(t *testing.T, o *Orchestrator, id string) *Run {
	t.Helper()
	for i := 0; i < 400; i++ {
		if r, ok := o.Get(id); ok && r.Status.Terminal() {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, _ := o.Get(id)
	t.Fatalf("run %s never reached a terminal status (last: %s / %q)", id, r.Status, r.Reason)
	return nil
}

func backendPlan() *factory.PlanDoc {
	return &factory.PlanDoc{
		Prose: "attempt",
		Tasks: []factory.PlanTask{{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"}},
	}
}

// --- fakes ---

// planThenApprove: planner emits a trivial plan (no approval gate); every other
// stage succeeds with an approving verdict, so the run walks straight to done.
type planThenApprove struct{}

func (planThenApprove) Run(_ context.Context, run *Run) (StageResult, error) {
	if run.Stage == "planner" {
		return StageResult{
			Task: Task{Role: "planner", State: "completed", Output: "# Plan"},
			Plan: backendPlan(),
		}, nil
	}
	return StageResult{
		Task:    Task{Role: run.Stage, State: "completed"},
		Verdict: factory.VerdictApprove,
	}, nil
}

func TestSubmitHappyPath(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusDone {
		t.Fatalf("status = %s, reason = %s", got.Status, got.Reason)
	}
	if got.Plan == "" {
		t.Fatal("no plan captured")
	}
}

// gatedThenApprove: planner demands human approval; after Approve the run walks
// to done.
type gatedThenApprove struct{}

func (gatedThenApprove) Run(_ context.Context, run *Run) (StageResult, error) {
	if run.Stage == "planner" {
		p := backendPlan()
		p.Approval = factory.ApprovalDecision{Required: true, Reason: "touches application code"}
		return StageResult{Task: Task{Role: "planner", State: "completed", Output: "# Plan"}, Plan: p}, nil
	}
	return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
}

func TestApprovalGate(t *testing.T) {
	o := New(nil, WithEngine(gatedThenApprove{}), WithWorkspace(nil))
	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paused := waitStatus(t, o, run.ID, StatusAwaitingApproval)
	if paused.Approval == nil || !paused.Approval.Required {
		t.Fatalf("approval = %+v, want required", paused.Approval)
	}
	if _, err := o.Approve(run.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusDone {
		t.Fatalf("after approve: status = %s / %q", got.Status, got.Reason)
	}
}

func TestRejectAbandons(t *testing.T) {
	o := New(nil, WithEngine(gatedThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitStatus(t, o, run.ID, StatusAwaitingApproval)
	got, err := o.Reject(run.ID, "", true)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
}

// alwaysRequestChanges: reviewers never approve and always route back to the
// backend developer. Bounded only by the per-stage attempt cap / budget.
type alwaysRequestChanges struct{ calls int }

func (a *alwaysRequestChanges) Run(_ context.Context, run *Run) (StageResult, error) {
	a.calls++
	if run.Stage == "planner" {
		return StageResult{Task: Task{Role: "planner", State: "completed", Output: "attempt"}, Plan: backendPlan()}, nil
	}
	if isReviewer(run.Stage) {
		return StageResult{
			Task:       Task{Role: run.Stage, State: "completed"},
			Findings:   []Finding{{Source: "code-reviewer", Severity: "high", TargetRole: factory.RoleBackendDeveloper, Title: "nope"}},
			Verdict:    factory.VerdictRequestChanges,
			TargetRole: factory.RoleBackendDeveloper,
		}, nil
	}
	return StageResult{Task: Task{Role: run.Stage, State: "completed"}}, nil
}

func TestRequestChangesStopsAtAttemptCap(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil),
		WithMaxStageIterations(3), WithDefaults(100, time.Hour))

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review (%q)", got.Status, got.Reason)
	}
	if got.Attempts[factory.RoleBackendDeveloper] <= 3 {
		t.Fatalf("backend attempts = %d, want > 3", got.Attempts[factory.RoleBackendDeveloper])
	}
}

func TestBudgetStopsInfiniteLoop(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil),
		WithMaxStageIterations(100), WithDefaults(5, time.Hour))

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review", got.Status)
	}
	if got.Reason != "iteration budget exhausted" {
		t.Fatalf("reason = %q", got.Reason)
	}
	if eng.calls != 5 {
		t.Fatalf("engine called %d times, want 5", eng.calls)
	}
}

func TestZeroBudgetDispatchesNothing(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil), WithDefaults(0, 0))

	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review", got.Status)
	}
	if eng.calls != 0 {
		t.Fatalf("engine called %d times with zero budget, want 0", eng.calls)
	}
}

// --- stage sequencing ---

func TestPlannedStagesSkipsUnusedRoles(t *testing.T) {
	got := plannedStages([]factory.PlanTask{{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"}})
	want := []string{factory.RoleBackendDeveloper, factory.RoleSecurityReviewer, factory.RoleCodeReviewer}
	if !equal(got, want) {
		t.Fatalf("backend-only stages = %v, want %v", got, want)
	}

	got = plannedStages([]factory.PlanTask{
		{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"},
		{ID: "T2", Role: factory.RoleFrontendDev, Title: "spa"},
	})
	want = []string{
		factory.RoleUIUXDesigner, factory.RoleBackendDeveloper, factory.RoleFrontendDev,
		factory.RoleSecurityReviewer, factory.RoleCodeReviewer,
	}
	if !equal(got, want) {
		t.Fatalf("full stages = %v, want %v", got, want)
	}

	got = plannedStages(nil)
	if !equal(got, want) {
		t.Fatalf("fallback stages = %v, want %v", got, want)
	}
}

func TestNextStageWalksStages(t *testing.T) {
	run := &Run{
		PlanTasks: []factory.PlanTask{{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"}},
		Attempts:  map[string]int{},
	}
	run.Stage = factory.RoleBackendDeveloper
	if got := nextStage(run); got != factory.RoleSecurityReviewer {
		t.Fatalf("after backend: %q", got)
	}
	run.Stage = factory.RoleSecurityReviewer
	if got := nextStage(run); got != factory.RoleCodeReviewer {
		t.Fatalf("after security: %q", got)
	}
	run.Stage = factory.RoleCodeReviewer
	if got := nextStage(run); got != "" {
		t.Fatalf("after code review: %q, want end", got)
	}
}

func TestNextStageFixPassSkipsToSecurityReviewer(t *testing.T) {
	run := &Run{
		PlanTasks: []factory.PlanTask{
			{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"},
			{ID: "T2", Role: factory.RoleFrontendDev, Title: "spa"},
		},
		Attempts: map[string]int{factory.RoleBackendDeveloper: 1},
		Stage:    factory.RoleBackendDeveloper,
	}
	if got := nextStage(run); got != factory.RoleSecurityReviewer {
		t.Fatalf("fix pass should skip straight to security reviewer, got %q", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
