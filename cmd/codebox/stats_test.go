package main

import (
	"bufio"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/etalab-ia/codebox/internal/codebox"
)

func statsTestPaths(t *testing.T) (stateDir, settingsPath string) {
	t.Helper()
	stateDir, _ = setupTestEnv(t)
	var err error
	settingsPath, err = codebox.UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEBOX_STATS_URL", "")
	previousSpawn := codebox.StatsFlushSpawn
	codebox.StatsFlushSpawn = func() {}
	t.Cleanup(func() { codebox.StatsFlushSpawn = previousSpawn })
	return stateDir, settingsPath
}

func TestStatsOffClearsTheQueueAndDisablesSending(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, true, true); err != nil {
		t.Fatal(err)
	}
	event := codebox.StatsEvent{SchemaVersion: codebox.StatsSchemaVersion, EventID: "e1", Kind: "setup"}
	if err := codebox.EnqueueStatsEvent(codebox.DefaultFS, stateDir, event); err != nil {
		t.Fatal(err)
	}
	if code, err := statsCmd([]string{"off"}); code != 0 || err != nil {
		t.Fatalf("stats off: code=%d err=%v", code, err)
	}
	enabled, asked := codebox.ReadStatsConsent(codebox.DefaultFS, settingsPath)
	if enabled || !asked {
		t.Fatalf("consent state = enabled=%v asked=%v", enabled, asked)
	}
	if events, _ := codebox.PendingStatsEvents(codebox.DefaultFS, stateDir); len(events) != 0 {
		t.Fatalf("stats off must drop the queue: %v", events)
	}
}

func TestStatsOffInvalidatesAPendingJournalConsent(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	j := codebox.SetupJournal{
		SchemaVersion: 1, Stage: codebox.StageCredential,
		StatsEnabled: true, StatsConsentAsked: true,
	}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, true, true); err != nil {
		t.Fatal(err)
	}
	if code, err := statsCmd([]string{"off"}); code != 0 || err != nil {
		t.Fatalf("stats off: code=%d err=%v", code, err)
	}
	after, err := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.StatsEnabled {
		t.Fatal("a pending journal must not resurrect a withdrawn consent")
	}
}

func TestStatsShowPrintsExactlyTheQueuedPayload(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, true, true); err != nil {
		t.Fatal(err)
	}
	secretRoot := filepath.Join(t.TempDir(), "projet-sensible-99")
	event, err := codebox.BuildStatsEvent(codebox.DefaultFS, stateDir, codebox.StatsEventInput{
		Kind: "init", Version: "1.2.3", ProjectRoot: secretRoot,
		Init: &codebox.InitStats{Skills: map[string]bool{"official/rgaa": true}, Runtime: "microsandbox", Isolation: "full", Harness: "opencode", SkillsStorage: "versioned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := codebox.EnqueueStatsEvent(codebox.DefaultFS, stateDir, event); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code, cerr := statsCmd([]string{"show"}); code != 0 || cerr != nil {
			t.Fatalf("stats show: code=%d err=%v", code, cerr)
		}
	})
	if !strings.Contains(out, string(encoded)) {
		t.Fatalf("stats show must print the queued event verbatim:\n%s", out)
	}
	if !strings.Contains(out, "File d'envoi : 1 événement(s)") {
		t.Fatalf("queue count missing:\n%s", out)
	}
	if strings.Contains(out, "projet-sensible-99") {
		t.Fatalf("stats show leaked the project path:\n%s", out)
	}
}

