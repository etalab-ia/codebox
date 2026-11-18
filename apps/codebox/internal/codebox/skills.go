package codebox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	projectSkillsRepository = "https://github.com/etalab-ia/skills.git"
	// Pin the catalogue at selection time. Launches consume the lockfile and
	// local cache; they never follow a moving branch.
	projectSkillsRevision = "c791677af5f9f50cda131bd60220b717220d5c02"
	maxSkillFileBytes     = 8 << 20
	maxSkillArchiveBytes  = 32 << 20
	maxSkillFiles         = 512
	maxProjectSkills      = 32
	maxSkillSetBytes      = 64 << 20
	skillSourceTimeout    = 90 * time.Second
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var userCacheDirFn = os.UserCacheDir

// ProjectSkill is a catalogue entry selected by the project. ID includes the
// catalogue category so official and experimental names cannot alias.
type ProjectSkill struct {
	ID           string
	Name         string
	Description  string
	Path         string
	Experimental bool
}

// SearchProjectSkills filters catalogue entries by case-insensitive words in
// the id, name, or description. Every query word must match.
func SearchProjectSkills(catalogue []ProjectSkill, query string) []ProjectSkill {
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(words) == 0 {
		return append([]ProjectSkill(nil), catalogue...)
	}
	result := make([]ProjectSkill, 0, len(catalogue))
	for _, skill := range catalogue {
		searchable := strings.ToLower(skill.ID + " " + skill.Name + " " + skill.Description)
		matched := true
		for _, word := range words {
			if !strings.Contains(searchable, word) {
				matched = false
				break
			}
		}
		if matched {
			result = append(result, skill)
		}
	}
	return result
}

// SkillLock pins the source commit and the exact guest artifact bytes.
type SkillLock struct {
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	SHA256     string `json:"sha256"`
}

// SkillPackage is the verified, normalized archive passed to the guest.
type SkillPackage struct {
	ID      string
	Name    string
	Lock    SkillLock
	Archive []byte
}

// LocalSkillSelections are explicitly host-local and never part of the
// checkout. Their files are still pinned to source commit and archive digest.
type LocalSkillSelections struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Skills        []string             `json:"skills,omitempty"`
	Pins          map[string]SkillLock `json:"pins,omitempty"`
}

func LocalSkillSelectionsPath(stateDir, instance string) (string, error) {
	if !validProjectInstance(instance) {
		return "", fmt.Errorf("instance de projet invalide pour l'état des skills locaux")
	}
	return filepath.Join(InstanceStateDir(stateDir, instance), "skills.json"), nil
}

func ReadLocalSkillSelections(fs FS, path string) (LocalSkillSelections, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LocalSkillSelections{SchemaVersion: 1}, nil
		}
		return LocalSkillSelections{}, err
	}
	var selection LocalSkillSelections
	if err := json.Unmarshal(data, &selection); err != nil {
		return LocalSkillSelections{}, fmt.Errorf("sélections de skills locaux %s : JSON invalide : %w", path, err)
	}
	if selection.SchemaVersion != 1 {
		return LocalSkillSelections{}, fmt.Errorf("sélections de skills locaux %s : schemaVersion %d non pris en charge", path, selection.SchemaVersion)
	}
	if err := validateSkillIDs(selection.Skills, nil); err != nil {
		return LocalSkillSelections{}, err
	}
	return selection, nil
}

// LoadProjectSkillPackages selects either the versioned lock or the explicit
// host-local selection and verifies every cache artifact before runtime start.
func LoadProjectSkillPackages(fs FS, projectRoot, stateDir, instance string) ([]SkillPackage, error) {
	packages, _, _, _, err := LoadProjectSkillState(fs, projectRoot, stateDir, instance)
	return packages, err
}

// LoadProjectSkillState also returns the desired IDs, whether this manifest
// version owns guest-skill reconciliation (needed for deselection), and
// whether the selection is host-local rather than versioned in the checkout.
func LoadProjectSkillState(fs FS, projectRoot, stateDir, instance string) ([]SkillPackage, []string, bool, bool, error) {
	var packages []SkillPackage
	var ids []string
	var manages, localOnly bool
	err := withProjectUpdateLock(fs, projectRoot, func() error {
		if err := recoverProjectUpdateLocked(fs, projectRoot); err != nil {
			return err
		}
		var err error
		packages, ids, manages, localOnly, err = loadProjectSkillStateLocked(fs, projectRoot, stateDir, instance)
		return err
	})
	return packages, ids, manages, localOnly, err
}

