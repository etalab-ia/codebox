package main

import (
	"context"
	"fmt"
	"os"

	"github.com/etalab-ia/codebox/internal/codebox"
)

var discoverMCPProjectRootFn = func() (string, error) {
	project, err := codebox.DiscoverProject(".")
	if err != nil {
		return "", err
	}
	return project.Root, nil
}

var probeRemoteMCPFn = codebox.ProbeRemoteMCP

func mcpCommand(args []string) (int, error) {
	if len(args) != 1 || args[0] != "status" {
		return 2, fmt.Errorf("Usage: codebox mcp status")
	}
	projectRoot, err := discoverMCPProjectRootFn()
	if err != nil {
		return 1, err
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		return 1, err
	}
	ids, err := codebox.ValidateMCPConnectorIDs(manifest.MCPConnectors)
	if err != nil {
		return 1, err
	}
	if len(ids) == 0 {
		fmt.Println("No managed MCPs configured.")
		return 0, nil
	}
	failed := false
	for _, id := range ids {
		if browser, ok := codebox.BrowserMCPByID(id); ok {
			fmt.Fprintf(os.Stdout, "%-16s guest-local configured (%s@%s); not probed from host\n", id, browser.Package, browser.Version)
			continue
		}
		if reason, removed := codebox.RemovedMCPIDs[id]; removed {
			fmt.Fprintf(os.Stdout, "%-16s inactive: %s\n", id, reason)
			continue
		}
		connector := connectorByID(id)
		health := probeRemoteMCPFn(context.Background(), nil, connector)
		fmt.Fprintf(os.Stdout, "%-10s %-28s configured -> %s (%s): %s\n", id, connector.Endpoint, health.State, connector.HealthAction, health.Detail)
		if health.State != codebox.MCPVerified {
			failed = true
		}
	}
	if failed {
		return 1, fmt.Errorf("one or more remote MCPs could not be verified")
	}
	return 0, nil
}

func connectorByID(id string) codebox.MCPConnector {
	for _, connector := range codebox.MCPConnectors() {
		if connector.ID == id {
			return connector
		}
	}
	return codebox.MCPConnector{}
}
