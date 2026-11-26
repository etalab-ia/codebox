package codebox

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	statusBarDirName          = "codebox-status"
	statusBarVersionFile      = ".codebox-version"
	statusBarStagedConfigName = "status-bar-config.json"
)

var (
	statusCoreTS         = mustAsset("status-core.ts")
	opencodeStatusTUITSX = mustAsset("opencode-status-tui.tsx")
	piStatusExtensionTS  = mustAsset("pi-status-extension.ts")
)

type statusBarConfig struct {
	ProjectName   string            `json:"projectName,omitempty"`
	ProviderNames map[string]string `json:"providerNames,omitempty"`
}

func statusBarProjectName(workspaceDir string) string {
	trimmed := strings.TrimSpace(workspaceDir)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(trimmed))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func statusBarProviderNames(providers []InferenceProvider) map[string]string {
	names := map[string]string{
		string(ProviderAlbert):  "Albert API",
		string(ProviderMistral): "Mistral AI",
		string(ProviderLocal):   "Serveur local",
	}
	for _, p := range providers {
		if p.Kind == "" || p.Kind == ProviderAlbert {
			continue
		}
		if name := strings.TrimSpace(p.Name); name != "" {
			names[string(p.Kind)] = name
		}
	}
	return names
}

func statusBarConfigJSON(cfg Config) ([]byte, error) {
	doc := statusBarConfig{
		ProjectName:   statusBarProjectName(cfg.WorkspaceDir),
		ProviderNames: statusBarProviderNames(cfg.Providers),
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// statusBarFiles returns the managed file set for harness along with its
// deterministic content digest. OpenCode and Pi share status-core.ts so
// display, limit parsing, and fallback logic stay identical across harnesses.
func statusBarFiles(harness Harness, cfg Config) (map[string][]byte, string, error) {
	configJSON, err := statusBarConfigJSON(cfg)
	if err != nil {
		return nil, "", err
	}
	return statusBarFilesWithConfigJSON(harness, configJSON)
}

func statusBarFilesWithConfigJSON(harness Harness, configJSON []byte) (map[string][]byte, string, error) {
	if len(bytes.TrimSpace(configJSON)) == 0 {
		var err error
		configJSON, err = statusBarConfigJSON(Config{})
		if err != nil {
			return nil, "", err
		}
	}
	files := map[string][]byte{
		"status-core.ts": []byte(statusCoreTS),
		"config.json":    configJSON,
	}
	if harness == HarnessPi {
		files["index.ts"] = []byte(piStatusExtensionTS)
	} else {
		files["tui.tsx"] = []byte(opencodeStatusTUITSX)
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(string(harness) + "\x00"))
	for _, name := range names {
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(files[name])
		h.Write([]byte{0})
	}
	digest := hex.EncodeToString(h.Sum(nil))
	files[statusBarVersionFile] = []byte(digest + "\n")
	return files, digest, nil
}

// statusBarDigest returns the deterministic digest of the managed status bar
// files for harness and cfg. Used to detect upgrades in InstanceState without
// triggering VM restarts or recreates.
func statusBarDigest(harness Harness, cfg Config) string {
	_, digest, err := statusBarFiles(harness, cfg)
	if err != nil {
		return ""
	}
	return digest
}

// statusBarArchive builds a deterministic tar.gz archive of the managed status
// bar files along with its content version digest and archive SHA-256.
func statusBarArchive(harness Harness, cfg Config) (archive []byte, versionDigest string, archiveSHA256 string, err error) {
	files, digest, err := statusBarFiles(harness, cfg)
	if err != nil {
		return nil, "", "", err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, "", "", err
	}
	gz.ModTime = time.Unix(0, 0).UTC()
	tw := tar.NewWriter(gz)
	for _, name := range names {
		body := files[name]
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			ModTime:  time.Unix(0, 0).UTC(),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, "", "", err
		}
		if _, err := tw.Write(body); err != nil {
			return nil, "", "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", "", err
	}
	if err := gz.Close(); err != nil {
		return nil, "", "", err
	}
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, digest, hex.EncodeToString(sum[:]), nil
}

func guestStatusBarPathsPrelude(harness Harness) string {
	common := "set -eu\n" +
		"if [ -z \"${HOME:-}\" ]; then echo 'HOME n’est pas défini dans la sandbox' >&2; exit 1; fi\n" +
		"config_home=${XDG_CONFIG_HOME:-\"$HOME/.config\"}\n" +
		"case \"$config_home\" in /*) ;; *) echo 'XDG_CONFIG_HOME doit être un chemin absolu' >&2; exit 1 ;; esac\n"
	if harness == HarnessPi {
		return common +
			"if [ -L \"$HOME/.pi\" ] || [ -L \"$HOME/.pi/agent\" ] || [ -L \"$HOME/.pi/agent/extensions\" ]; then echo 'lien symbolique refusé dans le chemin des extensions Pi' >&2; exit 1; fi\n" +
			"parent=\"$HOME/.pi/agent/extensions\"\n"
	}
	return common +
		"if [ -L \"$config_home\" ] || [ -L \"$config_home/opencode\" ] || [ -L \"$config_home/opencode/plugins\" ]; then echo 'lien symbolique refusé dans le chemin des plugins OpenCode' >&2; exit 1; fi\n" +
		"parent=\"$config_home/opencode/plugins\"\n"
}

func guestStatusBarInstallScript(harness Harness, archivePath, versionDigest, archiveSHA256 string) string {
	var b strings.Builder
	b.WriteString(guestStatusBarPathsPrelude(harness))
	fmt.Fprintf(&b, "archive=%s\n", shellQuote(archivePath))
	fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", statusBarDirName)
	fmt.Fprintf(&b, "version=%s\n", shellQuote(versionDigest))
	b.WriteString("if [ -L \"$dest\" ]; then rm -f -- \"$archive\"; echo 'lien symbolique refusé au chemin de la barre d’état gérée' >&2; exit 1; fi\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  if [ ! -d \"$dest\" ] || [ -L \"$dest/" + statusBarVersionFile + "\" ] || [ ! -f \"$dest/" + statusBarVersionFile + "\" ]; then rm -f -- \"$archive\"; echo 'le chemin de la barre d’état gérée entre en conflit avec des données utilisateur ; refus de l’écraser' >&2; exit 1; fi\n")
	b.WriteString("  if [ \"$(cat -- \"$dest/" + statusBarVersionFile + "\")\" = \"$version\" ]; then rm -f -- \"$archive\"; exit 0; fi\n")
	b.WriteString("fi\n")
	if archiveSHA256 != "" {
		b.WriteString("if ! (cd \"$(dirname \"$archive\")\" && printf '%s  %s\\n' ")
		fmt.Fprintf(&b, "%s %s | sha256sum -c - >/dev/null); then rm -f -- \"$archive\"; echo 'l’archive de la barre d’état a changé pendant le transfert' >&2; exit 1; fi\n", shellQuote(archiveSHA256), shellQuote(archivePath))
	}
	b.WriteString("mkdir -p -- \"$parent\" \"$config_home/codebox/status-staging\"\n")
	b.WriteString("stage=$(mktemp -d \"$config_home/codebox/status-staging/codebox-status.XXXXXX\")\n")
	b.WriteString("mkdir -- \"$stage/new\"\n")
	b.WriteString("cleanup() { rm -rf -- \"$stage\"; rm -f -- \"$archive\"; }\n")
	b.WriteString("trap cleanup EXIT HUP INT TERM\n")
	b.WriteString("tar -xzf \"$archive\" -C \"$stage/new\"\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  mv -- \"$dest\" \"$stage/old\"\n")
	b.WriteString("  if ! mv -- \"$stage/new\" \"$dest\"; then mv -- \"$stage/old\" \"$dest\" 2>/dev/null || true; exit 1; fi\n")
	b.WriteString("else\n")
	b.WriteString("  mv -- \"$stage/new\" \"$dest\"\n")
	b.WriteString("fi\n")
	b.WriteString("rm -rf -- \"$stage\" \"$archive\"\n")
	b.WriteString("trap - EXIT HUP INT TERM\n")
	return b.String()
}

// installManagedStatusBar installs or updates codebox's built-in status bar
// plugin (OpenCode) or extension (Pi) inside the Microsandbox guest, outside
// the project workspace so the project trust prompt does not apply (#318).
func (m *MicrosandboxRuntime) installManagedStatusBar(ctx context.Context) error {
	harness := m.cfg.EffectiveHarness()
	archive, versionDigest, archiveSHA, err := statusBarArchive(harness, m.cfg)
	if err != nil {
		return fmt.Errorf("préparation de la barre d'état gérée : %w", err)
	}
	archivePath := "/tmp/codebox-status-" + versionDigest[:16] + ".tar.gz"
	if err := m.Client.WriteFile(ctx, m.InstanceName(), archivePath, archive); err != nil {
		return fmt.Errorf("copie de la barre d'état gérée dans la sandbox : %w", err)
	}
	script := guestStatusBarInstallScript(harness, archivePath, versionDigest, archiveSHA)
	if err := m.guestShell(ctx, script); err != nil {
		return fmt.Errorf("installation de la barre d'état gérée dans la sandbox : %w", err)
	}
	return nil
}

// statusBarGuestShellScript returns a self-contained POSIX shell script that
// installs the managed status bar inside a Tart or agent-vm guest without
// relying on host-side file transfer helpers.
func statusBarGuestShellScript(harness Harness, cfg Config) (string, error) {
	archive, versionDigest, _, err := statusBarArchive(harness, cfg)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(archive)
	var b strings.Builder
	b.WriteString(guestStatusBarPathsPrelude(harness))
	fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", statusBarDirName)
	fmt.Fprintf(&b, "version=%s\n", shellQuote(versionDigest))
	b.WriteString("if [ -L \"$dest\" ]; then echo 'lien symbolique refusé au chemin de la barre d’état gérée' >&2; exit 1; fi\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  if [ ! -d \"$dest\" ] || [ -L \"$dest/" + statusBarVersionFile + "\" ] || [ ! -f \"$dest/" + statusBarVersionFile + "\" ]; then echo 'le chemin de la barre d’état gérée entre en conflit avec des données utilisateur ; refus de l’écraser' >&2; exit 1; fi\n")
	b.WriteString("  if [ \"$(cat -- \"$dest/" + statusBarVersionFile + "\")\" = \"$version\" ]; then exit 0; fi\n")
	b.WriteString("fi\n")
	b.WriteString("mkdir -p -- \"$parent\" \"$config_home/codebox/status-staging\"\n")
	b.WriteString("stage=$(mktemp -d \"$config_home/codebox/status-staging/codebox-status.XXXXXX\")\n")
	b.WriteString("mkdir -- \"$stage/new\"\n")
	b.WriteString("cleanup() { rm -rf -- \"$stage\"; }\n")
	b.WriteString("trap cleanup EXIT HUP INT TERM\n")
	fmt.Fprintf(&b, "printf '%%s' %s | (base64 -d 2>/dev/null || base64 -D) > \"$stage/archive.tar.gz\"\n", shellQuote(encoded))
	b.WriteString("tar -xzf \"$stage/archive.tar.gz\" -C \"$stage/new\"\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  mv -- \"$dest\" \"$stage/old\"\n")
	b.WriteString("  if ! mv -- \"$stage/new\" \"$dest\"; then mv -- \"$stage/old\" \"$dest\" 2>/dev/null || true; exit 1; fi\n")
	b.WriteString("else\n")
	b.WriteString("  mv -- \"$stage/new\" \"$dest\"\n")
	b.WriteString("fi\n")
	b.WriteString("rm -rf -- \"$stage\"\n")
	b.WriteString("trap - EXIT HUP INT TERM\n")
	return b.String(), nil
}

// InstallLocalStatusBar writes the managed status bar directly onto the local
// guest filesystem (used by the in-guest Tart Go bootstrap/prepare helpers).
func InstallLocalStatusBar(home, xdgConfigHome string, harness Harness, cfg Config) error {
	configJSON, err := statusBarConfigJSON(cfg)
	if err != nil {
		return err
	}
	return InstallLocalStatusBarWithConfigJSON(home, xdgConfigHome, harness, configJSON)
}

// readStagedStatusBarConfig loads non-secret status bar metadata staged by the
// host into the read-only Tart share, returning nil when absent or invalid so
// preparation falls back to defaults.
func readStagedStatusBarConfig(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return nil
	}
	var parsed statusBarConfig
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil
	}
	return append(canonical, '\n')
}

