package codebox

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// tarEntry is one member of a fake release archive. typeflag lets a test build
// the hostile archives the extractor must refuse.
type tarEntry struct {
	name     string
	content  string
	typeflag byte
	linkname string
}

func fakeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o755,
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(entry.content))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(entry.content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fakeZip(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(0o755)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// artifactFor builds an artifact matching the bytes a test serves.
func artifactFor(name, payload, binaryContent string) gitleaksArtifact {
	return gitleaksArtifact{
		name:         name,
		sha256:       digestOf([]byte(payload)),
		binary:       "gitleaks",
		binarySHA256: digestOf([]byte(binaryContent)),
	}
}

// serveArchive serves one archive at the path the installer builds and returns
// the base URL plus a counter of requests.
func serveArchive(t *testing.T, name string, payload []byte) (string, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server.URL, &hits
}

func useReleaseBaseURL(t *testing.T, baseURL string) {
	t.Helper()
	orig := gitleaksReleaseBaseURL
	gitleaksReleaseBaseURL = baseURL
	t.Cleanup(func() { gitleaksReleaseBaseURL = orig })
}

// openArchive writes payload to a temporary file and returns it open, which is
// what the installer passes to the extractor.
func openArchive(t *testing.T, payload []byte) (*os.File, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return file, info.Size()
}

func TestGitleaksArtifactFor(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range []struct {
		goos, goarch string
		binary       string
		zip          bool
	}{
		{"darwin", "arm64", "gitleaks", false},
		{"darwin", "amd64", "gitleaks", false},
		{"linux", "arm64", "gitleaks", false},
		{"linux", "amd64", "gitleaks", false},
		{"windows", "arm64", "gitleaks.exe", true},
		{"windows", "amd64", "gitleaks.exe", true},
	} {
		artifact, err := gitleaksArtifactFor(tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.goos, tc.goarch, err)
		}
		if artifact.binary != tc.binary {
			t.Errorf("%s/%s binary = %q, want %q", tc.goos, tc.goarch, artifact.binary, tc.binary)
		}
		if artifact.zip != tc.zip {
			t.Errorf("%s/%s zip = %v, want %v", tc.goos, tc.goarch, artifact.zip, tc.zip)
		}
		for label, digest := range map[string]string{"archive": artifact.sha256, "binary": artifact.binarySHA256} {
			if len(digest) != 64 {
				t.Errorf("%s/%s %s digest is not a sha256: %q", tc.goos, tc.goarch, label, digest)
			}
			if seen[digest] {
				t.Errorf("%s/%s %s digest repeats another platform's", tc.goos, tc.goarch, label)
			}
			seen[digest] = true
		}
		wantSuffix := ".tar.gz"
		if tc.zip {
			wantSuffix = ".zip"
		}
		if !strings.HasSuffix(artifact.name, wantSuffix) {
			t.Errorf("%s/%s asset = %q, want suffix %q", tc.goos, tc.goarch, artifact.name, wantSuffix)
		}
	}
	if _, err := gitleaksArtifactFor("linux", "riscv64"); err == nil {
		t.Error("unsupported architecture must be refused")
	}
	if _, err := gitleaksArtifactFor("plan9", "amd64"); err == nil {
		t.Error("unsupported OS must be refused")
	}
}

// TestEnsureGitleaksInstallsVerifiedArchive covers download, archive digest,
// extraction, executable digest and reuse.
func TestEnsureGitleaksInstallsVerifiedArchive(t *testing.T) {
	const binary = "#!/bin/sh\necho scanner-ok\n"
	payload := fakeTarGz(t,
		tarEntry{name: "LICENSE", content: "MIT\n"},
		tarEntry{name: "gitleaks", content: binary},
		tarEntry{name: "README", content: "readme\n"},
	)
	artifact := artifactFor("gitleaks_8.30.1_linux_x64.tar.gz", string(payload), binary)
	baseURL, hits := serveArchive(t, artifact.name, payload)
	useReleaseBaseURL(t, baseURL)

	stateDir := t.TempDir()
	path, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact)
	if err != nil {
		t.Fatalf("ensureGitleaksFor: %v", err)
	}
	if want := filepath.Join(gitleaksInstallDir(stateDir), "gitleaks"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no executable bit; Go reports 0666 for every file there.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != binary {
		t.Errorf("installed binary content = %q", body)
	}
	entries, err := os.ReadDir(gitleaksInstallDir(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "gitleaks" {
			t.Errorf("install directory must contain only the executable, found %s", entry.Name())
		}
	}

	// A second call reuses the verified copy without touching the network.
	if _, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact); err != nil {
		t.Fatalf("second ensureGitleaksFor: %v", err)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Errorf("downloads = %d, want 1 (verified copy must be reused)", got)
	}
}

func TestEnsureGitleaksRejectsTamperedArchive(t *testing.T) {
	const binary = "#!/bin/sh\necho expected\n"
	payload := fakeTarGz(t, tarEntry{name: "gitleaks", content: binary})
	tampered := fakeTarGz(t, tarEntry{name: "gitleaks", content: "#!/bin/sh\necho tampered\n"})
	artifact := artifactFor("gitleaks_8.30.1_linux_x64.tar.gz", string(payload), binary)
	baseURL, _ := serveArchive(t, artifact.name, tampered)
	useReleaseBaseURL(t, baseURL)

	stateDir := t.TempDir()
	_, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact)
	if err == nil || !strings.Contains(err.Error(), "integrity verification") {
		t.Fatalf("expected an integrity failure, got %v", err)
	}
	if _, statErr := os.Stat(gitleaksBinaryPath(stateDir, artifact)); statErr == nil {
		t.Error("a rejected archive must not leave an executable behind")
	}
}

