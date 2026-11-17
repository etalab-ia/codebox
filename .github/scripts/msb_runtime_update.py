#!/usr/bin/env python3
"""Prepare a coordinated Microsandbox SDK/runtime update from a verified attestation."""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile
import urllib.request

SDK_MODULE = "github.com/superradcompany/microsandbox/sdk/go"
DEFAULT_UPSTREAM_REPO = "superradcompany/microsandbox"
MAX_ARCHIVE_BYTES = 128 << 20
VERSION_RE = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$")
DIGEST_RE = re.compile(r"^[0-9a-f]{64}$")

ASSET_MEMBERS = {
    "microsandbox-darwin-aarch64.tar.gz": ("msb", "libkrunfw.5.dylib"),
    "microsandbox-linux-aarch64.tar.gz": ("msb", "libkrunfw.so.5.6.1"),
    "microsandbox-linux-x86_64.tar.gz": ("msb", "libkrunfw.so.5.6.1"),
    "microsandbox-windows-aarch64.tar.gz": ("msb.exe", "libkrunfw.dll"),
    "microsandbox-windows-x86_64.tar.gz": ("msb.exe", "libkrunfw.dll"),
}

ASSET_PLATFORMS = {
    "microsandbox-darwin-aarch64.tar.gz": ("darwin", "arm64"),
    "microsandbox-linux-aarch64.tar.gz": ("linux", "arm64"),
    "microsandbox-linux-x86_64.tar.gz": ("linux", "amd64"),
    "microsandbox-windows-aarch64.tar.gz": ("windows", "arm64"),
    "microsandbox-windows-x86_64.tar.gz": ("windows", "amd64"),
}

RUNTIME_SOURCE = "apps/codebox/internal/codebox/msb_runtime_install.go"
RUNTIME_TEST = "apps/codebox/internal/codebox/msb_runtime_install_test.go"
NATIVE_DIR = "apps/codebox/tests/integration/project-secrets/native"


class UpdateError(RuntimeError):
    """An update precondition or upstream artifact failed validation."""


def expected_assets_from_workflow(workflow: str) -> list[str]:
    match = re.search(r"EXPECTED_ASSETS:\s*>-\n((?:\s+microsandbox-[^\n]+\n)+)", workflow)
    if not match:
        raise UpdateError("cannot locate EXPECTED_ASSETS in msb-runtime-watch.yml")
    return re.findall(r"microsandbox-[A-Za-z0-9._-]+\.tar\.gz", match.group(1))


def attested_digests(attestation_path: Path, expected_assets: list[str]) -> dict[str, str]:
    try:
        document = json.loads(attestation_path.read_text())
        subjects = document["verificationResult"]["statement"]["subject"]
    except (OSError, KeyError, TypeError, json.JSONDecodeError) as error:
        raise UpdateError(f"invalid verified attestation: {error}") from error
    if set(expected_assets) != set(ASSET_MEMBERS) or len(expected_assets) != len(ASSET_MEMBERS):
        raise UpdateError("EXPECTED_ASSETS does not match the updater platform table")
    if not isinstance(subjects, list):
        raise UpdateError("invalid verified attestation: subject is not a list")
    found: dict[str, str] = {}
    for subject in subjects:
        if not isinstance(subject, dict):
            continue
        name = subject.get("name")
        digest_info = subject.get("digest")
        if name not in expected_assets:
            continue
        if name in found:
            raise UpdateError(f"attestation contains duplicate subject {name}")
        if not isinstance(digest_info, dict) or not isinstance(digest_info.get("sha256"), str):
            raise UpdateError(f"attestation has an invalid SHA-256 for {name}")
        digest = digest_info["sha256"]
        if not DIGEST_RE.fullmatch(digest):
            raise UpdateError(f"attestation has an invalid SHA-256 for {name}")
        found[name] = digest
    missing = set(expected_assets) - set(found)
    if missing:
        raise UpdateError(f"attestation is missing expected assets: {', '.join(sorted(missing))}")
    return found


