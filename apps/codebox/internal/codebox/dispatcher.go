package codebox

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// Dispatcher wires the runtimes together and implements the cross-runtime
// commands: stop (all), check (single), and runtime-conflict resolution.
type Dispatcher struct {
	cfg      Config
	backends map[Runtime]Backend
	// Runner is used only by the migration cleanup for the removed Docker
	// runtime; each backend carries its own.
	Runner Runner
	Launch *LaunchUX
}

// SetLaunchUX attaches the concise launch report to the supported runtime.
func (d *Dispatcher) SetLaunchUX(u *LaunchUX) {
	d.Launch = u
	if m, ok := d.backends[RuntimeMicrosandbox].(*MicrosandboxRuntime); ok {
		m.Launch = u
		if u != nil {
			u.TimingKey = LaunchTimingKeyFor(m.cfg, msbImageForMCPs(m.OpenCodeOverlay.MCPConnectors))
		}
	}
}

// SetGatewayProjectSecrets injects the remote development-secrets source into
// the Microsandbox backend. A nil source leaves the local OS-store workflow
// intact; declared is true when the project manifest has a `secrets` block, so
// a missing resolver fails closed instead of falling back to stale values.
func (d *Dispatcher) SetGatewayProjectSecrets(source ProjectSecretValueSource, manifest *ProjectSecretsManifest) {
	if m, ok := d.backends[RuntimeMicrosandbox].(*MicrosandboxRuntime); ok {
		m.ProjectSecretValues = source
		m.ProjectSecretsManifest = manifest
		m.ProjectSecretsDeclared = manifest != nil
	}
}

// RecordLaunchSkills runs after a successful start has reconciled the skills.
func (d *Dispatcher) RecordLaunchSkills() {
	var names []string
	for _, skill := range d.cfg.ProjectSkills {
		names = append(names, skill.ID)
	}
	d.Launch.RecordSkills(names)
}

// WaitLaunchReady gates reports and timing even when reconciliation bypasses
// Start: full isolation uses the guest marker, backend isolation uses health.
func (d *Dispatcher) WaitLaunchReady(ctx context.Context, b Backend) error {
	if d.Launch == nil {
		return nil
	}
	if d.cfg.Isolation == IsolationFull {
		if m, ok := b.(*MicrosandboxRuntime); ok {
			return m.waitForToolchain(ctx)
		}
		return nil
	}
	if d.cfg.StartTimeoutErr != nil {
		return d.cfg.StartTimeoutErr
	}
	endpoint, err := b.Endpoint(ctx)
	if err != nil {
		return err
	}
	health := DefaultHealthConfig()
	health.Deadline = d.cfg.StartTimeout
	health.Quiet = d.Launch.Terminal
	if !health.Quiet {
		health.Progress = os.Stderr
	}
	if err := WaitHealthy(ctx, endpoint, d.cfg.Username, d.cfg.Password, health, nil); err != nil {
		return fmt.Errorf("%w\n  Run 'codebox logs' to inspect the backend", err)
	}
	return nil
}

// NewDispatcher builds a dispatcher over the runtimes for a config. Without
// a project context the backends operate on the legacy singleton instances,
// so existing behavior is unchanged (P06).
func NewDispatcher(cfg Config) *Dispatcher {
	return NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: NewMicrosandboxRuntime(cfg),
		RuntimeTart:         NewTart(cfg),
		RuntimeAgentVM:      NewAgentVM(cfg),
	})
}

// NewDispatcherForInstance builds a dispatcher whose backends are all bound
// to one project-derived instance name (P06): every lifecycle operation the
// dispatcher drives targets that project's instances only.
func NewDispatcherForInstance(cfg Config, instance string) *Dispatcher {
	return NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: NewMicrosandboxRuntimeForInstance(cfg, instance),
		RuntimeTart:         NewTartForInstance(cfg, instance),
		RuntimeAgentVM:      NewAgentVMForInstance(cfg, instance),
	})
}

// NewDispatcherWith builds a dispatcher over an explicit set of backends,
// primarily for tests.
func NewDispatcherWith(cfg Config, backends map[Runtime]Backend) *Dispatcher {
	return &Dispatcher{cfg: cfg, backends: backends, Runner: OSRunner{}}
}

// SetOpenCodeOverlay applies the managed OpenCode configuration layer
// (P10) to every backend that carries one.
func (d *Dispatcher) SetOpenCodeOverlay(o ManagedOverlay) {
	for _, b := range d.backends {
		switch v := b.(type) {
		case *MicrosandboxRuntime:
			v.OpenCodeOverlay = o
		case *Tart:
			v.OpenCodeOverlay = o
		case *AgentVM:
			v.OpenCodeOverlay = o
		}
	}
}

