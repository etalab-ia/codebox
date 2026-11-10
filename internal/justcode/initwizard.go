package justcode

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The minimal project setup (P12b). This file owns the decisions and the
// files they produce; the CLI layer owns the prompts. Splitting them is what
// makes the flow testable without a terminal, and it is the shape P11's global
// setup already uses (setupwizard.go).
//
// What the setup collects is exactly what the launch path applies, and nothing
// else: where the project is, how the agent is isolated, which model and
// curated remote MCPs it uses, how much machine it gets, and which credential
// it may use. A question whose answer nothing reads is worse than no question:
// instance naming derives from the root, and the versioned location is the
// only supported storage mode.

// InitAnswers is one set of project-setup decisions.
type InitAnswers struct {
	// Root is the project directory. It is required and must exist; it is
	// canonicalized through the same discovery the launch path uses, so the
	// file this writes is the file the launch reads (a Git worktree root, not
	// a subdirectory of one).
	Root string
	// Runtime and Isolation are the agent placement choices. Empty means the
	// built-in default (Microsandbox, full) rather than "unset in the file".
	Runtime   Runtime
	Isolation Isolation
	// Model is the OpenCode model id. Empty keeps the built-in default.
	Model string
	// SmallModel is the lightweight model used for titles. Empty follows the
	// effective main model instead of pinning a duplicate selection.
	SmallModel string
	// CPUs and MemoryMB size the guest. Zero means the built-in default.
	CPUs     int
	MemoryMB int
	// CredentialRef names the credential this project may use (a P08
	// reference). Empty means the global default resolution applies.
	CredentialRef string
	// GitHubWorkflow records the user's project-init choice to approve the
	// optional GitHub binding on this host. It is not written to the manifest:
	// binding approval is a local trust decision (P09).
	GitHubWorkflow bool
	// BrowserResourceGuidance is host-specific review text, never persisted.
	BrowserResourceGuidance string
	// GitHubRemote is the sanitized origin shown in the review. It is derived
	// from the host Git config and is not persisted by InitWizard.
	GitHubRemote GitHubRemote
	// Skills are catalogue IDs selected by this init invocation. SkillsSet
	// distinguishes an unchanged empty answer from an explicit deselection.
	Skills    []string
	SkillsSet bool
	// RetainedSkills lists selected IDs that are absent from the current
	// skill catalogue. Plan preserves their existing pins verbatim instead
	// of resolving them again, so a catalogue rotation cannot drop a pin
	// the project already carries. Only meaningful with SkillsSet.
	RetainedSkills []string
	// SkillsLocalOnly stores selections and pins outside the checkout. Its
	// boolean has a separate presence bit so a rerun preserves current mode.
	SkillsLocalOnly    bool
	SkillsLocalOnlySet bool
	// MCPConnectors is the selected curated remote connector set. MCPsSet
	// distinguishes an unchanged empty answer from explicit deselection.
	MCPConnectors []string
	MCPsSet       bool
	// KnownSkills is the catalogue inventory seen during the run, used only
	// to record 0/1 statistics for every skill (issue #181). It never reaches
	// the manifest.
	KnownSkills []string
	// GitHubCase records which of the four #169 situations applied (1..4).
	// Zero means "unknown" (non-interactive flags, unsupported runtime).
	GitHubCase int
	// Harness records the agent harness chosen in the wizard. Only
	// "opencode" is supported today; Pi is tracked by #176.
	Harness string
	// Advanced records that the user opened the advanced settings.
	Advanced bool
}

// InitWizard validates project setup answers and writes what they imply.
// The seams keep every external dependency out of the flow: the model
// catalogue, the file system, and the review screen.
type InitWizard struct {
	FS FS
	// HostResources enforces the detected host capacity ceiling and adds
	// interactive review warnings for allocations that leave little headroom.
	// Nil leaves the pure planner usable without host-dependent policy.
	HostResources *HostResources
	// StateDir contains host-local per-project settings used only when the
	// user explicitly selects local-only skill storage.
	StateDir string
	// ValidateModel checks a model against the Albert catalogue (P10). Nil
	// skips validation; a rejection is an error (the answer must change), an
	// unreachable catalogue is a warning (the model is still recorded).
	ValidateModel func(model string) (warning string, err error)
	// ValidateSmallModel additionally checks that an explicit small model has
	// a connected provider. Nil uses ValidateModel.
	ValidateSmallModel func(model string) (warning string, err error)
	// Print receives the review screen. Nil means stdout.
	Print func(string)
	// ResolveSkills pins and caches selected catalogue entries. Nil selects
	// the production pinned catalogue; tests inject fixture content.
	ResolveSkills func(context.Context, []string) ([]ProjectSkill, map[string]SkillLock, error)
	// VerifySkills checks that each selected skill has a valid cached artifact.
	// Nil selects the normal verified-cache loader.
	VerifySkills func([]string, map[string]SkillLock) error
	// ProbeRemoteMCP probes a selected remote connector for the review
	// screen. Nil selects the production MCP initialize handshake; a probe
	// failure becomes a review warning, not an error.
	ProbeRemoteMCP func(context.Context, MCPConnector) error
	// ResolveDefaultModel returns the model reference that applies when the
	// user keeps the setup default (empty model answer). Nil uses the embedded
	// default, keeping the pure planner free of host state (#312).
	ResolveDefaultModel func() string
	// Expert marks an invocation that provided an init flag: the interactive
	// review keeps its technical detail (skill revisions, credential
	// reference) for those runs (#312).
	Expert bool
}

// Note on the FS seam: it carries the manifest and lockfile. Path validation
// (does the root exist, is it a directory, is it inside a worktree) uses the
// real filesystem, because those are host paths the user is asserting about
// rather than content this component owns.

func (w InitWizard) fs() FS {
	if w.FS != nil {
		return w.FS
	}
	return DefaultFS
}

func (w InitWizard) stateDir() string {
	if w.StateDir != "" {
		return w.StateDir
	}
	return DefaultStateDir()
}

func (w InitWizard) print(msg string) {
	if w.Print != nil {
		w.Print(msg)
		return
	}
	fmt.Print(msg)
}

