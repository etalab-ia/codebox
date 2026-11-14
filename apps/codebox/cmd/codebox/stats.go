package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// statsCmd implements the consent and inspection surface of issue #181:
//
//	stats on    # explicit opt-in
//	stats off   # stop sending immediately and drop the queue
//	stats show  # exactly what is (or would be) sent
//
// `stats` alone is `stats show`.
func statsCmd(args []string) (int, error) {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
		if len(args) > 1 {
			return 2, fmt.Errorf("unknown stats argument: %s (expected on, off or show)", args[1])
		}
	}
	path, err := codebox.UserSettingsPath()
	if err != nil {
		return 1, err
	}
	fs := codebox.DefaultFS
	stateDir := codebox.DefaultStateDir()
	switch sub {
	case "on":
		if err := codebox.WriteStatsConsent(fs, path, true, true); err != nil {
			return 1, err
		}
		// Synchronize a pending setup decision too: resuming an interrupted
		// wizard must not undo an opt-in just given.
		if j, err := codebox.ReadSetupJournal(fs, stateDir); err == nil && !j.StatsEnabled {
			j.StatsEnabled = true
			j.StatsConsentAsked = true
			if err := codebox.WriteSetupJournal(fs, stateDir, j); err != nil {
				return 1, fmt.Errorf("cannot record the consent opt-in in the setup journal: %w", err)
			}
		}
		fmt.Println("Statistiques d'usage : partagées.")
		if codebox.StatsEndpoint() == "" {
			fmt.Println("Aucun endpoint n'est configuré dans cette version : les événements restent locaux et ne sont pas envoyés.")
		}
		codebox.StatsFlushSpawn()
		return 0, nil
	case "off":
		if err := codebox.WriteStatsConsent(fs, path, false, true); err != nil {
			return 1, err
		}
		// Invalidate a pending setup decision BEFORE clearing the queue: an
		// enqueue that reads the journal between these two steps must already
		// see the refusal, or `stats off` would return with events still queued.
		if j, err := codebox.ReadSetupJournal(fs, stateDir); err == nil && j.StatsEnabled {
			j.StatsEnabled = false
			j.StatsConsentAsked = true
			if err := codebox.WriteSetupJournal(fs, stateDir, j); err != nil {
				return 1, fmt.Errorf("cannot record the consent withdrawal in the setup journal: %w", err)
			}
		}
		if err := codebox.ClearStatsQueue(fs, stateDir); err != nil {
			return 1, err
		}
		fmt.Println("Statistiques d'usage : non partagées. Aucun envoi ne sera effectué.")
		return 0, nil
	case "show":
		return statsShowCmd(fs, stateDir, path)
	default:
		return 2, fmt.Errorf("Unknown stats command: %s (expected on, off or show)", sub)
	}
}

func statsShowCmd(fs codebox.FS, stateDir, settingsPath string) (int, error) {
	enabled, asked := codebox.ReadStatsConsent(fs, settingsPath)
	// An answered setup journal decision is newer than the durable setting:
	// display the effective consent, not the stale one.
	if j, err := codebox.ReadSetupJournal(fs, stateDir); err == nil && j.StatsConsentAsked {
		enabled, asked = j.StatsEnabled, true
	}
	fmt.Println("Statistiques d'usage codebox")
	fmt.Println()
	switch {
	case !asked:
		fmt.Println("  Consentement        non demandé (désactivées)")
	case enabled:
		fmt.Println("  Consentement        accepté")
	default:
		fmt.Println("  Consentement        refusé")
	}
	endpoint := codebox.StatsEndpoint()
	if endpoint == "" {
		fmt.Println("  Envoi               désactivé (aucun endpoint configuré)")
	} else if enabled {
		fmt.Printf("  Envoi               activé vers %s\n", endpoint)
	} else {
		fmt.Println("  Envoi               désactivé")
	}
	if id := codebox.ReadStatsMachineID(fs, stateDir); id != "" {
		fmt.Printf("  Identifiant poste   %s\n", id)
	} else {
		fmt.Println("  Identifiant poste   pas encore généré")
	}
	if root := resolveProjectRootForAux(); root != "" {
		if id := codebox.ReadStatsProjectID(fs, stateDir, root); id != "" {
			fmt.Printf("  Identifiant projet  %s\n", id)
		}
	}

	events, err := codebox.PendingStatsEvents(fs, stateDir)
	if err != nil {
		return 1, err
	}
	fmt.Println()
	if len(events) == 0 {
		fmt.Println("Aucun envoi en attente.")
		return 0, nil
	}
	fmt.Printf("File d'envoi : %d événement(s)\n", len(events))
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			return 1, err
		}
		fmt.Println(string(data))
	}
	if !enabled {
		fmt.Fprintln(os.Stderr, "Les événements en file ne seront pas envoyés : le consentement est refusé.")
	}
	return 0, nil
}
