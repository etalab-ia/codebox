package codebox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeToolchainClock struct {
	now time.Time
}

func (c *fakeToolchainClock) Now() time.Time { return c.now }

func (c *fakeToolchainClock) After(delay time.Duration) <-chan time.Time {
	c.now = c.now.Add(delay)
	fired := make(chan time.Time, 1)
	fired <- c.now
	return fired
}

func TestMicrosandboxToolchainWaitContinuesWithLogProgress(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationFull),
		execResults: []fakeMSBExecResult{
			{code: msbPrepareProgress, stderr: "100:1"},
			{code: msbPrepareProgress, stderr: "100:1"},
			{code: msbPrepareProgress, stderr: "200:2"},
			{code: msbPrepareProgress, stderr: "200:2"},
			{code: msbPrepareReady},
		},
	}
	clock := &fakeToolchainClock{now: time.Unix(0, 0)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 50 * time.Millisecond
	m.toolchainPollDelay = 20 * time.Millisecond
	m.toolchainHardCap = 500 * time.Millisecond
	m.toolchainClock = clock

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start must outlive the former fixed timeout while the log advances: %v", err)
	}
	if got := clock.now.Sub(time.Unix(0, 0)); got != 80*time.Millisecond {
		t.Fatalf("elapsed readiness wait = %s, want 80ms", got)
	}
	if hasCall(client, "create") || hasCall(client, "remove "+msbSandbox) {
		t.Fatalf("continued progress must not recreate the guest: %v", client.calls)
	}
}

func TestMicrosandboxToolchainWaitTimesOutAfterInactivity(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationFull),
		execDefault: &fakeMSBExecResult{code: msbPrepareProgress, stderr: "100:1"},
	}
	clock := &fakeToolchainClock{now: time.Unix(0, 0)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 50 * time.Millisecond
	m.toolchainPollDelay = 20 * time.Millisecond
	m.toolchainHardCap = 500 * time.Millisecond
	m.toolchainClock = clock

	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sans nouvelle activité") || !strings.Contains(err.Error(), "arrière-plan") {
		t.Fatalf("Start error = %v, want inactivity and background-resume guidance", err)
	}
	if got := clock.now.Sub(time.Unix(0, 0)); got != 50*time.Millisecond {
		t.Fatalf("inactivity deadline = %s, want 50ms", got)
	}
	if hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("a live but stalled installer must not be relaunched: %v", client.calls)
	}
}

func TestMicrosandboxToolchainWaitHardCapBoundsContinuedProgress(t *testing.T) {
	results := make([]fakeMSBExecResult, 0, 6)
	for i := 0; i < 6; i++ {
		results = append(results, fakeMSBExecResult{code: msbPrepareProgress, stderr: fmt.Sprintf("%d:1", 100+i)})
	}
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationFull),
		execResults: results,
		execDefault: &fakeMSBExecResult{code: msbPrepareProgress, stderr: "last:1"},
	}
	clock := &fakeToolchainClock{now: time.Unix(0, 0)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 50 * time.Millisecond
	m.toolchainPollDelay = 20 * time.Millisecond
	m.toolchainHardCap = 120 * time.Millisecond
	m.toolchainClock = clock

	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "délai maximal") || !strings.Contains(err.Error(), "arrière-plan") {
		t.Fatalf("Start error = %v, want hard-cap and background-resume guidance", err)
	}
	if got := clock.now.Sub(time.Unix(0, 0)); got != 120*time.Millisecond {
		t.Fatalf("hard-cap deadline = %s, want 120ms", got)
	}
}

func TestMicrosandboxToolchainWaitResumesInterruptedGuestWithoutRecreate(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationFull),
		execDefault: &fakeMSBExecResult{code: msbPrepareProgress, stderr: "100:1"},
	}
	clock := &fakeToolchainClock{now: time.Unix(0, 0)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 50 * time.Millisecond
	m.toolchainPollDelay = 20 * time.Millisecond
	m.toolchainHardCap = 500 * time.Millisecond
	m.toolchainClock = clock

	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "arrière-plan") {
		t.Fatalf("interrupted Start error = %v, want background-resume guidance", err)
	}
	client.execDefault = &fakeMSBExecResult{code: msbPrepareReady}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("resumed Start must observe the completed background installation: %v", err)
	}
	for _, forbidden := range []string{"create", "remove " + msbSandbox, "exec " + msbSandbox + " " + msbRelaunchCommand} {
		if hasCall(client, forbidden) {
			t.Fatalf("resume must reuse the existing guest and installer state; unexpected %q in %v", forbidden, client.calls)
		}
	}
}

func TestMSBPrepareProbeReportsLogActivity(t *testing.T) {
	for _, harness := range []Harness{HarnessOpenCode, HarnessPi} {
		probe := msbGuestPrepareProbeFor(harness)
		for _, expected := range []string{"stat -c", msbGuestLog, ">&2"} {
			if !strings.Contains(probe, expected) {
				t.Errorf("%s probe lacks log-activity fragment %q: %s", harness, expected, probe)
			}
		}
	}
}
