package justcode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// GuestBootstrapCommand is a hidden subcommand of this same binary. The binary
// is staged into the Tart guest and re-invoked with it, so the in-VM bootstrap
// is Go rather than a shell script. It is an implementation detail, not a
// user-facing command.
const GuestBootstrapCommand = "__guest-bootstrap"

// GuestPrepareCommand is the isolation-full counterpart of the bootstrap: it
// installs OpenCode in the guest and sets the git identity, but launches no
// server and reads no secrets. The TUI is launched afterwards via tart exec -it.
const GuestPrepareCommand = "__guest-prepare"

// GuestSecretsCommand writes the secrets env file (0600) from stdin, for the
// isolation-full flow. Secrets travel on stdin only, never in argv.
const GuestSecretsCommand = "__guest-secrets"

const (
	// GuestBootstrapModelsArgIndex is the zero-based position in the hidden
	// bootstrap's os.Args[1:] vector containing enabled model JSON.
	GuestBootstrapModelsArgIndex = 5
	// GuestBootstrapMCPArgIndex is the position containing selected MCP IDs.
	GuestBootstrapMCPArgIndex = 8
	// GuestSecretsModelsArgIndex is the corresponding model position for __guest-secrets.
	GuestSecretsModelsArgIndex = 3
	// GuestSecretsMCPArgIndex is the corresponding MCP position for __guest-secrets.
	GuestSecretsMCPArgIndex = 4
)

const (
	// guestShareDir is the read-only share mounted into the VM. It holds only
	// the staged binary, never the checkout or its .env.
	guestShareDir = "/Volumes/My Shared Files/just-code"
	// guestShareBinary is the staged binary on that share.
	guestShareBinary = guestShareDir + "/just-code"
	// guestLocalBinary is the copy the guest executes. Running from the
	// virtiofs share is not reliably executable, so the payload is copied to
	// local disk first.
	guestLocalBinary = "/tmp/just-code-guest"

	guestWorkspaceDir  = "/Volumes/My Shared Files/workspace"
	opencodeInstallURL = "https://opencode.ai/v2/install"
)

// Execer replaces the current process image, so the server inherits the
// terminal, signals, and stdio.
type Execer interface {
	Exec(path string, argv []string, env []string) error
}

// SysExecer is the real Execer, backed by syscall.Exec.
type SysExecer struct{}

func (SysExecer) Exec(path string, argv []string, env []string) error {
	return syscall.Exec(path, argv, env)
}

// GuestConfig configures the in-VM bootstrap. Every dependency is injectable so
// the logic is testable without a macOS guest.
type GuestConfig struct {
	Port     string
	Username string
	// OpenCodeOverlay is the managed OpenCode configuration layer (P10).
	// The composed config content replaces the raw embedded asset in the
	// guest environment.
	OpenCodeOverlay ManagedOverlay
	// MTU is TART_MTU. Empty means "unset" and falls back to DefaultTartMTU;
	// "auto" leaves the guest network untouched.
	MTU    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	Runner Runner
	Execer Execer
	// Chdir sets the working directory before exec. Injectable so tests do not
	// mutate the test process's cwd.
	Chdir   func(dir string) error
	Home    string
	Environ []string
	HTTPGet func(url string) ([]byte, error)
	// GitName and GitEmail override the default git identity for guest
	// commits (P11: from the user settings). Empty keeps the defaults.
	GitName  string
	GitEmail string
}

// guestGitIdentity resolves the git identity for guest commits: the
// configured values, or the documented defaults when unset. The settings
// file is the durable source (the setup wizard writes it); the guest reads
// it through the caller-supplied values, never by touching host files
// itself.
func guestGitIdentity(cfg GuestConfig) (name, email string) {
	name, email = "Albert Code Agent", "albert-code@noreply.etalab.gouv.fr"
	if cfg.GitName != "" {
		name = cfg.GitName
	}
	if cfg.GitEmail != "" {
		email = cfg.GitEmail
	}
	return name, email
}

