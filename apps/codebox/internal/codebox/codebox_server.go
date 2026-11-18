package codebox

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// CodeboxServerURLDefault is the build-time endpoint for the #385
// development-secrets server. It is intentionally empty: no endpoint is
// invented, and a release build may set it with
// -ldflags "-X github.com/etalab-ia/codebox/apps/codebox/internal/codebox.CodeboxServerURLDefault=...".
// CODEBOX_SERVER_URL overrides it for tests and development.
var CodeboxServerURLDefault = ""

// CodeboxServerURL resolves the configured gateway URL, or "" when no
// gateway was configured. An unconfigured build keeps the local-only secret
// workflow intact; a configured HTTPS URL is required for remote resolution.
func CodeboxServerURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("CODEBOX_SERVER_URL"))
	if raw == "" {
		raw = strings.TrimSpace(CodeboxServerURLDefault)
	}
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrInvalidCodeboxServerURL
	}
	if u.User != nil {
		return "", ErrInvalidCodeboxServerURL
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String(), nil
}

// ErrInvalidCodeboxServerURL is returned for a non-HTTPS or malformed gateway
// URL. The gateway carries session tokens and secret values, so plaintext or
// userinfo-bearing URLs are refused rather than truncated.
var ErrInvalidCodeboxServerURL = errors.New("CODEBOX_SERVER_URL doit être une URL https absolue sans identifiants, paramètres de requête ni fragment")
