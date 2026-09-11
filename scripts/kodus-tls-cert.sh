#!/usr/bin/env bash
#
# Generates a self-signed cert/key for kodus-tls-proxy (see docker-compose.yml)
# into ./.kodus-tls/ if one doesn't already exist. Idempotent.
#
# The Kodus CLI refuses a non-localhost, non-HTTPS KODUS_API_URL and silently
# falls back to the public api.kodus.io — this cert is what lets the
# containerised code-reviewer reach the self-hosted kodus_api over HTTPS
# instead. agent-code-reviewer trusts it via NODE_EXTRA_CA_CERTS.

set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.kodus-tls"
CRT="$DIR/kodus-tls-proxy.crt"
KEY="$DIR/kodus-tls-proxy.key"

if [ -f "$CRT" ] && [ -f "$KEY" ]; then
    exit 0
fi

mkdir -p "$DIR"
openssl req -x509 -newkey rsa:2048 -sha256 -days 825 -nodes \
    -keyout "$KEY" -out "$CRT" \
    -subj "/CN=kodus-tls-proxy" \
    -addext "subjectAltName=DNS:kodus-tls-proxy,DNS:host.docker.internal,DNS:localhost" \
    >/dev/null 2>&1

chmod 600 "$KEY"
printf '>> generated %s\n' "${CRT#"$DIR"/../}" >&2
