#!/usr/bin/env bash
#
# Headless Kodus onboarding — no web login page.
#
# Drives the self-hosted Kodus API (started by `make kodus-up`) entirely over its
# public REST endpoints:
#
#   1. POST /auth/signUp                         -> first user + org + team (OWNER)
#   2. POST /auth/login                          -> JWT
#   3. GET  /team/                               -> team uuid
#   4. POST /organization-parameters/create-or-update  (key=byok_config)
#                                               -> inject the Anthropic API key
#   5. POST /teams/{teamId}/cli-keys             -> mint KODUS_TEAM_KEY
#   6. upsert KODUS_TEAM_KEY / KODUS_API_URL into ./.env
#
# Idempotent: re-running reuses the account in .kodus/bootstrap.json, re-injects
# the key, and rotates the CLI key.
#
# Env:
#   ANTHROPIC_API_KEY   sourced from ./.env if not already exported (Makefile
#                       exports it). Without it, the BYOK step is skipped and
#                       Kodus falls back to the LLM keys in .kodus/.env.
#   KODUS_API_URL_LOCAL host-side API base for THIS script (default
#                       http://localhost:3001).
#   KODUS_API_URL       value written to ./.env for the containerised
#                       code-reviewer (default http://host.docker.internal:3001).
#   KODUS_MODEL         BYOK model id to try first (default claude-sonnet-5).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

ENV_FILE="$REPO_ROOT/.env"
STATE_FILE="$REPO_ROOT/.kodus/bootstrap.json"
API="${KODUS_API_URL_LOCAL:-http://localhost:3001}"
API_URL_FOR_ENV="${KODUS_API_URL:-http://host.docker.internal:3001}"
KEY_NAME="ai-software-factory"

log()  { printf '\033[1;34m>>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

command -v curl    >/dev/null || die "curl is required"
command -v python3 >/dev/null || die "python3 is required"

# --- read a value out of a KEY=VALUE .env file (no export needed) -------------
# single-process awk: no pipe, so nothing for `set -o pipefail` to trip on.
env_get() {
    [ -f "$ENV_FILE" ] || return 0
    awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,"");print;exit}' "$ENV_FILE"
}

: "${ANTHROPIC_API_KEY:=$(env_get ANTHROPIC_API_KEY)}"

# --- JSON helpers ------------------------------------------------------------
# jget <json-string> <python-expression on `d`>  (d = parsed JSON, or {} on error)
jget() {
    python3 - "$2" <<'PY' "$1"
import json, sys
expr = sys.argv[1]
raw = sys.argv[2]
try:
    d = json.loads(raw)
except Exception:
    d = {}
try:
    v = eval(expr, {"d": d})
except Exception:
    v = ""
print("" if v is None else v)
PY
}

# --- 1. wait for the API ----------------------------------------------------
log "waiting for Kodus API at $API ..."
for i in $(seq 1 90); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$API/team/" || true)"
    # any real HTTP status (not 000 = connection refused) means the API is up
    case "$code" in 000|"") ;; *) break ;; esac
    [ "$i" = 90 ] && die "Kodus API not reachable at $API after 180s (is 'make kodus-up' done?)"
    sleep 2
done
log "API is up"

# --- 2. credentials -------------------------------------------------------
mkdir -p "$REPO_ROOT/.kodus"
if [ -f "$STATE_FILE" ]; then
    EMAIL="$(jget "$(cat "$STATE_FILE")" "d.get('email')")"
    PASSWORD="$(jget "$(cat "$STATE_FILE")" "d.get('password')")"
    NAME="$(jget "$(cat "$STATE_FILE")" "d.get('name') or 'AI Software Factory'")"
    # discard a stored account whose email can't pass @IsEmail (e.g. an older
    # run that used factory@localhost) so it gets regenerated below.
    case "$EMAIL" in *@*.*) ;; *) EMAIL=""; PASSWORD="" ;; esac
