#!/usr/bin/env bash
# Fail on a hosted runner before GitHub queues a paid hardware job when no
# explicitly provisioned Scaleway runner is online.
set -euo pipefail
: "${GH_REPO:?GH_REPO is required}"
: "${GH_TOKEN:?GH_TOKEN is required}"

runners="$(gh api "repos/${GH_REPO}/actions/runners")"
online="$(jq -r '[.runners[] |
  select(.status == "online") |
  select(any(.labels[]; (.name | ascii_downcase) == "scaleway")) |
  select(any(.labels[]; (.name | ascii_downcase) == "linux"))
] | length' <<<"$runners")"
if [[ "$online" =~ ^[0-9]+$ ]] && [ "$online" -gt 0 ]; then
  printf 'Found %s online Linux Scaleway runner(s).\n' "$online"
  exit 0
fi
cat >&2 <<'EOF'
::error::No online Linux self-hosted runner with the `scaleway` label is available.
Provision a runner explicitly before dispatching this paid qualification workflow.
The workflow never provisions Scaleway infrastructure itself. Tear the runner
down again after qualification to avoid ongoing cost.
EOF
exit 1
