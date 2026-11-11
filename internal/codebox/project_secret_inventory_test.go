package codebox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the three defects an independent review found in the
// revocation path (PR #183 review, 2026-10-14). Each one covers a composition
// the original tests missed: they exercised syncGuestSecretFiles in isolation
// and never the start/reconcile path that actually runs in production.

func projectSecretTestRuntime(t *testing.T, client msbClient) *MicrosandboxRuntime {
	t.Helper()
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.Client = client
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	return m
}

func approveForTest(t *testing.T, m *MicrosandboxRuntime, name, guestEnv string) {
	t.Helper()
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: name,
		GuestEnv: guestEnv, Destinations: []string{"api.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
}

func revokeForTest(t *testing.T, m *MicrosandboxRuntime, name string) {
	t.Helper()
	removed, err := RevokeProjectSecretBinding(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), "development", ".env", name)
	if err != nil || !removed {
		t.Fatalf("revoke: %v, %v", removed, err)
	}
}

// Finding 1: the direct Start path (what `restart` runs) applies the binding
// but used to record nothing, so the next removal could not name it.
func TestAppliedInventoryIsRecordedByTheStartPath(t *testing.T) {
	m, client := newGuestFSRuntimeWithStore(t)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"API_KEY": "project-value"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")

	// Direct start, not reconcile: this is the path `restart` takes.
	if err := m.syncProjectSecretDelivery(context.Background(), mustResolveProjectBindings(t, m)); err != nil {
		t.Fatal(err)
	}
	if got := m.appliedProjectBindings(); len(got) != 1 || got[0] != "JC_API_KEY" {
		t.Fatalf("the start path must record the applied guest variable, got %v", got)
	}

	// Revoking and applying again must name the variable for removal.
	revokeForTest(t, m, "API_KEY")
	bindings := mustResolveProjectBindings(t, m)
	if len(bindings) != 1 { // only the required Albert binding remains
		t.Fatalf("the revoked binding must leave the desired set: %+v", bindings)
	}
	options := msbNextStartOptions(m.nextStartEnv(), bindingsMetadata(bindings), m.appliedProjectBindings())
	if !containsString(options.SecretsRemove, "JC_API_KEY") {
		t.Fatalf("a revoked binding must be named in SecretsRemove: %v", options.SecretsRemove)
	}
	// And the managed guest file is removed at the same time.
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.files[msbGuestWorkspace+"/.env"]; ok {
		t.Fatal("the revoked approval's guest file must be removed")
	}
}

// Finding 2: a resumed journal must still name the variables a revocation has
// to unregister. The previous inventory is read from the host-local record at
// the moment the refresh runs, so it cannot be overwritten by the journal.
func TestResumedRefreshStillNamesTheRevokedBinding(t *testing.T) {
	m, _ := newGuestFSRuntimeWithStore(t)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"API_KEY": "project-value"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")
	if err := m.syncProjectSecretDelivery(context.Background(), mustResolveProjectBindings(t, m)); err != nil {
		t.Fatal(err)
	}
	revokeForTest(t, m, "API_KEY")

	// Simulate the resumed apply: the desired set no longer contains the
	// binding, and the inventory is read fresh — it must still name it.
	bindings := mustResolveProjectBindings(t, m)
	options := msbNextStartOptions(m.nextStartEnv(), bindingsMetadata(bindings), m.appliedProjectBindings())
	if !containsString(options.SecretsRemove, "JC_API_KEY") {
		t.Fatalf("a resumed refresh must still name the revoked variable: %v", options.SecretsRemove)
	}
}

// Finding 3: the applied inventory is host-local, so the reconcile journal
// write cannot clobber it. The record must survive a state-file rewrite.
func TestReconcileJournalDoesNotClobberTheAppliedInventory(t *testing.T) {
	m, _ := newGuestFSRuntimeWithStore(t)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"API_KEY": "project-value"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")
	bindings := mustResolveProjectBindings(t, m)
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}
	applied, err := ReadProjectSecretApplied(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace())
	if err != nil || len(applied.Files) != 1 || len(applied.Envs) != 1 {
		t.Fatalf("inventory after the first apply: %+v, %v", applied, err)
	}

	// A reconcile-style state write must not touch the inventory file.
	st := InstanceState{SchemaVersion: instanceStateSchemaVersion, Instance: m.InstanceName()}
	if err := WriteInstanceState(DefaultFS, instanceStatePath(m.StateDir, m.InstanceName()), st); err != nil {
		t.Fatal(err)
	}
	after, err := ReadProjectSecretApplied(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace())
	if err != nil || len(after.Files) != len(applied.Files) || len(after.Envs) != len(applied.Envs) {
		t.Fatalf("the inventory must be independent of the instance state: %+v, %v", after, err)
	}
	if got := m.appliedProjectBindings(); len(got) != 1 || got[0] != "JC_API_KEY" {
		t.Fatalf("applied inventory read back wrong: %v", got)
	}
}

