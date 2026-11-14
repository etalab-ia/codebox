package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type secretsCLIStore struct {
	mu     sync.Mutex
	values map[codebox.CredentialKind]string
}

func newSecretsCLIStore() *secretsCLIStore {
	return &secretsCLIStore{values: map[codebox.CredentialKind]string{}}
}
func (*secretsCLIStore) Kind() string                 { return "test" }
func (*secretsCLIStore) Verify(context.Context) error { return nil }
func (*secretsCLIStore) Generation(context.Context, codebox.CredentialKind) (string, error) {
	return "", nil
}
func (s *secretsCLIStore) Put(_ context.Context, k codebox.CredentialKind, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[k] = v
	return nil
}
func (s *secretsCLIStore) Get(_ context.Context, k codebox.CredentialKind) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[k]
	if !ok {
		return "", codebox.ErrCredentialNotFound
	}
	return v, nil
}
func (s *secretsCLIStore) Remove(_ context.Context, k codebox.CredentialKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.values[k]; !ok {
		return codebox.ErrCredentialNotFound
	}
	delete(s.values, k)
	return nil
}

func TestParseProjectSecretsImportArgs(t *testing.T) {
	opts, err := parseProjectSecretsImportArgs([]string{"--root", "/tmp/p", "--profile", "dev", "--file", ".env", "--file", "apps/api/.env.local", "--apply", "--yes"})
	if err != nil || opts.Root != "/tmp/p" || opts.Profile != "dev" || len(opts.Files) != 2 || !opts.Apply || !opts.Yes {
		t.Fatalf("parse options: %+v, %v", opts, err)
	}
	for _, args := range [][]string{
		{"--root"}, {"--profile", "dev", "--file", ".env", "--apply", "--apply"},
		{"--profile", "dev", "--file", ".env", "--yes"}, {"--profile", "dev", "--file", ".env", "--other"},
	} {
		if _, err := parseProjectSecretsImportArgs(args); err == nil {
			t.Errorf("parseProjectSecretsImportArgs(%q) unexpectedly succeeded", args)
		}
	}
}

func TestParseArgsRoutesSecretsBeforeConfiguration(t *testing.T) {
	parsed, err := parseArgs([]string{"secrets", "import", "--profile", "development", "--file", ".env"})
	if err != nil || parsed.action != "secrets" || !sameStrings(parsed.secretsArgs, []string{"import", "--profile", "development", "--file", ".env"}) {
		t.Fatalf("parsed secrets command: %+v, %v", parsed, err)
	}
}

func TestDiscoverProjectDotenvFilesIsBoundedAndSorted(t *testing.T) {
	root := t.TempDir()
	for name, value := range map[string]string{
		".env": "A=1\n", "apps/api/.env.local": "B=2\n", "docker.env": "C=3\n",
		".env.example": "X=4\n", "node_modules/x/.env": "N=5\n", ".git/config.env": "G=6\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := discoverProjectDotenvFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", "apps/api/.env.local", "docker.env"}
	if !sameStrings(got, want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
}

func TestReadProjectSecretsFilesRejectsOutsideAndSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, ".env")
	if err := os.WriteFile(secret, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectSecretsFiles(root, []string{secret}); err == nil {
		t.Fatal("file outside project accepted")
	}
	link := filepath.Join(root, ".env")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readProjectSecretsFiles(root, []string{".env"}); err == nil {
		t.Fatal("symlinked dotenv accepted")
	}
}

