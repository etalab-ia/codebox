package codebox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
)

// Native stores accept arbitrary service names but reject/transform empty and
// newline-containing values. This fake checks the encoded storage boundary,
// not native-store availability, which is covered by adapter tests separately.
type projectSecretFakeStore struct {
	mu          sync.Mutex
	values      map[CredentialKind]string
	puts        int
	gets        int
	failPut     int
	failRemove  bool
	verifyError error
}

func newProjectSecretFakeStore() *projectSecretFakeStore {
	return &projectSecretFakeStore{values: map[CredentialKind]string{}}
}

func (*projectSecretFakeStore) Kind() string                   { return "fake" }
func (f *projectSecretFakeStore) Verify(context.Context) error { return f.verifyError }
func (*projectSecretFakeStore) Generation(context.Context, CredentialKind) (string, error) {
	return "", nil
}
func (f *projectSecretFakeStore) Put(_ context.Context, k CredentialKind, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("store cannot accept this literal representation")
	}
	f.puts++
	f.values[k] = v
	if f.failPut == f.puts {
		return fmt.Errorf("uncertain write failed: %s", v)
	}
	return nil
}
func (f *projectSecretFakeStore) Get(_ context.Context, k CredentialKind) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	v, ok := f.values[k]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return v, nil
}

// getCount reports how many times a stored value was read, so a test can prove
// a fail-closed path never touched the credential.
func (f *projectSecretFakeStore) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}
func (f *projectSecretFakeStore) Remove(_ context.Context, k CredentialKind) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRemove {
		return errors.New("remove failed")
	}
	if _, ok := f.values[k]; !ok {
		return ErrCredentialNotFound
	}
	delete(f.values, k)
	return nil
}

