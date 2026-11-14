package codebox

import (
	"path/filepath"
	"strings"
	"testing"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

// TestMSBCreateOptionsNeverBindMountAWorkspace pins the P22 boundary at the
// SDK option level: whatever the spec says, the mount table must never carry
// a host path for /workspace. A regression here is the original leak — the
// guest would read the host checkout, including its .env files.
func TestMSBCreateOptionsNeverBindMountAWorkspace(t *testing.T) {
	for _, sealed := range []bool{true, false} {
		spec := msbSandboxSpec{
			Image:           msbImage,
			SealedWorkspace: sealed,
			StartScript:     "exec sleep infinity",
		}
		var cfg msb.SandboxConfig
		for _, option := range msbCreateOptions(spec) {
			option(&cfg)
		}
		ws, present := cfg.Volumes["/workspace"]
		if sealed {
			if !present {
				t.Fatalf("a sealed spec must still provide /workspace storage")
			}
			if ws.Bind != "" || ws.Kind() != msb.MountKindOwned {
				t.Fatalf("sealed /workspace mount = %+v (kind %v); want owned storage with no host source", ws, ws.Kind())
			}
			continue
		}
		// An unsealed spec is refused by the runtime; if it ever reaches the
		// options builder, it must not silently become a bind mount either.
		if present && ws.Bind != "" {
			t.Fatalf("an unsealed spec produced a host bind mount: %+v", ws)
		}
	}
}

// TestParseOwnedWorkspaceDistinguishesProvenance pins the persisted-config
// reading the provenance check depends on. The sealed model needs the
// question "does this guest have a host-mounted workspace?", which the raw
// document answers even when the SDK's typed view cannot see the mounts.
//
// Fixture provenance: the bind shapes are the live-captured document shape
// (see persistedWorkspaceBindMount, captured from a real sandbox); the owned
// spellings include the live-captured persisted shape (type:"Owned") and the
// selector the SDK's create path emits, pinned by upstream's
// TestOwnedMountWireShape. The live sealed-workspace capture was performed on
// macOS for issue #92 on 2026-09-28.
func TestParseOwnedWorkspaceDistinguishesProvenance(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		owned   bool
		wantErr bool
	}{
		{
			name:  "owned type",
			doc:   `{"mounts":[{"type":"Owned","guest":"/workspace"}]}`,
			owned: true,
		},
		{
			name:  "owned selector without a type, as the create path emits",
			doc:   `{"mounts":[{"owned":"dir","guest":"/workspace","quota_mib":null}]}`,
			owned: true,
		},
		{
			name: "missing type and source is ambiguous, not owned",
			doc:  `{"mounts":[{"guest":"/workspace"}]}`,
		},
		{
			name: "unknown owned selector is not positive evidence",
			doc:  `{"mounts":[{"owned":"unknown","guest":"/workspace"}]}`,
		},
		{
			name: "owned selector with a host source remains host-backed",
			doc:  `{"mounts":[{"owned":"dir","bind":"/host/workspace","guest":"/workspace"}]}`,
		},
		{
			name: "owned selector conflicts with bind type",
			doc:  `{"mounts":[{"type":"Bind","owned":"dir","guest":"/workspace"}]}`,
		},
		{
			name: "owned selector conflicts with unknown type",
			doc:  `{"mounts":[{"type":"Virtiofs","owned":"dir","guest":"/workspace"}]}`,
		},
		{
			name:  "owned with the options blob a persisted entry carries",
			doc:   `{"mounts":[{"type":"Owned","guest":"/workspace","options":{"readonly":false,"noexec":false,"nosuid":false,"nodev":false},"stat_virtualization":"strict","host_permissions":"private","follow_root_symlinks":false,"quota_mib":null}]}`,
			owned: true,
		},
		{
			name: "legacy host bind",
			doc:  `{"mounts":[{"type":"Bind","host":"/Users/luis/project","guest":"/workspace"}]}`,
		},
		{
			name: "bind with implicit type",
			doc:  `{"mounts":[{"host":"/home/u/p","guest":"/workspace"}]}`,
		},
		{
			name: "bind type with no host recorded is still host-backed",
			doc:  `{"mounts":[{"type":"Bind","guest":"/workspace"}]}`,
		},
		{
			name: "lowercase bind spelling",
			doc:  `{"mounts":[{"type":"bind","host":"/host/p","guest":"/workspace"}]}`,
		},
		{
			name: "named volume is not a host path",
			doc:  `{"mounts":[{"type":"Named","name":"vol","guest":"/workspace"}]}`,
		},
		{
			name: "no workspace mount at all",
			doc:  `{"mounts":[{"type":"Tmpfs","guest":"/tmp"}]}`,
		},
		{
			// The FFI create/restore wire shape: no "type", the host path
			// under "bind". Before the source-key set covered it, this read
			// as owned — a host-mounted workspace accepted as sealed.
			name: "bind source under the wire key, no type",
			doc:  `{"mounts":[{"bind":"/Users/luis/project","guest":"/workspace"}]}`,
		},
		{
			name: "named source under the wire key, no type",
			doc:  `{"mounts":[{"named":"vol","guest":"/workspace"}]}`,
		},
		{
			name: "generic source key",
			doc:  `{"mounts":[{"source":"/host/dir","guest":"/workspace"}]}`,
		},
		{
			name: "path key",
			doc:  `{"mounts":[{"path":"/host/dir","guest":"/workspace"}]}`,
		},
		{
			// An unrecognized non-empty type must not be assumed owned: a
			// future host-backed kind would otherwise be read as sealed.
			name: "unrecognized mount kind",
			doc:  `{"mounts":[{"type":"Virtiofs","guest":"/workspace"}]}`,
		},
		{
			name: "tmpfs is not owned storage",
			doc:  `{"mounts":[{"type":"Tmpfs","guest":"/workspace"}]}`,
		},
		{
			name:    "invalid json",
			doc:     `{not json`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owned, err := parseOwnedWorkspace(tc.doc, "/workspace")
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if owned != tc.owned {
				t.Fatalf("owned = %v, want %v", owned, tc.owned)
			}
			// The invariant that matters, in the dangerous direction: no
			// document may report "no host path" AND "owned" unless the
			// entry genuinely carries no host source. A host path that
			// parseWorkspaceMount cannot name (an unparsed spelling) must
			// still not be accepted as sealed.
			host, herr := parseWorkspaceMount(tc.doc, "/workspace")
			if herr != nil {
				return
			}
			if owned && host != "" {
				t.Fatalf("the parsers disagree: a host path %q was reported as owned storage", host)
			}
			if owned && strings.Contains(tc.doc, "/") {
				// Any document naming a path and still reported as owned must
				// be one whose path is not a mount source; the fixtures above
				// are the corpus that proves each spelling is refused.
				if !strings.Contains(tc.doc, `"owned"`) && !strings.Contains(tc.doc, `"type":"Owned"`) {
					t.Fatalf("a document naming a path was accepted as owned: %s", tc.doc)
				}
			}
		})
	}
}

