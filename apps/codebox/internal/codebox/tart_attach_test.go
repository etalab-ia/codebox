package codebox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWaitForAgentOnAttachIgnoresAnEarlierRunsFailure pins the P2 on the #219
// review: attaching to an already-running VM must not diagnose a boot failure
// from a shared, append-only log. An earlier run's keychain refusal is still in
// the file, and a transient first `tart exec` probe against a warming-up guest
// would otherwise report it instead of letting later probes succeed.
func TestWaitForAgentOnAttachIgnoresAnEarlierRunsFailure(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1, Stderr: "guest agent warming up"}
	}}
	tt := newTestTart(t, r)
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 3
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tt.LogPath(), []byte(
		`VZErrorDomain Code=-9 "The virtual machine encountered a security error"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := tt.waitForAgent(context.Background(), noLaunch)
	if err == nil {
		t.Fatal("a guest that never answers must still time out")
	}
	if !strings.Contains(err.Error(), "timed out waiting for") {
		t.Fatalf("attaching must not report a boot failure from the log: %v", err)
	}
	if strings.Contains(err.Error(), "security error") || strings.Contains(err.Error(), "keychain") {
		t.Fatalf("an earlier run's failure leaked into an attach: %v", err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("the wait must run its full course, got %d probes", len(r.calls))
	}
}

// TestAttachPathStillSucceedsWhenTheGuestAnswers is the other direction: a
// running VM whose agent responds must attach cleanly, whatever the log says.
func TestAttachPathStillSucceedsWhenTheGuestAnswers(t *testing.T) {
	r := &fakeRunner{} // exec succeeds
	tt := newTestTart(t, r)
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 3
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tt.LogPath(), []byte(
		`VZErrorDomain Code=-9 "The virtual machine encountered a security error"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tt.waitForAgent(context.Background(), noLaunch); err != nil {
		t.Fatalf("a running VM with a responsive agent must attach: %v", err)
	}
}

// TestNoLaunchDoesNotCollideWithARealOffset guards the sentinel: a log that
// legitimately has size 0 must still diagnose startup failures, so the "no
// launch" marker cannot simply be 0.
func TestNoLaunchDoesNotCollideWithARealOffset(t *testing.T) {
	if noLaunch == 0 {
		t.Fatal("noLaunch must not collide with a real, empty-log offset of 0")
	}
	if noLaunch >= 0 {
		t.Fatalf("noLaunch must be negative so a real offset cannot equal it, got %d", noLaunch)
	}
}
