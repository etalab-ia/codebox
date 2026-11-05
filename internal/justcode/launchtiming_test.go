package justcode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type launchPreparationClient struct {
	*fakeMSBClient
	preparation fakeMSBExecResult
	launched    bool
}

type reconcilePreparationClient struct {
	*fakeMSBClient
	probes int
	finish bool
}

func (c *reconcilePreparationClient) Exec(ctx context.Context, name, command string) (int, string, error) {
	if command == msbGuestPrepareProbe {
		c.record("exec " + name + " " + command)
		c.probes++
		if c.finish && c.probes >= 3 {
			return msbPrepareReady, "", nil
		}
		return msbPrepareProgress, "", nil
	}
	return c.fakeMSBClient.Exec(ctx, name, command)
}

func TestFullReconcileLaunchWaitsForPreparation(t *testing.T) {
	for _, finish := range []bool{false, true} {
		t.Run(fmt.Sprint(finish), func(t *testing.T) {
			isolateHostState(t)
			client := &fakeMSBClient{exists: true, status: "running", startScript: msbStartScript(IsolationFull)}
			m := newTestMicrosandbox(t, client)
			probe := &reconcilePreparationClient{fakeMSBClient: client, finish: finish}
			m.Client = probe
			m.cfg.Isolation = IsolationFull
			m.cfg.StartTimeout = time.Second
			if !finish {
				m.cfg.StartTimeout = 5 * time.Millisecond
			}
			m.toolchainPollDelay = time.Millisecond
			desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
			if err := WriteInstanceState(DefaultFS, instanceStatePath(DefaultStateDir(), m.InstanceName()), desired.toState()); err != nil {
				t.Fatal(err)
			}
			store := NewLaunchTimingStore(DefaultFS, t.TempDir())
			u := &LaunchUX{TimingStore: store}
			d := NewDispatcherWith(m.cfg, map[Runtime]Backend{RuntimeMicrosandbox: m})
			d.SetLaunchUX(u)
			if err := StartOrReconcile(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
				t.Fatal("fixture did not exercise the reconciliation restart")
			}
			err := d.WaitLaunchReady(context.Background(), m)
			if (err == nil) != finish {
				t.Fatalf("readiness error=%v, installation finished=%v", err, finish)
			}
			if probe.probes < 2 {
				t.Fatal("reconciliation bypassed the preparation check")
			}
			if err == nil {
				u.Observe(143 * time.Second)
			}
			_, _, cold := store.Observation(u.TimingKey, LaunchCold)
			_, _, warm := store.Observation(u.TimingKey, LaunchWarm)
			if cold != finish || warm {
				t.Fatalf("incorrect timing history: cold=%v warm=%v", cold, warm)
			}
		})
	}
}

func (c *launchPreparationClient) Exec(ctx context.Context, name, command string) (int, string, error) {
	if command == msbGuestPrepareProbe {
		c.record("exec " + name + " " + command)
		if c.launched {
			return msbPrepareReady, "", nil
		}
		return c.preparation.code, c.preparation.stderr, c.preparation.err
	}
	if command == msbRelaunchCommand {
		c.launched = true
	}
	return c.fakeMSBClient.Exec(ctx, name, command)
}

