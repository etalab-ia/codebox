package jcvendor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Phase 2 host-local binding records for project secrets. These records hold
// the non-secret delivery metadata for one project namespace — which imported
// variable maps to which guest variable, and which destinations may receive
// its value — and never a credential value. They live in host state, not in
// the project, because approving a destination is a host-local decision that
// must not travel with a clone.
//
// The credential value itself stays where Phase 1 put it (the native store,
// referenced by an opaque name in the project secret index); this file only
// says which of those entries the runtime may expose and where.

// ProjectSecretBinding is one approved mapping from an imported value to a
// guest-visible placeholder and destination set.
type ProjectSecretBinding struct {
	// Profile and Component identify the imported entry this binding exposes.
	Profile   string `json:"profile"`
	Component string `json:"component"`
	Name      string `json:"name"`
	// GuestEnv is the variable the guest sees; the value under it is the
	// placeholder, never the credential.
	GuestEnv string `json:"guestEnv"`
	// Destinations are the only hosts that may receive the real value.
	// Overlapping destinations across bindings are allowed: the qualified
	// runtime contract authorizes per binding, and the canary matrix in
	// tests/integration/project-secrets/native covers the shared-host and
	// cross-host denial cases.
	Destinations []string `json:"destinations"`
}

func (b ProjectSecretBinding) identity() string {
	return b.Profile + "\x00" + b.Component + "\x00" + b.Name
}

// projectSecretBindingSet is the persisted host-local record for a namespace.
type projectSecretBindingSet struct {
	SchemaVersion int    `json:"schemaVersion"`
	Namespace     string `json:"namespace"`
	// Profile is the project's active environment profile. Empty means no
	// project secrets are delivered: development and production are never
	// combined implicitly.
	Profile  string                 `json:"profile,omitempty"`
	Bindings []ProjectSecretBinding `json:"bindings,omitempty"`
}

const projectSecretBindingSchemaVersion = 1

// maxProjectSecretDestinations bounds one binding's destination list so a
// malformed record cannot inflate the runtime request without limit.
const maxProjectSecretDestinations = 32

// ProjectSecretBindingsPath is the host-local binding record for a project
// secret namespace.
func ProjectSecretBindingsPath(stateDir, namespace string) string {
	return filepath.Join(stateDir, "project-secrets", namespace, "bindings.json")
}

// ProjectSecretGuestEnv derives the guest variable name for a component and
// variable. The component participates so two components that define the same
// variable name stay distinct: apps/api/.env + API_KEY becomes
// JC_APPS_API_API_KEY while .env + API_KEY stays JC_API_KEY. The conventional
// `.env` component contributes no prefix; other dotenv spellings contribute
// their distinguishing stem (`docker.env` -> DOCKER, `.env.local` -> LOCAL).
// The derivation is deterministic so a restart presents the same name.
func ProjectSecretGuestEnv(component, name string) string {
	var parts []string
	dir, base := path.Split(component)
	for _, segment := range strings.Split(strings.Trim(dir, "/"), "/") {
		if segment == "" || segment == "." {
			continue
		}
		if s := sanitizeGuestEnvSegment(segment); s != "" {
			parts = append(parts, s)
		}
	}
	if stem := dotenvComponentStem(base); stem != "" {
		parts = append(parts, stem)
	}
	return "JC_" + strings.Join(append(parts, name), "_")
}

// dotenvComponentStem returns the distinguishing part of a dotenv filename:
// empty for the conventional `.env`, otherwise the variant or prefix.
func dotenvComponentStem(base string) string {
	lower := strings.ToLower(base)
	switch {
	case lower == ".env":
		return ""
	case strings.HasPrefix(lower, ".env."):
		return sanitizeGuestEnvSegment(strings.TrimPrefix(lower, ".env."))
	case strings.HasSuffix(lower, ".env"):
		return sanitizeGuestEnvSegment(strings.TrimSuffix(lower, ".env"))
	default:
		return sanitizeGuestEnvSegment(lower)
	}
}

