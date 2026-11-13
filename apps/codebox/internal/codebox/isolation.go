package codebox

import "fmt"

// Isolation identifies where the OpenCode agent lives: only its backend
// process inside the sandbox (backend), or the whole agent including the TUI
// (full).
type Isolation string

const (
	// IsolationBackend is the historical model: `opencode serve` inside the
	// sandbox, `opencode --server <url>` from the host (v2).
	IsolationBackend Isolation = "backend"
	// IsolationFull runs the whole agent — process, TUI, sessions, config and
	// provider credentials — inside the sandbox. The host is only a terminal
	// passthrough.
	IsolationFull Isolation = "full"
)

// allIsolations is the accepted --isolation / ISOLATION surface.
var allIsolations = []Isolation{IsolationBackend, IsolationFull}

// ResolveIsolation picks an isolation level from the explicit --isolation flag
// and the ISOLATION environment preference, mirroring ResolveRuntime: the
// flag wins, an empty preference defaults to full, and any other value is
// rejected with a message listing the accepted surface.
//
// The default is full since P12: the zero-flag path must be the one where the
// agent — and therefore its provider credentials — lives inside the sandbox,
// not the one where the host runs it and the guest is merely a backend.
// ISOLATION=backend (or --isolation backend) remains the explicit escape.
func ResolveIsolation(flag, preference string) (Isolation, error) {
	value := flag
	if value == "" {
		value = preference
	}
	if value == "" {
		return IsolationFull, nil
	}
	switch Isolation(value) {
	case IsolationBackend, IsolationFull:
		return Isolation(value), nil
	default:
		return "", fmt.Errorf("expected --isolation backend or --isolation full (ISOLATION must be backend or full), got %q", value)
	}
}

// ResolveIsolationLevel resolves the effective isolation level from the
// explicit --isolation flag plus the ISOLATION preference and the deferred
// error LoadConfig recorded for it. An explicit flag always wins, including
// over an invalid ISOLATION value, so a typo in .env can always be recovered
// from at the command line. Without a flag, an invalid preference is surfaced
// rather than silently replaced by the default.
func ResolveIsolationLevel(flag string, preference Isolation, preferenceErr error) (Isolation, error) {
	if flag != "" {
		return ResolveIsolation(flag, "")
	}
	if preferenceErr != nil {
		return "", preferenceErr
	}
	return ResolveIsolation("", string(preference))
}
