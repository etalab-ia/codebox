package codebox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	guestSkillInventorySchema  = 1
	openCodeServiceStopTimeout = 10 * time.Second
)

type guestSkillEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision string `json:"revision"`
	SHA256   string `json:"sha256"`
}

type guestSkillInventory struct {
	SchemaVersion int               `json:"schemaVersion"`
	Skills        []guestSkillEntry `json:"skills"`
}

func (m *MicrosandboxRuntime) installProjectSkills(ctx context.Context) error {
	inventory, packages, err := m.preflightProjectSkills(ctx)
	if err != nil || !m.cfg.ProjectSkillsManaged {
		return err
	}
	entries := make([]guestSkillEntry, 0, len(m.cfg.ProjectSkillIDs))
	for _, id := range m.cfg.ProjectSkillIDs {
		skill := packages[id]
		entries = append(entries, guestSkillEntry{ID: id, Name: skill.Name, Revision: skill.Lock.Revision, SHA256: skill.Lock.SHA256})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	if !guestSkillInventoryMatches(inventory, entries) {
		data, err := json.Marshal(guestSkillInventory{SchemaVersion: guestSkillInventorySchema, Skills: entries})
		if err != nil {
			return err
		}
		indexArchive := "/tmp/codebox-project-skills.json"
		if err := m.Client.WriteFile(ctx, m.InstanceName(), indexArchive, append(data, '\n')); err != nil {
			return fmt.Errorf("préparation de l'inventaire des skills du projet dans la machine isolée : %w", err)
		}
		if err := m.commitGuestSkillInventory(ctx, entries, indexArchive); err != nil {
			return err
		}
	}

	for _, id := range m.cfg.ProjectSkillIDs {
		skill := packages[id]
		archivePath := "/tmp/codebox-skill-" + skill.Name + "-" + skill.Lock.SHA256 + ".tar.gz"
		if err := m.Client.WriteFile(ctx, m.InstanceName(), archivePath, skill.Archive); err != nil {
			return fmt.Errorf("copie du skill %s dans la machine isolée : %w", skill.ID, err)
		}
		if err := m.installOneProjectSkill(ctx, skill, archivePath); err != nil {
			return fmt.Errorf("installation du skill épinglé %s dans la machine isolée : %w", skill.ID, err)
		}
	}
	return m.verifyProjectSkills(ctx)
}

// preflightProjectSkills validates the desired pins and current guest state
// without writing to the guest, so Restart can refuse before stopping it.
func (m *MicrosandboxRuntime) preflightProjectSkills(ctx context.Context) (guestSkillInventory, map[string]SkillPackage, error) {
	if !m.cfg.ProjectSkillsManaged {
		return guestSkillInventory{}, nil, nil
	}
	if err := validateSkillIDs(m.cfg.ProjectSkillIDs, nil); err != nil {
		return guestSkillInventory{}, nil, fmt.Errorf("sélection de skills du projet invalide : %w", err)
	}
	packages, err := skillPackagesBySelection(m.cfg.ProjectSkillIDs, m.cfg.ProjectSkills)
	if err != nil {
		return guestSkillInventory{}, nil, err
	}
	inventory, err := m.readGuestSkillInventory(ctx)
	if err != nil {
		return guestSkillInventory{}, nil, err
	}
	if err := ensureGuestSkillSelectionCompatible(inventory, packages); err != nil {
		return guestSkillInventory{}, nil, err
	}
	if m.cfg.ProjectSkillsLocalOnly {
		if err := m.preflightLocalOnlySkillInstructions(ctx); err != nil {
			return guestSkillInventory{}, nil, err
		}
	}
	if err := m.preflightGuestSkills(ctx, inventory, packages); err != nil {
		return guestSkillInventory{}, nil, err
	}
	return inventory, packages, nil
}

func (m *MicrosandboxRuntime) preflightLocalOnlySkillInstructions(ctx context.Context) error {
	stdout, stderr, code, err := m.execCaptureWithGuestAgentRetry(ctx, readGuestManagedInstructionsScript())
	if err != nil {
		return fmt.Errorf("inspection des instructions de skills gérés dans la machine isolée : %w", err)
	}
	if code != 0 {
		return fmt.Errorf("l'inspection des instructions de skills gérés dans la machine isolée s'est terminée avec le code %d : %s", code, strings.TrimSpace(stderr))
	}
	if len(stdout) > 1<<20 {
		return fmt.Errorf("AGENTS.md de la machine isolée dépasse 1 Mio ; refus d'inspecter les instructions de skills gérés")
	}
	zone, err := managedSkillZone(stdout)
	if err != nil {
		return fmt.Errorf("inspection des instructions de skills gérés dans la machine isolée : %w", err)
	}
	// The standing guidance alone is not a pin: only leftover pinned skills
	// mean versioned instructions survived the switch to host-local skills.
	if managedZoneHasSkillPins(zone) {
		return fmt.Errorf("l'espace de travail de la machine isolée contient encore des instructions de skills gérés versionnées, mais ce projet conserve désormais les skills localement sur le poste ; " +
			"exportez d'abord les modifications locales de la machine isolée avec 'codebox workspace export', puis lancez 'codebox workspace sync' pendant que la machine isolée tourne. " +
			"Si l'AGENTS.md du poste est absent ou exclu de la synchronisation, recréez avec 'codebox recreate'")
	}
	return nil
}

func readGuestManagedInstructionsScript() string {
	target := shellQuote(msbGuestWorkspace + "/AGENTS.md")
	return "set -eu\n" +
		"target=" + target + "\n" +
		"if [ -L \"$target\" ]; then echo 'AGENTS.md est un lien symbolique ; refus d’inspecter les instructions gérées' >&2; exit 1; fi\n" +
		"if [ -e \"$target\" ] && [ ! -f \"$target\" ]; then echo 'AGENTS.md n’est pas un fichier ordinaire ; refus d’inspecter les instructions gérées' >&2; exit 1; fi\n" +
		"if [ -f \"$target\" ]; then cat -- \"$target\"; fi\n"
}

func guestSkillInventoryMatches(current guestSkillInventory, desired []guestSkillEntry) bool {
	if current.SchemaVersion != guestSkillInventorySchema || len(current.Skills) != len(desired) {
		return false
	}
	for i := range desired {
		if current.Skills[i] != desired[i] {
			return false
		}
	}
	return true
}

func ensureGuestSkillSelectionCompatible(old guestSkillInventory, desired map[string]SkillPackage) error {
	if len(old.Skills) == 0 {
		return nil
	}
	if len(old.Skills) != len(desired) {
		return fmt.Errorf("la sélection des skills du projet a changé ; lancez 'codebox recreate' après avoir exporté les modifications de la machine isolée")
	}
	for _, installed := range old.Skills {
		pkg, ok := desired[installed.ID]
		if !ok || pkg.Name != installed.Name || pkg.Lock.Revision != installed.Revision || pkg.Lock.SHA256 != installed.SHA256 {
			return fmt.Errorf("la sélection des skills du projet a changé ; lancez 'codebox recreate' après avoir exporté les modifications de la machine isolée")
		}
	}
	return nil
}

func skillPackagesBySelection(ids []string, packages []SkillPackage) (map[string]SkillPackage, error) {
	if len(ids) > maxProjectSkills || len(packages) != len(ids) {
		return nil, fmt.Errorf("la sélection des skills du projet et les archives résolues ne correspondent pas")
	}
	byID := make(map[string]SkillPackage, len(packages))
	names := make(map[string]string, len(packages))
	var totalBytes int64
	for _, pkg := range packages {
		if !skillNamePattern.MatchString(pkg.Name) || pkg.ID == "" || !isGitRevision(pkg.Lock.Revision) ||
			pkg.Lock.Repository != projectSkillsRepository || !isSHA256(pkg.Lock.SHA256) {
			return nil, fmt.Errorf("skill de projet résolu invalide %q", pkg.ID)
		}
		if _, duplicate := byID[pkg.ID]; duplicate {
			return nil, fmt.Errorf("skill de projet résolu en double %q", pkg.ID)
		}
		if previous, duplicate := names[pkg.Name]; duplicate {
			return nil, fmt.Errorf("les skills %s et %s partagent le nom d'installation %q dans la machine isolée", previous, pkg.ID, pkg.Name)
		}
		digest := sha256.Sum256(pkg.Archive)
		if hex.EncodeToString(digest[:]) != pkg.Lock.SHA256 {
			return nil, fmt.Errorf("le skill de projet résolu %s a échoué à sa vérification d'intégrité SHA-256", pkg.ID)
		}
		totalBytes += int64(len(pkg.Archive))
		if totalBytes > maxSkillSetBytes {
			return nil, fmt.Errorf("les skills sélectionnés dépassent la limite de charge utile de %d octets pour la machine isolée", maxSkillSetBytes)
		}
		if err := validateNormalizedSkillArchive(pkg.Archive, pkg.Name); err != nil {
			return nil, fmt.Errorf("le skill de projet résolu %s a une archive invalide : %w", pkg.ID, err)
		}
		byID[pkg.ID] = pkg
		names[pkg.Name] = pkg.ID
	}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("le skill de projet %s n'a aucune archive résolue", id)
		}
	}
	return byID, nil
}

