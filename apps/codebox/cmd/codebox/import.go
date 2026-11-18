package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type albertCodeImportOptions struct {
	root            string
	apply           bool
	importAlbertKey bool
}

var resolveImportSkillsFn = codebox.ResolveProjectSkills
var verifyImportSkillsFn = func(ids []string, locks map[string]codebox.SkillLock) error {
	_, err := codebox.LoadLockedSkillPackages(ids, locks)
	return err
}
var bumpImportedCredentialGenerationFn = codebox.BumpCredentialGeneration
var importCredentialStoreFn = codebox.DefaultCredentialStore
var applyImportedProjectFn = func(wizard codebox.InitWizard, plan codebox.InitPlan) error {
	return wizard.Apply(plan, false)
}
var repairImportedProjectFn = func(wizard codebox.InitWizard, plan codebox.InitPlan) error {
	return wizard.Apply(plan, true)
}

func parseAlbertCodeImportArgs(args []string) (albertCodeImportOptions, error) {
	opts := albertCodeImportOptions{root: "."}
	if len(args) == 0 || args[0] != "albert-code" {
		return opts, fmt.Errorf("usage : codebox import albert-code [--root PATH] [--apply] [--import-albert-key]")
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			if opts.apply {
				return opts, fmt.Errorf("--apply ne peut être indiqué qu'une seule fois")
			}
			opts.apply = true
		case "--import-albert-key":
			if opts.importAlbertKey {
				return opts, fmt.Errorf("--import-albert-key ne peut être indiqué qu'une seule fois")
			}
			opts.importAlbertKey = true
		case "--root":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("--root exige un chemin de dossier")
			}
			i++
			opts.root = args[i]
		default:
			return opts, fmt.Errorf("option d'import inconnue %q", args[i])
		}
	}
	if opts.importAlbertKey && !opts.apply {
		return opts, fmt.Errorf("--import-albert-key exige --apply ; l'aperçu n'enregistre aucun identifiant")
	}
	return opts, nil
}

