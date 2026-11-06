package justcode

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// MCPConnector is a curated remote integration. Credentials are intentionally
// not part of its configuration: Context7 permits anonymous access, and its
// optional key remains an independently approved host binding.
type MCPConnector struct {
	ID           string
	Name         string
	Description  string
	Endpoint     string
	Destination  string
	Transport    string
	HealthAction string
	Credential   string
}

// BrowserMCP is a version-pinned local MCP process installed in the selected
// guest. It is kept separate from remote endpoints because it has no URL or
// host-side health probe.
type BrowserMCP struct {
	ID          string
	Name        string
	Description string
	Package     string
	Version     string
	Command     string
	Args        []string
}

type MCPSelection struct {
	ID          string
	Description string
	Local       bool
}

// Context7 was withdrawn from the catalogue because the service is private
// and outside the State (#168). Playwright duplicated chrome-devtools (#175).
// The ids stay valid for manifests written before the withdrawal: start and
// doctor keep reading them, only new selections are refused.
const removedMCPExplanation = "n'est plus proposé par just-code"

// RemovedMCPIDs lists catalogue entries withdrawn from the offering, with the
// reason shown to the user when a previous selection still references them.
var RemovedMCPIDs = map[string]string{
	"context7": removedMCPExplanation + " (service privé hors de l'État) ; la sélection existante est conservée mais inactive",
	"playwright": removedMCPExplanation +
		" (doublon de chrome-devtools pour tester une application web) ; la sélection existante est conservée mais inactive",
}

// BrowserDownloadNote announces the guest browser cost while the chrome-devtools
// box is checked. Figures await Luis's final values; the disk figure is the P03
// measurement already cited by BrowserResourceGuidance and the memory figure is
// the default guest allocation.
const BrowserDownloadNote = "Le navigateur est téléchargé dans la machine isolée au premier lancement (environ 2,6 Go). Prévoyez au moins 4 Go de mémoire."

// DefaultFreshMCPIDs is the pre-checked selection for a fresh initialization
// (no manifest, no flag): data-gouv is offered by default (#168).
var DefaultFreshMCPIDs = []string{"data-gouv"}

// MCPStepTitle is the wizard screen title from #168. The live screens compute
// the counter with initStepPrefix (#312, renumbered to five steps by #333);
// this value is the beginner-path spelling other packages and tests reuse.
const MCPStepTitle = "[4/5] MCP (facultatif)"

// MCPStepIntro is the one-line explanation printed under the MCP screen
// title. The line wizard and the huh form both render it (#168 texts, QA
// #301 bloquant 2), so the two renders cannot diverge again.
const MCPStepIntro = "Un MCP donne à l'agent l'accès à un service ou à un outil."

// ProbeRemoteMCPFunc probes a remote connector with an MCP initialize
// handshake. The init wizard uses it for the review screen's reachability
// line; nil skips the probe (offline runs, tests). It returns nil when the
// endpoint answered the handshake, a descriptive error otherwise.
var ProbeRemoteMCPFunc = func(ctx context.Context, connector MCPConnector) error {
	health := ProbeRemoteMCP(ctx, nil, connector)
	// MCPConfigured is only the initial state of a probe; a handshake that
	// succeeded always ends at MCPVerified. Every other terminal state
	// (offline, authentication-required, access-denied, drift) is a failure
	// worth showing on the review screen.
	if health.State != MCPVerified {
		return fmt.Errorf("%s (%s)", health.State, health.Detail)
	}
	return nil
}

var mcpCatalogue = map[string]MCPConnector{
	"data-gouv": {
		ID: "data-gouv", Name: "data-gouv",
		Description:  "rechercher et lire les données de data.gouv.fr",
		Endpoint:     "https://mcp.data.gouv.fr/mcp",
		Destination:  "data.gouv.fr",
		Transport:    "Streamable HTTP",
		HealthAction: "MCP initialize",
		Credential:   "none",
	},
}

// MCPConnectors lists the curated catalogue in stable display order.
func MCPConnectors() []MCPConnector {
	return []MCPConnector{mcpCatalogue["data-gouv"]}
}

var browserMPCCatalogue = map[string]BrowserMCP{
	"chrome-devtools": {
		ID: "chrome-devtools", Name: "chrome-devtools",
		Description: "navigateur dans la machine isolée, pour tester une application web : console, réseau, performances",
		Package:     "chrome-devtools-mcp", Version: "1.10.1", Command: "chrome-devtools-mcp",
		Args: []string{"--browserUrl", "http://127.0.0.1:9222"},
	},
}