// TestEnsureGitleaksRejectsMismatchedBinary pins the second trust anchor: an
// archive can match its digest while the executable inside is not the one this
// build pinned (a rebuilt release, or a substituted member).
func TestEnsureGitleaksRejectsMismatchedBinary(t *testing.T) {
	const binary = "#!/bin/sh\necho unexpected\n"
	payload := fakeTarGz(t, tarEntry{name: "gitleaks", content: binary})
	artifact := artifactFor("gitleaks_8.30.1_linux_x64.tar.gz", string(payload), "#!/bin/sh\necho pinned\n")
	baseURL, _ := serveArchive(t, artifact.name, payload)
	useReleaseBaseURL(t, baseURL)

	stateDir := t.TempDir()
	_, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact)
	if err == nil || !strings.Contains(err.Error(), "integrity verification") {
		t.Fatalf("expected the executable digest to be refused, got %v", err)
	}
	if _, statErr := os.Stat(gitleaksBinaryPath(stateDir, artifact)); statErr == nil {
		t.Error("a mismatched executable must not be published")
	}
	entries, readErr := os.ReadDir(gitleaksInstallDir(stateDir))
	if readErr == nil {
		for _, entry := range entries {
			t.Errorf("failed install left %s behind", entry.Name())
		}
	}
}

// TestEnsureGitleaksReplacesTamperedCache is the property a marker file could
// not provide: a modified cached executable is detected and replaced, not
// executed.
func TestEnsureGitleaksReplacesTamperedCache(t *testing.T) {
	const binary = "#!/bin/sh\necho pinned\n"
	payload := fakeTarGz(t, tarEntry{name: "gitleaks", content: binary})
	artifact := artifactFor("gitleaks_8.30.1_linux_x64.tar.gz", string(payload), binary)
	baseURL, hits := serveArchive(t, artifact.name, payload)
	useReleaseBaseURL(t, baseURL)

	stateDir := t.TempDir()
	path, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact)
	if err != nil {
		t.Fatalf("ensureGitleaksFor: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho evil\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if managedGitleaksTrusted(stateDir, artifact) {
		t.Fatal("a modified executable must not be trusted")
	}
	if _, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact); err != nil {
		t.Fatalf("repair install: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != binary {
		t.Errorf("cache was not repaired: %q", body)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Errorf("downloads = %d, want 2 (repair re-downloads)", got)
	}
}

