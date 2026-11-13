package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// FileCredentialStore is the P08 explicit fallback for hosts without a
// native store (headless Linux without a Secret Service). It stores
// credentials in a single JSON file under the user config directory with
// owner-only permissions.
//
// The store is NEVER created implicitly: NewFileCredentialStore fails with
// ErrFileStoreNotConsented when the file does not exist, and
// ConsentFileStore is the only creation path. A failed native store must
// surface its own error, not silently downgrade to this plaintext file.
//
// Layout: one JSON object { "schemaVersion": 1, "credentials": { kind:
// value } }. Writes are atomic (temp + rename in the same directory) and
// refuse a symlinked store file or directory, so a pre-positioned
// symlink cannot redirect the write.

// ErrFileStoreNotConsented is returned when the file fallback is
// requested but no store file exists and no explicit consent was given.
// It is the guard against silent plaintext creation.
var ErrFileStoreNotConsented = fmt.Errorf("file credential store not created: run 'codebox auth add --fallback' once to consent to an owner-only plaintext store")

// ErrFileStoreAbsent is the read-side twin of ErrFileStoreNotConsented:
// the fallback file does not exist, so there is nothing to report or
// remove. Callers use it to distinguish "never consented, absent" from
// an existing (possibly empty) store.
var ErrFileStoreAbsent = fmt.Errorf("file credential store absent: no fallback file was ever created")

// fileStoreSchemaVersion is the store file format version.
const fileStoreSchemaVersion = 1

// FileCredentialStore is the owner-only JSON file fallback.
type FileCredentialStore struct {
	// Path is the store file location. Defaults to credentials.json in
	// the user config directory.
	Path string
	// CreateOnConsent allows Put to create the store when the file is
	// absent. It is set only by ConsentFileStore; the zero value makes
	// Put on a missing store fail with ErrFileStoreNotConsented.
	CreateOnConsent bool
}

// NewFileCredentialStore returns the file fallback pointed at the default
// path. The returned store never creates the file: use ConsentFileStore
// first.
func NewFileCredentialStore() (*FileCredentialStore, error) {
	dir, err := UserConfigDir()
	if err != nil {
		return nil, err
	}
	return &FileCredentialStore{Path: filepath.Join(dir, "credentials.json")}, nil
}

// ConsentFileStore returns a store allowed to create the file on first
// Put, after the CLI has obtained the user's explicit consent. It still
// does not create anything by itself.
func ConsentFileStore() (*FileCredentialStore, error) {
	s, err := NewFileCredentialStore()
	if err != nil {
		return nil, err
	}
	s.CreateOnConsent = true
	return s, nil
}

func (f *FileCredentialStore) Kind() string { return "file" }

type fileStoreData struct {
	SchemaVersion int               `json:"schemaVersion"`
	Credentials   map[string]string `json:"credentials"`
	// Generations maps kind to a monotonically increasing counter bumped
	// by every Put. It is the non-secret rotation marker reconcile compares:
	// nothing is derived from any value.
	Generations map[string]int `json:"generations,omitempty"`
}

// read loads the store file. A missing file is ErrFileStoreNotConsented
// for operations that need existing data; Put interprets it via
// CreateOnConsent.
func (f *FileCredentialStore) read() (fileStoreData, error) {
	var data fileStoreData
	err := retryFilesystemOperation(func() error {
		var err error
		data, err = f.readOnce()
		return err
	}, isTransientFilesystemError)
	return data, err
}

func (f *FileCredentialStore) readOnce() (fileStoreData, error) {
	data := fileStoreData{SchemaVersion: fileStoreSchemaVersion, Credentials: map[string]string{}}
	// Symlink defense: refuse to follow a symlinked store file. A
	// pre-positioned link must not redirect reads (or writes, via the
	// same path check in write).
	fi, err := os.Lstat(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return data, ErrFileStoreNotConsented
		}
		return data, fmt.Errorf("credential store %s: %w", f.Path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return data, &StoreError{Op: "get", Kind: "file", State: "corrupt",
			Err: fmt.Errorf("%s is a symlink; refusing to use it", f.Path)}
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		return data, &StoreError{Op: "get", Kind: "file", State: "unavailable", Err: err}
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, &StoreError{Op: "get", Kind: "file", State: "corrupt",
			Err: fmt.Errorf("invalid JSON in %s: %w", f.Path, err)}
	}
	if data.SchemaVersion > fileStoreSchemaVersion {
		return data, &StoreError{Op: "get", Kind: "file", State: "corrupt",
			Err: fmt.Errorf("store schema version %d is newer than supported %d; update codebox", data.SchemaVersion, fileStoreSchemaVersion)}
	}
	if data.Credentials == nil {
		data.Credentials = map[string]string{}
	}
	return data, nil
}

