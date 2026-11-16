#!/usr/bin/env bash
# Reconcile the public codebox secrets-gateway with the Scaleway CLI.
# Required credentials and configuration arrive through the environment.
set -euo pipefail
set +x
umask 077

SCW_CLI=${SCW_CLI:-scw}
REGION=${SCW_DEFAULT_REGION:-fr-par}
NAMESPACE_NAME=codebox-secrets-gateway
CONTAINER_NAME=codebox-secrets-gateway
SEED_SECRET_NAME=gateway-token-seed
SEED_SECRET_PATH=/gateway
PROCONNECT_ISSUER=${PROCONNECT_ISSUER:-https://fca.integ01.dev-agentconnect.fr/api/v2}
GATEWAY_ALLOWED_SIRETS=${GATEWAY_ALLOWED_SIRETS:-}
PUBLIC_URL=${CODEBOX_SECRETS_GATEWAY_URL:-https://bootstrap.invalid}

fail() {
  printf '::error::%s\n' "$*" >&2
  exit 1
}

require_env() {
  local name
  for name in "$@"; do
    if [[ -z "${!name:-}" ]]; then
      fail "Missing required environment variable: $name"
    fi
  done
}

require_env \
  SCW_ACCESS_KEY \
  SCW_SECRET_KEY \
  SCW_DEFAULT_ORGANIZATION_ID \
  SCW_DEFAULT_PROJECT_ID \
  SCW_REGISTRY_NAMESPACE \
  PROCONNECT_CLIENT_ID \
  PROCONNECT_CLIENT_SECRET \
  IMAGE_TAG

for tool in "$SCW_CLI" jq openssl; do
  command -v "$tool" >/dev/null 2>&1 || fail "Required tool is not installed: $tool"
done

case "$REGION" in
  fr-par | nl-ams | pl-waw) ;;
  *) fail "Unsupported gateway region: $REGION" ;;
esac
[[ "$SCW_DEFAULT_ORGANIZATION_ID" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]] || \
  fail 'SCW_DEFAULT_ORGANIZATION_ID must be the organization UUID'
[[ "$SCW_DEFAULT_PROJECT_ID" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]] || \
  fail 'SCW_DEFAULT_PROJECT_ID must be the project UUID, not its name'
[[ "$SCW_REGISTRY_NAMESPACE" =~ ^[a-z0-9][a-z0-9-]*$ ]] || \
  fail 'SCW_REGISTRY_NAMESPACE must be the registry namespace name'
[[ "$IMAGE_TAG" =~ ^(codebox-server|secrets-gateway)-v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]] || \
  fail 'IMAGE_TAG must be an immutable codebox-server release tag'
