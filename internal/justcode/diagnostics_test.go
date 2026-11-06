package justcode

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsCollectDoesNotPrintCredentialValue(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	home := setupDiagnosticsEnv(t)
	secret := "test-credential-value-must-not-appear"
	t.Setenv("ALBERT_API_KEY", secret)
	settingsPath, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUserSettings(DefaultFS, settingsPath, UserSettings{SchemaVersion: userSettingsSchemaVersion, CredentialRef: secret}); err != nil {
		t.Fatal(err)
	}

	preflight := SetupPreflight{
		Platform:             "test/arch",
		VirtualizationOK:     true,
		DiskFreeBytes:        2 << 30,
		DiskOK:               true,
		RuntimeInstalled:     false,
		Fatal:                false,
		VirtualizationDetail: "test virtualization detail",
	}
	report := Diagnostics{
		Preflight:  &preflight,
		StateDir:   state,
		ProjectDir: root,
		LookPath: func(tool string) (string, error) {
			if tool == "go" {
				return "", exec.ErrNotFound
			}
			return filepath.Join(home, secret), nil
		},
		Run: func(context.Context, string, ...string) (ExecResult, error) {
			return ExecResult{Stdout: secret + " v1.2.3"}, nil
		},
		ProbeAlbert: func(context.Context, string) CredentialProbe {
			return CredentialProbe{Unreachable: true, Detail: "offline test: " + secret}
		},
		VerifyStore:    func(context.Context) (string, error) { return "test-store", nil },
		ReadCredential: noStoredCredentials(),
	}.Collect(context.Background())
	if strings.Contains(diagnosticText(report), secret) {
		t.Fatalf("diagnostics exposed the credential value: %q", diagnosticText(report))
	}
	if !strings.Contains(diagnosticText(report), "Point d'accès Albert") {
		t.Fatalf("provider section missing: %q", diagnosticText(report))
	}
	for _, section := range report.Sections {
		if section.Title != "Toolchain hôte" {
			continue
		}
		for _, item := range section.Items {
			if item.Name == "go" && item.Status != DiagnosticInfo {
				t.Fatalf("missing optional Go compiler should be informational, got %+v", item)
			}
		}
	}
}

func TestDiagnosticsProjectSectionsReportSelectionsWithoutFetching(t *testing.T) {
	setupDiagnosticsEnv(t)
	root := t.TempDir()
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".just-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := ProjectManifestPath(root)
	manifest := ProjectManifest{
		SchemaVersion: projectManifestSchemaVersion,
		Runtime:       string(RuntimeMicrosandbox),
		Isolation:     string(IsolationFull),
		Skills:        []string{"official/rgaa"},
		MCPConnectors: []string{"data-gouv"},
	}
	if err := WriteProjectManifest(DefaultFS, manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	lock := Lockfile{
		SchemaVersion:   lockfileSchemaVersion,
		DependencySetID: manifest.DependencySetID,
		Skills: map[string]SkillLock{
			"official/rgaa": {Repository: projectSkillsRepository, Revision: strings.Repeat("1", 40), SHA256: strings.Repeat("a", 64)},
		},
	}
	if err := WriteLockfile(DefaultFS, ProjectLockPath(root), lock); err != nil {
		t.Fatal(err)
	}

	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true, RuntimeInstalled: false}
	report := Diagnostics{
		Preflight:      &preflight,
		StateDir:       state,
		ProjectDir:     root,
		LookPath:       func(string) (string, error) { return "", execNotFound() },
		VerifyStore:    func(context.Context) (string, error) { return "test-store", nil },
		ReadAlbert:     func(context.Context) (string, error) { return "", ErrCredentialNotFound },
		ReadCredential: noStoredCredentials(),
	}.Collect(context.Background())
	text := diagnosticText(report)
	for _, want := range []string{"Skills et MCP du projet", "official/rgaa", "data-gouv", "Runtime Microsandbox géré"} {
		if !strings.Contains(text, want) {
			t.Fatalf("diagnostics missing %q: %q", want, text)
		}
	}
}

