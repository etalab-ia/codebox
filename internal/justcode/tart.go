package justcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tart orchestrates the OpenCode backend inside a Tart macOS VM. Runner and
// Starter are injectable so the lifecycle logic is testable without a real
// hypervisor.
type Tart struct {
	Config   Config
	Runner   Runner
	Starter  Starter
	StateDir string // host state dir; default ~/.local/state/just-code
	// Instance is the project-derived instance name (P05/P06). Empty keeps
	// the legacy singleton VM name from the config, so existing users' VMs
	// are found and never renamed.
	Instance string
	// SelfBinary is the binary staged into the guest. Empty means the running
	// executable (os.Executable), which is the production path.
	SelfBinary string
	// Interactive runs a foreground command attached to the terminal. It is a
	// seam for tests; production uses RunInteractive.
	Interactive func(name string, args ...string) error
	// CredentialRead reads a stored credential; production uses
	// readStoredWith. It is a test seam.
	CredentialRead credentialReader
	// OpenCodeOverlay is the managed OpenCode configuration layer (P10).
	OpenCodeOverlay ManagedOverlay

	KillPollInterval time.Duration
	KillMaxPolls     int
	// AgentWaitInterval and AgentWaitPolls bound the guest-agent wait, so a
	// test can exercise the timeout path without waiting a real minute.
	AgentWaitInterval time.Duration
	AgentWaitPolls    int
}

var tartHealthProbeFn = ProbeHealth

// NewTart builds a Tart orchestrator with production defaults.
func NewTart(cfg Config) *Tart {
	return &Tart{
		Config:            cfg,
		Runner:            OSRunner{},
		Starter:           OSStarter{},
		StateDir:          DefaultStateDir(),
		KillPollInterval:  time.Second,
		KillMaxPolls:      10,
		AgentWaitInterval: time.Second,
		AgentWaitPolls:    60,
	}
}

// NewTartForInstance builds a Tart orchestrator bound to a project-derived
// instance name (P06). The VM name becomes opencode-<instance>, and logs and
// staged files live under the instance's own state directory.
func NewTartForInstance(cfg Config, instance string) *Tart {
	t := NewTart(cfg)
	t.Instance = instance
	return t
}

// VMName returns the name of the VM this backend operates on: the project-
// derived name when an instance is set, the legacy config name otherwise.
func (t *Tart) VMName() string {
	if t.Instance != "" {
		return ManagedVMName(t.Instance)
	}
	return t.Config.TartVM
}

// IsLegacy reports whether this backend operates on the legacy singleton VM.
func (t *Tart) IsLegacy() bool { return t.Instance == "" }

// LogPath returns the host path of the Tart backend log. The legacy
// singleton keeps its historical path; a project instance logs under its
// own state directory.
func (t *Tart) LogPath() string {
	if t.IsLegacy() {
		return TartLogPath(t.StateDir)
	}
	return filepath.Join(InstanceStateDir(t.StateDir, t.Instance), "tart.log")
}

// StageDir returns the host directory for files staged into the guest.
func (t *Tart) StageDir() string {
	if t.IsLegacy() {
		return TartStageDir(t.StateDir)
	}
	return filepath.Join(InstanceStateDir(t.StateDir, t.Instance), "tart")
}

// ParseTartList extracts local, running VMs under prefix from `tart list`
// output. Columns are: source, name, ip, disk, size, state.
func ParseTartList(output, prefix string) []string {
	var vms []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "local" {
			continue
		}
		name := fields[1]
		if !strings.HasPrefix(name, prefix) || fields[len(fields)-1] != "running" {
			continue
		}
		vms = append(vms, name)
	}
	return vms
}

// RunningVMs returns the local, running managed VMs.
func (t *Tart) RunningVMs(ctx context.Context) ([]string, error) {
	res, err := t.Runner.Run(ctx, "tart", "list")
	if err != nil {
		return nil, err
	}
	return ParseTartList(res.Stdout, managedVMPrefix), nil
}

// IP returns the NAT IP of a VM, mirroring `tart ip --wait 60 <vm>`.
func (t *Tart) IP(ctx context.Context, vm string) (string, error) {
	res, err := t.Runner.Run(ctx, "tart", "ip", "--wait", "60", vm)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// vmRunning reports whether a specific VM is local and running.
func (t *Tart) vmRunning(ctx context.Context, vm string) (bool, error) {
	vms, err := t.RunningVMs(ctx)
	if err != nil {
		return false, err
	}
	for _, v := range vms {
		if v == vm {
			return true, nil
		}
	}
	return false, nil
}

// vmExists reports whether a VM is known locally in any state.
func (t *Tart) vmExists(ctx context.Context) (bool, error) {
	res, err := t.Runner.Run(ctx, "tart", "list")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "local" && fields[1] == t.VMName() {
			return true, nil
		}
	}
	return false, nil
}

