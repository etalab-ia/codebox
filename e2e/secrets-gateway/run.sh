#!/usr/bin/env bash
# Cross-service smoke test for the #385 development-secrets gateway.
# It uses dummy Scaleway/ProConnect values and never calls either provider:
# /healthz only reports the configured store kind. The codebox CLI check is
# expected to fail closed without an OS-stored gateway session token.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'kill "${gateway_pid:-}" 2>/dev/null || true; rm -rf "$tmp"' EXIT

cd "$repo_root"
go -C apps/secrets-gateway build -o "$tmp/secrets-gateway" ./cmd/secrets-gateway

PORT=18080 \
PORT_LISTEN=127.0.0.1:18080 \
GATEWAY_PUBLIC_URL=https://gateway.test.local \
GATEWAY_TOKEN_SEED="$(python3 -c 'import base64,os;print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))')" \
PROCONNECT_CLIENT_ID=test-client \
PROCONNECT_CLIENT_SECRET=test-secret \
SCW_SECRET_KEY=dummy-not-used \
SCW_PROJECT_ID=00000000-0000-4000-8000-000000000000 \
SCW_REGION=fr-par \
"$tmp/secrets-gateway" >"$tmp/gateway.log" 2>&1 &
gateway_pid=$!

for _ in $(seq 1 50); do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null; then
    break
  fi
  sleep 0.1
done
curl -fsS http://127.0.0.1:18080/healthz | grep -q '"status":"ok"'

# Build the CLI with the same public gateway URL. `secrets login` is never
# called, so `secrets check` must report the missing session rather than
# attempting a network call with a fabricated token.
cd "$repo_root/apps/codebox"
export GATEWAY_URL="https://gateway.test.local"
go build -trimpath -ldflags="-X github.com/etalab-ia/codebox/apps/codebox/internal/codebox.SecretsGatewayURLDefault=$GATEWAY_URL" \
  -o "$tmp/codebox" ./cmd/codebox
set +e
CODEBOX_SECRETS_GATEWAY_URL="" "$tmp/codebox" secrets check >"$tmp/codebox.out" 2>&1
code=$?
set -e
if [ "$code" -eq 0 ]; then
  echo "codebox secrets check unexpectedly succeeded without a session token" >&2
  cat "$tmp/codebox.out" >&2
  exit 1
fi
grep -q "no gateway session" "$tmp/codebox.out"

echo "secrets-gateway e2e smoke: health OK, CLI failed closed without a session"
