package codebox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The revocation tests pin issue #74's removal semantics: a removed key must
// stop working — live for a running sandbox, persisted for a stopped one —
// and a plaintext transport (Tart/agent-vm) blocks the removal outright.

func revokerForTest(client *fakeMSBClient, stateDir string, tartVMs, agentVMs []string) Revoker {
	return Revoker{
		MSB:            client,
		TartRunning:    func(context.Context) ([]string, error) { return tartVMs, nil },
		AgentVMRunning: func(context.Context) ([]string, error) { return agentVMs, nil },
		StateDir:       stateDir,
		FS:             DefaultFS,
	}
}

// writeBoundState persists a reconcile record marking kind as store-bound for
// instance under stateDir.
func writeBoundState(t *testing.T, stateDir, instance string, bound []string) {
	t.Helper()
	st := InstanceState{
		Instance:         instance,
		BoundCredentials: bound,
	}
	if err := WriteInstanceState(DefaultFS, instanceStatePath(stateDir, instance), st); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeBlockedByPlaintextRuntimes(t *testing.T) {
	client := &fakeMSBClient{}
	r := revokerForTest(client, t.TempDir(), []string{"opencode-proj-a1"}, nil)
	_, err := r.Revoke(context.Background(), "albert")
	var blocked *ErrRevocationBlocked
	if !errors.As(err, &blocked) {
		t.Fatalf("a running tart instance must block albert removal: %v", err)
	}
	if !reflect.DeepEqual(blocked.Instances, []string{"opencode-proj-a1"}) {
		t.Fatalf("blocked instances = %v", blocked.Instances)
	}
	// Enumeration is read-only; what must not happen is a mutation.
	for _, call := range client.calls {
		if strings.HasPrefix(call, "remove-secrets") {
			t.Fatalf("no sandbox may be mutated once removal is blocked: %v", client.calls)
		}
	}
}

func TestRevokeOptionalKindIgnoresPlaintextRuntimes(t *testing.T) {
	// Tart/agent-vm only ever transport albert; a github removal must not be
	// blocked by a running tart VM.
	client := &fakeMSBClient{}
	r := revokerForTest(client, t.TempDir(), []string{"opencode-proj-a1"}, nil)
	if _, err := r.Revoke(context.Background(), "github"); err != nil {
		t.Fatalf("github removal must not be blocked by tart: %v", err)
	}
}

func TestRevokeLiveOnRunningPersistedOnStopped(t *testing.T) {
	stateDir := t.TempDir()
	writeBoundState(t, stateDir, "cb-running", []string{"albert"})
	writeBoundState(t, stateDir, "cb-stopped", []string{"albert"})
	client := &fakeMSBClient{
		listed:        []string{"cb-running", "cb-stopped"},
		listedRunning: map[string]bool{"cb-running": true},
	}
	r := revokerForTest(client, stateDir, nil, nil)
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.LiveRevoked, []string{"cb-running"}) {
		t.Fatalf("live revoked = %v", rep.LiveRevoked)
	}
	if !reflect.DeepEqual(rep.StoppedCleared, []string{"cb-stopped"}) {
		t.Fatalf("stopped cleared = %v", rep.StoppedCleared)
	}
	if !rep.PlaceholderDangles {
		t.Fatal("a live revocation must flag the dangling placeholder")
	}
	if !hasCall(client, "remove-secrets cb-running") || !hasCall(client, "remove-secrets cb-stopped") {
		t.Fatalf("both instances must be revoked: %v", client.calls)
	}
}

func TestRevokeSkipsEnvSourcedInstances(t *testing.T) {
	// The persisted BoundCredentials record is the authority: an instance
	// whose credential came from the environment is not codebox's to
	// revoke, and touching it would be wrong.
	stateDir := t.TempDir()
	writeBoundState(t, stateDir, "cb-env", nil) // env-sourced: no store-bound kinds
	writeBoundState(t, stateDir, "cb-store", []string{"albert"})
	client := &fakeMSBClient{listed: []string{"cb-env", "cb-store"}}
	r := revokerForTest(client, stateDir, nil, nil)
	// The env-sourced instance still resolves from the environment, which is
	// what distinguishes it from an instance whose source is unknown.
	r.Config = Config{APIKey: "env-key"}
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.SkippedEnv, []string{"cb-env"}) {
		t.Fatalf("env-sourced instance must be skipped: %v", rep.SkippedEnv)
	}
	if hasCall(client, "remove-secrets cb-env") {
		t.Fatalf("env-sourced instance was touched: %v", client.calls)
	}
}

