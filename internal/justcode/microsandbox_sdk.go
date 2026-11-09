package justcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/etalab-ia/just-code/assets"
	msb "github.com/superradcompany/microsandbox/sdk/go"
)

// Guest assets are read directly from the binary, without writing host files.
var (
	guestPrepScript        = mustAsset("guest-prep.sh")
	piGuestPrepScript      = mustAsset("pi-guest-prep.sh")
	browserGuestPrepScript = mustAsset("browser-guest-prep.sh")
	opencodeConfigContent  = mustAsset("opencode-config.json")
)

// msbStartScript composes the guest start script: the shared prep (toolchain,
// git identity) followed by a mode-specific tail. In backend mode the tail
// execs `opencode serve`; in full mode the sandbox is only kept alive — the
// TUI is launched interactively by RunAgent, not by the entrypoint.
func msbStartScript(iso Isolation) string {
	return msbStartScriptFor(iso, nil, HarnessOpenCode)
}

func msbStartScriptFor(iso Isolation, mcpIDs []string, harness Harness) string {
	tail := "exec opencode serve --hostname 0.0.0.0 --port " + strconv.Itoa(DefaultPort)
	if iso == IsolationFull {
		tail = "exec sleep infinity"
	}
	// Pi has no server mode (ValidateHarnessIsolation refuses the pair), so its
	// guest is always kept alive and its managed configuration replaces the
	// OpenCode prep entirely. The browser MCP profile is OpenCode-only: a
	// connector just-code cannot apply must not drag in a heavier image.
	if harness == HarnessPi {
		return piGuestPrepScript + tail + "\n"
	}
	prep := guestPrepScript
	if len(browserMCPIDs(mcpIDs)) > 0 {
		prep = browserGuestPrepScript + githubCLIInstallScript() + browserDevToolsStartScript()
	}
	return prep + tail + "\n"
}

func msbImageForMCPs(mcpIDs []string) string {
	if len(browserMCPIDs(mcpIDs)) > 0 {
		return msbBrowserImage
	}
	return msbImage
}

func browserGuestProfileRevision() string {
	parts := []string{
		msbBrowserImage,
		browserGuestPrepScript,
		githubCLIInstallScript(),
		msbBrowserAptSnapshot,
		msbBrowserChromiumPackageVersionAMD64,
		msbBrowserChromiumPackageVersionARM64,
		msbBrowserFontsLiberationVersion,
		msbBrowserNodeVersion,
		msbBrowserNodeNPMVersion,
		msbBrowserNodeSHA256AMD64,
		msbBrowserNodeSHA256ARM64,
		msbBrowserOpenCodeVersion,
	}
	for _, browser := range BrowserMCPs() {
		parts = append(parts, browser.ID, browser.Package+"@"+browser.Version, browser.Command)
	}
	revision := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(revision[:])
}

func browserMCPFingerprint(ids []string) string {
	browserIDs := browserMCPIDs(ids)
	if len(browserIDs) == 0 {
		return ""
	}
	parts := []string{
		browserGuestProfileRevision(),
		strings.Join(browserIDs, ","),
		strings.Join(browserMCPInstallPackages(browserIDs), "\x00"),
		strings.Join(browserMCPBinaries(browserIDs), "\x00"),
	}
	fingerprint := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(fingerprint[:])
}

func browserGuestEnv(ids []string) map[string]string {
	browserIDs := browserMCPIDs(ids)
	if len(browserIDs) == 0 {
		return nil
	}
	return map[string]string{
		"JUST_CODE_APT_SNAPSHOT":                   msbBrowserAptSnapshot,
		"JUST_CODE_CHROMIUM_PACKAGE_VERSION_AMD64": msbBrowserChromiumPackageVersionAMD64,
		"JUST_CODE_CHROMIUM_PACKAGE_VERSION_ARM64": msbBrowserChromiumPackageVersionARM64,
		"JUST_CODE_CHROMIUM_VERSION_AMD64":         msbBrowserChromiumVersionAMD64,
		"JUST_CODE_CHROMIUM_VERSION_ARM64":         msbBrowserChromiumVersionARM64,
		"JUST_CODE_FONTS_LIBERATION_VERSION":       msbBrowserFontsLiberationVersion,
		"JUST_CODE_NODE_VERSION":                   msbBrowserNodeVersion,
		"JUST_CODE_NODE_NPM_VERSION":               msbBrowserNodeNPMVersion,
		"JUST_CODE_NODE_SHA256_AMD64":              msbBrowserNodeSHA256AMD64,
		"JUST_CODE_NODE_SHA256_ARM64":              msbBrowserNodeSHA256ARM64,
		"JUST_CODE_OPENCODE_VERSION":               msbBrowserOpenCodeVersion,
		"JUST_CODE_BROWSER_PROFILE":                browserGuestProfileRevision(),
		msbBrowserMCPIDsEnv:                        strings.Join(browserIDs, ","),
		msbBrowserMCPPackagesEnv:                   strings.Join(browserMCPInstallPackages(browserIDs), " "),
		msbBrowserMCPBinariesEnv:                   strings.Join(browserMCPBinaries(browserIDs), " "),
		msbBrowserMCPFingerprintEnv:                browserMCPFingerprint(browserIDs),
	}
}

