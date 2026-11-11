package codebox

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// codebox ships its own secret scanner (issue #177).
//
// The workspace transfer filter excludes files a secret scan flags, so the
// scanner is part of the product, not a prerequisite the user has to know
// about. Like the Microsandbox runtime, the binary is fetched from an
// immutable upstream release. Two digests are baked into this build and both
// are enforced: the archive's, checked before anything is unpacked, and the
// extracted executable's, checked after extraction and again on every reuse.
// The second one is what makes the cache trustworthy: a marker file can be
// forged by anyone who can write the state directory, a digest cannot.
//
// Pinning also makes findings reproducible: every user runs the same rule set,
// so a file excluded on one machine is excluded on another.
const (
	gitleaksVersion     = "8.30.1"
	gitleaksReleaseURL  = "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1"
	gitleaksHTTPTimeout = 3 * time.Minute
	// gitleaksMaxArchive bounds the download; the release archives are ~10-15
	// MB, so the caps are slack, not targets.
	gitleaksMaxArchive = 64 << 20
	gitleaksMaxBinary  = 128 << 20
)

// gitleaksReleaseBaseURL is the release the download comes from. It is a
// variable so tests can serve a fake archive from a local server; production
// always uses the pinned release URL above.
var gitleaksReleaseBaseURL = gitleaksReleaseURL

// gitleaksArtifact is one platform's release archive and the executable
// inside it. Both digests were taken from the published release and
// re-computed from the downloaded archives and their extracted binaries
// (v8.30.1) before being baked in here.
type gitleaksArtifact struct {
	name string
	// sha256 covers the archive.
	sha256 string
	// binary is the executable's name inside the archive.
	binary string
	// binarySHA256 covers the extracted executable.
	binarySHA256 string
	zip          bool
}

func gitleaksArtifactFor(goos, goarch string) (gitleaksArtifact, error) {
	var platform string
	switch goarch {
	case "amd64":
		platform = "x64"
	case "arm64":
		platform = "arm64"
	default:
		return gitleaksArtifact{}, fmt.Errorf("unsupported architecture for the secret scanner: %s", goarch)
	}

	artifact := gitleaksArtifact{
		name:   fmt.Sprintf("gitleaks_%s_%s_%s", gitleaksVersion, goos, platform),
		binary: "gitleaks",
	}
	switch goos + "/" + goarch {
	case "darwin/arm64":
		artifact.sha256 = "b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5"
		artifact.binarySHA256 = "ba52fb1bfabbcde42f032afad3d6e0b19dff8ed105229a16e7caa338bbc0e84f"
	case "darwin/amd64":
		artifact.sha256 = "dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709"
		artifact.binarySHA256 = "cee01fea7173f1b779dff188e1c26ecbcb4027d394acc573b23aaf0be260e291"
	case "linux/arm64":
		artifact.sha256 = "e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080"
		artifact.binarySHA256 = "00e91bbe655bd7c47753e8cfe61cb76ea1a5d7e7702fe161ee40102b46b3823b"
	case "linux/amd64":
		artifact.sha256 = "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"
		artifact.binarySHA256 = "88f91962aa2f93ac6ab281d553b9e125f5197bbbce38f9f2437f7299c32e5509"
	case "windows/arm64":
		artifact.sha256 = "b95f5e4f5c425cedca7ee203d9afd29597e692c4924a12ed42f970537c72cc0f"
		artifact.binarySHA256 = "200df852fdecbedb19a33960657333cba5e231740bc8968972b507b50f93b194"
		artifact.binary = "gitleaks.exe"
		artifact.zip = true
	case "windows/amd64":
		artifact.sha256 = "d29144deff3a68aa93ced33dddf84b7fdc26070add4aa0f4513094c8332afc4e"
		artifact.binarySHA256 = "17157e2ee8b76fc8b1d8bee607a250e34b8a8023c8bc81822d4b5ee4d78fcb7c"
		artifact.binary = "gitleaks.exe"
		artifact.zip = true
	default:
		return gitleaksArtifact{}, fmt.Errorf("unsupported platform for the secret scanner: %s/%s", goos, goarch)
	}
	if artifact.zip {
		artifact.name += ".zip"
	} else {
		artifact.name += ".tar.gz"
	}
	return artifact, nil
}

// gitleaksInstallDir is the per-version directory under the host state
// directory. The version in the name keeps an upgrade side-by-side with the
// previous binary instead of overwriting one a running process may use.
func gitleaksInstallDir(stateDir string) string {
	return filepath.Join(stateDir, "tools", "gitleaks-"+gitleaksVersion)
}

func gitleaksBinaryPath(stateDir string, artifact gitleaksArtifact) string {
	return filepath.Join(gitleaksInstallDir(stateDir), artifact.binary)
}