// sanitizeGuestEnvSegment reduces a path segment to ASCII upper-case
// characters, digits and underscores so the derived name is a portable
// environment variable identifier.
func sanitizeGuestEnvSegment(segment string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToUpper(segment) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// ProjectSecretPlaceholder derives the guest-visible placeholder for one
// imported identity. It is deliberately dollar-free: the guest loader matrix
// in tests/integration/project-secrets shows that Node dotenv-expand, Go
// godotenv and braced python-dotenv interpolation all erase an undefined
// "$MSB_..." reference, while an opaque marker survives every tested quote and
// interpolation mode. The value is a stable digest, so it changes only when
// the identity changes and never encodes the credential.
func ProjectSecretPlaceholder(namespace, profile, component, name string) string {
	h := sha256.New()
	for _, part := range []string{"jc-project-secret-marker-v1", namespace, profile, component, name} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "__JUST_CODE_SECRET_" + hex.EncodeToString(h.Sum(nil))[:16] + "__"
}

// reservedGuestEnvs lists the guest variables owned by the built-in bindings.
// A project secret may never take one of these names: the runtime would then
// have two registrations for one variable and the system binding's credential
// would be shadowed.
func reservedGuestEnvs() map[string]bool {
	reserved := map[string]bool{}
	for _, b := range msbSecretBindings() {
		reserved[b.GuestEnv] = true
	}
	return reserved
}

// validateProjectSecretBindingSet checks the structural invariants the
// runtime relies on: known-shaped identifiers, distinct guest variables,
// reserved-name protection, and a non-empty destination list of DNS names.
// Destinations may overlap between bindings; that is qualified behavior.
func validateProjectSecretBindingSet(set projectSecretBindingSet, namespace string) error {
	if set.SchemaVersion != projectSecretBindingSchemaVersion {
		return fmt.Errorf("project secret bindings: unsupported schema version %d", set.SchemaVersion)
	}
	if set.Namespace != namespace {
		return fmt.Errorf("project secret bindings belong to a different project")
	}
	if set.Profile != "" && !validSecretProfile(set.Profile) {
		return fmt.Errorf("project secret bindings: invalid active profile")
	}
	reserved := reservedGuestEnvs()
	byGuestEnv := map[string]string{}
	byIdentity := map[string]bool{}
	for _, b := range set.Bindings {
		if !validSecretProfile(b.Profile) || !validSecretComponent(b.Component) || !validSecretIdentifier(b.Name) {
			return fmt.Errorf("project secret bindings contain an invalid identity")
		}
		if b.GuestEnv == "" || !isGuestEnvName(b.GuestEnv) {
			return fmt.Errorf("project secret binding %s has an invalid guest variable name", b.Name)
		}
		if reserved[b.GuestEnv] {
			return fmt.Errorf("project secret binding %s would shadow the reserved guest variable %s", b.Name, b.GuestEnv)
		}
		if prev, dup := byGuestEnv[b.GuestEnv]; dup {
			return fmt.Errorf("project secret bindings %s and %s share guest variable %s", prev, b.Name, b.GuestEnv)
		}
		byGuestEnv[b.GuestEnv] = b.Name
		if byIdentity[b.identity()] {
			return fmt.Errorf("project secret bindings contain a duplicate entry for %s", b.Name)
		}
		byIdentity[b.identity()] = true
		if len(b.Destinations) == 0 {
			return fmt.Errorf("project secret binding %s needs at least one approved destination", b.Name)
		}
		if len(b.Destinations) > maxProjectSecretDestinations {
			return fmt.Errorf("project secret binding %s has too many destinations", b.Name)
		}
		seen := map[string]bool{}
		for _, host := range b.Destinations {
			if !isDNSName(host) {
				return fmt.Errorf("project secret binding %s: %q is not a DNS name", b.Name, host)
			}
			if seen[host] {
				return fmt.Errorf("project secret binding %s lists %s twice", b.Name, host)
			}
			seen[host] = true
		}
	}
	return nil
}

// isGuestEnvName reports whether name is a usable environment variable
// identifier: letters, digits and underscores, not starting with a digit.
func isGuestEnvName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ReadProjectSecretBindings loads and validates the host-local record. A
// missing file is not an error: it means no project secret is bound yet.
func ReadProjectSecretBindings(fs FS, path, namespace string) (projectSecretBindingSet, error) {
	set := projectSecretBindingSet{SchemaVersion: projectSecretBindingSchemaVersion, Namespace: namespace}
	data, err := fs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return set, nil
		}
		return set, fmt.Errorf("read project secret bindings: %w", err)
	}
	if err := json.Unmarshal(data, &set); err != nil {
		return set, fmt.Errorf("project secret bindings are not valid JSON: %w", err)
	}
	if err := validateProjectSecretBindingSet(set, namespace); err != nil {
		return set, err
	}
	return set, nil
}