func browserDevToolsStartScript() string {
	// The start script is persisted at creation, so inspect the current guest
	// env at every boot rather than baking in the initial connector selection.
	// P03 requires --no-sandbox when Chromium runs as guest root; the microVM
	// remains the isolation boundary and CDP is guest-loopback only.
	return "case \",${" + msbBrowserMCPIDsEnv + ":-},\" in\n" +
		"  *,chrome-devtools,*)\n" +
		"    mkdir -p /var/log/just-code\n" +
		"    if ! curl -fsS http://127.0.0.1:9222/json/version >/dev/null 2>&1; then\n" +
		"      chromium --headless --no-sandbox --disable-gpu --disable-dev-shm-usage --no-first-run --no-default-browser-check --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222 --user-data-dir=/var/lib/just-code/chromium-profile >/var/log/just-code/chromium.log 2>&1 &\n" +
		"    fi\n" +
		"    attempt=0\n" +
		"    until curl -fsS http://127.0.0.1:9222/json/version >/dev/null 2>&1; do\n" +
		"      attempt=$((attempt + 1))\n" +
		"      if [ \"$attempt\" -ge 30 ]; then echo 'guest Chromium DevTools endpoint did not become ready' >&2; exit 1; fi\n" +
		"      sleep 1\n" +
		"    done\n" +
		"    ;;\n" +
		"esac\n"
}

func mustAsset(name string) string {
	data, err := assets.Read(name)
	if err != nil {
		panic(fmt.Sprintf("justcode: embedded asset %s missing: %v", name, err))
	}
	return string(data)
}

func msbPortMappings(isolation Isolation) map[uint16]uint16 {
	ports := make(map[uint16]uint16, 12)
	// Full mode runs the TUI in the guest and has no OpenCode HTTP server.
	// Keep the server port only for backend mode, where the host TUI attaches.
	if isolation == IsolationBackend {
		ports[DefaultPort] = DefaultPort
	}
	for p := uint16(3000); p <= 3010; p++ {
		ports[p] = p
	}
	return ports
}

func msbProjectPortBindings(configured []PortMapping, portsConfigured bool, isolation Isolation) []msb.PortBinding {
	bindings := make([]msb.PortBinding, 0, len(configured)+1)
	if isolation == IsolationBackend {
		bindings = append(bindings, msb.PortBinding{Bind: "127.0.0.1", HostPort: uint16(DefaultPort), GuestPort: uint16(DefaultPort)})
	}
	if !portsConfigured {
		for p := uint16(3000); p <= 3010; p++ {
			bindings = append(bindings, msb.PortBinding{Bind: "127.0.0.1", HostPort: p, GuestPort: p})
		}
	} else {
		for _, port := range configured {
			bindings = append(bindings, msb.PortBinding{Bind: "127.0.0.1", HostPort: port.Host, GuestPort: port.Guest})
		}
	}
	return bindings
}

// msbManagedLabel is the ownership label attached to every sandbox just-code
// creates (P06). The lifecycle sweeps enumerate managed sandboxes by this
// label; the legacy singleton (created before labels) is recognized by name.
const msbManagedLabel = "justcode.managed"

type sdkMSBClient struct{}

var _ msbClient = sdkMSBClient{}

func (sdkMSBClient) EnsureInstalled(ctx context.Context) error {
	return ensureMSBRuntime(ctx, nil)
}

func (sdkMSBClient) Doctor(ctx context.Context) (string, error) {
	if err := validateInstalledMSBRuntime(ctx); err != nil {
		return "", err
	}
	path, err := msbRuntimeBinary()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("Microsandbox runtime is not installed; run 'just-code start --microsandbox' to install it")
		}
		return "", fmt.Errorf("inspect Microsandbox runtime: %w", err)
	}
	output, err := exec.CommandContext(ctx, path, "doctor").CombinedOutput()
	if err == nil {
		return string(output), nil
	}
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return "", fmt.Errorf("microsandbox doctor: %w", err)
	}
	return string(output), fmt.Errorf("microsandbox doctor: %w: %s", err, detail)
}

