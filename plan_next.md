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
  - Still **deferred**: `a2a-go` cluster mode / multi-replica agents (Phase 7);
    ingesting comments posted on the GitHub PR (Phase 5).

- **Phase 4 (done)** — a Vue 3 web UI served by the coordinator, plus the backend
  gaps it exposed.
  - **Durable run log**: `Run.Events []Event` records every transition
    (`submitted`, `stage_started/completed/failed`, `plan_ready`,
    `awaiting_approval`, `approved`, `request_changes`, `attempt_cap`,
    `budget_exhausted`, `pushed`, `pr_opened`, `review_*`, `resumed`,
    `recovered`, `finished`) with actor, duration and detail — persisted with the
    run, so a restart no longer loses its history. `Task` gained `attempt`,
    `startedAt`, `durationMs`, `verdict`, `filesWritten` and per-step `findings`.
  - **SPA** (`browser/asf-ui`, Vite + Element Plus): runs kanban, run detail
    (timeline / steps / plan / findings / raw), submit-PRD form, and A2A catalog
    list + agent detail. Served by the coordinator from `--ui-dir`
    (`internal/coordinator/ui`, negroni.Static + an SPA history fallback).
  - **Catalog BFF**: `GET /v1/agents` and `GET /v1/agents/{role}` on the
    coordinator read through to the catalog (`agentkit.ListAgents` /
    `GetAgentDetail`), so the browser stays same-origin and no CORS is needed.
  - **`resumeRun`** (`POST /v1/runs/{id}/resume`) un-sticks `needs_human_review`
    with fresh budget and reset attempt counters (Phase 6.2 extends it to
    `failed` runs + a routed human `comment` + `accept`); **`reviewRun`**
    (`POST /v1/runs/{id}/review`) closes the Phase-5 human loop: accept →
    `accepted`; request changes → each comment becomes
    `Finding{source:"human"}` and the run re-enters the factory.
  - Bugs this surfaced and fixed: a re-pushed branch re-opened its PR (now keeps
    the existing `prURL`); a fix pass returning "no changes" failed the run (now a
    legitimate outcome); an unparseable developer reply now retries once; a
    model-authored `.gitignore` could silently exclude its own source from the
    commit (`Repo.AddPaths` force-stages what the agent wrote); the
    security-reviewer never saw the PRD and rated explicitly out-of-scope gaps as
    `high`, looping runs to the attempt cap (it now gets the PRD and is told to
    respect scope).

- **Phase 5 (done)** — the build/test gate, GitHub PR-comment ingestion, PR
  revision history, and a developer model bump.
  - **`agent-build-gate`** (`internal/buildgate` + `internal/agents/buildgate`,
    port 9108, `Dockerfile.buildgate` with Go 1.26 + Node 22): a deterministic,
    LLM-free stage inserted after the developers and before the reviewers. It
    runs `go build ./...` / `go test ./...` and `npm install` / `npm run build`
    in the workspace; a non-zero exit becomes a `high` `Finding{source:"build-gate"}`
    routed by file path to the responsible developer, which re-enters the Phase-3
    `request_changes` loop (`nextStage`'s fix-pass shortcut now points at the gate
    so it re-runs before the reviewers). A missing toolchain is an `info` finding,
    never a failure. Verified e2e: a seeded `undefined:` compile error was caught,
    routed with file:line, fixed on the fix pass, and the gate re-ran clean.
  - **GitHub PR webhook** (`POST /v1/webhooks/github`, `internal/forge/webhook.go`,
    `Orchestrator.IngestPRReview`): HMAC-SHA256 verified against
    `GITHUB_WEBHOOK_SECRET`; a `pull_request_review` (changes_requested / approved)
    or `pull_request_review_comment` is matched to a run by PR URL / branch and
    fed through the existing `Review` path. `scripts/github-webhook.md` covers the
    `gh webhook forward` local setup and a real webhook. `issue_comment` events
    are still ignored.
  - **Model config**: `backend/frontend/mobile-developer` prompts →
    `effort: high`, `maxTokens: 128000` (Sonnet 5's ceiling; `internal/llm` now
    clamps anything larger and logs it, and `callTimeout`/`dispatchTimeout` grew
    to 30m/35m). The e2e notes/dice runs now produce a complete, buildable
    implementation on the first pass — the intermittent prose-instead-of-JSON and
    dropped-file failures did not recur.
  - **UI**: a **Revisions** tab on the run detail (`src/lib/revisions.js` +
    `RevisionList.vue`) derives the PR revision history from `run.events` — each
    branch-ready cycle, with what triggered the next (reviewer / build gate /
    human review).

