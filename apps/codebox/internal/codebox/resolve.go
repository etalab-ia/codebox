package codebox

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the P04 typed configuration resolver: pure sources in,
// typed values with per-field provenance out. It does not mutate the process
// environment and does not read any file implicitly; the caller supplies the
// user settings and project manifest as parsed values. Legacy environment
// handling (LoadConfig) stays untouched until P12; the resolver is internal
// and exercised by tests plus `codebox config explain`.

// Source ranks configuration sources for a non-secret field, strongest first.
// The order is the configuration contract of #29: explicit flags > namespaced
// environment > project manifest > user settings > built-in defaults.
type Source int

const (
	SourceFlag Source = iota
	SourceEnv
	SourceProject
	SourceUser
	SourceDefault
)

func (s Source) String() string {
	switch s {
	case SourceFlag:
		return "flag"
	case SourceEnv:
		return "env"
	case SourceProject:
		return "project"
	case SourceUser:
		return "user"
	default:
		return "default"
	}
}

// Setting is one explicitly supplied field value, whatever its source rank.
// Set distinguishes "unset" from "explicitly empty" (the same distinction the
// legacy Config keeps for the server password). source records the winning
// rank; the zero value (unset) reads as SourceDefault.
type Setting struct {
	Value  string
	Set    bool
	source Source
}

// Settings is a set of explicitly supplied values grouped by source rank.
type Settings struct {
	Flag    map[string]string
	Env     map[string]string
	Project map[string]string
	User    map[string]string
}

// setting resolves one field across the source ranks. An explicit empty string
// wins over any lower source (it means the user turned the field off), which
// is why presence is checked before value emptiness at each rank. The
// returned Setting carries the winning source.
func (s Settings) setting(key string) Setting {
	for _, rank := range []struct {
		src Source
		m   map[string]string
	}{
		{SourceFlag, s.Flag},
		{SourceEnv, s.Env},
		{SourceProject, s.Project},
		{SourceUser, s.User},
	} {
		if v, ok := rank.m[key]; ok {
			return Setting{Value: v, Set: true, source: rank.src}
		}
	}
	return Setting{}
}

// Resolved is a resolved field: its effective value, whether it was set at
// all (explicit empty included), and the winning source.
type Resolved struct {
	Value  string
	Set    bool
	Source Source
}

// resolveValue resolves key with def as the built-in default.
func (s Settings) resolveValue(key, def string) Resolved {
	st := s.setting(key)
	if st.Set {
		return Resolved{Value: st.Value, Set: true, Source: st.source}
	}
	return Resolved{Value: def, Set: false, Source: SourceDefault}
}

// UserSettings is the typed global user configuration file
// (~/.config/codebox/settings.json). It is secret-free: credentials live in
// the P08 credential store, referenced by name only.
type UserSettings struct {
	// SchemaVersion is the file format version. Readers fail clearly on
	// newer unsupported versions rather than guessing.
	SchemaVersion int `json:"schemaVersion"`

	// DefaultRuntime is the preferred runtime name (microsandbox, tart,
	// agent-vm). Empty means the built-in default.
	DefaultRuntime string `json:"defaultRuntime,omitempty"`

	// DefaultIsolation is the preferred isolation level (backend, full).
	DefaultIsolation string `json:"defaultIsolation,omitempty"`

	// DefaultModel is the preferred OpenCode model (provider/model-id).
	DefaultModel string `json:"defaultModel,omitempty"`
	// Models is the set of Albert models enabled in OpenCode's model picker.
	// Empty preserves the built-in model set for settings written by older versions.
	Models []string `json:"models,omitempty"`
	// Providers lists the connected inference platforms, in display order.
	// Empty means the historical single-platform configuration: Albert API.
	// Credentials are never stored here; each platform's key lives in the
	// host credential store under its kind.
	Providers []ProviderSettings `json:"providers,omitempty"`
	// ProviderModelIDs maps a provider reference to the API model ID it
	// aliases, when a platform exposes an alias (e.g. an imported OpenRouter
	// "glm" pointing at "z-ai/glm-4.6"). Empty means the reference is the ID.
	ProviderModelIDs map[string]string `json:"providerModelIDs,omitempty"`

	// GitName and GitEmail are the git identity for guest commits (P11).
	// Non-secret; the guest bootstrap configures git with them.
	GitName  string `json:"gitName,omitempty"`
	GitEmail string `json:"gitEmail,omitempty"`

	// CredentialRef names the default global credential (P08 reference,
	// never a literal).
	CredentialRef string `json:"credentialRef,omitempty"`

	// StatsEnabled records the explicit "o" to sharing non-nominative
	// configuration choices (issue #181). False is the default and means
	// nothing is queued or sent.
	StatsEnabled bool `json:"statsEnabled,omitempty"`
	// StatsConsentAsked records that the setup wizard asked the question, so
	// it is asked once and the answer also covers later `init` runs.
	StatsConsentAsked bool `json:"statsConsentAsked,omitempty"`
}

