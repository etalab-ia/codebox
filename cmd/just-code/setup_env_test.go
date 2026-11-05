package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
)

// The #308 tests are hermetic: ALBERT_API_KEY is always set through
// t.Setenv (never the host's value), probes are fakes, and the fake secret
// values must never appear in the captured output.

const envTestSecret = "sk-env-test-secret-123"

func envTestProvider(t *testing.T) justcode.InferenceProvider {
	t.Helper()
	provider, err := justcode.ResolveProvider(justcode.ProviderSettings{Kind: justcode.ProviderAlbert})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

// envTestWizard returns a wizard whose probe replays the scripted outcomes
// (the last one repeats) and records every key it was asked to validate.
func envTestWizard(stateDir string, store *fakeSetupStore, probes []justcode.CredentialProbe, validated *[]string) justcode.SetupWizard {
	calls := 0
	return justcode.SetupWizard{
		FS: justcode.DefaultFS, StateDir: stateDir,
		Store: func(context.Context) (justcode.SetupStore, error) { return store, nil },
		ValidateProvider: func(_ context.Context, _ justcode.InferenceProvider, key string) justcode.CredentialProbe {
			*validated = append(*validated, key)
			calls++
			if len(probes) == 0 {
				return justcode.CredentialProbe{}
			}
			if calls > len(probes) {
				return probes[len(probes)-1]
			}
			return probes[calls-1]
		},
	}
}

func stubEnvPasswordReader(t *testing.T, value string) {
	t.Helper()
	original := passwordReaderFn
	passwordReaderFn = func() (string, error) { return value, nil }
	t.Cleanup(func() { passwordReaderFn = original })
}

func TestAlbertEnvKeyAcceptedStoresWithoutPrintingValue(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	var verified bool
	var err error
	out := captureStdout(t, func() {
		_, verified, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("u\no\n")), envTestProvider(t), true, stateDir)
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if !verified {
		t.Fatal("the env key passed validation and must be marked verified")
	}
	if store.puts[justcode.CredentialAlbert] != envTestSecret {
		t.Fatalf("puts = %+v", store.puts)
	}
	if len(validated) != 1 || validated[0] != envTestSecret {
		t.Fatalf("the env key must go through the same validation as a typed key: %v", validated)
	}
	if !strings.Contains(out, "ALBERT_API_KEY") || !strings.Contains(out, "Enregistrer cette clé dans un fichier") {
		t.Errorf("the source and the storage agreement must be shown; output:\n%s", out)
	}
	if strings.Contains(out, envTestSecret) {
		t.Fatal("the secret value leaked into the output")
	}
}

func TestAlbertEnvKeyDeclinedUseFallsBackToManualEntry(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	var err error
	out := captureStdout(t, func() {
		// "s" declines the env key; the manual loop then runs and hits EOF
		// (no terminal in a test), which cancels without storing anything.
		_, _, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("s\n")), envTestProvider(t), true, stateDir)
	})
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("the manual loop must follow a refusal; err = %v", err)
	}
	if !strings.Contains(out, "Collez votre clé") {
		t.Errorf("the manual key prompt must be offered after a refusal; output:\n%s", out)
	}
	if len(store.puts) != 0 || len(validated) != 0 {
		t.Fatalf("nothing may be stored or validated after a refusal: puts=%+v validated=%v", store.puts, validated)
	}
	if strings.Contains(out, envTestSecret) {
		t.Fatal("the secret value leaked into the output")
	}
}

func TestAlbertEnvKeyDeclinedStorageFallsBackToManualEntry(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	var err error
	out := captureStdout(t, func() {
		_, _, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("u\nn\n")), envTestProvider(t), true, stateDir)
	})
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("a storage refusal must fall back to the manual loop; err = %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("a key validated but refused for storage must not be persisted: %+v", store.puts)
	}
	if len(validated) != 1 || validated[0] != envTestSecret {
		t.Fatalf("the env key must be validated before the storage question: %v", validated)
	}
	if !strings.Contains(out, "Enregistrer cette clé") {
		t.Errorf("the storage agreement was not asked; output:\n%s", out)
	}
}

