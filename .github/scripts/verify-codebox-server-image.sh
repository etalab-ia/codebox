#!/usr/bin/env bash
# Verify that an immutable release tag still resolves to the image just built.
set -euo pipefail
set +x

DOCKER_CLI=${DOCKER_CLI:-docker}

fail() {
  printf '::error::%s\n' "$*" >&2
  exit 1
}

[[ -n "${IMAGE_REFERENCE:-}" ]] || fail 'Missing required environment variable: IMAGE_REFERENCE'
[[ -n "${EXPECTED_IMAGE_DIGEST:-}" ]] || fail 'Missing required environment variable: EXPECTED_IMAGE_DIGEST'
[[ "$EXPECTED_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || \
  fail 'EXPECTED_IMAGE_DIGEST must be a lowercase sha256 digest'
command -v "$DOCKER_CLI" >/dev/null 2>&1 || fail "Required tool is not installed: $DOCKER_CLI"
command -v jq >/dev/null 2>&1 || fail 'Required tool is not installed: jq'

MANIFEST_JSON=$("$DOCKER_CLI" buildx imagetools inspect "$IMAGE_REFERENCE" --format '{{json .Manifest}}')
ACTUAL_IMAGE_DIGEST=$(jq -r '.digest // empty' <<<"$MANIFEST_JSON")
[[ "$ACTUAL_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]] || \
  fail 'The registry returned no valid image digest'
[[ "$ACTUAL_IMAGE_DIGEST" == "$EXPECTED_IMAGE_DIGEST" ]] || \
  fail "Image digest mismatch: expected $EXPECTED_IMAGE_DIGEST, got $ACTUAL_IMAGE_DIGEST"
printf 'Verified %s at %s\n' "$IMAGE_REFERENCE" "$ACTUAL_IMAGE_DIGEST"