- **Phase 6 (done)** — deployable output, component tests, tighter build-gate
  findings, and a persistent local workspace.
  - **`agent-test-engineer`** (`cmd/agent-test-engineer`, port 9109, reuses
    `devagent.Executor`, prompt `agent_prompts/test-engineer.md`): a new stage
    after the developers and before the build gate (`plannedStages`, only when
    `len(devs) > 0`). It derives scenarios from the PRD's acceptance criteria and
    writes a standalone `test/component/` Go suite, the root `docker-compose.yml`
    (app service with a `healthcheck`), and a `docker-compose.test.yml` overlay
    with a `tester` service — returning an empty file set for a library / docs
    only change.
  - **build-gate now deploys and component-tests**: `checkCompose` validates the
    root compose file (`docker compose config -q`) and `checkComponent` runs
    `docker compose … up --build --abort-on-container-exit --exit-code-from
    tester` against the host daemon (socket mounted in `docker-compose.yml`),
    with a per-run project name and an unconditional `down -v --rmi local`
    teardown. Gated by `isCodeChange` + `hasService` so docs-only / library
    changes are never flagged. `Dockerfile.buildgate` gained the Docker CLI +
    compose plugin.
  - **Tighter findings**: `agent-build-gate` gets `b.LLM` and makes one
    best-effort pass (`tighten`) that collapses a cascade of failure output into
    the fewest routed `Finding`s; the deterministic file:line parse stays as the
    fallback.
  - **Deployable output**: developer prompts require a multi-stage `Dockerfile`
    and a `/healthz` endpoint; the planner scaffolds a `Dockerfile` +
    `docker-compose.yml` for a new repo.
  - **`./local_git` as a remote**: per-run checkouts stay in the
    `factory-workspace` named volume (disposable), but the coordinator
    bind-mounts `./local_git` and treats it as a git remote — a local-repo run
    is `git clone`d from it (`workspace.Prepare` KindLocal → `cloneInto`, no more
    `git worktree`) and the `asf/run-<id>` branch is `git push`ed back on finish
    (`coordinator.finish`, `Repo.Push` after `Open` learns `origin`). `make up`
    seeds `./local_git/project`, the default clone source
    (`ASF_DEFAULT_REPO` / `coordinator.WithDefaultRepo`). Produced branches
    persist on the host; the working trees don't.

