# codebox-server deployment

`apps/codebox-server` is a stateless HTTP service. It holds no database, no
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
| `GATEWAY_TOKEN_SEED` | **yes** | base64url (no padding) 32-byte Ed25519 seed. Generate once with `codebox-server -print-token-seed`. Rotating it invalidates every issued session. |
| `GATEWAY_TOKEN_LIFETIME` | no | Session lifetime, default `12h`, maximum 24h. |
| `GATEWAY_TICKET_LIFETIME` | no | Lifetime of the terminal paste-the-code ticket, default `5m`. |
| `PROCONNECT_ISSUER` | no | Default `https://fca.integ01.dev-agentconnect.fr/api/v2` (sandbox). Production is `https://auth.agentconnect.gouv.fr/api/v2`. The `/api/v2` segment is part of the issuer: discovery is `<issuer>/.well-known/openid-configuration`, and omitting the segment returns 404. |
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
2. Declare the login redirect URI exactly: `<GATEWAY_PUBLIC_URL>/auth/callback`.
3. Declare the post-logout redirect URI exactly:
   `<GATEWAY_PUBLIC_URL>/auth/logout/callback`.
4. Select `RS256`, `ES256` or `HS256` for ID-token signing and, when signed
   UserInfo is enabled, for UserInfo signing as well.
5. Record the client ID and secret as deployment secrets.
6. For production, complete the DataPass authorisation and then change only
   `PROCONNECT_ISSUER` and the client pair.

## Domains and issuers

Verified on 2026-11-14 against the published
[domain table](https://partenaires.proconnect.gouv.fr/docs/ressources/valeur_ac_domain)
and by fetching both discovery documents:

| Environment | Network | Issuer |
| --- | --- | --- |
| Integration (sandbox) | Internet | `https://fca.integ01.dev-agentconnect.fr/api/v2` |
| Integration (sandbox) | RIE | `https://fca.integ02.agentconnect.rie.gouv.fr/api/v2` |
| Production | Internet | `https://auth.agentconnect.gouv.fr/api/v2` |

The sandbox discovery document advertises `HS256`, `ES256` and `RS256` for both
ID tokens and UserInfo. Select the registered algorithm for both token types:
RS256 and ES256 are verified with the provider's public JWKS, while HS256 is
verified only with `PROCONNECT_CLIENT_SECRET`. Public JWKS material is never used
as an HMAC secret. The gateway also accepts an unsigned JSON UserInfo response
when no UserInfo signing algorithm is registered.

Note that `test.proconnect.gouv.fr` is the integration test application, not an
OIDC issuer: pointing `PROCONNECT_ISSUER` at it returns 404 from discovery.

## Two-factor requirement

The gateway requests `acr` values `eidas0-mfa`, `eidas1-mfa`, `eidas2`, `eidas3` as
*essential* and rejects any ID token whose `acr` is not a two-factor value.
HR-backed identity providers return `eidas2`/`eidas3`; providers without HR
provisioning return `eidas1-mfa` or a `*-2fa` ProConnect assurance value. A
password-only assertion (`eidas1`, `eidas0`) is refused with `mfa_required`.

## Logout

`codebox secrets logout` removes the gateway session and retained ProConnect ID
token from the OS credential store, then starts the browser end-session flow.
The gateway discovers `end_session_endpoint`, generates a fresh single-use state,
and returns a URL containing `id_token_hint`, that state and the exact registered
`post_logout_redirect_uri`. `codebox secrets logout --terminal` prints the URL
instead of opening a browser. If discovery or ProConnect is unavailable, local
credential removal still succeeds and the CLI says that remote logout did not
start.

## Audit logging

Every meaningful action writes one JSON line to stdout: login start, provider
error code, MFA refusal, project creation, member update, secret push, and
secret resolution. Records carry the client IP, a hashed subject, the project
identifier, counts and revision numbers. **They never carry a secret value, a
secret name, an OAuth code, or a bearer token**; a canary test enforces this.

A failed login start (`login.start_failed`) also carries an `error_class`
naming the failure leg and kind — for example `discovery_timeout` (connection
to the issuer blackholed or filtered), `discovery_dns`, `token_http_status`,
`jwks_bad_payload`. The class is derived from the error chain; the wrapped
error text, which can embed request URLs or provider detail, is never logged.

Log retention is the platform's responsibility. The gateway keeps nothing.
