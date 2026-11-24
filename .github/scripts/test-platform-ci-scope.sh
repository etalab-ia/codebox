#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
CLASSIFIER="$SCRIPT_DIR/platform-ci-scope.sh"
checks=0

check() {
  local name="$1"
  local event_name="$2"
  local files="$3"
  local expected="$4"
  local actual

  actual=$(printf '%s' "$files" | bash "$CLASSIFIER" "$event_name")
  if [[ "$actual" != "run_platforms=$expected" ]]; then
    printf 'not ok - %s: expected run_platforms=%s, got %s\n' "$name" "$expected" "$actual" >&2
    return 1
  fi
  checks=$((checks + 1))
}

check 'codebox-server release pair' pull_request $'.github/release-please/codebox-server.manifest.json\napps/codebox-server/CHANGELOG.md\n' false
check 'codebox-server manifest only' pull_request $'.github/release-please/codebox-server.manifest.json\n' false
check 'gateway changelog only' pull_request $'apps/codebox-server/CHANGELOG.md\n' false
check 'empty pull request diff' pull_request '' true
check 'codebox manifest only' pull_request $'.github/release-please/codebox.manifest.json\n' true
check 'codebox release pair' pull_request $'.github/release-please/codebox.manifest.json\napps/codebox/CHANGELOG.md\n' true
check 'legacy root manifest is not skipped' pull_request $'.release-please-manifest.json\n' true
check 'CLI change mixed with release metadata' pull_request $'.github/release-please/codebox-server.manifest.json\napps/codebox-server/CHANGELOG.md\napps/codebox/cmd/codebox/main.go\n' true
check 'shared package change' pull_request $'packages/secrets/contract.go\n' true
check 'gateway source change' pull_request $'apps/codebox-server/internal/gateway/server.go\n' true
check 'workflow change mixed with release metadata' pull_request $'apps/codebox-server/CHANGELOG.md\n.github/workflows/ci.yml\n' true
check 'codebox release configuration change' pull_request $'.github/release-please/codebox.config.json\n' true
check 'codebox-server release configuration change' pull_request $'.github/release-please/codebox-server.config.json\n' true
check 'changelog prefix is not an exact match' pull_request $'apps/codebox-server/CHANGELOG.md.backup\n' true
check 'main push always runs platforms' push $'.github/release-please/codebox-server.manifest.json\napps/codebox-server/CHANGELOG.md\n' true

printf 'ok - %d platform CI scope checks passed\n' "$checks"
