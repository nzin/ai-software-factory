# Agent prompts

Each specialized agent loads its **system prompt** from a file in this directory
at startup:

```
agent_prompts/<role>.md
```

`<role>` is the agent's stable catalog key (`planner`, `backend-developer`, …).
The agent binary reads this file from disk on every start and **fails to start
if it is missing** — there is no compiled-in fallback. Edit the file and restart
the agent; no rebuild needed.

The directory is resolved relative to the process working directory. Override it
with `--prompts-dir <path>` or the `ASF_PROMPTS_DIR` environment variable.

## Front-matter

A file may begin with a YAML front-matter block delimited by `---` lines. It
carries the agent's identity **and the Claude model it runs on**:

```markdown
---
name: Backend Developer
description: Writes the backend as a Go REST API, spec-first with go-swagger.
skills: [golang, rest-api, go-swagger]
model:
  provider: anthropic
  model: claude-sonnet-5
  maxTokens: 64000
  effort: low          # "" | low | medium | high | xhigh | max
  thinking: adaptive    # adaptive | off
---
You are a senior Go backend engineer …
```

| Key | Effect |
|---|---|
| `name` / `description` / `skills` | catalog registration + AgentCard metadata |
| `model.provider` | LLM provider (only `anthropic` today) |
| `model.model` | Claude model id (default `claude-sonnet-5`) |
| `model.maxTokens` | output token budget |
| `model.effort` | reasoning effort |
| `model.thinking` | `adaptive` (default) or `off` |

**The file is the source of truth.** Every field is re-asserted into the catalog
each time the agent starts — edit the file, restart the agent, done (no rebuild).
An operator `PATCH /v1/agents/{role}/model` is a temporary override that lasts
only until that agent next restarts.

Any key you omit falls back to `modelext.Defaults` (a generic
`claude-sonnet-5` / 16k / adaptive). Omit the whole `model:` block and the agent
still starts on those defaults. Everything after the closing `---` is the system
prompt (required, non-empty).
