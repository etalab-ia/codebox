package justcode

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds the settings resolved from the environment for a run. Field
// semantics match the .env.example contract.
type Config struct {
	// Ports are the project's configured Microsandbox TCP forwards. A nil
	// slice retains the legacy preview-port defaults.
	Ports           []PortMapping
	PortsConfigured bool
	// ProjectSkills are immutable, hash-verified skill archives resolved from
	// this project's manifest/lock or host-local selection before runtime start.
	ProjectSkills []SkillPackage
	// ProjectSkillIDs and ProjectSkillsManaged include deselection state even
	// when no current archives are selected.
	ProjectSkillIDs      []string
	ProjectSkillsManaged bool
	// ProjectSkillsLocalOnly prevents stale versioned instructions in a
	// persistent guest from being accepted after the selection moves host-local.
	ProjectSkillsLocalOnly bool
	// WorkspaceDir is the host directory the project content comes from. Under
	// the sealed model (P22) it is the transfer SOURCE, not a mounted
	// directory: only filtered files ever cross into the guest. A zero-flag
	// launch uses the discovered project root; WORKSPACE_DIR (or the legacy
	// PROJECT_DIR) overrides it. It replaces the legacy PROJECT_DIR, which is
	// still honoured.
	WorkspaceDir string
	// WorkspaceDirSet records that WORKSPACE_DIR or PROJECT_DIR was explicitly
	// configured, so the launch path knows not to substitute the project root.
	WorkspaceDirSet bool
	// CPUs and MemoryMB size the Microsandbox guest. They were fixed
	// constants until P12b, which made the project's resource choice a
	// question the setup asks and the manifest records — a recorded setting
	// nothing reads would be worse than no question at all.
	CPUs     int
	MemoryMB int
	// SandboxResourcesErr records an invalid JUST_CODE_CPUS or
	// JUST_CODE_MEMORY_MB, surfaced where the value is consumed.
	SandboxResourcesErr error
	Username            string
	// Password is the HTTP basic-auth password. It is empty when
	// OPENCODE_SERVER_PASSWORD was explicitly set to an empty value, which
	// means "no auth". PasswordSet records whether the variable was present,
	// distinguishing "unset" (use default) from "set empty" (no auth).
	Password    string
	PasswordSet bool
	TartImage   string
	TartVM      string
	TartMTU     string
	// AgentVMTemplate is the Lima base template the managed VM is cloned from.
	// The default name is owned by just-code, which builds it on first use;
	// any other value is treated as user-managed and never built or replaced.
	AgentVMTemplate string
	// AgentVMVM is the managed Lima instance name, always under the opencode-
	// prefix so `stop`/`check` find it and agent-vm's own VMs stay untouched.
	AgentVMVM string
	// AgentVMImage is the Lima image the base template is created from.
	AgentVMImage string
	// Resource sizing for the base template. The guest holds the toolchain
	// only (the workspace is mounted, not copied), but Lima's disk resize is
	// grow-only, so an undersized default is worth avoiding.
	AgentVMDiskGB   int
	AgentVMMemoryGB int
	AgentVMCPUs     int
	// AgentVMResourcesErr records an invalid sizing value with the same
	// deferred-validation contract as StartTimeoutErr: commands that never
	// build a template must not be blocked by a typo in a variable they do
	// not read.
	AgentVMResourcesErr error
	APIKey              string
	// CredentialRef names the stored credential that provides the Albert key
	// (P08/P09): a reference, never a value. Precedence at resolution time is
	// JUST_CODE_CREDENTIAL_REF > project manifest credentialRef > user
	// settings credentialRef; empty means the legacy chain (ALBERT_API_KEY
	// environment, then the default stored albert credential).
	CredentialRef string
	// Providers are the resolved inference platforms this run may use, in
	// display order. Empty means the historical single-platform default
	// (Albert API), resolved by the runtime.
	Providers []InferenceProvider
	// GitHubRemote is a normalized, credential-free github.com origin used
	// only when this project's host-local GitHub binding is approved (P13).
	GitHubRemote GitHubRemote
	// GuestCredentialsAcknowledged records the explicit
	// --acknowledge-guest-credentials flag (P09): Tart and agent-vm transport
	// the credential into the guest in plaintext, so starting them requires
	// this acknowledgement.
	GuestCredentialsAcknowledged bool
	// StartTimeout bounds the backend health wait for the attach flow.
	// StartTimeoutErr records an invalid JUST_CODE_START_TIMEOUT so that
	// commands which never start a runtime (stop, clean, logs, doctor, check)
	// are not blocked by a typo in a variable they do not read.
	StartTimeout    time.Duration
	StartTimeoutErr error
	// Isolation is the resolved execution model (see isolation.go). It
	// defaults to full. IsolationErr records an invalid ISOLATION value
	// with the same deferred-validation contract as StartTimeoutErr.
	Isolation    Isolation
	IsolationErr error
	// Harness is the agent harness the project runs (#176), resolved from
	// JUST_CODE_HARNESS, the project manifest, then the default. The backends
	// read it when they are constructed, like Isolation.
	Harness Harness
}

