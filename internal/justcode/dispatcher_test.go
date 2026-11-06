package justcode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withGOOS temporarily points supportedRuntimes at a different platform so the
// dispatcher's platform-gated sweep can be exercised on any host.
func withGOOS(t *testing.T, goos string) {
	t.Helper()
	prev := currentGOOS
	currentGOOS = goos
	t.Cleanup(func() { currentGOOS = prev })
}

type fakeBackend struct {
	id      Runtime
	running bool
	stopped bool
}

func (f *fakeBackend) ID() Runtime                              { return f.id }
func (f *fakeBackend) Start(context.Context) error              { return nil }
func (f *fakeBackend) Stop(context.Context) error               { f.stopped = true; return nil }
func (f *fakeBackend) Restart(context.Context) error            { return nil }
func (f *fakeBackend) Recreate(context.Context) error           { return nil }
func (f *fakeBackend) Clean(context.Context) error              { return nil }
func (f *fakeBackend) Doctor(context.Context) error             { return nil }
func (f *fakeBackend) Logs() error                              { return nil }
func (f *fakeBackend) Shell() error                             { return nil }
func (f *fakeBackend) IsRunning(context.Context) (bool, error)  { return f.running, nil }
func (f *fakeBackend) Endpoint(context.Context) (string, error) { return "http://localhost:4096", nil }
func (f *fakeBackend) RunAgent(context.Context) error           { return nil }
func (f *fakeBackend) Status(context.Context) (string, error)   { return "fake is running", nil }

func TestDispatcherRunningAndMultiple(t *testing.T) {
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart, running: true},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM, running: true},
	})
	running, err := d.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// On Windows only microsandbox is probed, so the other backends are
	// invisible and SingleRunning would succeed. That case is exercised
	// separately below.
	if runtime.GOOS == "windows" {
		if len(running) != 1 || running[0] != RuntimeMicrosandbox {
			t.Fatalf("Running (windows) = %v", running)
		}
		return
	}
	if len(running) != 3 || running[0] != RuntimeMicrosandbox || running[1] != RuntimeTart || running[2] != RuntimeAgentVM {
		t.Fatalf("Running = %v", running)
	}
	if _, err := d.SingleRunning(context.Background()); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("SingleRunning expected multiple error, got %v", err)
	}
}

func TestDispatcherSingle(t *testing.T) {
	// Tart is the "running" backend here only where it can actually run; on
	// Windows the sweep is platform-gated, so the same intent uses microsandbox.
	running := RuntimeTart
	if runtime.GOOS == "windows" {
		running = RuntimeMicrosandbox
	}
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: running == RuntimeMicrosandbox},
		RuntimeTart:         &fakeBackend{id: RuntimeTart, running: running == RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	rt, err := d.SingleRunning(context.Background())
	if err != nil || rt != running {
		t.Fatalf("SingleRunning = %v, %v", rt, err)
	}
}

// TestDispatcherSingleOnWindows exercises the platform-gated sweep without
// rebuilding for Windows: with only microsandbox running and tart also present
// in the map, the Windows sweep must report exactly one runtime.
func TestDispatcherSingleOnWindows(t *testing.T) {
	withGOOS(t, "windows")
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart, running: true},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM, running: true},
	})
	running, err := d.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 1 || running[0] != RuntimeMicrosandbox {
		t.Fatalf("Running (windows) = %v, want [microsandbox]", running)
	}
	rt, err := d.SingleRunning(context.Background())
	if err != nil || rt != RuntimeMicrosandbox {
		t.Fatalf("SingleRunning (windows) = %v, %v", rt, err)
	}
}

func TestDispatcherStopAll(t *testing.T) {
	// The set of runtimes actually swept is platform-gated: on Windows tart is
	// invisible to stop, so it must not be marked stopped there.
	msb := &fakeBackend{id: RuntimeMicrosandbox, running: true}
	tart := &fakeBackend{id: RuntimeTart, running: true}
	avm := &fakeBackend{id: RuntimeAgentVM, running: true}
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: msb,
		RuntimeTart:         tart,
		RuntimeAgentVM:      avm,
	})
	if err := d.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if !msb.stopped || tart.stopped || avm.stopped {
			t.Fatalf("windows: msb=%v tart=%v avm=%v (want msb stopped, others untouched)", msb.stopped, tart.stopped, avm.stopped)
		}
		return
	}
	if !msb.stopped || !tart.stopped || !avm.stopped {
		t.Fatalf("expected all running backends to be stopped")
	}
}

