package justcode

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRoot is a Git-less project directory: the wizard's review notes that
// the manifest will not be versioned, which is useful for one test and
// harmless for the others.
func initTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInteractiveInitReviewMatchesAcceptanceText(t *testing.T) {
	root := initTestRoot(t)
	cache := t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	const privateRules = "Private user rules must stay out of the managed-zone diff.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(privateRules), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{
		ResolveSkills: fixtureSkillResolver(t),
		ProbeRemoteMCP: func(context.Context, MCPConnector) error {
			return errors.New("simulated offline endpoint")
		},
	}).Plan(InitAnswers{
		Root: root, Model: "albert/qwen3-coder", GitHubWorkflow: true,
		GitHubRemote: GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"},
		Skills:       []string{"official/securite-developpement", "official/rgaa"}, SkillsSet: true,
		MCPConnectors: []string{"data-gouv", "chrome-devtools"}, MCPsSet: true,
	})
	if err != nil {
		t.Fatalf("an offline MCP must not block the plan: %v", err)
	}
	review := FormatInteractiveInitReview(plan)
	for _, want := range []string{
		"Récapitulatif du projet : " + filepath.Base(root),
		"securite-developpement, rgaa                (avec le projet)",
		"data-gouv → https://mcp.data.gouv.fr/mcp   ⚠️ injoignable",
		"pull requests en brouillon sur owner/repo",
		"Microsandbox, isolation complète, 2 processeurs, 4 Go",
		"chrome-devtools — navigateur dans la machine isolée, pour tester une application web : console, réseau, performances ; installation au premier lancement",
		"L'initialisation ne démarre ni ne redémarre de machine isolée.",
		"Agent        OpenCode, modèle Qwen3 Coder",
		"+ .just-code/project.json",
		"+ .just-code/lock.json",
		"~ AGENTS.md",
		"section just-code ajoutée (voir le détail : d)",
		"La durée dépend de votre connexion.",
		"simulated offline endpoint",
	} {
		if !strings.Contains(review, want) {
			t.Errorf("interactive recap lacks %q:\n%s", want, review)
		}
	}
	for _, unwanted := range []string{"sha256", "managed-zone diff", privateRules, "None selected"} {
		if strings.Contains(review, unwanted) {
			t.Errorf("interactive recap must omit %q:\n%s", unwanted, review)
		}
	}
	if len(plan.MCPChecks) != 1 || !plan.MCPChecks[0].Checked || plan.MCPChecks[0].Reachable {
		t.Fatalf("remote MCP check = %+v, want one checked unreachable endpoint", plan.MCPChecks)
	}
	diff := FormatInitInstructionsDiff(plan)
	if !strings.Contains(diff, "Diff de la section just-code dans AGENTS.md") || !strings.Contains(diff, "official/rgaa") || strings.Contains(diff, privateRules) {
		t.Fatalf("on-demand diff must contain only the managed section:\n%s", diff)
	}
}

func TestInteractiveInitReviewMarksAnExistingEmptyInstructionsFileAsModified(t *testing.T) {
	root := initTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InstructionsExisted || !strings.Contains(FormatInteractiveInitReview(plan), "~ AGENTS.md") {
		t.Fatalf("an existing empty AGENTS.md must be shown as modified, not created:\n%s", FormatInteractiveInitReview(plan))
	}
}