func TestAlbertEnvKeyRejectedFallsBackToManualEntry(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, []justcode.CredentialProbe{{Rejected: true, Detail: "invalid key"}}, &validated)
	var err error
	out := captureStdout(t, func() {
		_, _, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("u\n")), envTestProvider(t), true, stateDir)
	})
	if !errors.Is(err, errSetupCancelled) {
		t.Fatalf("a rejected env key must fall back to the manual loop; err = %v", err)
	}
	if len(store.puts) != 0 {
		t.Fatalf("a rejected env key must never be stored: %+v", store.puts)
	}
	if !strings.Contains(out, "Collez votre clé") {
		t.Errorf("the manual key prompt must follow a rejection; output:\n%s", out)
	}
}

func TestAlbertEnvKeyUnreachableRetryThenVerified(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, []justcode.CredentialProbe{
		{Unreachable: true, Detail: "dial tcp: timeout"},
		{},
	}, &validated)
	var verified bool
	var err error
	captureStdout(t, func() {
		_, verified, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("u\nr\no\n")), envTestProvider(t), true, stateDir)
	})
	if err != nil || !verified {
		t.Fatalf("retry then success: verified=%v err=%v", verified, err)
	}
	if len(validated) != 2 {
		t.Fatalf("the retry must validate the env key again: %v", validated)
	}
	if store.puts[justcode.CredentialAlbert] != envTestSecret {
		t.Fatalf("puts = %+v", store.puts)
	}
}

func TestAlbertEnvKeyUnreachableContinueStoresUnverified(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, []justcode.CredentialProbe{{Unreachable: true, Detail: "down"}}, &validated)
	var verified bool
	var err error
	out := captureStdout(t, func() {
		_, verified, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("u\nc\no\n")), envTestProvider(t), true, stateDir)
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if verified {
		t.Fatal("an unreachable endpoint must leave the key unverified")
	}
	if store.puts[justcode.CredentialAlbert] != envTestSecret {
		t.Fatalf("puts = %+v", store.puts)
	}
	if !strings.Contains(out, "Clé enregistrée sans vérification.") {
		t.Errorf("the unverified storage must be stated; output:\n%s", out)
	}
}

func TestAlbertEnvKeyEmptyMeansNoProposal(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	stubEnvPasswordReader(t, "typed-key")
	t.Setenv("ALBERT_API_KEY", "")
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	out := captureStdout(t, func() {
		if _, _, err := providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("typed-key\n")), envTestProvider(t), true, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
	})
	if store.puts[justcode.CredentialAlbert] != "typed-key" {
		t.Fatalf("puts = %+v", store.puts)
	}
	if strings.Contains(out, "ALBERT_API_KEY") {
		t.Errorf("an empty variable must not trigger the proposal; output:\n%s", out)
	}
}

func TestAlbertEnvKeyAbsentMeansNoProposal(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	stubEnvPasswordReader(t, "typed-key")
	t.Setenv("ALBERT_API_KEY", "placeholder")
	if err := os.Unsetenv("ALBERT_API_KEY"); err != nil {
		t.Fatal(err)
	}
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	out := captureStdout(t, func() {
		if _, _, err := providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("typed-key\n")), envTestProvider(t), true, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
	})
	if store.puts[justcode.CredentialAlbert] != "typed-key" {
		t.Fatalf("puts = %+v", store.puts)
	}
	if strings.Contains(out, "ALBERT_API_KEY") {
		t.Errorf("an absent variable must not trigger the proposal; output:\n%s", out)
	}
}

func TestAlbertEnvKeyPipedSkipsProposal(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forcePipedWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	out := captureStdout(t, func() {
		if _, _, err := providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("typed-key\n")), envTestProvider(t), true, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
	})
	if store.puts[justcode.CredentialAlbert] != "typed-key" {
		t.Fatalf("a piped run must keep the manual behavior: %+v", store.puts)
	}
	if strings.Contains(out, "ALBERT_API_KEY") {
		t.Errorf("a piped run must not show the proposal; output:\n%s", out)
	}
	if strings.Contains(out, envTestSecret) {
		t.Fatal("the secret value leaked into the output")
	}
}

func TestAlbertEnvKeyHuhFlowUsesDefaults(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	origHuh, origRun, origTTY := wizardHuhFn, huhRun, stdinIsTTYFn
	wizardHuhFn = func() bool { return true }
	stdinIsTTYFn = func() bool { return true }
	forms := 0
	huhRun = func(*huh.Form) error {
		forms++
		return nil // both defaults: use the env key, accept the storage
	}
	t.Cleanup(func() { wizardHuhFn, huhRun, stdinIsTTYFn = origHuh, origRun, origTTY })
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	var verified bool
	var err error
	out := captureStdout(t, func() {
		_, verified, err = providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("")), envTestProvider(t), true, stateDir)
	})
	if err != nil || !verified {
		t.Fatalf("huh flow: verified=%v err=%v", verified, err)
	}
	if forms < 2 {
		t.Fatalf("the env choice and the storage confirmation must both run: %d forms", forms)
	}
	if store.puts[justcode.CredentialAlbert] != envTestSecret {
		t.Fatalf("puts = %+v", store.puts)
	}
	if strings.Contains(out, envTestSecret) {
		t.Fatal("the secret value leaked into the output")
	}
}

