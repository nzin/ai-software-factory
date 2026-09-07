package coordinator

import (
	"context"
	"time"

	"github.com/go-openapi/runtime/middleware"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"

	"github.com/nzin/ai-software-factory/internal/coordinator/gen/models"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/health"
	"github.com/nzin/ai-software-factory/internal/coordinator/gen/restapi/operations/runs"
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
}

func runSummaryToAPI(r *Run) *models.RunSummary {
	return &models.RunSummary{
		ID:                  r.ID,
		Status:              string(r.Status),
		Stage:               r.Stage,
		Title:               r.PRD.Title,
		RepoURL:             r.RepoURL,
		PrURL:               r.PRURL,
		IterationsRemaining: int64(r.Budget.IterationsRemaining),
		CreatedAt:           strfmt.DateTime(r.CreatedAt),
		UpdatedAt:           strfmt.DateTime(r.UpdatedAt),
	}
}

// runToAPI converts a Run to its generated model.
func runToAPI(r *Run) *models.Run {
	out := &models.Run{
		ID:                  swag.String(r.ID),
		ContextID:           r.ContextID,
		Status:              swag.String(string(r.Status)),
		Stage:               r.Stage,
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
		out.Findings = append(out.Findings, &models.Finding{
			Source: f.Source, Severity: f.Severity, Category: f.Category,
			File: f.File, Line: int64(f.Line), Title: f.Title,
			Suggestion: f.Suggestion, TargetRole: f.TargetRole,
		})
	}
	for _, t := range r.Tasks {
		out.Tasks = append(out.Tasks, &models.Task{
			Role: t.Role, TaskID: t.TaskID, State: t.State,
			Summary: t.Summary, CommitSha: t.CommitSHA, Output: t.Output,
		})
	}
	return out
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
