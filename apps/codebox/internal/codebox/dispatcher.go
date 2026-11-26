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

// SetProjectSecretApprovalGate sets a launch gate for new Microsandbox
// instances. Existing instances bypass the gate so revocation cleanup can run.
func (d *Dispatcher) SetProjectSecretApprovalGate(gate func() error) {
	if m, ok := d.backends[RuntimeMicrosandbox].(*MicrosandboxRuntime); ok {
		m.ProjectSecretApprovalGate = gate
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
		return fmt.Errorf("%w\n  Lancez 'codebox logs' pour inspecter le backend", err)
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
		return nil, fmt.Errorf("runtime inconnu %q", rt)
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
		return "", fmt.Errorf("plusieurs runtimes codebox sont en cours d'exécution ; lancez d'abord `codebox stop`")
	}
}

// instanceBackend is the per-instance surface a backend exposes for the
// global sweeps (StopAll, Prepare conflict detection). It is implemented by
// the tart, agent-vm, and microsandbox backends.
type instanceBackend interface {
	RunningInstances(ctx context.Context) ([]string, error)
	StopInstance(ctx context.Context, name string) error
}

// instancePortsBackend is implemented by backends whose managed instances can
// report their bound host ports by instance name (Microsandbox).
type instancePortsBackend interface {
	InstancePortMappings(ctx context.Context, name string) ([]PortMapping, error)
}

