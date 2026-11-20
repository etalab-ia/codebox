import os
import subprocess
import tempfile
import unittest
from pathlib import Path


class WaitForArtifactsTest(unittest.TestCase):
    def run_with_listing(self, listings, max_seconds="60"):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            calls = root / "calls"
            fake = root / "gh"
            fake.write_text(
                """#!/bin/bash
set -eu
count=0
if [ -f "$ARTIFACT_TEST_CALLS" ]; then
  count=$(cat "$ARTIFACT_TEST_CALLS")
fi
count=$((count + 1))
printf '%s' "$count" > "$ARTIFACT_TEST_CALLS"
index=$((count - 1))
if [ "$index" -ge "$ARTIFACT_TEST_COUNT" ]; then
  index=$((ARTIFACT_TEST_COUNT - 1))
fi
case "$index" in
"""
                + "".join(
                    f"  {i}) printf '%s\\n' '{listing}' ;;\n"
                    for i, listing in enumerate(listings)
                )
                + "esac\n",
                encoding="utf-8",
            )
            fake.chmod(0o755)
            script = Path(__file__).resolve().parent / "wait-for-artifacts.sh"
            env = os.environ.copy()
            env.update(
                PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                ARTIFACT_TEST_CALLS=str(calls),
                ARTIFACT_TEST_COUNT=str(len(listings)),
                ARTIFACT_WAIT_INTERVAL_SECONDS="0",
                ARTIFACT_WAIT_MAX_SECONDS=max_seconds,
                GITHUB_REPOSITORY="etalab-ia/codebox",
                GITHUB_RUN_ID="123",
                GH_TOKEN="synthetic-test-token",
            )
            result = subprocess.run(
                [str(script), "linux-x64", "linux-arm64", "darwin-arm64"],
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(calls.read_text(encoding="utf-8").isdigit(), True)
            return result, int(calls.read_text(encoding="utf-8"))

    def test_delayed_visibility_succeeds(self):
        result, calls = self.run_with_listing(
            [
                '{"artifacts":[{"name":"linux-x64","expired":false}]}',
                '{"artifacts":[{"name":"linux-x64","expired":false},{"name":"linux-arm64","expired":false},{"name":"darwin-arm64","expired":false}]}',
            ]
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, 2)
        self.assertIn("All 3 artifacts are visible", result.stdout)

    def test_missing_artifact_fails_after_bounded_wait(self):
        result, calls = self.run_with_listing(
            ['{"artifacts":[{"name":"linux-x64","expired":false}]}'],
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertEqual(calls, 1)
        self.assertIn("darwin-arm64", result.stderr)
        self.assertIn("visible: linux-x64", result.stderr)

    def test_expired_artifact_does_not_satisfy_the_wait(self):
        result, _ = self.run_with_listing(
            ['{"artifacts":[{"name":"darwin-arm64","expired":true}]}'],
            max_seconds="0",
        )
        self.assertEqual(result.returncode, 1)
        self.assertNotIn("visible: darwin-arm64", result.stderr)
    def test_workflows_check_out_before_using_the_guard(self):
        root = Path(__file__).resolve().parents[2]
        for name in ("ci.yml", "release-please.yml"):
            text = (root / ".github" / "workflows" / name).read_text(encoding="utf-8")
            guard = text.index("wait-for-artifacts.sh")
            self.assertIn("actions/checkout@", text[:guard], name)


if __name__ == "__main__":
    unittest.main()
