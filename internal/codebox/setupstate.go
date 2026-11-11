package codebox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// This file implements the P11 setup journal: the persisted progress record
// that makes an interrupted setup resumable. The wizard runs ordered stages
// (preflight, credential, identity, apply); each stage's inputs and outcome
// are journaled to host state, so a retry starts where it stopped instead of
// re-asking for the Albert key it already holds. The journal carries no
// secret values — a stored credential lives in the P08 store, referenced by
// kind only.

// SetupStage is one wizard stage.
type SetupStage string

const (
	// StagePreflight checks platform, virtualization and disk. Read-only.
	StagePreflight SetupStage = "preflight"
	// StageCredential stores the Albert credential (and optionally GitHub).
	StageCredential SetupStage = "credential"
	// The remaining stages of the plan (identity, settings, runtime, done)
	// are not journaled as Stage values: the flow is field-driven (a stage
	// is done when its journal field is set), so no constants exist for
	// them. Stage is the coarse "where did it stop" marker only.
)

// SetupJournal is the persisted wizard state.
type SetupJournal struct {
	SchemaVersion int `json:"schemaVersion"`
	// Stage is the next stage to run.
	Stage SetupStage `json:"stage"`
	// Preflight records the preflight outcome, so a retry does not re-run
	// a fatal check the user already saw.
	Preflight SetupPreflight `json:"preflight,omitempty"`
	// CredentialKinds lists the kinds stored so far (never values).
	CredentialKinds []string `json:"credentialKinds,omitempty"`
	// Platforms lists the inference platforms chosen by this run, in display
	// order. It carries no secret: each platform's key lives in the store and
	// is referenced by kind only.
	Platforms []SetupPlatform `json:"platforms,omitempty"`
	// AlbertVerified records whether the stored Albert key passed endpoint
	// validation. Nil means an older journal did not record the outcome.
	AlbertVerified *bool `json:"albertVerified,omitempty"`
	// GitHubLogin is the account returned by GitHub when its token was checked.
	GitHubLogin string `json:"githubLogin,omitempty"`
	// GitHubVerified records whether the stored token passed API validation.
	// Nil means an older journal did not record the outcome.
	GitHubVerified *bool `json:"githubVerified,omitempty"`
	// StoreKind records which store the credentials went into (native or
	// file), so a resumed run reuses the same store instead of silently
	// switching to the native one.
	StoreKind string `json:"storeKind,omitempty"`
	// SkipGitHub records an explicit "no" to the optional GitHub prompt, so
	// a resumed run does not re-ask.
	SkipGitHub bool `json:"skipGitHub,omitempty"`
	// GitHubReplacePending records that the user chose to replace an existing
	// token but left before storing its replacement. A resume must re-ask.
	GitHubReplacePending bool `json:"githubReplacePending,omitempty"`
	// GitName and GitEmail are the chosen identity.
	GitName  string `json:"gitName,omitempty"`
	GitEmail string `json:"gitEmail,omitempty"`
	// DefaultModel is the resolved model selection, when one was chosen.
	DefaultModel string `json:"defaultModel,omitempty"`
	// Models is the set of Albert models enabled in the setup run.
	Models []string `json:"models,omitempty"`
	// ModelChosen records that the model prompt was answered. Without it, a
	// resume could confuse an interruption with an explicit choice and restore
	// a previously saved model set.
	ModelChosen bool `json:"modelChosen,omitempty"`
	// StatsEnabled and StatsConsentAsked persist the statistics consent
	// answered during this run, so a resume does not ask again.
	StatsEnabled      bool `json:"statsEnabled,omitempty"`
	StatsConsentAsked bool `json:"statsConsentAsked,omitempty"`
}

// SetupPlatform is the journal form of one chosen inference platform.
type SetupPlatform struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	BaseURL string `json:"baseURL,omitempty"`
	// Verified records whether the stored key passed the endpoint check. Nil
	// means an older journal did not record the outcome.
	Verified *bool `json:"verified,omitempty"`
}

// PlatformSettings converts the journal platforms to the engine form.
func (j SetupJournal) PlatformSettings() []ProviderSettings {
	out := make([]ProviderSettings, 0, len(j.Platforms))
	for _, p := range j.Platforms {
		out = append(out, ProviderSettings{Kind: ProviderKind(p.Kind), Name: p.Name, BaseURL: p.BaseURL})
	}
	return out
}

