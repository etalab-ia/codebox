package codebox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// File fallback tests (P08). They run on every platform: the file store
// is the portable path. The two properties that matter most are pinned
// first: no implicit creation, and symlink refusal.

func newFileStore(t *testing.T) *FileCredentialStore {
	t.Helper()
	return &FileCredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json")}
}

// expectOwnerOnlyMode asserts the store file is owner-only. On Windows,
// Go's os.Chmod only toggles the read-only bit and Lstat reports the
// 666/444 convention, so the numeric assertion cannot hold there; the
// enforcement calls still run, and the Windows ACL surface is the
// platform's own protection. The check is real everywhere else.
func expectOwnerOnlyMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
}

// TestFileStorePutWithoutConsentNeverCreates pins the no-silent-plaintext
// rule: Put on a missing store must fail with ErrFileStoreNotConsented
// and must not create any file.
func TestFileStorePutWithoutConsentNeverCreates(t *testing.T) {
	s := newFileStore(t)
	err := s.Put(context.Background(), CredentialAlbert, "v1")
	if !errors.Is(err, ErrFileStoreNotConsented) {
		t.Fatalf("Put error = %v, want ErrFileStoreNotConsented", err)
	}
	if _, serr := os.Lstat(s.Path); serr == nil {
		t.Fatal("Put created the store file without consent")
	}
}

// TestFileStoreConsentedPutCreatesOwnerOnly verifies the consented
// creation path: 0600 file, 0700 directory, and a readable value.
func TestFileStoreConsentedPutCreatesOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	// The config directory does not exist yet: creation must make it.
	s := &FileCredentialStore{Path: filepath.Join(dir, "sub", "credentials.json"), CreateOnConsent: true}
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	expectOwnerOnlyMode(t, s.Path)
	dirFi, err := os.Lstat(filepath.Dir(s.Path))
	if err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if runtime.GOOS != "windows" && dirFi.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %o, want 0700", dirFi.Mode().Perm())
	}
	v, err := s.Get(context.Background(), CredentialAlbert)
	if err != nil || v != "v1" {
		t.Fatalf("Get = %q, %v; want v1", v, err)
	}
}

// TestFileStoreRotationReplacesValue pins rotation: a second Put of the
// same kind replaces the first value atomically.
func TestFileStoreRotationReplacesValue(t *testing.T) {
	s := newFileStore(t)
	s.CreateOnConsent = true
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := s.Put(context.Background(), CredentialAlbert, "rotated"); err != nil {
		t.Fatalf("Put rotated: %v", err)
	}
	v, err := s.Get(context.Background(), CredentialAlbert)
	if err != nil || v != "rotated" {
		t.Fatalf("Get = %q, %v; want rotated", v, err)
	}
}

// TestFileStoreRemoveAndNotFound verifies Remove deletes the entry and
// subsequent reads report ErrCredentialNotFound, not an error.
func TestFileStoreRemoveAndNotFound(t *testing.T) {
	s := newFileStore(t)
	s.CreateOnConsent = true
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Remove(context.Background(), CredentialAlbert); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.Get(context.Background(), CredentialAlbert); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Get after remove = %v, want ErrCredentialNotFound", err)
	}
	if err := s.Remove(context.Background(), CredentialAlbert); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Remove absent = %v, want ErrCredentialNotFound", err)
	}
}