func projectSecretFixture(t *testing.T, root, state string, store CredentialStore) *ProjectSecretStore {
	t.Helper()
	s, err := NewProjectSecretStore(root, state, store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func importProjectSecrets(t *testing.T, s *ProjectSecretStore, profile, component string, values map[string]string) ProjectSecretImportResult {
	t.Helper()
	result, err := s.Import(context.Background(), profile, component, values)
	if err != nil || !result.Committed {
		t.Fatalf("import: %+v, %v", result, err)
	}
	return result
}

func assertProjectSecret(t *testing.T, s *ProjectSecretStore, profile, component, name, want string) {
	t.Helper()
	got, err := s.Get(context.Background(), profile, component, name)
	if err != nil || got != want {
		t.Fatalf("get %s/%s/%s: value matches=%v, err=%v", profile, component, name, got == want, err)
	}
}

func TestProjectSecretsIsolateProjectsProfilesAndComponents(t *testing.T) {
	ctx := context.Background()
	store := newProjectSecretFakeStore()
	state := t.TempDir()
	a := projectSecretFixture(t, t.TempDir(), state, store)
	b := projectSecretFixture(t, t.TempDir(), state, store)
	if a.Namespace() == b.Namespace() || len(a.Namespace()) != 64 {
		t.Fatal("different canonical roots need different full namespaces")
	}
	importProjectSecrets(t, a, "development", "apps/api/.env", map[string]string{"API_KEY": "project-a-api"})
	importProjectSecrets(t, a, "development", "apps/worker/.env", map[string]string{"API_KEY": "project-a-worker"})
	importProjectSecrets(t, a, "test", "apps/api/.env", map[string]string{"API_KEY": "project-a-test"})
	importProjectSecrets(t, b, "development", "apps/api/.env", map[string]string{"API_KEY": "project-b-api"})
	assertProjectSecret(t, a, "development", "apps/api/.env", "API_KEY", "project-a-api")
	assertProjectSecret(t, a, "development", "apps/worker/.env", "API_KEY", "project-a-worker")
	assertProjectSecret(t, a, "test", "apps/api/.env", "API_KEY", "project-a-test")
	assertProjectSecret(t, b, "development", "apps/api/.env", "API_KEY", "project-b-api")
	for _, id := range [][3]string{{"production", "apps/api/.env", "API_KEY"}, {"development", ".env", "API_KEY"}, {"development", "apps/api/.env", "ALBERT_API_KEY"}} {
		store.values[CredentialKind(id[2])] = "global-value"
		if _, err := a.Get(ctx, id[0], id[1], id[2]); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("missing scoped entry must not fall back to global credentials: %v", err)
		}
	}
	importProjectSecrets(t, a, "development", "apps/api/.env", nil)
	assertProjectSecret(t, b, "development", "apps/api/.env", "API_KEY", "project-b-api")
	assertProjectSecret(t, a, "development", "apps/worker/.env", "API_KEY", "project-a-worker")
	assertProjectSecret(t, a, "test", "apps/api/.env", "API_KEY", "project-a-test")
}

func TestProjectSecretsPreserveLiteralValues(t *testing.T) {
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	values := map[string]string{"EMPTY": "", "MULTILINE": "first\nsecond\n", "LITERAL": "  $VAR `command` \\\"value\\\"  "}
	importProjectSecrets(t, s, "development", ".env", values)
	for name, want := range values {
		assertProjectSecret(t, s, "development", ".env", name, want)
	}
	raw, err := os.ReadFile(s.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value != "" && strings.Contains(string(raw), value) || strings.Contains(string(raw), encodeProjectSecret(value)) {
			t.Fatal("secret value or its encoded representation leaked into index")
		}
	}
	if len(store.values) != len(values) {
		t.Fatal("literal-value test must exercise actual store writes")
	}
}

type projectSecretFailFS struct {
	projectSecretsFS
	indexPath string
}

func (f projectSecretFailFS) RenameTmp(from, to string) error {
	if to == f.indexPath {
		return errors.New("index publication failed")
	}
	return f.projectSecretsFS.RenameTmp(from, to)
}

func TestProjectSecretsFailedImportPreservesPreviousGeneration(t *testing.T) {
	for _, failure := range []string{"put", "publish"} {
		t.Run(failure, func(t *testing.T) {
			store := newProjectSecretFakeStore()
			s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
			importProjectSecrets(t, s, "development", ".env", map[string]string{"KEY": "old"})
			before, _ := os.ReadFile(s.indexPath())
			if failure == "put" {
				store.failPut = store.puts + 2 // include a possibly committed failing write
			} else {
				s.fs = projectSecretFailFS{projectSecretsFS: s.fs, indexPath: s.indexPath()}
			}
			result, err := s.Import(context.Background(), "development", ".env", map[string]string{"KEY": "new-secret", "SECOND": "other-secret"})
			if err == nil || result.Committed {
				t.Fatalf("failed import must not publish: %+v, %v", result, err)
			}
			if strings.Contains(err.Error(), "new-secret") || strings.Contains(err.Error(), encodeProjectSecret("other-secret")) {
				t.Fatal("store diagnostic exposed a secret")
			}
			after, _ := os.ReadFile(s.indexPath())
			if string(before) != string(after) || len(store.values) != 1 {
				t.Fatal("failure must preserve index and remove all prepared credentials")
			}
			assertProjectSecret(t, s, "development", ".env", "KEY", "old")
		})
	}
}

func TestProjectSecretsApplyRejectsStalePreview(t *testing.T) {
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	first, err := s.Preview(context.Background(), "development", ".env", map[string]string{"KEY": "first"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Preview(context.Background(), "development", ".env", map[string]string{"KEY": "concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportIfRevision(context.Background(), "development", ".env", map[string]string{"KEY": "concurrent"}, current.Revision); err != nil {
		t.Fatal(err)
	}
	result, err := s.ImportIfRevision(context.Background(), "development", ".env", map[string]string{"KEY": "first"}, first.Revision)
	if !errors.Is(err, ErrProjectSecretSnapshotChanged) || result.Committed {
		t.Fatalf("stale approval must fail without committing: %+v, %v", result, err)
	}
	assertProjectSecret(t, s, "development", ".env", "KEY", "concurrent")
}

func TestProjectSecretsUnchangedImportRetainsReference(t *testing.T) {
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"KEY": "same", "OTHER": "also-same"})
	before, err := s.readIndex()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(context.Background(), "development", ".env", map[string]string{"KEY": "same", "OTHER": "also-same"})
	if err != nil || preview.Unchanged != 2 || preview.Added != 0 {
		t.Fatalf("unchanged preview: %+v, %v", preview, err)
	}
	result, err := s.ImportIfRevision(context.Background(), "development", ".env", map[string]string{"KEY": "same", "OTHER": "also-same"}, preview.Revision)
	if err != nil || !result.Committed || result.Unchanged != 2 {
		t.Fatalf("unchanged apply: %+v, %v", result, err)
	}
	after, err := s.readIndex()
	if err != nil || !reflect.DeepEqual(before.Entries, after.Entries) {
		t.Fatalf("unchanged credentials must retain their references: err=%v", err)
	}
}

func TestProjectSecretSizeLimitMatchesWindowsUTF16Blob(t *testing.T) {
	for _, size := range []int{0, MaxProjectSecretValueBytes, MaxProjectSecretValueBytes + 1, 1000, 1800} {
		value := strings.Repeat("a", size)
		// This is the actual representation used by WinCredStore.Put:
		// Encoding.Unicode.GetBytes on the ASCII base64 envelope.
		blobBytes := len(utf16.Encode([]rune(encodeProjectSecret(value)))) * 2
		err := validateProjectSecretValues(map[string]string{"KEY": value})
		if (err == nil) != (blobBytes <= 2560) {
			t.Errorf("size %d produced %d blob bytes: validation error=%v", size, blobBytes, err)
		}
	}
	if err := validateProjectSecretValues(map[string]string{"KEY": strings.Repeat("é", 476)}); err == nil {
		t.Fatal("limit must count UTF-8 value bytes, not characters")
	}
}

func TestProjectSecretsRecoverInterruptedRollback(t *testing.T) {
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	s := projectSecretFixture(t, root, state, store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"KEY": "old"})
	store.failPut, store.failRemove = store.puts+1, true
	result, err := s.Import(context.Background(), "development", ".env", map[string]string{"KEY": "new"})
	if result.Committed || !errors.Is(err, ErrProjectSecretCleanupPending) {
		t.Fatalf("failed rollback must be recoverable: %+v, %v", result, err)
	}
	if len(store.values) != 2 {
		t.Fatal("recovery test must contain an actual orphaned store entry")
	}
	store.failRemove = false
	reopened := projectSecretFixture(t, root, state, store)
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 1 {
		t.Fatal("recovery must remove the orphan without deleting the live generation")
	}
	assertProjectSecret(t, reopened, "development", ".env", "KEY", "old")
}

func TestProjectSecretsRecoverCommittedCleanup(t *testing.T) {
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"KEY": "old", "RETIRED": "old2"})
	store.failRemove = true
	result, err := s.Import(context.Background(), "development", ".env", map[string]string{"KEY": "new"})
	if !result.Committed || result.Replaced != 1 || result.Removed != 1 || !errors.Is(err, ErrProjectSecretCleanupPending) {
		t.Fatalf("publication plus failed cleanup must be distinguishable: %+v, %v", result, err)
	}
	assertProjectSecret(t, s, "development", ".env", "KEY", "new")
	store.failRemove = false
	if err := s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 1 {
		t.Fatal("only the new generation must remain")
	}
	result = importProjectSecrets(t, s, "development", ".env", nil)
	if result.Removed != 1 || len(store.values) != 0 {
		t.Fatal("removing a whole component must also clean its previous generation")
	}
}