// WriteProjectSecretBindings persists the record atomically. The caller owns
// validation; this writer re-checks it so a malformed set is never persisted.
func WriteProjectSecretBindings(fs FS, path, namespace string, set projectSecretBindingSet) error {
	set.SchemaVersion = projectSecretBindingSchemaVersion
	set.Namespace = namespace
	if err := validateProjectSecretBindingSet(set, namespace); err != nil {
		return err
	}
	sort.Slice(set.Bindings, func(i, j int) bool {
		return set.Bindings[i].identity() < set.Bindings[j].identity()
	})
	data, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o600)
}

// ProjectSecretDelivery is one binding resolved against the credential index:
// the approval, the guest variable, the derived placeholder, and the opaque
// store reference that actually holds the value.
type ProjectSecretDelivery struct {
	ProjectSecretBinding
	// Reference is the opaque credential-store entry from the index.
	Reference string
	// Marker is the guest-visible placeholder for this identity. It is
	// computed once here so the environment binding and any generated guest
	// dotenv file expose exactly the same placeholder.
	Marker string
}

// projectSecretDeliveries resolves this project's approved bindings, cheaply:
// it reads only host-local metadata and never touches a credential value, so
// the built-in path pays nothing when a project has no approvals.
type projectSecretResolution struct {
	deliveries  []ProjectSecretDelivery
	undelivered []string
}

// projectSecretStore builds the store for this project's workspace root, or an
// error if the project cannot be scoped.
// projectSecretInventoryStore resolves the store that backs the host-local
// project secret records: the binding approvals and the applied inventory.
// It exists so the two callers can state their intent, while the resolution
// rule stays in one place.
func (m *MicrosandboxRuntime) projectSecretInventoryStore() (*ProjectSecretStore, error) {
	return m.projectSecretStore()
}

func (m *MicrosandboxRuntime) projectSecretStore() (*ProjectSecretStore, error) {
	if m.ProjectSecrets != nil {
		return m.ProjectSecrets, nil
	}
	root := m.cfg.WorkspaceDir
	if root == "" {
		return nil, fmt.Errorf("project secret bindings need a project workspace root")
	}
	store := DefaultCredentialStore()
	if store == nil {
		fileStore, err := NewFileCredentialStore()
		if err != nil {
			return nil, err
		}
		store = fileStore
	}
	return NewProjectSecretStore(root, m.stateDirOrDefault(), store)
}

// projectSecretDeliveries returns the approved bindings for the current
// project, or an empty resolution when this project has none. A project with
// no binding record is not an error: it simply has no project secrets.
func (m *MicrosandboxRuntime) projectSecretDeliveries(ctx context.Context) (projectSecretResolution, error) {
	store, err := m.projectSecretStore()
	if err != nil {
		// A workspace root that is not a resolvable directory means this
		// runtime has no project scope (tests, legacy singletons); that is
		// not a reason to fail a start that has no project secrets.
		return projectSecretResolution{}, nil
	}
	set, err := ReadProjectSecretBindings(DefaultFS, ProjectSecretBindingsPath(m.stateDirOrDefault(), store.Namespace()), store.Namespace())
	if err != nil {
		return projectSecretResolution{}, err
	}
	if len(set.Bindings) == 0 {
		return projectSecretResolution{}, nil
	}
	deliveries, undelivered, err := store.ApprovedDeliveries()
	if err != nil {
		return projectSecretResolution{}, err
	}
	return projectSecretResolution{deliveries: deliveries, undelivered: undelivered}, nil
}

// ApprovedDeliveries resolves this store's approved bindings, or an empty
// result when the project has no approval record. It never touches a
// credential value, so a caller can use it as a cheap "is anything delivered?"
// check. It is the entry point both the runtime and the CLI use, so they can
// never disagree about which store answered.
func (s *ProjectSecretStore) ApprovedDeliveries() ([]ProjectSecretDelivery, []string, error) {
	set, err := ReadProjectSecretBindings(DefaultFS, ProjectSecretBindingsPath(s.stateDir, s.namespace), s.namespace)
	if err != nil {
		return nil, nil, err
	}
	if len(set.Bindings) == 0 {
		return nil, nil, nil
	}
	return s.Deliveries(context.Background())
}

