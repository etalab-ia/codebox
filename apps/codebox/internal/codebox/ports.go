package codebox

import (
	"fmt"
	"sort"
)

// ValidatePortMappings checks user-owned TCP mappings and sorts them by host
// port so manifest diffs and reconcile state are deterministic.
func ValidatePortMappings(ports []PortMapping) error {
	seen := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		if p.Host == 0 || p.Guest == 0 {
			return fmt.Errorf("les ports hôte et invité doivent être compris entre 1 et 65535")
		}
		if p.Host == DefaultPort {
			return fmt.Errorf("le port hôte %d est réservé au backend OpenCode", DefaultPort)
		}
		if p.Guest == DefaultPort {
			return fmt.Errorf("le port invité %d est réservé au backend OpenCode", DefaultPort)
		}
		if seen[p.Host] {
			return fmt.Errorf("le port hôte %d est mappé plusieurs fois", p.Host)
		}
		seen[p.Host] = true
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Host < ports[j].Host })
	return nil
}

func equalPortMappings(a, b []PortMapping) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func portMappingsChanged(appliedConfigured bool, applied, desired []PortMapping, desiredConfigured bool) bool {
	return appliedConfigured != desiredConfigured || !equalPortMappings(applied, desired)
}
