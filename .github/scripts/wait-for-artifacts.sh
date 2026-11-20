#!/usr/bin/env bash
# Wait for artifact-list visibility after uploads finalize. GitHub can accept an
# artifact before the run-level listing includes it; downloading immediately can
# otherwise return an incomplete set and fail the merged handoff.
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
case "$max_seconds:$interval" in
  *[!0-9:]*) printf 'wait bounds must be integer seconds\n' >&2; exit 2 ;;
esac

start=$SECONDS
while true; do
  if listing=$(gh api "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/artifacts?per_page=100"); then
    missing=()
    for name in "$@"; do
      if ! jq -e --arg name "$name" '.artifacts[] | select(.expired == false and .name == $name)' <<<"$listing" >/dev/null; then
        missing+=("$name")
      fi
    done
    if [ "${#missing[@]}" -eq 0 ]; then
      printf 'All %s artifacts are visible to the run-level listing.\n' "$#"
      exit 0
    fi
  else
    listing='{"artifacts":[]}'
    missing=("$@")
  fi
  if [ "$((SECONDS - start))" -ge "$max_seconds" ]; then
    printf 'Artifacts missing after %ss: %s\n' "$max_seconds" "${missing[*]}" >&2
    jq -r '.artifacts[] | select(.expired == false) | "visible: \(.name)"' <<<"$listing" >&2
    exit 1
  fi
  sleep "$interval"
done
