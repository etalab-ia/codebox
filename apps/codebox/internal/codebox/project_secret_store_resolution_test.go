package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubKindStore is a CredentialStore whose only interesting property is the
// Kind() it reports, which is what store selection keys on.
type stubKindStore struct{ kind string }

func (s *stubKindStore) Kind() string { return s.kind }
func (s *stubKindStore) Put(context.Context, CredentialKind, string) error {
	return nil
}
func (s *stubKindStore) Get(context.Context, CredentialKind) (string, error) {
	return "", ErrCredentialNotFound
}
func (s *stubKindStore) Remove(context.Context, CredentialKind) error { return nil }
func (s *stubKindStore) Verify(context.Context) error                 { return nil }
func (s *stubKindStore) Generation(context.Context, CredentialKind) (string, error) {
	return "", nil
}

// writeRecordedStoreKind writes an index.json recording the given store kind,
// exactly as an import would leave behind.
func writeRecordedStoreKind(t *testing.T, root, state, kind string) string {
	t.Helper()
	_, namespace, err := projectSecretScope(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "project-secrets", namespace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	idx := projectSecretIndex{
		SchemaVersion: 1,
		Namespace:     namespace,
		Store:         kind,
		Generation:    strings.Repeat("a", 32),
	}
	raw, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return namespace
}

// TestRecordedProjectSecretStoreKindReadsTheIndex is the cheap half of the
// fix: the store kind an import recorded must be readable without a store.
func TestRecordedProjectSecretStoreKindReadsTheIndex(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	namespace := writeRecordedStoreKind(t, root, state, "file")
	if got := RecordedProjectSecretStoreKind(state, namespace); got != "file" {
		t.Fatalf("RecordedProjectSecretStoreKind = %q, want %q", got, "file")
	}
}

// TestRecordedProjectSecretStoreKindAbsent covers the no-index case: a project
// that never imported anything records nothing, and selection falls back to
// the platform default rather than inventing a store kind.
func TestRecordedProjectSecretStoreKindAbsent(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	_, namespace, err := projectSecretScope(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := RecordedProjectSecretStoreKind(state, namespace); got != "" {
		t.Fatalf("RecordedProjectSecretStoreKind = %q, want empty", got)
	}
}

// TestProjectSecretStoreForHonorsRecordedFileStore is the #199 regression.
//
// On a host where the values were imported into the consented file store, the
// runtime's project secret store must be built on that store. Selection used to
// come from a fresh DefaultCredentialStore() probe, which on Linux always
// returns the Secret Service adapter — so the file store was never consulted
// and every start failed with a spurious index/store mismatch.
func TestProjectSecretStoreForHonorsRecordedFileStore(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	writeRecordedStoreKind(t, root, state, "file")

	store, err := projectSecretStoreFor(root, state)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Kind(); got != "file" {
		t.Fatalf("project secret store kind = %q, want %q (recorded kind was ignored)", got, "file")
	}
}

// TestPreferRecordedCredentialStoreKeepsMatchingStore pins the no-op path: a
// store that already matches the record is returned unchanged, so nothing is
// rebuilt when the flag and the record agree.
func TestPreferRecordedCredentialStoreKeepsMatchingStore(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	writeRecordedStoreKind(t, root, state, "matching-kind")
	active := &stubKindStore{kind: "matching-kind"}

	got, err := PreferRecordedCredentialStore(active, root, state)
	if err != nil {
		t.Fatal(err)
	}
	if got != CredentialStore(active) {
		t.Fatalf("matching store was replaced: got kind %q", got.Kind())
	}
}

// TestPreferRecordedCredentialStoreSwapsMismatchedStore pins the swap: a store
// that does not match the record loses to the recorded kind, which is what
// makes a --fallback-less `secrets bindings list` work on a headless host.
func TestPreferRecordedCredentialStoreSwapsMismatchedStore(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	writeRecordedStoreKind(t, root, state, "file")
	active := &stubKindStore{kind: "secret-service"}

	got, err := PreferRecordedCredentialStore(active, root, state)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind() != "file" {
		t.Fatalf("kind = %q, want file (recorded kind must win over the probe)", got.Kind())
	}
}

// TestPreferRecordedCredentialStoreResolvesWithNoActiveStore covers the
// no-native-adapter case: with no active store and no record, the platform
// default is resolved (or a clear error is returned), never a nil store.
func TestPreferRecordedCredentialStoreResolvesWithNoActiveStore(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	got, err := PreferRecordedCredentialStore(nil, root, state)
	if err != nil {
		// A platform with no native adapter and no record is a legitimate
		// error; it must name the recovery rather than fail obscurely.
		if !strings.Contains(err.Error(), "--fallback") {
			t.Fatalf("error must name the recovery path: %v", err)
		}
		return
	}
	want := "secret-service"
	switch runtime.GOOS {
	case "darwin":
		want = "keychain"
	case "windows":
		want = "credential-manager"
	}
	if got.Kind() != want {
		t.Fatalf("kind = %q, want %q", got.Kind(), want)
	}
}

// TestCredentialStoreForKindAcceptsNativeAdapterKinds is the regression for a
// bug introduced by the first version of this change: an import without
// --fallback records the adapter's own Kind() ("secret-service", "keychain",
// "credential-manager"), never the "native" alias. Rejecting those broke every
// normal native-backed import.
//
// This asserts against storeAvailability() rather than a literal so it holds
// on every platform the tests can run on, and it constructs the store's kind
// the same way the index does — from the adapter, not from the alias.
func TestCredentialStoreForKindAcceptsNativeAdapterKinds(t *testing.T) {
	native := storeAvailability()
	if native == "none" {
		t.Skipf("no native credential adapter on this platform")
	}
	store, err := credentialStoreForKind(native)
	if err != nil {
		t.Fatalf("credentialStoreForKind(%q) failed: %v", native, err)
	}
	if store.Kind() != native {
		t.Fatalf("kind = %q, want %q", store.Kind(), native)
	}
	// The alias the CLI uses as an input convention must keep working too.
	if _, err := credentialStoreForKind("native"); err != nil {
		t.Fatalf("the native alias must resolve: %v", err)
	}
}

// TestCredentialStoreForKindRejectsForeignKind pins the fail-closed branch: a
// store kind this host does not provide must error with the host's own kind
// named, not silently read somewhere else.
func TestCredentialStoreForKindRejectsForeignKind(t *testing.T) {
	if _, err := credentialStoreForKind("gpg-agent"); err == nil {
		t.Fatal("a foreign recorded store kind must be refused")
	}
}

// TestNativeImportStaysResolvable is the end-to-end shape of the Codex
// finding: a project whose index records the native adapter kind must still
// reopen its store. It writes the index the way a real native import does —
// with the adapter's Kind(), which is what made the first attempt wrong.
func TestNativeImportStaysResolvable(t *testing.T) {
	native := storeAvailability()
	if native == "none" {
		t.Skipf("no native credential adapter on this platform")
	}
	root, state := t.TempDir(), t.TempDir()
	writeRecordedStoreKind(t, root, state, native)

	store, err := projectSecretStoreFor(root, state)
	if err != nil {
		t.Fatalf("a native-backed project must still resolve: %v", err)
	}
	if store.Kind() != native {
		t.Fatalf("kind = %q, want %q", store.Kind(), native)
	}
}

// TestProjectSecretDeliveriesPropagatesSelectionFailure pins the P2: a store
// this host cannot build must surface from the preflight rather than read as
// "no project secrets". Swallowing it let Start and Restart pass the credential
// preflight with an empty binding set, and Restart could stop a usable sandbox
// before the same error appeared later in the start.
func TestProjectSecretDeliveriesPropagatesSelectionFailure(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	root, state := t.TempDir(), t.TempDir()
	m.cfg.WorkspaceDir = root
	m.StateDir = state
	// Record a store kind this host does not provide, so selection fails.
	foreign := "gpg-agent"
	if storeAvailability() == foreign {
		t.Skipf("test needs a kind this host does not provide")
	}
	writeRecordedStoreKind(t, root, state, foreign)

	if _, err := m.projectSecretDeliveries(context.Background()); err == nil {
		t.Fatal("a store-selection failure must propagate, not read as no secrets")
	}
}

// TestProjectSecretDeliveriesToleratesAnAbsentProjectScope pins the one case
// that must stay silent: a runtime with no project scope has no project
// secrets, so a default workspace that does not exist yet must not fail the
// preflight (Restart creates it).
func TestProjectSecretDeliveriesToleratesAnAbsentProjectScope(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.WorkspaceDir = ""
	if _, err := m.projectSecretDeliveries(context.Background()); err != nil {
		t.Fatalf("no project scope must not be an error: %v", err)
	}
}

// TestProjectSecretScopeMarksAnAbsentRootAsNoScope is the unit-level half:
// the sentinel must be wrapped, not returned raw, so callers can use errors.Is
// while the message still names the underlying cause.
func TestProjectSecretScopeMarksAnAbsentRootAsNoScope(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "workspace")
	_, _, err := projectSecretScope(missing)
	if err == nil {
		t.Fatal("an absent root must not resolve a scope")
	}
	if !errors.Is(err, ErrProjectSecretNoScope) {
		t.Fatalf("error must wrap ErrProjectSecretNoScope, got: %v", err)
	}
}

// TestRecordedCredentialStoreDistinguishesNoRecordFromAStore pins the helper a
// caller needs when its explicitly requested store could not be built: "no
// record" must be distinguishable from "a store to use", so the caller can
// preserve its own failure instead of being silently redirected.
func TestRecordedCredentialStoreDistinguishesNoRecordFromAStore(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()

	// No record: nil, nil — never a default store.
	store, err := RecordedCredentialStore(root, state)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatalf("no record must yield no store, got kind %q", store.Kind())
	}

	// With a record: the recorded kind, even if it matches the platform's own.
	native := storeAvailability()
	if native == "none" {
		t.Skipf("no native credential adapter on this platform")
	}
	writeRecordedStoreKind(t, root, state, native)
	store, err = RecordedCredentialStore(root, state)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil || store.Kind() != native {
		t.Fatalf("recorded store must resolve, got %v", store)
	}
}

