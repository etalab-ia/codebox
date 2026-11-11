//go:build linux

package codebox

import (
	"context"
	"strings"
)

// SecretServiceStore stores credentials in the Linux Secret Service
// (gnome-keyring or kwallet via the org.freedesktop.secrets D-Bus
// interface) using the `secret-tool` CLI from libsecret. The value never
// appears in argv: `secret-tool store` reads it from stdin when no value
// argument is given, and `secret-tool lookup` prints it to stdout.
type SecretServiceStore struct {
	// Runner executes the `secret-tool` CLI. Defaults to OSRunner; tests
	// inject a fake.
	Runner Runner
}

func (s *SecretServiceStore) runner() Runner {
	if s.Runner != nil {
		return s.Runner
	}
	return OSRunner{}
}

func (s *SecretServiceStore) Kind() string { return "secret-service" }

// secretServiceLocked reports whether a secret-tool stderr indicates a
// locked collection whose unlock prompt was declined (or timed out).
// The wording is not stable across gnome-keyring versions, so match the
// stable fragments: "collection" plus "locked"/"unlock". This keeps the
// locked state distinguishable from an authorization denial — the two
// need different recovery (unlock the keyring vs re-grant access).
func secretServiceLocked(stderr string) bool {
	l := strings.ToLower(stderr)
	return strings.Contains(l, "collection") &&
		(strings.Contains(l, "locked") || strings.Contains(l, "unlock"))
}

// Put stores the credential, replacing any existing entry of the same
// kind. secret-tool store replaces an existing matching collection item,
// so rotation is one operation.
func (s *SecretServiceStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	// No value argument: secret-tool reads the secret from stdin, keeping
	// it out of argv and process listings.
	res, err := s.runner().RunStdin(ctx, strings.NewReader(value), "secret-tool", "store",
		"--label", credentialService(kind), "service", credentialService(kind))
	if err != nil {
		return classifyExecError("add", s.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if secretServiceLocked(res.Stderr) {
			return storeErrorf("add", s.Kind(), "locked", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return storeErrorf("add", s.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Get returns the stored value. secret-tool lookup prints it to stdout and
// exits nonzero when absent.
func (s *SecretServiceStore) Get(ctx context.Context, kind CredentialKind) (string, error) {
	res, err := s.runner().Run(ctx, "secret-tool", "lookup", "service", credentialService(kind))
	if err != nil {
		return "", classifyExecError("get", s.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		// secret-tool exits 1 both for a missing item and for a locked
		// collection the user declined to unlock; stderr distinguishes
		// them in practice, but the wording is not stable across
		// versions, so a missing attribute with empty stderr is treated
		// as not-found and anything else as denied.
		if strings.TrimSpace(res.Stderr) == "" {
			return "", ErrCredentialNotFound
		}
		if secretServiceLocked(res.Stderr) {
			return "", storeErrorf("get", s.Kind(), "locked", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return "", storeErrorf("get", s.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	value := strings.TrimSuffix(res.Stdout, "\n")
	if value == "" {
		return "", storeErrorf("get", s.Kind(), "corrupt", "empty value stored for %s", kind)
	}
	return value, nil
}

func (s *SecretServiceStore) Remove(ctx context.Context, kind CredentialKind) error {
	res, err := s.runner().Run(ctx, "secret-tool", "clear", "service", credentialService(kind))
	if err != nil {
		return classifyExecError("remove", s.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if strings.TrimSpace(res.Stderr) == "" {
			return ErrCredentialNotFound
		}
		if secretServiceLocked(res.Stderr) {
			return storeErrorf("remove", s.Kind(), "locked", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
		}
		return storeErrorf("remove", s.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Verify probes the service with a lookup of a sentinel attribute set. It
// must not create anything; a nonzero exit with empty stderr proves the
// service is reachable and answering (item absent).
func (s *SecretServiceStore) Verify(ctx context.Context) error {
	res, err := s.runner().Run(ctx, "secret-tool", "lookup", "service", credentialService("__verify__"))
	if err != nil {
		// exec.Command not found: the libsecret CLI is not installed.
		return classifyExecError("verify", s.Kind(), res, err)
	}
	if res.ExitCode != 0 {
		if strings.TrimSpace(res.Stderr) == "" {
			return nil // service reachable, sentinel absent: healthy
		}
		return storeErrorf("verify", s.Kind(), "denied", "exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// Generation returns no marker: the native store has no store-local
// rotation counter, so reconcile uses the host-side generation counter
// (CredentialGeneration), which `auth add` bumps on every write.
func (s *SecretServiceStore) Generation(ctx context.Context, kind CredentialKind) (string, error) {
	return "", nil
}
