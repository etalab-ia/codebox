package codebox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

const msbHardwareQualificationEnv = "CODEBOX_MSB_HARDWARE_QUALIFICATION"

// TestMSBHardwareQualification is never part of ordinary CI. The manual
// workflow opts in on an explicitly provisioned Linux/KVM runner, so running
// paid hardware remains a deliberate decision. It exercises the production
// managed installer and real VM lifecycle rather than a fake msb client.
func TestMSBHardwareQualification(t *testing.T) {
	if os.Getenv(msbHardwareQualificationEnv) != "1" {
		t.Skip("manual hardware qualification is disabled; set CODEBOX_MSB_HARDWARE_QUALIFICATION=1 only on a provisioned KVM runner")
	}
	if runtime.GOOS != "linux" {
		t.Fatalf("manual Scaleway qualification requires Linux, got %s", runtime.GOOS)
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("manual qualification requires read/write /dev/kvm: %v", err)
	}
	if err := kvm.Close(); err != nil {
		t.Fatalf("close /dev/kvm probe: %v", err)
	}

	t.Setenv("MSB_BACKEND", "local")
	t.Setenv("MSB_PATH", "")
	t.Setenv("MSB_LIBKRUNFW_PATH", "")
	t.Setenv("MSB_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := ensureMSBRuntime(ctx, nil); err != nil {
		t.Fatalf("managed runtime download and verification: %v", err)
	}
	binary, err := msbRuntimeBinary()
	if err != nil {
		t.Fatal(err)
	}
	version, err := installedMSBVersion(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	if version != msbRuntimeVersion || msb.SDKVersion() != msbRuntimeVersion {
		t.Fatalf("SDK/runtime/binary version mismatch: SDK=%s runtime=%s binary=%s", msb.SDKVersion(), msbRuntimeVersion, version)
	}
	t.Logf("managed SDK/runtime/binary version %s verified", version)

	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "codebox-hardware-" + hex.EncodeToString(nonce[:])
	client := sdkMSBClient{}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cleanupCancel()
		if err := client.Remove(cleanupCtx, name); err != nil && !msb.IsKind(err, msb.ErrSandboxNotFound) {
			t.Errorf("remove owned qualification sandbox %s: %v", name, err)
		}
	}()

	spec := msbSandboxSpec{
		Name:            name,
		Image:           "docker.io/library/alpine:3.22",
		Isolation:       IsolationFull,
		Env:             map[string]string{},
		CPUs:            1,
		MemoryMB:        512,
		PortsConfigured: true,
		SealedWorkspace: true,
		StartScript:     "exec sleep 600\n",
	}
	if err := client.Create(ctx, spec); err != nil {
		t.Fatalf("create real Microsandbox VM: %v", err)
	}
	marker := "hardware-" + hex.EncodeToString(nonce[:])
	stdout, stderr, code, err := client.ExecCapture(ctx, name, "printf '%s\n' '"+marker+"' > /workspace/qualification-marker && cat /workspace/qualification-marker")
	if err != nil || code != 0 || strings.TrimSpace(stdout) != marker {
		t.Fatalf("write/read owned workspace: code=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}
	if err := client.Stop(ctx, name); err != nil {
		t.Fatalf("stop real Microsandbox VM: %v", err)
	}
	if err := client.Start(ctx, name); err != nil {
		t.Fatalf("restart real Microsandbox VM: %v", err)
	}
	stdout, stderr, code, err = client.ExecCapture(ctx, name, "cat /workspace/qualification-marker")
	if err != nil || code != 0 || strings.TrimSpace(stdout) != marker {
		t.Fatalf("workspace did not survive restart: code=%d stdout=%q stderr=%q err=%v", code, stdout, stderr, err)
	}
	t.Log("real VM boot, exec, owned-workspace write, restart persistence and version checks passed")
}