func TestInteractiveInitReviewPreservesExistingConfigAndLocalSkillRevisions(t *testing.T) {
	root := initTestRoot(t)
	if err := WriteProjectManifest(DefaultFS, ProjectManifestPath(root), ProjectManifest{Model: "albert/original", CPUs: 8}); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	review := FormatInteractiveInitReview(plan)
	if !strings.Contains(review, "albert/original") || !strings.Contains(review, "cpus") || !strings.Contains(review, "8") {
		t.Fatalf("replacement recap hides the old configuration:\n%s", review)
	}

	cache := t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	plan, err = (InitWizard{StateDir: t.TempDir(), ResolveSkills: fixtureSkillResolver(t)}).Plan(InitAnswers{
		Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true,
		SkillsLocalOnly: true, SkillsLocalOnlySet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	review = FormatInteractiveInitReview(plan)
	revision := plan.SkillLocks["official/rgaa"].Revision
	if !strings.Contains(review, "official/rgaa : révision "+revision) || strings.Contains(review, "sha256") {
		t.Fatalf("local-only skill revision missing or digest leaked:\n%s", review)
	}
}

func TestInteractiveInitReviewMCPReachabilityAndLocalState(t *testing.T) {
	root := initTestRoot(t)
	cache := t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	probes := 0
	plan, err := (InitWizard{
		StateDir: t.TempDir(), ResolveSkills: fixtureSkillResolver(t),
		ProbeRemoteMCP: func(context.Context, MCPConnector) error { probes++; return nil },
	}).Plan(InitAnswers{
		Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true,
		SkillsLocalOnly: true, SkillsLocalOnlySet: true,
		MCPConnectors: []string{"data-gouv"}, MCPsSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	review := FormatInteractiveInitReview(plan)
	if probes != 1 || !strings.Contains(review, "✅ joignable") {
		t.Fatalf("successful MCP probe = %d calls; review:\n%s", probes, review)
	}
	for _, want := range []string{"rgaa                (sur ce poste)", "non (récupération par just-code workspace apply)", "Fichiers modifiés sur ce poste, hors du projet :", "skills conservés sur ce poste"} {
		if !strings.Contains(review, want) {
			t.Errorf("local-state recap lacks %q:\n%s", want, review)
		}
	}
	if strings.Contains(review, "AGENTS.md") {
		t.Fatalf("a fresh local-only plan must not claim AGENTS.md:\n%s", review)
	}
}

func TestInitPlanDoesNotWriteProjectStateBeforeApply(t *testing.T) {
	root := initTestRoot(t)
	if _, err := (InitWizard{}).Plan(InitAnswers{Root: root}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "main.go" {
		t.Fatalf("Plan wrote project state before confirmation: %v", entries)
	}
}

func TestInitPlanResolvesDefaults(t *testing.T) {
	root := initTestRoot(t)
	plan, err := InitWizard{}.Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Answers.Runtime != RuntimeMicrosandbox {
		t.Fatalf("runtime = %q, want the built-in default", plan.Answers.Runtime)
	}
	if plan.Answers.Isolation != IsolationFull {
		t.Fatalf("isolation = %q, want the built-in default", plan.Answers.Isolation)
	}
	if plan.ManifestPath != ProjectManifestPath(plan.Answers.Root) || plan.LockPath != ProjectLockPath(plan.Answers.Root) {
		t.Fatalf("paths = %q / %q", plan.ManifestPath, plan.LockPath)
	}
	if plan.ExistingManifest != nil {
		t.Fatal("a fresh project must not report an existing manifest")
	}
	// Zero means "use the built-in default", not "zero CPUs".
	if plan.Answers.CPUs != 0 || plan.Answers.MemoryMB != 0 {
		t.Fatalf("sizing = %d / %d, want the unset representation", plan.Answers.CPUs, plan.Answers.MemoryMB)
	}
	review := FormatInitReview(plan)
	if !strings.Contains(review, "2 CPUs, 4G") {
		t.Fatalf("the review must show the effective sizing: %q", review)
	}
}

func TestInitPlanRejectsBadAnswers(t *testing.T) {
	root := initTestRoot(t)
	file := filepath.Join(root, "main.go")
	cases := []struct {
		name    string
		answers InitAnswers
		want    string
	}{
		{"missing root", InitAnswers{}, "root is required"},
		// The message is the OS's, so assert on the part this code owns: the
		// path it could not use. "no such file" is Unix wording; Windows says
		// "The system cannot find the file specified".
		{"nonexistent root", InitAnswers{Root: filepath.Join(root, "nope")}, "nope"},
		{"root is a file", InitAnswers{Root: file}, "not a directory"},
		{"unknown runtime", InitAnswers{Root: root, Runtime: "podman"}, "microsandbox"},
		{"unknown isolation", InitAnswers{Root: root, Isolation: "sometimes"}, "isolation"},
		{"cpus above the SDK range", InitAnswers{Root: root, CPUs: MaxSandboxCPUs + 1}, "CPUs must be"},
		{"negative memory", InitAnswers{Root: root, MemoryMB: -1}, "memory must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (InitWizard{}).Plan(tc.answers); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestInitPlanEnforcesHostResourceCeiling(t *testing.T) {
	root := initTestRoot(t)
	host := HostResources{CPUs: 8, MemoryMB: 16384}
	for _, tc := range []struct {
		name   string
		cpus   int
		memory int
	}{
		{name: "CPU above host capacity", cpus: 9, memory: 4096},
		{name: "memory above host capacity", cpus: 4, memory: 16385},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wizard := InitWizard{HostResources: &host}
			_, err := wizard.Plan(InitAnswers{Root: root, CPUs: tc.cpus, MemoryMB: tc.memory})
			if err == nil || !strings.Contains(err.Error(), "exceeds detected host capacity") {
				t.Fatalf("Plan error = %v, want host capacity rejection", err)
			}
		})
	}
}

func TestInitPlanAddsResourceWarningsToReview(t *testing.T) {
	root := initTestRoot(t)
	host := HostResources{CPUs: 8, MemoryMB: 16384}
	plan, err := (InitWizard{HostResources: &host}).Plan(InitAnswers{Root: root, CPUs: 8, MemoryMB: 13108})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	review := FormatInitReview(plan)
	for _, warning := range []string{"les 8 processeurs logiques du poste", "80.0 % de la mémoire détectée"} {
		if !strings.Contains(review, warning) {
			t.Errorf("review lacks warning %q: %s", warning, review)
		}
	}
}

func TestInitPlanDisplaysHostAdjustedDefaultsWithoutPersistingThem(t *testing.T) {
	root := initTestRoot(t)
	host := HostResources{CPUs: 2, MemoryMB: 4096}
	plan, err := (InitWizard{HostResources: &host}).Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Answers.CPUs != 0 || plan.Answers.MemoryMB != 0 {
		t.Fatalf("host-adjusted defaults were made explicit: %d / %d", plan.Answers.CPUs, plan.Answers.MemoryMB)
	}
	if plan.GuestCPUs != 1 || plan.GuestMemoryMB != 3072 {
		t.Fatalf("effective guest defaults = %d CPUs / %d MiB, want 1 / 3072", plan.GuestCPUs, plan.GuestMemoryMB)
	}
	if !strings.Contains(FormatInitReview(plan), "resources    1 CPUs, 3G") {
		t.Fatalf("review does not show effective host-adjusted defaults: %s", FormatInitReview(plan))
	}
}

func TestInitPlanGitHubWorkflowRequiresCanonicalRemote(t *testing.T) {
	root := initTestRoot(t)
	answers := InitAnswers{
		Root:           root,
		GitHubWorkflow: true,
		GitHubRemote:   GitHubRemote{URL: "https://user:token@github.com/owner/repo.git", Repo: "owner/repo"},
	}
	if _, err := (InitWizard{}).Plan(answers); err == nil {
		t.Fatal("a remote carrying credentials must not be accepted by the init plan")
	}
	answers.GitHubRemote = GitHubRemote{URL: "https://github.com/owner/repo.git", Repo: "owner/repo"}
	plan, err := (InitWizard{}).Plan(answers)
	if err != nil {
		t.Fatalf("canonical GitHub remote: %v", err)
	}
	if !strings.Contains(FormatInitReview(plan), "agent may push changes to a branch of owner/repo") {
		t.Fatal("the review must identify the approved repository")
	}
}

func TestInitPlanValidatesTheModel(t *testing.T) {
	root := initTestRoot(t)
	rejected := InitWizard{ValidateModel: func(string) (string, error) {
		return "", errors.New("the model is not in the catalogue")
	}}
	if _, err := rejected.Plan(InitAnswers{Root: root, Model: "albert/gone"}); err == nil {
		t.Fatal("a rejected model must stop the setup: the answer has to change")
	}
	if _, err := rejected.Plan(InitAnswers{Root: root, SmallModel: MinistralSmallModelRef}); err == nil || !strings.Contains(err.Error(), "validate small model") {
		t.Fatalf("a rejected small model must be identified and rejected: %v", err)
	}

	// An unreachable catalogue is a warning, not a refusal: the model is still
	// recorded and the user can correct it later.
	warned := InitWizard{ValidateModel: func(string) (string, error) {
		return "the catalogue could not be reached; the model was recorded unverified", nil
	}}
	plan, err := warned.Plan(InitAnswers{Root: root, Model: "albert/deepseek-v4-flash"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// The non-Git warning is expected too (the fixture is not a repository), so
	// assert the catalogue warning is present rather than alone.
	var found bool
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "unverified") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want the unverified-model note", plan.Warnings)
	}
	if !strings.Contains(FormatInitReview(plan), "unverified") {
		t.Fatal("the review must carry the warning the user has to weigh")
	}
}

func TestInitPlanAndReviewDistinctSmallModel(t *testing.T) {
	root := initTestRoot(t)
	var validated []string
	plan, err := (InitWizard{ValidateModel: func(model string) (string, error) {
		validated = append(validated, model)
		return "", nil
	}}).Plan(InitAnswers{Root: root, Model: "albert/qwen3-coder", SmallModel: MinistralSmallModelRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 2 || validated[0] != "albert/qwen3-coder" || validated[1] != MinistralSmallModelRef {
		t.Fatalf("main and small model must each be validated: %v", validated)
	}
	if !strings.Contains(FormatInitReview(plan), "small model  "+MinistralSmallModelRef) {
		t.Fatal("non-interactive review must distinguish the small model")
	}
	review := FormatInteractiveInitReview(plan)
	if !strings.Contains(review, "Petit modèle Ministral 3 8B") {
		t.Fatalf("interactive review must name the distinct small model:\n%s", review)
	}
}

func TestInitApplyWritesManifestAndLock(t *testing.T) {
	root := initTestRoot(t)
	plan, err := (InitWizard{}).Plan(InitAnswers{
		Root: root, Model: "albert/deepseek-v4-flash", SmallModel: MinistralSmallModelRef,
		CPUs: 4, MemoryMB: 8192, CredentialRef: "work",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if err := (InitWizard{}).Apply(plan, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	pm, err := ReadProjectManifest(DefaultFS, plan.ManifestPath)
	if err != nil {
		t.Fatalf("the manifest must be readable by the normal reader: %v", err)
	}
	if pm.Runtime != string(RuntimeMicrosandbox) || pm.Isolation != string(IsolationFull) ||
		pm.Model != "albert/deepseek-v4-flash" || pm.SmallModel != MinistralSmallModelRef || pm.CPUs != 4 || pm.MemoryMB != 8192 ||
		pm.CredentialRef != "work" {
		t.Fatalf("manifest = %+v", pm)
	}
	// Only settings the launch path actually applies are recorded: a field
	// nothing reads would be a decision the setup pretends to have taken.
	if pm.Project != "" || pm.Storage != "" {
		t.Fatalf("the manifest must not record settings no reader consumes: %+v", pm)
	}
	if pm.SchemaVersion != projectManifestSchemaVersion {
		t.Fatalf("schemaVersion = %d", pm.SchemaVersion)
	}
	lf, err := ReadLockfile(DefaultFS, plan.LockPath)
	if err != nil {
		t.Fatalf("the lockfile must exist and be readable: %v", err)
	}
	if len(lf.Entries) != 0 {
		t.Fatalf("nothing is pinned yet, entries = %v", lf.Entries)
	}
	// The manifest must never carry a credential value.
	raw, err := os.ReadFile(plan.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "API_KEY") {
		t.Fatalf("the manifest must reference the credential by name only: %s", raw)
	}
}

func TestInitApplyRefusesToReplaceWithoutConsent(t *testing.T) {
	root := initTestRoot(t)
	if err := os.MkdirAll(filepath.Dir(ProjectManifestPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	original := ProjectManifest{SchemaVersion: 1, Project: "kept", Model: "albert/original"}
	if err := WriteProjectManifest(DefaultFS, ProjectManifestPath(root), original); err != nil {
		t.Fatal(err)
	}

	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root, Model: "albert/new"})
	if err != nil {
		t.Fatalf("Plan on an existing manifest must still work: %v", err)
	}
	if plan.ExistingManifest == nil {
		t.Fatal("the plan must report the existing manifest")
	}
	// The review shows what would be replaced, not just that something exists.
	review := FormatInitReview(plan)
	if !strings.Contains(review, "REPLACED") || !strings.Contains(review, "albert/original") {
		t.Fatalf("the review must show what would be lost: %q", review)
	}

	if err := (InitWizard{}).Apply(plan, false); err == nil {
		t.Fatal("Apply must refuse to replace an existing manifest without consent")
	}
	kept, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if kept.Project != "kept" || kept.Model != "albert/original" {
		t.Fatalf("the existing manifest must be untouched: %+v", kept)
	}

	if err := (InitWizard{}).Apply(plan, true); err != nil {
		t.Fatalf("Apply with consent: %v", err)
	}
	replaced, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Model != "albert/new" {
		t.Fatalf("manifest = %+v", replaced)
	}
}

// TestInitPlanCanonicalizesToTheWorktreeRoot pins the rule that makes the
// written manifest readable: the launch path resolves a project to its Git
// worktree root, so initializing a subdirectory of one must write the root's
// manifest, not a nested file nothing would ever read.
func TestInitPlanCanonicalizesToTheWorktreeRoot(t *testing.T) {
	root := initTestRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}, {"add", "-A"}, {"commit", "-q", "-m", "i", "--allow-empty"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v (%s)", args, err, out)
		}
	}
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: sub})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Both sides are compared in their resolved form: t.TempDir() is the
	// unresolved spelling on macOS (/var -> /private/var) and Windows (8.3
	// short names), while discovery resolves it.
	wantRoot := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		wantRoot = resolved
	}
	if plan.Answers.Root != wantRoot {
		t.Fatalf("root = %q, want the worktree root %q", plan.Answers.Root, wantRoot)
	}
	if plan.ManifestPath != ProjectManifestPath(wantRoot) {
		t.Fatalf("manifest path = %q, want the root's manifest", plan.ManifestPath)
	}
	// The user is told the manifest covers more than the directory they named.
	var told bool
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "racine du projet") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the review must say the manifest covers the worktree: %v", plan.Warnings)
	}
	// And no nested manifest is written for the subdirectory.
	if err := (InitWizard{}).Apply(plan, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".just-code")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be written below the root (stat err = %v)", err)
	}
}

// TestInitApplyKeepsExistingPins pins that replacing a manifest is not a
// reason to discard a lockfile someone else committed.
func TestInitApplyKeepsExistingPins(t *testing.T) {
	root := initTestRoot(t)
	if err := os.MkdirAll(filepath.Dir(ProjectLockPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, ProjectLockPath(root), Lockfile{Entries: map[string]string{"skill": "rev-1"}}); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root, Model: "albert/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := (InitWizard{}).Apply(plan, false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	lf, err := ReadLockfile(DefaultFS, ProjectLockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if lf.Entries["skill"] != "rev-1" {
		t.Fatalf("an existing pin must survive a setup run: %v", lf.Entries)
	}
}

func TestInitReviewWarnsAboutAnUnversionedManifest(t *testing.T) {
	root := initTestRoot(t)
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) == 0 || !strings.Contains(plan.Warnings[0], "pas un dépôt Git") {
		t.Fatalf("warnings = %v", plan.Warnings)
	}
	// The sealed-runtime review must state what the guest receives, since that
	// is the difference the user is choosing between.
	if !strings.Contains(FormatInitReview(plan), "filtered copy") {
		t.Fatal("a Microsandbox project must be told the guest gets a filtered copy")
	}
	// The mounting-runtime wording only applies where such a runtime is
	// selectable: Tart is macOS-only, so this is skipped on Windows rather
	// than asserting a choice the platform refuses.
	mounting := RuntimeTart
	if _, err := parseRuntimeName(string(mounting)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", mounting, err)
	}
	mounted, err := (InitWizard{}).Plan(InitAnswers{Root: root, Runtime: mounting})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(FormatInitReview(mounted), "MOUNTS") {
		t.Fatal("a mounting runtime must be told the project is exposed in the guest")
	}
}

// TestInitPlanDoesNotWarnOnADifferentSpellingOfTheSameDirectory pins that the
// worktree warning is about a genuinely wider root, not about a path that
// merely resolves differently: t.TempDir() is the unresolved spelling on macOS
// (/var -> /private/var) and on Windows (8.3 short names), and warning there
// would fire on an ordinary launch.
func TestInitPlanDoesNotWarnOnADifferentSpellingOfTheSameDirectory(t *testing.T) {
	real := t.TempDir()
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == real {
		// The spelling does not differ here; exercise it through a symlink so
		// the property is tested on every platform.
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		real = link
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: real})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "racine du projet") {
			t.Fatalf("a differently-spelled path to the same directory must not warn: %v", plan.Warnings)
		}
	}
	if plan.Answers.Root != resolved {
		t.Fatalf("root = %q, want %q", plan.Answers.Root, resolved)
	}
}

