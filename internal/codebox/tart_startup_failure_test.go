package codebox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writingStarter is a Starter that writes to the log as it launches the VM,
// modelling `tart run` reporting a startup refusal immediately.
type writingStarter struct {
	onStart  func(logPath string)
	launched bool
}

func (w *writingStarter) Start(_ io.Reader, logPath string, _ string, _ ...string) error {
	w.launched = true
	if w.onStart != nil {
		w.onStart(logPath)
	}
	return nil
}

// TestTartStartupFailureRecognisesTheVZRefusal pins the parser against the
// real log lines a headless Mac produces. The wording varies across macOS
// versions, so the markers are the error domains, not a full sentence.
func TestTartStartupFailureRecognisesTheVZRefusal(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		match bool
	}{
		{"vz domain with code", `VZErrorDomain Code=-9 "The virtual machine encountered a security error"`, true},
		{"host key failure", `NSPOSIXErrorDomain Code=22 "Failed to create new HostKey"`, true},
		{"tart prefix variant", `tart: VZErrorDomain Code=-9 "The virtual machine encountered a security error"`, true},
		// VZErrorDomain covers many codes, and only the security-error one is
		// the keychain case. Blaming a locked keychain for an unrelated
		// Virtualization.framework failure would send the user to unlock
		// something that is not the problem.
		{"unrelated vz error", `VZErrorDomain Code=-5 "The virtual machine failed to start"`, false},
		{"invalid disk", `VZErrorDomain Code=2 "The disk image is invalid"`, false},
		// A normal boot must not be mistaken for a failure: reporting a false
		// cause would be worse than the timeout it replaces.
		{"ordinary boot line", "Booting opencode-tahoe-base-latest", false},
		{"guest talking", "opencode-tahoe-base-latest: login: ", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTartStartupFailure(tc.line); got != tc.match {
				t.Fatalf("isTartStartupFailure(%q) = %v, want %v", tc.line, got, tc.match)
			}
		})
	}
}

// TestWaitForAgentNamesTheVZFailureInsteadOfTimingOut pins #206: a VM that
// refused to boot must be reported as such, immediately, rather than waiting
// the full timeout and claiming the guest agent never answered.
func TestWaitForAgentNamesTheVZFailureInsteadOfTimingOut(t *testing.T) {
	tt := newTestTart(t, nil)
	// The exec probe never succeeds (the VM never booted), and the log the
	// failing `tart run` writes is appended during the wait — writing it up
	// front would make it an earlier run's entry, which the offset scan is
	// now correct to ignore.
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		f, err := os.OpenFile(tt.LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(`VZErrorDomain Code=-9 "The virtual machine encountered a security error"` + "\n")
			_ = f.Close()
		}
		return ExecResult{ExitCode: 1, Stderr: "no such process"}
	}}
	tt.Runner = r
	tt.Config.Isolation = IsolationFull
	// Small seam so the early-exit assertion does not depend on a full timeout
	// run: without it this test takes a real minute when the fix is absent.
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 3
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}

	err := tt.waitForAgent(context.Background(), 0)
	if err == nil {
		t.Fatal("a VM that refused to boot must be an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "security error") {
		t.Fatalf("the error must carry the VZ cause, got: %v", err)
	}
	if !strings.Contains(msg, "keychain") {
		t.Fatalf("the error must name the actionable cause, got: %v", err)
	}
	// The whole point is that it stops as soon as the log names the failure
	// rather than polling to exhaustion. With polls=3, the fixed code exits on
	// the first probe; an implementation that only checks the log after the
	// loop must consume all three.
	if len(r.calls) > 2 {
		t.Fatalf("the VM log must stop the poll early, got %d probes", len(r.calls))
	}
}

