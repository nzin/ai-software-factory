package coordinator

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/go-openapi/runtime/middleware"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"

	"github.com/nzin/ai-software-factory/internal/agentkit"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/models"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations"
	agentsops "github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/agents"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/health"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/runs"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/webhooks"
	"github.com/nzin/ai-software-factory/internal/forge"
	"github.com/nzin/ai-software-factory/internal/modelext"
	"github.com/nzin/ai-software-factory/internal/prd"
)

// Setup wires the generated coordinator handlers to the orchestrator.
func Setup(api *operations.CoordinatorAPI, o *Orchestrator) {
	api.HealthHealthHandler = health.HealthHandlerFunc(func(health.HealthParams) middleware.Responder {
		return health.NewHealthOK().WithPayload(&models.Health{Status: swag.String("ok")})
	})

	api.RunsSubmitPRDHandler = runs.SubmitPRDHandlerFunc(func(p runs.SubmitPRDParams) middleware.Responder {
		bad := func(msg string) middleware.Responder {
			return runs.NewSubmitPRDDefault(400).WithPayload(&models.Error{Message: swag.String(msg)})
		}
		if p.Body == nil {
			return bad("empty body")
		}
		var doc prd.PRD
		switch {
		case p.Body.Prd != nil:
			doc = prd.PRD{
				Title:              swag.StringValue(p.Body.Prd.Title),
				Description:        p.Body.Prd.Description,
				AcceptanceCriteria: p.Body.Prd.AcceptanceCriteria,
			}
		case p.Body.Markdown != "":
			parsed, err := prd.ParseMarkdown(p.Body.Markdown)
			if err != nil {
				return bad(err.Error())
			}
			doc = parsed
		default:
			return bad("provide either prd or markdown")
		}

		opts := SubmitOptions{
			RepoURL:         p.Body.RepoURL,
			BaseBranch:      p.Body.BaseBranch,
			IterationBudget: int(p.Body.IterationBudget),
		}
		if p.Body.DeadlineSeconds > 0 {
			opts.Deadline = time.Duration(p.Body.DeadlineSeconds) * time.Second
		}

		run, err := o.Submit(context.WithoutCancel(p.HTTPRequest.Context()), doc, opts)
		if err != nil {
			return bad(err.Error())
		}
		return runs.NewSubmitPRDAccepted().WithPayload(runToAPI(run))
	})

	api.RunsListRunsHandler = runs.ListRunsHandlerFunc(func(runs.ListRunsParams) middleware.Responder {
		out := make([]*models.RunSummary, 0)
		for _, r := range o.List() {
			out = append(out, runSummaryToAPI(r))
		}
		return runs.NewListRunsOK().WithPayload(out)
	})

	api.RunsGetRunHandler = runs.GetRunHandlerFunc(func(p runs.GetRunParams) middleware.Responder {
		r, ok := o.Get(p.ID)
		if !ok {
			return runs.NewGetRunNotFound().WithPayload(&models.Error{Message: swag.String("no such run")})
		}
		return runs.NewGetRunOK().WithPayload(runToAPI(r))
	})

	api.RunsApproveRunHandler = runs.ApproveRunHandlerFunc(func(p runs.ApproveRunParams) middleware.Responder {
		r, err := o.Approve(p.ID)
		if err != nil {
			return runs.NewApproveRunDefault(409).WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		return runs.NewApproveRunOK().WithPayload(runToAPI(r))
	})

	api.RunsRejectRunHandler = runs.RejectRunHandlerFunc(func(p runs.RejectRunParams) middleware.Responder {
		var feedback string
		var abandon bool
		if p.Body != nil {
			feedback, abandon = p.Body.Feedback, p.Body.Abandon
		}
		r, err := o.Reject(p.ID, feedback, abandon)
		if err != nil {
			return runs.NewRejectRunDefault(409).WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		return runs.NewRejectRunOK().WithPayload(runToAPI(r))
	})

	api.RunsResumeRunHandler = runs.ResumeRunHandlerFunc(func(p runs.ResumeRunParams) middleware.Responder {
		var opts ResumeOptions
		if p.Body != nil {
			opts.IterationBudget = int(p.Body.IterationBudget)
			opts.Deadline = time.Duration(p.Body.DeadlineSeconds) * time.Second
			opts.Abandon = p.Body.Abandon
		}
		r, err := o.Resume(p.ID, opts)
		if err != nil {
			return runs.NewResumeRunDefault(409).WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		return runs.NewResumeRunOK().WithPayload(runToAPI(r))
	})

	api.RunsReviewRunHandler = runs.ReviewRunHandlerFunc(func(p runs.ReviewRunParams) middleware.Responder {
		if p.Body == nil {
			return runs.NewReviewRunDefault(400).WithPayload(&models.Error{Message: swag.String("empty body")})
		}
		comments := make([]ReviewComment, 0, len(p.Body.Comments))
		for _, c := range p.Body.Comments {
			if c == nil {
				continue
			}
			comments = append(comments, ReviewComment{
				Note:       swag.StringValue(c.Note),
				TargetRole: c.TargetRole,
				File:       c.File,
				Line:       int(c.Line),
			})
		}
		r, err := o.Review(p.ID, swag.StringValue(p.Body.Decision), comments)
		if err != nil {
			return runs.NewReviewRunDefault(409).WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		return runs.NewReviewRunOK().WithPayload(runToAPI(r))
	})

	api.WebhooksGithubWebhookHandler = webhooks.GithubWebhookHandlerFunc(func(p webhooks.GithubWebhookParams) middleware.Responder {
		errBody := func(msg string) *models.Error { return &models.Error{Message: swag.String(msg)} }

		secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
		if secret == "" {
			return webhooks.NewGithubWebhookServiceUnavailable().
				WithPayload(errBody("webhooks not configured (set GITHUB_WEBHOOK_SECRET)"))
		}
		raw, err := io.ReadAll(io.LimitReader(p.HTTPRequest.Body, 5<<20))
		if err != nil {
			return webhooks.NewGithubWebhookDefault(400).WithPayload(errBody("could not read body"))
		}
		if !forge.VerifySignature(secret, raw, p.HTTPRequest.Header.Get("X-Hub-Signature-256")) {
			return webhooks.NewGithubWebhookUnauthorized().WithPayload(errBody("bad or missing signature"))
		}

		event := p.HTTPRequest.Header.Get("X-GitHub-Event")
		review, ok, err := forge.ParsePRReview(event, raw)
		if err != nil {
			log.Printf("coordinator: webhook %s: %v", event, err)
			return webhooks.NewGithubWebhookAccepted()
		}
		if !ok {
			return webhooks.NewGithubWebhookAccepted() // ping, opened, empty comment, …
		}
		if r, err := o.IngestPRReview(*review); err != nil {
			log.Printf("coordinator: webhook %s for PR %s: %v", event, review.PRURL, err)
		} else {
			log.Printf("coordinator: webhook %s → run %s now %s", event, r.ID, r.Status)
		}
		return webhooks.NewGithubWebhookAccepted()
	})

	api.AgentsListAgentsHandler = agentsops.ListAgentsHandlerFunc(func(p agentsops.ListAgentsParams) middleware.Responder {
		cat := o.Catalog()
		if cat == nil {
			return agentsops.NewListAgentsDefault(503).
				WithPayload(&models.Error{Message: swag.String("no catalog configured")})
		}
		ctx, cancel := context.WithTimeout(p.HTTPRequest.Context(), catalogTimeout)
		defer cancel()

		infos, err := cat.ListAgents(ctx)
		if err != nil {
			return agentsops.NewListAgentsDefault(502).
				WithPayload(&models.Error{Message: swag.String(err.Error())})
		}

		// Fan out for the model config: listAgents on the catalog returns
		// registrations only, and the roster is small (7 today).
		out := make([]*models.AgentInfo, len(infos))
		var wg sync.WaitGroup
		for i, info := range infos {
			out[i] = agentInfoToAPI(info, nil)
			wg.Add(1)
			go func(i int, role string) {
				defer wg.Done()
				if d, err := cat.GetAgentDetail(ctx, role); err == nil {
					out[i].Model = modelConfigToAPI(d.Model)
				}
			}(i, info.Role)
		}
		wg.Wait()
		return agentsops.NewListAgentsOK().WithPayload(out)
	})

	api.AgentsGetAgentDetailHandler = agentsops.GetAgentDetailHandlerFunc(func(p agentsops.GetAgentDetailParams) middleware.Responder {
		cat := o.Catalog()
		if cat == nil {
			return agentsops.NewGetAgentDetailDefault(503).
				WithPayload(&models.Error{Message: swag.String("no catalog configured")})
		}
		ctx, cancel := context.WithTimeout(p.HTTPRequest.Context(), catalogTimeout)
		defer cancel()

		d, err := cat.GetAgentDetail(ctx, p.Role)
		if err != nil {
			return agentsops.NewGetAgentDetailNotFound().
				WithPayload(&models.Error{Message: swag.String(err.Error())})
		}
		var card any
		if d.Card != nil {
			card = d.Card
		}
		return agentsops.NewGetAgentDetailOK().WithPayload(&models.AgentDetail{
			Info:      agentInfoToAPI(d.Info, &d.Model),
			Model:     modelConfigToAPI(d.Model),
			AgentCard: card,
		})
	})
}

// catalogTimeout bounds the coordinator's read-through to the catalog.
const catalogTimeout = 5 * time.Second

func agentInfoToAPI(a agentkit.AgentInfo, m *modelext.Config) *models.AgentInfo {
	out := &models.AgentInfo{
		Role:        a.Role,
		Name:        a.Name,
		Description: a.Description,
		BaseURL:     a.BaseURL,
		Transport:   a.Transport,
		Skills:      a.Skills,
		Concurrency: int64(a.Concurrency),
		Enabled:     a.Enabled,
		CreatedAt:   strfmt.DateTime(a.CreatedAt),
		UpdatedAt:   strfmt.DateTime(a.UpdatedAt),
	}
	if m != nil {
		out.Model = modelConfigToAPI(*m)
	}
	return out
}

func modelConfigToAPI(c modelext.Config) *models.ModelConfig {
	return &models.ModelConfig{
		Provider:  c.Provider,
		Model:     c.Model,
		MaxTokens: c.MaxTokens,
		Effort:    c.Effort,
		Thinking:  c.Thinking,
		Params:    c.Params,
	}
}

func runSummaryToAPI(r *Run) *models.RunSummary {
	out := &models.RunSummary{
		ID:                  r.ID,
		Status:              string(r.Status),
		Stage:               r.Stage,
		Title:               r.PRD.Title,
		RepoURL:             r.RepoURL,
		RepoKind:            r.RepoKind,
		WorkBranch:          r.WorkBranch,
		PrURL:               r.PRURL,
		IterationsRemaining: int64(r.Budget.IterationsRemaining),
		CreatedAt:           strfmt.DateTime(r.CreatedAt),
		UpdatedAt:           strfmt.DateTime(r.UpdatedAt),
	}
	if !r.Budget.Deadline.IsZero() {
		out.Deadline = strfmt.DateTime(r.Budget.Deadline)
	}
	return out
}

// runToAPI converts a Run to its generated model.
func runToAPI(r *Run) *models.Run {
	out := &models.Run{
		ID:                  swag.String(r.ID),
		ContextID:           r.ContextID,
		Status:              swag.String(string(r.Status)),
		Stage:               r.Stage,
		LastStage:           r.LastStage,
		Reason:              r.Reason,
		RepoURL:             r.RepoURL,
		RepoKind:            r.RepoKind,
		BaseBranch:          r.BaseBranch,
		WorkBranch:          r.WorkBranch,
		WorkspaceDir:        r.WorkspaceDir,
		PrURL:               r.PRURL,
		IterationsRemaining: int64(r.Budget.IterationsRemaining),
		Plan:                r.Plan,
		UISpec:              r.UISpec,
		Attempts:            intMap(r.Attempts),
		CreatedAt:           strfmt.DateTime(r.CreatedAt),
		UpdatedAt:           strfmt.DateTime(r.UpdatedAt),
	}
	if !r.Budget.Deadline.IsZero() {
		out.Deadline = strfmt.DateTime(r.Budget.Deadline)
	}
	if r.Approval != nil {
		out.Approval = &models.ApprovalDecision{Required: r.Approval.Required, Reason: r.Approval.Reason}
	}
	for _, t := range r.PlanTasks {
		out.PlanTasks = append(out.PlanTasks, &models.PlanTask{ID: t.ID, Role: t.Role, Title: t.Title, Details: t.Details})
	}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, findingToAPI(f))
	}
	for _, t := range r.Tasks {
		task := &models.Task{
			Role: t.Role, TaskID: t.TaskID, State: t.State,
			Summary: t.Summary, CommitSha: t.CommitSHA, Output: t.Output,
			Attempt: int64(t.Attempt), DurationMs: t.DurationMs,
			Verdict: t.Verdict, FilesWritten: t.FilesWritten,
		}
		if !t.StartedAt.IsZero() {
			task.StartedAt = strfmt.DateTime(t.StartedAt)
		}
		for _, f := range t.Findings {
			task.Findings = append(task.Findings, findingToAPI(f))
		}
		out.Tasks = append(out.Tasks, task)
	}
	for _, e := range r.Events {
		out.Events = append(out.Events, &models.RunEvent{
			Seq: int64(e.Seq), At: strfmt.DateTime(e.At), Kind: e.Kind,
			Stage: e.Stage, Status: e.Status, Message: e.Message, Detail: e.Detail,
			Attempt: int64(e.Attempt), DurationMs: e.DurationMs, Actor: e.Actor,
		})
	}
	return out
}

func findingToAPI(f Finding) *models.Finding {
	return &models.Finding{
		Source: f.Source, Severity: f.Severity, Category: f.Category,
		File: f.File, Line: int64(f.Line), Title: f.Title,
		Suggestion: f.Suggestion, TargetRole: f.TargetRole,
	}
}

func intMap(m map[string]int) map[string]int64 {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = int64(v)
	}
	return out
}