// Managed installs use the SDK's documented MSB_HOME layout; manual installs
// keep the SDK's MSB_PATH override. The CLI is invoked by absolute path solely
// because the SDK does not expose the host diagnostics API.
func msbRuntimeBinary() (string, error) {
	if path := os.Getenv("MSB_PATH"); path != "" {
		return path, nil
	}
	home, err := msbRuntimeHome()
	if err != nil {
		return "", err
	}
	name := "msb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, "bin", name), nil
}

func (sdkMSBClient) Lookup(ctx context.Context, name string) (msbSandboxInfo, bool, error) {
	h, err := msb.GetSandbox(ctx, name)
	if msb.IsKind(err, msb.ErrSandboxNotFound) {
		return msbSandboxInfo{}, false, nil
	}
	if err != nil {
		return msbSandboxInfo{}, false, err
	}
	return msbSandboxInfo{Name: h.Name(), Status: string(h.Status())}, true, nil
}

// List enumerates managed sandboxes by the ownership label, following the
// SDK's cursor pagination.
func (sdkMSBClient) List(ctx context.Context) ([]msbSandboxInfo, error) {
	var out []msbSandboxInfo
	cursor := ""
	for {
		opts := []msb.SandboxListOption{msb.WithListLabels(map[string]string{msbManagedLabel: "true"})}
		if cursor != "" {
			opts = append(opts, msb.WithListCursor(cursor))
		}
		page, err := msb.ListSandboxesWith(ctx, opts...)
		if err != nil {
			return nil, err
		}
		for _, h := range page.Sandboxes {
			out = append(out, msbSandboxInfo{Name: h.Name(), Status: string(h.Status())})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return out, nil
		}
		cursor = *page.NextCursor
	}
}

// Kept separate from Create so tests can verify the actual SDK options without
// loading native code or creating a VM.
//
// Secret handling (P09): the SDK's create surface only accepts inline values,
// so each binding is registered with the inert bootstrap sentinel and rotated
// to its host env reference immediately after creation (RotateSecretsLive),
// before the start script runs. The raw value never reaches the persisted
// sandbox config.
// msbGuestResources resolves the guest sizing from a spec, falling back to the
// built-in defaults so a zero value never asks the runtime for a zero-CPU or
// zero-memory VM.
func msbGuestResources(spec msbSandboxSpec) (cpus, memoryMB int) {
	cpus, memoryMB = spec.CPUs, spec.MemoryMB
	if cpus <= 0 {
		cpus = DefaultSandboxCPUs
	}
	if memoryMB <= 0 {
		memoryMB = DefaultSandboxMemoryMB
	}
	return cpus, memoryMB
}

func msbCreateOptions(spec msbSandboxSpec) []msb.SandboxOption {
	secrets := make([]msb.SecretEntry, 0, len(spec.Bindings))
	for _, b := range spec.Bindings {
		secrets = append(secrets, msb.Secret.Env(b.GuestEnv, msbSecretBootstrapValue, msb.SecretEnvOptions{
			Allow:       b.AllowHosts,
			Placeholder: b.Placeholder,
		}))
	}
	cpus, memoryMB := msbGuestResources(spec)
	return []msb.SandboxOption{
		msb.WithImage(spec.Image),
		msb.WithCPUs(uint8(cpus)),
		msb.WithMemory(uint32(memoryMB)),
		msb.WithWorkdir(msbGuestWorkspace),
		msb.WithShell("/bin/sh"),
		msb.WithEntrypoint("/bin/sh", "-c"),
		msb.WithCmd("exec " + msbGuestEntrypoint),
		msb.WithDetached(),
		msb.WithRootDisk(msb.RootDisk.Managed(8 * 1024)),
		msb.WithEnv(spec.Env),
		msb.WithMounts(msbWorkspaceMounts(spec)),
		msb.WithNetwork(msb.NetworkPolicy.FromProfiles(msb.NetworkProfilePublic)),
		msb.WithPortBindings(msbProjectPortBindings(spec.Ports, spec.PortsConfigured, spec.Isolation)...),
		msb.WithSecrets(secrets...),
		msb.WithScripts(map[string]string{"start": spec.StartScript}),
		// Ownership label (P06): the lifecycle sweeps list managed sandboxes
		// by this label rather than by name guessing, and the legacy singleton
		// is recognized separately.
		msb.WithLabel(msbManagedLabel, "true"),
	}
}

// msbGuestWorkspace is the guest path of the project working tree.
const msbGuestWorkspace = "/workspace"