// Stop stops this backend's VM. It is project-scoped (P06): stopping project
// A's VM never touches project B's. The all-instance sweep lives in
// StopInstance over RunningInstances, driven by the dispatcher.
func (t *Tart) Stop(ctx context.Context) error {
	return t.StopInstance(ctx, t.VMName())
}

// RunningInstances returns the names of all managed Tart VMs that are up,
// in deterministic order. Managed means carrying the just-code prefix.
func (t *Tart) RunningInstances(ctx context.Context) ([]string, error) {
	vms, err := t.RunningVMs(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(vms)
	return vms, nil
}

// StopInstance stops a named managed VM. It is the per-project form of Stop.
func (t *Tart) StopInstance(ctx context.Context, vm string) error {
	running, err := t.vmRunning(ctx, vm)
	if err != nil {
		return err
	}
	if !running {
		fmt.Printf("%s is not running.\n", vm)
		return nil
	}
	fmt.Printf("Stopping %s...\n", vm)
	res, err := t.Runner.Run(ctx, "tart", "stop", vm, "--timeout", "5")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("tart stop failed (exit %d)", res.ExitCode)
	}
	return nil
}

// StopAll stops every running managed Tart VM, the explicit all-instance
// operation (P06). The legacy singleton is included when it carries the
// managed prefix, which it does by construction.
func (t *Tart) StopAll(ctx context.Context) error {
	vms, err := t.RunningInstances(ctx)
	if err != nil {
		return err
	}
	if len(vms) == 0 {
		fmt.Println("No just-code Tart VM is running.")
		return nil
	}
	var firstErr error
	for _, vm := range vms {
		if err := t.StopInstance(ctx, vm); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// StopBackend terminates a stale opencode process inside the VM: SIGTERM via
// pkill, then SIGKILL after KillMaxPolls failed pgrep checks. It returns an
// error if the process is still alive afterward.
func (t *Tart) StopBackend(ctx context.Context) error {
	vm := t.VMName()
	// SIGTERM; "no process" (nonzero exit) is expected and ignored.
	_, _ = t.Runner.Run(ctx, "tart", "exec", vm, "pkill", "-x", "opencode")

	for i := 0; ; i++ {
		res, err := t.Runner.Run(ctx, "tart", "exec", vm, "pgrep", "-x", "opencode")
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			break // process exited
		}
		if i+1 >= t.KillMaxPolls {
			fmt.Fprintln(os.Stderr, "opencode ignored SIGTERM; force-killing...")
			_, _ = t.Runner.Run(ctx, "tart", "exec", vm, "pkill", "-9", "-x", "opencode")
			break
		}
		time.Sleep(t.KillPollInterval)
	}

	res, err := t.Runner.Run(ctx, "tart", "exec", vm, "pgrep", "-x", "opencode")
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return fmt.Errorf("failed to stop the previous opencode process; refusing to relaunch")
	}
	return nil
}

// stageGuestBinary copies the running binary into the read-only share so the
// guest can execute it as the in-VM bootstrap. The checkout, its .env, and
// other host-only files are never shared.
func (t *Tart) stageGuestBinary() error {
	stageDir := t.StageDir()
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return err
	}
	src := t.SelfBinary
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		src = exe
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stageDir, guestBinaryName), data, 0o755)
}

// guestBinaryName is the file name of the staged binary inside the share.
const guestBinaryName = "just-code"

// guestRun runs a command inside the VM synchronously.
func (t *Tart) guestRun(ctx context.Context, args ...string) error {
	full := append([]string{"exec", t.VMName()}, args...)
	return runOK(t.Runner, ctx, "tart", full...)
}

// BackendArgs returns the argv for the detached bootstrap invocation. The
// enabled model definitions and MCP selection are non-secret configuration;
// credentials remain on stdin.
func BackendArgs(vm, localBinary, port, username, mtu string, overlay ManagedOverlay, gitName, gitEmail string, mcps ...string) ([]string, error) {
	overlayJSON, err := encodeGuestOverlay(overlay)
	if err != nil {
		return nil, err
	}
	args := []string{"exec", "-i", vm, localBinary}
	return append(args, guestBootstrapArgs(port, username, mtu, overlay.EffectiveOverlay().Model, overlayJSON, gitName, gitEmail, mcps)...), nil
}