// TestNoDocumentIsBothUnmountedAndOwned is the directional invariant the
// per-file fixtures cannot express: for a corpus of documents that DO name a
// host directory, none may be reported as guest-owned. This is the failure
// that would put the host checkout inside the guest.
func TestNoDocumentIsBothUnmountedAndOwned(t *testing.T) {
	hostBacked := []string{
		`{"mounts":[{"type":"Bind","host":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"bind":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"host":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"source":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"path":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"type":"bind","bind":"/h","guest":"/workspace"}]}`,
		`{"mounts":[{"type":"Bind","guest":"/workspace"}]}`,
		`{"mounts":[{"type":"Named","named":"vol","guest":"/workspace"}]}`,
		`{"mounts":[{"type":"Disk","disk":"/d.img","guest":"/workspace"}]}`,
	}
	for _, doc := range hostBacked {
		owned, err := parseOwnedWorkspace(doc, "/workspace")
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		if owned {
			t.Fatalf("%s was reported as guest-owned storage", doc)
		}
	}
}

// TestParseWorkspaceMountStillDetectsLegacyBinds pins the check the runtime
// uses to refuse a pre-P22 instance: a host bind must be detected as such.
func TestParseWorkspaceMountStillDetectsLegacyBinds(t *testing.T) {
	mounted, err := parseWorkspaceMount(`{"mounts":[{"type":"Bind","host":"/host/path","guest":"/workspace"}]}`, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if mounted != "/host/path" {
		t.Fatalf("mounted = %q", mounted)
	}
	mounted, err = parseWorkspaceMount(`{"mounts":[{"type":"Owned","guest":"/workspace"}]}`, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if mounted != "" {
		t.Fatalf("an owned workspace has no host path, got %q", mounted)
	}
}

func TestParseServerPortForwardedReadsPersistedNetworkConfig(t *testing.T) {
	for _, tc := range []struct {
		name      string
		doc       string
		forwarded bool
		wantErr   bool
	}{
		{
			name:      "backend server mapping",
			doc:       `{"network":{"ports":[{"host_port":4096,"guest_port":4096,"protocol":"tcp","host_bind":"127.0.0.1"}]}}`,
			forwarded: true,
		},
		{
			name:      "server reachable through a different host port",
			doc:       `{"network":{"ports":[{"host_port":49152,"guest_port":4096}]}}`,
			forwarded: true,
		},
		{
			name: "preview-only full mode",
			doc:  `{"network":{"ports":[{"host_port":3000,"guest_port":3000},{"host_port":3010,"guest_port":3010}]}}`,
		},
		{
			name:    "missing network data",
			doc:     `{"name":"cb-project"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			doc:     `{not json`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseServerPortForwarded(tc.doc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected persisted port inspection error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.forwarded {
				t.Fatalf("forwarded = %v, want %v", got, tc.forwarded)
			}
		})
	}
}

func TestParseAppliedPortMappingsReadsPersistedNetworkConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     string
		want    []PortMapping
		wantErr bool
	}{
		{
			name: "server forward and preview mappings",
			doc:  `{"network":{"ports":[{"host_port":4096,"guest_port":4096,"protocol":"tcp","host_bind":"127.0.0.1"},{"host_port":8080,"guest_port":80}]}}`,
			want: []PortMapping{{Host: 4096, Guest: 4096}, {Host: 8080, Guest: 80}},
		},
		{
			name: "empty port list",
			doc:  `{"network":{"ports":[]}}`,
			want: []PortMapping{},
		},
		{
			name:    "missing network data",
			doc:     `{"name":"cb-project"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			doc:     `{not json`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAppliedPortMappings(tc.doc)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected persisted port inspection error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !equalPortMappings(got, tc.want) {
				t.Fatalf("applied mappings = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMSBSandboxEnvCarriesGitIdentity pins the P11 identity reaching the
// guest through the environment the prep script reads, so commits made inside
// the sandbox carry the configured identity rather than the script default.
func TestMSBSandboxEnvCarriesGitIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	path, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUserSettings(DefaultFS, path, UserSettings{
		SchemaVersion: 1, GitName: "Luis Arias", GitEmail: "luis@example.gouv.fr",
	}); err != nil {
		t.Fatal(err)
	}
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	env := m.sandboxEnv()
	if env[msbGitNameEnv] != "Luis Arias" || env[msbGitEmailEnv] != "luis@example.gouv.fr" {
		t.Fatalf("guest env identity = %q <%s>", env[msbGitNameEnv], env[msbGitEmailEnv])
	}
	// The prep script must actually read those names, or the env is inert.
	if !strings.Contains(guestPrepScript, msbGitNameEnv) || !strings.Contains(guestPrepScript, msbGitEmailEnv) {
		t.Fatalf("the guest prep script does not read the identity environment")
	}
}

// TestMSBSpecCarriesGuestSizing pins that the configured sizing reaches the
// sandbox options. Before this, the values were hardcoded, so a project's
// recorded resource choice was displayed by 'config explain' and then ignored.
func TestMSBSpecCarriesGuestSizing(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	m.cfg.CPUs = 6
	m.cfg.MemoryMB = 12288
	spec := m.sandboxSpec(nil)
	if spec.CPUs != 6 || spec.MemoryMB != 12288 {
		t.Fatalf("spec sizing = %d cpus / %d MB", spec.CPUs, spec.MemoryMB)
	}
	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(spec) {
		option(&cfg)
	}
	if cfg.CPUs != 6 || cfg.MemoryMiB != 12288 {
		t.Fatalf("sandbox options = %d cpus / %d MB", cfg.CPUs, cfg.MemoryMiB)
	}
	// A spec that never resolved sizing still boots with the defaults rather
	// than asking for a zero-CPU VM.
	var zero msb.SandboxConfig
	for _, option := range msbCreateOptions(msbSandboxSpec{Image: msbImage, SealedWorkspace: true}) {
		option(&zero)
	}
	if zero.CPUs != DefaultSandboxCPUs || zero.MemoryMiB != DefaultSandboxMemoryMB {
		t.Fatalf("unset sizing = %d cpus / %d MB", zero.CPUs, zero.MemoryMiB)
	}
}
