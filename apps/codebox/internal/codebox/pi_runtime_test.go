package codebox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPiRuntimeUsesReviewedRelease(t *testing.T) {
	if PiPackage != "@earendil-works/pi-coding-agent" || PiVersion != "1.5.2" {
		t.Fatalf("Pi pin = %s@%s, want @earendil-works/pi-coding-agent@1.5.2", PiPackage, PiVersion)
	}
}

func TestPiGuestInstallAcceptsStderrVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the guest install fragment requires a POSIX shell")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pi 0.73.1 prints --version on stderr. The install must recognize that
	// response instead of reinstalling and then failing its own version check.
	pi := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s' >&2\n", PiVersion)
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte(pi), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	npm := "#!/bin/sh\nprintf 'npm must not reinstall an already matching Pi' >&2\nexit 99\n"
	if err := os.WriteFile(filepath.Join(tools, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", piGuestInstallScript())
	cmd.Env = []string{"HOME=" + home, "PATH=" + tools + ":" + os.Getenv("PATH")}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stderr-only Pi version was not accepted: %v\n%s", err, output)
	}
}

func TestPiPrepAndProbeAcceptStderrVersion(t *testing.T) {
	if !strings.Contains(piGuestPrepScript, "pi --version 2>&1") {
		t.Fatal("Pi prep discards the version printed on stderr")
	}
	if !strings.Contains(msbGuestPrepareProbeFor(HarnessPi), "pi --version 2>&1") {
		t.Fatal("Pi readiness discards the version printed on stderr")
	}
}
