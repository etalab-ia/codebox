package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	oldBase, oldStore, oldClient, oldOut := codeboxServerBaseURLFn, codeboxServerStoreFn, codeboxServerClientFn, codeboxServerLoginOut
	buf := &bytes.Buffer{}
	codeboxServerBaseURLFn = func() (string, error) { return base, nil }
	codeboxServerStoreFn = func() (codebox.CredentialStore, error) { return store, nil }
	codeboxServerLoginOut = buf
	if strings.HasPrefix(base, "http://") {
		codeboxServerClientFn = func() (*gatewayclient.Client, error) {
			return gatewayclient.NewInsecureForTest(base, store.values[codebox.CredentialCodeboxServer], http.DefaultClient)
		}
	}
	t.Cleanup(func() {
		codeboxServerBaseURLFn, codeboxServerStoreFn, codeboxServerClientFn, codeboxServerLoginOut = oldBase, oldStore, oldClient, oldOut
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
	store := &fakeCredentialStore{values: map[codebox.CredentialKind]string{codebox.CredentialCodeboxServer: "token-1"}}
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
	store := &fakeCredentialStore{values: map[codebox.CredentialKind]string{codebox.CredentialCodeboxServer: "token-1"}}
	buf := withRemoteSeams(t, "https://gateway.example", store)
	code, err := secretsLogoutCmd(nil)
	if err != nil || code != 0 {
		t.Fatalf("secretsLogoutCmd = %d, %v", code, err)
	}
	if _, ok := store.values[codebox.CredentialCodeboxServer]; ok {
		t.Fatal("session token was not removed")
	}
	if !strings.Contains(buf.String(), "Déconnecté") {
		t.Fatalf("output = %q", buf.String())
	}
}

func TestSecretsLoginRejectsUnboundLoopbackCallback(t *testing.T) {
	store := &fakeCredentialStore{values: map[codebox.CredentialKind]string{}}
	withRemoteSeams(t, "https://gateway.example", store)

	loginURLCh := make(chan string, 1)
	oldOpenURL := codeboxServerOpenURLFn
	codeboxServerOpenURLFn = func(raw string) error {
		loginURLCh <- raw
		return nil
	}
	t.Cleanup(func() { codeboxServerOpenURLFn = oldOpenURL })

	type loginResult struct {
		code int
		err  error
	}
	resultCh := make(chan loginResult, 1)
	go func() {
		code, err := secretsLoginCmd(nil)
		resultCh <- loginResult{code: code, err: err}
	}()

	var rawLoginURL string
	select {
	case rawLoginURL = <-loginURLCh:
	case <-time.After(5 * time.Second):
		t.Fatal("login URL was not opened")
	}
	parsed, err := url.Parse(rawLoginURL)
	if err != nil {
		t.Fatalf("login URL: %v", err)
	}
	port := parsed.Query().Get("port")
	if port == "" {
		t.Fatalf("login URL %q has no loopback port", rawLoginURL)
	}
	loopbackState := parsed.Query().Get("loopback_state")
	callback := "http://127.0.0.1:" + port + "/callback?token=attacker-session"
	if loopbackState != "" {
		callback += "&state=wrong-state"
	}
	wrongResp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("unbound callback: %v", err)
	}
	_ = wrongResp.Body.Close()
	storedAfterWrong, _ := store.Get(context.Background(), codebox.CredentialCodeboxServer)

	if loopbackState != "" {
		validResp, err := http.Get("http://127.0.0.1:" + port + "/callback?token=legitimate-session&state=" + url.QueryEscape(loopbackState))
		if err != nil {
			t.Errorf("legitimate callback: %v", err)
		} else {
			_ = validResp.Body.Close()
			if validResp.StatusCode != http.StatusOK {
				t.Errorf("legitimate callback status = %d, want 200", validResp.StatusCode)
			}
		}
	}
	select {
	case result := <-resultCh:
		if result.err != nil || result.code != 0 {
			t.Errorf("secretsLoginCmd = %d, %v", result.code, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login did not finish")
	}
	if wrongResp.StatusCode != http.StatusForbidden {
		t.Errorf("unbound callback status = %d, want 403", wrongResp.StatusCode)
	}
	if storedAfterWrong != "" {
		t.Errorf("unbound callback stored a session %q", storedAfterWrong)
	}
	if loopbackState != "" {
		stored, _ := store.Get(context.Background(), codebox.CredentialCodeboxServer)
		if stored != "legitimate-session" {
			t.Errorf("legitimate callback stored %q, want the legitimate session", stored)
		}
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

	if err := ensureDeclaredBindingApprovals(root); err == nil || !strings.Contains(err.Error(), "pas approuvées") {
		t.Fatalf("error = %v, want fail-closed approval error", err)
	}
}

func TestFilterDevelopmentDotenvPathsRejectsProduction(t *testing.T) {
	cases := []string{
		".env", ".env.local", "apps/api/.env.development",
	}
	if _, err := filterDevelopmentDotenvPaths(cases); err != nil {
		t.Fatalf("development files rejected: %v", err)
	}
	for _, bad := range []string{".env.production", ".env.prod", "production.env", "prod.env", "prd.env", "apps/api/.env.production.local"} {
		if _, err := filterDevelopmentDotenvPaths([]string{bad}); err == nil {
			t.Errorf("production-named file %q was accepted", bad)
		}
	}
}

func TestValidateRemoteBindingAgainstManifest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codebox"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := codebox.ProjectManifest{Secrets: &codebox.ProjectSecretsManifest{
		ProjectID: "22222222-2222-4222-8222-222222222222",
		Bindings: []codebox.ProjectSecretBindingHint{
			{Component: ".env", Name: "API_KEY"},
			{Component: ".env", Name: "DB_PASSWORD", Delivery: codebox.ProjectSecretDeliveryGuest},
		},
	}}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	proxy := codebox.ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
		GuestEnv: "JC_API_KEY", Destinations: []string{"api.example.com"},
	}
	if err := validateRemoteBindingAgainstManifest(root, proxy); err != nil {
		t.Fatalf("valid proxy approval rejected: %v", err)
	}
	if err := validateRemoteBindingAgainstManifest(root, codebox.ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY", GuestEnv: "JC_API_KEY",
	}); err == nil {
		t.Fatal("proxy approval without a destination was accepted")
	}
	guest := codebox.ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "DB_PASSWORD",
		Delivery: codebox.ProjectSecretDeliveryGuest,
	}
	if err := validateRemoteBindingAgainstManifest(root, guest); err != nil {
		t.Fatalf("valid guest approval rejected: %v", err)
	}
	if err := validateRemoteBindingAgainstManifest(root, codebox.ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "NOT_DECLARED", Destinations: []string{"api.example.com"},
	}); err == nil {
		t.Fatal("an undeclared variable was accepted")
	}
}

