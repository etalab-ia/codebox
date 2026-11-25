package codebox

import (
	"context"
	"strings"
	"testing"
)

func TestParseDNSNameservers(t *testing.T) {
	got, err := ParseDNSNameservers("9.9.9.9, 2620:fe::fe 1.1.1.1:53 9.9.9.9")
	if err != nil {
		t.Fatalf("ParseDNSNameservers: %v", err)
	}
	want := []string{"9.9.9.9", "2620:fe::fe", "1.1.1.1:53"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ParseDNSNameservers = %v, want %v", got, want)
	}

	for _, bad := range []string{
		"",
		"   ",
		"not-an-ip",
		"fe80::1",
		"fe80::1%en0",
		"[fe80::1]:53",
		"9.9.9.9:0",
	} {
		if _, err := ParseDNSNameservers(bad); err == nil {
			t.Errorf("ParseDNSNameservers(%q) succeeded, want rejection", bad)
		}
	}
}

func TestLoadConfigDNSNameserversDeferredError(t *testing.T) {
	cfg := LoadConfig(func(k string) (string, bool) {
		if k == "CODEBOX_DNS_NAMESERVERS" {
			return "9.9.9.9,2620:fe::fe", true
		}
		return "", false
	})
	if cfg.DNSNameserversErr != nil {
		t.Fatalf("unexpected error: %v", cfg.DNSNameserversErr)
	}
	if strings.Join(cfg.DNSNameservers, ",") != "9.9.9.9,2620:fe::fe" {
		t.Fatalf("DNSNameservers = %v", cfg.DNSNameservers)
	}

	badCfg := LoadConfig(func(k string) (string, bool) {
		if k == "CODEBOX_DNS_NAMESERVERS" {
			return "fe80::1%en0", true
		}
		return "", false
	})
	if badCfg.DNSNameserversErr == nil {
		t.Fatal("expected deferred error for link-local IPv6 DNS")
	}
	m := NewMicrosandboxRuntime(badCfg)
	if err := m.validateConfig(); err == nil || !strings.Contains(err.Error(), "CODEBOX_DNS_NAMESERVERS") {
		t.Fatalf("validateConfig must surface DNSNameserversErr: %v", err)
	}
}

func TestMSBNetworkConfigWiresExplicitDNSNameservers(t *testing.T) {
	plain := msbNetworkConfig(0, nil)
	if plain.DNS != nil {
		t.Fatalf("default network config must leave DNS nil so host DNS is used: %+v", plain.DNS)
	}
	explicit := msbNetworkConfig(0, []string{"9.9.9.9", "2620:fe::fe"})
	if explicit.DNS == nil || strings.Join(explicit.DNS.Nameservers, ",") != "9.9.9.9,2620:fe::fe" {
		t.Fatalf("explicit DNS = %+v, want [9.9.9.9 2620:fe::fe]", explicit.DNS)
	}
}

func TestInspectHostDNSDetectsHotspotLinkLocalIPv6(t *testing.T) {
	t.Run("macOS scutil link-local-only IPv6 hotspot", func(t *testing.T) {
		scutil := `DNS configuration

resolver #1
  nameserver[0] : fe80::1234:5678:9abc:def0%en0
  if_index : 14 (en0)
  flags    : Request A records, Request AAAA records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  domain   : local
  options  : mdns
  timeout  : 5
  flags    : Request A records, Request AAAA records
  order    : 300000

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 9.9.9.9
`
		info := InspectHostDNS(context.Background(), "darwin", func(context.Context, string, ...string) (ExecResult, error) {
			return ExecResult{Stdout: scutil}, nil
		}, newMapFS())
		if !info.LinkLocalIPv6Only {
			t.Fatalf("expected LinkLocalIPv6Only=true, got %+v", info)
		}
		if strings.Join(info.Nameservers, ",") != "fe80::1234:5678:9abc:def0%en0" {
			t.Fatalf("Nameservers = %v", info.Nameservers)
		}
	})

	t.Run("macOS scutil mixed routable and link-local is not link-local-only", func(t *testing.T) {
		scutil := `DNS configuration

resolver #1
  nameserver[0] : fe80::1%en0
  nameserver[1] : 9.9.9.9
`
		info := InspectHostDNS(context.Background(), "darwin", func(context.Context, string, ...string) (ExecResult, error) {
			return ExecResult{Stdout: scutil}, nil
		}, newMapFS())
		if info.LinkLocalIPv6Only {
			t.Fatalf("expected LinkLocalIPv6Only=false when routable DNS exists: %+v", info)
		}
	})

	t.Run("linux resolv.conf link-local-only", func(t *testing.T) {
		fs := newMapFS()
		fs.files["/etc/resolv.conf"] = []byte("# hotspot\nnameserver fe80::1%wlan0\n")
		info := InspectHostDNS(context.Background(), "linux", nil, fs)
		if !info.LinkLocalIPv6Only {
			t.Fatalf("expected LinkLocalIPv6Only=true, got %+v", info)
		}
	})
}

