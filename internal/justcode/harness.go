package justcode

import "fmt"

// Harness identifies the agent harness just-code installs, configures and
// launches inside the guest. It replaced the earlier implicit choice: the
// wizard asked for a harness since #333 but nothing read the answer, so a
// project could not actually run anything but OpenCode (#176).
type Harness string

const (
	// HarnessOpenCode is OpenCode v2: the managed configuration layer rides
	// OPENCODE_CONFIG_CONTENT, and in backend isolation the guest serves
	// `opencode serve` while the host attaches the TUI.
	HarnessOpenCode Harness = "opencode"
	// HarnessPi is the Pi coding agent (@mariozechner/pi-coding-agent),
	// configured through ~/.pi/agent/models.json. Pi has no server mode: it is
	// a single-process TUI, so it only ever runs with full isolation, where the
	// whole agent already lives in the guest.
	HarnessPi Harness = "pi"
)

// DefaultHarness is the harness a project that never chose one uses. It is
// OpenCode for compatibility: every project configured before this setting
// existed runs OpenCode.
const DefaultHarness = HarnessOpenCode

// allHarnesses is the accepted harness surface, in display order.
var allHarnesses = []Harness{HarnessOpenCode, HarnessPi}

// The Pi release just-code installs, pinned exactly. The guest must not track
// a moving release: an unqualified Pi appearing inside a qualified guest would
// invalidate the evidence the release rests on, and Pi's request shape (no
// tool_choice, array content parts) is precisely what had to be re-verified
// before this pin.
const (
	// PiPackage is the npm package that provides the `pi` CLI.
	PiPackage = "@mariozechner/pi-coding-agent"
	// PiVersion is the pinned release.
	PiVersion = "0.73.1"
	// PiVersionEnv carries the pin into the Microsandbox guest, where the prep
	// script installs it. The guest never invents a version.
	PiVersionEnv = "JUST_CODE_PI_VERSION"
	// PiModelsEnv carries the composed models.json into the Microsandbox
	// guest. Models.json is non-secret configuration, exactly like
	// OPENCODE_CONFIG_CONTENT, so it may ride the sandbox environment.
	PiModelsEnv = "JUST_CODE_PI_MODELS_JSON"
	// PiSettingsEnv carries the composed settings.json the same way.
	PiSettingsEnv = "JUST_CODE_PI_SETTINGS_JSON"
	// PiConfigFingerprintEnv carries a digest of both composed documents into
	// the Microsandbox guest. The prep writes it back to a marker once the two
	// files are on disk, and the readiness probe requires that marker, so a
	// guest whose configuration is still being written is not reported ready.
	PiConfigFingerprintEnv = "JUST_CODE_PI_CONFIG_FINGERPRINT"
	// PiSkipVersionCheckEnv disables Pi's own update check inside the guest.
	// A managed guest has no update path (see #178 for OpenCode's equivalent):
	// the release is the pinned one above, and prompting about a newer Pi would
	// only suggest a change the guest cannot honour.
	PiSkipVersionCheckEnv = "PI_SKIP_VERSION_CHECK"
)

// EffectiveHarness returns the harness a Config selects. The field is empty
// on paths that never read a project manifest (and in tests that build a
// Config directly), so those keep the historical OpenCode behaviour.
func (c Config) EffectiveHarness() Harness {
	if c.Harness == "" {
		return DefaultHarness
	}
	return c.Harness
}

// DisplayName is the harness name as shown to a user, in the launch report,
// the wizard review and the diagnostics.
func (h Harness) DisplayName() string {
	if h == HarnessPi {
		return "Pi"
	}
	return "OpenCode"
}

// ResolveHarness validates an explicit harness value. An empty value means
// "not chosen here" and resolves to the default, mirroring ResolveRuntime and
// ResolveIsolation: a caller passing "" wants the default, not an error.
func ResolveHarness(value string) (Harness, error) {
	if value == "" {
		return DefaultHarness, nil
	}
	switch Harness(value) {
	case HarnessOpenCode, HarnessPi:
		return Harness(value), nil
	default:
		return "", fmt.Errorf("expected a harness of opencode or pi, got %q", value)
	}
}

// HarnessNames lists the accepted harness values, for messages.
func HarnessNames() []string {
	out := make([]string, 0, len(allHarnesses))
	for _, h := range allHarnesses {
		out = append(out, string(h))
	}
	return out
}

// HarnessRequiresFullIsolation reports whether the harness can only run with
// the whole agent inside the guest. Pi has no server mode, so backend
// isolation — where the host TUI attaches to a guest server — has nothing to
// attach to.
func HarnessRequiresFullIsolation(h Harness) bool {
	return h == HarnessPi
}

// ValidateHarnessIsolation rejects a harness/isolation pair the guest cannot
// run, before anything is started. It is the single place that rule lives, so
// the wizard and the launch path cannot drift apart.
func ValidateHarnessIsolation(h Harness, iso Isolation) error {
	if HarnessRequiresFullIsolation(h) && iso != IsolationFull {
		return fmt.Errorf("%s requires full isolation: it has no server mode, so there is no backend for the host to attach to; use --isolation full", h)
	}
	return nil
}