func guestBootstrapArgs(port, username, mtu, model, modelsJSON, gitName, gitEmail string, mcps []string) []string {
	args := []string{GuestBootstrapCommand, port, username, mtu, model, modelsJSON, gitName, gitEmail}
	if len(mcps) > 0 {
		args = append(args, strings.Join(mcps, ","))
	}
	return args
}

func guestSecretsArgs(username string, overlay ManagedOverlay, mcps []string) ([]string, error) {
	overlayJSON, err := encodeGuestOverlay(overlay)
	if err != nil {
		return nil, err
	}
	args := []string{GuestSecretsCommand, username, overlay.EffectiveOverlay().Model, overlayJSON}
	if len(mcps) > 0 {
		args = append(args, strings.Join(mcps, ","))
	}
	return args, nil
}

// guestOverlayWire is the argv/JSON form of the managed overlay. It carries no
// secret: provider entries name the guest environment variable, never a value.
type guestOverlayWire struct {
	Models        []AlbertModelConfig `json:"models"`
	Providers     []ProviderOverlay   `json:"providers,omitempty"`
	AlbertEnabled bool                `json:"albertEnabled,omitempty"`
	SmallModel    string              `json:"smallModel,omitempty"`
}

// encodeGuestOverlay serialises the managed overlay for the guest helpers.
func encodeGuestOverlay(overlay ManagedOverlay) (string, error) {
	data, err := json.Marshal(guestOverlayWire{Models: overlay.Models, Providers: overlay.Providers, AlbertEnabled: overlay.AlbertEnabled, SmallModel: overlay.SmallModel})
	if err != nil {
		return "", fmt.Errorf("encode guest model configuration: %w", err)
	}
	return string(data), nil
}

// DecodeGuestOverlay parses the guest-visible overlay. It accepts the
// historical bare-array form (a list of Albert models) so a guest binary
// staged by an older host still understands the arguments it receives.
func DecodeGuestOverlay(raw string) (ManagedOverlay, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return ManagedOverlay{}, nil
	}
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var models []AlbertModelConfig
		if err := json.Unmarshal([]byte(trimmed), &models); err != nil {
			return ManagedOverlay{}, err
		}
		if err := validateGuestModels(models); err != nil {
			return ManagedOverlay{}, err
		}
		return ManagedOverlay{Models: models}, nil
	}
	var wire guestOverlayWire
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return ManagedOverlay{}, err
	}
	if err := validateGuestModels(wire.Models); err != nil {
		return ManagedOverlay{}, err
	}
	for _, p := range wire.Providers {
		if strings.TrimSpace(p.Kind) == "" || strings.TrimSpace(p.APIKeyEnv) == "" || strings.TrimSpace(p.BaseURL) == "" {
			return ManagedOverlay{}, fmt.Errorf("provider kind, base URL and API key variable must be non-empty")
		}
	}
	return ManagedOverlay{Models: wire.Models, Providers: wire.Providers, AlbertEnabled: wire.AlbertEnabled, SmallModel: wire.SmallModel}, nil
}

func validateGuestModels(models []AlbertModelConfig) error {
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Name) == "" {
			return fmt.Errorf("model IDs and names must be non-empty")
		}
	}
	return nil
}

// hostGitIdentity reads the git identity (P11, written by setup) from the
// host user settings. The guest helpers cannot read it themselves: they run
// inside the VM, where the host settings file does not exist, so the
// identity travels on argv like the model — never on stdin (it is not a
// secret, but stdin is the secrets channel and must stay unambiguous).
func hostGitIdentity() (name, email string) {
	path, err := UserSettingsPath()
	if err != nil {
		return "", ""
	}
	us, err := ReadUserSettings(DefaultFS, path)
	if err != nil {
		return "", ""
	}
	return us.GitName, us.GitEmail
}

// SecretsReader returns the bootstrap stdin payload: password on line 1, API
// key on line 2.
func SecretsReader(password, apiKey string) io.Reader {
	return strings.NewReader(password + "\n" + apiKey + "\n")
}

// SecretsReaderWithKeys returns the multi-platform bootstrap stdin payload:
// password on line 1, a JSON object of provider key assignments on line 2.
// Only the values listed in keys travel; no other credential is inferred.
func SecretsReaderWithKeys(password string, keys map[string]string) (io.Reader, error) {
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("encode guest provider keys: %w", err)
	}
	return strings.NewReader(password + "\n" + string(data) + "\n"), nil
}