// userSettingsSchemaVersion is the current settings.json format version.
const userSettingsSchemaVersion = 1

// maxSupportedSettingsSchema is the highest schemaVersion this build reads.
const maxSupportedSettingsSchema = 1

// ProjectManifest is the typed project manifest (.codebox/project.json),
// intended for version control and therefore secret-free and free of
// host-absolute paths.
type ProjectManifest struct {
	// SchemaVersion is the manifest format version. Readers fail clearly
	// on newer unsupported versions.
	SchemaVersion int `json:"schemaVersion"`

	// Project is an optional readable name. Nothing currently writes it (P12b
	// dropped the question) and instance naming derives from the project root
	// (P05); it is read for backward compatibility and shown in the setup
	// review when an older manifest carries one.
	Project string `json:"project,omitempty"`

	// Runtime and Isolation are the project's explicit runtime and
	// isolation choices. Empty means "not chosen here".
	Runtime   string `json:"runtime,omitempty"`
	Isolation string `json:"isolation,omitempty"`
	Model     string `json:"model,omitempty"`
	// Harness is the agent harness the project runs (#176). Empty means the
	// default: OpenCode. The field carries semantic weight — a manifest that
	// selects anything else must not be read by a binary that would silently
	// run OpenCode for it — which is why adding it bumps the schemaVersion.
	Harness string `json:"harness,omitempty"`
	// SmallModel is an explicit lightweight model for OpenCode titles. Empty
	// follows the effective main model, including later changes to that model.
	SmallModel string `json:"smallModel,omitempty"`
	CPUs       int    `json:"cpus,omitempty"`
	MemoryMB   int    `json:"memoryMB,omitempty"`
	// Ports are explicit host-to-guest TCP forwards for Microsandbox. Nil
	// preserves the historical default preview range for older manifests.
	Ports           []PortMapping `json:"ports,omitempty"`
	PortsConfigured bool          `json:"portsConfigured,omitempty"`

	// CredentialRef names the project's credential (a P08 reference).
	CredentialRef string `json:"credentialRef,omitempty"`

	// Storage recorded where the manifest and lock live. Only the versioned
	// location has readers today, so nothing writes this; the field is read
	// for backward compatibility and shown in the setup review.
	Storage string `json:"storage,omitempty"`
	// Skills are catalogue IDs selected for this project. Their immutable
	// source revisions and content digests live in the lockfile.
	Skills []string `json:"skills,omitempty"`
	// SkillsLocalOnly keeps selections in host state instead of project files.
	// A checkout with this flag never receives generated skill instructions.
	SkillsLocalOnly bool `json:"skillsLocalOnly,omitempty"`
	// MCPConnectors are selected entries from the curated remote connector
	// catalogue. They contain identifiers only, never credentials.
	MCPConnectors []string `json:"mcpConnectors,omitempty"`
	// DependencySetID binds this manifest to the lockfile generation written
	// by an explicit project update. Empty is accepted for pre-P18 projects.
	DependencySetID string `json:"dependencySetId,omitempty"`
	// Secrets carries only non-secret remote-secret references and delivery
	// hints. Values and host approvals never live in this file.
	Secrets *ProjectSecretsManifest `json:"secrets,omitempty"`
}

// ProjectSecretsManifest is the version-controlled, secret-free declaration for
// a project's shared development secrets. A repository manifest never grants
// access: the host-local bindings.json approval remains authoritative.
type ProjectSecretsManifest struct {
	// ProjectID is a canonical lowercase UUIDv4 naming the remote project.
	ProjectID string `json:"projectId"`
	// Bindings describe how approved variables should be delivered. They are
	// hints the CLI turns into one local approval screen; they do not carry
	// secret values.
	Bindings []ProjectSecretBindingHint `json:"bindings,omitempty"`
}

