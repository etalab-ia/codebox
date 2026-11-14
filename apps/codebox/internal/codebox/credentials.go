package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This file implements the P08 host credential store: global credentials
// live in the OS-native store (macOS Keychain, Windows Credential Manager,
// Linux Secret Service), never in project files or shell profiles. A
// owner-only file fallback exists for headless hosts, but it is created
// only after explicit consent — a failed or unavailable native store is
// an error, never a silent downgrade to plaintext.
//
// The store is host-side only: storing a credential does not grant any
// guest access. Binding a stored credential to a sandbox is P09's job.

// CredentialKind identifies what a stored credential is for. The store
// accepts arbitrary service names so future bindings (P09, P13) do not
// need a schema change, but the CLI only manages the kinds it knows.
type CredentialKind string

const (
	// CredentialAlbert is the Albert API key.
	CredentialAlbert CredentialKind = "albert"
	// CredentialMistral is the Mistral AI key.
	CredentialMistral CredentialKind = "mistral"
	// CredentialOpenAICompatible is the key of the user-named
	// OpenAI-compatible platform (one platform of that kind per host).
	CredentialOpenAICompatible CredentialKind = "openai-compatible"
	// CredentialGithub is a GitHub token, bound into the guest only after
	// explicit per-project approval (P09).
	CredentialGithub CredentialKind = "github"
	// CredentialContext7 is a Context7 API key, bound like github.
	CredentialContext7 CredentialKind = "context7"
)

// KnownCredentialKinds lists the kinds the CLI manages, in display order.
// The store itself accepts arbitrary service names so a credentialRef can
// name a non-standard entry (e.g. a second Albert key).
func KnownCredentialKinds() []CredentialKind {
	return []CredentialKind{CredentialAlbert, CredentialMistral, CredentialOpenAICompatible, CredentialGithub, CredentialContext7}
}

// IsKnownCredentialKind reports whether the CLI manages this kind.
func IsKnownCredentialKind(kind string) bool {
	for _, known := range KnownCredentialKinds() {
		if string(known) == kind {
			return true
		}
	}
	return false
}

// credentialService maps a kind to the native store service name. The
// prefix keeps codebox's entries distinguishable from other items in a
// shared keychain.
func credentialService(kind CredentialKind) string {
	return "codebox/" + string(kind)
}

// StoreError distinguishes the failure modes the plan requires to be
// reported separately: an unavailable store (no daemon/backend), a locked
// or denied store (present but access refused), and a corrupt store
// (returned data cannot be interpreted). Callers must not collapse these
// into one message: the recovery action differs.
type StoreError struct {
	// Op is the failing operation ("add", "get", "remove", "verify").
	Op string
	// Kind is the store kind that failed (keychain, credential-manager,
	// secret-service, file).
	Kind string
	// State is one of "unavailable", "locked", "denied", "corrupt".
	State string
	// Err is the underlying error, if any.
	Err error
}

func (e *StoreError) Error() string {
	msg := fmt.Sprintf("credential store (%s) %s: %s", e.Kind, e.Op, e.State)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *StoreError) Unwrap() error { return e.Err }

// storeErrorf builds a StoreError with a formatted detail message.
func storeErrorf(op, kind, state, format string, args ...any) *StoreError {
	return &StoreError{Op: op, Kind: kind, State: state, Err: fmt.Errorf(format, args...)}
}

// ErrCredentialNotFound is returned by Get and Remove when no credential of
// the requested kind is stored. It is a normal outcome (an empty store is
// not an error), so callers report it as a status, not a failure.
var ErrCredentialNotFound = errors.New("credential not found")

// CredentialStore is the minimal P08 surface: put, get, remove and a
// verification probe. Implementations must never log or expose the value
// through error messages.
type CredentialStore interface {
	// Kind returns the store identifier for display ("keychain",
	// "credential-manager", "secret-service", "file").
	Kind() string
	// Put stores value under kind. An existing credential of the same
	// kind is replaced (rotation).
	Put(ctx context.Context, kind CredentialKind, value string) error
	// Get returns the stored value for kind, or ErrCredentialNotFound.
	Get(ctx context.Context, kind CredentialKind) (string, error)
	// Remove deletes the credential for kind, or ErrCredentialNotFound.
	Remove(ctx context.Context, kind CredentialKind) error
	// Verify probes the store without touching any value: it reports
	// whether the store is reachable and usable. It must not create
	// anything.
	Verify(ctx context.Context) error
	// Generation returns a non-secret, store-local revision marker for
	// kind, or "" when the store provides none: reconcile falls back to the
	// host-side generation counter (CredentialGeneration), which every
	// rotation path bumps. Nothing is ever derived from a value.
	Generation(ctx context.Context, kind CredentialKind) (string, error)
}

