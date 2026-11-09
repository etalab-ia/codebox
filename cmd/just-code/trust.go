package main

import (
	"fmt"
	"os"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
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

func approveLaunchInputs(root string, inputs []justcode.UnapprovedInput) error {
	var paths []string
	for _, input := range inputs {
		paths = append(paths, input.Path+" ("+input.Reason+")")
	}
	declared, err := justcode.DiscoverProjectExecutionInputs(root)
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
			return justcode.ApproveReviewedInputs(justcode.DefaultFS, justcode.DefaultStateDir(), root, inputs)
		case "detail":
			detail = strings.Join(paths, "\n")
		default:
			return fmt.Errorf("démarrage annulé ; aucune entrée approuvée")
		}
	}
}

// trustCmd implements `just-code trust <subcommand>` (P10): the host-local
// approval of execution-relevant project inputs (OpenCode project and
// auto-discovered plugins, MCP server commands, Pi project extensions and
// settings). The record lives in host state keyed by the canonical project
// root and content hashes — never in the repository.
//
//	trust status    # what is declared, what is approved, what changed
//	trust approve   # record approval for the current content of every input
func trustCmd(args []string, projectRoot string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: just-code trust status | approve")
		return 2, nil
	}
	switch args[0] {
	case "status":
		return trustStatusCmd(projectRoot)
	case "approve":
		if err := justcode.ApproveExecutionInputs(justcode.DefaultFS, justcode.DefaultStateDir(), projectRoot); err != nil {
			return 1, err
		}
		fmt.Println("Approved the current content of every execution input this project declares.")
		fmt.Println("The record is host-local and never travels with the repository; a changed file needs a new approval.")
		return 0, nil
	default:
		return 2, fmt.Errorf("Unknown trust command: %s (expected status or approve)", args[0])
	}
}

// trustStatusCmd reports the declared execution inputs and their approval
// state, without creating or changing anything.
func trustStatusCmd(projectRoot string) (int, error) {
	inputs, err := justcode.DiscoverProjectExecutionInputs(projectRoot)
	if err != nil {
		return 1, err
	}
	unapproved, err := justcode.DiscoverUnapprovedInputs(justcode.DefaultFS, justcode.DefaultStateDir(), projectRoot)
	if err != nil {
		return 1, err
	}
	if len(inputs.ExecutionInputPaths(projectRoot)) == 0 {
		fmt.Println("No execution-relevant inputs found in this project.")
		return 0, nil
	}
	fmt.Println("Execution-relevant inputs in this project:")
	if inputs.ConfigPath != "" && (len(inputs.Plugins) > 0 || len(inputs.MCPCommands) > 0) {
		for _, p := range inputs.Plugins {
			fmt.Printf("  plugin declared in the project config: %s\n", p)
		}
		for _, c := range inputs.MCPCommands {
			fmt.Printf("  MCP server command declared in the project config: %s\n", c)
		}
		fmt.Printf("  (approved as the config file %s at its content hash)\n", inputs.ConfigPath)
	}
	for _, p := range inputs.AutoDiscoveredPlugins {
		fmt.Printf("  auto-discovered plugin (executes without declaration): %s\n", p)
	}
	if inputs.PiProjectConfig != "" {
		fmt.Printf("  project-local Pi settings (can declare extensions and packages): %s\n", inputs.PiProjectConfig)
	}
	for _, p := range inputs.PiAutoDiscoveredExtensions {
		fmt.Printf("  Pi extension (executes at launch without declaration): %s\n", p)
	}
	for _, p := range inputs.PiDeclaredCodeFiles {
		fmt.Printf("  Pi code declared in the project settings (extension or local package): %s\n", p)
	}
	if len(unapproved) == 0 {
		fmt.Println("All inputs are approved at their current content.")
		return 0, nil
	}
	fmt.Println("Needs approval (run 'just-code trust approve' after reviewing):")
	for _, u := range unapproved {
		fmt.Printf("  %s (%s)\n", u.Path, u.Reason)
	}
	return 0, nil
}
