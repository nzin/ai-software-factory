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
	"path/filepath"
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
	defaultRepo     string // used when a submission leaves RepoURL empty
	pruneKeep       int

	mu      sync.Mutex
	driving map[string]bool // runs with a live drive goroutine
	pending map[string]bool // a start() arrived for this run while its drive goroutine was finishing — replay it
}

// Option configures an Orchestrator.
type Option func(*Orchestrator)

func WithEngine(e Engine) Option                { return func(o *Orchestrator) { o.engine = e } }
func WithWorkspace(m *workspace.Manager) Option { return func(o *Orchestrator) { o.workspace = m } }
func WithStore(s Store) Option                  { return func(o *Orchestrator) { o.store = s } }
func WithCatalog(c CatalogReader) Option        { return func(o *Orchestrator) { o.catalog = c } }
func WithMaxStageIterations(n int) Option       { return func(o *Orchestrator) { o.maxStageIter = n } }
func WithBaseBranch(b string) Option            { return func(o *Orchestrator) { o.baseBranch = b } }

// WithDefaultRepo sets the repo a submission targets when its RepoURL is empty
// (instead of scaffolding a throwaway repo). Used for the local persistent
// workspace, where empty-repoURL runs branch off a shared repo.
func WithDefaultRepo(url string) Option { return func(o *Orchestrator) { o.defaultRepo = url } }

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
		pending:         make(map[string]bool),
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
	Attachments     []AttachmentInput
}

// AttachmentInput is a raw evidence image (e.g. a bug screenshot) submitted
// alongside a PRD. It is written into the run's workspace once, at submission
// time; only the resulting prd.Attachment reference is kept afterward.
type AttachmentInput struct {
	Filename  string
	MediaType string
	Data      []byte
}

// attachmentPathPrefix is where evidence images submitted with a PRD are
// written in the run's workspace, mirroring test/e2e/screenshots/ for
// build-gate screenshots.
const attachmentPathPrefix = "docs/prd/attachments/"

// AttachmentMediaTypes are the media types a PRD attachment may use — the
// same raster-only restriction llm.ImageInput vision input requires.
var AttachmentMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// Limits on evidence images submitted with a PRD.
const (
	MaxAttachmentBytes = 8 << 20 // 8MB
	MaxAttachments     = 10
)

// attachAttachments writes the submitted evidence images into repo's
// workspace and commits them, returning the persisted references in
// submission order.
func attachAttachments(ctx context.Context, repo *workspace.Repo, attachments []AttachmentInput) ([]prd.Attachment, error) {
	refs := make([]prd.Attachment, 0, len(attachments))
	files := make(map[string]string, len(attachments))
	for i, a := range attachments {
		path := attachmentPathPrefix + fmt.Sprintf("%03d-%s", i+1, sanitizeAttachmentFilename(a.Filename, a.MediaType, i+1))
		files[path] = string(a.Data)
		refs = append(refs, prd.Attachment{Path: path, Filename: a.Filename, MediaType: a.MediaType})
	}
	written, err := repo.WriteFiles(files)
	if err != nil {
		return nil, fmt.Errorf("coordinator: write attachments: %w", err)
	}
	if err := repo.AddPaths(ctx, written); err != nil {
		return nil, fmt.Errorf("coordinator: stage attachments: %w", err)
	}
	if _, err := repo.Commit(ctx, "prd", fmt.Sprintf("attach %d screenshot(s) as evidence", len(attachments))); err != nil {
		return nil, fmt.Errorf("coordinator: commit attachments: %w", err)
	}
	return refs, nil
}

// MediaTypeForExt returns the media type for a raster image file extension
// (with leading dot, e.g. ".png", case-insensitive), and whether it's one of
// AttachmentMediaTypes.
func MediaTypeForExt(ext string) (string, bool) {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png", true
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".gif":
		return "image/gif", true
	case ".webp":
		return "image/webp", true
	default:
		return "", false
	}
}