func TestProjectSecretsImportCLIIsPreviewFirstAndNeverPrintsValues(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	secret := "top-secret-canary-72a5"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY="+secret+"\nOTHER=$LITERAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSecretsCLIStore()
	originalTTY, originalState, originalCredential, originalFactory := projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn
	projectSecretsTTYFn = func() bool { return false }
	projectSecretsStateDirFn = func() string { return state }
	projectSecretsCredentialStoreFn = func(bool) (codebox.CredentialStore, error) { return store, nil }
	projectSecretsStoreFactoryFn = codebox.NewProjectSecretStore
	t.Cleanup(func() {
		projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn = originalTTY, originalState, originalCredential, originalFactory
	})

	output := captureSecretsStdout(t, func() {
		code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env"})
		if code != 0 || err != nil {
			t.Fatalf("preview: code=%d err=%v", code, err)
		}
	})
	if strings.Contains(output, secret) || strings.Contains(output, "$LITERAL") || !strings.Contains(output, "API_KEY") || !strings.Contains(output, "Preview only") {
		t.Fatalf("preview leaked values or omitted names (output=%q)", output)
	}
	if len(store.values) != 0 {
		t.Fatal("preview wrote credentials")
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply"}); code != 2 || err == nil {
		t.Fatalf("noninteractive apply without --yes: code=%d err=%v", code, err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("apply: code=%d err=%v", code, err)
	}
	if len(store.values) != 2 {
		t.Fatal("apply did not store both variables")
	}
	idx, err := codebox.NewProjectSecretStore(root, state, store)
	if err != nil {
		t.Fatal(err)
	}
	value, err := idx.Get(context.Background(), "development", ".env", "API_KEY")
	if err != nil || value != secret {
		t.Fatal("scoped value did not round-trip through the credential store")
	}
	output = captureSecretsStdout(t, func() {
		if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env"}); code != 0 || err != nil {
			t.Fatalf("second preview: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(output, "0 added, 0 changed, 2 unchanged, 0 removed") || strings.Contains(output, secret) {
		t.Fatalf("reimport preview failed to identify unchanged values or leaked: %q", output)
	}
}

func TestProjectSecretsImportTwoCheckoutsRemainIsolated(t *testing.T) {
	rootA, rootB, state := t.TempDir(), t.TempDir(), t.TempDir()
	store := newSecretsCLIStore()
	originalTTY, originalState, originalCredential, originalFactory := projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn
	projectSecretsTTYFn = func() bool { return false }
	projectSecretsStateDirFn = func() string { return state }
	projectSecretsCredentialStoreFn = func(bool) (codebox.CredentialStore, error) { return store, nil }
	projectSecretsStoreFactoryFn = codebox.NewProjectSecretStore
	t.Cleanup(func() {
		projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn = originalTTY, originalState, originalCredential, originalFactory
	})
	for root, value := range map[string]string{rootA: "project-a", rootB: "project-b"} {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SAME_KEY="+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
			t.Fatalf("import root %s: code=%d err=%v", root, code, err)
		}
	}
	for root, want := range map[string]string{rootA: "project-a", rootB: "project-b"} {
		s, err := codebox.NewProjectSecretStore(root, state, store)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(context.Background(), "development", ".env", "SAME_KEY")
		if err != nil || got != want {
			t.Fatalf("project-scoped import did not isolate equal names: got matches=%v err=%v", got == want, err)
		}
	}
}

func TestProjectSecretsApplyRefusesSourceChangedAfterPreview(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	file := filepath.Join(root, ".env")
	if err := os.WriteFile(file, []byte("KEY=before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newSecretsCLIStore()
	originalTTY, originalState, originalCredential, originalFactory, originalConfirm := projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn, projectSecretsConfirmFn
	projectSecretsTTYFn = func() bool { return true }
	projectSecretsStateDirFn = func() string { return state }
	projectSecretsCredentialStoreFn = func(bool) (codebox.CredentialStore, error) { return store, nil }
	projectSecretsStoreFactoryFn = codebox.NewProjectSecretStore
	projectSecretsConfirmFn = func(string, string, bool) (bool, error) {
		return true, os.WriteFile(file, []byte("KEY=after\n"), 0o600)
	}
	t.Cleanup(func() {
		projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn, projectSecretsConfirmFn = originalTTY, originalState, originalCredential, originalFactory, originalConfirm
	})
	code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply"})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("source change must invalidate preview: code=%d err=%v", code, err)
	}
	if len(store.values) != 0 {
		t.Fatal("changed source must fail before credential writes")
	}
}

func captureSecretsStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var _ codebox.CredentialStore = (*secretsCLIStore)(nil)