// TestEnsureGitleaksRejectsSymlinkedCache pins the Lstat check: a symlink is
// not a regular file, so it can never be trusted even if its target matches.
func TestEnsureGitleaksRejectsSymlinkedCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks on Windows needs a privilege the test cannot assume")
	}
	const binary = "#!/bin/sh\necho pinned\n"
	payload := fakeTarGz(t, tarEntry{name: "gitleaks", content: binary})
	artifact := artifactFor("gitleaks_8.30.1_linux_x64.tar.gz", string(payload), binary)
	baseURL, _ := serveArchive(t, artifact.name, payload)
	useReleaseBaseURL(t, baseURL)

	stateDir := t.TempDir()
	path, err := ensureGitleaksFor(context.Background(), nil, stateDir, artifact)
	if err != nil {
		t.Fatalf("ensureGitleaksFor: %v", err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if managedGitleaksTrusted(stateDir, artifact) {
		t.Fatal("a symlink must not be trusted even when its target matches")
	}
}

// TestEnsureGitleaksRefusesRelativeStateDir closes the workspace-injection
// path: a project can ship .local/state/codebox/..., so a relative fallback
// must never be used for the executable cache.
func TestEnsureGitleaksRefusesRelativeStateDir(t *testing.T) {
	artifact, err := gitleaksArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ensureGitleaksFor(context.Background(), nil, filepath.Join(".local", "state", "codebox"), artifact)
	if err == nil || !strings.Contains(err.Error(), "absolute state directory") {
		t.Fatalf("expected a refusal, got %v", err)
	}
}

func TestExtractGitleaksZip(t *testing.T) {
	const binary = "MZ fake\n"
	payload := fakeZip(t, tarEntry{name: "LICENSE", content: "MIT\n"}, tarEntry{name: "gitleaks.exe", content: binary})
	archive, size := openArchive(t, payload)
	artifact := gitleaksArtifact{
		binary:       "gitleaks.exe",
		binarySHA256: digestOf([]byte(binary)),
		zip:          true,
	}
	dir := t.TempDir()
	staged, err := extractGitleaks(archive, size, dir, artifact)
	if err != nil {
		t.Fatalf("extractGitleaks: %v", err)
	}
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != binary {
		t.Errorf("extracted content = %q", body)
	}
}

// TestExtractGitleaksRejectsHostileArchives covers the member shapes a crafted
// release (or a corrupted cache) could carry.
func TestExtractGitleaksRejectsHostileArchives(t *testing.T) {
	const binary = "#!/bin/sh\necho pinned\n"
	digest := digestOf([]byte(binary))

	t.Run("tar duplicate member", func(t *testing.T) {
		payload := fakeTarGz(t,
			tarEntry{name: "gitleaks", content: binary},
			tarEntry{name: "./gitleaks", content: "#!/bin/sh\necho second\n"},
		)
		archive, size := openArchive(t, payload)
		_, err := extractGitleaks(archive, size, t.TempDir(), gitleaksArtifact{binary: "gitleaks", binarySHA256: digest})
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected a duplicate refusal, got %v", err)
		}
	})

	t.Run("zip duplicate member", func(t *testing.T) {
		payload := fakeZip(t,
			tarEntry{name: "gitleaks.exe", content: "MZ one\n"},
			tarEntry{name: "bin/gitleaks.exe", content: "MZ two\n"},
		)
		archive, size := openArchive(t, payload)
		artifact := gitleaksArtifact{binary: "gitleaks.exe", binarySHA256: digestOf([]byte("MZ one\n")), zip: true}
		_, err := extractGitleaks(archive, size, t.TempDir(), artifact)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected a duplicate refusal, got %v", err)
		}
	})

	t.Run("tar symlink member", func(t *testing.T) {
		payload := fakeTarGz(t, tarEntry{name: "gitleaks", typeflag: tar.TypeSymlink, linkname: "/bin/sh"})
		archive, size := openArchive(t, payload)
		_, err := extractGitleaks(archive, size, t.TempDir(), gitleaksArtifact{binary: "gitleaks", binarySHA256: digest})
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("expected a symlink refusal, got %v", err)
		}
	})

	t.Run("missing member", func(t *testing.T) {
		payload := fakeTarGz(t, tarEntry{name: "LICENSE", content: "MIT\n"})
		archive, size := openArchive(t, payload)
		_, err := extractGitleaks(archive, size, t.TempDir(), gitleaksArtifact{binary: "gitleaks", binarySHA256: digest})
		if err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Fatalf("expected a missing-member refusal, got %v", err)
		}
	})
}

// TestCopyBoundedRejectsOversizedStream pins the sink's advertised limit: the
// helper refuses rather than silently truncating.
func TestCopyBoundedRejectsOversizedStream(t *testing.T) {
	var buf bytes.Buffer
	if _, err := copyBounded(&buf, strings.NewReader("0123456789"), 5); err == nil {
		t.Fatal("an oversized stream must be refused")
	}
	buf.Reset()
	n, err := copyBounded(&buf, strings.NewReader("01234"), 5)
	if err != nil || n != 5 {
		t.Fatalf("exact-size copy = %d, %v; want 5, nil", n, err)
	}
}

// TestProbeGitleaksUsesBundledScanner proves the probe no longer depends on
// anything installed on the host: PATH holds no scanner, and the only one
// available is the path the resolver hands over.
func TestProbeGitleaksUsesBundledScanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stub is a POSIX shell script")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "scanner")
	report := `[{"RuleID":"generic-api-key","File":"config.txt","StartLine":3,"Match":"REDACTED"}]`
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s' '"+report+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	orig := gitleaksExecutable
	gitleaksExecutable = func(context.Context) (string, error) { return stub, nil }
	t.Cleanup(func() { gitleaksExecutable = orig })

	probe, err := probeGitleaks(context.Background(), dir)
	if err != nil {
		t.Fatalf("probeGitleaks: %v", err)
	}
	if probe.missing {
		t.Fatal("a resolvable bundled scanner must not be reported missing")
	}
	if len(probe.findings) != 1 {
		t.Fatalf("findings = %v, want one", probe.findings)
	}
	if probe.findings[0].RuleID != "generic-api-key" || probe.findings[0].File != "config.txt" {
		t.Errorf("finding = %+v", probe.findings[0])
	}
}

