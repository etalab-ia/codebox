package gatewayclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/packages/secrets"
)

func TestValueSourceResolvesAllIdentitiesInOneCall(t *testing.T) {
	const projectID = "22222222-2222-4222-8222-222222222222"
	var calls int
	var gotComponents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req secrets.ResolveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotComponents = req.Components
		_ = json.NewEncoder(w).Encode(secrets.ResolveResponse{
			ProjectID: projectID,
			Revision:  4,
			Components: map[string]map[string]string{
				".env":          {"API_KEY": "a", "DB_PASSWORD": "b"},
				"apps/api/.env": {"TOKEN": "c"},
			},
		})
	}))
	defer server.Close()

	source, err := NewValueSource(newForTest(server.URL, "token", server.Client()), projectID)
	if err != nil {
		t.Fatalf("NewValueSource: %v", err)
	}
	requests := []codebox.ProjectSecretValueRequest{
		{Identity: codebox.ProjectSecretIdentity{Profile: "development", Component: ".env", Name: "API_KEY"}, Reference: "r1"},
		{Identity: codebox.ProjectSecretIdentity{Profile: "development", Component: ".env", Name: "DB_PASSWORD"}, Reference: "r2"},
		{Identity: codebox.ProjectSecretIdentity{Profile: "development", Component: "apps/api/.env", Name: "TOKEN"}, Reference: "r3"},
	}
	values, err := source.ResolveProjectSecretValues(context.Background(), requests)
	if err != nil {
		t.Fatalf("ResolveProjectSecretValues: %v", err)
	}
	if calls != 1 {
		t.Fatalf("gateway resolve calls = %d, want 1", calls)
	}
	if len(gotComponents) != 2 {
		t.Fatalf("requested components = %v, want the two distinct component paths", gotComponents)
	}
	if values[requests[0].Identity].Value != "a" || values[requests[1].Identity].Value != "b" || values[requests[2].Identity].Value != "c" {
		t.Fatalf("resolved values = %+v", values)
	}
	if values[requests[0].Identity].Source != "gateway" || values[requests[0].Identity].Reference != "r1" {
		t.Fatalf("resolved metadata = %+v", values[requests[0].Identity])
	}
}

func TestValueSourceFailsClosedOnMissingVariable(t *testing.T) {
	const projectID = "22222222-2222-4222-8222-222222222222"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(secrets.ResolveResponse{
			ProjectID: projectID, Revision: 4,
			Components: map[string]map[string]string{".env": {"OTHER": "x"}},
		})
	}))
	defer server.Close()
	source, err := NewValueSource(newForTest(server.URL, "token", server.Client()), projectID)
	if err != nil {
		t.Fatalf("NewValueSource: %v", err)
	}
	_, err = source.ResolveProjectSecretValues(context.Background(), []codebox.ProjectSecretValueRequest{
		{Identity: codebox.ProjectSecretIdentity{Profile: "development", Component: ".env", Name: "API_KEY"}},
	})
	if err == nil {
		t.Fatal("a missing approved variable was silently accepted")
	}
}
