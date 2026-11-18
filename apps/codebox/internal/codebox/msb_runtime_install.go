package codebox

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
	"golang.org/x/term"
)

// msbRuntimeReleaseURL is the upstream Microsandbox release the runtime is
// fetched from. Upstream publishes immutable releases, so the archives cannot
// be swapped after publication, and GitHub issues a Sigstore-signed release
// attestation covering them (see msb-runtime-watch.yml). The download is
// nevertheless verified against the digest baked in msbRuntimeArtifactFor
// before extraction: that digest is the trust anchor, and the download source
// is untrusted by design. A local mirror is deliberately not kept, because it
// would not add any guarantee.
const (
	msbRuntimeVersion     = "0.7.15"
	msbRuntimeReleaseURL  = "https://github.com/superradcompany/microsandbox/releases/download/v0.7.15"
	msbRuntimeHTTPTimeout = 5 * time.Minute
	msbRuntimeMaxArchive  = 128 << 20
	msbRuntimeMaxFile     = 64 << 20
)

type msbRuntimeArtifact struct {
	name       string
	sha256     string
	msbName    string
	libName    string
	libSymlink [][2]string
}

func msbRuntimeArtifactFor(goos, goarch string) (msbRuntimeArtifact, error) {
	platform := ""
	switch goarch {
	case "amd64":
		platform = "x86_64"
	case "arm64":
		platform = "aarch64"
	default:
		return msbRuntimeArtifact{}, fmt.Errorf("architecture Microsandbox non prise en charge : %s", goarch)
	}

	var artifact msbRuntimeArtifact
	switch goos + "/" + goarch {
	case "darwin/arm64":
		artifact = msbRuntimeArtifact{
			sha256:     "99b1b0e1d5a30a30e12b4371a339e1683e57f7d12a57be32e63198c8809e1f3d",
			msbName:    "msb",
			libName:    "libkrunfw.5.dylib",
			libSymlink: [][2]string{{"libkrunfw.dylib", "libkrunfw.5.dylib"}},
		}
	case "linux/arm64":
		artifact = msbRuntimeArtifact{
			sha256:  "1ace5e63bb5793034f0e8afe7d73c6af3e4a2b05cb5b9287cb2da42520a293d5",
			msbName: "msb",
			libName: "libkrunfw.so.5.6.1",
			libSymlink: [][2]string{
				{"libkrunfw.so.5", "libkrunfw.so.5.6.1"},
				{"libkrunfw.so", "libkrunfw.so.5"},
			},
		}
	case "linux/amd64":
		artifact = msbRuntimeArtifact{
			sha256:  "37ffc7bf7b2b977ab004f620f47ae4466f021a9b880c94f46f697c56cf6932b7",
			msbName: "msb",
			libName: "libkrunfw.so.5.6.1",
			libSymlink: [][2]string{
				{"libkrunfw.so.5", "libkrunfw.so.5.6.1"},
				{"libkrunfw.so", "libkrunfw.so.5"},
			},
		}
	case "windows/arm64":
		artifact = msbRuntimeArtifact{
			sha256:  "48f80a41148476c286b13d81d3e3fc501319c8c3f5f651a1eff5bf009f3d5bab",
			msbName: "msb.exe",
			libName: "libkrunfw.dll",
		}
	case "windows/amd64":
		artifact = msbRuntimeArtifact{
			sha256:  "615d84ebf764cfb960f8f6256f8eade13646fec43957824ee173b3f8edfbe633",
			msbName: "msb.exe",
			libName: "libkrunfw.dll",
		}
	default:
		return msbRuntimeArtifact{}, fmt.Errorf("plateforme Microsandbox non prise en charge : %s/%s", goos, goarch)
	}
	artifact.name = fmt.Sprintf("microsandbox-%s-%s.tar.gz", goos, platform)
	return artifact, nil
}

// MSBSDKVersion returns the Microsandbox SDK version this build embeds. It
// backs the hidden -print-msb-sdk-version CLI flag used by CI to compare the
// embedded SDK against the latest upstream release.
func MSBSDKVersion() string {
	return msb.SDKVersion()
}

func ensureMSBRuntime(ctx context.Context, client *http.Client) error {
	if msb.SDKVersion() != msbRuntimeVersion {
		return fmt.Errorf("incompatibilité de version Microsandbox SDK/runtime : SDK %s, runtime hébergé %s", msb.SDKVersion(), msbRuntimeVersion)
	}
	if external, err := validateExternalMSBRuntime(ctx); external || err != nil {
		return err
	}

	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	home, err := msbRuntimeHome()
	if err != nil {
		return err
	}

	if managedMSBRuntimeTrusted(home, artifact) {
		if _, err := msb.ResolveRuntime(msb.RuntimeConfig{}); err == nil {
			return nil
		}
		_ = os.Remove(msbRuntimeMarker(home))
	}

	fmt.Printf("[1/3] Téléchargement de la machine isolée v%s…\n", msbRuntimeVersion)
	if client == nil {
		client = &http.Client{Timeout: msbRuntimeHTTPTimeout}
	}
	if err := downloadAndInstallMSBRuntime(ctx, client, home, artifact); err != nil {
		return err
	}
	if _, err := msb.ResolveRuntime(msb.RuntimeConfig{}); err != nil {
		_ = os.Remove(msbRuntimeMarker(home))
		return fmt.Errorf("validation du runtime Microsandbox installé : %w", err)
	}
	return writeMSBRuntimeMarker(home, artifact)
}