// InitPlan is a validated set of answers: what will be written, and what the
// user should know before confirming.
type InitPlan struct {
	Answers InitAnswers
	// GuestCPUs and GuestMemoryMB are the effective values shown in review.
	// Zero-valued answer fields remain implicit in the manifest.
	GuestCPUs     int
	GuestMemoryMB int
	// ManifestPath and LockPath are the files Apply will write, in the
	// checkout. They are the same paths the launch path reads.
	ManifestPath string
	LockPath     string
	// ExistingManifest is the manifest already on disk, if any. Apply refuses
	// to replace it unless it is told to, so a re-run cannot silently discard
	// decisions someone else committed.
	ExistingManifest *ProjectManifest
	// Warnings are non-fatal notes shown in the review.
	Warnings            []string
	SkillEntries        []ProjectSkill
	SkillLocks          map[string]SkillLock
	LocalSkillsPath     string
	InstructionsPath    string
	InstructionsBefore  string
	InstructionsAfter   string
	InstructionsExisted bool
	InstructionsChanged bool
	// MCPChecks and FileChanges describe the interactive review. A local MCP
	// has no host endpoint to probe; it is described separately by the review.
	MCPChecks   []InitMCPCheck
	FileChanges []InitFileChange
	// DefaultModelName is the display name of the model that applies when no
	// model was chosen: the setup default, or the embedded default (#312).
	DefaultModelName string
	// Expert keeps technical detail in the interactive review for invocations
	// that provided an init flag (#312).
	Expert bool
	// These snapshots protect the file summary from changes made while the
	// review is open. AGENTS.md remains a deliberate merge, not a whole-file
	// precondition, so unrelated user text is preserved.
	lockfileSnapshot   reviewedFileSnapshot
	localSkillSnapshot reviewedFileSnapshot
}

type reviewedFileSnapshot struct {
	data    []byte
	existed bool
}

func readReviewedFileSnapshot(fs FS, path string) (reviewedFileSnapshot, error) {
	data, err := fs.ReadFile(path)
	if err == nil {
		return reviewedFileSnapshot{data: data, existed: true}, nil
	}
	if os.IsNotExist(err) {
		return reviewedFileSnapshot{}, nil
	}
	return reviewedFileSnapshot{}, err
}

func (s reviewedFileSnapshot) validateCurrent(fs FS, path string) error {
	current, err := readReviewedFileSnapshot(fs, path)
	if err != nil {
		return fmt.Errorf("cannot re-read %s before Apply: %w", path, err)
	}
	if current.existed != s.existed || !bytes.Equal(current.data, s.data) {
		return fmt.Errorf("%s changed after the review; run init again to review the current files before applying", path)
	}
	return nil
}

// InitMCPCheck is the result of one remote MCP handshake. Checked is false
// only when no probe is available, so a missing probe is never shown as a
// successful connection.
type InitMCPCheck struct {
	ID        string
	Endpoint  string
	Checked   bool
	Reachable bool
}

// InitFileChange is one file Apply will create or update. Local-only skill
// state lives outside the project and is identified as such in the review.
type InitFileChange struct {
	Path        string
	Description string
	Existed     bool
	Local       bool
}

type managedFileMutation struct {
	path  string
	mode  os.FileMode
	write func() error
}

type managedFileSnapshot struct {
	path   string
	data   []byte
	mode   os.FileMode
	exists bool
}