// SetPlatforms stores the resolved platform set in the journal, keeping any
// verification outcome already recorded for an unchanged platform.
func (j *SetupJournal) SetPlatforms(providers []InferenceProvider) {
	previous := map[string]SetupPlatform{}
	for _, p := range j.Platforms {
		previous[p.Kind] = p
	}
	j.Platforms = make([]SetupPlatform, 0, len(providers))
	for _, p := range providers {
		entry := SetupPlatform{Kind: string(p.Kind), Name: p.Name, BaseURL: p.BaseURL}
		if old, ok := previous[string(p.Kind)]; ok && old.BaseURL == entry.BaseURL {
			entry.Verified = old.Verified
		}
		j.Platforms = append(j.Platforms, entry)
	}
}

// Platform returns the journal entry for one kind.
func (j SetupJournal) Platform(kind ProviderKind) (SetupPlatform, bool) {
	for _, p := range j.Platforms {
		if p.Kind == string(kind) {
			return p, true
		}
	}
	return SetupPlatform{}, false
}

const setupJournalSchemaVersion = 1

// SetupPreflight is the read-only diagnosis result.
type SetupPreflight struct {
	// Platform is runtime.GOOS/GOARCH.
	Platform string
	// VirtualizationOK reports whether the runtime's own doctor accepts the
	// host (KVM on Linux, the framework on macOS).
	VirtualizationOK bool
	// VirtualizationDetail is the doctor's output or failure summary.
	VirtualizationDetail string
	// DiskFreeBytes is the free space of the runtime's state home.
	DiskFreeBytes int64
	// DiskOK reports whether DiskFreeBytes clears the minimum.
	DiskOK bool
	// DiskDetail carries a disk-probe failure (kept separate from the
	// virtualization detail: they are different failures).
	DiskDetail string
	// RuntimeInstalled reports whether the managed runtime is already
	// present and trusted.
	RuntimeInstalled bool
	// Fatal records whether any check makes installation impossible.
	Fatal bool
}

// SetupMinDiskBytes is the minimum free disk for the managed runtime plus
// guest state. The archive is bounded at 128 MiB; the extracted runtime plus
// a first guest easily reaches hundreds of MiB, so the gate is 1 GiB.
const SetupMinDiskBytes = 1 << 30

// setupJournalPath is the journal location, in host state.
func setupJournalPath(stateDir string) string {
	return filepath.Join(stateDir, "setup-journal.json")
}

// SetupJournalPath exposes the journal path for the CLI.
func SetupJournalPath(stateDir string) string { return setupJournalPath(stateDir) }

// ReadSetupJournal loads the journal; a missing file means a fresh start.
func ReadSetupJournal(fs FS, stateDir string) (SetupJournal, error) {
	data, err := fs.ReadFile(setupJournalPath(stateDir))
	if err != nil {
		if os.IsNotExist(err) {
			return SetupJournal{SchemaVersion: setupJournalSchemaVersion, Stage: StagePreflight}, nil
		}
		return SetupJournal{}, err
	}
	var j SetupJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return SetupJournal{}, fmt.Errorf("setup journal %s is corrupt (%w): remove it and re-run 'codebox setup' to start over", setupJournalPath(stateDir), err)
	}
	if j.SchemaVersion > setupJournalSchemaVersion {
		return SetupJournal{}, fmt.Errorf("setup journal: schemaVersion %d is newer than this build supports (%d)", j.SchemaVersion, setupJournalSchemaVersion)
	}
	return j, nil
}

// WriteSetupJournal atomically persists the journal.
func WriteSetupJournal(fs FS, stateDir string, j SetupJournal) error {
	j.SchemaVersion = setupJournalSchemaVersion
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return atomicWrite(fs, setupJournalPath(stateDir), append(data, '\n'), 0o600)
}

// RemoveSetupJournal drops the journal (setup done or cancelled).
func RemoveSetupJournal(fs FS, stateDir string) error {
	err := fs.Remove(setupJournalPath(stateDir))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// ContainsCredentialKind reports whether the journal has stored the kind.
func ContainsCredentialKind(kinds []string, kind string) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}