func albertCodeImportCmd(args []string) (int, error) {
	if (len(args) == 1 && args[0] == "--help") || (len(args) == 2 && args[0] == "albert-code" && args[1] == "--help") {
		fmt.Println("Usage : codebox import albert-code [--root PATH] [--apply] [--import-albert-key]")
		fmt.Println("L'aperçu est en lecture seule. --apply écrit la configuration du projet ; --import-albert-key importe séparément une clé littérale prise en charge depuis .env.")
		return 0, nil
	}
	opts, err := parseAlbertCodeImportArgs(args)
	if err != nil {
		return 2, err
	}
	plan, err := codebox.DiscoverAlbertCodeImport(opts.root)
	if err != nil {
		return 1, err
	}
	if len(plan.SkillNames) > 0 {
		catalogue, err := projectSkillCatalogueFn(context.Background())
		if err != nil {
			if opts.apply {
				return 1, fmt.Errorf("résoudre les skills héritées par rapport au catalogue officiel épinglé : %w", err)
			}
			plan.SkillCatalogueUnavailable = true
			plan.ReviewItems = append(plan.ReviewItems, "catalogue officiel de skills indisponible ; les skills héritées ne peuvent pas être résolues")
			sort.Strings(plan.ReviewItems)
		} else {
			plan.ResolveSkills(catalogue)
		}
	}
	if opts.importAlbertKey && !plan.CredentialAvailable {
		if plan.CredentialNeedsReview {
			return 2, fmt.Errorf("le .env contient une affectation ALBERT_API_KEY non prise en charge ou répétée ; réduisez-la à une seule affectation littérale avant d'importer")
		}
		return 2, fmt.Errorf("--import-albert-key exige une valeur littérale ALBERT_API_KEY dans le .env du projet")
	}
	if opts.importAlbertKey {
		fmt.Printf("L'ALBERT_API_KEY de %s/.env va être enregistrée dans le trousseau natif (%s) ; la valeur ne sera pas affichée.\n", plan.Root, codebox.StoreAvailability())
	}
	if opts.apply && len(plan.Platforms) > 0 {
		if err := validateImportedPlatforms(plan); err != nil {
			return 1, err
		}
	}
	fmt.Print(plan.Format())
	if !opts.apply {
		fmt.Println("Aperçu seulement. Indiquez --apply pour écrire le manifeste et le fichier de verrouillage du projet codebox.")
		fmt.Println("Aucun changement effectué.")
		warnUnapprovedImportInputs(plan.Root)
		return 0, nil
	}
	if len(plan.SourceFiles) == 0 {
		return 1, fmt.Errorf("aucun fichier de projet Albert Code pris en charge n'a été détecté")
	}
	credentialRef := ""
	if opts.importAlbertKey {
		credentialRef = "albert"
	}

	answers := codebox.InitAnswers{
		Root:          plan.Root,
		Runtime:       codebox.RuntimeMicrosandbox,
		Isolation:     codebox.IsolationFull,
		Model:         plan.Model,
		CredentialRef: credentialRef,
		MCPConnectors: plan.MCPConnectors,
		MCPsSet:       true,
	}
	if plan.HasSkillsFile {
		answers.Skills, answers.SkillsSet = plan.Skills, true
	}
	wizard := codebox.InitWizard{
		FS:            codebox.DefaultFS,
		ResolveSkills: resolveImportSkillsFn,
		VerifySkills:  verifyImportSkillsFn,
	}
	setupPlan, err := wizard.Plan(answers)
	if err != nil {
		return 1, fmt.Errorf("construire le plan d'import codebox : %w", err)
	}
	for _, warning := range setupPlan.Warnings {
		fmt.Fprintf(os.Stderr, "Attention : %s\n", warning)
	}
	existingMatches := false
	if existing, err := codebox.ReadProjectManifest(codebox.DefaultFS, setupPlan.ManifestPath); err == nil {
		rawManifest, readErr := readExistingImportManifest(setupPlan.ManifestPath)
		if readErr != nil {
			return 1, readErr
		}
		unknownFields, fieldErr := manifestHasUnknownFields(rawManifest)
		if fieldErr != nil {
			return 1, fieldErr
		}
		lock, lockErr := codebox.ReadLockfile(codebox.DefaultFS, setupPlan.LockPath)
		if lockErr != nil {
			return 1, lockErr
		}
		if unknownFields || !importMatchesManifest(setupPlan.Answers, existing) ||
			codebox.ValidateDependencySet(existing, lock) != nil ||
			!importSkillLocksMatch(setupPlan.SkillLocks, lock.Skills) {
			return 1, fmt.Errorf("%s contient déjà une configuration codebox différente de celle de l'import ; examinez-la avec 'codebox init' plutôt que de l'écraser", setupPlan.ManifestPath)
		}
		existingMatches = true
	} else if !os.IsNotExist(err) {
		return 1, err
	}

	if !existingMatches && plan.Model != "" && codebox.ModelRefProvider(plan.Model) == codebox.ProviderAlbert {
		// Only Albert references are validated against the Albert catalogue:
		// an imported OpenAI-compatible model lives on its own platform.
		warning, err := catalogueModelWarningFn(plan.Root, plan.Model)
		if err != nil {
			return 1, fmt.Errorf("valider le modèle importé : %w", err)
		}
		if warning != "" {
			fmt.Fprintf(os.Stderr, "Attention : %s\n", warning)
		}
	}
	apply := func() error {
		return applyAlbertCodeImport(plan, wizard, setupPlan, opts.importAlbertKey, existingMatches)
	}
	if opts.importAlbertKey {
		if err := withAlbertCredentialImportLock(apply); err != nil {
			return 1, err
		}
	} else if err := apply(); err != nil {
		return 1, err
	}
	if len(plan.Platforms) > 0 {
		if err := mergeImportedProviders(plan); err != nil {
			return 1, err
		}
	}
	warnUnapprovedImportInputs(plan.Root)
	return 0, nil
}

