package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func TestParseProjectSecretBindingsArgs(t *testing.T) {
	opts, err := parseProjectSecretBindingsArgs([]string{"development", ".env", "API_KEY", "--destination", "api.example.com", "--destination", "auth.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Profile != "development" || opts.Component != ".env" || opts.Name != "API_KEY" || len(opts.Destin) != 2 {
		t.Fatalf("positional form: %+v", opts)
	}
	flagged, err := parseProjectSecretBindingsArgs([]string{"--profile", "test", "--component", "apps/api/.env", "--name", "TOKEN", "--destination", "api.example.com", "--guest-env", "JC_CUSTOM"})
	if err != nil {
		t.Fatal(err)
	}
	if flagged.Profile != "test" || flagged.Component != "apps/api/.env" || flagged.GuestEnv != "JC_CUSTOM" {
		t.Fatalf("flag form: %+v", flagged)
	}
	for _, args := range [][]string{
		{"--destination"}, {"--nope", "x"}, {"a", "b", "c", "d"}, {"--clear", "--clear"},
	} {
		if _, err := parseProjectSecretBindingsArgs(args); err == nil {
			t.Errorf("parseProjectSecretBindingsArgs(%q) unexpectedly succeeded", args)
		}
	}
}

// withSecretsCLISeams isolates the CLI on a temporary project and state dir.
func withSecretsCLISeams(t *testing.T) (root, state string, store *secretsCLIStore) {
	t.Helper()
	root, state = t.TempDir(), t.TempDir()
	store = newSecretsCLIStore()
	origTTY, origState, origCred, origFactory := projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn
	projectSecretsTTYFn = func() bool { return false }
	projectSecretsStateDirFn = func() string { return state }
	projectSecretsCredentialStoreFn = func(bool) (codebox.CredentialStore, error) { return store, nil }
	projectSecretsStoreFactoryFn = codebox.NewProjectSecretStore
	t.Cleanup(func() {
		projectSecretsTTYFn, projectSecretsStateDirFn, projectSecretsCredentialStoreFn, projectSecretsStoreFactoryFn = origTTY, origState, origCred, origFactory
	})
	return root, state, store
}

func TestSecretsBindingsApproveListRevoke(t *testing.T) {
	root, _, store := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\nOTHER=value-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}

	// Listing before any approval shows the import but delivers nothing.
	out := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "(none selected)") || !strings.Contains(out, "Imported but not delivered: .env#API_KEY, .env#OTHER") {
		t.Fatalf("unapproved state must be visible: %q", out)
	}

	// Approving requires a destination: the approval is the authorization.
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--root", root}); code != 2 || err == nil {
		t.Fatalf("approval without a destination: code=%d err=%v", code, err)
	}

	out = captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root}); code != 0 || err != nil {
			t.Fatalf("approve: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "JC_API_KEY") || !strings.Contains(out, "api.example.com") {
		t.Fatalf("approval must name the derived variable and destination: %q", out)
	}
	// The value must never be displayed by any of these commands.
	if strings.Contains(out, "value-a") {
		t.Fatal("approval output must not show a value")
	}

	out = captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list after approve: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "API_KEY -> JC_API_KEY") || !strings.Contains(out, "profile: development") || !strings.Contains(out, ".env#OTHER") {
		t.Fatalf("approved state: %q", out)
	}
	if strings.Contains(out, "value-a") || strings.Contains(out, "value-b") {
		t.Fatal("listing must not show values")
	}

	// Approving an unimported variable is recorded but reported as
	// undeliverable at approval time, not silently accepted.
	// --yes so the failure under test is the unresolvable binding, not the
	// non-interactive confirmation gate (which would make this pass for the
	// wrong reason).
	if _, err := secretsBindingsCmd([]string{"approve", "development", ".env", "NOT_IMPORTED", "--destination", "api.example.com", "--yes", "--root", root}); err == nil {
		t.Fatal("approving a variable that was never imported must fail loudly")
	}

	if code, err := secretsBindingsCmd([]string{"revoke", "development", ".env", "API_KEY", "--root", root}); code != 0 || err != nil {
		t.Fatalf("revoke: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"revoke", "development", ".env", "API_KEY", "--root", root}); code != 0 || err != nil {
		t.Fatalf("revoking twice must be a no-op: code=%d err=%v", code, err)
	}
	// The imported value itself survives the revocation.
	s, err := codebox.NewProjectSecretStore(root, projectSecretsStateDirFn(), store)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(t.Context(), "development", ".env", "API_KEY"); err != nil || got != "value-a" {
		t.Fatalf("revocation must not delete the import: %v", err)
	}
}

func TestSecretsProfileSelection(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}
	if code, err := secretsProfileCmd([]string{"development", "--root", root}); code != 0 || err != nil {
		t.Fatalf("profile: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root}); code != 0 || err != nil {
		t.Fatalf("approve: code=%d err=%v", code, err)
	}
	// Clearing the profile while bindings remain would make every start fail
	// with an unexplained error, so it is refused.
	if code, err := secretsProfileCmd([]string{"--clear", "--root", root}); code != 1 || err == nil {
		t.Fatalf("clearing with bindings must be refused: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"revoke", "development", ".env", "API_KEY", "--root", root}); code != 0 || err != nil {
		t.Fatal(err)
	}
	if code, err := secretsProfileCmd([]string{"--clear", "--root", root}); code != 0 || err != nil {
		t.Fatalf("clearing without bindings: code=%d err=%v", code, err)
	}
	if code, err := secretsProfileCmd([]string{"--clear", "development", "--root", root}); code != 2 || err == nil {
		t.Fatalf("naming a profile and --clear together must be refused: code=%d", code)
	}
}

func TestSecretsCommandDispatch(t *testing.T) {
	if code, err := secretsCmd([]string{"nope"}); code != 2 || err == nil {
		t.Fatalf("unknown subcommand: code=%d err=%v", code, err)
	}
	if code, err := secretsCmd(nil); code != 0 || err != nil {
		t.Fatalf("bare secrets must print usage: code=%d err=%v", code, err)
	}
	if code, err := secretsCmd([]string{"bindings"}); code != 2 || err != nil {
		t.Fatalf("bare bindings must print usage with code 2: code=%d err=%v", code, err)
	}
}
