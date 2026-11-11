package codebox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const projectUpdateJournalName = "update-journal.json"

// SkillUpdate describes one selected skill whose source archive changes.
type SkillUpdate struct {
	ID           string
	FromRevision string
	FromSHA256   string
	ToRevision   string
	ToSHA256     string
}

// ProjectUpdatePlan is a read-only proposal. Apply rechecks the input bytes
// so concurrent edits cannot be overwritten by a plan reviewed earlier.
type ProjectUpdatePlan struct {
	Changes []SkillUpdate
	NoOp    bool

	root                string
	oldManifest         []byte
	oldLock             []byte
	newManifest         []byte
	newLock             []byte
	instructionsPath    string
	oldInstructions     []byte
	newInstructions     []byte
	instructionsExisted bool
}

type ProjectSkillResolver func(context.Context, []string, string) ([]ProjectSkill, map[string]SkillLock, error)

type projectUpdateJournal struct {
	SchemaVersion       int    `json:"schemaVersion"`
	OldManifest         []byte `json:"oldManifest"`
	OldLock             []byte `json:"oldLock"`
	NewManifest         []byte `json:"newManifest"`
	NewLock             []byte `json:"newLock"`
	NewInstructionsHash string `json:"newInstructionsHash,omitempty"`
	InstructionsExisted bool   `json:"instructionsExisted,omitempty"`
}

var projectUpdateStateDirFn = DefaultStateDir

// LatestProjectSkillsRevision resolves the public skills repository's current
// default HEAD to an immutable commit ID. It never checks out or executes the
// repository.
func LatestProjectSkillsRevision(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, skillSourceTimeout)
	defer cancel()
	tmp, err := os.MkdirTemp("", "codebox-skills-head-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	out, err := runGit(ctx, tmp, "ls-remote", projectSkillsRepository, "HEAD")
	if err != nil {
		return "", fmt.Errorf("refresh the project skills catalogue: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[1] != "HEAD" || !isGitRevision(fields[0]) {
		return "", errors.New("the project skills catalogue returned an invalid HEAD revision")
	}
	return fields[0], nil
}

// PlanProjectUpdate resolves only the requested project skills at revision.
// An empty selection means all versioned project skills. No project files are
// written during planning.
func PlanProjectUpdate(ctx context.Context, fs FS, root string, selected []string, revision string, resolve ProjectSkillResolver) (ProjectUpdatePlan, error) {
	if resolve == nil {
		resolve = ResolveProjectSkillsAtRevision
	}
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := ensureRegularManagedFile(manifestPath); err != nil {
		return ProjectUpdatePlan{}, err
	}
	if err := ensureRegularManagedFile(lockPath); err != nil {
		return ProjectUpdatePlan{}, err
	}
	manifestBytes, err := fs.ReadFile(manifestPath)
	if err != nil {
		return ProjectUpdatePlan{}, fmt.Errorf("read project manifest: %w", err)
	}
	manifest, err := ReadProjectManifest(fs, manifestPath)
	if err != nil {
		return ProjectUpdatePlan{}, err
	}
	if manifest.SkillsLocalOnly {
		return ProjectUpdatePlan{}, errors.New("project skill updates currently require versioned skills; local-only selections were not changed")
	}
	lockBytes, err := fs.ReadFile(lockPath)
	if err != nil {
		return ProjectUpdatePlan{}, fmt.Errorf("read project lockfile: %w", err)
	}
	lock, err := ReadLockfile(fs, lockPath)
	if err != nil {
		return ProjectUpdatePlan{}, err
	}
	if err := ValidateDependencySet(manifest, lock); err != nil {
		return ProjectUpdatePlan{}, err
	}
	ids := append([]string(nil), selected...)
	if len(ids) == 0 {
		ids = append(ids, manifest.Skills...)
	}
	if len(ids) == 0 {
		return ProjectUpdatePlan{NoOp: true, root: root}, nil
	}
	if err := validateUpdateSelection(manifest.Skills, ids); err != nil {
		return ProjectUpdatePlan{}, err
	}
	if !isGitRevision(revision) {
		return ProjectUpdatePlan{}, fmt.Errorf("invalid target skill revision %q", revision)
	}
	_, proposedSkills, err := resolve(ctx, ids, revision)
	if err != nil {
		return ProjectUpdatePlan{}, fmt.Errorf("resolve selected skills at %s: %w", revision, err)
	}
	if err := validateResolvedSkillLocks(ids, proposedSkills, revision); err != nil {
		return ProjectUpdatePlan{}, err
	}
	changes := make([]SkillUpdate, 0, len(ids))
	for _, id := range ids {
		before, hadBefore := lock.Skills[id]
		after := proposedSkills[id]
		if hadBefore && before == after {
			continue
		}
		changes = append(changes, SkillUpdate{ID: id, FromRevision: before.Revision, FromSHA256: before.SHA256, ToRevision: after.Revision, ToSHA256: after.SHA256})
		if lock.Skills == nil {
			lock.Skills = make(map[string]SkillLock)
		}
		lock.Skills[id] = after
	}
	if len(changes) == 0 {
		return ProjectUpdatePlan{NoOp: true, root: root}, nil
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	setID, err := newDependencySetID()
	if err != nil {
		return ProjectUpdatePlan{}, fmt.Errorf("create dependency-set identity: %w", err)
	}
	manifest.DependencySetID = setID
	manifest.SchemaVersion = projectManifestSchemaVersion
	lock.DependencySetID = setID
	lock.SchemaVersion = lockfileSchemaVersion
	newManifest, err := marshalProjectManifestUpdate(manifestBytes, manifest)
	if err != nil {
		return ProjectUpdatePlan{}, err
	}
	newLock, err := marshalLockfileUpdate(lockBytes, lock)
	if err != nil {
		return ProjectUpdatePlan{}, err
	}
	plan := ProjectUpdatePlan{
		Changes: changes, root: root,
		oldManifest: manifestBytes, oldLock: lockBytes,
		newManifest: newManifest, newLock: newLock,
	}
	if len(manifest.Skills) > 0 {
		plan.instructionsPath = filepath.Join(root, "AGENTS.md")
		if info, statErr := os.Lstat(plan.instructionsPath); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return ProjectUpdatePlan{}, fmt.Errorf("%s is not a regular file; refusing to update managed instructions", plan.instructionsPath)
			}
		} else if !os.IsNotExist(statErr) {
			return ProjectUpdatePlan{}, statErr
		}
		plan.oldInstructions, err = fs.ReadFile(plan.instructionsPath)
		if err == nil {
			plan.instructionsExisted = true
		} else if !os.IsNotExist(err) {
			return ProjectUpdatePlan{}, fmt.Errorf("read managed instructions: %w", err)
		}
		updated, mergeErr := MergeManagedInstructions(string(plan.oldInstructions), manifest.Skills, lock.Skills, managedInstructionRuntime(manifest.Runtime))
		if mergeErr != nil {
			return ProjectUpdatePlan{}, mergeErr
		}
		plan.newInstructions = []byte(updated)
	}
	return plan, nil
}

