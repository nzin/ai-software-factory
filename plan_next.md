# AI Software Factory — remaining phases

Phase 1 (done) delivered the catalog service, the `modelext` A2A extension, the
shared `agentkit`, the planner agent (real Claude call), and a coordinator that
runs a PRD to a plan with a TTL/budget guard.

---

## Phase 2 — more specialized agents

- [ ] `ui-ux-designer`, `backend-developer`, `frontend-developer`,
      `mobile-developer`, `security-reviewer`, `code-reviewer`.
- [ ] Each is `agentkit.Bootstrap` + `agentkit.LLMExecutor(client, prompt)` under
      its own `Role`, with a `cmd/agent-<role>` binary — same shape as
      `internal/agents/planner`.
- [ ] Add per-role defaults to `modelext.Defaults`.
- [ ] Reviewers return a **structured verdict** (`approve` |
      `request_changes{targetRole, findings[]}`) — define the wire format
      (an `a2a` `DataPart` JSON payload) so the coordinator can route on it.

## Phase 3 — feedback-loop orchestrator (state machine, not a DAG)

- [ ] Coordinator drives the `Run` through a default order: planner → designer →
      {backend, frontend, mobile} → security-reviewer → code-reviewer.
- [ ] On `request_changes`, route the `Run` back to `targetRole` (developer for
      implementation bugs, planner for design flaws), re-run the affected
      reviewers, repeat.
- [ ] Every dispatch decrements `Budget.IterationsRemaining` and is checked
      against `Budget.Deadline` (already enforced in Phase 1); add a per-stage
      `maxIterations` local guard. Budget exhaustion → `needs_human_review`.
- [ ] Pass artifacts between stages as `a2a.Artifact` / `DataPart`; track each
      dispatch as an `a2a.Task` with real state.
- [ ] Move `Run` state into a store (`internal/coordinator/runstore`, GORM/SQLite,
      same pattern as `internal/catalog`) with a worker pool, at-least-once
      dispatch, resume-after-crash.
- [ ] Make `POST /v1/prd` asynchronous; add `GET /v1/runs` and `GET /v1/runs/{id}`.
- [ ] Multi-replica agents via `a2a-go` cluster mode
      (`a2asrv.WithClusterMode`, see `examples/clustermode`).

## Phase 4 — VueJS web UI (served statically by the coordinator)

- [ ] Vue 3 + `vue-router` + `axios` + Element Plus, scaffolded under
      `browser/asf-ui/` (like `github.com/nzin/golang-skeleton` step5).
- [ ] `make build_ui` → `browser/asf-ui/dist/`; the coordinator's
      `configure_coordinator.go` `setupGlobalMiddleware` serves it
      (`negroni.Static{Dir: http.Dir("browser/asf-ui/dist"), IndexFile:
      "index.html"}` with SPA fallback, or `//go:embed`). `/v1/**` and
      `/healthz` continue to the generated `restapi`.
- [ ] New coordinator operations in `api/coordinator.swagger.yml`: `listRuns`,
      `getRun`, `getRunTasks`, `resumeRun` (`POST /v1/runs/{id}/resume` with extra
      `iterationBudget` / new `deadline`), `reviewPR`
      (`POST /v1/runs/{id}/review` with `{decision, comments[]}`).
- [ ] Screens:
  - **Submit PRD** — title + markdown, `POST /v1/prd`.
  - **PRDs list** — all runs with status + TTL remaining.
  - **Active PRD detail** — current stage, budget remaining, plan, per-agent
    task status (poll `getRun`). Show a **Resume with +N budget** / **Abandon**
    control when `needs_human_review`; show the PR link + **Accept** / **Request
    changes** controls when `pr_ready`.
- [ ] Makefile: `build_ui`, `run_ui`; `make build` depends on `build_ui`.
- [ ] Deps: `github.com/urfave/negroni`, `github.com/rs/cors`; Node ≥ 18.

## Phase 5 — code + PR generation + PR-review loop

- [ ] Developer agents get a git worktree workspace, produce a real diff, run
      build/tests, open a GitHub PR (`gh` or API). Run enters `pr_ready`.
- [ ] Human review via `reviewPR`: **accept** → `accepted` (terminal);
      **request changes** with comments → each comment becomes
      `Finding{source:"human", targetRole}`, run returns to `running` and
      re-enters the factory (spends `iterationBudget`); budget exhausted →
      `needs_human_review`.
- [ ] Optionally ingest review comments posted directly on the GitHub PR via a
      webhook.
- [ ] Persist runs and outcomes; surface the PR link + revision history in the UI.

## Phase 6 — hardening

- [ ] AuthN/AuthZ between agents (A2A `SecuritySchemes` + `securityRequirements`).
- [ ] Catalog heartbeats / health of registered agents; drop stale registrations.
- [ ] Retries, circuit-breaking, cost cap (token/$ accumulated from `resp.Usage`).
- [ ] Observability (`a2a-go` `examples/observability` — OTel traces/metrics).
- [ ] UI auth; live run updates over SSE instead of polling.
