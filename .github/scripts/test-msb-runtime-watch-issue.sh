#!/usr/bin/env bash
# Exercises msb_runtime_watch_issue.sh with a stubbed `gh`, so the tracking
# issue logic is checked without touching GitHub.
#
# Mirrors the style of test-platform-ci-scope.sh: a flat list of checks, each
# naming the behaviour it pins.
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
TARGET="$SCRIPT_DIR/msb_runtime_watch_issue.sh"
checks=0

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"

# Stub gh: records every call and answers the GitHub operations the script makes.
cat > "$tmpdir/bin/gh" <<'STUB'
#!/usr/bin/env bash
printf 'GH-CALLED: %s\n' "$*" >> "$GH_CALLS"
if [[ "$1" == "issue" && "$2" == "list" ]]; then
  printf '%s' "${GH_LIST_RESULT:-}"
elif [[ "$1" == "release" && "$2" == "verify" ]]; then
  # The script redirects this into its attestation path, so the fixture goes to
  # stdout rather than being named in the environment.
  cat "${GH_ATTESTATION:?}"
elif [[ "$1" == "issue" && "$2" == "create" ]]; then
  printf '%s\n' "${GH_ISSUE_URL:-https://github.com/etalab-ia/codebox/issues/481}"
fi
STUB
chmod +x "$tmpdir/bin/gh"

ALL_ASSETS="microsandbox-darwin-aarch64.tar.gz microsandbox-linux-aarch64.tar.gz microsandbox-linux-x86_64.tar.gz microsandbox-windows-aarch64.tar.gz microsandbox-windows-x86_64.tar.gz"

# Build an attestation fixture covering the given assets.
write_attestation() {  # $1=path, rest=asset names
  local path="$1"; shift
  local subjects="" asset
  for asset in "$@"; do
    if [[ -n "$subjects" ]]; then
      subjects="$subjects,"
    fi
    subjects="$subjects{\"name\":\"$asset\",\"digest\":{\"sha256\":\"$(printf '%064d' 0)\"}}"
  done
  printf '{"verificationResult":{"signature":{"certificate":{"subjectAlternativeName":"https://dotcom.releases.github.com"}},"statement":{"subject":[%s]}}}\n' \
    "$subjects" > "$path"
}

attestation="$tmpdir/fixture-attestation.json"
# shellcheck disable=SC2086
write_attestation "$attestation" $ALL_ASSETS
jq -e '.verificationResult.statement.subject | length == 5' "$attestation" >/dev/null

# Run the script with the stub in PATH; extra args are env assignments.
run_script() {
  ( cd "$tmpdir" && env PATH="$tmpdir/bin:$PATH" \
      GH_CALLS="$tmpdir/calls" \
      GH_ATTESTATION="${GH_ATTESTATION_OVERRIDE:-$attestation}" \
      GITHUB_OUTPUT="$tmpdir/github-output" \
      UPSTREAM_REPO=superradcompany/microsandbox \
      EXPECTED_ASSETS="${EXPECTED_ASSETS_OVERRIDE:-$ALL_ASSETS}" \
      "$@" bash "$TARGET" )
}

reset_calls() { : > "$tmpdir/calls"; : > "$tmpdir/github-output"; rm -f "$tmpdir/issue-body.md"; }

fail() { printf 'not ok - %s\n' "$1" >&2; exit 1; }

# 1. An open issue suppresses duplicate creation but not attestation
#    verification, and its number is exported for the update PR.
check_skips_when_issue_exists() {
  reset_calls
  local out
  out=$(run_script UPSTREAM_VERSION=1.2.3 SDK_VERSION=1.2.2 GH_LIST_RESULT="481")
  case "$out" in
    *"already open (#481)"*) : ;;
    *) fail "expected the skip message, got: $out" ;;
  esac
  if ! grep -q 'release verify v1.2.3' "$tmpdir/calls"; then
    fail "an existing issue bypassed release verification"
  fi
  if grep -q 'issue create' "$tmpdir/calls"; then
    fail "created an issue despite one already being open"
  fi
  if ! grep -q '^issue_number=481$' "$tmpdir/github-output"; then
    fail "did not export the existing issue number"
  fi
  checks=$((checks + 1))
}