fi
if [ -z "${EMAIL:-}" ] || [ -z "${PASSWORD:-}" ]; then
    # must satisfy class-validator @IsEmail (needs a real domain with a TLD —
    # "factory@localhost" is rejected). Override with KODUS_BOOTSTRAP_EMAIL.
    EMAIL="${KODUS_BOOTSTRAP_EMAIL:-factory@example.com}"
    NAME="AI Software Factory"
    # strong password (IsStrongPassword: >=8, lower+upper+digit+symbol).
    # python3, not `tr </dev/urandom | head` — the latter SIGPIPEs `tr` and,
    # under `set -o pipefail`, aborts the script (exit 141).
    PASSWORD="$(python3 -c 'import secrets, string
a = string.ascii_letters + string.digits
print("Asf1!" + "".join(secrets.choice(a) for _ in range(20)))')"
    python3 - "$STATE_FILE" "$EMAIL" "$PASSWORD" "$NAME" <<'PY'
import json, os, sys
path, email, password, name = sys.argv[1:5]
json.dump({"email": email, "password": password, "name": name}, open(path, "w"), indent=2)
os.chmod(path, 0o600)
PY
    log "generated a new bootstrap account ($EMAIL) -> ${STATE_FILE#$REPO_ROOT/}"
else
    log "reusing bootstrap account ($EMAIL) from ${STATE_FILE#$REPO_ROOT/}"
fi

json_field() { python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1"; }

# --- 3. sign up (idempotent) --------------------------------------------
log "POST /auth/signUp"
signup_body="{\"name\":$(json_field "$NAME"),\"email\":$(json_field "$EMAIL"),\"password\":$(json_field "$PASSWORD")}"
code="$(curl -s -o /tmp/kodus_signup.$$ -w '%{http_code}' \
    -H 'Content-Type: application/json' -d "$signup_body" "$API/auth/signUp" || true)"
case "$code" in
    2*)  log "  created" ;;
    409) log "  already exists — ok" ;;
    *)   warn "  signUp returned HTTP $code: $(cat /tmp/kodus_signup.$$ 2>/dev/null)" ;;
esac
rm -f /tmp/kodus_signup.$$

# --- 4. log in ---------------------------------------------------------
log "POST /auth/login"
login_body="{\"email\":$(json_field "$EMAIL"),\"password\":$(json_field "$PASSWORD")}"
login_res="$(curl -s -H 'Content-Type: application/json' -d "$login_body" "$API/auth/login" || true)"
TOKEN="$(jget "$login_res" "d.get('data',{}).get('accessToken') or d.get('accessToken')")"
[ -n "$TOKEN" ] || die "login failed: $login_res"
AUTH=(-H "Authorization: Bearer $TOKEN")

# --- 5. team id ------------------------------------------------------
log "GET /team/"
team_res="$(curl -s "${AUTH[@]}" "$API/team/" || true)"
TEAM_ID="$(jget "$team_res" "(d.get('data') or [{}])[0].get('uuid')")"
[ -n "$TEAM_ID" ] || die "could not resolve team id: $team_res"
log "  team $TEAM_ID"