// launchBackend stages the binary, copies it to the guest's local disk, and
// starts the OpenCode server inside the guest in the background.
func (t *Tart) launchBackend(ctx context.Context) error {
	cfg := t.Config
	vm := t.VMName()
	fmt.Printf("Launching OpenCode server inside %s...\n", vm)
	if err := os.MkdirAll(t.StateDir, 0o755); err != nil {
		return err
	}
	if err := t.stageGuestBinary(); err != nil {
		return err
	}

	// Executing straight from the virtiofs share is not reliably supported, so
	// the payload is copied onto the guest's local disk first.
	if err := t.guestRun(ctx, "/bin/cp", guestShareBinary, guestLocalBinary); err != nil {
		return err
	}
	if err := t.guestRun(ctx, "/bin/chmod", "755", guestLocalBinary); err != nil {
		return err
	}

	// The credentials are resolved at launch time (credentialRef, legacy
	// environment, or the store) and travel on stdin only — never argv.
	keys, err := t.resolveProviderKeysFor(ctx)
	if err != nil {
		return err
	}
	stdin, err := SecretsReaderWithKeys(cfg.Password, keys)
	if err != nil {
		return err
	}
	name, email := hostGitIdentity()
	args, err := BackendArgs(vm, guestLocalBinary, strconv.Itoa(DefaultPort), cfg.Username, cfg.TartMTU, t.OpenCodeOverlay, name, email, t.OpenCodeOverlay.MCPConnectors...)
	if err != nil {
		return err
	}
	if err := t.Starter.Start(stdin, t.LogPath(), "tart", args...); err != nil {
		return err
	}
	return t.recordBackendOverlayFingerprint()
}

func (t *Tart) backendOverlayFingerprint() (string, error) {
	content, err := ComposeConfigContent(t.OpenCodeOverlay)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:]), nil
}

func (t *Tart) backendOverlayFingerprintPath() string {
	dir := t.StateDir
	if t.Instance != "" {
		dir = InstanceStateDir(t.StateDir, t.Instance)
	}
	return filepath.Join(dir, "tart-opencode-config.sha256")
}

func (t *Tart) overlayChangedSinceBackendLaunch() (bool, error) {
	want, err := t.backendOverlayFingerprint()
	if err != nil {
		return false, err
	}
	data, err := DefaultFS.ReadFile(t.backendOverlayFingerprintPath())
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read Tart OpenCode configuration fingerprint: %w", err)
	}
	return strings.TrimSpace(string(data)) != want, nil
}

func (t *Tart) recordBackendOverlayFingerprint() error {
	fingerprint, err := t.backendOverlayFingerprint()
	if err != nil {
		return err
	}
	path := t.backendOverlayFingerprintPath()
	if err := DefaultFS.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(DefaultFS, path, []byte(fingerprint+"\n"), 0o600)
}

// waitForAgent polls `tart exec <vm> true` until the guest agent responds, up
// to 60 seconds.
//
// fromOffset is the log length captured *before* the VM was launched: only
// bytes past it belong to this launch, and startup-log diagnosis applies only
// in that case. noLaunch says this call did not boot the VM but attached to one
// already running; then the log cannot describe this call's boot at all, and a
// failure in it belongs to an earlier run — so it is not read, rather than read
// from some arbitrary point.
//
// The offset is a parameter rather than something read here on purpose. The
// start failure this function diagnoses is emitted within seconds of the
// detached `tart run` starting, and Start has already returned by the time the
// first probe runs — so taking the mark here would seek past the very error it
// is looking for and fall back to the generic timeout this fixes. It must be
// captured before startVM.
func (t *Tart) waitForAgent(ctx context.Context, fromOffset int64) error {
	polls := t.AgentWaitPolls
	if polls <= 0 {
		// A zero value is a test-built Tart that did not set the seam; the
		// production default is what a real orchestrator carries, but a struct
		// literal must not silently disable the wait entirely.
		polls = 60
	}
	interval := t.AgentWaitInterval
	if interval <= 0 {
		interval = time.Second
	}
	// The VM log is append-only: OSStarter opens it with O_APPEND and nothing
	// rotates it. A previous failed run therefore still contains its VZ error,
	// and scanning from byte 0 would report that stale failure on a healthy
	// retry — precisely when the user has just unlocked the keychain and tried
	// again. Only bytes written past the pre-launch mark belong to this run.
	diagnoseStartup := fromOffset != noLaunch
	logOffset := fromOffset
	for i := 0; i < polls; i++ {
		res, err := t.Runner.Run(ctx, "tart", "exec", t.VMName(), "true")
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		// A guest that never answers is usually a guest that never booted: on
		// a headless Mac, Virtualization.framework refuses to create the VM's
		// host key when the login keychain is locked, and the run dies within
		// seconds. Polling for the full timeout and then reporting a generic
		// "guest agent" timeout hides the real cause, which is only visible in
		// the VM log. Stop as soon as the log names a startup failure.
		if diagnoseStartup {
			if reason := tartStartupFailure(t.LogPath(), logOffset); reason != "" {
				return fmt.Errorf("tart could not start %s: %s\n"+
					"The VM never booted, so no guest agent could answer. On a machine with no GUI session "+
					"(SSH, a rented Mac), Virtualization.framework cannot create the VM host key when the login "+
					"keychain is locked. Unlock that keychain or open a GUI session, then retry. Full log: %s",
					t.VMName(), reason, t.LogPath())
			}
		}
		time.Sleep(interval)
	}
	if diagnoseStartup {
		if reason := tartStartupFailure(t.LogPath(), logOffset); reason != "" {
			return fmt.Errorf("timed out waiting for %s guest agent; the VM log reports a startup failure: %s (full log: %s)",
				t.VMName(), reason, t.LogPath())
		}
	}
	return fmt.Errorf("timed out waiting for %s guest agent", t.VMName())
}