// EnvLookup is an injectable subset of os.LookupEnv, for tests.
type EnvLookup func(string) (string, bool)

const (
	DefaultUsername  = "opencode"
	DefaultPassword  = "albert-dev-pass"
	DefaultPort      = 4096
	DefaultTartImage = "ghcr.io/cirruslabs/macos-tahoe-base:latest"
	DefaultTartMTU   = "1280"
	// DefaultAgentVMTemplate is the base template just-code builds and owns
	// when it is absent. Any other AGENT_VM_TEMPLATE value is treated as
	// user-managed and is never built or overwritten.
	DefaultAgentVMTemplate = "agent-vm-base"
	DefaultAgentVMVM       = "opencode-agent-vm"
	// DefaultAgentVMImage is the Lima image the base template is built from.
	// It is pinned to a release on purpose: `template://debian` is a rolling
	// alias whose target has already moved (Debian 12 through Lima 1.2, then
	// Debian 13 from Lima 2.0), so it would silently change the guest on a
	// Lima upgrade.
	//
	// The two-slash form is what this default uses because it is the only
	// spelling both Lima 1.x and 2.x accept: on 1.x the name comes from
	// url.Host, so `template:debian-12` yields an empty name and fails with
	// "identifier must not be empty", while Lima 2.0 added an RFC 3986 opaque
	// branch (lima commit 677fa1f0) that accepts it and prefers it. Users on
	// Lima 2.x may therefore write either form. Other locator kinds
	// (https://, file://, a local path) are equally valid for AGENT_VM_IMAGE;
	// no form is constrained, only the value this package defaults to.
	// debian-12 is shipped by every Lima release this code supports.
	DefaultAgentVMImage = "template://debian-12"
	// Default base template sizing. The disk default is deliberately generous
	// for the toolchain: Lima resizes disks up only, never down, and Agent
	// CLI installations grow over time.
	DefaultAgentVMDiskGB   = 20
	DefaultAgentVMMemoryGB = 4
	DefaultAgentVMCPUs     = 2
	// DefaultStartTimeout is deliberately generous: a first Microsandbox boot
	// installs ~384 MiB of packages inside the microVM before OpenCode starts.
	DefaultStartTimeout = 300 * time.Second
	// DefaultSandboxCPUs and DefaultSandboxMemoryMB size the Microsandbox
	// guest. They are the values the runtime used as constants before the
	// resource choice became configurable, so an unconfigured project is
	// unchanged.
	DefaultSandboxCPUs     = 2
	DefaultSandboxMemoryMB = 4096
	// MaxSandboxCPUs is the largest CPU count the sandbox SDK can carry (it
	// takes a uint8); a larger value is rejected rather than wrapped.
	MaxSandboxCPUs = 255
	// MaxSandboxMemoryMB is the largest memory size the SDK can carry (it
	// takes a uint32 of MiB). Unreachable in practice, but the bound is the
	// same representation limit as MaxSandboxCPUs: wrapping silently is the
	// failure mode being avoided, not the magnitude. The value is the largest
	// the configuration's int can hold on every supported architecture, so the
	// bound itself cannot overflow a 32-bit build.
	MaxSandboxMemoryMB = 1<<31 - 1
)

