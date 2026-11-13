package codebox

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
	"golang.org/x/mod/modfile"
)

// sdkModulePath is the Microsandbox SDK module, shared by this build and the
// separate native qualification kit module so both pins stay visible.
const sdkModulePath = "github.com/superradcompany/microsandbox/sdk/go"

// Test the guest loader itself, not ParseProjectDotenv: the host importer
// deliberately disables interpolation, but guest applications may not.
func TestOpaqueProjectSecretMarkersSurviveGuestGodotenv(t *testing.T) {
	markers := []string{
		"__CODEBOX_SECRET_COMPONENT_A_API_KEY__",
		"__CODEBOX_SECRET_COMPONENT_B_API_KEY__",
	}
	for _, marker := range markers {
		for _, quote := range []string{"", "'", "\""} {
			values, err := godotenv.Unmarshal("API_KEY=" + quote + marker + quote + "\n")
			if err != nil || values["API_KEY"] != marker {
				t.Fatalf("guest loader changed an opaque marker: quote=%q err=%v", quote, err)
			}
		}
	}
}

func TestDollarProjectSecretMarkerIsNotGuestGodotenvSafe(t *testing.T) {
	const marker = "$MSB_CODEBOX_SECRET_A"
	values, err := godotenv.Unmarshal("API_KEY=" + marker + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if values["API_KEY"] != "" {
		t.Fatal("expected an undefined dollar reference to be expanded away")
	}
}

// Validate evidence integrity and agreement with the current runtime pins.
// This does not boot a VM or turn CI into native qualification evidence.
func TestProjectSecretNativeEvidenceMatchesRuntimePins(t *testing.T) {
	if MSBSDKVersion() != "0.7.14" || msbRuntimeVersion != "0.7.14" {
		t.Fatal("SDK/runtime changed; requalify and update the project-secret native evidence")
	}
	artifact, err := msbRuntimeArtifactFor("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"darwin-arm64-run-1.txt", "darwin-arm64-run-2.txt"} {
		path := filepath.Join("..", "..", "tests", "integration", "project-secrets", "native", "evidence", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if fmt.Sprintf("%x", hash) != "f334a086b7344549edf3afb75bd75ca390d36a99305defd7d248197c54ff7266" {
			t.Fatalf("native evidence %s changed", name)
		}
		text := string(data)
		if !strings.Contains(text, "sdk=0.7.14 runtime=0.7.14 archive_sha256="+artifact.sha256) ||
			strings.Contains(text, "FAIL ") || strings.Count(text, "PASS ") != 29 {
			t.Fatalf("native evidence %s does not match the recorded qualification", name)
		}
	}
}

// The native kit is a separate Go module, so the root build never compiles it
// and nothing else noticed when its SDK pin lagged the runtime it drives. A
// kit on an older SDK cannot launch a newer runtime (the probe then fails
// create-inert-sentinels with "no tested sandbox launch contract"), and that
// failure only appears on the machine that runs the qualification.
func TestProjectSecretNativeKitMatchesRuntimeSDK(t *testing.T) {
	kitMod := filepath.Join("..", "..", "tests", "integration", "project-secrets", "native", "go.mod")
	data, err := os.ReadFile(kitMod)
	if err != nil {
		t.Fatal(err)
	}
	// Parse rather than token-scan: a module path mentioned in a comment or a
	// non-require directive must not satisfy the check, and a duplicate entry
	// must fail instead of the last one winning.
	file, err := modfile.Parse(kitMod, data, nil)
	if err != nil {
		t.Fatalf("parse the native kit go.mod: %v", err)
	}
	pinned := ""
	seen := 0
	for _, require := range file.Require {
		if require.Mod.Path != sdkModulePath {
			continue
		}
		seen++
		pinned = require.Mod.Version
	}
	if seen != 1 {
		t.Fatalf("native kit go.mod must require %s exactly once, found %d", sdkModulePath, seen)
	}
	if pinned != "v"+msbRuntimeVersion {
		t.Fatalf("native kit pins %s %q, but the embedded runtime is v%s; update both together", sdkModulePath, pinned, msbRuntimeVersion)
	}
	sums, err := os.ReadFile(filepath.Join("..", "..", "tests", "integration", "project-secrets", "native", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sums), sdkModulePath+" "+pinned+" ") {
		t.Fatalf("native kit go.sum does not cover the %s %s pin", sdkModulePath, pinned)
	}
}
