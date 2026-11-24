#!/usr/bin/env bash
# Print run_platforms=false only for a PR made exclusively of the two files
# release-please updates for the codebox-server component (source path retained).
set -euo pipefail

event_name="${1:-}"
if [[ "$event_name" != "pull_request" ]]; then
  printf 'run_platforms=true\n'
  exit 0
fi

run_platforms=false
saw_file=false
while IFS= read -r file || [[ -n "$file" ]]; do
  # An empty diff is not evidence of a release-only PR.
  if [[ -z "$file" ]]; then
    continue
  fi
  saw_file=true
  case "$file" in
    .github/release-please/codebox-server.manifest.json | apps/codebox-server/CHANGELOG.md)
      ;;
    *)
      run_platforms=true
      ;;
  esac
done

if [[ "$saw_file" != true ]]; then
  run_platforms=true
fi
printf 'run_platforms=%s\n' "$run_platforms"
