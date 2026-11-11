package codebox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A binding approved for another profile than the active one is a STRUCTURAL
// mismatch: no amount of waiting or retrying repairs it, because the approval
// names a profile the project is no longer using. The no-op start path used to
// tolerate every resolution failure, so `codebox start` on an already
// healthy sandbox exited 0 with a warning while the guest kept serving the
// previous profile's secrets (#204).
//
// The tolerance exists for the opposite case — a transient failure such as a
// locked store or a credential not yet stored — where an instance that only
// needs a restart must not become unusable. Both cases are pinned here so the
// fix cannot quietly drop the other.

// crossProfileRuntime builds a runtime whose sandbox is already healthy (so
// the plan is a no-op) and whose approved binding belongs to a profile other
// than the active one.
func crossProfileRuntime(t *testing.T) (*MicrosandboxRuntime, *fakeMSBClient) {
	t.Helper()
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := projectSecretTestRuntime(t, client)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"DUPE_KEY": "dev-value"})
	// Approve under `development`, then switch the active profile to
	// `staging` — exactly the sequence the qualification protocol ran.
	approveForTest(t, m, "DUPE_KEY", "JC_DUPE_KEY")
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, m.ProjectSecrets.Namespace(), "staging"); err != nil {
		t.Fatal(err)
	}
	// Make the guest genuinely healthy so the plan is a no-op: that is the
	// path the issue reports as exiting 0, not the credential-op path.
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, path, m.desiredState(true, testBindings(m.cfg.APIKey), nil).toState()); err != nil {
		t.Fatal(err)
	}
	return m, client
}

// TestStaleProfileStartFailsInsteadOfServingTheOldProfile pins the required
// behaviour: the mismatch must surface as a failure, not an exit-0 warning.
func TestStaleProfileStartFailsInsteadOfServingTheOldProfile(t *testing.T) {
	m, _ := crossProfileRuntime(t)

	err := m.Reconcile(context.Background())
	if err == nil {
		t.Fatal("a binding approved for another profile must fail the start, not warn and succeed")
	}
	// The message has to name both profiles, or the user cannot tell which
	// approval is stale.
	if !strings.Contains(err.Error(), "development") || !strings.Contains(err.Error(), "staging") {
		t.Fatalf("the refusal must name both the binding's profile and the active one: %v", err)
	}
	if strings.Contains(err.Error(), "reusing the persisted secret references") {
		t.Fatalf("a structural mismatch must not advertise reuse of stale references: %v", err)
	}
}

// TestTransientResolutionFailureStillStartsNoOp guards the tolerance the fix
// must not remove: a locked store, or a credential not stored yet, is
// transient and must still let an otherwise-healthy instance run.
func TestTransientResolutionFailureStillStartsNoOp(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	// The required Albert credential cannot be read: a transient condition —
	// the user simply has not stored it yet.
	m.cfg.APIKey = ""
	m.cfg.CredentialRef = ""
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	// A healthy guest whose recorded state matches the unresolved desired
	// state: the plan is a no-op, which is the path that must stay tolerant.
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, path, m.desiredState(false, nil, nil).toState()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(path)
		_ = os.RemoveAll(filepath.Dir(path))
	}()

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("a transient resolution failure must not make a healthy instance unusable: %v", err)
	}
}

// TestApprovedProjectSecretWithMissingValueFailsTheNoOpStart covers the second
// gap an independent review found, after the classification was inverted. Both
// a credential that was never stored and an approved value whose backing entry
// disappeared report themselves as not-found, but only the first is
// transient. The second is an inconsistent project state — the user imported
// and approved the value, so only re-importing or removing the approval
// repairs it — and tolerating it would let a no-op start exit 0 while the
// guest kept serving the previously delivered secret.
func TestApprovedProjectSecretWithMissingValueFailsTheNoOpStart(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	store := newProjectSecretFakeStore()
	m.ProjectSecrets = projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	importProjectSecrets(t, m.ProjectSecrets, "development", ".env", map[string]string{"DUPE_KEY": "value"})
	approveForTest(t, m, "DUPE_KEY", "JC_DUPE_KEY")

	// The index still names the entry, but the store no longer holds it: the
	// state a lost keychain entry or an out-of-band deletion leaves behind.
	idx, err := m.ProjectSecrets.readIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) == 0 {
		t.Fatal("the fixture must have indexed the imported value, or this test proves nothing")
	}
	for _, e := range idx.Entries {
		delete(store.values, CredentialKind(e.Reference))
	}
	// Sanity: the binding must still resolve through the index, so execution
	// reaches the value read rather than an earlier refusal.
	if _, _, err := m.ProjectSecrets.Deliveries(context.Background()); err != nil {
		t.Fatalf("the binding must still resolve from the index for this test to reach the value read: %v", err)
	}

	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, m.desiredState(true, testBindings(m.cfg.APIKey), nil).toState()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(statePath)
		_ = os.RemoveAll(filepath.Dir(statePath))
	}()

	err = m.Reconcile(context.Background())
	if err == nil {
		t.Fatal("an approved project secret with no imported value must fail the start, not warn and succeed")
	}
	if !errors.Is(err, ErrProjectSecretImportMissing) {
		t.Fatalf("the refusal must be classified structural, not transient: %v", err)
	}
}

// TestCorruptBindingMetadataFailsTheNoOpStart covers the gap an independent
// review found in the first version of this fix. That version listed the
// structural cases explicitly and let everything else fall through to the
// tolerant path, so malformed `bindings.json` — which no retry can repair —
// still produced an exit-0 start. The classification is now inverted: only a
// credential-store fault is tolerated, and anything unrecognised fails closed.
func TestCorruptBindingMetadataFailsTheNoOpStart(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := projectSecretTestRuntime(t, client)
	// A record that parses as JSON but cannot be validated: the same class as
	// an unsupported schema version, and permanent.
	path := ProjectSecretBindingsPath(m.StateDir, m.ProjectSecrets.Namespace())
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":999,"bindings":[{"profile":"development"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, m.desiredState(true, testBindings(m.cfg.APIKey), nil).toState()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(statePath)
		_ = os.RemoveAll(filepath.Dir(statePath))
	}()

	// Sanity: the fixture must actually be rejected by the reader, or this
	// test would pass for the wrong reason on a permissive validator.
	if _, err := ReadProjectSecretBindings(DefaultFS, path, m.ProjectSecrets.Namespace()); err == nil {
		t.Fatal("the fixture must be rejected by ReadProjectSecretBindings for this test to mean anything")
	}

	err := m.Reconcile(context.Background())
	if err == nil {
		t.Fatal("unreadable binding metadata is permanent and must fail the start, not warn and succeed")
	}
}