// TestRevokeScopedToTheEditedStore pins the Codex P2 on PR #87: the native
// and fallback stores can both hold the same kind, so removing one must not
// revoke instances bound to the other.
func TestRevokeScopedToTheEditedStore(t *testing.T) {
	stateDir := t.TempDir()
	writeBoundState(t, stateDir, "cb-native", []string{"albert@native#albert"})
	writeBoundState(t, stateDir, "cb-file", []string{"albert@file#albert"})
	client := &fakeMSBClient{listed: []string{"cb-native", "cb-file"}}

	// Removing the fallback entry revokes only the fallback-bound instance.
	r := revokerForTest(client, stateDir, nil, nil)
	r.Store = "file"
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.StoppedCleared, []string{"cb-file"}) {
		t.Fatalf("only the fallback-bound instance may be revoked: %+v", rep)
	}
	if hasCall(client, "remove-secrets cb-native") {
		t.Fatalf("the native-bound instance was revoked by a fallback edit: %v", client.calls)
	}
	if !reflect.DeepEqual(rep.SkippedEnv, []string{"cb-native"}) {
		t.Fatalf("the other-store instance must be reported as skipped: %+v", rep)
	}
}

// TestRevokeTreatsPreStoreRecordAsBound pins the migration direction: a
// BoundCredentials entry written before the store was recorded ("albert")
// names no store, so revoking is the safe reading — skipping it could leave
// a live binding behind.
func TestRevokeTreatsPreStoreRecordAsBound(t *testing.T) {
	stateDir := t.TempDir()
	writeBoundState(t, stateDir, "cb-old", []string{"albert"}) // legacy format
	client := &fakeMSBClient{listed: []string{"cb-old"}}
	r := revokerForTest(client, stateDir, nil, nil)
	r.Store = "file"
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.StoppedCleared, []string{"cb-old"}) {
		t.Fatalf("a store-less record must be revoked, not skipped: %+v", rep)
	}
}

// TestRevokeReportsPendingWhenSourceUnknown pins the honesty requirement: an
// instance whose source cannot be confirmed is revoked (never skipped) but
// reported, so a removal that could not verify what it revoked does not
// silently claim success.
func TestRevokeReportsPendingWhenSourceUnknown(t *testing.T) {
	stateDir := t.TempDir()
	// No state file at all and a credential that does not resolve: neither
	// the record nor re-resolution can answer.
	client := &fakeMSBClient{listed: []string{"cb-mystery"}}
	r := revokerForTest(client, stateDir, nil, nil)
	r.Config = Config{} // no env key, no credentialRef, no store entry
	r.ReadCredential = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.Pending, []string{"cb-mystery"}) {
		t.Fatalf("an unconfirmable source must be reported as pending: %+v", rep)
	}
	if hasCall(client, "remove-secrets cb-mystery") == false {
		t.Fatalf("an unconfirmable instance is still revoked: %v", client.calls)
	}
	if len(rep.SkippedEnv) != 0 {
		t.Fatalf("an unconfirmable instance must not be skipped: %+v", rep)
	}
}

