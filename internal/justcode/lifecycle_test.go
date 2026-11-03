package justcode

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
)

// P06: project-aware lifecycle across runtimes. The acceptance properties
// under test: stopping project A leaves project B intact; missing project
// context never silently becomes stop-all; legacy instances remain
// discoverable; per-instance state (logs, staged files) is separated.

// sealMicrosandbox replaces the real SDK seam on every Microsandbox backend
// with a fake that reports no sandboxes. The lifecycle tests below exercise the
// dispatcher over faked tart/agent-vm backends, but supportedRuntimes() also
// probes the Microsandbox backend, whose SDK adapter opens the runtime's
// private storage catalog. From SDK/runtime 0.7.13 that catalog validates its
// ancestors and rejects a storage root below a shared writable directory such
// as GitHub Actions' /github/home, so without this seam the tests fail on
// CI's HOME rather than on the behaviour they assert.
func sealMicrosandbox(d *Dispatcher) {
	for _, b := range d.backends {
		if m, ok := b.(*MicrosandboxRuntime); ok {
			m.Client = &fakeMSBClient{}
		}
	}
}

func TestStopProjectLeavesOtherProjectIntact(t *testing.T) {
	// Two dispatchers bound to two instances over the same fake runtime
	// surface: stopping one must not stop the other. The platform is pinned
	// so the faked tart/agent-vm backends are probed on every OS (on Windows
	// supportedRuntimes would otherwise skip them entirely).
	withGOOS(t, "linux")
	surface := newFakeInstanceSurface()
	cfg := Config{}

	a := NewDispatcherForInstance(cfg, "jc-a-11111")
	a.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-a-11111"}
	b := NewDispatcherForInstance(cfg, "jc-b-22222")
	b.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-b-22222"}
	sealMicrosandbox(a)
	sealMicrosandbox(b)

	surface.running["opencode-jc-a-11111"] = true
	surface.running["opencode-jc-b-22222"] = true

	if err := a.Stop(context.Background()); err != nil {
		t.Fatalf("Stop A: %v", err)
	}
	if surface.running["opencode-jc-a-11111"] {
		t.Error("project A's instance is still running after Stop")
	}
	if !surface.running["opencode-jc-b-22222"] {
		t.Fatal("project B's instance was stopped by project A's Stop")
	}
}

func TestStopAllStopsEveryInstance(t *testing.T) {
	withGOOS(t, "linux")
	surface := newFakeInstanceSurface()
	d := NewDispatcherForInstance(Config{}, "jc-a-11111")
	d.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-a-11111"}
	d.backends[RuntimeAgentVM] = &fakeInstanceBackend{surface: surface, id: RuntimeAgentVM, instance: "opencode-jc-b-22222"}
	sealMicrosandbox(d)

	surface.running["opencode-jc-a-11111"] = true
	surface.running["opencode-jc-b-22222"] = true

	if err := d.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	for name := range surface.running {
		if surface.running[name] {
			t.Errorf("%s still running after StopAll", name)
		}
	}
}

func TestMissingProjectContextDoesNotStopAll(t *testing.T) {
	// A dispatcher without a project context falls back to the legacy
	// singleton; its Stop must still target only its own instance, not every
	// managed VM on the host.
	withGOOS(t, "linux")
	surface := newFakeInstanceSurface()
	d := NewDispatcher(Config{})
	d.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: DefaultTartVMName()}
	sealMicrosandbox(d)

	surface.running[DefaultTartVMName()] = true
	surface.running["opencode-jc-other-99999"] = true

	if err := d.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if surface.running[DefaultTartVMName()] {
		t.Error("legacy instance still running after Stop")
	}
	if !surface.running["opencode-jc-other-99999"] {
		t.Fatal("unrelated project instance was stopped by a legacy-scoped Stop")
	}
}

func TestTartInstancePathsAreSeparated(t *testing.T) {
	cfg := Config{}
	a := NewTartForInstance(cfg, "jc-a-11111")
	b := NewTartForInstance(cfg, "jc-b-22222")
	if a.LogPath() == b.LogPath() {
		t.Errorf("two projects share a log path: %s", a.LogPath())
	}
	if a.StageDir() == b.StageDir() {
		t.Errorf("two projects share a stage dir: %s", a.StageDir())
	}
	if a.VMName() == b.VMName() {
		t.Errorf("two projects share a VM name: %s", a.VMName())
	}
	if !strings.HasPrefix(a.VMName(), managedVMPrefix) {
		t.Errorf("project VM name %q lacks the managed prefix; sweeps would miss it", a.VMName())
	}
}

func TestTartLegacyKeepsHistoricalPaths(t *testing.T) {
	cfg := Config{}
	setTartVMForTest(t, &cfg)
	tt := NewTart(cfg)
	if tt.VMName() != cfg.TartVM {
		t.Errorf("legacy VMName = %q, want %q", tt.VMName(), cfg.TartVM)
	}
	if tt.LogPath() != TartLogPath(tt.StateDir) {
		t.Errorf("legacy LogPath = %q, want %q", tt.LogPath(), TartLogPath(tt.StateDir))
	}
}

