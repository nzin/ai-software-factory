# Base image for every Go service in the factory (catalog, coordinator, and the
# LLM-only agents). The code-reviewer and security-reviewer images extend this
# one (Dockerfile.codereview / Dockerfile.secreview).

# The Vue SPA the coordinator serves at /. Built here so `dist/` (git-ignored)
# never has to exist on the host.
FROM node:22-alpine AS ui
WORKDIR /ui
COPY browser/asf-ui/package.json browser/asf-ui/package-lock.json ./
RUN npm ci
COPY browser/asf-ui/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl \
 && rm -rf /var/lib/apt/lists/* \
 # The per-run workspace is a bind-mount of a host repo owned by the host user;
 # containers run as root, so git 2.35+ would otherwise refuse it as "dubious
 # ownership".
 && git config --system --add safe.directory '*'
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY agent_prompts/ /app/agent_prompts/
ENV ASF_PROMPTS_DIR=/app/agent_prompts
# Only the coordinator reads this; the other services ignore it.
COPY --from=ui /ui/dist/ /app/ui/
ENV ASF_UI_DIR=/app/ui
# service selected via `command:` in docker-compose
