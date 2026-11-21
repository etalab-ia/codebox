package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type fakeACPBackend struct {
	codebox.Backend
	startCalls int
	runCalls   int
	input      string
	startErr   error
}

func (b *fakeACPBackend) Start(context.Context) error {
	b.startCalls++
	return b.startErr
}

func (b *fakeACPBackend) IsRunning(context.Context) (bool, error) { return false, nil }

func (b *fakeACPBackend) RunACP(_ context.Context, stdin io.ReadCloser, stdout, _ io.Writer) error {
	defer stdin.Close()
	b.runCalls++
	input, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	b.input = string(input)
	_, err = stdout.Write(input)
	return err
}

func TestACPCmdStartsPreparedRuntimeAndMediatesInput(t *testing.T) {
	backend := &fakeACPBackend{}
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: backend,
		codebox.RuntimeTart:         backend,
		codebox.RuntimeAgentVM:      backend,
	})
	d.Runner = noLegacyLaunchRunner{}
	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"clientCapabilities\":{\"fs\":{\"readTextFile\":true,\"writeTextFile\":true},\"terminal\":true}}}\n" +
		"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"session/new\",\"params\":{\"cwd\":\"/host/project\",\"mcpServers\":[]}}\n"
	var output, diagnostics bytes.Buffer
	code, err := acpCmd(d, codebox.Config{Harness: codebox.HarnessOpenCode, Isolation: codebox.IsolationFull}, codebox.RuntimeMicrosandbox,
		io.NopCloser(strings.NewReader(input)), &output, &diagnostics)
	if err != nil || code != 0 {
		t.Fatalf("acpCmd = code %d, err %v", code, err)
	}
	if backend.startCalls != 1 || backend.runCalls != 1 {
		t.Fatalf("Start/RunACP calls = %d/%d, want 1/1", backend.startCalls, backend.runCalls)
	}
	if output.String() != backend.input {
		t.Fatal("protocol output includes bytes other than the mediated ACP stream")
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("ACP output lines = %d, want 2: %q", len(lines), output.String())
	}
	var initialize, session map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initialize); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &session); err != nil {
		t.Fatal(err)
	}
	capabilities := initialize["params"].(map[string]any)["clientCapabilities"].(map[string]any)
	if capabilities["terminal"] != false {
		t.Fatalf("terminal capability = %#v, want false", capabilities["terminal"])
	}
	fs := capabilities["fs"].(map[string]any)
	if fs["readTextFile"] != false || fs["writeTextFile"] != false {
		t.Fatalf("filesystem capabilities = %#v, want disabled", fs)
	}
	if session["params"].(map[string]any)["cwd"] != "/workspace" {
		t.Fatalf("session cwd = %#v, want /workspace", session["params"].(map[string]any)["cwd"])
	}
}

func TestACPCmdRejectsUnsupportedConfigurationBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		name    string
		runtime codebox.Runtime
		config  codebox.Config
	}{
		{name: "runtime", runtime: codebox.RuntimeTart, config: codebox.Config{Harness: codebox.HarnessOpenCode, Isolation: codebox.IsolationFull}},
		{name: "isolation", runtime: codebox.RuntimeMicrosandbox, config: codebox.Config{Harness: codebox.HarnessOpenCode, Isolation: codebox.IsolationBackend}},
		{name: "harness", runtime: codebox.RuntimeMicrosandbox, config: codebox.Config{Harness: codebox.HarnessPi, Isolation: codebox.IsolationFull}},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, err := acpCmd(nil, test.config, test.runtime, io.NopCloser(strings.NewReader("")), io.Discard, io.Discard)
			if err == nil || code != 2 {
				t.Fatalf("acpCmd = code %d, err %v, want configuration refusal", code, err)
			}
		})
	}
}

func TestACPParseAndLaunchClassification(t *testing.T) {
	parsed, err := parseArgs([]string{"acp"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if parsed.action != "acp" || !isLaunchAction(parsed.action) {
		t.Fatalf("ACP action/classification = %q / %v", parsed.action, isLaunchAction(parsed.action))
	}
}
