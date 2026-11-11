package codebox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const agentVMSecretsEnvPath = "/tmp/codebox-opencode.env"

// AgentVM orchestrates the OpenCode backend inside a Lima VM cloned from an
// agent-vm base template (https://github.com/sylvinus/agent-vm). codebox
// drives `limactl` directly rather than sourcing agent-vm.sh: the only
// agent-vm artifact it needs is the base template, built by the user with
// `agent-vm setup`. Runner and Starter are injectable so the lifecycle logic
// is testable without Lima.
type AgentVM struct {
	Config   Config
	Runner   Runner
	Starter  Starter
	StateDir string // host state dir; default ~/.local/state/codebox
	// Instance is the project-derived instance name (P05/P06). Empty keeps
	// the legacy singleton VM name from the config, so existing users' VMs
	// are found and never renamed.
	Instance string

	// KillPollInterval and KillMaxPolls bound the stale-backend kill loop.
	KillPollInterval time.Duration
	KillMaxPolls     int
	// Interactive runs a foreground command attached to the terminal. It is a
	// seam for tests; production uses RunInteractive.
	Interactive func(name string, args ...string) error
	// OpenCodeOverlay is the managed OpenCode configuration layer (P10).
	OpenCodeOverlay ManagedOverlay
}

// NewAgentVM builds an agent-vm orchestrator with production defaults.
func NewAgentVM(cfg Config) *AgentVM {
	return &AgentVM{
		Config:           cfg,
		Runner:           OSRunner{},
		Starter:          OSStarter{},
		StateDir:         DefaultStateDir(),
		KillPollInterval: time.Second,
		KillMaxPolls:     10,
	}
}

// NewAgentVMForInstance builds an agent-vm orchestrator bound to a
// project-derived instance name (P06).
func NewAgentVMForInstance(cfg Config, instance string) *AgentVM {
	a := NewAgentVM(cfg)
	a.Instance = instance
	return a
}

// VMName returns the name of the Lima instance this backend operates on: the
// project-derived name when an instance is set, the legacy config name
// otherwise.
func (a *AgentVM) VMName() string {
	if a.Instance != "" {
		return ManagedVMName(a.Instance)
	}
	return a.Config.AgentVMVM
}

// IsLegacy reports whether this backend operates on the legacy singleton VM.
func (a *AgentVM) IsLegacy() bool { return a.Instance == "" }

// LogPath returns the host path of the agent-vm backend log. The legacy
// singleton keeps its historical path; a project instance logs under its own
// state directory.
func (a *AgentVM) LogPath() string {
	if a.IsLegacy() {
		return AgentVMLogPath(a.StateDir)
	}
	return filepath.Join(InstanceStateDir(a.StateDir, a.Instance), "agent-vm.log")
}

// StageDir returns the host directory for files staged into the guest.
func (a *AgentVM) StageDir() string {
	if a.IsLegacy() {
		return AgentVMStageDir(a.StateDir)
	}
	return filepath.Join(InstanceStateDir(a.StateDir, a.Instance), "agent-vm")
}

// ParseLimaList parses `limactl list --format '{{.Name}}|{{.Status}}'` output
// into name -> running. A VM in any other state (Stopped, Breakout...) is
// present but not running.
func ParseLimaList(output string) map[string]bool {
	vms := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		if name == "" {
			continue
		}
		vms[name] = strings.TrimSpace(fields[1]) == "Running"
	}
	return vms
}

