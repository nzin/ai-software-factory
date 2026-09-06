# Architecture

## Goal

A human submits a **PRD**. A coordinator agent drives a set of specialized
agents (planner, UI/UX designer, backend / frontend / mobile developer, security
reviewer, code reviewer) to produce a feature as a **Pull Request** for human
review. Agents communicate over the [A2A protocol](https://a2a-protocol.org/)
(`github.com/a2aproject/a2a-go`). Everything is Go.

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
- `Config` ⇄ `a2a.AgentExtension` (`ToExtension` / `FromExtension` / `FromCard`)
- `Defaults(role)` — per-role starting configuration

**Ownership:** the catalog is authoritative. An agent sends a default `Config` on
first registration (`PUT /v1/agents/{role}` with `modelConfig`); thereafter the
default is ignored and an operator overrides via
`PATCH /v1/agents/{role}/model`. Agents fetch their **effective** config from
`GET /v1/agents/{role}/model` on startup (`agentkit.Bootstrap`).

### Agent kit

`internal/agentkit` is the shared bootstrap every specialized agent uses:

1. `Register` — idempotent `PUT /v1/agents/{role}` with the base registration and
   default model.
2. `GetModel` — fetch the effective `modelext.Config` back.
3. build the local `a2a.AgentCard` (model extension from the effective config),
   construct an `llm.Client`.
4. `Serve` — `a2asrv.NewHandler(executor)` behind
   `mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(...))` +
   `NewStaticAgentCardHandler`.

`LLMExecutor(client, systemPrompt)` is the executor every role agent shares: read
the text parts of the incoming message, run one Claude turn, yield the response
as one terminal agent message. Roles differ only in prompt and model config.

### Coordinator

Accepts a PRD (`api/coordinator.swagger.yml`), drives a `Run`.

## Run lifecycle, TTL, and human review

**States:**

```
queued → running → pr_ready → { accepted (terminal) | changes_requested → running }
                       │
running ───────────────┼──▶ needs_human_review   (budget spent; a human can resume)
                       └──▶ failed                (unrecoverable engine error)
done                        (Phase 1 terminal: plan produced)
```

**TTL / budget** (`coordinator.Budget`, on every `Run`), whichever trips first:

- `IterationsRemaining` — decremented **once per agent dispatch**, including
  reviewer → developer bounce-backs.
- `Deadline` — wall-clock cap (zero = none).
- *(later)* a token/$ cost cap accumulated from `resp.Usage`.

When the budget is spent the orchestrator stops dispatching, sets
`needs_human_review` with a `reason`, and keeps the partial artifacts. A human
(in the UI, Phase 4) can **resume with +N budget** or **abandon** (→ `failed`).

**This is a state machine, not a DAG.** A DAG is acyclic; "security-reviewer
finds an issue → back to the developer → back to the reviewer" is a cycle. The
happy-path order is just the default transition table:

```
planner → ui-ux-designer → { backend, frontend, mobile } → security-reviewer → code-reviewer → PR
```

Reviewers return a structured verdict — `approve` or
`request_changes{targetRole, findings[]}`. On `request_changes` the coordinator
routes the `Run` back to `targetRole` (a developer for an implementation bug, the
planner for a design flaw), re-runs the affected reviewers, and repeats — bounded
by the run budget and a per-stage `maxIterations` local guard.

**PR review loop (Phase 5):** when a developer stage opens a PR the run enters
`pr_ready`. The human either **accepts** (→ `accepted`, terminal; the human
merges) or **requests changes** with comments — each comment becomes a
`Finding{source:"human", targetRole}`, the run returns to `running` and
re-enters the factory, spending `IterationsRemaining`. Budget already spent →
`needs_human_review`.

**Engine abstraction.** `coordinator.Engine` (`Run(ctx, *Run) (StageResult,
error)` + `Next(*Run) string`) is what the state-machine loop calls. The default
`a2aEngine` discovers agents through the catalog and dispatches over A2A; tests
substitute fakes (e.g. an always-`request_changes` engine that proves the budget
halts the loop).

## Concurrency & multi-PRD

- Agents are stateless; an `a2asrv` server handles each request in its own
  goroutine, so one agent process serves many tasks concurrently.
- Each `Run` gets a fresh A2A `ContextID`; every dispatch for that PRD carries it.
- The coordinator runs each `Run` on its own goroutine. Back-pressure: per-role
  semaphores sized from `AgentRegistration.concurrency` + a global `--max-runs`
  cap (Phase 3).
- Phase 1 keeps `Run` state in memory. Phase 3 moves it to a store with a worker
  pool, at-least-once dispatch, and crash-resume; multi-replica agents via
  `a2a-go` cluster mode.

## Web UI (Phase 4)

Vue 3 SPA under `browser/asf-ui/`, built to `dist/` and served **by the
coordinator** from the `go-swagger` editable `configure_coordinator.go`
(`negroni.Static` with SPA fallback, or `//go:embed`) — the pattern from
`github.com/nzin/golang-skeleton` step5. One `coordinator serve` process serves
both the API and the UI. Screens: submit a PRD, list PRDs, watch an active PRD
and its per-agent tasks, and act on `needs_human_review` / `pr_ready`.

## Code generation

`make gen`, per service: `swagger validate` → `swagger generate server` (models +
`restapi/operations` handler interfaces) → `swagger generate client`. The
editable `configure_<svc>.go` is preserved across regeneration (copy-out /
copy-back). Generated code is committed so `go build ./...` works without the
tool. We hand-write `handlers.go`, `store.go` / `service.go`, and the `cmd/<svc>`
wiring that builds `operations.NewXxxAPI`, calls `<svc>.Setup(api, impl)`, and
serves via `restapi.NewServer`.