func applyManagedFileMutations(fs FS, mutations []managedFileMutation) error {
	snapshots := make([]managedFileSnapshot, len(mutations))
	for i, mutation := range mutations {
		snapshot := managedFileSnapshot{path: mutation.path, mode: mutation.mode}
		if info, err := os.Lstat(mutation.path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink; refusing to replace managed data", mutation.path)
			}
			if info.Mode().IsRegular() {
				snapshot.mode = info.Mode().Perm()
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := fs.ReadFile(mutation.path)
		if err == nil {
			snapshot.data, snapshot.exists = data, true
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("snapshot %s before Apply: %w", mutation.path, err)
		}
		snapshots[i] = snapshot
	}
	for i, mutation := range mutations {
		if err := mutation.write(); err != nil {
			var rollbackErrors []string
			for j := i - 1; j >= 0; j-- {
				snapshot := snapshots[j]
				if snapshot.exists {
					if rollbackErr := atomicWrite(fs, snapshot.path, snapshot.data, snapshot.mode); rollbackErr != nil {
						rollbackErrors = append(rollbackErrors, fmt.Sprintf("restore %s: %v", snapshot.path, rollbackErr))
					}
				} else if rollbackErr := fs.Remove(snapshot.path); rollbackErr != nil && !os.IsNotExist(rollbackErr) {
					rollbackErrors = append(rollbackErrors, fmt.Sprintf("remove %s: %v", snapshot.path, rollbackErr))
				}
			}
			if len(rollbackErrors) > 0 {
				return fmt.Errorf("Apply failed (%v) and rollback was incomplete: %s", err, strings.Join(rollbackErrors, "; "))
			}
			return err
		}
	}
	return nil
}

// subtractSkillIDs returns the elements of ids that do not appear in
// remove, preserving ids order.
func subtractSkillIDs(ids, remove []string) []string {
	if len(remove) == 0 {
		return ids
	}
	excluded := make(map[string]bool, len(remove))
	for _, id := range remove {
		excluded[id] = true
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if !excluded[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

// Plan validates the answers and resolves what they imply. It reads only the
// existing manifest; nothing is written here, so a user can see the review and
// walk away.
func (w InitWizard) Plan(answers InitAnswers) (InitPlan, error) {
	plan := InitPlan{Answers: answers}
	plan.Expert = w.Expert
	fs := w.fs()

	given := strings.TrimSpace(answers.Root)
	if given == "" {
		return plan, fmt.Errorf("the project root is required")
	}
	// The same discovery the launch path uses, so the two cannot disagree: a
	// directory inside a Git worktree resolves to the worktree root, and
	// writing a manifest anywhere else would be a manifest nothing reads.
	pc, err := DiscoverProject(given)
	if err != nil {
		return plan, fmt.Errorf("project root %s: %w", given, err)
	}
	info, err := os.Stat(pc.Root)
	if err != nil {
		return plan, fmt.Errorf("project root %s: %w", given, err)
	}
	if !info.IsDir() {
		return plan, fmt.Errorf("project root %s is not a directory", pc.Root)
	}
	plan.Answers.Root = pc.Root
	// Warn only when the user named a directory that is genuinely INSIDE the
	// worktree: the same directory spelled differently (macOS /var for
	// /private/var, a Windows 8.3 short name) is not a surprise worth a
	// warning, and warning about it would be noise on every launch.
	// The absolute form comes first: a relative path never prefix-matches the
	// absolute root, so a relative nested root would lose the notice.
	if abs, err := filepath.Abs(strings.TrimSpace(answers.Root)); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			if resolved != pc.Root && strings.HasPrefix(resolved, pc.Root+string(filepath.Separator)) {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("la racine du projet est %s (et non %s) : le fichier de configuration couvre toute cette copie de travail", pc.Root, resolved))
			}
		}
	}

	runtimeName := answers.Runtime
	if runtimeName == "" {
		runtimeName = RuntimeMicrosandbox
	}
	rt, err := parseRuntimeName(string(runtimeName))
	if err != nil {
		return plan, err
	}
	plan.Answers.Runtime = rt
	if answers.GitHubWorkflow {
		if rt != RuntimeMicrosandbox {
			return plan, fmt.Errorf("the protected GitHub guest workflow requires the microsandbox runtime")
		}
		remote, err := ParseGitHubRemote(answers.GitHubRemote.URL)
		if err != nil || remote != answers.GitHubRemote {
			return plan, fmt.Errorf("the GitHub guest workflow requires a GitHub.com origin remote")
		}
		plan.Answers.GitHubRemote = remote
	}

	iso := answers.Isolation
	if iso == "" {
		iso = IsolationFull
	}
	iso, err = ResolveIsolation(string(iso), "")
	if err != nil {
		return plan, err
	}
	plan.Answers.Isolation = iso

	// The harness (#176). An empty answer means the default, and the pair
	// harness/isolation is validated here so the wizard refuses a combination
	// the guest cannot run before it writes anything.
	harness, err := ResolveHarness(string(answers.Harness))
	if err != nil {
		return plan, err
	}
	if err := ValidateHarnessIsolation(harness, iso); err != nil {
		return plan, err
	}
	plan.Answers.Harness = string(harness)
	// The recap names the model a project with no model answer will launch
	// with, and that fallback depends on the harness: OpenCode takes the
	// embedded asset's model, Pi takes the first curated default it can run.
	resolvedDefault := w.resolveDefaultModel(harness)
	plan.DefaultModelName = initReviewModelName(resolvedDefault)

	if model := strings.TrimSpace(answers.Model); model != "" {
		plan.Answers.Model = model
		// A model Pi cannot run is refused before anything is written: the
		// project must not record a selection its own launch would reject
		// (#347).
		if harness == HarnessPi {
			if reason := PiUnsupportedModelReason(model); reason != "" {
				return plan, fmt.Errorf("%s. Ce modèle est écarté pour Pi : choisissez un autre modèle principal, ou OpenCode", reason)
			}
		}
		if w.ValidateModel != nil {
			warning, err := w.ValidateModel(model)
			if err != nil {
				return plan, err
			}
			if warning != "" {
				plan.Warnings = append(plan.Warnings, warning)
			}
		}
	} else if harness == HarnessPi && resolvedDefault != "" {
		// A Pi project must not be left with an empty model the way an
		// OpenCode project can. Empty means the launch resolves the model from
		// the user settings, which is OpenCode's (albert/deepseek-v4-flash by
		// default) — one Pi refuses, so the project the recap just described
		// could not launch (#347 review). Recording the model the recap showed
		// is also what makes the recap a promise rather than a hint.
		//
		// The fallback skips the validation an explicit answer gets: it is a
		// curated model already checked against Pi's refusals, and the
		// catalogue check warns rather than refuses, so running it here would
		// add a warning about a model the user never chose.
		plan.Answers.Model = resolvedDefault
	}
	if model := strings.TrimSpace(answers.SmallModel); model != "" {
		plan.Answers.SmallModel = model
		validate := w.ValidateSmallModel
		if validate == nil {
			validate = w.ValidateModel
		}
		if validate != nil {
			warning, err := validate(model)
			if err != nil {
				return plan, fmt.Errorf("validate small model: %w", err)
			}
			if warning != "" {
				plan.Warnings = append(plan.Warnings, warning)
			}
		}
	}

	if answers.CPUs != 0 {
		if answers.CPUs < 0 || answers.CPUs > MaxSandboxCPUs {
			return plan, fmt.Errorf("CPUs must be 1 to %d, got %d", MaxSandboxCPUs, answers.CPUs)
		}
	}
	if answers.MemoryMB != 0 {
		if answers.MemoryMB < 0 || answers.MemoryMB > MaxSandboxMemoryMB {
			return plan, fmt.Errorf("memory must be 1 to %d MiB, got %d", MaxSandboxMemoryMB, answers.MemoryMB)
		}
	}
	if w.HostResources != nil {
		cpus := resolvedCPUs(answers.CPUs)
		memoryMB := resolvedMemoryMB(answers.MemoryMB)
		if answers.CPUs == 0 {
			cpus = RecommendedDefaultGuestCPUs(w.HostResources.CPUs)
		}
		if answers.MemoryMB == 0 {
			memoryMB = RecommendedDefaultGuestMemoryMB(w.HostResources.MemoryMB)
		}
		if err := ValidateGuestResources(*w.HostResources, cpus, memoryMB); err != nil {
			return plan, err
		}
		plan.Warnings = append(plan.Warnings, GuestResourceWarnings(*w.HostResources, cpus, memoryMB)...)
		plan.GuestCPUs, plan.GuestMemoryMB = cpus, memoryMB
	} else {
		plan.GuestCPUs = resolvedCPUs(answers.CPUs)
		plan.GuestMemoryMB = resolvedMemoryMB(answers.MemoryMB)
	}

	plan.ManifestPath = ProjectManifestPath(pc.Root)
	plan.LockPath = ProjectLockPath(pc.Root)

	if existing, err := ReadProjectManifest(fs, plan.ManifestPath); err == nil {
		plan.ExistingManifest = &existing
	} else if !os.IsNotExist(err) {
		// A present-but-unreadable manifest is a real problem: the setup must
		// not present a review that quietly ignores it.
		return plan, err
	}

	// A rerun without skill flags preserves the prior selection and pin. An
	// explicit selection resolves now so the review can show the exact source
	// revision and content digest before Apply changes project files.
	selected := []string(nil)
	localOnly := false
	oldLocalOnly := false
	var lock Lockfile
	var existingPins map[string]SkillLock
	if plan.ExistingManifest != nil {
		if !answers.MCPsSet {
			plan.Answers.MCPConnectors = append([]string(nil), plan.ExistingManifest.MCPConnectors...)
		}
		selected = append(selected, plan.ExistingManifest.Skills...)
		oldLocalOnly = plan.ExistingManifest.SkillsLocalOnly
		localOnly = oldLocalOnly
		if oldLocalOnly {
			plan.LocalSkillsPath, err = LocalSkillSelectionsPath(w.stateDir(), pc.InstanceName())
			if err != nil {
				return plan, err
			}
			local, readErr := ReadLocalSkillSelections(fs, plan.LocalSkillsPath)
			if readErr != nil {
				return plan, readErr
			}
			selected = append([]string(nil), local.Skills...)
			plan.SkillLocks = local.Pins
			existingPins = local.Pins
		} else if len(plan.ExistingManifest.Skills) > 0 && !answers.SkillsSet {
			lock, err = ReadLockfile(fs, plan.LockPath)
			if err != nil {
				return plan, err
			}
			plan.SkillLocks = lock.Skills
			existingPins = lock.Skills
		} else if answers.SkillsSet && len(answers.RetainedSkills) > 0 {
			// A selection that retains out-of-catalogue pins needs the
			// lockfile even though the selection itself changed: the
			// retained pins are merged below, not resolved.
			lock, err = ReadLockfile(fs, plan.LockPath)
			if err != nil {
				return plan, err
			}
			existingPins = lock.Skills
		}
	}
	if answers.MCPsSet {
		connectors, err := ValidateMCPNewSelection(answers.MCPConnectors)
		if err != nil {
			return plan, err
		}
		plan.Answers.MCPConnectors = connectors
	}
	// Check every selected remote MCP for the interactive recap (#168, #171).
	// A failed handshake stays a warning: it must not prevent the user from
	// saving a configuration for a temporarily unavailable endpoint.
	// Withdrawn connectors are deliberately not contacted.
	for _, connector := range MCPConnectors() {
		if !containsMCPConnector(plan.Answers.MCPConnectors, connector.ID) {
			continue
		}
		check := InitMCPCheck{ID: connector.ID, Endpoint: connector.Endpoint}
		probe := w.ProbeRemoteMCP
		if probe == nil {
			probe = ProbeRemoteMCPFunc
		}
		if probe != nil {
			check.Checked = true
			if err := probe(context.Background(), connector); err != nil {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf(
					"le test d'accès à %s a échoué : %v", connector.Endpoint, err))
			} else {
				check.Reachable = true
			}
		}
		plan.MCPChecks = append(plan.MCPChecks, check)
	}
	if answers.SkillsLocalOnlySet {
		localOnly = answers.SkillsLocalOnly
	}
	if answers.SkillsSet {
		selected = append([]string(nil), answers.Skills...)
		// Retained skills are absent from the current catalogue, so they
		// cannot be resolved again: keep their existing pins verbatim and
		// resolve only the rest. The selection still lists them, so the
		// manifest, review, and managed instructions keep the pin the
		// project already carries.
		resolvable := selected
		if len(answers.RetainedSkills) > 0 {
			resolvable = subtractSkillIDs(selected, answers.RetainedSkills)
		}
		if len(resolvable) > 0 {
			if err := validateProjectSkillRuntime(plan.Answers.Runtime, resolvable); err != nil {
				return plan, err
			}
			resolve := w.ResolveSkills
			if resolve == nil {
				resolve = ResolveProjectSkills
			}
			plan.SkillEntries, plan.SkillLocks, err = resolve(context.Background(), resolvable)
			if err != nil {
				return plan, err
			}
		} else {
			plan.SkillLocks = map[string]SkillLock{}
		}
		for _, id := range answers.RetainedSkills {
			pin, ok := existingPins[id]
			if !ok {
				return plan, fmt.Errorf("retained skill %q has no existing pin to preserve", id)
			}
			if plan.SkillLocks == nil {
				plan.SkillLocks = map[string]SkillLock{}
			}
			plan.SkillLocks[id] = pin
		}
	}
	plan.Answers.Skills = selected
	plan.Answers.SkillsSet = answers.SkillsSet
	plan.Answers.SkillsLocalOnly = localOnly
	plan.Answers.SkillsLocalOnlySet = answers.SkillsLocalOnlySet
	if err := validateProjectSkillRuntime(plan.Answers.Runtime, selected); err != nil {
		return plan, err
	}
	if len(selected) > 0 && len(plan.SkillLocks) == 0 {
		return plan, fmt.Errorf("the selected skills have no pinned lock data")
	}
	if len(selected) > 0 {
		verifySkills := w.VerifySkills
		if verifySkills == nil {
			verifySkills = func(ids []string, locks map[string]SkillLock) error {
				_, err := LoadLockedSkillPackages(ids, locks)
				return err
			}
		}
		if err := verifySkills(selected, plan.SkillLocks); err != nil {
			return plan, err
		}
	}
	if localOnly {
		if plan.LocalSkillsPath == "" {
			plan.LocalSkillsPath, err = LocalSkillSelectionsPath(w.stateDir(), pc.InstanceName())
			if err != nil {
				return plan, err
			}
		}
	}
	// The managed zone is claimed only when this plan can change it: a
	// selection with pins or the Microsandbox standing guidance rewrites
	// the zone, and so does a stale zone left by an earlier versioned
	// setup (a local-only strip removes it, a merge refreshes it). A
	// local-only plan never merges, so it claims only for an actual stale
	// zone. A pin-less Tart or agent-vm project without a zone renders
	// nothing to change, so its AGENTS.md is neither validated nor
	// claimed — a symlinked AGENTS.md must not block an init that touches
	// nothing.
	instructionsPath := filepath.Join(pc.Root, "AGENTS.md")
	needsZone := !localOnly && (len(selected) > 0 || plan.Answers.Runtime == RuntimeMicrosandbox)
	if !needsZone {
		// No AGENTS.md at all reads as no zone; an unreadable or
		// non-regular entry fails closed, same as a zone, and the
		// validation below refuses it.
		needsZone = managedZoneAt(instructionsPath)
	}
	if needsZone {
		plan.InstructionsPath = instructionsPath
		if info, statErr := os.Lstat(plan.InstructionsPath); statErr == nil {
			if !info.Mode().IsRegular() {
				return plan, fmt.Errorf("%s is not a regular file; refusing to change managed instructions", plan.InstructionsPath)
			}
			plan.InstructionsExisted = true
			data, readErr := fs.ReadFile(plan.InstructionsPath)
			if readErr != nil {
				return plan, readErr
			}
			plan.InstructionsBefore = string(data)
		} else if !os.IsNotExist(statErr) {
			return plan, statErr
		}
		if localOnly {
			plan.InstructionsAfter, err = RemoveManagedInstructions(plan.InstructionsBefore)
		} else {
			plan.InstructionsAfter, err = MergeManagedInstructions(plan.InstructionsBefore, selected, plan.SkillLocks, plan.Answers.Runtime)
		}
		if err != nil {
			return plan, err
		}
		plan.InstructionsChanged = plan.InstructionsBefore != plan.InstructionsAfter
	}
	plan.lockfileSnapshot, err = readReviewedFileSnapshot(fs, plan.LockPath)
	if err != nil {
		return plan, err
	}
	plan.FileChanges = []InitFileChange{
		{Path: plan.ManifestPath, Description: "configuration du projet", Existed: plan.ExistingManifest != nil},
		{Path: plan.LockPath, Description: "références des dépendances du projet", Existed: plan.lockfileSnapshot.existed},
	}
	if localOnly {
		plan.localSkillSnapshot, err = readReviewedFileSnapshot(fs, plan.LocalSkillsPath)
		if err != nil {
			return plan, err
		}
		plan.FileChanges = append(plan.FileChanges, InitFileChange{
			Path: plan.LocalSkillsPath, Description: "skills conservés sur ce poste", Existed: plan.localSkillSnapshot.existed, Local: true,
		})
	}
	if plan.InstructionsChanged {
		description := "section just-code ajoutée"
		before, _ := managedSkillZone(plan.InstructionsBefore)
		if before != "" {
			description = "section just-code modifiée"
		}
		plan.FileChanges = append(plan.FileChanges, InitFileChange{
			Path: plan.InstructionsPath, Description: description, Existed: plan.InstructionsExisted,
		})
	}
	if !pc.IsGit {
		plan.Warnings = append(plan.Warnings, "ce dossier n'est pas un dépôt Git : le fichier de configuration dans .just-code/ ne sera pas versionné avec le projet")
	}
	return plan, nil
}