// limaList queries `limactl list` and returns the parsed VM map. It is the
// single choke point for existence checks, so the tristate discipline lives
// here: a failed query is an error, never "the VM does not exist".
func (a *AgentVM) limaList(ctx context.Context) (map[string]bool, error) {
	res, err := a.Runner.Run(ctx, "limactl", "list", "--format", "{{.Name}}|{{.Status}}")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("limactl list failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	return ParseLimaList(res.Stdout), nil
}

// vmExists reports whether the managed VM is known to Lima in any state.
func (a *AgentVM) vmExists(ctx context.Context) (bool, error) {
	vms, err := a.limaList(ctx)
	if err != nil {
		return false, err
	}
	_, ok := vms[a.VMName()]
	return ok, nil
}

// vmRunning reports whether the managed VM is running.
func (a *AgentVM) vmRunning(ctx context.Context) (bool, error) {
	vms, err := a.limaList(ctx)
	if err != nil {
		return false, err
	}
	return vms[a.VMName()], nil
}

// templateExists reports whether the base template is available to clone from.
func (a *AgentVM) templateExists(ctx context.Context) (bool, error) {
	vms, err := a.limaList(ctx)
	if err != nil {
		return false, err
	}
	_, ok := vms[a.Config.AgentVMTemplate]
	return ok, nil
}

// ownsBaseTemplate reports whether codebox is allowed to create or replace
// the base template. Only the default name is ours: any other
// AGENT_VM_TEMPLATE value names a template the user built and maintains, and
// silently rebuilding it would destroy their work.
func (a *AgentVM) ownsBaseTemplate() bool {
	return a.Config.AgentVMTemplate == DefaultAgentVMTemplate
}

// BaseTemplateSpec returns the base template description for the configured
// resources.
func (a *AgentVM) BaseTemplateSpec() BaseTemplateSpec {
	return BaseTemplateSpec{
		Name:     a.Config.AgentVMTemplate,
		Image:    a.Config.AgentVMImage,
		DiskGB:   a.Config.AgentVMDiskGB,
		MemoryGB: a.Config.AgentVMMemoryGB,
		CPUs:     a.Config.AgentVMCPUs,
	}
}

// ensureBaseTemplate builds the base template when it is missing or was left
// behind by an interrupted build. A template the user maintains (any
// non-default AGENT_VM_TEMPLATE) is never built: it is reported as missing
// instead, pointing at the variable that named it.
func (a *AgentVM) ensureBaseTemplate(ctx context.Context) error {
	if !a.ownsBaseTemplate() {
		return fmt.Errorf("base template %s not found; build it with 'agent-vm setup' or point AGENT_VM_TEMPLATE at an existing template", a.Config.AgentVMTemplate)
	}
	if a.Config.AgentVMResourcesErr != nil {
		return a.Config.AgentVMResourcesErr
	}
	// Checked before the first destructive step of the build (which deletes
	// any existing template), so an unresolvable image costs a second rather
	// than a failed multi-minute build with the old template already gone.
	if err := ValidateBaseTemplateImage(ctx, a.Runner, a.Config.AgentVMImage); err != nil {
		return err
	}
	if err := BuildBaseTemplate(ctx, a.Runner, a.BaseTemplateSpec(), a.templateMarkerPath(), mustAsset("agentvm-base-prep.sh"), os.Stdout); err != nil {
		return err
	}
	return nil
}

// templateMarkerPath is the host-side completion marker for an owned base
// template.
func (a *AgentVM) templateMarkerPath() string {
	return AgentVMTemplateMarkerPath(a.StateDir, a.Config.AgentVMTemplate)
}

// ownedTemplateUsable reports whether an existing template may be cloned.
//
// An instance that exists is not by itself proof of a finished build: an
// interrupted build registers the instance with `limactl create` and cannot
// run its own rollback, so the name alone would let Start clone a template
// that was never provisioned (no OpenCode in it). The marker is written only
// after provisioning and the final stop succeed, so for a template we own, its
// absence means "rebuild" rather than "use".
//
// A user-maintained template has no marker and is taken at face value: we did
// not build it, so we cannot judge it, and refusing to use it would break
// every existing setup.
func (a *AgentVM) ownedTemplateUsable() bool {
	if !a.ownsBaseTemplate() {
		return true
	}
	_, err := os.Stat(a.templateMarkerPath())
	return err == nil
}

// mountsJSON builds the Lima mounts array for the workspace: the host
// WORKSPACE_DIR mounted writable at the same path inside the guest, mirroring
// agent-vm's per-directory mount model.
func mountsJSON(workspaceDir string) string {
	mounts := []map[string]any{{
		"location": workspaceDir,
		"writable": true,
	}}
	b, _ := json.Marshal(mounts)
	return string(b)
}

// createVM clones the base template into the managed VM and applies the
// workspace mount, mirroring agent-vm's clone-then-edit flow. Editing is
// only allowed on a stopped instance, so it happens right after the clone.
func (a *AgentVM) createVM(ctx context.Context) error {
	fmt.Printf("Cloning %s to %s...\n", a.Config.AgentVMTemplate, a.VMName())
	res, err := a.Runner.Run(ctx, "limactl", "clone", a.Config.AgentVMTemplate, a.VMName(), "--tty=false")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl clone failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	res, err = a.Runner.Run(ctx, "limactl", "edit", a.VMName(), "--tty=false",
		"--set", ".mounts = "+mountsJSON(a.Config.WorkspaceDir))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		// Roll back the incomplete clone: a registered VM without the
		// workspace mount is unusable, and Start would treat it as a valid
		// existing instance instead of re-creating it.
		fmt.Printf("limactl edit (mounts) failed (exit %d): %s\n", res.ExitCode, res.Stderr)
		fmt.Printf("Removing the incomplete clone %s...\n", a.VMName())
		if _, delErr := a.Runner.Run(ctx, "limactl", "delete", a.VMName(), "--force"); delErr != nil {
			return fmt.Errorf("limactl edit (mounts) failed (exit %d): %s; cleanup also failed: %v (run 'limactl delete %s --force' manually)",
				res.ExitCode, res.Stderr, delErr, a.VMName())
		}
		return fmt.Errorf("limactl edit (mounts) failed (exit %d): %s (the incomplete clone was removed; retry the start)",
			res.ExitCode, res.Stderr)
	}
	return nil
}

