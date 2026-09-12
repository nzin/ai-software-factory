package coordinator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/agents/planner"
	"github.com/nzin/ai-software-factory/internal/factory"
	"github.com/nzin/ai-software-factory/internal/workspace"
)

// dispatchTimeout bounds one agent call. A developer dispatch now runs the
// model once per planner task (one commit each), so a whole role's work happens
// under this single call — several sequential model calls, each itself bounded
// by llm.callTimeout (30m). The run-level Budget.Deadline is the real cap.
const dispatchTimeout = 90 * time.Minute

// pipelineEngine dispatches stages to real A2A agents.
type pipelineEngine struct {
	catalog *agentkit.CatalogClient
}

func (e *pipelineEngine) Run(ctx context.Context, run *Run) (StageResult, error) {
	switch {
	case run.Stage == planner.Role:
		out, err := e.dispatchPlanner(ctx, run, e.plannerInput(ctx, run))
		if err != nil {
			return StageResult{}, err
		}
		doc := factory.ParsePlan(out)
		return StageResult{
			Task: Task{Role: planner.Role, State: "completed",
				Summary: fmt.Sprintf("%d tasks, approval=%v", len(doc.Tasks), doc.Approval.Required),
				Output:  doc.Prose},
			Plan: &doc,
		}, nil

	case run.Stage == factory.RoleUIUXDesigner:
		res, err := e.dispatchEnvelope(ctx, run)
		if err != nil {
			return StageResult{}, err
		}
		run.UISpec = res.Summary
		return StageResult{Task: Task{Role: run.Stage, State: "completed", Summary: "UI/UX spec produced"}}, nil

	case factory.IsDeveloperRole(run.Stage) || run.Stage == factory.RoleTestEngineer:
		res, err := e.dispatchEnvelope(ctx, run)
		if err != nil {
			return StageResult{}, err
		}
		return StageResult{
			Task: Task{
				Role: run.Stage, State: "completed",
				Summary: res.Summary, CommitSHA: res.CommitSHA,
				FilesWritten: res.FilesWritten,
			},
			Findings: res.Findings,
		}, nil

	case isReviewer(run.Stage) || isGate(run.Stage):
		res, err := e.dispatchEnvelope(ctx, run)
		if err != nil {
			return StageResult{}, err
		}
		return StageResult{
			Task: Task{
				Role: run.Stage, State: "completed", Summary: res.Summary, Verdict: res.Verdict,
				CommitSHA: res.CommitSHA, FilesWritten: res.FilesWritten,
			},
			Findings:   res.Findings,
			Verdict:    res.Verdict,
			TargetRole: res.TargetRole,
		}, nil

	default:
		return StageResult{}, fmt.Errorf("coordinator: unknown stage %q", run.Stage)
	}
}

// plannerInput is the PRD text plus a repository snapshot so the plan is
// grounded. On a human-requested revision it also carries the previous plan and
// the requested changes.
func (e *pipelineEngine) plannerInput(ctx context.Context, run *Run) string {
	in := run.PRD.Text()
	if fb := strings.TrimSpace(run.PlanFeedback); fb != "" {
		var b strings.Builder
		b.WriteString("# Revise the previous plan\n\nA human reviewed your last plan and asked for changes. Produce an updated plan that addresses this feedback; keep the parts that were already fine.\n\n")
		if strings.TrimSpace(run.Plan) != "" {
			b.WriteString("## Your previous plan\n\n" + run.Plan + "\n\n")
		}
		b.WriteString("## Requested changes\n\n" + fb + "\n\n")
		in = b.String() + in
	}
	if run.WorkspaceDir == "" {
		return in
	}
	repo, err := workspace.Open(ctx, run.WorkspaceDir)
	if err != nil {
		return in
	}
	repo.BaseBranch = run.BaseBranch
	s := repo.Summarize(ctx, 200)
	var b string
	switch run.RepoKind {
	case string(workspace.KindNew):
		b = "# Repository\n\nThis is a **brand-new empty repository**. Your first task MUST scaffold it (module/manifest, README, .gitignore, minimal CI, a Dockerfile for the primary service, and a root docker-compose.yml).\n"
	default:
		b = fmt.Sprintf("# Repository (existing, branch %s)\n\nFiles:\n%s\n", run.BaseBranch, bullet(s.Tree))
		if s.ReadmeHead != "" {
			b += "\nREADME:\n" + s.ReadmeHead + "\n"
		}
	}
	return b + "\n" + in
}

func bullet(xs []string) string {
	out := ""
	for _, x := range xs {
		out += "- " + x + "\n"
	}
	return out
}

// --- A2A dispatch ---

