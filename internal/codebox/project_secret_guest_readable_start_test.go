package codebox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The composition the start path runs: approvals -> resolved value -> file in
// the guest -> inventory. The earlier phases each had a bug that lived exactly
// here, in the seam between a tested unit and the path that calls it.

func TestStartPathDeliversGuestReadableValue(t *testing.T) {
	m, client := newGuestFSRuntime(t, nil)
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"DB_PASSWORD": "local-secret", "API_KEY": "remote-secret"})
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, s.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "DB_PASSWORD", Delivery: ProjectSecretDeliveryGuest,
	}); err != nil {
		t.Fatal(err)
	}

	// The runtime is invoked the way the start path invokes it, with the
	// bindings it resolved.
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}

	guestPath := msbGuestWorkspace + "/.env"
	content, ok := client.files[guestPath]
	if !ok {
		t.Fatalf("the delivered file is missing: %v", client.files)
	}
	if !strings.Contains(content, "DB_PASSWORD='local-secret'\n") {
		t.Fatalf("the real value must be delivered: %q", content)
	}
	if mode := client.modes[guestPath]; mode != 0o600 {
		t.Fatalf("the delivered file must be 0600, got %v", mode)
	}
	// A guest-readable binding contributes no proxy variable, so the applied
	// inventory must not claim one: that inventory is what a later revocation
	// unregisters.
	applied, err := ReadProjectSecretApplied(DefaultFS, m.StateDir, s.Namespace())
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Envs) != 0 {
		t.Fatalf("no proxy variable may be recorded for a guest-readable value: %v", applied.Envs)
	}
	if len(applied.Files) != 1 || applied.Files[0] != guestPath {
		t.Fatalf("the delivered file must be recorded: %v", applied.Files)
	}

	// Withdrawing the approval removes the delivered file at the next apply.
	if _, err := RevokeProjectSecretBinding(DefaultFS, m.StateDir, s.Namespace(), "development", ".env", "DB_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	bindings, err = m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.syncProjectSecretDelivery(context.Background(), bindings); err != nil {
		t.Fatal(err)
	}
	if _, still := client.files[guestPath]; still {
		t.Fatal("revoking the approval must remove the delivered value")
	}
}

// `codebox start` does not call Start: it reconciles, and a healthy sandbox
// whose VM configuration is unchanged plans a no-op. The approvals are
// host-local and deliberately absent from that revision, so without an explicit
// step here an approval delivers nothing and, worse, a revocation leaves the
// plaintext file in the guest while the CLI says the revocation is handled.
func TestReconcileNoOpAppliesGuestReadableDeliveryAndRevocation(t *testing.T) {
	isolateHostState(t)
	m, client := newGuestFSRuntime(t, nil)
	// The sandbox must already exist and be healthy, or the plan is not a
	// no-op and the test would exercise the create path instead.
	client.fakeMSBClient.exists = true
	client.fakeMSBClient.status = "running"
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: true} }

	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"DB_PASSWORD": "local-secret"})
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, s.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "DB_PASSWORD", Delivery: ProjectSecretDeliveryGuest,
	}); err != nil {
		t.Fatal(err)
	}

	// The persisted state already matches the desired state, so the plan is a
	// no-op — the path a healthy `codebox start` takes.
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, desired.toState()); err != nil {
		t.Fatal(err)
	}

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	guestPath := msbGuestWorkspace + "/.env"
	if _, ok := client.files[guestPath]; !ok {
		t.Fatalf("a no-op reconcile must still deliver an approved guest-readable value: %v", client.calls)
	}

	// The revocation half is the one that matters: what stays behind is the
	// real value, not an inert placeholder.
	if _, err := RevokeProjectSecretBinding(DefaultFS, m.StateDir, s.Namespace(), "development", ".env", "DB_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, still := client.files[guestPath]; still {
		t.Fatal("a no-op reconcile after a revocation must remove the delivered plaintext")
	}
	_ = os.Remove(statePath)
	_ = os.RemoveAll(filepath.Dir(statePath))
}
