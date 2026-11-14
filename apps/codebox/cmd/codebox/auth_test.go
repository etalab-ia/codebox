package main

import (
	"context"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// CLI auth tests (P08): argument parsing and dispatch. Store behavior is
// covered by the internal package's tests; these pin the CLI surface.

func TestParseArgsAuthCapturesSubcommand(t *testing.T) {
	p, err := parseArgs([]string{"auth", "add", "--stdin"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if p.action != "auth" {
		t.Fatalf("action = %q, want auth", p.action)
	}
	if strings.Join(p.authArgs, " ") != "add --stdin" {
		t.Fatalf("authArgs = %v", p.authArgs)
	}
}

func TestParseArgsAuthStandalone(t *testing.T) {
	p, err := parseArgs([]string{"auth"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if p.action != "auth" || len(p.authArgs) != 0 {
		t.Fatalf("parsed = %+v", p)
	}
}

func TestParseAuthArgsDefaultsAndRejects(t *testing.T) {
	kind, stdin, fallback, err := parseAuthArgs(nil)
	if err != nil || kind != codebox.CredentialAlbert || stdin || fallback {
		t.Fatalf("defaults = %q, %v, %v, %v", kind, stdin, fallback, err)
	}
	if _, _, _, err := parseAuthArgs([]string{"gitlab"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	for _, known := range []string{"albert", "github", "context7"} {
		kind, _, _, err := parseAuthArgs([]string{known})
		if err != nil || kind != codebox.CredentialKind(known) {
			t.Fatalf("known kind %q: %q, %v", known, kind, err)
		}
	}
	if _, _, _, err := parseAuthArgs([]string{"albert", "--stdin", "--fallback"}); err != nil {
		t.Fatalf("valid flags: %v", err)
	}
}

func TestAuthCmdUnknownSubcommand(t *testing.T) {
	code, err := authCmd([]string{"list"})
	if code != 2 || err == nil {
		t.Fatalf("code = %d, err = %v; want 2, error", code, err)
	}
}

func TestAuthCmdUsage(t *testing.T) {
	code, err := authCmd(nil)
	if code != 2 || err != nil {
		t.Fatalf("code = %d, err = %v; want 2, nil", code, err)
	}
}

func TestAuthAddFallbackCreatesStoreOnlyAfterNonemptySecret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the CLI path test uses XDG/HOME credential directories")
	}
	home := t.TempDir()
	config := t.TempDir()
	state := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("APPDATA", config)

	store, err := codebox.ConsentFileStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("precondition: fallback store already exists: %v", err)
	}
	withAuthTestStdin(t, "\n")
	if code, err := authAddCmd([]string{"github", "--stdin", "--fallback"}); code != 1 || err == nil {
		t.Fatalf("empty fallback credential: code=%d err=%v", code, err)
	}
	if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
		t.Fatalf("empty input created the fallback store: %v", err)
	}

	const testValue = "not-a-real-github-token"
	withAuthTestStdin(t, testValue+"\n")
	if code, err := authAddCmd([]string{"github", "--stdin", "--fallback"}); code != 0 || err != nil {
		t.Fatalf("first explicit fallback write: code=%d err=%v", code, err)
	}
	got, err := store.Get(context.Background(), codebox.CredentialGithub)
	if err != nil || got != testValue {
		t.Fatalf("stored test value = %q, err=%v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(store.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("fallback store mode = %o, want 0600", info.Mode().Perm())
		}
	}
	// The write records the store it used, so a later start reads the value
	// back from the file instead of probing a native adapter that may be
	// unreachable (#201).
	if recorded := codebox.RecordedCredentialStoreForKind(codebox.DefaultStateDir(), codebox.CredentialGithub); recorded != "file" {
		t.Fatalf("recorded store = %q, want file", recorded)
	}
}

// TestAuthUseFallbackRecordsAnExistingValue covers the migration path: a host
// that stored with --fallback before the store choice was recorded can declare
// it without re-entering a secret it cannot read back.
func TestAuthUseFallbackRecordsAnExistingValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the CLI path test uses XDG/HOME credential directories")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	// Nothing stored yet: recording would pin a store that holds nothing, so it
	// must refuse and leave no record.
	if code, err := authUseFallbackCmd([]string{"albert"}); code != 1 || err == nil {
		t.Fatalf("no stored value: code=%d err=%v", code, err)
	}
	if recorded := codebox.RecordedCredentialStoreForKind(codebox.DefaultStateDir(), codebox.CredentialAlbert); recorded != "" {
		t.Fatalf("a refused command recorded %q", recorded)
	}

	store, err := codebox.ConsentFileStore()
	if err != nil {
		t.Fatal(err)
	}
	const testValue = "not-a-real-albert-key"
	if err := store.Put(context.Background(), codebox.CredentialAlbert, testValue); err != nil {
		t.Fatal(err)
	}
	if code, err := authUseFallbackCmd([]string{"albert"}); code != 0 || err != nil {
		t.Fatalf("record existing fallback value: code=%d err=%v", code, err)
	}
	if recorded := codebox.RecordedCredentialStoreForKind(codebox.DefaultStateDir(), codebox.CredentialAlbert); recorded != "file" {
		t.Fatalf("recorded store = %q, want file", recorded)
	}
	// Recording must not read or rewrite the value.
	got, err := store.Get(context.Background(), codebox.CredentialAlbert)
	if err != nil || got != testValue {
		t.Fatalf("the stored value changed: %q, %v", got, err)
	}
}

// TestAuthStatusReportsTheRecordedStore covers the user-visible half of #201: a
// host that stored with --fallback must be able to see which store a start will
// read from, including when the fallback file was never created (the early
// return that the record display has to survive).
func TestAuthStatusReportsTheRecordedStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the CLI path test uses XDG/HOME credential directories")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	if code, err := authStatusCmd(); code != 0 || err != nil {
		t.Fatalf("status with no record: code=%d err=%v", code, err)
	}
	if err := codebox.RecordCredentialStore(codebox.DefaultStateDir(), codebox.CredentialAlbert, "file"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code, err := authStatusCmd(); code != 0 || err != nil {
			t.Fatalf("status: code=%d err=%v", code, err)
		}
	})
	if !strings.Contains(out, "albert: read from the file store") {
		t.Fatalf("status did not report the recorded store:\n%s", out)
	}
}

func withAuthTestStdin(t *testing.T, input string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() {
		os.Stdin = previous
		_ = reader.Close()
	})
}