// mergeImportedProviders records the OpenAI-compatible platform recovered
// from the legacy OpenCode config in the user settings, so the imported model
// is usable. The key itself is never imported: it must be stored with
// 'codebox auth add openai-compatible'.
func mergeImportedProviders(plan codebox.AlbertCodeImport) error {
	path, err := codebox.UserSettingsPath()
	if err != nil {
		return err
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, path)
	if err != nil {
		return err
	}
	if len(us.Providers) == 0 {
		// Preserve the host's existing implicit Albert connection: settings
		// written before #180 have no providers list, and an empty list means
		// Albert is connected. On a truly fresh install, only retain Albert
		// when the imported configuration actually declared it.
		if plan.AlbertPresent || us.DefaultModel != "" || len(us.Models) > 0 || us.GitName != "" || us.GitEmail != "" || us.CredentialRef != "" {
			us.Providers = codebox.DefaultProviderSettings()
		}
	}
	for _, platform := range plan.Platforms {
		configured := false
		for _, existing := range us.Providers {
			if existing.Kind == platform.Kind {
				if existing.BaseURL != platform.BaseURL {
					return fmt.Errorf("une plateforme compatible OpenAI est déjà configurée pour %s ; importer %s attacherait ses modèles au mauvais point d'accès. Supprimez ou alignez d'abord la plateforme existante", existing.BaseURL, platform.BaseURL)
				}
				configured = true
				break
			}
		}
		if !configured {
			us.Providers = append(us.Providers, platform)
		}
	}
	for _, model := range plan.ProviderModels {
		if !containsString(us.Models, model) {
			us.Models = append(us.Models, model)
		}
	}
	for ref, apiID := range plan.ProviderModelIDs {
		if us.ProviderModelIDs == nil {
			us.ProviderModelIDs = map[string]string{}
		}
		us.ProviderModelIDs[ref] = apiID
	}
	if plan.Model != "" {
		us.DefaultModel = plan.Model
	}
	if _, err := codebox.ResolveProviders(us.Providers); err != nil {
		return fmt.Errorf("la configuration de plateforme fusionnée est invalide : %w", err)
	}
	if err := codebox.WriteUserSettings(codebox.DefaultFS, path, us); err != nil {
		return err
	}
	fmt.Printf("Plateforme importée dans les réglages : %s (clé à stocker avec codebox auth add openai-compatible)\n", plan.Platforms[0].Name)
	return nil
}

// validateImportedPlatforms refuses an import that would conflict with the
// host's existing OpenAI-compatible platform, or that would write an endpoint
// the resolver rejects (and therefore make every later launch fail).
func validateImportedPlatforms(plan codebox.AlbertCodeImport) error {
	path, err := codebox.UserSettingsPath()
	if err != nil {
		return err
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, path)
	if err != nil {
		return err
	}
	existing := us.Providers
	if len(existing) == 0 {
		if plan.AlbertPresent || us.DefaultModel != "" || len(us.Models) > 0 || us.GitName != "" || us.GitEmail != "" || us.CredentialRef != "" {
			existing = codebox.DefaultProviderSettings()
		}
	}
	for _, platform := range plan.Platforms {
		if _, err := codebox.ResolveProvider(platform); err != nil {
			return fmt.Errorf("la plateforme importée est invalide : %w", err)
		}
		matched := false
		for _, current := range existing {
			if current.Kind == platform.Kind {
				if current.BaseURL != platform.BaseURL {
					return fmt.Errorf("une plateforme compatible OpenAI est déjà configurée pour %s ; importer %s attacherait ses modèles au mauvais point d'accès. Supprimez ou alignez d'abord la plateforme existante", current.BaseURL, platform.BaseURL)
				}
				matched = true
			}
		}
		if !matched {
			existing = append(existing, platform)
		}
	}
	// The complete set must satisfy the same invariants as a launch: in
	// particular, two platforms may not share a destination host.
	if _, err := codebox.ResolveProviders(existing); err != nil {
		return fmt.Errorf("la plateforme importée entre en conflit avec la configuration actuelle : %w", err)
	}
	return nil
}

// warnUnapprovedImportInputs lists the execution-relevant project inputs that
// the launch trust gate will block on, so a successful import does not leave
// the user surprised by the first run (#154). It mirrors the launch error's
// wording on purpose. Read-only: it computes, never approves.
// warnUnapprovedImportInputs announces execution inputs that still need trust
// approval. The recommendation names the project directory because 'trust
// approve' resolves its project from the current directory: an import into a
// different --root must not suggest approving whatever project the caller
// happens to stand in.
func warnUnapprovedImportInputs(projectRoot string) {
	unapproved, err := codebox.DiscoverUnapprovedInputs(codebox.DefaultFS, codebox.DefaultStateDir(), projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Attention : impossible de vérifier quelles entrées du projet attendent encore une approbation de confiance : %v\n", err)
		return
	}
	if len(unapproved) == 0 {
		return
	}
	var lines []string
	for _, u := range unapproved {
		lines = append(lines, fmt.Sprintf("  %s (%s)", u.Path, u.Reason))
	}
	fmt.Printf("Ce projet déclare des entrées qui exécutent du code ou lancent des processus (greffons OpenCode et commandes MCP, extensions et réglages Pi), et elles ne sont pas approuvées pour leur contenu actuel :\n%s\n"+
		"Le premier lancement de 'codebox' s'arrêtera pour cela. Relisez-les, puis lancez 'codebox trust approve' depuis %s pour enregistrer l'approbation du contenu actuel.\n",
		strings.Join(lines, "\n"), projectRoot)
}