// startVM boots the VM detached. The Starter's log captures limactl output.
func (a *AgentVM) startVM(ctx context.Context) error {
	return a.Starter.Start(nil, a.LogPath(), "limactl", "start", a.VMName())
}

// guestRun runs a command inside the VM synchronously via limactl shell.
func (a *AgentVM) guestRun(ctx context.Context, args ...string) error {
	full := append([]string{"shell", a.VMName()}, args...)
	return runOK(a.Runner, ctx, "limactl", full...)
}

// waitForAgent polls `limactl shell <vm> true` until the guest responds, up
// to 120 seconds. A Lima boot is slower than a microVM: the guest runs a
// full systemd.
func (a *AgentVM) waitForAgent(ctx context.Context) error {
	for i := 0; i < 120; i++ {
		res, err := a.Runner.Run(ctx, "limactl", "shell", a.VMName(), "true")
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("timed out waiting for %s guest agent", a.VMName())
}

// StopBackend terminates a stale opencode process inside the VM: SIGTERM via
// pkill, then SIGKILL after KillMaxPolls failed pgrep checks. Same recovery
// pattern as the Tart backend.
func (a *AgentVM) StopBackend(ctx context.Context) error {
	vm := a.VMName()
	// SIGTERM; "no process" (nonzero exit) is expected and ignored.
	_, _ = a.Runner.Run(ctx, "limactl", "shell", vm, "pkill", "-x", "opencode")

	for i := 0; ; i++ {
		res, err := a.Runner.Run(ctx, "limactl", "shell", vm, "pgrep", "-x", "opencode")
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			break // process exited
		}
		if i+1 >= a.KillMaxPolls {
			fmt.Fprintln(os.Stderr, "opencode ignored SIGTERM; force-killing...")
			_, _ = a.Runner.Run(ctx, "limactl", "shell", vm, "pkill", "-9", "-x", "opencode")
			break
		}
		time.Sleep(a.KillPollInterval)
	}

	res, err := a.Runner.Run(ctx, "limactl", "shell", vm, "pgrep", "-x", "opencode")
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return fmt.Errorf("failed to stop the previous opencode process; refusing to relaunch")
	}
	return nil
}

