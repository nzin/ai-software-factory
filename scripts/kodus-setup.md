# One-time Kodus setup

The `code-reviewer` agent shells out to the [Kodus CLI](https://github.com/kodustech/cli)
(`kodus review`) against a **self-hosted Kodus** instance. Kodus is a full stack
(web, API, workers, webhooks, MCP manager, RabbitMQ, Postgres/pgvector, Mongo)
and needs a one-time bootstrap that isn't automated here.

Until this is done, the `code-reviewer` agent still runs — it just returns a
single informational finding ("KODUS_TEAM_KEY not set") and the pipeline
completes without a Kodus review.

## 1. Start the stack

```bash
make kodus-up
```

This clones `github.com/kodustech/kodus-installer` into `.kodus/`, copies
`.env.example` to `.env`, and runs its `docker compose up -d`. The API comes up
on `http://localhost:3001`, the web UI on `http://localhost:3000`.

`make kodus-up` also pre-fills the LLM lines in `.kodus/.env` to use Claude via
Anthropic's OpenAI-compatible endpoint, matching the factory's default model:

```
API_OPEN_AI_API_KEY=<your ANTHROPIC_API_KEY>
API_OPENAI_FORCE_BASE_URL=https://api.anthropic.com/v1/
API_LLM_PROVIDER_MODEL=claude-sonnet-5
```

Edit `.kodus/.env` first if you need to change ports, DB credentials, or the
model, then `cd .kodus && docker compose up -d` again.

## 2. Configure Kodus (web UI, http://localhost:3000)

1. Create the first user / organization.
2. **LLM provider — BYOK:** confirm the Claude model (default `claude-sonnet-5`),
   or add your key here if the env pre-fill did not take. Kodus is
   model-agnostic; any OpenAI-compatible endpoint works.
3. You do **not** need to connect a GitHub/GitLab repo — the CLI reviews local
   diffs directly.

## 3. Get a team key

```bash
# inside .kodus, or with the CLI installed on the host:
npx @kodus/cli auth login          # or: kodus auth team-key --key kodus_xxx
npx @kodus/cli auth status
```

Copy the team key (`kodus_...`) into this repo's `.env`:

```
KODUS_TEAM_KEY=kodus_xxxxxxxxxxxx
KODUS_API_URL=http://host.docker.internal:3001
```

`make up` (or restarting `agent-code-reviewer`) picks these up.

## Known rough edges

- **HTTPS check:** the Kodus CLI wants HTTPS for a non-`localhost`
  `KODUS_API_URL`. `http://host.docker.internal:3001` may be rejected. If so:
  run the code-reviewer on the host instead of in a container (it then uses
  `http://localhost:3001`), or put a TLS-terminating proxy in front of
  `kodus-api`.
- **Version drift:** `kodus-installer`'s compose and `.env` schema change over
  time. If `make kodus-up` fails, follow the installer's own README in `.kodus/`.
