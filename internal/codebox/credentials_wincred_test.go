//go:build windows

package codebox

import (
	"context"
	"strings"
	"testing"
)

// Windows Credential Manager adapter tests (P08) with a fake Runner.
// They pin the invocation contract: `powershell -Command` joins
// everything after the flag into one command string and never populates
// $args, so the op and target must be injected into the script text
// itself, and the secret must travel on stdin, never in argv. The
// $args-based shape was a real bug caught by review: the fake runner
// here is what makes the contract regression-visible.

func TestWinCredPutInjectsOpAndTarget(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil
	}}
	w := &WinCredStore{Runner: f}
	if err := w.Put(context.Background(), CredentialAlbert, "secret-value-123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(f.calls))
	}
	c := f.calls[0]
	if !strings.HasPrefix(c, "powershell -NoProfile -NonInteractive -Command ") {
		t.Fatalf("argv prefix wrong: %s", c)
	}
	if !strings.Contains(c, "$op = 'put'; $target = 'codebox/albert'") {
		t.Fatalf("op/target not injected into script: %s", c)
	}
	if strings.Contains(c, "secret-value-123") {
		t.Fatalf("secret leaked into argv: %s", c)
	}
	// The trailing "put codebox/albert" argv form must be gone: it
	// silently left $args empty on real Windows.
	if strings.Contains(c, " codebox/albert\n") || strings.HasSuffix(c, " codebox/albert") {
		t.Fatalf("target still passed as argv: %s", c)
	}
}

func TestWinCredGetInjectsOpAndTarget(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{Stdout: "the-secret", ExitCode: 0}, nil
	}}
	w := &WinCredStore{Runner: f}
	v, err := w.Get(context.Background(), CredentialAlbert)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v != "the-secret" {
		t.Fatalf("value = %q", v)
	}
	if !strings.Contains(f.calls[0], "$op = 'get'; $target = 'codebox/albert'") {
		t.Fatalf("op/target not injected: %s", f.calls[0])
	}
}

func TestWinCredRemoveMapsNotFound(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 1}, nil // ERROR_NOT_FOUND
	}}
	w := &WinCredStore{Runner: f}
	err := w.Remove(context.Background(), CredentialAlbert)
	if !IsCredentialNotFound(err) {
		t.Fatalf("want not-found, got %v", err)
	}
	if !strings.Contains(f.calls[0], "$op = 'remove'; $target = 'codebox/albert'") {
		t.Fatalf("op/target not injected: %s", f.calls[0])
	}
}

func TestWinCredVerifyMapsReachable(t *testing.T) {
	f := &fakeStoreRunner{onRun: func(name string, args []string) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil // sentinel not found: reachable
	}}
	w := &WinCredStore{Runner: f}
	if err := w.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !strings.Contains(f.calls[0], "$op = 'verify'; $target = 'codebox/__verify__'") {
		t.Fatalf("op/target not injected: %s", f.calls[0])
	}
}