type instanceConflict struct {
	name    string
	runtime Runtime
	backend instanceBackend
	ports   []uint16
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

// Prepare resolves real conflicts before starting or restarting a runtime: it
// migrates a host still carrying the removed Docker runtime's container, then
// checks for instances that genuinely cannot coexist with this launch — either
// the same project running on a different runtime, or another project's
// running instance whose host-loopback port bindings overlap with this
// project's effective ports (persisted ports for an existing sandbox, or
// desired ports for a new sandbox) (#519). Independent projects with disjoint
// host ports are allowed to run concurrently without stopping each other.
func (d *Dispatcher) Prepare(ctx context.Context, requested Runtime) error {
	return d.prepare(ctx, requested, false)
}

// PrepareForRecreate resolves conflicts before an explicit recreate, where the
// recreated instance will bind the manifest's desired host ports rather than
// the old instance's persisted ports (#519).
func (d *Dispatcher) PrepareForRecreate(ctx context.Context, requested Runtime) error {
	return d.prepare(ctx, requested, true)
}

func (d *Dispatcher) prepare(ctx context.Context, requested Runtime, recreating bool) error {
	if err := d.clearLegacyDocker(ctx); err != nil {
		return err
	}
	conflicts, err := d.otherRunningInstances(ctx, requested, recreating)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}

	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].name < conflicts[j].name })
	namesList := make([]string, len(conflicts))
	portSet := make(map[uint16]bool)
	for i, c := range conflicts {
		namesList[i] = c.name
		for _, p := range c.ports {
			portSet[p] = true
		}
	}
	names := strings.Join(namesList, " ")
	portSuffix := ""
	portHint := ""
	if len(portSet) > 0 {
		ports := make([]uint16, 0, len(portSet))
		for p := range portSet {
			ports = append(ports, p)
		}
		portSuffix = fmt.Sprintf(" (ports hôte en conflit : %s)", formatPortList(ports))
		portHint = " Configurez des ports distincts avec 'codebox ports' ou lancez `codebox stop` avant de démarrer %s."
	}
	if !isTerminal(os.Stdin) {
		if portHint != "" {
			return fmt.Errorf("%s est déjà en cours d'exécution%s."+portHint, names, portSuffix, requested)
		}
		return fmt.Errorf("%s est déjà en cours d'exécution. Lancez `codebox stop` avant de démarrer %s.", names, requested)
	}
	fmt.Printf("%s est déjà en cours d'exécution%s. L'arrêter pour démarrer %s ? [o/N] ", names, portSuffix, requested)
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(reply)) {
	case "o", "oui", "y", "yes":
		for _, c := range conflicts {
			if c.backend != nil {
				if err := c.backend.StopInstance(ctx, c.name); err != nil {
					return err
				}
				continue
			}
			if b, ok := d.backends[c.runtime]; ok {
				if err := b.Stop(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("%s reste en cours d'exécution", names)
	}
}

// otherRunningInstances returns running managed instances that genuinely
// conflict with starting requested for the current project:
//   - the current project's own instance on a different runtime;
//   - any other project's instance whose host-loopback port bindings overlap
//     with the host ports requested will bind (or whose ports cannot be read
//     when requested needs host ports).
func (d *Dispatcher) otherRunningInstances(ctx context.Context, requested Runtime, recreating bool) ([]instanceConflict, error) {
	ownRequested := d.ownInstanceName(requested)
	wantPorts := d.launchHostPorts(ctx, requested, ownRequested, recreating)
	var conflicts []instanceConflict
	for _, rt := range supportedRuntimes() {
		b := d.backends[rt]
		ib, ok := b.(instanceBackend)
		if !ok {
			// Backend without per-instance enumeration (none today outside
			// tests): its project-scoped IsRunning reports this project's own
			// instance on that runtime.
			if rt == requested {
				continue
			}
			running, err := b.IsRunning(ctx)
			if err != nil {
				return nil, err
			}
			if running {
				conflicts = append(conflicts, instanceConflict{name: string(rt), runtime: rt})
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
		ownOnRT := d.ownInstanceName(rt)
		for _, name := range instances {
			if rt == requested && name == ownRequested {
				// The current project's own instance of the requested
				// runtime: not a conflict, Prepare reuses it.
				continue
			}
			if rt != requested && ownOnRT != "" && name == ownOnRT {
				// Same project running on a different runtime.
				conflicts = append(conflicts, instanceConflict{name: name, runtime: rt, backend: ib})
				continue
			}
			if len(wantPorts) == 0 {
				// Starting requested binds no host loopback ports, so another
				// project's instance cannot collide with it.
				continue
			}
			overlap, conflict := d.hostPortConflict(ctx, rt, b, name, wantPorts)
			if conflict {
				conflicts = append(conflicts, instanceConflict{name: name, runtime: rt, backend: ib, ports: overlap})
			}
		}
	}
	return conflicts, nil
}

// launchHostPorts returns the host loopback TCP ports that this launch will
// actually bind. For a non-recreating Microsandbox start/restart of an
// existing instance, the sandbox's persisted port mappings are authoritative
// because restart boots the existing configuration without reapplying manifest
// port edits; for a recreate or a not-yet-created instance, desiredHostPorts
// applies.
func (d *Dispatcher) launchHostPorts(ctx context.Context, requested Runtime, own string, recreating bool) map[uint16]bool {
	if !recreating && requested == RuntimeMicrosandbox && own != "" {
		if ipb, ok := d.backends[requested].(instancePortsBackend); ok {
			if applied, err := ipb.InstancePortMappings(ctx, own); err == nil {
				ports := make(map[uint16]bool, len(applied))
				for _, pm := range applied {
					if pm.Host > 0 {
						ports[pm.Host] = true
					}
				}
				return ports
			}
		}
	}
	return d.desiredHostPorts(requested)
}

// desiredHostPorts returns the host loopback TCP ports that creating requested
// from the current manifest configuration will bind.
func (d *Dispatcher) desiredHostPorts(requested Runtime) map[uint16]bool {
	ports := make(map[uint16]bool)
	switch requested {
	case RuntimeMicrosandbox:
		if d.cfg.Isolation == IsolationBackend {
			ports[uint16(DefaultPort)] = true
		}
		if !d.cfg.PortsConfigured {
			for p := uint16(3000); p <= 3010; p++ {
				ports[p] = true
			}
		} else {
			for _, pm := range d.cfg.Ports {
				if pm.Host > 0 {
					ports[pm.Host] = true
				}
			}
		}
	case RuntimeAgentVM:
		if d.cfg.Isolation == IsolationBackend {
			ports[uint16(DefaultPort)] = true
		}
	case RuntimeTart:
		// Tart VMs use their own NAT IP rather than static host loopback binds.
	}
	return ports
}

// hostPortConflict reports whether another project's running instance (rt, name)
// collides with any host loopback port in wantPorts.
func (d *Dispatcher) hostPortConflict(ctx context.Context, rt Runtime, b Backend, name string, wantPorts map[uint16]bool) ([]uint16, bool) {
	switch rt {
	case RuntimeTart:
		return nil, false
	case RuntimeAgentVM:
		if !wantPorts[uint16(DefaultPort)] {
			return nil, false
		}
		ipb, ok := b.(instancePortsBackend)
		if !ok {
			// Cannot inspect whether the running AgentVM serves 4096; fail closed.
			return []uint16{uint16(DefaultPort)}, true
		}
		applied, err := ipb.InstancePortMappings(ctx, name)
		if err != nil {
			// Fail closed when the AgentVM's backend state cannot be probed.
			return []uint16{uint16(DefaultPort)}, true
		}
		for _, pm := range applied {
			if wantPorts[pm.Host] {
				return []uint16{pm.Host}, true
			}
		}
		return nil, false
	case RuntimeMicrosandbox:
		ipb, ok := b.(instancePortsBackend)
		if !ok {
			// Cannot inspect ports on a fake backend; fail closed because
			// wantPorts is non-empty.
			return nil, true
		}
		applied, err := ipb.InstancePortMappings(ctx, name)
		if err != nil {
			// Fail closed when the running instance's applied ports cannot be
			// verified and the new launch binds host ports.
			return nil, true
		}
		var overlap []uint16
		for _, pm := range applied {
			if wantPorts[pm.Host] {
				overlap = append(overlap, pm.Host)
			}
		}
		if len(overlap) > 0 {
			return overlap, true
		}
		return nil, false
	default:
		return nil, false
	}
}

// formatPortList renders sorted unique ports, collapsing runs of 3+ contiguous
// ports into start-end ranges (e.g. "3000-3010, 4096").
func formatPortList(ports []uint16) string {
	if len(ports) == 0 {
		return ""
	}
	sorted := append([]uint16(nil), ports...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	uniq := sorted[:0]
	for _, p := range sorted {
		if len(uniq) == 0 || uniq[len(uniq)-1] != p {
			uniq = append(uniq, p)
		}
	}
	var parts []string
	for i := 0; i < len(uniq); {
		j := i
		for j+1 < len(uniq) && uniq[j+1] == uniq[j]+1 {
			j++
		}
		switch {
		case j-i >= 2:
			parts = append(parts, fmt.Sprintf("%d-%d", uniq[i], uniq[j]))
		case j-i == 1:
			parts = append(parts, fmt.Sprintf("%d", uniq[i]), fmt.Sprintf("%d", uniq[j]))
		default:
			parts = append(parts, fmt.Sprintf("%d", uniq[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
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
	case interface{ InstanceName() string }:
		return b.InstanceName()
	}
	return ""
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
		fmt.Printf("isolation complète : la session %s s'exécute de façon interactive dans la machine isolée (aucun point de terminaison de santé).\n", d.cfg.EffectiveHarness().DisplayName())
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
		fmt.Printf("accessibilité des ports : impossible de lire les mappages appliqués de la machine isolée en cours d'exécution (%v) ; les ports configurés ne sont pas testés.\n", err)
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
		fmt.Printf("port %d (machine isolée %d) : configuré mais non transmis par la machine isolée en cours d'exécution ; lancez 'codebox recreate --microsandbox' pour l'appliquer.\n", pm.Host, pm.Guest)
	}
	for _, pm := range applied {
		// The OpenCode backend's own server forward is not a user
		// preview mapping; ValidatePortMappings keeps DefaultPort out
		// of configured mappings entirely.
		if pm.Host == DefaultPort && pm.Guest == DefaultPort {
			continue
		}
		if !desiredSet[pm] {
			fmt.Printf("port %d (machine isolée %d) : encore transmis jusqu'à la recréation de la machine isolée (retiré des mappages de ports du projet).\n", pm.Host, pm.Guest)
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
			fmt.Printf("port %d (machine isolée %d) : non testé ; le budget de test des ports (%s) a été consommé par les mappages précédents.\n", pm.Host, pm.Guest, portProbeBudget)
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
			fmt.Printf("port %d (machine isolée %d) : impossible de construire la sonde : %v\n", pm.Host, pm.Guest, err)
			continue
		}
		resp, err := client.Do(req)
		cancel()
		if err != nil {
			fmt.Printf("port %d (machine isolée %d) : aucune réponse de la redirection du poste. Si un serveur de développement devrait écouter, il doit se lier à 0.0.0.0 dans la machine isolée ; une liaison à 127.0.0.1 y est injoignable via la redirection.\n", pm.Host, pm.Guest)
			continue
		}
		resp.Body.Close()
		fmt.Printf("port %d (machine isolée %d) : joignable (%s)\n", pm.Host, pm.Guest, resp.Status)
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
