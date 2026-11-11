//go:build darwin

package codebox

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Keychain adapter tests (P08) with a fake Runner: they pin the argv
// contract (mutations go through `security -i`, the subcommand line on
// stdin, so the secret never appears in this process's argv) and the
// error mapping. Real-store behavior is pinned by
// credentials_real_test.go on machines with a user keychain.

// fakeKeychainRunner records calls and captures the stdin payload, which
// is where the secret travels.
type fakeKeychainRunner struct {
	calls   []string
	stdins  []string
	onRun   func(name string, args []string, stdin string) (ExecResult, error)
	lastStd string
}

func (f *fakeKeychainRunner) Run(ctx context.Context, name string, args ...string) (ExecResult, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.onRun(name, args, "")
}

func (f *fakeKeychainRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (ExecResult, error) {
	return f.Run(ctx, name, args...)
}

func (f *fakeKeychainRunner) RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (ExecResult, error) {
	buf, err := io.ReadAll(stdin)
	if err != nil {
		return ExecResult{}, err
	}
	f.lastStd = string(buf)
	f.stdins = append(f.stdins, f.lastStd)
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.onRun(name, args, f.lastStd)
}

// TestKeychainPutSendsSubcommandOnStdin pins the transport: the secret
// is inside the `security -i` stdin line, never in argv.
func TestKeychainPutSendsSubcommandOnStdin(t *testing.T) {
	f := &fakeKeychainRunner{onRun: func(name string, args []string, stdin string) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil
	}}
	k := &KeychainStore{Runner: f}
	if err := k.Put(context.Background(), CredentialAlbert, "secret-value-123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != "security -i" {
		t.Fatalf("calls = %v, want one `security -i`", f.calls)
	}
	want := "add-generic-password -U -s codebox/albert -a codebox -w secret-value-123\n"
	if f.lastStd != want {
		t.Fatalf("stdin = %q, want %q", f.lastStd, want)
	}
}

// TestKeychainGetMapsNotFound verifies the not-found stderr wording maps
// to ErrCredentialNotFound.
func TestKeychainGetMapsNotFound(t *testing.T) {
	f := &fakeKeychainRunner{onRun: func(name string, args []string, stdin string) (ExecResult, error) {
		return ExecResult{ExitCode: 44, Stderr: "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain."}, nil
	}}
	k := &KeychainStore{Runner: f}
	if _, err := k.Get(context.Background(), CredentialAlbert); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Get error = %v, want ErrCredentialNotFound", err)
	}
}

// TestKeychainGetMapsDenied verifies a refused read reports denied.
func TestKeychainGetMapsDenied(t *testing.T) {
	f := &fakeKeychainRunner{onRun: func(name string, args []string, stdin string) (ExecResult, error) {
		return ExecResult{ExitCode: 51, Stderr: "security: SecKeychainItemCopyAttributesAndData: user refused"}, nil
	}}
	k := &KeychainStore{Runner: f}
	_, err := k.Get(context.Background(), CredentialAlbert)
	var se *StoreError
	if !errors.As(err, &se) || se.State != "denied" {
		t.Fatalf("Get error = %v, want denied StoreError", err)
	}
}

// TestKeychainRemoveSendsSubcommandOnStdin pins remove's transport: no
// value anywhere, subcommand on stdin.
func TestKeychainRemoveSendsSubcommandOnStdin(t *testing.T) {
	f := &fakeKeychainRunner{onRun: func(name string, args []string, stdin string) (ExecResult, error) {
		return ExecResult{ExitCode: 0}, nil
	}}
	k := &KeychainStore{Runner: f}
	if err := k.Remove(context.Background(), CredentialAlbert); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if f.lastStd != "delete-generic-password -s codebox/albert\n" {
		t.Fatalf("stdin = %q", f.lastStd)
	}
}

// TestKeychainVerifyProbesSentinel verifies the probe reads a sentinel
// service and treats not-found as healthy.
func TestKeychainVerifyProbesSentinel(t *testing.T) {
	f := &fakeKeychainRunner{onRun: func(name string, args []string, stdin string) (ExecResult, error) {
		return ExecResult{ExitCode: 44, Stderr: "could not be found"}, nil
	}}
	k := &KeychainStore{Runner: f}
	if err := k.Verify(context.Background()); err != nil {
		t.Fatalf("Verify = %v, want nil (not-found is healthy)", err)
	}
	for _, c := range f.calls {
		if !strings.Contains(c, "find-generic-password") {
			t.Fatalf("Verify call = %q, want a find-generic-password probe", c)
		}
	}
}
