import json
import re
import subprocess
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW_PATH = REPO_ROOT / ".github/workflows/release-please.yml"
SCOPE_SCRIPT = REPO_ROOT / ".github/scripts/platform-ci-scope.sh"
SEMVER_RE = re.compile(r"^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")


def parse_release_please_steps(workflow_text: str):
    """Extract release-please-action steps and job outputs from the release-please job."""
    lines = workflow_text.splitlines()
    in_job = False
    in_outputs = False
    in_steps = False
    outputs = {}
    steps = []
    current = None
    in_with = False

    for line in lines:
        if re.match(r"^  [a-zA-Z0-9_-]+:\s*$", line):
            if in_job:
                break
            if line.strip() == "release-please:":
                in_job = True
            continue
        if not in_job:
            continue

        if line == "    outputs:":
            in_outputs = True
            in_steps = False
            continue
        if line == "    steps:":
            in_outputs = False
            in_steps = True
            continue

        if in_outputs:
            m = re.match(r"^      ([a-zA-Z0-9_]+):\s*(.+?)\s*$", line)
            if m:
                outputs[m.group(1)] = m.group(2)
            continue

        if not in_steps:
            continue

        step_start = re.match(r"^      - name:\s*(.+?)\s*$", line)
        if step_start:
            if current is not None:
                steps.append(current)
            current = {"name": step_start.group(1), "with": {}}
            in_with = False
            continue

        if current is None:
            continue

        if line == "        with:":
            in_with = True
            continue

        if in_with:
            kv = re.match(r"^          ([a-zA-Z0-9_-]+):\s*(.+?)\s*$", line)
            if kv:
                current["with"][kv.group(1)] = kv.group(2)
            elif not line.strip().startswith("#") and line.strip() != "":
                in_with = False

        if not in_with:
            kv = re.match(r"^        ([a-zA-Z0-9_-]+):\s*(.+?)\s*$", line)
            if kv:
                current[kv.group(1)] = kv.group(2)

    if current is not None:
        steps.append(current)

    rp_steps = [
        s
        for s in steps
        if s.get("uses", "").startswith("googleapis/release-please-action@")
    ]
    return outputs, rp_steps


class ReleasePleaseConfigTest(unittest.TestCase):
    def test_no_legacy_root_files_remain(self):
        self.assertFalse((REPO_ROOT / "release-please-config.json").exists())
        self.assertFalse((REPO_ROOT / ".release-please-manifest.json").exists())

    def test_workflow_runs_separate_release_passes_before_pr_passes(self):
        outputs, rp_steps = parse_release_please_steps(
            WORKFLOW_PATH.read_text(encoding="utf-8")
        )
        self.assertEqual(len(rp_steps), 4, "expected 2 release steps + 2 PR steps")

        release_steps = rp_steps[:2]
        pr_steps = rp_steps[2:]

        for step in release_steps:
            self.assertEqual(step["with"].get("skip-github-pull-request"), "true")
            self.assertNotIn("skip-github-release", step["with"])

        for step in pr_steps:
            self.assertEqual(step["with"].get("skip-github-release"), "true")
            self.assertNotIn("skip-github-pull-request", step["with"])

        release_pairs = [
            (s["with"]["config-file"], s["with"]["manifest-file"])
            for s in release_steps
        ]
        pr_pairs = [
            (s["with"]["config-file"], s["with"]["manifest-file"]) for s in pr_steps
        ]
        self.assertEqual(release_pairs, pr_pairs)

        # Each component must own a distinct manifest and config file.
        self.assertEqual(len({c for c, _ in release_pairs}), 2)
        self.assertEqual(len({m for _, m in release_pairs}), 2)

        # Job outputs must read from the release-pass step IDs.
        self.assertIn("steps.release_codebox.outputs['apps/codebox--release_created']", outputs["codebox_release_created"])
        self.assertIn("steps.release_codebox.outputs['apps/codebox--tag_name']", outputs["codebox_tag_name"])
        self.assertIn("steps.release_codebox_server.outputs['apps/codebox-server--release_created']", outputs["codebox_server_release_created"])
        self.assertIn("steps.release_codebox_server.outputs['apps/codebox-server--tag_name']", outputs["codebox_server_tag_name"])

    def test_per_component_configs_and_manifests_are_isolated_and_valid(self):
        _, rp_steps = parse_release_please_steps(
            WORKFLOW_PATH.read_text(encoding="utf-8")
        )
        components = {}
        manifests_by_component = {}

        for step in rp_steps[:2]:
            cfg_rel = step["with"]["config-file"]
            man_rel = step["with"]["manifest-file"]

            cfg_path = REPO_ROOT / cfg_rel
            man_path = REPO_ROOT / man_rel
            self.assertTrue(cfg_path.is_file(), f"missing {cfg_rel}")
            self.assertTrue(man_path.is_file(), f"missing {man_rel}")

            # Configs and manifests must live outside package trees so config
            # edits cannot trigger spurious component releases.
            self.assertFalse(cfg_rel.startswith("apps/"))
            self.assertFalse(man_rel.startswith("apps/"))

            cfg = json.loads(cfg_path.read_text(encoding="utf-8"))
            man = json.loads(man_path.read_text(encoding="utf-8"))

            # separate-pull-requests keeps component-scoped branch names so
            # separate invocations do not collide on release-please--branches--main.
            self.assertIs(cfg.get("separate-pull-requests"), True)

            packages = cfg.get("packages", {})
            self.assertEqual(len(packages), 1, f"{cfg_rel} must own exactly 1 package")
            self.assertEqual(list(packages.keys()), list(man.keys()))

            pkg_path, pkg_cfg = next(iter(packages.items()))
            version = man[pkg_path]
            self.assertRegex(version, SEMVER_RE)
            self.assertIs(pkg_cfg.get("include-component-in-tag"), True)
            component = pkg_cfg.get("component")
            self.assertTrue(component)
            self.assertNotIn(component, components)
            components[component] = pkg_path
            manifests_by_component[component] = man_rel

        self.assertEqual(
            components,
            {
                "codebox": "apps/codebox",
                "codebox-server": "apps/codebox-server",
            },
        )

        # Verify platform-ci-scope.sh stays aligned with the split manifests.
        server_res = subprocess.run(
            ["bash", str(SCOPE_SCRIPT), "pull_request"],
            input=f"{manifests_by_component['codebox-server']}\napps/codebox-server/CHANGELOG.md\n",
            text=True,
            capture_output=True,
            check=True,
        )
        self.assertEqual(server_res.stdout.strip(), "run_platforms=false")

        cli_res = subprocess.run(
            ["bash", str(SCOPE_SCRIPT), "pull_request"],
            input=f"{manifests_by_component['codebox']}\n",
            text=True,
            capture_output=True,
            check=True,
        )
        self.assertEqual(cli_res.stdout.strip(), "run_platforms=true")


if __name__ == "__main__":
    unittest.main()
