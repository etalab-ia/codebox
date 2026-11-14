package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/apps/codebox/internal/gatewayclient"
	"github.com/etalab-ia/codebox/packages/secrets"
)

type fakeCredentialStore struct {
	values map[codebox.CredentialKind]string
}

func (f *fakeCredentialStore) Kind() string { return "fake" }
func (f *fakeCredentialStore) Get(_ context.Context, kind codebox.CredentialKind) (string, error) {
	v, ok := f.values[kind]
	if !ok {
		return "", codebox.ErrCredentialNotFound
	}
	return v, nil
}
func (f *fakeCredentialStore) Put(_ context.Context, kind codebox.CredentialKind, value string) error {
	f.values[kind] = value
	return nil
}
func (f *fakeCredentialStore) Remove(_ context.Context, kind codebox.CredentialKind) error {
	if _, ok := f.values[kind]; !ok {
		return codebox.ErrCredentialNotFound
	}
	delete(f.values, kind)
	return nil
}
func (f *fakeCredentialStore) Verify(context.Context) error { return nil }
func (f *fakeCredentialStore) Generation(context.Context, codebox.CredentialKind) (string, error) {
	return "", nil
}

func withRemoteSeams(t *testing.T, base string, store *fakeCredentialStore) *bytes.Buffer {
	t.Helper()
	oldBase, oldStore, oldClient, oldOut := secretsGatewayBaseURLFn, secretsGatewayStoreFn, secretsGatewayClientFn, secretsGatewayLoginOut
	buf := &bytes.Buffer{}
	secretsGatewayBaseURLFn = func() (string, error) { return base, nil }
	secretsGatewayStoreFn = func() (codebox.CredentialStore, error) { return store, nil }
	secretsGatewayLoginOut = buf
	if strings.HasPrefix(base, "http://") {
		secretsGatewayClientFn = func() (*gatewayclient.Client, error) {
			return gatewayclient.NewInsecureForTest(base, store.values[codebox.CredentialSecretsGateway], http.DefaultClient)
		}
	}
	t.Cleanup(func() {
		secretsGatewayBaseURLFn, secretsGatewayStoreFn, secretsGatewayClientFn, secretsGatewayLoginOut = oldBase, oldStore, oldClient, oldOut
	})
	return buf
}

func TestSecretsCheckPrintsIdentityWithoutToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(secrets.WhoAmIResponse{Email: "dev@beta.gouv.fr", ACR: "eidas2"})
	}))
	defer server.Close()
	store := &fakeCredentialStore{values: map[codebox.CredentialKind]string{codebox.CredentialSecretsGateway: "token-1"}}
	buf := withRemoteSeams(t, server.URL, store)

	code, err := secretsCheckCmd(nil)
	if err != nil || code != 0 {
		t.Fatalf("secretsCheckCmd = %d, %v", code, err)
	}
	if !strings.Contains(buf.String(), "dev@beta.gouv.fr") || strings.Contains(buf.String(), "token-1") {
		t.Fatalf("check output = %q", buf.String())
	}
}

func TestSecretsLogoutRemovesSession(t *testing.T) {
	store := &fakeCredentialStore{values: map[codebox.CredentialKind]string{codebox.CredentialSecretsGateway: "token-1"}}
	buf := withRemoteSeams(t, "https://gateway.example", store)
	code, err := secretsLogoutCmd(nil)
	if err != nil || code != 0 {
		t.Fatalf("secretsLogoutCmd = %d, %v", code, err)
	}
	if _, ok := store.values[codebox.CredentialSecretsGateway]; ok {
		t.Fatal("session token was not removed")
	}
	if !strings.Contains(buf.String(), "Logged out") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestParseACLEntry(t *testing.T) {
	kind, value, err := parseACLEntry("User@Beta.gouv.fr")
	if err != nil || kind != secrets.EntryMember || value != "user@beta.gouv.fr" {
		t.Fatalf("email entry = %q %q %v", kind, value, err)
	}
	kind, value, err = parseACLEntry("siret:13002526500013")
	if err != nil || kind != secrets.EntrySIRET || value != "13002526500013" {
		t.Fatalf("siret entry = %q %q %v", kind, value, err)
	}
	if _, _, err := parseACLEntry("siret:123"); err == nil {
		t.Fatal("short SIRET accepted")
	}
}

func TestEnsureDeclaredBindingApprovalsFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codebox"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := codebox.ProjectManifest{
		Secrets: &codebox.ProjectSecretsManifest{
			ProjectID: "22222222-2222-4222-8222-222222222222",
			Bindings:  []codebox.ProjectSecretBindingHint{{Component: ".env", Name: "API_KEY"}},
		},
	}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	oldState, oldTTY, oldConfirm := projectSecretsStateDirFn, projectSecretsTTYFn, projectSecretsConfirmFn
	projectSecretsStateDirFn = func() string { return filepath.Join(root, ".state") }
	projectSecretsTTYFn = func() bool { return false }
	projectSecretsConfirmFn = func(string, string, bool) (bool, error) { return false, nil }
	t.Cleanup(func() {
		projectSecretsStateDirFn, projectSecretsTTYFn, projectSecretsConfirmFn = oldState, oldTTY, oldConfirm
	})

	if err := ensureDeclaredBindingApprovals(root); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("error = %v, want fail-closed approval error", err)
	}
}