// resolveDefaultModel is the model a project with no model answer launches
// with: the caller-provided setup default, or the embedded default when the
// seam is unset or empty. The setup default is harness-independent and may name
// a model Pi refuses, so a Pi project falls back to the first curated default
// Pi can run instead of inheriting a model that would only fail at launch
// (#347).
func (w InitWizard) resolveDefaultModel(harness Harness) string {
	model := ""
	if w.ResolveDefaultModel != nil {
		model = strings.TrimSpace(w.ResolveDefaultModel())
	}
	if model != "" && PiUnsupportedModelReason(model) == "" {
		return model
	}
	if harness == HarnessPi {
		if piDefault := piDefaultModelRef(); piDefault != "" {
			return piDefault
		}
	}
	if model != "" {
		return model
	}
	return defaultOpencodeModel
}

// FormatInteractiveInitReview is the concise, shared interactive recap (#171).
// The non-interactive FormatInitReview intentionally remains the detailed
// --yes output, including its pinned skill revisions and content digests.
func FormatInteractiveInitReview(plan InitPlan) string {
	var b strings.Builder
	a := plan.Answers
	row := func(label, value string) { fmt.Fprintf(&b, "  %-12s %s\n", label, value) }
	continuation := func(value string) { fmt.Fprintf(&b, "                %s\n", value) }
	fmt.Fprintf(&b, "Récapitulatif du projet : %s\n\n", filepath.Base(a.Root))

	if len(a.Skills) == 0 {
		row("Skills", "aucune sélectionnée")
	} else {
		names := make([]string, 0, len(a.Skills))
		for _, id := range a.Skills {
			names = append(names, strings.TrimPrefix(id, "official/"))
		}
		storage := "(avec le projet)"
		if a.SkillsLocalOnly {
			storage = "(sur ce poste)"
		}
		row("Skills", strings.Join(names, ", ")+"                "+storage)
		if plan.Expert {
			for _, id := range a.Skills {
				if lock := plan.SkillLocks[id]; lock.Revision != "" {
					continuation(fmt.Sprintf("%s : révision %s", id, lock.Revision))
				}
			}
		}
	}

	if len(a.MCPConnectors) == 0 {
		row("MCP", "aucun sélectionné")
	}
	checked := make(map[string]InitMCPCheck, len(plan.MCPChecks))
	for _, check := range plan.MCPChecks {
		checked[check.ID] = check
	}
	firstMCP := true
	writeMCP := func(id, detail string) {
		if firstMCP {
			row("MCP", detail)
			firstMCP = false
		} else {
			continuation(detail)
		}
	}
	for _, connector := range MCPConnectors() {
		if !containsMCPConnector(a.MCPConnectors, connector.ID) {
			continue
		}
		check := checked[connector.ID]
		status := "⚠️ accès non vérifié"
		if check.Checked {
			status = "⚠️ injoignable"
			if check.Reachable {
				status = "✅ joignable"
			}
		}
		writeMCP(connector.ID, fmt.Sprintf("%s → %s   %s", connector.ID, connector.Endpoint, status))
	}
	for _, choice := range BrowserMCPs() {
		if containsMCPConnector(a.MCPConnectors, choice.ID) {
			writeMCP(choice.ID, choice.ID+" — "+choice.Description+" ; installation au premier lancement")
		}
	}
	for _, id := range a.MCPConnectors {
		if reason, removed := RemovedMCPIDs[id]; removed {
			writeMCP(id, id+" : non repris — "+reason)
		}
	}

	if a.GitHubWorkflow {
		row("GitHub", "pull requests en brouillon sur "+a.GitHubRemote.Repo)
	} else {
		row("GitHub", "non (récupération par just-code workspace apply)")
		continuation("une autorisation déjà enregistrée sur ce poste reste inchangée")
	}

	isolation := "complète"
	if a.Isolation == IsolationBackend {
		isolation = "partielle"
	}
	cpuLabel := "processeurs"
	if plan.GuestCPUs == 1 {
		cpuLabel = "processeur"
	}
	runtimeName := string(a.Runtime)
	if a.Runtime == RuntimeMicrosandbox {
		runtimeName = "Microsandbox"
	}
	row("Machine", fmt.Sprintf("%s, isolation %s, %d %s, %s", runtimeName, isolation, plan.GuestCPUs, cpuLabel, formatInitReviewMemory(plan.GuestMemoryMB)))
	model := initReviewModelName(a.Model)
	if a.Model == "" && plan.DefaultModelName != "" {
		model = plan.DefaultModelName
	}
	harness := DefaultHarness
	if resolved, err := ResolveHarness(a.Harness); err == nil {
		harness = resolved
	}
	row("Agent", harness.DisplayName()+", modèle "+model)
	if a.SmallModel != "" {
		row("Petit modèle", initReviewModelName(a.SmallModel))
	}
	credential := "clé Albert API globale (jamais sa valeur)"
	if plan.Expert && a.CredentialRef != "" {
		credential = fmt.Sprintf("référence %q (jamais sa valeur)", a.CredentialRef)
	} else if plan.Expert {
		credential = "clé Albert API globale (référence, jamais sa valeur)"
	}
	row("Identifiant", credential)
	if a.BrowserResourceGuidance != "" {
		row("Conseil", a.BrowserResourceGuidance)
	}

	if a.Runtime == RuntimeMicrosandbox {
		b.WriteString("\nCopie filtrée du projet : fichiers .env, fichiers ignorés et liens symboliques exclus par défaut.\n")
	} else {
		b.WriteString("\nLe dossier est partagé avec la machine isolée : l'agent peut lire tous ses fichiers, y compris les secrets. Un fichier .env bloque le démarrage.\n")
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(&b, "Attention : %s\n", warning)
	}
	if plan.ExistingManifest != nil {
		b.WriteString("Attention : la configuration existante sera remplacée :\n")
		b.WriteString(formatManifestSummary(*plan.ExistingManifest))
	}

	b.WriteString("\nFichiers modifiés dans votre projet :\n")
	localChanges := make([]InitFileChange, 0, 1)
	for _, change := range plan.FileChanges {
		if change.Local {
			localChanges = append(localChanges, change)
			continue
		}
		writeInitFileChange(&b, a.Root, change)
	}
	if len(localChanges) > 0 {
		b.WriteString("Fichiers modifiés sur ce poste, hors du projet :\n")
		for _, change := range localChanges {
			writeInitFileChange(&b, a.Root, change)
		}
	}

	b.WriteString("\nL'initialisation ne démarre ni ne redémarre de machine isolée.\n")
	b.WriteString("Au premier lancement : téléchargement de la machine isolée, puis installation et démarrage. La durée dépend de votre connexion.\n")
	if len(a.Skills) > 0 {
		b.WriteString("Les skills sont installés dans la machine isolée au premier lancement.\n")
	}
	if HasBrowserMCPSelection(a.MCPConnectors) {
		b.WriteString(BrowserDownloadNote + "\n")
	}
	if len(a.MCPConnectors) > 0 {
		b.WriteString("Les changements de MCP prennent effet au prochain lancement ; cela peut redémarrer la machine isolée sans effacer son contenu.\n")
	}
	return b.String()
}

