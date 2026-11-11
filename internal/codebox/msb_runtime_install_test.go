package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

func TestMSBRuntimeVersionMatchesSDK(t *testing.T) {
	if got := msb.SDKVersion(); got != msbRuntimeVersion {
		t.Fatalf("Microsandbox SDK version = %s, hosted runtime = %s; update both together", got, msbRuntimeVersion)
	}
}

func TestRuntimeDownloadProgressReportsBytes(t *testing.T) {
	var progress *runtimeDownloadProgress
	out := captureStderr(t, func() {
		progress = &runtimeDownloadProgress{total: 2 << 20, enabled: true}
		if n, err := progress.Write(make([]byte, 1<<20)); err != nil || n != 1<<20 {
			t.Fatalf("progress write = %d, %v", n, err)
		}
		progress.finish()
	})
	for _, want := range []string{"[1/3] Téléchargement", "█████░░░░░", "1.0 Mo / 2.0 Mo"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output %q does not contain %q", out, want)
		}
	}
}

func TestMSBRuntimeArtifacts(t *testing.T) {
	tests := []struct {
		goos, goarch string
		name         string
		sha256       string
	}{
		{"darwin", "arm64", "microsandbox-darwin-aarch64.tar.gz", "9cc2bd2874cc1aa98cb139429a9482830b1e64b48255aa9f40a25b8979a23a9f"},
		{"linux", "arm64", "microsandbox-linux-aarch64.tar.gz", "a0c54cd06bea4d2a5bd3fc291063f8f2f5e073232db78f6860c527922ec71906"},
		{"linux", "amd64", "microsandbox-linux-x86_64.tar.gz", "34d49a7388685f0ea24bfbeecf6864a44718959b57087f1d7481775672db9585"},
		{"windows", "arm64", "microsandbox-windows-aarch64.tar.gz", "c543bd33fdcb03cfbb9a16b7d91632643d52f31d2538715a34595edaa49748e4"},
		{"windows", "amd64", "microsandbox-windows-x86_64.tar.gz", "e97223e5c4dc46142244d418ba0685a2ffb94993ac8d60b955a59ed5364692bd"},
	}
	for _, test := range tests {
		t.Run(test.goos+"-"+test.goarch, func(t *testing.T) {
			artifact, err := msbRuntimeArtifactFor(test.goos, test.goarch)
			if err != nil {
				t.Fatal(err)
			}
			if artifact.name != test.name || artifact.sha256 != test.sha256 {
				t.Fatalf("artifact = %+v, want name %q and SHA-256 %q", artifact, test.name, test.sha256)
			}
		})
	}
	for _, unsupported := range [][2]string{{"darwin", "amd64"}, {"freebsd", "amd64"}, {"linux", "riscv64"}} {
		if _, err := msbRuntimeArtifactFor(unsupported[0], unsupported[1]); err == nil {
			t.Errorf("msbRuntimeArtifactFor(%q, %q): expected error", unsupported[0], unsupported[1])
		}
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("verified runtime")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := verifySHA256(bytes.NewReader(data), digest); err != nil {
		t.Fatalf("verifySHA256(valid): %v", err)
	}
	if err := verifySHA256(strings.NewReader("corrupted runtime"), digest); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("verifySHA256(corrupt) = %v, want mismatch", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCorruptedMSBRuntimeIsRejectedBeforeExtraction(t *testing.T) {
	home := t.TempDir()
	artifact := msbRuntimeArtifact{
		name:    "microsandbox-test.tar.gz",
		sha256:  strings.Repeat("0", 64),
		msbName: "msb",
		libName: "libkrunfw.so.5.6.1",
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/"+artifact.name) {
			t.Fatalf("download path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("not the expected archive")),
			Header:     make(http.Header),
		}, nil
	})}

	err := downloadAndInstallMSBRuntime(context.Background(), client, home, artifact)
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("downloadAndInstallMSBRuntime = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(home, "bin", artifact.msbName)); !os.IsNotExist(err) {
		t.Fatalf("corrupt archive was extracted: %v", err)
	}
	if _, err := os.Stat(msbRuntimeMarker(home)); !os.IsNotExist(err) {
		t.Fatalf("corrupt archive was marked trusted: %v", err)
	}
}

func TestExtractMSBRuntime(t *testing.T) {
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	archive := runtimeArchive(t, map[string]string{
		artifact.msbName: "runtime-binary",
		artifact.libName: "firmware-library",
		"ignored.txt":    "not installed",
	})
	home := t.TempDir()
	if err := extractMSBRuntime(bytes.NewReader(archive), home, artifact); err != nil {
		t.Fatalf("extractMSBRuntime: %v", err)
	}
	assertFileContents(t, filepath.Join(home, "bin", artifact.msbName), "runtime-binary")
	assertFileContents(t, filepath.Join(home, "lib", artifact.libName), "firmware-library")
	if _, err := os.Stat(filepath.Join(home, "bin", "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("unexpected archive entry was extracted: %v", err)
	}
	for _, link := range artifact.libSymlink {
		target, err := os.Readlink(filepath.Join(home, "lib", link[0]))
		if err != nil {
			t.Fatalf("read symlink %s: %v", link[0], err)
		}
		if target != link[1] {
			t.Errorf("symlink %s -> %s, want %s", link[0], target, link[1])
		}
	}
}

func TestExtractMSBRuntimeRequiresBothFiles(t *testing.T) {
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	archive := runtimeArchive(t, map[string]string{artifact.msbName: "runtime-binary"})
	err = extractMSBRuntime(bytes.NewReader(archive), t.TempDir(), artifact)
	if err == nil || !strings.Contains(err.Error(), artifact.libName) {
		t.Fatalf("extractMSBRuntime = %v, want missing library error", err)
	}
}

func TestManagedMSBRuntimeTrustMarker(t *testing.T) {
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	home := t.TempDir()
	if managedMSBRuntimeTrusted(home, artifact) {
		t.Fatal("runtime without files or marker must not be trusted")
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", artifact.msbName), []byte("msb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "lib", artifact.libName), []byte("lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, link := range artifact.libSymlink {
		if err := os.Symlink(link[1], filepath.Join(home, "lib", link[0])); err != nil {
			t.Fatal(err)
		}
	}
	if managedMSBRuntimeTrusted(home, artifact) {
		t.Fatal("runtime files without provenance marker must not be trusted")
	}
	if err := writeMSBRuntimeMarker(home, artifact); err != nil {
		t.Fatal(err)
	}
	if !managedMSBRuntimeTrusted(home, artifact) {
		t.Fatal("runtime with matching files and provenance marker should be trusted")
	}
	if len(artifact.libSymlink) > 0 {
		if err := os.Remove(filepath.Join(home, "lib", artifact.libSymlink[0][0])); err != nil {
			t.Fatal(err)
		}
		if managedMSBRuntimeTrusted(home, artifact) {
			t.Fatal("runtime with a missing library symlink must not be trusted")
		}
		if err := os.Symlink(artifact.libSymlink[0][1], filepath.Join(home, "lib", artifact.libSymlink[0][0])); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(msbRuntimeMarker(home), []byte(strings.Repeat("f", 64)), 0o644); err != nil {
		t.Fatal(err)
	}
	if managedMSBRuntimeTrusted(home, artifact) {
		t.Fatal("runtime with mismatched provenance marker must not be trusted")
	}
}

func TestRuntimeInstallCheckRecognizesManagedMicrosandbox(t *testing.T) {
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	home := t.TempDir()
	t.Setenv("MSB_HOME", home)
	t.Setenv("MSB_PATH", "")
	t.Setenv("MSB_LIBKRUNFW_PATH", "")
	if installed, hint := RuntimeInstallCheck(RuntimeMicrosandbox); installed || hint != "codebox setup" {
		t.Fatalf("missing runtime check = %v, %q", installed, hint)
	}
	archive := runtimeArchive(t, map[string]string{artifact.msbName: "runtime", artifact.libName: "library"})
	if err := extractMSBRuntime(bytes.NewReader(archive), home, artifact); err != nil {
		t.Fatal(err)
	}
	if err := writeMSBRuntimeMarker(home, artifact); err != nil {
		t.Fatal(err)
	}
	if installed, _ := RuntimeInstallCheck(RuntimeMicrosandbox); !installed {
		t.Fatal("a trusted managed runtime must be recognized")
	}
	// An explicit but broken override must not be hidden by the managed copy.
	t.Setenv("MSB_PATH", filepath.Join(home, "missing"))
	if installed, _ := RuntimeInstallCheck(RuntimeMicrosandbox); installed {
		t.Fatal("an invalid explicit runtime override must be reported")
	}
}

func TestExternalMSBRuntimeRequiresBothPaths(t *testing.T) {
	t.Setenv("MSB_PATH", filepath.Join(t.TempDir(), "msb"))
	t.Setenv("MSB_LIBKRUNFW_PATH", "")
	external, err := validateExternalMSBRuntime(context.Background())
	if !external || err == nil || !strings.Contains(err.Error(), "set both") {
		t.Fatalf("validateExternalMSBRuntime = %v, %v; want configured error", external, err)
	}
}

func TestExternalMSBRuntimeValidatesVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
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
	external, err := validateExternalMSBRuntime(context.Background())
	if !external || err != nil {
		t.Fatalf("validateExternalMSBRuntime = %v, %v; want valid external runtime", external, err)
	}
}

func TestSDKDoctorRejectsIncompleteExternalRuntime(t *testing.T) {
	t.Setenv("MSB_PATH", filepath.Join(t.TempDir(), "msb"))
	t.Setenv("MSB_LIBKRUNFW_PATH", "")
	_, err := (sdkMSBClient{}).Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "set both MSB_PATH and MSB_LIBKRUNFW_PATH") {
		t.Fatalf("Doctor error = %v, want external runtime pair diagnostic", err)
	}
}

func TestSDKDoctorRejectsUntrustedManagedRuntime(t *testing.T) {
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("unsupported test platform: %v", err)
	}
	home := t.TempDir()
	for _, dir := range []string{"bin", "lib"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "bin", artifact.msbName), []byte("untrusted executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "lib", artifact.libName), []byte("runtime library"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, link := range artifact.libSymlink {
		if err := os.Symlink(link[1], filepath.Join(home, "lib", link[0])); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MSB_HOME", home)
	t.Setenv("MSB_PATH", "")
	t.Setenv("MSB_LIBKRUNFW_PATH", "")

	_, err = (sdkMSBClient{}).Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed integrity checks") {
		t.Fatalf("Doctor error = %v, want untrusted managed runtime diagnostic", err)
	}
	if _, err := os.Stat(msbRuntimeMarker(home)); !os.IsNotExist(err) {
		t.Fatalf("Doctor wrote a trust marker: stat error = %v", err)
	}
}

func TestSDKDoctorRejectsExternalRuntimeVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-only")
	}
	dir := t.TempDir()
	msbPath := filepath.Join(dir, "msb")
	libPath := filepath.Join(dir, "libkrunfw")
	if err := os.WriteFile(msbPath, []byte("#!/bin/sh\necho 'msb 0.7.2'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(libPath, []byte("library"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MSB_PATH", msbPath)
	t.Setenv("MSB_LIBKRUNFW_PATH", libPath)

	_, err := (sdkMSBClient{}).Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "requires msb "+msbRuntimeVersion) {
		t.Fatalf("Doctor error = %v, want external runtime version diagnostic", err)
	}
}

func TestMSBRuntimeBinaryHonorsOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom-msb")
	t.Setenv("MSB_PATH", want)
	got, err := msbRuntimeBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("msbRuntimeBinary = %q, want %q", got, want)
	}
}

func runtimeArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s = %q, want %q", path, contents, want)
	}
}