// TestProbeGitleaksUnavailableIsNotFatal keeps the documented degradation: the
// dotenv half of the filter still applies, the cause is retained for
// diagnostics, and nothing user-facing names a tool to install.
func TestProbeGitleaksUnavailableIsNotFatal(t *testing.T) {
	orig := gitleaksExecutable
	gitleaksExecutable = func(context.Context) (string, error) {
		return "", fmt.Errorf("secret scanner download failed: HTTP 503")
	}
	t.Cleanup(func() { gitleaksExecutable = orig })

	probe, err := probeGitleaks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("an unavailable scanner must not fail the transfer: %v", err)
	}
	if !probe.missing {
		t.Fatal("unavailable scanner must be reported missing")
	}
	if !strings.Contains(probe.detail, "503") {
		t.Errorf("detail = %q, want the underlying cause", probe.detail)
	}
	assertToolAgnostic(t, workspaceScanUnavailableWarning())
}

// TestTransferSummaryStaysToolAgnostic pins the review-screen wording when the
// scan could not run: it says what did not happen, and the technical cause
// stays out of the rendered text.
func TestTransferSummaryStaysToolAgnostic(t *testing.T) {
	man := TransferManifest{
		GitleaksMissing: true,
		GitleaksDetail:  "secret scanner download failed: https://github.com/gitleaks/gitleaks/...",
	}
	summary := man.Summary()
	if !strings.Contains(strings.ToLower(summary), "secret detection did not run") {
		t.Fatalf("summary must say the detection did not run: %s", summary)
	}
	assertToolAgnostic(t, summary)
	if strings.Contains(summary, "github.com") {
		t.Errorf("summary must not leak the release URL: %s", summary)
	}
}

func assertToolAgnostic(t *testing.T, text string) {
	t.Helper()
	lower := strings.ToLower(text)
	for _, forbidden := range []string{"gitleaks", "install", "http", "github", "download"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("user-facing text must not contain %q: %s", forbidden, text)
		}
	}
}

func TestArchiveBaseNameAcceptsBothSeparators(t *testing.T) {
	for _, name := range []string{"gitleaks", "bin/gitleaks", "bin\\gitleaks", "./gitleaks"} {
		if got := archiveBaseName(name); got != "gitleaks" {
			t.Errorf("archiveBaseName(%q) = %q, want gitleaks", name, got)
		}
	}
	if got := archiveBaseName("..\\..\\etc\\passwd"); got != "passwd" {
		t.Errorf("archiveBaseName of an escaping path = %q, want the base name", got)
	}
}

// TestEnsureGitleaksDownloadsPinnedRelease is opt-in: it fetches the real
// release over the network and runs it. It is the only test that exercises the
// published digests, so it is also the check to run when bumping
// gitleaksVersion.
func TestEnsureGitleaksDownloadsPinnedRelease(t *testing.T) {
	if os.Getenv("CODEBOX_TEST_GITLEAKS_DOWNLOAD") == "" {
		t.Skip("set CODEBOX_TEST_GITLEAKS_DOWNLOAD=1 to download the pinned release")
	}
	stateDir := t.TempDir()
	path, err := ensureGitleaks(context.Background(), nil, stateDir)
	if err != nil {
		t.Fatalf("ensureGitleaks: %v", err)
	}
	version, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("run %s version: %v (%s)", path, err, version)
	}
	if !strings.Contains(string(version), gitleaksVersion) {
		t.Fatalf("installed scanner reports %q, want %s", strings.TrimSpace(string(version)), gitleaksVersion)
	}

	// A real scan, through the injected resolver, with nothing on PATH. The
	// AWS documented example key is deliberately not used: gitleaks
	// whitelists it.
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "config.txt"),
		[]byte("password: \"hunter2correcthorsebatterystaple12345\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	orig := gitleaksExecutable
	gitleaksExecutable = func(context.Context) (string, error) { return path, nil }
	t.Cleanup(func() { gitleaksExecutable = orig })

	probe, err := probeGitleaks(context.Background(), workspace)
	if err != nil {
		t.Fatalf("probeGitleaks: %v", err)
	}
	if probe.missing || len(probe.findings) == 0 {
		t.Fatalf("the pinned scanner reported nothing for a secret-shaped file: %+v", probe)
	}
}