func initReviewModelName(model string) string {
	if model == "" {
		return "par défaut de setup"
	}
	name := strings.TrimPrefix(model, "albert/")
	for _, choice := range CuratedAlbertModels() {
		if model == choice.Ref() || model == choice.ID {
			return choice.Name
		}
	}
	return name
}

func formatInitReviewMemory(memoryMB int) string {
	if memoryMB > 0 && memoryMB%1024 == 0 {
		return strconv.Itoa(memoryMB/1024) + " Go"
	}
	return FormatMemorySize(memoryMB)
}

func writeInitFileChange(b *strings.Builder, root string, change InitFileChange) {
	marker := "+"
	if change.Existed {
		marker = "~"
	}
	fmt.Fprintf(b, "  %s %-32s %s\n", marker, initReviewPath(root, change.Path), change.Description)
}

// initReviewPath displays the user's home as ~ and project files relative to
// the worktree, avoiding machine-specific paths in the confirmation screen.
func initReviewPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(filepath.Join("~", rel))
		}
	}
	return path
}

// FormatInitInstructionsDiff returns only the managed AGENTS.md section.
// Everything outside that section is preserved and never copied into the diff.
func FormatInitInstructionsDiff(plan InitPlan) string {
	if !plan.InstructionsChanged {
		return ""
	}
	var b strings.Builder
	before, _ := managedSkillZone(plan.InstructionsBefore)
	after, _ := managedSkillZone(plan.InstructionsAfter)
	b.WriteString("Diff de la section just-code dans AGENTS.md (tout le texte hors de cette section est conservé à l'identique) :\n")
	b.WriteString("Si le filtre de copie exclut AGENTS.md, une autorisation fichier par fichier sera nécessaire pour le transmettre à la machine isolée.\n")
	if before == "" {
		b.WriteString("  actuel : aucune section de skills gérée par just-code\n")
	} else {
		fmt.Fprintf(&b, "--- actuel ---\n%s", ensureReviewNewline(before))
	}
	if after == "" {
		b.WriteString("+++ proposé : suppression de la section gérée\n")
	} else {
		fmt.Fprintf(&b, "+++ proposé +++\n%s", ensureReviewNewline(after))
	}
	return b.String()
}