// Backend returns the backend for a runtime.
func (d *Dispatcher) Backend(rt Runtime) (Backend, error) {
	b, ok := d.backends[rt]
	if !ok {
		return nil, fmt.Errorf("unknown runtime %q", rt)
	}
	return b, nil
}

// Running returns the active runtimes in deterministic order. Only runtimes
// supported on this platform are probed (see supportedRuntimes).
func (d *Dispatcher) Running(ctx context.Context) ([]Runtime, error) {
	var out []Runtime
	for _, rt := range supportedRuntimes() {
		on, err := d.backends[rt].IsRunning(ctx)
		if err != nil {
			return nil, err
		}
		if on {
			out = append(out, rt)
		}
	}
	return out, nil
}

// SingleRunning returns the sole active runtime, or an error if none or more
// than one is running.
func (d *Dispatcher) SingleRunning(ctx context.Context) (Runtime, error) {
	running, err := d.Running(ctx)
	if err != nil {
		return "", err
	}
	switch len(running) {
	case 0:
		return "", fmt.Errorf("aucune machine isolée codebox n'est en cours d'exécution")
	case 1:
		return running[0], nil
	default:
		return "", fmt.Errorf("multiple codebox runtimes are running; run `codebox stop` first")
	}
}

// instanceBackend is the per-instance surface a backend exposes for the
// global sweeps (StopAll, Prepare conflict detection). It is implemented by
// the tart, agent-vm, and microsandbox backends.
type instanceBackend interface {
	RunningInstances(ctx context.Context) ([]string, error)
	StopInstance(ctx context.Context, name string) error
}