// msbWorkspaceMounts returns the /workspace mount for a spec. The sealed
// model (P22) is the only supported one: an owned volume lives inside the
// sandbox, so no host file — a developer .env, an untracked secret, anything
// ignored — is reachable from the guest. Project content arrives only through
// the filtered transfer (transfer.go), never through a bind mount.
//
// A spec without Sealed set is rejected rather than silently bind-mounted:
// the sealed boundary is all-or-nothing per project, and a fallback bind
// mount would be the leak this design removes.
func msbWorkspaceMounts(spec msbSandboxSpec) map[string]msb.MountConfig {
	if !spec.SealedWorkspace {
		// The runtime refuses such a spec before reaching here; this guard
		// keeps the mount table from ever carrying a host bind.
		return map[string]msb.MountConfig{}
	}
	// The owned volume lives on the sandbox's managed root disk (8 GiB, see
	// msbCreateOptions), so guest workspace growth is bounded by it and is
	// accounted for in that sizing rather than through a separate quota.
	return map[string]msb.MountConfig{
		msbGuestWorkspace: msb.Mount.Owned(msb.OwnedVolumeOptions{Kind: msb.VolumeKindDir}),
	}
}

func (sdkMSBClient) Create(ctx context.Context, spec msbSandboxSpec) error {
	sb, err := msb.CreateSandbox(ctx, spec.Name, msbCreateOptions(spec)...)
	if err != nil {
		return err
	}
	return detachMSBSandbox(sb)
}

// Close stops detached VMs too. Detach releases ownership without stopping
// them; use a fresh bounded context so caller cancellation cannot skip cleanup.
type msbDetachable interface {
	Detach(context.Context) error
}

func detachMSBSandbox(sb msbDetachable) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sb.Detach(ctx)
}

func (sdkMSBClient) Start(ctx context.Context, name string) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	sb, err := h.StartDetached(ctx)
	if err != nil {
		return err
	}
	return detachMSBSandbox(sb)
}

// msbNextStartOptions builds the next-boot refresh for an existing sandbox.
// Every binding is re-registered as a host env reference ({"kind":"env",
// "var":...}): the raw value is never persisted, and the reference is
// re-resolved from the just-code process environment at apply/boot time, so a
// rotated credential is picked up on the next boot without any value crossing
// the persisted config.
//
// EnvRemove names every managed guest variable: it scrubs a raw value an
// earlier version may have persisted in the guest environment (the secret
// registration for the same name coexists with it, as it always has).
// SecretsRemove names the managed variables absent from the desired set: the
// SDK modification is a patch, so a registration persisted by an earlier
// apply survives unless it is named explicitly.
func msbNextStartOptions(env map[string]string, bindings []msbSecretBinding, previouslyApplied []string) msb.ModifyOptions {
	opts := msb.ModifyOptions{
		Env:           env,
		EnvRemove:     allBindingGuestEnvs(),
		Secrets:       make(map[string]msb.SecretModifySpec, len(bindings)),
		SecretsRemove: staleBindingGuestEnvs(bindings, previouslyApplied),
		Policy:        msb.ModificationPolicyNextStart,
	}
	for _, b := range bindings {
		opts.Secrets[b.GuestEnv] = msb.SecretModifySpec{Env: b.HostEnv, Placeholder: b.Placeholder, AllowedHosts: b.AllowHosts}
	}
	return opts
}

func (sdkMSBClient) ModifyNextStart(ctx context.Context, name string, env map[string]string, bindings []msbSecretBinding, previouslyApplied []string) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	_, err = h.Modify(ctx, msbNextStartOptions(env, bindings, previouslyApplied))
	return err
}

// msbRotateLiveOptions re-registers each binding as a host env reference with
// the NoRestart policy: the only secret change the runtime applies to a
// running sandbox. The create path uses it to replace the bootstrap sentinel
// before the start script runs.
func msbRotateLiveOptions(bindings []msbSecretBinding) msb.ModifyOptions {
	opts := msb.ModifyOptions{
		Secrets: make(map[string]msb.SecretModifySpec, len(bindings)),
		Policy:  msb.ModificationPolicyNoRestart,
	}
	for _, b := range bindings {
		opts.Secrets[b.GuestEnv] = msb.SecretModifySpec{Env: b.HostEnv, Placeholder: b.Placeholder, AllowedHosts: b.AllowHosts}
	}
	return opts
}