func validateUpdateSelection(available, selected []string) error {
	allowed := make(map[string]bool, len(available))
	for _, id := range available {
		allowed[id] = true
	}
	seen := map[string]bool{}
	for _, id := range selected {
		if !allowed[id] {
			return fmt.Errorf("skill %q is not selected in this project", id)
		}
		if seen[id] {
			return fmt.Errorf("skill %q was selected for update more than once", id)
		}
		seen[id] = true
	}
	return nil
}

func validateResolvedSkillLocks(ids []string, locks map[string]SkillLock, revision string) error {
	for _, id := range ids {
		pin, ok := locks[id]
		if !ok || pin.Repository != projectSkillsRepository || pin.Revision != revision || !isSHA256(pin.SHA256) {
			return fmt.Errorf("resolved skill %s failed source or SHA-256 validation", id)
		}
	}
	return nil
}

// ManagedInstructionsDiff returns a reviewable diff for codebox's bounded
// AGENTS.md region. User-authored text outside the markers is never returned.
func (p ProjectUpdatePlan) ManagedInstructionsDiff() (string, error) {
	before, err := managedSkillZone(string(p.oldInstructions))
	if err != nil {
		return "", err
	}
	after, err := managedSkillZone(string(p.newInstructions))
	if err != nil {
		return "", err
	}
	if before == after {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("--- AGENTS.md (managed skills)\n")
	for _, line := range strings.Split(strings.TrimSuffix(before, "\n"), "\n") {
		if line != "" {
			b.WriteString("- " + line + "\n")
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(after, "\n"), "\n") {
		if line != "" {
			b.WriteString("+ " + line + "\n")
		}
	}
	return b.String(), nil
}

// Apply commits a reviewed plan through a durable journal. Any interrupted
// write is recovered before a later launch can load project skills.
func (p ProjectUpdatePlan) Apply(fs FS) error {
	if p.NoOp {
		return nil
	}
	if len(p.Changes) == 0 || p.root == "" {
		return errors.New("invalid empty project update plan")
	}
	return withProjectUpdateLock(fs, p.root, func() error { return p.applyLocked(fs) })
}

func (p ProjectUpdatePlan) applyLocked(fs FS) error {
	if err := ensureProjectStateDirectory(p.root); err != nil {
		return fmt.Errorf("validate project state directory: %w", err)
	}
	if err := recoverProjectUpdateLocked(fs, p.root); err != nil {
		return fmt.Errorf("recover prior project update: %w", err)
	}
	manifestPath, lockPath := ProjectManifestPath(p.root), ProjectLockPath(p.root)
	if err := ensureRegularManagedFile(manifestPath); err != nil {
		return err
	}
	if err := ensureRegularManagedFile(lockPath); err != nil {
		return err
	}
	currentManifest, err := fs.ReadFile(manifestPath)
	if err != nil || !equalBytes(currentManifest, p.oldManifest) {
		return errors.New("project manifest changed after the update plan was created; review a fresh plan")
	}
	currentLock, err := fs.ReadFile(lockPath)
	if err != nil || !equalBytes(currentLock, p.oldLock) {
		return errors.New("project lockfile changed after the update plan was created; review a fresh plan")
	}
	if p.instructionsPath != "" {
		if err := ensureRegularManagedFile(p.instructionsPath); err != nil {
			return err
		}
		current, readErr := fs.ReadFile(p.instructionsPath)
		if p.instructionsExisted && (readErr != nil || !equalBytes(current, p.oldInstructions)) {
			return errors.New("AGENTS.md changed after the update plan was created; review a fresh plan")
		}
		if !p.instructionsExisted && !os.IsNotExist(readErr) {
			return errors.New("AGENTS.md appeared after the update plan was created; review a fresh plan")
		}
	}
	journal := projectUpdateJournal{
		SchemaVersion: 1, OldManifest: p.oldManifest, OldLock: p.oldLock,
		NewManifest: p.newManifest, NewLock: p.newLock,
		InstructionsExisted: p.instructionsExisted,
	}
	if p.instructionsPath != "" {
		journal.NewInstructionsHash = fmt.Sprintf("%x", sha256.Sum256(p.newInstructions))
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	journalPath := ProjectUpdateJournalPath(p.root)
	if err := atomicWrite(fs, journalPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("stage project update journal: %w", err)
	}
	if err := atomicWrite(fs, manifestPath, p.newManifest, 0o644); err != nil {
		return recoverAfterUpdateFailure(fs, p.root, err)
	}
	if err := atomicWrite(fs, lockPath, p.newLock, 0o644); err != nil {
		return recoverAfterUpdateFailure(fs, p.root, err)
	}
	if p.instructionsPath != "" && !equalBytes(p.oldInstructions, p.newInstructions) {
		if err := atomicWrite(fs, p.instructionsPath, p.newInstructions, 0o644); err != nil {
			return recoverAfterUpdateFailure(fs, p.root, err)
		}
	}
	if err := fs.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return recoverAfterUpdateFailure(fs, p.root, fmt.Errorf("remove project update journal %s: %w", journalPath, err))
	}
	return nil
}

func recoverAfterUpdateFailure(fs FS, root string, cause error) error {
	rolledForward, err := recoverProjectUpdateLockedResult(fs, root)
	if err != nil {
		return fmt.Errorf("project update failed (%v) and recovery is pending: %w", cause, err)
	}
	if rolledForward {
		return nil
	}
	return cause
}

// ProjectUpdateJournalPath returns the crash-recovery journal path.
func ProjectUpdateJournalPath(root string) string {
	return filepath.Join(ProjectStateDir(root), projectUpdateJournalName)
}

// RecoverProjectUpdate completes a fully written pair or restores the
// previous pair if the update stopped between files. It is safe to call before
// every project launch.
func RecoverProjectUpdate(fs FS, root string) error {
	return withProjectUpdateLock(fs, root, func() error { return recoverProjectUpdateLocked(fs, root) })
}

// RollbackProjectUpdate restores the journaled previous manifest and lock.
// Before writing, it backs up the current managed files under .codebox so
// explicit recovery never discards unjournaled edits.
func RollbackProjectUpdate(fs FS, root string) (string, error) {
	var backupDir string
	err := withProjectUpdateLock(fs, root, func() error {
		var err error
		backupDir, err = rollbackProjectUpdateLocked(fs, root)
		return err
	})
	return backupDir, err
}

func rollbackProjectUpdateLocked(fs FS, root string) (string, error) {
	var backupDir string
	if err := ensureProjectStateDirectory(root); err != nil {
		return "", err
	}
	journalPath := ProjectUpdateJournalPath(root)
	if err := ensureRegularManagedFile(journalPath); err != nil {
		return "", err
	}
	data, err := fs.ReadFile(journalPath)
	if err != nil {
		return "", fmt.Errorf("read project update journal %s: %w", journalPath, err)
	}
	var journal projectUpdateJournal
	if err := json.Unmarshal(data, &journal); err != nil || journal.SchemaVersion != 1 || len(journal.OldManifest) == 0 || len(journal.OldLock) == 0 || len(journal.NewManifest) == 0 || len(journal.NewLock) == 0 {
		return "", fmt.Errorf("project update journal %s is invalid; refusing rollback", journalPath)
	}
	oldManifest, oldLock, err := validateUpdateJournalPair(journal.OldManifest, journal.OldLock)
	if err != nil {
		return "", fmt.Errorf("project update journal %s has an invalid previous pair: %w", journalPath, err)
	}
	if _, _, err := validateUpdateJournalPair(journal.NewManifest, journal.NewLock); err != nil {
		return "", fmt.Errorf("project update journal %s has an invalid proposed pair: %w", journalPath, err)
	}
	paths := map[string]string{
		"project.json": ProjectManifestPath(root),
		"lock.json":    ProjectLockPath(root),
		"AGENTS.md":    filepath.Join(root, "AGENTS.md"),
	}
	current := make(map[string][]byte, len(paths))
	for name, path := range paths {
		if err := ensureRegularManagedFile(path); err != nil {
			return "", err
		}
		contents, err := fs.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read current %s before rollback: %w", path, err)
		}
		current[name] = contents
	}
	var mergedInstructions string
	if journal.InstructionsExisted {
		var mergeErr error
		mergedInstructions, mergeErr = MergeManagedInstructions(string(current["AGENTS.md"]), oldManifest.Skills, oldLock.Skills, managedInstructionRuntime(oldManifest.Runtime))
		if mergeErr != nil {
			return "", fmt.Errorf("prepare previous managed instructions: %w", mergeErr)
		}
	} else {
		// The update had not created AGENTS.md, so the previous state has no
		// managed zone at all; strip the one the update wrote.
		var removeErr error
		mergedInstructions, removeErr = RemoveManagedInstructions(string(current["AGENTS.md"]))
		if removeErr != nil {
			return "", fmt.Errorf("prepare previous managed instructions: %w", removeErr)
		}
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate recovery backup name: %w", err)
	}
	backupRoot := filepath.Join(ProjectStateDir(root), "recovery-backups")
	if info, err := os.Lstat(backupRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%s is not a regular recovery backup directory", backupRoot)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	backupDir = filepath.Join(backupRoot, hex.EncodeToString(suffix[:]))
	if err := fs.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create recovery backup directory: %w", err)
	}
	for name, contents := range current {
		if err := atomicWrite(fs, filepath.Join(backupDir, name), contents, 0o600); err != nil {
			return backupDir, fmt.Errorf("back up current %s before rollback: %w", name, err)
		}
	}
	if err := atomicWrite(fs, paths["project.json"], journal.OldManifest, 0o644); err != nil {
		return backupDir, fmt.Errorf("restore previous project manifest: %w", err)
	}
	if err := atomicWrite(fs, paths["lock.json"], journal.OldLock, 0o644); err != nil {
		return backupDir, fmt.Errorf("restore previous project lockfile: %w", err)
	}
	if mergedInstructions == "" && !journal.InstructionsExisted {
		if err := fs.Remove(paths["AGENTS.md"]); err != nil && !os.IsNotExist(err) {
			return backupDir, fmt.Errorf("remove update-created AGENTS.md during rollback: %w", err)
		}
	} else if _, existed := current["AGENTS.md"]; existed || (journal.InstructionsExisted && len(oldManifest.Skills) > 0) {
		if err := atomicWrite(fs, paths["AGENTS.md"], []byte(mergedInstructions), 0o644); err != nil {
			return backupDir, fmt.Errorf("restore previous managed instructions: %w", err)
		}
	}
	if err := fs.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return backupDir, fmt.Errorf("remove rolled-back project update journal: %w", err)
	}
	return backupDir, nil
}

