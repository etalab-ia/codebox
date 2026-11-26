package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type launchReconciler struct {
	codebox.Backend
	reconcile func() error
	endpoint  string
}

func (b launchReconciler) Reconcile(context.Context) error          { return b.reconcile() }
func (b launchReconciler) Endpoint(context.Context) (string, error) { return b.endpoint, nil }

type lifecycleLaunchBackend struct {
	codebox.Backend
	endpoint   string
	operations int
}

func (b *lifecycleLaunchBackend) IsRunning(context.Context) (bool, error)  { return false, nil }
func (b *lifecycleLaunchBackend) Endpoint(context.Context) (string, error) { return b.endpoint, nil }
func (b *lifecycleLaunchBackend) Restart(context.Context) error            { b.operations++; return nil }
func (b *lifecycleLaunchBackend) Recreate(context.Context) error           { b.operations++; return nil }

type noLegacyLaunchRunner struct{ codebox.Runner }

func (noLegacyLaunchRunner) Run(context.Context, string, ...string) (codebox.ExecResult, error) {
	return codebox.ExecResult{ExitCode: 1}, nil
}

func TestLifecycleLaunchWaitsForBackendReadiness(t *testing.T) {
	oldConfirm := confirmRecreateFn
	confirmRecreateFn = func(codebox.Runtime) (int, error) { return 0, nil }
	t.Cleanup(func() { confirmRecreateFn = oldConfirm })
	for _, action := range []string{"restart", "recreate"} {
		for _, healthy := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%v", action, healthy), func(t *testing.T) {
				var probes atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					probes.Add(1)
					if !healthy {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					fmt.Fprint(w, `{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`)
				}))
				defer srv.Close()
				b := &lifecycleLaunchBackend{endpoint: srv.URL}
				d := codebox.NewDispatcherWith(codebox.Config{Isolation: codebox.IsolationBackend, StartTimeout: time.Nanosecond}, map[codebox.Runtime]codebox.Backend{
					codebox.RuntimeMicrosandbox: b, codebox.RuntimeTart: b, codebox.RuntimeAgentVM: b,
				})
				d.Runner = noLegacyLaunchRunner{}
				d.Launch = &codebox.LaunchUX{Terminal: true}
				var err error
				output := captureStderr(t, func() {
					_, err = lifecycleCmd(d, codebox.RuntimeMicrosandbox, action, false)
				})
				if b.operations != 1 {
					t.Fatalf("runtime operation count = %d", b.operations)
				}
				if probes.Load() == 0 {
					t.Fatal("readiness was never checked")
				}
				if (err == nil) != healthy {
					t.Fatalf("readiness error: %v", err)
				}
				if strings.Contains(output, "Agent prêt") != healthy {
					t.Fatalf("incorrect readiness checklist: %q", output)
				}
			})
		}
	}
}

func TestLaunchTimingWaitsForBackendReadiness(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		t.Run(fmt.Sprint(healthy), func(t *testing.T) {
			var elapsed atomic.Int64
			oldNow := launchNowFn
			launchNowFn = func() time.Time { return time.Unix(100+elapsed.Load(), 0) }
			t.Cleanup(func() { launchNowFn = oldNow })
			var probes atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				elapsed.Add(140)
				if !healthy {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, `{"version":"2.2.5","pid":1,"urls":[],"paths":{"tmp":"/tmp/opencode"}}`)
			}))
			defer srv.Close()
			store := codebox.NewLaunchTimingStore(codebox.DefaultFS, t.TempDir())
			u := &codebox.LaunchUX{TimingStore: store, TimingKey: "backend"}
			d := codebox.NewDispatcherWith(codebox.Config{Isolation: codebox.IsolationBackend, StartTimeout: time.Nanosecond}, nil)
			d.Launch = u
			b := launchReconciler{endpoint: srv.URL, reconcile: func() error {
				elapsed.Add(3)
				u.RecordFirstStart()
				return nil
			}}
			err := startWithLaunchReport(context.Background(), d, b)
			if probes.Load() == 0 {
				t.Fatal("readiness was never checked")
			}
			if (err == nil) != healthy {
				t.Fatalf("readiness error: %v", err)
			}
			low, high, ok := store.Observation("backend", codebox.LaunchCold)
			if healthy && (!ok || low != 143 || high != 143) {
				t.Fatalf("incomplete timing: %d..%d present=%v", low, high, ok)
			}
			if !healthy && ok {
				t.Fatal("failed readiness persisted a successful timing")
			}
		})
	}
}