// writeSecretsEnv writes the backend secrets to a 0600 host file that is
// pushed into the guest with limactl copy and sourced by the launch script.
// Secrets never appear in argv or in the Lima instance config. The apiKey is
// resolved by the caller (credentialRef, legacy environment, or store).
func (a *AgentVM) writeSecretsEnv(dir string, keys map[string]string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "opencode.env")
	// The file is sourced by /bin/sh in the guest, so every value must be
	// single-quoted: raw values containing spaces, $(), backticks or
	// semicolons would break parsing or be evaluated. The provider config
	// rides along so the full-mode TUI sees the same Albert provider/model
	// definition as the backend-mode server.
	configContent, cerr := ComposeConfigContent(a.OpenCodeOverlay)
	if cerr != nil {
		configContent = opencodeConfigContent
	}
	// OpenCodeDisableUpdateAssignment rides in the file because this env file
	// is the only environment the guest processes inherit; the managed config
	// cannot carry the setting (see OpenCodeDisableUpdateEnv).
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nOPENCODE_PASSWORD=%s\nOPENCODE_SERVER_PASSWORD=%s\nOPENCODE_SERVER_USERNAME=%s\nOPENCODE_CONFIG_CONTENT=%s\n",
		OpenCodeDisableUpdateAssignment, shellQuote(a.Config.Password), shellQuote(a.Config.Password), shellQuote(a.Config.Username), shellQuote(configContent))
	for _, assignment := range providerKeyAssignments(keys) {
		name, value, _ := strings.Cut(assignment, "=")
		fmt.Fprintf(&b, "%s=%s\n", name, shellQuote(value))
	}
	content := b.String()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// shellQuote makes s safe for a POSIX shell to parse as a single-quoted
// word: wrap in single quotes, replacing embedded single quotes with the
// '\” idiom. This is the standard safe representation for arbitrary data.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ensureOpenCode upgrades the managed default guest in place. A custom
// template is user-owned and must provide the supported CLI itself.
func (a *AgentVM) ensureOpenCode(ctx context.Context) error {
	probe := func() bool {
		result, err := a.Runner.Run(ctx, "limactl", "shell", a.VMName(), "opencode", "--version")
		return err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) == "opencode v"+OpenCodeVersion
	}
	if probe() {
		return nil
	}
	if a.Config.AgentVMTemplate != DefaultAgentVMTemplate {
		return fmt.Errorf("custom agent-vm template requires OpenCode v%s; install @opencode/cli@%s in the guest before starting codebox", OpenCodeVersion, OpenCodeVersion)
	}
	fmt.Println("Updating managed OpenCode inside agent-vm (sessions are retained)...")
	if err := a.guestRun(ctx, "sh", "-c", "curl -fsSL https://opencode.ai/v2/install | bash -s -- --version "+OpenCodeVersion+" --no-modify-path"); err != nil {
		return err
	}
	if !probe() {
		return fmt.Errorf("agent-vm must provide OpenCode v%s after installation; check the guest PATH", OpenCodeVersion)
	}
	return nil
}

// launchBackend pushes the secrets env file into the guest and starts the
// OpenCode server detached, mirroring the Tart launch flow.
func (a *AgentVM) launchBackend(ctx context.Context) error {
	if err := a.ensureOpenCode(ctx); err != nil {
		return err
	}
	vm := a.VMName()
	fmt.Printf("Launching OpenCode server inside %s...\n", vm)
	if err := os.MkdirAll(a.StateDir, 0o755); err != nil {
		return err
	}
	stageDir := a.StageDir()
	// The credentials are resolved at launch time (credentialRef, legacy
	// environment, or the store).
	keys, err := ResolveProviderKeysWith(readStoredWith, ctx, a.Config, "")
	if err != nil {
		return err
	}
	secretsPath, err := a.writeSecretsEnv(stageDir, keys)
	if err != nil {
		return err
	}
	// Push the env file into the guest at a fixed location.
	if err := runOK(a.Runner, ctx, "limactl", "copy", secretsPath, vm+":/tmp/codebox-opencode.env"); err != nil {
		return err
	}
	// Ensure the file is only readable by the guest user.
	if err := a.guestRun(ctx, "chmod", "600", "/tmp/codebox-opencode.env"); err != nil {
		return err
	}
	// Launch detached: source the env file, then exec opencode serve in the
	// background. The Starter writes output to the host-side log.
	launch := fmt.Sprintf(`set -a; . /tmp/codebox-opencode.env; set +a; exec opencode serve --hostname 0.0.0.0 --port %d`, DefaultPort)
	args := []string{"shell", vm, "sh", "-c", launch}
	return a.Starter.Start(nil, a.LogPath(), "limactl", args...)
}