func recoverProjectUpdateLocked(fs FS, root string) error {
	_, err := recoverProjectUpdateLockedResult(fs, root)
	return err
}

func recoverProjectUpdateLockedResult(fs FS, root string) (bool, error) {
	journalPath := ProjectUpdateJournalPath(root)
	data, err := fs.ReadFile(journalPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := ensureProjectStateDirectory(root); err != nil {
		return false, err
	}
	if err := ensureRegularManagedFile(journalPath); err != nil {
		return false, err
	}
	var journal projectUpdateJournal
	if err := json.Unmarshal(data, &journal); err != nil || journal.SchemaVersion != 1 || len(journal.OldManifest) == 0 || len(journal.OldLock) == 0 || len(journal.NewManifest) == 0 || len(journal.NewLock) == 0 {
		return false, fmt.Errorf("project update journal %s is invalid; refusing to launch", journalPath)
	}
	if _, _, err := validateUpdateJournalPair(journal.OldManifest, journal.OldLock); err != nil {
		return false, fmt.Errorf("project update journal %s has an invalid previous pair: %w", journalPath, err)
	}
	if _, _, err := validateUpdateJournalPair(journal.NewManifest, journal.NewLock); err != nil {
		return false, fmt.Errorf("project update journal %s has an invalid proposed pair: %w", journalPath, err)
	}
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := ensureRegularManagedFile(manifestPath); err != nil {
		return false, err
	}
	if err := ensureRegularManagedFile(lockPath); err != nil {
		return false, err
	}
	currentManifest, mErr := fs.ReadFile(manifestPath)
	currentLock, lErr := fs.ReadFile(lockPath)
	if mErr != nil {
		return false, fmt.Errorf("read current project manifest while recovering journal %s: %w", journalPath, mErr)
	}
	if !equalBytes(currentManifest, journal.OldManifest) && !equalBytes(currentManifest, journal.NewManifest) {
		return false, fmt.Errorf("project manifest %s contains bytes not recorded in pending journal %s; refusing recovery", manifestPath, journalPath)
	}
	if lErr != nil {
		return false, fmt.Errorf("read current project lockfile while recovering journal %s: %w", journalPath, lErr)
	}
	if !equalBytes(currentLock, journal.OldLock) && !equalBytes(currentLock, journal.NewLock) {
		return false, fmt.Errorf("project lockfile %s contains bytes not recorded in pending journal %s; refusing recovery", lockPath, journalPath)
	}
	rollForward := mErr == nil && lErr == nil && equalBytes(currentManifest, journal.NewManifest) && equalBytes(currentLock, journal.NewLock)
	chosenManifest, chosenLock := journal.OldManifest, journal.OldLock
	if rollForward {
		chosenManifest, chosenLock = journal.NewManifest, journal.NewLock
	}
	if err := atomicWrite(fs, manifestPath, chosenManifest, 0o644); err != nil {
		return false, fmt.Errorf("recover project manifest: %w", err)
	}
	if err := atomicWrite(fs, lockPath, chosenLock, 0o644); err != nil {
		return false, fmt.Errorf("recover project lockfile: %w", err)
	}
	manifest, lock, err := validateUpdateJournalPair(chosenManifest, chosenLock)
	if err != nil {
		return false, fmt.Errorf("recover project dependency pair: %w", err)
	}
	if len(manifest.Skills) > 0 || journal.InstructionsExisted {
		instructionsPath := filepath.Join(root, "AGENTS.md")
		if !rollForward && !journal.InstructionsExisted {
			current, readErr := fs.ReadFile(instructionsPath)
			if os.IsNotExist(readErr) {
				// The update had not created the file.
			} else if readErr != nil {
				return false, fmt.Errorf("read managed instructions during recovery: %w", readErr)
			} else if fmt.Sprintf("%x", sha256.Sum256(current)) == journal.NewInstructionsHash {
				if err := fs.Remove(instructionsPath); err != nil && !os.IsNotExist(err) {
					return false, fmt.Errorf("restore managed instructions: %w", err)
				}
			} else if len(manifest.Skills) > 0 {
				updated, mergeErr := MergeManagedInstructions(string(current), manifest.Skills, lock.Skills, managedInstructionRuntime(manifest.Runtime))
				if mergeErr != nil {
					return false, fmt.Errorf("recover managed instructions: %w", mergeErr)
				}
				if err := atomicWrite(fs, instructionsPath, []byte(updated), 0o644); err != nil {
					return false, fmt.Errorf("recover managed instructions: %w", err)
				}
			}
		} else if len(manifest.Skills) > 0 {
			current, readErr := fs.ReadFile(instructionsPath)
			if readErr != nil && !os.IsNotExist(readErr) {
				return false, fmt.Errorf("read managed instructions during recovery: %w", readErr)
			}
			if os.IsNotExist(readErr) && journal.InstructionsExisted {
				return false, errors.New("AGENTS.md disappeared during project update recovery; refusing to replace it")
			}
			updated, mergeErr := MergeManagedInstructions(string(current), manifest.Skills, lock.Skills, managedInstructionRuntime(manifest.Runtime))
			if mergeErr != nil {
				return false, fmt.Errorf("recover managed instructions: %w", mergeErr)
			}
			if err := atomicWrite(fs, instructionsPath, []byte(updated), 0o644); err != nil {
				return false, fmt.Errorf("recover managed instructions: %w", err)
			}
		}
	}
	if err := fs.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove recovered project update journal: %w", err)
	}
	return rollForward, nil
}

