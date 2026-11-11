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

	"github.com/etalab-ia/codebox/internal/codebox"
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
					_, err = lifecycleCmd(d, codebox.RuntimeMicrosandbox, action)
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
