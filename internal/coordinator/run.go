package coordinator

import (
	"fmt"
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

	// Per-step detail, for the run-detail UI.
	Attempt      int       `json:"attempt,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitempty"`
	DurationMs   int64     `json:"durationMs,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	FilesWritten []string  `json:"filesWritten,omitempty"`
	// Findings this step produced. Run.Findings stays the routing accumulator;
	// this is the per-step audit record.
	Findings []Finding `json:"findings,omitempty"`
}

// Event kinds. Every transition a run goes through gets one.
const (
	EventSubmitted        = "submitted"
	EventStageStarted     = "stage_started"
	EventStageCompleted   = "stage_completed"
	EventStageFailed      = "stage_failed"
	EventPlanReady        = "plan_ready"
	EventAwaitingApproval = "awaiting_approval"
	EventApproved         = "approved"
	EventRejected         = "rejected"
	EventRequestChanges   = "request_changes"
	EventBuildFailed      = "build_failed"
	EventPRComment        = "pr_comment"
	EventAttemptCap       = "attempt_cap"
	EventBudgetExhausted  = "budget_exhausted"
	EventPushed           = "pushed"
	EventPROpened         = "pr_opened"
	EventReviewAccepted   = "review_accepted"
	EventReviewChanges    = "review_changes_requested"
	EventResumed          = "resumed"
	EventRecovered        = "recovered"
	EventFinished         = "finished"
	EventTruncated        = "truncated"
)

// Actors behind an event.
const (
	ActorSystem = "system"
	ActorHuman  = "human"
)

// Limits on the event log: the whole Run is marshalled into one store row.
const (
	maxEventDetail = 8 << 10 // 8 KiB
	maxEvents      = 500
)

// Event is one entry in a run's durable audit log.
type Event struct {
	Seq        int       `json:"seq"`
	At         time.Time `json:"at"`
	Kind       string    `json:"kind"`
	Stage      string    `json:"stage,omitempty"`
	Status     string    `json:"status,omitempty"` // run status after this event
	Message    string    `json:"message"`
	Detail     string    `json:"detail,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	DurationMs int64     `json:"durationMs,omitempty"`
	Actor      string    `json:"actor,omitempty"`
}

// addEvent appends an event and returns a pointer to it so the caller can fill
// in the optional fields (Detail, DurationMs, ...). It stamps Seq and At, and
// keeps the log bounded.
func (r *Run) addEvent(kind, stage, message string) *Event {
	seq := 1
	if n := len(r.Events); n > 0 {
		seq = r.Events[n-1].Seq + 1
	}
	r.Events = append(r.Events, Event{
		Seq:     seq,
		At:      time.Now().UTC(),
		Kind:    kind,
		Stage:   stage,
		Status:  string(r.Status),
		Message: message,
		Actor:   ActorSystem,
	})
	if len(r.Events) > maxEvents {
		// Drop the oldest half and leave a marker so the gap is visible.
		drop := len(r.Events) - maxEvents
		r.Events = append([]Event{{
			Seq:     r.Events[0].Seq,
			At:      r.Events[0].At,
			Kind:    EventTruncated,
			Message: fmt.Sprintf("%d earlier events dropped (log capped at %d)", drop, maxEvents),
			Actor:   ActorSystem,
		}}, r.Events[drop+1:]...)
	}
	return &r.Events[len(r.Events)-1]
}

// WithDetail attaches a (truncated) detail body to an event.
func (e *Event) WithDetail(s string) *Event {
	if len(s) > maxEventDetail {
		s = s[:maxEventDetail] + "\n… (truncated)"
	}
	e.Detail = s
	return e
}

// By marks who triggered the event.
func (e *Event) By(actor string) *Event { e.Actor = actor; return e }

// Run is one PRD flowing through the factory.
type Run struct {
	ID        string  `json:"id"`
	ContextID string  `json:"contextID"`
	PRD       prd.PRD `json:"prd"`
	Status    Status  `json:"status"`
	Stage     string  `json:"stage"`
	LastStage string  `json:"lastStage,omitempty"` // stage the run stopped in; where resume restarts
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
	Plan         string                    `json:"plan,omitempty"`
	PlanTasks    []factory.PlanTask        `json:"planTasks,omitempty"`
	Approval     *factory.ApprovalDecision `json:"approval,omitempty"`
	UISpec       string                    `json:"uiSpec,omitempty"`
	PlanFeedback string                    `json:"planFeedback,omitempty"` // human note for the next planner pass; consumed once

	// Loop state.
	Findings []Finding `json:"findings,omitempty"` // every finding raised across the whole run's history — for display
	// LastRoundFindings is only the findings from whichever gate/reviewer/human
	// round most recently bumped an Attempts counter — what actually triggered
	// the run's current retry. A developer's fix-pass dispatch is scoped to
	// this, not the full Findings history, so a retry isn't handed
	// possibly-already-resolved findings from several rounds back alongside
	// the ones that are actually current.
	LastRoundFindings []Finding      `json:"lastRoundFindings,omitempty"`
	Tasks             []Task         `json:"tasks,omitempty"`
	Events            []Event        `json:"events,omitempty"`
	Attempts          map[string]int `json:"attempts,omitempty"`
	PausedAt          time.Time      `json:"pausedAt,omitempty"`

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
