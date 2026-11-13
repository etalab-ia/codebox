//go:build linux || darwin || windows

package codebox

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// Real-store tests (P08 acceptance: put/get/remove on each supported OS
// family). They run only when the native store is actually available on
// the machine: a developer Mac exercises the Keychain, a desktop Linux
// with a session Secret Service exercises secret-tool, and Windows
// exercises Credential Manager through PowerShell. CI containers have no
// such service, so the tests skip there — the fake-runner tests pin the
// adapter contracts, these pin the real store behavior.
//
// Windows is included deliberately: leaving it out made the qualification
// procedure in tests/integration/credentials/evidence/README.md
// impossible to execute on Windows at all, because the test names it asks
// for did not exist in a Windows build. Adding a real-store test here is
// what makes that platform qualifiable rather than merely documented.

// realStoreAvailable reports whether the platform's native store tool
// is present. Availability of the tool does not prove the service is
// running; the first operation still maps service failures to
// StoreError states.
func realStoreAvailable(t *testing.T) CredentialStore {
	t.Helper()
	s := DefaultCredentialStore()
	if s == nil {
		t.Skip("no native credential store adapter on this platform")
	}
	tool := "secret-tool"
	switch s.Kind() {
	case "keychain":
		tool = "security"
	case "credential-manager":
		// The Windows adapter drives Credential Manager through
		// PowerShell, so the executable whose presence matters is
		// powershell, not a store-specific CLI (there is none).
		tool = "powershell"
	}
	if _, err := exec.LookPath(tool); err != nil {
		t.Skipf("%s not installed; native store unavailable", tool)
	}
	// Probe the service itself: on a headless CI container secret-tool
	// exists but no D-Bus session answers, and the Keychain probe fails
	// when no user session keychain exists.
	if err := s.Verify(context.Background()); err != nil {
		t.Skipf("native store not usable here: %v", err)
	}
	return s
}

// TestRealStorePutGetRemoveRoundTrip exercises the full lifecycle against
// the real native store. The test credential is a synthetic marker value,
// never a real key.
func TestRealStorePutGetRemoveRoundTrip(t *testing.T) {
	s := realStoreAvailable(t)
	ctx := context.Background()
	kind := CredentialKind("p08-selftest")
	t.Cleanup(func() {
		_ = s.Remove(ctx, kind)
	})
	if err := s.Put(ctx, kind, "p08-roundtrip-marker"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	v, err := s.Get(ctx, kind)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "p08-roundtrip-marker" {
		t.Fatalf("Get = %q, want the stored marker", v)
	}
	// Rotation: a second Put replaces.
	if err := s.Put(ctx, kind, "p08-rotated-marker"); err != nil {
		t.Fatalf("Put rotate: %v", err)
	}
	if v, err := s.Get(ctx, kind); err != nil || v != "p08-rotated-marker" {
		t.Fatalf("Get after rotate = %q, %v", v, err)
	}
	if err := s.Remove(ctx, kind); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.Get(ctx, kind); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Get after remove = %v, want ErrCredentialNotFound", err)
	}
}

// TestRealStoreRemoveAbsentIsNotFound verifies the absent-credential
// removal maps to not-found, not a store failure.
func TestRealStoreRemoveAbsentIsNotFound(t *testing.T) {
	s := realStoreAvailable(t)
	err := s.Remove(context.Background(), CredentialKind("p08-absent"))
	if !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Remove absent = %v, want ErrCredentialNotFound", err)
	}
}
