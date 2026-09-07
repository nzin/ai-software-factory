#!/usr/bin/env bash
# End-to-end demo of the AI Software Factory (Phase 1).
#
#   catalog  <-- planner registers, coordinator discovers
#   planner  <-- coordinator sends the PRD over A2A, gets a plan back
#
# Requires ANTHROPIC_API_KEY (the planner makes a real Claude call).
set -euo pipefail

cd "$(dirname "$0")/.."

# Load .env (git-ignored) if present, so `./scripts/demo.sh` works standalone too.
if [[ -f .env ]]; then
  set -a; source .env; set +a
fi

if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ANTHROPIC_API_KEY is not set - put it in .env (see .env.example) or export it." >&2
  exit 1
fi

DB="/tmp/asf-demo.db"
PLAN_OUT="/tmp/asf-demo-plan.md"
rm -f "$DB" "$PLAN_OUT"

go build -o bin/ ./cmd/...

cleanup() { kill "${CATALOG_PID:-}" "${PLANNER_PID:-}" 2>/dev/null || true; }
trap cleanup EXIT

echo "== starting catalog on :8080 =="
./bin/catalog --addr 127.0.0.1:8080 --db "$DB" &
CATALOG_PID=$!
until curl -sf 127.0.0.1:8080/healthz >/dev/null; do sleep 0.2; done

echo "== starting planner agent on :9101 =="
# run from the repo root so the default --prompts-dir (agent_prompts) resolves
./bin/agent-planner --addr 127.0.0.1:9101 --public-url http://127.0.0.1:9101 --catalog-url http://127.0.0.1:8080 &
PLANNER_PID=$!
until curl -sf 127.0.0.1:9101/.well-known/agent-card.json >/dev/null; do sleep 0.2; done

echo
echo "== catalog view of the planner =="
curl -s 127.0.0.1:8080/v1/agents/planner | python3 -m json.tool

echo
echo "== planner's public AgentCard (note the model extension) =="
curl -s 127.0.0.1:9101/.well-known/agent-card.json | python3 -m json.tool

echo
echo "== submitting docs/sample-prd.md through the coordinator =="
./bin/coordinator submit --prd docs/sample-prd.md --out "$PLAN_OUT" --catalog-url http://127.0.0.1:8080

echo
echo "== generated plan ($PLAN_OUT) =="
cat "$PLAN_OUT"

echo
echo "== model override: switch planner to claude-sonnet-5 =="
curl -s -X PATCH 127.0.0.1:8080/v1/agents/planner/model \
  -H 'content-type: application/json' -d '{"model":"claude-sonnet-5"}' | python3 -m json.tool
echo "(restart agent-planner to pick up the new model)"
