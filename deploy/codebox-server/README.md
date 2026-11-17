# codebox-server deployment

The container is deployed into a dedicated, otherwise empty Scaleway project.
It holds no production infrastructure and no CI/CD secrets. The gateway's IAM
application is scoped to that project only.

## Deploy from the GitHub UI

The default deployment path is the **Deploy codebox server** GitHub Actions
workflow. It has only a `workflow_dispatch` trigger, serializes concurrent runs,
and installs a pinned, SHA256-verified Scaleway CLI through the official
`scaleway/action-scw` action, itself pinned to an immutable commit. Run it from
`main` after the workflow is merged; it is not automatically tested against real
infrastructure by a pull request.

Configure these repository settings before the first run:

| Name | Kind | Meaning |
| --- | --- | --- |
| `SCW_ACCESS_KEY` | secret | Access key of the deploy IAM application scoped to the dedicated project |
| `SCW_SECRET_KEY` | secret | Secret key of that deploy IAM application |
| `SCW_ORGANIZATION_ID` | variable | Scaleway organization UUID containing the dedicated project |
| `SCW_PROJECT_ID` | variable | Dedicated project UUID, not its name |
| `PROCONNECT_CLIENT_ID` | secret | ProConnect sandbox client ID |
| `PROCONNECT_CLIENT_SECRET` | secret | Regenerated ProConnect sandbox client secret |
| `SCW_REGISTRY_NAMESPACE` | variable | Registry namespace name, such as `codebox` |
| `CODEBOX_SERVER_URL` | variable | Final public HTTPS origin; leave unset for the bootstrap run |
| `CODEBOX_SERVER_ALLOWED_SIRETS` | variable | Optional comma-separated project-creation allowlist |

The deploy application needs `ContainersFullAccess`,
`ContainerRegistryReadOnly`, and `SecretManagerFullAccess` on the dedicated
project. Release CI can keep its separate `SCW_REGISTRY_SECRET_KEY` with
`ContainerRegistryFullAccess`; the deploy key does not need to push images.

Use this two-run bootstrap sequence:

1. Register the ProConnect application with the temporary callback
   `https://bootstrap.invalid/auth/callback`, leaving logout URLs empty and
   selecting RS256 where configurable. Leave `CODEBOX_SERVER_URL`
   unset.
2. Open **Actions → Deploy codebox server → Run workflow**, enter a published
   immutable tag such as `codebox-server-vX.Y.Z`, and run from `main`.
3. The workflow creates or reuses the `codebox-server` namespace and
   container, stores the signing seed in Secret Manager at
   `/gateway/gateway-token-seed`, waits for readiness, and prints the native
   HTTPS endpoint in the run summary. No credential values are printed.
4. Set `CODEBOX_SERVER_URL` to that native origin and replace the
   ProConnect temporary callback with `<native-origin>/auth/callback`.
5. Rerun the workflow from the GitHub UI with the same image tag. This updates
   the same container with the final public URL and preserves the signing seed.
   Check `<native-origin>/healthz` before attempting a CLI login.

Reruns converge to the selected image and configuration without creating
additional namespaces, containers, or token-seed versions. The workflow never
updates ProConnect or GitHub repository settings automatically.

## Build and publish

PR 5 adds a release-please component for `apps/codebox-server` with the tag
series `codebox-server-vX.Y.Z`. A release for that component builds and pushes
the distroless image from [Dockerfile](Dockerfile) to the Scaleway Container
Registry namespace named by the repository variable `SCW_REGISTRY_NAMESPACE`,
using the repository secret `SCW_REGISTRY_SECRET_KEY` with the fixed Docker
username `nologin`. No secret value is written to the image. The registry image
is named `codebox-server`.

## Operator-local OpenTofu alternative

OpenTofu reads the existing registry namespace and immutable image tag, creates
the Serverless Containers namespace and container, and injects non-secret
configuration plus secret environment variables. Supply all sensitive values
through `TF_VAR_*` or an operator-local `.tfvars` file that is never committed:

```bash
export TF_VAR_project_id="<codebox-dev-secrets project UUID>"
export TF_VAR_scw_access_key="<deploy IAM access key>"
export TF_VAR_scw_secret_key="<deploy IAM secret key>"
export TF_VAR_gateway_public_url="https://<gateway domain>"
export TF_VAR_gateway_token_seed="$(codebox-server -print-token-seed)"
export TF_VAR_proconnect_client_id="<ProConnect sandbox client ID>"
export TF_VAR_proconnect_client_secret="<ProConnect sandbox client secret>"
export TF_VAR_registry_namespace_name="<registry namespace>"
export TF_VAR_image_tag="codebox-server-vX.Y.Z"

tofu -chdir=deploy/codebox-server init
tofu -chdir=deploy/codebox-server plan
tofu -chdir=deploy/codebox-server apply
```

The default `PROCONNECT_ISSUER` is the verified ProConnect sandbox issuer
`https://fca.integ01.dev-agentconnect.fr/api/v2`. Production switches to
`https://auth.agentconnect.gouv.fr/api/v2` only after the DataPass
authorisation and the production client pair are available.

`gateway_public_url` must match the exact ProConnect redirect URI registered
for the client (`<url>/auth/callback`). Use the container's native HTTPS domain
after the first apply, or a stable custom domain configured before registering
the ProConnect application.

### OpenTofu operator values

| Name | Secret | Meaning |
| --- | --- | --- |
| `project_id` | no | UUID of the dedicated `codebox-dev-secrets` Scaleway project |
| `scw_access_key` | yes | IAM application access key used by OpenTofu and the gateway |
| `scw_secret_key` | yes | IAM application secret key used by OpenTofu and the gateway |
| `gateway_public_url` | no | Public HTTPS origin; ProConnect redirect is `<url>/auth/callback` |
| `gateway_token_seed` | yes | Base64url Ed25519 session-token seed; rotation logs everyone out |
| `proconnect_issuer` | no | Defaults to the verified sandbox issuer |
| `proconnect_client_id` | yes | ProConnect OIDC client ID |
| `proconnect_client_secret` | yes | ProConnect OIDC client secret |
| `registry_namespace_name` | no | Existing Scaleway registry namespace; must match `SCW_REGISTRY_NAMESPACE` |
| `registry_image_name` | no | Defaults to `codebox-server` |
| `image_tag` | no | Immutable release tag to deploy, e.g. `codebox-server-vX.Y.Z` |
| `SCW_REGISTRY_SECRET_KEY` | yes (repository secret) | API secret key used by release CI to push the image (`nologin` Docker username) |
| `allowed_sirets` | no | Optional comma-separated SIRET allowlist for project creation |

The CLI build separately consumes `CODEBOX_SERVER_URL` as a repository
variable through `-ldflags`. Forks and unconfigured release builds leave remote
secrets inert; local `secrets import` keeps working.

OpenTofu state contains the secret environment variables above. Keep the state
backend access-controlled and encrypted; never commit `.tfstate` or `.tfvars`.

The container is pinned to exactly one warm replica (`min_scale = max_scale = 1`).
Pending ProConnect login attempts and single-use terminal tickets live in the
gateway process's in-memory `AttemptStore`; multiple replicas would reject
callbacks that land on a different instance and could replay a terminal ticket,
while scaling to zero could discard a login in progress. Raise `max_scale` only
after moving those records to shared storage.
