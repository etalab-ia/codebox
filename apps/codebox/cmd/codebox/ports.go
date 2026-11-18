package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

func portsCommand(args []string) (int, error) {
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return 2, fmt.Errorf("usage : codebox ports list | add <host[:guest]> | remove <host>")
		}
		pc, err := codebox.DiscoverProject(".")
		if err != nil {
			return 1, err
		}
		pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root))
		if err != nil {
			return 1, fmt.Errorf("lire la configuration des ports du projet : %w", err)
		}
		if err := codebox.ValidatePortMappings(pm.Ports); err != nil {
			return 1, fmt.Errorf("configuration des ports du projet invalide : %w", err)
		}
		if pm.Ports == nil {
			fmt.Println("Configuré : la redirection d'aperçu par défaut héritée transfère 3000-3010 (TCP, loopback)")
			return 0, nil
		}
		if len(pm.Ports) == 0 {
			fmt.Println("Aucun port d'aperçu configuré")
			return 0, nil
		}
		for _, p := range pm.Ports {
			fmt.Printf("127.0.0.1:%d -> machine isolée:%d (TCP)\n", p.Host, p.Guest)
		}
		return 0, nil
	}
	if len(args) != 2 || (args[0] != "add" && args[0] != "remove") {
		return 2, fmt.Errorf("usage : codebox ports list | add <host[:guest]> | remove <host>")
	}
	pc, err := codebox.DiscoverProject(".")
	if err != nil {
		return 1, err
	}
	path := codebox.ProjectManifestPath(pc.Root)
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, path)
	if err != nil {
		return 1, fmt.Errorf("lire la configuration des ports du projet : %w", err)
	}
	if err := codebox.ValidatePortMappings(pm.Ports); err != nil {
		return 1, fmt.Errorf("configuration des ports du projet invalide : %w", err)
	}
	ports := append([]codebox.PortMapping(nil), pm.Ports...)
	switch args[0] {
	case "add":
		parts := strings.Split(args[1], ":")
		if len(parts) > 2 {
			return 2, fmt.Errorf("la correspondance de port doit être host ou host:guest")
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
				return 2, fmt.Errorf("le port hôte %d est déjà configuré ; supprimez-le avant de le remapper", host)
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
			return 1, fmt.Errorf("le port hôte %d n'est pas configuré", host)
		}
		ports = filtered
	}
	if err := codebox.ValidatePortMappings(ports); err != nil {
		return 2, err
	}
	if err := codebox.WriteProjectPorts(codebox.DefaultFS, path, ports); err != nil {
		return 1, err
	}
	fmt.Printf("Enregistré %d correspondance(s) de port d'aperçu dans %s.\n", len(ports), path)
	fmt.Println("Les changements ne s'appliquent qu'à la création de la machine Microsandbox ; un redémarrage ordinaire ne les appliquera pas.")
	fmt.Println("Pour les appliquer, lancez 'codebox recreate --microsandbox' après avoir confirmé que les fichiers et sessions propres à la machine isolée peuvent être perdus.")
	return 0, nil
}

func parsePort(value string) (uint16, error) {
	n, err := strconv.ParseUint(value, 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("port TCP %q invalide : un nombre de 1 à 65535 est attendu", value)
	}
	return uint16(n), nil
}
