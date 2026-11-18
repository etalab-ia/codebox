//go:build darwin

package codebox

import (
	"context"
	"fmt"
)

// Stubs for the native adapters that are not compiled on darwin (the
// Keychain adapter, in credentials_keychain.go, is the real one here).
// They satisfy the CredentialStore interface so
// DefaultCredentialStore's references resolve, but every operation
// reports the store as unavailable: the platform cannot use it, and the
// CLI must not pretend otherwise.

type WinCredStore struct{}

func (*WinCredStore) Kind() string { return "credential-manager" }
func (*WinCredStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	return &StoreError{Op: "add", Kind: "credential-manager", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Windows Credential Manager n'est pas compilé sur cette plateforme")}
}
func (*WinCredStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	return "", &StoreError{Op: "get", Kind: "credential-manager", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Windows Credential Manager n'est pas compilé sur cette plateforme")}
}
func (*WinCredStore) Remove(ctx context.Context, kind CredentialKind) error {
	return &StoreError{Op: "remove", Kind: "credential-manager", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Windows Credential Manager n'est pas compilé sur cette plateforme")}
}
func (*WinCredStore) Verify(ctx context.Context) error {
	return &StoreError{Op: "verify", Kind: "credential-manager", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Windows Credential Manager n'est pas compilé sur cette plateforme")}
}

func (*WinCredStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}

type SecretServiceStore struct{}

func (*SecretServiceStore) Kind() string { return "secret-service" }
func (*SecretServiceStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	return &StoreError{Op: "add", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Secret Service n'est pas compilé sur cette plateforme")}
}
func (*SecretServiceStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	return "", &StoreError{Op: "get", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Secret Service n'est pas compilé sur cette plateforme")}
}
func (*SecretServiceStore) Remove(ctx context.Context, kind CredentialKind) error {
	return &StoreError{Op: "remove", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Secret Service n'est pas compilé sur cette plateforme")}
}
func (*SecretServiceStore) Verify(ctx context.Context) error {
	return &StoreError{Op: "verify", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Secret Service n'est pas compilé sur cette plateforme")}
}

func (*SecretServiceStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}
