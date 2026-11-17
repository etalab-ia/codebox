#!/usr/bin/env python3
"""Exercise the scw deployment reconciler against a stateful fake CLI."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
DEPLOY_SCRIPT = ROOT / ".github/scripts/deploy-codebox-server.sh"
WORKFLOW = ROOT / ".github/workflows/deploy-codebox-server.yml"
RELEASE_WORKFLOW = ROOT / ".github/workflows/release-please.yml"
RELEASE_CONFIG = ROOT / "release-please-config.json"
PROJECT_ID = "11111111-1111-4111-8111-111111111111"
ORGANIZATION_ID = "22222222-2222-4222-8222-222222222222"
DOMAIN = "codeboxsecretsgateway.functions.fnc.fr-par.scw.cloud"

FAKE_SCW = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

state_path = Path(os.environ["FAKE_SCW_STATE"])
if state_path.exists():
    state = json.loads(state_path.read_text())
else:
    state = {
        "namespaces": [], "containers": [], "secrets": [], "versions": {},
        "namespace_creations": 0, "container_creations": 0,
        "container_updates": 0, "secret_creations": 0, "version_creations": 0,
    }

args = sys.argv[1:]
json_output = False
wait = False
filtered = []
i = 0
while i < len(args):
    if args[i] == "-o":
        json_output = args[i + 1] == "json"
        i += 2
    elif args[i] == "--wait":
        wait = True
        i += 1
    else:
        filtered.append(args[i])
        i += 1
args = filtered


def fail(message):
    print(message, file=sys.stderr)
    sys.exit(2)


def save():
    state_path.write_text(json.dumps(state))


def emit(value, raw=False):
    save()
    if raw:
        sys.stdout.write(value)
    else:
        print(json.dumps(value))


def values(tokens):
    positional = []
    result = {}
    for token in tokens:
        if "=" not in token:
            positional.append(token)
            continue
        key, value = token.split("=", 1)
        if value == "true":
            value = True
        elif value == "false":
            value = False
        elif value.isdigit():
            value = int(value)
        result[key] = value
    return positional, result


def filtered_resources(items, options):
    result = items
    for argument, field in (("project-id", "project_id"), ("namespace-id", "namespace_id"), ("name", "name"), ("path", "path")):
        if argument in options:
            result = [item for item in result if item.get(field) == options[argument]]
    return result


def apply_options(item, options):
    for key, value in options.items():
        if key.startswith("environment-variables."):
            item.setdefault("environment_variables", {})[key.split(".", 1)[1]] = value
        elif key.startswith("secret-environment-variables."):
            item.setdefault("secret_environment_variables", {})[key.split(".", 1)[1]] = value
        else:
            item[key.replace("-", "_")] = value


if len(args) < 3:
    fail("not enough arguments")
command = tuple(args[:3])
if " ".join(command) in state.get("fail_commands", []):
    fail("injected CLI failure")
positional, options = values(args[3:])

if command == ("container", "namespace", "list"):
    emit(filtered_resources(state["namespaces"], options))
elif command == ("container", "namespace", "create"):
    if not wait:
        fail("namespace create did not wait")
    state["namespace_creations"] += 1
    item = {"id": "namespace-1", "status": "ready"}
    apply_options(item, options)
    state["namespaces"].append(item)
    emit(item)
elif command == ("secret", "secret", "list"):
    emit(filtered_resources(state["secrets"], options))
elif command == ("secret", "secret", "create"):
    state["secret_creations"] += 1
    item = {"id": "seed-secret-1"}
    apply_options(item, options)
    state["secrets"].append(item)
    state["versions"][item["id"]] = []
    emit(item)
elif command == ("secret", "version", "list"):
    secret_id = positional[0]
    versions = state["versions"].get(secret_id, [])
    if options.get("status.0") == "enabled":
        versions = [version for version in versions if version["status"] == "enabled"]
    emit([{k: v for k, v in version.items() if k != "data"} for version in versions])
elif command == ("secret", "version", "create"):
    secret_id = positional[0]
    source = options["data"]
    if not source.startswith("@"):
        fail("version data was not passed by file")
    data = Path(source[1:]).read_text()
    versions = state["versions"].setdefault(secret_id, [])
    revision = len(versions) + 1
    versions.append({"revision": revision, "status": "enabled", "data": data})
    state["version_creations"] += 1
    emit({"revision": revision, "status": "enabled"})
elif command == ("secret", "version", "access"):
    secret_id = positional[0]
    versions = [v for v in state["versions"].get(secret_id, []) if v["status"] == "enabled"]
    if not versions:
        fail("no enabled version")
    data = versions[-1]["data"]
    if options.get("raw") is True:
        emit(data, raw=True)
    else:
        emit({"data": data})
elif command == ("container", "container", "list"):
    emit(filtered_resources(state["containers"], options))
elif command == ("container", "container", "create"):
    if not wait:
        fail("container create did not wait")
    if options.get("https-connections-only") is not True:
        fail("create requires plural https-connections-only=true")
    state["container_creations"] += 1
    namespace_id = options["namespace-id"]
    namespaces = [item for item in state["namespaces"] if item["id"] == namespace_id]
    if len(namespaces) != 1:
        fail("namespace not found")
    item = {
        "id": "container-1", "status": "ready",
        "domain_name": "codeboxsecretsgateway.functions.fnc.fr-par.scw.cloud",
        "project_id": namespaces[0]["project_id"],
    }
    apply_options(item, options)
    state["containers"].append(item)
    emit(item)
elif command == ("container", "container", "update"):
    if "name" in options:
        fail("unknown argument 'name'")
    if not wait:
        fail("container update did not wait")
    if options.get("https-connection-only") is not True or "https-connections-only" in options:
        fail("update requires singular https-connection-only=true")
    container_id = positional[0]
    matches = [item for item in state["containers"] if item["id"] == container_id]
    if len(matches) != 1:
        fail("container not found")
    apply_options(matches[0], options)
    matches[0]["status"] = "ready"
    state["container_updates"] += 1
    emit(matches[0])
elif command == ("container", "container", "get"):
    container_id = positional[0]
    matches = [item for item in state["containers"] if item["id"] == container_id]
    if len(matches) != 1:
        fail("container not found")
    emit(matches[0])
else:
    fail("unsupported fake command: " + " ".join(command))
'''


def empty_state():
    return {
        "namespaces": [],
        "containers": [],
        "secrets": [],
        "versions": {},
        "namespace_creations": 0,
        "container_creations": 0,
        "container_updates": 0,
        "secret_creations": 0,
        "version_creations": 0,
    }


class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.state_path = self.directory / "state.json"
        self.output_path = self.directory / "github-output"
        self.fake_path = self.directory / "scw"
        self.fake_path.write_text(FAKE_SCW)
        self.fake_path.chmod(0o755)
        self.env = os.environ.copy()
        self.env.update({
            "PATH": str(self.directory) + os.pathsep + self.env["PATH"],
            "FAKE_SCW_STATE": str(self.state_path),
            "GITHUB_OUTPUT": str(self.output_path),
            "SCW_CLI": "scw",
            "SCW_ACCESS_KEY": "test-access-key",
            "SCW_SECRET_KEY": "test-scw-secret",
            "SCW_DEFAULT_ORGANIZATION_ID": ORGANIZATION_ID,
            "SCW_DEFAULT_PROJECT_ID": PROJECT_ID,
            "SCW_DEFAULT_REGION": "fr-par",
            "SCW_REGISTRY_NAMESPACE": "codebox",
            "PROCONNECT_CLIENT_ID": "test-proconnect-id",
            "PROCONNECT_CLIENT_SECRET": "test-proconnect-secret",
            "IMAGE_TAG": "codebox-server-v0.2.0",
        })
        self.env.pop("CODEBOX_SERVER_URL", None)

    def state(self):
        return json.loads(self.state_path.read_text())

    def run_deploy(self, overrides=None, expected=0):
        env = self.env.copy()
        if overrides:
            env.update(overrides)
        result = subprocess.run(
            ["bash", str(DEPLOY_SCRIPT)], env=env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertEqual(
            expected, result.returncode,
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}",
        )
        return result

    def assert_no_unmasked_secrets(self, result, seed):
        output = re.sub(r"::add-mask::[^\n]*", "", result.stdout + result.stderr)
        for value in ("test-access-key", "test-scw-secret", "test-proconnect-id", "test-proconnect-secret", seed):
            self.assertNotIn(value, output)

    def test_bootstrap_rerun_and_final_url_converge(self):
        first = self.run_deploy()
        initial = self.state()
        namespace_id = initial["namespaces"][0]["id"]
        container_id = initial["containers"][0]["id"]
        seed = initial["versions"]["seed-secret-1"][0]["data"]
        self.assertEqual(1, initial["namespace_creations"])
        self.assertEqual(1, initial["container_creations"])
        self.assertEqual(1, initial["secret_creations"])
        self.assertEqual(1, initial["version_creations"])
        self.assertEqual("https://bootstrap.invalid", initial["containers"][0]["environment_variables"]["GATEWAY_PUBLIC_URL"])
        self.assertTrue(initial["containers"][0]["https_connections_only"])
        self.assertEqual(512, initial["containers"][0]["mvcpu_limit"])
        self.assertEqual("1GB", initial["containers"][0]["memory_limit_bytes"])
        self.assertIn("native endpoint: https://" + DOMAIN, first.stdout)
        self.assert_no_unmasked_secrets(first, seed)

        second = self.run_deploy()
        repeated = self.state()
        self.assertEqual(namespace_id, repeated["namespaces"][0]["id"])
        self.assertEqual(container_id, repeated["containers"][0]["id"])
        self.assertEqual(seed, repeated["versions"]["seed-secret-1"][0]["data"])
        self.assertEqual(1, repeated["namespace_creations"])
        self.assertEqual(1, repeated["container_creations"])
        self.assertEqual(1, repeated["secret_creations"])
        self.assertEqual(1, repeated["version_creations"])
        self.assertEqual(1, repeated["container_updates"])
        self.assert_no_unmasked_secrets(second, seed)

        final_url = "https://" + DOMAIN
        third = self.run_deploy({
            "CODEBOX_SERVER_URL": final_url,
            "IMAGE_TAG": "codebox-server-v0.2.1",
        })
        final = self.state()
        self.assertEqual(1, len(final["namespaces"]))
        self.assertEqual(1, len(final["containers"]))
        self.assertEqual(container_id, final["containers"][0]["id"])
        self.assertEqual(seed, final["versions"]["seed-secret-1"][0]["data"])
        self.assertEqual(final_url, final["containers"][0]["environment_variables"]["GATEWAY_PUBLIC_URL"])
        self.assertEqual(
            "rg.fr-par.scw.cloud/codebox/codebox-server:codebox-server-v0.2.1",
            final["containers"][0]["image"],
        )
        self.assertEqual(2, final["container_updates"])
        self.assert_no_unmasked_secrets(third, seed)

    def test_recovers_secret_metadata_without_a_version(self):
        state = empty_state()
        state["secrets"] = [{
            "id": "seed-secret-1", "project_id": PROJECT_ID,
            "name": "gateway-token-seed", "path": "/gateway",
        }]
        state["versions"] = {"seed-secret-1": []}
        self.state_path.write_text(json.dumps(state))
        result = self.run_deploy()
        recovered = self.state()
        self.assertEqual(1, recovered["version_creations"])
        self.assertEqual(1, len(recovered["versions"]["seed-secret-1"]))
        self.assert_no_unmasked_secrets(result, recovered["versions"]["seed-secret-1"][0]["data"])

    def test_current_release_tag_uses_server_image(self):
        self.run_deploy({"IMAGE_TAG": "codebox-server-v0.2.0"})
        state = self.state()
        self.assertEqual(
            "rg.fr-par.scw.cloud/codebox/codebox-server:codebox-server-v0.2.0",
            state["containers"][0]["image"],
        )

    def test_missing_organization_id_fails_before_any_cli_call(self):
        env = self.env.copy()
        env.pop("SCW_DEFAULT_ORGANIZATION_ID")
        result = subprocess.run(
            ["bash", str(DEPLOY_SCRIPT)], env=env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertNotEqual(0, result.returncode)
        self.assertIn("Missing required environment variable: SCW_DEFAULT_ORGANIZATION_ID", result.stderr)
        self.assertFalse(self.state_path.exists())

    def test_missing_credentials_fail_before_any_cli_call(self):
        env = self.env.copy()
        env.pop("PROCONNECT_CLIENT_SECRET")
        result = subprocess.run(
            ["bash", str(DEPLOY_SCRIPT)], env=env, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
        )
        self.assertNotEqual(0, result.returncode)
        self.assertIn("Missing required environment variable: PROCONNECT_CLIENT_SECRET", result.stderr)
        self.assertFalse(self.state_path.exists())

    def test_invalid_public_url_fails_before_any_cli_call(self):
        result = self.run_deploy(
            {"CODEBOX_SERVER_URL": "https://gateway.example/path"}, expected=1,
        )
        self.assertIn("must be an HTTPS origin", result.stderr)
        self.assertFalse(self.state_path.exists())

    def test_duplicate_namespace_fails_closed(self):
        state = empty_state()
        for suffix in ("a", "b"):
            state["namespaces"].append({
                "id": "namespace-" + suffix, "project_id": PROJECT_ID,
                "name": "codebox-server", "status": "ready",
            })
        self.state_path.write_text(json.dumps(state))
        result = self.run_deploy(expected=1)
        self.assertIn("Multiple container namespaces", result.stderr)
        self.assertEqual([], self.state()["containers"])

    def test_duplicate_container_fails_closed(self):
        self.run_deploy()
        state = self.state()
        duplicate = dict(state["containers"][0])
        duplicate["id"] = "container-2"
        state["containers"].append(duplicate)
        self.state_path.write_text(json.dumps(state))
        result = self.run_deploy(expected=1)
        self.assertIn("Multiple containers", result.stderr)
        self.assertEqual(0, self.state()["container_updates"])

    def test_cli_failure_can_be_retried_without_duplicates(self):
        state = empty_state()
        state["fail_commands"] = ["secret secret list"]
        self.state_path.write_text(json.dumps(state))
        result = self.run_deploy(expected=2)
        self.assertIn("injected CLI failure", result.stderr)
        self.assertNotIn("is ready; native endpoint", result.stdout)
        self.assertEqual([], self.state()["containers"])
        state = self.state()
        state["fail_commands"] = []
        self.state_path.write_text(json.dumps(state))
        self.run_deploy()
        recovered = self.state()
        self.assertEqual(1, recovered["namespace_creations"])
        self.assertEqual(1, recovered["container_creations"])
        self.assertEqual(1, len(recovered["containers"]))

    def test_release_component_and_image_are_renamed(self):
        config = json.loads(RELEASE_CONFIG.read_text())
        self.assertEqual("codebox-server", config["packages"]["apps/codebox-server"]["component"])
        workflow = RELEASE_WORKFLOW.read_text()
        self.assertIn("build-codebox-server-container:", workflow)
        self.assertIn("codebox-server-*)", workflow)
        self.assertNotIn("codebox-server-* | codebox-server-*)", workflow)
        self.assertIn("/codebox-server:${{ needs.release-please.outputs.codebox_server_tag_name }}", workflow)

    def test_workflow_is_manual_only_and_serialized(self):
        workflow = WORKFLOW.read_text()
        self.assertIn("name: Deploy codebox server", workflow)
        self.assertIn("workflow_dispatch:", workflow)
        self.assertIn("group: codebox-server-deploy", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertNotIn("if: github.ref", workflow)
        self.assertIn("SCW_DEFAULT_ORGANIZATION_ID: ${{ vars.SCW_ORGANIZATION_ID }}", workflow)
        self.assertIn("SCW_ACCESS_KEY: ${{ secrets.SCW_ACCESS_KEY }}", workflow)
        self.assertIn("SCW_SECRET_KEY: ${{ secrets.SCW_SECRET_KEY }}", workflow)
        self.assertIn("SCW_DEFAULT_PROJECT_ID: ${{ vars.SCW_PROJECT_ID }}", workflow)
        self.assertIn("PROCONNECT_CLIENT_ID: ${{ secrets.PROCONNECT_CLIENT_ID }}", workflow)
        self.assertIn("PROCONNECT_CLIENT_SECRET: ${{ secrets.PROCONNECT_CLIENT_SECRET }}", workflow)
        self.assertIn("uses: scaleway/action-scw@2e34a1eb35cf3cac627f24643a101fea269cbd83", workflow)
        self.assertIn("version: v2.66.0", workflow)
        self.assertIn('description: "Immutable codebox-server release tag"', workflow)
        self.assertNotIn('default: "codebox-server-v', workflow)
        for trigger in ("\n  push:", "\n  pull_request:", "\n  schedule:"):
            self.assertNotIn(trigger, workflow)


if __name__ == "__main__":
    unittest.main(verbosity=2)