def download_and_validate(version: str, upstream_repo: str, digests: dict[str, str]) -> None:
    """Validate all downloads before any repository file is modified."""
    with tempfile.TemporaryDirectory(prefix="codebox-msb-update-") as temporary:
        for asset, expected_members in ASSET_MEMBERS.items():
            url = f"https://github.com/{upstream_repo}/releases/download/v{version}/{asset}"
            archive_path = Path(temporary) / asset
            digest = hashlib.sha256()
            size = 0
            request = urllib.request.Request(url, headers={"User-Agent": "codebox-msb-runtime-watch"})
            try:
                with urllib.request.urlopen(request, timeout=90) as response, archive_path.open("wb") as output:
                    while chunk := response.read(1024 * 1024):
                        size += len(chunk)
                        if size > MAX_ARCHIVE_BYTES:
                            raise UpdateError(f"{asset} exceeds the {MAX_ARCHIVE_BYTES}-byte archive limit")
                        digest.update(chunk)
                        output.write(chunk)
            except UpdateError:
                raise
            except Exception as error:
                raise UpdateError(f"download {url}: {error}") from error
            actual = digest.hexdigest()
            if not hmac.compare_digest(actual, digests[asset]):
                raise UpdateError(f"{asset}: SHA-256 mismatch: got {actual}, expected {digests[asset]}")
            try:
                with tarfile.open(archive_path, mode="r:gz") as archive:
                    members = archive.getmembers()
            except (OSError, tarfile.TarError) as error:
                raise UpdateError(f"inspect {asset}: {error}") from error
            for expected_name in expected_members:
                matches = [
                    member
                    for member in members
                    if PurePosixPath(member.name).name == expected_name and member.isfile()
                ]
                if len(matches) != 1:
                    raise UpdateError(
                        f"{asset}: expected exactly one regular file named {expected_name}, found {len(matches)}"
                    )


def current_runtime_digests(source: str) -> dict[str, str]:
    result: dict[str, str] = {}
    for asset, (goos, goarch) in ASSET_PLATFORMS.items():
        case_match = re.search(
            rf'case "{re.escape(goos + "/" + goarch)}":\s*artifact = msbRuntimeArtifact\{{(.*?)\n\s*\}}',
            source,
            re.DOTALL,
        )
        if not case_match:
            raise UpdateError(f"cannot locate runtime artifact case {goos}/{goarch}")
        digest_match = re.search(r'sha256:\s*"([0-9a-f]{64})"', case_match.group(1))
        if not digest_match:
            raise UpdateError(f"cannot locate runtime digest for {goos}/{goarch}")
        result[asset] = digest_match.group(1)
    return result


def replace_exact(
    contents: dict[Path, str], root: Path, relative: str, old: str, new: str, expected_count: int = 1
) -> None:
    path = root / relative
    text = contents.get(path)
    if text is None:
        try:
            text = path.read_text()
        except OSError as error:
            raise UpdateError(f"read {relative}: {error}") from error
    actual_count = text.count(old)
    if actual_count != expected_count:
        raise UpdateError(f"{relative}: expected {expected_count} occurrences of {old!r}, found {actual_count}")
    contents[path] = text.replace(old, new)


def run_go(module_dir: Path, *arguments: str) -> None:
    env = os.environ.copy()
    env["GOWORK"] = "off"
    try:
        subprocess.run(["go", *arguments], cwd=module_dir, env=env, check=True)
    except (OSError, subprocess.CalledProcessError) as error:
        raise UpdateError(f"go {' '.join(arguments)} failed in {module_dir}: {error}") from error


def evidence_version(root: Path) -> str:
    text = (root / NATIVE_DIR / "evidence/README.md").read_text()
    match = re.search(r"SDK/runtime ([0-9]+\.[0-9]+\.[0-9]+)", text)
    if not match:
        raise UpdateError("cannot determine the historical native evidence version")
    return match.group(1)


def write_pr_body(
    path: Path,
    version: str,
    sdk_version: str,
    issue_number: int,
    digests: dict[str, str],
    native_version: str,
    upstream_repo: str,
) -> None:
    digest_lines = "\n".join(f"| `{asset}` | `{digest}` |" for asset, digest in digests.items())
    path.write_text(
        f"Prepare the coordinated Microsandbox SDK/runtime update from v{sdk_version} to v{version}. "
        "The archives were downloaded, checked against the verified release attestation, and inspected for the expected runtime members.\n\n"
        "This is a draft so review and hardware qualification decisions remain explicit. "
        "Marking it ready for review starts the required hosted CI checks. "
        f"Review the [upstream release](https://github.com/{upstream_repo}/releases/tag/v{version}) and "
        f"[changes since v{sdk_version}](https://github.com/{upstream_repo}/compare/v{sdk_version}...v{version}).\n\n"
        "<details><summary>Update details</summary>\n\n"
        "- Main and native-kit Go module pins, including `go.sum`\n"
        "- Runtime version, release URL, five embedded digests, and digest test table\n"
        "- Runtime and native-kit documentation\n\n"
        "| Archive | Verified SHA-256 |\n|---|---|\n"
        f"{digest_lines}\n\n"
        "</details>\n\n"
        "## Qualification boundary\n\n"
        f"Historical macOS secret-transport evidence remains pinned to v{native_version}; no new native pass is claimed. "
        "Its transcripts are unchanged. Scaleway hardware qualification, when selected during review, is manually dispatched; "
        "this automatic update neither provisions infrastructure nor runs paid hardware tests.\n\n"
        f"Closes #{issue_number}\n\n"
        "👾 Generated with [Letta Code](https://letta.com)\n"
    )