func TestDiagnosticsStoredCredentialAndJournalUseResolvedStateDir(t *testing.T) {
	setupDiagnosticsEnv(t)
	state := t.TempDir()
	if err := WriteSetupJournal(DefaultFS, state, SetupJournal{
		Stage: SetupStage("credential"), StoreKind: "native", CredentialKinds: []string{"albert"},
	}); err != nil {
		t.Fatal(err)
	}
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	secret := "stored-test-credential-never-print"
	var probed string
	report := Diagnostics{
		Preflight: &preflight, StateDir: state, ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert:  func(context.Context) (string, error) { return secret, nil },
		// The journal records albert, but the store is what doctor must read.
		// An injected reader keeps the assertion about the store rather than
		// about the host's real keyring.
		ReadCredential: func(_ context.Context, kind CredentialKind) (string, string, error) {
			if kind == CredentialAlbert {
				return secret, "native", nil
			}
			return "", "native", ErrCredentialNotFound
		},
		ProbeAlbert: func(_ context.Context, value string) CredentialProbe {
			probed = value
			return CredentialProbe{Rejected: true, Detail: "test rejection"}
		},
	}.Collect(context.Background())
	text := diagnosticText(report)
	if probed != secret {
		t.Fatalf("stored credential passed to probe = %q", probed)
	}
	if strings.Contains(text, secret) || !strings.Contains(text, "native") || !strings.Contains(text, "albert") {
		t.Fatalf("credential/storage diagnostics missing kinds or exposing secret: %q", text)
	}
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées"); got.Value != "albert" {
		t.Fatalf("the stored credential must come from the store, not only the journal: %+v", got)
	}
}

// #193: a finished setup removes its journal, so doctor must confirm the
// credential from the store instead of concluding "none recorded".
func TestDiagnosticsReportsStoredCredentialAfterSetupJournalRemoved(t *testing.T) {
	setupDiagnosticsEnv(t)
	state := t.TempDir()
	secret := "credential-value-must-never-print"
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight:   &preflight,
		StateDir:    state,
		ProjectDir:  t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert:  func(context.Context) (string, error) { return secret, nil },
		ReadCredential: func(_ context.Context, kind CredentialKind) (string, string, error) {
			if kind == CredentialAlbert {
				return secret, "native", nil
			}
			return "", "native", ErrCredentialNotFound
		},
		ProbeAlbert: func(context.Context, string) CredentialProbe {
			return CredentialProbe{Unreachable: true, Detail: "offline test"}
		},
	}.Collect(context.Background())
	text := diagnosticText(report)
	if strings.Contains(text, secret) {
		t.Fatalf("diagnostics exposed the credential value: %q", text)
	}
	if strings.Contains(text, "none recorded in setup journal") {
		t.Fatalf("doctor still concludes from the journal: %q", text)
	}
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées"); got.Value != "albert" || got.Status != DiagnosticOK {
		t.Fatalf("doctor did not confirm the credential from the store: %+v", got)
	}
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Journal de setup"); got.Value != "absent" {
		t.Fatalf("an absent journal must be reported as absent, not as an empty inventory: %+v", got)
	}
}

// The journal is a resume marker, never proof that a credential exists: when
// it records a kind the store no longer holds, doctor reports the store.
func TestDiagnosticsDoesNotTrustJournalAsCredentialInventory(t *testing.T) {
	setupDiagnosticsEnv(t)
	state := t.TempDir()
	if err := WriteSetupJournal(DefaultFS, state, SetupJournal{
		Stage: SetupStage("credential"), StoreKind: "native", CredentialKinds: []string{"albert"},
	}); err != nil {
		t.Fatal(err)
	}
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: state, ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert:  func(context.Context) (string, error) { return "", ErrCredentialNotFound },
		ReadCredential: func(_ context.Context, _ CredentialKind) (string, string, error) {
			return "", "native", ErrCredentialNotFound
		},
	}.Collect(context.Background())
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées"); got.Value != "aucune" {
		t.Fatalf("an empty store must report none even when the journal lists a kind: %+v", got)
	}
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Journal de setup"); got.Value != string(StageCredential) {
		t.Fatalf("a present journal must still report the interrupted stage: %+v", got)
	}
}

