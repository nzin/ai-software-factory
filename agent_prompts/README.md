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

## Optional front-matter

A file may begin with a YAML front-matter block delimited by `---` lines. Its
keys override the agent's built-in metadata (used for catalog registration and
the AgentCard):

```markdown
---
name: Planner
description: Turns a PRD into a concrete implementation plan.
skills: [planning, architecture, breakdown]
---
You are a senior software architect …
```

| Key | Effect |
|---|---|
| `name` | Agent display name |
| `description` | Agent description |
| `skills` | List of skill tags |

Front-matter wins over the code defaults. Any key you omit falls back to the
code default. Everything after the closing `---` is the system prompt (required,
non-empty).
