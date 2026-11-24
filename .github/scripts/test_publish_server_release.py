import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / ".github" / "scripts" / "publish-server-release.sh"
RELEASE_CONFIG = ROOT / ".github" / "release-please" / "codebox-server.config.json"
RELEASE_WORKFLOW = ROOT / ".github" / "workflows" / "release-please.yml"
CI_WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"


def make_release(
    tag_name,
    *,
    draft=False,
    prerelease=False,
    published_at="2026-11-24T10:00:00Z",
    assets=None,
):
    return {
        "tag_name": tag_name,
        "draft": draft,
        "prerelease": prerelease,
        "published_at": published_at,
        "assets": [{"name": name} for name in (assets or [])],
    }


FAKE_GH_TEMPLATE = """#!/usr/bin/env python3
import json
import sys
from pathlib import Path

state_path = Path({state_path!r})
state = json.loads(state_path.read_text(encoding="utf-8"))
state["calls"].append(sys.argv[1:])


def save():
    state_path.write_text(json.dumps(state), encoding="utf-8")


def find_release(tag):
    for rel in state["releases"]:
        if rel["tag_name"] == tag:
            return rel
    return None


def compute_latest():
    if state.get("force_latest_tag"):
        return find_release(state["force_latest_tag"])
    explicit = state.get("explicit_latest")
    if explicit:
        rel = find_release(explicit)
        if rel and not rel["draft"] and not rel["prerelease"]:
            return rel
    published = [
        rel
        for rel in state["releases"]
        if not rel["draft"] and not rel["prerelease"]
    ]
    if not published:
        return None
    published.sort(key=lambda r: r.get("published_at") or "")
    return published[-1]


args = sys.argv[1:]
if len(args) >= 3 and args[0] == "release" and args[1] == "edit":
    tag = args[2]
    rel = find_release(tag)
    if rel is None:
        sys.stderr.write(f"release not found: {{tag}}\\n")
        save()
        sys.exit(1)
    draft_flag = None
    latest_flag = None
    repo = None
    i = 3
    while i < len(args):
        arg = args[i]
        if arg == "--repo" and i + 1 < len(args):
            repo = args[i + 1]
            i += 2
        elif arg.startswith("--repo="):
            repo = arg.split("=", 1)[1]
            i += 1
        elif arg == "--draft":
            draft_flag = True
            i += 1
        elif arg.startswith("--draft="):
            draft_flag = arg.split("=", 1)[1].lower() == "true"
            i += 1
        elif arg == "--latest":
            latest_flag = True
            i += 1
        elif arg.startswith("--latest="):
            latest_flag = arg.split("=", 1)[1].lower() == "true"
            i += 1
        else:
            sys.stderr.write(f"unexpected gh release edit arg: {{arg}}\\n")
            save()
            sys.exit(2)
    if repo != state["repo"]:
        sys.stderr.write(f"unexpected repo: {{repo!r}}\\n")
        save()
        sys.exit(2)
    was_draft = rel["draft"]
    if draft_flag is not None:
        rel["draft"] = draft_flag
        if was_draft and not draft_flag:
            rel["published_at"] = state.get(
                "publish_timestamp", "2026-11-24T12:00:00Z"
            )
    # Model GitHub's real make_latest semantics:
    # - make_latest=true explicitly pins this non-draft, non-prerelease release.
    # - make_latest=false declines forcing Latest; it does NOT demote a release
    #   that already holds Latest, nor prevent fallback to publish date when no
    #   release is explicitly pinned.
    if latest_flag is True and not rel["draft"] and not rel["prerelease"]:
        state["explicit_latest"] = tag
    elif latest_flag is None and was_draft and not rel["draft"] and not rel["prerelease"]:
        state["explicit_latest"] = tag
    save()
    print(f"https://github.com/{{repo}}/releases/tag/{{tag}}")
    sys.exit(0)

if len(args) >= 2 and args[0] == "api":
    paginate = False
    rest = []
    for arg in args[1:]:
        if arg == "--paginate":
            paginate = True
        else:
            rest.append(arg)
    if not rest:
        sys.stderr.write("missing api endpoint\\n")
        save()
        sys.exit(2)
    endpoint = rest[0]
    repo = state["repo"]
    if endpoint == f"repos/{{repo}}/releases/latest":
        latest = compute_latest()
        save()
        if latest is None:
            sys.stderr.write("HTTP 404: Not Found\\n")
            sys.exit(1)
        print(json.dumps(latest))
        sys.exit(0)
    if endpoint.startswith(f"repos/{{repo}}/releases"):
        save()
        releases = state["releases"]
        if paginate and len(releases) > 2:
            # Emit multiple concatenated JSON arrays like `gh api --paginate`.
            mid = len(releases) // 2
            print(json.dumps(releases[:mid]))
            print(json.dumps(releases[mid:]))
        else:
            print(json.dumps(releases))
        sys.exit(0)
    sys.stderr.write(f"unexpected api endpoint: {{endpoint}}\\n")
    save()
    sys.exit(2)

sys.stderr.write(f"unexpected gh invocation: {{args!r}}\\n")
save()
sys.exit(2)
"""


