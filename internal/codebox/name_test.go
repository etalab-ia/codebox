package codebox

import "testing"

func TestVMName(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/cirruslabs/macos-tahoe-base:latest":        "opencode-tahoe-base-latest",
		"ghcr.io/cirruslabs/macos-sonoma-base:latest":       "opencode-sonoma-base-latest",
		"ghcr.io/cirruslabs/macos-tahoe-base@sha256:abc123": "opencode-tahoe-base-sha256-abc123",
		"registry.example.com/foo/macos-sonoma-base:1.2":    "opencode-sonoma-base-1.2",
	}
	for in, want := range cases {
		if got := VMName(in); got != want {
			t.Errorf("VMName(%q) = %q, want %q", in, got, want)
		}
	}
}