// TestAlbertEnvKeyStoredJournalTakesPriority: when the journal already
// records an Albert key, the wizard keeps it and never proposes the
// environment variable (#308 priority rule).
func TestAlbertEnvKeyStoredJournalTakesPriority(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	j := resumeTestJournal([]justcode.SetupPlatform{{Kind: "albert"}}, string(justcode.CredentialAlbert))
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	out := captureStdout(t, func() {
		if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("")), true, &j, false, justcode.DefaultFS, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
	})
	if strings.Contains(out, "ALBERT_API_KEY") {
		t.Errorf("a stored key must win over the environment: %s", out)
	}
	if len(store.puts) != 0 || len(validated) != 0 {
		t.Fatalf("nothing new may be stored or validated: puts=%+v validated=%v", store.puts, validated)
	}
}

// TestAlbertEnvKeyEditReplaceAllowsManualReplacement drives the edit/replace
// path with an environment key present: declining it validates and stores only
// the manually entered replacement (#308).
func TestAlbertEnvKeyEditReplaceAllowsManualReplacement(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	stubEnvPasswordReader(t, "manual-key")
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	j := resumeTestJournal([]justcode.SetupPlatform{{Kind: "albert"}}, string(justcode.CredentialAlbert))
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	out := captureStdout(t, func() {
		if _, err := setupPlatformStage(context.Background(), w, bufio.NewReader(strings.NewReader("\nr\ns\nmanual-key\n")), true, &j, true, justcode.DefaultFS, stateDir); err != nil {
			t.Fatalf("stage: %v", err)
		}
	})
	if store.puts[justcode.CredentialAlbert] != "manual-key" {
		t.Fatalf("puts = %+v, want only the manual replacement", store.puts)
	}
	if len(validated) != 1 || validated[0] != "manual-key" {
		t.Fatalf("validated = %v, want only the manual replacement", validated)
	}
	if strings.Contains(out, envTestSecret) {
		t.Fatal("the environment secret leaked into the output")
	}
}

func TestSetupCatalogueUsesManualKeyAfterEnvironmentRefusal(t *testing.T) {
	stateDir, _ := setupTestEnv(t)
	forceLineWizard(t)
	stubEnvPasswordReader(t, "manual-key")
	t.Setenv("ALBERT_API_KEY", envTestSecret)
	t.Setenv("JUST_CODE_CREDENTIAL_REF", "")
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	// A whitespace reference is empty to runtime resolution too, so the new
	// journal credential must win before final settings are written.
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{SchemaVersion: 1, CredentialRef: " \t"}); err != nil {
		t.Fatal(err)
	}
	store := &fakeSetupStore{}
	var validated []string
	w := envTestWizard(stateDir, store, nil, &validated)
	if _, _, err := providerKeyStage(context.Background(), w, bufio.NewReader(strings.NewReader("s\n")), envTestProvider(t), true, stateDir); err != nil {
		t.Fatalf("decline the environment key and store a manual key: %v", err)
	}
	if store.puts[justcode.CredentialAlbert] != "manual-key" || len(validated) != 1 || validated[0] != "manual-key" {
		t.Fatalf("only the manual key may be stored and validated: puts=%+v validated=%v", store.puts, validated)
	}
	if got := resolveSetupCatalogueCredentialRefAt(t.TempDir()); got != string(justcode.CredentialAlbert) {
		t.Fatalf("catalogue credentialRef = %q, want the newly stored Albert key instead of the declined environment key", got)
	}

	// An explicit custom reference remains authoritative even with a new
	// journal credential: setup must not silently replace the user's choice.
	if err := justcode.WriteUserSettings(justcode.DefaultFS, settingsPath, justcode.UserSettings{SchemaVersion: 1, CredentialRef: "work"}); err != nil {
		t.Fatal(err)
	}
	if got := resolveSetupCatalogueCredentialRefAt(t.TempDir()); got != "work" {
		t.Fatalf("custom catalogue credentialRef = %q, want work", got)
	}
}