// StopAll stops every active managed instance on the host, across runtimes
// and projects, mirroring `just stop --all`. The dispatcher's backends are
// bound to the current project (P06), so their Stop is project-scoped and
// would leave other projects' instances running. The sweep therefore
// enumerates instances globally per backend (RunningInstances) and stops
// each by name (StopInstance), not the dispatcher's own instance.
func (d *Dispatcher) StopAll(ctx context.Context) error {
	if err := d.clearLegacyDocker(ctx); err != nil {
		return err
	}
	var firstErr error
	stoppedAny := false
	for _, rt := range supportedRuntimes() {
		b := d.backends[rt]
		ib, ok := b.(instanceBackend)
		if !ok {
			// Backend without per-instance enumeration (none today outside
			// tests): fall back to its project-scoped Stop when it runs.
			running, err := b.IsRunning(ctx)
			if err != nil {
				return err
			}
			if !running {
				continue
			}
			stoppedAny = true
			if err := b.Stop(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		instances, err := ib.RunningInstances(ctx)
		if err != nil {
			if commandNotFound(err) {
				continue
			}
			return err
		}
		for _, name := range instances {
			stoppedAny = true
			if err := ib.StopInstance(ctx, name); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if !stoppedAny && firstErr == nil {
		fmt.Println("Aucune machine isolée codebox n'est en cours d'exécution.")
	}
	return firstErr
}

// Stop stops the current project's instance on every runtime, the new
// project-scoped `stop` contract (P06). Stopping project A never touches
// project B: each backend stops only the instance it was constructed with.
// An explicit all-instance stop is StopAll (`stop --all`).
func (d *Dispatcher) Stop(ctx context.Context) error {
	if err := d.clearLegacyDocker(ctx); err != nil {
		return err
	}
	stopped := false
	for _, rt := range supportedRuntimes() {
		b, ok := d.backends[rt]
		if !ok {
			continue
		}
		running, err := b.IsRunning(ctx)
		if err != nil {
			return err
		}
		if !running {
			continue
		}
		if err := b.Stop(ctx); err != nil {
			return err
		}
		stopped = true
	}
	if !stopped {
		fmt.Println("Aucune machine isolée codebox n'est en cours d'exécution pour ce projet.")
	}
	return nil
}

// Prepare resolves conflicts before starting a runtime: it migrates a host
// still carrying the removed Docker runtime's container, then, if a different
// runtime is already active, prompts to stop it (interactive) or refuses.
// Prepare detects conflicts before starting the requested runtime. Since
// P06 the backends are project-scoped: d.Running reports only the current
// project's instances, so a conflict in another project would be invisible
// and two backends could run simultaneously. Conflicts are therefore
// enumerated globally: every managed instance on the host, across runtimes
// and projects. The user can still confirm and switch runtimes; the stop
// applies to the conflicting instances by name.
func (d *Dispatcher) Prepare(ctx context.Context, requested Runtime) error {
	if err := d.clearLegacyDocker(ctx); err != nil {
		return err
	}
	conflicts, err := d.otherRunningInstances(ctx, requested)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}

	sort.Strings(conflicts)
	names := strings.Join(conflicts, " ")
	if !isTerminal(os.Stdin) {
		return fmt.Errorf("%s est déjà en cours d'exécution. Lancez `codebox stop` avant de démarrer %s.", names, requested)
	}
	fmt.Printf("%s est déjà en cours d'exécution. L'arrêter pour démarrer %s ? [o/N] ", names, requested)
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(reply)) {
	case "o", "oui", "y", "yes":
		for _, name := range conflicts {
			ib, ok := d.instanceBackendFor(name)
			if !ok {
				continue
			}
			if err := ib.StopInstance(ctx, name); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s reste en cours d'exécution", names)
	}
}

// otherRunningInstances enumerates every managed instance running on the
// host, across runtimes and projects, except the current project's instance
// of the requested runtime (that is the instance Prepare is about to start;
// reusing it is legitimate). Only the current project's own instance is
// ignored: another project's instance of the SAME runtime is a conflict,
// both for the one-active-instance policy and because the per-project
// backends can contend for the same host ports.
func (d *Dispatcher) otherRunningInstances(ctx context.Context, requested Runtime) ([]string, error) {
	own := d.ownInstanceName(requested)
	var conflicts []string
	for _, rt := range supportedRuntimes() {
		b := d.backends[rt]
		ib, ok := b.(instanceBackend)
		if !ok {
			// Backend without per-instance enumeration (none today outside
			// tests): its project-scoped IsRunning is the only signal.
			if rt == requested {
				continue
			}
			running, err := b.IsRunning(ctx)
			if err != nil {
				return nil, err
			}
			if running {
				conflicts = append(conflicts, string(rt))
			}
			continue
		}
		instances, err := ib.RunningInstances(ctx)
		if err != nil {
			if commandNotFound(err) {
				continue
			}
			return nil, err
		}
		for _, name := range instances {
			if rt == requested && name == own {
				// The current project's own instance of the requested
				// runtime: not a conflict, Prepare reuses it.
				continue
			}
			conflicts = append(conflicts, name)
		}
	}
	return conflicts, nil
}

// ownInstanceName returns the current project's instance name for a runtime,
// or "" when the backend is not instance-bound.
func (d *Dispatcher) ownInstanceName(rt Runtime) string {
	switch b := d.backends[rt].(type) {
	case *Tart:
		return b.VMName()
	case *AgentVM:
		return b.VMName()
	case *MicrosandboxRuntime:
		return b.InstanceName()
	}
	return ""
}

// instanceBackendFor returns the backend able to stop the named instance.
func (d *Dispatcher) instanceBackendFor(name string) (instanceBackend, bool) {
	for _, rt := range supportedRuntimes() {
		ib, ok := d.backends[rt].(instanceBackend)
		if !ok {
			continue
		}
		if d.ownInstanceName(rt) == name {
			return ib, true
		}
		// Names not bound to this project may belong to this runtime's
		// global namespace; probe by enumeration.
		instances, err := ib.RunningInstances(context.Background())
		if err == nil {
			for _, n := range instances {
				if n == name {
					return ib, true
				}
			}
		}
	}
	return nil, false
}

// Check verifies the single running backend and reports its Albert provider
// status, mirroring `just check`. In isolation full there is no health
// endpoint to probe: it reports the VM state and that the session runs
// interactively inside the guest.
func (d *Dispatcher) Check(ctx context.Context, client *http.Client) error {
	rt, err := d.SingleRunning(ctx)
	if err != nil {
		return err
	}
	if d.cfg.Isolation == IsolationFull {
		state, err := d.backends[rt].Status(ctx)
		if err != nil {
			return err
		}
		fmt.Println(state)
		fmt.Printf("isolation full: the %s session runs interactively inside the guest (no health endpoint).\n", d.cfg.EffectiveHarness().DisplayName())
		if rt == RuntimeMicrosandbox {
			d.probePublishedPorts(ctx)
		}
		return nil
	}
	endpoint, err := d.backends[rt].Endpoint(ctx)
	if err != nil {
		return err
	}
	checkErr := CheckBackend(ctx, client, endpoint, d.cfg.Username, d.cfg.Password)
	if rt == RuntimeMicrosandbox {
		d.probePublishedPorts(ctx)
	}
	return checkErr
}

// appliedPortMappingsBackend is implemented by backends whose running
// instance can report the port mappings it was created with.
type appliedPortMappingsBackend interface {
	AppliedPortMappings(ctx context.Context) ([]PortMapping, error)
}

// probePublishedPorts reports, for every port the project explicitly mapped,
// whether a request from this machine gets an answer through the host-side
// forward. It is print-only: `check` reports preview reachability as
// information and never fails because a dev server is down. The legacy
// implicit 3000-3010 range is not probed; only configured mappings are, and
// only on Microsandbox: the mappings describe that runtime's host-loopback
// forwards, which no other runtime creates. An explicitly emptied mapping
// list still counts as configured: the sandbox keeps forwarding the removed
// mappings until recreate, and check reports them.
//
// Only mappings the running sandbox actually applied are probed: manifest
// changes take effect on recreate, so a newly configured host port may belong
// to an unrelated service and a removed mapping may still be forwarded.
func (d *Dispatcher) probePublishedPorts(ctx context.Context) {
	if !d.cfg.PortsConfigured {
		return
	}
	ab, ok := d.backends[RuntimeMicrosandbox].(appliedPortMappingsBackend)
	if !ok {
		// Backends that cannot report instance state keep the manifest
		// view; the production Microsandbox backend implements it.
		d.probePortMappings(ctx, d.cfg.Ports)
		return
	}
	applied, err := ab.AppliedPortMappings(ctx)
	if err != nil {
		fmt.Printf("port reachability: could not read the running sandbox's applied mappings (%v); not probing configured ports.\n", err)
		return
	}
	appliedSet := make(map[PortMapping]bool, len(applied))
	for _, pm := range applied {
		appliedSet[pm] = true
	}
	desiredSet := make(map[PortMapping]bool, len(d.cfg.Ports))
	for _, pm := range d.cfg.Ports {
		desiredSet[pm] = true
	}
	var live []PortMapping
	for _, pm := range d.cfg.Ports {
		if appliedSet[pm] {
			live = append(live, pm)
			continue
		}
		fmt.Printf("port %d (guest %d): configured but not forwarded by the running sandbox; run 'codebox recreate --microsandbox' to apply.\n", pm.Host, pm.Guest)
	}
	for _, pm := range applied {
		// The OpenCode backend's own server forward is not a user
		// preview mapping; ValidatePortMappings keeps DefaultPort out
		// of configured mappings entirely.
		if pm.Host == DefaultPort && pm.Guest == DefaultPort {
			continue
		}
		if !desiredSet[pm] {
			fmt.Printf("port %d (guest %d): still forwarded until the sandbox is recreated (removed from the project's port mappings).\n", pm.Host, pm.Guest)
		}
	}
	d.probePortMappings(ctx, live)
}

// portProbeBudget bounds the total time spent probing host forwards. It is
// a package variable so tests can shrink it; each mapping's probe keeps its
// own shorter timeout.
var portProbeBudget = 10 * time.Second

func (d *Dispatcher) probePortMappings(ctx context.Context, mappings []PortMapping) {
	// The first response is the reachability answer: following a redirect
	// would let a diagnostic probe make an unintended request outside
	// loopback, and a failed redirect target would hide a forward that
	// answered.
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Every dead forward costs its full probe timeout, so the loop runs
	// under a shared budget: a project carrying many unavailable mappings
	// cannot stall the check for minutes. Mappings beyond the budget are
	// reported as not probed rather than silently skipped.
	deadline := time.Now().Add(portProbeBudget)
	for _, pm := range mappings {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			fmt.Printf("port %d (guest %d): not probed; the port-probe budget (%s) was spent on earlier mappings.\n", pm.Host, pm.Guest, portProbeBudget)
			continue
		}
		timeout := 2 * time.Second
		if remaining < timeout {
			timeout = remaining
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(pctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", pm.Host), nil)
		if err != nil {
			cancel()
			fmt.Printf("port %d (guest %d): could not build the probe: %v\n", pm.Host, pm.Guest, err)
			continue
		}
		resp, err := client.Do(req)
		cancel()
		if err != nil {
			fmt.Printf("port %d (guest %d): no answer from the host forward. If a dev server should be listening, it must bind 0.0.0.0 inside the guest; a 127.0.0.1 bind there is unreachable through the forward.\n", pm.Host, pm.Guest)
			continue
		}
		resp.Body.Close()
		fmt.Printf("port %d (guest %d): reachable (%s)\n", pm.Host, pm.Guest, resp.Status)
	}
}

func runtimeNames(rts []Runtime) []string {
	out := make([]string, len(rts))
	for i, rt := range rts {
		out[i] = string(rt)
	}
	return out
}

// isTerminal reports whether f is a character device (an interactive TTY).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