// staleBindingGuestEnvs lists the guest variables whose proxy registration
// must be dropped: the registered built-ins absent from the desired set, plus
// the project-secret variables that were applied on an earlier run and are not
// in the desired set now. The SDK modification is a patch: omitting a name
// never removes it, so a registration persisted by an earlier apply survives
// every later refresh unless it is named explicitly. Without this, an optional
// binding whose approval is lifted (bindings.json deleted, unreadable, or
// revoked) would keep its proxy registration — and therefore its credential —
// through the next refresh and restart, which is exactly the state the
// per-project approval exists to prevent.
//
// previouslyApplied carries the project-secret guest variables recorded as
// applied on the last run. They cannot be derived from the registry because
// they are project data, so the caller supplies the persisted record.
func staleBindingGuestEnvs(bindings []msbSecretBinding, previouslyApplied []string) []string {
	desired := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		desired[b.GuestEnv] = true
	}
	seen := map[string]bool{}
	var stale []string
	add := func(env string) {
		if env == "" || desired[env] || seen[env] {
			return
		}
		seen[env] = true
		stale = append(stale, env)
	}
	for _, env := range AllProviderAPIKeyEnvs() {
		add(env)
	}
	for _, b := range msbSecretBindings() {
		add(b.GuestEnv)
	}
	for _, env := range previouslyApplied {
		add(env)
	}
	sort.Strings(stale)
	return stale
}

// allBindingGuestEnvs lists every managed guest variable name, in registry
// order.
func allBindingGuestEnvs() []string {
	registered := msbSecretBindings()
	out := make([]string, 0, len(registered)+len(AllProviderAPIKeyEnvs()))
	seen := map[string]bool{}
	for _, env := range AllProviderAPIKeyEnvs() {
		if !seen[env] {
			seen[env] = true
			out = append(out, env)
		}
	}
	for _, b := range registered {
		if !seen[b.GuestEnv] {
			seen[b.GuestEnv] = true
			out = append(out, b.GuestEnv)
		}
	}
	return out
}

func (sdkMSBClient) RotateSecretsLive(ctx context.Context, name string, bindings []msbSecretBinding) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	_, err = h.Modify(ctx, msbRotateLiveOptions(bindings))
	return err
}

// msbRemoveSecretsOptions drops the proxy registrations for guestEnvs. Live
// (running sandbox, NoRestart) the registration goes away immediately but the
// guest process keeps its environment — the placeholder variable dangles
// until restart, which the caller must warn about. On a stopped sandbox the
// removal is persisted for the next boot and the placeholder's variable is
// scrubbed too.
func msbRemoveSecretsOptions(guestEnvs []string, live bool) msb.ModifyOptions {
	opts := msb.ModifyOptions{SecretsRemove: guestEnvs}
	if live {
		opts.Policy = msb.ModificationPolicyNoRestart
	} else {
		opts.Policy = msb.ModificationPolicyNextStart
		opts.EnvRemove = guestEnvs
	}
	return opts
}

func (sdkMSBClient) RemoveSecrets(ctx context.Context, name string, guestEnvs []string, live bool) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	_, err = h.Modify(ctx, msbRemoveSecretsOptions(guestEnvs, live))
	return err
}

func connectMSBSandbox(ctx context.Context, name string) (*msb.Sandbox, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	return h.Connect(ctx)
}

func (sdkMSBClient) Exec(ctx context.Context, name, command string) (code int, stderr string, err error) {
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return 0, "", err
	}
	// Connect returns a non-owning handle, so Close only releases it and does
	// not stop the VM.
	defer func() { err = errors.Join(err, sb.Close()) }()
	out, err := sb.Shell(ctx, command)
	if err != nil {
		return 0, "", err
	}
	return out.ExitCode(), out.Stderr(), nil
}

func (sdkMSBClient) ExecCapture(ctx context.Context, name, command string) (stdout, stderr string, code int, err error) {
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { err = errors.Join(err, sb.Close()) }()
	out, err := sb.Shell(ctx, command)
	if err != nil {
		return "", "", 0, err
	}
	return out.Stdout(), out.Stderr(), out.ExitCode(), nil
}