# 2. With no existing issue, the attestation is verified and an issue created
#    from the verified subjects.
check_creates_issue_from_verified_digests() {
  reset_calls
  run_script UPSTREAM_VERSION=1.2.3 SDK_VERSION=1.2.2 GH_LIST_RESULT="" >/dev/null
  if ! grep -q 'release verify v1.2.3' "$tmpdir/calls"; then
    fail "did not verify the upstream attestation"
  fi
  if ! grep -q 'issue create' "$tmpdir/calls"; then
    fail "did not create an issue"
  fi
  if [[ ! -f "$tmpdir/issue-body.md" ]]; then
    fail "wrote no issue body"
  fi
  if ! grep -q 'v1.2.3' "$tmpdir/issue-body.md"; then
    fail "issue body does not name the upstream version"
  fi
  if ! grep -q '^issue_number=481$' "$tmpdir/github-output"; then
    fail "did not export the created issue number"
  fi
  checks=$((checks + 1))
}

# 3. An asset absent from the attestation fails closed: no issue is opened
#    carrying unverified digests.
check_fails_closed_when_asset_uncovered() {
  reset_calls
  local partial="$tmpdir/partial.json"
  write_attestation "$partial" microsandbox-darwin-aarch64.tar.gz
  if GH_ATTESTATION_OVERRIDE="$partial" \
      run_script UPSTREAM_VERSION=1.2.3 SDK_VERSION=1.2.2 GH_LIST_RESULT="" >/dev/null 2>&1; then
    fail "expected a failure when an expected asset is not covered"
  fi
  if grep -q 'issue create' "$tmpdir/calls"; then
    fail "opened an issue from an attestation missing an asset"
  fi
  checks=$((checks + 1))
}

check_skips_when_issue_exists
check_creates_issue_from_verified_digests
check_fails_closed_when_asset_uncovered

# 4. The workflow must keep gating the step on a version mismatch. The script
#    itself does not compare SDK_VERSION with UPSTREAM_VERSION, so removing
#    this condition would open an update issue for the version already
#    embedded on every scheduled run.
check_workflow_gates_on_version_mismatch() {
  local workflow
  workflow=$(cat "$SCRIPT_DIR/../workflows/msb-runtime-watch.yml")
  local step
  step=${workflow#*- name: Open or update the tracking issue}
  step=${step%%run:*}
  case "$step" in
    *"if: steps.versions.outputs.upstream_version != steps.versions.outputs.sdk_version"*) : ;;
    *) fail "the tracking-issue step lost its version-mismatch condition" ;;
  esac
  checks=$((checks + 1))
}

check_workflow_gates_on_version_mismatch

# 5. Automatic PR creation stays signed, idempotent and inexpensive. Paid
#    hardware qualification lives only in its separate manual workflow.
check_automatic_pr_boundaries() {
  local workflow
  workflow=$(cat "$SCRIPT_DIR/../workflows/msb-runtime-watch.yml")
  # Single quotes intentionally keep the GitHub expression literal.
  # shellcheck disable=SC2016
  for required in 'sign-commits: true' 'draft: always-true' "steps.existing-pr.outputs.pr_number == ''" 'MSB_ISSUE_BODY_PATH: ${{ runner.temp }}'; do
    if ! grep -Fq "$required" <<<"$workflow"; then
      fail "automatic update workflow lost $required"
    fi
  done
  if grep -Eiq 'self-hosted|scaleway|SCW_|msb-hardware' <<<"$workflow"; then
    fail "automatic update workflow gained a paid hardware path"
  fi
  local required_workflow
  for required_workflow in ci.yml gitleaks.yml osv-scanner.yml; do
    if ! grep -Fq 'ready_for_review' "$SCRIPT_DIR/../workflows/$required_workflow"; then
      fail "$required_workflow will not run when a bot-created draft becomes ready"
    fi
  done
  checks=$((checks + 1))
}

check_automatic_pr_boundaries

printf 'ok - %d msb_runtime_watch_issue checks passed\n' "$checks"