// BrowserMCPs lists the individually selectable guest-local browser tools.
func BrowserMCPs() []BrowserMCP {
	return []BrowserMCP{browserMPCCatalogue["chrome-devtools"]}
}

func BrowserMCPByID(id string) (BrowserMCP, bool) {
	connector, ok := browserMPCCatalogue[id]
	return connector, ok
}

func browserMCPIDs(ids []string) []string {
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	var out []string
	for _, connector := range BrowserMCPs() {
		if selected[connector.ID] {
			out = append(out, connector.ID)
		}
	}
	return out
}

// HasBrowserMCPSelection reports whether a selection requires the browser guest profile.
func HasBrowserMCPSelection(ids []string) bool {
	return len(browserMCPIDs(ids)) > 0
}

func browserMCPInstallPackages(ids []string) []string {
	var packages []string
	for _, id := range browserMCPIDs(ids) {
		connector := browserMPCCatalogue[id]
		packages = append(packages, connector.Package+"@"+connector.Version)
	}
	return packages
}

func browserMCPBinaries(ids []string) []string {
	var binaries []string
	for _, id := range browserMCPIDs(ids) {
		binaries = append(binaries, browserMPCCatalogue[id].Command)
	}
	return binaries
}

// MCPSelections returns the full user-facing choice list in stable order.
func MCPSelections() []MCPSelection {
	choices := make([]MCPSelection, 0, len(mcpCatalogue)+len(browserMPCCatalogue))
	for _, connector := range MCPConnectors() {
		choices = append(choices, MCPSelection{ID: connector.ID, Description: connector.Description})
	}
	for _, connector := range BrowserMCPs() {
		choices = append(choices, MCPSelection{ID: connector.ID, Description: connector.Description, Local: true})
	}
	return choices
}

// ValidateMCPConnectorIDs validates and canonicalizes a user selection.
// Selections withdrawn from the catalogue stay readable so manifests written
// before the withdrawal keep starting; new user input goes through
// ValidateMCPNewSelection, which refuses them.
func ValidateMCPConnectorIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if _, remote := mcpCatalogue[id]; !remote {
			if _, local := browserMPCCatalogue[id]; !local {
				if _, removed := RemovedMCPIDs[id]; !removed {
					return nil, fmt.Errorf("unknown managed MCP %q (managed catalogue: data-gouv, chrome-devtools)", id)
				}
			}
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ValidateMCPNewSelection validates an interactive or flag selection: every id
// must be currently offered. Withdrawn ids are named with their reason instead
// of failing as unknown.
func ValidateMCPNewSelection(ids []string) ([]string, error) {
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if reason, removed := RemovedMCPIDs[id]; removed {
			return nil, fmt.Errorf("le MCP %q est refusé : %s", id, reason)
		}
	}
	return ValidateMCPConnectorIDs(ids)
}

func containsMCPConnector(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

func mcpOverlay(ids []string) (map[string]any, error) {
	valid, err := ValidateMCPConnectorIDs(ids)
	if err != nil {
		return nil, err
	}
	// The managed entries are anonymous by design: no headers, no env
	// reference. The proxied CONTEXT7_API_KEY binding serves user-owned
	// `mcp` entries in the guest's own config, not this curated overlay.
	entries := make(map[string]any, len(valid))
	for _, id := range valid {
		connector, ok := mcpCatalogue[id]
		if !ok {
			continue
		}
		entries[connector.Name] = map[string]any{
			"type":    "remote",
			"url":     connector.Endpoint,
			"enabled": true,
		}
	}
	return entries, nil
}

func browserMCPOverlay(ids []string) (map[string]any, error) {
	valid, err := ValidateMCPConnectorIDs(ids)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]any)
	for _, id := range valid {
		connector, ok := browserMPCCatalogue[id]
		if !ok {
			continue
		}
		command := append([]string{connector.Command}, connector.Args...)
		entries[connector.Name] = map[string]any{
			"type":    "local",
			"command": command,
			"enabled": true,
		}
	}
	return entries, nil
}

func managedMCPOverlay(ids []string) (map[string]any, error) {
	remote, err := mcpOverlay(ids)
	if err != nil {
		return nil, err
	}
	local, err := browserMCPOverlay(ids)
	if err != nil {
		return nil, err
	}
	return deepMergeMaps(remote, local), nil
}

func canonicalMCPEntry(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