// ExecCaptureStdin runs a shell command with stdin supplied from the host and
// returns its stdout, stderr and exit code.
//
// The data is written to the exec session's stdin pipe, never interpolated into
// the command: the runtime records exec argv in its own log, so a
// secret-derived value has to travel out of band. The sink is closed before
// draining so a guest command that reads to end-of-input sees the end.
func (sdkMSBClient) ExecCaptureStdin(ctx context.Context, name, command string, stdin []byte) (stdout, stderr string, code int, err error) {
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { err = errors.Join(err, sb.Close()) }()
	h, err := sb.ShellStream(ctx, command, msb.WithExecStdinPipe(), msb.WithExecTimeout(execStdinTimeout))
	if err != nil {
		return "", "", 0, err
	}
	defer func() { err = errors.Join(err, h.Close()) }()
	sink := h.TakeStdin()
	if sink == nil {
		return "", "", 0, errors.New("the exec session did not provide a stdin pipe")
	}
	if _, err := sink.Write(stdin); err != nil {
		_ = sink.Close()
		return "", "", 0, fmt.Errorf("writing to the guest command's stdin: %w", err)
	}
	if err := sink.Close(); err != nil {
		return "", "", 0, fmt.Errorf("closing the guest command's stdin: %w", err)
	}
	out, err := h.Collect(ctx)
	if err != nil {
		return "", "", 0, err
	}
	return out.Stdout(), out.Stderr(), out.ExitCode(), nil
}

// execStdinTimeout bounds a stdin-fed exec so a guest command that never reads
// its input cannot hang a start forever.
const execStdinTimeout = 60 * time.Second

func (sdkMSBClient) Stop(ctx context.Context, name string) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	return h.Stop(ctx, msb.WithStopTimeout(3*time.Second))
}

func (sdkMSBClient) Remove(ctx context.Context, name string) error {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	// Destroy handles running and stopped sandboxes and binds removal to the
	// identity just retrieved, refusing to remove a same-name replacement.
	return h.Destroy(ctx, msb.WithDestroyForce())
}

// persistedMounts mirrors how the runtime stores a sandbox's mounts in the
// persisted config document. The SDK's typed SandboxConfig cannot see them:
// its Volumes map decodes as nil for a sandbox created with spec mounts
// (verified against a live handle — same blind spot as the spec env, see
// persistedGuestEnv), so the raw document is the only readable source.
type persistedMounts struct {
	Mounts []persistedMount `json:"mounts"`
}

// persistedMount is one entry of the persisted mounts section.
type persistedMount struct {
	Type  string `json:"type"`
	Host  string `json:"host"`
	Guest string `json:"guest"`
	// Every remaining field that can carry a host source, in any spelling
	// the runtime's two serializers are known to use: the persisted
	// document writes host/name/disk, while the FFI create-and-restore
	// wire shape writes bind/named (internal/ffi MountSpec). A source
	// key that is not parsed is a host path the provenance check cannot
	// see, which is the one failure this model must not have.
	Name   string `json:"name"`
	Disk   string `json:"disk"`
	Bind   string `json:"bind"`
	Named  string `json:"named"`
	Source string `json:"source"`
	Path   string `json:"path"`
	// Owned is the selector the create path emits for owned storage
	// ("dir"/"disk"), as opposed to the persisted document's
	// {"type":"Owned"}.
	Owned string `json:"owned"`
}

// hasHostSource reports whether the entry names a host-backed source, in any
// spelling the runtime is known to write. Named volumes and disks count: they
// are not owned storage, and treating them as sealed would be a silent
// widening of what the guard accepts.
func (m persistedMount) hasHostSource() bool {
	return m.bindSource() != "" || m.Name != "" || m.Disk != "" || m.Named != ""
}

// bindSource returns the host directory this entry binds, whichever spelling
// carries it, so a refusal can name the path instead of describing it
// abstractly.
func (m persistedMount) bindSource() string {
	for _, s := range []string{m.Host, m.Bind, m.Source, m.Path} {
		if s != "" {
			return s
		}
	}
	return ""
}

// parseWorkspaceMount returns the host path bound at the guest path, or ""
// when the document carries no such mount.
//
// The type comparison is case-insensitive for the same reason the owned side
// is: the serializer has emitted different spellings across versions, and a
// missed bind mount here would read as "no host workspace" — the one answer
// that must never be produced by accident.
func parseWorkspaceMount(configJSON, guestPath string) (string, error) {
	var raw persistedMounts
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return "", fmt.Errorf("decode persisted sandbox config: %w", err)
	}
	for _, m := range raw.Mounts {
		if m.Guest == guestPath && (m.Type == "" || strings.EqualFold(m.Type, "Bind")) {
			return m.bindSource(), nil
		}
	}
	return "", nil
}