// noLaunch marks a waitForAgent call that attached to an already-running VM
// rather than booting one, so startup-log diagnosis is skipped entirely.
const noLaunch = -1

// logSize reports the current size of a log file, or 0 when it cannot be
// read. Zero is the right fallback: it scans the whole file, which is what an
// absent or unreadable log would have produced anyway.
func logSize(logPath string) int64 {
	info, err := os.Stat(logPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

// tartStartupFailure extracts the first Virtualization.framework startup error
// written after fromOffset, or "" when the log shows no such failure. The log
// is append-only and shared across runs, so the offset is what keeps a
// previous run's failure from being read as this run's. The file is also live
// — the VM may still be writing — so an unreadable or absent log stays silent
// rather than turn a slow boot into a false error.
func tartStartupFailure(logPath string, fromOffset int64) string {
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if isTartStartupFailure(line) {
			return line
		}
	}
	return ""
}

// isTartStartupFailure reports whether a log line describes the specific
// startup failure a locked login keychain causes: the host key cannot be
// created.
//
// The match is deliberately narrow. VZErrorDomain covers many distinct codes —
// invalid configuration, invalid disk, VM limit, network failures — and only
// one of them is the keychain case, so matching the domain alone would blame a
// locked keychain for any Virtualization.framework error and send the user to
// unlock something that is not the problem. Anything else is left to the
// generic timeout, which reports the true cause without inventing one.
//
// Wording varies across macOS versions, so both the security-error message and
// the host-key POSIX failure are accepted, in either the tart or the bare
// framework form.
func isTartStartupFailure(line string) bool {
	if line == "" {
		return false
	}
	if strings.Contains(line, "Failed to create new HostKey") {
		return true
	}
	if strings.Contains(line, "VZErrorDomain") && strings.Contains(line, "security error") {
		return true
	}
	return false
}

func (t *Tart) clone(ctx context.Context) error {
	res, err := t.Runner.Run(ctx, "tart", "clone", t.Config.TartImage, t.VMName())
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("tart clone failed (exit %d)", res.ExitCode)
	}
	return nil
}

func (t *Tart) startVM(ctx context.Context) error {
	args := []string{
		"run", "--no-graphics",
		"--dir=workspace:" + t.Config.WorkspaceDir,
		"--dir=just-code:" + t.StageDir() + ":ro",
		t.VMName(),
	}
	return t.Starter.Start(nil, t.LogPath(), "tart", args...)
}

// Start brings the OpenCode backend up, mirroring the Tart branch of
// `just start` and `just code`. In isolation full it only brings the VM up:
// the TUI is launched interactively by RunAgent, so no server is started.
// validateConfig checks the deterministic start-time configuration
// (credential, acknowledgement, MTU). Restart calls it before the destructive
// Clean so a configuration error cannot destroy a VM that Start would then
// refuse to recreate.
//
// Tart has no secret proxy: the credential travels into the guest in
// plaintext (stdin at launch, 0600 env file for the TUI), where any process —
// the agent included — can read and exfiltrate it. Starting therefore
// requires the explicit --acknowledge-guest-credentials flag (P09, issue
// #74); the warning stays so the choice is visible on every start.
func (t *Tart) validateConfig(ctx context.Context) error {
	if !t.Config.GuestCredentialsAcknowledged {
		return fmt.Errorf("tart hands the inference credentials to the guest in plaintext, where any process (the agent included) can read them; " +
			"pass --acknowledge-guest-credentials to accept this, or use --microsandbox, which keeps the credential behind the secret proxy")
	}
	if _, err := t.resolveProviderKeysFor(ctx); err != nil {
		return err
	}
	warnUnprotectedRuntime("Tart")
	return ValidateMTU(t.Config.TartMTU)
}

