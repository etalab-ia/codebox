package codebox

import (
	"strings"
	"testing"
)

// The runtime-selection contract differs by platform: Windows defaults to and
// only supports microsandbox, so the cases are exercised per-platform rather
// than asserting behavior that cannot hold everywhere. resolveRuntimeOn takes
// the platform explicitly so the Windows cases run in normal (non-Windows) CI.
func TestResolveRuntimeOnWindows(t *testing.T) {
	got, err := resolveRuntimeOn("", "", "windows")
	if err != nil {
		t.Fatalf("resolveRuntimeOn(\"\", \"\", windows): %v", err)
	}
	if got != RuntimeMicrosandbox {
		t.Errorf("resolveRuntimeOn(\"\", \"\", windows) = %q, want microsandbox", got)
	}
	// Tart is macOS-only and agent-vm is macOS/Linux: on Windows both must
	// fail with a platform message that points at the runtime that does work
	// there, not a raw exec lookup error.
	for _, arg := range []struct{ flag, pref string }{
		{"--tart", ""},
		{"", "tart"},
		{"--agent-vm", ""},
		{"", "agent-vm"},
	} {
		_, err := resolveRuntimeOn(arg.flag, arg.pref, "windows")
		if err == nil {
			t.Fatalf("resolveRuntimeOn(%q, %q, windows): expected error", arg.flag, arg.pref)
		}
		if !strings.Contains(err.Error(), "microsandbox") {
			t.Errorf("resolveRuntimeOn(%q, %q, windows) = %v, want a message pointing at --microsandbox", arg.flag, arg.pref, err)
		}
	}
	if got, err := resolveRuntimeOn("--microsandbox", "", "windows"); err != nil || got != RuntimeMicrosandbox {
		t.Errorf("resolveRuntimeOn(--microsandbox, \"\", windows) = %q, %v", got, err)
	}
}

func TestResolveRuntimeFlag(t *testing.T) {
	cases := []struct {
		flag, pref string
		want       Runtime
	}{
		{"--tart", "", RuntimeTart},
		{"--microsandbox", "", RuntimeMicrosandbox},
		{"--agent-vm", "", RuntimeAgentVM},
		{"--tart", "microsandbox", RuntimeTart}, // explicit flag wins
		{"--microsandbox", "tart", RuntimeMicrosandbox},
		{"--agent-vm", "tart", RuntimeAgentVM},
	}
	for _, c := range cases {
		got, err := resolveRuntimeOn(c.flag, c.pref, "linux")
		if err != nil {
			t.Fatalf("resolveRuntimeOn(%q, %q, linux): %v", c.flag, c.pref, err)
		}
		if got != c.want {
			t.Errorf("resolveRuntimeOn(%q, %q, linux) = %q, want %q", c.flag, c.pref, got, c.want)
		}
	}
}

func TestResolveRuntimePreference(t *testing.T) {
	for _, name := range []string{"microsandbox", "tart", "agent-vm"} {
		got, err := resolveRuntimeOn("", name, "linux")
		if err != nil {
			t.Fatalf("resolveRuntimeOn(\"\", %q, linux): %v", name, err)
		}
		if string(got) != name {
			t.Errorf("resolveRuntimeOn(\"\", %q, linux) = %q", name, got)
		}
	}
	// Docker was removed: RUNTIME=docker must be rejected like any other
	// unknown runtime, on every platform.
	if _, err := resolveRuntimeOn("", "docker", "linux"); err == nil {
		t.Errorf("resolveRuntimeOn(\"\", \"docker\", linux): expected error")
	}
}

func TestResolveRuntimeErrors(t *testing.T) {
	cases := []struct{ flag, pref string }{
		{"--podman", ""},
		{"", "podman"},
		{"--", ""},
	}
	for _, c := range cases {
		if _, err := resolveRuntimeOn(c.flag, c.pref, "linux"); err == nil {
			t.Errorf("resolveRuntimeOn(%q, %q, linux): expected error", c.flag, c.pref)
		}
	}
	// An empty selection is never an error: Microsandbox is the built-in
	// default on every platform (P12).
	for _, goos := range []string{"linux", "darwin", "windows"} {
		got, err := resolveRuntimeOn("", "", goos)
		if err != nil {
			t.Errorf("resolveRuntimeOn(\"\", \"\", %s): %v", goos, err)
		}
		if got != RuntimeMicrosandbox {
			t.Errorf("resolveRuntimeOn(\"\", \"\", %s) = %q, want microsandbox", goos, got)
		}
	}
}

func TestSupportedRuntimesOn(t *testing.T) {
	if got := supportedRuntimesOn("windows"); len(got) != 1 || got[0] != RuntimeMicrosandbox {
		t.Errorf("supportedRuntimesOn(windows) = %v, want [microsandbox]", got)
	}
	if got := supportedRuntimesOn("linux"); len(got) != 3 || got[0] != RuntimeMicrosandbox || got[1] != RuntimeTart || got[2] != RuntimeAgentVM {
		t.Errorf("supportedRuntimesOn(linux) = %v, want [microsandbox tart agent-vm]", got)
	}
	if got := supportedRuntimesOn("darwin"); len(got) != 3 || got[0] != RuntimeMicrosandbox || got[1] != RuntimeTart || got[2] != RuntimeAgentVM {
		t.Errorf("supportedRuntimesOn(darwin) = %v, want [microsandbox tart agent-vm]", got)
	}
}