[[ "$PUBLIC_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || \
  fail 'CODEBOX_SECRETS_GATEWAY_URL must be an HTTPS origin without a path or trailing slash'

IMAGE_REFERENCE="rg.$REGION.scw.cloud/$SCW_REGISTRY_NAMESPACE/codebox-server:$IMAGE_TAG"
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

NAMESPACES_JSON=$("$SCW_CLI" container namespace list \
  "project-id=$SCW_DEFAULT_PROJECT_ID" "name=$NAMESPACE_NAME" "region=$REGION" -o json)
NAMESPACE_COUNT=$(jq 'length' <<<"$NAMESPACES_JSON") || fail 'Could not parse the container namespace list'
[[ "$NAMESPACE_COUNT" -le 1 ]] || fail 'Multiple container namespaces match the deployment name'
if [[ "$NAMESPACE_COUNT" -eq 0 ]]; then
  NAMESPACE_JSON=$("$SCW_CLI" container namespace create \
    "project-id=$SCW_DEFAULT_PROJECT_ID" \
    "name=$NAMESPACE_NAME" \
    'description=Stateless development-secrets gateway for codebox' \
    "region=$REGION" --wait -o json)
  NAMESPACE_ID=$(jq -r '.id // empty' <<<"$NAMESPACE_JSON")
else
  NAMESPACE_ID=$(jq -r '.[0].id // empty' <<<"$NAMESPACES_JSON")
fi
[[ -n "$NAMESPACE_ID" ]] || fail 'Scaleway returned no container namespace ID'

SECRETS_JSON=$("$SCW_CLI" secret secret list \
  "project-id=$SCW_DEFAULT_PROJECT_ID" \
  "name=$SEED_SECRET_NAME" "path=$SEED_SECRET_PATH" "region=$REGION" -o json)
SECRETS_JSON=$(jq '[.[] | select(.name == $name and .path == $path)]' \
  --arg name "$SEED_SECRET_NAME" --arg path "$SEED_SECRET_PATH" <<<"$SECRETS_JSON") || \
  fail 'Could not parse the token-seed secret list'
SECRET_COUNT=$(jq 'length' <<<"$SECRETS_JSON")
[[ "$SECRET_COUNT" -le 1 ]] || fail 'Multiple Secret Manager entries match the token-seed name and path'
if [[ "$SECRET_COUNT" -eq 0 ]]; then
  SECRET_JSON=$("$SCW_CLI" secret secret create \
    "project-id=$SCW_DEFAULT_PROJECT_ID" \
    "name=$SEED_SECRET_NAME" "path=$SEED_SECRET_PATH" \
    'description=codebox gateway session-token signing seed' \
    'type=opaque' 'protected=true' "region=$REGION" -o json)
  SEED_SECRET_ID=$(jq -r '.id // empty' <<<"$SECRET_JSON")
else
  SEED_SECRET_ID=$(jq -r '.[0].id // empty' <<<"$SECRETS_JSON")
fi
[[ -n "$SEED_SECRET_ID" ]] || fail 'Scaleway returned no token-seed secret ID'

ENABLED_VERSIONS_JSON=$("$SCW_CLI" secret version list "$SEED_SECRET_ID" \
  'status.0=enabled' "region=$REGION" -o json)
ENABLED_VERSION_COUNT=$(jq 'length' <<<"$ENABLED_VERSIONS_JSON") || \
  fail 'Could not parse the token-seed version list'
if [[ "$ENABLED_VERSION_COUNT" -eq 0 ]]; then
  SEED_FILE="$WORK_DIR/token-seed"
  openssl rand 32 | base64 | tr '+/' '-_' | tr -d '=\n' >"$SEED_FILE"
  [[ "$(wc -c <"$SEED_FILE" | tr -d ' ')" == 43 ]] || fail 'Could not generate a valid gateway token seed'
  "$SCW_CLI" secret version create "$SEED_SECRET_ID" \
    "data=@$SEED_FILE" \
    'description=Initial codebox gateway signing seed' \
    'disable-previous=false' "region=$REGION" -o json >/dev/null
  rm -f "$SEED_FILE"
fi

GATEWAY_TOKEN_SEED=$("$SCW_CLI" secret version access "$SEED_SECRET_ID" \
  'revision=latest_enabled' 'raw=true' "region=$REGION")
[[ "$GATEWAY_TOKEN_SEED" =~ ^[A-Za-z0-9_-]{43}$ ]] || \
  fail 'The stored gateway token seed has an unexpected format'
printf '::add-mask::%s\n' "$GATEWAY_TOKEN_SEED"

CONTAINERS_JSON=$("$SCW_CLI" container container list \
  "project-id=$SCW_DEFAULT_PROJECT_ID" "namespace-id=$NAMESPACE_ID" \
  "name=$CONTAINER_NAME" "region=$REGION" -o json)
CONTAINER_COUNT=$(jq 'length' <<<"$CONTAINERS_JSON") || fail 'Could not parse the container list'
[[ "$CONTAINER_COUNT" -le 1 ]] || fail 'Multiple containers match the deployment name'

CONTAINER_ARGS=(
  'description=Stateless development-secrets gateway for codebox'
  "image=$IMAGE_REFERENCE"
  'port=8080'
  'mvcpu-limit=512'
  'memory-limit-bytes=268435456'
  'min-scale=1'
  'max-scale=1'
  'privacy=public'
  'protocol=http1'
  "environment-variables.GATEWAY_PUBLIC_URL=$PUBLIC_URL"
  "environment-variables.PROCONNECT_ISSUER=$PROCONNECT_ISSUER"
  "environment-variables.SCW_PROJECT_ID=$SCW_DEFAULT_PROJECT_ID"
  "environment-variables.SCW_REGION=$REGION"
  "environment-variables.GATEWAY_ALLOWED_SIRETS=$GATEWAY_ALLOWED_SIRETS"
  "secret-environment-variables.GATEWAY_TOKEN_SEED=$GATEWAY_TOKEN_SEED"
  "secret-environment-variables.PROCONNECT_CLIENT_ID=$PROCONNECT_CLIENT_ID"
  "secret-environment-variables.PROCONNECT_CLIENT_SECRET=$PROCONNECT_CLIENT_SECRET"
  "secret-environment-variables.SCW_SECRET_KEY=$SCW_SECRET_KEY"
)

if [[ "$CONTAINER_COUNT" -eq 0 ]]; then
  CONTAINER_JSON=$("$SCW_CLI" container container create \
    "namespace-id=$NAMESPACE_ID" \
    "name=$CONTAINER_NAME" \
    "${CONTAINER_ARGS[@]}" \
    'https-connections-only=true' "region=$REGION" --wait -o json)
  CONTAINER_ID=$(jq -r '.id // empty' <<<"$CONTAINER_JSON")
else
  CONTAINER_ID=$(jq -r '.[0].id // empty' <<<"$CONTAINERS_JSON")
  [[ -n "$CONTAINER_ID" ]] || fail 'Scaleway returned no existing container ID'
  CONTAINER_JSON=$("$SCW_CLI" container container update "$CONTAINER_ID" \
    "${CONTAINER_ARGS[@]}" \
    'https-connection-only=true' "region=$REGION" --wait -o json)
fi
[[ -n "$CONTAINER_ID" ]] || fail 'Scaleway returned no deployed container ID'

CONTAINER_JSON=$("$SCW_CLI" container container get "$CONTAINER_ID" "region=$REGION" -o json)
CONTAINER_STATUS=$(jq -r '.status // empty' <<<"$CONTAINER_JSON")
CONTAINER_DOMAIN=$(jq -r '.domain_name // .public_endpoint // empty' <<<"$CONTAINER_JSON")
[[ "$CONTAINER_STATUS" == ready ]] || fail "Gateway container is not ready (status: $CONTAINER_STATUS)"
[[ -n "$CONTAINER_DOMAIN" ]] || fail 'Scaleway returned no public container domain'
case "$CONTAINER_DOMAIN" in
  https://* | http://*) NATIVE_ORIGIN="$CONTAINER_DOMAIN" ;;
  *) NATIVE_ORIGIN="https://$CONTAINER_DOMAIN" ;;
esac

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    printf 'container_id=%s\n' "$CONTAINER_ID"
    printf 'container_domain_name=%s\n' "$CONTAINER_DOMAIN"
    printf 'container_status=%s\n' "$CONTAINER_STATUS"
    printf 'native_origin=%s\n' "$NATIVE_ORIGIN"
    printf 'gateway_public_url=%s\n' "$PUBLIC_URL"
  } >>"$GITHUB_OUTPUT"
fi
printf 'Gateway %s is ready; native endpoint: %s; configured public URL: %s\n' \
  "$CONTAINER_ID" "$NATIVE_ORIGIN" "$PUBLIC_URL"
