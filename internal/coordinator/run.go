package coordinator

import (
	"time"

	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/prd"
)

// Status is a run's lifecycle state.
type Status string

const (
	StatusQueued           Status = "queued"
	StatusRunning          Status = "running"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusPRReady          Status = "pr_ready"
	StatusPROpen           Status = "pr_open"
	StatusAccepted         Status = "accepted"
	StatusChangesRequested Status = "changes_requested"
	StatusNeedsHumanReview Status = "needs_human_review"
	StatusFailed           Status = "failed"
	StatusDone             Status = "done"
)

// Terminal reports whether a run in this status will never advance on its own.
func (s Status) Terminal() bool {
	switch s {
	case StatusPRReady, StatusPROpen, StatusAccepted, StatusFailed, StatusDone, StatusNeedsHumanReview:
		return true
	}
	return false
}

// Budget is the TTL that stops a run from looping forever.
type Budget struct {
	// IterationsRemaining is decremented once per agent dispatch.
	IterationsRemaining int `json:"iterationsRemaining"`
	// Deadline is a wall-clock cap; zero means no deadline.
	Deadline time.Time `json:"deadline"`
}

// Exhausted reports whether the budget is spent, with a human-readable reason.
func (b Budget) Exhausted() (bool, string) {
	if b.IterationsRemaining <= 0 {
		return true, "iteration budget exhausted"
	}
	if !b.Deadline.IsZero() && time.Now().After(b.Deadline) {
		return true, "deadline exceeded"
	}
	return false, ""
}

// Finding is re-exported from internal/factory for the coordinator API.
type Finding = factory.Finding

// Task records one agent dispatch.
type Task struct {
	Role      string `json:"role"`
	TaskID    string `json:"taskID"`
	State     string `json:"state"`
	Summary   string `json:"summary,omitempty"`
	CommitSHA string `json:"commitSha,omitempty"`
	Output    string `json:"output,omitempty"`
}

// Run is one PRD flowing through the factory.
type Run struct {
	ID        string  `json:"id"`
	ContextID string  `json:"contextID"`
	PRD       prd.PRD `json:"prd"`
	Status    Status  `json:"status"`
	Stage     string  `json:"stage"`
	Reason    string  `json:"reason,omitempty"`
	Budget    Budget  `json:"budget"`

	// Repository / workspace.
	RepoURL      string `json:"repoUrl,omitempty"`
	RepoKind     string `json:"repoKind,omitempty"` // new | local | remote
	BaseBranch   string `json:"baseBranch,omitempty"`
	WorkBranch   string `json:"workBranch,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
	PRURL        string `json:"prUrl,omitempty"`

	// Planner output.
	Plan      string                    `json:"plan,omitempty"`
	PlanTasks []factory.PlanTask        `json:"planTasks,omitempty"`
	Approval  *factory.ApprovalDecision `json:"approval,omitempty"`
	UISpec    string                    `json:"uiSpec,omitempty"`

	// Loop state.
	Findings []Finding      `json:"findings,omitempty"`
	Tasks    []Task         `json:"tasks,omitempty"`
	Attempts map[string]int `json:"attempts,omitempty"`
	PausedAt time.Time      `json:"pausedAt,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// lastDeveloper returns the most recent developer role that ran, or "".
func (r *Run) lastDeveloper() string {
	for i := len(r.Tasks) - 1; i >= 0; i-- {
		if factory.IsDeveloperRole(r.Tasks[i].Role) {
			return r.Tasks[i].Role
		}
	}
	return ""
}
