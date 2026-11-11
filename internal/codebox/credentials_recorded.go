package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// This file records which credential store actually received each kind.
//
// The read path could otherwise only guess: on Linux the native adapter exists
// on every host, so DefaultCredentialStore() returns a secret-service store even
// where no D-Bus session is reachable. A `--fallback` write is a consent
// decision the user already made, and re-probing the native store on a later
// start cannot recover it — the probe either answers not-found (the value is in
// the file) or fails with denied/unavailable (the contract forbids reading the
// file, which is right, but the value the user stored is then unreachable).
//
// Recording the store the write used turns that consent into a durable fact, the
// same way a project-secret import records its store (see
// project_secret_bindings.go). It is metadata only: never a value, never a
// generation, never anything derived from one.

// recordedCredentialStoreFile is the host-state file holding the choices.
func recordedCredentialStoreFile(stateDir string) string {
	return filepath.Join(stateDir, "credential-stores.json")
}

type recordedCredentialStores struct {
	SchemaVersion int               `json:"schemaVersion"`
	Stores        map[string]string `json:"stores"`
}

const recordedCredentialStoresSchemaVersion = 1

// RecordedCredentialStoreForKind returns the store kind a previous write
// recorded for kind ("file", "secret-service", "keychain",
// "credential-manager"), or "" when nothing is recorded. An unreadable record
// is reported as "" rather than an error: the record only ever narrows a read,
// so losing it degrades to the documented default chain instead of blocking a
// start.
func RecordedCredentialStoreForKind(stateDir string, kind CredentialKind) string {
	stores, err := readRecordedCredentialStores(DefaultFS, stateDir)
	if err != nil {
		return ""
	}
	return stores.Stores[string(kind)]
}

func readRecordedCredentialStores(fs FS, stateDir string) (recordedCredentialStores, error) {
	data := recordedCredentialStores{
		SchemaVersion: recordedCredentialStoresSchemaVersion,
		Stores:        map[string]string{},
	}
	raw, err := fs.ReadFile(recordedCredentialStoreFile(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("credential store record: %w", err)
	}
	if data.Stores == nil {
		data.Stores = map[string]string{}
	}
	return data, nil
}

// RecordCredentialStore records that kind was stored in the store whose Kind()
// is storeKind. It is written after a successful write, by the same callers that
// bump the rotation generation.
func RecordCredentialStore(stateDir string, kind CredentialKind, storeKind string) error {
	return writeRecordedCredentialStoreLocked(DefaultFS, stateDir, kind, storeKind)
}

func writeRecordedCredentialStore(fs FS, stateDir string, kind CredentialKind, storeKind string) error {
	return writeRecordedCredentialStoreLocked(fs, stateDir, kind, storeKind)
}

// writeRecordedCredentialStoreLocked performs the read-modify-write under a
// cross-process lock. Atomic rename protects a single write, not a transaction:
// two concurrent `auth add --fallback` runs for different kinds would each read
// the same map and each write it back, so the second would drop the first kind's
// record and silently unpin a credential that is still stored.
func writeRecordedCredentialStoreLocked(fs FS, stateDir string, kind CredentialKind, storeKind string) error {
	if storeKind == "" {
		return nil
	}
	release, err := lockRecordedCredentialStores(fs, stateDir)
	if err != nil {
		return err
	}
	defer release()
	data, err := readRecordedCredentialStores(fs, stateDir)
	if err != nil {
		return err
	}
	data.Stores[string(kind)] = storeKind
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return atomicWrite(fs, recordedCredentialStoreFile(stateDir), append(raw, '\n'), 0o600)
}

// lockRecordedCredentialStores serializes the record's read-modify-write
// transaction. It reuses ProjectLock, whose exclusivity comes from an
// O_CREATE|O_EXCL lockfile rather than a check-then-write sequence.
func lockRecordedCredentialStores(fs FS, stateDir string) (func(), error) {
	return (&ProjectLock{Path: recordedCredentialStoreFile(stateDir) + ".lock", FS: fs}).Acquire()
}

// ForgetCredentialStore drops the record for kind, but only when it names the
// store being edited. Removing an entry from the native store must not erase the
// record of a consented file-store value that removal did not touch.
func ForgetCredentialStore(stateDir string, kind CredentialKind, storeKind string) error {
	return forgetRecordedCredentialStore(DefaultFS, stateDir, kind, storeKind)
}

func forgetRecordedCredentialStore(fs FS, stateDir string, kind CredentialKind, storeKind string) error {
	// Check before locking: taking the lock creates the state directory, and a
	// removal from a store the record never named must not leave one behind on a
	// host that never wrote a record. The lock is still taken for the
	// read-modify-write itself, so a concurrent writer cannot be lost between
	// this read and the update.
	if before, err := readRecordedCredentialStores(fs, stateDir); err != nil {
		return err
	} else if recorded, ok := before.Stores[string(kind)]; !ok || recorded != storeKind {
		return nil
	}
	release, err := lockRecordedCredentialStores(fs, stateDir)
	if err != nil {
		return err
	}
	defer release()
	data, err := readRecordedCredentialStores(fs, stateDir)
	if err != nil {
		return err
	}
	recorded, ok := data.Stores[string(kind)]
	if !ok || recorded != storeKind {
		return nil
	}
	delete(data.Stores, string(kind))
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, recordedCredentialStoreFile(stateDir), append(raw, '\n'), 0o600)
}