func TestRecordInitStatsCarriesTheInventory(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	previousSpawn := codebox.StatsFlushSpawn
	codebox.StatsFlushSpawn = func() {}
	t.Cleanup(func() { codebox.StatsFlushSpawn = previousSpawn })
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, true, true); err != nil {
		t.Fatal(err)
	}
	recordInitStats(codebox.InitAnswers{
		Root:            filepath.Join(t.TempDir(), "projet-prive"),
		Runtime:         codebox.RuntimeMicrosandbox,
		Isolation:       codebox.IsolationFull,
		Skills:          []string{"official/rgaa"},
		KnownSkills:     []string{"official/rgaa", "official/security"},
		MCPConnectors:   []string{"data-gouv"},
		GitHubCase:      1,
		Advanced:        true,
		SkillsLocalOnly: false,
	})
	events, err := codebox.PendingStatsEvents(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "init" {
		t.Fatalf("events = %+v", events)
	}
	init := events[0].Payload.Init
	if init == nil {
		t.Fatal("init payload missing")
	}
	if !init.Skills["official/rgaa"] || init.Skills["official/security"] {
		t.Fatalf("skills 0/1 map = %v", init.Skills)
	}
	if !init.MCPs["data-gouv"] {
		t.Fatalf("mcps map = %v", init.MCPs)
	}
	if init.GitHubCase != 1 || !init.Advanced || init.SkillsStorage != "versioned" || init.Harness != "opencode" {
		t.Fatalf("init payload = %+v", init)
	}
	data, _ := json.Marshal(events[0])
	if strings.Contains(string(data), "projet-prive") {
		t.Fatalf("payload leaked the project path: %s", data)
	}
}

func TestSetupConsentStaysOffWithoutATerminal(t *testing.T) {
	statsTestPaths(t)
	forceNonTTY(t)
	enabled, answered, err := setupStatsConsent(bufio.NewReader(strings.NewReader("")))
	if err != nil {
		t.Fatal(err)
	}
	if enabled || answered {
		t.Fatalf("a piped run must not answer consent implicitly: enabled=%v answered=%v", enabled, answered)
	}
}

func TestSetupStatsProvidersReflectsTheFinalSelection(t *testing.T) {
	providers, err := codebox.ResolveProviders([]codebox.ProviderSettings{{Kind: codebox.ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	got := setupStatsProviders(providers)
	if len(got) != 1 || got[0] != "mistral" {
		t.Fatalf("a removed platform must not be reported: %v", got)
	}
}

func TestStatsOnSynchronizesThePendingJournal(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	j := codebox.SetupJournal{
		SchemaVersion: 1, Stage: codebox.StageCredential,
		StatsEnabled: false, StatsConsentAsked: true,
	}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, false, true); err != nil {
		t.Fatal(err)
	}
	if code, err := statsCmd([]string{"on"}); code != 0 || err != nil {
		t.Fatalf("stats on: code=%d err=%v", code, err)
	}
	after, err := codebox.ReadSetupJournal(codebox.DefaultFS, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if !after.StatsEnabled {
		t.Fatal("stats on must synchronize a pending journal refusal")
	}
}

func TestStatsShowUsesThePendingJournalDecision(t *testing.T) {
	stateDir, settingsPath := statsTestPaths(t)
	j := codebox.SetupJournal{
		SchemaVersion: 1, Stage: codebox.StageCredential,
		StatsEnabled: true, StatsConsentAsked: true,
	}
	if err := codebox.WriteSetupJournal(codebox.DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	if err := codebox.WriteStatsConsent(codebox.DefaultFS, settingsPath, false, false); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code, err := statsShowCmd(codebox.DefaultFS, stateDir, settingsPath); code != 0 || err != nil {
			t.Fatalf("stats show: %v", err)
		}
	})
	if !strings.Contains(out, "Consentement        accepté") {
		t.Fatalf("a pending acceptance must be displayed: %s", out)
	}
}

func TestSetupConsentSkippedKeepsExistingAcceptance(t *testing.T) {
	statsTestPaths(t)
	forceNonTTY(t)
	enabled, answered, err := setupStatsConsent(bufio.NewReader(strings.NewReader("")))
	if err != nil {
		t.Fatal(err)
	}
	if enabled || answered {
		t.Fatal("a skipped question answers nothing")
	}
}