// validateConfig checks the deterministic start-time configuration
// (credential, acknowledgement). Restart calls it before stopping so a
// configuration error cannot stop a VM that Start would then refuse to boot.
//
// agent-vm has no secret proxy: the credential travels into the guest in
// plaintext (a 0600 env file pushed with limactl copy), where any process —
// the agent included — can read and exfiltrate it. Starting therefore
// requires the explicit --acknowledge-guest-credentials flag (P09, issue
// #74); the warning stays so the choice is visible on every start.
func (a *AgentVM) validateConfig(ctx context.Context) error {
	if !a.Config.GuestCredentialsAcknowledged {
		return fmt.Errorf("agent-vm hands the inference credentials to the guest in plaintext, where any process (the agent included) can read them; " +
			"pass --acknowledge-guest-credentials to accept this, or use --microsandbox, which keeps the credential behind the secret proxy")
	}
	if _, err := ResolveProviderKeysWith(readStoredWith, ctx, a.Config, ""); err != nil {
		return err
	}
	warnUnprotectedRuntime("agent-vm")
	return nil
}

// Start brings the OpenCode backend up on the agent-vm runtime.
func (a *AgentVM) Start(ctx context.Context) error {
	cfg := a.Config
	if err := a.validateConfig(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return err
	}
	if err := CheckWorkspaceGate(ctx, cfg.WorkspaceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(a.StateDir, 0o755); err != nil {
		return err
	}

	vm := a.VMName()
	running, err := a.vmRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		if err := a.waitForAgent(ctx); err != nil {
			return err
		}
		if cfg.Isolation == IsolationFull {
			fmt.Printf("%s is running (isolation full; the TUI runs inside the VM).\n", vm)
			return nil
		}
		endpoint, err := a.Endpoint(ctx)
		if err != nil {
			return err
		}
		client := &http.Client{Timeout: 5 * time.Second}
		if ProbeHealth(ctx, client, endpoint, cfg.Username, cfg.Password).Healthy {
			fmt.Printf("%s is running with a healthy OpenCode backend.\n", vm)
			return nil
		}
		fmt.Printf("%s is running but OpenCode is not healthy; restarting backend...\n", vm)
		if err := a.StopBackend(ctx); err != nil {
			return err
		}
		return a.launchBackend(ctx)
	}

	exists, err := a.vmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		templateOK, err := a.templateExists(ctx)
		if err != nil {
			return err
		}
		if !templateOK || !a.ownedTemplateUsable() {
			if err := a.ensureBaseTemplate(ctx); err != nil {
				return err
			}
		}
		if err := a.createVM(ctx); err != nil {
			return err
		}
	}

	fmt.Printf("Starting %s with Lima...\n", vm)
	if err := a.startVM(ctx); err != nil {
		return err
	}
	if err := a.waitForAgent(ctx); err != nil {
		return err
	}
	if cfg.Isolation == IsolationFull {
		fmt.Printf("%s is running (isolation full; the TUI runs inside the VM).\n", vm)
		return nil
	}
	return a.launchBackend(ctx)
}

// Stop stops the managed VM. Unlike Tart (which stops every managed VM),
// agent-vm has a single managed instance, so there is exactly one to stop.
// Stop stops this backend's VM. It is project-scoped (P06); the
// all-instance sweep is StopInstance over RunningInstances.
func (a *AgentVM) Stop(ctx context.Context) error {
	return a.StopInstance(ctx, a.VMName())
}