// gitleaksExecutable resolves the pinned scanner, installing it on first use.
// It is a variable so tests can inject a stub: the real resolver downloads
// from the network, which a unit test must never do.
var gitleaksExecutable = func(ctx context.Context) (string, error) {
	return ensureGitleaks(ctx, nil, DefaultStateDir())
}

// ensureGitleaks returns the path of the verified executable, downloading and
// installing it when this host does not have the pinned build yet.
func ensureGitleaks(ctx context.Context, client *http.Client, stateDir string) (string, error) {
	artifact, err := gitleaksArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	return ensureGitleaksFor(ctx, client, stateDir, artifact)
}

// ensureGitleaksFor installs one specific artifact. Kept separate from
// ensureGitleaks so tests can drive a fake archive whose digests they own.
func ensureGitleaksFor(ctx context.Context, client *http.Client, stateDir string, artifact gitleaksArtifact) (string, error) {
	// A relative state directory would put the executable cache inside the
	// current working directory — a project could then ship its own "trusted"
	// scanner. Refuse rather than install somewhere a checkout can reach.
	if !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("secret scanner needs an absolute state directory, got %q", stateDir)
	}
	if managedGitleaksTrusted(stateDir, artifact) {
		return gitleaksBinaryPath(stateDir, artifact), nil
	}

	dir := gitleaksInstallDir(stateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create secret scanner directory: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: gitleaksHTTPTimeout}
	}
	archivePath, err := downloadGitleaksArchive(ctx, client, dir, artifact)
	if err != nil {
		return "", err
	}
	defer os.Remove(archivePath)

	archive, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open secret scanner archive: %w", err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect secret scanner archive: %w", err)
	}
	// Hash the descriptor, then rewind it and extract from the SAME
	// descriptor: verifying by path and reopening would let a local writer
	// swap the bytes in between.
	if err := verifySHA256(io.LimitReader(archive, gitleaksMaxArchive+1), artifact.sha256); err != nil {
		return "", fmt.Errorf("secret scanner archive failed integrity verification")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind secret scanner archive: %w", err)
	}

	staged, err := extractGitleaks(archive, info.Size(), dir, artifact)
	if err != nil {
		return "", err
	}
	defer os.Remove(staged)
	if err := publishGitleaks(staged, gitleaksBinaryPath(stateDir, artifact)); err != nil {
		// A concurrent install may have published the same bytes first.
		if managedGitleaksTrusted(stateDir, artifact) {
			return gitleaksBinaryPath(stateDir, artifact), nil
		}
		return "", err
	}
	if !managedGitleaksTrusted(stateDir, artifact) {
		return "", fmt.Errorf("secret scanner failed integrity verification after installation")
	}
	return gitleaksBinaryPath(stateDir, artifact), nil
}

