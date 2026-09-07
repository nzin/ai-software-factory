# Base image for every Go service in the factory (catalog, coordinator, and the
# LLM-only agents). The code-reviewer and security-reviewer images extend this
# one (Dockerfile.codereview / Dockerfile.secreview).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /out/ /app/bin/
COPY agent_prompts/ /app/agent_prompts/
ENV ASF_PROMPTS_DIR=/app/agent_prompts
# service selected via `command:` in docker-compose