class PublishServerReleaseTest(unittest.TestCase):
    def run_script(
        self,
        *,
        tag,
        releases,
        explicit_latest=None,
        force_latest_tag=None,
        publish_timestamp="2026-11-24T12:00:00Z",
        repo="etalab-ia/codebox",
    ):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            state_path = root / "state.json"
            state_path.write_text(
                json.dumps(
                    {
                        "repo": repo,
                        "releases": releases,
                        "explicit_latest": explicit_latest,
                        "force_latest_tag": force_latest_tag,
                        "publish_timestamp": publish_timestamp,
                        "calls": [],
                    }
                ),
                encoding="utf-8",
            )
            fake_gh = root / "gh"
            fake_gh.write_text(
                FAKE_GH_TEMPLATE.format(state_path=str(state_path)),
                encoding="utf-8",
            )
            fake_gh.chmod(0o755)
            env = os.environ.copy()
            env.update(
                PATH=str(root) + os.pathsep + os.environ.get("PATH", ""),
                GH_TOKEN="synthetic-test-token",
                GH_REPO=repo,
                TAG=tag,
            )
            result = subprocess.run(
                ["bash", str(SCRIPT)],
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            final_state = json.loads(state_path.read_text(encoding="utf-8"))
            return result, final_state

    def workflow_job(self, workflow_path, job_name):
        lines = workflow_path.read_text(encoding="utf-8").splitlines()
        start = lines.index(f"  {job_name}:")
        end = len(lines)
        for index in range(start + 1, len(lines)):
            line = lines[index]
            if line.startswith("  ") and not line.startswith("   ") and line.endswith(":"):
                end = index
                break
        return "\n".join(lines[start:end])

    def test_publishes_draft_without_displacing_explicit_cli_latest(self):
        result, state = self.run_script(
            tag="codebox-server-v0.4.2",
            releases=[
                make_release(
                    "codebox-server-v0.4.2",
                    draft=True,
                    published_at=None,
                ),
                make_release(
                    "codebox-v0.21.0",
                    published_at="2026-11-24T10:43:15Z",
                    assets=["codebox-linux-x64", "SHA256SUMS"],
                ),
            ],
            explicit_latest="codebox-v0.21.0",
        )
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        server_rel = next(
            r for r in state["releases"] if r["tag_name"] == "codebox-server-v0.4.2"
        )
        self.assertFalse(server_rel["draft"])
        self.assertEqual(state["explicit_latest"], "codebox-v0.21.0")
        self.assertEqual(
            state["calls"],
            [
                [
                    "release",
                    "edit",
                    "codebox-server-v0.4.2",
                    "--repo",
                    "etalab-ia/codebox",
                    "--draft=false",
                    "--latest=false",
                ],
                ["api", "repos/etalab-ia/codebox/releases/latest"],
            ],
        )

    def test_recovers_when_server_release_already_hijacked_latest(self):
        # Reproduce the exact codebox-server-v0.4.1 incident: the server
        # release is already published and pinned as Latest, so --latest=false
        # is a no-op and the script must paginate releases and re-pin the
        # newest stable CLI release carrying SHA256SUMS.
        result, state = self.run_script(
            tag="codebox-server-v0.4.1",
            releases=[
                make_release(
                    "codebox-server-v0.4.1",
                    draft=False,
                    published_at="2026-11-24T10:47:35Z",
                ),
                make_release(
                    "codebox-v0.22.0",
                    draft=True,
                    published_at=None,
                    assets=["SHA256SUMS"],
                ),
                make_release(
                    "codebox-v0.22.0-rc.1",
                    prerelease=True,
                    published_at="2026-11-24T11:00:00Z",
                    assets=["SHA256SUMS"],
                ),
                make_release(
                    "codebox-v0.21.1",
                    draft=False,
                    published_at="2026-11-24T10:45:00Z",
                    assets=[],
                ),
                make_release(
                    "codebox-v0.21.0-beta.1",
                    draft=False,
                    prerelease=False,
                    published_at="2026-11-24T10:44:00Z",
                    assets=["SHA256SUMS"],
                ),
                make_release(
                    "codebox-v0.21.0",
                    draft=False,
                    published_at="2026-11-24T10:43:15Z",
                    assets=["codebox-linux-x64", "SHA256SUMS"],
                ),
                make_release(
                    "codebox-v0.9.0",
                    draft=False,
                    published_at="2026-10-01T00:00:00Z",
                    assets=["codebox-linux-x64", "SHA256SUMS"],
                ),
            ],
            explicit_latest="codebox-server-v0.4.1",
        )
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertEqual(state["explicit_latest"], "codebox-v0.21.0")
        self.assertIn("restored Latest release to codebox-v0.21.0", result.stdout)
        self.assertIn(
            [
                "release",
                "edit",
                "codebox-v0.21.0",
                "--repo",
                "etalab-ia/codebox",
                "--latest",
            ],
            state["calls"],
        )

    def test_recovers_when_github_falls_back_to_publish_date_without_explicit_latest(self):
        result, state = self.run_script(
            tag="codebox-server-v0.4.2",
            releases=[
                make_release(
                    "codebox-server-v0.4.2",
                    draft=True,
                    published_at=None,
                ),
                make_release(
                    "codebox-v0.21.0",
                    published_at="2026-11-24T10:43:15Z",
                    assets=["codebox-linux-x64", "SHA256SUMS"],
                ),
            ],
            explicit_latest=None,
            publish_timestamp="2026-11-24T12:00:00Z",
        )
        self.assertEqual(result.returncode, 0, msg=result.stderr)
        self.assertEqual(state["explicit_latest"], "codebox-v0.21.0")
        self.assertIn("restored Latest release to codebox-v0.21.0", result.stdout)

    def test_fails_loudly_when_no_published_cli_release_exists(self):
        result, state = self.run_script(
            tag="codebox-server-v0.4.2",
            releases=[
                make_release("codebox-server-v0.4.2", draft=True, published_at=None),
                make_release("codebox-v0.21.0", draft=True, assets=["SHA256SUMS"]),
                make_release("codebox-v0.20.0", draft=False, assets=[]),
            ],
            explicit_latest=None,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No published codebox CLI release with SHA256SUMS", result.stderr)
        self.assertIsNone(state["explicit_latest"])

    def test_fails_loudly_when_latest_still_points_to_server_after_recovery(self):
        result, _ = self.run_script(
            tag="codebox-server-v0.4.1",
            releases=[
                make_release(
                    "codebox-server-v0.4.1",
                    published_at="2026-11-24T10:47:35Z",
                ),
                make_release(
                    "codebox-v0.21.0",
                    published_at="2026-11-24T10:43:15Z",
                    assets=["SHA256SUMS"],
                ),
            ],
            explicit_latest="codebox-server-v0.4.1",
            force_latest_tag="codebox-server-v0.4.1",
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Failed to restore Latest to codebox-v0.21.0", result.stderr)

    def test_rejects_non_server_tag_before_calling_gh(self):
        for bad_tag in ("codebox-v0.21.0", "v0.4.1", "codebox-server-v0.4.1;rm"):
            with self.subTest(tag=bad_tag):
                result, state = self.run_script(
                    tag=bad_tag,
                    releases=[],
                )
                self.assertEqual(result.returncode, 2)
                self.assertEqual(state["calls"], [])
                self.assertIn("Expected a codebox-server release tag", result.stderr)

    def test_release_config_creates_server_releases_as_tagged_drafts(self):
        config = json.loads(RELEASE_CONFIG.read_text(encoding="utf-8"))
        server_pkg = config["packages"]["apps/codebox-server"]
        self.assertTrue(
            server_pkg.get("draft"),
            "apps/codebox-server must create draft releases so release-please cannot claim Latest",
        )
        self.assertTrue(
            server_pkg.get("force-tag-creation"),
            "apps/codebox-server must force tag creation so container checkout and changelog boundaries still see the tag while draft",
        )

    def test_release_workflow_wires_publish_server_release_guard(self):
        workflow = RELEASE_WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn("  mark-server-release-not-latest:", workflow)
        block = self.workflow_job(RELEASE_WORKFLOW, "publish-server-release")
        for snippet in (
            "needs: release-please",
            "if: needs.release-please.outputs.codebox_server_release_created == 'true'",
            "permissions:\n      contents: write",
            "uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
            "GH_TOKEN: ${{ github.token }}",
            "GH_REPO: ${{ github.repository }}",
            "TAG: ${{ needs.release-please.outputs.codebox_server_tag_name }}",
            "run: bash .github/scripts/publish-server-release.sh",
        ):
            self.assertIn(snippet, block)

    def test_ci_runs_publish_server_release_tests(self):
        block = self.workflow_job(CI_WORKFLOW, "platform-scope")
        self.assertIn("python3 .github/scripts/test_publish_server_release.py", block)


if __name__ == "__main__":
    unittest.main()
