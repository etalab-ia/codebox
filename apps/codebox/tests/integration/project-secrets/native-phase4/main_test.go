package main

// Unit tests for the probe's pure machinery. They run anywhere (including
// this Linux authoring sandbox); the native run itself must happen on
// darwin/arm64. Run with: go test ./...

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubRedactsEveryCanaryForm(t *testing.T) {
	canary := "0123456789abcdef0123456789abcdef0123456789abcdef"
	sensitive = nil
	registerCanaries(canary)
	forms := []string{
		canary,
		base64.StdEncoding.EncodeToString([]byte(canary)),
		base64.RawStdEncoding.EncodeToString([]byte(canary)),
		base64.StdEncoding.EncodeToString([]byte("user:" + canary)),
	}
	for _, form := range forms {
		out := scrub("prefix " + form + " suffix")
		if strings.Contains(out, form) {
			t.Fatalf("scrub left canary form intact: %q", out)
		}
		if !strings.Contains(out, "[REDACTED-CANARY]") {
			t.Fatalf("scrub produced no redaction marker for form: %q", out)
		}
	}
}

func TestScrubRedactsAuthorizationShapes(t *testing.T) {
	for _, line := range []string{
		"Proxy-Authorization: Basic dXNlcjpwYXNz",
		"authorization: bearer abc.def.ghi",
		"Basic aGVsbG8=",
	} {
		if out := scrub("x " + line + " y"); strings.Contains(out, "dXNlcjpwYXNz") || strings.Contains(out, "abc.def.ghi") || strings.Contains(out, "aGVsbG8=") {
			t.Fatalf("scrub left authorization material in %q", out)
		}
	}
}

func TestScrubRedactsGenericLongHex(t *testing.T) {
	sensitive = nil
	snapshotDigest := strings.Repeat("ab", 32)
	out := scrub("snapshot digest " + snapshotDigest + " failed")
	if strings.Contains(out, snapshotDigest) {
		t.Fatalf("scrub left a generic 64-hex token in %q", out)
	}
	if !strings.Contains(out, "[REDACTED-HEX]") {
		t.Fatalf("scrub produced no hex marker in %q", out)
	}
	if got := scrub("pin " + source); !strings.Contains(got, source) {
		t.Fatal("scrub must preserve the pinned source commit constant")
	}
	if got := scrub("pin " + archiveSHA); !strings.Contains(got, archiveSHA) {
		t.Fatal("scrub must preserve the pinned archive digest constant")
	}
}