func (m *MicrosandboxRuntime) readGuestSkillInventory(ctx context.Context) (guestSkillInventory, error) {
	stdout, stderr, code, err := m.execCaptureWithGuestAgentRetry(ctx, readGuestSkillInventoryScript(m.cfg.EffectiveHarness()))
	if err != nil {
		return guestSkillInventory{}, fmt.Errorf("lecture de l'inventaire des skills de la machine isolée : %w", err)
	}
	if code != 0 {
		return guestSkillInventory{}, fmt.Errorf("la lecture de l'inventaire des skills de la machine isolée s'est terminée avec le code %d : %s", code, strings.TrimSpace(stderr))
	}
	if len(stdout) > 64<<10 {
		return guestSkillInventory{}, fmt.Errorf("l'inventaire des skills de la machine isolée dépasse 64 Kio")
	}
	var inventory guestSkillInventory
	if err := json.Unmarshal([]byte(stdout), &inventory); err != nil {
		return inventory, fmt.Errorf("inventaire des skills de la machine isolée invalide : %w", err)
	}
	if inventory.SchemaVersion != guestSkillInventorySchema || len(inventory.Skills) > 64 {
		return inventory, fmt.Errorf("inventaire des skills de la machine isolée non pris en charge ou trop volumineux")
	}
	seenIDs, seenNames := map[string]bool{}, map[string]bool{}
	for _, item := range inventory.Skills {
		if err := validateSkillIDs([]string{item.ID}, nil); err != nil {
			return inventory, fmt.Errorf("inventaire des skills de la machine isolée invalide : %w", err)
		}
		name := skillNameFromID(item.ID)
		if item.Name != name || !isGitRevision(item.Revision) || !isSHA256(item.SHA256) || seenIDs[item.ID] || seenNames[item.Name] {
			return inventory, fmt.Errorf("entrée d'inventaire des skills de la machine isolée invalide ou en double %q", item.ID)
		}
		seenIDs[item.ID], seenNames[item.Name] = true, true
	}
	return inventory, nil
}

