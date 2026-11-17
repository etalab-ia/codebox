#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
TARGET="$SCRIPT_DIR/msb-hardware-watchdog.sh"
checks=0

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/gh" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$GH_CALLS"
case "$*" in
  *"/jobs?per_page=100"*)
    count=0
    [ ! -f "$GH_CALL_COUNT" ] || count=$(<"$GH_CALL_COUNT")
    count=$((count + 1))
    printf '%s' "$count" > "$GH_CALL_COUNT"
    status="${GH_FIRST_STATUS:-queued}"
    if [ "$count" -gt 1 ] && [ -n "${GH_SECOND_STATUS:-}" ]; then
      status="$GH_SECOND_STATUS"
    fi
    printf '{"jobs":[{"name":"qualify","status":"%s"}]}\n' "$status"
    ;;
  *"-X POST"*"/cancel")
    printf '{}\n'
    ;;
  *)
    printf 'unexpected gh call: %s\n' "$*" >&2
    exit 2
    ;;
esac
STUB
chmod +x "$tmpdir/bin/gh"

reset_case() {
  : > "$tmpdir/calls"
  : > "$tmpdir/call-count"
}
run_check() {
  (cd "$tmpdir" && env PATH="$tmpdir/bin:$PATH" \
    GH_TOKEN=test-token GH_REPO=etalab-ia/codebox GITHUB_RUN_ID=123 \
    GH_CALLS="$tmpdir/calls" GH_CALL_COUNT="$tmpdir/call-count" \
    GH_FIRST_STATUS="${GH_FIRST_STATUS:-queued}" GH_SECOND_STATUS="${GH_SECOND_STATUS:-}" \
    WAIT_SECONDS="${WAIT_SECONDS:-0}" POLL_INTERVAL=0 \
    bash "$TARGET")
}
fail() { printf 'not ok - %s\n' "$1" >&2; exit 1; }

reset_case
if run_check >/dev/null 2>&1; then
  fail 'queued qualification was not cancelled at the deadline'
fi
grep -q -- '-X POST repos/etalab-ia/codebox/actions/runs/123/cancel' "$tmpdir/calls" || fail 'timeout did not cancel the workflow run'
checks=$((checks + 1))

reset_case
if ! GH_FIRST_STATUS=in_progress run_check >/dev/null; then
  fail 'in-progress qualification did not stop the watchdog'
fi
if grep -q -- '-X POST' "$tmpdir/calls"; then fail 'in-progress qualification was cancelled'; fi
checks=$((checks + 1))

reset_case
if ! GH_FIRST_STATUS=completed run_check >/dev/null; then
  fail 'completed qualification did not stop the watchdog'
fi
if grep -q -- '-X POST' "$tmpdir/calls"; then fail 'completed qualification was cancelled'; fi
checks=$((checks + 1))

reset_case
if ! GH_FIRST_STATUS=queued GH_SECOND_STATUS=in_progress WAIT_SECONDS=5 run_check >/dev/null; then
  fail 'runner acquisition after an initial queue was rejected'
fi
if grep -q -- '-X POST' "$tmpdir/calls"; then fail 'newly acquired qualification was cancelled'; fi
[ "$(<"$tmpdir/call-count")" -ge 2 ] || fail 'watchdog did not poll again while queued'
checks=$((checks + 1))

workflow=$(cat "$SCRIPT_DIR/../workflows/msb-hardware-qualification.yml")
triggers=${workflow%%jobs:*}
[[ "$triggers" == *'workflow_dispatch:'* ]] || fail 'hardware workflow lost manual dispatch'
for forbidden in 'pull_request:' 'schedule:' 'push:' 'workflow_call:'; do
  if grep -Fq "$forbidden" <<<"$triggers"; then fail "hardware workflow gained automatic trigger $forbidden"; fi
done
for required in 'runs-on: [self-hosted, linux, scaleway]' 'CODEBOX_MSB_HARDWARE_QUALIFICATION: "1"' 'actions: write' 'msb-hardware-watchdog.sh' 'WAIT_SECONDS: "120"'; do
  grep -Fq "$required" <<<"$workflow" || fail "hardware workflow lost $required"
done
if grep -Fq 'needs: runner-check' <<<"$workflow"; then fail 'qualification still depends on the administration-only inventory gate'; fi
if grep -Eq 'secrets\.(SCW|.*SCALEWAY)|scaleway/action-scw|scw ' <<<"$workflow"; then
  fail 'hardware workflow can provision Scaleway infrastructure'
fi
checks=$((checks + 1))
printf 'ok - %d msb hardware watchdog checks passed\n' "$checks"
