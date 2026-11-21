import json
import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path


def make_artifact(name, artifact_id, expired=False):
    return {"id": artifact_id, "name": name, "expired": expired}


class WaitForArtifactsTest(unittest.TestCase):
    def run_guard(
        self,
        listings,
        artifact_responses=None,
        max_seconds="60",
        settle_seconds="0",
        artifacts=None,
    ):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            state_path = root / "state.json"
            state_path.write_text(
                json.dumps(
                    {
                        "listings": listings,
                        "artifacts": {
                            str(k): v for k, v in (artifact_responses or {}).items()
                        },
                        "list_calls": 0,
                        "id_calls": {},
                    }
                ),
                encoding="utf-8",
            )
            fake = root / "gh"
            fake.write_text(
                f"""#!/usr/bin/env python3
import json
import sys
from pathlib import Path

state_path = Path({str(state_path)!r})
state = json.loads(state_path.read_text(encoding="utf-8"))
if len(sys.argv) < 3 or sys.argv[1] != "api":
    sys.stderr.write(f"unexpected gh args: {{sys.argv!r}}\\n")
    sys.exit(2)

endpoint = sys.argv[2]
if "/actions/runs/" in endpoint and "/artifacts" in endpoint:
    idx = state["list_calls"]
    state["list_calls"] = idx + 1
    listings = state["listings"]
    item = listings[min(idx, len(listings) - 1)]
    state_path.write_text(json.dumps(state), encoding="utf-8")
    if item is None:
        sys.stderr.write("transient listing failure\\n")
        sys.exit(1)
    print(json.dumps(item))
    sys.exit(0)

if "/actions/artifacts/" in endpoint:
    artifact_id = endpoint.rsplit("/", 1)[-1]
    id_calls = state["id_calls"]
    idx = id_calls.get(artifact_id, 0)
    id_calls[artifact_id] = idx + 1
    state_path.write_text(json.dumps(state), encoding="utf-8")
    responses = state["artifacts"].get(artifact_id)
    if responses is None:
        latest_idx = max(0, state["list_calls"] - 1)
        latest = state["listings"][min(latest_idx, len(state["listings"]) - 1)] or {{}}
        for entry in latest.get("artifacts", []):
            if str(entry.get("id")) == artifact_id:
                print(json.dumps(entry))
                sys.exit(0)
        sys.stderr.write(f"No artifacts found for ID: {{artifact_id}}\\n")
        sys.exit(1)
    item = responses[min(idx, len(responses) - 1)]
    if item is None:
        sys.stderr.write(f"No artifacts found for ID: {{artifact_id}}\\n")
        sys.exit(1)
    print(json.dumps(item))
    sys.exit(0)

sys.stderr.write(f"unexpected endpoint: {{endpoint}}\\n")
sys.exit(2)
""",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            script = Path(__file__).resolve().parent / "wait-for-artifacts.sh"
            env = os.environ.copy()
            env.update(
                PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                ARTIFACT_WAIT_INTERVAL_SECONDS="0",
                ARTIFACT_WAIT_MAX_SECONDS=max_seconds,
                ARTIFACT_WAIT_SETTLE_SECONDS=settle_seconds,
                GITHUB_REPOSITORY="etalab-ia/codebox",
                GITHUB_RUN_ID="123",
                GH_TOKEN="synthetic-test-token",
            )
            command = ["bash", str(script)]
            command.extend(artifacts or ["linux-x64", "linux-arm64", "darwin-arm64"])
            result = subprocess.run(
                command,
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            final_state = json.loads(state_path.read_text(encoding="utf-8"))
            return result, final_state["list_calls"], final_state["id_calls"]

    def workflow_job(self, name, job):
        root = Path(__file__).resolve().parents[2]
        lines = (root / ".github" / "workflows" / name).read_text(encoding="utf-8").splitlines()
        start = lines.index(f"  {job}:")
        is_job = re.compile(r"^  [A-Za-z0-9_-]+:\s*$").match
        end = next(
            (index for index in range(start + 1, len(lines)) if is_job(lines[index])),
            len(lines),
        )
        return "\n".join(lines[start:end])

    def test_delayed_visibility_succeeds(self):
        result, list_calls, id_calls = self.run_guard(
            [
                {"artifacts": [make_artifact("linux-x64", 101)]},
                {
                    "artifacts": [
                        make_artifact("linux-x64", 101),
                        make_artifact("linux-arm64", 102),
                        make_artifact("darwin-arm64", 103),
                    ]
                },
            ]
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(list_calls, 2)
        self.assertEqual(id_calls.get("103"), 1)
        self.assertIn("All 3 artifacts are visible and resolvable by ID", result.stdout)

    def test_delayed_id_resolution_succeeds(self):
        listing = {
            "artifacts": [
                make_artifact("linux-x64", 101),
                make_artifact("linux-arm64", 102),
                make_artifact("darwin-arm64", 103),
            ]
        }
        result, list_calls, id_calls = self.run_guard(
            [listing],
            artifact_responses={
                103: [None, make_artifact("darwin-arm64", 103)],
            },
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(list_calls, 2)
        self.assertEqual(id_calls.get("103"), 2)

    def test_unresolvable_artifact_id_fails_after_bounded_wait(self):
        listing = {
            "artifacts": [
                make_artifact("linux-x64", 101),
                make_artifact("linux-arm64", 102),
                make_artifact("darwin-arm64", 103),
            ]
        }
        result, list_calls, _ = self.run_guard(
            [listing],
            artifact_responses={103: [None]},
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(list_calls, 1)
        self.assertIn("not resolvable by ID", result.stderr)
        self.assertIn("darwin-arm64#103", result.stderr)

    def test_expired_or_mismatched_id_response_does_not_satisfy_wait(self):
        listing = {
            "artifacts": [
                make_artifact("linux-x64", 101),
                make_artifact("linux-arm64", 102),
                make_artifact("darwin-arm64", 103),
            ]
        }
        result, _, _ = self.run_guard(
            [listing],
            artifact_responses={
                103: [make_artifact("darwin-arm64", 103, expired=True)],
            },
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("darwin-arm64#103", result.stderr)

    def test_settle_confirmation_retries_after_replica_flap(self):
        listing = {
            "artifacts": [
                make_artifact("linux-x64", 101),
                make_artifact("linux-arm64", 102),
                make_artifact("darwin-arm64", 103),
            ]
        }
        result, list_calls, id_calls = self.run_guard(
            [listing],
            artifact_responses={
                103: [
                    make_artifact("darwin-arm64", 103),
                    None,
                    make_artifact("darwin-arm64", 103),
                    make_artifact("darwin-arm64", 103),
                ],
            },
            max_seconds="10",
            settle_seconds="1",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(list_calls, 4)
        self.assertEqual(id_calls.get("103"), 4)

    def test_duplicate_name_checks_latest_non_expired_id(self):
        listing = {
            "artifacts": [
                make_artifact("linux-x64", 101),
                make_artifact("linux-arm64", 102),
                make_artifact("darwin-arm64", 103),
                make_artifact("darwin-arm64", 203),
            ]
        }
        result, _, id_calls = self.run_guard(
            [listing],
            artifact_responses={
                103: [None],
                203: [make_artifact("darwin-arm64", 203)],
            },
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIsNone(id_calls.get("103"))
        self.assertEqual(id_calls.get("203"), 1)

    def test_missing_artifact_fails_after_bounded_wait(self):
        result, calls, _ = self.run_guard(
            [{"artifacts": [make_artifact("linux-x64", 101)]}],
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(calls, 1)
        self.assertIn("darwin-arm64", result.stderr)
        self.assertIn("visible: linux-x64", result.stderr)

    def test_expired_artifact_does_not_satisfy_the_wait(self):
        result, _, _ = self.run_guard(
            [{"artifacts": [make_artifact("darwin-arm64", 103, expired=True)]}],
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("visible: darwin-arm64", result.stderr)

    def test_workflows_check_out_before_using_the_guard(self):
        for name, job in (("ci.yml", "artifact-handoff"), ("release-please.yml", "publish")):
            block = self.workflow_job(name, job)
            guard = block.index("wait-for-artifacts.sh")
            self.assertIn("actions/checkout@", block[:guard], f"{name}:{job}")

    def test_delayed_windows_visibility_succeeds(self):
        names = ["linux-x64", "linux-arm64", "darwin-arm64", "windows-x64", "windows-arm64"]
        first = {"artifacts": [make_artifact(name, 200 + i) for i, name in enumerate(names[:3])]}
        second = {"artifacts": [make_artifact(name, 200 + i) for i, name in enumerate(names)]}
        result, calls, _ = self.run_guard([first, second], artifacts=names)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, 2)
        self.assertIn("All 5 artifacts are visible and resolvable by ID", result.stdout)

    def test_release_guard_includes_windows_artifacts(self):
        block = self.workflow_job("release-please.yml", "publish")
        guards = [line for line in block.splitlines() if "wait-for-artifacts.sh" in line]
        self.assertGreaterEqual(len(guards), 2)
        for guard in guards:
            for name in ("windows-x64", "windows-arm64"):
                self.assertIn(name, guard)

    def workflow_steps(self, name, job):
        block = self.workflow_job(name, job)
        marker = "\n    steps:\n"
        self.assertIn(marker, block, f"{name}:{job}")
        steps_text = block.split(marker, 1)[1]
        return re.split(r"(?m)^      - ", "\n" + steps_text)[1:]

    def test_workflows_retry_download_cleanly_without_suppressing_final_error(self):
        cases = (
            ("ci.yml", "artifact-handoff", "downloaded", "download_staged_artifacts"),
            ("release-please.yml", "publish", "dist", "download_release_artifacts"),
        )
        for name, job, target_dir, step_id in cases:
            steps = self.workflow_steps(name, job)
            downloads = [s for s in steps if "uses: actions/download-artifact@" in s]
            self.assertEqual(len(downloads), 2, f"{name}:{job}")
            self.assertIn(f"id: {step_id}", downloads[0], f"{name}:{job}")
            self.assertIn("continue-on-error: true", downloads[0], f"{name}:{job}")
            # Final retry step must never suppress failures, regardless of key order.
            self.assertNotIn("continue-on-error:", downloads[1], f"{name}:{job}")
            self.assertIn(
                f"if: success() && steps.{step_id}.outcome == 'failure'",
                downloads[1],
                f"{name}:{job}",
            )
            reverify = [s for s in steps if f"rm -rf {target_dir}" in s]
            self.assertEqual(len(reverify), 1, f"{name}:{job}")
            self.assertIn(
                f"if: success() && steps.{step_id}.outcome == 'failure'",
                reverify[0],
                f"{name}:{job}",
            )
            self.assertIn("wait-for-artifacts.sh", reverify[0], f"{name}:{job}")

    def test_platform_scope_runs_artifact_guard_tests(self):
        block = self.workflow_job("ci.yml", "platform-scope")
        self.assertIn("python3 .github/scripts/test_wait_for_artifacts.py", block)


if __name__ == "__main__":
    unittest.main()
