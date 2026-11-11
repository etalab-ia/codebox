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