// FormatInitReview renders the confirmation screen: every decision the user
// is about to commit, plus what the guest will actually receive.
func FormatInitReview(plan InitPlan) string {
	var b strings.Builder
	a := plan.Answers
	b.WriteString("Review the project setup:\n")
	fmt.Fprintf(&b, "  project      %s\n", a.Root)
	fmt.Fprintf(&b, "  sharing      %s runtime, isolation %s\n", a.Runtime, a.Isolation)
	if a.Isolation == IsolationFull {
		b.WriteString("               the agent, its TUI and its credentials run inside the guest\n")
	} else {
		b.WriteString("               the agent runs inside the guest and the TUI attaches from this machine\n")
	}
	if a.Model != "" {
		fmt.Fprintf(&b, "  model        %s\n", a.Model)
	} else {
		b.WriteString("  model        the built-in default\n")
	}
	if a.SmallModel != "" {
		fmt.Fprintf(&b, "  small model  %s\n", a.SmallModel)
	} else {
		b.WriteString("  small model  same as the main model\n")
	}
	fmt.Fprintf(&b, "  resources    %d CPUs, %s (fixed when the guest is created)\n", plan.GuestCPUs, FormatMemorySize(plan.GuestMemoryMB))
	if a.BrowserResourceGuidance != "" {
		fmt.Fprintf(&b, "  host fit     %s\n", a.BrowserResourceGuidance)
	}
	if a.CredentialRef != "" {
		fmt.Fprintf(&b, "  credential   reference %q\n", a.CredentialRef)
	} else {
		b.WriteString("  credential   the global Albert credential\n")
	}
	// No claim is made here about where the credential VALUE lives: the
	// manifest records a reference, and what each runtime then does with the
	// secret differs (Microsandbox proxies it, Tart and agent-vm hand it to
	// the guest). Promising "it stays on the host" would be false for two of
	// the three.
	b.WriteString("               (the manifest records the reference, never a value)\n")
	if a.GitHubWorkflow {
		fmt.Fprintf(&b, "  GitHub       agent may push changes to a branch of %s and open a draft pull request\n", a.GitHubRemote.Repo)
		b.WriteString("               Approval is local to this machine and is not written to the project\n")
	} else {
		b.WriteString("  GitHub       no approval for this project; any approval already recorded on this machine is unchanged\n")
	}
	if len(a.MCPConnectors) == 0 {
		b.WriteString("  MCPs         none selected\n")
	} else {
		fmt.Fprintf(&b, "  MCPs         %s\n", strings.Join(a.MCPConnectors, ", "))
		for _, id := range a.MCPConnectors {
			reason, removed := RemovedMCPIDs[id]
			if !removed {
				continue
			}
			fmt.Fprintf(&b, "               %s : %s\n", id, reason)
		}
		for _, choice := range MCPSelections() {
			if !containsMCPConnector(a.MCPConnectors, choice.ID) {
				continue
			}
			if choice.Local {
				fmt.Fprintf(&b, "               %s -> %s\n", choice.ID, choice.Description)
				continue
			}
			for _, connector := range MCPConnectors() {
				if connector.ID == choice.ID {
					fmt.Fprintf(&b, "               %s -> %s (%s; auth: %s)\n", connector.Name, connector.Endpoint, connector.Transport, connector.Credential)
					break
				}
			}
		}
	}
	if len(a.Skills) == 0 {
		b.WriteString("  skills       no managed project skills selected\n")
	} else {
		storage := "versioned in .just-code/lock.json"
		if a.SkillsLocalOnly {
			storage = "host-local; not shared with repository clones"
		}
		fmt.Fprintf(&b, "  skills       %s\n", storage)
		for _, id := range a.Skills {
			lock := plan.SkillLocks[id]
			fmt.Fprintf(&b, "               %s at %s (sha256:%s)\n", id, lock.Revision, lock.SHA256[:12])
		}
		b.WriteString("               selected skills are instruction artifacts, not a security boundary\n")
		if a.SkillsLocalOnly {
			b.WriteString("               local-only selection does not add managed rules to AGENTS.md\n")
		}
	}
	b.WriteString("  state        versioned in .just-code/ (shared with whoever clones the repository)\n")
	fmt.Fprintf(&b, "  manifest     %s\n", plan.ManifestPath)
	fmt.Fprintf(&b, "  lockfile     %s\n", plan.LockPath)
	if plan.LocalSkillsPath != "" {
		action := "previous host-local selection will be removed after migration to versioned storage"
		if a.SkillsLocalOnly {
			action = "selected IDs and pins stored here (mode 0600; not committed)"
			if len(a.Skills) == 0 {
				action = "empty selection; existing local pins will be cleared (mode 0600; not committed)"
			}
		}
		fmt.Fprintf(&b, "  local skills %s (%s)\n", plan.LocalSkillsPath, action)
	}
	b.WriteString("\nDownloads and activation:\n")
	if len(plan.SkillEntries) > 0 {
		b.WriteString("  Selected skill archives are resolved and verified in the host cache for these pins; the guest installs them on its next launch.\n")
	} else if len(a.Skills) > 0 {
		b.WriteString("  Existing skill pins are preserved; the guest reconciles them on its next launch.\n")
	} else {
		b.WriteString("  No project skill archives are selected.\n")
	}
	if HasBrowserMCPSelection(a.MCPConnectors) {
		b.WriteString("  The digest-pinned Debian browser profile and selected MCP packages download inside the guest on first launch; no host browser is used.\n")
		b.WriteString("  DevTools listens only on guest loopback. Changing to or from this image on an existing guest requires explicit recreation; init never recreates a guest.\n")
	}
	b.WriteString("  Init does not boot or restart a VM.\n")
	b.WriteString("  Managed MCP changes apply on the next launch, which may restart a running guest non-destructively.\n")
	if a.Runtime == RuntimeMicrosandbox {
		b.WriteString("\nThe guest receives a filtered copy of the project: .env files, ignored files,\n" +
			"symlinks and files flagged by secret detection are excluded by default ('just-code workspace status').\n")
	} else {
		b.WriteString("\nThis runtime MOUNTS the project into the guest: everything in it is readable there,\n" +
			"including any secret it holds (a .env file blocks the start).\n")
	}
	for _, warning := range plan.Warnings {
		fmt.Fprintf(&b, "\nWarning: %s\n", warning)
	}
	if plan.ExistingManifest != nil {
		b.WriteString("\nA project manifest already exists and would be REPLACED:\n")
		b.WriteString(formatManifestSummary(*plan.ExistingManifest))
	}
	if plan.InstructionsChanged {
		before, _ := managedSkillZone(plan.InstructionsBefore)
		after, _ := managedSkillZone(plan.InstructionsAfter)
		b.WriteString("\nAGENTS.md managed-zone diff (all text outside this block is preserved byte-for-byte):\n")
		b.WriteString("If the workspace filter marks AGENTS.md ignored or secret-like, it will not reach the guest without a per-file allow decision.\n")
		if before == "" {
			b.WriteString("  current: no managed skills block\n")
		} else {
			fmt.Fprintf(&b, "--- current ---\n%s", ensureReviewNewline(before))
		}
		if after == "" {
			b.WriteString("+++ proposed: remove the managed block\n")
		} else {
			fmt.Fprintf(&b, "+++ proposed +++\n%s", ensureReviewNewline(after))
		}
	}
	return b.String()
}