func (m *MicrosandboxRuntime) execCaptureWithGuestAgentRetry(ctx context.Context, script string) (stdout, stderr string, code int, err error) {
	delay := m.launchRetryDelay
	if delay == 0 {
		delay = msbLaunchRetryDelay
	}
	for attempt := 1; attempt <= msbLaunchAttempts; attempt++ {
		stdout, stderr, code, err = m.Client.ExecCapture(ctx, m.InstanceName(), script)
		if err == nil || attempt == msbLaunchAttempts {
			return stdout, stderr, code, err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return "", "", 0, ctx.Err()
		}
	}
	return stdout, stderr, code, err
}

func readGuestSkillInventoryScript(harness Harness) string {
	// The OpenCode path goes through config_home so the documented
	// XDG_CONFIG_HOME fallback applies; Pi's configuration directory is not an
	// XDG path and is anchored on the guest home.
	skills := `"$config_home/opencode/skills"`
	if harness == HarnessPi {
		skills = `"$HOME/.pi/agent/skills"`
	}
	return "set -eu\n" +
		"config_home=${XDG_CONFIG_HOME:-\"$HOME/.config\"}\n" +
		"case \"$config_home\" in /*) ;; *) echo 'XDG_CONFIG_HOME doit être un chemin absolu' >&2; exit 1 ;; esac\n" +
		"state=\"$config_home/codebox\"\n" +
		"index=\"$state/project-skills.json\"\n" +
		"skills=" + skills + "\n" +
		"if [ -L \"$config_home\" ] || [ -L \"$state\" ] || [ -L \"$index\" ]; then echo 'lien symbolique refusé dans le chemin d’état des skills gérés' >&2; exit 1; fi\n" +
		"if [ -e \"$state\" ] && [ ! -d \"$state\" ]; then echo 'le chemin d’état des skills gérés n’est pas un répertoire' >&2; exit 1; fi\n" +
		"if [ -e \"$index\" ] && [ ! -f \"$index\" ]; then echo 'l’inventaire des skills gérés n’est pas un fichier ordinaire' >&2; exit 1; fi\n" +
		"if [ -f \"$index\" ]; then cat -- \"$index\"; else\n" +
		"  for marker in \"$skills\"/*/.codebox-source; do\n" +
		"    if [ -e \"$marker\" ] || [ -L \"$marker\" ]; then echo 'un répertoire de skills gérés existe sans son inventaire de skills du projet' >&2; exit 1; fi\n" +
		"  done\n" +
		"  printf '%s\\n' '{\"schemaVersion\":1,\"skills\":[]}'\n" +
		"fi\n"
}

