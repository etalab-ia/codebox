package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func portsCommand(args []string) (int, error) {
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return 2, fmt.Errorf("usage: codebox ports list | add <host[:guest]> | remove <host>")
		}
		pc, err := codebox.DiscoverProject(".")
		if err != nil {
			return 1, err
		}
		pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root))
		if err != nil {
			return 1, fmt.Errorf("read project port configuration: %w", err)
		}
		if err := codebox.ValidatePortMappings(pm.Ports); err != nil {
			return 1, fmt.Errorf("invalid project port configuration: %w", err)
		}
		if pm.Ports == nil {
			fmt.Println("Configured: legacy default preview forwards 3000-3010 (TCP, loopback)")
			return 0, nil
		}
		if len(pm.Ports) == 0 {
			fmt.Println("No preview ports configured")
			return 0, nil
		}
		for _, p := range pm.Ports {
			fmt.Printf("127.0.0.1:%d -> guest:%d (TCP)\n", p.Host, p.Guest)
		}
		return 0, nil
	}
	if len(args) != 2 || (args[0] != "add" && args[0] != "remove") {
		return 2, fmt.Errorf("usage: codebox ports list | add <host[:guest]> | remove <host>")
	}
	pc, err := codebox.DiscoverProject(".")
	if err != nil {
		return 1, err
	}
	path := codebox.ProjectManifestPath(pc.Root)
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, path)
	if err != nil {
		return 1, fmt.Errorf("read project port configuration: %w", err)
	}
	if err := codebox.ValidatePortMappings(pm.Ports); err != nil {
		return 1, fmt.Errorf("invalid project port configuration: %w", err)
	}
	ports := append([]codebox.PortMapping(nil), pm.Ports...)
	switch args[0] {
	case "add":
		parts := strings.Split(args[1], ":")
		if len(parts) > 2 {
			return 2, fmt.Errorf("port mapping must be host or host:guest")
		}
		host, err := parsePort(parts[0])
		if err != nil {
			return 2, err
		}
		guest := host
		if len(parts) == 2 {
			guest, err = parsePort(parts[1])
			if err != nil {
				return 2, err
			}
		}
		for _, p := range ports {
			if p.Host == host {
				return 2, fmt.Errorf("host port %d is already configured; remove it before remapping", host)
			}
		}
		ports = append(ports, codebox.PortMapping{Host: host, Guest: guest})
	case "remove":
		host, err := parsePort(args[1])
		if err != nil {
			return 2, err
		}
		found := false
		filtered := ports[:0]
		for _, p := range ports {
			if p.Host == host {
				found = true
				continue
			}
			filtered = append(filtered, p)
		}
		if !found {
			return 1, fmt.Errorf("host port %d is not configured", host)
		}
		ports = filtered
	}
	if err := codebox.ValidatePortMappings(ports); err != nil {
		return 2, err
	}
	if err := codebox.WriteProjectPorts(codebox.DefaultFS, path, ports); err != nil {
		return 1, err
	}
	fmt.Printf("Saved %d preview port mapping(s) in %s.\n", len(ports), path)
	fmt.Println("Changes apply only when the Microsandbox is created; ordinary restart will not apply them.")
	fmt.Println("To apply, run 'codebox recreate --microsandbox' after confirming guest-only files and sessions can be lost.")
	return 0, nil
}

func parsePort(value string) (uint16, error) {
	n, err := strconv.ParseUint(value, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid TCP port %q: expected a number from 1 to 65535", value)
	}
	return uint16(n), nil
}
