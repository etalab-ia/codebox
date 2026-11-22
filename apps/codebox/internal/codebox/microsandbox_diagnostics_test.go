package codebox

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMicrosandboxLogsReadGuestPrepLog(t *testing.T) {
	client := &fakeMSBClient{execCaptureDefault: &fakeMSBExecCaptureResult{
		stdout: "codebox : installation des paquets système avec apk…\nGet:1 package download\n",
	}}
	m := newTestMicrosandbox(t, client)
	var err error
	output := captureStdout(t, func() { err = m.Logs(context.Background(), false) })
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(output, "installation des paquets système") || !strings.Contains(output, "package download") {
		t.Fatalf("piped diagnostics must contain the guest prep stages and downloads, got %q", output)
	}
	if !hasCall(client, "execcapture "+msbSandbox+" cat "+msbGuestLog) {
		t.Fatalf("Logs did not read the guest prep file: %v", client.calls)
	}
	if hasCall(client, "attach "+msbSandbox) {
		t.Fatalf("finite Logs must return rather than attach a follower: %v", client.calls)
	}
}

func TestMicrosandboxPiLogsUsePiPath(t *testing.T) {
	client := &fakeMSBClient{execCaptureDefault: &fakeMSBExecCaptureResult{stdout: "codebox : écriture de la configuration Pi…\n"}}
	m := newTestMicrosandbox(t, client)
	m.cfg.Harness = HarnessPi
	var err error
	output := captureStdout(t, func() { err = m.Logs(context.Background(), false) })
	if err != nil || !strings.Contains(output, "configuration Pi") {
		t.Fatalf("Pi Logs output = %q, err = %v", output, err)
	}
	calls := strings.Join(client.calls, "\n")
	if !hasCall(client, "execcapture "+msbSandbox) || !strings.Contains(calls, "cat "+msbPiGuestLog) || !strings.Contains(calls, "cat "+msbGuestLog) {
		t.Fatalf("Pi Logs must read Pi's prep file with the legacy fallback: %v", client.calls)
	}
}

func TestMicrosandboxLogsFollowAttachesTail(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	if err := m.Logs(context.Background(), true); err != nil {
		t.Fatalf("Logs --follow: %v", err)
	}
	if !hasCall(client, "attach "+msbSandbox+" tail -f "+msbGuestLog+" /") {
		t.Fatalf("follow mode did not tail the guest prep log: %v", client.calls)
	}
	if hasCall(client, "execcapture "+msbSandbox+" cat ") {
		t.Fatalf("follow mode unexpectedly performed a finite read: %v", client.calls)
	}
}

func TestMicrosandboxPiLogsFollowIncludesLegacyFallback(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	m.cfg.Harness = HarnessPi
	if err := m.Logs(context.Background(), true); err != nil {
		t.Fatalf("Pi Logs --follow: %v", err)
	}
	calls := strings.Join(client.calls, "\n")
	if !hasCall(client, "attach "+msbSandbox+" sh -c ") || !strings.Contains(calls, "tail -f "+msbPiGuestLog) || !strings.Contains(calls, "tail -f "+msbGuestLog) {
		t.Fatalf("Pi follow mode lacks the current and legacy log paths: %v", client.calls)
	}
}

func TestMicrosandboxLogsFollowTreatsInterruptAsCleanExit(t *testing.T) {
	cases := []struct {
		name string
		code int
		err  error
	}{
		{name: "signaled -1", code: -1},
		{name: "sigint 130", code: 130},
		{name: "context canceled", err: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeMSBClient{attachCode: tc.code, attachErr: tc.err}
			m := newTestMicrosandbox(t, client)
			if err := m.Logs(context.Background(), true); err != nil {
				t.Fatalf("Logs --follow interruption (%s) should return nil, got %v", tc.name, err)
			}
		})
	}
}

func TestMicrosandboxLogsFollowPreservesRealFailure(t *testing.T) {
	client := &fakeMSBClient{attachCode: 1}
	m := newTestMicrosandbox(t, client)
	err := m.Logs(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "s'est terminé avec le code 1") {
		t.Fatalf("Logs --follow exit 1 = %v, want error reporting code 1", err)
	}
}

func TestPiReadinessGuidanceUsesPiLog(t *testing.T) {
	client := &fakeMSBClient{execDefault: &fakeMSBExecResult{code: msbPrepareProgress, stderr: "100:1"}}
	clock := &fakeToolchainClock{now: time.Unix(0, 0)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Harness = HarnessPi
	m.cfg.StartTimeout = 50 * time.Millisecond
	m.toolchainPollDelay = 20 * time.Millisecond
	m.toolchainHardCap = 500 * time.Millisecond
	m.toolchainClock = clock

	err := m.waitForToolchain(context.Background())
	if err == nil || !strings.Contains(err.Error(), msbPiGuestLog) || strings.Contains(err.Error(), msbGuestLog) {
		t.Fatalf("Pi readiness error = %v, want only Pi's log path", err)
	}
}

func TestPiPrepareProbeUsesPiLogAndLegacyFallback(t *testing.T) {
	probe := msbGuestPrepareProbeFor(HarnessPi)
	if !strings.Contains(probe, msbPiGuestLog) || !strings.Contains(probe, msbGuestLog) {
		t.Fatalf("Pi readiness must follow Pi's prep log and the legacy fallback: %s", probe)
	}
}

func assertTailLogModes(t *testing.T, backend Backend, path string, setInteractive func(func(string, ...string) error)) {
	t.Helper()
	for _, follow := range []bool{false, true} {
		var gotName string
		var gotArgs []string
		setInteractive(func(name string, args ...string) error {
			gotName = name
			gotArgs = append([]string(nil), args...)
			return nil
		})
		if err := backend.Logs(context.Background(), follow); err != nil {
			t.Fatalf("Logs(follow=%v): %v", follow, err)
		}
		wantArgs := []string{path}
		if follow {
			wantArgs = []string{"-f", path}
		}
		if gotName != "tail" || !slices.Equal(gotArgs, wantArgs) {
			t.Errorf("Logs(follow=%v) = %s %v, want tail %v", follow, gotName, gotArgs, wantArgs)
		}
	}
}

func TestTartLogsTerminateByDefaultAndFollowOnRequest(t *testing.T) {
	backend := NewTart(Config{})
	backend.StateDir = t.TempDir()
	assertTailLogModes(t, backend, backend.LogPath(), func(fn func(string, ...string) error) {
		backend.Interactive = fn
	})
}

func TestAgentVMLogsTerminateByDefaultAndFollowOnRequest(t *testing.T) {
	backend := NewAgentVM(Config{})
	backend.StateDir = t.TempDir()
	assertTailLogModes(t, backend, backend.LogPath(), func(fn func(string, ...string) error) {
		backend.Interactive = fn
	})
}
