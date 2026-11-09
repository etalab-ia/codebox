package justcode

import (
	"fmt"
	"strings"
)

// This file holds the guest-side half of Pi support (#176): the shell
// fragments the host runs in the guest to install the pinned release, to write
// the managed configuration, and to decide whether a launch resumes.
//
// Pi's global configuration lives in ~/.pi/agent (models.json, settings.json,
// skills, sessions). just-code owns the two configuration files and leaves
// everything else to Pi; a project's own .pi/ files remain the project's
// business, exactly like a project opencode.jsonc.
//
// The fragments are expressed through $HOME so the same text works in the
// macOS guest, the Lima guest and the Microsandbox guest, without the host
// having to know the guest user.

// piGuestInstallScript is a POSIX shell fragment that installs the pinned Pi
// release in the guest when it is missing or at another version. The guest
// needs npm on its PATH; every supported runtime's guest image provides it.
func piGuestInstallScript() string {
	return fmt.Sprintf(
		`if ! command -v pi >/dev/null 2>&1 || [ "$(pi --version 2>/dev/null)" != %s ]; then npm install -g %s@%s || exit 1; fi`,
		shellQuote(PiVersion), shellQuote(PiPackage), shellQuote(PiVersion))
}

// piGuestConfigScript is a POSIX shell fragment that writes the managed Pi
// configuration into the guest user's home.
//
// The JSON is written with printf and a single-quoted argument: shellQuote
// escapes any embedded quote, so the configuration cannot break out of the
// command it travels in.
func piGuestConfigScript(overlay ManagedOverlay) (string, error) {
	models, err := ComposePiModelsConfig(overlay)
	if err != nil {
		return "", err
	}
	settings, err := ComposePiSettingsConfig()
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		`if [ -z "$HOME" ]; then echo "HOME is not set in the guest" >&2; exit 1; fi`,
		`mkdir -p "$HOME/.pi/agent"`,
		`printf '%s\n' ` + shellQuote(models) + ` > "$HOME/.pi/agent/models.json"`,
		`printf '%s\n' ` + shellQuote(settings) + ` > "$HOME/.pi/agent/settings.json"`,
		`chmod 600 "$HOME/.pi/agent/models.json" "$HOME/.pi/agent/settings.json"`,
	}, " && "), nil
}

// piGuestSessionProbe is a POSIX shell fragment that prints a line when the
// guest holds at least one saved Pi session. Pi organises sessions by working
// directory, so the search is recursive; `head -n 1` stops at the first hit.
func piGuestSessionProbe() string {
	return `find "$HOME/.pi/agent/sessions" -name '*.jsonl' 2>/dev/null | head -n 1`
}

// piResumeArgs returns the flags that resume the most recent Pi session, or an
// empty slice when there is nothing to resume. Pi starts a fresh session when
// launched without them, so the caller can always pass the result through.
func piResumeArgs(hasSessions bool) []string {
	if !hasSessions {
		return nil
	}
	return []string{"--continue"}
}

// piAgentLaunch is the in-guest launch line for full isolation: source the
// 0600 secrets env file, cd into the workspace, exec the TUI. Both paths are
// shell-quoted: the Tart workspace share contains spaces.
func piAgentLaunch(secretsPath, workspaceDir string, resumeArgs []string) string {
	return fmt.Sprintf("set -a; . %s; set +a; cd %s; exec pi%s",
		shellQuote(secretsPath), shellQuote(workspaceDir), strings.Join(append([]string{""}, resumeArgs...), " "))
}