func TestBackendRetryTimingUsesPreparationMarker(t *testing.T) {
	cases := []struct {
		name  string
		probe fakeMSBExecResult
		kind  LaunchKind
	}{
		{"ready", fakeMSBExecResult{code: msbPrepareReady}, LaunchWarm},
		{"installing", fakeMSBExecResult{code: msbPrepareProgress}, LaunchCold},
		{"interrupted", fakeMSBExecResult{code: msbPrepareIdle}, LaunchCold},
		{"unknown", fakeMSBExecResult{code: 1}, ""},
		{"unavailable", fakeMSBExecResult{err: fmt.Errorf("guest agent unavailable")}, ""},
	}
	for _, status := range []string{"stopped", "running"} {
		for _, tc := range cases {
			t.Run(status+"/"+tc.name, func(t *testing.T) {
				client := &fakeMSBClient{exists: true, status: status}
				m := newTestMicrosandbox(t, client)
				m.Client = &launchPreparationClient{fakeMSBClient: client, preparation: tc.probe}
				store := NewLaunchTimingStore(DefaultFS, t.TempDir())
				m.Launch = &LaunchUX{TimingStore: store, TimingKey: "retry"}
				if err := m.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				if !hasCall(client, "exec "+msbSandbox+" "+msbGuestPrepareProbe) {
					t.Fatal("preparation state was never checked")
				}
				if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
					t.Fatal("classification prevented backend launch")
				}
				if m.Launch.IsFirstStart() != (tc.kind == LaunchCold) {
					t.Fatalf("incorrect cold classification: %v", m.Launch.IsFirstStart())
				}
				m.Launch.Observe(125 * time.Second)
				_, _, coldSample := store.Observation("retry", LaunchCold)
				_, _, warmSample := store.Observation("retry", LaunchWarm)
				if coldSample != (tc.kind == LaunchCold) || warmSample != (tc.kind == LaunchWarm) {
					t.Fatalf("wrong history: cold=%v warm=%v", coldSample, warmSample)
				}
				if tc.kind == "" && !m.Launch.HasWarnings() {
					t.Fatal("unknown preparation must explain the missing timing")
				}
			})
		}
	}
}

func TestLaunchHygieneDetailStaysInNonInteractiveLogs(t *testing.T) {
	withNoGitleaks(t)
	for _, action := range []string{"start", "restart", "reconcile"} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/terminal=%v", action, terminal), func(t *testing.T) {
				isolateHostState(t)
				m := newTestMicrosandbox(t, &fakeMSBClient{exists: true, status: "stopped"})
				m.Launch = &LaunchUX{Terminal: terminal}
				if err := os.WriteFile(filepath.Join(m.cfg.WorkspaceDir, ".env"), []byte("FIXTURE=value\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				output := captureStderr(t, func() {
					var err error
					switch action {
					case "restart":
						err = m.Restart(context.Background())
					case "reconcile":
						err = m.Reconcile(context.Background())
					default:
						err = m.Start(context.Background())
					}
					if err != nil {
						t.Fatal(err)
					}
				})
				if strings.Contains(output, "dotenv file (excluded from transfer)") == terminal {
					t.Fatalf("incorrect launch hygiene output: %q", output)
				}
			})
		}
	}
}