// A keyring that refuses a read cannot prove absence: the kind is unreadable,
// not "none".
func TestDiagnosticsReportsUnreadableCredentialKindSeparately(t *testing.T) {
	setupDiagnosticsEnv(t)
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert:  func(context.Context) (string, error) { return "", nil },
		ReadCredential: func(_ context.Context, kind CredentialKind) (string, string, error) {
			if kind == CredentialGithub {
				return "", "native", &StoreError{Op: "get", Kind: "native", State: "locked", Err: errors.New("collection locked")}
			}
			return "", "native", ErrCredentialNotFound
		},
	}.Collect(context.Background())
	text := diagnosticText(report)
	if !strings.Contains(text, "illisibles : github") {
		t.Fatalf("a refused read must be named as unreadable: %q", text)
	}
	got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées")
	if got.Status != DiagnosticWarn || got.Value != "inconnues" || strings.Contains(got.Detail, "aucun des types connus") {
		t.Fatalf("an unreadable kind must leave the inventory unknown, not empty: %+v", got)
	}
}

func TestDiagnosticsPartialCredentialInventoryPreservesKnownKinds(t *testing.T) {
	for _, unreadableKind := range KnownCredentialKinds() {
		t.Run(string(unreadableKind), func(t *testing.T) {
			d := Diagnostics{ReadCredential: func(_ context.Context, kind CredentialKind) (string, string, error) {
				if kind == unreadableKind {
					return "", "", errors.New("read refused: secret-error-canary")
				}
				return "secret-value-canary", "native", nil
			}}
			got := d.storedCredentialsItem(context.Background())
			if got.Status != DiagnosticWarn || !strings.HasSuffix(got.Value, " (partiel)") {
				t.Fatalf("an incomplete inventory must be marked partial: %+v", got)
			}
			for _, kind := range KnownCredentialKinds() {
				if kind != unreadableKind && !strings.Contains(got.Value, string(kind)) {
					t.Fatalf("confirmed kind %s missing: %+v", kind, got)
				}
			}
			if strings.Contains(got.Value, string(unreadableKind)) || !strings.Contains(got.Detail, "illisibles : "+string(unreadableKind)) {
				t.Fatalf("unreadable kind must not be counted as stored: %+v", got)
			}
			if strings.Contains(got.Value+got.Detail, "canary") {
				t.Fatalf("inventory exposed a value or raw error: %+v", got)
			}
		})
	}
}

func TestDiagnosticsAbsentJournalDoesNotInventSetupHistory(t *testing.T) {
	state := t.TempDir()
	journal, err := ReadSetupJournal(DefaultFS, state)
	if err != nil {
		t.Fatal(err)
	}
	got := setupJournalItem(DefaultFS, state, journal, nil)
	if got.Value != "absent" || got.Status != DiagnosticInfo || got.Detail != "aucun point de reprise de setup n'est présent ; le journal n'est pas un inventaire des clés enregistrées" {
		t.Fatalf("missing journal on a fresh host must not imply setup ran: %+v", got)
	}
}

// A keyring that refuses every read proves nothing: doctor says the inventory
// was not read rather than reporting an empty one. Absence has to be earned by
// a read that succeeded.
func TestDiagnosticsReportsNotReadWhenEveryCredentialReadIsRefused(t *testing.T) {
	setupDiagnosticsEnv(t)
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) {
			return "", &StoreError{Op: "verify", Kind: "native", State: "unavailable", Err: errors.New("daemon not running")}
		},
		ReadAlbert: func(context.Context) (string, error) { return "", nil },
		ReadCredential: func(_ context.Context, _ CredentialKind) (string, string, error) {
			return "", "", &StoreError{Op: "get", Kind: "native", State: "unavailable", Err: errors.New("daemon not running")}
		},
	}.Collect(context.Background())
	got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées")
	if got.Value != "non lues" || got.Status != DiagnosticWarn {
		t.Fatalf("a store that refused every read must not claim an inventory: %+v", got)
	}
	if strings.Contains(got.Detail, "aucun des types connus") {
		t.Fatalf("refused reads must not be reported as absence: %+v", got)
	}
}

