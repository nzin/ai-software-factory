# Architecture

## Goal

A human submits a **PRD**. A coordinator agent drives a set of specialized
agents (planner, UI/UX designer, backend / frontend / mobile developer, test
engineer, build gate, security reviewer, code reviewer) to produce a feature as
a **Pull Request** for human review. Agents communicate over the
[A2A protocol](https://a2a-protocol.org/) (`github.com/a2aproject/a2a-go`).
Everything is Go.

## Components

### Catalog service

A2A has no registry — discovery is per-agent via `/.well-known/agent-card.json`.
The catalog is our own component and the **source of truth for the agent
roster**. For each agent it stores:

- the A2A **registration**: `role` (stable key), name, description, base URL,
  transport, skills, `concurrency` (max in-flight tasks the coordinator will send
  it), enabled flag;
- the **model configuration**: provider, model, `maxTokens`, `effort`,
  `thinking`, free-form `params`.

It assembles the agent's `a2a.AgentCard` and injects the model configuration as a
capability extension (see below). REST API is spec-first
(`api/catalog.swagger.yml`, `go-swagger`). Persistence is GORM + SQLite.

### "The model to use" — the `modelext` extension

`a2a.AgentCard` is a fixed struct with no model field, but
`AgentCard.Capabilities.Extensions` is the sanctioned place for custom metadata.
`internal/modelext` defines:

- `URI = "https://ai-software-factory.dev/ext/model/v1"`
- `Config` (`provider` / `model` / `maxTokens` / `effort` / `thinking` / `params`,
  with both `json` and `yaml` tags) ⇄ `a2a.AgentExtension`
- `Config.WithDefaults()` — fills blanks; `DefaultModel = "claude-sonnet-5"`
- `Defaults(role)` — the generic fallback for a prompt file with no `model:` block

**Ownership:** the agent's **prompt file is the source of truth**. Its `model:`
front-matter block is re-asserted into the catalog on every start
(`Register` with `forceModel=true`), exactly like `skills:`. The catalog stores
it, serves it on the `AgentCard`, and an operator `PATCH /v1/agents/{role}/model`
is a *temporary* override that the next agent restart reverts. Agents read the
effective config from `GET /v1/agents/{role}/model` on startup
(`agentkit.Bootstrap` → `resolveModel`: `Options.DefaultModel` → prompt `model:`
→ `modelext.Defaults`).

### Agent kit

`internal/agentkit` is the shared bootstrap every specialized agent uses:

1. `agentprompts.Load(dir, role)` — read `agent_prompts/<role>.md` from disk
   (hard error if missing); split the YAML front-matter
   (`name`/`description`/`skills`/`model`) from the prompt body.
2. `mergeMeta` + `resolveModel` — front-matter overrides the code-default identity
   and model, field by field.
3. `Register` — `PUT /v1/agents/{role}?forceModel=true` with the merged identity
   and the resolved model (the file re-asserts on every start).
4. `GetModel` — fetch the effective `modelext.Config` back.
5. build the local `a2a.AgentCard` (model extension from the effective config),
   construct an `llm.Client`.
6. `Serve` — `a2asrv.NewHandler(executor)` behind
   `mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(...))` +
   `NewStaticAgentCardHandler`.

`LLMExecutor(client, systemPrompt)` is the executor every role agent shares: read
the text parts of the incoming message, run one Claude turn, yield the response
as one terminal agent message. Roles differ **only** in their
`agent_prompts/<role>.md` file — prompt, skills, and model; `internal/agents/<role>`
holds just the role key and code-default identity. Editing a prompt file needs
only a restart of that agent — no rebuild, no code change.

### Coordinator

Accepts a PRD + a `repoURL` (`api/coordinator.swagger.yml`), resolves a per-run
workspace (`internal/workspace`, `workspace/<runId>/repo` — a new `git init`
repo, a clone of an existing local repo, or a clone of a remote), and
drives a `Run` through the loop (`internal/coordinator/coordinator.go` `drive`,
stage order in `plan.go`, A2A dispatch in `pipeline.go`):

```
planner → [human approval gate] → ui-ux-designer? → {backend, frontend, mobile}?
  → test-engineer? → build-gate → security-reviewer → code-reviewer
                      (request_changes / build_failed ↺ back to a developer)
```

- The **planner** is handed a repository snapshot (a new/empty marker, or a
  capped file tree + README head) and returns a prose plan plus a fenced `json`
  block with `tasks` **and** an `approval` decision. The coordinator parses it
  (`factory.ParsePlan`), normalises the roles, and dispatches **only the
  developer roles that have tasks**. If the repo is new, the planner's T1
  scaffolds it. `ui-ux-designer` runs only when there is frontend or mobile work.
- **Human approval gate.** When `approval.required` is true (planner's call:
  non-trivial **and** touches code or deploy config) the run pauses at
  `awaiting_approval` before the first developer. `POST /v1/runs/{id}/approve`
  resumes it; `/reject` (`{feedback, abandon}`) replans or abandons. The gate
  freezes the deadline.
- **Developer agents** (`internal/agents/devagent`) receive a
  `factory.DispatchEnvelope` (A2A DataPart) with the workspace path, the plan,
  the UI spec, their tasks, and — on a fix pass — the `Findings` + `Attempt`
  count. They write whole files, `git commit`, and return a
  `factory.ResultEnvelope`.
- **test-engineer** (`agent-test-engineer`, shares `internal/agents/devagent`'s
  executor — only its prompt, `agent_prompts/test-engineer.md`, differs) runs
  once after the developers, when at least one ran. It writes into the
  workspace: a component/API test suite (`test/component/`, a dependency-free
  Go program, one test per PRD acceptance criterion), a Playwright e2e suite
  (`test/e2e/`, `@playwright/test`, only when there's a browser-facing
  frontend, one test per user-facing acceptance criterion, screenshotting to a
  fixed `/output/screenshots/` path), a per-feature **Traefik gateway**
  (`gateway/`, `traefik:v3.x`, file-provider `dynamic.yml` — never the Docker
  provider, so it needs no Docker socket) as the app's single ingress
  (`/api/*` → backend, else → frontend), and the deployment glue: the root
  `docker-compose.yml` (`app` / `frontend` get healthchecks, no host port
  mappings — only `gateway` is externally reachable) plus a
  `docker-compose.test.yml` overlay adding one `tester` service that runs both
  suites against `http://gateway` and exits non-zero on any failure.
- **build-gate** (`internal/buildgate`, `agent-build-gate`, no LLM) compiles and
  tests the workspace — `go build ./...` / `go test ./...` and `npm install` /
  `npm run build` — and turns any non-zero exit into a `high`
  `Finding{source:"build-gate"}` routed by file path. A missing toolchain is an
  `info` finding, never a failure. When those pass and the workspace has a
  `docker-compose.test.yml` (i.e. test-engineer ran), it also validates
  `docker compose config` and then runs `checkComponent`: brings up the full
  stack plus the `tester` overlay under a per-run compose project name with
  `GATEWAY_PORT=0` (an ephemeral host port, so concurrent runs' gateways never
  collide), waits for `tester` to exit, `docker cp`'s out any Playwright
  screenshots into `test/e2e/screenshots`, tears the stack down unconditionally,
  and folds the `PASS`/`FAIL [api]`/`[e2e]` counts into the check summary (e.g.
  "api 8/8 passed, e2e 5/5 passed, 3 screenshot(s) captured"). It needs current
  Go + Node + a Docker CLI with the compose plugin against the **host Docker
  daemon** (mounted socket) to drive that nested `docker compose`, so it ships
  its own `Dockerfile.buildgate` (Go 1.26 + Node 22 + Docker CLI) rather than
  reusing `security-reviewer`'s stale Debian toolchain.
- **security-reviewer** runs `internal/sast` over the workspace plus one LLM pass
  over the diff; **code-reviewer** runs the Kodus CLI. Each returns a `Verdict`
  (`approve` | `request_changes`, derived from finding severity by
  `factory.VerdictFor`) and a `TargetRole`.
- On `request_changes` (or the gate's `build_failed`) the coordinator routes back
  to a developer (`res.TargetRole`, else `factory.RouteRole`), bumps
  `Run.Attempts[role]`, and re-runs — a bounced developer's `nextStage` shortcut
  jumps to the **build gate** so it re-verifies before the reviewers. More than
  `WithMaxStageIterations` (default 3) attempts on one role → `needs_human_review`.
- On finish: `HasCommits` uses `merge-base(base, HEAD)..HEAD`. A remote clone
  gets `git push` + a GitHub PR (`internal/forge`, `GITHUB_TOKEN`) →
  `pr_open`/`prURL`, else `pr_ready`. A local-repo run pushes its branch back
  to the `file://` origin and ends at `pr_ready`; a brand-new repo ends at
  `pr_ready` naming the branch.

Agents in docker-compose share the per-run checkout through the
`factory-workspace` named volume, mounted at `/workspace` in the coordinator and
every agent; the envelope carries absolute container paths. Those checkouts are
disposable. The coordinator additionally bind-mounts `./local_git` (at
`/var/lib/asf/local_git`) and treats it as a **git remote**: for a local-repo run
it `git clone`s from `./local_git` into `factory-workspace/<runID>/repo` and, on
finish, `git push`es the `asf/run-<id>` branch back. `make up` seeds the shared
repo `./local_git/project` that empty-`repoURL` runs clone (`ASF_DEFAULT_REPO` /
`coordinator.WithDefaultRepo`), instead of scaffolding a throwaway repo per run —
so the produced branches persist on the host while the working trees do not. A
bare-name `repoURL` (no `/`, e.g. `"demo"`) is likewise resolved under
`./local_git/<name>` and auto-`git init`'d on first use (`ASF_LOCAL_GIT_ROOT` /
`workspace.Manager.LocalGitRoot`, default `./local_git`), then treated as an
existing local repo from then on.

## Run lifecycle, TTL, and human review

**States:**

```
queued → running ⇄ awaiting_approval   (planner gate; approve | request changes:
                                       feedback → planner revises its plan |
                                       reject: abandon)
   running → pr_open   (remote: branch pushed + GitHub PR opened)
   running → pr_ready  (local repo: branch pushed back; new repo: branch ready)
   running → done      (no commits were produced)
   running → needs_human_review   (budget spent, or a role fails review 3×,
                                   or the coordinator restarted mid-run)
   running → failed    (unrecoverable engine error, or rejected+abandoned)
pr_ready/pr_open → { accepted (terminal) | request_changes → running }
needs_human_review | failed → running   (resume: +budget, optional comment →
                                         routed human finding) | accepted/pr_*
                                         (accept as-is) | failed (abandon)
```

**TTL / budget** (`coordinator.Budget`, on every `Run`), whichever trips first:

- `IterationsRemaining` — decremented **once per agent dispatch**, including
  reviewer → developer bounce-backs.
- `Deadline` — wall-clock cap (zero = none).
- *(later)* a token/$ cost cap accumulated from `resp.Usage`.

When the budget is spent the orchestrator stops dispatching, sets
`needs_human_review` with a `reason`, and keeps the partial artifacts. A human
`POST /v1/runs/{id}/resume`s it — from `needs_human_review` **or** `failed` —
with +N budget and, optionally, a `comment` (recorded as a high-severity
`Finding{source:"human"}` and routed to the responsible developer for a fix
pass) or `accept: true` (take the branch as-is: push / open the PR and stop). A
bare resume just retries. The other resumable pause is the planner gate, via
`POST /v1/runs/{id}/approve`.

**This is a state machine, not a DAG.** A DAG is acyclic; "security-reviewer
finds an issue → back to the developer → back to the reviewer" is a cycle. The
happy-path order is just the default transition table:

```
planner → ui-ux-designer → { backend, frontend, mobile } → test-engineer → build-gate → security-reviewer → code-reviewer → PR
```

The build gate and the reviewers return a structured verdict — `approve` or
`request_changes{targetRole, findings[]}`. On `request_changes` (or
`build_failed`) the coordinator routes the `Run` back to `targetRole` (a
developer for an implementation bug or a broken build, the planner for a design
flaw), re-runs the gate and the affected reviewers, and repeats — bounded by the
run budget and a per-role `maxIterations` local guard.

**PR review loop.** When the branch is ready the run enters `pr_ready` (or
`pr_open` with a real GitHub PR). The human either **accepts** (→ `accepted`,
terminal; the human merges) or **requests changes** with comments — each comment
becomes a `Finding{source:"human", targetRole}`, the run returns to `running` and
re-enters the factory, spending `IterationsRemaining`. Budget already spent, or
too many rounds on one role → `needs_human_review`. Ingesting comments left on
the GitHub PR itself (rather than in the UI) is Phase 5.

**Engine abstraction.** `coordinator.Engine` (`Run(ctx, *Run) (StageResult,
error)`) dispatches one stage. The default `pipelineEngine` discovers agents
through the catalog and dispatches over A2A; stage sequencing
(`plannedStages` / `nextStage` / `firstDevelopmentStage`) lives in the
orchestrator (`plan.go`), not the engine. Tests substitute fakes (e.g. an
always-`request_changes` engine that proves the attempt cap and budget halt the
loop).

**Human review of a finished run.** `POST /v1/runs/{id}/review` takes the human's
verdict on a run in `pr_ready`/`pr_open`. **accept** → `accepted` (terminal).
**request_changes** turns each comment into a
`Finding{source:"human", severity:"high"}`, routes it with the same
`factory.RouteRole` the reviewers use, bumps `Attempts[target]`, and drops the run
back into `drive` at that developer — a fix pass, the build gate, the reviewers,
and a new revision on the same branch. Past `maxStageIter` it parks in
`needs_human_review` instead. `POST /v1/runs/{id}/resume` un-sticks a parked run:
it grants budget, **clears `Attempts`** (a run stopped by the retry cap would
otherwise re-trip at once) and restarts at `Run.LastStage`.

**Deleting a run.** `DELETE /v1/runs/{id}` (`Orchestrator.Delete`) drops a run in
a terminal state — record (`Store.Delete`) and workspace (`workspace.Manager.Remove`).
It refuses a run that is still queued/running/awaiting_approval or has a live
drive goroutine (`409`). There is no automatic run expiry — `pruneKeep` bounds
only on-disk workspaces, never the run records — so a failed run stays on the
board until deleted.

**GitHub PR reviews.** `POST /v1/webhooks/github` (`internal/forge/webhook.go` +
`Orchestrator.IngestPRReview`) is the same loop, triggered from GitHub instead of
the UI. It verifies the `X-Hub-Signature-256` HMAC against
`GITHUB_WEBHOOK_SECRET`, parses a `pull_request_review` /
`pull_request_review_comment`, matches the run by PR URL (or branch + repo), and
calls `Review` — an approval accepts, a change request re-enters at a developer.
Unknown/stale/ignored events are logged and answered `202`. Local setup and the
webhook config are in `scripts/github-webhook.md`.

**Run event log.** `Run.Events` is the run's audit trail: one `Event{Seq, At,
Kind, Stage, Status, Message, Detail, Attempt, DurationMs, Actor}` per
transition, appended by `Orchestrator.event` — which also writes the same line to
stdout, so the persisted log and the process log cannot drift. `Task` carries the
per-step record (`Attempt`, `StartedAt`, `DurationMs`, `Verdict`, `FilesWritten`,
`Findings`). Both ride along in the run's JSON blob, capped (500 events, 8 KiB of
detail each) so one long run cannot bloat its store row. This is what the UI's
run-detail timeline renders, and it survives a coordinator restart.

**Durable runs.** Every transition is persisted through `coordinator.Store`.
The default is in-memory; `internal/coordinator/runstore` is a GORM/SQLite
implementation (`--runstore` / `ASF_RUNSTORE`) that survives a restart.
`Orchestrator.Recover()` runs on startup: a `running` run left mid-flight becomes
`needs_human_review`; an `awaiting_approval` run stays resumable. `POST /v1/prd`
returns `202` immediately and the run drives on a background goroutine.

## Concurrency & multi-PRD

- Agents are stateless; an `a2asrv` server handles each request in its own
  goroutine, so one agent process serves many tasks concurrently.
- Each `Run` gets a fresh A2A `ContextID`; every dispatch for that PRD carries it.
- The coordinator runs each `Run` on its own background goroutine (guarded so a
  run is never driven twice at once). Back-pressure via per-role semaphores and a
  global `--max-runs` cap is still ahead.
- `Run` state is persisted through `coordinator.Store` (SQLite via `runstore`);
  crash-resume is `Recover()`. A worker pool, at-least-once dispatch, and
  multi-replica agents via `a2a-go` cluster mode are Phase 7.

## Web UI

Vue 3 SPA under `browser/asf-ui/` (Vite + vue-router + axios + Element Plus),
built to `dist/` and served **by the coordinator** — the pattern from
`github.com/nzin/golang-skeleton` step5. One `coordinator serve` process serves
the API under `/v1` and the SPA at `/`, so the browser is same-origin with both.

**Serving** (`internal/coordinator/ui`, wired into `setupGlobalMiddleware` in the
editable `configure_coordinator.go`): a negroni stack of
`Recovery → Static → SPAFallback → the go-swagger router`. `negroni.Static`
serves real files, but its `IndexFile` only applies when the path resolves to a
*directory* — a deep link like `/runs/abc-123` misses and would reach the API's
404 — so `SPAFallback` answers any GET/HEAD outside `/v1/`, `/healthz`, `/docs`
and `/swagger.json` that accepts HTML with `index.html`. `ui.Dir()` returns `""`
unless the directory exists *and* contains an `index.html`, and the static
middleware is skipped entirely in that case: `http.Dir("")` resolves against the
process working directory, so installing it with an empty path would serve the
repo. That also means a coordinator built without the SPA just serves the API,
which is why `make build` needs no Node toolchain.

**Catalog BFF.** The catalog is a separate service on another port and neither
service sets CORS headers, so the SPA cannot call it directly. Instead the
coordinator exposes `GET /v1/agents` and `GET /v1/agents/{role}`, reading through
with `agentkit.CatalogClient.ListAgents` / `GetAgentDetail`. The list endpoint
fans the per-role detail out concurrently (the catalog's own `listAgents` returns
registrations without the model config), degrading to a blank model column rather
than failing if the catalog is slow.

**Screens.** A runs kanban bucketed by status; a run detail with the event
timeline, the per-step record, the plan, the findings and the raw JSON, plus an
action bar that switches on status (approve/reject · resume/abandon ·
accept/request-changes · delete when failed); a submit-PRD form; and the A2A catalog roster with a
per-agent card view. Updates are by polling — SSE is Phase 7.

## Code generation

`make gen`, per service: `swagger validate` → `swagger generate server` (models +
`restapi/operations` handler interfaces) → `swagger generate client`. The
editable `configure_<svc>.go` is preserved across regeneration (copy-out /
copy-back). Generated code is committed so `go build ./...` works without the
tool. We hand-write `handlers.go`, `store.go` / `service.go`, and the `cmd/<svc>`
wiring that builds `operations.NewXxxAPI`, calls `<svc>.Setup(api, impl)`, and
serves via `restapi.NewServer`.