func skillNameFromID(id string) string {
	name := strings.TrimPrefix(id, "official/")
	return strings.TrimPrefix(name, "experimental/")
}

// preflightGuestSkills refuses all destructive reconciliation unless every
// existing codebox-owned directory matches its pinned digest and inventory.
func (m *MicrosandboxRuntime) preflightGuestSkills(ctx context.Context, old guestSkillInventory, desired map[string]SkillPackage) error {
	allowed := make(map[string]map[string]bool)
	for _, item := range old.Skills {
		marker := item.Revision + ":" + item.SHA256
		allowed[item.Name] = map[string]bool{marker: true}
	}
	for _, pkg := range desired {
		if allowed[pkg.Name] == nil {
			allowed[pkg.Name] = map[string]bool{}
		}
		allowed[pkg.Name][pkg.Lock.Revision+":"+pkg.Lock.SHA256] = true
	}
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(m.cfg.EffectiveHarness()))
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	b.WriteString(guestSkillInventoryOrphanCheckScript(names))
	for _, name := range names {
		markers := allowed[name]
		fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", name)
		b.WriteString("if [ -L \"$dest\" ]; then echo 'lien symbolique refusé à un chemin de skill géré' >&2; exit 1; fi\n")
		b.WriteString("if [ -e \"$dest\" ]; then\n")
		b.WriteString("  if [ ! -d \"$dest\" ] || [ ! -f \"$dest/.codebox-source\" ]; then echo 'le chemin de skill géré entre en conflit avec des données utilisateur' >&2; exit 1; fi\n")
		b.WriteString("  marker=$(cat -- \"$dest/.codebox-source\")\n  case \"$marker\" in ")
		first := true
		markerValues := make([]string, 0, len(markers))
		for marker := range markers {
			markerValues = append(markerValues, marker)
		}
		sort.Strings(markerValues)
		for _, marker := range markerValues {
			if !first {
				b.WriteByte('|')
			}
			first = false
			b.WriteString(shellQuote(marker))
		}
		b.WriteString(") ;; *) echo 'l’épinglage du skill géré diffère de la sélection approuvée' >&2; exit 1 ;; esac\n")
		b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
		b.WriteString("fi\n")
	}
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("pré-vérification des changements de skills de la machine isolée ; aucun skill géré n'a été supprimé : %w", err)
	}
	return nil
}

func guestSkillInventoryOrphanCheckScript(allowedNames []string) string {
	var b strings.Builder
	b.WriteString("for dest in \"$parent\"/*; do\n")
	b.WriteString("  if [ ! -e \"$dest\" ] && [ ! -L \"$dest\" ]; then continue; fi\n")
	b.WriteString("  marker=\"$dest/.codebox-source\"\n")
	b.WriteString("  if [ -e \"$marker\" ] || [ -L \"$marker\" ]; then\n")
	b.WriteString("    name=${dest##*/}\n")
	if len(allowedNames) == 0 {
		b.WriteString("    echo 'un répertoire de skills gérés est absent de l’inventaire de skills du projet' >&2; exit 1\n")
	} else {
		b.WriteString("    case \"$name\" in ")
		for i, name := range allowedNames {
			if i > 0 {
				b.WriteByte('|')
			}
			b.WriteString(name)
		}
		b.WriteString(") ;; *) echo 'un répertoire de skills gérés est absent de l’inventaire de skills du projet' >&2; exit 1 ;; esac\n")
	}
	b.WriteString("  fi\ndone\n")
	return b.String()
}