# --- 6. inject the Anthropic key as a BYOK provider ----------------------
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
    tried=""
    ok=""
    for model in "${KODUS_MODEL:-claude-sonnet-5}" claude-sonnet-4-5-20250929 claude-3-5-sonnet-20241022; do
        case " $tried " in *" $model "*) continue ;; esac
        tried="$tried $model"
        log "POST /organization-parameters/create-or-update (byok_config, model=$model)"
        byok=$(python3 - "$ANTHROPIC_API_KEY" "$model" <<'PY'
import json, sys
api_key, model = sys.argv[1], sys.argv[2]
print(json.dumps({
    "key": "byok_config",
    "configValue": {
        "version": 2,
        "credentials": [
            {"id": "anthropic-main", "provider": "anthropic", "apiKey": api_key, "settings": {}}
        ],
        "models": [
            {"id": "primary", "credentialId": "anthropic-main", "model": model}
        ],
        "routing": {"mode": "manual", "defaultModelId": "primary"},
    },
}))
PY
)
        code="$(curl -s -o /tmp/kodus_byok.$$ -w '%{http_code}' "${AUTH[@]}" \
            -H 'Content-Type: application/json' -d "$byok" \
            "$API/organization-parameters/create-or-update" || true)"
        case "$code" in
            2*) rm -f /tmp/kodus_byok.$$ ;;
            *)  warn "  save returned HTTP $code: $(cat /tmp/kodus_byok.$$ 2>/dev/null)"
                rm -f /tmp/kodus_byok.$$
                continue ;;
        esac
        # the save itself does not validate the model id (configValue is
        # free-form) — probe it against the provider. verdict: ok | bad | unknown
        probe="$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
            -d "{\"provider\":\"anthropic\",\"model\":$(json_field "$model")}" \
            "$API/organization-parameters/test-byok-model" || true)"
        verdict="$(jget "$probe" "(lambda x: 'ok' if (x.get('ok') is True or x.get('valid') is True or x.get('success') is True) else ('bad' if (x.get('ok') is False or x.get('valid') is False or x.get('error') or x.get('message')) else 'unknown'))(d.get('data', d) if isinstance(d, dict) else {})")"
        case "$verdict" in
            ok)      log "  BYOK configured with $model (validated)"; ok=1; break ;;
            unknown) warn "  could not validate $model — keeping it (save succeeded)"; ok=1; break ;;
            *)       warn "  test-byok-model rejected $model: $probe" ;;
        esac
    done
    [ -n "$ok" ] || warn "no BYOK model accepted; Kodus will use the LLM keys from .kodus/.env"
else
    warn "ANTHROPIC_API_KEY not set — skipping BYOK injection (Kodus uses .kodus/.env keys)"
fi

# --- 7. mint a CLI team key --------------------------------------------
log "POST /teams/$TEAM_ID/cli-keys"
# a key with our name may already exist and can't be re-read — drop it first
existing="$(curl -s "${AUTH[@]}" "$API/teams/$TEAM_ID/cli-keys" || true)"
old_id="$(KEY_NAME="$KEY_NAME" python3 - "$existing" <<'PY'
import json, os, sys
try:
    d = json.loads(sys.argv[1])
except Exception:
    d = {}
name = os.environ["KEY_NAME"]
print(next((k.get("uuid", "") for k in (d.get("data") or []) if k.get("name") == name), ""))
PY
)"
if [ -n "$old_id" ]; then
    log "  rotating existing key $old_id"
    curl -s -o /dev/null "${AUTH[@]}" -X DELETE "$API/teams/$TEAM_ID/cli-keys/$old_id" || true
fi
key_res="$(curl -s "${AUTH[@]}" -H 'Content-Type: application/json' \
    -d "{\"name\":$(json_field "$KEY_NAME")}" "$API/teams/$TEAM_ID/cli-keys" || true)"
TEAM_KEY="$(jget "$key_res" "d.get('data',{}).get('key') or d.get('key')")"
[ -n "$TEAM_KEY" ] || die "could not mint a team key: $key_res"

# --- 8. persist -------------------------------------------------------
upsert_env() { # key value
    touch "$ENV_FILE"
    if grep -q "^$1=" "$ENV_FILE"; then
        python3 - "$ENV_FILE" "$1" "$2" <<'PY'
import sys
path, key, val = sys.argv[1:4]
lines = open(path).read().splitlines()
out = [f"{key}={val}" if l.startswith(key + "=") else l for l in lines]
open(path, "w").write("\n".join(out) + "\n")
PY
    else
        printf '%s=%s\n' "$1" "$2" >>"$ENV_FILE"
    fi
}
upsert_env KODUS_API_URL  "$API_URL_FOR_ENV"
upsert_env KODUS_TEAM_KEY "$TEAM_KEY"

python3 - "$STATE_FILE" "$TEAM_KEY" "$TEAM_ID" <<'PY'
import json, sys
path, team_key, team_id = sys.argv[1:4]
d = json.load(open(path))
d.update(teamKey=team_key, teamId=team_id)
json.dump(d, open(path, "w"), indent=2)
PY

log "done — KODUS_TEAM_KEY written to .env"
log "next: 'make up' (or restart agent-code-reviewer) to pick it up"
