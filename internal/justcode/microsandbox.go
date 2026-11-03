package justcode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	msbImage = "ghcr.io/anomalyco/opencode@sha256:b34342987ca889fc2cc19cbc046eefc2418e5980a3d696e209fbb401a288f631"
	// The browser profile uses a multi-arch Debian index pinned to the digest
	// verified against Docker Hub on 2026-10-04.
	msbBrowserImage = "debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251"

	msbBrowserAptSnapshot = "20261002T000000Z"
	// Chromium point releases differ between architectures in the pinned
	// snapshot (bookworm-security carries .92 for amd64 and .57 for arm64),
	// so the package pin is per-architecture like the Node SHA-256 below.
	msbBrowserChromiumPackageVersionAMD64 = "154.0.8037.92-1~deb12u1"
	msbBrowserChromiumPackageVersionARM64 = "154.0.8037.57-1~deb12u1"
	msbBrowserChromiumVersionAMD64        = "154.0.8037.92"
	msbBrowserChromiumVersionARM64        = "154.0.8037.57"
	msbBrowserFontsLiberationVersion      = "1:1.07.4-11"
	msbBrowserNodeVersion                 = "22.14.0"
	msbBrowserNodeNPMVersion              = "10.9.2"
	msbBrowserNodeSHA256AMD64             = "69b09dba5c8dcb05c4e4273a4340db1005abeafe3927efda2bc5b249e80437ec"
	msbBrowserNodeSHA256ARM64             = "08bfbf538bad0e8cbb0269f0173cca28d705874a67a22f60b57d99dc99e30050"
	msbBrowserOpenCodeVersion             = OpenCodeVersion

	// msbSandbox is the legacy singleton instance name, used before project
	// identity (P05). It is kept only to recognize the legacy instance: an
	// existing sandbox with this name is discovered as legacy, never renamed
	// or deleted automatically, and never adopted for a project.
	msbSandbox = "albert-opencode-sandbox"

	// msbGuestEntrypoint is the container entrypoint inside the microVM. It is
	// what launches `opencode serve`.
	msbGuestEntrypoint = "/.msb/scripts/start"

	// msbGuestLog collects the start script's output inside the guest; it is
	// the first place to look when the backend never becomes healthy.
	msbGuestLog = "/var/log/opencode.log"

	// msbToolchainMarker is written by guest-prep.sh once the toolchain
	// install completes. It doubles as the readiness signal for full mode,
	// which has no health endpoint.
	msbToolchainMarker  = "/var/lib/just-code/toolchain-ready"
	msbBrowserMCPMarker = "/var/lib/just-code/browser-mcps-ready"

	msbBrowserMCPIDsEnv         = "JUST_CODE_BROWSER_MCP_IDS"
	msbBrowserMCPPackagesEnv    = "JUST_CODE_BROWSER_MCP_PACKAGES"
	msbBrowserMCPBinariesEnv    = "JUST_CODE_BROWSER_MCP_BINARIES"
	msbBrowserMCPFingerprintEnv = "JUST_CODE_BROWSER_MCP_FINGERPRINT"

	// msbGuestPrepareProbe reports the guest's preparation state through its
	// exit code (Exec surfaces stderr only): ready, preparing, or idle with
	// nothing running. The process scan reads /proc rather than using pgrep,
	// which guest-prep only installs partway through — during a first install
	// pgrep is absent exactly when "is something preparing?" matters. The
	// probe skips its own PID: its command line necessarily contains the
	// entrypoint path it greps for.
	msbGuestPrepareProbe = "fingerprint=\"${" + msbBrowserMCPFingerprintEnv + ":-}\"; " +
		"if [ -f " + msbToolchainMarker + " ] && [ \"$(opencode --version 2>/dev/null)\" = \"opencode v" + OpenCodeVersion + "\" ]; then " +
		"if [ -z \"$fingerprint\" ] || [ \"$(cat " + msbBrowserMCPMarker + " 2>/dev/null)\" = \"$fingerprint\" ]; then exit 0; fi; fi; " +
		"self=$$; " +
		"for p in /proc/[0-9]*/cmdline; do " +
		"[ \"$p\" = \"/proc/$self/cmdline\" ] && continue; " +
		"tr '\\0' ' ' < \"$p\" 2>/dev/null | grep -q '" + msbGuestEntrypoint + "' && exit 2; " +
		"done; exit 3"

	msbPrepareReady    = 0
	msbPrepareProgress = 2
	msbPrepareIdle     = 3

	// msbToolchainRelaunchLimit bounds how often the wait relaunches a guest
	// where nothing is preparing, so a start script that dies repeatedly
	// cannot spin.
	msbToolchainRelaunchLimit = 3
	// msbRelaunchCommand restarts that entrypoint from inside a live VM.
	// Microsandbox keeps a VM across restarts but runs the entrypoint only at
	// creation, so a VM that came back from a restart reports `running` with no
	// backend process listening: "VM up" is not "backend ready".
	//
	// The script is invoked through `sh` because the Go SDK stores script
	// bodies verbatim (no shebang is prepended, unlike the Rust builder and
	// the CLI), and sandboxes created before the shebang was added to
	// guest-prep.sh persist a script that cannot be exec'd directly.
	// The launcher is backgrounded, then checked after one second: a bare
	// `nohup ... &` would report the exit status of the trailing sleep (0)
	// even when the script died instantly, so the compound ends with
	// `kill -0` on the launcher PID to make an immediate death visible.
	msbRelaunchCommand = "nohup sh " + msbGuestEntrypoint + " >" + msbGuestLog + " 2>&1 & pid=$!; sleep 1; kill -0 \"$pid\" 2>/dev/null"

	// msbLaunchAttempts bounds the retry while the guest agent catches up with
	// a freshly started VM.
	msbLaunchAttempts   = 10
	msbLaunchRetryDelay = 2 * time.Second
	msbDoctorTimeout    = 5 * time.Minute

	// msbAllowHosts restricts where the ALBERT_API_KEY secret may be
	// substituted: only the Albert API host ever sees the real value.
	msbAllowHost = "albert.api.etalab.gouv.fr"

	// msbAPISecretEnv is the guest environment variable OpenCode reads for the
	// Albert provider. Microsandbox exposes the secret *placeholder* under this
	// name; the real key stays on the host and is swapped in at the network
	// boundary for msbAllowHost only.
	//
	// Earlier versions instead persisted the real key under this same name in
	// isolation full, so the name doubles as the marker for a sandbox that must
	// be recreated rather than booted (see rejectLegacyRawKey). Naming it is
	// safe now: the guest only ever holds the placeholder.
	msbAPISecretEnv = "ALBERT_API_KEY"

	// msbAPISecretPlaceholder is the exact value the runtime substitutes for
	// the secret in the guest. It mirrors the runtime's own
	// default_placeholder(env_var) = "$MSB_" + env_var. Only this exact value
	// is treated as protected: matching the "$MSB_" prefix instead would let a
	// real credential that happens to start that way pass as a placeholder.
	msbAPISecretPlaceholder = "$MSB_" + msbAPISecretEnv
)

// MicrosandboxRuntime runs the OpenCode backend in a named Microsandbox
// microVM through the embedded Microsandbox Go SDK. No `msb` CLI install is
// required: the runtime downloads to ~/.microsandbox/ on first use. The real
// ALBERT_API_KEY stays on the host; only the secret-proxy substitution enters
// the microVM, and only for the Albert API host.
type MicrosandboxRuntime struct {
	cfg Config
	// instance is the sandbox name this backend operates on. It is the
	// project-derived instance name (P05); when Instance is empty the
	// backend stays on the legacy singleton name for compatibility.
	instance string
	// Client is the Microsandbox seam. Tests inject a fake; production uses the
	// SDK adapter (see microsandbox_sdk.go).
	Client msbClient
	// Probe overrides the health probe. It exists so tests can decide the
	// outcome of the "VM running, is the backend alive?" check without binding
	// a real port.
	Probe func(ctx context.Context, endpoint, username, password string) HealthProbe
	// launchRetryDelay is configurable for tests; production uses two seconds.
	launchRetryDelay time.Duration
	// toolchainPollDelay paces the full-mode readiness wait; configurable for tests.
	toolchainPollDelay time.Duration
	// StateDir is the host state dir the binding approvals are read from;
	// empty means the default (~/.local/state/just-code).
	StateDir string
	// CredentialStore names the store the credential must resolve from:
	// "" selects the normal order (native, then file fallback), "native" or
	// "file" pin one store (revocation uses this).
	CredentialStore string
	// OpenCodeOverlay is the managed OpenCode configuration layer (P10):
	// the model selection resolved from the project/user config. The
	// composed OPENCODE_CONFIG_CONTENT replaces the raw embedded asset at
	// every launch, so a model change is effective at the next restart
	// without recreating the sandbox.
	OpenCodeOverlay ManagedOverlay
	// credentialRead reads a stored credential; production uses
	// readStoredWith. It is a seam so tests can drive the store paths
	// without a real keychain.
	credentialRead credentialReader
	// ProjectSecrets is the project-scoped secret store (Phase 2). Nil means
	// the runtime derives it from the workspace root and the host credential
	// store; a caller that already resolved one (the CLI, or tests) injects it
	// so the index's recorded store matches the store actually used.
	ProjectSecrets *ProjectSecretStore
	// Launch is the first-launch report (issue #172). When set on a terminal,
	// the runtime records what each phase did into it and stops printing its
	// own verbose English lines, because the CLI renders one French checklist
	// from that state. A nil Launch keeps the legacy verbose behaviour.
	Launch *LaunchUX
}

