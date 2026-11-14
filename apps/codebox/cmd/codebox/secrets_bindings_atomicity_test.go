package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBindingsApproveRecordsNothingWhenItCannotResolve pins #210: the command
// used to record the approval and switch the active profile, and only then
// check that the binding resolves. On the exit-1 path it therefore left a
// phantom approval and a changed active profile behind, which `bindings list`
// then showed as if the user had succeeded.
//
// The scenario is the reported one: an import that was cancelled, so the
// profile has no imported value, followed by an approval of one of its
// variables.
func TestBindingsApproveRecordsNothingWhenItCannotResolve(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TT_VAR=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, err := secretsBindingsCmd([]string{"approve", "ctrlc", ".env", "TT_VAR", "--destination", "httpbin.org", "--yes", "--root", root})
	if code != 1 || err == nil {
		t.Fatalf("approving an unimported variable must fail: code=%d err=%v", code, err)
	}
	// The message must not claim an approval was recorded.
	if strings.Contains(err.Error(), "the approval was recorded") {
		t.Fatalf("nothing was recorded, so the message must not say it was: %v", err)
	}
	if !strings.Contains(err.Error(), "nothing was recorded") {
		t.Fatalf("the refusal must state that no state changed: %v", err)
	}

	// The ghost state of the report: a listed approval and a switched active
	// profile. Neither may be present.
	list := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if strings.Contains(list, "TT_VAR") {
		t.Fatalf("a failed approval must not leave an approval behind: %q", list)
	}
	if strings.Contains(list, "httpbin.org") {
		t.Fatalf("a failed approval must not leave its destination behind: %q", list)
	}
	if strings.Contains(list, "ctrlc") {
		t.Fatalf("a failed approval must not switch the active profile: %q", list)
	}
}

// TestBindingsApproveStillWorksWhenItResolves guards the other direction: the
// pre-write validation must not refuse an approval that genuinely resolves,
// including the case where the approval establishes the project's first
// profile.
func TestBindingsApproveStillWorksWhenItResolves(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}

	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root}); code != 0 || err != nil {
		t.Fatalf("a resolvable approval must succeed: code=%d err=%v", code, err)
	}
	list := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(list, "API_KEY") || !strings.Contains(list, "api.example.com") {
		t.Fatalf("the approval must be recorded: %q", list)
	}
}

// TestBindingsApproveRefusesAProfileMismatchWithoutWriting pins the second
// half of the resolution rule. A binding whose profile differs from the active
// one cannot resolve; the refusal must again leave the host-local state alone
// rather than adopting the mismatched profile.
func TestBindingsApproveRefusesAProfileMismatchWithoutWriting(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, "dev.env"), []byte("API_KEY=v\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prod.env"), []byte("API_KEY=w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", "dev.env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import dev: code=%d err=%v", code, err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "production", "--file", "prod.env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import prod: code=%d err=%v", code, err)
	}

	// The active profile is development; approving the production binding
	// cannot resolve while development is active.
	if code, err := secretsBindingsCmd([]string{"approve", "development", "dev.env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root}); code != 0 || err != nil {
		t.Fatalf("first approval: code=%d err=%v", code, err)
	}
	before := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})

	code, err := secretsBindingsCmd([]string{"approve", "production", "prod.env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root})
	if code != 1 || err == nil {
		t.Fatalf("a profile mismatch must be refused: code=%d err=%v", code, err)
	}
	after := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if before != after {
		t.Fatalf("a refused approval changed the recorded state:\nbefore=%q\nafter=%q", before, after)
	}
}
