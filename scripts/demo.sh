#!/usr/bin/env bash
# End-to-end demo of the AI Software Factory pipeline (Phase 3), on the host.
#
#   catalog        <- every agent registers, the coordinator discovers
#   planner        <- PRD in, prose plan + task list + approval decision out
#   (approval gate) <- if the planner flags the plan non-trivial, the run pauses
#                      at awaiting_approval; this script auto-approves it
#   ui-ux-designer <- UI spec (when there are frontend/mobile tasks)
#   backend/frontend/mobile developers <- write files into a per-run git workspace
#   security-reviewer <- gosec/govulncheck/npm-audit + an LLM pass
#   code-reviewer     <- Kodus (informational finding if KODUS_TEAM_KEY unset)
#   reviewers can bounce the work back to a developer (request_changes) until
#   they approve or the per-stage attempt cap trips.
#
# Requires ANTHROPIC_API_KEY. Kodus is optional (see scripts/kodus-setup.md).
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ -f .env ]]; then set -a; source .env; set +a; fi
if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "ANTHROPIC_API_KEY is not set - put it in .env (see .env.example)." >&2
  exit 1
fi

PRD="${1:-docs/sample-prd.md}"
DB="/tmp/asf-demo.db"
WS="/tmp/asf-demo-workspace"
rm -rf "$DB" "$WS"

go build -o bin/ ./cmd/...

PIDS=()
cleanup() { kill "${PIDS[@]}" 2>/dev/null || true; }
trap cleanup EXIT

start() { # name addr binary
  echo "== $1 on $2 =="
  "./bin/$3" --addr "127.0.0.1:$2" --public-url "http://127.0.0.1:$2" --catalog-url http://127.0.0.1:8080 &
  PIDS+=($!)
}

./bin/catalog --addr 127.0.0.1:8080 --db "$DB" & PIDS+=($!)
until curl -sf 127.0.0.1:8080/healthz >/dev/null; do sleep 0.2; done

start planner                9101 agent-planner
start ui-ux-designer         9102 agent-ui-ux-designer
start backend-developer      9103 agent-backend-developer
start frontend-developer     9104 agent-frontend-developer
start mobile-developer       9105 agent-mobile-developer
start security-reviewer      9106 agent-security-reviewer
start code-reviewer          9107 agent-code-reviewer
start build-gate             9108 agent-build-gate

for p in 9101 9102 9103 9104 9105 9106 9107 9108; do
  for _ in $(seq 1 50); do
    curl -sf "127.0.0.1:$p/.well-known/agent-card.json" >/dev/null && break
    sleep 0.2
  done
  curl -sf "127.0.0.1:$p/.well-known/agent-card.json" >/dev/null \
    || { echo "agent on :$p never came up"; exit 1; }
done

echo
echo "== registered agents =="
curl -s 127.0.0.1:8080/v1/agents | python3 -c '
import sys, json
for a in json.load(sys.stdin):
    print("  %-22s %s" % (a["role"], a["name"]))
'

echo
echo "== submitting $PRD through the coordinator (async; we poll for progress) =="
RUNSTORE="/tmp/asf-demo-runs.db"; rm -f "$RUNSTORE"
./bin/coordinator serve --addr 127.0.0.1:8090 --catalog-url http://127.0.0.1:8080 \
  --workspace-root "$WS" --runstore "$RUNSTORE" & PIDS+=($!)
until curl -sf 127.0.0.1:8090/healthz >/dev/null; do sleep 0.2; done

# repoURL: "" (default) => the run scaffolds a brand-new local repo in the workspace.
python3 -c 'import json; print(json.dumps({"markdown": open("'"$PRD"'").read(), "repoURL": "", "deadlineSeconds": 3000}))' \
  | curl -sS -X POST 127.0.0.1:8090/v1/prd -H 'content-type: application/json' -d @- \
  > /tmp/asf-demo-run.json
python3 -m json.tool < /tmp/asf-demo-run.json || { echo "no run JSON"; exit 1; }

RUN_ID=$(python3 -c 'import json; print(json.load(open("/tmp/asf-demo-run.json")).get("id",""))')
[[ -n "$RUN_ID" ]] || { echo "no run id"; exit 1; }

echo
echo "== polling run $RUN_ID =="
LAST=""
for _ in $(seq 1 600); do
  R=$(curl -sf "127.0.0.1:8090/v1/runs/$RUN_ID") || { sleep 2; continue; }
  STATUS=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]).get("status",""))' "$R")
  STAGE=$(python3 -c 'import json,sys; print(json.loads(sys.argv[1]).get("stage",""))' "$R")
  [[ "$STATUS/$STAGE" != "$LAST" ]] && { echo "  $STATUS ${STAGE:+($STAGE)}"; LAST="$STATUS/$STAGE"; }
  case "$STATUS" in
    awaiting_approval)
      REASON=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print((d.get("approval") or {}).get("reason",""))' "$R")
      echo "  --> human approval required: $REASON"
      echo "  --> approving"
      curl -sf -X POST "127.0.0.1:8090/v1/runs/$RUN_ID/approve" >/dev/null
      ;;
    pr_ready|pr_open|accepted|failed|done|needs_human_review)
      echo
      echo "== final =="
      python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print("status :", d.get("status")); print("reason :", d.get("reason")); print("prURL  :", d.get("prURL") or "(none)")' "$R"
      break
      ;;
  esac
  sleep 2
done
echo
echo "== workspace $WS/$RUN_ID/repo =="
if [[ -n "$RUN_ID" && -d "$WS/$RUN_ID/repo" ]]; then
  git -C "$WS/$RUN_ID/repo" log --oneline
  echo
  git -C "$WS/$RUN_ID/repo" ls-files | head -60
fi