// TestInitApplyRefusesToDiscardAnUnreadableLock pins the other half of the
// preservation rule: a lockfile that cannot be read must stop the run, not be
// replaced with an empty one. Replacing it would discard whatever pins it
// held, silently — the same refusal the manifest already gets.
func TestInitApplyRefusesToDiscardAnUnreadableLock(t *testing.T) {
	root := initTestRoot(t)
	if err := os.MkdirAll(filepath.Dir(ProjectLockPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectLockPath(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := (InitWizard{}).Plan(InitAnswers{Root: root, Model: "albert/x"})
	if err != nil {
		t.Fatalf("Plan (a corrupt lock is not the manifest's problem): %v", err)
	}
	if err := (InitWizard{}).Apply(plan, false); err == nil {
		t.Fatal("Apply must refuse to overwrite a lockfile it cannot read")
	}
	raw, err := os.ReadFile(ProjectLockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{not json" {
		t.Fatalf("the unreadable lock must be left untouched, got %q", raw)
	}
}

func TestInitPinsVersionedSkillsAndManagedInstructionsIdempotently(t *testing.T) {
	root := initTestRoot(t)
	cache, state := t.TempDir(), t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	const userText = "User-authored rules stay byte-for-byte.\n\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(userText), 0o644); err != nil {
		t.Fatal(err)
	}
	wizard := InitWizard{StateDir: state, ResolveSkills: fixtureSkillResolver(t)}
	plan, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(FormatInitReview(plan), "AGENTS.md managed-zone diff") || !plan.InstructionsChanged {
		t.Fatalf("review omitted the generated instruction diff: %+v", plan)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil || len(manifest.Skills) != 1 || manifest.Skills[0] != "official/rgaa" || manifest.SkillsLocalOnly {
		t.Fatalf("manifest = %+v, err = %v", manifest, err)
	}
	lock, err := ReadLockfile(DefaultFS, ProjectLockPath(root))
	if err != nil || lock.Skills["official/rgaa"].Revision != projectSkillsRevision || !isSHA256(lock.Skills["official/rgaa"].SHA256) {
		t.Fatalf("lock = %+v, err = %v", lock, err)
	}
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil || !strings.HasPrefix(string(agents), userText) || !strings.Contains(string(agents), projectSkillsRevision) {
		t.Fatalf("managed AGENTS.md = %q, err = %v", agents, err)
	}
	beforeManifest, beforeLock, beforeAgents := readFileForTest(t, ProjectManifestPath(root)), readFileForTest(t, ProjectLockPath(root)), append([]byte(nil), agents...)
	// A replacement with no skill flags keeps the pinned selection, lock and
	// managed zone exactly unchanged and needs no network access.
	wizard.ResolveSkills = nil
	repeat, err := wizard.Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if repeat.InstructionsChanged {
		t.Fatalf("identical pinned selection produced an AGENTS.md diff: before=%q after=%q", repeat.InstructionsBefore, repeat.InstructionsAfter)
	}
	if err := wizard.Apply(repeat, true); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeManifest, readFileForTest(t, ProjectManifestPath(root))) ||
		!bytes.Equal(beforeLock, readFileForTest(t, ProjectLockPath(root))) ||
		!bytes.Equal(beforeAgents, readFileForTest(t, filepath.Join(root, "AGENTS.md"))) {
		t.Fatal("reapplying an unchanged skill selection changed project files")
	}
	if _, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeAgentVM}); err == nil {
		t.Fatal("preserved project skills must be rejected with a non-Microsandbox runtime")
	}
	clear, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{}, SkillsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !clear.InstructionsChanged || strings.Contains(clear.InstructionsAfter, "official/rgaa") {
		t.Fatalf("clearing skills must remove the bounded block: before=%q after=%q", clear.InstructionsBefore, clear.InstructionsAfter)
	}
	if !strings.Contains(clear.InstructionsAfter, "## Managed just-code instructions") {
		t.Fatalf("clearing skills must keep the standing managed instructions: after=%q", clear.InstructionsAfter)
	}
	if err := wizard.Apply(clear, true); err != nil {
		t.Fatal(err)
	}
	cleared, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil || len(cleared.Skills) != 0 || cleared.SkillsLocalOnly {
		t.Fatalf("cleared manifest = %+v, err = %v", cleared, err)
	}
	clearedAgents := readFileForTest(t, filepath.Join(root, "AGENTS.md"))
	if !bytes.HasPrefix(clearedAgents, []byte(userText)) || bytes.Contains(clearedAgents, []byte("official/rgaa")) {
		t.Fatalf("clearing skills damaged user instructions or kept a managed skill: %q", clearedAgents)
	}
}

func TestInitPlanPreservesRetainedSkillPinsWithoutResolvingThem(t *testing.T) {
	root := initTestRoot(t)
	cache, state := t.TempDir(), t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	// The resolver records every call: a retained skill must never reach
	// it, because resolving it again is exactly what drops its pin.
	base := fixtureSkillResolver(t)
	var resolved []string
	resolver := func(ctx context.Context, ids []string) ([]ProjectSkill, map[string]SkillLock, error) {
		resolved = append(resolved, strings.Join(ids, ","))
		return base(ctx, ids)
	}
	wizard := InitWizard{StateDir: state, ResolveSkills: resolver}
	plan, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{"official/ancienne"}, SkillsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, false); err != nil {
		t.Fatal(err)
	}
	oldLock, err := ReadLockfile(DefaultFS, ProjectLockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	oldPin := oldLock.Skills["official/ancienne"]
	if oldPin.SHA256 == "" {
		t.Fatal("fixture did not pin the older skill")
	}

	// The catalogue rotated: the new selection keeps the older pin and
	// adds a resolvable skill. Plan must resolve only the resolvable ID
	// and merge the retained pin verbatim.
	resolved = nil
	plan, err = wizard.Plan(InitAnswers{
		Root:           root,
		Skills:         []string{"official/rgaa", "official/ancienne"},
		SkillsSet:      true,
		RetainedSkills: []string{"official/ancienne"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0] != "official/rgaa" {
		t.Fatalf("resolver calls = %v, want exactly [official/rgaa]", resolved)
	}
	if pin := plan.SkillLocks["official/ancienne"]; pin != oldPin {
		t.Fatalf("retained pin = %+v, want the existing pin verbatim: %+v", pin, oldPin)
	}
	if got := strings.Join(plan.Answers.Skills, ","); got != "official/rgaa,official/ancienne" {
		t.Fatalf("selection must still list the retained skill, got %q", got)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil || strings.Join(manifest.Skills, ",") != "official/rgaa,official/ancienne" {
		t.Fatalf("manifest after the mixed selection = %+v, err = %v", manifest, err)
	}
	lock, err := ReadLockfile(DefaultFS, ProjectLockPath(root))
	if err != nil || lock.Skills["official/ancienne"] != oldPin {
		t.Fatalf("retained pin changed on apply: %+v, err = %v", lock.Skills, err)
	}

	// A retained-only selection resolves nothing at all and still keeps
	// the pin.
	resolved = nil
	plan, err = wizard.Plan(InitAnswers{
		Root:           root,
		Skills:         []string{"official/ancienne"},
		SkillsSet:      true,
		RetainedSkills: []string{"official/ancienne"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 0 {
		t.Fatalf("a retained-only selection must not resolve anything, got %v", resolved)
	}
	if pin := plan.SkillLocks["official/ancienne"]; pin != oldPin {
		t.Fatalf("retained-only pin = %+v, want %+v", pin, oldPin)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	manifest, err = ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil || strings.Join(manifest.Skills, ",") != "official/ancienne" {
		t.Fatalf("manifest after the retained-only selection = %+v, err = %v", manifest, err)
	}
	lock, err = ReadLockfile(DefaultFS, ProjectLockPath(root))
	if err != nil || len(lock.Skills) != 1 || lock.Skills["official/ancienne"] != oldPin {
		t.Fatalf("retained-only lock = %+v, err = %v", lock.Skills, err)
	}
}

func TestInitPlanRejectsRetainedSkillWithoutExistingPin(t *testing.T) {
	root := initTestRoot(t)
	cache, state := t.TempDir(), t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	wizard := InitWizard{StateDir: state, ResolveSkills: fixtureSkillResolver(t)}
	// Fresh project: nothing was ever pinned, so there is no pin to
	// preserve and the plan must fail closed.
	_, err := wizard.Plan(InitAnswers{
		Root:           root,
		Skills:         []string{"official/rgaa", "official/fantome"},
		SkillsSet:      true,
		RetainedSkills: []string{"official/fantome"},
	})
	if err == nil || !strings.Contains(err.Error(), `retained skill "official/fantome" has no existing pin`) {
		t.Fatalf("retaining an unpinned skill on a fresh project must fail, got %v", err)
	}
	// A pinned project must reject a retained ID it never pinned.
	plan, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, false); err != nil {
		t.Fatal(err)
	}
	_, err = wizard.Plan(InitAnswers{
		Root:           root,
		Skills:         []string{"official/rgaa", "official/fantome"},
		SkillsSet:      true,
		RetainedSkills: []string{"official/fantome"},
	})
	if err == nil || !strings.Contains(err.Error(), `retained skill "official/fantome" has no existing pin`) {
		t.Fatalf("retaining a never-pinned skill must fail, got %v", err)
	}
}

func TestInitApplyRejectsReviewedStateChangesBeforeWriting(t *testing.T) {
	for _, kind := range []string{"lock-created", "lock-edited", "lock-deleted", "local-created", "local-edited"} {
		t.Run(kind, func(t *testing.T) {
			root := initTestRoot(t)
			localOnly := strings.HasPrefix(kind, "local-")
			manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
			if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{SkillsLocalOnly: localOnly}); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(kind, "-created") {
				if err := WriteLockfile(DefaultFS, lockPath, Lockfile{}); err != nil {
					t.Fatal(err)
				}
			}
			wizard := InitWizard{StateDir: t.TempDir()}
			answers := InitAnswers{Root: root}
			if localOnly {
				answers.SkillsLocalOnly, answers.SkillsLocalOnlySet = true, true
				if kind == "local-edited" {
					path, err := LocalSkillSelectionsPath(wizard.StateDir, DiscoverInstanceForTest(t, root))
					if err != nil {
						t.Fatal(err)
					}
					if err := WriteLocalSkillSelections(DefaultFS, path, LocalSkillSelections{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			plan, err := wizard.Plan(answers)
			if err != nil {
				t.Fatal(err)
			}
			var path string
			switch {
			case localOnly:
				path = plan.LocalSkillsPath
				// Simulate a local file created or edited after the review.
				selection := LocalSkillSelections{}
				if kind == "local-edited" {
					selection.Skills = []string{"official/rgaa"}
				}
				err = WriteLocalSkillSelections(DefaultFS, path, selection)
			case kind == "lock-created":
				path = lockPath
				err = WriteLockfile(DefaultFS, lockPath, Lockfile{})
			case kind == "lock-edited":
				path = lockPath
				err = WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{"skill": "new-pin"}})
			case kind == "lock-deleted":
				path = lockPath
				err = os.Remove(lockPath)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, readErr := readReviewedFileSnapshot(DefaultFS, path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			applyErr := wizard.Apply(plan, true)
			if applyErr == nil || !strings.Contains(applyErr.Error(), "changed after the review") {
				t.Fatalf("changed %s was not refused: %v", kind, applyErr)
			}
			after, readErr := readReviewedFileSnapshot(DefaultFS, path)
			if readErr != nil || after.existed != before.existed || !bytes.Equal(after.data, before.data) {
				t.Fatalf("Apply changed the concurrent file %s: before=%+v after=%+v err=%v", path, before, after, readErr)
			}
			if got, err := ReadProjectManifest(DefaultFS, manifestPath); err != nil || len(got.MCPConnectors) != 0 {
				t.Fatalf("refused Apply changed the manifest: %+v, %v", got, err)
			}
		})
	}
}

func TestInitApplyMergesAGENTSChangesMadeAfterPlan(t *testing.T) {
	root := initTestRoot(t)
	cache := t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	wizard := InitWizard{ResolveSkills: fixtureSkillResolver(t)}
	plan, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true})
	if err != nil {
		t.Fatal(err)
	}
	const concurrentEdit = "User edit made while the review was open.\n"
	current := plan.InstructionsAfter + "\n" + concurrentEdit
	if err := os.WriteFile(plan.InstructionsPath, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, false); err != nil {
		t.Fatal(err)
	}
	got := readFileForTest(t, plan.InstructionsPath)
	if !bytes.Contains(got, []byte("official/rgaa")) || !bytes.Contains(got, []byte(concurrentEdit)) {
		t.Fatalf("Apply lost concurrent user text or managed skill: %q", got)
	}
}

func TestInitApplyRepairsManagedInstructionsAfterNoDiffReview(t *testing.T) {
	root := initTestRoot(t)
	cache := t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	wizard := InitWizard{ResolveSkills: fixtureSkillResolver(t)}
	answers := InitAnswers{Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true}
	first, err := wizard.Plan(answers)
	if err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(first, false); err != nil {
		t.Fatal(err)
	}
	plan, err := wizard.Plan(answers)
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsChanged {
		t.Fatal("unchanged versioned skills should have an unchanged managed-zone preview")
	}
	const concurrentEdit = "User edit made after an unchanged preview.\n"
	if err := os.WriteFile(plan.InstructionsPath, []byte(concurrentEdit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	got := readFileForTest(t, plan.InstructionsPath)
	if !bytes.Contains(got, []byte("official/rgaa")) || !bytes.Contains(got, []byte(concurrentEdit)) {
		t.Fatalf("Apply did not restore the managed skill zone or lost concurrent user text: %q", got)
	}
}

func TestInitApplyRemovesStaleInstructionsForEmptyVersionedSelection(t *testing.T) {
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeMicrosandbox), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	const userInstructions = "User policy.\n"
	instructionsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(instructionsPath, []byte(userInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	wizard := InitWizard{}
	plan, err := wizard.Plan(InitAnswers{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsPath == "" || !plan.InstructionsChanged ||
		!strings.Contains(plan.InstructionsAfter, "BEGIN JUST-CODE MANAGED SKILLS") ||
		!strings.Contains(plan.InstructionsAfter, "## Managed just-code instructions") ||
		strings.Contains(plan.InstructionsAfter, "official/rgaa") {
		t.Fatalf("empty schema-v2 selection must own the managed zone with standing instructions and no pins: %+v", plan)
	}
	pins := map[string]SkillLock{"official/rgaa": {Revision: projectSkillsRevision}}
	stale, err := MergeManagedInstructions(userInstructions, []string{"official/rgaa"}, pins, RuntimeMicrosandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.InstructionsPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	got := readFileForTest(t, instructionsPath)
	if !bytes.Contains(got, []byte(userInstructions)) ||
		!bytes.Contains(got, []byte("BEGIN JUST-CODE MANAGED SKILLS")) ||
		!bytes.Contains(got, []byte("## Managed just-code instructions")) ||
		bytes.Contains(got, []byte("official/rgaa")) {
		t.Fatalf("empty versioned selection must keep the standing zone without stale pins and preserve user text: %q", got)
	}
}

func TestInitApplyLeavesTartSelectionWithoutPinsZoneless(t *testing.T) {
	// The standing zone describes the Microsandbox port forward. A tart
	// project without pinned skills has nothing runtime-neutral to say, so
	// its AGENTS.md must stay untouched.
	// Tart is macOS-only, so this is skipped on Windows rather than
	// asserting a choice the platform refuses (same as the mounting test).
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	const userInstructions = "User policy.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(userInstructions), 0o644); err != nil {
		t.Fatal(err)
	}
	wizard := InitWizard{}
	// The runtime comes from the invocation: a rerun without --runtime
	// re-plans the project as Microsandbox, so a tart plan is explicit.
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsChanged || strings.Contains(plan.InstructionsAfter, "JUST-CODE MANAGED SKILLS") {
		t.Fatalf("pin-less tart selection must leave instructions untouched: changed=%v after=%q", plan.InstructionsChanged, plan.InstructionsAfter)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	if got := readFileForTest(t, filepath.Join(root, "AGENTS.md")); string(got) != userInstructions {
		t.Fatalf("tart AGENTS.md was modified: %q", got)
	}
}

func TestInitLocalOnlySkillsStayOutsideTheCheckout(t *testing.T) {
	root := initTestRoot(t)
	cache, state := t.TempDir(), t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	wizard := InitWizard{StateDir: state, ResolveSkills: fixtureSkillResolver(t)}
	plan, err := wizard.Plan(InitAnswers{
		Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true,
		SkillsLocalOnly: true, SkillsLocalOnlySet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsChanged || strings.Contains(FormatInitReview(plan), "AGENTS.md managed-zone diff") {
		t.Fatal("local-only selection must not preview a project instruction edit")
	}
	if err := wizard.Apply(plan, false); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(root))
	if err != nil || !manifest.SkillsLocalOnly || len(manifest.Skills) != 0 {
		t.Fatalf("manifest = %+v, err = %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("local-only mode wrote AGENTS.md (stat err = %v)", err)
	}
	path, err := LocalSkillSelectionsPath(state, DiscoverInstanceForTest(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(path, root+string(filepath.Separator)) {
		t.Fatalf("host-local skill state is inside the checkout: %s", path)
	}
	local, err := ReadLocalSkillSelections(DefaultFS, path)
	if err != nil || len(local.Skills) != 1 || local.Pins["official/rgaa"].Revision != projectSkillsRevision {
		t.Fatalf("local selections = %+v, err = %v", local, err)
	}
	packages, err := LoadProjectSkillPackages(DefaultFS, root, state, DiscoverInstanceForTest(t, root))
	if err != nil || len(packages) != 1 {
		t.Fatalf("local packages = %d, err = %v", len(packages), err)
	}
}

type failRenameForPathFS struct {
	FS
	Path string
}

func (f failRenameForPathFS) RenameTmp(oldPath, newPath string) error {
	if newPath == f.Path {
		return os.ErrPermission
	}
	return f.FS.RenameTmp(oldPath, newPath)
}

func TestInitLocalOnlyStateWriteFailurePreservesVersionedFiles(t *testing.T) {
	root := initTestRoot(t)
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	cache, state := t.TempDir(), t.TempDir()
	oldCacheDir := userCacheDirFn
	userCacheDirFn = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDirFn = oldCacheDir })
	ids := []string{"official/rgaa"}
	_, pins, err := fixtureSkillResolver(t)(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	fs := newMapFS()
	agentsPath := filepath.Join(root, "AGENTS.md")
	agents, err := MergeManagedInstructions("User rules.\n", ids, pins, RuntimeMicrosandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentsPath, []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	fs.files[agentsPath] = []byte(agents)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(fs, manifestPath, ProjectManifest{Skills: ids, Runtime: string(RuntimeMicrosandbox), Isolation: string(IsolationFull)}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(fs, lockPath, Lockfile{Skills: pins}); err != nil {
		t.Fatal(err)
	}
	beforeManifest := append([]byte(nil), fs.files[manifestPath]...)
	beforeLock := append([]byte(nil), fs.files[lockPath]...)
	beforeAgents := append([]byte(nil), fs.files[agentsPath]...)
	wizard := InitWizard{FS: fs, StateDir: state}
	plan, err := wizard.Plan(InitAnswers{Root: root, SkillsLocalOnly: true, SkillsLocalOnlySet: true})
	if err != nil {
		t.Fatal(err)
	}
	wizard.FS = failRenameForPathFS{FS: fs, Path: plan.ManifestPath}
	if err := wizard.Apply(plan, true); err == nil {
		t.Fatal("manifest failure after staging local pins must be reported")
	}
	if !bytes.Equal(fs.files[manifestPath], beforeManifest) || !bytes.Equal(fs.files[lockPath], beforeLock) || !bytes.Equal(fs.files[agentsPath], beforeAgents) {
		t.Fatal("failed local-only state persistence changed versioned project files")
	}
	localPath, err := LocalSkillSelectionsPath(state, DiscoverInstanceForTest(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := fs.files[localPath]; exists {
		t.Fatal("failed local-only persistence left a partial selection file")
	}
}

func TestMergeManagedInstructionsRefusesSymlink(t *testing.T) {
	root := initTestRoot(t)
	outside := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(outside, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "AGENTS.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wizard := InitWizard{ResolveSkills: fixtureSkillResolver(t)}
	if _, err := wizard.Plan(InitAnswers{Root: root, Skills: []string{"official/rgaa"}, SkillsSet: true}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlinked AGENTS.md error = %v", err)
	}
	if got := readFileForTest(t, outside); string(got) != "keep me\n" {
		t.Fatalf("external file changed: %q", got)
	}
}

func TestInitZonelessPlanLeavesSymlinkedAgentsUntouched(t *testing.T) {
	// A pin-less tart project has nothing to write into AGENTS.md, so a
	// symlinked AGENTS.md must not block init: the plan claims the file
	// only when pins, Microsandbox standing guidance, or a stale managed
	// zone require changing it.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(outside, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wizard := InitWizard{}
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart})
	if err != nil {
		t.Fatalf("a zoneless plan must not validate an AGENTS.md it will not touch: %v", err)
	}
	if plan.InstructionsChanged || plan.InstructionsPath != "" {
		t.Fatalf("zoneless plan must not claim AGENTS.md: changed=%v path=%q", plan.InstructionsChanged, plan.InstructionsPath)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	if got := readFileForTest(t, outside); string(got) != "keep me\n" {
		t.Fatalf("external file changed: %q", got)
	}
}

func TestInitLocalOnlyZonelessPlanLeavesSymlinkedAgentsUntouched(t *testing.T) {
	// A local-only strip with no managed zone is a no-op: the plan must
	// not claim (and refuse) a symlinked AGENTS.md it would not change.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(outside, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wizard := InitWizard{StateDir: t.TempDir()}
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart, SkillsLocalOnly: true, SkillsLocalOnlySet: true})
	if err != nil {
		t.Fatalf("a zoneless local-only plan must not validate an AGENTS.md it will not touch: %v", err)
	}
	if plan.InstructionsChanged || plan.InstructionsPath != "" {
		t.Fatalf("zoneless local-only plan must not claim AGENTS.md: changed=%v path=%q", plan.InstructionsChanged, plan.InstructionsPath)
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	if got := readFileForTest(t, outside); string(got) != "keep me\n" {
		t.Fatalf("external file changed: %q", got)
	}
}

func TestInitLocalOnlyStripsStaleZoneOnOtherRuntimes(t *testing.T) {
	// The zone-presence check must not swallow the strip it exists for: a
	// stale managed zone on a pin-less tart project is still removed by a
	// local-only init.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}
	zoned, err := MergeManagedInstructions("User rules.\n", []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": lock}, RuntimeTart)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(zoned), 0o644); err != nil {
		t.Fatal(err)
	}
	wizard := InitWizard{StateDir: t.TempDir()}
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart, SkillsLocalOnly: true, SkillsLocalOnlySet: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InstructionsChanged {
		t.Fatal("a stale managed zone must be stripped by a local-only init")
	}
	if err := wizard.Apply(plan, true); err != nil {
		t.Fatal(err)
	}
	got := string(readFileForTest(t, filepath.Join(root, "AGENTS.md")))
	if strings.Contains(got, "BEGIN JUST-CODE MANAGED SKILLS") {
		t.Fatalf("managed zone survived the local-only strip: %q", got)
	}
	if !strings.Contains(got, "User rules.") {
		t.Fatalf("unmanaged bytes were lost: %q", got)
	}
}

func TestInitLocalOnlyPlansClaimOnlyForAnActualZone(t *testing.T) {
	// Local-only plans never merge, so pins and the Microsandbox runtime
	// must not force the AGENTS.md claim: without a managed zone there is
	// nothing to strip, and a symlinked AGENTS.md must not block them.
	cases := []struct {
		name    string
		runtime Runtime
		skills  []string
		skip    func(t *testing.T)
	}{
		{
			name:    "microsandbox without skills",
			runtime: RuntimeMicrosandbox,
		},
		{
			// Project skills validate only on the sealed Microsandbox
			// runtime, so a nonempty local selection implies msb.
			name:    "microsandbox with a local selection",
			runtime: RuntimeMicrosandbox,
			skills:  []string{"official/rgaa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skip != nil {
				tc.skip(t)
			}
			root := initTestRoot(t)
			manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
			if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
				Runtime: string(tc.runtime), Isolation: string(IsolationFull),
			}); err != nil {
				t.Fatal(err)
			}
			if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "instructions.md")
			if err := os.WriteFile(outside, []byte("keep me\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
			cache, state := t.TempDir(), t.TempDir()
			oldCacheDir := userCacheDirFn
			userCacheDirFn = func() (string, error) { return cache, nil }
			t.Cleanup(func() { userCacheDirFn = oldCacheDir })
			wizard := InitWizard{StateDir: state, ResolveSkills: fixtureSkillResolver(t)}
			answers := InitAnswers{
				Root:               root,
				Runtime:            tc.runtime,
				Skills:             tc.skills,
				SkillsSet:          len(tc.skills) > 0,
				SkillsLocalOnly:    true,
				SkillsLocalOnlySet: true,
			}
			plan, err := wizard.Plan(answers)
			if err != nil {
				t.Fatalf("a zoneless local-only plan must not validate an AGENTS.md it will not touch: %v", err)
			}
			if plan.InstructionsChanged || plan.InstructionsPath != "" {
				t.Fatalf("zoneless local-only plan must not claim AGENTS.md: changed=%v path=%q", plan.InstructionsChanged, plan.InstructionsPath)
			}
			if err := wizard.Apply(plan, true); err != nil {
				t.Fatal(err)
			}
			if got := readFileForTest(t, outside); string(got) != "keep me\n" {
				t.Fatalf("external file changed: %q", got)
			}
		})
	}
}

func TestInitApplyStripsZoneAddedAfterPlanning(t *testing.T) {
	// A managed zone can appear between Plan and Apply; the apply-time
	// recheck must strip it so the committed manifest never contradicts
	// the instructions on disk.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	cases := []struct {
		name      string
		localOnly bool
	}{
		{name: "local-only plan", localOnly: true},
		{name: "pin-less plan"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := initTestRoot(t)
			manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
			if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
				Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
			}); err != nil {
				t.Fatal(err)
			}
			if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("User rules.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			wizard := InitWizard{StateDir: t.TempDir()}
			answers := InitAnswers{Root: root, Runtime: RuntimeTart}
			if tc.localOnly {
				answers.SkillsLocalOnly = true
				answers.SkillsLocalOnlySet = true
			}
			plan, err := wizard.Plan(answers)
			if err != nil {
				t.Fatal(err)
			}
			if plan.InstructionsPath != "" {
				t.Fatal("a zone-less plan must not claim AGENTS.md")
			}
			lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}
			zoned, err := MergeManagedInstructions("User rules.\n", []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": lock}, RuntimeTart)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(zoned), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := wizard.Apply(plan, true); err != nil {
				t.Fatal(err)
			}
			got := string(readFileForTest(t, filepath.Join(root, "AGENTS.md")))
			if strings.Contains(got, "BEGIN JUST-CODE MANAGED SKILLS") {
				t.Fatalf("zone added after planning survived the apply: %q", got)
			}
			if !strings.Contains(got, "User rules.") {
				t.Fatalf("unmanaged bytes were lost: %q", got)
			}
		})
	}
}

func TestInitApplyRefusesZoneAddedBehindSymlinkAfterPlanning(t *testing.T) {
	// A zone appearing behind a symlinked AGENTS.md must abort the apply
	// instead of writing through the link.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(outside, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	wizard := InitWizard{StateDir: t.TempDir()}
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart, SkillsLocalOnly: true, SkillsLocalOnlySet: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsPath != "" {
		t.Fatal("a zone-less plan must not claim AGENTS.md")
	}
	lock := SkillLock{Repository: projectSkillsRepository, Revision: strings.Repeat("a", 40), SHA256: strings.Repeat("b", 64)}
	zoned, err := MergeManagedInstructions("keep me\n", []string{"official/rgaa"}, map[string]SkillLock{"official/rgaa": lock}, RuntimeTart)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte(zoned), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wizard.Apply(plan, true); err == nil {
		t.Fatal("apply must refuse a non-regular AGENTS.md holding a managed zone")
	}
	if got := readFileForTest(t, outside); !strings.Contains(string(got), "BEGIN JUST-CODE MANAGED SKILLS") {
		t.Fatalf("the refused apply must leave the external file untouched: %q", got)
	}
}

func TestHasManagedSkillZoneIn(t *testing.T) {
	// The streaming scan must match hasManagedSkillZone's semantics:
	// either marker line claims the file, a marker that is only part of
	// a longer line does not, and overlong lines are skipped whole.
	const start = "<!-- BEGIN JUST-CODE MANAGED SKILLS -->"
	const end = "<!-- END JUST-CODE MANAGED SKILLS -->"
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{name: "no markers", content: "User rules.\n", want: false},
		{name: "balanced zone", content: "a\n" + start + "\nx\n" + end + "\n", want: true},
		{name: "begin only", content: "a\n" + start + "\n", want: true},
		{name: "end only", content: "a\n" + end + "\n", want: true},
		{name: "marker as substring of a longer line", content: "see " + start + " inline\n", want: false},
		{name: "indented marker", content: "  " + start + "  \n", want: true},
		{name: "crlf line endings", content: "a\r\n" + start + "\r\n", want: true},
		{name: "overlong line before marker", content: strings.Repeat("a", 200000) + "\n" + start + "\n", want: true},
		{name: "marker before overlong line", content: start + "\n" + strings.Repeat("a", 200000) + "\n", want: true},
		{name: "marker as tail of an overlong line", content: strings.Repeat("a", 64<<10) + start + "\n", want: false},
		{name: "final unterminated marker line", content: "a\n" + start, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasManagedSkillZoneIn(strings.NewReader(tc.content)); got != tc.want {
				t.Fatalf("hasManagedSkillZoneIn = %v, want %v", got, tc.want)
			}
			if got := hasManagedSkillZone(tc.content); got != tc.want {
				t.Fatalf("scan disagrees with hasManagedSkillZone (%v, want %v)", got, tc.want)
			}
		})
	}
}

func TestInitPlanScansOversizedAgentsWithoutClaiming(t *testing.T) {
	// The zone probe scans for markers in bounded memory: an oversized
	// zone-less AGENTS.md is not claimed (so a symlink to one stays
	// untouched), while an oversized file carrying a marker is.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	const start = "<!-- BEGIN JUST-CODE MANAGED SKILLS -->"
	const end = "<!-- END JUST-CODE MANAGED SKILLS -->"
	cases := []struct {
		name      string
		symlink   bool
		withZone  bool
		wantClaim bool
	}{
		{name: "regular oversized zone-less"},
		{name: "symlink to oversized zone-less", symlink: true},
		{name: "oversized with a marker", withZone: true, wantClaim: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := initTestRoot(t)
			manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
			if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
				Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
			}); err != nil {
				t.Fatal(err)
			}
			if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
				t.Fatal(err)
			}
			content := strings.Repeat("a", 1<<20+1)
			if tc.withZone {
				content += "\n" + start + "\n" + end + "\n"
			}
			agentsPath := filepath.Join(root, "AGENTS.md")
			if tc.symlink {
				outside := filepath.Join(t.TempDir(), "instructions.md")
				if err := os.WriteFile(outside, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, agentsPath); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
			} else if err := os.WriteFile(agentsPath, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			wizard := InitWizard{StateDir: t.TempDir()}
			plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantClaim && plan.InstructionsPath == "" {
				t.Fatal("an oversized AGENTS.md carrying a marker must be claimed")
			}
			if !tc.wantClaim && plan.InstructionsPath != "" {
				t.Fatal("an oversized zone-less AGENTS.md must not be claimed")
			}
		})
	}
}

func readFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func DiscoverInstanceForTest(t *testing.T, root string) string {
	t.Helper()
	project, err := DiscoverProject(root)
	if err != nil {
		t.Fatal(err)
	}
	return project.InstanceName()
}

// TestInitWarnsForARelativeNestedRoot pins that the "covers the whole
// worktree" notice survives a relative invocation: `just-code init nested/dir`
// names a directory inside the worktree just as much as an absolute path does.
func TestInitWarnsForARelativeNestedRoot(t *testing.T) {
	root := initTestRoot(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}, {"add", "-A"}, {"commit", "-q", "-m", "i", "--allow-empty"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()

	plan, err := (InitWizard{}).Plan(InitAnswers{Root: filepath.Join("nested")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var told bool
	for _, warning := range plan.Warnings {
		if strings.Contains(warning, "racine du projet") {
			told = true
		}
	}
	if !told {
		t.Fatalf("a relative nested root must still be reported: %v", plan.Warnings)
	}
}
