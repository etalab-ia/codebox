//go:build linux

package codebox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Secret Service adapter tests (P08) with a fake Runner: they pin the
// argv contract (the secret is never an argument) and the error mapping.
// Real-store tests are separate and availability-gated. The fake runner
// itself lives in credentials_fake_runner_test.go (shared across OSes).

// TestSecretServicePutKeepsValueOutOfArgs pins the transport contract:
// the secret is never in argv; it goes through stdin.
func TestSecretServicePutKeepsValueOutOfArgs(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil
	}}
	s := &SecretServiceStore{Runner: f}
	if err := s.Put(context.Background(), CredentialAlbert, "secret-value-123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "secret-value-123") {
			t.Fatalf("secret leaked into argv: %s", c)
		}
	}
	if !hasStoreCall(f, "secret-tool store --label codebox/albert service codebox/albert") {
		t.Fatalf("unexpected calls: %v", f.calls)
	}
}

func TestSecretServiceLockedCollectionIsLocked(t *testing.T) {
	// A declined unlock prompt is a locked collection, not an access
	// denial: the recovery differs (unlock the keyring vs re-grant).
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "secret-tool: Failed to unlock the collection\n"}, nil
	}}
	s := &SecretServiceStore{Runner: f}
	_, err := s.Get(context.Background(), CredentialAlbert)
	var se *StoreError
	if !errors.As(err, &se) || se.State != "locked" {
		t.Fatalf("want locked, got %v", err)
	}
}

// TestSecretServiceGetMapsNotFound verifies a missing item (nonzero
// exit, empty stderr) reports ErrCredentialNotFound.
func TestSecretServiceGetMapsNotFound(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 1}, nil
	}}
	s := &SecretServiceStore{Runner: f}
	if _, err := s.Get(context.Background(), CredentialAlbert); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Get error = %v, want ErrCredentialNotFound", err)
	}
}

// TestSecretServiceGetMapsDeniedToStoreError verifies an access denial
// (nonzero exit, stderr present, not a locked-collection wording)
// reports a denied StoreError.
func TestSecretServiceGetMapsDeniedToStoreError(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 1, Stderr: "secret-tool: operation not permitted"}, nil
	}}
	s := &SecretServiceStore{Runner: f}
	_, err := s.Get(context.Background(), CredentialAlbert)
	var se *StoreError
	if !errors.As(err, &se) || se.State != "denied" {
		t.Fatalf("Get error = %v, want denied StoreError", err)
	}
}

// TestSecretServiceMissingToolReportsUnavailable verifies a missing
// secret-tool binary reports unavailable, not denied.
func TestSecretServiceMissingToolReportsUnavailable(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{}, errors.New("exec: not found")
	}}
	s := &SecretServiceStore{Runner: f}
	_, err := s.Get(context.Background(), CredentialAlbert)
	var se *StoreError
	if !errors.As(err, &se) || se.State != "unavailable" {
		t.Fatalf("Get error = %v, want unavailable StoreError", err)
	}
}

// TestSecretServiceVerifyReachable verifies the probe: a sentinel
// lookup that answers not-found proves the service is healthy.
func TestSecretServiceVerifyReachable(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 1}, nil
	}}
	s := &SecretServiceStore{Runner: f}
	if err := s.Verify(context.Background()); err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if !hasStoreCall(f, "secret-tool lookup service codebox/__verify__") {
		t.Fatalf("Verify must probe a sentinel, got: %v", f.calls)
	}
}
