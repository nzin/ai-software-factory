package coordinator

import (
	"time"

	"github.com/nzin/ai-software-factory/internal/prd"
)

// Status is a run's lifecycle state.
type Status string

const (
	StatusQueued           Status = "queued"
	StatusRunning          Status = "running"
	StatusPRReady          Status = "pr_ready"
	StatusAccepted         Status = "accepted"
	StatusChangesRequested Status = "changes_requested"
	StatusNeedsHumanReview Status = "needs_human_review"
	StatusFailed           Status = "failed"
	StatusDone             Status = "done"
)

// Budget is the TTL that stops a run from looping forever.
type Budget struct {
	// IterationsRemaining is decremented once per agent dispatch.
	IterationsRemaining int
	// Deadline is a wall-clock cap; zero means no deadline.
	Deadline time.Time
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

// Finding is an issue raised by a reviewer (or a human) that routes work back
// into the factory.
type Finding struct {
	Source     string `json:"source"`
	TargetRole string `json:"targetRole"`
	Note       string `json:"note"`
}

// Task records one agent dispatch.
type Task struct {
	Role   string `json:"role"`
	TaskID string `json:"taskID"`
	State  string `json:"state"`
	Output string `json:"output"`
}

// Run is one PRD flowing through the factory.
type Run struct {
	ID        string    `json:"id"`
	ContextID string    `json:"contextID"`
	PRD       prd.PRD   `json:"prd"`
	Status    Status    `json:"status"`
	Stage     string    `json:"stage"`
	Reason    string    `json:"reason,omitempty"`
	Budget    Budget    `json:"-"`
	Plan      string    `json:"plan,omitempty"`
	Findings  []Finding `json:"findings,omitempty"`
	Tasks     []Task    `json:"tasks,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