// guestSkillPathsPrelude returns the shared shell prelude for the managed
// skill scripts. It defines two variables every caller uses: `config_home`,
// which holds codebox's own state, and `parent`, the harness-specific
// directory the managed skills are installed into.
//
// The two harnesses read skills from different places — OpenCode from its
// config directory, Pi from ~/.pi/agent/skills — and both are outside the
// project, so the guest workspace never gains files the user did not write.
// A symlink anywhere on the path is refused: the skills are written by
// codebox and read by the agent, and a repointed path would let a project
// redirect that write.
func guestSkillPathsPrelude(harness Harness) string {
	common := "set -eu\n" +
		"config_home=${XDG_CONFIG_HOME:-\"$HOME/.config\"}\n" +
		"case \"$config_home\" in /*) ;; *) echo 'XDG_CONFIG_HOME doit être un chemin absolu' >&2; exit 1 ;; esac\n"
	if harness == HarnessPi {
		return common +
			"if [ -z \"${HOME:-}\" ]; then echo 'HOME n’est pas défini dans la machine isolée' >&2; exit 1; fi\n" +
			"if [ -L \"$HOME/.pi\" ] || [ -L \"$HOME/.pi/agent\" ] || [ -L \"$HOME/.pi/agent/skills\" ]; then echo 'lien symbolique refusé dans le chemin des skills de la machine isolée' >&2; exit 1; fi\n" +
			"parent=\"$HOME/.pi/agent/skills\"\n"
	}
	return common +
		"if [ -L \"$config_home/opencode\" ] || [ -L \"$config_home/opencode/skills\" ]; then echo 'lien symbolique refusé dans le chemin des skills de la machine isolée' >&2; exit 1; fi\n" +
		"parent=\"$config_home/opencode/skills\"\n"
}

func guestSkillDirectoryIntegrityScript(dest string) string {
	return "  if [ -L \"" + dest + "/.codebox-checksums\" ] || [ ! -f \"" + dest + "/.codebox-checksums\" ]; then echo 'le fichier d’intégrité des skills gérés est absent ou est un lien symbolique' >&2; exit 1; fi\n" +
		"  if find \"" + dest + "\" ! -type f ! -type d | grep -q .; then echo 'un skill géré contient des entrées de système de fichiers non prises en charge' >&2; exit 1; fi\n" +
		"  expected=$(($(wc -l < \"" + dest + "/.codebox-checksums\") + 2))\n" +
		"  actual=$(find \"" + dest + "\" ! -type d | wc -l | tr -d '[:space:]')\n" +
		"  if [ \"$actual\" != \"$expected\" ] || ! (cd \"" + dest + "\" && sha256sum -c .codebox-checksums >/dev/null 2>&1); then echo 'le contenu d’un skill géré a été modifié ; il est préservé et la réconciliation est refusée' >&2; exit 1; fi\n"
}