func loadProjectSkillStateLocked(fs FS, projectRoot, stateDir, instance string) ([]SkillPackage, []string, bool, bool, error) {
	manifest, err := ReadProjectManifest(fs, ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, false, nil
		}
		return nil, nil, false, false, err
	}
	lock, err := ReadLockfile(fs, ProjectLockPath(projectRoot))
	if err != nil {
		return nil, nil, false, false, err
	}
	if err := ValidateDependencySet(manifest, lock); err != nil {
		return nil, nil, false, false, err
	}
	ids := manifest.Skills
	pins := lock.Skills
	if manifest.SkillsLocalOnly {
		path, err := LocalSkillSelectionsPath(stateDir, instance)
		if err != nil {
			return nil, nil, false, false, err
		}
		local, err := ReadLocalSkillSelections(fs, path)
		if err != nil {
			return nil, nil, false, false, err
		}
		ids, pins = local.Skills, local.Pins
	}
	packages, err := LoadLockedSkillPackages(ids, pins)
	if err != nil {
		return nil, nil, false, false, err
	}
	return packages, append([]string(nil), ids...), manifest.SchemaVersion >= 2, manifest.SkillsLocalOnly, nil
}

func WriteLocalSkillSelections(fs FS, path string, selection LocalSkillSelections) error {
	if err := validateSkillIDs(selection.Skills, nil); err != nil {
		return err
	}
	selection.SchemaVersion = 1
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(fs, path, append(data, '\n'), 0o600)
}

func validProjectInstance(instance string) bool {
	if instance == "" || filepath.Base(instance) != instance || strings.ContainsAny(instance, `/\\`) {
		return false
	}
	for _, r := range instance {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type skillTreeEntry struct {
	Mode string
	Path string
	OID  string
}

// ProjectSkillCatalogue returns the official and explicitly labeled
// experimental skills at the source revision recorded in this binary.
func ProjectSkillCatalogue(ctx context.Context) ([]ProjectSkill, error) {
	return ProjectSkillCatalogueAtRevision(ctx, projectSkillsRevision)
}

// ProjectSkillCatalogueAtRevision returns the skill catalogue at an exact Git
// commit. Update uses this to inspect a candidate revision without changing
// the revision used by ordinary init.
func ProjectSkillCatalogueAtRevision(ctx context.Context, revision string) ([]ProjectSkill, error) {
	if !isGitRevision(revision) {
		return nil, fmt.Errorf("révision des skills de projet invalide %q", revision)
	}
	ctx, cancel := context.WithTimeout(ctx, skillSourceTimeout)
	defer cancel()
	repo, err := cachedProjectSkillsRepoAt(ctx, revision)
	if err != nil {
		return nil, err
	}
	entries, err := readSkillTreeAt(ctx, repo, revision)
	if err != nil {
		return nil, err
	}
	return parseProjectSkillCatalogue(entries, func(oid string) ([]byte, error) {
		return gitOutput(ctx, repo, "cat-file", "blob", oid)
	})
}

func parseProjectSkillCatalogue(entries []skillTreeEntry, readBlob func(string) ([]byte, error)) ([]ProjectSkill, error) {
	var result []ProjectSkill
	seen := map[string]string{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, "skills/") || path.Base(entry.Path) != "SKILL.md" {
			continue
		}
		category, name, ok := skillPathIdentity(entry.Path)
		if !ok {
			continue
		}
		if entry.Mode != "100644" {
			return nil, fmt.Errorf("le skill %s n'est pas un fichier régulier", entry.Path)
		}
		data, err := readBlob(entry.OID)
		if err != nil {
			return nil, err
		}
		meta, err := parseSkillFrontmatter(data)
		if err != nil {
			return nil, fmt.Errorf("skill %s : %w", entry.Path, err)
		}
		if !skillNamePattern.MatchString(meta.Name) || meta.Name != name {
			return nil, fmt.Errorf("le skill %s déclare un nom %q invalide ou incohérent", entry.Path, meta.Name)
		}
		if previous, exists := seen[meta.Name]; exists {
			return nil, fmt.Errorf("nom de skill %q en double dans %s et %s", meta.Name, previous, entry.Path)
		}
		seen[meta.Name] = entry.Path
		result = append(result, ProjectSkill{
			ID: category + "/" + name, Name: name, Description: meta.Description,
			Path: path.Dir(entry.Path), Experimental: category == "experimental",
		})
	}
	return result, nil
}

// ResolveProjectSkills validates user selections, builds archives from the
// pinned repository tree, verifies them, and places them in the host cache.
func ResolveProjectSkills(ctx context.Context, ids []string) ([]ProjectSkill, map[string]SkillLock, error) {
	return ResolveProjectSkillsAtRevision(ctx, ids, projectSkillsRevision)
}

// ResolveProjectSkillsAtRevision resolves selected skill archives at an exact
// source commit. Archives are content-addressed in the host cache before the
// lock can be written to a project.
func ResolveProjectSkillsAtRevision(ctx context.Context, ids []string, revision string) ([]ProjectSkill, map[string]SkillLock, error) {
	if !isGitRevision(revision) {
		return nil, nil, fmt.Errorf("révision des skills de projet invalide %q", revision)
	}
	ctx, cancel := context.WithTimeout(ctx, skillSourceTimeout)
	defer cancel()
	catalogue, err := ProjectSkillCatalogueAtRevision(ctx, revision)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]ProjectSkill, len(catalogue))
	for _, skill := range catalogue {
		byID[skill.ID] = skill
	}
	if err := validateSkillIDs(ids, byID); err != nil {
		return nil, nil, err
	}
	repo, err := cachedProjectSkillsRepoAt(ctx, revision)
	if err != nil {
		return nil, nil, err
	}
	selected := make([]ProjectSkill, 0, len(ids))
	locks := make(map[string]SkillLock, len(ids))
	var totalBytes int64
	for _, id := range ids {
		skill := byID[id]
		raw, err := gitOutput(ctx, repo, "archive", "--format=tar", revision, "LICENSE", skill.Path)
		if err != nil {
			return nil, nil, fmt.Errorf("archivage du skill sélectionné %s : %w", id, err)
		}
		archive, err := normalizeSkillArchive(bytes.NewReader(raw), skill.Path, skill.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("archivage du skill sélectionné %s : %w", id, err)
		}
		totalBytes += int64(len(archive))
		if totalBytes > maxSkillSetBytes {
			return nil, nil, fmt.Errorf("les skills sélectionnés dépassent la limite totale de %d octets du cache et de la machine isolée", maxSkillSetBytes)
		}
		digest := sha256.Sum256(archive)
		lock := SkillLock{Repository: projectSkillsRepository, Revision: revision, SHA256: hex.EncodeToString(digest[:])}
		if err := cacheSkillArchive(skill, lock, archive); err != nil {
			return nil, nil, err
		}
		cached, err := readCachedSkillArchive(skill.Name, lock)
		if err != nil || !equalBytes(cached, archive) {
			return nil, nil, fmt.Errorf("le skill %s a échoué à sa vérification d'intégrité SHA-256 après mise en cache", id)
		}
		selected = append(selected, skill)
		locks[id] = lock
	}
	return selected, locks, nil
}

