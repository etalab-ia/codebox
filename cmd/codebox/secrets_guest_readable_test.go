package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func TestSecretsBindingsApproveGuestReadable(t *testing.T) {
	root, _, store := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=local-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}

	// Guest-readable delivery needs no destination, and must state the trade.
	out := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--yes", "--root", root}); code != 0 || err != nil {
			t.Fatalf("approve guest: code=%d err=%v", code, err)
		}
	})
	for _, want := range []string{"GUEST-READABLE", "including the agent", "snapshot"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the warning must mention %q: %q", want, out)
		}
	}
	if strings.Contains(out, "local-value") {
		t.Fatal("approval must never print the value")
	}

	// The two modes are mutually exclusive: a guest-readable binding has no
	// destination to substitute toward.
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--destination", "db.example.com", "--root", root}); code != 2 || err == nil {
		t.Fatalf("guest delivery with a destination must be refused: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--guest-env", "JC_X", "--root", root}); code != 2 || err == nil {
		t.Fatalf("guest delivery with a guest variable must be refused: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "whenever", "--root", root}); code != 2 || err == nil {
		t.Fatalf("an unknown delivery must be refused: code=%d err=%v", code, err)
	}

	// The listing must make the exposure unmistakable.
	out = captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"list", "--root", root}); code != 0 || err != nil {
			t.Fatalf("list: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "READABLE INSIDE THE SANDBOX") || !strings.Contains(out, "snapshot") {
		t.Fatalf("the listing must show the delivery mode and its consequence: %q", out)
	}

	// The recorded binding carries the mode, and no guest variable.
	projectStore, err := codebox.NewProjectSecretStore(root, projectSecretsStateDirFn(), store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := codebox.ReadProjectSecretBindingsRecordFor(projectStore)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Bindings) != 1 || !record.Bindings[0].GuestReadable() || record.Bindings[0].GuestEnv != "" {
		t.Fatalf("recorded binding: %+v", record.Bindings)
	}
}

func TestRevokeMessageMatchesTheDeliveryMode(t *testing.T) {
	root, _, _ := withSecretsCLISeams(t)
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=local-value\nAPI_KEY=remote-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, err := projectSecretsImportCmd([]string{"--root", root, "--profile", "development", "--file", ".env", "--apply", "--yes"}); code != 0 || err != nil {
		t.Fatalf("import: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "DB_PASSWORD", "--delivery", "guest", "--yes", "--root", root}); code != 0 || err != nil {
		t.Fatalf("approve guest: code=%d err=%v", code, err)
	}
	if code, err := secretsBindingsCmd([]string{"approve", "development", ".env", "API_KEY", "--destination", "api.example.com", "--yes", "--root", root}); code != 0 || err != nil {
		t.Fatalf("approve proxy: code=%d err=%v", code, err)
	}

	// The plaintext mode is the one where a wrong message is dangerous: the
	// guest keeps the real value, not an inert placeholder.
	out := captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"revoke", "development", ".env", "DB_PASSWORD", "--root", root}); code != 0 || err != nil {
			t.Fatalf("revoke guest: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "still readable inside the running sandbox") || !strings.Contains(out, "snapshot") {
		t.Fatalf("a guest-readable revocation must state what actually remains: %q", out)
	}
	if strings.Contains(out, "inert placeholder") {
		t.Fatalf("the guest-readable revocation must not reuse the proxy wording: %q", out)
	}

	out = captureSecretsStdout(t, func() {
		if code, err := secretsBindingsCmd([]string{"revoke", "development", ".env", "API_KEY", "--root", root}); code != 0 || err != nil {
			t.Fatalf("revoke proxy: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "inert placeholder") {
		t.Fatalf("a proxy revocation keeps its own wording: %q", out)
	}
}