def update(
    repo_root: Path,
    attestation_path: Path,
    body_path: Path,
    version: str,
    sdk_version: str,
    issue_number: int,
    expected_assets: list[str],
    upstream_repo: str = DEFAULT_UPSTREAM_REPO,
) -> None:
    if not VERSION_RE.fullmatch(version) or not VERSION_RE.fullmatch(sdk_version):
        raise UpdateError("versions must be semantic versions without a leading v")
    if version == sdk_version:
        raise UpdateError("upstream and embedded versions already match")
    if issue_number <= 0:
        raise UpdateError("a positive tracking issue number is required")

    digests = attested_digests(attestation_path, expected_assets)
    download_and_validate(version, upstream_repo, digests)

    contents: dict[Path, str] = {}
    source_path = repo_root / RUNTIME_SOURCE
    old_digests = current_runtime_digests(source_path.read_text())
    replace_exact(contents, repo_root, RUNTIME_SOURCE, f'msbRuntimeVersion     = "{sdk_version}"', f'msbRuntimeVersion     = "{version}"')
    replace_exact(
        contents,
        repo_root,
        RUNTIME_SOURCE,
        f"https://github.com/{upstream_repo}/releases/download/v{sdk_version}",
        f"https://github.com/{upstream_repo}/releases/download/v{version}",
    )
    for asset in ASSET_MEMBERS:
        replace_exact(contents, repo_root, RUNTIME_SOURCE, old_digests[asset], digests[asset])
        replace_exact(contents, repo_root, RUNTIME_TEST, old_digests[asset], digests[asset])

    replace_exact(contents, repo_root, "docs/runtime-microsandbox.md", f"releases/tag/v{sdk_version}", f"releases/tag/v{version}")
    replace_exact(contents, repo_root, "docs/runtime-microsandbox.md", f"msb {sdk_version}", f"msb {version}")

    native_main = f"{NATIVE_DIR}/main.go"
    replace_exact(contents, repo_root, native_main, f'const runtimeVersion = "{sdk_version}"', f'const runtimeVersion = "{version}"')
    replace_exact(contents, repo_root, native_main, old_digests["microsandbox-darwin-aarch64.tar.gz"], digests["microsandbox-darwin-aarch64.tar.gz"])
    replace_exact(contents, repo_root, f"{NATIVE_DIR}/run.py", f"SDK_VERSION='{sdk_version}'", f"SDK_VERSION='{version}'")
    replace_exact(contents, repo_root, f"{NATIVE_DIR}/runtime-download.py", f"VERSION='{sdk_version}'", f"VERSION='{version}'")
    replace_exact(
        contents,
        repo_root,
        f"{NATIVE_DIR}/runtime-download.py",
        old_digests["microsandbox-darwin-aarch64.tar.gz"],
        digests["microsandbox-darwin-aarch64.tar.gz"],
    )
    native_readme = f"{NATIVE_DIR}/README.md"
    replace_exact(contents, repo_root, native_readme, f"{SDK_MODULE} v{sdk_version}", f"{SDK_MODULE} v{version}")
    replace_exact(contents, repo_root, native_readme, f"releases/download/v{sdk_version}", f"releases/download/v{version}")
    replace_exact(
        contents,
        repo_root,
        native_readme,
        old_digests["microsandbox-darwin-aarch64.tar.gz"],
        digests["microsandbox-darwin-aarch64.tar.gz"],
    )

    for module in ("apps/codebox", NATIVE_DIR):
        mod_path = repo_root / module / "go.mod"
        mod_text = mod_path.read_text()
        old_pin = f"{SDK_MODULE} v{sdk_version}"
        if mod_text.count(old_pin) != 1:
            raise UpdateError(f"{module}/go.mod does not contain exactly one {old_pin} pin")

    for path, text in contents.items():
        path.write_text(text)
    for module in ("apps/codebox", NATIVE_DIR):
        module_dir = repo_root / module
        run_go(module_dir, "get", f"{SDK_MODULE}@v{version}")
        run_go(module_dir, "mod", "tidy")

    write_pr_body(body_path, version, sdk_version, issue_number, digests, evidence_version(repo_root), upstream_repo)
    print(f"Prepared Microsandbox v{sdk_version} -> v{version}; PR body: {body_path}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo-root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--attestation", type=Path, required=True)
    parser.add_argument("--body-path", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--sdk-version", required=True)
    parser.add_argument("--issue-number", type=int, required=True)
    parser.add_argument("--upstream-repo", default=DEFAULT_UPSTREAM_REPO)
    args = parser.parse_args()
    expected_assets = os.environ.get("EXPECTED_ASSETS", "").split()
    update(
        repo_root=args.repo_root,
        attestation_path=args.attestation,
        body_path=args.body_path,
        version=args.version,
        sdk_version=args.sdk_version,
        issue_number=args.issue_number,
        expected_assets=expected_assets,
        upstream_repo=args.upstream_repo,
    )


if __name__ == "__main__":
    main()
