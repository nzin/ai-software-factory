# AI Software Factory — remaining phases

- **Phase 1 (done)** — catalog service, `modelext` A2A extension, `agentkit`, the
  planner agent, a coordinator that runs a PRD to a plan with a TTL/budget guard.
- **Phase 2 (done)** — the full agent roster (ui-ux-designer, backend / frontend /
  mobile developers, security-reviewer, code-reviewer); per-run git worktrees
  (`internal/workspace`); the planner emits a structured task list; the
  coordinator runs a **fixed linear pipeline** (`internal/coordinator/pipeline.go`)
  dispatching only the developer roles that have tasks; `security-reviewer` = SAST
  (`internal/sast`) + LLM pass; `code-reviewer` = Kodus CLI (`internal/kodus`);
  A2A message contracts in `internal/factory` (DataPart envelopes); everything
  containerised (`Dockerfile*`, `docker-compose.yml`); `scripts/kodus-setup.md`
  for the self-hosted Kodus.
  **Verified end to end** against a small backend PRD: planner → backend-developer
  (14 files committed, `go build`/`go test` green) → security-reviewer →
  code-reviewer → `pr_ready`. All 3 Docker images build; the Kodus stack comes up
  and the CLI routes to it.

- **Phase 3 (done)** — the feedback loop, the human-approval gate, real git
  repositories, and durable async runs.
  - Reviewers return a **verdict** (`approve` | `request_changes`) plus a
    `targetRole`; the coordinator (`internal/coordinator/coordinator.go` `drive`)
    routes `request_changes` back to a developer with the findings
    (`DispatchEnvelope.Findings`/`Attempt` → a fix pass), re-runs the reviewers,
    and repeats — bounded by `WithMaxStageIterations` (default 3) inside the
    global `Budget`; the cap trips → `needs_human_review`.
  - **Human-approval gate**: the planner emits `approval: {required, reason}` in
    its JSON block (required iff the plan is non-trivial **and** touches code or
    deploy config). The run pauses at `awaiting_approval`; `POST
    /v1/runs/{id}/approve` resumes it, `/reject` (`{feedback, abandon}`) replans
    or abandons. The gate freezes the deadline (extended by the pause on resume).
  - **Repositories** (`internal/workspace`, resolved from a single `repoURL` on
    `POST /v1/prd`): empty ⇒ `git init` a new local repo (the planner's T1
    scaffolds it); local path / `file://` ⇒ `git worktree add` so the
    `asf/run-<id>` branch lands in the user's own repo; https/ssh URL ⇒
    `git clone`, then branch **pushed** and a **PR opened** (`internal/forge`,
    GitHub REST + `GITHUB_TOKEN`) — `pr_open` with `prURL`, else `pr_ready`.
    Diffs/`HasCommits` use `merge-base(base, HEAD)..HEAD`.
  - **Durable async runs**: `POST /v1/prd` returns `202` immediately;
    `GET /v1/runs`, `GET /v1/runs/{id}`. `internal/coordinator/runstore`
    (GORM/SQLite, `--runstore` / `ASF_RUNSTORE`) persists every transition;
    `Orchestrator.Recover()` parks `running` runs left by a restart as
    `needs_human_review` and keeps `awaiting_approval` runs resumable.
  - Still **deferred**: `a2a-go` cluster mode / multi-replica agents (Phase 6);
    ingesting comments posted on the GitHub PR (Phase 5).

### Known limitations still open

- **Single-call codegen doesn't scale.** A developer agent gets *all* its tasks in
  one LLM call and must emit every file as one JSON array. A large PRD (≈15+ tasks)
  blows the token budget (`stop_reason=max_tokens`) before any file is written.
  Fix: dispatch developer tasks in **batches** (or one task at a time), or use
  structured outputs with a file-streaming protocol. Current mitigation: developer
  roles run at `effort: low`, `maxTokens: 64000` (set in their
  `agent_prompts/<role>.md` `model:` block).
- **No build/test gate.** Generated code is committed unverified. Add a
  `go build ./... && go test ./...` / `npm run build` gate after each developer
  stage (Phase 5 item, but cheap to pull forward).
- **`govulncheck` noise.** Findings are real stdlib CVEs reachable from generated
  code but not caused by it — consider down-ranking stdlib-only traces to `info`.

---

## Phase 4 — VueJS web UI (served statically by the coordinator)

- [ ] Vue 3 + `vue-router` + `axios` + Element Plus, scaffolded under
      `browser/asf-ui/` (like `github.com/nzin/golang-skeleton` step5).
- [ ] `make build_ui` → `browser/asf-ui/dist/`; the coordinator's
      `configure_coordinator.go` `setupGlobalMiddleware` serves it
      (`negroni.Static{Dir: http.Dir("browser/asf-ui/dist"), IndexFile:
      "index.html"}` with SPA fallback, or `//go:embed`). `/v1/**` and
      `/healthz` continue to the generated `restapi`.
- [ ] `listRuns` / `getRun` / `approve` / `reject` already exist (Phase 3). Add
      `getRunTasks`, `resumeRun` (`POST /v1/runs/{id}/resume` with extra
      `iterationBudget` / new `deadline` — un-sticks `needs_human_review`), and
      `reviewPR` (`POST /v1/runs/{id}/review` with `{decision, comments[]}`).
- [ ] Screens:
  - **Submit PRD** — title + markdown, `POST /v1/prd`.
  - **PRDs list** — all runs with status + TTL remaining.
  - **Active PRD detail** — current stage, budget remaining, plan, per-agent
    task status (poll `getRun`). Show a **Resume with +N budget** / **Abandon**
    control when `needs_human_review`; show the PR link + **Accept** / **Request
    changes** controls when `pr_ready`.
- [ ] Makefile: `build_ui`, `run_ui`; `make build` depends on `build_ui`.
- [ ] Deps: `github.com/urfave/negroni`, `github.com/rs/cors`; Node ≥ 18.

## Phase 5 — build/test gates + PR-review loop

- [ ] Add build/test gates: run `go build ./... && go test ./...` / `npm run
      build` in the workspace after each developer stage; a failure becomes a
      routed `Finding` (reuses the Phase 3 request_changes loop).
- [ ] PR opening exists (Phase 3, `internal/forge`, GitHub). Extend to other
      hosts; ingest review comments posted directly on the PR via a webhook so
      each becomes `Finding{source:"human", targetRole}` and re-enters the loop.
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
- [ ] Kodus self-hosted on the same compose network with a real TLS endpoint
      (drop the `host.docker.internal` workaround); script the org/team-key
      bootstrap so `make kodus-up` is one step.
- [ ] Structured outputs for developer file lists (`output_config.format`)
      instead of parsing a fenced JSON array.
