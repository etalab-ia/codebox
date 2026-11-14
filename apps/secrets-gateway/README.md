# secrets-gateway deployment

`apps/secrets-gateway` is a stateless HTTP service. It holds no database, no
user table and no secret of its own beyond the Scaleway IAM application key it
uses to read the dedicated secrets project.

## Why a dedicated Scaleway project

The gateway runs against a **separate, otherwise empty Scaleway project**
(`codebox-dev-secrets`) that contains no application infrastructure. Its IAM
application is scoped to that project only, so a total gateway compromise
cannot read another project's secrets. This is a hard resource boundary, not a
CEL condition, and it removes the need for folder-prefix qualification.

Two protected secrets exist per `codebox` project inside that Scaleway project:

| Path | Purpose |
| --- | --- |
| `/acl/<projectId>` | Line-oriented access-control list (`owner:` / `member:` / `siret:`). |
| `/secrets/<projectId>` | Single versioned JSON bundle `{component -> {NAME -> value}}`, capped at 60 KiB. |

`Secret protection` is enabled on both, so neither can be deleted from the
Scaleway console even by an administrator mistake. Versions are immutable; the
gateway disables the previous version when it writes a new one.

## Environment variables

| Variable | Required | Meaning |
| --- | --- | --- |
| `PORT` | no | Listen port, default `8080` (`PORT_LISTEN` overrides the whole address). |
| `GATEWAY_PUBLIC_URL` | **yes** | Externally reachable origin. The ProConnect redirect is derived from it (`<url>/auth/callback`). |
| `GATEWAY_TOKEN_SEED` | **yes** | base64url (no padding) 32-byte Ed25519 seed. Generate once with `secrets-gateway -print-token-seed`. Rotating it invalidates every issued session. |
| `GATEWAY_TOKEN_LIFETIME` | no | Session lifetime, default `12h`, maximum 24h. |
| `GATEWAY_TICKET_LIFETIME` | no | Lifetime of the terminal paste-the-code ticket, default `5m`. |
| `PROCONNECT_ISSUER` | no | Default `https://test.proconnect.gouv.fr`. Production changes only this value. |
| `PROCONNECT_CLIENT_ID` | **yes** | OIDC client ID issued by the ProConnect partner portal. |
| `PROCONNECT_CLIENT_SECRET` | **yes** | OIDC client secret. |
| `PROCONNECT_SCOPES` | no | Comma-separated scopes; defaults to `openid,email,given_name,usual_name,uid,siret`. |
| `GATEWAY_ALLOWED_SIRETS` | no | Comma-separated SIRET allowlist for project **creation**. Empty means "any recognised public-sector domain". |
| `SCW_SECRET_KEY` | **yes** | Scaleway API secret key of the `codebox-dev-secrets` IAM application. |
| `SCW_PROJECT_ID` | **yes** | Scaleway project ID of `codebox-dev-secrets`. |
| `SCW_REGION` | no | `fr-par` (default), `nl-ams` or `pl-waw`. |
| `SCW_API_URL` | no | Default `https://api.scaleway.com`; used by tests. |

The service refuses to start when a required variable is missing, so a
misconfigured deployment fails at boot rather than at the first user request.

## ProConnect registration

1. Create the OIDC application in the ProConnect partner portal (sandbox first).
2. Declare the redirect URI exactly: `<GATEWAY_PUBLIC_URL>/auth/callback`.
3. Record the client ID and secret as deployment secrets.
4. For production, complete the DataPass authorisation and then change only
   `PROCONNECT_ISSUER` and the client pair.

## Two-factor requirement

The gateway requests `acr` values `eidas1-mfa`, `eidas2`, `eidas3` as
*essential* and rejects any ID token whose `acr` is not a two-factor value.
HR-backed identity providers return `eidas2`/`eidas3`; providers without HR
provisioning return `eidas1-mfa` or a `*-2fa` ProConnect assurance value. A
password-only assertion (`eidas1`, `eidas0`) is refused with `mfa_required`.

## Audit logging

Every meaningful action writes one JSON line to stdout: login start, provider
error code, MFA refusal, project creation, member update, secret push, and
secret resolution. Records carry the client IP, a hashed subject, the project
identifier, counts and revision numbers. **They never carry a secret value, a
secret name, an OAuth code, or a bearer token**; a canary test enforces this.

Log retention is the platform's responsibility. The gateway keeps nothing.