func TestDispatcherPrepareNoConflict(t *testing.T) {
	// Use the running backend that is actually visible to the sweep: on Windows
	// tart is not probed, so the "no conflict" intent is exercised with msb.
	running := RuntimeTart
	if runtime.GOOS == "windows" {
		running = RuntimeMicrosandbox
	}
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: running == RuntimeMicrosandbox},
		RuntimeTart:         &fakeBackend{id: RuntimeTart, running: running == RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	if err := d.Prepare(context.Background(), running); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestDispatcherPrepareRefusesConflict(t *testing.T) {
	if isTerminal(os.Stdin) {
		t.Skip("stdin is a TTY; the non-interactive refusal path is not testable here")
	}
	// On Windows tart is invisible to the sweep, so there is no conflict to
	// refuse; that platform-gated behavior is covered by the windows-specific
	// test above. Here we want the conflict path, which only exists off Windows.
	if runtime.GOOS == "windows" {
		t.Skip("windows sweep only probes microsandbox; covered by TestDispatcherSingleOnWindows")
	}
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	err := d.Prepare(context.Background(), RuntimeTart)
	if err == nil || !strings.Contains(err.Error(), "déjà en cours d'exécution") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestDispatcherCheckBackendModeProbesHealth(t *testing.T) {
	// The backend-mode check hits the endpoint; the fake's Endpoint is used,
	// and CheckBackend fails against a non-listening localhost, which is the
	// observable difference from the full-mode branch.
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	if err := d.Check(context.Background(), nil); err == nil {
		t.Fatal("backend-mode check must probe the health endpoint (unreachable here)")
	}
}

func TestDispatcherCheckFullModeReportsVMState(t *testing.T) {
	d := NewDispatcherWith(Config{Isolation: IsolationFull}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	if err := d.Check(context.Background(), nil); err != nil {
		t.Fatalf("full-mode check must not probe any endpoint: %v", err)
	}
}

// freeLocalPort returns a loopback port with nothing listening on it: a
// listener is bound to grab a port and closed again, which is race-free for a
// listen socket that never accepted a connection.
func freeLocalPort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return uint16(port)
}

func TestDispatcherCheckReportsConfiguredPortReachability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	live, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	dead := freeLocalPort(t)
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: uint16(live), Guest: 8080}, {Host: dead, Guest: 8081}},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatalf("a dead preview port must not fail check: %v", checkErr)
	}
	reachable := "port " + strconv.Itoa(live) + " (guest 8080): reachable (200 OK)"
	if !strings.Contains(out, reachable) {
		t.Fatalf("reachable port was not reported: %q", out)
	}
	unreachable := "port " + strconv.Itoa(int(dead)) + " (guest 8081): no answer from the host forward"
	if !strings.Contains(out, unreachable) || !strings.Contains(out, "must bind 0.0.0.0 inside the guest") {
		t.Fatalf("unreachable port hint was not reported: %q", out)
	}
}

func TestDispatcherCheckProbesPortsEvenWhenHealthFails(t *testing.T) {
	// Backend mode with the fake's dead health endpoint: the probe results
	// must still print, because preview reachability does not depend on the
	// OpenCode backend being healthy.
	dead := freeLocalPort(t)
	cfg := Config{
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: dead, Guest: 3000}},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	_ = checkErr // the health result is owned by CheckBackend's own tests
	if !strings.Contains(out, "no answer from the host forward") {
		t.Fatalf("port probe output missing while the health endpoint was down: %q", out)
	}
}

func TestDispatcherCheckDoesNotProbePortsOnOtherRuntimes(t *testing.T) {
	// The configured port mappings describe Microsandbox host-loopback
	// forwards. On Tart the agent connects to the VM IP directly and on
	// agent-vm forwarding is dynamic, so a localhost probe there would hit
	// an unrelated service and report a forward that does not exist.
	// Tart is macOS-only, so the sweep is pointed at darwin to keep the
	// tart path under test on every host (including the Windows CI leg).
	withGOOS(t, "darwin")
	dead := freeLocalPort(t)
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: dead, Guest: 8080}},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox},
		RuntimeTart:         &fakeBackend{id: RuntimeTart, running: true},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatalf("tart check failed: %v", checkErr)
	}
	if strings.Contains(out, "host forward") || strings.Contains(out, "reachable (") {
		t.Fatalf("port probe must not run on tart: %q", out)
	}
}

