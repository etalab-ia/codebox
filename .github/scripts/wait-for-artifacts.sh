#!/usr/bin/env bash
# Wait for artifact visibility and per-ID resolution after uploads finalize.
# GitHub Actions artifacts can finalize before the run-level listing includes
# them, and can even appear in the listing before ID-based download lookups
# succeed across read replicas. Polling both endpoints plus a short settle
# confirmation prevents premature downloads from failing the merged handoff.
set -euo pipefail

if [ "$#" -eq 0 ]; then
  printf 'usage: wait-for-artifacts.sh <artifact-name>...\n' >&2
  exit 2
fi
: "${GITHUB_REPOSITORY:?repository is required}"
: "${GITHUB_RUN_ID:?run id is required}"
: "${GH_TOKEN:?GitHub token is required}"
max_seconds=${ARTIFACT_WAIT_MAX_SECONDS:-60}
interval=${ARTIFACT_WAIT_INTERVAL_SECONDS:-5}
settle_seconds=${ARTIFACT_WAIT_SETTLE_SECONDS:-3}
case "$max_seconds:$interval:$settle_seconds" in
  *[!0-9:]*) printf 'wait bounds must be integer seconds\n' >&2; exit 2 ;;
esac

listing='{"artifacts":[]}'
missing=()
unresolvable=()

check_artifacts() {
  missing=()
  unresolvable=()
  if ! listing=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/artifacts?per_page=100"); then
    listing='{"artifacts":[]}'
    missing=("$@")
    return 1
  fi
  if ! jq -e '.artifacts | type == "array"' <<<"$listing" >/dev/null 2>&1; then
    listing='{"artifacts":[]}'
    missing=("$@")
    return 1
  fi

  local name id artifact
  for name in "$@"; do
    id=$(jq -r --arg name "$name" \
      '[.artifacts[] | select(.expired == false and .name == $name and (.id | type == "number"))] | max_by(.id) | .id // empty' \
      <<<"$listing")
    if [ -z "$id" ]; then
      missing+=("$name")
      continue
    fi
    if ! artifact=$(gh api "repos/$GITHUB_REPOSITORY/actions/artifacts/$id"); then
      unresolvable+=("${name}#${id}")
      continue
    fi
    if ! jq -e --arg name "$name" --argjson id "$id" \
      'select(.expired == false and .id == $id and .name == $name)' \
      <<<"$artifact" >/dev/null 2>&1; then
      unresolvable+=("${name}#${id}")
    fi
  done

  [ "${#missing[@]}" -eq 0 ] && [ "${#unresolvable[@]}" -eq 0 ]
}

settled=0
start=$SECONDS
while true; do
  if check_artifacts "$@"; then
    if [ "$settle_seconds" -eq 0 ] || [ "$settled" -eq 1 ]; then
      printf 'All %s artifacts are visible and resolvable by ID.\n' "$#"
      exit 0
    fi
    settled=1
    if [ "$((SECONDS - start))" -ge "$max_seconds" ]; then
      printf 'All %s artifacts are visible and resolvable by ID.\n' "$#"
      exit 0
    fi
    sleep "$settle_seconds"
    continue
  fi
  settled=0
  if [ "$((SECONDS - start))" -ge "$max_seconds" ]; then
    if [ "${#missing[@]}" -gt 0 ]; then
      printf 'Artifacts missing after %ss: %s\n' "$max_seconds" "${missing[*]}" >&2
    fi
    if [ "${#unresolvable[@]}" -gt 0 ]; then
      printf 'Artifacts not resolvable by ID after %ss: %s\n' "$max_seconds" "${unresolvable[*]}" >&2
    fi
    jq -r '.artifacts[] | select(.expired == false) | "visible: \(.name)"' <<<"$listing" >&2
    exit 1
  fi
  sleep "$interval"
done
