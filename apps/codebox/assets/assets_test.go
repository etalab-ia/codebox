package assets

import (
	"strings"
	"testing"
)

func TestReadEveryAsset(t *testing.T) {
	for _, name := range Files {
		data, err := Read(name)
		if err != nil {
			t.Fatalf("Read(%q): %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("asset %s is empty", name)
		}
	}
}

func TestManagedGuestOpenCodeUsesPrivateNPMPrefix(t *testing.T) {
	for _, name := range []string{"guest-prep.sh", "browser-guest-prep.sh"} {
		data, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		script := string(data)
		for _, want := range []string{
			`npm install --global --prefix /opt/codebox/opencode "@opencode/cli@${CODEBOX_OPENCODE_VERSION}"`,
			`ln -sf /opt/codebox/opencode/bin/opencode /usr/local/bin/opencode`,
			`test "$(opencode --version)" = "opencode v${CODEBOX_OPENCODE_VERSION}"`,
		} {
			// The browser script uses the same expansion without braces at this assertion.
			if strings.Contains(want, `test `) && name == "browser-guest-prep.sh" {
				want = `test "$(opencode --version)" = "opencode v$CODEBOX_OPENCODE_VERSION"`
			}
			if !strings.Contains(script, want) {
				t.Errorf("%s missing namespace-safe pinned v2 install: %s", name, want)
			}
		}
		if strings.Contains(script, "npm install --global --force") {
			t.Fatalf("%s must not force overwrite another package's bin", name)
		}
	}
}

func TestReadRejectsUnknownAsset(t *testing.T) {
	if _, err := Read("unknown"); err == nil {
		t.Fatal("expected an error for an unknown asset")
	}
}

// TestGuestPrepMessagesAreFrench pins the user-facing prose of the guest
// provisioning scripts. Their output surfaces in provisioning logs, so it must
// stay French like the rest of the CLI; identifiers, commands, paths and
// environment variable names are deliberately preserved verbatim.
func TestGuestPrepMessagesAreFrench(t *testing.T) {
	want := map[string][]string{
		"agentvm-base-prep.sh": {
			`codebox : installation des paquets de base…`,
			`codebox : installation de Node.js ${NODE_MAJOR}…`,
			`codebox : installation d'OpenCode…`,
			`codebox : OpenCode n'a pas été installé dans \$HOME/.opencode/bin/opencode`,
			`codebox : opencode est introuvable dans le PATH système après l'installation`,
			`codebox : provisionnement du template de base terminé.`,
		},
		"pi-guest-prep.sh": {
			`pi-guest-prep : la variable HOME n'est pas définie dans l'invité`,
			`pi-guest-prep : la configuration Pi gérée est absente de l'environnement de l'invité`,
			`pi-guest-prep : l'empreinte de la configuration Pi gérée est absente de l'environnement de l'invité`,
		},
		"browser-guest-prep.sh": {
			`Chromium n'est pas qualifié pour $(uname -m)`,
			`Node.js ${CODEBOX_NODE_VERSION} n'est pas qualifié pour $(uname -m)`,
		},
	}
	// The English wording these messages replaced must not come back.
	english := []string{
		"installing base packages",
		"installing OpenCode",
		"did not install to",
		"is not resolvable on the system PATH",
		"provisioning complete",
		"HOME is not set in the guest",
		"is missing from the guest environment",
		"is not qualified for",
	}

	for name, snippets := range want {
		data, err := Read(name)
		if err != nil {
			t.Fatalf("Read(%q): %v", name, err)
		}
		script := string(data)
		for _, snippet := range snippets {
			if !strings.Contains(script, snippet) {
				t.Errorf("%s missing French user-facing message: %s", name, snippet)
			}
		}
		for _, snippet := range english {
			if strings.Contains(script, snippet) {
				t.Errorf("%s still contains English user-facing prose: %s", name, snippet)
			}
		}
	}
}
