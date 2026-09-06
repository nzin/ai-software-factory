package coordinator

import (
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

		opts := SubmitOptions{IterationBudget: int(p.Body.IterationBudget)}
		if p.Body.DeadlineSeconds > 0 {
			opts.Deadline = time.Duration(p.Body.DeadlineSeconds) * time.Second
		}

		run, err := o.Submit(p.HTTPRequest.Context(), doc, opts)
		if err != nil {
			return bad(err.Error())
		}
		return runs.NewSubmitPRDOK().WithPayload(runToAPI(run))
	})
}

// runToAPI converts a Run to its generated model.
func runToAPI(r *Run) *models.Run {
	out := &models.Run{
		ID:                  swag.String(r.ID),
		ContextID:           r.ContextID,
		Status:              swag.String(string(r.Status)),
		Stage:               swag.String(r.Stage),
		Reason:              r.Reason,
		IterationsRemaining: int64(r.Budget.IterationsRemaining),
		Plan:                r.Plan,
		CreatedAt:           strfmt.DateTime(r.CreatedAt),
		UpdatedAt:           strfmt.DateTime(r.UpdatedAt),
	}
	if !r.Budget.Deadline.IsZero() {
		out.Deadline = strfmt.DateTime(r.Budget.Deadline)
	}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, &models.Finding{Source: f.Source, TargetRole: f.TargetRole, Note: f.Note})
	}
	for _, t := range r.Tasks {
		out.Tasks = append(out.Tasks, &models.Task{Role: t.Role, TaskID: t.TaskID, State: t.State, Output: t.Output})
	}
	return out
}
