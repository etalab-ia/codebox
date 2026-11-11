//go:build darwin

package codebox

import (
	"context"
	"fmt"
	"strings"
)

// KeychainStore stores credentials in the macOS Keychain via the `security`
// CLI. The value never appears in argv: every mutation runs through
// `security -i`, which reads its subcommand line from stdin (verified
// empirically: `printf 'add-generic-password ... -w v1\n' | security -i`
// stores the value, exits cleanly on EOF, and the trailing-`-w` prompt
// form is NOT used because a non-tty stdin makes it store an empty value
// instead of the piped bytes). Reads come back on stdout of a subprocess
// we own; removes take no value at all.
type KeychainStore struct {
	// Runner executes the `security` CLI. Defaults to OSRunner; tests
	// inject a fake.
	Runner Runner
}

func (k *KeychainStore) runner() Runner {
	if k.Runner != nil {
		return k.Runner
	}
	return OSRunner{}
}

func (k *KeychainStore) Kind() string { return "keychain" }

// Put stores the credential, replacing any existing entry of the same
// kind: -U updates in place, so rotation needs no delete first.
func (k *KeychainStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	r := k.runner()
	// The subcommand line travels on stdin (security -i); the value is
	// inside that line, never in this process's argv.
	line := fmt.Sprintf("add-generic-password -U -s %s -a codebox -w %s\n", credentialService(kind), value)
	res, err := r.RunStdin(ctx, strings.NewReader(line), "security", "-i")
	if err != nil {
		return classifyExecError("add", k.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		return storeErrorf("add", k.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Get returns the stored value. `security find-generic-password -w`
// prints the password to stdout; the value lives only in this process.
func (k *KeychainStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	res, err := k.runner().Run(ctx, "security", "find-generic-password",
		"-s", credentialService(kind), "-w")
	if err != nil {
		return "", classifyExecError("get", k.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if strings.Contains(res.Stderr, "could not be found") {
			return "", ErrCredentialNotFound
		}
		// "could not be decrypted" and user-denied ACL prompts both land
		// here; the keychain is present but access is refused.
		return "", storeErrorf("get", k.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	value := strings.TrimSuffix(res.Stdout, "\n")
	if value == "" {
		return "", storeErrorf("get", k.Kind(), "corrupt", "empty value stored for %s", kind)
	}
	return value, nil
}

func (k *KeychainStore) Remove(ctx context.Context, kind CredentialKind) error {
	r := k.runner()
	line := fmt.Sprintf("delete-generic-password -s %s\n", credentialService(kind))
	res, err := r.RunStdin(ctx, strings.NewReader(line), "security", "-i")
	if err != nil {
		return classifyExecError("remove", k.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if strings.Contains(res.Stderr, "could not be found") {
			return ErrCredentialNotFound
		}
		return storeErrorf("remove", k.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Verify probes the keychain with a read of a sentinel service name. It
// must not create anything; a "not found" answer proves the store is
// reachable and answering.
func (k *KeychainStore) Verify(ctx context.Context) error {
	res, err := k.runner().Run(ctx, "security", "find-generic-password",
		"-s", credentialService("__verify__"), "-w")
	if err != nil {
		return classifyExecError("verify", k.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if strings.Contains(res.Stderr, "could not be found") {
			return nil // store reachable, entry absent: healthy
		}
		return storeErrorf("verify", k.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return fmt.Errorf("verify sentinel unexpectedly found in keychain")
}

// Generation returns no marker: the native store has no store-local
// rotation counter, so reconcile uses the host-side generation counter
// (CredentialGeneration), which `auth add` bumps on every write.
func (k *KeychainStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}