func ensureReviewNewline(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

func resolvedCPUs(cpus int) int {
	if cpus == 0 {
		return DefaultSandboxCPUs
	}
	return cpus
}

func resolvedMemoryMB(memory int) int {
	if memory == 0 {
		return DefaultSandboxMemoryMB
	}
	return memory
}

// formatManifestSummary lists the settings an existing manifest records, so
// the review shows what would be lost rather than only that something exists.
func formatManifestSummary(pm ProjectManifest) string {
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, fmt.Sprintf("    %-12s %s", label, value))
		}
	}
	add("project", pm.Project)
	add("runtime", pm.Runtime)
	add("isolation", pm.Isolation)
	add("model", pm.Model)
	add("smallModel", pm.SmallModel)
	if pm.CPUs > 0 {
		add("cpus", fmt.Sprintf("%d", pm.CPUs))
	}
	if pm.MemoryMB > 0 {
		add("memory", fmt.Sprintf("%d MiB", pm.MemoryMB))
	}
	add("credential", pm.CredentialRef)
	add("storage", pm.Storage)
	if len(pm.Skills) > 0 {
		add("skills", strings.Join(pm.Skills, ", "))
	}
	if pm.SkillsLocalOnly {
		add("skillStorage", "local-only")
	}
	if len(lines) == 0 {
		return "    (the existing manifest sets nothing)\n"
	}
	return strings.Join(lines, "\n") + "\n"
}

// Apply writes the reviewed plan. It refuses to replace an existing manifest
// unless replace is set: overwriting decisions that were committed on purpose
// is not something a re-run should do quietly.
func (w InitWizard) Apply(plan InitPlan, replace bool) error {
	fs := w.fs()
	return withProjectUpdateLock(fs, plan.Answers.Root, func() error {
		if err := ensureProjectStateDirectory(plan.Answers.Root); err != nil {
			return err
		}
		if err := recoverProjectUpdateLocked(fs, plan.Answers.Root); err != nil {
			return fmt.Errorf("recover prior project update: %w", err)
		}
		return w.applyLocked(plan, replace)
	})
}