// ProjectSecretDeliveries exposes the approved deliveries for the CLI, so
// `secrets bindings list` and the launch notes report the same set the runtime
// would use.
func ProjectSecretDeliveries(root, stateDir string) ([]ProjectSecretDelivery, []string, error) {
	store, err := projectSecretStoreFor(root, stateDir)
	if err != nil {
		return nil, nil, err
	}
	return store.ApprovedDeliveries()
}

func projectSecretStoreFor(root, stateDir string) (*ProjectSecretStore, error) {
	store := DefaultCredentialStore()
	if store == nil {
		fileStore, err := NewFileCredentialStore()
		if err != nil {
			return nil, err
		}
		store = fileStore
	}
	return NewProjectSecretStore(root, stateDir, store)
}

// ProjectSecretBindingsRecord is the CLI-facing view of the host-local
// approvals: what is approved, for which profile, and what is imported but not
// yet delivered.
type ProjectSecretBindingsRecord struct {
	Namespace   string
	Profile     string
	Bindings    []ProjectSecretBinding
	Undelivered []string
	// Pending marks a resolution failure (a missing entry or a profile
	// mismatch) so the CLI can report it rather than showing an empty list.
	Pending string
}

// ReadProjectSecretBindingsRecord loads the record for display without
// failing on an unresolvable binding: the CLI must be able to show the user
// what is wrong and how to fix it.
func ReadProjectSecretBindingsRecord(root, stateDir string) (ProjectSecretBindingsRecord, error) {
	store, err := projectSecretStoreFor(root, stateDir)
	if err != nil {
		return ProjectSecretBindingsRecord{}, err
	}
	return ReadProjectSecretBindingsRecordFor(store)
}

// ReadProjectSecretBindingsRecordFor is the injectable form: the caller
// supplies the store, so the record is always read through the same backend
// the import wrote to.
func ReadProjectSecretBindingsRecordFor(store *ProjectSecretStore) (ProjectSecretBindingsRecord, error) {
	stateDir := store.stateDir
	record := ProjectSecretBindingsRecord{Namespace: store.Namespace()}
	set, err := ReadProjectSecretBindings(DefaultFS, ProjectSecretBindingsPath(stateDir, store.Namespace()), store.Namespace())
	if err != nil {
		return record, err
	}
	record.Profile = set.Profile
	record.Bindings = set.Bindings
	index, err := store.readIndex()
	if err != nil {
		return record, err
	}
	approved := map[string]bool{}
	for _, b := range set.Bindings {
		approved[b.identity()] = true
	}
	_, undelivered, deliverErr := store.Deliveries(context.Background())
	if deliverErr != nil {
		record.Pending = deliverErr.Error()
	}
	if len(set.Bindings) > 0 && deliverErr == nil {
		record.Undelivered = undelivered
	}
	if len(set.Bindings) == 0 {
		for _, e := range index.Entries {
			record.Undelivered = append(record.Undelivered, e.Component+"#"+e.Name)
		}
		sort.Strings(record.Undelivered)
	}
	return record, nil
}

// SetProjectSecretActiveProfile selects the profile a project delivers, or
// clears the selection. It refuses to clear while bindings remain, because
// that state would fail every start with an unexplained error.
func SetProjectSecretActiveProfile(fs FS, stateDir, namespace, profile string) error {
	path := ProjectSecretBindingsPath(stateDir, namespace)
	set, err := ReadProjectSecretBindings(fs, path, namespace)
	if err != nil {
		return err
	}
	if profile == "" && len(set.Bindings) > 0 {
		return fmt.Errorf("remove the %d approved bindings before clearing the active profile", len(set.Bindings))
	}
	if profile != "" && !validSecretProfile(profile) {
		return fmt.Errorf("profile must use 1–64 ASCII letters, digits, hyphens or underscores")
	}
	set.Profile = profile
	return WriteProjectSecretBindings(fs, path, namespace, set)
}