type fakeAppliedPortsBackend struct {
	fakeBackend
	applied    []PortMapping
	appliedErr error
}

func (f *fakeAppliedPortsBackend) AppliedPortMappings(context.Context) ([]PortMapping, error) {
	return f.applied, f.appliedErr
}

func TestDispatcherCheckProbesOnlyAppliedPortMappings(t *testing.T) {
	// Manifest changes apply on recreate: a mapping configured after the
	// sandbox was created has no forward, so an unrelated listener on that
	// host port must not be reported as a reachable preview.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer live.Close()
	livePort, err := strconv.Atoi(strings.TrimPrefix(live.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer unrelated.Close()
	unrelatedPort, err := strconv.Atoi(strings.TrimPrefix(unrelated.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	dead := freeLocalPort(t)
	stale := freeLocalPort(t)
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports: []PortMapping{
			{Host: uint16(livePort), Guest: 8080},
			{Host: uint16(unrelatedPort), Guest: 8081},
			{Host: dead, Guest: 8082},
		},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeAppliedPortsBackend{
			fakeBackend: fakeBackend{id: RuntimeMicrosandbox, running: true},
			applied: []PortMapping{
				{Host: uint16(livePort), Guest: 8080},
				{Host: dead, Guest: 8082},
				{Host: stale, Guest: 3000},
				{Host: uint16(DefaultPort), Guest: uint16(DefaultPort)},
			},
		},
		RuntimeTart:    &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM: &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	if got := "port " + strconv.Itoa(livePort) + " (guest 8080): reachable (200 OK)"; !strings.Contains(out, got) {
		t.Fatalf("applied mapping was not probed: %q", out)
	}
	if got := "port " + strconv.Itoa(int(dead)) + " (guest 8082): no answer from the host forward"; !strings.Contains(out, got) {
		t.Fatalf("applied dead mapping was not probed: %q", out)
	}
	if got := "port " + strconv.Itoa(unrelatedPort) + " (guest 8081): configured but not forwarded by the running sandbox; run 'just-code recreate --microsandbox' to apply."; !strings.Contains(out, got) {
		t.Fatalf("pending mapping was not reported as pending: %q", out)
	}
	if strings.Contains(out, "port "+strconv.Itoa(unrelatedPort)+" (guest 8081): reachable") {
		t.Fatalf("a mapping without a forward must not be reported reachable: %q", out)
	}
	if got := "port " + strconv.Itoa(int(stale)) + " (guest 3000): still forwarded until the sandbox is recreated"; !strings.Contains(out, got) {
		t.Fatalf("stale applied mapping was not reported: %q", out)
	}
	// Match the reported line shape, not the bare digits: an ephemeral port
	// such as 40967 contains "4096" and made this assertion fail at random.
	if strings.Contains(out, fmt.Sprintf("port %d (guest %d):", DefaultPort, DefaultPort)) {
		t.Fatalf("the OpenCode server forward must not be reported as a preview mapping: %q", out)
	}
}

func TestDispatcherCheckPortProbeBudgetBoundsStalledChecks(t *testing.T) {
	// A forward that accepts but never answers costs its full probe
	// timeout; without a shared budget, many such mappings stall the check
	// for minutes. The budget is a seam so the test runs fast.
	oldBudget := portProbeBudget
	portProbeBudget = 150 * time.Millisecond
	t.Cleanup(func() { portProbeBudget = oldBudget })
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hang.Close()
	hangPort, err := strconv.Atoi(strings.TrimPrefix(hang.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	mappings := []PortMapping{
		{Host: uint16(hangPort), Guest: 8080},
		{Host: uint16(hangPort), Guest: 8081},
		{Host: uint16(hangPort), Guest: 8082},
	}
	cfg := Config{Isolation: IsolationFull, PortsConfigured: true, Ports: mappings}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeAppliedPortsBackend{
			fakeBackend: fakeBackend{id: RuntimeMicrosandbox, running: true},
			applied:     mappings,
		},
		RuntimeTart:    &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM: &fakeBackend{id: RuntimeAgentVM},
	})
	start := time.Now()
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("three stalled probes took %s; the budget did not bind", elapsed)
	}
	if got := fmt.Sprintf("port %d (guest 8080): no answer from the host forward", hangPort); !strings.Contains(out, got) {
		t.Fatalf("first stalled mapping was not probed: %q", out)
	}
	if got := fmt.Sprintf("port %d (guest 8082): not probed; the port-probe budget", hangPort); !strings.Contains(out, got) {
		t.Fatalf("mapping beyond the budget was not reported as not probed: %q", out)
	}
}

func TestDispatcherCheckSkipsPortProbesWhenAppliedMappingsUnreadable(t *testing.T) {
	// When the applied mappings cannot be read, probing the manifest view
	// would report services the sandbox does not forward: fail closed.
	dead := freeLocalPort(t)
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: dead, Guest: 3000}},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeAppliedPortsBackend{
			fakeBackend: fakeBackend{id: RuntimeMicrosandbox, running: true},
			appliedErr:  errors.New("sandbox api unavailable"),
		},
		RuntimeTart:    &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM: &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	if !strings.Contains(out, "could not read the running sandbox's applied mappings") {
		t.Fatalf("missing applied-mappings failure note: %q", out)
	}
	if strings.Contains(out, "no answer from the host forward") {
		t.Fatalf("ports must not be probed while applied mappings are unreadable: %q", out)
	}
}

func TestDispatcherCheckReportsStaleForwardAfterLastMappingRemoved(t *testing.T) {
	// `ports remove` of the final entry persists portsConfigured with an
	// empty list: the sandbox keeps forwarding the removed mapping until
	// recreate, and check must still report it rather than going silent.
	stale := freeLocalPort(t)
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports:           nil,
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeAppliedPortsBackend{
			fakeBackend: fakeBackend{id: RuntimeMicrosandbox, running: true},
			applied: []PortMapping{
				{Host: stale, Guest: 3000},
				{Host: uint16(DefaultPort), Guest: uint16(DefaultPort)},
			},
		},
		RuntimeTart:    &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM: &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	if got := "port " + strconv.Itoa(int(stale)) + " (guest 3000): still forwarded until the sandbox is recreated"; !strings.Contains(out, got) {
		t.Fatalf("stale forward after the last mapping was removed was not reported: %q", out)
	}
	// See the note in TestDispatcherCheckProbesOnlyAppliedPortMappings: the
	// bare digits match unrelated ephemeral ports by coincidence.
	if strings.Contains(out, fmt.Sprintf("port %d (guest %d):", DefaultPort, DefaultPort)) {
		t.Fatalf("the OpenCode server forward must not be reported as a preview mapping: %q", out)
	}
}

func TestDispatcherCheckTreatsRedirectAnswerAsReachable(t *testing.T) {
	// A forward that answers with a redirect is reachable; following the
	// redirect would turn a diagnostic probe into a request to an arbitrary
	// target, and a dead target would hide a forward that answered.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/unreachable", http.StatusFound)
	}))
	defer srv.Close()
	live, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Isolation:       IsolationFull,
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: uint16(live), Guest: 8080}},
	}
	d := NewDispatcherWith(cfg, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatalf("a redirecting preview port must not fail check: %v", checkErr)
	}
	if !strings.Contains(out, "port "+strconv.Itoa(live)+" (guest 8080): reachable (302 Found)") {
		t.Fatalf("redirect answer was not reported as reachable: %q", out)
	}
}

func TestDispatcherCheckDoesNotProbeLegacyPortRange(t *testing.T) {
	// Without configured ports the implicit 3000-3010 forwards exist, but
	// check must stay quiet about them: they are defaults, not choices.
	d := NewDispatcherWith(Config{Isolation: IsolationFull}, map[Runtime]Backend{
		RuntimeMicrosandbox: &fakeBackend{id: RuntimeMicrosandbox, running: true},
		RuntimeTart:         &fakeBackend{id: RuntimeTart},
		RuntimeAgentVM:      &fakeBackend{id: RuntimeAgentVM},
	})
	var out string
	var checkErr error
	out = captureStdout(t, func() {
		checkErr = d.Check(context.Background(), nil)
	})
	if checkErr != nil {
		t.Fatalf("check without configured ports failed: %v", checkErr)
	}
	if strings.Contains(out, "host forward") || strings.Contains(out, "reachable (") {
		t.Fatalf("legacy port range was probed: %q", out)
	}
}