// LoadConfigEnv resolves configuration from the process environment.
//
// It no longer loads a .env implicitly (P12). The implicit load made the
// launch depend on an untracked file in the working directory — the exact kind
// of file that carries secrets and that the sealed workspace exists to keep
// out of the guest — and it made behaviour differ between two invocations of
// the same binary. Exported variables are still honoured, and a .env left in
// place is reported with the explicit command that imports it, so the change
// is visible rather than silent.
func LoadConfigEnv() Config {
	// Detect without applying: the user is told the file was not read and how
	// to adopt it, instead of discovering the difference from behaviour.
	for _, dir := range dotenvDirs() {
		path := filepath.Join(dir, ".env")
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Attention : %s n'a pas pu être inspecté (%v) ; il n'est de toute façon pas lu, car just-code ne charge plus .env automatiquement.\n", path, err)
			}
			continue
		}
		if info.IsDir() {
			// A directory named .env is not a configuration file; the note
			// below would be misleading.
			continue
		}
		// The absolute path is what the user needs: two directories are
		// searched, so "." alone would not say which file is being ignored.
		shown := path
		if abs, err := filepath.Abs(path); err == nil {
			shown = abs
		}
		fmt.Fprintf(os.Stderr, "Attention : %s n'a pas été lu : just-code ne charge plus .env automatiquement.\n"+
			"      Les variables exportées restent prises en compte. Pour voir son contenu et où va\n"+
			"      chaque valeur : 'just-code config import-env %s' (aperçu seulement ; la clé Albert\n"+
			"      se range dans le magasin d'identifiants : 'just-code auth add albert').\n", shown, shown)
		break
	}
	if _, ok := os.LookupEnv("WORKSPACE_DIR"); !ok {
		if _, legacy := os.LookupEnv("PROJECT_DIR"); legacy {
			fmt.Fprintln(os.Stderr, "Attention : PROJECT_DIR est déprécié ; utilisez WORKSPACE_DIR à la place.")
		}
	}
	return LoadConfig(os.LookupEnv)
}

// parseStartTimeout parses JUST_CODE_START_TIMEOUT, a whole number of seconds.
func parseStartTimeout(value string) (time.Duration, error) {
	if value == "" {
		return 0, fmt.Errorf("JUST_CODE_START_TIMEOUT must be a whole number of seconds")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("JUST_CODE_START_TIMEOUT must be a whole number of seconds")
		}
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("JUST_CODE_START_TIMEOUT must be a whole number of seconds")
	}
	if seconds < 1 {
		return 0, fmt.Errorf("JUST_CODE_START_TIMEOUT must be at least 1 second")
	}
	return time.Duration(seconds) * time.Second, nil
}

// parsePositiveInt parses a strictly positive whole number, rejecting the
// empty string, signs, decimals and zero. Resource sizing variables use it so
// that a bad value is reported rather than silently becoming zero.
func parsePositiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("must be a whole number, got %q", value)
	}
	if n < 1 {
		return 0, fmt.Errorf("must be at least 1, got %d", n)
	}
	return n, nil
}

