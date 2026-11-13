package codebox

import (
	"fmt"
	"strings"
	"testing"
)

func TestResolveIsolation(t *testing.T) {
	cases := []struct {
		name       string
		flag, pref string
		want       Isolation
	}{
		// P12: the zero-flag default is full, so the agent (and its provider
		// credentials) lives in the sandbox unless the user asks otherwise.
		{"default is full", "", "", IsolationFull},
		{"flag backend", "backend", "", IsolationBackend},
		{"flag full", "full", "", IsolationFull},
		{"flag beats preference", "full", "backend", IsolationFull},
		{"preference used when no flag", "", "full", IsolationFull},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveIsolation(c.flag, c.pref)
			if err != nil {
				t.Fatalf("ResolveIsolation(%q, %q): %v", c.flag, c.pref, err)
			}
			if got != c.want {
				t.Errorf("ResolveIsolation(%q, %q) = %q, want %q", c.flag, c.pref, got, c.want)
			}
		})
	}
}

func TestResolveIsolationRejectsUnknown(t *testing.T) {
	for _, value := range []string{"ful", "Full", "back-end", "0"} {
		_, err := ResolveIsolation(value, "")
		if err == nil {
			t.Fatalf("ResolveIsolation(%q) accepted an unknown value", value)
		}
		if !strings.Contains(err.Error(), "backend") || !strings.Contains(err.Error(), "full") {
			t.Errorf("error %q must list the accepted surface", err)
		}
	}
	// An invalid preference must also be rejected, not silently defaulted.
	if _, err := ResolveIsolation("", "sometimes"); err == nil {
		t.Fatal("an invalid ISOLATION preference must be rejected")
	}
}

func TestLoadConfigIsolation(t *testing.T) {
	// P12: the zero-flag default is full.
	if got := LoadConfig(lookupFrom(nil)).Isolation; got != IsolationFull {
		t.Errorf("default Isolation = %q, want full", got)
	}
	cfg := LoadConfig(lookupFrom(map[string]string{"ISOLATION": "backend"}))
	if cfg.Isolation != IsolationBackend {
		t.Errorf("Isolation = %q, want backend (the explicit escape still works)", cfg.Isolation)
	}
	if cfg.IsolationErr != nil {
		t.Fatalf("unexpected IsolationErr: %v", cfg.IsolationErr)
	}
}

// TestResolveIsolationLevelPrecedence pins the flag-over-environment
// contract: an explicit flag always wins, including over an invalid ISOLATION
// value, and an invalid preference is surfaced only when no flag was given.
func TestResolveIsolationLevelPrecedence(t *testing.T) {
	badEnv := fmt.Errorf("ISOLATION must be backend or full, got \"sometimes\"")

	got, err := ResolveIsolationLevel("", IsolationBackend, nil)
	if err != nil || got != IsolationBackend {
		t.Fatalf("no flag, valid env: got (%q, %v)", got, err)
	}
	got, err = ResolveIsolationLevel("full", IsolationBackend, nil)
	if err != nil || got != IsolationFull {
		t.Fatalf("flag over env preference: got (%q, %v)", got, err)
	}
	// The recovery path: a typo in .env must not block an explicit flag.
	got, err = ResolveIsolationLevel("backend", "", badEnv)
	if err != nil || got != IsolationBackend {
		t.Fatalf("explicit flag must beat an invalid environment value: got (%q, %v)", got, err)
	}
	got, err = ResolveIsolationLevel("full", "", badEnv)
	if err != nil || got != IsolationFull {
		t.Fatalf("explicit flag must beat an invalid environment value: got (%q, %v)", got, err)
	}
	// Without a flag, the invalid preference is reported rather than ignored.
	if _, err := ResolveIsolationLevel("", "", badEnv); err == nil {
		t.Fatal("an invalid ISOLATION must be surfaced when no flag is given")
	}
	// An invalid flag value is always an error, even with a valid preference.
	if _, err := ResolveIsolationLevel("sometimes", IsolationFull, nil); err == nil {
		t.Fatal("an invalid --isolation value must be rejected")
	}
}

// TestLoadConfigInvalidIsolationIsNotFatal records the deferred-validation
// contract: an invalid ISOLATION must not break commands that never read it.
func TestLoadConfigInvalidIsolationIsNotFatal(t *testing.T) {
	cfg := LoadConfig(lookupFrom(map[string]string{"ISOLATION": "sometimes"}))
	if cfg.IsolationErr == nil {
		t.Fatal("expected IsolationErr for an invalid value")
	}
	if cfg.Isolation != IsolationFull {
		t.Errorf("Isolation = %q, want the full default despite the error", cfg.Isolation)
	}
}