// TestRevokeCredentialRefEntryDropsItsGuestBinding pins the Codex P1 on the
// second review: an Albert binding fed by `credentialRef: "github"` is guest
// binding ALBERT_API_KEY resolved from store entry "github". Removing the
// entry must drop ALBERT_API_KEY — not GITHUB_TOKEN, which is a different
// binding — and must still hit the plaintext-runtime guard.
func TestRevokeCredentialRefEntryDropsItsGuestBinding(t *testing.T) {
	stateDir := t.TempDir()
	// The persisted record names both the entry and the guest binding it fed.
	writeBoundState(t, stateDir, "cb-ref", []string{"github@native#albert"})
	client := &fakeMSBClient{listed: []string{"cb-ref"}}
	r := revokerForTest(client, stateDir, nil, nil)
	rep, err := r.Revoke(context.Background(), "github")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.removedSecrets, []string{msbAPISecretEnv}) {
		t.Fatalf("the entry's guest binding (ALBERT_API_KEY) must be dropped, got %v", client.removedSecrets)
	}
	if !reflect.DeepEqual(rep.Revolved, []string{msbAPISecretEnv}) {
		t.Fatalf("the report must name the guest binding removed: %+v", rep)
	}

	// The plaintext guard keys on the guest binding, not the entry name: a
	// running Tart VM holds ALBERT_API_KEY in cleartext regardless of which
	// store entry resolved it.
	blocked := revokerForTest(&fakeMSBClient{listed: []string{"cb-ref"}}, stateDir, []string{"opencode-vm"}, nil)
	blocked.Config = Config{CredentialRef: "github"}
	if _, err := blocked.Revoke(context.Background(), "github"); err == nil {
		t.Fatal("removing a credentialRef that feeds the Albert binding must be blocked by a running plaintext runtime")
	}
}

// TestRevokeUnknownEntryTouchesNoSandbox is the other half: an entry that
// names nothing codebox ever proxied has no guest binding to drop, so
// deleting it from the store is the whole revocation.
func TestRevokeUnknownEntryTouchesNoSandbox(t *testing.T) {
	client := &fakeMSBClient{listed: []string{"cb-ref"}}
	r := revokerForTest(client, t.TempDir(), nil, nil)
	rep, err := r.Revoke(context.Background(), "some-other-secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Revolved) != 0 || hasCall(client, "remove-secrets") {
		t.Fatalf("an unproxied entry must touch no sandbox: %+v", rep)
	}
}

func TestRevokeTreatsMissingStateAsBound(t *testing.T) {
	// An instance that predates state tracking (or lost it) may hold a
	// store-sourced binding; skipping revocation on a guess could leave it
	// live, so the safe direction is to revoke.
	client := &fakeMSBClient{listed: []string{"cb-legacy"}}
	r := revokerForTest(client, t.TempDir(), nil, nil)
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.StoppedCleared, []string{"cb-legacy"}) {
		t.Fatalf("legacy instance must be revoked: %+v", rep)
	}
}

func TestRevokeEnumeratesLegacySingleton(t *testing.T) {
	// The legacy singleton predates the ownership label and is invisible to
	// List; it must still be revoked (lookup-driven).
	client := &fakeMSBClient{exists: true, status: "running"}
	r := revokerForTest(client, t.TempDir(), nil, nil)
	rep, err := r.Revoke(context.Background(), "albert")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rep.LiveRevoked, []string{msbSandbox}) {
		t.Fatalf("legacy singleton must be revoked live: %+v", rep)
	}
}

func TestRemoveSecretsOptionsPolicies(t *testing.T) {
	live := msbRemoveSecretsOptions([]string{"ALBERT_API_KEY"}, true)
	if live.Policy != "no_restart" || len(live.EnvRemove) != 0 {
		t.Fatalf("live removal must be NoRestart and leave the guest env alone: %+v", live)
	}
	stopped := msbRemoveSecretsOptions([]string{"ALBERT_API_KEY"}, false)
	if stopped.Policy != "next_start" || !reflect.DeepEqual(stopped.EnvRemove, []string{"ALBERT_API_KEY"}) {
		t.Fatalf("stopped removal must persist and scrub the guest env: %+v", stopped)
	}
}

func TestRotateLiveOptionsCarryNoValues(t *testing.T) {
	bindings := bindingsMetadata(testBindings("secret-value"))
	opts := msbRotateLiveOptions(bindings)
	spec := opts.Secrets[msbAPISecretEnv]
	if spec.Value != "" || spec.Env == "" {
		t.Fatalf("rotation must be an env reference, never a value: %+v", spec)
	}
	if opts.Policy != "no_restart" {
		t.Fatalf("rotation must apply live: %+v", opts.Policy)
	}
}

