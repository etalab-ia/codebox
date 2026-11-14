package jcvendor

// support.go is KIT-AUTHORED SCAFFOLDING, not vendored logic.
//
// The two vendored files in this package (project_secret_guest_files.go and
// project_secret_bindings.go) are byte-identical copies of just-code's
// internal/justcode sources at commit
// f483a8003d7745dfe3cdea7363d5f366af51eebc, with only the package clause
// renamed. They reference symbols defined elsewhere in just-code; this file
// supplies exactly those symbols so the vendored files compile unchanged.
//
// Every entry is labelled:
//
//	EXACT COPY  — taken verbatim (comments included) from the named
//	              just-code source file at the pinned commit. Assertions in
//	              this kit DO exercise these.
//	KIT STUB    — a compile-only stand-in for machinery outside the Phase 3
//	              scope (credential-store resolution). No assertion in this
//	              kit executes a KIT STUB path; each stub fails loudly so an
//	              accidental call cannot pass silently.
//
// The README's "Vendoring" section repeats this inventory.

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// MicrosandboxRuntime shell (EXACT structure where the vendored files touch
// it; guestShell body is an EXACT COPY from workspace_sealed.go).
// ---------------------------------------------------------------------------

// GuestClient is the subset of just-code's msbClient interface
// (internal/justcode/microsandbox.go) that the vendored guest-file code
// calls. The three method signatures are EXACT COPIES of the corresponding
// msbClient entries. just-code's SDK-backed implementation connects to the
// sandbox and runs Shell/FS().Write; the kit's probe client does the same.
type GuestClient interface {
	// WriteFile writes one payload to a path inside the running sandbox. It
	// is the only byte channel into the guest; callers write resolved files
	// one at a time.
	WriteFile(ctx context.Context, name, guestPath string, data []byte) error
	// Exec runs a shell command in the sandbox, returning its exit code and stderr.
	Exec(ctx context.Context, name, command string) (int, string, error)
	// ExecCapture is Exec with stdout captured too.
	ExecCapture(ctx context.Context, name, command string) (stdout, stderr string, code int, err error)
}

// MicrosandboxRuntime mirrors the fields and methods of just-code's
// MicrosandboxRuntime that the two vendored files reference. Field and
// method names match the original exactly; bodies that only glue the kit
// together are KIT STUBs as marked.
type MicrosandboxRuntime struct {
	// Client backs every guest operation the vendored publish path issues.
	Client GuestClient
	// ProjectSecrets is referenced by the vendored projectSecretStore;
	// that path is a KIT STUB and never runs in this kit.
	ProjectSecrets *ProjectSecretStore
	// instanceName and stateDir stand in for the original runtime's
	// instance identity and state directory.
	instanceName string
	stateDir     string
	// cfg mirrors the original runtime's config handle; only WorkspaceDir
	// is referenced by the vendored files.
	cfg runtimeConfig
}

// runtimeConfig mirrors the one field of the original runtime config the
// vendored files read.
type runtimeConfig struct {
	WorkspaceDir string
}

// NewGuestRuntimeForTest builds the kit's runtime shell around a real
// SDK-backed GuestClient. It exists so the package-main probe can drive the
// vendored (unexported) publish pipeline; it adds no logic of its own.
func NewGuestRuntimeForTest(client GuestClient, instanceName string) *MicrosandboxRuntime {
	return &MicrosandboxRuntime{Client: client, instanceName: instanceName}
}

// InstanceName returns the sandbox name, exactly as the original method does.
func (m *MicrosandboxRuntime) InstanceName() string { return m.instanceName }

// stateDirOrDefault mirrors the original method's contract for the one caller
// in the vendored files (the store path, a KIT STUB path never exercised).
func (m *MicrosandboxRuntime) stateDirOrDefault() string { return m.stateDir }