// RunGuestBootstrap is the in-VM entry point: it clamps the MTU, installs
// OpenCode if needed, and execs `opencode serve`. It replaces the former
// tart-bootstrap.sh.
func RunGuestBootstrap(ctx context.Context, cfg GuestConfig) error {
	applyGuestDefaults(&cfg)

	pathEnv := guestPath(cfg.Environ, cfg.Home)
	env := withEnv(cfg.Environ, "PATH="+pathEnv)

	mtu := cfg.MTU
	if mtu == "" {
		mtu = DefaultTartMTU
	}
	if mtu != MTUAuto {
		if err := applyMTU(ctx, cfg, env, mtu); err != nil {
			return err
		}
	}

	password, keys, err := readSecrets(cfg.Stdin)
	if err != nil {
		return err
	}

	g := &guest{cfg: cfg, pathEnv: pathEnv, env: env}
	if err := g.ensureOpencode(ctx); err != nil {
		return err
	}
	// The curl installer drops the binary in ~/.opencode/bin; guestPath already
	// includes it, so a fresh resolve picks it up.
	if _, err := g.resolve("opencode"); err != nil {
		return fmt.Errorf("opencode is not available on PATH inside the VM")
	}

	// Best-effort git identity and safe.directory, matching the shell behavior.
	gitName, gitEmail := guestGitIdentity(cfg)
	_, _ = g.run(ctx, "git", "config", "--global", "user.name", gitName)
	_, _ = g.run(ctx, "git", "config", "--global", "user.email", gitEmail)
	_, _ = g.run(ctx, "git", "config", "--global", "--add", "safe.directory", "*")

	workspace := guestWorkspaceDir
	if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
		workspace = filepath.Join(cfg.Home, "workspace")
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return err
		}
	}
	if err := cfg.Chdir(workspace); err != nil {
		return err
	}

	opencodePath, err := g.resolve("opencode")
	if err != nil {
		return err
	}
	configContent, cerr := ComposeConfigContent(cfg.OpenCodeOverlay)
	if cerr != nil {
		configContent = opencodeConfigContent
	}
	envAssignments := []string{
		OpenCodeDisableUpdateAssignment,
		"OPENCODE_PASSWORD=" + password,
		"OPENCODE_SERVER_PASSWORD=" + password,
		"OPENCODE_SERVER_USERNAME=" + cfg.Username,
		"OPENCODE_CONFIG_CONTENT=" + configContent,
	}
	envAssignments = append(envAssignments, providerKeyAssignments(keys)...)
	childEnv := withEnv(env, envAssignments...)
	return cfg.Execer.Exec(opencodePath, []string{"opencode", "serve", "--hostname", "0.0.0.0", "--port", cfg.Port}, childEnv)
}

// guest holds the resolved PATH for one bootstrap run.
type guest struct {
	cfg     GuestConfig
	pathEnv string
	env     []string
}

func applyGuestDefaults(cfg *GuestConfig) {
	if cfg.Stdin == nil {
		cfg.Stdin = os.Stdin
	}
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Runner == nil {
		cfg.Runner = OSRunner{}
	}
	if cfg.Execer == nil {
		cfg.Execer = SysExecer{}
	}
	if cfg.Chdir == nil {
		cfg.Chdir = os.Chdir
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ()
	}
	if cfg.Home == "" {
		cfg.Home, _ = os.UserHomeDir()
	}
	if cfg.HTTPGet == nil {
		cfg.HTTPGet = httpGet
	}
	if cfg.Port == "" {
		cfg.Port = fmt.Sprint(DefaultPort)
	}
	if cfg.Username == "" {
		cfg.Username = DefaultUsername
	}
}

// guestPath builds the guest PATH: the inherited PATH plus the macOS
// administration directories (route, ifconfig) and the curl installer's
// ~/.opencode/bin.
func guestPath(environ []string, home string) string {
	current := ""
	for _, kv := range environ {
		if strings.HasPrefix(kv, "PATH=") {
			current = strings.TrimPrefix(kv, "PATH=")
		}
	}
	dirs := []string{}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".opencode", "bin"))
	}
	dirs = append(dirs, current, "/usr/sbin", "/sbin")
	var out []string
	for _, d := range dirs {
		if d != "" {
			out = append(out, d)
		}
	}
	return strings.Join(out, string(os.PathListSeparator))
}

