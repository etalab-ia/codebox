package codebox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const guestSkillInventorySchema = 1

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
			return fmt.Errorf("stage project skill inventory in the guest: %w", err)
		}
		if err := m.commitGuestSkillInventory(ctx, entries, indexArchive); err != nil {
			return err
		}
	}

	for _, id := range m.cfg.ProjectSkillIDs {
		skill := packages[id]
		archivePath := "/tmp/codebox-skill-" + skill.Name + "-" + skill.Lock.SHA256 + ".tar.gz"
		if err := m.Client.WriteFile(ctx, m.InstanceName(), archivePath, skill.Archive); err != nil {
			return fmt.Errorf("copy skill %s into the guest: %w", skill.ID, err)
		}
		if err := m.installOneProjectSkill(ctx, skill, archivePath); err != nil {
			return fmt.Errorf("install pinned skill %s in the guest: %w", skill.ID, err)
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
		return guestSkillInventory{}, nil, fmt.Errorf("invalid project skill selection: %w", err)
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
		return fmt.Errorf("inspect guest managed skill instructions: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("inspect guest managed skill instructions exited %d: %s", code, strings.TrimSpace(stderr))
	}
	if len(stdout) > 1<<20 {
		return fmt.Errorf("guest AGENTS.md exceeds 1 MiB; refusing to inspect managed skill instructions")
	}
	zone, err := managedSkillZone(stdout)
	if err != nil {
		return fmt.Errorf("inspect guest managed skill instructions: %w", err)
	}
	// The standing guidance alone is not a pin: only leftover pinned skills
	// mean versioned instructions survived the switch to host-local skills.
	if managedZoneHasSkillPins(zone) {
		return fmt.Errorf("the guest workspace still contains versioned managed skill instructions, but this project now keeps skills host-local; " +
			"export guest-local changes first with 'codebox workspace export', then run 'codebox workspace sync' while the guest is running. " +
			"If the host AGENTS.md is missing or excluded from sync, recreate with 'codebox recreate'")
	}
	return nil
}

func readGuestManagedInstructionsScript() string {
	target := shellQuote(msbGuestWorkspace + "/AGENTS.md")
	return "set -eu\n" +
		"target=" + target + "\n" +
		"if [ -L \"$target\" ]; then echo 'AGENTS.md is a symlink; refusing to inspect managed instructions' >&2; exit 1; fi\n" +
		"if [ -e \"$target\" ] && [ ! -f \"$target\" ]; then echo 'AGENTS.md is not a regular file; refusing to inspect managed instructions' >&2; exit 1; fi\n" +
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
		return fmt.Errorf("project skill selection changed; run 'codebox recreate' after exporting guest changes")
	}
	for _, installed := range old.Skills {
		pkg, ok := desired[installed.ID]
		if !ok || pkg.Name != installed.Name || pkg.Lock.Revision != installed.Revision || pkg.Lock.SHA256 != installed.SHA256 {
			return fmt.Errorf("project skill selection changed; run 'codebox recreate' after exporting guest changes")
		}
	}
	return nil
}

func skillPackagesBySelection(ids []string, packages []SkillPackage) (map[string]SkillPackage, error) {
	if len(ids) > maxProjectSkills || len(packages) != len(ids) {
		return nil, fmt.Errorf("project skill selection and resolved archives do not match")
	}
	byID := make(map[string]SkillPackage, len(packages))
	names := make(map[string]string, len(packages))
	var totalBytes int64
	for _, pkg := range packages {
		if !skillNamePattern.MatchString(pkg.Name) || pkg.ID == "" || !isGitRevision(pkg.Lock.Revision) ||
			pkg.Lock.Repository != projectSkillsRepository || !isSHA256(pkg.Lock.SHA256) {
			return nil, fmt.Errorf("invalid resolved project skill %q", pkg.ID)
		}
		if _, duplicate := byID[pkg.ID]; duplicate {
			return nil, fmt.Errorf("duplicate resolved project skill %q", pkg.ID)
		}
		if previous, duplicate := names[pkg.Name]; duplicate {
			return nil, fmt.Errorf("skills %s and %s share the guest install name %q", previous, pkg.ID, pkg.Name)
		}
		digest := sha256.Sum256(pkg.Archive)
		if hex.EncodeToString(digest[:]) != pkg.Lock.SHA256 {
			return nil, fmt.Errorf("resolved project skill %s failed its SHA-256 integrity check", pkg.ID)
		}
		totalBytes += int64(len(pkg.Archive))
		if totalBytes > maxSkillSetBytes {
			return nil, fmt.Errorf("selected skills exceed the %d-byte guest payload limit", maxSkillSetBytes)
		}
		if err := validateNormalizedSkillArchive(pkg.Archive, pkg.Name); err != nil {
			return nil, fmt.Errorf("resolved project skill %s has an invalid archive: %w", pkg.ID, err)
		}
		byID[pkg.ID] = pkg
		names[pkg.Name] = pkg.ID
	}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("project skill %s has no resolved archive", id)
		}
	}
	return byID, nil
}

