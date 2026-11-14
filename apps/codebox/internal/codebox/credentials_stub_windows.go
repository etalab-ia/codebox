//go:build windows

package codebox

import (
	"context"
	"fmt"
)

// Stub for the Secret Service adapter on Windows: the platform compiles
// only the Credential Manager adapter. The stub satisfies the
// CredentialStore interface so DefaultCredentialStore's reference
// resolves, but every operation reports the store as unavailable.

type KeychainStore struct{}

func (*KeychainStore) Kind() string { return "keychain" }
func (*KeychainStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	return &StoreError{Op: "add", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("the Keychain adapter is not compiled on this platform")}
}
func (*KeychainStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	return "", &StoreError{Op: "get", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("the Keychain adapter is not compiled on this platform")}
}
func (*KeychainStore) Remove(ctx context.Context, kind CredentialKind) error {
	return &StoreError{Op: "remove", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("the Keychain adapter is not compiled on this platform")}
}
func (*KeychainStore) Verify(ctx context.Context) error {
	return &StoreError{Op: "verify", Kind: "keychain", State: "unavailable",
		Err: fmt.Errorf("the Keychain adapter is not compiled on this platform")}
}

func (*KeychainStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}

type SecretServiceStore struct{}

func (*SecretServiceStore) Kind() string { return "secret-service" }
func (*SecretServiceStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	return &StoreError{Op: "add", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("the Secret Service adapter is not compiled on this platform")}
}
func (*SecretServiceStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	return "", &StoreError{Op: "get", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("the Secret Service adapter is not compiled on this platform")}
}
func (*SecretServiceStore) Remove(ctx context.Context, kind CredentialKind) error {
	return &StoreError{Op: "remove", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("the Secret Service adapter is not compiled on this platform")}
}
func (*SecretServiceStore) Verify(ctx context.Context) error {
	return &StoreError{Op: "verify", Kind: "secret-service", State: "unavailable",
		Err: fmt.Errorf("the Secret Service adapter is not compiled on this platform")}
}

func (*SecretServiceStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}
