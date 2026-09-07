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
	"strings"
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

// CatalogReader is the slice of the catalog the UI needs. The coordinator acts
// as a BFF for the browser, which is same-origin with it but not the catalog.
type CatalogReader interface {
	ListAgents(ctx context.Context) ([]agentkit.AgentInfo, error)
	GetAgentDetail(ctx context.Context, role string) (*agentkit.AgentDetail, error)
}

// Orchestrator drives runs and owns their persistence.
type Orchestrator struct {
	engine    Engine
	workspace *workspace.Manager
	store     Store
	catalog   CatalogReader

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
func WithCatalog(c CatalogReader) Option        { return func(o *Orchestrator) { o.catalog = c } }
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
		catalog:         catalogOrNil(catalog),
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

// catalogOrNil avoids stuffing a typed nil pointer into the interface, which
// would make `o.catalog != nil` true for a nil client.
func catalogOrNil(c *agentkit.CatalogClient) CatalogReader {
	if c == nil {
		return nil
	}
	return c
}

// Catalog returns the catalog reader, or nil when none is configured.
func (o *Orchestrator) Catalog() CatalogReader { return o.catalog }

// event appends an entry to the run's durable log and mirrors it to stdout, so
// the two can never drift. The returned pointer takes optional detail.
func (o *Orchestrator) event(run *Run, kind, stage, format string, a ...any) *Event {
	msg := fmt.Sprintf(format, a...)
	log.Printf("run %s: %s", run.ID, msg)
	return run.addEvent(kind, stage, msg)
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
			r.LastStage = r.Stage
			r.Status = StatusNeedsHumanReview
			r.Reason = "coordinator restarted mid-run"
			r.UpdatedAt = time.Now().UTC()
			o.event(r, EventRecovered, r.LastStage, "parked (coordinator restarted mid-%s)", r.LastStage)
			_ = o.store.Put(r)
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

	o.event(run, EventSubmitted, run.Stage,
		"submitted %q (repo kind=%s, branch=%s, budget=%d)",
		p.Title, orDash(run.RepoKind), orDash(run.WorkBranch), iters).
		By(ActorHuman).
		WithDetail(p.Text())

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
	o.event(run, EventApproved, run.Stage, "plan approved by a human — resuming at %s", orDash(run.Stage)).
		By(ActorHuman)
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
		o.event(run, EventRejected, run.LastStage, "plan rejected and abandoned by a human").
			By(ActorHuman).WithDetail(feedback)
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
	o.event(run, EventRejected, run.Stage, "plan rejected by a human — replanning").
		By(ActorHuman).WithDetail(feedback)
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// ResumeOptions are the knobs on un-sticking a needs_human_review run.
type ResumeOptions struct {
	IterationBudget int           // extra iterations to grant (0 = a default top-up)
	Deadline        time.Duration // extra wall-clock to grant (0 = a default top-up)
	Abandon         bool
}

// Resume restarts a run parked in needs_human_review, granting fresh budget.
func (o *Orchestrator) Resume(id string, opts ResumeOptions) (*Run, error) {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("coordinator: no run %s", id)
	}
	if run.Status != StatusNeedsHumanReview {
		return run, fmt.Errorf("coordinator: run %s is %s, not needs_human_review", id, run.Status)
	}
	if opts.Abandon {
		o.event(run, EventRejected, run.LastStage, "abandoned by a human").By(ActorHuman)
		o.stop(run, StatusFailed, "abandoned by human")
		if r, ok, _ := o.store.Get(run.ID); ok {
			return r, nil
		}
		return run, nil
	}

	grant := opts.IterationBudget
	if grant <= 0 {
		grant = o.iterationBudget
	}
	run.Budget.IterationsRemaining += grant

	extend := opts.Deadline
	if extend <= 0 {
		extend = o.deadline
	}
	if extend > 0 {
		from := time.Now().UTC()
		if run.Budget.Deadline.After(from) {
			from = run.Budget.Deadline
		}
		run.Budget.Deadline = from.Add(extend)
	}

	// A run parked by the per-stage attempt cap would re-trip immediately unless
	// the counters are cleared: the human is explicitly saying "try again".
	run.Attempts = map[string]int{}

	run.Stage = run.LastStage
	if run.Stage == "" {
		run.Stage = firstDevelopmentStage(run)
	}
	if run.Stage == "" {
		run.Stage = planner.Role
	}
	run.Status = StatusRunning
	run.Reason = ""
	run.UpdatedAt = time.Now().UTC()
	o.event(run, EventResumed, run.Stage,
		"resumed by a human at %s (+%d iterations)", run.Stage, grant).By(ActorHuman)
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// ReviewComment is one human note on a finished run's branch/PR.
type ReviewComment struct {
	Note       string
	TargetRole string
	File       string
	Line       int
}

// Review decisions.
const (
	ReviewAccept         = "accept"
	ReviewRequestChanges = "request_changes"
)

// Review applies a human's verdict on a run that produced a branch/PR. Accepting
// is terminal; requesting changes turns each comment into a human Finding and
// sends the run back through the factory.
func (o *Orchestrator) Review(id, decision string, comments []ReviewComment) (*Run, error) {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("coordinator: no run %s", id)
	}
	if run.Status != StatusPRReady && run.Status != StatusPROpen {
		return run, fmt.Errorf("coordinator: run %s is %s, not pr_ready/pr_open", id, run.Status)
	}

	switch decision {
	case ReviewAccept:
		o.event(run, EventReviewAccepted, run.LastStage, "changes accepted by a human").By(ActorHuman)
		o.stop(run, StatusAccepted, "accepted by human review")
		if r, ok, _ := o.store.Get(run.ID); ok {
			return r, nil
		}
		return run, nil

	case ReviewRequestChanges:
		if len(comments) == 0 {
			return run, fmt.Errorf("coordinator: request_changes needs at least one comment")
		}
		fresh := make([]Finding, 0, len(comments))
		for _, c := range comments {
			if strings.TrimSpace(c.Note) == "" {
				continue
			}
			fresh = append(fresh, Finding{
				Source:     "human",
				Severity:   "high",
				Title:      c.Note,
				TargetRole: c.TargetRole,
				File:       c.File,
				Line:       c.Line,
			})
		}
		if len(fresh) == 0 {
			return run, fmt.Errorf("coordinator: request_changes needs at least one non-empty comment")
		}
		run.Findings = append(run.Findings, fresh...)

		target := factory.RouteRole(fresh, run.lastDeveloper())
		if run.Attempts == nil {
			run.Attempts = map[string]int{}
		}
		run.Attempts[target]++
		if run.Attempts[target] > o.maxStageIter {
			o.event(run, EventAttemptCap, target,
				"%s already had %d fix attempts — parking for a human", target, o.maxStageIter)
			o.stop(run, StatusNeedsHumanReview,
				fmt.Sprintf("%s still failing review after %d fix attempts", target, o.maxStageIter))
			if r, ok, _ := o.store.Get(run.ID); ok {
				return r, nil
			}
			return run, nil
		}

		run.Status = StatusRunning
		run.Stage = target
		run.Reason = ""
		run.UpdatedAt = time.Now().UTC()
		o.event(run, EventReviewChanges, target,
			"human requested changes (%d comments) → back to %s (attempt %d)",
			len(fresh), target, run.Attempts[target]).
			By(ActorHuman).WithDetail(renderComments(fresh))
		_ = o.store.Put(run)
		o.start(run)
		if r, ok, _ := o.store.Get(run.ID); ok {
			return r, nil
		}
		return run, nil

	default:
		return run, fmt.Errorf("coordinator: unknown review decision %q", decision)
	}
}

func renderComments(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		if f.File != "" {
			fmt.Fprintf(&b, "- %s:%d — %s\n", f.File, f.Line, f.Title)
		} else {
			fmt.Fprintf(&b, "- %s\n", f.Title)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
			o.event(run, EventBudgetExhausted, run.Stage, "%s — parking for a human", reason)
			o.stop(run, StatusNeedsHumanReview, reason)
			return
		}
		run.Budget.IterationsRemaining--

		stage, attempt := run.Stage, run.Attempts[run.Stage]
		o.event(run, EventStageStarted, stage, "stage %s started (budget %d left, attempt %d)",
			stage, run.Budget.IterationsRemaining, attempt).Attempt = attempt

		started := time.Now()
		res, err := o.engine.Run(ctx, run)
		took := time.Since(started)
		if err != nil {
			ev := o.event(run, EventStageFailed, stage, "stage %s FAILED after %s: %v",
				stage, took.Round(time.Second), err)
			ev.DurationMs, ev.Attempt = took.Milliseconds(), attempt
			o.stop(run, StatusFailed, err.Error())
			return
		}
		ev := o.event(run, EventStageCompleted, stage, "stage %s done in %s — %s (verdict=%q, %d findings)",
			stage, took.Round(time.Second), res.Task.Summary, res.Verdict, len(res.Findings))
		ev.DurationMs, ev.Attempt = took.Milliseconds(), attempt

		task := res.Task
		task.Attempt = attempt
		task.StartedAt = started.UTC()
		task.DurationMs = took.Milliseconds()
		if task.Verdict == "" {
			task.Verdict = res.Verdict
		}
		task.Findings = res.Findings
		run.Tasks = append(run.Tasks, task)
		run.Findings = mergeFindings(run.Findings, res.Findings)
		run.UpdatedAt = time.Now().UTC()

		switch {
		case stage == planner.Role:
			o.applyPlan(run, res)
			o.event(run, EventPlanReady, stage, "plan ready: %d tasks, approval required=%v",
				len(run.PlanTasks), run.Approval != nil && run.Approval.Required).
				WithDetail(run.Plan)
			if run.Approval != nil && run.Approval.Required {
				run.PausedAt = time.Now().UTC()
				run.Status = StatusAwaitingApproval
				run.LastStage = firstDevelopmentStage(run)
				run.Stage = ""
				o.event(run, EventAwaitingApproval, "", "awaiting human approval — %s", run.Approval.Reason)
				_ = o.store.Put(run)
				return
			}
			run.Stage = firstDevelopmentStage(run)

		case isReviewer(stage) && res.Verdict == factory.VerdictRequestChanges:
			target := res.TargetRole
			if !factory.IsDeveloperRole(target) {
				target = factory.RouteRole(res.Findings, run.lastDeveloper())
			}
			run.Attempts[target]++
			if run.Attempts[target] > o.maxStageIter {
				o.event(run, EventAttemptCap, target,
					"%s still failing review after %d fix attempts", target, o.maxStageIter)
				o.stop(run, StatusNeedsHumanReview,
					fmt.Sprintf("%s still failing review after %d fix attempts", target, o.maxStageIter))
				return
			}
			o.event(run, EventRequestChanges, target,
				"%s requested changes → back to %s (attempt %d)", stage, target, run.Attempts[target]).
				Attempt = run.Attempts[target]
			run.Stage = target

		default:
			run.Stage = nextStage(run)
		}

		_ = o.store.Put(run)

		if run.Stage == "" {
			o.finish(ctx, run)
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
		o.event(run, EventPushed, "", "branch %s pushed to origin", run.WorkBranch)

		// A run coming back from human review already has a PR; re-opening it
		// would 422 and silently demote the run to pr_ready.
		if run.PRURL != "" {
			o.stop(run, StatusPROpen, "PR updated: "+run.PRURL)
			return
		}
		if url, _ := forge.OpenPR(ctx, run.RepoURL, run.BaseBranch, run.WorkBranch, run.PRD.Title, run.Plan); url != "" {
			run.PRURL = url
			o.event(run, EventPROpened, "", "pull request opened: %s", url)
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
	if run.Stage != "" {
		run.LastStage = run.Stage // where Resume picks the run back up
	}
	run.Status = status
	run.Reason = reason
	run.Stage = ""
	run.UpdatedAt = time.Now().UTC()
	o.event(run, EventFinished, run.LastStage, "run %s — %s", status, orDash(reason))
	_ = o.store.Put(run)
}

func mergeFindings(existing, incoming []Finding) []Finding {
	if len(incoming) == 0 {
		return existing
	}
	return append(existing, incoming...)
}
