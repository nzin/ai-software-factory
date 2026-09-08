PWD    := $(shell pwd)
GOPATH := $(shell go env GOPATH)
SWAGGER := $(GOPATH)/bin/swagger

SERVICES := catalog coordinator

# Load local secrets/config from .env (git-ignored) and export them to recipes.
# Put ANTHROPIC_API_KEY=sk-ant-... in .env (see .env.example).
ifneq (,$(wildcard .env))
include .env
export
endif

KODUS_DIR := .kodus

UI_DIR := browser/asf-ui

.PHONY: all gen build build_ui run_ui test test_ui vet demo tools clean up down logs compose-build local-git reset-workspace kodus-up kodus-down kodus-bootstrap

all: gen build_ui build test

## tools: install pinned build tools (go-swagger)
tools:
	@go install github.com/go-swagger/go-swagger/cmd/swagger

## gen: regenerate the go-swagger server + client for every service, preserving
## the hand-edited configure_<svc>.go files.
gen:
	@for svc in $(SERVICES); do \
		echo ">> $$svc: validate"; \
		$(SWAGGER) validate api/$$svc.swagger.yml; \
		cp internal/$$svc/gen/restapi/configure_$$svc.go /tmp/configure_$$svc.go 2>/dev/null || true; \
		echo ">> $$svc: generate server"; \
		$(SWAGGER) generate server -q -t internal/$$svc/gen -f api/$$svc.swagger.yml -A $$svc --exclude-main; \
		echo ">> $$svc: generate client"; \
		$(SWAGGER) generate client -q -t internal/$$svc/gen -f api/$$svc.swagger.yml -A $$svc; \
		cp /tmp/configure_$$svc.go internal/$$svc/gen/restapi/configure_$$svc.go 2>/dev/null || true; \
	done
	@go mod tidy

## build: compile every binary into ./bin
## Deliberately Go-only: the coordinator serves the SPA if browser/asf-ui/dist
## exists and just serves the API if it does not, so nobody needs Node to
## compile, test or run the factory headless. Use `make build_ui` for the UI.
build:
	@mkdir -p bin
	@go build -o bin/ ./cmd/...
	@echo "built: $$(ls bin)"

## build_ui: build the Vue SPA into browser/asf-ui/dist (needs Node >= 20)
build_ui:
	@cd $(UI_DIR) && (test -d node_modules || npm ci) && npm run build
	@echo "built: $(UI_DIR)/dist"

## run_ui: Vite dev server on :5173, proxying /v1 to a local coordinator
run_ui:
	@cd $(UI_DIR) && (test -d node_modules || npm ci) && npm run dev

test:
	@go test ./...

test_ui:
	@cd $(UI_DIR) && (test -d node_modules || npm ci) && npm test

vet:
	@go vet ./...

## demo: run the full local stack against a sample PRD
## (needs ANTHROPIC_API_KEY, from .env or the environment)
demo: build
	@# ANTHROPIC_API_KEY is exported from .env above; reference it via the shell
	@# ($$VAR) so it never appears in `make -n` output.
	@if [ -z "$$ANTHROPIC_API_KEY" ]; then \
		echo "ANTHROPIC_API_KEY is not set. Create a .env file (see .env.example)."; \
		exit 1; \
	fi
	@./scripts/demo.sh

## compose-build: build the base image, then every service image
compose-build:
	@docker build -t ai-software-factory-base:latest -f Dockerfile .
	@docker compose build

## local-git: create the shared repo that empty-repoURL runs clone (and push back to)
local-git:
	@mkdir -p local_git/project
	@test -d local_git/project/.git || ( cd local_git/project \
		&& git init -q -b main \
		&& git config user.email "factory@local" \
		&& git config user.name "AI Software Factory" \
		&& git commit -q --allow-empty -m "chore: base" )

## up: build + start the factory (catalog, coordinator, all agents)
up: compose-build local-git
	@docker compose up -d
	@echo "web UI:      http://localhost:8090"
	@echo "coordinator: http://localhost:8090   catalog: http://localhost:8080"
	@echo "local_git:   ./local_git  (the git remote runs push their branches to)"

## down: stop the factory
down:
	@docker compose down

## reset-workspace: delete the local_git remote and every branch runs pushed to it
reset-workspace:
	@rm -rf local_git

logs:
	@docker compose logs -f --tail=100

## kodus-up: clone kodus-installer, start the self-hosted Kodus stack, then run
## the headless bootstrap (kodus-bootstrap) to write KODUS_TEAM_KEY into .env.
kodus-up:
	@test -d $(KODUS_DIR) || git clone --depth 1 https://github.com/kodustech/kodus-installer $(KODUS_DIR)
	@if [ ! -f $(KODUS_DIR)/.env ]; then \
		cp $(KODUS_DIR)/.env.example $(KODUS_DIR)/.env; \
		bash $(KODUS_DIR)/scripts/generate-secrets.sh; \
		key=$$(grep '^ANTHROPIC_API_KEY=' .env | cut -d= -f2-); \
		sed -i.bak "s|^API_OPEN_AI_API_KEY=.*|API_OPEN_AI_API_KEY=$$key|; \
			s|^API_OPENAI_FORCE_BASE_URL=.*|API_OPENAI_FORCE_BASE_URL=https://api.anthropic.com/v1/|; \
			s|^API_LLM_PROVIDER_MODEL=.*|API_LLM_PROVIDER_MODEL=claude-sonnet-5|" $(KODUS_DIR)/.env; \
		rm -f $(KODUS_DIR)/.env.bak; \
	fi
	@for n in shared-network monitoring-network kodus-backend-services; do docker network create $$n 2>/dev/null || true; done
	@cd $(KODUS_DIR) && docker compose up -d
	@echo "Kodus API: http://localhost:3001   web: http://localhost:3000"
	@$(MAKE) --no-print-directory kodus-bootstrap

## kodus-bootstrap: create the first org/user via the Kodus API, inject the
## Anthropic key as a BYOK provider, and write KODUS_TEAM_KEY into .env.
## No web login required. Safe to re-run.
kodus-bootstrap:
	@bash scripts/kodus-bootstrap.sh

kodus-down:
	@cd $(KODUS_DIR) && docker compose down

## clean: remove build output (keeps ./local_git — use `make reset-workspace` for that)
clean:
	@rm -rf bin *.db /tmp/asf-demo* workspace $(UI_DIR)/dist