func TestEnsureDeclaredBindingApprovalsAcceptsGuestReadable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codebox"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := codebox.ProjectManifest{Secrets: &codebox.ProjectSecretsManifest{
		ProjectID: "22222222-2222-4222-8222-222222222222",
		Bindings: []codebox.ProjectSecretBindingHint{
			{Component: ".env", Name: "DB_PASSWORD", Delivery: codebox.ProjectSecretDeliveryGuest},
		},
	}}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, ".state")
	oldState, oldTTY, oldConfirm := projectSecretsStateDirFn, projectSecretsTTYFn, projectSecretsConfirmFn
	projectSecretsStateDirFn = func() string { return stateDir }
	projectSecretsTTYFn = func() bool { return false }
	projectSecretsConfirmFn = func(string, string, bool) (bool, error) { return false, nil }
	t.Cleanup(func() {
		projectSecretsStateDirFn, projectSecretsTTYFn, projectSecretsConfirmFn = oldState, oldTTY, oldConfirm
	})

	store, err := projectSecretStoreForCLI(root, false)
	if err != nil {
		t.Fatalf("projectSecretStoreForCLI: %v", err)
	}
	if err := codebox.SetProjectSecretActiveProfile(codebox.DefaultFS, stateDir, store.Namespace(), "development"); err != nil {
		t.Fatalf("SetProjectSecretActiveProfile: %v", err)
	}
	if err := codebox.ApproveProjectSecretBinding(codebox.DefaultFS, stateDir, store.Namespace(), codebox.ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "DB_PASSWORD",
		Delivery: codebox.ProjectSecretDeliveryGuest,
	}); err != nil {
		t.Fatalf("ApproveProjectSecretBinding: %v", err)
	}
	if err := ensureDeclaredBindingApprovals(root); err != nil {
		t.Fatalf("valid guest-readable approval was rejected: %v", err)
	}
}

func TestFilterDevelopmentDotenvPathsKeepsEmptyForExplicitCheck(t *testing.T) {
	// The push command separately refuses an empty discovery result. The filter
	// itself must not invent files or reject an already-empty list.
	out, err := filterDevelopmentDotenvPaths(nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("filter(nil) = %v, %v", out, err)
	}
}

func TestSecretsPushRefusesEmptyDiscoveredBundle(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codebox"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := codebox.ProjectManifest{Secrets: &codebox.ProjectSecretsManifest{ProjectID: "22222222-2222-4222-8222-222222222222"}}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	oldBase, oldClient := codeboxServerBaseURLFn, codeboxServerClientFn
	codeboxServerBaseURLFn = func() (string, error) { return "https://gateway.example", nil }
	codeboxServerClientFn = func() (*gatewayclient.Client, error) {
		return gatewayclient.NewInsecureForTest("https://gateway.example", "token", http.DefaultClient)
	}
	t.Cleanup(func() { codeboxServerBaseURLFn, codeboxServerClientFn = oldBase, oldClient })

	code, err := secretsPushCmd(nil)
	if err == nil || code == 0 {
		t.Fatalf("secretsPushCmd = %d, %v; want refusal", code, err)
	}
	if !strings.Contains(err.Error(), "lot vide") && !strings.Contains(err.Error(), "aucun fichier dotenv de développement") {
		t.Fatalf("unexpected error = %v", err)
	}
}