// recordedFileStorePin reports whether a completed write recorded the owner-only
// file store for kind, so a later read answers from it without re-probing the
// native adapter.
//
// Only the file store is pinned. A recorded native kind changes nothing: the
// native store already leads the documented chain, and pinning it would turn a
// not-found into a hard error where falling through to the file is still the
// documented outcome. A record naming a native adapter this host does not
// provide (a state directory synced from another platform) is ignored for the
// same reason — it describes a store that is not on this machine.
func recordedFileStorePin(stateDir string, kind CredentialKind) bool {
	return RecordedCredentialStoreForKind(stateDir, kind) == "file"
}

// CredentialStoreForRead reports the store a start will actually read kind from
// given the current record, and whether that answer is authoritative.
//
// It exists because the record is not the whole answer: a record naming a native
// store says nothing about whether that entry still exists, so resolution still
// falls through to the file on not-found. Printing the record as "read from the
// native store" would then contradict what `start` does. Only a file-store
// record is a definite answer; otherwise this reports "" and false and callers
// describe the value as the last write rather than a pin.
func CredentialStoreForRead(stateDir string, kind CredentialKind) (string, bool) {
	if recordedFileStorePin(stateDir, kind) {
		return "file", true
	}
	return "", false
}

// credentialStoreReaders are the two store adapters the read chain uses, so the
// record logic can be tested without a real keychain.
type credentialStoreReaders struct {
	native func(context.Context, CredentialKind) (string, error)
	file   func(context.Context, CredentialKind) (string, error)
}

func defaultCredentialStoreReaders() credentialStoreReaders {
	return credentialStoreReaders{
		native: func(ctx context.Context, kind CredentialKind) (string, error) {
			if s := DefaultCredentialStore(); s == nil {
				return "", ErrCredentialNotFound
			} else {
				return s.Get(ctx, kind)
			}
		},
		file: func(ctx context.Context, kind CredentialKind) (string, error) {
			fs, err := NewFileCredentialStore()
			if err != nil {
				return "", err
			}
			return fs.Get(ctx, kind)
		},
	}
}

// readStoredCredentialIn resolves kind from the store a previous write recorded,
// or from the documented native-then-fallback chain when nothing is recorded.
//
// The pinned branch never consults the native adapter, which is the point: a
// secret-service store that cannot autolaunch D-Bus must not fail a read of a
// value the user deliberately placed in the consented file store.
func readStoredCredentialIn(ctx context.Context, kind CredentialKind, stateDir string, readers credentialStoreReaders) (string, string, error) {
	if recordedFileStorePin(stateDir, kind) {
		v, err := readers.file(ctx, kind)
		if errors.Is(err, ErrFileStoreAbsent) || errors.Is(err, ErrFileStoreNotConsented) {
			// The record names a store that no longer exists. The remedy is the
			// same as never having stored the credential, so report exactly
			// that instead of a store-internals error.
			return "", "file", ErrCredentialNotFound
		}
		return v, "file", err
	}
	return readStoredWithNative(ctx, kind, readers.native, readers.file)
}