func (m *MicrosandboxRuntime) installOneProjectSkill(ctx context.Context, skill SkillPackage, archivePath string) error {
	marker := skill.Lock.Revision + ":" + skill.Lock.SHA256
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(m.cfg.EffectiveHarness()))
	fmt.Fprintf(&b, "archive=%s\n", shellQuote(archivePath))
	fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", skill.Name)
	fmt.Fprintf(&b, "marker=%s\n", shellQuote(marker))
	b.WriteString("if [ -L \"$dest\" ]; then echo 'lien symbolique refusé au chemin de skill géré' >&2; exit 1; fi\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  if [ ! -d \"$dest\" ] || [ ! -f \"$dest/.codebox-source\" ]; then echo 'le chemin de skill géré entre en conflit avec des données utilisateur ; refus de l’écraser' >&2; exit 1; fi\n")
	b.WriteString("  if [ \"$(cat -- \"$dest/.codebox-source\")\" != \"$marker\" ]; then echo 'l’épinglage du skill géré a changé ; recréez la machine isolée après avoir exporté les modifications de la machine isolée' >&2; exit 1; fi\n")
	b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
	b.WriteString("  rm -f -- \"$archive\"; exit 0\nfi\n")
	b.WriteString("if ! (cd \"$(dirname \"$archive\")\" && printf '%s  %s\\n' ")
	fmt.Fprintf(&b, "%s %s | sha256sum -c - >/dev/null); then echo 'l’archive du skill a changé pendant le transfert' >&2; exit 1; fi\n", shellQuote(skill.Lock.SHA256), shellQuote(archivePath))
	b.WriteString("mkdir -p -- \"$parent\" \"$config_home/codebox/skill-staging\"\n")
	fmt.Fprintf(&b, "stage=$(mktemp -d \"$config_home/codebox/skill-staging/%s.XXXXXX\")\n", skill.Name)
	b.WriteString("mkdir -- \"$stage/new\"\n")
	b.WriteString("cleanup() { rm -rf -- \"$stage\"; rm -f -- \"$archive\"; }\n")
	b.WriteString("trap cleanup EXIT HUP INT TERM\n")
	b.WriteString("tar -xzf \"$archive\" -C \"$stage/new\"\n")
	b.WriteString("printf '%s\\n' \"$marker\" > \"$stage/new/.codebox-source\"\n")
	b.WriteString(guestSkillDirectoryIntegrityScript("$stage/new"))
	b.WriteString("if [ -e \"$dest\" ]; then echo 'un chemin de skill géré est apparu pendant l’installation ; refus de l’écraser' >&2; exit 1; fi\n")
	b.WriteString("mv -- \"$stage/new\" \"$dest\"\n")
	b.WriteString("rm -f -- \"$archive\"\ntrap - EXIT HUP INT TERM\nrmdir -- \"$stage\"\n")
	return m.guestShell(ctx, b.String())
}

func (m *MicrosandboxRuntime) commitGuestSkillInventory(ctx context.Context, desired []guestSkillEntry, indexArchive string) error {
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(m.cfg.EffectiveHarness()))
	b.WriteString("index=\"$config_home/codebox/project-skills.json\"\n")
	b.WriteString("mkdir -p -- \"$config_home/codebox\"\n")
	b.WriteString("if [ -L \"$index\" ]; then echo 'lien symbolique refusé à l’inventaire des skills de la machine isolée' >&2; exit 1; fi\n")
	fmt.Fprintf(&b, "mv -- %s \"$index\"\nchmod 600 \"$index\"\n", shellQuote(indexArchive))
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("mise à jour de l'inventaire des skills de la machine isolée : %w", err)
	}
	return nil
}

func (m *MicrosandboxRuntime) verifyProjectSkills(ctx context.Context) (err error) {
	if len(m.cfg.ProjectSkills) == 0 {
		return nil
	}
	// Pi exposes no skill-listing surface to query from the host, so its
	// verification is at the filesystem level: the managed directories must be
	// there, which is what codebox delivered. OpenCode is asked what it
	// actually discovered, which is the stronger check and stays as it was.
	if m.cfg.EffectiveHarness() == HarnessPi {
		return m.verifyPiProjectSkills(ctx)
	}
	if m.cfg.Isolation == IsolationFull {
		// Full isolation normally leaves no OpenCode service running. Reuse the
		// persistent service for cold-start retries, but stop it only when it was
		// stopped on entry; a pre-existing service is not verification-owned.
		stopped, statusErr := m.openCodeServiceStoppedForVerification(ctx)
		if statusErr != nil {
			return statusErr
		}
		if stopped {
			defer func() {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), openCodeServiceStopTimeout)
				defer cancel()
				err = errors.Join(err, m.stopOpenCodeServiceForVerification(cleanupCtx))
			}()
		}
	}
	expected := make(map[string]bool, len(m.cfg.ProjectSkills))
	for _, skill := range m.cfg.ProjectSkills {
		expected[skill.Name] = true
	}
	// Right after a cold start, the persistent OpenCode 2.2.5 service can answer
	// /api/skill before its skill index is ready (upstream anomalyco/opencode
	// #57524). That incomplete result is a startup race, not a failed install, so
	// the same service is queried again with the bounded guest-agent backoff when
	// a pinned skill is reported missing. A genuine command error, a malformed
	// response, or a duplicated report stays a hard failure: none of those resolve
	// by waiting.
	delay := m.launchRetryDelay
	if delay == 0 {
		delay = msbLaunchRetryDelay
	}
	var problem error
	for attempt := 1; attempt <= msbLaunchAttempts; attempt++ {
		missing, returned, err := m.queryOpenCodeSkillDiscovery(ctx, expected)
		if err != nil {
			return err
		}
		// A skill reported more than once is a real response anomaly, not a
		// cold-start race; surface it immediately rather than retrying.
		if dup := firstDuplicate(expected, returned); dup != "" {
			return fmt.Errorf("vérification de la découverte des skills dans la machine isolée : OpenCode signale le skill épinglé %q plus d'une fois", dup)
		}
		if len(missing) == 0 {
			return nil
		}
		problem = fmt.Errorf("vérification de la découverte des skills dans la machine isolée : OpenCode n'a pas découvert le skill épinglé %q", missing[0])
		if attempt == msbLaunchAttempts {
			break
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return problem
}

func (m *MicrosandboxRuntime) openCodeServiceStoppedForVerification(ctx context.Context) (bool, error) {
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), "opencode service status")
	if err != nil {
		return false, fmt.Errorf("vérification de la découverte des skills : inspection de l'état du service OpenCode : %w", err)
	}
	if code != 0 {
		return false, fmt.Errorf("vérification de la découverte des skills : 'opencode service status' s'est terminé avec le code %d : %s", code, strings.TrimSpace(stderr))
	}
	status := strings.TrimSpace(stdout)
	if status == "stopped" {
		return true, nil
	}
	if strings.HasPrefix(status, "http://127.0.0.1:") {
		return false, nil
	}
	return false, fmt.Errorf("vérification de la découverte des skills : état du service OpenCode non reconnu %q", status)
}