// LoadLockedSkillPackages verifies local cached archives for offline launch.
func LoadLockedSkillPackages(ids []string, locks map[string]SkillLock) ([]SkillPackage, error) {
	if err := validateSkillIDs(ids, nil); err != nil {
		return nil, err
	}
	packages := make([]SkillPackage, 0, len(ids))
	var totalBytes int64
	for _, id := range ids {
		lock, ok := locks[id]
		if !ok {
			return nil, fmt.Errorf("le skill %s n'a pas d'entrée de verrouillage ; relancez 'codebox init' en ligne pour l'épingler", id)
		}
		if lock.Repository != projectSkillsRepository || !isGitRevision(lock.Revision) || !isSHA256(lock.SHA256) {
			return nil, fmt.Errorf("le skill %s a un verrouillage de source invalide", id)
		}
		_, name, ok := parseProjectSkillID(id)
		if !ok {
			return nil, fmt.Errorf("identifiant de skill invalide %q", id)
		}
		archive, err := readCachedSkillArchive(name, lock)
		if err != nil {
			return nil, fmt.Errorf("skill %s : %w", id, err)
		}
		totalBytes += int64(len(archive))
		if totalBytes > maxSkillSetBytes {
			return nil, fmt.Errorf("les skills sélectionnés dépassent la limite totale de %d octets du cache et de la machine isolée", maxSkillSetBytes)
		}
		if err := validateNormalizedSkillArchive(archive, name); err != nil {
			return nil, fmt.Errorf("archive en cache du skill %s : %w", id, err)
		}
		packages = append(packages, SkillPackage{ID: id, Name: name, Lock: lock, Archive: archive})
	}
	return packages, nil
}

