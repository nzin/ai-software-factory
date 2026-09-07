# AI Software Factory

Submit a **PRD** (Product Requirements Definition); a network of specialized AI
agents collaborates to produce a feature as a **Pull Request** for human review.

Agents talk to each other over the [A2A protocol](https://a2a-protocol.org/)
using [`a2a-go`](https://github.com/a2aproject/a2a-go). Everything is Go.

> **Status: Phase 1.** A working vertical slice: the catalog service, the model
> extension, the shared agent kit, one real agent (the **planner**, which makes a
> genuine Claude call), and a coordinator that runs a PRD through it. The
> remaining phases are in [`plan_next.md`](plan_next.md).

## Architecture

```
                      ┌───────────────┐
   PRD ──────────────▶│  coordinator  │  drives a Run (state machine + TTL)
                      └───────┬───────┘
                              │ 1. discover agent (REST)
                              ▼
                      ┌───────────────┐
                      │    catalog    │  source of truth for the agent roster
                      │  (SQLite/GORM)│  + each agent's model configuration
                      └───────┬───────┘
                              │ agents register on startup
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
        ┌──────────┐    ┌──────────┐    ┌──────────┐
        │ planner  │    │ backend  │    │   ...    │   A2A JSON-RPC agents
        └────┬─────┘    └──────────┘    └──────────┘   (Phase 2+)
             │ 2. coordinator sends the PRD over A2A, gets a plan back
             ▼
          Claude  (model chosen per-agent by the catalog)
```

- **catalog** — REST service (`go-swagger`, spec-first: `api/catalog.swagger.yml`).
  Stores each agent's A2A registration **and** its model configuration, and
  assembles the agent's `AgentCard`.
- **coordinator** — accepts a PRD (`api/coordinator.swagger.yml`), discovers the
  planner through the catalog, delegates over A2A, returns the plan. Each run
  carries a **TTL / budget** so it can never loop forever; when the budget is
  spent the run stops in `needs_human_review`.
- **agent-planner** — an A2A agent built on `internal/agentkit`. Registers with
  the catalog, fetches its effective model config, serves
  `/.well-known/agent-card.json` and `/invoke`.

### Agent prompts

Each agent's **system prompt** is a file — `agent_prompts/<role>.md` — read from
disk at startup (no rebuild to change it; the agent fails to start if the file is
missing). An optional YAML front-matter block overrides the agent's
`name` / `description` / `skills`:

```markdown
---
name: Planner
skills: [planning, architecture, breakdown]
---
You are a senior software architect …
```

Override the directory with `--prompts-dir` or `ASF_PROMPTS_DIR`. See
[agent_prompts/README.md](agent_prompts/README.md).

### "The model to use"

`a2a.AgentCard` has no field for a model, so the model configuration is carried
as a **custom A2A capability extension** (`internal/modelext`, URI
`https://ai-software-factory.dev/ext/model/v1`). The **catalog owns it**: an
agent sends a default on first registration, after which an operator can override
it with `PATCH /v1/agents/{role}/model` — no redeploy. Every assembled
`AgentCard` surfaces the current config under `capabilities.extensions`.

## Prerequisites

- Go ≥ 1.25 (`a2a-go` requires it)
- An Anthropic API key — the planner calls Claude. Put it in `.env`:
  ```bash
  cp .env.example .env && $EDITOR .env   # ANTHROPIC_API_KEY=sk-ant-...
  ```
  `.env` is git-ignored; the Makefile loads and exports it. (An `ant auth login`
  profile or an exported `ANTHROPIC_API_KEY` also work.)
- `make tools` once, to install the pinned `go-swagger` (only needed to run `make gen`)

## Build & test

```bash
make build      # -> ./bin/{catalog,coordinator,agent-planner}
make test
make vet
make gen        # regenerate go-swagger code from api/*.swagger.yml
```

## Run the demo

```bash
make demo          # reads ANTHROPIC_API_KEY from .env
```

`scripts/demo.sh` starts the catalog and the planner, shows the assembled
`AgentCard` (with the model extension), submits `docs/sample-prd.md` through the
coordinator, prints the generated plan, and demonstrates a model override.

### Manual

```bash
# 1. catalog
./bin/catalog --addr 127.0.0.1:8080 --db catalog.db &

# 2. planner (registers itself with the catalog)
./bin/agent-planner --catalog-url http://127.0.0.1:8080 &

# 3. run a PRD
./bin/coordinator submit --prd docs/sample-prd.md --out plan.md

# inspect
curl -s 127.0.0.1:8080/v1/agents/planner | jq
curl -s 127.0.0.1:9101/.well-known/agent-card.json | jq '.capabilities.extensions'

# override the planner's model
curl -s -X PATCH 127.0.0.1:8080/v1/agents/planner/model \
  -H 'content-type: application/json' -d '{"model":"claude-sonnet-5"}'
```

The coordinator can also run as an HTTP service: `./bin/coordinator serve --addr :8090`.

## Repository layout

| Path | What |
|---|---|
| `agent_prompts/<role>.md` | each agent's system prompt (+ optional front-matter) |
| `api/*.swagger.yml` | OpenAPI 2.0 specs — source of truth for the REST APIs |
| `internal/<svc>/gen/` | `go-swagger` output (committed) |
| `internal/modelext/` | the model-configuration A2A extension |
| `internal/catalog/` | catalog store (GORM/SQLite), service, handlers, card builder |
| `internal/llm/` | Anthropic/Claude wrapper |
| `internal/agentprompts/` | loads `agent_prompts/<role>.md` (front-matter + body) |
| `internal/agentkit/` | agent bootstrap: load prompt → register → fetch model → serve A2A |
| `internal/agents/planner/` | the planner agent's identity (role + code-default metadata) |
| `internal/coordinator/` | the Run state machine + A2A dispatch engine |
| `internal/prd/` | PRD type + Markdown/JSON parsing |
| `cmd/` | `catalog`, `coordinator`, `agent-planner` binaries |
| `docs/ARCHITECTURE.md` | full target design, including later phases |