func (m *MicrosandboxRuntime) readGuestSkillInventory(ctx context.Context) (guestSkillInventory, error) {
	stdout, stderr, code, err := m.execCaptureWithGuestAgentRetry(ctx, readGuestSkillInventoryScript(m.cfg.EffectiveHarness()))
	if err != nil {
		return guestSkillInventory{}, fmt.Errorf("read guest skill inventory: %w", err)
	}
	if code != 0 {
		return guestSkillInventory{}, fmt.Errorf("read guest skill inventory exited %d: %s", code, strings.TrimSpace(stderr))
	}
	if len(stdout) > 64<<10 {
		return guestSkillInventory{}, fmt.Errorf("guest skill inventory exceeds 64 KiB")
	}
	var inventory guestSkillInventory
	if err := json.Unmarshal([]byte(stdout), &inventory); err != nil {
		return inventory, fmt.Errorf("invalid guest skill inventory: %w", err)
	}
	if inventory.SchemaVersion != guestSkillInventorySchema || len(inventory.Skills) > 64 {
		return inventory, fmt.Errorf("unsupported or oversized guest skill inventory")
	}
	seenIDs, seenNames := map[string]bool{}, map[string]bool{}
	for _, item := range inventory.Skills {
		if err := validateSkillIDs([]string{item.ID}, nil); err != nil {
			return inventory, fmt.Errorf("invalid guest skill inventory: %w", err)
		}
		name := skillNameFromID(item.ID)
		if item.Name != name || !isGitRevision(item.Revision) || !isSHA256(item.SHA256) || seenIDs[item.ID] || seenNames[item.Name] {
			return inventory, fmt.Errorf("invalid or duplicate guest skill inventory entry %q", item.ID)
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
		"case \"$config_home\" in /*) ;; *) echo 'XDG_CONFIG_HOME must be absolute' >&2; exit 1 ;; esac\n" +
		"state=\"$config_home/codebox\"\n" +
		"index=\"$state/project-skills.json\"\n" +
		"skills=" + skills + "\n" +
		"if [ -L \"$config_home\" ] || [ -L \"$state\" ] || [ -L \"$index\" ]; then echo 'refusing a symlink in the managed skill state path' >&2; exit 1; fi\n" +
		"if [ -e \"$state\" ] && [ ! -d \"$state\" ]; then echo 'managed skill state path is not a directory' >&2; exit 1; fi\n" +
		"if [ -e \"$index\" ] && [ ! -f \"$index\" ]; then echo 'managed skill inventory is not a regular file' >&2; exit 1; fi\n" +
		"if [ -f \"$index\" ]; then cat -- \"$index\"; else\n" +
		"  for marker in \"$skills\"/*/.codebox-source; do\n" +
		"    if [ -e \"$marker\" ] || [ -L \"$marker\" ]; then echo 'managed skill directory exists without its project-skill inventory' >&2; exit 1; fi\n" +
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
		b.WriteString("if [ -L \"$dest\" ]; then echo 'refusing a symlink at a managed skill path' >&2; exit 1; fi\n")
		b.WriteString("if [ -e \"$dest\" ]; then\n")
		b.WriteString("  if [ ! -d \"$dest\" ] || [ ! -f \"$dest/.codebox-source\" ]; then echo 'managed skill path conflicts with user data' >&2; exit 1; fi\n")
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
		b.WriteString(") ;; *) echo 'managed skill pin differs from the approved selection' >&2; exit 1 ;; esac\n")
		b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
		b.WriteString("fi\n")
	}
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("preflight guest skill changes; no managed skill was removed: %w", err)
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
		b.WriteString("    echo 'managed skill directory is missing from the project-skill inventory' >&2; exit 1\n")
	} else {
		b.WriteString("    case \"$name\" in ")
		for i, name := range allowedNames {
			if i > 0 {
				b.WriteByte('|')
			}
			b.WriteString(name)
		}
		b.WriteString(") ;; *) echo 'managed skill directory is missing from the project-skill inventory' >&2; exit 1 ;; esac\n")
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
		"case \"$config_home\" in /*) ;; *) echo 'XDG_CONFIG_HOME must be absolute' >&2; exit 1 ;; esac\n"
	if harness == HarnessPi {
		return common +
			"if [ -z \"${HOME:-}\" ]; then echo 'HOME is not set in the guest' >&2; exit 1; fi\n" +
			"if [ -L \"$HOME/.pi\" ] || [ -L \"$HOME/.pi/agent\" ] || [ -L \"$HOME/.pi/agent/skills\" ]; then echo 'refusing a symlink in the guest skill path' >&2; exit 1; fi\n" +
			"parent=\"$HOME/.pi/agent/skills\"\n"
	}
	return common +
		"if [ -L \"$config_home/opencode\" ] || [ -L \"$config_home/opencode/skills\" ]; then echo 'refusing a symlink in the guest skill path' >&2; exit 1; fi\n" +
		"parent=\"$config_home/opencode/skills\"\n"
}

func guestSkillDirectoryIntegrityScript(dest string) string {
	return "  if [ -L \"" + dest + "/.codebox-checksums\" ] || [ ! -f \"" + dest + "/.codebox-checksums\" ]; then echo 'managed skill integrity file is missing or a symlink' >&2; exit 1; fi\n" +
		"  if find \"" + dest + "\" ! -type f ! -type d | grep -q .; then echo 'managed skill contains unsupported filesystem entries' >&2; exit 1; fi\n" +
		"  expected=$(($(wc -l < \"" + dest + "/.codebox-checksums\") + 2))\n" +
		"  actual=$(find \"" + dest + "\" ! -type d | wc -l | tr -d '[:space:]')\n" +
		"  if [ \"$actual\" != \"$expected\" ] || ! (cd \"" + dest + "\" && sha256sum -c .codebox-checksums >/dev/null 2>&1); then echo 'managed skill content was modified; preserving it and refusing reconciliation' >&2; exit 1; fi\n"
}

func (m *MicrosandboxRuntime) installOneProjectSkill(ctx context.Context, skill SkillPackage, archivePath string) error {
	marker := skill.Lock.Revision + ":" + skill.Lock.SHA256
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(m.cfg.EffectiveHarness()))
	fmt.Fprintf(&b, "archive=%s\n", shellQuote(archivePath))
	fmt.Fprintf(&b, "dest=\"$parent/%s\"\n", skill.Name)
	fmt.Fprintf(&b, "marker=%s\n", shellQuote(marker))
	b.WriteString("if [ -L \"$dest\" ]; then echo 'refusing a symlink at the managed skill path' >&2; exit 1; fi\n")
	b.WriteString("if [ -e \"$dest\" ]; then\n")
	b.WriteString("  if [ ! -d \"$dest\" ] || [ ! -f \"$dest/.codebox-source\" ]; then echo 'managed skill path conflicts with user data; refusing to overwrite it' >&2; exit 1; fi\n")
	b.WriteString("  if [ \"$(cat -- \"$dest/.codebox-source\")\" != \"$marker\" ]; then echo 'managed skill pin changed; recreate the guest after exporting guest changes' >&2; exit 1; fi\n")
	b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
	b.WriteString("  rm -f -- \"$archive\"; exit 0\nfi\n")
	b.WriteString("if ! (cd \"$(dirname \"$archive\")\" && printf '%s  %s\\n' ")
	fmt.Fprintf(&b, "%s %s | sha256sum -c - >/dev/null); then echo 'skill archive changed during transfer' >&2; exit 1; fi\n", shellQuote(skill.Lock.SHA256), shellQuote(archivePath))
	b.WriteString("mkdir -p -- \"$parent\" \"$config_home/codebox/skill-staging\"\n")
	fmt.Fprintf(&b, "stage=$(mktemp -d \"$config_home/codebox/skill-staging/%s.XXXXXX\")\n", skill.Name)
	b.WriteString("mkdir -- \"$stage/new\"\n")
	b.WriteString("cleanup() { rm -rf -- \"$stage\"; rm -f -- \"$archive\"; }\n")
	b.WriteString("trap cleanup EXIT HUP INT TERM\n")
	b.WriteString("tar -xzf \"$archive\" -C \"$stage/new\"\n")
	b.WriteString("printf '%s\\n' \"$marker\" > \"$stage/new/.codebox-source\"\n")
	b.WriteString(guestSkillDirectoryIntegrityScript("$stage/new"))
	b.WriteString("if [ -e \"$dest\" ]; then echo 'managed skill path appeared during installation; refusing to overwrite it' >&2; exit 1; fi\n")
	b.WriteString("mv -- \"$stage/new\" \"$dest\"\n")
	b.WriteString("rm -f -- \"$archive\"\ntrap - EXIT HUP INT TERM\nrmdir -- \"$stage\"\n")
	return m.guestShell(ctx, b.String())
}