func TestCheckGuestDNS(t *testing.T) {
	t.Run("healthy guest returns nil", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{code: 0}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-ok")
		m.Client = client
		m.hostDNSLookup = func(context.Context, string) bool {
			t.Fatal("host lookup should not run when guest DNS succeeds")
			return false
		}
		if diag := m.CheckGuestDNS(context.Background()); diag != nil {
			t.Fatalf("expected nil diagnostic when guest DNS works, got: %v", diag)
		}
	})

	t.Run("offline host does not produce false DNS error", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{code: 2}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-offline")
		m.Client = client
		m.hostDNSLookup = func(context.Context, string) bool { return false }
		if diag := m.CheckGuestDNS(context.Background()); diag != nil {
			t.Fatalf("expected nil diagnostic when offline, got: %v", diag)
		}
	})

	t.Run("guest fails while host succeeds with link-local IPv6 DNS", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{code: 2}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-hotspot")
		m.Client = client
		m.goos = "darwin"
		m.hostDNSLookup = func(context.Context, string) bool { return true }
		m.hostDNSInspect = func(context.Context) HostDNSInfo {
			return HostDNSInfo{
				Nameservers:       []string{"fe80::1%en0"},
				LinkLocalIPv6Only: true,
			}
		}
		diag := m.CheckGuestDNS(context.Background())
		if diag == nil {
			t.Fatal("expected diagnostic when guest DNS fails while host resolves")
		}
		msg := diag.Error()
		for _, want := range []string{msbAllowHost, "IPv6 lien-local", "fe80::1%en0", "networksetup -setdnsservers", "codebox restart", "CODEBOX_DNS_NAMESERVERS"} {
			if !strings.Contains(msg, want) {
				t.Errorf("diagnostic missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("guest fails while host has routable DNS points to restart", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{code: 2}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-stale")
		m.Client = client
		m.goos = "darwin"
		m.hostDNSLookup = func(context.Context, string) bool { return true }
		m.hostDNSInspect = func(context.Context) HostDNSInfo {
			return HostDNSInfo{
				Nameservers:       []string{"9.9.9.9"},
				LinkLocalIPv6Only: false,
			}
		}
		diag := m.CheckGuestDNS(context.Background())
		if diag == nil {
			t.Fatal("expected diagnostic when guest DNS is stale")
		}
		msg := diag.Error()
		for _, want := range []string{msbAllowHost, "démarrage", "codebox restart"} {
			if !strings.Contains(msg, want) {
				t.Errorf("diagnostic missing %q:\n%s", want, msg)
			}
		}
	})

	t.Run("guest probe deadline exceeded triggers host confirmation and diagnostic", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{err: context.DeadlineExceeded}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-timeout")
		m.Client = client
		m.goos = "darwin"
		m.hostDNSLookup = func(context.Context, string) bool { return true }
		m.hostDNSInspect = func(context.Context) HostDNSInfo {
			return HostDNSInfo{
				Nameservers:       []string{"fe80::1%en0"},
				LinkLocalIPv6Only: true,
			}
		}
		diag := m.CheckGuestDNS(context.Background())
		if diag == nil {
			t.Fatal("expected diagnostic when guest DNS probe times out while host resolves")
		}
		if !strings.Contains(diag.Error(), "IPv6 lien-local") {
			t.Fatalf("unexpected diagnostic: %s", diag.Error())
		}
	})

	t.Run("unrelated exec error does not emit false DNS diagnostic", func(t *testing.T) {
		client := &fakeMSBClient{
			execCaptureResults: []fakeMSBExecCaptureResult{{err: context.Canceled}},
		}
		m := NewMicrosandboxRuntimeForInstance(Config{}, "cb-dns-exec-err")
		m.Client = client
		m.hostDNSLookup = func(context.Context, string) bool {
			t.Fatal("host lookup should not run on unrelated exec error")
			return true
		}
		if diag := m.CheckGuestDNS(context.Background()); diag != nil {
			t.Fatalf("expected nil diagnostic on unrelated exec error, got: %v", diag)
		}
	})
}

func TestDiagnosticsReportsLinkLocalIPv6HostDNS(t *testing.T) {
	setupDiagnosticsEnv(t)
	preflight := SetupPreflight{
		Platform:         "darwin/arm64",
		VirtualizationOK: true,
		DiskFreeBytes:    2 << 30,
		DiskOK:           true,
	}
	report := Diagnostics{
		Preflight:      &preflight,
		StateDir:       t.TempDir(),
		ProjectDir:     t.TempDir(),
		LookPath:       func(string) (string, error) { return "", nil },
		Run:            func(context.Context, string, ...string) (ExecResult, error) { return ExecResult{}, nil },
		ProbeAlbert:    func(context.Context, string) CredentialProbe { return CredentialProbe{Unreachable: true} },
		VerifyStore:    func(context.Context) (string, error) { return "keychain", nil },
		ReadCredential: noStoredCredentials(),
		InspectHostDNS: func(context.Context) HostDNSInfo {
			return HostDNSInfo{
				Nameservers:       []string{"fe80::1%en0"},
				LinkLocalIPv6Only: true,
			}
		},
	}.Collect(context.Background())
	var found bool
	for _, sec := range report.Sections {
		if sec.Title != "Capacité de l'hôte" {
			continue
		}
		for _, item := range sec.Items {
			if item.Name == "DNS de l'hôte" {
				found = true
				if item.Status != DiagnosticWarn {
					t.Fatalf("host DNS status = %v, want warn", item.Status)
				}
				if !strings.Contains(item.Detail, "IPv6 lien-local") || !strings.Contains(item.Detail, "networksetup") {
					t.Fatalf("unexpected host DNS detail: %q", item.Detail)
				}
			}
		}
	}
	if !found {
		t.Fatal("missing DNS de l'hôte item in diagnostics report")
	}
}
