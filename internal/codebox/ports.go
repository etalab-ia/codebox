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
			return fmt.Errorf("host and guest ports must be between 1 and 65535")
		}
		if p.Host == DefaultPort {
			return fmt.Errorf("host port %d is reserved for the OpenCode backend", DefaultPort)
		}
		if p.Guest == DefaultPort {
			return fmt.Errorf("guest port %d is reserved for the OpenCode backend", DefaultPort)
		}
		if seen[p.Host] {
			return fmt.Errorf("host port %d is mapped more than once", p.Host)
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