func applyAlbertCodeImport(plan codebox.AlbertCodeImport, wizard codebox.InitWizard, setupPlan codebox.InitPlan, importKey, existingMatches bool) error {
	var store codebox.CredentialStore
	var credentialCreated bool
	var importedCredential string
	if importKey {
		store = importCredentialStoreFn()
		if store == nil {
			return fmt.Errorf("le trousseau natif est indisponible ; utilisez 'codebox auth add albert' pour configurer les identifiants manuellement")
		}
		data, err := readAlbertImportDotenv(filepath.Join(plan.Root, ".env"))
		if err != nil {
			return err
		}
		var available bool
		importedCredential, available = codebox.ReadLegacyAlbertCredential(string(data))
		if !available {
			if codebox.LegacyAlbertCredentialNeedsManualReview(string(data)) {
				return fmt.Errorf("le .env contient une affectation ALBERT_API_KEY non prise en charge ou répétée ; réduisez-la à une seule affectation littérale avant d'importer")
			}
			return fmt.Errorf("aucune valeur littérale ALBERT_API_KEY non vide n'a été trouvée dans le .env")
		}
		credentialCreated, err = storeImportedAlbertCredential(context.Background(), store, importedCredential)
		if err != nil {
			return err
		}
	}
	if existingMatches {
		if !setupPlan.InstructionsChanged {
			fmt.Println("Ce projet Albert Code a déjà été importé ; aucun fichier de projet n'a été modifié.")
			return nil
		}
		if err := repairImportedProjectFn(wizard, setupPlan); err != nil {
			if removeErr := rollbackImportedCredential(context.Background(), store, credentialCreated, importedCredential); removeErr != nil {
				return fmt.Errorf("réparer les instructions de skills gérées : %v ; l'identifiant fraîchement importé n'a pas pu être supprimé : %w", err, removeErr)
			}
			return fmt.Errorf("réparer les instructions de skills gérées : %w", err)
		}
		fmt.Printf("Import existant et instructions de skills gérées réconciliés dans %s.\n", setupPlan.ManifestPath)
		return nil
	}
	if err := applyImportedProjectFn(wizard, setupPlan); err != nil {
		if removeErr := rollbackImportedCredential(context.Background(), store, credentialCreated, importedCredential); removeErr != nil {
			return fmt.Errorf("appliquer l'import du projet : %v ; l'identifiant fraîchement importé n'a pas pu être supprimé : %w", err, removeErr)
		}
		return fmt.Errorf("appliquer l'import du projet : %w", err)
	}
	fmt.Printf("Réglages reconnus importés dans %s. Les fichiers de configuration hérités ont été laissés intacts.\n", setupPlan.ManifestPath)
	if len(plan.Skills) > 0 {
		fmt.Println("AGENTS.md n'a été mis à jour que par la section de skills gérées de codebox ; le texte existant a été préservé.")
	}
	return nil
}

func withAlbertCredentialImportLock(action func() error) error {
	lock := &codebox.ProjectLock{
		Path: filepath.Join(codebox.DefaultStateDir(), "albert-import-credential.lock"),
		FS:   codebox.DefaultFS,
	}
	release, err := lock.Acquire()
	if err != nil {
		return fmt.Errorf("acquérir le verrou d'import de l'identifiant Albert : %w", err)
	}
	defer release()
	return action()
}