// A host with no native keyring is not a failure: the run path resolves from
// the consented file fallback there, so doctor still reports that inventory.
func TestDiagnosticsReportsFileFallbackInventoryWithoutNativeStore(t *testing.T) {
	setupDiagnosticsEnv(t)
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	secret := "fallback-credential-never-print"
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) {
			return "none", errors.New("native credential store is not supported on this platform")
		},
		ReadAlbert: func(context.Context) (string, error) { return "", nil },
		ReadCredential: func(_ context.Context, kind CredentialKind) (string, string, error) {
			if kind == CredentialAlbert {
				return secret, "file", nil
			}
			// Faithful to the runtime's aggregate read path: an absent or
			// never-consented fallback comes back as ErrCredentialNotFound.
			return "", "", ErrCredentialNotFound
		},
	}.Collect(context.Background())
	text := diagnosticText(report)
	if strings.Contains(text, secret) {
		t.Fatalf("diagnostics exposed the credential value: %q", text)
	}
	got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées")
	if got.Value != "albert" || got.Status != DiagnosticOK {
		t.Fatalf("the file fallback inventory must still be reported: %+v", got)
	}
}

// The inventory is read under one shared deadline, not one per kind: a keyring
// daemon that is not answering must not cost a multiple of the budget. The
// fake refuses to read unless the context already carries a deadline, so this
// fails if the bound is ever dropped.
func TestDiagnosticsCredentialInventoryReadsUnderABoundedContext(t *testing.T) {
	setupDiagnosticsEnv(t)
	deadlines := 0
	var first time.Time
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert:  func(context.Context) (string, error) { return "", nil },
		ReadCredential: func(ctx context.Context, _ CredentialKind) (string, string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				return "", "", errors.New("credential inventory read has no deadline")
			}
			// The shared budget must cover all kinds, so the first deadline is
			// the whole budget and later reads inherit it rather than starting
			// their own. A per-kind timeout would multiply the worst case.
			if first.IsZero() {
				first = deadline
			} else if !deadline.Equal(first) {
				return "", "", errors.New("each kind got its own deadline instead of one shared budget")
			}
			deadlines++
			return "", "", errors.New("secret-service: daemon not answering")
		},
	}.Collect(context.Background())
	if deadlines != len(KnownCredentialKinds()) {
		t.Fatalf("every known kind must be read under a deadline, got %d", deadlines)
	}
	if got := diagnosticItemFrom(t, report, "Stockage des identifiants", "Clés enregistrées"); got.Value != "non lues" {
		t.Fatalf("a store that never answered must not claim an inventory: %+v", got)
	}
}

func TestDiagnosticsDoesNotMutateInterruptedProjectUpdate(t *testing.T) {
	setupDiagnosticsEnv(t)
	root := t.TempDir()
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".just-code"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := ProjectManifest{SchemaVersion: projectManifestSchemaVersion, Runtime: string(RuntimeMicrosandbox)}
	if err := WriteProjectManifest(DefaultFS, ProjectManifestPath(root), manifest); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, ProjectLockPath(root), Lockfile{SchemaVersion: lockfileSchemaVersion}); err != nil {
		t.Fatal(err)
	}
	journalPath := ProjectUpdateJournalPath(root)
	journal := []byte("interrupted transaction marker")
	if err := os.WriteFile(journalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}
	previousStateDirFn := projectUpdateStateDirFn
	projectUpdateStateDirFn = func() string { return state }
	t.Cleanup(func() { projectUpdateStateDirFn = previousStateDirFn })
	readonly := &diagnosticsReadOnlyFS{FS: DefaultFS}
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, FS: readonly, StateDir: state, ProjectDir: root,
		LookPath:       func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore:    func(context.Context) (string, error) { return "test-store", nil },
		ReadAlbert:     func(context.Context) (string, error) { return "", ErrCredentialNotFound },
		ReadCredential: noStoredCredentials(),
	}.Collect(context.Background())
	if readonly.writes != 0 {
		t.Fatalf("doctor attempted %d filesystem mutations", readonly.writes)
	}
	if got, err := os.ReadFile(journalPath); err != nil || string(got) != string(journal) {
		t.Fatalf("doctor modified the pending project journal: bytes=%q err=%v", got, err)
	}
	lockPath := projectUpdateLockPath(root)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("doctor created the project update lock %s: %v", lockPath, err)
	}
	if !strings.Contains(diagnosticText(report), "Mise à jour du projet en attente") {
		t.Fatalf("doctor did not report the pending update without recovering it: %q", diagnosticText(report))
	}
}