// dotenvDirs lists the directories searched for a .env file, most specific
// first.
func dotenvDirs() []string {
	dirs := []string{"."}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "." {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// LoadConfig resolves configuration from the environment. lookup defaults to
// os.LookupEnv. Using LookupEnv (rather than Getenv + default) is the fix for
// the empty-password bug: an explicitly empty OPENCODE_SERVER_PASSWORD must
// mean "no auth", not silently fall back to the default.
func LoadConfig(lookup EnvLookup) Config {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := Config{
		Username:     envDefault(lookup, "OPENCODE_SERVER_USERNAME", DefaultUsername),
		TartImage:    envDefault(lookup, "TART_IMAGE", DefaultTartImage),
		TartMTU:      envDefault(lookup, "TART_MTU", DefaultTartMTU),
		WorkspaceDir: envDefault(lookup, "WORKSPACE_DIR", envDefault(lookup, "PROJECT_DIR", "./workspace")),
		StartTimeout: DefaultStartTimeout,
	}
	cfg.AgentVMTemplate = envDefault(lookup, "AGENT_VM_TEMPLATE", DefaultAgentVMTemplate)
	cfg.AgentVMVM = envDefault(lookup, "AGENT_VM_VM", DefaultAgentVMVM)
	cfg.AgentVMImage = envDefault(lookup, "AGENT_VM_IMAGE", DefaultAgentVMImage)
	cfg.AgentVMDiskGB = DefaultAgentVMDiskGB
	cfg.AgentVMMemoryGB = DefaultAgentVMMemoryGB
	cfg.AgentVMCPUs = DefaultAgentVMCPUs
	for _, f := range []struct {
		key string
		def int
		dst *int
	}{
		{"AGENT_VM_DISK_GB", DefaultAgentVMDiskGB, &cfg.AgentVMDiskGB},
		{"AGENT_VM_MEMORY_GB", DefaultAgentVMMemoryGB, &cfg.AgentVMMemoryGB},
		{"AGENT_VM_CPUS", DefaultAgentVMCPUs, &cfg.AgentVMCPUs},
	} {
		v, ok := lookup(f.key)
		if !ok || v == "" {
			continue
		}
		n, err := parsePositiveInt(v)
		if err != nil {
			if cfg.AgentVMResourcesErr == nil {
				cfg.AgentVMResourcesErr = fmt.Errorf("%s %w", f.key, err)
			}
			continue
		}
		*f.dst = n
	}
	if v, ok := lookup("JUST_CODE_START_TIMEOUT"); ok && v != "" {
		if d, err := parseStartTimeout(v); err != nil {
			cfg.StartTimeoutErr = err
		} else {
			cfg.StartTimeout = d
		}
	}
	cfg.CPUs, cfg.MemoryMB = DefaultSandboxCPUs, DefaultSandboxMemoryMB
	for _, f := range []struct {
		key string
		max int
		dst *int
	}{
		// MaxSandboxCPUs is not a policy choice: the sandbox SDK takes the CPU
		// count as a uint8, so a larger value would wrap silently.
		{"JUST_CODE_CPUS", MaxSandboxCPUs, &cfg.CPUs},
		{"JUST_CODE_MEMORY_MB", MaxSandboxMemoryMB, &cfg.MemoryMB},
	} {
		v, ok := lookup(f.key)
		if !ok || v == "" {
			continue
		}
		var n int
		var err error
		if f.key == "JUST_CODE_MEMORY_MB" {
			n, err = ParseMemorySize(v)
		} else {
			n, err = parsePositiveInt(v)
		}
		if err == nil && f.max > 0 && n > f.max {
			err = fmt.Errorf("must be at most %d, got %d", f.max, n)
		}
		if err != nil {
			if cfg.SandboxResourcesErr == nil {
				cfg.SandboxResourcesErr = fmt.Errorf("%s %w", f.key, err)
			}
			continue
		}
		*f.dst = n
	}
	if iso, err := ResolveIsolation("", envDefault(lookup, "ISOLATION", string(IsolationFull))); err != nil {
		// Keep a usable default so commands that never read Isolation
		// still work; the error is surfaced where the value is consumed.
		cfg.Isolation, cfg.IsolationErr = IsolationFull, err
	} else {
		cfg.Isolation = iso
	}
	if v, ok := lookup("OPENCODE_SERVER_PASSWORD"); ok {
		cfg.Password = v
		cfg.PasswordSet = true
	} else {
		cfg.Password = DefaultPassword
	}
	cfg.APIKey, _ = lookup("ALBERT_API_KEY")
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	if abs, err := filepath.Abs(cfg.WorkspaceDir); err == nil {
		cfg.WorkspaceDir = abs
	}
	// An explicitly configured workspace source is honoured as given; a
	// zero-flag launch resolves it to the project root instead of the
	// historical ./workspace subdirectory, which under the sealed model would
	// be an empty directory nothing ever fills.
	if v, ok := lookup("WORKSPACE_DIR"); ok && strings.TrimSpace(v) != "" {
		cfg.WorkspaceDirSet = true
	} else if v, ok := lookup("PROJECT_DIR"); ok && strings.TrimSpace(v) != "" {
		cfg.WorkspaceDirSet = true
	}
	cfg.TartVM = VMName(cfg.TartImage)
	return cfg
}

// envDefault returns the value of key when it is set and non-empty, otherwise
// the default. It is used only for keys whose empty string has no special
// meaning.
func envDefault(lookup EnvLookup, key, def string) string {
	if v, ok := lookup(key); ok && v != "" {
		return v
	}
	return def
}

// ApplyDotenv loads a .env file (when present) and exports its keys into the
// process environment without overriding already-set variables. This mirrors
// just's `set dotenv-load`.
func ApplyDotenv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for k, v := range parseDotenv(string(data)) {
		if _, ok := os.LookupEnv(k); !ok {
			os.Setenv(k, v)
		}
	}
	return nil
}

// parseDotenv parses simple KEY=VALUE lines, ignoring blanks, comments, and
// optional `export ` prefixes. It does not expand variables.
func parseDotenv(content string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		m[key] = val
	}
	return m
}
