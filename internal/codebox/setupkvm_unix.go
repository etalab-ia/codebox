//go:build !windows

package codebox

import (
	"os"

	"golang.org/x/sys/unix"
)

func osStatKVM() (os.FileInfo, error) { return os.Stat("/dev/kvm") }

// probeKVM reports whether /dev/kvm exists, is the KVM character device, and
// is accessible by the current user. A device that exists but cannot be
// opened (the user is not in the kvm group) would pass an existence check
// and then fail at the first VM launch, so accessibility is part of the
// probe.
func probeKVM() (bool, string) {
	if fi, err := osStatKVM(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false, ""
	}
	if err := unix.Access("/dev/kvm", unix.R_OK|unix.W_OK); err != nil {
		return false, ""
	}
	return true, "/dev/kvm present and accessible"
}

// probeKVMFailure explains the failure for the user.
func probeKVMFailure() string {
	if fi, err := osStatKVM(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return "/dev/kvm exists but this user cannot open it: add yourself to the kvm group (sudo usermod -aG kvm $USER, then re-login)"
	}
	return "/dev/kvm is missing or inaccessible: install KVM support (on a VM host, enable nested virtualization)"
}