// Finding 4: a configured workspace that cannot be scoped must fail rather
// than resolve to nothing, because an empty set would unregister live
// bindings.
func TestUnresolvableProjectScopeDoesNotSilentlyDropBindings(t *testing.T) {
	m, _ := newGuestFSRuntimeWithStore(t)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"API_KEY": "project-value"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")
	if err := m.syncProjectSecretDelivery(context.Background(), mustResolveProjectBindings(t, m)); err != nil {
		t.Fatal(err)
	}
	// The workspace root disappears: the project scope is configured but
	// cannot be resolved.
	m.ProjectSecrets = nil
	m.cfg.WorkspaceDir = m.cfg.WorkspaceDir + "/gone"
	if err := m.syncProjectSecretDelivery(context.Background(), nil); err == nil {
		t.Fatal("an unresolvable configured project must be an error, not an empty apply")
	}
}

// The published files must survive a second apply unchanged, and the
// inventory must describe exactly what the guest holds.
func TestAppliedInventoryMatchesTheGuest(t *testing.T) {
	m, client := newGuestFSRuntimeWithStore(t)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"KEY_A": "a"})
	importProjectSecrets(t, m.ProjectSecrets, "development", "apps/api/.env", map[string]string{"KEY_B": "b"})
	approveForTest(t, m, "KEY_A", "JC_KEY_A")
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: "apps/api/.env", Name: "KEY_B",
		GuestEnv: "JC_APPS_API_KEY_B", Destinations: []string{"api.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	bindings := mustResolveProjectBindings(t, m)
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}
	applied, err := ReadProjectSecretApplied(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Files) != 2 || len(applied.Envs) != 2 {
		t.Fatalf("inventory: %+v", applied)
	}
	for _, guestPath := range applied.Files {
		if _, ok := client.files[guestPath]; !ok {
			t.Fatalf("recorded file %s is not in the guest", guestPath)
		}
	}
	// A second apply is a no-op and the inventory is unchanged.
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}
	again, err := ReadProjectSecretApplied(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace())
	if err != nil || len(again.Files) != len(applied.Files) || strings.Join(again.Files, ",") != strings.Join(applied.Files, ",") {
		t.Fatalf("inventory changed on a no-op apply: %+v, %v", again, err)
	}
}

// A record from another project, or one naming a path outside the workspace,
// must be refused rather than obeyed.
func TestAppliedInventoryRecordValidation(t *testing.T) {
	state := t.TempDir()
	if err := WriteProjectSecretApplied(DefaultFS, state, "ns", ProjectSecretApplied{
		Envs:  []string{"JC_API_KEY"},
		Files: []string{msbGuestWorkspace + "/.env"},
	}); err != nil {
		t.Fatal(err)
	}
	// A record copied into another namespace's path must be refused, not
	// obeyed: the namespace is checked as well as the location.
	copied, err := ReadProjectSecretApplied(DefaultFS, state, "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(copied.Files) != 1 {
		t.Fatalf("fixture: %+v", copied)
	}
	if err := os.MkdirAll(filepath.Dir(ProjectSecretAppliedPath(state, "other-ns")), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(ProjectSecretAppliedPath(state, "ns"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectSecretAppliedPath(state, "other-ns"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProjectSecretApplied(DefaultFS, state, "other-ns"); err == nil {
		t.Fatal("a foreign inventory must be refused")
	}
	if err := WriteProjectSecretApplied(DefaultFS, state, "ns", ProjectSecretApplied{Files: []string{"/etc/passwd"}}); err == nil {
		t.Fatal("a path outside the guest workspace must be refused")
	}
	if err := WriteProjectSecretApplied(DefaultFS, state, "ns", ProjectSecretApplied{Envs: []string{msbAPISecretEnv}}); err == nil {
		t.Fatal("a reserved guest variable must be refused")
	}
}

func newGuestFSRuntimeWithStore(t *testing.T) (*MicrosandboxRuntime, *guestFSClient) {
	t.Helper()
	m, client := newGuestFSRuntime(t, nil)
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	return m, client
}

func mustResolveProjectBindings(t *testing.T, m *MicrosandboxRuntime) []resolvedBinding {
	t.Helper()
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return bindings
}
