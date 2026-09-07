// Package coordinator accepts a PRD, discovers specialized agents via the
// catalog, and drives a Run through the factory: planner → (human approval gate)
// → developers → reviewers, looping back to a developer on request_changes until
// the reviewers approve or a per-stage attempt cap is hit. Each Run carries a
// TTL/budget and works inside a per-run git workspace.
package coordinator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/planner"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/forge"
	"github.com/nzin/ai-software-factory/internal/prd"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// Defaults for a run.
const (
	DefaultIterationBudget    = 40
	DefaultDeadline           = 3 * time.Hour
	DefaultMaxStageIterations = 3
	DefaultBaseBranch         = "main"
)

// StageResult is what an Engine returns for one dispatched stage.
type StageResult struct {
	Task       Task
	Findings   []Finding
	Verdict    string // reviewers: factory.VerdictApprove | VerdictRequestChanges
	TargetRole string // reviewers: preferred fix-pass role
	Plan       *factory.PlanDoc
}

// Engine dispatches one stage to a real agent (or a fake, in tests).
type Engine interface {
	Run(ctx context.Context, run *Run) (StageResult, error)
}

// Orchestrator drives runs and owns their persistence.
type Orchestrator struct {
	engine    Engine
	workspace *workspace.Manager
	store     Store

	iterationBudget int
	deadline        time.Duration
	maxStageIter    int
	baseBranch      string
	pruneKeep       int

	mu      sync.Mutex
	driving map[string]bool // runs with a live drive goroutine
}

// Option configures an Orchestrator.
type Option func(*Orchestrator)

func WithEngine(e Engine) Option                { return func(o *Orchestrator) { o.engine = e } }
func WithWorkspace(m *workspace.Manager) Option { return func(o *Orchestrator) { o.workspace = m } }
func WithStore(s Store) Option                  { return func(o *Orchestrator) { o.store = s } }
func WithMaxStageIterations(n int) Option       { return func(o *Orchestrator) { o.maxStageIter = n } }
func WithBaseBranch(b string) Option            { return func(o *Orchestrator) { o.baseBranch = b } }

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
		engine:          &pipelineEngine{catalog: catalog},
		workspace:       workspace.NewManager("workspace"),
		store:           NewMemStore(),
		iterationBudget: DefaultIterationBudget,
		deadline:        DefaultDeadline,
		maxStageIter:    DefaultMaxStageIterations,
		baseBranch:      DefaultBaseBranch,
		pruneKeep:       50,
		driving:         make(map[string]bool),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Recover reconciles persisted runs on startup: a run left mid-flight (running)
// is parked for a human; awaiting_approval runs stay resumable.
func (o *Orchestrator) Recover() error {
	runs, err := o.store.All()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if r.Status == StatusRunning {
			r.Status = StatusNeedsHumanReview
			r.Reason = "coordinator restarted mid-run"
			r.UpdatedAt = time.Now().UTC()
			_ = o.store.Put(r)
			log.Printf("run %s: parked (coordinator restarted mid-%s)", r.ID, r.Stage)
		}
	}
	return nil
}

// SubmitOptions overrides per-run defaults.
type SubmitOptions struct {
	RepoURL         string
	BaseBranch      string
	IterationBudget int
	Deadline        time.Duration
}

