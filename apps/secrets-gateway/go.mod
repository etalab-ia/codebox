module github.com/etalab-ia/codebox/apps/secrets-gateway

go 1.26.0

toolchain go1.26.10

require github.com/etalab-ia/codebox/packages/secrets v0.0.0

// The shared contract is resolved from the workspace checkout during local and
// CI builds; this relative replace keeps standalone module consumers (GOWORK=off,
// OSV-Scanner, Dependabot) resolving the same local package.
replace github.com/etalab-ia/codebox/packages/secrets => ../../packages/secrets