func TestLaunchMeasuresCompleteReconciliationAndOnlySuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			now := time.Unix(100, 0)
			oldNow := launchNowFn
			launchNowFn = func() time.Time { return now }
			t.Cleanup(func() { launchNowFn = oldNow })
			store := codebox.NewLaunchTimingStore(codebox.DefaultFS, t.TempDir())
			u := &codebox.LaunchUX{TimingStore: store, TimingKey: "measured"}
			d := codebox.NewDispatcherWith(codebox.Config{Isolation: codebox.IsolationFull}, nil)
			d.Launch = u
			b := launchReconciler{reconcile: func() error {
				now = now.Add(143 * time.Second)
				u.RecordFirstStart()
				if fail {
					return fmt.Errorf("start failed")
				}
				return nil
			}}
			err := startWithLaunchReport(context.Background(), d, b)
			if (err != nil) != fail {
				t.Fatal(err)
			}
			low, high, ok := store.Observation("measured", codebox.LaunchCold)
			if fail && ok {
				t.Fatal("failed launch was persisted")
			}
			if !fail && (!ok || low != 143 || high != 143) {
				t.Fatalf("measured %d..%d, present=%v", low, high, ok)
			}
		})
	}
}

func TestLaunchTrustDecisions(t *testing.T) {
	for _, decision := range []string{"approve", "detail", "quit", "error", "non-tty", "changed"} {
		t.Run(decision, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			config := filepath.Join(root, "opencode.json")
			if err := os.WriteFile(config, []byte(`{"mcp":{"docs":{"command":"npx"}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			oldTTY, oldChoice := launchTrustTTYFn, launchTrustChoiceFn
			t.Cleanup(func() { launchTrustTTYFn, launchTrustChoiceFn = oldTTY, oldChoice })
			launchTrustTTYFn = func() bool { return decision != "non-tty" }
			calls := 0
			launchTrustChoiceFn = func(detail string) (string, error) {
				calls++
				if decision == "non-tty" {
					t.Fatal("prompted without a terminal")
				}
				if decision == "error" {
					return "", fmt.Errorf("cancelled")
				}
				if decision == "detail" && calls == 1 {
					return "detail", nil
				}
				if decision == "detail" && !strings.Contains(detail, "opencode.json") {
					t.Fatal(detail)
				}
				if decision == "changed" {
					if err := os.WriteFile(config, []byte(`{"mcp":{"docs":{"command":"other"}}}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if decision == "quit" {
					return "quit", nil
				}
				return "approve", nil
			}
			err := requireTrustedExecutionInputs(root)
			approved := decision == "approve" || decision == "detail"
			if (err == nil) != approved {
				t.Fatalf("result = %v", err)
			}
			remaining, err := codebox.DiscoverUnapprovedInputs(codebox.DefaultFS, codebox.DefaultStateDir(), root)
			if err != nil {
				t.Fatal(err)
			}
			if (len(remaining) == 0) != approved {
				t.Fatalf("remaining = %v", remaining)
			}
		})
	}
}

func TestHandleInteractiveRecreateOnLaunchDeclinePreservesSandbox(t *testing.T) {
	oldConfirm := confirmLaunchRecreateFn
	oldRecover := launchRecreateRecoverFn
	oldRun := launchRecreateRunFn
	t.Cleanup(func() {
		confirmLaunchRecreateFn = oldConfirm
		launchRecreateRecoverFn = oldRecover
		launchRecreateRunFn = oldRun
	})

	recoverCalled := false
	launchRecreateRecoverFn = func(context.Context, *codebox.MicrosandboxRuntime, codebox.Config, string) {
		recoverCalled = true
	}
	recreateCalled := false
	launchRecreateRunFn = func(context.Context, *codebox.MicrosandboxRuntime) error {
		recreateCalled = true
		return nil
	}
	var seenReason string
	confirmLaunchRecreateFn = func(reason string) (bool, error) {
		seenReason = reason
		return false, nil
	}

	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("LOCALAPPDATA", stateDir)
	ws := t.TempDir()
	m := codebox.NewMicrosandboxRuntimeForInstance(codebox.Config{WorkspaceDir: ws}, "cb-test-recreate")
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: m,
	})
	recreateErr := &codebox.ErrRecreateNeeded{
		Instance: "cb-test-recreate",
		Reason:   "le serveur de modèles local a été activé sur le port 11434 (élément fixé à la création)",
		Reasons:  []string{"le serveur de modèles local a été activé sur le port 11434"},
	}

	var code int
	var err error
	errOut := captureStderr(t, func() {
		code, err = handleInteractiveRecreateOnLaunch(context.Background(), d, m, codebox.Config{WorkspaceDir: ws}, "cb-test-recreate", ws, recreateErr)
	})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "rien n'a été détruit") {
		t.Fatalf("declined recreate = code %d, err %v; want refusal preserving sandbox", code, err)
	}
	if !recoverCalled {
		t.Fatal("expected pre-recreate recovery inspection to run before confirmation")
	}
	if recreateCalled {
		t.Fatal("declining confirmation must never invoke Recreate")
	}
	if seenReason != recreateErr.Reason {
		t.Fatalf("confirm prompt received reason %q, want %q", seenReason, recreateErr.Reason)
	}
	if !strings.Contains(errOut, "le serveur de modèles local a été activé sur le port 11434") {
		t.Fatalf("stderr must display the exact recreate cause:\n%s", errOut)
	}
}

