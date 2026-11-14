// Package gateway implements the stateless secrets-gateway: ProConnect OIDC
// authentication, authorisation against a per-project access-control list, and
// resolution of development secret bundles held in an isolated Scaleway Secret
// Manager project.
//
// The service never persists state and never logs a secret value, an OAuth
// authorization code, or a bearer token.
package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// writeJSON writes v as a JSON response body with the given status code.
// Encoding failures after headers are sent cannot be recovered, so they are
// best effort by design.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal_error","message":"response encoding failed"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// writeError emits the redacted error envelope. The message never carries a
// secret value, a token, or an OAuth code: callers pass fixed strings.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, secrets.APIError{Code: code, Message: message})
}
