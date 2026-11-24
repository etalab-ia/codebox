package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func extractTarGzMap(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		files[hdr.Name] = body
	}
	return files
}

func TestStatusBarArchiveContentsAndDeterminism(t *testing.T) {
	cfg := Config{
		WorkspaceDir: filepath.Join("tmp", "mon-projet"),
		APIKey:       "synthetic-albert-secret-must-not-enter-config",
		Providers: []InferenceProvider{
			{Kind: ProviderAlbert, Name: "Albert API (État)"},
			{Kind: ProviderMistral, Name: "Mistral AI"},
			{Kind: ProviderOpenAICompatible, Name: "Scaleway"},
		},
	}

	for _, tc := range []struct {
		harness    Harness
		entryFile  string
		absentFile string
	}{
		{harness: HarnessOpenCode, entryFile: "tui.tsx", absentFile: "index.ts"},
		{harness: HarnessPi, entryFile: "index.ts", absentFile: "tui.tsx"},
	} {
		t.Run(string(tc.harness), func(t *testing.T) {
			a1, d1, s1, err := statusBarArchive(tc.harness, cfg)
			if err != nil {
				t.Fatalf("statusBarArchive: %v", err)
			}
			a2, d2, s2, err := statusBarArchive(tc.harness, cfg)
			if err != nil {
				t.Fatalf("second statusBarArchive: %v", err)
			}
			if !bytes.Equal(a1, a2) || d1 != d2 || s1 != s2 {
				t.Fatal("status bar archive must be deterministic")
			}

			files := extractTarGzMap(t, a1)
			for _, want := range []string{tc.entryFile, "status-core.ts", "config.json", statusBarVersionFile} {
				if len(files[want]) == 0 {
					t.Errorf("archive missing %s", want)
				}
			}
			if _, ok := files[tc.absentFile]; ok {
				t.Errorf("archive for %s must not contain %s", tc.harness, tc.absentFile)
			}
			if strings.TrimSpace(string(files[statusBarVersionFile])) != d1 {
				t.Fatalf("version marker = %q, want %q", strings.TrimSpace(string(files[statusBarVersionFile])), d1)
			}
			if bytes.Contains(files["config.json"], []byte(cfg.APIKey)) {
				t.Fatal("config.json must never contain an API key")
			}

			var parsed statusBarConfig
			if err := json.Unmarshal(files["config.json"], &parsed); err != nil {
				t.Fatalf("parse config.json: %v", err)
			}
			if parsed.ProjectName != "mon-projet" {
				t.Errorf("projectName = %q, want mon-projet", parsed.ProjectName)
			}
			if parsed.ProviderNames["albert"] != "Albert API" || parsed.ProviderNames["openai-compatible"] != "Scaleway" {
				t.Errorf("providerNames = %#v", parsed.ProviderNames)
			}
		})
	}
}

func TestGuestStatusBarInstallScriptRealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test runs on Unix")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum unavailable")
	}

	cfg := Config{WorkspaceDir: "/host/mon-projet"}
	for _, tc := range []struct {
		harness   Harness
		relDest   string
		entryFile string
	}{
		{harness: HarnessOpenCode, relDest: filepath.Join("xdg", "opencode", "plugins", statusBarDirName), entryFile: "tui.tsx"},
		{harness: HarnessPi, relDest: filepath.Join("home", ".pi", "agent", "extensions", statusBarDirName), entryFile: "index.ts"},
	} {
		t.Run(string(tc.harness), func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			xdg := filepath.Join(root, "xdg")
			if err := os.MkdirAll(home, 0o755); err != nil {
				t.Fatal(err)
			}

			archive, digest, sha, err := statusBarArchive(tc.harness, cfg)
			if err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(root, "status.tar.gz")
			if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
				t.Fatal(err)
			}

			run := func(script string) (string, error) {
				cmd := exec.Command("sh", "-c", script)
				cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg)
				out, err := cmd.CombinedOutput()
				return string(out), err
			}

			// Initial install.
			script := guestStatusBarInstallScript(tc.harness, archivePath, digest, sha)
			if out, err := run(script); err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			dest := filepath.Join(root, tc.relDest)
			for _, name := range []string{tc.entryFile, "status-core.ts", "config.json", statusBarVersionFile} {
				if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
					t.Errorf("missing installed file %s: %v", name, err)
				}
			}
			if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
				t.Errorf("temp archive was not cleaned up")
			}

			// Idempotent re-install at same version.
			if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := run(script); err != nil {
				t.Fatalf("idempotent reinstall failed: %v\n%s", err, out)
			}

			// Upgrade in place to a new project name / digest.
			cfg2 := Config{WorkspaceDir: "/host/autre-projet"}
			archive2, digest2, sha2, err := statusBarArchive(tc.harness, cfg2)
			if err != nil {
				t.Fatal(err)
			}
			if digest2 == digest {
				t.Fatal("expected different digest for updated config")
			}
			if err := os.WriteFile(archivePath, archive2, 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := run(guestStatusBarInstallScript(tc.harness, archivePath, digest2, sha2)); err != nil {
				t.Fatalf("upgrade failed: %v\n%s", err, out)
			}
			ver, err := os.ReadFile(filepath.Join(dest, statusBarVersionFile))
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(ver)) != digest2 {
				t.Fatalf("upgraded version = %q, want %q", strings.TrimSpace(string(ver)), digest2)
			}
			cfgBytes, err := os.ReadFile(filepath.Join(dest, "config.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(cfgBytes), "autre-projet") {
				t.Fatalf("upgraded config.json = %s, want autre-projet", string(cfgBytes))
			}

			// Refuses checksum tampering.
			if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := run(guestStatusBarInstallScript(tc.harness, archivePath, digest, strings.Repeat("0", 64))); err == nil {
				t.Fatal("tampered archive checksum must be rejected")
			}

			// Refuses overwriting a user-owned directory without .codebox-version.
			if err := os.Remove(filepath.Join(dest, statusBarVersionFile)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := run(script); err == nil || !strings.Contains(out, "données utilisateur") {
				t.Fatalf("expected user-data conflict error, got err=%v out=%s", err, out)
			}

			// Refuses symlink at destination.
			if err := os.RemoveAll(dest); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "elsewhere")
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, dest); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
				t.Fatal(err)
			}
			if out, err := run(script); err == nil || !strings.Contains(out, "lien symbolique") {
				t.Fatalf("expected symlink refusal, got err=%v out=%s", err, out)
			}
		})
	}
}

func TestStatusBarGuestShellScriptInlineInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test runs on Unix")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		harness Harness
		dest    string
		entry   string
	}{
		{HarnessOpenCode, filepath.Join(xdg, "opencode", "plugins", statusBarDirName), "tui.tsx"},
		{HarnessPi, filepath.Join(home, ".pi", "agent", "extensions", statusBarDirName), "index.ts"},
	} {
		script, err := statusBarGuestShellScript(tc.harness, Config{WorkspaceDir: "/work/mon-projet"})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s inline install failed: %v\n%s", tc.harness, err, string(out))
		}
		if _, err := os.Stat(filepath.Join(tc.dest, tc.entry)); err != nil {
			t.Fatalf("%s missing %s: %v", tc.harness, tc.entry, err)
		}
	}
}

func TestInstallLocalStatusBarLifecycleAndSafety(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallLocalStatusBar(home, xdg, HarnessOpenCode, Config{WorkspaceDir: "/a/proj1"}); err != nil {
		t.Fatalf("initial InstallLocalStatusBar: %v", err)
	}
	dest := filepath.Join(xdg, "opencode", "plugins", statusBarDirName)
	for _, f := range []string{"tui.tsx", "status-core.ts", "config.json", statusBarVersionFile} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}

	// Upgrade in place.
	if err := InstallLocalStatusBar(home, xdg, HarnessOpenCode, Config{WorkspaceDir: "/a/proj2"}); err != nil {
		t.Fatalf("upgrade InstallLocalStatusBar: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "proj2") {
		t.Fatalf("expected proj2 in config.json, got %s", string(raw))
	}

	// Refuse user-owned directory without version marker.
	if err := os.Remove(filepath.Join(dest, statusBarVersionFile)); err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalStatusBar(home, xdg, HarnessOpenCode, Config{WorkspaceDir: "/a/proj2"}); err == nil {
		t.Fatal("expected refusal for user-owned directory")
	}

	// Refuse symlinked parent directory (Unix).
	if runtime.GOOS != "windows" {
		symRoot := t.TempDir()
		realTarget := filepath.Join(symRoot, "real")
		if err := os.MkdirAll(realTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		symXDG := filepath.Join(symRoot, "xdg")
		if err := os.Symlink(realTarget, symXDG); err != nil {
			t.Fatal(err)
		}
		if err := InstallLocalStatusBar(home, symXDG, HarnessOpenCode, Config{}); err == nil {
			t.Fatal("expected refusal for symlinked XDG_CONFIG_HOME")
		}
	}
}

func TestManagedStatusBarDoesNotTriggerProjectTrustPrompt(t *testing.T) {
	projectRoot := t.TempDir()
	stateDir := t.TempDir()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")

	// Install both managed status bars in global harness directories.
	if err := InstallLocalStatusBar(home, xdg, HarnessOpenCode, Config{WorkspaceDir: projectRoot}); err != nil {
		t.Fatal(err)
	}
	if err := InstallLocalStatusBar(home, xdg, HarnessPi, Config{WorkspaceDir: projectRoot}); err != nil {
		t.Fatal(err)
	}

	unapproved, err := DiscoverUnapprovedInputs(DefaultFS, stateDir, projectRoot)
	if err != nil {
		t.Fatalf("DiscoverUnapprovedInputs: %v", err)
	}
	if len(unapproved) != 0 {
		t.Fatalf("managed status bar must not trigger project trust review, got: %+v", unapproved)
	}
}

func TestReconcileNoOpInstallsStatusBarInPlaceWhenMissingOrOutdated(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}

	// Simulate an existing healthy sandbox created before #318 (empty StatusBarDigest).
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	if desired.StatusBarDigest == "" {
		t.Fatal("desired.StatusBarDigest must be non-empty")
	}
	preExisting := desired.toState()
	preExisting.StatusBarDigest = ""
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, preExisting); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(statePath)
		_ = os.RemoveAll(filepath.Dir(statePath))
	}()

	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if countCalls(client, "create") != 0 || countCalls(client, "start") != 0 || countCalls(client, "stop") != 0 {
		t.Fatalf("status bar update must happen in place without restart/recreate; calls: %v", client.calls)
	}
	if len(client.written) != 1 || !strings.HasPrefix(client.written[0].guestPath, "/tmp/codebox-status-") {
		t.Fatalf("expected status bar archive write, got: %+v", client.written)
	}
	updated, err := ReadInstanceState(DefaultFS, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.StatusBarDigest != desired.StatusBarDigest {
		t.Fatalf("persisted StatusBarDigest = %q, want %q", updated.StatusBarDigest, desired.StatusBarDigest)
	}

	// Second Reconcile must now be a strict no-op with zero writes.
	writesBefore := len(client.written)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(client.written) != writesBefore {
		t.Fatalf("second Reconcile unexpectedly wrote %d files", len(client.written)-writesBefore)
	}
}
