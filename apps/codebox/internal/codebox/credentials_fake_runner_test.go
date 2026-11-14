package codebox

import (
	"context"
	"io"
	"strings"
)

// fakeStoreRunner is shared by the per-OS adapter tests (keychain,
// secretservice, wincred). It lives in a build-tag-neutral file so every
// platform's test build sees it.
type fakeStoreRunner struct {
	// calls records each (name, args...) invocation.
	calls []string
	// onRun answers each call.
	onRun func(name string, args []string) (ExecResult, error)
}

func (f *fakeStoreRunner) Run(ctx context.Context, name string, args ...string) (ExecResult, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.onRun(name, args)
}

func (f *fakeStoreRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (ExecResult, error) {
	return f.Run(ctx, name, args...)
}

func (f *fakeStoreRunner) RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (ExecResult, error) {
	return f.Run(ctx, name, args...)
}

func hasStoreCall(f *fakeStoreRunner, prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
