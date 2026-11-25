package codebox

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"time"
)

const (
	guestDNSProbeTimeout = 5 * time.Second
	hostDNSProbeTimeout  = 3 * time.Second
)

// ParseDNSNameservers parses CODEBOX_DNS_NAMESERVERS: a comma- or
// whitespace-separated list of IP addresses or IP:port pairs (for example
// "9.9.9.9,2620:fe::fe"). Link-local IPv6 addresses (fe80::/10) are rejected
// because Microsandbox cannot route them without an interface scope.
func ParseDNSNameservers(raw string) ([]string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) == 0 {
		return nil, fmt.Errorf("CODEBOX_DNS_NAMESERVERS ne contient aucune adresse IP valide")
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, entry := range fields {
		addr, normalized, err := parseDNSNameserverEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("CODEBOX_DNS_NAMESERVERS contient une adresse invalide %q (attendu : IP ou IP:port, ex. 9.9.9.9) : %w", entry, err)
		}
		if addr.Is6() && addr.IsLinkLocalUnicast() {
			return nil, fmt.Errorf("CODEBOX_DNS_NAMESERVERS ne peut pas utiliser une adresse IPv6 lien-local (%s) ; indiquez un serveur DNS routable (ex. 9.9.9.9)", entry)
		}
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out, nil
}

func parseDNSNameserverEntry(entry string) (netip.Addr, string, error) {
	if addr, err := netip.ParseAddr(entry); err == nil {
		if addr.Zone() != "" {
			return addr, "", fmt.Errorf("zone réseau non prise en charge")
		}
		return addr, addr.String(), nil
	}
	ap, err := netip.ParseAddrPort(entry)
	if err != nil {
		return netip.Addr{}, "", err
	}
	if ap.Addr().Zone() != "" {
		return ap.Addr(), "", fmt.Errorf("zone réseau non prise en charge")
	}
	if ap.Port() == 0 {
		return ap.Addr(), "", fmt.Errorf("le port doit être supérieur à 0")
	}
	return ap.Addr(), ap.String(), nil
}

// HostDNSInfo summarizes the DNS servers currently advertised by the host OS.
type HostDNSInfo struct {
	Nameservers       []string
	LinkLocalIPv6Only bool
}

// isLinkLocalIPv6Nameserver reports whether raw is an IPv6 link-local address
// (fe80::/10), with or without an RFC 4007 zone suffix such as %en0.
func isLinkLocalIPv6Nameserver(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Is6() && addr.IsLinkLocalUnicast()
	}
	if idx := strings.IndexByte(s, '%'); idx > 0 {
		if addr, err := netip.ParseAddr(s[:idx]); err == nil {
			return addr.Is6() && addr.IsLinkLocalUnicast()
		}
	}
	return false
}

func classifyHostDNS(nameservers []string) HostDNSInfo {
	if len(nameservers) == 0 {
		return HostDNSInfo{}
	}
	allLinkLocal := true
	for _, ns := range nameservers {
		if !isLinkLocalIPv6Nameserver(ns) {
			allLinkLocal = false
			break
		}
	}
	return HostDNSInfo{
		Nameservers:       nameservers,
		LinkLocalIPv6Only: allLinkLocal,
	}
}

// parseResolvConfNameservers extracts nameserver entries from resolv.conf.
func parseResolvConfNameservers(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			ns := fields[1]
			if !seen[ns] {
				seen[ns] = true
				out = append(out, ns)
			}
		}
	}
	return out
}

// parseScutilDNSNameservers extracts the primary (unscoped, default-route)
// nameserver entries from macOS `scutil --dns` output.
func parseScutilDNSNameservers(content string) []string {
	var out []string
	seen := map[string]bool{}
	inPrimaryBlock := false
	inUnscopedResolver := false
	hasDomain := false
	var current []string

	flush := func() {
		if inUnscopedResolver && !hasDomain {
			for _, ns := range current {
				if !seen[ns] {
					seen[ns] = true
					out = append(out, ns)
				}
			}
		}
		current = nil
		hasDomain = false
		inUnscopedResolver = false
	}

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "DNS configuration (for scoped queries)") {
			flush()
			break
		}
		if line == "DNS configuration" {
			inPrimaryBlock = true
			continue
		}
		if !inPrimaryBlock {
			continue
		}
		if strings.HasPrefix(line, "resolver #") {
			flush()
			inUnscopedResolver = true
			continue
		}
		if !inUnscopedResolver {
			continue
		}
		if strings.HasPrefix(line, "domain ") || strings.HasPrefix(line, "domain:") {
			hasDomain = true
			continue
		}
		if strings.HasPrefix(line, "nameserver[") {
			if idx := strings.IndexByte(line, ':'); idx >= 0 {
				ns := strings.TrimSpace(line[idx+1:])
				if ns != "" {
					current = append(current, ns)
				}
			}
		}
	}
	flush()
	return out
}

