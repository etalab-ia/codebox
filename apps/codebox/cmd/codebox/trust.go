package main

import (
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

var launchTrustTTYFn = isTTY
var launchTrustChoiceFn = func(detail string) (string, error) {
	choice := "quit"
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Ce projet peut exécuter des plugins et des commandes MCP.").
			Description(detail).Options(
			huh.NewOption("Approuver et démarrer", "approve"),
			huh.NewOption("Voir le détail", "detail"),
			huh.NewOption("Quitter", "quit"),
		).Value(&choice),
	)))
	return choice, err
}

func approveLaunchInputs(root string, inputs []codebox.UnapprovedInput) error {
	var paths []string
	for _, input := range inputs {
		paths = append(paths, input.Path+" ("+input.Reason+")")
	}
	declared, err := codebox.DiscoverProjectExecutionInputs(root)
	if err != nil {
		return err
	}
	for _, plugin := range declared.Plugins {
		paths = append(paths, "Plugin : "+plugin)
	}
	for _, command := range declared.MCPCommands {
		paths = append(paths, "Commande MCP : "+command)
	}
	for _, endpoint := range declared.MCPEndpoints {
		paths = append(paths, "Service MCP : "+endpoint)
	}
	detail := fmt.Sprintf("%d entrée(s) à approuver. L'approbation reste sur ce poste et porte sur leur contenu actuel.", len(inputs))
	for {
		choice, err := launchTrustChoiceFn(detail)
		if err != nil {
			return err
		}
		switch choice {
		case "approve":
			return codebox.ApproveReviewedInputs(codebox.DefaultFS, codebox.DefaultStateDir(), root, inputs)
		case "detail":
			detail = strings.Join(paths, "\n")
		default:
			return fmt.Errorf("démarrage annulé ; aucune entrée approuvée")
		}
	}
}

// trustCmd implements `codebox trust <subcommand>` (P10): the host-local
// approval of execution-relevant project inputs (OpenCode project and
// auto-discovered plugins, MCP server commands, Pi project extensions and
// settings). The record lives in host state keyed by the canonical project
// root and content hashes — never in the repository.
//
//	trust status    # what is declared, what is approved, what changed
//	trust approve   # record approval for the current content of every input
func trustCmd(args []string, projectRoot string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage : codebox trust status | approve")
		return 2, nil
	}
	switch args[0] {
	case "status":
		return trustStatusCmd(projectRoot)
	case "approve":
		if err := codebox.ApproveExecutionInputs(codebox.DefaultFS, codebox.DefaultStateDir(), projectRoot); err != nil {
			return 1, err
		}
		fmt.Println("Contenu actuel de chaque entrée d'exécution déclarée par ce projet approuvé.")
		fmt.Println("La consignation reste locale au poste et ne voyage jamais avec le dépôt ; un fichier modifié exige une nouvelle approbation.")
		return 0, nil
	default:
		return 2, fmt.Errorf("commande trust inconnue : %s (attendu : status ou approve)", args[0])
	}
}

// trustStatusCmd reports the declared execution inputs and their approval
// state, without creating or changing anything.
func trustStatusCmd(projectRoot string) (int, error) {
	inputs, err := codebox.DiscoverProjectExecutionInputs(projectRoot)
	if err != nil {
		return 1, err
	}
	unapproved, err := codebox.DiscoverUnapprovedInputs(codebox.DefaultFS, codebox.DefaultStateDir(), projectRoot)
	if err != nil {
		return 1, err
	}
	if len(inputs.ExecutionInputPaths(projectRoot)) == 0 {
		fmt.Println("Aucune entrée pertinente pour l'exécution trouvée dans ce projet.")
		return 0, nil
	}
	fmt.Println("Entrées pertinentes pour l'exécution dans ce projet :")
	if inputs.ConfigPath != "" && (len(inputs.Plugins) > 0 || len(inputs.MCPCommands) > 0) {
		for _, p := range inputs.Plugins {
			fmt.Printf("  greffon déclaré dans la configuration du projet : %s\n", p)
		}
		for _, c := range inputs.MCPCommands {
			fmt.Printf("  commande de serveur MCP déclarée dans la configuration du projet : %s\n", c)
		}
		fmt.Printf("  (approuvé en tant que fichier de configuration %s à son empreinte de contenu)\n", inputs.ConfigPath)
	}
	for _, p := range inputs.AutoDiscoveredPlugins {
		fmt.Printf("  greffon auto-découvert (s'exécute sans déclaration) : %s\n", p)
	}
	if inputs.PiProjectConfig != "" {
		fmt.Printf("  réglages Pi locaux au projet (peuvent déclarer des extensions et des paquets) : %s\n", inputs.PiProjectConfig)
	}
	for _, p := range inputs.PiAutoDiscoveredExtensions {
		fmt.Printf("  extension Pi (s'exécute au lancement sans déclaration) : %s\n", p)
	}
	for _, p := range inputs.PiDeclaredCodeFiles {
		fmt.Printf("  code Pi déclaré dans les réglages du projet (extension ou paquet local) : %s\n", p)
	}
	if len(unapproved) == 0 {
		fmt.Println("Toutes les entrées sont approuvées dans leur contenu actuel.")
		return 0, nil
	}
	fmt.Println("Approbation requise (lancez 'codebox trust approve' après examen) :")
	for _, u := range unapproved {
		fmt.Printf("  %s (%s)\n", u.Path, u.Reason)
	}
	return 0, nil
}