// TestFileStoreRefusesSymlinkedFile pins the symlink defense: a
// pre-positioned symlink at the store path must not be read through or
// written through.
func TestFileStoreRefusesSymlinkedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "attacker-controlled.json")
	if err := os.WriteFile(target, []byte(`{"schemaVersion":1,"credentials":{"albert":"leaked"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credentials.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	s := &FileCredentialStore{Path: link, CreateOnConsent: true}
	// Get must not follow the link to the attacker's file.
	if _, err := s.Get(context.Background(), CredentialAlbert); err == nil {
		t.Fatal("Get followed a symlinked store file")
	}
	// Put must not write through the link.
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err == nil {
		t.Fatal("Put wrote through a symlinked store file")
	}
	// The attacker's file must be untouched.
	raw, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(raw), "leaked") {
		t.Fatalf("attacker file was modified: %v", err)
	}
}

// TestFileStoreRefusesSymlinkedDirectory pins the directory-level
// defense: the config directory itself must not be a symlink.
func TestFileStoreRefusesSymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	s := &FileCredentialStore{Path: filepath.Join(linkDir, "credentials.json"), CreateOnConsent: true}
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err == nil {
		t.Fatal("Put wrote through a symlinked config directory")
	}
	if _, err := os.Lstat(filepath.Join(realDir, "credentials.json")); err == nil {
		t.Fatal("a file appeared in the symlink target directory")
	}
}

// TestFileStoreCorruptJSONReportsCorrupt verifies a corrupt store is
// reported as corrupt, not silently recreated.
func TestFileStoreCorruptJSONReportsCorrupt(t *testing.T) {
	s := newFileStore(t)
	if err := os.WriteFile(s.Path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get(context.Background(), CredentialAlbert)
	var se *StoreError
	if !errors.As(err, &se) || se.State != "corrupt" {
		t.Fatalf("Get error = %v, want corrupt StoreError", err)
	}
	// Put on a corrupt store must fail, not recreate over it.
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err == nil {
		t.Fatal("Put silently recreated over a corrupt store")
	}
}

// TestFileStoreTightensLoosePermissions verifies the write path repairs
// a pre-existing store created with looser permissions.
func TestFileStoreTightensLoosePermissions(t *testing.T) {
	s := newFileStore(t)
	if err := os.WriteFile(s.Path, []byte(`{"schemaVersion":1,"credentials":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s.CreateOnConsent = true
	if err := s.Put(context.Background(), CredentialAlbert, "v1"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	expectOwnerOnlyMode(t, s.Path)
}

// TestFileStoreVerifyNeverCreates verifies the status probe is
// side-effect free.
func TestFileStoreVerifyNeverCreates(t *testing.T) {
	s := newFileStore(t)
	if err := s.Verify(context.Background()); !errors.Is(err, ErrFileStoreAbsent) {
		t.Fatalf("Verify on absent store = %v, want ErrFileStoreAbsent", err)
	}
	if _, serr := os.Lstat(s.Path); serr == nil {
		t.Fatal("Verify created the store file")
	}
}

func TestFileStoreRemoveAbsentIsNotFound(t *testing.T) {
	s := newFileStore(t)
	if err := s.Remove(context.Background(), CredentialAlbert); !IsCredentialNotFound(err) {
		t.Fatalf("Remove on never-created store = %v, want not-found", err)
	}
	if _, serr := os.Lstat(s.Path); serr == nil {
		t.Fatal("Remove created the store file")
	}
}

func TestFileStoreConcurrentProjectsAndAuthProcesses(t *testing.T) {
	const workers = 6
	ctx := context.Background()
	store := &FileCredentialStore{Path: filepath.Join(t.TempDir(), "credentials.json"), CreateOnConsent: true}
	state := t.TempDir()
	roots := make([]string, workers)
	for i := range roots {
		roots[i] = t.TempDir()
		if err := store.Put(ctx, CredentialKind(fmt.Sprintf("obsolete-%d", i)), "disposable"); err != nil {
			t.Fatal(err)
		}
	}
	// Separate test processes guarantee this exercises the file lock, not a
	// same-process mutex. Every child uses the production store adapter.
	errs := make(chan error, workers)
	for i := range roots {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFileStoreConcurrentProcessHelper$")
		cmd.Env = append(os.Environ(),
			"CODEBOX_FILE_STORE_HELPER_PATH="+store.Path,
			"CODEBOX_FILE_STORE_HELPER_ROOT="+roots[i],
			"CODEBOX_FILE_STORE_HELPER_STATE="+state,
			fmt.Sprintf("CODEBOX_FILE_STORE_HELPER_ID=%d", i))
		go func() {
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("store helper failed: %w: %s", err, out)
			}
			errs <- err
		}()
	}
	var helperErrors []error
	for range roots {
		if err := <-errs; err != nil {
			helperErrors = append(helperErrors, err)
		}
	}
	if len(helperErrors) != 0 {
		t.Fatal(errors.Join(helperErrors...))
	}
	for i, root := range roots {
		if _, err := store.Get(ctx, CredentialKind(fmt.Sprintf("obsolete-%d", i))); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("concurrent removal was lost: worker %d, %v", i, err)
		}
		if i == 0 {
			if got, err := store.Get(ctx, CredentialAlbert); err != nil || got != "auth-3" {
				t.Fatalf("global auth credential lost: %v", err)
			}
			continue
		}
		project, err := NewProjectSecretStore(root, state, store)
		if err != nil {
			t.Fatal(err)
		}
		got, err := project.Get(ctx, "development", ".env", "KEY")
		if err != nil || got != fmt.Sprintf("project-%d-3", i) {
			t.Fatalf("published project reference lost: worker %d, %v", i, err)
		}
	}
}

func TestFileStoreConcurrentProcessHelper(t *testing.T) {
	storePath := os.Getenv("CODEBOX_FILE_STORE_HELPER_PATH")
	if storePath == "" {
		return
	}
	id, err := strconv.Atoi(os.Getenv("CODEBOX_FILE_STORE_HELPER_ID"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := &FileCredentialStore{Path: storePath, CreateOnConsent: true}
	project, err := NewProjectSecretStore(os.Getenv("CODEBOX_FILE_STORE_HELPER_ROOT"), os.Getenv("CODEBOX_FILE_STORE_HELPER_STATE"), store)
	if err != nil {
		t.Fatal(err)
	}
	for j := 0; j < 4; j++ {
		if id == 0 {
			err = store.Put(ctx, CredentialAlbert, fmt.Sprintf("auth-%d", j))
		} else {
			_, err = project.Import(ctx, "development", ".env", map[string]string{"KEY": fmt.Sprintf("project-%d-%d", id, j)})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Remove(ctx, CredentialKind(fmt.Sprintf("obsolete-%d", id))); err != nil {
		t.Fatal(err)
	}
}