// InspectHostDNS reads the host's configured DNS servers on macOS and Linux.
// It is read-only and returns an empty HostDNSInfo on unsupported platforms or
// probe failures.
func InspectHostDNS(ctx context.Context, goos string, run func(context.Context, string, ...string) (ExecResult, error), fs FS) HostDNSInfo {
	if fs == nil {
		fs = DefaultFS
	}
	switch goos {
	case "darwin":
		if run != nil {
			res, err := run(ctx, "scutil", "--dns")
			if err == nil && res.ExitCode == 0 {
				if servers := parseScutilDNSNameservers(res.Stdout); len(servers) > 0 {
					return classifyHostDNS(servers)
				}
			}
		}
		if raw, err := fs.ReadFile("/etc/resolv.conf"); err == nil {
			return classifyHostDNS(parseResolvConfNameservers(string(raw)))
		}
	case "linux":
		if raw, err := fs.ReadFile("/etc/resolv.conf"); err == nil {
			return classifyHostDNS(parseResolvConfNameservers(string(raw)))
		}
	}
	return HostDNSInfo{}
}

// GuestDNSDiagnostic reports a confirmed guest DNS resolution failure where
// the host can resolve the same domain, preventing a long agent timeout (#508).
type GuestDNSDiagnostic struct {
	Host         string
	GuestFailed  bool
	HostResolved bool
	HostDNS      HostDNSInfo
	GOOS         string
}

// Error formats the diagnostic in French with actionable remediation.
func (d *GuestDNSDiagnostic) Error() string {
	if d == nil {
		return ""
	}
	if d.HostDNS.LinkLocalIPv6Only {
		resolvers := strings.Join(d.HostDNS.Nameservers, ", ")
		if d.GOOS == "darwin" {
			return fmt.Sprintf(
				"la machine isolée ne peut pas résoudre %s (alors que le poste y accède) : "+
					"le poste n'expose qu'un serveur DNS IPv6 lien-local (%s, fréquent en partage de connexion), "+
					"que Microsandbox ne sait pas encore relayer.\n"+
					"Contournement : définissez un DNS routable sur le poste "+
					"(ex. 'networksetup -setdnsservers Wi-Fi 9.9.9.9 2620:fe::fe') puis lancez 'codebox restart', "+
					"ou recréez la machine avec 'CODEBOX_DNS_NAMESERVERS=9.9.9.9 codebox recreate --microsandbox'.",
				d.Host, resolvers)
		}
		return fmt.Sprintf(
			"la machine isolée ne peut pas résoudre %s (alors que le poste y accède) : "+
				"le poste n'expose qu'un serveur DNS IPv6 lien-local (%s, fréquent en partage de connexion), "+
				"que Microsandbox ne sait pas encore relayer.\n"+
				"Contournement : configurez un serveur DNS routable sur le poste puis lancez 'codebox restart', "+
				"ou recréez la machine avec 'CODEBOX_DNS_NAMESERVERS=9.9.9.9 codebox recreate --microsandbox'.",
			d.Host, resolvers)
	}
	return fmt.Sprintf(
		"la machine isolée ne peut pas résoudre %s alors que le poste y accède "+
			"(Microsandbox lit les serveurs DNS du poste uniquement au démarrage de la machine).\n"+
			"Lancez 'codebox restart' pour relire la configuration DNS actuelle du poste, "+
			"ou 'CODEBOX_DNS_NAMESERVERS=9.9.9.9 codebox recreate --microsandbox' si le réseau actuel ne fournit pas de DNS exploitable.",
		d.Host)
}

func (m *MicrosandboxRuntime) dnsProbeHost() string {
	providers := m.cfg.Providers
	if len(providers) == 0 {
		return msbAllowHost
	}
	for _, p := range providers {
		if !p.NoKey && p.Host != "" {
			return p.Host
		}
	}
	return ""
}

func (m *MicrosandboxRuntime) lookupHostDNS(ctx context.Context, host string) bool {
	if m.hostDNSLookup != nil {
		return m.hostDNSLookup(ctx, host)
	}
	probeCtx, cancel := context.WithTimeout(ctx, hostDNSProbeTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(probeCtx, host)
	return err == nil && len(addrs) > 0
}

func (m *MicrosandboxRuntime) inspectHostDNSInfo(ctx context.Context) HostDNSInfo {
	if m.hostDNSInspect != nil {
		return m.hostDNSInspect(ctx)
	}
	probeCtx, cancel := context.WithTimeout(ctx, hostDNSProbeTimeout)
	defer cancel()
	return InspectHostDNS(probeCtx, m.hostOS(), (OSRunner{}).Run, DefaultFS)
}

func (m *MicrosandboxRuntime) hostOS() string {
	if m.goos != "" {
		return m.goos
	}
	return runtime.GOOS
}

// CheckGuestDNS verifies that the running Microsandbox guest can resolve the
// configured remote inference provider host before attaching an agent TUI
// (#508). It returns a non-nil *GuestDNSDiagnostic only when the guest fails
// while the host succeeds (avoiding false positives when offline).
func (m *MicrosandboxRuntime) CheckGuestDNS(ctx context.Context) *GuestDNSDiagnostic {
	host := m.dnsProbeHost()
	if host == "" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, guestDNSProbeTimeout)
	defer cancel()
	_, _, code, err := m.Client.ExecCapture(probeCtx, m.InstanceName(), "getent hosts "+shellQuote(host)+" >/dev/null 2>&1")
	if err != nil || code == 0 {
		return nil
	}
	if !m.lookupHostDNS(ctx, host) {
		return nil
	}
	return &GuestDNSDiagnostic{
		Host:         host,
		GuestFailed:  true,
		HostResolved: true,
		HostDNS:      m.inspectHostDNSInfo(ctx),
		GOOS:         m.hostOS(),
	}
}