func (m *MicrosandboxRuntime) commitGuestSkillInventory(ctx context.Context, desired []guestSkillEntry, indexArchive string) error {
	var b strings.Builder
	b.WriteString(guestSkillPathsPrelude(m.cfg.EffectiveHarness()))
	b.WriteString("index=\"$config_home/codebox/project-skills.json\"\n")
	b.WriteString("mkdir -p -- \"$config_home/codebox\"\n")
	b.WriteString("if [ -L \"$index\" ]; then echo 'refusing a symlink at the guest skill inventory' >&2; exit 1; fi\n")
	fmt.Fprintf(&b, "mv -- %s \"$index\"\nchmod 600 \"$index\"\n", shellQuote(indexArchive))
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("update guest skill inventory: %w", err)
	}
	return nil
}

func (m *MicrosandboxRuntime) verifyProjectSkills(ctx context.Context) error {
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
	expected := make(map[string]bool, len(m.cfg.ProjectSkills))
	for _, skill := range m.cfg.ProjectSkills {
		expected[skill.Name] = true
	}
	// Right after a cold start, OpenCode 2.2.5 can answer /api/skill with an
	// empty list before its skill index is ready (upstream anomalycoco/opencode
	// #61669). That empty result is a startup race, not a failed install, so the
	// query is retried with the bounded guest-agent backoff when a pinned skill
	// is reported missing. A genuine command error, a malformed response, or a
	// duplicated report stays a hard failure: none of those resolve by waiting.
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
			return fmt.Errorf("verify guest skill discovery: OpenCode reports the pinned skill %q more than once", dup)
		}
		if len(missing) == 0 {
			return nil
		}
		problem = fmt.Errorf("verify guest skill discovery: OpenCode did not discover the pinned skill %q", missing[0])
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