// parseOwnedWorkspace reports whether the document mounts guestPath as owned
// storage (no host source). A bind mount and an owned volume are mutually
// exclusive at the same guest path, so this answers the provenance question
// the sealed model needs: "does this guest have a host-mounted workspace?".
//
// Owned storage is recognized only by positive evidence, in either spelling
// the runtime uses: {"type":"Owned"} in the persisted document, or
// {"owned":"dir"} on the create path (pinned by the SDK's
// TestOwnedMountWireShape). An entry with no type and no source is ambiguous
// and must be refused; the live persisted config confirms that an owned mount
// carries type:"Owned".
//
// Everything else is refused, which is the direction that matters. A host
// source is refused in every spelling known to either serializer, including
// the FFI wire shape {"bind":"/host/dir"} that carries no "type" at all; a
// host-backed kind (bind, named, disk) is refused even when its source field
// is empty; and an unrecognized non-empty type is refused rather than assumed
// owned, because a future host-backed kind must not be read as sealed.
//
// The asymmetry is deliberate: reading an unrecognized shape as "not owned"
// costs an actionable refusal (recreate the instance), while reading a
// host-backed shape as owned costs the host checkout — every untracked
// secret in it included. Confidentiality wins over convenience here.
func parseOwnedWorkspace(configJSON, guestPath string) (bool, error) {
	var raw persistedMounts
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return false, fmt.Errorf("decode persisted sandbox config: %w", err)
	}
	for _, m := range raw.Mounts {
		if m.Guest != guestPath {
			continue
		}
		if m.hasHostSource() {
			return false, nil
		}
		switch {
		case strings.EqualFold(m.Type, "Owned"):
			return true, nil
		case m.Type == "" && (m.Owned == "dir" || m.Owned == "disk"):
			// The create-path wire form uses an explicit owned selector
			// instead of a type field. These are the SDK's only valid owned
			// kinds; an unknown selector or conflicting non-owned type is
			// refused below.
			return true, nil
		default:
			// A known host-backed kind (bind/named/disk), or a kind this
			// build does not recognize. Both are refused.
			return false, nil
		}
	}
	return false, nil
}