// webPreviewGuidance states the port-forward facts agents get wrong: a
// guest-side 127.0.0.1 bind is unreachable from the host, while the host side
// of a Microsandbox forward binds loopback only, so a guest-side 0.0.0.0 bind
// exposes nothing on the network.
const webPreviewGuidance = "Web preview: a dev server must listen on 0.0.0.0 inside this workspace; " +
	"binding 127.0.0.1 makes it unreachable through the host port forward. " +
	"Only the host's 127.0.0.1 can reach the forwarded port, so a 0.0.0.0 bind here does not expose anything on the network. " +
	"A request from inside this workspace proves nothing about host reachability; " +
	"run `codebox check` to verify a published port from the host.\n"

// MergeManagedInstructions replaces the bounded codebox section with the
// current standing instructions and pinned-skill list. All bytes outside the
// markers are preserved exactly. The standing web-preview guidance describes
// the Microsandbox port forward, so it is rendered for that runtime only; on
// other runtimes a selection without pins leaves no managed section at all.
func MergeManagedInstructions(existing string, ids []string, locks map[string]SkillLock, runtime Runtime) (string, error) {
	const start = "<!-- BEGIN CODEBOX MANAGED SKILLS -->"
	const end = "<!-- END CODEBOX MANAGED SKILLS -->"
	standing := runtime == RuntimeMicrosandbox
	if !standing && len(ids) == 0 {
		return RemoveManagedInstructions(existing)
	}
	block, err := renderManagedInstructions(start, end, ids, locks, standing)
	if err != nil {
		return "", err
	}
	return replaceManagedZone(existing, start, end, block)
}

// RemoveManagedInstructions removes the managed section entirely, preserving
// all bytes outside the markers. It is the transition for projects that keep
// skills host-local: no managed content remains in the file.
func RemoveManagedInstructions(existing string) (string, error) {
	const start = "<!-- BEGIN CODEBOX MANAGED SKILLS -->"
	const end = "<!-- END CODEBOX MANAGED SKILLS -->"
	return replaceManagedZone(existing, start, end, "")
}

// managedInstructionRuntime resolves a manifest's runtime name for managed
// instruction rendering. The stored name is parsed without host compatibility
// checks: a manifest written on macOS (tart) or Linux (agent-vm) must keep
// its runtime-specific rendering when its instructions are regenerated on a
// host where that runtime cannot execute — those checks belong to runtime
// execution, not instruction rendering. An unparsable name falls back to the
// wizard default (Microsandbox): these paths run during rollback and
// recovery, where the guidance text must not turn a broken manifest into a
// hard failure.
func managedInstructionRuntime(name string) Runtime {
	switch name {
	case string(RuntimeTart):
		return RuntimeTart
	case string(RuntimeAgentVM):
		return RuntimeAgentVM
	default:
		return RuntimeMicrosandbox
	}
}

// hasManagedSkillZone reports whether AGENTS.md content carries a managed
// skill zone, including a malformed one: both must be cleaned up by a plan
// that rewrites the zone, so both make the plan claim the file.
func hasManagedSkillZone(text string) bool {
	zone, err := managedSkillZone(text)
	return err != nil || zone != ""
}

// managedZoneAt reports whether the AGENTS.md at path carries a managed
// zone. It opens the path once with O_NONBLOCK — opening a FIFO for
// reading blocks until a writer appears — inspects the opened handle
// rather than re-resolving the path, and scans the opened regular file
// for markers in bounded memory, so an entry swapped in after
// validation cannot stall the probe and a huge or sparse file cannot
// balloon it. A missing or dangling path reads as no zone; any other
// non-regular or unreadable entry fails closed, same as a zone, so the
// caller claims the path and its own validation refuses it.
func managedZoneAt(path string) bool {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return !os.IsNotExist(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return true
	}
	return hasManagedSkillZoneIn(f)
}

// hasManagedSkillZoneIn reports whether r contains a codebox managed
// skill marker line, matching hasManagedSkillZone: a balanced pair is a
// zone, and any other marker combination is malformed and fails closed
// there, so either marker's presence means the file must be claimed. It
// reads in bounded chunks — a line longer than the buffer cannot be a
// marker and is skipped — so file size does not bound memory.
func hasManagedSkillZoneIn(r io.Reader) bool {
	const start = "<!-- BEGIN CODEBOX MANAGED SKILLS -->"
	const end = "<!-- END CODEBOX MANAGED SKILLS -->"
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// A line longer than the buffer cannot be a marker
			// line; discard it entirely, including its final
			// chunk, and keep scanning.
			line, err = br.ReadSlice('\n')
			for err == bufio.ErrBufferFull {
				line, err = br.ReadSlice('\n')
			}
			if err == io.EOF {
				return false
			}
			if err != nil {
				return true
			}
			continue
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == start || trimmed == end {
			return true
		}
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
	}
}

