# secrets-gateway deployment

The container is deployed into a dedicated, otherwise empty Scaleway project
named `codebox-dev-secrets`. It holds no production infrastructure and no CI/CD
secrets. The gateway's IAM application is scoped to that project only.

## Build and publish

PR 5 adds a release-please component for `apps/secrets-gateway` with the tag
series `secrets-gateway-vX.Y.Z`. A release for that component builds and pushes
the distroless image from [Dockerfile](Dockerfile) to the Scaleway Container
Registry namespace named by the repository variable `SCW_REGISTRY_NAMESPACE`,
using the repository secret `SCW_REGISTRY_SECRET_KEY` with the fixed Docker
username `nologin`. No secret value is written to the image.

## Apply

OpenTofu reads the existing registry namespace and immutable image tag, creates
the Serverless Containers namespace and container, and injects non-secret
configuration plus secret environment variables. Supply all sensitive values
through `TF_VAR_*` or an operator-local `.tfvars` file that is never committed:

```bash
export TF_VAR_project_id="<codebox-dev-secrets project UUID>"
export TF_VAR_scw_access_key="<deploy IAM access key>"
export TF_VAR_scw_secret_key="<deploy IAM secret key>"
export TF_VAR_gateway_public_url="https://<gateway domain>"
export TF_VAR_gateway_token_seed="$(secrets-gateway -print-token-seed)"
export TF_VAR_proconnect_client_id="<ProConnect sandbox client ID>"
export TF_VAR_proconnect_client_secret="<ProConnect sandbox client secret>"
export TF_VAR_registry_namespace_name="<registry namespace>"
export TF_VAR_image_tag="secrets-gateway-v0.1.0"

tofu -chdir=deploy/secrets-gateway init
tofu -chdir=deploy/secrets-gateway plan
tofu -chdir=deploy/secrets-gateway apply
```

The default `PROCONNECT_ISSUER` is the verified ProConnect sandbox issuer
`https://fca.integ01.dev-agentconnect.fr/api/v2`. Production switches to
`https://auth.agentconnect.gouv.fr/api/v2` only after the DataPass
authorisation and the production client pair are available.

`gateway_public_url` must match the exact ProConnect redirect URI registered
for the client (`<url>/auth/callback`). Use the container's native HTTPS domain
after the first apply, or a stable custom domain configured before registering
the ProConnect application.

## Required operator values

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
| `registry_image_name` | no | Defaults to `secrets-gateway` |
| `image_tag` | no | Immutable release tag to deploy, e.g. `secrets-gateway-v0.1.0` |
| `SCW_REGISTRY_SECRET_KEY` | yes (repository secret) | API secret key used by release CI to push the image (`nologin` Docker username) |
| `allowed_sirets` | no | Optional comma-separated SIRET allowlist for project creation |

The CLI build separately consumes `CODEBOX_SECRETS_GATEWAY_URL` as a repository
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