// TestWaitForAgentSucceedsOnARetryAfterAFailedRun pins the user-visible
// consequence of the P1: after the advice to unlock the keychain is followed,
// the very next start must be able to succeed even though the shared log still
// holds the previous refusal.
func TestWaitForAgentSucceedsOnARetryAfterAFailedRun(t *testing.T) {
	r := &fakeRunner{} // exec succeeds: the retry's VM booted
	tt := newTestTart(t, r)
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 3
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	// The log left behind by the failed run, and the mark taken before this
	// launch — which is what keeps it out of this run's verdict.
	if err := os.WriteFile(tt.LogPath(), []byte(
		`VZErrorDomain Code=-9 "The virtual machine encountered a security error"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleMark := logSize(tt.LogPath())

	if err := tt.waitForAgent(context.Background(), staleMark); err != nil {
		t.Fatalf("a healthy retry must not inherit the previous run's failure: %v", err)
	}
}

// TestWaitForAgentDoesNotReportAPreviousRunsFailureOnTimeout is the
// discriminating case for the P1: probes keep failing while the log holds only
// an earlier run's refusal. The wait must run its course and report the plain
// timeout; reading the shared log from byte 0 would surface the stale failure
// on the very first poll and end the wait instantly with a false cause.
func TestWaitForAgentDoesNotReportAPreviousRunsFailureOnTimeout(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1, Stderr: "not ready"}
	}}
	tt := newTestTart(t, r)
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 4
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	// An earlier run's failure, already on disk before this wait begins.
	if err := os.WriteFile(tt.LogPath(), []byte(
		`VZErrorDomain Code=-9 "The virtual machine encountered a security error"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleMark := logSize(tt.LogPath())

	err := tt.waitForAgent(context.Background(), staleMark)
	if err == nil {
		t.Fatal("a guest that never answers must time out")
	}
	if !strings.Contains(err.Error(), "timed out waiting for") {
		t.Fatalf("the previous run's failure must not be reported, got: %v", err)
	}
	if strings.Contains(err.Error(), "security error") {
		t.Fatalf("the stale failure leaked into this run's verdict: %v", err)
	}
	if len(r.calls) != 4 {
		t.Fatalf("the wait must run its full course, got %d probes", len(r.calls))
	}
}

// TestWaitForAgentKeepsThePlainTimeoutWithoutALogFailure pins the other half:
// a guest that simply is not ready yet must still be polled, and must keep the
// original message when the log shows no startup failure.
func TestWaitForAgentKeepsThePlainTimeoutWithoutALogFailure(t *testing.T) {
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		return ExecResult{ExitCode: 1, Stderr: "not ready"}
	}}
	tt := newTestTart(t, r)
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 5
	if err := os.MkdirAll(filepath.Dir(tt.LogPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tt.LogPath(), []byte("Booting vm\nstill booting\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The poll must run its full course and keep the original wording: no
	// failure may be invented from a healthy log.
	err := tt.waitForAgent(context.Background(), 0)
	if err == nil {
		t.Fatal("a guest that never answers must time out")
	}
	if !strings.Contains(err.Error(), "timed out waiting for") {
		t.Fatalf("a healthy log must keep the plain timeout, got: %v", err)
	}
	if strings.Contains(err.Error(), "VZErrorDomain") || strings.Contains(err.Error(), "keychain") {
		t.Fatalf("no startup failure may be invented: %v", err)
	}
	if len(r.calls) != 5 {
		t.Fatalf("the wait must poll the configured number of times, got %d", len(r.calls))
	}
}

// TestNewTartWiresTheAgentWaitSeam pins that the production constructor sets
// the wait seam, so the defaults are not only the loop's fallback.
func TestNewTartWiresTheAgentWaitSeam(t *testing.T) {
	tt := NewTart(Config{TartVM: "vm"})
	if tt.AgentWaitPolls != 60 {
		t.Fatalf("AgentWaitPolls = %d, want 60", tt.AgentWaitPolls)
	}
	if tt.AgentWaitInterval != time.Second {
		t.Fatalf("AgentWaitInterval = %v, want 1s", tt.AgentWaitInterval)
	}
}

// TestStartMarksTheLogBeforeLaunchingTheVM pins the P1 ordering: the start
// failure is emitted within seconds of the detached `tart run` starting, and
// that call has already returned before the first agent probe. Capturing the
// mark inside waitForAgent would therefore seek past the very error it looks
// for and fall back to the generic timeout the change exists to replace.
func TestStartMarksTheLogBeforeLaunchingTheVM(t *testing.T) {
	// A starter that writes the failure as the VM is launched — the worst case
	// for an offset taken after the call, and the realistic one: `tart run`
	// reports the refusal immediately.
	starter := &writingStarter{onStart: func(logPath string) {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = f.WriteString(`VZErrorDomain Code=-9 "The virtual machine encountered a security error"` + "\n")
		_ = f.Close()
	}}
	tt := newTestTart(t, nil)
	r := &fakeRunner{onRun: func(name string, args []string) ExecResult {
		if name == "tart" && len(args) > 0 && args[0] == "list" {
			// `local <vm> - 50G stopped`: the VM exists, so clone is skipped,
			// but it is not running, so Start takes the launch path.
			return ExecResult{ExitCode: 0, Stdout: "local " + tt.VMName() + " - 50G stopped\n"}
		}
		return ExecResult{ExitCode: 1, Stderr: "no guest agent yet"}
	}}
	tt.Runner = r
	tt.Starter = starter
	tt.Config.Isolation = IsolationFull
	tt.AgentWaitInterval = time.Millisecond
	tt.AgentWaitPolls = 3

	err := tt.Start(context.Background())
	if err == nil {
		t.Fatal("a VM that refused to boot must fail the start")
	}
	if !strings.Contains(err.Error(), "security error") {
		t.Fatalf("the launch failure must be named, not masked by the timeout: %v", err)
	}
	// Guard against the harness silently not exercising the path.
	if !starter.launched {
		t.Fatal("the starter must have been invoked for this test to mean anything")
	}
}

// TestTartStartupFailureToleratesAMissingLog pins the safety property: the log
// is written by a live process, so an absent or unreadable file must stay
// silent rather than turn a slow boot into a false error.
func TestTartStartupFailureToleratesAMissingLog(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.log")
	if reason := tartStartupFailure(missing, 0); reason != "" {
		t.Fatalf("a missing log must not report a failure, got %q", reason)
	}
}

// TestTartStartupFailureIgnoresAnEarlierRunsFailure pins the P1 on the #219
// review: the VM log is append-only and shared across runs, so a previous
// failure is still in the file when the user unlocks the keychain and retries.
// Scanning from byte 0 reported that stale failure immediately, turning a
// healthy retry into a false error.
func TestTartStartupFailureIgnoresAnEarlierRunsFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tart.log")
	stale := "VZErrorDomain Code=-9 \"The virtual machine encountered a security error\"\n"
	if err := os.WriteFile(logPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	offset := logSize(logPath)
	if offset == 0 {
		t.Fatal("the stale run must have written bytes")
	}

	// Unchanged file: the previous failure is present but belongs to the
	// earlier run, so it must not be reported.
	if reason := tartStartupFailure(logPath, offset); reason != "" {
		t.Fatalf("an earlier run's failure must not be reported, got %q", reason)
	}
	// Without the offset the same bytes are (correctly) seen — this is what
	// made the old scan wrong, so the difference is what the test pins.
	if reason := tartStartupFailure(logPath, 0); reason == "" {
		t.Fatal("the fixture must contain a readable failure for this test to mean anything")
	}

	// A genuine failure in this run still registers.
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`NSPOSIXErrorDomain Code=22 "Failed to create new HostKey"` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	reason := tartStartupFailure(logPath, offset)
	if !strings.Contains(reason, "Failed to create new HostKey") {
		t.Fatalf("this run's failure must be reported, got %q", reason)
	}
}

// TestLoginKeychainLockedReadsSecurityOutput pins the doctor probe: only a
// locked keychain is a warning, and a probe that cannot run is not.
func TestLoginKeychainLockedReadsSecurityOutput(t *testing.T) {
	cases := []struct {
		name   string
		res    ExecResult
		locked bool
	}{
		{"unlocked", ExecResult{ExitCode: 0}, false},
		{"locked", ExecResult{ExitCode: 1, Stderr: "The specified keychain could not be found."}, false},
		{"locked wording", ExecResult{ExitCode: 1, Stderr: "keychain is locked"}, true},
		{"locked stdout", ExecResult{ExitCode: 1, Stdout: "The keychain is locked."}, true},
		// The wording a locked keychain returns in a session with no GUI to
		// prompt in: this is the headless case the warning exists for, and it
		// never says "locked".
		{"no user interaction", ExecResult{ExitCode: 1, Stderr: "security: User interaction is not allowed."}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{onRun: func(string, []string) ExecResult { return tc.res }}
			got, _ := loginKeychainLocked(context.Background(), r)
			if got != tc.locked {
				t.Fatalf("loginKeychainLocked = %v, want %v", got, tc.locked)
			}
		})
	}
}

// TestLoginKeychainLockedIsSilentWhenSecurityIsAbsent pins that a probe which
// cannot run never blocks doctor or invents a warning.
func TestLoginKeychainLockedIsSilentWhenSecurityIsAbsent(t *testing.T) {
	r := &fakeRunner{onRun: func(string, []string) ExecResult {
		return ExecResult{ExitCode: 127, Stderr: "security: command not found"}
	}}
	if locked, detail := loginKeychainLocked(context.Background(), r); locked {
		t.Fatalf("an unusable probe must not warn, got %v (%s)", locked, detail)
	}
}
