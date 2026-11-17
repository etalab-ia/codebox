#!/usr/bin/env bash
# Bound the time a manually dispatched paid hardware job may wait for its
# self-hosted runner. Uses workflow-job permissions, not the administration-only
# self-hosted-runner inventory API.
set -euo pipefail
: "${GH_REPO:?GH_REPO is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"
: "${GITHUB_RUN_ID:?GITHUB_RUN_ID is required}"
job_name="${QUALIFY_JOB_NAME:-qualify}"
wait_seconds="${WAIT_SECONDS:-120}"
poll_interval="${POLL_INTERVAL:-10}"
[[ "$wait_seconds" =~ ^[0-9]+$ ]] || { echo 'WAIT_SECONDS must be an integer' >&2; exit 2; }
[[ "$poll_interval" =~ ^[0-9]+$ ]] || { echo 'POLL_INTERVAL must be an integer' >&2; exit 2; }

start=$SECONDS
while true; do
  status=unavailable
  if jobs=$(gh api "repos/${GH_REPO}/actions/runs/${GITHUB_RUN_ID}/jobs?per_page=100"); then
    if ! status=$(jq -r --arg name "$job_name" '[.jobs[] | select(.name == $name)] |
      if length == 0 then "missing"
      elif length == 1 then .[0].status
      else error("duplicate qualification jobs") end' <<<"$jobs"); then
      status=unavailable
    fi
  fi
  case "$status" in
    in_progress)
      printf '%s acquired a runner; watchdog exiting.\n' "$job_name"
      exit 0
      ;;
    completed)
      printf '%s completed before the watchdog deadline.\n' "$job_name"
      exit 0
      ;;
  esac
  if (( SECONDS - start >= wait_seconds )); then
    break
  fi
  sleep "$poll_interval"
done

printf '::error::%s remained %s after %s seconds. No Scaleway runner acquired the job; cancelling this manual run.\n' \
  "$job_name" "$status" "$wait_seconds"
printf 'Provision an online Linux runner with the scaleway label, then dispatch again. No infrastructure is provisioned automatically.\n'
gh api -X POST "repos/${GH_REPO}/actions/runs/${GITHUB_RUN_ID}/cancel" >/dev/null
exit 1