func (m *MicrosandboxRuntime) stopOpenCodeServiceForVerification(ctx context.Context) error {
	_, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), "opencode service stop")
	if err != nil {
		return fmt.Errorf("vérification de la découverte des skills : arrêt du service OpenCode utilisé pour la vérification : %w", err)
	}
	if code != 0 {
		return fmt.Errorf("vérification de la découverte des skills : 'opencode service stop' s'est terminé avec le code %d : %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// queryOpenCodeSkillDiscovery asks the persistent OpenCode service what skills
// it discovered and returns the pinned skills that were not reported (missing)
// and the full set of reported names (returned). Command, exit and JSON errors
// are returned as hard errors.
func (m *MicrosandboxRuntime) queryOpenCodeSkillDiscovery(ctx context.Context, expected map[string]bool) (missing, returned []string, err error) {
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), "opencode api GET /api/skill")
	if err != nil {
		return nil, nil, fmt.Errorf("vérification de la découverte des skills dans la machine isolée : %w", err)
	}
	if code != 0 {
		return nil, nil, fmt.Errorf("vérification de la découverte des skills : 'opencode api GET /api/skill' s'est terminé avec le code %d : %s", code, strings.TrimSpace(stderr))
	}
	var response struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		return nil, nil, fmt.Errorf("vérification de la découverte des skills : réponse OpenCode invalide : %w", err)
	}
	seen := make(map[string]int, len(expected))
	for _, item := range response.Data {
		if expected[item.Name] {
			seen[item.Name]++
			returned = append(returned, item.Name)
		}
	}
	for _, skill := range m.cfg.ProjectSkills {
		if seen[skill.Name] == 0 {
			missing = append(missing, skill.Name)
		}
	}
	return missing, returned, nil
}

func firstDuplicate(expected map[string]bool, returned []string) string {
	seen := make(map[string]bool, len(returned))
	for _, name := range returned {
		if !expected[name] {
			continue
		}
		if seen[name] {
			return name
		}
		seen[name] = true
	}
	return ""
}

// verifyPiProjectSkills checks that every pinned skill is present in the
// directory Pi reads. Each managed skill directory carries its own
// .codebox-source marker and a verified checksum list, so the check reads
// codebox's own bookkeeping rather than trusting a listing.
func (m *MicrosandboxRuntime) verifyPiProjectSkills(ctx context.Context) error {
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(HarnessPi))
	for _, skill := range m.cfg.ProjectSkills {
		fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", skill.Name)
		b.WriteString("if [ ! -d \"$dest\" ]; then echo \"le répertoire de skill géré est absent\" >&2; exit 1; fi\n")
		b.WriteString("if [ ! -f \"$dest/.codebox-source\" ]; then echo \"le marqueur de skill géré est absent\" >&2; exit 1; fi\n")
		b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
	}
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("vérification des skills épinglés dans la machine isolée : %w", err)
	}
	return nil
}