// applyMTU validates and applies the requested MTU to the guest's default
// IPv4 interface. The value was already validated host-side; this is a backstop.
func applyMTU(ctx context.Context, cfg GuestConfig, env []string, mtu string) error {
	if err := ValidateMTU(mtu); err != nil {
		return err
	}
	res, err := cfg.Runner.RunEnv(ctx, env, "route", "-n", "get", "default")
	if err != nil {
		return err
	}
	iface := parseDefaultInterface(res.Stdout)
	if iface == "" {
		return fmt.Errorf("cannot determine the guest default network interface for TART_MTU")
	}
	res, err = cfg.Runner.RunEnv(ctx, env, "sudo", "-n", "ifconfig", iface, "mtu", mtu)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("cannot apply TART_MTU=%s to %s (passwordless sudo required)", mtu, iface)
	}
	fmt.Fprintf(cfg.Stdout, "Guest network: %s MTU=%s\n", iface, mtu)
	return nil
}

// parseDefaultInterface extracts the interface from `route -n get default`
// output, e.g. "  interface: en0".
func parseDefaultInterface(output string) string {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "interface:" {
			return fields[1]
		}
	}
	return ""
}

// readSecrets reads the password (line 1) and the provider keys (line 2) from
// the host's stdin stream. A missing password line falls back to the default;
// an explicitly empty line means "no auth". The second line is either a JSON
// object of variable assignments (the multi-platform form) or a bare Albert
// API key (the historical form, still accepted).
func readSecrets(stdin io.Reader) (string, map[string]string, error) {
	r := bufio.NewReader(stdin)
	password := DefaultPassword
	if line, err := r.ReadString('\n'); err == nil {
		password = trimLine(line)
	} else if err != io.EOF {
		return "", nil, err
	}
	keys := map[string]string{}
	if line, err := r.ReadString('\n'); err == nil {
		raw := trimLine(line)
		if strings.HasPrefix(strings.TrimSpace(raw), "{") {
			if err := json.Unmarshal([]byte(raw), &keys); err != nil {
				return "", nil, fmt.Errorf("parse guest provider keys: %w", err)
			}
		} else if raw != "" {
			keys[msbAPISecretEnv] = raw
		}
	} else if err != io.EOF {
		return "", nil, err
	}
	return password, keys, nil
}

// providerKeyAssignments renders the resolved provider keys as env
// assignments, sorted for a stable environment.
func providerKeyAssignments(keys map[string]string) []string {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+keys[name])
	}
	return out
}

func trimLine(line string) string {
	return strings.TrimRight(line, "\r\n")
}