// TestRecreateClearsStateDir covers the leftover-state half of revocation:
// after a recreate, no stale persisted proxy state may keep a removed key
// working — Recreate removes the instance entirely and drops the journal.
func TestRecreateClearsBindingApprovals(t *testing.T) {
	// Binding approvals are host-local and survive a recreate by design
	// (they are a trust decision, not instance state). The reconcile journal
	// is dropped (covered by TestMicrosandboxRecreateRebuildsAndDropsJournal).
	// This test pins that approvals are keyed by instance name, so a
	// recreated instance with the same name keeps its approvals.
	dir := t.TempDir()
	path := bindingApprovalsPath(dir, "cb-x")
	if err := ApproveBinding(DefaultFS, path, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	a, err := ReadBindingApprovals(DefaultFS, path)
	if err != nil || !a.Approves(CredentialGithub) {
		t.Fatalf("approvals must be keyed by instance: %+v, %v", a, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
}

// TestRevokeRemoveAllFedBindings pins the Codex P1 on the third review: a
// credentialRef plus an approved optional binding feed two guest bindings from
// one entry, and removing the entry must remove both registrations, not only
// the first recorded.
func TestRevokeRemoveAllFedBindings(t *testing.T) {
	stateDir := t.TempDir()
	writeBoundState(t, stateDir, "cb-both", []string{"github@native#albert", "github@native#github"})
	client := &fakeMSBClient{listed: []string{"cb-both"}, status: "stopped"}
	r := revokerForTest(client, stateDir, nil, nil)
	rep, err := r.Revoke(context.Background(), "github")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.removedSecrets, []string{"ALBERT_API_KEY", "GITHUB_TOKEN"}) {
		t.Fatalf("both fed bindings must be removed: %v", client.removedSecrets)
	}
	if len(rep.Revolved) != 2 {
		t.Fatalf("the report must name both: %+v", rep)
	}
}

// TestRevokeBlockedByOtherProjectsVMRecord pins the fourth review's P1: a
// running Tart VM in ANOTHER project, whose persisted record shows the
// removed entry feeding its Albert binding, must block the removal even
// though the caller's own reference names nothing relevant. The VM name is
// the instance name (opencode-<instance>), so the state file applies.
func TestRevokeBlockedByOtherProjectsVMRecord(t *testing.T) {
	stateDir := t.TempDir()
	// Project B's instance: credentialRef "github" feeds its Albert binding.
	writeBoundState(t, stateDir, "cb-proj-b1", []string{"github@native#albert"})
	// Caller (project A) has no credentialRef at all.
	r := revokerForTest(&fakeMSBClient{}, stateDir, []string{"opencode-cb-proj-b1"}, nil)
	if _, err := r.Revoke(context.Background(), "github"); err == nil {
		t.Fatal("removing an entry that feeds another project's plaintext VM Albert binding must be refused")
	}
}

// TestRevokeOptionalKindUnblockedByUnrelatedVMRecord is the inverse: a
// running VM whose record does NOT involve the entry must not block a
// github removal through the per-VM inspection (the blanket rule only
// covers the Albert binding).
func TestRevokeOptionalKindUnblockedByUnrelatedVMRecord(t *testing.T) {
	stateDir := t.TempDir()
	// The VM's record binds albert only; the entry "github" feeds nothing
	// there.
	writeBoundState(t, stateDir, "cb-proj-b1", []string{"albert@native#albert"})
	r := revokerForTest(&fakeMSBClient{}, stateDir, []string{"opencode-cb-proj-b1"}, nil)
	// Note: the VM runs with a plaintext ALBERT credential; removing the
	// "github" entry does not threaten it.
	rep, err := r.Revoke(context.Background(), "github")
	if err != nil {
		t.Fatalf("an unrelated VM record must not block: %v", err)
	}
	_ = rep
}
