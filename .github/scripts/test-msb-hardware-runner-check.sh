#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
TARGET="$SCRIPT_DIR/msb-hardware-runner-check.sh"
checks=0

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/gh" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GH_CALLS"
cat "$GH_RUNNERS"
STUB
chmod +x "$tmpdir/bin/gh"

run_check() {
  (cd "$tmpdir" && env PATH="$tmpdir/bin:$PATH" \
    GH_TOKEN=test-token GH_REPO=etalab-ia/codebox \
    GH_CALLS="$tmpdir/calls" GH_RUNNERS="$tmpdir/runners.json" \
    bash "$TARGET")
}

expect_failure() {
  local name="$1"
  if run_check >/dev/null 2>&1; then
    printf 'not ok - %s unexpectedly succeeded\n' "$name" >&2
    exit 1
  fi
  checks=$((checks + 1))
}

printf '%s\n' '{"runners":[]}' > "$tmpdir/runners.json"
expect_failure 'no runners'

printf '%s\n' '{"runners":[{"status":"offline","labels":[{"name":"self-hosted"},{"name":"Linux"},{"name":"scaleway"}]}]}' > "$tmpdir/runners.json"
expect_failure 'offline Scaleway runner'

printf '%s\n' '{"runners":[{"status":"online","labels":[{"name":"self-hosted"},{"name":"Linux"}]}]}' > "$tmpdir/runners.json"
expect_failure 'runner without Scaleway label'

printf '%s\n' '{"runners":[{"status":"online","labels":[{"name":"self-hosted"},{"name":"Linux"},{"name":"scaleway"}]}]}' > "$tmpdir/runners.json"
if ! run_check >/dev/null; then
  printf 'not ok - online Scaleway runner was rejected\n' >&2
  exit 1
fi
if ! grep -q 'repos/etalab-ia/codebox/actions/runners' "$tmpdir/calls"; then
  printf 'not ok - runner inventory queried the wrong route\n' >&2
  exit 1
fi
checks=$((checks + 1))

workflow=$(cat "$SCRIPT_DIR/../workflows/msb-hardware-qualification.yml")
triggers=${workflow%%jobs:*}
if [[ "$triggers" != *'workflow_dispatch:'* ]]; then
  printf 'not ok - hardware workflow lost manual dispatch\n' >&2
  exit 1
fi
for forbidden in 'pull_request:' 'schedule:' 'push:' 'workflow_call:'; do
  if grep -Fq "$forbidden" <<<"$triggers"; then
    printf 'not ok - hardware workflow gained automatic trigger %s\n' "$forbidden" >&2
    exit 1
  fi
done
for required in 'runs-on: [self-hosted, linux, scaleway]' 'CODEBOX_MSB_HARDWARE_QUALIFICATION: "1"' 'needs: runner-check'; do
  if ! grep -Fq "$required" <<<"$workflow"; then
    printf 'not ok - hardware workflow lost %s\n' "$required" >&2
    exit 1
  fi
done
if grep -Eq 'secrets\.(SCW|.*SCALEWAY)|scaleway/action-scw|scw ' <<<"$workflow"; then
  printf 'not ok - hardware workflow can provision Scaleway infrastructure\n' >&2
  exit 1
fi
checks=$((checks + 1))
printf 'ok - %d msb hardware runner checks passed\n' "$checks"