// validateExternalMSBRuntime preserves the SDK's direct path overrides as an
// explicit alternative to the managed, verified download.
func validateExternalMSBRuntime(ctx context.Context) (bool, error) {
	msbPath := os.Getenv("MSB_PATH")
	libPath := os.Getenv("MSB_LIBKRUNFW_PATH")
	if msbPath == "" && libPath == "" {
		return false, nil
	}
	if msbPath == "" || libPath == "" {
		return true, fmt.Errorf("définissez à la fois MSB_PATH et MSB_LIBKRUNFW_PATH pour un runtime Microsandbox installé manuellement")
	}
	for name, file := range map[string]string{"MSB_PATH": msbPath, "MSB_LIBKRUNFW_PATH": libPath} {
		info, err := os.Stat(file)
		if err != nil {
			return true, fmt.Errorf("%s pointe vers %s : %w", name, file, err)
		}
		if !info.Mode().IsRegular() {
			return true, fmt.Errorf("%s doit pointer vers un fichier régulier : %s", name, file)
		}
	}
	version, err := installedMSBVersion(ctx, msbPath)
	if err != nil {
		return true, err
	}
	if version != msbRuntimeVersion {
		return true, fmt.Errorf("MSB_PATH signale msb %s ; codebox requiert msb %s", version, msbRuntimeVersion)
	}
	return true, nil
}

// validateInstalledMSBRuntime checks the selected runtime without repairing,
// downloading, or changing its provenance marker.
func validateInstalledMSBRuntime(ctx context.Context) error {
	if msb.SDKVersion() != msbRuntimeVersion {
		return fmt.Errorf("incompatibilité de version Microsandbox SDK/runtime : SDK %s, runtime hébergé %s", msb.SDKVersion(), msbRuntimeVersion)
	}
	if external, err := validateExternalMSBRuntime(ctx); external || err != nil {
		return err
	}
	artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	home, err := msbRuntimeHome()
	if err != nil {
		return err
	}
	if !managedMSBRuntimeTrusted(home, artifact) {
		return fmt.Errorf("le runtime Microsandbox géré est absent ou a échoué aux contrôles d'intégrité ; lancez 'codebox start --microsandbox' pour l'installer ou le réparer")
	}
	return nil
}

func installedMSBVersion(ctx context.Context, binary string) (string, error) {
	out, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("exécution de %s --version : %w : %s", binary, err, strings.TrimSpace(string(out)))
	}
	version := strings.TrimSpace(string(out))
	if !strings.HasPrefix(version, "msb ") {
		return "", fmt.Errorf("sortie inattendue de %s --version : %q", binary, version)
	}
	return strings.TrimPrefix(version, "msb "), nil
}

func msbRuntimeHome() (string, error) {
	if home := os.Getenv("MSB_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("résolution du répertoire personnel : %w", err)
	}
	return filepath.Join(home, ".microsandbox"), nil
}

func msbRuntimeMarker(home string) string {
	return filepath.Join(home, fmt.Sprintf(".codebox-runtime-v%s.sha256", msbRuntimeVersion))
}

func markerContent(artifact msbRuntimeArtifact) string {
	return artifact.sha256 + "  " + artifact.name + "\n"
}