func storeImportedAlbertCredential(ctx context.Context, store codebox.CredentialStore, value string) (bool, error) {
	stored, err := store.Get(ctx, codebox.CredentialAlbert)
	if err == nil {
		if stored != value {
			return false, fmt.Errorf("un identifiant Albert est déjà enregistré ; refus de le remplacer pendant l'import")
		}
		return false, nil
	}
	if !codebox.IsCredentialNotFound(err) {
		return false, fmt.Errorf("lire l'état de l'identifiant Albert natif : %w", err)
	}
	if err := store.Put(ctx, codebox.CredentialAlbert, value); err != nil {
		return false, fmt.Errorf("enregistrer l'identifiant Albert : %w", err)
	}
	bumpImportedCredentialGenerationFn(codebox.CredentialAlbert)
	// Record the store the import wrote to, so later starts read the value from
	// there rather than re-probing a native adapter this host may not be able to
	// answer. Best-effort, like the generation bump: the value is already
	// stored.
	_ = codebox.RecordCredentialStore(codebox.DefaultStateDir(), codebox.CredentialAlbert, store.Kind())
	return true, nil
}

func rollbackImportedCredential(ctx context.Context, store codebox.CredentialStore, created bool, expected string) error {
	if !created {
		return nil
	}
	stored, err := store.Get(ctx, codebox.CredentialAlbert)
	if err != nil {
		if codebox.IsCredentialNotFound(err) {
			return nil
		}
		return err
	}
	if stored != expected {
		return fmt.Errorf("l'identifiant Albert a changé pendant l'import ; refus de le supprimer")
	}
	if err := store.Remove(ctx, codebox.CredentialAlbert); err != nil && !codebox.IsCredentialNotFound(err) {
		return err
	}
	// The rollback removed the entry it just created, so the record must not
	// keep pointing at a store that no longer holds it. Scoped to this store,
	// like `auth remove`.
	_ = codebox.ForgetCredentialStore(codebox.DefaultStateDir(), codebox.CredentialAlbert, store.Kind())
	bumpImportedCredentialGenerationFn(codebox.CredentialAlbert)
	return nil
}

func importMatchesManifest(expected codebox.InitAnswers, existing codebox.ProjectManifest) bool {
	if existing.Project != "" || existing.Storage != "" {
		return false
	}
	if existing.Runtime != string(expected.Runtime) || existing.Isolation != string(expected.Isolation) {
		return false
	}
	if existing.Model != expected.Model || existing.SmallModel != expected.SmallModel || existing.CPUs != expected.CPUs || existing.MemoryMB != expected.MemoryMB {
		return false
	}
	// The harness is compared through its resolver: a manifest that says
	// nothing and one that names the default describe the same project, and an
	// import must not silently replace another harness with OpenCode.
	existingHarness, err := codebox.ResolveHarness(existing.Harness)
	if err != nil {
		return false
	}
	expectedHarness, err := codebox.ResolveHarness(expected.Harness)
	if err != nil || existingHarness != expectedHarness {
		return false
	}
	if existing.SkillsLocalOnly || !sameStrings(existing.Skills, expected.Skills) {
		return false
	}
	if !sameStrings(existing.MCPConnectors, expected.MCPConnectors) {
		return false
	}
	return existing.CredentialRef == expected.CredentialRef
}

func importSkillLocksMatch(expected, actual map[string]codebox.SkillLock) bool {
	for id, expectedLock := range expected {
		if actualLock, ok := actual[id]; !ok || actualLock != expectedLock {
			return false
		}
	}
	return true
}

func manifestHasUnknownFields(raw []byte) (bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false, fmt.Errorf("lire les champs du manifeste de projet existant : %w", err)
	}
	known := map[string]bool{
		"schemaVersion": true, "project": true, "runtime": true, "isolation": true,
		"model": true, "harness": true, "smallModel": true, "cpus": true, "memoryMB": true, "credentialRef": true,
		"storage": true, "skills": true, "skillsLocalOnly": true,
		"mcpConnectors": true, "dependencySetId": true,
	}
	for name := range fields {
		if !known[name] {
			return true, nil
		}
	}
	return false, nil
}

func readExistingImportManifest(path string) ([]byte, error) {
	data, err := codebox.ReadAlbertCodeImportFile(path)
	if err != nil {
		return nil, fmt.Errorf("lire le manifeste d'import existant %s : %w", path, err)
	}
	return data, nil
}

func sameStrings(a, b []string) bool {
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

func readAlbertImportDotenv(path string) ([]byte, error) {
	data, err := codebox.ReadAlbertCodeImportFile(path)
	if err != nil {
		return nil, fmt.Errorf("lire le .env du projet : %w", err)
	}
	return data, nil
}
