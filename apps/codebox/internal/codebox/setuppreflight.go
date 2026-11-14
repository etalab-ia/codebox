package codebox

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// This file implements the P11 setup preflight: the read-only diagnosis that
// runs before anything is installed or asked. The plan's hard rule: no
// credential prompt may precede a fatal virtualization blocker — the user
// types nothing until the machine is known to be usable.

// SetupDoctor is the seam for the runtime's diagnostic command. Production
// shells the managed `msb doctor` output; tests inject results.
type SetupDoctor func(ctx context.Context) (output string, err error)

// SetupPreflighter runs the diagnosis with injectable seams.
type SetupPreflighter struct {
	// Doctor runs the runtime doctor. Nil uses the managed runtime binary
	// when present, and reports not-installed otherwise.
	Doctor SetupDoctor
	// StatFS reports free bytes for a path. Nil uses os.Stat.
	StatFS func(path string) (free int64, err error)
	// RuntimeHome overrides the runtime home for tests.
	RuntimeHome string
}

// RunPreflight diagnoses the host. It never installs, writes, or asks.
func (p SetupPreflighter) RunPreflight(ctx context.Context) SetupPreflight {
	out := SetupPreflight{Platform: runtime.GOOS + "/" + runtime.GOARCH}

	// Runtime installation state first: an installed, trusted runtime skips
	// the doctor (the doctor is also the install check).
	home := p.RuntimeHome
	if home == "" {
		if h, err := msbRuntimeHome(); err == nil {
			home = h
		}
	}
	installed := false
	if home != "" {
		if artifact, err := msbRuntimeArtifactFor(runtime.GOOS, runtime.GOARCH); err == nil {
			installed = managedMSBRuntimeTrusted(home, artifact)
		}
	}
	out.RuntimeInstalled = installed

	// Virtualization: run the runtime's own doctor when the runtime is
	// installed; otherwise probe the host's virtualization primitive
	// directly, so a fatal blocker surfaces before any download.
	if installed {
		doctor := p.Doctor
		if doctor == nil {
			doctor = func(ctx context.Context) (string, error) {
				path, err := msbRuntimeBinary()
				if err != nil {
					return "", err
				}
				output, err := runRuntimeDoctor(ctx, path)
				return output, err
			}
		}
		output, err := doctor(ctx)
		if err != nil {
			out.VirtualizationDetail = strings.TrimSpace(fmt.Sprintf("%v", err))
			out.Fatal = true
		} else {
			out.VirtualizationOK = true
			out.VirtualizationDetail = strings.TrimSpace(output)
		}
	} else {
		ok, detail := probeVirtualizationPrimitive()
		out.VirtualizationOK = ok
		out.VirtualizationDetail = detail
		if !ok {
			out.Fatal = true
		}
	}

	// Disk: free space of the runtime home's filesystem.
	statFS := p.StatFS
	if statFS == nil {
		statFS = freeDiskBytes
	}
	if home != "" {
		if free, err := statFS(home); err == nil {
			out.DiskFreeBytes = free
			out.DiskOK = free >= SetupMinDiskBytes
		} else {
			out.DiskFreeBytes = -1
			out.DiskOK = false
			out.DiskDetail = err.Error()
			// A disk probe failure on a home that does not exist yet is
			// not fatal: the directory is created at install time. Only a
			// probe failure on an existing path keeps Fatal set.
			if _, serr := os.Stat(home); serr != nil && os.IsNotExist(serr) {
				out.DiskOK = true
				out.DiskDetail = ""
			}
		}
		if !out.DiskOK {
			out.Fatal = true
		}
	} else {
		out.Fatal = true
		out.DiskDetail = "the runtime state home could not be resolved (HOME is unset); set HOME and re-run"
	}
	return out
}

// probeVirtualizationPrimitive checks the host's virtualization primitive
// without the runtime. Linux needs /dev/kvm accessible (existence alone is
// not enough: the user must be able to open it, i.e. be in the kvm group);
// macOS needs the Apple framework (present on all supported hardware, so
// the check is existence of
// /System/Library/Frameworks/Hypervisor.framework). Windows
// needs Hyper-V, checked by the WHP availability of the virtualization
// instruction; without the runtime this is reported as unknown rather than
// fatal, because the runtime's doctor is the authority there.
func probeVirtualizationPrimitive() (bool, string) {
	switch runtime.GOOS {
	case "linux":
		if ok, detail := probeKVM(); ok {
			return true, detail
		}
		return false, probeKVMFailure()
	case "darwin":
		if _, err := os.Stat("/System/Library/Frameworks/Hypervisor.framework"); err == nil {
			return true, "Hypervisor framework present"
		}
		return false, "Hypervisor.framework not found"
	default:
		// Unknown platform: not fatal from the primitive alone; the runtime
		// doctor decides after installation.
		return true, "virtualization will be checked by the runtime doctor after installation"
	}
}

// runRuntimeDoctor executes the managed runtime's doctor command.
func runRuntimeDoctor(ctx context.Context, path string) (string, error) {
	res, err := OSRunner{}.Run(ctx, path, "doctor")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("runtime doctor failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// freeDiskBytes reports the free bytes available to the calling user on the
// filesystem containing path (Bavail, not the raw free blocks: the
// filesystem's reserved blocks are not usable).
func freeDiskBytes(path string) (int64, error) {
	// statfs/statvfs differ per platform; the OS-specific helpers implement
	// the probe.
	return diskFree(path)
}
