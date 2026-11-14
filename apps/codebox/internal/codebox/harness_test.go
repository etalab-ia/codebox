package codebox

import (
	"strings"
	"testing"
)

func TestResolveHarness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    Harness
		wantErr bool
	}{
		{"empty means the default", "", HarnessOpenCode, false},
		{"opencode", "opencode", HarnessOpenCode, false},
		{"pi", "pi", HarnessPi, false},
		// The stored value is exact. A hand-edited manifest is not silently
		// reinterpreted: the wrong harness running would be worse than an
		// error the user can read.
		{"different case is rejected", "Pi", "", true},
		{"unknown value is rejected", "claude", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveHarness(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolveHarness(%q) = %q, want an error", tc.in, got)
				}
				if !strings.Contains(err.Error(), "opencode or pi") {
					t.Fatalf("error %q does not name the accepted surface", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveHarness(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ResolveHarness(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Pi has no server mode, so backend isolation would leave the host with
// nothing to attach to. The pair must be refused, and the message must name
// the flag that fixes it.
func TestValidateHarnessIsolation(t *testing.T) {
	err := ValidateHarnessIsolation(HarnessPi, IsolationBackend)
	if err == nil {
		t.Fatal("Pi with backend isolation was accepted")
	}
	if !strings.Contains(err.Error(), "--isolation full") {
		t.Fatalf("refusal %q does not name the fix", err)
	}
	if err := ValidateHarnessIsolation(HarnessPi, IsolationFull); err != nil {
		t.Fatalf("Pi with full isolation was refused: %v", err)
	}
	if err := ValidateHarnessIsolation(HarnessOpenCode, IsolationBackend); err != nil {
		t.Fatalf("OpenCode with backend isolation was refused: %v", err)
	}
	if !HarnessRequiresFullIsolation(HarnessPi) || HarnessRequiresFullIsolation(HarnessOpenCode) {
		t.Fatal("HarnessRequiresFullIsolation does not distinguish the two harnesses")
	}
}

// An unset field means "not read here" and must keep the historical default,
// because every project configured before the setting existed has no value.
func TestEffectiveHarnessDefaultsToOpenCode(t *testing.T) {
	var c Config
	if got := c.EffectiveHarness(); got != HarnessOpenCode {
		t.Fatalf("EffectiveHarness() = %q, want %q", got, HarnessOpenCode)
	}
	c.Harness = HarnessPi
	if got := c.EffectiveHarness(); got != HarnessPi {
		t.Fatalf("EffectiveHarness() = %q, want %q", got, HarnessPi)
	}
}

func TestHarnessDisplayNames(t *testing.T) {
	if got := HarnessOpenCode.DisplayName(); got != "OpenCode" {
		t.Fatalf("OpenCode display name = %q", got)
	}
	if got := HarnessPi.DisplayName(); got != "Pi" {
		t.Fatalf("Pi display name = %q", got)
	}
	if got := HarnessNames(); len(got) != 2 || got[0] != "opencode" || got[1] != "pi" {
		t.Fatalf("HarnessNames() = %v", got)
	}
}