func TestCaseNameContract(t *testing.T) {
	// Every case the probe can emit. The runner only accepts
	// ^(PASS|FAIL) [a-z0-9-]+$ so a canary (48 lowercase hex chars, no
	// dash) could still slip through a hand-typed name; the runtime guard
	// in result() rejects anything off-pattern, and this list keeps the
	// emitted vocabulary reviewable.
	names := []string{
		"isolated-runtime-preflight", "native-darwin-arm64-required", "ownership-file",
		"create-vm", "tmpfs-mount-present", "guest-fixtures-pushed",
		"deliver-disk-write", "deliver-tmpfs-write", "deliver-stdin-file", "deliver-stdin-memory",
		"chmod-0600-achievable", "guest-dirs-no-leftovers", "guest-tmpfs-no-leftovers",
		"argv-scan-finds-control", "environ-scan-finds-control",
		"argv-clean-for-delivered", "environ-clean-for-delivered", "guest-varlog-clean",
		"host-argv-clean", "host-oslog-clean", "runtime-log-clean-for-delivered",
		"persisted-config-holds-control", "persisted-config-clean-for-delivered",
		"host-files-clean-for-delivered", "host-persisted-store-shows-control",
		"removal-reported-gone", "restart-succeeded", "removal-survives-restart",
		"tmpfs-cleared-on-restart", "tmpfs-mount-persists-across-restart",
		"snapshot-created", "tmpfs-never-on-host", "snapshot-restored", "snapshot-excludes-tmpfs",
		"host-files-clean-after-teardown", "disk-value-removed-with-vm",
		"get-handle", "cleanup-own-vm", "cleanup-ownership", "cleanup", "invalid-case-name",
	}
	for _, n := range names {
		if !validCaseName.MatchString(n) {
			t.Fatalf("case name off contract: %q", n)
		}
	}
	for _, bad := range []string{"", "A", "has space", "under_score", "dot.name"} {
		if validCaseName.MatchString(bad) {
			t.Fatalf("case-name guard accepted %q", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"msb-home/sandboxes/cb-p4-x/upper/ext4.img":   "disk-image",
		"msb-home/snapshots/cbp4snap-y/disk.img":      "snapshot",
		"msb-home/snapshots/cbp4snap-y/manifest.json": "snapshot",
		"msb-home/logs/cb-p4-x/stdout.log":            "log",
		"msb-home/journal/session-1.bin":              "log",
		"msb-home/msb.db":                             "store",
		"msb-home/registry/config.json":               "store",
		"runtime/msb":                                 "other",
		"probe":                                       "other",
		"runtime/official.tar.gz":                     "other",
	}
	for rel, want := range cases {
		if got := classify(rel); got != want {
			t.Fatalf("classify(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestHostViolationsAndClasses(t *testing.T) {
	hits := []hostHit{
		{Canary: cA, Path: "msb-home/sandboxes/x/upper/ext4.img", Class: "disk-image"},
		{Canary: cB, Path: "msb-home/snapshots/s/disk.img", Class: "snapshot"},
		{Canary: cD, Path: "msb-home/msb.db", Class: "store"},
		{Canary: cC, Path: "msb-home/logs/x/stdout.log", Class: "log"},
		{Canary: cF, Path: "probe", Class: "other"},
	}
	if got := hostViolations(cA, hits); len(got) != 0 {
		t.Fatalf("channel A disk-image hit must be allowed, got %v", got)
	}
	if got := hostViolations(cB, hits); len(got) != 1 {
		t.Fatalf("channel B snapshot hit must be a violation, got %v", got)
	}
	if got := hostViolations(cD, hits); len(got) != 0 {
		t.Fatalf("channel D store hit must be allowed, got %v", got)
	}
	if got := hostViolations(cC, hits); len(got) != 0 {
		t.Fatalf("channel C log hit must be allowed (measured), got %v", got)
	}
	if got := hostViolations(cF, hits); len(got) != 1 {
		t.Fatalf("channel F binary hit must be a violation, got %v", got)
	}
	if !hostHasClass(cD, "store", hits) {
		t.Fatal("hostHasClass missed the persisted-store control")
	}
	if hostHasClass(cD, "log", hits) {
		t.Fatal("hostHasClass hallucinated a log hit")
	}
}

func TestParseScanLines(t *testing.T) {
	out := `{"pattern":"A","cmdline":[],"environ":[],"files":[]}
{"pattern":"C","cmdline":[412],"environ":[],"files":["/var/log/messages"]}
not json at all
{"pattern":"F","cmdline":[],"environ":[418],"files":[]}
`
	got := parseScanLines(out)
	if len(got) != 3 {
		t.Fatalf("expected 3 parsed lines, got %d", len(got))
	}
	if len(got["C"].Cmdline) != 1 || got["C"].Cmdline[0] != 412 {
		t.Fatalf("argv control line misparsed: %+v", got["C"])
	}
	if len(got["F"].Environ) != 1 || got["F"].Environ[0] != 418 {
		t.Fatalf("env control line misparsed: %+v", got["F"])
	}
	if len(got["A"].Cmdline) != 0 || len(got["A"].Files) != 0 {
		t.Fatalf("clean channel A line misparsed: %+v", got["A"])
	}
}

func TestScanHostFindsCanariesByClass(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	canaries := map[string]string{}
	for _, id := range canaryIDs {
		canaries[id] = id + "-unit-test-canary-" + strings.Repeat("z", 40)
	}
	mk("msb-home/sandboxes/x/upper/ext4.img", "noise "+canaries[cA]+" noise")
	mk("msb-home/msb.db", "prefix "+canaries[cD])
	mk("msb-home/logs/x/run.log", base64.StdEncoding.EncodeToString([]byte(canaries[cC])))
	mk("msb-home/clean/empty.txt", "nothing here")
	hits := scanHost(root, canaries)
	if !hostHasClass(cA, "disk-image", hits) {
		t.Fatalf("scanHost missed disk-image canary: %v", hits)
	}
	if !hostHasClass(cD, "store", hits) {
		t.Fatalf("scanHost missed persisted-store canary: %v", hits)
	}
	if !hostHasClass(cC, "log", hits) {
		t.Fatalf("scanHost missed base64 log canary: %v", hits)
	}
	for _, id := range []string{cB, cE1, cE2, cF} {
		if len(hits3For(id, hits)) != 0 {
			t.Fatalf("scanHost hallucinated %s hit: %v", id, hits)
		}
	}
	if got := hostViolations(cB, hits); len(got) != 0 {
		t.Fatalf("channel B must have no hits here: %v", got)
	}
}

func TestDigest(t *testing.T) {
	if digest("hi\n") != "98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4" {
		t.Fatal("digest mismatch against known sha256 of \"hi\\n\"")
	}
}