// TestPreferRecordedCredentialStoreKeepsActiveWhenRecordMatches is the
// regression for comparing kinds by building the recorded store first: that
// made a matching active store fail on a host where the kind is a test double
// the resolver cannot construct. The active store must win on a kind match,
// whatever its provenance.
func TestPreferRecordedCredentialStoreKeepsActiveWhenRecordMatches(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	writeRecordedStoreKind(t, root, state, "test")
	active := &stubKindStore{kind: "test"}

	got, err := PreferRecordedCredentialStore(active, root, state)
	if err != nil {
		t.Fatalf("a matching active store must be kept without error: %v", err)
	}
	if got != CredentialStore(active) {
		t.Fatalf("matching store was replaced: got kind %q", got.Kind())
	}
}

// TestRestartRechecksProjectSecretsAfterCreatingWorkspace pins the second P2:
// a workspace deleted while its sandbox and project-secret state survived is
// not "no project scope". Restart must not stop a usable sandbox before the
// recorded store is checked.
//
// The assertion is on the stop call, not on the error: Start reports the same
// failure either way, so returning an error proves nothing. What the fix buys
// is that the sandbox is never taken down.
func TestRestartRechecksProjectSecretsAfterCreatingWorkspace(t *testing.T) {
	// The sandbox must be running: a stopped one makes Stop a no-op that
	// records nothing, so the test could not tell whether it was taken down.
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	root := filepath.Join(t.TempDir(), "missing", "workspace")
	m.cfg.WorkspaceDir = root
	// The state dir already records a store this host cannot build, exactly as
	// a project whose secrets were imported elsewhere would.
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "gpg-agent"
	if storeAvailability() == foreign {
		t.Skipf("test needs a kind this host does not provide")
	}
	writeRecordedStoreKind(t, root, m.StateDir, foreign)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}

	if err := m.Restart(context.Background()); err == nil {
		t.Fatal("Restart must report the unresolvable recorded store")
	}
	if hasCall(client, "stop") {
		t.Fatalf("Restart stopped a usable sandbox before reporting the credential failure; calls: %v", client.calls)
	}
}
