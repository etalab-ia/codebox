package codebox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

const acpNativeQualificationEnv = "CODEBOX_ACP_NATIVE"

// TestACPNativeStreamRoundtrip is an explicit native smoke test, never an
// ordinary CI microVM launch. It proves the production non-TTY adapter and
// relay with a real process before OpenCode ACP is exposed in the next layer.
func TestACPNativeStreamRoundtrip(t *testing.T) {
	if os.Getenv(acpNativeQualificationEnv) != "1" {
		t.Skip("native ACP smoke test is disabled; set CODEBOX_ACP_NATIVE=1 on a provisioned Microsandbox host")
	}
	t.Setenv("MSB_BACKEND", "local")
	for _, name := range []string{"MSB_PATH", "MSB_LIBKRUNFW_PATH"} {
		t.Setenv(name, "")
		// The native resolver distinguishes an unset override from an empty one.
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unset %s: %v", name, err)
		}
	}
	// Reuse the production runtime and OCI cache; the sandbox itself remains
	// unique and disposable. No host credential or existing sandbox is used.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := ensureMSBRuntime(ctx, nil); err != nil {
		t.Fatalf("managed runtime download and verification: %v", err)
	}

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "codebox-acp-transport-" + hex.EncodeToString(nonce[:])
	client := sdkMSBClient{}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		if err := client.Remove(cleanupCtx, name); err != nil && !msb.IsKind(err, msb.ErrSandboxNotFound) {
			t.Errorf("remove owned ACP smoke sandbox %s: %v", name, err)
		}
	}()

	spec := msbSandboxSpec{
		Name:            name,
		Image:           "alpine:3.19",
		Isolation:       IsolationFull,
		Env:             map[string]string{},
		CPUs:            1,
		MemoryMB:        512,
		PortsConfigured: true,
		SealedWorkspace: true,
		StartScript:     "exec sleep 600\n",
	}
	if err := client.Create(ctx, spec); err != nil {
		t.Fatalf("create real ACP transport sandbox: %v", err)
	}
	stream, err := client.ExecStream(ctx, name, "cat", nil, "/workspace")
	if err != nil {
		t.Fatalf("start native non-TTY stream: %v", err)
	}

	input := "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"notification\"}\n"
	var stdout, stderr bytes.Buffer
	if err := relayACP(ctx, stream, io.NopCloser(strings.NewReader(input)), &stdout, &stderr); err != nil {
		t.Fatalf("native ACP byte relay: %v", err)
	}
	if got := stdout.String(); got != input {
		t.Fatalf("native stdout = %q, want exact input bytes %q", got, input)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("native stderr = %q, want empty diagnostics", got)
	}
	t.Log("real non-TTY Microsandbox stream preserved client bytes and exited cleanly on EOF")
}
