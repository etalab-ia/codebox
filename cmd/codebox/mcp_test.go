package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func stubMCPProject(t *testing.T, ids []string) {
	t.Helper()
	root := t.TempDir()
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), codebox.ProjectManifest{MCPConnectors: ids}); err != nil {
		t.Fatal(err)
	}
	old := discoverMCPProjectRootFn
	discoverMCPProjectRootFn = func() (string, error) { return root, nil }
	t.Cleanup(func() { discoverMCPProjectRootFn = old })
}

func TestMCPStatusWithoutManagedConnectorsDoesNotProbe(t *testing.T) {
	stubMCPProject(t, nil)
	oldProbe := probeRemoteMCPFn
	probeCalls := 0
	probeRemoteMCPFn = func(context.Context, *http.Client, codebox.MCPConnector) codebox.MCPHealth {
		probeCalls++
		return codebox.MCPHealth{}
	}
	t.Cleanup(func() { probeRemoteMCPFn = oldProbe })
	if code, err := mcpCommand([]string{"status"}); code != 0 || err != nil || probeCalls != 0 {
		t.Fatalf("mcp status: code=%d err=%v probes=%d", code, err, probeCalls)
	}
}

func TestMCPStatusReturnsFailureForUnverifiedEndpoint(t *testing.T) {
	stubMCPProject(t, []string{"data-gouv"})
	oldProbe := probeRemoteMCPFn
	probeRemoteMCPFn = func(context.Context, *http.Client, codebox.MCPConnector) codebox.MCPHealth {
		return codebox.MCPHealth{State: codebox.MCPOffline, Detail: "offline"}
	}
	t.Cleanup(func() { probeRemoteMCPFn = oldProbe })
	if code, err := mcpCommand([]string{"status"}); code != 1 || err == nil {
		t.Fatalf("mcp status: code=%d err=%v, want verification failure", code, err)
	}
}

func TestMCPStatusDoesNotProbeGuestLocalOrWithdrawnServersFromHost(t *testing.T) {
	stubMCPProject(t, []string{"playwright", "chrome-devtools"})
	oldProbe := probeRemoteMCPFn
	probeCalls := 0
	probeRemoteMCPFn = func(context.Context, *http.Client, codebox.MCPConnector) codebox.MCPHealth {
		probeCalls++
		return codebox.MCPHealth{State: codebox.MCPVerified}
	}
	t.Cleanup(func() { probeRemoteMCPFn = oldProbe })
	if code, err := mcpCommand([]string{"status"}); code != 0 || err != nil || probeCalls != 0 {
		t.Fatalf("mcp status: code=%d err=%v probes=%d", code, err, probeCalls)
	}
}