// ProjectSecretBindingHint is the manifest-level delivery hint for one dotenv
// variable. It mirrors packages/secrets.BindingHint without importing another
// Go module from the CLI's config model.
type ProjectSecretBindingHint struct {
	Component    string   `json:"component"`
	Name         string   `json:"name"`
	Delivery     string   `json:"delivery,omitempty"`
	GuestEnv     string   `json:"guestEnv,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
}

// PortMapping publishes a guest TCP port on a host loopback port.
type PortMapping struct {
	Host  uint16 `json:"host"`
	Guest uint16 `json:"guest"`
}

// projectManifestSchemaVersion is the current project.json format version.
// P18 adds a dependency-set generation so a launch can reject a partially
// written manifest/lock pair after an interrupted update. #176 adds the
// harness: a binary that does not know the field would run OpenCode for a
// project that asked for something else, which is the silent substitution the
// version check exists to prevent. #385 adds the optional `secrets` block:
// a binary that ignores it would silently skip approved remote secrets.
const projectManifestSchemaVersion = 7

// maxSupportedManifestSchema is the highest manifest schemaVersion this build
// reads. A newer version is a hard error: the file was written by a binary
// this one cannot interpret.
const maxSupportedManifestSchema = 7

// Lockfile is the project lock (.codebox/lock.json): resolved revisions of
// everything the manifest pins (skills, images, connector packages), so a
// launch is reproducible without floating "latest" references.
type Lockfile struct {
	SchemaVersion int `json:"schemaVersion"`
	// DependencySetID must match the manifest when set. Empty is accepted for
	// pre-P18 projects.
	DependencySetID string `json:"dependencySetId,omitempty"`
	// Entries maps a manifest pin name to its resolved revision.
	Entries map[string]string `json:"entries,omitempty"`
	// Skills pins the selected catalogue artifacts by ID.
	Skills map[string]SkillLock `json:"skills,omitempty"`
}

const lockfileSchemaVersion = 3

// maxSupportedLockfileSchema is the highest lock.json schemaVersion this
// build reads.
const maxSupportedLockfileSchema = 3

// ValidateProjectSecretsManifest checks the version-controlled secrets block.
// It holds no values; only a canonical lowercase UUIDv4 project ID and
// delivery hints. Full hint validation happens when the CLI turns them into
// host-local approvals (PR 4).
func ValidateProjectSecretsManifest(s *ProjectSecretsManifest) error {
	if s == nil {
		return nil
	}
	if !validCanonicalUUIDv4(s.ProjectID) {
		return fmt.Errorf("project secrets: projectId must be a canonical lowercase UUIDv4")
	}
	seen := map[string]bool{}
	for _, hint := range s.Bindings {
		if err := ValidateComponentPathForManifest(hint.Component); err != nil {
			return fmt.Errorf("project secrets binding: %w", err)
		}
		if !validSecretIdentifier(hint.Name) {
			return fmt.Errorf("project secrets binding: invalid variable name %q", hint.Name)
		}
		id := hint.Component + "\x00" + hint.Name
		if seen[id] {
			return fmt.Errorf("project secrets binding: duplicate %s in %s", hint.Name, hint.Component)
		}
		seen[id] = true
		switch hint.Delivery {
		case "", "proxy":
		case "guest":
			if len(hint.Destinations) > 0 || hint.GuestEnv != "" {
				return fmt.Errorf("project secrets binding %s (%s): guest delivery must not declare destinations or guestEnv", hint.Name, hint.Component)
			}
		default:
			return fmt.Errorf("project secrets binding %s (%s): unsupported delivery %q", hint.Name, hint.Component, hint.Delivery)
		}
	}
	return nil
}

// ValidateComponentPathForManifest performs the structural component-path check
// without importing the shared secrets module into the CLI config model.
func ValidateComponentPathForManifest(p string) error {
	if p == "" || len(p) > 256 || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:") {
		return fmt.Errorf("invalid dotenv component path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid dotenv component path %q", p)
		}
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return fmt.Errorf("invalid control character in component path")
		}
	}
	return nil
}

func validCanonicalUUIDv4(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		case 14:
			if c != '4' {
				return false
			}
		case 19:
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
	}
	return true
}

// checkSchemaVersion rejects a version this build cannot interpret. It is
// shared by all three file formats so the failure mode is uniform.
func checkSchemaVersion(kind string, got, max int) error {
	if got < 1 {
		return fmt.Errorf("%s: schemaVersion missing or invalid (%d)", kind, got)
	}
	if got > max {
		return fmt.Errorf("%s: schemaVersion %d is newer than this build supports (%d); upgrade codebox to read it", kind, got, max)
	}
	return nil
}

// Platform directories. All paths go through these helpers so no build bakes
// in a Linux-only layout.

// UserConfigDir returns the directory holding the global user settings.
func UserConfigDir() (string, error) {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "codebox"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "codebox"), nil
}

// UserSettingsPath returns the global settings file path.
func UserSettingsPath() (string, error) {
	dir, err := UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// ProjectStateDir returns the version-controlled project state directory for
// a project root (the directory containing the manifest and lockfile).
func ProjectStateDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".codebox")
}

// ProjectManifestPath returns the project manifest path for a root.
func ProjectManifestPath(projectRoot string) string {
	return filepath.Join(ProjectStateDir(projectRoot), "project.json")
}

// ProjectLockPath returns the project lockfile path for a root.
func ProjectLockPath(projectRoot string) string {
	return filepath.Join(ProjectStateDir(projectRoot), "lock.json")
}

// EnvNamespace is the prefix of namespaced environment variables read by the
// resolver (CODEBOX_*). Legacy unprefixed variables keep their documented
// meaning during the deprecation interval; the resolver itself only reads
// the namespaced forms.
const EnvNamespace = "CODEBOX_"

// namespacedEnvKey maps a logical field name to its namespaced variable.
func namespacedEnvKey(field string) string {
	return EnvNamespace + strings.ToUpper(field)
}

// NamespacedEnvKeys lists the namespaced variables the resolver reads, for
// documentation and the explain command.
var namespacedEnvFields = []string{
	"runtime",
	"isolation",
	"workspace_dir",
	"model",
	"cpus",
	"memory_mb",
	"credential_ref",
}

// EnvSettings extracts the namespaced CODEBOX_* values from a lookup
// without touching os.Environ. Only known fields are read; unknown
// namespaced variables are ignored (they may belong to a newer schema).
func EnvSettings(lookup EnvLookup) map[string]string {
	out := map[string]string{}
	for _, field := range namespacedEnvFields {
		if v, ok := lookup(namespacedEnvKey(field)); ok {
			out[field] = v
		}
	}
	return out
}

// secretFieldNames are the manifest/settings fields that must never hold a
// literal secret. The managed schema rejects them at parse time.
var secretFieldNames = map[string]bool{
	"apiKey": true, "api_key": true, "password": true, "secret": true,
	"token": true, "credentials": true,
}

// checkNoSecretFields rejects a raw parsed map containing secret-suggestive
// keys, so a literal credential can never enter a version-controlled file
// through the managed schema. Credential references (credentialRef) are the
// only supported form.
func checkNoSecretFields(kind string, raw map[string]any) error {
	var offenders []string
	for k := range raw {
		if secretFieldNames[k] {
			offenders = append(offenders, k)
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf("%s: %s must not appear in a managed file; use credentialRef to name a stored credential", kind, strings.Join(offenders, ", "))
}

// ExplainEntry is one row of the `config explain` output.
type ExplainEntry struct {
	Field  string
	Value  string
	Set    bool
	Source Source
}

// Explain renders the effective configuration with per-field provenance.
// Secret values (the credential reference target) are never included: only
// the reference name is shown.
func Explain(s Settings) []ExplainEntry {
	rows := []struct {
		field, def string
	}{
		{"runtime", "microsandbox"},
		{"isolation", "backend"},
		{"workspace_dir", "./workspace"},
		{"model", ""},
		{"cpus", "0"},
		{"memory_mb", "0"},
		{"credential_ref", ""},
	}
	out := make([]ExplainEntry, 0, len(rows))
	for _, r := range rows {
		v := s.resolveValue(r.field, r.def)
		out = append(out, ExplainEntry{Field: r.field, Value: v.Value, Set: v.Set, Source: v.Source})
	}
	return out
}

// FormatExplain renders explain entries as stable text rows. Unset fields
// still show their effective value — the built-in default — with source
// "default"; the Set flag distinguishes explicit values (explicit empty
// included) from defaults.
func FormatExplain(entries []ExplainEntry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%-16s %-8s %s\n", e.Field, e.Source, e.Value)
	}
	return b.String()
}