// Submit creates a run, kicks off the pipeline in the background, and returns
// immediately with the run in its initial state.
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
	base := opts.BaseBranch
	if base == "" {
		base = o.baseBranch
	}

	run := &Run{
		ID:         uuid.NewString(),
		ContextID:  a2a.NewContextID(),
		PRD:        p,
		Status:     StatusRunning,
		Stage:      planner.Role,
		Budget:     Budget{IterationsRemaining: iters, Deadline: deadline},
		RepoURL:    opts.RepoURL,
		BaseBranch: base,
		Attempts:   map[string]int{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if o.workspace != nil {
		ref := workspace.ParseRepoURL(opts.RepoURL, base)
		repo, err := o.workspace.Prepare(context.Background(), run.ID, ref)
		if err != nil {
			return nil, fmt.Errorf("coordinator: prepare workspace: %w", err)
		}
		run.WorkspaceDir = repo.Dir
		run.WorkBranch = repo.WorkBranch
		run.RepoKind = string(repo.Kind)
	}

	if err := o.store.Put(run); err != nil {
		return nil, err
	}
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// Get returns a run by id.
func (o *Orchestrator) Get(id string) (*Run, bool) {
	r, ok, _ := o.store.Get(id)
	return r, ok
}

// List returns all runs.
func (o *Orchestrator) List() []*Run {
	runs, _ := o.store.All()
	return runs
}

// Approve resumes a run waiting at the human-approval gate.
func (o *Orchestrator) Approve(id string) (*Run, error) {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("coordinator: no run %s", id)
	}
	if run.Status != StatusAwaitingApproval {
		return run, fmt.Errorf("coordinator: run %s is %s, not awaiting_approval", id, run.Status)
	}
	o.unfreeze(run)
	run.Status = StatusRunning
	run.Stage = firstDevelopmentStage(run)
	run.UpdatedAt = time.Now().UTC()
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// Reject sends a run at the gate back to the planner with feedback, or abandons it.
func (o *Orchestrator) Reject(id, feedback string, abandon bool) (*Run, error) {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("coordinator: no run %s", id)
	}
	if run.Status != StatusAwaitingApproval {
		return run, fmt.Errorf("coordinator: run %s is %s, not awaiting_approval", id, run.Status)
	}
	if abandon {
		o.stop(run, StatusFailed, "abandoned by human at the approval gate")
		if r, ok, _ := o.store.Get(run.ID); ok {
			return r, nil
		}
		return run, nil
	}
	o.unfreeze(run)
	if feedback != "" {
		run.PRD.Description += "\n\n## Reviewer feedback on the previous plan\n" + feedback
	}
	run.Status = StatusRunning
	run.Stage = planner.Role
	run.Plan, run.PlanTasks, run.Approval = "", nil, nil
	run.UpdatedAt = time.Now().UTC()
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// unfreeze extends the deadline by the time the run spent paused.
func (o *Orchestrator) unfreeze(run *Run) {
	if !run.PausedAt.IsZero() && !run.Budget.Deadline.IsZero() {
		run.Budget.Deadline = run.Budget.Deadline.Add(time.Since(run.PausedAt))
	}
	run.PausedAt = time.Time{}
}

// start launches drive() in the background unless one is already running.
func (o *Orchestrator) start(run *Run) {
	o.mu.Lock()
	if o.driving[run.ID] {
		o.mu.Unlock()
		return
	}
	o.driving[run.ID] = true
	o.mu.Unlock()

	go func() {
		defer func() {
			o.mu.Lock()
			delete(o.driving, run.ID)
			o.mu.Unlock()
		}()
		o.drive(context.Background(), run)
	}()
}

// drive is the run state machine. It returns when the run pauses (awaiting
// approval) or reaches a terminal state.
func (o *Orchestrator) drive(ctx context.Context, run *Run) {
	if run.Attempts == nil {
		run.Attempts = map[string]int{}
	}
	for {
		if run.Status == StatusAwaitingApproval || run.Status.Terminal() {
			return
		}
		if spent, reason := run.Budget.Exhausted(); spent {
			o.stop(run, StatusNeedsHumanReview, reason)
			return
		}
		run.Budget.IterationsRemaining--

		log.Printf("run %s: stage %s (budget %d left, attempt %d)",
			run.ID, run.Stage, run.Budget.IterationsRemaining, run.Attempts[run.Stage])
		started := time.Now()
		res, err := o.engine.Run(ctx, run)
		if err != nil {
			log.Printf("run %s: stage %s FAILED after %s: %v",
				run.ID, run.Stage, time.Since(started).Round(time.Second), err)
			o.stop(run, StatusFailed, err.Error())
			return
		}
		log.Printf("run %s: stage %s done in %s — %s (verdict=%q, %d findings)",
			run.ID, run.Stage, time.Since(started).Round(time.Second), res.Task.Summary, res.Verdict, len(res.Findings))

		run.Tasks = append(run.Tasks, res.Task)
		run.Findings = mergeFindings(run.Findings, res.Findings)
		run.UpdatedAt = time.Now().UTC()

		switch {
		case run.Stage == planner.Role:
			o.applyPlan(run, res)
			if run.Approval != nil && run.Approval.Required {
				run.PausedAt = time.Now().UTC()
				run.Status = StatusAwaitingApproval
				run.Stage = ""
				_ = o.store.Put(run)
				log.Printf("run %s: awaiting human approval — %s", run.ID, run.Approval.Reason)
				return
			}
			run.Stage = firstDevelopmentStage(run)

		case isReviewer(run.Stage) && res.Verdict == factory.VerdictRequestChanges:
			target := res.TargetRole
			if !factory.IsDeveloperRole(target) {
				target = factory.RouteRole(res.Findings, run.lastDeveloper())
			}
			run.Attempts[target]++
			if run.Attempts[target] > o.maxStageIter {
				o.stop(run, StatusNeedsHumanReview,
					fmt.Sprintf("%s still failing review after %d fix attempts", target, o.maxStageIter))
				return
			}
			log.Printf("run %s: %s requested changes → back to %s (attempt %d)",
				run.ID, run.Stage, target, run.Attempts[target])
			run.Stage = target

		default:
			run.Stage = nextStage(run)
		}

		_ = o.store.Put(run)

		if run.Stage == "" {
			o.finish(ctx, run)
			log.Printf("run %s: complete — status=%s", run.ID, run.Status)
			return
		}
	}
}

func (o *Orchestrator) applyPlan(run *Run, res StageResult) {
	if res.Plan != nil {
		run.Plan = res.Plan.Prose
		run.PlanTasks = res.Plan.Tasks
		run.Approval = &res.Plan.Approval
	} else if res.Task.Output != "" {
		run.Plan = res.Task.Output
	}
}

func (o *Orchestrator) finish(ctx context.Context, run *Run) {
	if run.WorkspaceDir == "" {
		o.stop(run, StatusDone, "")
		return
	}
	repo, err := workspace.Open(ctx, run.WorkspaceDir)
	if err != nil {
		o.stop(run, StatusDone, "workspace unavailable")
		return
	}
	repo.BaseBranch = run.BaseBranch
	if !repo.HasCommits(ctx) {
		o.stop(run, StatusDone, "no changes were made")
		return
	}

	switch run.RepoKind {
	case string(workspace.KindRemote):
		if err := repo.Push(ctx); err != nil {
			// The clone-fallback for a local repo has no reachable origin; treat
			// a push failure there as "branch ready locally".
			o.stop(run, StatusPRReady, "commits ready on "+run.WorkBranch+" (push failed: "+err.Error()+")")
			return
		}
		if url, _ := forge.OpenPR(ctx, run.RepoURL, run.BaseBranch, run.WorkBranch, run.PRD.Title, run.Plan); url != "" {
			run.PRURL = url
			o.stop(run, StatusPROpen, "PR opened: "+url)
			return
		}
		o.stop(run, StatusPRReady, "branch "+run.WorkBranch+" pushed to origin")

	case string(workspace.KindLocal):
		src := run.RepoURL
		o.stop(run, StatusPRReady,
			fmt.Sprintf("branch %s ready in %s — `git merge %s`", run.WorkBranch, src, run.WorkBranch))

	default: // new
		o.stop(run, StatusPRReady, "branch "+run.WorkBranch+" in "+run.WorkspaceDir)
	}

	if o.workspace != nil {
		_ = o.workspace.Prune(o.pruneKeep)
	}
}

func (o *Orchestrator) stop(run *Run, status Status, reason string) {
	run.Status = status
	run.Reason = reason
	run.Stage = ""
	run.UpdatedAt = time.Now().UTC()
	_ = o.store.Put(run)
}

func mergeFindings(existing, incoming []Finding) []Finding {
	if len(incoming) == 0 {
		return existing
	}
	return append(existing, incoming...)
}