// renderManagedInstructions renders the managed section: standing guidance
// for runtimes with a host-loopback port forward, plus the pinned-skill list
// when a selection exists.
func renderManagedInstructions(start, end string, ids []string, locks map[string]SkillLock, standing bool) (string, error) {
	var b strings.Builder
	b.WriteString(start + "\n")
	if standing {
		b.WriteString("## Managed codebox instructions\n\n")
		b.WriteString(webPreviewGuidance)
	}
	if len(ids) > 0 {
		if standing {
			b.WriteString("\n")
		}
		b.WriteString("## Managed project skills\n\n")
		b.WriteString("These pinned skills provide agent instructions; they are not a security boundary.\n\n")
		for _, id := range ids {
			lock, ok := locks[id]
			if !ok {
				return "", fmt.Errorf("le skill %s n'a pas d'entrée de verrouillage", id)
			}
			fmt.Fprintf(&b, "- `%s` at `%s`\n", id, lock.Revision)
		}
	}
	b.WriteString(end + "\n")
	return b.String(), nil
}

// replaceManagedZone splices block between the managed markers in existing,
// preserving every byte outside them. An empty block removes the section; an
// absent section is appended after the existing content.
func replaceManagedZone(existing, start, end, block string) (string, error) {
	startAt, endAt, err := managedSkillMarkers(existing, start, end)
	if err != nil {
		return "", err
	}
	if startAt < 0 {
		if block == "" {
			return existing, nil
		}
		if existing == "" {
			return block, nil
		}
		separator := ""
		if !strings.HasSuffix(existing, "\n") {
			separator = "\n"
		}
		return existing + separator + "\n" + block, nil
	}
	before, after := existing[:startAt], existing[endAt:]
	if block == "" {
		return before + after, nil
	}
	return before + block + after, nil
}

// managedZoneHasSkillPins reports whether the managed zone lists pinned
// skills (lines of the form "- `id` at `rev`"). The standing guidance is not
// a pin: a zone with only standing text is not evidence that versioned
// skills remain.
func managedZoneHasSkillPins(zone string) bool {
	for _, line := range strings.Split(zone, "\n") {
		if strings.HasPrefix(line, "- `") {
			return true
		}
	}
	return false
}

func managedSkillZone(text string) (string, error) {
	const start = "<!-- BEGIN CODEBOX MANAGED SKILLS -->"
	const end = "<!-- END CODEBOX MANAGED SKILLS -->"
	from, to, err := managedSkillMarkers(text, start, end)
	if err != nil || from < 0 {
		return "", err
	}
	return text[from:to], nil
}

func cloneSkillLocks(in map[string]SkillLock) map[string]SkillLock {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]SkillLock, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func managedSkillMarkers(text, start, end string) (int, int, error) {
	startLine, endLine := -1, -1
	starts, ends := 0, 0
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if strings.TrimSpace(trimmed) == start {
			starts++
			startLine = offset
		}
		if strings.TrimSpace(trimmed) == end {
			ends++
			endLine = offset + len(line)
		}
		offset += len(line)
	}
	if starts == 0 && ends == 0 {
		return -1, -1, nil
	}
	if starts != 1 || ends != 1 || startLine >= endLine {
		return -1, -1, errors.New("AGENTS.md a des marqueurs de skills codebox mal formés ou ambigus ; corrigez les marqueurs avant de continuer")
	}
	return startLine, endLine, nil
}

func parseSkillFrontmatter(data []byte) (skillFrontmatter, error) {
	var meta skillFrontmatter
	if len(data) > maxSkillFileBytes {
		return meta, fmt.Errorf("SKILL.md dépasse la limite de %d octets", maxSkillFileBytes)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return meta, errors.New("SKILL.md doit commencer par un frontmatter YAML")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return meta, errors.New("le frontmatter de SKILL.md n'est pas terminé")
	}
	frontmatter := []byte(strings.Join(lines[1:end], "\n"))
	if len(frontmatter) > 64<<10 {
		return meta, errors.New("le frontmatter de SKILL.md dépasse la limite de 64 KiB")
	}
	if err := yaml.Unmarshal(frontmatter, &meta); err != nil {
		return meta, fmt.Errorf("frontmatter YAML invalide : %w", err)
	}
	meta.Name = strings.TrimSpace(meta.Name)
	meta.Description = strings.TrimSpace(meta.Description)
	if meta.Name == "" || meta.Description == "" {
		return meta, errors.New("SKILL.md doit fournir un nom et une description non vides")
	}
	return meta, nil
}

