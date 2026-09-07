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

.PHONY: all gen build test vet demo tools clean

all: gen build test

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
build:
	@mkdir -p bin
	@go build -o bin/ ./cmd/...
	@echo "built: $$(ls bin)"

test:
	@go test ./...

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

clean:
	@rm -rf bin *.db /tmp/asf-demo*