// verbose reports whether this runtime should keep printing its own per-phase
// lines. It is true unless a launch report is taking over on a terminal, so
// scripts, CI and logs lose nothing.
func (m *MicrosandboxRuntime) verbose() bool {
	return m.Launch == nil || !m.Launch.Terminal
}

func (m *MicrosandboxRuntime) say(format string, args ...any) {
	if m.verbose() {
		fmt.Printf(format+"\n", args...)
	}
}

func (m *MicrosandboxRuntime) warn(summary, format string, args ...any) {
	if m.Launch != nil && m.Launch.Terminal {
		m.Launch.AddWarning("%s", summary)
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// An existing VM can still need installation after an interrupted first start.
// Check before relaunching: a fast install could finish before the health wait.
func (m *MicrosandboxRuntime) announceExistingStart(ctx context.Context) {
	if m.Launch == nil {
		return
	}
	code, _, err := m.Client.Exec(ctx, m.InstanceName(), msbGuestPrepareProbe)
	switch {
	case err == nil && code == msbPrepareReady:
		m.Launch.AnnounceWarmStart()
	case err == nil && (code == msbPrepareProgress || code == msbPrepareIdle):
		m.Launch.AnnounceFirstStart()
	default:
		// A failed probe must not turn an unknown state into a warm sample.
		m.Launch.timingUnavailable = true
		m.Launch.AddWarning("durée non enregistrée : état de préparation des outils non vérifié")
	}
}

// NewMicrosandboxRuntime builds a Microsandbox backend with production
// defaults. Without a project context it operates on the legacy singleton
// instance name, so existing behavior is unchanged until callers pass an
// instance (P06 wires the CLI).
func NewMicrosandboxRuntime(cfg Config) *MicrosandboxRuntime {
	return &MicrosandboxRuntime{
		cfg:                cfg,
		instance:           msbSandbox,
		Client:             sdkMSBClient{},
		launchRetryDelay:   msbLaunchRetryDelay,
		toolchainPollDelay: msbLaunchRetryDelay,
	}
}

// NewMicrosandboxRuntimeForInstance builds a backend bound to a
// project-derived instance name (P05).
func NewMicrosandboxRuntimeForInstance(cfg Config, instance string) *MicrosandboxRuntime {
	m := NewMicrosandboxRuntime(cfg)
	if instance != "" {
		m.instance = instance
	}
	return m
}

// InstanceName returns the sandbox name this backend operates on.
func (m *MicrosandboxRuntime) InstanceName() string {
	if m.instance != "" {
		return m.instance
	}
	return msbSandbox
}

// AppliedPortMappings reports the port mappings the running sandbox instance
// was created with. Mappings changed in the project manifest only apply on
// recreate, so this is the authority for which host forwards exist right now.
func (m *MicrosandboxRuntime) AppliedPortMappings(ctx context.Context) ([]PortMapping, error) {
	return m.Client.AppliedPortMappings(ctx, m.InstanceName())
}

func (m *MicrosandboxRuntime) ID() Runtime { return RuntimeMicrosandbox }

// msbClient is the Microsandbox surface just-code uses, narrowed from the SDK
// so tests can fake it without a real microVM runtime.
type msbClient interface {
	// EnsureInstalled downloads the microsandbox runtime if missing.
	EnsureInstalled(ctx context.Context) error
	// Doctor runs the diagnostic command from the SDK-managed runtime.
	Doctor(ctx context.Context) (string, error)
	// Lookup returns the managed sandbox's status, or false if it does not exist.
	Lookup(ctx context.Context, name string) (msbSandboxInfo, bool, error)
	// Create creates and boots the sandbox with the given spec.
	Create(ctx context.Context, spec msbSandboxSpec) error
	// Start boots a stopped sandbox.
	Start(ctx context.Context, name string) error
	// ModifyNextStart persists env and secret-reference changes for the next
	// boot. Bindings carry metadata only (guest variable, host transport
	// variable, allowed hosts) — never a value. previouslyApplied lists the
	// project-secret guest variables recorded as applied on the last run, so
	// a lifted approval is actually unregistered instead of surviving as a
	// stale patch entry.
	ModifyNextStart(ctx context.Context, name string, env map[string]string, bindings []msbSecretBinding, previouslyApplied []string) error
	// RotateSecretsLive re-registers each binding as a host env reference on
	// a running sandbox (NoRestart). It is the create path's second step:
	// creation registers an inert sentinel, and this rotates it to the
	// reference before the start script runs.
	RotateSecretsLive(ctx context.Context, name string, bindings []msbSecretBinding) error
	// RemoveSecrets drops the proxy registrations for guestEnvs: live when
	// the sandbox is running (the placeholder dangles in the guest
	// environment until restart), persisted for the next boot when stopped.
	RemoveSecrets(ctx context.Context, name string, guestEnvs []string, live bool) error
	// AppliedPortMappings reports the port mappings the sandbox was created
	// with, from its persisted configuration. Mappings changed in the
	// project manifest only apply on recreate, so this list is the
	// authority for which host forwards exist right now.
	AppliedPortMappings(ctx context.Context, name string) ([]PortMapping, error)
	// Exec runs a shell command in the sandbox, returning its exit code and stderr.
	Exec(ctx context.Context, name, command string) (int, string, error)
	// ExecCapture is Exec with stdout captured too. Change delivery (P22)
	// needs the guest's diff text, which Exec discards.
	ExecCapture(ctx context.Context, name, command string) (stdout, stderr string, code int, err error)
	// ExecCaptureStdin is ExecCapture with data supplied on the command's
	// standard input. It exists because the runtime persists exec argv in its
	// own log, so a secret-derived argument — the digest of a managed file —
	// would be recorded there. Standard input is not part of argv.
	ExecCaptureStdin(ctx context.Context, name, command string, stdin []byte) (stdout, stderr string, code int, err error)
	// Stop gracefully stops a running sandbox.
	Stop(ctx context.Context, name string) error
	// Remove deletes the sandbox and its local state.
	Remove(ctx context.Context, name string) error
	// WorkspaceMount returns the host path mounted at the guest /workspace.
	// An owned (sealed) workspace has no host path, so this returns "".
	WorkspaceMount(ctx context.Context, name string) (string, error)
	// WorkspaceOwned reports whether the sandbox's /workspace is owned
	// storage inside the sandbox rather than a host bind mount (P22). It is
	// the provenance check that replaces mount-staleness detection.
	WorkspaceOwned(ctx context.Context, name string) (bool, error)
	// ServerPortForwarded reports whether the persisted configuration forwards
	// OpenCode's HTTP server port from the guest to the host.
	ServerPortForwarded(ctx context.Context, name string) (bool, error)
	// WriteFile writes one payload to a path inside the running sandbox. It
	// is the host->guest transfer channel; the caller passes files already
	// resolved by the transfer filter.
	WriteFile(ctx context.Context, name, guestPath string, data []byte) error
	// StartScript returns the persisted start script of an existing sandbox,
	// or "" when it cannot be read. The script is fixed at creation, so it is
	// how the sandbox's original isolation mode is detected.
	StartScript(ctx context.Context, name string) (string, error)
	// Env returns the persisted guest environment of an existing sandbox. An
	// error means the environment could not be inspected, which the caller
	// treats as a refusal rather than as an empty environment. The values are
	// used only to detect a plaintext credential persisted by an earlier
	// version; they are never copied into a new sandbox.
	Env(ctx context.Context, name string) (map[string]string, error)
	// Logs streams sandbox logs to the terminal until interrupted.
	Logs(name string) error
	// Shell opens an interactive shell in the sandbox.
	Shell(name string) error
	// AttachInteractive runs a command with args interactively in the sandbox
	// with a working directory, blocking until it exits. It is the TUI channel
	// used by isolation full; Shell() is the /bin/bash special case of it.
	AttachInteractive(ctx context.Context, name, cmd string, args []string, cwd string) (int, error)
	// List returns every managed sandbox known to the runtime, in
	// deterministic order. Managed means carrying the ownership label; the
	// legacy singleton is reported separately by the runtime (it predates
	// labels).
	List(ctx context.Context) ([]msbSandboxInfo, error)
}

// msbSandboxInfo is the status snapshot of the managed sandbox.
type msbSandboxInfo struct {
	Name   string
	Status string
}

// msbSandboxSpec is the full sandbox configuration passed to the SDK. It
// carries binding *metadata* only: no secret value may appear here, so a spec
// can never leak a credential into a persisted config or an SDK error dump.
type msbSandboxSpec struct {
	Name      string // instance name (P05); empty means the legacy singleton
	Image     string
	Isolation Isolation
	Env       map[string]string
	// CPUs and MemoryMB size the guest. Zero means "unset": the create
	// options then fall back to the built-in defaults, so a spec built by a
	// path that does not resolve resources still boots.
	CPUs            int
	MemoryMB        int
	Ports           []PortMapping
	PortsConfigured bool
	// SealedWorkspace selects the P22 sealed model: /workspace is an owned
	// volume inside the sandbox and the host checkout is NOT mounted. The
	// zero value is refused rather than falling back to a bind mount.
	SealedWorkspace bool
	Bindings        []msbSecretBinding // secret-proxy bindings (metadata only)
	StartScript     string             // guest start script body
}

// validateConfig checks the deterministic start-time configuration (the
// full-mode start timeout). Credential resolution is NOT checked here: it
// involves the credential store and runs in resolveBindings at operation
// time. Restart calls validateConfig before the destructive Clean so a
// configuration error cannot destroy a sandbox that Start would then refuse
// to recreate.
func (m *MicrosandboxRuntime) validateConfig() error {
	// A typo in JUST_CODE_START_TIMEOUT must be reported rather than silently
	// replaced by the default. Full mode waits on the guest without ever
	// reaching the validation the backend path performs before its health
	// wait, so validate before doing any runtime work.
	if m.cfg.Isolation == IsolationFull && m.cfg.StartTimeoutErr != nil {
		return m.cfg.StartTimeoutErr
	}
	// A typo in JUST_CODE_CPUS or JUST_CODE_MEMORY_MB is the same contract:
	// every sibling deferred error is surfaced where the value is consumed,
	// and a recorded-but-unreported error would mean an invalid sizing
	// behaves exactly like an unset one — the "configured but ignored"
	// defect this sizing work exists to remove.
	if m.cfg.SandboxResourcesErr != nil {
		return m.cfg.SandboxResourcesErr
	}
	return nil
}

// stateDirOrDefault returns the host state directory the binding approvals
// live under.
func (m *MicrosandboxRuntime) stateDirOrDefault() string {
	if m.StateDir != "" {
		return m.StateDir
	}
	return DefaultStateDir()
}

// appliedProjectBindings reads the guest variables the host-local inventory
// says this project's sandbox holds. They are the memory the runtime's
// patch-style secret modification needs to unregister a binding whose approval
// was lifted: the record is written by the apply path itself, so it is current
// on the direct Start that `restart` uses as well as on the reconcile path.
//
// An unreadable record is reported as empty, which only means a stale
// registration may survive one more refresh — never that a live one is dropped.
func (m *MicrosandboxRuntime) appliedProjectBindings() []string {
	store, err := m.projectSecretInventoryStore()
	if err != nil {
		return nil
	}
	applied, err := ReadProjectSecretApplied(DefaultFS, m.stateDirOrDefault(), store.Namespace())
	if err != nil {
		return nil
	}
	return append([]string(nil), applied.Envs...)
}

// resolveBindings computes the binding set in effect for this instance right
// now: the required Albert binding (credentialRef > legacy env > stored
// albert) plus every optional binding that is both approved for this instance
// (host-local bindings.json) and stored. An approved-but-unstored optional
// binding is skipped with a warning rather than blocking the start. Values
// are resolved here, immediately before the runtime operation, and travel
// only through withHostSecrets.
func (m *MicrosandboxRuntime) resolveBindings(ctx context.Context) ([]resolvedBinding, error) {
	read := m.credentialRead
	if read == nil {
		read = readStoredWith
	}
	registered := msbSecretBindingsFor(m.cfg.Providers)
	if err := validateSecretBindings(registered); err != nil {
		return nil, err
	}
	var out []resolvedBinding
	for _, b := range registered {
		if b.Provider {
			v, src, store, entry, err := resolveProviderWith(read, ctx, m.cfg, b.Kind, b.GuestEnv, m.CredentialStore)
			if err != nil {
				return nil, err
			}
			out = append(out, resolvedBinding{msbSecretBinding: b, source: src, value: v, store: store, entry: entry})
			continue
		}
		approvals, err := ReadBindingApprovals(DefaultFS, bindingApprovalsPath(m.stateDirOrDefault(), m.InstanceName()))
		if err != nil {
			m.warn("approbations illisibles : accès facultatifs ignorés ; détail : just-code bindings list", "Warning: cannot read the binding approvals for %s (%v); optional bindings are skipped", m.InstanceName(), err)
			continue
		}
		if !approvals.Approves(b.Kind) {
			continue
		}
		v, store, err := read(ctx, b.Kind, m.CredentialStore)
		if err != nil {
			if isNotFound(err) {
				m.warn("identifiant facultatif absent ; détail : just-code bindings list", "Warning: the %s binding is approved for %s but no %q credential is stored; skipping it (just-code auth add %s)",
					b.Kind, m.InstanceName(), b.Kind, b.Kind)
				continue
			}
			// A locked, denied, unavailable or corrupt store on an APPROVED
			// binding is a hard error: dropping the binding here would make
			// the omission the desired set, and the refresh would strip the
			// persisted registration (the patch semantics make absence
			// authoritative). A transient store failure must not become the
			// loss of a live binding. A store *fault* is the one failure the
			// no-op start path still tolerates, because the store may come
			// back and an instance that only needs a restart must not become
			// unusable meanwhile (#204). Storage faults are recognised by
			// their typed store error, not by an allow-list of the cases we
			// happened to think of, so anything unrecognised fails closed.
			return nil, fmt.Errorf("cannot read the approved %s credential for %s: %w", b.Kind, m.InstanceName(), err)
		}
		out = append(out, resolvedBinding{msbSecretBinding: b, source: bindingSourceStore, value: v, store: store, entry: string(b.Kind)})
	}
	// Phase 2: approved project-secret bindings, after the built-ins. They are
	// resolved last so a system binding always wins a name collision — which
	// validation already refuses — and so the built-in behavior is unchanged
	// for every project that has no project-secret bindings.
	project, err := m.resolveProjectSecretBindings(ctx)
	if err != nil {
		return nil, err
	}
	out = append(out, project...)
	return out, nil
}

// resolveProjectSecretBindings turns the host-local approvals of this project
// into resolved bindings. A project with no binding record resolves to nothing
// and never touches a credential store, so the built-in path is unaffected.
//
// Fail-closed rules, all of which block the start rather than degrade it:
//   - an approved binding whose imported entry is missing, whose profile is
//     not the active one, or which exists with no active profile selected is
//     an error (ProjectSecretStore.Deliveries);
//   - an unreadable or locked credential store is an error, never a silent
//     omission — an omitted binding becomes the desired set, and the runtime
//     patch semantics would then remove a live registration.
func (m *MicrosandboxRuntime) resolveProjectSecretBindings(ctx context.Context) ([]resolvedBinding, error) {
	record, err := m.projectSecretDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	if len(record.deliveries) == 0 {
		for _, undelivered := range record.undelivered {
			m.warn("secrets importés non transmis faute d'approbation ; détail : just-code secrets bindings list", "Note: the imported project secret %s is not approved for guest delivery; run 'just-code secrets bindings approve' to expose it.", undelivered)
		}
		return nil, nil
	}
	store, err := m.projectSecretStore()
	if err != nil {
		return nil, err
	}
	var out []resolvedBinding
	seen := map[string]bool{}
	for _, d := range record.deliveries {
		// A guest-readable binding is not a proxy binding: the value is
		// written into the guest for local consumption, so registering a
		// placeholder with the proxy would be pointless and would widen the
		// value's exposure beyond what the user approved.
		if d.GuestReadable() {
			continue
		}
		if seen[d.GuestEnv] {
			return nil, fmt.Errorf("two project secret bindings share the guest variable %s", d.GuestEnv)
		}
		seen[d.GuestEnv] = true
		value, err := store.Get(ctx, d.Profile, d.Component, d.Name)
		if err != nil {
			// An APPROVED project secret whose backing entry is gone is not
			// the tolerable "not stored yet" case, even though the store
			// reports both as not-found: the user already imported and
			// approved it, so only re-importing or removing the approval can
			// restore a consistent project state. Treating it as transient
			// would let a healthy no-op start exit 0 while the guest kept
			// serving the previous value (#204). The sentinel is therefore
			// unwrapped away: it is the store *fault* branch — a typed
			// *StoreError — that stays tolerable, and that still matches.
			if isNotFound(err) {
				return nil, fmt.Errorf("cannot read the approved project secret %s (%s): %w", d.Name, d.Component, ErrProjectSecretImportMissing)
			}
			return nil, fmt.Errorf("cannot read the approved project secret %s (%s): %w", d.Name, d.Component, err)
		}
		ref := d.Reference
		out = append(out, resolvedBinding{
			msbSecretBinding: msbSecretBinding{
				Kind:        CredentialKind(d.GuestEnv),
				GuestEnv:    d.GuestEnv,
				Placeholder: d.Marker,
				HostEnv:     "JUST_CODE_HOST_PROJECT_" + d.GuestEnv,
				AllowHosts:  append([]string(nil), d.Destinations...),
				// Required once approved: the approval is the opt-in, and an
				// approved-but-unreadable value must be an error, not a skip.
				Optional: false,
				Project: &ProjectBindingRef{
					Profile: d.Profile, Component: d.Component, Name: d.Name, Reference: ref,
				},
			},
			source: bindingSourceStore,
			value:  value,
			store:  store.Kind(),
			entry:  ref,
		})
	}
	return out, nil
}

func (m *MicrosandboxRuntime) Start(ctx context.Context) error {
	if err := m.validateConfig(); err != nil {
		return err
	}
	bindings, err := m.resolveBindings(ctx)
	if err != nil {
		return err
	}
	if err := m.validateStoredGitHubOrigin(bindings); err != nil {
		return err
	}
	// The host transport variables exist only for the duration of the start:
	// the SDK's env references resolve from this process's environment at
	// apply/boot time, and are restored afterwards.
	return withHostSecrets(bindings, func() error {
		return m.start(ctx, bindings)
	})
}

// start brings the instance up, then guarantees its sealed workspace exists.
// The two steps are separate because provisioning needs a running guest: the
// transfer is written into the sandbox, so it can only happen after boot.
func (m *MicrosandboxRuntime) start(ctx context.Context, bindings []resolvedBinding) error {
	skillsReconciledBeforeLaunch := false
	preflightSkills := func() error {
		if err := m.installProjectSkills(ctx); err != nil {
			return err
		}
		skillsReconciledBeforeLaunch = true
		return nil
	}
	if err := m.startInstanceWithBackendPreflight(ctx, bindings, preflightSkills); err != nil {
		return err
	}
	if err := m.validateExistingGitHubPublicationPolicy(ctx); err != nil {
		return err
	}
	// Idempotent: a guest that already carries its repository is left
	// untouched. Refreshing an existing workspace is explicit
	// (SyncGuestWorkspace / 'just-code workspace sync'), never a side effect
	// of a plain start.
	if err := m.ProvisionGuestWorkspace(ctx, SyncOptions{}); err != nil {
		return err
	}
	if !skillsReconciledBeforeLaunch {
		if err := m.installProjectSkills(ctx); err != nil {
			return err
		}
	}
	// Phase 3: publish the approved project secrets as managed guest dotenv
	// files, after the workspace exists so the files are not overwritten by
	// the transfer, and before the GitHub preparation so a failure stops the
	// start rather than leaving a half-delivered project.
	if err := m.syncProjectSecretDelivery(ctx, bindings); err != nil {
		return err
	}
	return m.configureGitHubWorkspace(ctx, bindings)
}

// syncProjectSecretDelivery applies the approved project secrets to the guest
// — guest variables through the secret proxy, and managed dotenv files — and
// records what was applied in the host-local inventory.
//
// It runs on every start, including the direct Start that `restart` uses, so
// the inventory is never stale on the path a revocation tells the user to
// take. Recording both halves together is what makes a later removal possible:
// the runtime's secret modification is a patch, so a binding that is no longer
// approved is only unregistered when its guest variable is named explicitly.
func (m *MicrosandboxRuntime) syncProjectSecretDelivery(ctx context.Context, bindings []resolvedBinding) error {
	store, err := m.projectSecretInventoryStore()
	if err != nil {
		// An empty workspace root means this runtime has no project scope at
		// all (the legacy singleton, tests): the built-in path is unaffected.
		// A configured root that cannot be resolved is a different matter —
		// returning nothing there would make the empty set authoritative and
		// unregister live bindings — so it is an error.
		if m.cfg.WorkspaceDir == "" && len(m.projectSecretDeliveriesOf(bindings)) == 0 {
			return nil
		}
		return err
	}
	applied, err := ReadProjectSecretApplied(DefaultFS, m.stateDirOrDefault(), store.Namespace())
	if err != nil {
		return err
	}
	record, err := m.projectSecretDeliveries(ctx)
	if err != nil {
		return err
	}
	deliveries, err := m.projectSecretFileDeliveries(ctx, store, record.deliveries)
	if err != nil {
		return err
	}
	desiredEnvs := projectBindingGuestEnvs(bindings)
	if len(deliveries) == 0 && len(applied.Envs) == 0 && len(applied.Files) == 0 {
		return nil
	}
	// The file set is published first: if it fails, the inventory still
	// describes what the guest holds, so the next run retries the removal or
	// the write instead of forgetting it.
	managed, err := m.syncGuestSecretFiles(ctx, deliveries, applied.Files)
	if err != nil {
		return err
	}
	if err := WriteProjectSecretApplied(DefaultFS, m.stateDirOrDefault(), store.Namespace(), ProjectSecretApplied{Envs: desiredEnvs, Files: managed}); err != nil {
		// The record is what makes a later revocation able to find the file.
		// Without it the guest holds a plaintext value that no later run
		// knows about, so remove what was just written and report: the next
		// run re-delivers from the approvals, which are the durable record.
		_, _ = m.syncGuestSecretFiles(ctx, nil, managed)
		return fmt.Errorf("recording the applied project secret inventory: %w", err)
	}
	if len(managed) > 0 {
		m.say("Published %d managed project secret file(s) into the guest workspace.", len(managed))
	}
	return nil
}

// projectSecretFileDeliveries renders the approved bindings into the set the
// guest receives as files. A guest-readable value is resolved here, at the
// last moment before the SDK call that writes it; a proxy-protected one keeps
// its marker, because the placeholder is all the guest may hold.
func (m *MicrosandboxRuntime) projectSecretFileDeliveries(ctx context.Context, store *ProjectSecretStore, approved []ProjectSecretDelivery) ([]ProjectSecretDelivery, error) {
	out := make([]ProjectSecretDelivery, 0, len(approved))
	guestReadable := 0
	for _, d := range approved {
		if d.GuestReadable() {
			value, err := store.Get(ctx, d.Profile, d.Component, d.Name)
			if err != nil {
				return nil, fmt.Errorf("cannot read the guest-readable project secret %s (%s): %w", d.Name, d.Component, err)
			}
			d.Value = &value
			guestReadable++
		}
		out = append(out, d)
	}
	if guestReadable > 0 {
		// The warning is the point of the separate approval: it is repeated at
		// every start because the exposure lasts as long as the sandbox does.
		m.warn(fmt.Sprintf("%d secret(s) lisible(s) par l'agent et conservé(s) dans les snapshots ; détail : just-code secrets bindings list", guestReadable), "Warning: %d project secret(s) are delivered readable inside %s. "+
			"Everything running in that sandbox, including the agent, can read them; they are readable for as long as the sandbox holds them, "+
			"and a snapshot taken now captures them. Run 'just-code secrets bindings list' to review.",
			guestReadable, m.InstanceName())
	}
	return out, nil
}

// projectSecretDeliveriesOf renders the project-scoped bindings of a resolved
// set as deliveries, carrying the marker the proxy already exposes.
func (m *MicrosandboxRuntime) projectSecretDeliveriesOf(bindings []resolvedBinding) []ProjectSecretDelivery {
	var deliveries []ProjectSecretDelivery
	for _, b := range bindings {
		if b.Project == nil {
			continue
		}
		deliveries = append(deliveries, ProjectSecretDelivery{
			ProjectSecretBinding: ProjectSecretBinding{
				Profile: b.Project.Profile, Component: b.Project.Component, Name: b.Project.Name,
				GuestEnv: b.GuestEnv, Destinations: append([]string(nil), b.AllowHosts...),
			},
			Reference: b.Project.Reference,
			Marker:    b.Placeholder,
		})
	}
	return deliveries
}

func (m *MicrosandboxRuntime) startInstance(ctx context.Context, bindings []resolvedBinding) error {
	return m.startInstanceWithBackendPreflight(ctx, bindings, nil)
}

func (m *MicrosandboxRuntime) startInstanceWithBackendPreflight(ctx context.Context, bindings []resolvedBinding, beforeBackendLaunch func() error) error {
	if beforeBackendLaunch == nil {
		beforeBackendLaunch = func() error { return m.installProjectSkills(ctx) }
	}
	if err := os.MkdirAll(m.cfg.WorkspaceDir, 0o755); err != nil {
		return err
	}
	// No workspace gate: with the sealed model (P22) the host checkout is not
	// mounted, so a secret in it is not reachable from the guest. The scan's
	// detection logic now runs at the transfer boundary
	// (ResolveTransferSet), where refusing a file actually protects
	// something. What remains here is hygiene advice, never a block.
	m.noteWorkspaceHygiene(ctx)
	if err := m.Client.EnsureInstalled(ctx); err != nil {
		return err
	}

	sandbox, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil {
		return err
	}
	if exists {
		// A sandbox whose /workspace is not guest-owned was created by a
		// pre-P22 version: it exposes the checkout to the guest, which the
		// sealed model forbids. Such an instance is never adopted in place.
		if err := m.requireSealedWorkspace(ctx); err != nil {
			return err
		}
		// The mode-specific start script is persisted at creation and the SDK
		// cannot rewrite it, so switching isolation on an existing sandbox
		// would boot the wrong process (a full-mode sandbox would start
		// `opencode serve`; a backend-mode one would only sleep). Fail with
		// guidance instead of booting into the wrong mode.
		if err := m.rejectIsolationMismatch(ctx, sandbox); err != nil {
			return err
		}
		// Port mappings are fixed in the persisted sandbox config. A full-mode
		// instance created by an older build may still expose the unused
		// OpenCode server port; do not silently adopt it after an upgrade.
		if err := m.rejectFullServerPortForwarding(ctx); err != nil {
			return err
		}
		// A sandbox created by an earlier version may still persist the real
		// Albert key in its guest environment. Refuse to boot it: the key
		// would stay readable inside the guest.
		if err := m.rejectLegacyRawKey(ctx); err != nil {
			return err
		}
	}

	if exists && sandbox.Status == "running" {
		if m.cfg.Isolation == IsolationFull {
			// "Running" only means the VM is up: a previous creation may have
			// timed out waiting for the toolchain while the background
			// installer kept going. Gate on the readiness marker here too,
			// or a retry would attach the TUI to an unprepared guest.
			if err := m.waitForToolchain(ctx); err != nil {
				return err
			}
			if !m.Launch.IsFirstStart() {
				m.Launch.AnnounceWarmStart()
			}
			m.say("%s is running (isolation full; the TUI runs inside the microVM).", m.InstanceName())
			return nil
		}
		if m.backendHealthy(ctx) {
			m.Launch.AnnounceWarmStart()
			m.say("%s is running with a healthy OpenCode backend.", m.InstanceName())
			return nil
		}
		m.announceExistingStart(ctx)
		if err := beforeBackendLaunch(); err != nil {
			return err
		}
		m.say("%s is running but the OpenCode backend is not responding; restarting it inside the microVM...", m.InstanceName())
		return m.launchBackend(ctx)
	}

	if exists {
		m.say("Starting %s...", m.InstanceName())
		// Both isolation modes use the same secret-proxy configuration: the
		// guest environment carries the placeholder, never the real key. The
		// reference re-resolves from this process's environment at boot, so a
		// rotated credential is picked up here without any value being
		// persisted.
		// The composed OPENCODE_CONFIG_CONTENT (managed model selection over
		// the base asset) rides the same modification, so a model change is
		// effective on the next boot of a stopped sandbox.
		if err := m.Client.ModifyNextStart(ctx, m.InstanceName(), m.nextStartEnv(), bindingsMetadata(bindings), m.appliedProjectBindings()); err != nil {
			return err
		}
		if err := m.Client.Start(ctx, m.InstanceName()); err != nil {
			return err
		}
		if m.cfg.Isolation == IsolationFull {
			// Booting a stopped VM does not re-run the entrypoint: relaunch
			// the (idempotent) start script so a sandbox stopped mid-
			// preparation finishes installing, then gate on readiness as
			// at creation.
			if err := m.launchBackend(ctx); err != nil {
				return err
			}
			if err := m.waitForToolchain(ctx); err != nil {
				return err
			}
			if !m.Launch.IsFirstStart() {
				m.Launch.AnnounceWarmStart()
			}
			m.say("%s started (isolation full; the TUI runs inside the microVM).", m.InstanceName())
			return nil
		}
		// Booting a stopped VM does not re-run the container entrypoint, so the
		// backend has to be launched explicitly. Reconcile managed skills first
		// so an orphaned skill can never be served by the restarted backend.
		m.announceExistingStart(ctx)
		if err := beforeBackendLaunch(); err != nil {
			return err
		}
		m.say("Launching OpenCode inside %s...", m.InstanceName())
		return m.launchBackend(ctx)
	}

	// Creating the sandbox is the first start: the guest has no toolchain yet
	// and the installer has to run before anything can be said about readiness.
	m.Launch.AnnounceFirstStart()
	m.say("Creating %s microVM...", m.InstanceName())
	m.say("First start installs the toolchain inside the microVM (build-base, node, python); this can take several minutes.")
	if err := m.Client.Create(ctx, m.sandboxSpec(bindings)); err != nil {
		return err
	}
	// The SDK create surface only accepts inline values, so each binding was
	// registered with an inert sentinel (msbSecretBootstrapValue). Rotate
	// every binding to its host env reference now, before the start script
	// runs: the raw value must never be what the persisted config holds. A
	// failed rotation removes the incomplete sandbox rather than leaving a
	// sentinel-bound or value-bound instance behind.
	if err := m.Client.RotateSecretsLive(ctx, m.InstanceName(), bindingsMetadata(bindings)); err != nil {
		_ = m.Client.Remove(ctx, m.InstanceName())
		return fmt.Errorf("rotating the credentials of %s onto the secret proxy failed (%w); the incomplete sandbox was removed, retry the start", m.InstanceName(), err)
	}
	// `msb create` boots an idle VM: the Go SDK has no equivalent of the Rust
	// SDK's transient LaunchIntent::Background, so the persisted start script
	// (which installs the toolchain and then execs `opencode serve`, or keeps
	// the VM alive in full mode) is never run by creation itself. Launch it
	// explicitly, the same way a restarted VM would.
	m.say("Launching the start script inside %s...", m.InstanceName())
	if err := m.launchBackend(ctx); err != nil {
		return err
	}
	if m.cfg.Isolation == IsolationFull {
		// Full mode has no health endpoint and attach runs the TUI
		// immediately after Start returns, so creation must not report
		// success while the guest is still preparing — or after the start
		// script died, in which case the marker never appears.
		return m.waitForToolchain(ctx)
	}
	return nil
}

// waitForToolchain blocks until the guest prep script signals completion by
// writing its marker, bounded by the configured start timeout. A guest where
// nothing is preparing is relaunched rather than polled: an idle VM left by
// the pre-#61 creation path, or a start script whose launch retries were
// exhausted, would otherwise wait out the whole timeout on every attempt and
// only be recoverable by destructive recreation.
func (m *MicrosandboxRuntime) waitForToolchain(ctx context.Context) error {
	timeout := m.cfg.StartTimeout
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}
	delay := m.toolchainPollDelay
	if delay <= 0 {
		delay = msbLaunchRetryDelay
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	relaunches := 0
	for first := true; ; first = false {
		code, _, err := m.Client.Exec(ctx, m.InstanceName(), msbGuestPrepareProbe)
		if err == nil {
			switch {
			case code == msbPrepareReady:
				return nil
			case code == msbPrepareProgress:
				// The installer is alive; keep waiting.
				if !m.Launch.IsFirstStart() {
					m.Launch.AnnounceFirstStart()
				}
			case code == msbPrepareIdle && relaunches < msbToolchainRelaunchLimit:
				if !m.Launch.IsFirstStart() {
					m.Launch.AnnounceFirstStart()
				}
				relaunches++
				m.say("Nothing is preparing the guest in %s; launching the start script...", m.InstanceName())
				if err := m.launchBackend(ctx); err != nil {
					return err
				}
			}
		}
		if first {
			// Quiet in the common case (sandbox already prepared); the wait
			// message only makes sense when there is something to wait for.
			m.say("Waiting for the guest toolchain to finish installing...")
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return fmt.Errorf("the guest toolchain was not ready within %s (the start script may have failed); "+
				"run 'just-code logs --microsandbox' or check %s inside the guest: %w",
				timeout, msbGuestLog, ctx.Err())
		}
	}
}

// sandboxSpec builds the full sandbox configuration from the config, the
// resolved bindings, and the embedded OpenCode config and start script. The
// secret proxy is configured identically for both isolation modes: where the
// TUI happens to run must not change whether credentials are protected. The
// spec carries binding metadata only — values transit exclusively through
// withHostSecrets.
func (m *MicrosandboxRuntime) sandboxSpec(bindings []resolvedBinding) msbSandboxSpec {
	mcpIDs := m.OpenCodeOverlay.MCPConnectors
	return msbSandboxSpec{
		Name:            m.InstanceName(),
		Image:           msbImageForMCPs(mcpIDs),
		Isolation:       m.cfg.Isolation,
		Env:             m.sandboxEnv(),
		SealedWorkspace: true,
		CPUs:            m.cfg.CPUs,
		MemoryMB:        m.cfg.MemoryMB,
		Ports:           append([]PortMapping(nil), m.cfg.Ports...),
		PortsConfigured: m.cfg.PortsConfigured,
		Bindings:        bindingsMetadata(bindings),
		StartScript:     msbStartScriptFor(m.cfg.Isolation, mcpIDs),
	}
}

// nextStartEnv builds the guest env persisted for the next boot of an
// existing stopped sandbox. The real ALBERT_API_KEY is deliberately absent:
// it travels as the proxy secret passed to ModifyNextStart, and the guest
// reads the placeholder Microsandbox exposes under the same variable name.
// ModifyNextStart merges, so only the fields that change need refreshing
// here: the server credentials, and the composed OpenCode config (a managed
// model selection is effective at the next boot).
func (m *MicrosandboxRuntime) nextStartEnv() map[string]string {
	content, err := ComposeConfigContent(m.OpenCodeOverlay)
	if err != nil {
		content = opencodeConfigContent
	}
	name, email := guestGitIdentity(m.guestGitConfig())
	env := map[string]string{
		"JUST_CODE_OPENCODE_VERSION": OpenCodeVersion,
		OpenCodeDisableUpdateEnv:     "1",
		"OPENCODE_PASSWORD":          m.cfg.Password,
		"OPENCODE_SERVER_PASSWORD":   m.cfg.Password,
		"OPENCODE_SERVER_USERNAME":   m.cfg.Username,
		"OPENCODE_CONFIG_CONTENT":    content,
		msbGitNameEnv:                name,
		msbGitEmailEnv:               email,
	}
	for key, value := range browserGuestEnv(m.OpenCodeOverlay.MCPConnectors) {
		env[key] = value
	}
	return env
}

// sandboxEnv builds the guest environment for the sandbox. As with
// nextStartEnv, the Albert key is provided by the secret proxy rather than
// the environment.
func (m *MicrosandboxRuntime) sandboxEnv() map[string]string {
	content, err := ComposeConfigContent(m.OpenCodeOverlay)
	if err != nil {
		// The embedded asset parses at init (mustAsset panics otherwise);
		// a failure here means the overlay itself was malformed, which the
		// composed document cannot represent. Fall back to the base asset
		// rather than losing the provider block entirely.
		content = opencodeConfigContent
	}
	env := map[string]string{
		"JUST_CODE_OPENCODE_VERSION": OpenCodeVersion,
		OpenCodeDisableUpdateEnv:     "1",
		"OPENCODE_PASSWORD":          m.cfg.Password,
		"OPENCODE_SERVER_PASSWORD":   m.cfg.Password,
		"OPENCODE_SERVER_USERNAME":   m.cfg.Username,
		"OPENCODE_CONFIG_CONTENT":    content,
	}
	// The commit identity for guest work (P11 settings, documented defaults
	// otherwise). It is configuration, not a secret.
	name, email := guestGitIdentity(m.guestGitConfig())
	env[msbGitNameEnv] = name
	env[msbGitEmailEnv] = email
	for key, value := range browserGuestEnv(m.OpenCodeOverlay.MCPConnectors) {
		env[key] = value
	}
	return env
}

// msbGitNameEnv and msbGitEmailEnv carry the guest commit identity into the
// sandbox environment, where the prep script reads them.
const (
	msbGitNameEnv  = "JUST_CODE_GIT_NAME"
	msbGitEmailEnv = "JUST_CODE_GIT_EMAIL"
)

// rejectLegacyRawKey refuses to boot a sandbox that persists a plaintext
// Albert key in its guest environment. Versions before proxy secret injection
// wrote the real ALBERT_API_KEY there in isolation full. The SDK cannot edit a
// persisted secret value, and booting such a sandbox would keep serving the
// plaintext key to the guest, so the only safe recovery is to recreate it.
//
// The inspection fails closed. Scrub-on-next-start (EnvRemove in
// msbNextStartOptions) only runs on the stopped path, and even there it cannot
// repair a guest that is already up: a running sandbox is returned early by
// Start and never reconfigured. So when the guest environment cannot be read
// there is no way to tell a protected sandbox from a leaking one, and treating
// the unreadable case as safe would let exactly the case this guard exists for
// slip through.
func (m *MicrosandboxRuntime) rejectLegacyRawKey(ctx context.Context) error {
	env, err := m.Client.Env(ctx, m.InstanceName())
	if err != nil {
		return fmt.Errorf("cannot read the guest environment of %s to check whether it stores a plaintext %s; "+
			"refusing to boot a sandbox whose credentials cannot be verified. "+
			"Run 'just-code restart --microsandbox' to recreate it with the key behind the secret proxy: %w",
			m.InstanceName(), msbAPISecretEnv, err)
	}
	value, ok := env[msbAPISecretEnv]
	if !ok {
		return nil
	}
	// Only the exact documented placeholder is the protected configuration and
	// safe to reuse. Anything else is a value the guest can read, including a
	// credential that merely looks like a runtime placeholder.
	if value == msbAPISecretPlaceholder {
		return nil
	}
	return fmt.Errorf("%s was created before proxy secret injection and stores a plaintext %s in its guest environment; "+
		"the value cannot be replaced in place. "+
		"Run 'just-code restart --microsandbox' to recreate the sandbox with the key behind the secret proxy: %w",
		m.InstanceName(), msbAPISecretEnv, errLegacyRawKeySandbox)
}

// warnUnprotectedRuntime tells the user that a runtime without a secret proxy
// hands the real credential to the guest. Switching to isolation backend does
// not avoid this: Tart and agent-vm expose the key in backend mode too, so the
// warning is about the runtime, not the isolation level.
func warnUnprotectedRuntime(runtimeName string) {
	fmt.Fprintf(os.Stderr, "Warning: %s has no secret injection, so the real inference credentials are readable inside the VM "+
		"(isolation backend and full alike). Any process in the guest, including the agent itself, can read and exfiltrate them. "+
		"Prefer --microsandbox for untrusted work, and treat the guest as holding a live credential.\n", runtimeName)
}

// errLegacyRawKeySandbox marks the refusal to boot a sandbox that stores a
// plaintext credential.
var errLegacyRawKeySandbox = errors.New("legacy sandbox stores a plaintext API key")

// launchBackend runs the container entrypoint inside a live VM. Recreating the
// sandbox runs it automatically; booting an existing one does not. The guest
// agent can lag the VM by a moment after start, hence the bounded retry.
//
// The command backgrounds the entrypoint and then sleeps one second, so a
// launcher that dies immediately (missing toolchain, ENOEXEC on an old
// shebang-less script) surfaces as a nonzero exit here instead of a silent
// success that only the downstream health wait would catch, minutes later.
func (m *MicrosandboxRuntime) launchBackend(ctx context.Context) error {
	delay := m.launchRetryDelay
	if delay == 0 {
		delay = msbLaunchRetryDelay
	}
	var last error
	for attempt := 1; attempt <= msbLaunchAttempts; attempt++ {
		code, stderr, err := m.Client.Exec(ctx, m.InstanceName(), msbRelaunchCommand)
		if err == nil && code == 0 {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("sandbox exec exited %d: %s", code, strings.TrimSpace(stderr))
		}
		if attempt == msbLaunchAttempts {
			break
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("failed to launch OpenCode inside %s: %w", m.InstanceName(), last)
}

// backendHealthy probes the backend that should be listening in the VM.
func (m *MicrosandboxRuntime) backendHealthy(ctx context.Context) bool {
	endpoint, err := m.Endpoint(ctx)
	if err != nil {
		return false
	}
	probe := m.Probe
	if probe == nil {
		probe = func(ctx context.Context, endpoint, username, password string) HealthProbe {
			return ProbeHealth(ctx, nil, endpoint, username, password)
		}
	}
	return probe(ctx, endpoint, m.cfg.Username, m.cfg.Password).Healthy
}

// requireSealedWorkspace proves that the instance's /workspace is guest-owned
// storage before anything reads or writes it. Every operation that touches the
// guest workspace calls this first: with a pre-P22 bind mount, /workspace
// inside the guest IS the host checkout, so a transfer would extract over the
// user's files and `git add -A` would stage and commit them.
//
// The check is positive and fail-closed. Asking "is there a bind mount?" and
// defaulting to safe when the answer is unclear would let an unrecognized
// mount shape through; the question that matters is "is this storage owned by
// the guest?", and anything less than a proven yes is refused.
//
// The reading comes from the persisted sandbox configuration, not from
// just-code's own spec, so a spec the runtime ignored cannot satisfy it.
func (m *MicrosandboxRuntime) requireSealedWorkspace(ctx context.Context) error {
	if _, exists, err := m.Client.Lookup(ctx, m.InstanceName()); err != nil {
		return fmt.Errorf("cannot look up %s to verify its workspace isolation: %w", m.InstanceName(), err)
	} else if !exists {
		return fmt.Errorf("%s does not exist yet; run 'just-code start' first", m.InstanceName())
	}
	// A recognized bind source is reported by path: naming it is the clearest
	// way to explain the refusal.
	mounted, err := m.Client.WorkspaceMount(ctx, m.InstanceName())
	if err != nil {
		return fmt.Errorf("cannot read the /workspace provenance of %s; refusing to use an instance whose workspace isolation cannot be verified. "+
			"Recreate it with 'just-code clean --microsandbox' then 'just-code start --microsandbox': %w", m.InstanceName(), err)
	}
	if mounted != "" {
		return fmt.Errorf("%s was created with %s mounted from the host at /workspace, which exposes the host checkout to the guest. "+
			"The sealed workspace model has no host mount, and an existing instance is never converted in place: "+
			"run 'just-code clean --microsandbox' then 'just-code start --microsandbox' to recreate it, or use an explicit legacy runtime (--tart) for this project",
			m.InstanceName(), mounted)
	}
	// No recognized bind source is not proof either: confirm owned storage.
	owned, err := m.Client.WorkspaceOwned(ctx, m.InstanceName())
	if err != nil {
		return fmt.Errorf("cannot confirm that /workspace of %s is guest-owned storage; refusing to use an instance whose workspace isolation cannot be verified. "+
			"Recreate it with 'just-code clean --microsandbox' then 'just-code start --microsandbox': %w", m.InstanceName(), err)
	}
	if !owned {
		return fmt.Errorf("/workspace of %s is not guest-owned storage: the persisted configuration shows a workspace this runtime does not recognize as sealed, "+
			"so the host checkout may be mounted into the guest. The sealed model has no host mount, and an existing instance is never converted in place: "+
			"run 'just-code clean --microsandbox' then 'just-code start --microsandbox' to recreate it", m.InstanceName())
	}
	return nil
}

// rejectFullServerPortForwarding refuses an existing full-mode sandbox whose
// persisted network config still exposes the backend-only OpenCode server
// port. Port maps are creation-time state and cannot be removed in place;
// migration therefore requires the explicit destructive recreate command.
func (m *MicrosandboxRuntime) rejectFullServerPortForwarding(ctx context.Context) error {
	if m.cfg.Isolation != IsolationFull {
		return nil
	}
	forwarded, err := m.Client.ServerPortForwarded(ctx, m.InstanceName())
	if err != nil {
		return &ErrRecreateNeeded{
			Instance: m.InstanceName(),
			Reason:   fmt.Sprintf("cannot verify whether the persisted network configuration still forwards the OpenCode server port: %v", err),
		}
	}
	if forwarded {
		return &ErrRecreateNeeded{
			Instance: m.InstanceName(),
			Reason:   fmt.Sprintf("the full-mode sandbox still forwards the backend-only OpenCode server port %d; persisted port mappings cannot be changed in place", DefaultPort),
		}
	}
	return nil
}

// workspaceHygieneCounts reports how many files in the host checkout look like
// secrets. It is a measurement only: it never blocks, because under the sealed
// model the checkout is not mounted and the transfer filter refuses these
// files at the guest boundary. How the number is presented is the caller's
// decision — the plain path prints the detail block, the first-launch
// checklist (issue #172) folds it into a single line.
func workspaceHygieneCounts(ctx context.Context, dir string) (dotenv, secrets int) {
	res, err := ScanWorkspace(ctx, dir)
	if err != nil {
		return 0, 0
	}
	return len(res.DotenvFiles), len(res.GitleaksFindings)
}

// warnWorkspaceHygiene prints host-side advice about secrets sitting in the
// checkout, in full. A block here would be advice pretending to be a security
// boundary, so this is guidance, never a gate.
func warnWorkspaceHygiene(ctx context.Context, dir string) {
	res, err := ScanWorkspace(ctx, dir)
	if err != nil {
		return
	}
	if len(res.DotenvFiles) == 0 && len(res.GitleaksFindings) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "Note: %s carries files that look like secrets in the host checkout. They are NOT mounted into the guest and the transfer filter excludes them"+
		" (re-include a specific file with 'just-code workspace allow <path>' if the guest genuinely needs it).\n", dir)
	for _, f := range res.DotenvFiles {
		fmt.Fprintf(os.Stderr, "  dotenv file (excluded from transfer): %s\n", f)
	}
	for _, f := range res.GitleaksFindings {
		fmt.Fprintf(os.Stderr, "  secret found in the checkout (excluded from transfer): %s\n", f)
	}
}

// noteWorkspaceHygiene is the launch-aware form of the host-checkout advice.
// During a first launch (issue #172) the copy line of the checklist already
// says how many files stayed behind, so the English block would state the same
// fact twice; it is dropped and `just-code doctor` keeps the detail. Anywhere
// else the block prints as before, so scripts, CI and plain starts lose nothing.
func (m *MicrosandboxRuntime) noteWorkspaceHygiene(ctx context.Context) {
	if m.Launch != nil && m.Launch.Terminal {
		return
	}
	warnWorkspaceHygiene(ctx, m.cfg.WorkspaceDir)
}

// rejectIsolationMismatch compares the isolation mode the sandbox was
// created in (inferred from its persisted start script) with the requested
// mode. The script cannot be rewritten in place, so a mismatch is an error:
// booting would run the wrong process for the requested mode.
func (m *MicrosandboxRuntime) rejectIsolationMismatch(ctx context.Context, sandbox msbSandboxInfo) error {
	script, err := m.Client.StartScript(ctx, sandbox.Name)
	if err != nil || script == "" {
		// Undetectable (older runtime, unreadable config): proceed as before
		// rather than blocking every start on a best-effort check.
		return nil
	}
	createdFull := strings.Contains(script, "sleep infinity")
	if createdFull == (m.cfg.Isolation == IsolationFull) {
		if !strings.Contains(script, "@opencode/cli@") {
			return fmt.Errorf("%s has a legacy OpenCode start script fixed at creation; OpenCode v%s requires 'just-code recreate --microsandbox'. Export any guest sessions and changes first: recreation deletes guest-only state", sandbox.Name, OpenCodeVersion)
		}
		return nil
	}
	createdMode := IsolationBackend
	requestedMode := m.cfg.Isolation
	if createdFull {
		createdMode = IsolationFull
	}
	if requestedMode == "" {
		requestedMode = IsolationBackend
	}
	return fmt.Errorf("%s was created in isolation %s mode and cannot be switched to %s mode in place: "+
		"the start script is fixed when the sandbox is created. "+
		"Run 'just-code recreate --microsandbox' to rebuild it in %s mode",
		sandbox.Name, createdMode, requestedMode, requestedMode)
}

func (m *MicrosandboxRuntime) Stop(ctx context.Context) error {
	running, err := m.IsRunning(ctx)
	if err != nil {
		return err
	}
	if !running {
		fmt.Printf("%s is not running.\n", m.InstanceName())
		return nil
	}
	fmt.Printf("Stopping %s...\n", m.InstanceName())
	return m.Client.Stop(ctx, m.InstanceName())
}

// Restart is non-destructive (P07): it stops and starts the existing
// sandbox, preserving disk and guest state. The preflights run first so a
// rejected workspace or a bad config aborts before anything is touched.
// The destructive rebuild is Recreate.
func (m *MicrosandboxRuntime) Restart(ctx context.Context) error {
	if err := m.validateConfig(); err != nil {
		return err
	}
	// Resolve credentials before stopping: a resolution failure must not
	// leave a previously usable sandbox stopped.
	bindings, err := m.resolveBindings(ctx)
	if err != nil {
		return err
	}
	if err := m.validateStoredGitHubOrigin(bindings); err != nil {
		return err
	}
	if err := os.MkdirAll(m.cfg.WorkspaceDir, 0o755); err != nil {
		return err
	}
	m.noteWorkspaceHygiene(ctx)
	// Re-run the credential preflight now that the directory exists. A
	// workspace deleted while its sandbox and project-secret state survived is
	// not "no project scope": recreating the same path yields the same
	// namespace, and the store recorded for it must still resolve. The earlier
	// resolution saw a missing root and skipped that check, so without this
	// Stop would take a usable sandbox down before Start reported the same
	// failure.
	bindings, err = m.resolveBindings(ctx)
	if err != nil {
		return err
	}
	if err := m.validateStoredGitHubOrigin(bindings); err != nil {
		return err
	}
	// Detect recreation-only states before stopping: a creation-fixed
	// isolation mismatch cannot be fixed by restart, and stopping first
	// would leave a previously usable sandbox down with a looping error.
	if err := m.rejectRecreationOnlyStates(ctx); err != nil {
		return err
	}
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

// rejectRecreationOnlyStates inspects an existing sandbox for conditions
// restart cannot fix. It is a no-op when the sandbox does not exist.
func (m *MicrosandboxRuntime) rejectRecreationOnlyStates(ctx context.Context) error {
	sandbox, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil || !exists {
		return err
	}
	// Workspace provenance is a recreation-only state too: Restart runs Stop
	// before Start's own check, so without this a legacy bind-mounted
	// instance would be stopped and then refused, leaving a previously
	// usable sandbox down.
	if err := m.requireSealedWorkspace(ctx); err != nil {
		return err
	}
	// A recorded sizing change is recreation-only as well: Restart boots the
	// existing guest with the sizing it was created with, so proceeding would
	// keep the old allocation while appearing to apply the new one. Isolation
	// is refused on this same path; sizing must not be the one attribute that
	// is silently kept.
	if err := m.rejectResourceSizingChange(); err != nil {
		return err
	}
	if err := m.rejectIsolationMismatch(ctx, sandbox); err != nil {
		return err
	}
	// Port mappings are also creation-fixed: refuse before Stop so a stale
	// full-mode instance remains available until the user approves recreation.
	if err := m.rejectFullServerPortForwarding(ctx); err != nil {
		return err
	}
	if sandbox.Status == "running" {
		if _, _, err := m.preflightProjectSkills(ctx); err != nil {
			return err
		}
	}
	return nil
}

// rejectResourceSizingChange refuses a restart when the applied state records
// a sizing different from the configured one. An unrecorded state (0) is not
// a mismatch: it cannot be compared, and the sizing is not what the user
// asked to change.
func (m *MicrosandboxRuntime) rejectResourceSizingChange() error {
	// The reconcile journal's location, not the binding-approval directory:
	// this reads the record Reconcile writes, so the two must agree even when
	// a caller overrides StateDir.
	applied, err := ReadInstanceState(DefaultFS, instanceStatePath(DefaultStateDir(), m.InstanceName()))
	if err != nil {
		// An unreadable record cannot answer the question. Fail closed, as the
		// reconcile path does with the same file: booting on a guess would be
		// the silent case this guard exists to prevent.
		return fmt.Errorf("cannot read the recorded state of %s to check its guest sizing; refusing to restart on an unverifiable record. "+
			"Recreate it with 'just-code recreate --microsandbox' if the record cannot be recovered: %w", m.InstanceName(), err)
	}
	if applied == nil {
		return nil
	}
	changed := (applied.CPUs != 0 && applied.CPUs != m.cfg.CPUs) ||
		(applied.MemoryMB != 0 && applied.MemoryMB != m.cfg.MemoryMB)
	if !changed {
		return nil
	}
	return fmt.Errorf("%s was created with %d CPUs and %d MiB, and a microVM cannot be resized after creation (now %d CPUs and %d MiB). "+
		"Run 'just-code recreate --microsandbox' to apply the new sizing, or restore the previous value",
		m.InstanceName(), applied.CPUs, applied.MemoryMB, m.cfg.CPUs, m.cfg.MemoryMB)
}

func (m *MicrosandboxRuntime) Clean(ctx context.Context) error {
	_, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("%s does not exist.\n", m.InstanceName())
		return nil
	}
	// Disposing is destructive: say so before and after, so a silent success
	// can never be mistaken for "nothing happened".
	fmt.Printf("Removing %s (sandbox and local state)...\n", m.InstanceName())
	if err := m.Client.Remove(ctx, m.InstanceName()); err != nil {
		return err
	}
	fmt.Printf("%s removed.\n", m.InstanceName())
	return nil
}

// Doctor verifies the installed runtime without installing or mutating it.
// Unlike lifecycle commands, its output must reach the user even on success —
// a doctor that prints nothing is indistinguishable from one that did not run.
func (m *MicrosandboxRuntime) Doctor(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, msbDoctorTimeout)
	defer cancel()
	output, err := m.Client.Doctor(dctx)
	if err != nil {
		return err
	}
	fmt.Print(output)
	if !strings.HasSuffix(output, "\n") {
		fmt.Println()
	}
	fmt.Println("Microsandbox runtime is ready.")
	return nil
}

func (m *MicrosandboxRuntime) Logs() error {
	return m.Client.Logs(m.InstanceName())
}

func (m *MicrosandboxRuntime) Shell() error {
	return m.Client.Shell(m.InstanceName())
}

// RunAgent launches the OpenCode TUI in the foreground inside the microVM,
// resuming its latest session (the TUI's /new command starts a fresh one),
// with /workspace as its working directory (isolation full). The SDK attach
// channel passes the host terminal through.
func (m *MicrosandboxRuntime) RunAgent(ctx context.Context) error {
	sessions, _, listCode, listErr := m.Client.ExecCapture(ctx, m.InstanceName(), "cd /workspace && opencode session list --standalone --format json")
	if listErr == nil && listCode != 0 {
		listErr = fmt.Errorf("OpenCode session list exited with code %d", listCode)
	}
	hasSessions := false
	if listErr == nil {
		hasSessions, listErr = OpenCodeHasSessions(sessions)
	}
	var help, stderr string
	var probeErr error
	if listErr == nil && hasSessions {
		var code int
		help, stderr, code, probeErr = m.Client.ExecCapture(ctx, m.InstanceName(), "opencode --help")
		if probeErr == nil && code != 0 {
			probeErr = fmt.Errorf("OpenCode help exited with code %d", code)
		}
	}
	resumeArgs, warn := OpenCodeResumeArgs(hasSessions, listErr, help+"\n"+stderr, probeErr)
	if warn {
		if m.Launch != nil && m.Launch.Terminal {
			m.Launch.AddWarning("reprise automatique indisponible ; les sessions restent accessibles dans OpenCode")
		} else {
			WarnOpenCodeResumeUnavailable("dans le sandbox Microsandbox")
		}
	}
	if m.Launch != nil && m.Launch.Terminal {
		for _, line := range m.Launch.Checklist() {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	code, err := m.Client.AttachInteractive(ctx, m.InstanceName(), "opencode", append([]string{"--standalone"}, resumeArgs...), "/workspace")
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("opencode TUI exited %d inside %s", code, m.InstanceName())
	}
	return nil
}

// Status describes the sandbox's current state, for `check` in isolation
// full where there is no health endpoint to probe.
func (m *MicrosandboxRuntime) Status(ctx context.Context) (string, error) {
	sandbox, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil {
		return "", err
	}
	if !exists {
		return fmt.Sprintf("%s does not exist", m.InstanceName()), nil
	}
	return fmt.Sprintf("%s is %s", m.InstanceName(), sandbox.Status), nil
}

func (m *MicrosandboxRuntime) IsRunning(ctx context.Context) (bool, error) {
	sandbox, exists, err := m.Client.Lookup(ctx, m.InstanceName())
	if err != nil {
		return false, err
	}
	return exists && sandbox.Status == "running", nil
}

// RunningInstances returns the names of all managed Microsandbox sandboxes
// that are up, plus the legacy singleton when it is up (it predates the
// ownership label, so it is invisible to List). The order is deterministic.
func (m *MicrosandboxRuntime) RunningInstances(ctx context.Context) ([]string, error) {
	var names []string
	sandboxes, err := m.Client.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, sb := range sandboxes {
		if sb.Status == "running" {
			names = append(names, sb.Name)
		}
	}
	// The legacy singleton is enumerated by name: it was created before the
	// ownership label existed.
	if m.InstanceName() == msbSandbox || m.instance != "" {
		if legacy, exists, err := m.Client.Lookup(ctx, msbSandbox); err == nil && exists && legacy.Status == "running" {
			names = append(names, msbSandbox)
		} else if err != nil {
			return nil, err
		}
	}
	sort.Strings(names)
	return names, nil
}

// StopInstance stops a named managed instance. It is the per-project form of
// Stop: stopping project A's instance never touches project B's.
func (m *MicrosandboxRuntime) StopInstance(ctx context.Context, name string) error {
	sandbox, exists, err := m.Client.Lookup(ctx, name)
	if err != nil {
		return err
	}
	if !exists {
		fmt.Printf("%s does not exist.\n", name)
		return nil
	}
	if sandbox.Status != "running" {
		fmt.Printf("%s is not running.\n", name)
		return nil
	}
	fmt.Printf("Stopping %s...\n", name)
	return m.Client.Stop(ctx, name)
}

func (m *MicrosandboxRuntime) Endpoint(context.Context) (string, error) {
	return "http://localhost:" + strconv.Itoa(DefaultPort), nil
}