// RunningInstances returns the names of all managed Lima instances that are
// up, in deterministic order. Managed means carrying the codebox prefix;
// the base template (agent-vm-base) does not, so it is never touched.
func (a *AgentVM) RunningInstances(ctx context.Context) ([]string, error) {
	vms, err := a.limaList(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, running := range vms {
		if running && IsManagedVM(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// StopInstance stops a named managed Lima instance. It is the per-project
// form of Stop.
func (a *AgentVM) StopInstance(ctx context.Context, vm string) error {
	vms, err := a.limaList(ctx)
	if err != nil {
		return err
	}
	if !vms[vm] {
		fmt.Printf("%s is not running.\n", vm)
		return nil
	}
	fmt.Printf("Stopping %s...\n", vm)
	res, err := a.Runner.Run(ctx, "limactl", "stop", vm)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl stop failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// StopAll stops every running managed Lima instance, the explicit
// all-instance operation (P06).
func (a *AgentVM) StopAll(ctx context.Context) error {
	vms, err := a.RunningInstances(ctx)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("No codebox agent-vm VM is running.")
		return nil
	}
	var firstErr error
	for _, vm := range vms {
		if err := a.StopInstance(ctx, vm); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Clean stops and deletes the managed VM and its local state.
func (a *AgentVM) Clean(ctx context.Context) error {
	vm := a.VMName()
	running, err := a.vmRunning(ctx)
	if err != nil {
		return err
	}
	if running {
		if _, err := a.Runner.Run(ctx, "limactl", "stop", vm); err != nil {
			return err
		}
	}
	exists, err := a.vmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("%s does not exist.\n", vm)
		// The staged secrets env (Albert API key, HTTP password) is host-side
		// state and must go even when the VM was already deleted manually.
		_ = os.RemoveAll(a.StageDir())
		return nil
	}
	// Deleting is destructive: say so before and after, so a silent success
	// can never be mistaken for "nothing happened".
	fmt.Printf("Deleting %s (VM and local state)...\n", vm)
	res, err := a.Runner.Run(ctx, "limactl", "delete", vm, "--force")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl delete failed (exit %d): %s", res.ExitCode, res.Stderr)
	}
	_ = os.RemoveAll(a.StageDir())
	fmt.Printf("%s deleted.\n", vm)
	return nil
}

// Doctor verifies the Lima installation and the base template, mirroring the
// pre-flight contract of the other runtimes.
func (a *AgentVM) Doctor(ctx context.Context) error {
	res, err := a.Runner.Run(ctx, "limactl", "--version")
	if err != nil {
		if commandNotFound(err) {
			return fmt.Errorf("limactl is not installed; install Lima from https://lima-vm.io and build the base template with 'agent-vm setup'")
		}
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("limactl is not installed")
	}
	fmt.Print(res.Stdout)
	templateOK, err := a.templateExists(ctx)
	if err != nil {
		return err
	}
	if !templateOK || !a.ownedTemplateUsable() {
		if a.ownsBaseTemplate() {
			// Doctor stays read-only: building the template is a multi-minute
			// side effect, so it is reported here and performed by Start.
			// An existing but unmarked template is the interrupted-build case,
			// and Start rebuilds it.
			//
			// The image is checked here because this is the last moment it can
			// be reported before Start rebuilds: a start that fails on it has
			// already deleted the previous template.
			if err := ValidateBaseTemplateImage(ctx, a.Runner, a.Config.AgentVMImage); err != nil {
				return err
			}
			fmt.Printf("base template %s is not ready; it will be built on the next start from %s.\n", a.Config.AgentVMTemplate, a.Config.AgentVMImage)
			return nil
		}
		return fmt.Errorf("base template %s not found; build it with 'agent-vm setup' or point AGENT_VM_TEMPLATE at an existing template", a.Config.AgentVMTemplate)
	}
	fmt.Printf("agent-vm runtime is ready (template %s).\n", a.Config.AgentVMTemplate)
	return nil
}

func (a *AgentVM) ID() Runtime { return RuntimeAgentVM }

// Restart is non-destructive (P07): it stops and starts the existing VM,
// preserving disk and guest state. The preflights run first so a rejected
// workspace aborts before anything is touched. The destructive rebuild is
// Recreate.
func (a *AgentVM) Restart(ctx context.Context) error {
	if err := a.RestartPreflights(ctx); err != nil {
		return err
	}
	if err := a.Stop(ctx); err != nil {
		return err
	}
	return a.Start(ctx)
}

// Recreate is the explicit destructive rebuild of the VM (P07). It names the
// loss before and after. No lifecycle path reaches it implicitly.
func (a *AgentVM) Recreate(ctx context.Context) error {
	fmt.Printf("Recreating %s. This DESTROYS: guest sessions, tools installed in the guest, and guest-only files.\n", a.VMName())
	if err := a.RestartPreflights(ctx); err != nil {
		return err
	}
	if err := a.Clean(ctx); err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("%s recreated.\n", a.VMName())
	return nil
}

// RestartPreflights runs the deterministic prechecks shared by the lifecycle
// commands: configuration validation, workspace creation and the workspace
// gate. A rejected workspace or bad config must abort before any destructive
// step, so the VM and its persistent state survive.
func (a *AgentVM) RestartPreflights(ctx context.Context) error {
	if err := a.validateConfig(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(a.Config.WorkspaceDir, 0o755); err != nil {
		return err
	}
	return CheckWorkspaceGate(ctx, a.Config.WorkspaceDir)
}

// Logs follows the backend log.
func (a *AgentVM) Logs() error {
	return RunInteractive("tail", "-f", a.LogPath())
}

// Shell opens an interactive shell in the VM.
func (a *AgentVM) Shell() error {
	return RunInteractive("limactl", "shell", a.VMName())
}

// RunAgent launches the OpenCode TUI in the foreground inside the VM
// (isolation full). The secrets env file is pushed with limactl copy (same
// channel as the backend flow), then the TUI runs under `limactl shell`,
// sourcing the 0600 env file and execing opencode --continue in the workspace
// mount, which shares the host's absolute path. The TUI's /new command starts
// a fresh session.
func (a *AgentVM) RunAgent(ctx context.Context) error {
	if a.Config.EffectiveHarness() == HarnessPi {
		return a.runPiAgent(ctx)
	}
	if err := a.ensureOpenCode(ctx); err != nil {
		return err
	}
	cfg := a.Config
	vm := a.VMName()
	if err := os.MkdirAll(a.StateDir, 0o755); err != nil {
		return err
	}
	keys, err := ResolveProviderKeysWith(readStoredWith, ctx, cfg, "")
	if err != nil {
		return err
	}
	secretsPath, err := a.writeSecretsEnv(a.StageDir(), keys)
	if err != nil {
		return err
	}
	if err := runOK(a.Runner, ctx, "limactl", "copy", secretsPath, vm+":"+agentVMSecretsEnvPath); err != nil {
		return err
	}
	if err := a.guestRun(ctx, "chmod", "600", agentVMSecretsEnvPath); err != nil {
		return err
	}
	listCommand := fmt.Sprintf("set -a; . %s; set +a; cd %s && opencode session list --standalone --format json", agentVMSecretsEnvPath, shellQuote(cfg.WorkspaceDir))
	sessions, err := a.Runner.Run(ctx, "limactl", "shell", vm, "sh", "-c", listCommand)
	var listErr error
	if err != nil {
		listErr = err
	} else if sessions.ExitCode != 0 {
		listErr = fmt.Errorf("OpenCode session list exited with code %d", sessions.ExitCode)
	}
	hasSessions := false
	if listErr == nil {
		hasSessions, listErr = OpenCodeHasSessions(sessions.Stdout)
	}
	var helpOutput string
	var helpErr error
	if listErr == nil && hasSessions {
		helpCommand := fmt.Sprintf("set -a; . %s; set +a; opencode --help", agentVMSecretsEnvPath)
		help, err := a.Runner.Run(ctx, "limactl", "shell", vm, "sh", "-c", helpCommand)
		if err != nil {
			helpErr = err
		} else if help.ExitCode != 0 {
			helpErr = fmt.Errorf("OpenCode help exited with code %d", help.ExitCode)
		} else {
			helpOutput = help.Stdout + "\n" + help.Stderr
		}
	}
	resumeArgs, warn := OpenCodeResumeArgs(hasSessions, listErr, helpOutput, helpErr)
	if warn {
		WarnOpenCodeResumeUnavailable("dans le guest agent-vm")
	}
	resumeFlag := strings.Join(resumeArgs, " ")
	if resumeFlag != "" {
		resumeFlag = " " + resumeFlag
	}
	launch := fmt.Sprintf(`set -a; . %s; set +a; cd %s; exec opencode --standalone%s`, agentVMSecretsEnvPath, shellQuote(cfg.WorkspaceDir), resumeFlag)
	interactive := a.Interactive
	if interactive == nil {
		interactive = RunInteractive
	}
	return interactive("limactl", "shell", vm, "sh", "-c", launch)
}

// runPiAgent launches Pi's TUI inside the guest (#176). Pi has no server mode,
// so the whole agent — configuration, skills and sessions — stays in the
// guest and the host is only a terminal passthrough. The preparation mirrors
// the OpenCode path where it must: the secrets env file is written host-side
// and copied in, because Pi reads its provider key from the environment.
func (a *AgentVM) runPiAgent(ctx context.Context) error {
	cfg := a.Config
	vm := a.VMName()
	if err := os.MkdirAll(a.StateDir, 0o755); err != nil {
		return err
	}
	keys, err := ResolveProviderKeysWith(readStoredWith, ctx, cfg, "")
	if err != nil {
		return err
	}
	secretsPath, err := a.writeSecretsEnv(a.StageDir(), keys)
	if err != nil {
		return err
	}
	if err := runOK(a.Runner, ctx, "limactl", "copy", secretsPath, vm+":"+agentVMSecretsEnvPath); err != nil {
		return err
	}
	if err := a.guestRun(ctx, "chmod", "600", agentVMSecretsEnvPath); err != nil {
		return err
	}
	// Install the pinned release first: a custom template may ship Node
	// without Pi, and the managed template deliberately does not install Pi.
	if err := a.guestRun(ctx, "sh", "-c", piGuestInstallScript()); err != nil {
		return err
	}
	configScript, err := piGuestConfigScript(a.OpenCodeOverlay)
	if err != nil {
		return err
	}
	if err := a.guestRun(ctx, "sh", "-c", configScript); err != nil {
		return err
	}
	launchArgs := append(piModelArgs(a.OpenCodeOverlay), piResumeArgs(a.piGuestHasSessions(ctx, vm))...)
	launch := piAgentLaunch(agentVMSecretsEnvPath, cfg.WorkspaceDir, launchArgs)
	interactive := a.Interactive
	if interactive == nil {
		interactive = RunInteractive
	}
	return interactive("limactl", "shell", vm, "sh", "-c", launch)
}

// piGuestHasSessions reports whether the guest holds a saved Pi session. A
// probe that cannot run reports "no session": starting a fresh session is
// always a valid launch, unlike attaching to a guest that is not there.
func (a *AgentVM) piGuestHasSessions(ctx context.Context, vm string) bool {
	result, err := a.Runner.Run(ctx, "limactl", "shell", vm, "sh", "-c", piGuestSessionProbe())
	if err != nil || result.ExitCode != 0 {
		return false
	}
	return strings.TrimSpace(result.Stdout) != ""
}

// Status describes the managed VM's current state, for `check` in isolation
// full where there is no health endpoint to probe.
func (a *AgentVM) Status(ctx context.Context) (string, error) {
	running, err := a.vmRunning(ctx)
	if err != nil {
		return "", err
	}
	if running {
		return fmt.Sprintf("%s is running", a.VMName()), nil
	}
	exists, err := a.vmExists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return fmt.Sprintf("%s is stopped", a.VMName()), nil
	}
	return fmt.Sprintf("%s does not exist", a.VMName()), nil
}

// IsRunning reports whether the managed VM is running.
func (a *AgentVM) IsRunning(ctx context.Context) (bool, error) {
	running, err := a.vmRunning(ctx)
	if err != nil {
		if commandNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return running, nil
}

// Endpoint returns the backend URL. Lima's dynamic port forwarding exposes
// guest localhost ports on the host at 127.0.0.1, so the endpoint is
// host-local and needs no IP discovery.
func (a *AgentVM) Endpoint(ctx context.Context) (string, error) {
	return "http://127.0.0.1:" + strconv.Itoa(DefaultPort), nil
}