// ApproveProjectSecretBinding records one binding approval. Approving a
// destination set is the security decision: it is what allows the runtime to
// substitute that value toward those hosts, so it is stored host-locally and
// never in the project.
func ApproveProjectSecretBinding(fs FS, stateDir, namespace string, b ProjectSecretBinding) error {
	path := ProjectSecretBindingsPath(stateDir, namespace)
	set, err := ReadProjectSecretBindings(fs, path, namespace)
	if err != nil {
		return err
	}
	if b.GuestEnv == "" {
		b.GuestEnv = ProjectSecretGuestEnv(b.Component, b.Name)
	}
	replaced := false
	for i, existing := range set.Bindings {
		if existing.identity() == b.identity() {
			set.Bindings[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		set.Bindings = append(set.Bindings, b)
	}
	if set.Profile == "" {
		set.Profile = b.Profile
	}
	return WriteProjectSecretBindings(fs, path, namespace, set)
}

// RevokeProjectSecretBinding removes one approval. It reports whether an
// approval was removed, so the caller can distinguish "revoked" from
// "nothing was approved". The value stays in the credential store: this
// removes the guest access, not the import.
func RevokeProjectSecretBinding(fs FS, stateDir, namespace, profile, component, name string) (bool, error) {
	path := ProjectSecretBindingsPath(stateDir, namespace)
	set, err := ReadProjectSecretBindings(fs, path, namespace)
	if err != nil {
		return false, err
	}
	target := ProjectSecretBinding{Profile: profile, Component: component, Name: name}.identity()
	kept := set.Bindings[:0]
	removed := false
	for _, b := range set.Bindings {
		if b.identity() == target {
			removed = true
			continue
		}
		kept = append(kept, b)
	}
	if !removed {
		return false, nil
	}
	set.Bindings = kept
	return true, WriteProjectSecretBindings(fs, path, namespace, set)
}

// Deliveries resolves the approved bindings of the active profile against the
// credential index. It fails closed in both directions the plan requires:
//
//   - an approved binding whose entry is missing from the index is an error,
//     because the user asked for a delivery just-code cannot perform;
//   - a binding approved for a different profile than the active one is an
//     error too, so a profile switch cannot silently deliver the wrong
//     environment's value.
//
// Entries present in the index but not approved are simply not delivered and
// are reported by the caller, never bound implicitly.
func (s *ProjectSecretStore) Deliveries(ctx context.Context) ([]ProjectSecretDelivery, []string, error) {
	path := ProjectSecretBindingsPath(s.stateDir, s.namespace)
	set, err := ReadProjectSecretBindings(DefaultFS, path, s.namespace)
	if err != nil {
		return nil, nil, err
	}
	index, err := s.readIndex()
	if err != nil {
		return nil, nil, err
	}
	byIdentity := map[string]ProjectSecretEntry{}
	for _, e := range index.Entries {
		byIdentity[e.identity()] = e
	}
	var deliveries []ProjectSecretDelivery
	for _, b := range set.Bindings {
		if set.Profile == "" {
			return nil, nil, fmt.Errorf("project secret bindings exist for %s but no active profile is selected", s.namespace)
		}
		if b.Profile != set.Profile {
			return nil, nil, fmt.Errorf("project secret binding %s belongs to profile %q while the active profile is %q", b.Name, b.Profile, set.Profile)
		}
		entry, ok := byIdentity[b.identity()]
		if !ok {
			return nil, nil, fmt.Errorf("project secret binding %s is approved but no imported value matches it; re-import the component or remove the approval", b.Name)
		}
		deliveries = append(deliveries, ProjectSecretDelivery{
			ProjectSecretBinding: b,
			Reference:            entry.Reference,
			Marker:               ProjectSecretPlaceholder(s.namespace, b.Profile, b.Component, b.Name),
		})
	}
	// Report imported entries the active profile does not deliver, so the
	// omission is visible rather than silent.
	approved := map[string]bool{}
	for _, b := range set.Bindings {
		approved[b.identity()] = true
	}
	var undelivered []string
	for _, e := range index.Entries {
		if e.Profile == set.Profile && approved[e.identity()] {
			continue
		}
		undelivered = append(undelivered, e.Component+"#"+e.Name)
	}
	sort.Strings(undelivered)
	return deliveries, undelivered, nil
}