func TestHandleInteractiveRecreateOnLaunchRevalidatesExecutionTrustAfterRecovery(t *testing.T) {
	oldConfirm := confirmLaunchRecreateFn
	oldRecover := launchRecreateRecoverFn
	oldRun := launchRecreateRunFn
	oldTrustTTY := launchTrustTTYFn
	t.Cleanup(func() {
		confirmLaunchRecreateFn = oldConfirm
		launchRecreateRecoverFn = oldRecover
		launchRecreateRunFn = oldRun
		launchTrustTTYFn = oldTrustTTY
	})

	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	t.Setenv("LOCALAPPDATA", stateDir)
	ws := t.TempDir()
	launchTrustTTYFn = func() bool { return false }

	wantRelPath := filepath.Join(".opencode", "plugin", "recovered.ts")
	// Simulate workspace recovery applying a new guest-authored OpenCode plugin
	// into the host checkout after the initial launch trust check passed.
	launchRecreateRecoverFn = func(_ context.Context, _ *codebox.MicrosandboxRuntime, cfg codebox.Config, _ string) {
		pluginPath := filepath.Join(cfg.WorkspaceDir, wantRelPath)
		if err := os.MkdirAll(filepath.Dir(pluginPath), 0o755); err != nil {
			t.Fatalf("mkdir plugin dir: %v", err)
		}
		if err := os.WriteFile(pluginPath, []byte("export default {}\n"), 0o644); err != nil {
			t.Fatalf("write plugin: %v", err)
		}
	}
	confirmCalled := false
	confirmLaunchRecreateFn = func(string) (bool, error) {
		confirmCalled = true
		return true, nil
	}
	recreateCalled := false
	launchRecreateRunFn = func(context.Context, *codebox.MicrosandboxRuntime) error {
		recreateCalled = true
		return nil
	}

	m := codebox.NewMicrosandboxRuntimeForInstance(codebox.Config{WorkspaceDir: ws}, "cb-test-trust")
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: m,
	})
	recreateErr := &codebox.ErrRecreateNeeded{
		Instance: "cb-test-trust",
		Reason:   "la mémoire de la machine isolée a changé",
		Reasons:  []string{"la mémoire de la machine isolée a changé"},
	}

	var code int
	var err error
	captureStderr(t, func() {
		code, err = handleInteractiveRecreateOnLaunch(context.Background(), d, m, codebox.Config{WorkspaceDir: ws}, "cb-test-trust", ws, recreateErr)
	})
	if code != 1 || err == nil || !strings.Contains(err.Error(), wantRelPath) || !strings.Contains(err.Error(), "codebox trust approve") {
		t.Fatalf("unapproved recovered plugin = code %d, err %v; want execution-trust block mentioning %q", code, err, wantRelPath)
	}
	if confirmCalled {
		t.Fatal("recreate confirmation must not run while recovered execution inputs are unapproved")
	}
	if recreateCalled {
		t.Fatal("Recreate must never run while recovered execution inputs are unapproved")
	}
}

func TestLaunchRecreateBackupPathIsUniqueAcrossRuns(t *testing.T) {
	oldNow := launchNowFn
	t.Cleanup(func() { launchNowFn = oldNow })
	stateDir := t.TempDir()

	launchNowFn = func() time.Time { return time.Date(2026, 11, 25, 10, 0, 0, 1, time.UTC) }
	first := launchRecreateBackupPath(stateDir, "cb-inst")
	launchNowFn = func() time.Time { return time.Date(2026, 11, 25, 10, 5, 0, 2, time.UTC) }
	second := launchRecreateBackupPath(stateDir, "cb-inst")

	if first == second {
		t.Fatalf("repeated automatic backups must not overwrite the same path: %q", first)
	}
	if !strings.HasPrefix(first, codebox.InstanceStateDir(stateDir, "cb-inst")) || !strings.HasSuffix(first, ".patch") {
		t.Fatalf("unexpected backup path: %q", first)
	}
}