func validateUpdateJournalPair(manifestData, lockData []byte) (ProjectManifest, Lockfile, error) {
	var manifestRaw map[string]any
	if err := json.Unmarshal(manifestData, &manifestRaw); err != nil {
		return ProjectManifest{}, Lockfile{}, fmt.Errorf("invalid project manifest JSON: %w", err)
	}
	if err := checkNoSecretFields("project manifest", manifestRaw); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	var lockRaw map[string]any
	if err := json.Unmarshal(lockData, &lockRaw); err != nil {
		return ProjectManifest{}, Lockfile{}, fmt.Errorf("invalid project lockfile JSON: %w", err)
	}
	if err := checkNoSecretFields("project lockfile", lockRaw); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	if err := checkSchemaVersion("project manifest", manifest.SchemaVersion, maxSupportedManifestSchema); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	var lock Lockfile
	if err := json.Unmarshal(lockData, &lock); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	if err := checkSchemaVersion("project lockfile", lock.SchemaVersion, maxSupportedLockfileSchema); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	if err := ValidateDependencySet(manifest, lock); err != nil {
		return ProjectManifest{}, Lockfile{}, err
	}
	return manifest, lock, nil
}

func withProjectUpdateLock(fs FS, root string, action func() error) error {
	lock := &ProjectLock{Path: projectUpdateLockPath(root), FS: fs}
	release, err := lock.Acquire()
	if err != nil {
		return fmt.Errorf("acquire project update lock: %w", err)
	}
	defer release()
	return action()
}

func projectUpdateLockPath(root string) string {
	instance := InstanceName(root, filepath.Base(root))
	return filepath.Join(InstanceStateDir(projectUpdateStateDirFn(), instance), "project-update.lock")
}

func ensureRegularManagedFile(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; refusing project update", path)
	}
	return nil
}

func ensureProjectStateDirectory(root string) error {
	dir := ProjectStateDir(root)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a regular project state directory", dir)
	}
	return nil
}

func marshalProjectManifestUpdate(original []byte, manifest ProjectManifest) ([]byte, error) {
	return marshalUpdatedJSON(original, manifest)
}

func marshalLockfileUpdate(original []byte, lock Lockfile) ([]byte, error) {
	return marshalUpdatedJSON(original, lock)
}

func marshalUpdatedJSON(original []byte, value any) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original, &fields); err != nil {
		return nil, fmt.Errorf("decode project file for update: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &known); err != nil {
		return nil, err
	}
	for key, raw := range known {
		fields[key] = raw
	}
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func newDependencySetID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func isUpdateID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func equalBytes(a, b []byte) bool {
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