// write persists the store atomically: a temp file in the same directory,
// chmod 0600, then rename over the target. The directory is created
// 0700 only under consent (CreateOnConsent), and must not be a symlink.
func (f *FileCredentialStore) write(data fileStoreData) error {
	dir := filepath.Dir(f.Path)
	// Symlink defense for the directory: resolve it and require that it
	// not be a symlink itself.
	dirFi, err := os.Lstat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
		}
		if !f.CreateOnConsent {
			return ErrFileStoreNotConsented
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
		}
	} else if dirFi.Mode()&os.ModeSymlink != 0 {
		return &StoreError{Op: "add", Kind: "file", State: "corrupt",
			Err: fmt.Errorf("%s is a symlink; refusing to write through it", dir)}
	}
	// An existing store file must be a regular file: refuse symlinks so
	// the rename cannot replace a link target's neighbor instead.
	if fi, err := os.Lstat(f.Path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return &StoreError{Op: "add", Kind: "file", State: "corrupt",
				Err: fmt.Errorf("%s is a symlink; refusing to write through it", f.Path)}
		}
		// Tighten a pre-existing store that was created with looser
		// permissions by an older tool: the write path enforces 0600.
		if fi.Mode().Perm() != 0o600 {
			if err := os.Chmod(f.Path, 0o600); err != nil {
				return &StoreError{Op: "add", Kind: "file", State: "denied", Err: err}
			}
		}
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return &StoreError{Op: "add", Kind: "file", State: "corrupt", Err: err}
	}
	tmp, err := os.CreateTemp(dir, ".credentials.json.tmp-*")
	if err != nil {
		return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return &StoreError{Op: "add", Kind: "file", State: "denied", Err: err}
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
	}
	if err := tmp.Close(); err != nil {
		return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
	}
	if err := retryFilesystemOperation(func() error { return os.Rename(name, f.Path) }, isTransientFilesystemError); err != nil {
		return &StoreError{Op: "add", Kind: "file", State: "unavailable", Err: err}
	}
	return nil
}

func (f *FileCredentialStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	release, err := f.mutationLock(f.CreateOnConsent)
	if err != nil {
		return err
	}
	defer release()
	data, err := f.read()
	if err != nil {
		if err != ErrFileStoreNotConsented {
			return err
		}
		// Missing store: only consent creates it.
		if !f.CreateOnConsent {
			return ErrFileStoreNotConsented
		}
		data = fileStoreData{SchemaVersion: fileStoreSchemaVersion, Credentials: map[string]string{}}
	}
	data.Credentials[string(kind)] = value
	if data.Generations == nil {
		data.Generations = map[string]int{}
	}
	data.Generations[string(kind)]++
	return f.write(data)
}

func (f *FileCredentialStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	data, err := f.read()
	if err != nil {
		return "", err
	}
	v, ok := data.Credentials[string(kind)]
	if !ok || v == "" {
		return "", ErrCredentialNotFound
	}
	return v, nil
}

// Generation returns the kind's write counter. A missing credential has no
// generation (empty string), which callers treat as "no marker" rather than
// as an error.
func (f *FileCredentialStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	data, err := f.read()
	if err != nil {
		if err == ErrFileStoreNotConsented {
			return "", nil
		}
		return "", err
	}
	n, ok := data.Generations[string(kind)]
	if !ok {
		return "", nil
	}
	return strconv.Itoa(n), nil
}

func (f *FileCredentialStore) Remove(ctx context.Context, kind CredentialKind) error {
	release, err := f.mutationLock(false)
	if err != nil {
		if errors.Is(err, ErrFileStoreNotConsented) {
			return ErrCredentialNotFound
		}
		return err
	}
	defer release()
	data, err := f.read()
	if err != nil {
		if err == ErrFileStoreNotConsented {
			// No fallback file was ever created: there is
			// nothing to remove. Idempotent, same outcome as
			// removing an absent credential.
			return ErrCredentialNotFound
		}
		return err
	}
	if _, ok := data.Credentials[string(kind)]; !ok {
		return ErrCredentialNotFound
	}
	delete(data.Credentials, string(kind))
	return f.write(data)
}

// All mutations, including global auth operations, share this cross-process
// lock. Atomic rename alone does not protect a read-modify-write transaction.
func (f *FileCredentialStore) mutationLock(create bool) (func(), error) {
	dir := filepath.Dir(f.Path)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		if !create {
			return nil, ErrFileStoreNotConsented
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("credential store directory must be a non-symlink directory")
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(canonical, filepath.Base(f.Path)+".lock")
	var lockInfo os.FileInfo
	err = retryFilesystemOperation(func() error {
		var err error
		lockInfo, err = os.Lstat(lockPath)
		return err
	}, isTransientFilesystemError)
	if err == nil && lockInfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("credential store lock must not be a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return (&ProjectLock{Path: lockPath, FS: realFS{}}).Acquire()
}

// Verify reports whether the store file exists and parses. It never
// creates anything.
func (f *FileCredentialStore) Verify(ctx context.Context) error {
	_, err := f.read()
	if err == ErrFileStoreNotConsented {
		return ErrFileStoreAbsent // absent store is a valid state, not a failure
	}
	return err
}