// resolveProviderKeysFor resolves every configured platform's key through the
// same injected reader the Albert path uses.
func (t *Tart) resolveProviderKeysFor(ctx context.Context) (map[string]string, error) {
	read := t.CredentialRead
	if read == nil {
		read = readStoredWith
	}
	return ResolveProviderKeysWith(read, ctx, t.Config, "")
}

func (t *Tart) Start(ctx context.Context) error {
	cfg := t.Config
	if err := t.validateConfig(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		return err
	}
	if err := CheckWorkspaceGate(ctx, cfg.WorkspaceDir); err != nil {
		return err
	}
	if err := os.MkdirAll(t.StateDir, 0o755); err != nil {
		return err
	}

	vm := t.VMName()
	running, err := t.vmRunning(ctx, vm)
	if err != nil {
		return err
	}
	if running {
		// No launch happened in this call: the VM is already up, so its log
		// says nothing about a boot in progress and a failure in it belongs to
		// an earlier run. Startup-log diagnosis is skipped rather than applied
		// to some arbitrary point in the file.
		if err := t.waitForAgent(ctx, noLaunch); err != nil {
			return err
		}
		if cfg.Isolation == IsolationFull {
			fmt.Printf("%s is running (isolation full; the TUI runs inside the VM).\n", vm)
			return nil
		}
		ip, err := t.IP(ctx, vm)
		if err == nil && ip != "" {
			client := &http.Client{Timeout: 5 * time.Second}
			endpoint := "http://" + ip + ":" + strconv.Itoa(DefaultPort)
			if tartHealthProbeFn(ctx, client, endpoint, cfg.Username, cfg.Password).Healthy {
				changed, err := t.overlayChangedSinceBackendLaunch()
				if err != nil {
					return err
				}
				if changed {
					fmt.Printf("%s has changed OpenCode configuration; restarting backend...\n", vm)
					if err := t.StopBackend(ctx); err != nil {
						return err
					}
					return t.launchBackend(ctx)
				}
				fmt.Printf("%s is running with a healthy OpenCode backend.\n", vm)
				return nil
			}
		}
		fmt.Printf("%s is running but OpenCode is not healthy; restarting backend...\n", vm)
		if err := t.StopBackend(ctx); err != nil {
			return err
		}
		return t.launchBackend(ctx)
	}

	exists, err := t.vmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("Cloning %s to %s...\n", cfg.TartImage, vm)
		if err := t.clone(ctx); err != nil {
			return err
		}
	}

	// Stage before booting so the share already holds the binary.
	if err := t.stageGuestBinary(); err != nil {
		return err
	}
	fmt.Printf("Starting %s with Tart...\n", vm)
	// Mark the log before launching: the detached process starts writing
	// immediately, and the failure this diagnoses can be on disk by the time
	// startVM returns. Taking the mark after the launch would skip it.
	logOffset := logSize(t.LogPath())
	if err := t.startVM(ctx); err != nil {
		return err
	}
	if err := t.waitForAgent(ctx, logOffset); err != nil {
		return err
	}
	if cfg.Isolation == IsolationFull {
		fmt.Printf("%s is running (isolation full; the TUI runs inside the VM).\n", vm)
		return nil
	}
	return t.launchBackend(ctx)
}

// Clean stops and deletes the VM and its writable state, mirroring
// `just clean --tart`.
func (t *Tart) Clean(ctx context.Context) error {
	vm := t.VMName()
	running, err := t.vmRunning(ctx, vm)
	if err != nil {
		return err
	}
	if running {
		if _, err := t.Runner.Run(ctx, "tart", "stop", vm, "--timeout", "5"); err != nil {
			return err
		}
	}
	exists, err := t.vmExists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("%s does not exist.\n", vm)
		return nil
	}
	// Disposing is destructive: say so before and after, so a silent success
	// can never be mistaken for "nothing happened".
	fmt.Printf("Deleting %s (VM and local state)...\n", vm)
	res, err := t.Runner.Run(ctx, "tart", "delete", vm)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("tart delete failed (exit %d)", res.ExitCode)
	}
	fmt.Printf("%s deleted.\n", vm)
	return nil
}