- **Phase 6.1 (done)** — per-task developer batching + a leaner model budget, to
  stop the single-call JSON reply truncating on multi-file PRDs.
  - A developer's **first pass** is now dispatched one planner task at a time
    inside `devagent.Executor` (`runBatched`): one model call and one `git commit`
    per task (`"<role>: <task title>"`), each call scoped to its task with the
    earlier tasks' commits visible in the repo snapshot. Fix passes
    (`Attempt > 0`), the test-engineer, and single/zero-task roles still run as
    one call (`runOnce`). The coordinator is unchanged — it still sees one
    `backend-developer` stage returning one aggregated `ResultEnvelope`, so
    `plannedStages` / `nextStage` / the `indexOf` cursor are untouched.
  - Developer + test-engineer prompts dropped to `effort: medium` (reasoning
    tokens share the 128k `max_tokens` budget with the JSON output);
    `thinking: adaptive` and `maxTokens: 128000` kept. `devagent.maxContextBytes`
    60k → 40k. `pipeline.dispatchTimeout` 35m → 90m (a dispatch now covers a
    whole role's tasks).
  - Planner prompt asks for coherent, self-contained tasks (≈ one file or one
    endpoint + wiring) and no longer counts task volume toward the approval gate.

- **Phase 6.2 (done)** — human steering of stuck runs. `POST /v1/runs/{id}/resume`
  now also accepts a `failed` run (not just `needs_human_review`), and takes:
  - `comment` — recorded as a `Finding{source:"human", severity:"high"}`, routed
    via `factory.RouteRole` to the responsible developer with `Attempts[role]=1`
    so it lands in the devagent `# Fix pass` block (the `Review` request_changes
    mechanism, reused). A planner-stage failure with no plan yet instead appends
    the note to the PRD and replans.
  - `accept` — run `finish()` on the current branch (push / open PR) and stop,
    skipping the rest of the pipeline; requires commits.
  - `targetRole` — optional routing hint for the comment.
  RunDetail.vue gains a comment box + "Accept as-is" button, and shows the
  resume/restart actions for `failed` runs.

- **Phase 6.3 (done)** — "Request changes" at the plan-approval gate. The gate now
  offers three actions instead of a Reject dialog with a hidden abandon checkbox:
  **Approve** / **Request changes…** (comments → the planner **revises** its
  previous plan) / **Reject** (abandon). `Reject(feedback, abandon=false)` stores
  the note on a transient `Run.PlanFeedback` (no longer polluting
  `PRD.Description`) and keeps `run.Plan`; `pipeline.plannerInput` folds the
  previous plan + the feedback into the next planner prompt; `applyPlan` clears
  `PlanFeedback` once consumed. No swagger change (`RejectRequest` already had
  `feedback`/`abandon`).

- **Phase 6.4 (done)** — escaping-free file transport for the developer agents.
  The developers / test-engineer no longer return files as a JSON array of
  `{path, content}` (the model kept corrupting large `content` strings with
  unescaped newlines/quotes — `invalid character '\n' in string literal`).
  They now emit each file as a verbatim block (`=== FILE: <path> ===` …
  `=== END FILE: <path> ===`, `factory.ParseFileBlocks`); a legacy JSON array
  and a bare `NO CHANGES` are still accepted (`devagent.interpret`). An
  unterminated final block is a *detected* truncation: `runOnce` / `runBatched`
  commit the files that fully arrived and note it in the summary, and the
  missing file surfaces at the build gate as a normal fix-pass finding — no
  re-prompting to stitch a cut-off reply.

- **Phase 6.5 (done)** — SAST findings get a path-based owner + dev-only npm noise
  dropped. `gosec` / `govulncheck` / `npm-audit` findings now carry a
  `TargetRole` derived from their file path (`factory.RoleForPath`, promoted from
  the build gate's `routeByPath` and shared with it): `frontend/**` /
  `package.json` → frontend-developer, Go files → backend-developer. Fixes the
  loop where a `critical` frontend `npm audit` finding was routed to
  backend-developer (who can't fix it) until the attempt cap. `npm audit` also
  runs with `--omit=dev` so devDependency advisories (vite / vitest / esbuild —
  never shipped) are not reported at all. The UI Findings "Owner" column is now
  populated for tool findings.

- **Phase 6.6 (done)** — delete a run. `DELETE /v1/runs/{id}`
  (`Orchestrator.Delete` → new `Store.Delete` on both the mem and GORM stores +
  `workspace.Manager.Remove`) removes a run in a terminal state; refuses one
  still queued/running/awaiting_approval or with a live drive goroutine (409).
  RunDetail shows a **Delete** button when the run is `failed` (redirects to the
  board on success). No automatic run expiry — `pruneKeep` still bounds only the
  on-disk workspaces.

### Known limitations still open

- **Oversized single task.** Batching bounds each call to one task, but a single
  task that itself spans many files can still truncate. This is now *detected*
  (`ParseFileBlocks` → `BlockResult.Truncated`) and the complete files are kept,
  but the cut-off file still needs a fix pass to land. Follow-up: split such a
  task, or stream files as they close instead of buffering the whole reply.
- **`govulncheck` noise.** Findings are real stdlib CVEs reachable from generated
  code but not caused by it — consider down-ranking stdlib-only traces to `info`.

---

## Phase 5 remainder (deferred out of Phase 5)

- [ ] **Per-developer** build gate — one after each developer stage rather than
      one after all of them. Needs `plannedStages` to carry repeated stages,
      which breaks the `indexOf`-based `nextStage`.
- [ ] LLM summarization of build/test output into tighter findings (like
      `code-reviewer` does for Kodus) — deterministic parse is in place; add if
      the raw output proves noisy.
- [ ] Ingest `issue_comment` webhook events (plain PR conversation), not just
      formal reviews and inline review comments.
- [ ] Extend `internal/forge` beyond GitHub (GitLab, Gitea) — PR opening and the
      webhook.
- [ ] Batch developer tasks (see the standing limitation).

## Phase 7 — hardening

- [ ] AuthN/AuthZ between agents (A2A `SecuritySchemes` + `securityRequirements`).
- [ ] Catalog heartbeats / health of registered agents; drop stale registrations.
- [ ] Retries, circuit-breaking, cost cap (token/$ accumulated from `resp.Usage`).
- [ ] Observability (`a2a-go` `examples/observability` — OTel traces/metrics).
- [ ] UI auth; live run updates over SSE instead of the current polling.
- [ ] Multi-replica agents via `a2a-go` cluster mode
      (`a2asrv.WithClusterMode`); a worker pool + at-least-once dispatch in the
      coordinator.
- [ ] Kodus self-hosted on the same compose network with a real TLS endpoint
      (drop the `host.docker.internal` workaround); script the org/team-key
      bootstrap so `make kodus-up` is one step.
- [ ] Constrained-decoding structured outputs (`output_config.format`) for the
      developer file list — Phase 6.4's `=== FILE: … ===` block format removed
      the JSON-escaping failure without it, so this is now only a nicety.
