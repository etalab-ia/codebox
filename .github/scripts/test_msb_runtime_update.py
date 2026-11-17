#!/usr/bin/env python3
"""Unit tests for the attestation-backed Microsandbox update preparer."""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("msb_runtime_update.py")
SPEC = importlib.util.spec_from_file_location("msb_runtime_update", SCRIPT)
UPDATER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(UPDATER)
REPO_ROOT = Path(__file__).resolve().parents[2]

REQUIRED_FILES = [
    "apps/codebox/go.mod",
    "apps/codebox/go.sum",
    UPDATER.RUNTIME_SOURCE,
    UPDATER.RUNTIME_TEST,
    "docs/runtime-microsandbox.md",
    f"{UPDATER.NATIVE_DIR}/go.mod",
    f"{UPDATER.NATIVE_DIR}/go.sum",
    f"{UPDATER.NATIVE_DIR}/main.go",
    f"{UPDATER.NATIVE_DIR}/run.py",
    f"{UPDATER.NATIVE_DIR}/runtime-download.py",
    f"{UPDATER.NATIVE_DIR}/README.md",
    f"{UPDATER.NATIVE_DIR}/evidence/README.md",
    f"{UPDATER.NATIVE_DIR}/evidence/darwin-arm64-run-1.txt",
    f"{UPDATER.NATIVE_DIR}/evidence/darwin-arm64-run-2.txt",
]


def archive_bytes(members: tuple[str, ...]) -> bytes:
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w:gz") as archive:
        for name in members:
            payload = f"fixture:{name}\n".encode()
            member = tarfile.TarInfo(f"package/{name}")
            member.size = len(payload)
            member.mode = 0o755
            archive.addfile(member, io.BytesIO(payload))
    return output.getvalue()


class UpdateTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "repo"
        self.root.mkdir()
        for relative in REQUIRED_FILES:
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(REPO_ROOT / relative, destination)
        runtime = (self.root / UPDATER.RUNTIME_SOURCE).read_text()
        version_match = re.search(r'msbRuntimeVersion\s+=\s+"([0-9]+)\.([0-9]+)\.([0-9]+)"', runtime)
        self.assertIsNotNone(version_match, "test fixture must expose its actual source runtime pin")
        major, minor, patch = (int(part) for part in version_match.groups())
        self.sdk_version = f"{major}.{minor}.{patch}"
        self.version = f"{major}.{minor}.{patch + 1}"
        self.archives = {
            asset: archive_bytes(members) for asset, members in UPDATER.ASSET_MEMBERS.items()
        }
        self.attestation = Path(self.temporary.name) / "attestation.json"
        self.write_attestation(self.archives)
        self.body = Path(self.temporary.name) / "pr-body.md"

    def tearDown(self):
        self.temporary.cleanup()

    def write_attestation(self, archives):
        subjects = [
            {"name": asset, "digest": {"sha256": hashlib.sha256(payload).hexdigest()}}
            for asset, payload in archives.items()
        ]
        self.attestation.write_text(json.dumps({"verificationResult": {"statement": {"subject": subjects}}}))

    def fake_urlopen(self, request, timeout):
        asset = request.full_url.rsplit("/", 1)[-1]
        return io.BytesIO(self.archives[asset])

    def fake_run(self, command, cwd, env, check):
        self.assertEqual(command[0], "go")
        self.assertEqual(env["GOWORK"], "off")
        self.assertTrue(check)
        module_dir = Path(cwd)
        if command[1] == "get":
            self.assertEqual(command[2], f"{UPDATER.SDK_MODULE}@v{self.version}")
            for name in ("go.mod", "go.sum"):
                path = module_dir / name
                path.write_text(path.read_text().replace(f"v{self.sdk_version}", f"v{self.version}"))
        else:
            self.assertEqual(command[1:], ["mod", "tidy"])
        return subprocess.CompletedProcess(command, 0)

    def run_update(self):
        return UPDATER.update(
            repo_root=self.root,
            attestation_path=self.attestation,
            body_path=self.body,
            version=self.version,
            sdk_version=self.sdk_version,
            issue_number=419,
            expected_assets=list(UPDATER.ASSET_MEMBERS),
        )

    def snapshot(self):
        return {relative: (self.root / relative).read_bytes() for relative in REQUIRED_FILES}

    def test_happy_path_updates_all_pins_and_preserves_evidence(self):
        evidence = {
            name: (self.root / f"{UPDATER.NATIVE_DIR}/evidence/{name}").read_bytes()
            for name in ("README.md", "darwin-arm64-run-1.txt", "darwin-arm64-run-2.txt")
        }
        with mock.patch.object(UPDATER.urllib.request, "urlopen", side_effect=self.fake_urlopen), mock.patch.object(
            UPDATER.subprocess, "run", side_effect=self.fake_run
        ) as run:
            self.run_update()
        self.assertEqual(run.call_count, 4)
        runtime = (self.root / UPDATER.RUNTIME_SOURCE).read_text()
        runtime_test = (self.root / UPDATER.RUNTIME_TEST).read_text()
        self.assertIn(f'msbRuntimeVersion     = "{self.version}"', runtime)
        self.assertIn(f"releases/download/v{self.version}", runtime)
        for asset, payload in self.archives.items():
            digest = hashlib.sha256(payload).hexdigest()
            expected_count = sum(
                hashlib.sha256(candidate).hexdigest() == digest for candidate in self.archives.values()
            )
            self.assertEqual(runtime.count(digest), expected_count, asset)
            self.assertEqual(runtime_test.count(digest), expected_count, asset)
        native_main = (self.root / f"{UPDATER.NATIVE_DIR}/main.go").read_text()
        self.assertIn(f'const runtimeVersion = "{self.version}"', native_main)
        self.assertIn(f"SDK_VERSION='{self.version}'", (self.root / f"{UPDATER.NATIVE_DIR}/run.py").read_text())
        self.assertIn(
            f"VERSION='{self.version}'", (self.root / f"{UPDATER.NATIVE_DIR}/runtime-download.py").read_text()
        )
        for name, before in evidence.items():
            self.assertEqual((self.root / f"{UPDATER.NATIVE_DIR}/evidence/{name}").read_bytes(), before)
        body = self.body.read_text()
        self.assertIn("Closes #419", body)
        self.assertIn("manually dispatched", body)
        self.assertIn(f"compare/v{self.sdk_version}...v{self.version}", body)
        self.assertIn(f"v{UPDATER.evidence_version(self.root)}", body)
        self.assertIn("👾 Generated with", body)

    def test_missing_attested_asset_fails_before_download_or_edits(self):
        document = json.loads(self.attestation.read_text())
        document["verificationResult"]["statement"]["subject"].pop()
        self.attestation.write_text(json.dumps(document))
        before = self.snapshot()
        with mock.patch.object(UPDATER.urllib.request, "urlopen") as download, mock.patch.object(
            UPDATER.subprocess, "run"
        ) as run:
            with self.assertRaisesRegex(UPDATER.UpdateError, "missing expected assets"):
                self.run_update()
        download.assert_not_called()
        run.assert_not_called()
        self.assertEqual(self.snapshot(), before)

    def test_digest_mismatch_leaves_tree_unchanged(self):
        document = json.loads(self.attestation.read_text())
        document["verificationResult"]["statement"]["subject"][0]["digest"]["sha256"] = "0" * 64
        self.attestation.write_text(json.dumps(document))
        before = self.snapshot()
        with mock.patch.object(UPDATER.urllib.request, "urlopen", side_effect=self.fake_urlopen), mock.patch.object(
            UPDATER.subprocess, "run"
        ) as run:
            with self.assertRaisesRegex(UPDATER.UpdateError, "SHA-256 mismatch"):
                self.run_update()
        run.assert_not_called()
        self.assertEqual(self.snapshot(), before)

    def test_unexpected_archive_layout_leaves_tree_unchanged(self):
        asset = "microsandbox-darwin-aarch64.tar.gz"
        self.archives[asset] = archive_bytes(("msb",))
        self.write_attestation(self.archives)
        before = self.snapshot()
        with mock.patch.object(UPDATER.urllib.request, "urlopen", side_effect=self.fake_urlopen), mock.patch.object(
            UPDATER.subprocess, "run"
        ) as run:
            with self.assertRaisesRegex(UPDATER.UpdateError, "libkrunfw.5.dylib"):
                self.run_update()
        run.assert_not_called()
        self.assertEqual(self.snapshot(), before)

    def test_workflow_asset_list_matches_updater(self):
        workflow = (REPO_ROOT / ".github/workflows/msb-runtime-watch.yml").read_text()
        self.assertEqual(set(UPDATER.ASSET_MEMBERS), set(UPDATER.expected_assets_from_workflow(workflow)))


if __name__ == "__main__":
    unittest.main()