func TestDiagnosticsValidatesExplicitRuntimeWithoutManagedInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell runtime fixture is Unix-only")
	}
	setupDiagnosticsEnv(t)
	dir := t.TempDir()
	msbPath := filepath.Join(dir, "msb")
	libPath := filepath.Join(dir, "libkrunfw")
	if err := os.WriteFile(msbPath, []byte("#!/bin/sh\necho 'msb "+msbRuntimeVersion+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libPath, []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MSB_PATH", msbPath)
	t.Setenv("MSB_LIBKRUNFW_PATH", libPath)
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath:       func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore:    func(context.Context) (string, error) { return "test-store", nil },
		ReadAlbert:     func(context.Context) (string, error) { return "", ErrCredentialNotFound },
		ReadCredential: noStoredCredentials(),
	}.Collect(context.Background())
	for _, section := range report.Sections {
		if section.Title != "Runtime et état de l'instance du projet" {
			continue
		}
		for _, item := range section.Items {
			if item.Name == "Runtime Microsandbox explicite" && item.Status == DiagnosticOK {
				return
			}
		}
	}
	t.Fatalf("valid explicit runtime was not accepted without a managed install: %+v", report.Sections)
}

// TestDiagnosticsNamesAnUnreachedFallbackCredential is the #201 diagnostic: the
// native store fails, the read never reaches the consented file store, and that
// store holds the credential. Doctor must say so and name the remedies, instead
// of implying nothing was ever stored.
func TestDiagnosticsNamesAnUnreachedFallbackCredential(t *testing.T) {
	setupDiagnosticsEnv(t)
	file, err := ConsentFileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(context.Background(), CredentialAlbert, "secret-value-canary"); err != nil {
		t.Fatal(err)
	}
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert: func(context.Context) (string, error) {
			return "", &StoreError{Op: "get", Kind: "secret-service", State: "denied", Err: errors.New("exit 1: secret-tool: Cannot autolaunch D-Bus without X11 $DISPLAY")}
		},
		ReadCredential: func(_ context.Context, _ CredentialKind) (string, string, error) {
			return "", "native", ErrCredentialNotFound
		},
	}.Collect(context.Background())

	got := diagnosticItemFrom(t, report, "Vérification Albert", "Clé du magasin fichier")
	if got.Value != "présente, non lue" || got.Status != DiagnosticWarn {
		t.Fatalf("the unreached fallback must be reported: %+v", got)
	}
	for _, want := range []string{"auth use-fallback albert", "ALBERT_API_KEY"} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("detail must name %q: %q", want, got.Detail)
		}
	}
	// Presence is the claim; the value must never reach the report.
	if strings.Contains(diagnosticText(report), "secret-value-canary") {
		t.Fatal("the diagnostic report leaked the stored value")
	}
}

