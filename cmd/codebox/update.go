package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func updateCommandSkipsStartupRecovery(args []string) bool {
	for _, arg := range args {
		if arg == "--recover" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func updateCmd(args []string, projectRoot string) (int, error) {
	var selected []string
	approve := false
	recover := false
	rollback := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Println("Usage : codebox update [--skill <catalogue/id>]... [--yes]\n        codebox update --recover [--rollback] [--yes]\n\nCompare les skills de projet versionnées sélectionnées au catalogue actuel.\nSans --yes, l'application demande une confirmation interactive. Une exécution\nsans terminal et sans --yes se contente d'un aperçu. --recover --rollback\nrestaure le manifeste et le verrou précédents consignés dans le journal, après\nsauvegarde des fichiers gérés actuels.")
			return 0, nil
		case "--skill":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return 2, fmt.Errorf("--skill requires a catalogue ID such as official/rgaa")
			}
			i++
			selected = append(selected, args[i])
		case "--yes":
			approve = true
		case "--recover":
			recover = true
		case "--rollback":
			rollback = true
		default:
			return 2, fmt.Errorf("unknown update option %q", args[i])
		}
	}
	if rollback && !recover {
		return 2, fmt.Errorf("--rollback requires --recover")
	}
	if recover {
		if len(selected) > 0 {
			return 2, fmt.Errorf("--recover cannot be combined with --skill")
		}
		if !rollback {
			if err := codebox.RecoverProjectUpdate(codebox.DefaultFS, projectRoot); err != nil {
				return 1, fmt.Errorf("%w\nSi les fichiers gérés actuels contiennent des modifications voulues, inspectez le journal et lancez 'codebox update --recover --rollback' pour restaurer la paire précédente avec sauvegarde", err)
			}
			fmt.Println("Mise à jour de projet en attente récupérée, ou aucune récupération n'était nécessaire.")
			return 0, nil
		}
		if !approve {
			if !isTTY() {
				fmt.Println("Aperçu seulement : le retour arrière restaure le manifeste et le verrou précédents consignés, en ne fusionnant que la section codebox gérée dans AGENTS.md. Les fichiers gérés actuels seront sauvegardés sous .codebox/recovery-backups/. Relancez avec --yes pour approuver.")
				return 0, nil
			}
			fmt.Print("Restaurer la configuration de projet précédente consignée ? Les fichiers gérés actuels seront d'abord sauvegardés. [o/N] ")
			reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if !isAffirmativeAnswer(reply) {
				fmt.Println("Aucun changement appliqué.")
				return 0, nil
			}
		}
		backupDir, err := codebox.RollbackProjectUpdate(codebox.DefaultFS, projectRoot)
		if err != nil {
			return 1, err
		}
		fmt.Printf("Configuration de projet précédente restaurée. Fichiers gérés actuels sauvegardés sous .codebox/recovery-backups/%s.\n", filepath.Base(backupDir))
		return 0, nil
	}
	if err := codebox.RecoverProjectUpdate(codebox.DefaultFS, projectRoot); err != nil {
		return 1, fmt.Errorf("recover project update: %w", err)
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		return 1, err
	}
	if manifest.SkillsLocalOnly {
		return 1, fmt.Errorf("la mise à jour des skills de projet exige des skills versionnées ; les sélections locales n'ont pas été modifiées")
	}
	if len(selected) == 0 && len(manifest.Skills) == 0 {
		plan, err := codebox.PlanProjectUpdate(context.Background(), codebox.DefaultFS, projectRoot, nil, "", nil)
		if err != nil {
			return 1, err
		}
		if !plan.NoOp {
			return 1, fmt.Errorf("unexpected update plan for a project without versioned skills")
		}
		fmt.Println("Aucune skill de projet versionnée n'est sélectionnée ; rien à mettre à jour.")
		return 0, nil
	}
	selectedIDs := make(map[string]bool, len(manifest.Skills))
	for _, id := range manifest.Skills {
		selectedIDs[id] = true
	}
	seen := map[string]bool{}
	for _, id := range selected {
		if !selectedIDs[id] {
			return 2, fmt.Errorf("la skill %q n'est pas sélectionnée dans ce projet", id)
		}
		if seen[id] {
			return 2, fmt.Errorf("la skill %q a été sélectionnée plusieurs fois pour la mise à jour", id)
		}
		seen[id] = true
	}
	revision, err := codebox.LatestProjectSkillsRevision(context.Background())
	if err != nil {
		return 1, err
	}
	plan, err := codebox.PlanProjectUpdate(context.Background(), codebox.DefaultFS, projectRoot, selected, revision, nil)
	if err != nil {
		return 1, err
	}
	if plan.NoOp {
		fmt.Println("Les skills de projet sont déjà à jour ; rien n'a changé.")
		return 0, nil
	}
	fmt.Printf("Mise à jour de skills disponible au commit de catalogue %s :\n", revision)
	for _, change := range plan.Changes {
		from := change.FromRevision
		if from == "" {
			from = "unlocked"
		}
		fromDigest := change.FromSHA256
		if len(fromDigest) > 12 {
			fromDigest = fromDigest[:12]
		}
		toDigest := change.ToSHA256
		if len(toDigest) > 12 {
			toDigest = toDigest[:12]
		}
		fmt.Printf("  %s  %s (%s) -> %s (%s)\n", change.ID, from, fromDigest, change.ToRevision, toDigest)
	}
	managedDiff, err := plan.ManagedInstructionsDiff()
	if err != nil {
		return 1, fmt.Errorf("prepare managed-instructions preview: %w", err)
	}
	if managedDiff != "" {
		fmt.Print(managedDiff)
	}
	if !approve {
		if !isTTY() {
			fmt.Println("Aperçu seulement ; aucun fichier de projet modifié. Relancez avec --yes pour approuver cette mise à jour.")
			return 0, nil
		}
		fmt.Print("Appliquer ces mises à jour de projet sélectionnées ? [o/N] ")
		reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !isAffirmativeAnswer(reply) {
			fmt.Println("Aucun changement appliqué.")
			return 0, nil
		}
	}
	if err := plan.Apply(codebox.DefaultFS); err != nil {
		return 1, err
	}
	fmt.Printf("Mise à jour de %d skills de projet et de leurs instructions gérées.\n", len(plan.Changes))
	fmt.Println("Cette commande ne modifie ni ne recrée les paquets de la machine isolée ni ses images d'exécution.")
	return 0, nil
}

// isAffirmativeAnswer reports whether a free-text confirmation answer means yes.
// French prompts accept both the French and English forms, as the init prompts do.
func isAffirmativeAnswer(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "o", "oui", "y", "yes":
		return true
	}
	return false
}