func TestProjectSecretsRejectForeignIndexAndJournal(t *testing.T) {
	store := newProjectSecretFakeStore()
	state := t.TempDir()
	a := projectSecretFixture(t, t.TempDir(), state, store)
	b := projectSecretFixture(t, t.TempDir(), state, store)
	importProjectSecrets(t, a, "development", ".env", map[string]string{"KEY": "a"})
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(a.indexPath())
	if err := os.WriteFile(b.indexPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(context.Background(), "development", ".env", "KEY"); err == nil {
		t.Fatal("copied project index must not resolve another project's secrets")
	}
	if err := os.Remove(b.indexPath()); err != nil {
		t.Fatal(err)
	}
	idx, _ := a.readIndex()
	foreign := projectSecretJournal{SchemaVersion: 1, Namespace: b.namespace, Store: "fake", Generation: strings.Repeat("a", 32), Prepared: []string{idx.Entries[0].Reference}}
	if err := b.writeJSON(b.journalPath(), foreign); err != nil {
		t.Fatal(err)
	}
	if err := b.Recover(context.Background()); err == nil {
		t.Fatal("journal references outside this project must be refused")
	}
	assertProjectSecret(t, a, "development", ".env", "KEY", "a")
}

func TestProjectSecretsCanonicalAliasesAndStateSymlinks(t *testing.T) {
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	a := projectSecretFixture(t, root, state, store)
	b := projectSecretFixture(t, alias, state, store)
	if a.Namespace() != b.Namespace() {
		t.Fatal("aliases of the same root must share a scope")
	}
	importProjectSecrets(t, a, "development", ".env", map[string]string{"KEY": "a"})
	assertProjectSecret(t, b, "development", ".env", "KEY", "a")
	if err := os.Remove(a.indexPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), a.indexPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Import(context.Background(), "development", ".env", map[string]string{"KEY": "b"}); err == nil {
		t.Fatal("symlinked state must not redirect secret-index publication")
	}
}

func TestProjectSecretsRejectUnsafeIdentitiesBeforeWriting(t *testing.T) {
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	for _, component := range []string{"../.env", "/tmp/.env", "apps/../.env", "C:/app/.env", "apps\\.env", ".", ""} {
		if _, err := s.Import(context.Background(), "development", component, map[string]string{"KEY": "value"}); err == nil {
			t.Fatalf("unsafe component accepted: %q", component)
		}
	}
	for _, profile := range []string{"", "../other", "development\n"} {
		if _, err := s.Import(context.Background(), profile, ".env", map[string]string{"KEY": "value"}); err == nil {
			t.Fatalf("invalid profile accepted: %q", profile)
		}
	}
	if _, err := s.Import(context.Background(), "development", ".env", map[string]string{"INVALID KEY": "value"}); err == nil {
		t.Fatal("invalid dotenv key accepted")
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) || len(store.values) != 0 {
		t.Fatal("invalid identities must fail before any state or store writes")
	}
}

func TestProjectSecretsConcurrentComponentImports(t *testing.T) {
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	errs := make(chan error, 2)
	for _, component := range []string{"api/.env", "worker/.env"} {
		component := component
		go func() {
			s, err := NewProjectSecretStore(root, state, store)
			if err == nil {
				_, err = s.Import(context.Background(), "development", component, map[string]string{"KEY": component})
			}
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s := projectSecretFixture(t, root, state, store)
	assertProjectSecret(t, s, "development", "api/.env", "KEY", "api/.env")
	assertProjectSecret(t, s, "development", "worker/.env", "KEY", "worker/.env")
}

func TestProjectSecretsRequireExplicitFileFallbackConsent(t *testing.T) {
	store := &FileCredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	if _, err := s.Import(context.Background(), "development", ".env", map[string]string{"KEY": "value"}); err == nil {
		t.Fatal("an absent fallback must not be created without explicit consent")
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatal("failed verification must not create the fallback")
	}
	store.CreateOnConsent = true
	importProjectSecrets(t, s, "development", ".env", map[string]string{"KEY": "value"})
	assertProjectSecret(t, s, "development", ".env", "KEY", "value")
}

func TestProjectSecretsLockedStoreFailsBeforePreparingCredentials(t *testing.T) {
	store := newProjectSecretFakeStore()
	store.verifyError = &StoreError{Op: "verify", Kind: "fake", State: "locked", Err: errors.New("secret-containing diagnostic")}
	s := projectSecretFixture(t, t.TempDir(), t.TempDir(), store)
	_, err := s.Import(context.Background(), "development", ".env", map[string]string{"KEY": "value"})
	var se *StoreError
	if !errors.As(err, &se) || se.State != "locked" || strings.Contains(err.Error(), "secret-containing diagnostic") {
		t.Fatalf("retain store state, not diagnostics: %v", err)
	}
	if len(store.values) != 0 {
		t.Fatal("locked store must fail before credential writes")
	}
	if _, err := os.Stat(s.journalPath()); !os.IsNotExist(err) {
		t.Fatal("locked store must fail before transaction preparation")
	}
}
