//go:build linux

package codebox

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPreflightKVMInaccessibleNamesTheGroupRemedy(t *testing.T) {
	fi, err := os.Stat("/dev/kvm")
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		t.Skip("host has no KVM device; the existence path is covered elsewhere")
	}
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err == nil {
		t.Skip("this user can open /dev/kvm; the accessibility failure cannot be exercised")
	}
	// The device exists but this user cannot open it: the detail must say
	// so (the kvm group), not the generic missing-device message.
	detail := probeKVMFailure()
	if !stringsContainsLower(detail, "kvm group") {
		t.Fatalf("detail must name the kvm group remedy: %q", detail)
	}
}