func skillPathIdentity(p string) (category, name string, ok bool) {
	parts := strings.Split(p, "/")
	switch {
	case len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md":
		category, name = "official", parts[1]
	case len(parts) == 4 && parts[0] == "skills" && parts[1] == ".experimental" && parts[3] == "SKILL.md":
		category, name = "experimental", parts[2]
	default:
		return "", "", false
	}
	if !skillNamePattern.MatchString(name) {
		return "", "", false
	}
	return category, name, true
}

func parseProjectSkillID(id string) (category, name string, ok bool) {
	category, name, found := strings.Cut(id, "/")
	if !found || (category != "official" && category != "experimental") || !skillNamePattern.MatchString(name) {
		return "", "", false
	}
	return category, name, true
}

func validateSkillIDs(ids []string, known map[string]ProjectSkill) error {
	if len(ids) > maxProjectSkills {
		return fmt.Errorf("au plus %d skills de projet peuvent être sélectionnés", maxProjectSkills)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || (!strings.HasPrefix(id, "official/") && !strings.HasPrefix(id, "experimental/")) {
			return fmt.Errorf("identifiant de skill invalide %q ; utilisez official/<name> ou experimental/<name>", id)
		}
		if _, _, ok := parseProjectSkillID(id); !ok {
			return fmt.Errorf("identifiant de skill invalide %q", id)
		}
		if seen[id] {
			return fmt.Errorf("le skill %q a été sélectionné plusieurs fois", id)
		}
		seen[id] = true
		if known != nil {
			if _, ok := known[id]; !ok {
				return fmt.Errorf("skill de projet inconnu %q", id)
			}
		}
	}
	return nil
}

// ValidateProjectSkillIDs is the network-free init flag validator.
func ValidateProjectSkillIDs(ids []string) error {
	return validateSkillIDs(ids, nil)
}

func validateProjectSkillRuntime(runtime Runtime, ids []string) error {
	if len(ids) > 0 && runtime != RuntimeMicrosandbox {
		return fmt.Errorf("les skills de projet sélectionnés exigent le runtime microsandbox scellé")
	}
	return nil
}

func normalizeSkillArchive(src io.Reader, sourceDir, expectedName string) ([]byte, error) {
	tr := tar.NewReader(src)
	files := make(map[string][]byte)
	var total int64
	foundSkill := false
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("archive source invalide : %w", err)
		}
		// git archive may add a PAX global header for the source commit
		// timestamp. It carries no skill payload and is not extracted.
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if count >= maxSkillFiles {
			return nil, fmt.Errorf("l'archive dépasse %d entrées", maxSkillFiles)
		}
		name := strings.ReplaceAll(h.Name, "\\", "/")
		if sourceDir != "" {
			if name == "LICENSE" && h.Typeflag != tar.TypeDir {
				// The upstream MIT notice ships with every installed skill.
			} else {
				if h.Typeflag == tar.TypeDir && strings.HasPrefix(sourceDir, strings.TrimSuffix(name, "/")+"/") {
					continue
				}
				prefix := sourceDir + "/"
				if strings.TrimSuffix(name, "/") == sourceDir && h.Typeflag == tar.TypeDir {
					continue
				}
				if !strings.HasPrefix(name, prefix) {
					return nil, fmt.Errorf("l'entrée d'archive %q est hors du skill sélectionné", h.Name)
				}
				name = strings.TrimPrefix(name, prefix)
			}
		}
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		clean := path.Clean(name)
		if clean == "." && h.Typeflag == tar.TypeDir {
			continue
		}
		if name == "" || path.IsAbs(name) || clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("entrée d'archive %q non sûre", h.Name)
		}
		if strings.ContainsAny(clean, " \t\r\n\\") {
			return nil, fmt.Errorf("l'entrée d'archive %q a un chemin non pris en charge", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return nil, fmt.Errorf("l'entrée d'archive %q n'est pas un fichier régulier", h.Name)
		}
		if h.Size < 0 || h.Size > maxSkillFileBytes || total+h.Size > maxSkillArchiveBytes {
			return nil, fmt.Errorf("l'entrée d'archive %q dépasse la limite de taille", h.Name)
		}
		if _, exists := files[clean]; exists {
			return nil, fmt.Errorf("l'archive contient un chemin en double %q", clean)
		}
		data, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("le corps de l'entrée d'archive %q est tronqué", h.Name)
		}
		total += h.Size
		files[clean] = data
		if clean == "SKILL.md" {
			meta, err := parseSkillFrontmatter(data)
			if err != nil {
				return nil, err
			}
			if meta.Name != expectedName {
				return nil, fmt.Errorf("SKILL.md déclare %q, attendu %q", meta.Name, expectedName)
			}
			foundSkill = true
		}
	}
	if !foundSkill {
		return nil, errors.New("l'archive ne contient pas de SKILL.md à la racine")
	}
	if _, exists := files[".codebox-checksums"]; exists {
		return nil, errors.New("l'archive de skill utilise un chemin de métadonnées codebox réservé")
	}
	var checksums strings.Builder
	for _, name := range sortedSkillFiles(files) {
		digest := sha256.Sum256(files[name])
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	files[".codebox-checksums"] = []byte(checksums.String())
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	zw.Header.ModTime = time.Unix(0, 0)
	tw := tar.NewWriter(zw)
	for _, name := range sortedSkillFiles(files) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(files[name])), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if compressed.Len() > maxSkillArchiveBytes {
		return nil, fmt.Errorf("l'archive normalisée dépasse %d octets", maxSkillArchiveBytes)
	}
	return compressed.Bytes(), nil
}