func (w InitWizard) applyLocked(plan InitPlan, replace bool) error {
	fs := w.fs()
	if err := ensureProjectStateDirectory(plan.Answers.Root); err != nil {
		return err
	}
	// Re-read rather than trusting the plan's snapshot: a manifest created
	// between Plan and Apply must not be overwritten without consent, and the
	// guarantee would be only as strong as the snapshot.
	existingManifest, readErr := ReadProjectManifest(fs, plan.ManifestPath)
	if readErr == nil && !replace {
		return fmt.Errorf("%s already exists; re-run with the replace option, or edit it directly", plan.ManifestPath)
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		// An unreadable manifest must not be silently replaced either.
		return readErr
	}
	// A lock or local selection created or edited after the review is not
	// covered by its file-change summary. Refuse it before any write, including
	// when --replace was supplied; replacement must not hide concurrent work.
	if err := plan.lockfileSnapshot.validateCurrent(fs, plan.LockPath); err != nil {
		return err
	}
	if plan.Answers.SkillsLocalOnly {
		if err := plan.localSkillSnapshot.validateCurrent(fs, plan.LocalSkillsPath); err != nil {
			return err
		}
	}
	// Validate the lock before writing any project file: a malformed or
	// unreadable lock must not leave a new manifest beside discarded pins.
	kept, err := ReadLockfile(fs, plan.LockPath)
	if err != nil {
		return err
	}
	if readErr == nil {
		if err := ValidateDependencySet(existingManifest, kept); err != nil {
			return fmt.Errorf("recover or repair the project before reinitializing: %w", err)
		}
	}
	dependencySetID := ""
	if readErr == nil {
		dependencySetID = existingManifest.DependencySetID
	} else if isUpdateID(kept.DependencySetID) {
		dependencySetID = kept.DependencySetID
	}
	if dependencySetID == "" {
		dependencySetID, err = newDependencySetID()
		if err != nil {
			return fmt.Errorf("create dependency-set identity: %w", err)
		}
	}
	instructionsChanged := false
	instructionsAfter := plan.InstructionsAfter
	instructionsPath := plan.InstructionsPath
	if instructionsPath == "" {
		// A managed zone can appear between Plan and Apply — the plan
		// left the path unclaimed because none existed. Re-check at apply
		// time so the committed manifest never contradicts the
		// instructions on disk: a zone that appeared is stripped or
		// refreshed on current content, and a non-regular AGENTS.md
		// holding one is refused.
		candidate := filepath.Join(plan.Answers.Root, "AGENTS.md")
		if managedZoneAt(candidate) {
			instructionsPath = candidate
		}
	}
	if instructionsPath != "" {
		if info, err := os.Lstat(instructionsPath); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file; refusing to change managed instructions", instructionsPath)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		current, err := fs.ReadFile(instructionsPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if plan.Answers.SkillsLocalOnly {
			instructionsAfter, err = RemoveManagedInstructions(string(current))
		} else {
			instructionsAfter, err = MergeManagedInstructions(string(current), plan.Answers.Skills, plan.SkillLocks, plan.Answers.Runtime)
		}
		if err != nil {
			return err
		}
		instructionsChanged = string(current) != instructionsAfter
	}
	var localSelection LocalSkillSelections
	if plan.Answers.SkillsLocalOnly {
		if plan.LocalSkillsPath == "" {
			return fmt.Errorf("local-only skill state path was not resolved")
		}
		localSelection = LocalSkillSelections{Skills: append([]string(nil), plan.Answers.Skills...), Pins: cloneSkillLocks(plan.SkillLocks)}
	}
	// The manifest is secret-free by construction: a credential is referenced
	// by name, never written here. It also carries no host-absolute path, so a
	// teammate who clones the repository gets the same project identity.
	pm := ProjectManifest{
		Runtime:         string(plan.Answers.Runtime),
		Isolation:       string(plan.Answers.Isolation),
		Model:           plan.Answers.Model,
		Harness:         string(plan.Answers.Harness),
		SmallModel:      plan.Answers.SmallModel,
		CPUs:            plan.Answers.CPUs,
		MemoryMB:        plan.Answers.MemoryMB,
		CredentialRef:   plan.Answers.CredentialRef,
		SkillsLocalOnly: plan.Answers.SkillsLocalOnly,
		MCPConnectors:   append([]string(nil), plan.Answers.MCPConnectors...),
		DependencySetID: dependencySetID,
	}
	if !plan.Answers.SkillsLocalOnly {
		pm.Skills = append([]string(nil), plan.Answers.Skills...)
	}
	// The lockfile is written even though nothing is pinned yet: it is where
	// the skills and images of later chantiers record their resolved
	// revisions, and an empty lock is the honest "nothing pinned" state. An
	// existing lock is preserved: replacing the manifest is not a reason to
	// discard pins someone else committed.
	// A lock that cannot be read is NOT replaced with an empty one: that would
	// discard whatever pins it held, silently, which is the same refusal the
	// manifest gets above. A missing lock reads as the zero lock, no error.
	lock := Lockfile{Entries: map[string]string{}, DependencySetID: dependencySetID}
	if len(kept.Entries) > 0 {
		lock.Entries = kept.Entries
	}
	if !plan.Answers.SkillsLocalOnly && len(plan.Answers.Skills) > 0 {
		lock.Skills = cloneSkillLocks(plan.SkillLocks)
	}
	mutations := make([]managedFileMutation, 0, 4)
	if plan.Answers.SkillsLocalOnly {
		mutations = append(mutations, managedFileMutation{
			path: plan.LocalSkillsPath, mode: 0o600,
			write: func() error { return WriteLocalSkillSelections(fs, plan.LocalSkillsPath, localSelection) },
		})
	}
	mutations = append(mutations,
		managedFileMutation{path: plan.ManifestPath, mode: 0o644, write: func() error { return WriteProjectManifest(fs, plan.ManifestPath, pm) }},
		managedFileMutation{path: plan.LockPath, mode: 0o644, write: func() error { return WriteLockfile(fs, plan.LockPath, lock) }},
	)
	if instructionsChanged {
		mutations = append(mutations, managedFileMutation{
			path: instructionsPath, mode: 0o644,
			write: func() error { return atomicWrite(fs, instructionsPath, []byte(instructionsAfter), 0o644) },
		})
	}
	if err := applyManagedFileMutations(fs, mutations); err != nil {
		return err
	}
	w.print(fmt.Sprintf("Écrit %s\nÉcrit %s\n", plan.ManifestPath, plan.LockPath))
	if instructionsChanged {
		w.print(fmt.Sprintf("Mis à jour %s\n", instructionsPath))
	}
	if plan.Answers.SkillsLocalOnly && len(plan.Answers.Skills) > 0 {
		w.print("Skills conservés sur ce poste, hors du projet.\n")
	}
	return nil
}