// guestShell runs a command in the guest, surfacing stderr on failure.
// EXACT COPY from internal/justcode/workspace_sealed.go at the pinned commit.
func (m *MicrosandboxRuntime) guestShell(ctx context.Context, command string) error {
	code, stderr, err := m.Client.Exec(ctx, m.InstanceName(), command)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("guest command exited %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// msbGuestWorkspace is the guest path of the project working tree.
// EXACT COPY from internal/justcode/microsandbox_sdk.go at the pinned commit.
const msbGuestWorkspace = "/workspace"

// shellQuote makes s safe for a POSIX shell to parse as a single-quoted
// word: wrap in single quotes, replacing embedded single quotes with the
// '\” idiom. This is the standard safe representation for arbitrary data.
// EXACT COPY from internal/justcode/agentvm.go at the pinned commit.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---------------------------------------------------------------------------
// Dotenv and component validation helpers.
// ---------------------------------------------------------------------------

// IsProjectDotenvName reports whether name is a dotenv file that can carry
// real values.
// EXACT COPY from internal/justcode/workspace.go at the pinned commit.
func IsProjectDotenvName(name string) bool { return isDotenvName(name) }

// dotenvSafeSuffixes are .env file suffixes that never carry real values.
// EXACT COPY from internal/justcode/workspace.go at the pinned commit.
var dotenvSafeSuffixes = map[string]bool{
	".example": true,
	".sample":  true,
}

// isDotenvName reports whether name is a dotenv file that can carry real
// values.
// EXACT COPY from internal/justcode/workspace.go at the pinned commit.
func isDotenvName(name string) bool {
	lower := strings.ToLower(name)
	if dotenvSafeSuffixes[filepath.Ext(lower)] {
		// .env.example / .env.sample: a document, not a value.
		return false
	}
	if lower == ".env" {
		return true
	}
	if strings.HasPrefix(lower, ".env.") {
		return true
	}
	return strings.HasSuffix(lower, ".env")
}

// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
func validSecretIdentifier(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	for i, r := range v {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
func validSecretProfile(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if r != '_' && r != '-' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
func validSecretComponent(v string) bool {
	if v == "" || v == "." || path.IsAbs(v) || path.Clean(v) != v || strings.ContainsAny(v, "\\:") {
		return false
	}
	for _, part := range strings.Split(v, "/") {
		if part == ".." {
			return false
		}
	}
	for _, r := range v {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// isDNSName reports whether host is a plausible DNS name: dot-separated
// labels of letters, digits and hyphens, no leading or trailing hyphen, at
// least one dot. Wildcards and schemes are rejected.
// EXACT COPY from internal/justcode/bindings.go at the pinned commit.
func isDNSName(host string) bool {
	if !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Reserved guest variables (used by the vendored validateProjectSecretBindingSet).
// ---------------------------------------------------------------------------

// CredentialKind names a credential-store entry kind.
// EXACT COPY (subset) from internal/justcode/credentials.go at the pinned commit.
type CredentialKind string

const (
	// CredentialAlbert is the Albert API key.
	CredentialAlbert CredentialKind = "albert"
	// CredentialGithub is a GitHub token, bound into the guest only after
	// the project grants it.
	CredentialGithub CredentialKind = "github"
	// CredentialContext7 is a Context7 API key, bound like github.
	CredentialContext7 CredentialKind = "context7"
)

// EXACT COPY from internal/justcode/microsandbox.go at the pinned commit.
const msbAllowHost = "albert.api.etalab.gouv.fr"

// EXACT COPY from internal/justcode/microsandbox.go at the pinned commit.
const msbAPISecretEnv = "ALBERT_API_KEY"

// ProjectBindingRef identifies a project-scoped binding beyond the shared
// binding metadata.
// EXACT COPY from internal/justcode/bindings.go at the pinned commit.
type ProjectBindingRef struct {
	Profile   string
	Component string
	Name      string
	Reference string
}

// msbSecretBinding is the host-side record for one guest-bound credential.
// EXACT COPY from internal/justcode/bindings.go at the pinned commit.
type msbSecretBinding struct {
	// Kind is the credential store kind the value resolves from.
	Kind CredentialKind
	// GuestEnv is the variable name the guest sees. Microsandbox exposes the
	// secret *placeholder* under this name; the real value is swapped in at
	// the network boundary for AllowHosts only.
	GuestEnv string
	// Placeholder is the value exposed to guest processes and matched by the
	// proxy. Empty uses the runtime default ($MSB_<GuestEnv>); project-scoped
	// application secrets can set a stable, collision-resistant placeholder.
	Placeholder string
	// HostEnv is the host-side transport variable the env reference resolves
	// from. It is namespaced under JUST_CODE_ and exists only for the
	// duration of an SDK call (see withHostSecrets), so a dedicated name —
	// never the user's own variable — avoids clobbering anything.
	HostEnv string
	// AllowHosts are the only hosts that ever receive the real value. Hosts
	// are disjoint across bindings (validateSecretBindings), so one binding's
	// credential can never be substituted toward another binding's
	// destination.
	AllowHosts []string
	// Optional bindings are inert until the project approves them explicitly
	// (host-local bindings.json); global storage alone never binds.
	Optional bool
	// Project is set for a Phase 2 project-secret binding: it names the
	// imported entry the value comes from. Nil for the built-in bindings.
	Project *ProjectBindingRef
}

// EXACT COPY from internal/justcode/bindings.go at the pinned commit.
func msbSecretBindings() []msbSecretBinding {
	return []msbSecretBinding{
		{
			Kind:       CredentialAlbert,
			GuestEnv:   msbAPISecretEnv,
			HostEnv:    "JUST_CODE_HOST_ALBERT_API_KEY",
			AllowHosts: []string{msbAllowHost},
		},
		{
			Kind:       CredentialGithub,
			GuestEnv:   "GITHUB_TOKEN",
			HostEnv:    "JUST_CODE_HOST_GITHUB_TOKEN",
			AllowHosts: []string{"github.com", "api.github.com", "raw.githubusercontent.com"},
			Optional:   true,
		},
		{
			Kind:       CredentialContext7,
			GuestEnv:   "CONTEXT7_API_KEY",
			HostEnv:    "JUST_CODE_HOST_CONTEXT7_API_KEY",
			AllowHosts: []string{"context7.com"},
			Optional:   true,
		},
	}
}

// ---------------------------------------------------------------------------
// Host filesystem abstraction (used by the vendored bindings persistence).
// ---------------------------------------------------------------------------

// EXACT COPY from internal/justcode/resolve_files.go at the pinned commit.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	MkdirAll(path string, perm os.FileMode) error
	Remove(path string) error
	RenameTmp(oldPath, newPath string) error
	// WriteTemp writes data to a fresh temporary file in dir (named with
	// base as a prefix) and returns its path. It exists so atomic writes
	// stay inside the injected FS instead of touching the real disk.
	WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error)
	// CreateExclusive creates path with its content, failing with
	// os.ErrExist when it already exists. It is the exclusive-create
	// primitive used by per-project locks: the OS, not a check-then-write
	// sequence, guarantees exclusivity.
	CreateExclusive(path string, data []byte, perm os.FileMode) error
}

// EXACT COPY from internal/justcode/resolve_files.go at the pinned commit.
type realFS struct{}

func (realFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (realFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func (realFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func (realFS) Remove(path string) error                     { return os.Remove(path) }
func (realFS) RenameTmp(oldPath, newPath string) error      { return os.Rename(oldPath, newPath) }
func (realFS) WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error) {
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func (realFS) CreateExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// DefaultFS is the real filesystem.
// EXACT COPY from internal/justcode/resolve_files.go at the pinned commit.
var DefaultFS FS = realFS{}

// atomicWrite writes data to path via a temp file + rename, creating parent
// directories first. The temp file lives in the same directory so rename
// stays on one filesystem. All filesystem access goes through fs, so an
// injected FS sees the temp file and the rename.
// EXACT COPY from internal/justcode/resolve_files.go at the pinned commit.
func atomicWrite(fs FS, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	name, err := fs.WriteTemp(dir, filepath.Base(path), data, perm)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := fs.RenameTmp(name, path); err != nil {
		_ = fs.Remove(name)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Credential-store resolution: KIT STUBS only.
//
// The vendored project_secret_bindings.go references the Phase 1/2 store
// machinery below. Phase 3 (guest dotenv files) consumes *deliveries*, which
// this kit constructs directly with the vendored ProjectSecretPlaceholder —
// the same derivation the real Deliveries performs. None of the stubs below
// executes in any assertion; each returns an error so an accidental call
// fails loudly instead of fabricating store state.
// ---------------------------------------------------------------------------

// CredentialStore is the interface NewProjectSecretStore accepts.
// KIT STUB (trimmed to a marker method; the real interface lives in
// internal/justcode/credentials.go and is out of scope for this kit).
type CredentialStore interface {
	// Kind returns the store identifier for display ("keychain",
	// "credential-manager", "secret-service", "file").
	Kind() string
}

// DefaultCredentialStore is a KIT STUB: the kit never resolves a native
// credential store. It returns nil, which the vendored callers already handle
// by falling back to NewFileCredentialStore (also a stub).
func DefaultCredentialStore() CredentialStore { return nil }

// NewFileCredentialStore is a KIT STUB: it always fails. Store resolution is
// Phase 2 scope and no assertion in this kit reaches it.
func NewFileCredentialStore() (CredentialStore, error) {
	return nil, fmt.Errorf("kit stub: file credential store resolution is outside the phase 3 qualification scope")
}

// ProjectSecretStore mirrors the original struct's fields the vendored files
// touch (stateDir, namespace). Construction goes through the NewProjectSecretStore
// KIT STUB.
type ProjectSecretStore struct {
	root      string
	namespace string
	stateDir  string
}

// NewProjectSecretStore is a KIT STUB: it always fails. No assertion resolves
// a project store; deliveries are constructed directly.
func NewProjectSecretStore(root, stateDir string, store CredentialStore) (*ProjectSecretStore, error) {
	return nil, fmt.Errorf("kit stub: project secret store resolution is outside the phase 3 qualification scope")
}

// Namespace reports the store's project namespace. It is a plain accessor
// over scaffolding state, used only by vendored code paths this kit never
// invokes.
func (s *ProjectSecretStore) Namespace() string { return s.namespace }

// ProjectSecretEntry is one imported value's index record.
// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
type ProjectSecretEntry struct {
	Profile   string `json:"profile"`
	Component string `json:"component"`
	Name      string `json:"name"`
	Reference string `json:"reference"`
}

// identity is the profile/component/name triple that names one imported value
// within a project namespace.
// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
func (e ProjectSecretEntry) identity() string {
	return e.Profile + "\x00" + e.Component + "\x00" + e.Name
}

// projectSecretIndex mirrors the persisted index shape readIndex returns.
// EXACT COPY from internal/justcode/project_secrets.go at the pinned commit.
type projectSecretIndex struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Namespace     string               `json:"namespace"`
	Store         string               `json:"store"`
	Generation    string               `json:"generation,omitempty"`
	Entries       []ProjectSecretEntry `json:"entries"`
}

// readIndex is a KIT STUB: it always fails. The vendored Deliveries and
// ReadProjectSecretBindingsRecordFor reference it, but this kit constructs
// deliveries directly and never resolves the credential index.
func (s *ProjectSecretStore) readIndex() (projectSecretIndex, error) {
	return projectSecretIndex{}, fmt.Errorf("kit stub: project secret index resolution is outside the phase 3 qualification scope")
}

// ---------------------------------------------------------------------------
// Exported wrappers over vendored unexported functions.
//
// These forward verbatim, adding no logic, so the package-main native probe
// can drive the exact vendored pipeline (syncGuestSecretFiles, which chains
// BuildGuestSecretFiles, readGuestSecretFiles, PlanGuestSecretPublish and
// publishGuestSecretFiles) and inspect the vendored helpers.
// ---------------------------------------------------------------------------

// SyncGuestSecretFilesForTest forwards to the vendored syncGuestSecretFiles.
func (m *MicrosandboxRuntime) SyncGuestSecretFilesForTest(ctx context.Context, deliveries []ProjectSecretDelivery, previouslyManaged []string) ([]string, error) {
	return m.syncGuestSecretFiles(ctx, deliveries, previouslyManaged)
}

// IsManagedGuestSecretFileForTest forwards to the vendored isManagedGuestSecretFile.
func IsManagedGuestSecretFileForTest(content string) bool {
	return isManagedGuestSecretFile(content)
}

// GuestWorkspaceDotenvPathForTest forwards to the vendored guestWorkspaceDotenvPath.
func GuestWorkspaceDotenvPathForTest(component string) (string, error) {
	return guestWorkspaceDotenvPath(component)
}