// sanitizeAttachmentFilename keeps only a safe charset from name, falling
// back to a generic name derived from mediaType (and idx, to stay unique)
// when nothing safe remains.
func sanitizeAttachmentFilename(name, mediaType string, idx int) string {
	base := filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	if clean := strings.Trim(b.String(), "."); clean != "" {
		return clean
	}
	ext := "png"
	if parts := strings.SplitN(mediaType, "/", 2); len(parts) == 2 && parts[1] != "" {
		ext = parts[1]
	}
	return fmt.Sprintf("attachment-%d.%s", idx, ext)
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
	repoURL := opts.RepoURL
	if repoURL == "" {
		repoURL = o.defaultRepo
	}

	run := &Run{
		ID:         uuid.NewString(),
		ContextID:  a2a.NewContextID(),
		PRD:        p,
		Status:     StatusRunning,
		Stage:      planner.Role,
		Budget:     Budget{IterationsRemaining: iters, Deadline: deadline},
		RepoURL:    repoURL,
		BaseBranch: base,
		Attempts:   map[string]int{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if o.workspace != nil {
		ref := workspace.ParseRepoURL(repoURL, base)
		repo, err := o.workspace.Prepare(context.Background(), run.ID, ref)
		if err != nil {
			return nil, fmt.Errorf("coordinator: prepare workspace: %w", err)
		}
		run.WorkspaceDir = repo.Dir
		run.WorkBranch = repo.WorkBranch
		run.RepoKind = string(repo.Kind)

		if len(opts.Attachments) > 0 {
			refs, err := attachAttachments(context.Background(), repo, opts.Attachments)
			if err != nil {
				return nil, err
			}
			run.PRD.Attachments = refs
		}
	} else if len(opts.Attachments) > 0 {
		return nil, fmt.Errorf("coordinator: attachments require a workspace")
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
	// Keep run.Plan as context for the revision; plannerInput folds it and the
	// feedback into the next planner prompt. PlanFeedback is consumed by
	// applyPlan once the planner reruns.
	run.PlanFeedback = feedback
	run.Status = StatusRunning
	run.Stage = planner.Role
	run.PlanTasks, run.Approval = nil, nil
	run.UpdatedAt = time.Now().UTC()
	o.event(run, EventRejected, run.Stage, "plan sent back for revision by a human").
		By(ActorHuman).WithDetail(feedback)
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// ResumeOptions are the knobs on un-sticking a needs_human_review or failed run.
type ResumeOptions struct {
	IterationBudget int           // extra iterations to grant (0 = a default top-up)
	Deadline        time.Duration // extra wall-clock to grant (0 = a default top-up)
	Abandon         bool
	// Accept takes the run's branch as-is (push / open PR) and stops, skipping
	// the remaining pipeline stages.
	Accept bool
	// Comment is human guidance: recorded as a high-severity finding and routed
	// to the responsible developer for a fix pass on resume.
	Comment    string
	TargetRole string // developer the comment routes to; inferred when empty
}

// Resume restarts a run parked in needs_human_review or failed, granting fresh
// budget. With a Comment the note steers a developer fix pass; with Accept the
// branch is taken as-is.
func (o *Orchestrator) Resume(ctx context.Context, id string, opts ResumeOptions) (*Run, error) {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("coordinator: no run %s", id)
	}
	if run.Status != StatusNeedsHumanReview && run.Status != StatusFailed {
		return run, fmt.Errorf("coordinator: run %s is %s, not needs_human_review or failed", id, run.Status)
	}
	if opts.Abandon {
		o.event(run, EventRejected, run.LastStage, "abandoned by a human").By(ActorHuman)
		o.stop(run, StatusFailed, "abandoned by human")
		if r, ok, _ := o.store.Get(run.ID); ok {
			return r, nil
		}
		return run, nil
	}
	if opts.Accept {
		if run.WorkspaceDir == "" {
			o.event(run, EventReviewAccepted, run.LastStage, "parked run accepted as-is by a human").By(ActorHuman)
			o.finish(ctx, run)
			if r, ok, _ := o.store.Get(run.ID); ok {
				return r, nil
			}
			return run, nil
		}
		repo, err := workspace.Open(ctx, run.WorkspaceDir)
		if err != nil {
			return run, fmt.Errorf("coordinator: cannot accept run %s: %w", id, err)
		}
		repo.BaseBranch = run.BaseBranch
		if !repo.HasCommits(ctx) {
			return run, fmt.Errorf("coordinator: nothing to accept — run %s produced no commits", id)
		}
		o.event(run, EventReviewAccepted, run.LastStage, "parked run accepted as-is by a human").By(ActorHuman)
		o.finish(ctx, run)
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

	comment := strings.TrimSpace(opts.Comment)
	switch {
	case comment != "" && len(run.PlanTasks) > 0:
		// Steer a developer fix pass with the note, exactly like Review's
		// request_changes path: a human finding, routed, with Attempt>0 so the
		// dispatch carries it into the "# Fix pass" prompt block.
		f := Finding{Source: "human", Severity: "high", Title: comment, TargetRole: opts.TargetRole}
		run.Findings = append(run.Findings, f)
		run.LastRoundFindings = []Finding{f}
		target := factory.RouteRole([]Finding{f}, run.lastDeveloper())
		run.Attempts[target] = 1
		run.Stage = target
		o.event(run, EventResumed, run.Stage,
			"resumed by a human with a comment → %s (attempt 1, +%d iterations)", target, grant).
			By(ActorHuman).WithDetail(comment)
	case comment != "":
		// No plan yet (a planner-stage failure): the note goes on the PRD the
		// way Reject does, and the run replans.
		run.PRD.Description += "\n\n## Operator note\n" + comment
		run.Stage = planner.Role
		o.event(run, EventResumed, run.Stage,
			"resumed by a human with a comment — replanning (+%d iterations)", grant).
			By(ActorHuman).WithDetail(comment)
	default:
		run.Stage = run.LastStage
		if run.Stage == "" {
			run.Stage = firstDevelopmentStage(run)
		}
		if run.Stage == "" {
			run.Stage = planner.Role
		}
		o.event(run, EventResumed, run.Stage,
			"resumed by a human at %s (+%d iterations)", run.Stage, grant).By(ActorHuman)
	}

	run.Status = StatusRunning
	run.Reason = ""
	run.UpdatedAt = time.Now().UTC()
	_ = o.store.Put(run)
	o.start(run)
	if r, ok, _ := o.store.Get(run.ID); ok {
		return r, nil
	}
	return run, nil
}

// Delete removes a terminal run and its workspace. It refuses a run that is
// still queued/running/awaiting_approval or has a live drive goroutine.
func (o *Orchestrator) Delete(ctx context.Context, id string) error {
	run, ok, _ := o.store.Get(id)
	if !ok {
		return fmt.Errorf("coordinator: no run %s", id)
	}
	if !run.Status.Terminal() {
		return fmt.Errorf("coordinator: run %s is %s, not a terminal state", id, run.Status)
	}
	o.mu.Lock()
	driving := o.driving[id]
	o.mu.Unlock()
	if driving {
		return fmt.Errorf("coordinator: run %s is still running", id)
	}

	if o.workspace != nil && run.WorkspaceDir != "" {
		_ = o.workspace.Remove(ctx, run.ID, nil) // best-effort; may already be pruned
	}
	return o.store.Delete(id)
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
// (re-)pushes the work branch to origin, opens the PR when possible, and is
// terminal; requesting changes turns each comment into a human Finding and
// sends the run back through the factory.
func (o *Orchestrator) Review(ctx context.Context, id, decision string, comments []ReviewComment) (*Run, error) {
	return o.review(ctx, id, decision, comments, "")
}

// review is Review's implementation. via, when non-empty, is folded into the
// request_changes event's detail inside the same critical section that
// persists the run and launches start() — not applied afterward by reading
// Review's return value, mutating it, and Put-ing it back (which is how
// IngestPRReview used to attribute a review to the GitHub webhook). That
// trailing Put raced the drive() goroutine start() had just launched: it
// reads a snapshot taken essentially the instant start() returns, so on a
// fast (or fake, in tests) pipeline that goroutine can run to completion and
// clear its own bookkeeping before the trailing Put executes — which then
// clobbers the finished run back to that stale pre-drive snapshot, stranding
// it with no goroutine left to drive it further.
func (o *Orchestrator) review(ctx context.Context, id, decision string, comments []ReviewComment, via string) (*Run, error) {
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
		// The branch was normally pushed when the run first reached pr_ready, but
		// that push may have failed (unreachable origin) — retry it here so an
		// accepted run always lands on origin. Best-effort: a failure is surfaced
		// in the reason, not blocking the accept.
		reason := "accepted by human review"
		if err := o.deliver(ctx, run); err != nil {
			reason = "accepted by human review (push failed: " + err.Error() + ")"
		} else if run.PRURL != "" {
			reason = "accepted by human review — " + run.PRURL
		} else if run.RepoKind == string(workspace.KindLocal) || run.RepoKind == string(workspace.KindRemote) {
			reason = "accepted by human review — branch " + run.WorkBranch + " pushed to origin"
		}
		o.stop(run, StatusAccepted, reason)
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
		run.LastRoundFindings = fresh

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
		detail := renderComments(fresh)
		if via != "" {
			detail = via + "\n\n" + detail
		}
		o.event(run, EventReviewChanges, target,
			"human requested changes (%d comments) → back to %s (attempt %d)",
			len(fresh), target, run.Attempts[target]).
			By(ActorHuman).WithDetail(detail)
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

// IngestPRReview matches a GitHub PR review event to a run and applies it through
// the existing Review path. It never fails loudly: an unmatched or stale event
// returns an error the webhook handler logs and answers 202, so GitHub does not
// keep retrying.
func (o *Orchestrator) IngestPRReview(ctx context.Context, r forge.PRReview) (*Run, error) {
	run := o.matchPR(r)
	if run == nil {
		return nil, fmt.Errorf("coordinator: no run matches PR %s (branch %q)", r.PRURL, r.HeadRef)
	}
	if run.Status != StatusPRReady && run.Status != StatusPROpen {
		return run, fmt.Errorf("coordinator: run %s is %s — ignoring PR review", run.ID, run.Status)
	}

	if r.State == "approved" {
		res, err := o.Review(ctx, run.ID, ReviewAccept, nil)
		if err == nil {
			o.event(res, EventReviewAccepted, res.LastStage,
				"PR approved on GitHub by %s", orDash(r.Author)).By(ActorHuman)
			_ = o.store.Put(res)
		}
		return res, err
	}

	comments := make([]ReviewComment, 0, len(r.Comments))
	for _, c := range r.Comments {
		comments = append(comments, ReviewComment{Note: c.Body, File: c.Path, Line: c.Line})
	}
	if len(comments) == 0 {
		o.event(run, EventPRComment, run.LastStage,
			"comment on the PR by %s — no change requested", orDash(r.Author)).By(ActorHuman)
		_ = o.store.Put(run)
		return run, nil
	}
	return o.review(ctx, run.ID, ReviewRequestChanges, comments, "via GitHub PR review by "+orDash(r.Author))
}

// matchPR finds the run this PR event belongs to: exact PR URL first, then the
// work branch scoped to the same GitHub repo.
func (o *Orchestrator) matchPR(r forge.PRReview) *Run {
	runs, _ := o.store.All()
	if r.PRURL != "" {
		for _, run := range runs {
			if run.PRURL == r.PRURL {
				return run
			}
		}
	}
	if r.HeadRef != "" && r.RepoFullName != "" {
		for _, run := range runs {
			if run.WorkBranch == r.HeadRef && strings.Contains(run.RepoURL, r.RepoFullName) {
				return run
			}
		}
	}
	return nil
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

// start launches drive() in the background unless one is already running, in
// which case the request is coalesced rather than dropped: the in-flight
// goroutine notices and replays drive() once it finishes.
//
// This matters because a caller (Review/Approve/Reject/Resume) reads a run
// from the store, mutates it, persists it, and calls start() — but the
// *previous* drive() goroutine for that run may have already made that exact
// persisted state visible (e.g. drive's stop() calls store.Put with a terminal
// status) and merely be in the middle of returning, with driving[id] not yet
// cleared. A plain "no-op if driving[id]" guard would silently strand the run
// in that window; coalescing via pending[id] closes it.
func (o *Orchestrator) start(run *Run) {
	o.mu.Lock()
	if o.driving[run.ID] {
		o.pending[run.ID] = true
		o.mu.Unlock()
		return
	}
	o.driving[run.ID] = true
	o.mu.Unlock()

	go o.driveLoop(run)
}

// driveLoop runs drive() and, if a start() request was coalesced while it ran,
// re-fetches the run's latest persisted state (the coalesced caller's own
// mutations live only in *their* copy, since the store hands out independent
// snapshots) and drives it again — repeating until a pass finishes with no new
// request pending.
func (o *Orchestrator) driveLoop(run *Run) {
	for {
		o.drive(context.Background(), run)

		o.mu.Lock()
		if !o.pending[run.ID] {
			delete(o.driving, run.ID)
			o.mu.Unlock()
			return
		}
		delete(o.pending, run.ID)
		o.mu.Unlock()

		r, ok, _ := o.store.Get(run.ID)
		if !ok {
			o.mu.Lock()
			delete(o.driving, run.ID)
			o.mu.Unlock()
			return
		}
		run = r
	}
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
		run.LastRoundFindings = res.Findings
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

		case (isReviewer(stage) || isGate(stage)) && res.Verdict == factory.VerdictRequestChanges:
			target := res.TargetRole
			if !factory.IsDeveloperRole(target) {
				target = factory.RouteRole(res.Findings, run.lastDeveloper())
			}
			run.Attempts[target]++
			if run.Attempts[target] > o.maxStageIter {
				o.event(run, EventAttemptCap, target,
					"%s still failing after %d fix attempts", target, o.maxStageIter)
				o.stop(run, StatusNeedsHumanReview,
					fmt.Sprintf("%s still failing after %d fix attempts", target, o.maxStageIter))
				return
			}
			kind, verb := EventRequestChanges, "requested changes"
			if isGate(stage) {
				kind, verb = EventBuildFailed, "the build failed"
			}
			o.event(run, kind, target,
				"%s: %s → back to %s (attempt %d)", stage, verb, target, run.Attempts[target]).
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
	run.PlanFeedback = "" // consumed: the planner has produced a fresh plan
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
	case string(workspace.KindRemote), string(workspace.KindLocal):
		// A run coming back from human review already has a PR; deliver() then
		// only re-pushes (re-opening would 422 and silently demote to pr_ready).
		hadPR := run.PRURL != ""
		if err := o.deliver(ctx, run); err != nil {
			o.stop(run, StatusPRReady, "commits ready on "+run.WorkBranch+" (push failed: "+err.Error()+")")
			return
		}
		switch {
		case hadPR:
			o.stop(run, StatusPROpen, "PR updated: "+run.PRURL)
		case run.PRURL != "":
			o.stop(run, StatusPROpen, "PR opened: "+run.PRURL)
		case run.RepoKind == string(workspace.KindLocal):
			o.stop(run, StatusPRReady, fmt.Sprintf(
				"branch %s pushed to %s — `git checkout %s`", run.WorkBranch, run.RepoURL, run.WorkBranch))
		default:
			o.stop(run, StatusPRReady, "branch "+run.WorkBranch+" pushed to origin")
		}

	default: // new
		o.stop(run, StatusPRReady, "branch "+run.WorkBranch+" in "+run.WorkspaceDir)
	}

	if o.workspace != nil {
		_ = o.workspace.Prune(o.pruneKeep)
	}
}

// deliver pushes the run's work branch to its origin and, for a GitHub remote
// with a token, opens a PR (recording pushed / pr_opened events and run.PRURL).
// It leaves the run's status to the caller. A missing workspace, a commit-less
// branch, or a "new" repo (no origin) is a no-op. A push failure is returned.
//
// git push is idempotent — a branch already on origin exits "Everything
// up-to-date" — so this is safe to call again from the human-accept path.
func (o *Orchestrator) deliver(ctx context.Context, run *Run) error {
	if run.WorkspaceDir == "" {
		return nil
	}
	if run.RepoKind != string(workspace.KindRemote) && run.RepoKind != string(workspace.KindLocal) {
		return nil
	}
	repo, err := workspace.Open(ctx, run.WorkspaceDir)
	if err != nil {
		return fmt.Errorf("workspace unavailable: %w", err)
	}
	repo.BaseBranch = run.BaseBranch
	if !repo.HasCommits(ctx) {
		return nil
	}
	if err := repo.Push(ctx); err != nil {
		return err
	}
	o.event(run, EventPushed, "", "branch %s pushed to %s", run.WorkBranch, run.RepoURL)

	// OpenPR is a no-op ("", nil) for a file:// / non-GitHub origin, so the
	// local-repo case simply keeps run.PRURL empty.
	if run.PRURL == "" {
		if url, _ := forge.OpenPR(ctx, run.RepoURL, run.BaseBranch, run.WorkBranch, run.PRD.Title, run.Plan); url != "" {
			run.PRURL = url
			o.event(run, EventPROpened, "", "pull request opened: %s", url)
		}
	}
	return nil
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

// mergeFindings folds incoming into the run's whole-history findings list,
// collapsing a recurring finding (the same underlying problem raised again
// next round because the fix pass didn't resolve it) into a single entry
// updated in place, rather than appending a lookalike duplicate every round.
func mergeFindings(existing, incoming []Finding) []Finding {
	for _, f := range incoming {
		if i := recurrenceIndex(existing, f); i >= 0 {
			existing[i] = f // same problem, newer wording — keep the latest
			continue
		}
		existing = append(existing, f)
	}
	return existing
}

// recurrenceIndex returns the index in existing of the same underlying
// problem as f, or -1 if none matches. The match key is source + target role
// + file + verbatim evidence: evidence is the raw tool/command output a
// finding was derived from, so it stays byte-identical across rounds when
// the same failure recurs — unlike Title/Suggestion, which are an LLM
// paraphrase that reliably drifts in wording each time it's regenerated, and
// so can't be used to recognise a repeat. A finding with no evidence is never
// matched: without it there's no reliable signal that two findings are the
// same problem rather than two distinct ones that happen to share a file.
func recurrenceIndex(existing []Finding, f Finding) int {
	if f.Evidence == "" {
		return -1
	}
	for i, e := range existing {
		if e.Source == f.Source && e.TargetRole == f.TargetRole &&
			e.File == f.File && e.Evidence == f.Evidence {
			return i
		}
	}
	return -1
}