func downloadGitleaksArchive(ctx context.Context, client *http.Client, dir string, artifact gitleaksArtifact) (string, error) {
	file, err := os.CreateTemp(dir, ".scanner-download-*")
	if err != nil {
		return "", fmt.Errorf("create secret scanner download: %w", err)
	}
	archivePath := file.Name()
	cleanup := func() {
		file.Close()
		os.Remove(archivePath)
	}

	url := gitleaksReleaseBaseURL + "/" + artifact.name
	reqCtx, cancel := context.WithTimeout(ctx, gitleaksHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		cleanup()
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("secret scanner download failed: %s", transportDetail(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", fmt.Errorf("secret scanner download failed: HTTP %d", resp.StatusCode)
	}
	n, err := io.Copy(file, io.LimitReader(resp.Body, gitleaksMaxArchive+1))
	if err != nil {
		cleanup()
		return "", fmt.Errorf("secret scanner download failed: %s", transportDetail(err))
	}
	if n > gitleaksMaxArchive {
		cleanup()
		return "", fmt.Errorf("secret scanner archive exceeds %d bytes", gitleaksMaxArchive)
	}
	if err := file.Close(); err != nil {
		os.Remove(archivePath)
		return "", err
	}
	return archivePath, nil
}

// transportDetail keeps the cause of a transport failure without the request
// URL: the URL names the tool, and the user-facing sentence must not.
func transportDetail(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}

// extractGitleaks unpacks exactly one executable into a staging file next to
// the final destination, verifies its digest, and returns the staged path. The
// whole archive is read before returning: a malformed tail or a duplicate
// member must fail the install, not be discovered after the executable is in
// place.
func extractGitleaks(archive io.ReaderAt, size int64, dir string, artifact gitleaksArtifact) (string, error) {
	staged, err := os.CreateTemp(dir, ".scanner-bin-*")
	if err != nil {
		return "", fmt.Errorf("create staged secret scanner: %w", err)
	}
	stagedPath := staged.Name()
	cleanup := func() {
		staged.Close()
		os.Remove(stagedPath)
	}
	hasher := sha256.New()
	var copies int
	if artifact.zip {
		copies, err = copyGitleaksFromZip(archive, size, io.MultiWriter(staged, hasher), artifact)
	} else {
		copies, err = copyGitleaksFromTar(archive, io.MultiWriter(staged, hasher), artifact)
	}
	if err != nil {
		cleanup()
		return "", err
	}
	if copies != 1 {
		cleanup()
		return "", fmt.Errorf("secret scanner archive must contain exactly one %s (found %d)", artifact.binary, copies)
	}
	if err := staged.Close(); err != nil {
		os.Remove(stagedPath)
		return "", err
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != artifact.binarySHA256 {
		os.Remove(stagedPath)
		return "", fmt.Errorf("secret scanner executable failed integrity verification")
	}
	return stagedPath, nil
}

func copyGitleaksFromTar(archive io.ReaderAt, dst io.Writer, artifact gitleaksArtifact) (int, error) {
	section := io.NewSectionReader(archive, 0, gitleaksMaxArchive)
	gz, err := gzip.NewReader(section)
	if err != nil {
		return 0, fmt.Errorf("secret scanner archive is not valid gzip")
	}
	defer gz.Close()

	tarReader := tar.NewReader(gz)
	copies := 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			return copies, nil
		}
		if err != nil {
			return copies, fmt.Errorf("secret scanner archive is corrupt")
		}
		if archiveBaseName(header.Name) != artifact.binary {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return copies, fmt.Errorf("secret scanner archive member %s is not a regular file", artifact.binary)
		}
		if copies > 0 {
			return copies, fmt.Errorf("secret scanner archive contains duplicate %s members", artifact.binary)
		}
		if header.Size < 0 || header.Size > gitleaksMaxBinary {
			return copies, fmt.Errorf("secret scanner executable exceeds %d bytes", gitleaksMaxBinary)
		}
		if _, err := copyBounded(dst, tarReader, gitleaksMaxBinary); err != nil {
			return copies, err
		}
		copies++
	}
}

func copyGitleaksFromZip(archive io.ReaderAt, size int64, dst io.Writer, artifact gitleaksArtifact) (int, error) {
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return 0, fmt.Errorf("secret scanner archive is not a valid zip")
	}
	copies := 0
	for _, entry := range reader.File {
		if archiveBaseName(entry.Name) != artifact.binary {
			continue
		}
		if !entry.Mode().IsRegular() {
			return copies, fmt.Errorf("secret scanner archive member %s is not a regular file", artifact.binary)
		}
		if copies > 0 {
			return copies, fmt.Errorf("secret scanner archive contains duplicate %s members", artifact.binary)
		}
		if entry.UncompressedSize64 > gitleaksMaxBinary {
			return copies, fmt.Errorf("secret scanner executable exceeds %d bytes", gitleaksMaxBinary)
		}
		body, err := entry.Open()
		if err != nil {
			return copies, fmt.Errorf("secret scanner archive is corrupt")
		}
		_, copyErr := copyBounded(dst, body, gitleaksMaxBinary)
		body.Close()
		if copyErr != nil {
			return copies, copyErr
		}
		copies++
	}
	return copies, nil
}

// copyBounded copies and refuses to truncate: a reader that yields more than
// the cap is an error, not a silently shortened binary.
func copyBounded(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, fmt.Errorf("unpack secret scanner: %w", err)
	}
	if n > limit {
		return n, fmt.Errorf("secret scanner executable exceeds %d bytes", limit)
	}
	return n, nil
}

// publishGitleaks renames the staged file into place. The rename is atomic, so
// a concurrent reader sees either no executable or the complete one.
func publishGitleaks(staged, destination string) error {
	if err := os.Chmod(staged, 0o755); err != nil {
		return fmt.Errorf("prepare secret scanner executable: %w", err)
	}
	if err := os.Rename(staged, destination); err != nil {
		return fmt.Errorf("publish secret scanner executable: %w", err)
	}
	return nil
}

// managedGitleaksTrusted reports whether this host already has the pinned
// build. The check is the executable's own digest — not a marker file, which
// anyone able to write the state directory could forge — and the path must be
// a regular file: a symlink would let the digest describe one file while
// execution follows another.
func managedGitleaksTrusted(stateDir string, artifact gitleaksArtifact) bool {
	path := gitleaksBinaryPath(stateDir, artifact)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > gitleaksMaxBinary {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	return verifySHA256(file, artifact.binarySHA256) == nil
}

// archiveBaseName reduces an archive member name to its base, accepting both
// separators: a crafted archive (or a zip written on Windows) can carry
// backslashes, and path.Base alone would keep the whole name and miss the
// executable — or, worse, treat an escaping path as a plain name.
func archiveBaseName(name string) string {
	return path.Base(strings.ReplaceAll(name, `\`, "/"))
}
