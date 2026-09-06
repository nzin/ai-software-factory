// Package coordinator accepts a PRD, discovers specialized agents via the
// catalog, and drives a Run through the factory. Each Run carries a TTL/budget
// so it can never loop forever; when the budget is spent the Run stops in
// needs_human_review.
package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/google/uuid"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/planner"
	"github.com/nzin/ai-software-factory/internal/prd"
)

// Defaults for a run's budget.
const (
	DefaultIterationBudget = 30
	DefaultDeadline        = 2 * time.Hour
)

// StageResult is what an Engine returns for one dispatched stage.
type StageResult struct {
	Task     Task
	Findings []Finding
}

// Engine executes stages and decides what runs next. The default engine talks
// to real agents over A2A; tests can substitute a fake.
type Engine interface {
	// Run executes run.Stage and returns its result.
	Run(ctx context.Context, run *Run) (StageResult, error)
	// Next returns the stage to move to, or "" when the run is complete.
	Next(run *Run) string
}

// Orchestrator owns the in-memory set of runs and drives them.
type Orchestrator struct {
	engine Engine

	iterationBudget int
	deadline        time.Duration

	mu   sync.RWMutex
	runs map[string]*Run
}

// Option configures an Orchestrator.
type Option func(*Orchestrator)

// WithEngine overrides the stage engine (used in tests).
func WithEngine(e Engine) Option { return func(o *Orchestrator) { o.engine = e } }

// WithDefaults overrides the default per-run budget.
func WithDefaults(iterationBudget int, deadline time.Duration) Option {
	return func(o *Orchestrator) {
		o.iterationBudget = iterationBudget
		o.deadline = deadline
	}
}

// New builds an Orchestrator. catalog may be nil if a custom engine is supplied.
func New(catalog *agentkit.CatalogClient, opts ...Option) *Orchestrator {
	o := &Orchestrator{
		engine:          &a2aEngine{catalog: catalog},
		iterationBudget: DefaultIterationBudget,
		deadline:        DefaultDeadline,
		runs:            make(map[string]*Run),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// SubmitOptions overrides per-run defaults.
type SubmitOptions struct {
	IterationBudget int
	Deadline        time.Duration
}

// Submit creates a run for p and drives it to completion (Phase 1 is
// synchronous). The returned Run reflects the final state.
func (o *Orchestrator) Submit(ctx context.Context, p prd.PRD, opts SubmitOptions) (*Run, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	iters := o.iterationBudget
	if opts.IterationBudget != 0 {
		iters = opts.IterationBudget
	}
	dl := o.deadline
	if opts.Deadline != 0 {
		dl = opts.Deadline
	}
	var deadline time.Time
	if dl > 0 {
		deadline = now.Add(dl)
	}

	run := &Run{
		ID:        uuid.NewString(),
		ContextID: a2a.NewContextID(),
		PRD:       p,
		Status:    StatusRunning,
		Stage:     planner.Role,
		Budget:    Budget{IterationsRemaining: iters, Deadline: deadline},
		CreatedAt: now,
		UpdatedAt: now,
	}
	o.mu.Lock()
	o.runs[run.ID] = run
	o.mu.Unlock()

	o.drive(ctx, run)
	return run, nil
}

// Get returns a run by id.
func (o *Orchestrator) Get(id string) (*Run, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	r, ok := o.runs[id]
	return r, ok
}

// List returns all runs.
func (o *Orchestrator) List() []*Run {
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*Run, 0, len(o.runs))
	for _, r := range o.runs {
		out = append(out, r)
	}
	return out
}

// drive runs the state machine until the run reaches a terminal (or
// needs-human) state or the budget is spent.
func (o *Orchestrator) drive(ctx context.Context, run *Run) {
	for {
		if spent, reason := run.Budget.Exhausted(); spent {
			o.stop(run, StatusNeedsHumanReview, reason)
			return
		}
		run.Budget.IterationsRemaining--

		res, err := o.engine.Run(ctx, run)
		if err != nil {
			o.stop(run, StatusFailed, err.Error())
			return
		}
		o.mu.Lock()
		run.Tasks = append(run.Tasks, res.Task)
		run.Findings = append(run.Findings, res.Findings...)
		if res.Task.Role == planner.Role && res.Task.Output != "" {
			run.Plan = res.Task.Output
		}
		run.UpdatedAt = time.Now().UTC()
		o.mu.Unlock()

		next := o.engine.Next(run)
		if next == "" {
			o.stop(run, StatusDone, "")
			return
		}
		run.Stage = next
	}
}

func (o *Orchestrator) stop(run *Run, status Status, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	run.Status = status
	run.Reason = reason
	run.UpdatedAt = time.Now().UTC()
}

// --- default engine: talk to real agents over A2A ---

type a2aEngine struct {
	catalog *agentkit.CatalogClient
}

func (e *a2aEngine) Run(ctx context.Context, run *Run) (StageResult, error) {
	switch run.Stage {
	case planner.Role:
		out, err := e.dispatch(ctx, planner.Role, run.ContextID, run.PRD.Text())
		if err != nil {
			return StageResult{}, err
		}
		return StageResult{Task: Task{Role: planner.Role, State: "completed", Output: out}}, nil
	default:
		return StageResult{}, fmt.Errorf("coordinator: no engine for stage %q", run.Stage)
	}
}

// Next: Phase 1 stops after the planner.
func (e *a2aEngine) Next(run *Run) string {
	if run.Stage == planner.Role {
		return ""
	}
	return ""
}

// dispatch discovers the agent for role via the catalog and sends it one message.
func (e *a2aEngine) dispatch(ctx context.Context, role, contextID, text string) (string, error) {
	if e.catalog == nil {
		return "", fmt.Errorf("coordinator: no catalog configured")
	}
	baseURL, err := e.catalog.GetAgentBaseURL(ctx, role)
	if err != nil {
		return "", err
	}
	card, err := agentcard.DefaultResolver.Resolve(ctx, baseURL)
	if err != nil {
		return "", fmt.Errorf("coordinator: resolve %s card: %w", role, err)
	}
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		return "", fmt.Errorf("coordinator: a2a client for %s: %w", role, err)
	}

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text))
	msg.ContextID = contextID

	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		return "", fmt.Errorf("coordinator: send to %s: %w", role, err)
	}
	return resultText(res), nil
}

func resultText(res a2a.SendMessageResult) string {
	switch v := res.(type) {
	case *a2a.Message:
		return partsText(v.Parts)
	case *a2a.Task:
		if v.Status.Message != nil {
			if t := partsText(v.Status.Message.Parts); t != "" {
				return t
			}
		}
		for i := len(v.History) - 1; i >= 0; i-- {
			if v.History[i].Role == a2a.MessageRoleAgent {
				if t := partsText(v.History[i].Parts); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

func partsText(parts a2a.ContentParts) string {
	var out string
	for _, p := range parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			if out != "" {
				out += "\n"
			}
			out += t
		}
	}
	return out
}
