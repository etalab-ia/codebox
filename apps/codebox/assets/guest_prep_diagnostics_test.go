package assets

import (
	"strings"
	"testing"
)

func TestGuestPrepReportsInstallationStagesAndDownloads(t *testing.T) {
	cases := map[string][]string{
		"guest-prep.sh": {
			"codebox : installation des paquets système avec apk",
			"codebox : installation d'OpenCode ${CODEBOX_OPENCODE_VERSION} avec npm",
		},
		"pi-guest-prep.sh": {
			"codebox : installation des paquets système avec apk",
			"codebox : installation de Pi ${CODEBOX_PI_VERSION} avec npm",
			"codebox : écriture de la configuration Pi",
		},
		"browser-guest-prep.sh": {
			"codebox : mise à jour des index apt",
			"codebox : installation des paquets système et de Chromium avec apt",
			"codebox : téléchargement de Node.js ${CODEBOX_NODE_VERSION}",
			"codebox : installation d'OpenCode ${CODEBOX_OPENCODE_VERSION} avec npm",
			"codebox : installation des MCP navigateur avec npm",
		},
	}
	for name, snippets := range cases {
		data, err := Read(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(data), snippet) {
				t.Errorf("%s does not report %q", name, snippet)
			}
		}
	}
}
