package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

// withApproveConfirmSeams installs a TTY plus a confirm function so approval
// can be exercised in both directions. The returned pointer records how many
// times the prompt was consulted, which is what distinguishes "no prompt was
// declared" from "a prompt was declined".
func withApproveConfirmSeams(t *testing.T, tty, answer bool) (root string, promptCalls *int) {
	t.Helper()
	var calls int
	root, _, _ = withSecretsCLISeams(t)
	origTTY, origConfirm := projectSecretsTTYFn, projectSecretsConfirmFn
	projectSecretsTTYFn = func() bool { return tty }
	projectSecretsConfirmFn = func(string, string, bool) (bool, error) {
		calls++
		return answer, nil
	}
	t.Cleanup(func() {
		projectSecretsTTYFn, projectSecretsConfirmFn = origTTY, origConfirm
	})
	return root, &calls
}

// TestBindingsApproveRequiresYesWhenNonInteractive pins the head of #209:
// --yes was declared and parsed but never read, so no prompt existed at all.
// The fix makes a non-interactive approval require it explicitly, matching the
// rule `secrets import --apply` already follows.
func TestBindingsApproveRequiresYesWhenNonInteractive(t *testing.T) {
	root, calls := withApproveConfirmSeams(t, false, true)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--root", root}); code != 2 || err == nil {
		t.Fatalf("non-interactive approval without --yes must be refused: code=%d err=%v", code, err)
	}
	if *calls != 0 {
		t.Fatalf("no prompt may be attempted without a TTY, got %d", *calls)
	}
}

// TestBindingsApprovePromptsOnTTY pins the body of #209: on a terminal the
// approval asks first, and a declined prompt records nothing.
func TestBindingsApprovePromptsOnTTY(t *testing.T) {
	root, calls := withApproveConfirmSeams(t, true, false)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Import first so the binding would otherwise resolve.
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}

	out := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--root", root}); code != 0 || err != nil {
			t.Fatalf("a declined prompt is a normal outcome: code=%d err=%v", code, err)
		}
	})
	if *calls != 1 {
		t.Fatalf("a TTY approval must ask exactly once, got %d", *calls)
	}
	if !strings.Contains(out, "Cancelled") {
		t.Fatalf("a declined approval must say so: %q", out)
	}
	// Nothing may be recorded when the prompt is declined.
	list := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if strings.Contains(list, "api.example.com") {
		t.Fatalf("a declined approval must not be recorded: %q", list)
	}
}

// countingStore wraps a credential store and records whether it was touched,
// so a test can assert that a refused invocation never reached it.
type countingStore struct {
	codebox.CredentialStore
	gets int
}

func (c *countingStore) Get(ctx context.Context, k codebox.CredentialKind) (string, error) {
	c.gets++
	return c.CredentialStore.Get(ctx, k)
}

// TestBindingsApproveRejectsBeforeTouchingTheStore pins the first P2 on the
// #218 review: the non-interactive gate must run before the store is built and
// before any value is read, so an invocation that should be refused as invalid
// automation cannot unlock a collection, block on backend access, or fail with
// an unrelated lookup error instead of the --yes requirement.
func TestBindingsApproveRejectsBeforeTouchingTheStore(t *testing.T) {
	root, calls := withApproveConfirmSeams(t, false, true)
	_ = calls
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}
	// Swap in a counting store so the refused invocation's access is visible.
	counting := &countingStore{CredentialStore: newSecretsCLIStore()}
	origCred := projectSecretsCredentialStoreFn
	projectSecretsCredentialStoreFn = func(bool) (codebox.CredentialStore, error) {
		return counting, nil
	}
	t.Cleanup(func() { projectSecretsCredentialStoreFn = origCred })

	code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--root", root})
	if code != 2 || err == nil {
		t.Fatalf("a non-interactive approval without --yes must be refused: code=%d err=%v", code, err)
	}
	if strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("the refusal must be the --yes requirement, not a lookup error: %v", err)
	}
	if counting.gets != 0 {
		t.Fatalf("a refused invocation must not read the credential store, got %d reads", counting.gets)
	}
}

// TestBindingsApproveCancellationReportsAnExistingApproval pins the second P2
// on the #218 review: approvals are upserted, so declining a re-run leaves the
// previous approval active. Saying only "No approval recorded" could read as
// "no sandbox access" when access is in fact still granted.
func TestBindingsApproveCancellationReportsAnExistingApproval(t *testing.T) {
	root, _ := withApproveConfirmSeams(t, true, true)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=value-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}
	// First approval succeeds (seam answers yes).
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--root", root}); code != 0 || err != nil {
		t.Fatalf("first approval: code=%d err=%v", code, err)
	}
	// Re-run and decline: the earlier approval must be reported as still active.
	origConfirm := projectSecretsConfirmFn
	projectSecretsConfirmFn = func(string, string, bool) (bool, error) { return false, nil }
	t.Cleanup(func() { projectSecretsConfirmFn = origConfirm })

	out := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "other.example.com", "--root", root}); code != 0 || err != nil {
			t.Fatalf("declined re-run: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "Cancelled") {
		t.Fatalf("the cancellation must be reported: %q", out)
	}
	if !strings.Contains(out, "remains in effect") {
		t.Fatalf("a declined re-run must say the previous approval still applies: %q", out)
	}
	// And the old destination must be the one still recorded.
	list := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(list, "api.example.com") || strings.Contains(list, "other.example.com") {
		t.Fatalf("the declined change must not be applied: %q", list)
	}
}