// DefaultCredentialStore returns the OS-native store, or nil when the
// platform has no native adapter. The adapters are defined in
// OS-specific files (credentials_keychain.go, credentials_wincred.go,
// credentials_secretservice.go); on a platform without its file, this
// returns nil and the caller reports the file-fallback plan.
func DefaultCredentialStore() CredentialStore {
	switch runtime.GOOS {
	case "darwin":
		return &KeychainStore{}
	case "windows":
		return &WinCredStore{}
	case "linux":
		return &SecretServiceStore{}
	default:
		return nil
	}
}

// storeAvailability reports the store kind a platform would use, for
// status display on platforms where the adapter is not compiled in (the
// CLI prints the plan rather than an empty string).
func storeAvailability() string {
	switch runtime.GOOS {
	case "darwin":
		return "keychain"
	case "windows":
		return "credential-manager"
	case "linux":
		return "secret-service"
	default:
		return "none"
	}
}

// StoreAvailability is the exported display form of storeAvailability.
func StoreAvailability() string { return storeAvailability() }

// IsCredentialNotFound reports whether err is the not-found outcome,
// wrapping included. It is the form the CLI uses.
func IsCredentialNotFound(err error) bool { return isNotFound(err) }

// HasCredential reports whether the file fallback holds a credential of
// the kind, without exposing the value. It is a status helper; the store
// is never created by the check.
func (f *FileCredentialStore) HasCredential(kind CredentialKind) bool {
	v, err := f.Get(context.Background(), kind)
	return err == nil && v != ""
}

// CredentialBoundToRunningInstance is superseded by P09's RevokeCredential
// (see revoke.go), which revokes live bindings instead of refusing removal.

// CredentialGeneration is the host-side, store-independent rotation marker:
// a counter per credential kind, bumped by every successful Put. Reconcile
// compares it (preferring the store's own marker when one exists) so a value
// rotation on a healthy running instance schedules a refresh instead of
// taking the no-op path. The counter is non-secret and lives in host state,
// never in the workspace or a repository.
func CredentialGeneration(kind CredentialKind) (int, error) {
	return credentialGenerationIn(DefaultFS, DefaultStateDir(), kind)
}

// BumpCredentialGeneration is the exported form of
// bumpCredentialGeneration, for the CLI's auth add.
func BumpCredentialGeneration(kind CredentialKind) {
	bumpCredentialGeneration(kind)
}

// bumpCredentialGeneration advances the counter after a successful Put. It
// is best-effort: a rotation is still detected at the next boot even if the
// bump fails, so the credential write itself never fails because of it.
func bumpCredentialGeneration(kind CredentialKind) {
	_ = writeCredentialGeneration(DefaultFS, DefaultStateDir(), kind)
}

// credentialGenerationFile is the host-state file holding the counters.
func credentialGenerationFile(stateDir string) string {
	return filepath.Join(stateDir, "credential-generations.json")
}

type credentialGenerations struct {
	SchemaVersion int            `json:"schemaVersion"`
	Generations   map[string]int `json:"generations"`
}

const credentialGenerationsSchemaVersion = 1

func readCredentialGenerations(fs FS, stateDir string) (credentialGenerations, error) {
	data := credentialGenerations{SchemaVersion: credentialGenerationsSchemaVersion, Generations: map[string]int{}}
	raw, err := fs.ReadFile(credentialGenerationFile(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return data, nil
		}
		return data, err
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return data, fmt.Errorf("credential generations: %w", err)
	}
	if data.Generations == nil {
		data.Generations = map[string]int{}
	}
	return data, nil
}

func credentialGenerationIn(fs FS, stateDir string, kind CredentialKind) (int, error) {
	data, err := readCredentialGenerations(fs, stateDir)
	if err != nil {
		return 0, err
	}
	return data.Generations[string(kind)], nil
}

func writeCredentialGeneration(fs FS, stateDir string, kind CredentialKind) error {
	data, err := readCredentialGenerations(fs, stateDir)
	if err != nil {
		return err
	}
	data.Generations[string(kind)]++
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	return atomicWrite(fs, credentialGenerationFile(stateDir), append(raw, '\n'), 0o600)
}

// isNotFound reports whether err is the not-found outcome, wrapping
// included.
func isNotFound(err error) bool {
	return errors.Is(err, ErrCredentialNotFound)
}

// classifyExecError maps a failed external command to a StoreError state.
// The exit codes and stderr fragments are per-tool; each adapter states
// its own mapping so this helper stays generic.
func classifyExecError(op, kind string, res ExecResult, launchErr error) *StoreError {
	if launchErr != nil {
		return &StoreError{Op: op, Kind: kind, State: "unavailable", Err: launchErr}
	}
	return storeErrorf(op, kind, "unavailable", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
}