func managedMSBRuntimeTrusted(home string, artifact msbRuntimeArtifact) bool {
	marker, err := os.ReadFile(msbRuntimeMarker(home))
	if err != nil || string(marker) != markerContent(artifact) {
		return false
	}
	for _, file := range []string{
		filepath.Join(home, "bin", artifact.msbName),
		filepath.Join(home, "lib", artifact.libName),
	} {
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	for _, link := range artifact.libSymlink {
		target, err := os.Readlink(filepath.Join(home, "lib", link[0]))
		if err != nil || target != link[1] {
			return false
		}
	}
	return true
}

func downloadAndInstallMSBRuntime(ctx context.Context, client *http.Client, home string, artifact msbRuntimeArtifact) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("création du répertoire Microsandbox %s : %w", home, err)
	}
	archive, err := os.CreateTemp(home, ".codebox-msb-runtime-*.tar.gz")
	if err != nil {
		return fmt.Errorf("création du téléchargement du runtime : %w", err)
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	url := msbRuntimeReleaseURL + "/" + artifact.name
	reqCtx, cancel := context.WithTimeout(ctx, msbRuntimeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		archive.Close()
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		archive.Close()
		return fmt.Errorf("téléchargement de %s : %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		archive.Close()
		return fmt.Errorf("téléchargement de %s : HTTP %d", url, resp.StatusCode)
	}
	progress := &runtimeDownloadProgress{total: resp.ContentLength, enabled: term.IsTerminal(int(os.Stderr.Fd()))}
	n, err := io.Copy(archive, io.LimitReader(io.TeeReader(resp.Body, progress), msbRuntimeMaxArchive+1))
	progress.finish()
	if err != nil {
		archive.Close()
		return fmt.Errorf("téléchargement de %s : %w", url, err)
	}
	if n > msbRuntimeMaxArchive {
		archive.Close()
		return fmt.Errorf("téléchargement de %s : l'archive dépasse %d octets", url, msbRuntimeMaxArchive)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		archive.Close()
		return err
	}
	fmt.Fprintln(os.Stderr, "  [2/3] Vérification de l'intégrité")
	if err := verifySHA256(archive, artifact.sha256); err != nil {
		archive.Close()
		return fmt.Errorf("vérification de %s : %w", artifact.name, err)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		archive.Close()
		return err
	}
	fmt.Fprintln(os.Stderr, "  [3/3] Préparation")
	if err := extractMSBRuntime(archive, home, artifact); err != nil {
		archive.Close()
		return fmt.Errorf("extraction de %s : %w", artifact.name, err)
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return nil
}

type runtimeDownloadProgress struct {
	total   int64
	read    int64
	last    time.Time
	enabled bool
}

func (p *runtimeDownloadProgress) Write(data []byte) (int, error) {
	p.read += int64(len(data))
	if p.enabled && time.Since(p.last) >= 250*time.Millisecond {
		p.render()
	}
	return len(data), nil
}

func (p *runtimeDownloadProgress) render() {
	if p.total <= 0 {
		fmt.Fprintf(os.Stderr, "\r  [1/3] Téléchargement %s", runtimeBytes(p.read))
	} else {
		fraction := float64(p.read) / float64(p.total)
		if fraction > 1 {
			fraction = 1
		}
		filled := int(fraction * 10)
		fmt.Fprintf(os.Stderr, "\r  [1/3] Téléchargement %s %s / %s", strings.Repeat("█", filled)+strings.Repeat("░", 10-filled), runtimeBytes(p.read), runtimeBytes(p.total))
	}
	p.last = time.Now()
}

func (p *runtimeDownloadProgress) finish() {
	if p.enabled {
		p.render()
		fmt.Fprintln(os.Stderr)
	}
}

func runtimeBytes(n int64) string {
	return fmt.Sprintf("%.1f Mo", float64(n)/(1<<20))
}

func verifySHA256(r io.Reader, expected string) error {
	hash := sha256.New()
	if _, err := io.Copy(hash, r); err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if actual != strings.ToLower(expected) {
		return fmt.Errorf("empreinte SHA-256 non concordante : obtenue %s, attendue %s", actual, expected)
	}
	return nil
}

func extractMSBRuntime(r io.Reader, home string, artifact msbRuntimeArtifact) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("lecture gzip : %w", err)
	}
	defer gz.Close()

	destinations := map[string]string{
		artifact.msbName: filepath.Join(home, "bin", artifact.msbName),
		artifact.libName: filepath.Join(home, "lib", artifact.libName),
	}
	found := make(map[string]bool, len(destinations))
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("lecture tar : %w", err)
		}
		name := path.Base(header.Name)
		destination, wanted := destinations[name]
		if !wanted {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("%s n'est pas un fichier régulier", header.Name)
		}
		if found[name] {
			return fmt.Errorf("fichier de runtime en double %s", name)
		}
		if err := atomicCopyFile(destination, tarReader, 0o755); err != nil {
			return err
		}
		found[name] = true
	}
	for name := range destinations {
		if !found[name] {
			return fmt.Errorf("l'archive du runtime ne contient pas %s", name)
		}
	}
	for _, link := range artifact.libSymlink {
		linkPath := filepath.Join(home, "lib", link[0])
		if err := os.Remove(linkPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("suppression de l'ancien lien symbolique %s : %w", linkPath, err)
		}
		if err := os.Symlink(link[1], linkPath); err != nil {
			return fmt.Errorf("lien symbolique %s -> %s : %w", linkPath, link[1], err)
		}
	}
	return nil
}

func atomicCopyFile(destination string, source io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".codebox-runtime-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	n, err := io.Copy(temp, io.LimitReader(source, msbRuntimeMaxFile+1))
	if err != nil {
		temp.Close()
		return err
	}
	if n > msbRuntimeMaxFile {
		temp.Close()
		return fmt.Errorf("le fichier du runtime dépasse %d octets", msbRuntimeMaxFile)
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(destination)
	}
	if err := os.Rename(tempName, destination); err != nil {
		return fmt.Errorf("remplacement de %s : %w", destination, err)
	}
	return nil
}

func writeMSBRuntimeMarker(home string, artifact msbRuntimeArtifact) error {
	return atomicCopyFile(msbRuntimeMarker(home), strings.NewReader(markerContent(artifact)), 0o644)
}