// resolve finds an executable by name on the guest PATH.
func (g *guest) resolve(name string) (string, error) {
	for _, dir := range filepath.SplitList(g.pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found on PATH", name)
}

func (g *guest) run(ctx context.Context, name string, args ...string) (int, error) {
	res, err := g.cfg.Runner.RunEnv(ctx, g.env, name, args...)
	if err != nil {
		return -1, err
	}
	return res.ExitCode, nil
}

// ensureOpencode upgrades an existing guest in place without recreating it or
// discarding sessions. The private vendor install wins over old system binaries.
func (g *guest) ensureOpencode(ctx context.Context) error {
	probe := func() bool {
		// os/exec resolves a bare executable before applying Cmd.Env. Use the
		// guest's absolute resolution, and re-resolve after installation so a
		// stale system v1 binary cannot shadow the private v2 installation.
		binary, err := g.resolve("opencode")
		if err != nil {
			return false
		}
		result, err := g.cfg.Runner.RunEnv(ctx, g.env, binary, "--version")
		return err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "opencode v"+OpenCodeVersion
	}
	if probe() {
		return nil
	}
	if _, err := g.resolve("opencode"); err == nil {
		fmt.Fprintln(g.cfg.Stdout, "Updating managed OpenCode inside macOS VM...")
		if err := g.runInstallerScript(ctx); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(g.cfg.Stdout, "Installing OpenCode inside macOS VM...")
		if err := g.installOpencode(ctx); err != nil {
			return err
		}
	}
	if !probe() {
		return fmt.Errorf("guest must provide OpenCode v%s after installation; check the guest PATH", OpenCodeVersion)
	}
	return nil
}

// installOpencode installs the managed v2 package, falling back to the vendor's
// v2 installer with an explicit version. Homebrew's unversioned v1 formula is
// deliberately not used.
func (g *guest) installOpencode(ctx context.Context) error {
	if _, err := g.resolve("npm"); err == nil {
		if code, _ := g.run(ctx, "npm", "install", "-g", "@opencode/cli@"+OpenCodeVersion); code == 0 {
			return nil
		}
	}
	return g.runInstallerScript(ctx)
}

func (g *guest) runInstallerScript(ctx context.Context) error {
	script, err := g.cfg.HTTPGet(opencodeInstallURL)
	if err != nil {
		return fmt.Errorf("cannot download the OpenCode installer: %w", err)
	}
	tmp, err := os.CreateTemp("", "just-code-opencode-install-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(script); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if code, err := g.run(ctx, "bash", tmp.Name(), "--version", OpenCodeVersion, "--no-modify-path"); err != nil {
		return err
	} else if code != 0 {
		return fmt.Errorf("the OpenCode installer failed (exit %d)", code)
	}
	return nil
}

// httpGet is the default installer fetch, bounded by a timeout.
func httpGet(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// RunGuestPrepare is the isolation-full in-VM entry point: it installs
// OpenCode if missing and sets the git identity, but launches nothing. It
// reads no secrets; they are pushed separately via RunGuestSecrets so the
// interactive TUI step needs no stdin plumbing.
func RunGuestPrepare(ctx context.Context, cfg GuestConfig) error {
	applyGuestDefaults(&cfg)

	pathEnv := guestPath(cfg.Environ, cfg.Home)
	env := withEnv(cfg.Environ, "PATH="+pathEnv)

	g := &guest{cfg: cfg, pathEnv: pathEnv, env: env}
	if err := g.ensureOpencode(ctx); err != nil {
		return err
	}
	if _, err := g.resolve("opencode"); err != nil {
		return fmt.Errorf("opencode is not available on PATH inside the VM")
	}

	// Best-effort git identity and safe.directory, matching the bootstrap.
	gitName, gitEmail := guestGitIdentity(cfg)
	_, _ = g.run(ctx, "git", "config", "--global", "user.name", gitName)
	_, _ = g.run(ctx, "git", "config", "--global", "user.email", gitEmail)
	_, _ = g.run(ctx, "git", "config", "--global", "--add", "safe.directory", "*")
	return nil
}

// guestSecretsEnvPath is where the isolation-full TUI step sources its
// credentials from.
const guestSecretsEnvPath = "/tmp/just-code-agent.env"

// RunGuestSecrets writes the isolation-full secrets env file from stdin
// (password line 1, API key line 2), with restrictive permissions. The file
// is sourced by the interactive TUI launch; it is never written by a command
// whose argv carries the values.
func RunGuestSecrets(cfg GuestConfig) error {
	applyGuestDefaults(&cfg)

	password, keys, err := readSecrets(cfg.Stdin)
	if err != nil {
		return err
	}
	content := guestSecretsEnv(password, cfg.Username, keys, cfg.OpenCodeOverlay)
	if err := os.WriteFile(guestSecretsEnvPath, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Chmod(guestSecretsEnvPath, 0o600)
}

// guestSecretsEnv builds the sourced env file body. Every value is
// single-quoted because the file is sourced by a shell; the shell syntax in
// the embedded provider config makes this non-optional.
func guestSecretsEnv(password, username string, keys map[string]string, overlay ManagedOverlay) string {
	// The provider config travels the same way as in backend mode (env), so
	// the full-mode TUI sees the same Albert provider/model definition.
	configContent, err := ComposeConfigContent(overlay)
	if err != nil {
		configContent = opencodeConfigContent
	}
	// The update-check opt-out rides in the same file for the full-mode TUI:
	// it is the only process-level lever OpenCode honours (see
	// OpenCodeDisableUpdateEnv), and this file is the only environment that
	// TUI receives.
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nOPENCODE_PASSWORD=%s\nOPENCODE_SERVER_PASSWORD=%s\nOPENCODE_SERVER_USERNAME=%s\nOPENCODE_CONFIG_CONTENT=%s\n",
		OpenCodeDisableUpdateAssignment, shellQuote(password), shellQuote(password), shellQuote(username), shellQuote(configContent))
	for _, assignment := range providerKeyAssignments(keys) {
		name, value, _ := strings.Cut(assignment, "=")
		fmt.Fprintf(&b, "%s=%s\n", name, shellQuote(value))
	}
	return b.String()
}
