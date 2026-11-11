package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// version is the release string, settable at build time with
// -ldflags "-X main.version=v1.2.3". It defaults to "dev" for local builds.
var version = "dev"

// buildInfo describes which binary is running. When several builds of
// codebox are around (Go and Bun, different commits, local and downloaded),
// `codebox version` must be able to tell them apart.
type buildInfo struct {
	Version    string
	Revision   string
	CommitTime string
	Modified   bool
	GoVersion  string
	Platform   string
}

// readBuildInfo reads the module and VCS metadata the Go toolchain embeds.
func readBuildInfo() buildInfo {
	bi := buildInfo{
		Version:   version,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return bi
	}
	if info.GoVersion != "" {
		bi.GoVersion = info.GoVersion
	}
	// An explicit -ldflags stamp wins. Otherwise fall back to the module
	// version, but not to the "(devel)" placeholder or a VCS pseudo-version
	// (v0.0.0-<timestamp>-<hash>), since the commit is reported separately.
	if bi.Version == "" || bi.Version == "dev" {
		if v := info.Main.Version; v != "" && v != "(devel)" && !strings.HasPrefix(v, "v0.0.0-") {
			bi.Version = v
		}
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			bi.Revision = s.Value
		case "vcs.time":
			bi.CommitTime = s.Value
		case "vcs.modified":
			bi.Modified = s.Value == "true"
		}
	}
	return bi
}

// printBuildInfo writes a stable, machine-readable-enough summary so the Go
// build can be compared against another implementation's version output.
func printBuildInfo() {
	bi := readBuildInfo()
	revision := bi.Revision
	if revision == "" {
		revision = "unknown"
	} else if len(revision) > 12 {
		revision = revision[:12]
	}
	if bi.Modified {
		revision += "-dirty"
	}
	fmt.Println("codebox (Go)")
	fmt.Printf("  version:  %s\n", bi.Version)
	fmt.Printf("  commit:   %s\n", revision)
	if bi.CommitTime != "" {
		fmt.Printf("  built:    %s\n", bi.CommitTime)
	}
	fmt.Printf("  go:       %s\n", bi.GoVersion)
	fmt.Printf("  platform: %s\n", bi.Platform)
}