// InstallLocalStatusBarWithConfigJSON installs the managed status bar locally
// using the supplied canonical config.json payload.
func InstallLocalStatusBarWithConfigJSON(home, xdgConfigHome string, harness Harness, configJSON []byte) error {
	if strings.TrimSpace(home) == "" {
		return fmt.Errorf("HOME n'est pas défini dans la sandbox")
	}
	configHome := strings.TrimSpace(xdgConfigHome)
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(configHome) {
		return fmt.Errorf("XDG_CONFIG_HOME doit être un chemin absolu")
	}
	var components []string
	var parent string
	if harness == HarnessPi {
		components = []string{
			filepath.Join(home, ".pi"),
			filepath.Join(home, ".pi", "agent"),
			filepath.Join(home, ".pi", "agent", "extensions"),
		}
		parent = components[len(components)-1]
	} else {
		components = []string{
			configHome,
			filepath.Join(configHome, "opencode"),
			filepath.Join(configHome, "opencode", "plugins"),
		}
		parent = components[len(components)-1]
	}
	for _, p := range components {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lien symbolique refusé dans le chemin de la barre d'état gérée : %s", p)
		}
	}
	dest := filepath.Join(parent, statusBarDirName)
	files, digest, err := statusBarFilesWithConfigJSON(harness, configJSON)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(dest); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lien symbolique refusé au chemin de la barre d'état gérée : %s", dest)
		}
		if !fi.IsDir() {
			return fmt.Errorf("le chemin de la barre d'état gérée entre en conflit avec des données utilisateur : %s", dest)
		}
		verPath := filepath.Join(dest, statusBarVersionFile)
		vfi, verr := os.Lstat(verPath)
		if verr != nil || vfi.Mode()&os.ModeSymlink != 0 || !vfi.Mode().IsRegular() {
			return fmt.Errorf("le chemin de la barre d'état gérée entre en conflit avec des données utilisateur : %s", dest)
		}
		if raw, rerr := os.ReadFile(verPath); rerr == nil && strings.TrimSpace(string(raw)) == digest {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stagingBase := filepath.Join(configHome, "codebox", "status-staging")
	if err := os.MkdirAll(stagingBase, 0o755); err != nil {
		return err
	}
	stageDir, err := os.MkdirTemp(stagingBase, statusBarDirName+".")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)

	newDir := filepath.Join(stageDir, "new")
	if err := os.Mkdir(newDir, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(newDir, name), body, 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(dest); err == nil {
		oldDir := filepath.Join(stageDir, "old")
		if err := os.Rename(dest, oldDir); err != nil {
			return err
		}
		if err := os.Rename(newDir, dest); err != nil {
			_ = os.Rename(oldDir, dest)
			return err
		}
		return nil
	}
	return os.Rename(newDir, dest)
}