// parseServerPortForwarded checks the persisted network port map rather than
// the requested spec: an existing sandbox keeps the mapping it was created
// with. Either side of a mapping to DefaultPort is enough to expose the
// OpenCode server to the host.
//
// ConfigJSON normalizes the request-side port map into an array of
// {host_port, guest_port} entries; parse this persisted shape, not the SDK
// option shape.
func parseServerPortForwarded(configJSON string) (bool, error) {
	var raw struct {
		Network *struct {
			Ports *[]struct {
				HostPort  uint16 `json:"host_port"`
				GuestPort uint16 `json:"guest_port"`
			} `json:"ports"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return false, fmt.Errorf("decode persisted sandbox config: %w", err)
	}
	if raw.Network == nil || raw.Network.Ports == nil {
		return false, fmt.Errorf("persisted sandbox config has no readable network port list")
	}
	for _, port := range *raw.Network.Ports {
		if port.HostPort == DefaultPort || port.GuestPort == DefaultPort {
			return true, nil
		}
	}
	return false, nil
}

// parseAppliedPortMappings returns the port mappings the sandbox was created
// with, from the same persisted network port list parseServerPortForwarded
// reads. Mappings changed in the project manifest only apply on recreate, so
// this list is the authority for which host forwards exist right now.
func parseAppliedPortMappings(configJSON string) ([]PortMapping, error) {
	var raw struct {
		Network *struct {
			Ports *[]struct {
				HostPort  uint16 `json:"host_port"`
				GuestPort uint16 `json:"guest_port"`
			} `json:"ports"`
		} `json:"network"`
	}
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return nil, fmt.Errorf("decode persisted sandbox config: %w", err)
	}
	if raw.Network == nil || raw.Network.Ports == nil {
		return nil, fmt.Errorf("persisted sandbox config has no readable network port list")
	}
	mappings := make([]PortMapping, 0, len(*raw.Network.Ports))
	for _, port := range *raw.Network.Ports {
		mappings = append(mappings, PortMapping{Host: port.HostPort, Guest: port.GuestPort})
	}
	return mappings, nil
}

// AppliedPortMappings reports the port mappings the running sandbox was
// created with, read from its persisted configuration.
func (sdkMSBClient) AppliedPortMappings(ctx context.Context, name string) ([]PortMapping, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	return parseAppliedPortMappings(h.ConfigJSON())
}

func (sdkMSBClient) WorkspaceMount(ctx context.Context, name string) (string, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return "", err
	}
	return parseWorkspaceMount(h.ConfigJSON(), msbGuestWorkspace)
}

// WorkspaceOwned reports whether the sandbox's /workspace is an owned volume
// (the sealed model) rather than a host bind mount. It reads the persisted
// sandbox configuration, which is the authority for what the guest can
// actually reach — not just-code's own spec.
func (sdkMSBClient) WorkspaceOwned(ctx context.Context, name string) (bool, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return false, err
	}
	return parseOwnedWorkspace(h.ConfigJSON(), msbGuestWorkspace)
}

// ServerPortForwarded reports whether the persisted sandbox config exposes
// OpenCode's backend server port to the host.
func (sdkMSBClient) ServerPortForwarded(ctx context.Context, name string) (bool, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return false, err
	}
	return parseServerPortForwarded(h.ConfigJSON())
}

// WriteFile writes data to a file inside the running sandbox, creating it.
//
// This is the host->guest transfer channel (P22). It is deliberately the only
// way bytes cross into the guest: the caller writes files resolved by the
// transfer filter, one payload at a time, instead of handing the runtime a
// host directory to copy. CopyFromHost exists in the SDK and is NOT used
// here, because it would move a tree without the filter seeing each file.
func (sdkMSBClient) WriteFile(ctx context.Context, name, guestPath string, data []byte) error {
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return err
	}
	defer func() { _ = sb.Close() }()
	return sb.FS().Write(ctx, guestPath, data)
}

func (sdkMSBClient) StartScript(ctx context.Context, name string) (string, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return "", err
	}
	cfg, err := h.Config()
	if err != nil {
		return "", err
	}
	return cfg.Scripts["start"], nil
}

// persistedGuestEnv mirrors how the runtime stores a sandbox's guest
// environment. The runtime flattens its spec into the sandbox config
// (serde flatten) and the spec's env field is a list of {key, value} entries,
// so the stored JSON carries "env" as an array of objects.
//
// The SDK's own SandboxConfig.Env map cannot see it. SandboxConfig.UnmarshalJSON
// decodes into an internal struct with no top-level env field, and fills the
// public Env map only from the init section; reading it therefore always
// yields nil for a sandbox created with a spec env. Parsing the raw config is
// the only way to observe what the guest actually holds.
//
// The representation is stable across releases: the runtime's catalog encoder
// lists config.env among the collections that "have the same representation in
// every supported release" (sdk/rust/lib/db/encoding.rs), so this holds for
// sandboxes written by older builds as well. A document whose env is shaped
// differently does not decode into the slice below and surfaces as an error,
// which the caller treats as a refusal.
type persistedGuestEnv struct {
	Env []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"env"`
}

func parseGuestEnv(configJSON string) (map[string]string, error) {
	var raw persistedGuestEnv
	if err := json.Unmarshal([]byte(configJSON), &raw); err != nil {
		return nil, fmt.Errorf("decode persisted sandbox config: %w", err)
	}
	env := make(map[string]string, len(raw.Env))
	for _, entry := range raw.Env {
		env[entry.Key] = entry.Value
	}
	return env, nil
}

// persistedConfigReader is the part of an SDK sandbox handle the guest-env
// reader needs. Naming it keeps the reader verifiable without a live sandbox:
// the real handle's ConfigJSON carries the persisted document, whereas its
// typed SandboxConfig is blind to the spec env (see persistedGuestEnv).
type persistedConfigReader interface {
	ConfigJSON() string
}

func guestEnvFromHandle(h persistedConfigReader) (map[string]string, error) {
	return parseGuestEnv(h.ConfigJSON())
}

// Env returns the persisted guest environment. Earlier versions stored the
// real ALBERT_API_KEY here in isolation full, so it is read back to detect
// sandboxes that must be recreated rather than booted with a plaintext key.
func (sdkMSBClient) Env(ctx context.Context, name string) (map[string]string, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	return guestEnvFromHandle(h)
}

func (sdkMSBClient) Logs(name string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return err
	}
	stream, err := h.LogStream(ctx, msb.LogStreamOptions{Follow: true})
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		entry, err := stream.Recv(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		if _, err := os.Stdout.Write(entry.Data); err != nil {
			return err
		}
	}
}

func (sdkMSBClient) Shell(name string) (err error) {
	ctx := context.Background()
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sb.Close()) }()
	code, err := sb.Attach(ctx, "/bin/bash")
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("sandbox shell exited %d", code)
	}
	return nil
}

// AttachInteractive runs cmd with args interactively in the sandbox with cwd
// as the working directory, blocking until it exits. It is the isolation-full
// TUI channel; the host terminal is passed through by the SDK attach stream.
func (sdkMSBClient) AttachInteractive(ctx context.Context, name, cmd string, args []string, cwd string) (int, error) {
	sb, err := connectMSBSandbox(ctx, name)
	if err != nil {
		return 0, err
	}
	defer func() { _ = sb.Close() }()
	return sb.AttachWith(ctx, cmd, args, msb.WithAttachCwd(cwd))
}