func TestLaunchTimingPersistence(t *testing.T) {
	dir := t.TempDir()
	store := NewLaunchTimingStore(DefaultFS, dir)
	if _, _, ok := store.Observation("test", LaunchCold); ok {
		t.Fatal("missing measurements must not invent an estimate")
	}
	if err := store.Observe("test", LaunchCold, 125*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe("test", LaunchWarm, 12*time.Second); err != nil {
		t.Fatal(err)
	}
	reloaded := NewLaunchTimingStore(DefaultFS, dir)
	for kind, want := range map[LaunchKind]int64{LaunchCold: 125, LaunchWarm: 12} {
		low, high, ok := reloaded.Observation("test", kind)
		if !ok || low != want || high != want {
			t.Fatalf("%s = %d %d %v", kind, low, high, ok)
		}
	}
	for i := 0; i < maxLaunchSamples+2; i++ {
		if err := reloaded.Observe("test", LaunchCold, 10*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if got := reloaded.load().Entries["test"].FirstColdSeconds; got != 125 {
		t.Fatalf("first cold baseline overwritten: %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, launchTimingsFile), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := reloaded.Observation("test", LaunchCold); ok {
		t.Fatal("corrupt history must not produce an estimate")
	}
}

func TestLaunchTimingFingerprintIncludesImageDigest(t *testing.T) {
	key := func(image string) string {
		return LaunchTimingKey(image, OpenCodeVersion, IsolationFull, 2, 4096, "linux", "arm64")
	}
	if key("debian@sha256:a") == key("debian@sha256:b") {
		t.Fatal("new images must not inherit old timing samples")
	}
}

func TestLaunchTimingUsesSelectedGuestImage(t *testing.T) {
	for _, isolation := range []Isolation{IsolationFull, IsolationBackend} {
		for _, ids := range [][]string{nil, {"chrome-devtools"}} {
			m := NewMicrosandboxRuntime(Config{Isolation: isolation})
			m.OpenCodeOverlay.MCPConnectors = ids
			d := NewDispatcherWith(m.cfg, map[Runtime]Backend{RuntimeMicrosandbox: m})
			u := &LaunchUX{}
			d.SetLaunchUX(u)
			want := LaunchTimingKeyFor(m.cfg, msbImageForMCPs(ids))
			if u.TimingKey != want {
				t.Errorf("isolation=%s MCPs=%v: timing image differs from guest image", isolation, ids)
			}
		}
	}
}

func TestLaunchUXMeasuredHintsAndWarningSynthesis(t *testing.T) {
	u := &LaunchUX{TimingStore: NewLaunchTimingStore(DefaultFS, t.TempDir()), TimingKey: "test", AgentRuntime: "OpenCode"}
	if strings.Contains(u.LaunchLine(), "30 secondes") {
		t.Fatal("unmeasured launch must not promise a duration")
	}
	u.RecordFirstStart()
	u.Observe(125 * time.Second)
	if !strings.Contains(u.LaunchLine(), "2 minutes") {
		t.Fatal(u.LaunchLine())
	}
	u.AddWarning("ressources limitées")
	u.AddWarning("ressources limitées")
	u.AddWarning("secret non recherché")
	lines := u.Checklist()
	count := 0
	for _, line := range lines {
		if strings.Contains(line, "⚠") {
			count++
			if !strings.Contains(line, "ressources limitées") || !strings.Contains(line, "secret non recherché") {
				t.Fatal(line)
			}
		}
	}
	if count != 1 {
		t.Fatalf("got %d warning lines: %v", count, lines)
	}
}

func TestLaunchDisablesOpenCodeUpdates(t *testing.T) {
	m := NewMicrosandboxRuntime(Config{Isolation: IsolationFull})
	for _, env := range []map[string]string{m.sandboxEnv(), m.nextStartEnv()} {
		if env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
			t.Fatal("guest must not offer unpinned updates")
		}
	}
	d := NewDispatcherWith(Config{}, map[Runtime]Backend{RuntimeMicrosandbox: m})
	u := &LaunchUX{}
	d.SetLaunchUX(u)
	if m.Launch != u || u.TimingKey == "" {
		t.Fatal("report must reach the runtime")
	}
	d.SetLaunchUX(nil)
}

func TestLaunchTransferCountsAreStructuredAndIdempotent(t *testing.T) {
	u := &LaunchUX{}
	manifest := TransferManifest{Entries: []TransferEntry{
		{Included: true},
		{Exclusion: TransferExclusionDotenv, Reason: "arbitrary wording"},
		{Exclusion: TransferExclusionSecret, Reason: "arbitrary wording"},
		{Exclusion: TransferExclusionGitIgnored},
	}}
	u.RecordTransfer(manifest)
	u.RecordTransfer(manifest)
	if u.copiedFiles != 1 || u.dotenvExcluded != 1 || u.secretExcluded != 1 {
		t.Fatalf("incorrect counts: %+v", u)
	}
	if !strings.Contains(u.transferLine(), ".env") || !strings.Contains(u.transferLine(), "sensibles") {
		t.Fatal(u.transferLine())
	}
}

func TestApproveReviewedInputsRejectsChanges(t *testing.T) {
	for _, change := range []string{"unchanged", "changed", "added", "unreadable"} {
		t.Run(change, func(t *testing.T) {
			root, state := trustProject(t), t.TempDir()
			inputs, err := DiscoverUnapprovedInputs(DefaultFS, state, root)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "changed":
				err = os.WriteFile(filepath.Join(root, ".opencode/plugin/hook.js"), []byte("// changed"), 0o600)
			case "added":
				err = os.WriteFile(filepath.Join(root, ".opencode/plugin/new.js"), []byte("// new"), 0o600)
			case "unreadable":
				inputs[0].Hash = ""
			}
			if err != nil {
				t.Fatal(err)
			}
			err = ApproveReviewedInputs(DefaultFS, state, root, inputs)
			if (err == nil) != (change == "unchanged") {
				t.Fatalf("approval result: %v", err)
			}
			remaining, err := DiscoverUnapprovedInputs(DefaultFS, state, root)
			if err != nil {
				t.Fatal(err)
			}
			if change != "unchanged" && len(remaining) == 0 {
				t.Fatal("unreviewed content gained approval")
			}
		})
	}
}
