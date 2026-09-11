package coordinator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/prd"
	"github.com/nzin/ai-software-factory/internal/workspace"
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

func TestRejectWithFeedbackRevisesPlan(t *testing.T) {
	o := New(nil, WithEngine(gatedThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X", Description: "orig"}, SubmitOptions{})
	paused := waitStatus(t, o, run.ID, StatusAwaitingApproval)
	if paused.Plan == "" {
		t.Fatal("no plan to revise")
	}

	got, err := o.Reject(run.ID, "add rate limiting to the API", false)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got.PlanFeedback != "add rate limiting to the API" {
		t.Fatalf("PlanFeedback = %q", got.PlanFeedback)
	}
	if got.Plan == "" {
		t.Fatal("previous plan was wiped — the planner has no revision context")
	}
	if got.PlanTasks != nil {
		t.Fatalf("PlanTasks not cleared: %+v", got.PlanTasks)
	}
	if got.Stage != "planner" || got.Status != StatusRunning {
		t.Fatalf("stage=%q status=%q, want planner/running", got.Stage, got.Status)
	}
	if got.PRD.Description != "orig" {
		t.Fatalf("PRD description was polluted with feedback: %q", got.PRD.Description)
	}
	// The planner reruns and re-pauses; PlanFeedback is consumed by applyPlan.
	revised := waitStatus(t, o, run.ID, StatusAwaitingApproval)
	if revised.PlanFeedback != "" {
		t.Fatalf("PlanFeedback not consumed after replan: %q", revised.PlanFeedback)
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

// buildKeepsFailing: the build gate never passes and routes to backend; every
// other non-planner stage approves. Exercises the gate → developer → gate loop.
type buildKeepsFailing struct{ gateRuns, devRuns int }

func (b *buildKeepsFailing) Run(_ context.Context, run *Run) (StageResult, error) {
	switch {
	case run.Stage == "planner":
		return StageResult{Task: Task{Role: "planner", State: "completed"}, Plan: backendPlan()}, nil
	case isGate(run.Stage):
		b.gateRuns++
		return StageResult{
			Task: Task{Role: run.Stage, State: "completed", Verdict: factory.VerdictRequestChanges},
			Findings: []Finding{{
				Source: "build-gate", Severity: "high", File: "main.go", Line: 3,
				Title: "undefined: doThing", TargetRole: factory.RoleBackendDeveloper,
			}},
			Verdict:    factory.VerdictRequestChanges,
			TargetRole: factory.RoleBackendDeveloper,
		}, nil
	case factory.IsDeveloperRole(run.Stage):
		b.devRuns++
		return StageResult{Task: Task{Role: run.Stage, State: "completed"}}, nil
	default:
		return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
	}
}

func TestBuildGateBouncesToDeveloperThenCaps(t *testing.T) {
	eng := &buildKeepsFailing{}
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
	// Each failed gate bounces to backend, whose fix pass re-runs the gate.
	if eng.gateRuns < 4 || eng.devRuns < 4 {
		t.Fatalf("gate/dev loop did not run: gateRuns=%d devRuns=%d", eng.gateRuns, eng.devRuns)
	}
	if got.Attempts[factory.RoleBackendDeveloper] <= 3 {
		t.Fatalf("backend attempts = %d, want > 3", got.Attempts[factory.RoleBackendDeveloper])
	}
	if !hasKind(got, EventBuildFailed) {
		t.Fatalf("no build_failed event: %v", kinds(got))
	}
	if hasKind(got, EventRequestChanges) {
		t.Fatalf("a build-gate bounce should be build_failed, not request_changes: %v", kinds(got))
	}
}

func TestBuildGatePassFlowsToReviewers(t *testing.T) {
	// planThenApprove approves at every non-planner stage, gate included.
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	got := waitTerminal(t, o, run.ID)
	if got.Status != StatusDone {
		t.Fatalf("status = %s / %q", got.Status, got.Reason)
	}
	roles := make([]string, 0, len(got.Tasks))
	for _, tk := range got.Tasks {
		roles = append(roles, tk.Role)
	}
	// test-engineer then build-gate must sit between the developer and the reviewers.
	di := indexOf(roles, factory.RoleBackendDeveloper)
	ti := indexOf(roles, factory.RoleTestEngineer)
	gi := indexOf(roles, factory.RoleBuildGate)
	si := indexOf(roles, factory.RoleSecurityReviewer)
	if !(di >= 0 && ti > di && gi > ti && si > gi) {
		t.Fatalf("stage order wrong: %v", roles)
	}
}

// testEngineerThenGate: test-engineer commits; build-gate then fails a component
// test once and routes to the backend developer; after the fix the gate passes.
type testEngineerThenGate struct{ teRuns, gateRuns, devFix int }

func (e *testEngineerThenGate) Run(_ context.Context, run *Run) (StageResult, error) {
	switch {
	case run.Stage == "planner":
		return StageResult{Task: Task{Role: "planner", State: "completed"}, Plan: backendPlan()}, nil
	case run.Stage == factory.RoleTestEngineer:
		e.teRuns++
		return StageResult{Task: Task{Role: run.Stage, State: "completed", CommitSHA: "abc123", FilesWritten: []string{"test/component/main.go", "docker-compose.yml"}}}, nil
	case isGate(run.Stage):
		e.gateRuns++
		if e.gateRuns == 1 {
			return StageResult{
				Task:       Task{Role: run.Stage, State: "completed", Verdict: factory.VerdictRequestChanges},
				Findings:   []Finding{{Source: "build-gate", Severity: "high", Category: "component-test", Title: "POST /x returns 500 for empty body, expected 400", TargetRole: factory.RoleBackendDeveloper}},
				Verdict:    factory.VerdictRequestChanges,
				TargetRole: factory.RoleBackendDeveloper,
			}, nil
		}
		return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
	case factory.IsDeveloperRole(run.Stage):
		if run.Attempts[run.Stage] > 0 {
			e.devFix++
		}
		return StageResult{Task: Task{Role: run.Stage, State: "completed"}}, nil
	default:
		return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
	}
}

func TestComponentTestFailureRoutesToDeveloperThenReVerifies(t *testing.T) {
	eng := &testEngineerThenGate{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	got := waitTerminal(t, o, run.ID)

	if got.Status != StatusDone {
		t.Fatalf("status = %s / %q", got.Status, got.Reason)
	}
	if eng.teRuns != 1 {
		t.Fatalf("test-engineer ran %d times, want 1", eng.teRuns)
	}
	if eng.gateRuns != 2 {
		t.Fatalf("build gate ran %d times, want 2 (fail, then re-verify)", eng.gateRuns)
	}
	if eng.devFix != 1 {
		t.Fatalf("developer fix passes = %d, want 1", eng.devFix)
	}
	if got.Attempts[factory.RoleBackendDeveloper] != 1 {
		t.Fatalf("attempts = %v", got.Attempts)
	}
	if !hasKind(got, EventBuildFailed) {
		t.Fatalf("component-test failure should emit a build_failed event: %v", kinds(got))
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
	want := []string{
		factory.RoleBackendDeveloper, factory.RoleTestEngineer, factory.RoleBuildGate,
		factory.RoleSecurityReviewer, factory.RoleCodeReviewer,
	}
	if !equal(got, want) {
		t.Fatalf("backend-only stages = %v, want %v", got, want)
	}

	got = plannedStages([]factory.PlanTask{
		{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"},
		{ID: "T2", Role: factory.RoleFrontendDev, Title: "spa"},
	})
	want = []string{
		factory.RoleUIUXDesigner, factory.RoleBackendDeveloper, factory.RoleFrontendDev,
		factory.RoleTestEngineer, factory.RoleBuildGate,
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
	if got := nextStage(run); got != factory.RoleTestEngineer {
		t.Fatalf("after backend: %q, want test-engineer", got)
	}
	run.Stage = factory.RoleTestEngineer
	if got := nextStage(run); got != factory.RoleBuildGate {
		t.Fatalf("after test-engineer: %q, want build-gate", got)
	}
	run.Stage = factory.RoleBuildGate
	if got := nextStage(run); got != factory.RoleSecurityReviewer {
		t.Fatalf("after build-gate: %q", got)
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

func TestNextStageFixPassGoesToBuildGate(t *testing.T) {
	run := &Run{
		PlanTasks: []factory.PlanTask{
			{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"},
			{ID: "T2", Role: factory.RoleFrontendDev, Title: "spa"},
		},
		Attempts: map[string]int{factory.RoleBackendDeveloper: 1},
		Stage:    factory.RoleBackendDeveloper,
	}
	if got := nextStage(run); got != factory.RoleBuildGate {
		t.Fatalf("fix pass should re-run the build gate first, got %q", got)
	}
}

// --- event log ---

func kinds(r *Run) []string {
	out := make([]string, 0, len(r.Events))
	for _, e := range r.Events {
		out = append(out, e.Kind)
	}
	return out
}

func hasKind(r *Run, kind string) bool {
	for _, e := range r.Events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func TestEventLogRecordsTheWholeRun(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	run, err := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)

	// The whole life of the run must be reconstructable from the log alone.
	for _, want := range []string{
		EventSubmitted, EventStageStarted, EventStageCompleted, EventPlanReady, EventFinished,
	} {
		if !hasKind(got, want) {
			t.Fatalf("missing %q event; got %v", want, kinds(got))
		}
	}
	if got.Events[0].Kind != EventSubmitted {
		t.Fatalf("first event = %q, want submitted", got.Events[0].Kind)
	}
	if last := got.Events[len(got.Events)-1]; last.Kind != EventFinished {
		t.Fatalf("last event = %q, want finished", last.Kind)
	}
	// Seq is monotonic so the UI can order without relying on timestamps.
	for i, e := range got.Events {
		if e.Seq != i+1 {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		if e.At.IsZero() || e.Message == "" {
			t.Fatalf("event %d is incomplete: %+v", i, e)
		}
	}
	// A completed stage carries the timing the run-detail view shows.
	for _, e := range got.Events {
		if e.Kind == EventStageCompleted && e.DurationMs < 0 {
			t.Fatalf("stage_completed has a negative duration: %+v", e)
		}
	}
}

func TestEventLogMarksHumanActions(t *testing.T) {
	o := New(nil, WithEngine(gatedThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitStatus(t, o, run.ID, StatusAwaitingApproval)
	if _, err := o.Approve(run.ID); err != nil {
		t.Fatal(err)
	}
	got := waitTerminal(t, o, run.ID)

	if !hasKind(got, EventAwaitingApproval) {
		t.Fatalf("no awaiting_approval event: %v", kinds(got))
	}
	var approved *Event
	for i := range got.Events {
		if got.Events[i].Kind == EventApproved {
			approved = &got.Events[i]
		}
	}
	if approved == nil {
		t.Fatalf("no approved event: %v", kinds(got))
	}
	if approved.Actor != ActorHuman {
		t.Fatalf("approved event actor = %q, want human", approved.Actor)
	}
}

func TestTaskRecordsPerStepDetail(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	got := waitTerminal(t, o, run.ID)

	if len(got.Tasks) == 0 {
		t.Fatal("no tasks recorded")
	}
	for i, task := range got.Tasks {
		if task.StartedAt.IsZero() {
			t.Fatalf("task %d (%s) has no StartedAt", i, task.Role)
		}
	}
}

// --- resume ---

func TestResumeRestartsAtLastStage(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil),
		WithMaxStageIterations(1), WithDefaults(100, time.Hour))

	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	parked := waitTerminal(t, o, run.ID)
	if parked.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review", parked.Status)
	}
	if parked.LastStage == "" {
		t.Fatal("LastStage was not recorded, so resume has nowhere to restart")
	}
	if parked.Attempts[factory.RoleBackendDeveloper] == 0 {
		t.Fatal("expected a non-zero attempt count before resuming")
	}
	budgetBefore := parked.Budget.IterationsRemaining

	resumed, err := o.Resume(context.Background(), run.ID, ResumeOptions{IterationBudget: 7})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.Budget.IterationsRemaining <= budgetBefore {
		t.Fatalf("budget = %d, want more than %d", resumed.Budget.IterationsRemaining, budgetBefore)
	}
	// Without clearing the counters the attempt cap would trip again immediately.
	if n := resumed.Attempts[factory.RoleBackendDeveloper]; n != 0 {
		t.Fatalf("attempts not reset on resume: %d", n)
	}
	if !hasKind(resumed, EventResumed) {
		t.Fatalf("no resumed event: %v", kinds(resumed))
	}
	waitTerminal(t, o, run.ID) // let the goroutine finish before the test ends
}

func TestResumeAbandons(t *testing.T) {
	eng := &alwaysRequestChanges{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil), WithDefaults(3, time.Hour))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitStatus(t, o, run.ID, StatusNeedsHumanReview)

	got, err := o.Resume(context.Background(), run.ID, ResumeOptions{Abandon: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
}

func TestResumeRejectsWrongStatus(t *testing.T) {
	o := New(nil, WithEngine(planThenApprove{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitTerminal(t, o, run.ID) // ends in `done`, not needs_human_review

	if _, err := o.Resume(context.Background(), run.ID, ResumeOptions{}); err == nil {
		t.Fatal("resume from a done run should fail")
	}
	if _, err := o.Resume(context.Background(), "nope", ResumeOptions{}); err == nil {
		t.Fatal("resume of an unknown run should fail")
	}
}

// failsOnBackend: planner emits a plan, then the backend stage errors — driving
// the run to `failed`.
type failsOnBackend struct{}

func (failsOnBackend) Run(_ context.Context, run *Run) (StageResult, error) {
	if run.Stage == "planner" {
		return StageResult{
			Task: Task{Role: "planner", State: "completed", Output: "# Plan"},
			Plan: backendPlan(),
		}, nil
	}
	if run.Stage == factory.RoleBackendDeveloper {
		return StageResult{}, errors.New("boom: model reply truncated")
	}
	return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
}

func TestResumeFailedRunWithComment(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil), WithDefaults(100, time.Hour))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	failed := waitTerminal(t, o, run.ID)
	if failed.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", failed.Status)
	}

	resumed, err := o.Resume(context.Background(), run.ID,
		ResumeOptions{Comment: "the gosec G404 is a false positive — add // #nosec"})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	var human *Finding
	for i := range resumed.Findings {
		if resumed.Findings[i].Source == "human" {
			human = &resumed.Findings[i]
		}
	}
	if human == nil || human.Title != "the gosec G404 is a false positive — add // #nosec" {
		t.Fatalf("human finding not recorded: %+v", resumed.Findings)
	}
	if human.Severity != "high" {
		t.Fatalf("human finding severity = %q, want high", human.Severity)
	}
	if resumed.Attempts[factory.RoleBackendDeveloper] != 1 {
		t.Fatalf("attempts[backend] = %d, want 1", resumed.Attempts[factory.RoleBackendDeveloper])
	}
	var ev *Event
	for i := range resumed.Events {
		if resumed.Events[i].Kind == EventResumed {
			ev = &resumed.Events[i]
		}
	}
	if ev == nil || ev.Actor != ActorHuman {
		t.Fatalf("resumed event missing or not by human: %+v", ev)
	}
	waitTerminal(t, o, run.ID) // let the goroutine settle (it will fail again)
}

func TestResumeAcceptAsIs(t *testing.T) {
	o := New(nil, WithEngine(&alwaysRequestChanges{}), WithWorkspace(nil),
		WithMaxStageIterations(1), WithDefaults(100, time.Hour))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitStatus(t, o, run.ID, StatusNeedsHumanReview)

	got, err := o.Resume(context.Background(), run.ID, ResumeOptions{Accept: true})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	// With a nil workspace finish() short-circuits to done.
	if !got.Status.Terminal() || got.Status == StatusNeedsHumanReview {
		t.Fatalf("status = %s, want a fresh terminal state", got.Status)
	}
	if !hasKind(got, EventReviewAccepted) {
		t.Fatalf("no review_accepted event: %v", kinds(got))
	}
}

func TestDeleteRun(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil), WithDefaults(100, time.Hour))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	if s := waitTerminal(t, o, run.ID).Status; s != StatusFailed {
		t.Fatalf("status = %s, want failed", s)
	}

	if err := o.Delete(context.Background(), run.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := o.Get(run.ID); ok {
		t.Fatal("run still present after delete")
	}
	all, _ := o.store.All()
	if len(all) != 0 {
		t.Fatalf("store not empty: %+v", all)
	}
	if err := o.Delete(context.Background(), run.ID); err == nil {
		t.Fatal("deleting a missing run should error")
	}
}

func TestDeleteRunAccepted(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil))
	o.store.Put(&Run{ID: "r1", Status: StatusAccepted})
	if err := o.Delete(context.Background(), "r1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := o.Get("r1"); ok {
		t.Fatal("run still present after delete")
	}
}

func TestDeleteRunDone(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil))
	o.store.Put(&Run{ID: "r1", Status: StatusDone})
	if err := o.Delete(context.Background(), "r1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := o.Get("r1"); ok {
		t.Fatal("run still present after delete")
	}
}

func TestDeleteRunRejectsNonTerminal(t *testing.T) {
	o := New(nil, WithEngine(failsOnBackend{}), WithWorkspace(nil))
	o.store.Put(&Run{ID: "r1", Status: StatusRunning})
	if err := o.Delete(context.Background(), "r1"); err == nil {
		t.Fatal("expected an error deleting a running run")
	}
	if _, ok := o.Get("r1"); !ok {
		t.Fatal("run must survive a rejected delete")
	}
}

// --- human review ---

// prReady drives a run to pr_ready so the review endpoints have something to act
// on: the planner emits a backend task and every other stage approves.
type prReady struct{ calls int }

func (p *prReady) Run(_ context.Context, run *Run) (StageResult, error) {
	p.calls++
	if run.Stage == "planner" {
		return StageResult{Task: Task{Role: "planner", State: "completed", Output: "plan"}, Plan: backendPlan()}, nil
	}
	return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
}

// driveToPRReady runs a fake pipeline to completion and forces the pr_ready
// status a real workspace with commits would have produced.
func driveToPRReady(t *testing.T, o *Orchestrator, id string) *Run {
	t.Helper()
	waitTerminal(t, o, id)
	run, _, _ := o.store.Get(id)
	run.Status = StatusPRReady
	run.Reason = "branch ready"
	if err := o.store.Put(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestReviewAcceptIsTerminal(t *testing.T) {
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	driveToPRReady(t, o, run.ID)

	got, err := o.Review(context.Background(), run.ID, ReviewAccept, nil)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got.Status != StatusAccepted {
		t.Fatalf("status = %s, want accepted", got.Status)
	}
	if !got.Status.Terminal() {
		t.Fatal("accepted should be terminal")
	}
	if !hasKind(got, EventReviewAccepted) {
		t.Fatalf("no review_accepted event: %v", kinds(got))
	}
}

// testGit runs git in dir and fails the test on error.
func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

// gitOriginFixture builds a real local repo (one commit on main) to clone from.
func gitOriginFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	testGit(t, dir, "init", "-q", "-b", "main")
	testGit(t, dir, "config", "user.name", "Fixture")
	testGit(t, dir, "config", "user.email", "fixture@test.local")
	testGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", "-A")
	testGit(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// committingEngine approves every stage and writes+commits a file on the
// developer stage, so the run reaches finish() with real commits on its branch.
type committingEngine struct{}

func (committingEngine) Run(ctx context.Context, run *Run) (StageResult, error) {
	if run.Stage == "planner" {
		return StageResult{Task: Task{Role: "planner", State: "completed", Output: "plan"}, Plan: backendPlan()}, nil
	}
	if factory.IsDeveloperRole(run.Stage) {
		repo, err := workspace.Open(ctx, run.WorkspaceDir)
		if err != nil {
			return StageResult{}, err
		}
		if _, err := repo.WriteFiles(map[string]string{"main.go": "package main\n"}); err != nil {
			return StageResult{}, err
		}
		if _, err := repo.Commit(ctx, run.Stage, "implement the thing"); err != nil {
			return StageResult{}, err
		}
	}
	return StageResult{Task: Task{Role: run.Stage, State: "completed"}, Verdict: factory.VerdictApprove}, nil
}

// A run whose earlier push failed (origin was unreachable) sits at pr_ready with
// its branch missing from origin. Accepting it must (re-)push the branch.
func TestReviewAcceptPushesTheBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	src := gitOriginFixture(t)
	o := New(nil, WithEngine(committingEngine{}), WithWorkspace(workspace.NewManager(t.TempDir())),
		WithDefaults(100, time.Hour))

	run, err := o.Submit(ctx, prd.PRD{Title: "X"}, SubmitOptions{RepoURL: "file://" + src})
	if err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, o, run.ID, StatusPRReady)
	branch := got.WorkBranch
	if branch == "" {
		t.Fatal("run has no work branch")
	}

	// finish() already pushed once; drop the branch from origin to model a push
	// that had failed, then confirm accept puts it back.
	testGit(t, src, "branch", "-D", branch)
	if out := testGit(t, src, "branch", "--list", branch); strings.Contains(out, branch) {
		t.Fatalf("branch not removed from origin: %q", out)
	}

	accepted, err := o.Review(ctx, run.ID, ReviewAccept, nil)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.Status != StatusAccepted {
		t.Fatalf("status = %s, want accepted", accepted.Status)
	}
	if !hasKind(accepted, EventPushed) {
		t.Fatalf("no pushed event on accept: %v", kinds(accepted))
	}
	if out := testGit(t, src, "branch", "--list", branch); !strings.Contains(out, branch) {
		t.Fatalf("branch %s not pushed to origin on accept: %q", branch, out)
	}
}

func TestReviewRequestChangesReentersTheFactory(t *testing.T) {
	eng := &prReady{}
	o := New(nil, WithEngine(eng), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	driveToPRReady(t, o, run.ID)
	callsBefore := eng.calls

	got, err := o.Review(context.Background(), run.ID, ReviewRequestChanges, []ReviewComment{
		{Note: "the roll endpoint accepts 0 dice", File: "internal/api/handlers.go", Line: 42},
	})
	if err != nil {
		t.Fatalf("request_changes: %v", err)
	}

	// The comment becomes a routed finding, not a dropped note.
	var human *Finding
	for i := range got.Findings {
		if got.Findings[i].Source == "human" {
			human = &got.Findings[i]
		}
	}
	if human == nil {
		t.Fatalf("no human finding recorded: %+v", got.Findings)
	}
	if human.Title != "the roll endpoint accepts 0 dice" || human.File != "internal/api/handlers.go" {
		t.Fatalf("human finding lost detail: %+v", human)
	}
	if !factory.IsDeveloperRole(got.Stage) && got.Status == StatusRunning {
		t.Fatalf("run re-entered at %q, want a developer role", got.Stage)
	}
	if got.Attempts[factory.RoleBackendDeveloper] != 1 {
		t.Fatalf("attempts = %v, want backend-developer at 1", got.Attempts)
	}
	if !hasKind(got, EventReviewChanges) {
		t.Fatalf("no review_changes_requested event: %v", kinds(got))
	}

	final := waitTerminal(t, o, run.ID)
	if eng.calls <= callsBefore {
		t.Fatal("the engine was never re-dispatched after request_changes")
	}
	if final.Status == StatusFailed {
		t.Fatalf("second pass failed: %s", final.Reason)
	}
}

func TestReviewRequestChangesNeedsAComment(t *testing.T) {
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	driveToPRReady(t, o, run.ID)

	if _, err := o.Review(context.Background(), run.ID, ReviewRequestChanges, nil); err == nil {
		t.Fatal("request_changes with no comments should fail")
	}
	if _, err := o.Review(context.Background(), run.ID, ReviewRequestChanges, []ReviewComment{{Note: "   "}}); err == nil {
		t.Fatal("request_changes with a blank comment should fail")
	}
}

func TestReviewRejectsWrongStatusAndDecision(t *testing.T) {
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})
	waitTerminal(t, o, run.ID) // `done`, not pr_ready

	if _, err := o.Review(context.Background(), run.ID, ReviewAccept, nil); err == nil {
		t.Fatal("review of a done run should fail")
	}
	driveToPRReady(t, o, run.ID)
	if _, err := o.Review(context.Background(), run.ID, "maybe", nil); err == nil {
		t.Fatal("an unknown decision should fail")
	}
}

func TestReviewCapsRepeatedRejections(t *testing.T) {
	o := New(nil, WithEngine(&prReady{}), WithWorkspace(nil), WithMaxStageIterations(1))
	run, _ := o.Submit(context.Background(), prd.PRD{Title: "X"}, SubmitOptions{})

	comment := []ReviewComment{{Note: "still wrong", TargetRole: factory.RoleBackendDeveloper}}

	driveToPRReady(t, o, run.ID)
	if _, err := o.Review(context.Background(), run.ID, ReviewRequestChanges, comment); err != nil {
		t.Fatal(err)
	}
	driveToPRReady(t, o, run.ID)
	got, err := o.Review(context.Background(), run.ID, ReviewRequestChanges, comment)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusNeedsHumanReview {
		t.Fatalf("status = %s, want needs_human_review past the attempt cap", got.Status)
	}
	if !hasKind(got, EventAttemptCap) {
		t.Fatalf("no attempt_cap event: %v", kinds(got))
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
