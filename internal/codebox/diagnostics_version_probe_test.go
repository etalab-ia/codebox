package codebox

import (
	"context"
	"strings"
	"testing"
)

// TestToolItemProbesGoWithTheGoSubcommand pins #202: every tool was probed
// with --version, but `go --version` exits 2 with a usage message, so a
// machine with a working Go was reported as "unusable".
//
// The assertion is on the argv the probe actually passes, which is the thing
// the fix changes: an assertion on the resulting status would pass on any
// machine whose fake happens to answer.
func TestToolItemProbesGoWithTheGoSubcommand(t *testing.T) {
	var got []string
	d := Diagnostics{
		LookPath: func(string) (string, error) { return "/usr/local/go/bin/go", nil },
		Run: func(_ context.Context, name string, args ...string) (ExecResult, error) {
			got = append([]string{name}, args...)
			return ExecResult{Stdout: "go version go1.24.0 linux/amd64"}, nil
		},
	}
	item := d.toolItem(context.Background(), "go")

	if len(got) != 2 || got[1] != "version" {
		t.Fatalf("go must be probed with the version subcommand, got argv %v", got)
	}
	if strings.Contains(strings.Join(got, " "), "--version") {
		t.Fatalf("go does not accept --version, got argv %v", got)
	}
	if item.Status != DiagnosticOK {
		t.Fatalf("a working Go must not be reported as %q: %+v", item.Value, item)
	}
}

// TestToolItemKeepsDashDashVersionForOtherTools pins that the Go special case
// did not change every other tool: --version stays the probe for tools that
// actually accept it.
func TestToolItemKeepsDashDashVersionForOtherTools(t *testing.T) {
	var got []string
	d := Diagnostics{
		LookPath: func(string) (string, error) { return "/usr/bin/git", nil },
		Run: func(_ context.Context, name string, args ...string) (ExecResult, error) {
			got = append([]string{name}, args...)
			return ExecResult{Stdout: "git version 2.51.0"}, nil
		},
	}
	d.toolItem(context.Background(), "git")

	if len(got) != 2 || got[1] != "--version" {
		t.Fatalf("git must keep the --version probe, got argv %v", got)
	}
}