// TestDiagnosticsDoesNotClaimAnUnreachedFallbackWhenEmpty: with nothing in the
// file store the extra line would send the user to a command that refuses.
func TestDiagnosticsDoesNotClaimAnUnreachedFallbackWhenEmpty(t *testing.T) {
	setupDiagnosticsEnv(t)
	preflight := SetupPreflight{Platform: "test/arch", VirtualizationOK: true, DiskFreeBytes: 2 << 30, DiskOK: true}
	report := Diagnostics{
		Preflight: &preflight, StateDir: t.TempDir(), ProjectDir: t.TempDir(),
		LookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		VerifyStore: func(context.Context) (string, error) { return "native", nil },
		ReadAlbert: func(context.Context) (string, error) {
			return "", &StoreError{Op: "get", Kind: "secret-service", State: "denied", Err: errors.New("denied")}
		},
		ReadCredential: func(_ context.Context, _ CredentialKind) (string, string, error) {
			return "", "native", ErrCredentialNotFound
		},
	}.Collect(context.Background())
	if strings.Contains(diagnosticText(report), "Clé du magasin fichier") {
		t.Fatalf("an empty file store must not be advertised:\n%s", diagnosticText(report))
	}
}

func setupDiagnosticsEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// Windows resolves os.UserConfigDir() from APPDATA, not from HOME or
	// XDG_CONFIG_HOME. Without this the credential tests read the runner's real
	// AppData\just-code\credentials.json, so a test that expects "nothing is
	// stored" fails on any machine that has a fallback file — an environmental
	// failure, not a defect in the code under test.
	t.Setenv("APPDATA", filepath.Join(home, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "localappdata"))
	t.Setenv("ALBERT_API_KEY", "")
	t.Setenv("MSB_PATH", "")
	t.Setenv("MSB_LIBKRUNFW_PATH", "")
	return home
}

type diagnosticsReadOnlyFS struct {
	FS
	writes int
}

func (fs *diagnosticsReadOnlyFS) WriteFile(string, []byte, os.FileMode) error {
	fs.writes++
	return errors.New("read-only test filesystem")
}
func (fs *diagnosticsReadOnlyFS) MkdirAll(string, os.FileMode) error {
	fs.writes++
	return errors.New("read-only test filesystem")
}
func (fs *diagnosticsReadOnlyFS) Remove(string) error {
	fs.writes++
	return errors.New("read-only test filesystem")
}
func (fs *diagnosticsReadOnlyFS) RenameTmp(string, string) error {
	fs.writes++
	return errors.New("read-only test filesystem")
}
func (fs *diagnosticsReadOnlyFS) WriteTemp(string, string, []byte, os.FileMode) (string, error) {
	fs.writes++
	return "", errors.New("read-only test filesystem")
}
func (fs *diagnosticsReadOnlyFS) CreateExclusive(string, []byte, os.FileMode) error {
	fs.writes++
	return errors.New("read-only test filesystem")
}

func diagnosticText(report DiagnosticsReport) string {
	var b strings.Builder
	b.WriteString(report.Platform + "\n")
	for _, section := range report.Sections {
		b.WriteString(section.Title + "\n")
		for _, item := range section.Items {
			b.WriteString(item.Name + " " + item.Value + " " + item.Detail + "\n")
		}
	}
	return b.String()
}

func execNotFound() error { return exec.ErrNotFound }

// noStoredCredentials is the hermetic stand-in for the credential store: it
// reports every kind as absent, so a doctor test never reads the host's real
// keyring. A test that only passes because this machine holds no credentials
// would fail on a maintainer's laptop, which is the fragility this avoids.
func noStoredCredentials() func(context.Context, CredentialKind) (string, string, error) {
	return func(_ context.Context, _ CredentialKind) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
}

// diagnosticItemFrom finds one report item by section title and item name, so
// assertions read the structured value instead of matching a rendered line.
func diagnosticItemFrom(t *testing.T, report DiagnosticsReport, sectionTitle, itemName string) DiagnosticItem {
	t.Helper()
	for _, section := range report.Sections {
		if section.Title != sectionTitle {
			continue
		}
		for _, item := range section.Items {
			if item.Name == itemName {
				return item
			}
		}
	}
	t.Fatalf("report has no %q item in section %q: %+v", itemName, sectionTitle, report.Sections)
	return DiagnosticItem{}
}
