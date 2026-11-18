//go:build linux

package codebox

import (
	"context"
	"fmt"
)

// Stub for the Windows Credential Manager adapter on Linux: the platform
// compiles only the Secret Service adapter. The stub satisfies the
// CredentialStore interface so DefaultCredentialStore's reference
// resolves, but every operation reports the store as unavailable.

type KeychainStore struct{}

func (*KeychainStore) Kind() string { return "keychain" }
func (*KeychainStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	return &StoreError{Op: "add", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Keychain n'est pas compilé sur cette plateforme")}
}
func (*KeychainStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	return "", &StoreError{Op: "get", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Keychain n'est pas compilé sur cette plateforme")}
}
func (*KeychainStore) Remove(ctx context.Context, kind CredentialKind) error {
	return &StoreError{Op: "remove", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Keychain n'est pas compilé sur cette plateforme")}
}
func (*KeychainStore) Verify(ctx context.Context) error {
	return &StoreError{Op: "verify", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("l'adaptateur Keychain n'est pas compilé sur cette plateforme")}
}

func (*KeychainStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}

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