// queryOpenCodeSkillDiscovery asks OpenCode what skills it discovered and
// returns the pinned skills that were not reported (missing) and the full set
// of reported names (returned). Command, exit and JSON errors are returned as
// hard errors.
func (m *MicrosandboxRuntime) queryOpenCodeSkillDiscovery(ctx context.Context, expected map[string]bool) (missing, returned []string, err error) {
	stdout, stderr, code, err := m.Client.ExecCapture(ctx, m.InstanceName(), "opencode api --standalone GET /api/skill")
	if err != nil {
		return nil, nil, fmt.Errorf("verify guest skill discovery: %w", err)
	}
	if code != 0 {
		return nil, nil, fmt.Errorf("verify guest skill discovery: opencode api --standalone GET /api/skill exited %d: %s", code, strings.TrimSpace(stderr))
	}
	var response struct {
		Data []struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		return nil, nil, fmt.Errorf("verify guest skill discovery: invalid OpenCode response: %w", err)
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
		b.WriteString("if [ ! -d \"$dest\" ]; then echo \"managed skill directory is missing\" >&2; exit 1; fi\n")
		b.WriteString("if [ ! -f \"$dest/.codebox-source\" ]; then echo \"managed skill marker is missing\" >&2; exit 1; fi\n")
		b.WriteString(guestSkillDirectoryIntegrityScript("$dest"))
	}
	if err := m.guestShell(ctx, b.String()); err != nil {
		return fmt.Errorf("verify the pinned skills in the guest: %w", err)
	}
	return nil
}