// setTartVMForTest fills cfg.TartVM the way LoadConfigEnv does, without
// touching the environment.
func setTartVMForTest(t *testing.T, cfg *Config) {
	t.Helper()
	cfg.TartImage = DefaultTartImage
	cfg.TartVM = VMName(cfg.TartImage)
}

func TestAgentVMInstancePathsAreSeparated(t *testing.T) {
	a := NewAgentVMForInstance(Config{}, "jc-a-11111")
	b := NewAgentVMForInstance(Config{}, "jc-b-22222")
	if a.LogPath() == b.LogPath() || a.StageDir() == b.StageDir() || a.VMName() == b.VMName() {
		t.Errorf("two agent-vm projects share state: %q / %q / %q", a.VMName(), a.LogPath(), a.StageDir())
	}
}

func TestAgentVMRunningInstancesSkipsTemplate(t *testing.T) {
	// The base template is a Lima instance too; the sweep must never
	// enumerate or stop it.
	a := NewAgentVM(Config{})
	fake := &fakeRunner{}
	fake.onRun = func(name string, args []string) ExecResult {
		if name == "limactl" && len(args) >= 2 && args[0] == "list" {
			return ExecResult{ExitCode: 0, Stdout: "agent-vm-base|Running\nopencode-agent-vm|Running\nopencode-jc-x-12345|Running\n"}
		}
		return ExecResult{ExitCode: 0}
	}
	a.Runner = fake
	instances, err := a.RunningInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range instances {
		if name == "agent-vm-base" {
			t.Fatalf("base template enumerated as a managed instance: %v", instances)
		}
	}
	if len(instances) != 2 {
		t.Errorf("RunningInstances = %v, want the two managed VMs only", instances)
	}
}

func TestManagedVMNameAndOwnership(t *testing.T) {
	name := ManagedVMName("jc-demo-ab12c")
	if !IsManagedVM(name) {
		t.Errorf("IsManagedVM(%q) = false", name)
	}
	if IsManagedVM("agent-vm-base") {
		t.Error("the base template must not be managed")
	}
	if IsManagedVM("some-user-vm") {
		t.Error("a foreign VM must not be managed")
	}
}

func TestMicrosandboxRunningInstancesIncludesLegacySingleton(t *testing.T) {
	// The legacy singleton predates the ownership label, so List cannot see
	// it; RunningInstances must still report it when it is up.
	client := &fakeMSBClient{
		listed:        []string{"jc-a-11111"},
		listedRunning: map[string]bool{"jc-a-11111": true},
	}
	m := NewMicrosandboxRuntimeForInstance(Config{}, "jc-a-11111")
	m.Client = client
	// Make the legacy singleton "exist and running" through the fake's
	// status fields: the fake Lookup answers from status/exists.
	client.exists = true
	client.status = "running"

	names, err := m.RunningInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sawLegacy, sawProject bool
	for _, n := range names {
		if n == msbSandbox {
			sawLegacy = true
		}
		if n == "jc-a-11111" {
			sawProject = true
		}
	}
	if !sawLegacy {
		t.Errorf("legacy singleton missing from RunningInstances: %v", names)
	}
	if !sawProject {
		t.Errorf("project instance missing from RunningInstances: %v", names)
	}
}

