package runstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nzin/ai-software-factory/internal/coordinator"
	"github.com/nzin/ai-software-factory/internal/factory"
)

func openTemp(t *testing.T) coordinator.Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st
}

func TestRoundTrip(t *testing.T) {
	st := openTemp(t)
	now := time.Now().UTC().Truncate(time.Second)
	in := &coordinator.Run{
		ID:         "r1",
		Status:     coordinator.StatusAwaitingApproval,
		Stage:      "",
		RepoURL:    "https://github.com/nzin/x",
		RepoKind:   "remote",
		BaseBranch: "main",
		WorkBranch: "asf/run-r1",
		Approval:   &factory.ApprovalDecision{Required: true, Reason: "touches code"},
		PlanTasks:  []factory.PlanTask{{ID: "T1", Role: factory.RoleBackendDeveloper, Title: "api"}},
		Attempts:   map[string]int{"backend-developer": 2},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.Put(in); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, ok, err := st.Get("r1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Status != coordinator.StatusAwaitingApproval || got.WorkBranch != "asf/run-r1" {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
	if got.Approval == nil || !got.Approval.Required || got.Approval.Reason != "touches code" {
		t.Fatalf("approval = %+v", got.Approval)
	}
	if got.Attempts["backend-developer"] != 2 || len(got.PlanTasks) != 1 {
		t.Fatalf("attempts/tasks lost: %+v", got)
	}

	if _, ok, _ := st.Get("missing"); ok {
		t.Fatal("missing run reported present")
	}
}

func TestRecoverParksRunningRuns(t *testing.T) {
	st := openTemp(t)
	now := time.Now().UTC()
	must := func(r *coordinator.Run) {
		if err := st.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	must(&coordinator.Run{ID: "running", Status: coordinator.StatusRunning, Stage: "backend-developer", CreatedAt: now, UpdatedAt: now})
	must(&coordinator.Run{ID: "paused", Status: coordinator.StatusAwaitingApproval, CreatedAt: now, UpdatedAt: now,
		Approval: &factory.ApprovalDecision{Required: true, Reason: "x"}})
	must(&coordinator.Run{ID: "done", Status: coordinator.StatusPRReady, CreatedAt: now, UpdatedAt: now})

	o := coordinator.New(nil, coordinator.WithStore(st), coordinator.WithWorkspace(nil))
	if err := o.Recover(); err != nil {
		t.Fatalf("recover: %v", err)
	}

	if r, _ := o.Get("running"); r.Status != coordinator.StatusNeedsHumanReview {
		t.Fatalf("running run = %s, want needs_human_review", r.Status)
	}
	if r, _ := o.Get("paused"); r.Status != coordinator.StatusAwaitingApproval {
		t.Fatalf("paused run = %s, want awaiting_approval (preserved)", r.Status)
	}
	if r, _ := o.Get("done"); r.Status != coordinator.StatusPRReady {
		t.Fatalf("done run = %s, want pr_ready (untouched)", r.Status)
	}
}