func validateNormalizedSkillArchive(data []byte, expectedName string) error {
	if len(data) > maxSkillArchiveBytes {
		return fmt.Errorf("l'archive dépasse %d octets", maxSkillArchiveBytes)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("archive gzip invalide : %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	files := make(map[string][]byte)
	var total int64
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("archive invalide : %w", err)
		}
		if count >= maxSkillFiles+1 {
			return fmt.Errorf("l'archive dépasse %d entrées", maxSkillFiles+1)
		}
		name := h.Name
		if name == "" || path.Clean(name) != name || path.IsAbs(name) || strings.HasPrefix(name, "../") || strings.ContainsAny(name, " \t\r\n\\") {
			return fmt.Errorf("entrée d'archive %q non sûre", name)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return fmt.Errorf("l'entrée d'archive %q n'est pas un fichier régulier", name)
		}
		if h.Mode != 0o644 || h.Size < 0 || h.Size > maxSkillFileBytes || total+h.Size > maxSkillArchiveBytes {
			return fmt.Errorf("l'entrée d'archive %q a un mode ou une taille non sûrs", name)
		}
		if _, exists := files[name]; exists {
			return fmt.Errorf("l'archive contient un chemin en double %q", name)
		}
		body, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil || int64(len(body)) != h.Size {
			return fmt.Errorf("l'entrée d'archive %q est tronquée", name)
		}
		files[name] = body
		total += h.Size
	}
	metadata, ok := files[".codebox-checksums"]
	if !ok {
		return errors.New("l'archive n'a pas de manifeste d'intégrité")
	}
	skill, ok := files["SKILL.md"]
	if !ok {
		return errors.New("l'archive ne contient pas de SKILL.md à la racine")
	}
	if _, ok := files["LICENSE"]; !ok {
		return errors.New("l'archive n'a pas la licence du catalogue amont")
	}
	frontmatter, err := parseSkillFrontmatter(skill)
	if err != nil {
		return err
	}
	if frontmatter.Name != expectedName {
		return fmt.Errorf("SKILL.md déclare %q, attendu %q", frontmatter.Name, expectedName)
	}
	content := make(map[string][]byte, len(files)-1)
	for name, body := range files {
		if name != ".codebox-checksums" {
			content[name] = body
		}
	}
	var want strings.Builder
	for _, name := range sortedSkillFiles(content) {
		digest := sha256.Sum256(content[name])
		fmt.Fprintf(&want, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	if !bytes.Equal(metadata, []byte(want.String())) {
		return errors.New("le manifeste d'intégrité de l'archive ne correspond pas au contenu des fichiers")
	}
	return nil
}

func sortedSkillFiles(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func cachedProjectSkillsRepo(ctx context.Context) (string, error) {
	return cachedProjectSkillsRepoAt(ctx, projectSkillsRevision)
}

func cachedProjectSkillsRepoAt(ctx context.Context, revision string) (string, error) {
	if !isGitRevision(revision) {
		return "", fmt.Errorf("révision des skills de projet invalide %q", revision)
	}
	cache, err := userCacheDirFn()
	if err != nil {
		return "", fmt.Errorf("résolution du dossier de cache utilisateur : %w", err)
	}
	parent := filepath.Join(cache, "codebox", "skill-sources")
	dest := filepath.Join(parent, revision)
	if info, err := os.Lstat(dest); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("le cache source des skills %s n'est pas un dossier régulier", dest)
		}
		got, err := gitOutput(ctx, dest, "rev-parse", "FETCH_HEAD")
		if err != nil || strings.TrimSpace(string(got)) != revision {
			return "", fmt.Errorf("le cache source des skills %s ne correspond pas à la révision épinglée %s", dest, revision)
		}
		return dest, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(parent, ".skills-source-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if _, err := runGit(ctx, tmp, "init"); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, tmp, "remote", "add", "origin", projectSkillsRepository); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, tmp, "fetch", "--depth=1", "origin", revision); err != nil {
		return "", fmt.Errorf("récupération du catalogue de skills de projet sur %s : %w", revision, err)
	}
	// Read directly from fetched Git objects. A checkout would invoke any
	// user-configured smudge filter named by the external repository.
	got, err := gitOutput(ctx, tmp, "rev-parse", "FETCH_HEAD")
	if err != nil || strings.TrimSpace(string(got)) != revision {
		return "", errors.New("la source des skills n'a pas résolu vers le commit demandé")
	}
	if err := os.Rename(tmp, dest); err != nil {
		if info, statErr := os.Lstat(dest); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			got, checkErr := gitOutput(ctx, dest, "rev-parse", "FETCH_HEAD")
			if checkErr == nil && strings.TrimSpace(string(got)) == revision {
				return dest, nil
			}
		}
		return "", fmt.Errorf("mise en cache de la source des skills épinglée : %w", err)
	}
	return dest, nil
}

func readSkillTree(ctx context.Context, repo string) ([]skillTreeEntry, error) {
	return readSkillTreeAt(ctx, repo, projectSkillsRevision)
}

func readSkillTreeAt(ctx context.Context, repo, revision string) ([]skillTreeEntry, error) {
	data, err := gitOutput(ctx, repo, "ls-tree", "-r", "-z", revision)
	if err != nil {
		return nil, err
	}
	var entries []skillTreeEntry
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, p, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, errors.New("enregistrement d'arbre Git des skills invalide")
		}
		parts := strings.Fields(string(meta))
		if len(parts) != 3 {
			return nil, errors.New("métadonnées d'arbre Git des skills invalides")
		}
		entries = append(entries, skillTreeEntry{Mode: parts[0], OID: parts[2], Path: string(p)})
	}
	return entries, nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	hooks, err := os.MkdirTemp("", "codebox-git-hooks-disabled-")
	if err != nil {
		return nil, fmt.Errorf("création du dossier de hooks Git isolé : %w", err)
	}
	defer os.RemoveAll(hooks)
	fullArgs := append([]string{"-C", dir, "-c", "core.hooksPath=" + hooks, "-c", "init.templateDir="}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return runGit(ctx, dir, args...)
}