func TestMicrosandboxStopInstanceIsScoped(t *testing.T) {
	client := &fakeMSBClient{}
	client.exists = true
	client.status = "running"
	m := NewMicrosandboxRuntimeForInstance(Config{}, "jc-a-11111")
	m.Client = client

	if err := m.StopInstance(context.Background(), "jc-a-11111"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(client, "stop jc-a-11111") {
		t.Fatalf("StopInstance did not target the named instance; calls: %v", client.calls)
	}
}

// DefaultTartVMName returns the legacy Tart VM name for the default image.
func DefaultTartVMName() string {
	return VMName(DefaultTartImage)
}

// fakeInstanceSurface is the shared map of running instance names across the
// fake backends of one test, so cross-project interference is observable.
type fakeInstanceSurface struct {
	mu      sync.Mutex
	running map[string]bool
	stopped []string
}

func newFakeInstanceSurface() *fakeInstanceSurface {
	return &fakeInstanceSurface{running: map[string]bool{}}
}

// fakeInstanceBackend implements Backend over the shared surface. Its
// IsRunning/Stop answer for exactly one instance name.
type fakeInstanceBackend struct {
	surface  *fakeInstanceSurface
	id       Runtime
	instance string
}

func (f *fakeInstanceBackend) ID() Runtime { return f.id }
func (f *fakeInstanceBackend) Start(context.Context) error {
	f.surface.running[f.instance] = true
	return nil
}
func (f *fakeInstanceBackend) Stop(context.Context) error {
	f.surface.mu.Lock()
	defer f.surface.mu.Unlock()
	f.surface.running[f.instance] = false
	f.surface.stopped = append(f.surface.stopped, f.instance)
	return nil
}
func (f *fakeInstanceBackend) Restart(context.Context) error { return nil }
func (f *fakeInstanceBackend) Recreate(context.Context) error {
	f.surface.running[f.instance] = true
	return nil
}
func (f *fakeInstanceBackend) Clean(context.Context) error  { return nil }
func (f *fakeInstanceBackend) Doctor(context.Context) error { return nil }
func (f *fakeInstanceBackend) Logs() error                  { return nil }
func (f *fakeInstanceBackend) Shell() error                 { return nil }
func (f *fakeInstanceBackend) IsRunning(context.Context) (bool, error) {
	f.surface.mu.Lock()
	defer f.surface.mu.Unlock()
	return f.surface.running[f.instance], nil
}
func (f *fakeInstanceBackend) Endpoint(context.Context) (string, error) {
	return "http://localhost:4096", nil
}
func (f *fakeInstanceBackend) RunAgent(context.Context) error { return nil }
func (f *fakeInstanceBackend) Status(context.Context) (string, error) {
	return f.instance + " state", nil
}
func (f *fakeInstanceBackend) StopInstance(_ context.Context, n string) error {
	f.surface.mu.Lock()
	defer f.surface.mu.Unlock()
	f.surface.running[n] = false
	return nil
}
func (f *fakeInstanceBackend) RunningInstances(context.Context) ([]string, error) {
	f.surface.mu.Lock()
	defer f.surface.mu.Unlock()
	var names []string
	for name, up := range f.surface.running {
		if up {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// TestStopAllSweepsOtherProjectsInstances pins the P06 review fix: StopAll
// must enumerate instances globally (RunningInstances/StopInstance), not
// stop only the dispatcher's own project-scoped backends. Here project B's
// instance is running while the dispatcher is bound to project A; the old
// Running()/Stop() implementation left B running.
func TestStopAllSweepsOtherProjectsInstances(t *testing.T) {
	withGOOS(t, "linux")
	surface := newFakeInstanceSurface()
	d := NewDispatcherForInstance(Config{}, "jc-a-11111")
	d.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-a-11111"}
	d.backends[RuntimeAgentVM] = &fakeInstanceBackend{surface: surface, id: RuntimeAgentVM, instance: "opencode-jc-b-22222"}
	sealMicrosandbox(d)

	// Only another project's instance is running; A's own instance is down.
	surface.running["opencode-jc-b-22222"] = true

	if err := d.StopAll(context.Background()); err != nil {
		t.Fatalf("StopAll: %v", err)
	}
	if surface.running["opencode-jc-b-22222"] {
		t.Fatal("StopAll left another project's instance running: Running()/Stop() was used instead of the global sweep")
	}
}

// TestPrepareDetectsConflictsInOtherProjects pins the second P06 review
// fix: Prepare must enumerate running instances globally. The dispatcher is
// bound to project A with no instance of its own running, but project B has
// an instance up on another runtime — that is a conflict the old
// project-scoped Running() could not see.
func TestPrepareDetectsConflictsInOtherProjects(t *testing.T) {
	withGOOS(t, "linux")
	surface := newFakeInstanceSurface()
	d := NewDispatcherForInstance(Config{}, "jc-a-11111")
	d.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-a-11111"}
	d.backends[RuntimeAgentVM] = &fakeInstanceBackend{surface: surface, id: RuntimeAgentVM, instance: "opencode-jc-b-22222"}
	sealMicrosandbox(d)

	surface.running["opencode-jc-b-22222"] = true

	err := d.Prepare(context.Background(), RuntimeTart)
	if err == nil {
		t.Fatal("Prepare missed a conflict running in another project: project-scoped Running() was used instead of the global enumeration")
	}
	if !strings.Contains(err.Error(), "opencode-jc-b-22222") {
		t.Errorf("conflict message should name the running instance, got: %v", err)
	}
}

func TestPrepareDetectsSameRuntimeOtherProject(t *testing.T) {
	withGOOS(t, "linux")
	if isTerminal(os.Stdin) {
		t.Skip("stdin is a TTY; the non-interactive refusal path is not testable here")
	}
	// Observation Codex PR #81 : une instance du MÊME runtime dans un autre
	// projet doit compter comme conflit — l'ancien code sautait tout le
	// runtime demandé et ne la voyait jamais.
	surface := newFakeInstanceSurface()
	d := NewDispatcherForInstance(Config{}, "jc-a-11111")
	d.backends[RuntimeTart] = &fakeInstanceBackend{surface: surface, id: RuntimeTart, instance: "opencode-jc-a-11111"}

	surface.running["opencode-jc-b-22222"] = true // autre projet, même runtime

	err := d.Prepare(context.Background(), RuntimeTart)
	if err == nil || !strings.Contains(err.Error(), "opencode-jc-b-22222") {
		t.Fatalf("expected conflict on the other project's instance, got %v", err)
	}

	// L'instance du projet courant ne doit PAS être un conflit (réutilisation).
	surface.running["opencode-jc-a-11111"] = true
	surface.running["opencode-jc-b-22222"] = false
	if err := d.Prepare(context.Background(), RuntimeTart); err != nil {
		t.Fatalf("own instance must not be a conflict: %v", err)
	}
}
