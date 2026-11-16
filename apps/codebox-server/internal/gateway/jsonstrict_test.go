package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// TestPushRejectsLossyJSONBeforeDecoding covers the finding that request bodies
// were decoded straight into Go strings, so an unpaired surrogate became U+FFFD
// and a null became an empty string, storing altered credentials with a 200.
func TestPushRejectsLossyJSONBeforeDecoding(t *testing.T) {
	ts := newTestServer(t, Identity{})
	owner := TokenClaims{Subject: "s1", Email: "owner@beta.gouv.fr", ACR: "eidas2"}
	projectID := createProjectFor(t, ts, owner)
	token := ts.sessionToken(t, owner)

	bodies := map[string]string{
		"unpaired high surrogate": `{"baseRevision":1,"components":{".env":{"KEY":"\ud800"}}}`,
		"unpaired low surrogate":  `{"baseRevision":1,"components":{".env":{"KEY":"\udc00"}}}`,
		"null value":              `{"baseRevision":1,"components":{".env":{"KEY":null}}}`,
		"null component":          `{"baseRevision":1,"components":{".env":null}}`,
		"null scope":              `{"baseRevision":1,"scope":null,"components":{".env":{"KEY":"v"}}}`,
	}
	for name, body := range bodies {
		req := httptest.NewRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		ts.server.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", name, rec.Code, rec.Body.String())
			continue
		}
		var apiErr secrets.APIError
		decodeJSON(t, rec, &apiErr)
		if apiErr.Code != secrets.ErrCodeInvalidRequest {
			t.Errorf("%s: error code = %q, want %q", name, apiErr.Code, secrets.ErrCodeInvalidRequest)
		}
	}

	// Positive control through the same plumbing: a legitimate body still lands.
	rec := httptest.NewRecorder()
	req := newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", secrets.PushSecretsRequest{
		BaseRevision: 1,
		Components:   map[string]map[string]string{".env": {"KEY": "value-ok"}},
	})
	req.Header.Set("Authorization", "Bearer "+token)
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legitimate push status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// And the stored value is byte-exact, including astral characters.
	astral := "emoji-🚀-and-ñ"
	rec = httptest.NewRecorder()
	req = newJSONRequest(http.MethodPut, "/v1/projects/"+projectID+"/secrets", secrets.PushSecretsRequest{
		BaseRevision: 2,
		Components:   map[string]map[string]string{".env": {"KEY": astral}},
	})
	req.Header.Set("Authorization", "Bearer "+token)
	ts.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("astral push status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	resolveRec := httptest.NewRecorder()
	resolveReq := newJSONRequest(http.MethodPost, "/v1/projects/"+projectID+"/resolve", secrets.ResolveRequest{})
	resolveReq.Header.Set("Authorization", "Bearer "+token)
	ts.server.Handler().ServeHTTP(resolveRec, resolveReq)
	var resolved secrets.ResolveResponse
	decodeJSON(t, resolveRec, &resolved)
	if resolved.Components[".env"]["KEY"] != astral {
		t.Fatalf("resolved value = %q, want %q", resolved.Components[".env"]["KEY"], astral)
	}
}