// Doctor verifies the Tart installation, mirroring `just doctor --tart`.
func (t *Tart) Doctor(ctx context.Context) error {
	res, err := t.Runner.Run(ctx, "tart", "--version")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("tart is not installed")
	}
	fmt.Print(res.Stdout)
	// A locked login keychain is not fatal to `tart` itself but makes every VM
	// fail to boot, and the failure surfaces much later as an unhelpful guest
	// agent timeout. Name it here, where the user is looking, rather than
	// letting it be discovered after a five-minute wait. This is advisory: a
	// keychain can be unlocked, and doctor stays read-only.
	if locked, detail := loginKeychainLocked(ctx, t.Runner); locked {
		fmt.Printf("Warning: the login keychain appears locked (%s). Virtualization.framework stores each VM's host key there, so a Tart VM cannot boot until it is unlocked — `start --tart` would fail with a guest agent timeout. Unlock it (or open a GUI session) before starting.\n", detail)
	}
	fmt.Println("Tart runtime is ready.")
	return nil
}

// loginKeychainLocked reports a keychain state that will stop a Tart VM from
// booting, using the only read-only signal the `security` CLI offers.
//
// This is deliberately narrow about what it claims. `security
// show-keychain-info` displays keychain *settings* (lock-on-sleep, timeout),
// not lock status, so a zero exit only means "the command ran", not "the
// keychain is unlocked" — Apple's own status API (SecKeychainGetStatus) is not
// reachable from the shell. The probe therefore warns only on the two errors a
// headless session actually produces, and treats success as inconclusive
// rather than as proof. A false "ready" is a known limit of this check, and
// the real diagnosis remains the VM log read in waitForAgent.
//
// Read-only throughout: it must never unlock or modify a keychain, so an
// unusable probe stays silent rather than blocking doctor.
func loginKeychainLocked(ctx context.Context, r Runner) (bool, string) {
	res, err := r.Run(ctx, "security", "show-keychain-info")
	if err != nil || res.ExitCode == 0 {
		return false, ""
	}
	detail := strings.TrimSpace(res.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(res.Stdout)
	}
	lower := strings.ToLower(detail)
	// "locked" is the explicit wording; "user interaction is not allowed" is
	// what a locked keychain returns in a session with no GUI to prompt in,
	// which is exactly the headless case this warning exists for.
	if strings.Contains(lower, "locked") || strings.Contains(lower, "user interaction is not allowed") {
		return true, detail
	}
	return false, ""
}

func (t *Tart) ID() Runtime { return RuntimeTart }

// Restart is non-destructive (P07): it stops and starts the existing VM,
// preserving disk and guest state. The preflights run first so a rejected
// workspace or a bad config aborts before anything is touched. The
// destructive rebuild is Recreate.
func (t *Tart) Restart(ctx context.Context) error {
	if err := t.RestartPreflights(ctx); err != nil {
		return err
	}
	if err := t.Stop(ctx); err != nil {
		return err
	}
	return t.Start(ctx)
}

// Recreate is the explicit destructive rebuild of the VM (P07). It names the
// loss before and after. No lifecycle path reaches it implicitly.
func (t *Tart) Recreate(ctx context.Context) error {
	fmt.Printf("Recreating %s. This DESTROYS: guest sessions, tools installed in the guest, and guest-only files.\n", t.VMName())
	if err := t.RestartPreflights(ctx); err != nil {
		return err
	}
	if err := t.Clean(ctx); err != nil {
		return err
	}
	if err := t.Start(ctx); err != nil {
		return err
	}
	fmt.Printf("%s recreated.\n", t.VMName())
	return nil
}

// RestartPreflights runs the deterministic prechecks shared by the lifecycle
// commands: configuration validation, workspace creation and the workspace
// gate. A rejected workspace or bad config must abort before any destructive
// step, so the VM and its persistent state survive.
func (t *Tart) RestartPreflights(ctx context.Context) error {
	if err := t.validateConfig(ctx); err != nil {
		return err
	}
	if err := os.MkdirAll(t.Config.WorkspaceDir, 0o755); err != nil {
		return err
	}
	return CheckWorkspaceGate(ctx, t.Config.WorkspaceDir)
}

// Logs follows the backend log, mirroring `just logs --tart`.
func (t *Tart) Logs() error {
	return RunInteractive("tail", "-f", t.LogPath())
}

// Shell opens an interactive shell in the VM, mirroring `just shell --tart`.
func (t *Tart) Shell() error {
	return RunInteractive("tart", "exec", "-it", t.VMName(), "/bin/zsh")
}