func (e *pipelineEngine) client(ctx context.Context, role string) (*a2aclient.Client, error) {
	if e.catalog == nil {
		return nil, fmt.Errorf("coordinator: no catalog configured")
	}
	baseURL, err := e.catalog.GetAgentBaseURL(ctx, role)
	if err != nil {
		return nil, err
	}
	card, err := agentcard.DefaultResolver.Resolve(ctx, baseURL)
	if err != nil {
		return nil, fmt.Errorf("coordinator: resolve %s card: %w", role, err)
	}
	hc := &http.Client{Timeout: dispatchTimeout}
	cl, err := a2aclient.NewFromCard(ctx, card,
		a2aclient.WithJSONRPCTransport(hc),
		a2aclient.WithRESTTransport(hc),
	)
	if err != nil {
		return nil, fmt.Errorf("coordinator: a2a client for %s: %w", role, err)
	}
	return cl, nil
}

// dispatchPlanner sends the planner its prompt plus the run's workspace dir
// (as a DataPart, reusing factory.DispatchEnvelope purely as a convenient
// carrier) so the planner's executor can look at any PRD evidence images
// committed under docs/prd/attachments/. Unlike dispatchEnvelope, the
// planner's reply is plain prose (parsed by factory.ParsePlan), not a
// structured ResultEnvelope, so the response side is handled the same way
// dispatchText's used to be.
func (e *pipelineEngine) dispatchPlanner(ctx context.Context, run *Run, text string) (string, error) {
	cl, err := e.client(ctx, planner.Role)
	if err != nil {
		return "", err
	}
	env := factory.DispatchEnvelope{WorkspaceDir: run.WorkspaceDir, PRDText: text}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(env))
	msg.ContextID = run.ContextID
	res, err := cl.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		return "", fmt.Errorf("coordinator: send to %s: %w", planner.Role, err)
	}
	return resultText(res), nil
}

func (e *pipelineEngine) dispatchEnvelope(ctx context.Context, run *Run) (factory.ResultEnvelope, error) {
	role := run.Stage
	cl, err := e.client(ctx, role)
	if err != nil {
		return factory.ResultEnvelope{}, err
	}
	env := factory.DispatchEnvelope{
		RunID:        run.ID,
		Stage:        role,
		WorkspaceDir: run.WorkspaceDir,
		BaseBranch:   run.BaseBranch,
		WorkBranch:   run.WorkBranch,
		RepoURL:      run.RepoURL,
		RepoKind:     run.RepoKind,
		PRDText:      run.PRD.Text(),
		Plan:         run.Plan,
		UISpec:       run.UISpec,
		Tasks:        factory.TasksForRole(run.PlanTasks, role),
		Attempt:      run.Attempts[role],
	}
	if factory.IsDeveloperRole(role) && env.Attempt > 0 {
		env.Findings = findingsForRole(run.LastRoundFindings, role)
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(env))
	msg.ContextID = run.ContextID

	res, err := cl.SendMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		return factory.ResultEnvelope{}, fmt.Errorf("coordinator: send to %s: %w", role, err)
	}
	return resultEnvelope(res, role)
}

// findingsForRole returns the findings a fix pass for role should address:
// those explicitly targeting it, plus any untargeted high/critical ones. The
// caller passes only the triggering round's findings (run.LastRoundFindings),
// not the run's whole history — a retry should see what just failed, not
// every finding raised across every earlier round too, some of which may
// already be resolved.
func findingsForRole(all []Finding, role string) []Finding {
	role = factory.NormalizeRole(role)
	var out []Finding
	for _, f := range all {
		// Findings are commonly targeted with a role shorthand ("frontend",
		// "backend") rather than the full stage name ("frontend-developer")
		// that `role` here always is — normalize both before comparing, or a
		// legitimately-targeted finding falls through to the "not a developer
		// role, so untargeted" branch below and gets handed to every role.
		t := factory.NormalizeRole(f.TargetRole)
		if t == role || t == "" || !factory.IsDeveloperRole(t) {
			out = append(out, f)
		}
	}
	return out
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

func resultEnvelope(res a2a.SendMessageResult, role string) (factory.ResultEnvelope, error) {
	var parts a2a.ContentParts
	switch v := res.(type) {
	case *a2a.Message:
		parts = v.Parts
	case *a2a.Task:
		if v.Status.Message != nil {
			parts = v.Status.Message.Parts
		} else {
			for i := len(v.History) - 1; i >= 0; i-- {
				if v.History[i].Role == a2a.MessageRoleAgent {
					parts = v.History[i].Parts
					break
				}
			}
		}
	}
	for _, p := range parts {
		if p == nil {
			continue
		}
		if d := p.Data(); d != nil {
			return factory.Decode[factory.ResultEnvelope](d)
		}
	}
	if t := partsText(parts); t != "" {
		return factory.ResultEnvelope{}, fmt.Errorf("coordinator: %s returned text, not a result envelope: %s", role, t)
	}
	return factory.ResultEnvelope{}, fmt.Errorf("coordinator: %s returned no result envelope", role)
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