func cacheSkillArchive(skill ProjectSkill, lock SkillLock, archive []byte) error {
	path, err := skillArchiveCachePath(skill.Name, lock)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(DefaultFS, path, archive, 0o600)
}

func readCachedSkillArchive(name string, lock SkillLock) ([]byte, error) {
	path, err := skillArchiveCachePath(name, lock)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("l'archive épinglée est absente du cache hors ligne ; lancez 'codebox init --replace --skill <category/%s>' en ligne", name)
		}
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != lock.SHA256 {
		return nil, fmt.Errorf("l'archive en cache a échoué à sa vérification d'intégrité SHA-256")
	}
	return data, nil
}

func skillArchiveCachePath(name string, lock SkillLock) (string, error) {
	if !skillNamePattern.MatchString(name) || !isGitRevision(lock.Revision) || !isSHA256(lock.SHA256) {
		return "", errors.New("clé de cache de skill invalide")
	}
	cache, err := userCacheDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "codebox", "skills", lock.Revision, name+"-"+lock.SHA256+".tar.gz"), nil
}

func isGitRevision(rev string) bool {
	if len(rev) != 40 {
		return false
	}
	_, err := hex.DecodeString(rev)
	return err == nil
}

func isSHA256(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	_, err := hex.DecodeString(sum)
	return err == nil
}