// RunAgent launches the OpenCode TUI in the foreground inside the VM
// (isolation full), resuming the latest session when one exists. The TUI's
// /new command starts a fresh session. Secrets are pushed first via the staged
// guest binary on stdin (never argv); the TUI then runs under `tart exec -it`,
// sourcing the 0600 env file and execing opencode --continue in the workspace.
func (t *Tart) RunAgent(ctx context.Context) error {
	cfg := t.Config
	vm := t.VMName()
	if err := t.stageGuestBinary(); err != nil {
		return err
	}
	// Copy the staged binary to guest-local disk (the share is not reliably
	// executable), then push secrets and prepare the guest.
	if err := t.guestRun(ctx, "/bin/cp", guestShareBinary, guestLocalBinary); err != nil {
		return err
	}
	if err := t.guestRun(ctx, "/bin/chmod", "755", guestLocalBinary); err != nil {
		return err
	}
	keys, err := t.resolveProviderKeysFor(ctx)
	if err != nil {
		return err
	}
	guestArgs, err := guestSecretsArgs(cfg.Username, t.OpenCodeOverlay, t.OpenCodeOverlay.MCPConnectors)
	if err != nil {
		return err
	}
	secretArgs := append([]string{"tart", "exec", "-i", vm, guestLocalBinary}, guestArgs...)
	stdin, err := SecretsReaderWithKeys(cfg.Password, keys)
	if err != nil {
		return err
	}
	if err := runStdinOK(t.Runner, ctx, stdin, secretArgs[0], secretArgs[1:]...); err != nil {
		return err
	}
	name, email := hostGitIdentity()
	// argv: __guest-prepare <username> <gitName> <gitEmail> — the consumer
	// reads Username at 1, GitName at 2, GitEmail at 3.
	if err := t.guestRun(ctx, guestLocalBinary, GuestPrepareCommand, cfg.Username, name, email); err != nil {
		return err
	}
	// Probe sessions and CLI support through the same login shell/PATH as the
	// TUI: the vendor installer may keep OpenCode in ~/.opencode/bin. Source
	// the same managed environment so project config and plugins resolve alike.
	envPrefix := fmt.Sprintf("set -a; . %s; set +a; ", shellQuote(guestSecretsEnvPath))
	listCommand := fmt.Sprintf("%scd %s && opencode session list --standalone --format json", envPrefix, shellQuote(guestWorkspaceDir))
	sessions, err := t.Runner.Run(ctx, "tart", "exec", vm, "/bin/zsh", "-lc", listCommand)
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
		help, err := t.Runner.Run(ctx, "tart", "exec", vm, "/bin/zsh", "-lc", envPrefix+"opencode --help")
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
		WarnOpenCodeResumeUnavailable("dans le guest Tart")
	}
	interactive := t.Interactive
	if interactive == nil {
		interactive = RunInteractive
	}
	return interactive("tart", "exec", "-it", vm, "/bin/zsh", "-lc", tartAgentLaunch(guestSecretsEnvPath, guestWorkspaceDir, len(resumeArgs) > 0))
}

// tartAgentLaunch builds the in-guest launch line for isolation full: source
// the 0600 secrets env file, cd into the workspace share, exec the TUI. Both
// paths are shell-quoted: the workspace share contains spaces, and the line
// runs under `zsh -lc`.
func tartAgentLaunch(secretsPath, workspaceDir string, continueSession bool) string {
	resume := ""
	if continueSession {
		resume = " --continue"
	}
	return fmt.Sprintf("set -a; . %s; set +a; cd %s; exec opencode --standalone%s", shellQuote(secretsPath), shellQuote(workspaceDir), resume)
}

// Status describes the managed VM's current state, for `check` in isolation
// full where there is no health endpoint to probe.
func (t *Tart) Status(ctx context.Context) (string, error) {
	running, err := t.vmRunning(ctx, t.VMName())
	if err != nil {
		return "", err
	}
	if running {
		return fmt.Sprintf("%s is running", t.VMName()), nil
	}
	exists, err := t.vmExists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return fmt.Sprintf("%s is stopped", t.VMName()), nil
	}
	return fmt.Sprintf("%s does not exist", t.VMName()), nil
}

// IsRunning reports whether the VM bound to this project is running.
// Scope la vérification à la VM du projet (t.VMName()) : compter toutes les
// VM gérées ferait dire « running » pour un projet sans VM dès qu'un autre
// projet en a une d'active.
func (t *Tart) IsRunning(ctx context.Context) (bool, error) {
	vms, err := t.RunningVMs(ctx)
	if err != nil {
		if commandNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, vm := range vms {
		if vm == t.VMName() {
			return true, nil
		}
	}
	return false, nil
}

// Endpoint returns the backend URL for the managed VM.
func (t *Tart) Endpoint(ctx context.Context) (string, error) {
	ip, err := t.IP(ctx, t.VMName())
	if err != nil {
		return "", err
	}
	return "http://" + ip + ":" + strconv.Itoa(DefaultPort), nil
}
