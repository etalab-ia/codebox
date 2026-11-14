package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestReadableApprovalRefusesAnUndeliverableValue(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=pa$$word\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}
	// The user is present now: refusing here is better than failing every
	// start later.
	// --yes so the failure under test is the undeliverable value, not the
	// non-interactive confirmation gate.
	code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--yes", "--root", root})
	if code != 1 || err == nil {
		t.Fatalf("an undeliverable value must be refused at approval: code=%d err=%v", code, err)
	}
	if !strings.Contains(err.Error(), "DB_PASSWORD") || !strings.Contains(err.Error(), "proxy delivery") {
		t.Fatalf("the refusal must name the variable and the alternative: %v", err)
	}
}
