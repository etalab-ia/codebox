package codebox

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// SecretsGatewayURLDefault is the build-time endpoint for the #385
// development-secrets gateway. It is intentionally empty: no endpoint is
// invented, and a release build may set it with
// -ldflags "-X github.com/etalab-ia/codebox/apps/codebox/internal/codebox.SecretsGatewayURLDefault=...".
// CODEBOX_SECRETS_GATEWAY_URL overrides it for tests and development.
var SecretsGatewayURLDefault = ""

// SecretsGatewayURL resolves the configured gateway URL, or "" when no
// gateway was configured. An unconfigured build keeps the local-only secret
// workflow intact; a configured HTTPS URL is required for remote resolution.
func SecretsGatewayURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("CODEBOX_SECRETS_GATEWAY_URL"))
	if raw == "" {
		raw = strings.TrimSpace(SecretsGatewayURLDefault)
	}
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidSecretsGatewayURL
	}
	if u.User != nil {
		return "", ErrInvalidSecretsGatewayURL
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

// ErrInvalidSecretsGatewayURL is returned for a non-HTTPS or malformed gateway
// URL. The gateway carries session tokens and secret values, so plaintext or
// userinfo-bearing URLs are refused rather than truncated.
var ErrInvalidSecretsGatewayURL = errors.New("CODEBOX_SECRETS_GATEWAY_URL must be an absolute https URL without credentials, query or fragment")
