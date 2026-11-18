package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/huh/v2"
	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type projectSecretsImportOptions struct {
	Root     string
	Profile  string
	Files    []string
	Apply    bool
	Yes      bool
	Fallback bool
}

type projectSecretsFile struct {
	Component   string
	Parsed      codebox.ProjectDotenv
	Preview     codebox.ProjectSecretImportResult
	Fingerprint [32]byte
}

var projectSecretsTTYFn = func() bool { return wizardHuhFn() }
var projectSecretsConfirmFn = huhConfirm
var projectSecretsStateDirFn = codebox.DefaultStateDir
var projectSecretsCredentialStoreFn = func(fallback bool) (codebox.CredentialStore, error) {
	if fallback {
		return codebox.ConsentFileStore()
	}
	store := codebox.DefaultCredentialStore()
	if store == nil {
		return nil, fmt.Errorf("aucun trousseau natif n'est disponible ; n'utilisez --fallback qu'après avoir accepté le magasin en clair réservé à votre utilisateur")
	}
	return store, nil
}
var projectSecretsStoreFactoryFn = codebox.NewProjectSecretStore

func parseProjectSecretsImportArgs(args []string) (projectSecretsImportOptions, error) {
	opts := projectSecretsImportOptions{Root: "."}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--root", "--profile", "--file":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("%s exige une valeur", a)
			}
			i++
			value := args[i]
			switch a {
			case "--root":
				if seen[a] {
					return opts, fmt.Errorf("--root ne peut être indiqué qu'une fois")
				}
				opts.Root = value
			case "--profile":
				if seen[a] {
					return opts, fmt.Errorf("--profile ne peut être indiqué qu'une fois")
				}
				opts.Profile = value
			case "--file":
				opts.Files = append(opts.Files, value)
			}
			seen[a] = true
		case "--apply", "--yes", "--fallback":
			if seen[a] {
				return opts, fmt.Errorf("%s ne peut être indiqué qu'une fois", a)
			}
			seen[a] = true
			switch a {
			case "--apply":
				opts.Apply = true
			case "--yes":
				opts.Yes = true
			case "--fallback":
				opts.Fallback = true
			}
		default:
			return opts, fmt.Errorf("option inconnue de secrets import %q", a)
		}
	}
	if opts.Yes && !opts.Apply {
		return opts, fmt.Errorf("--yes exige --apply")
	}
	return opts, nil
}

func secretsCmd(args []string) (int, error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSecretsUsage()
		return 0, nil
	}
	switch args[0] {
	case "import":
		return projectSecretsImportCmd(args[1:])
	case "bindings":
		return secretsBindingsCmd(args[1:])
	case "profile":
		return secretsProfileCmd(args[1:])
	case "login", "logout", "check", "init", "push", "members":
		return secretsRemoteCmd(args)
	default:
		return 2, fmt.Errorf("commande inconnue de secrets %q", args[0])
	}
}

func printSecretsUsage() {
	fmt.Println("Usage : codebox secrets import [--root PATH] --profile NAME [--file PATH ...] [--apply [--yes]] [--fallback]")
	fmt.Println("       codebox secrets bindings list | approve <profile> <component> <NAME> --destination <host> | revoke <profile> <component> <NAME>")
	fmt.Println("       codebox secrets profile <name> | --clear")
	fmt.Println("       codebox secrets login [--terminal] | logout | check | init | push | members <list|add|remove>")
	fmt.Println("L'aperçu est en lecture seule. --apply enregistre les valeurs littérales dans le périmètre projet/profil/fichier sélectionné ; il n'approuve ni ne livre aucune valeur à la machine isolée.")
	fmt.Println("Approuver une liaison (secrets bindings approve) est l'étape distincte, locale au poste, qui autorise le proxy de la machine isolée à exposer une valeur importée vers des destinations précises.")
	fmt.Println("Les secrets de développement partagés passent par ProConnect et le codebox-server ; les secrets de production sont hors périmètre.")
	fmt.Println("Sans --file, un terminal interactif propose une découverte bornée des fichiers dotenv. Un usage non interactif doit nommer chaque --file.")
}

func projectSecretsImportCmd(args []string) (int, error) {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			printSecretsUsage()
			return 0, nil
		}
	}
	opts, err := parseProjectSecretsImportArgs(args)
	if err != nil {
		return 2, err
	}
	tty := projectSecretsTTYFn()
	if !opts.Apply && opts.Yes {
		return 2, fmt.Errorf("--yes exige --apply")
	}
	if opts.Apply && !opts.Yes && !tty {
		return 2, fmt.Errorf("l'application non interactive exige --yes explicitement")
	}
	project, err := codebox.DiscoverProject(opts.Root)
	if err != nil {
		return 1, err
	}
	if opts.Profile == "" {
		if !tty {
			return 2, fmt.Errorf("l'import non interactif exige --profile NAME")
		}
		opts.Profile, err = projectSecretsAskProfile()
		if err != nil {
			return 1, err
		}
	}
	if !codebox.IsValidProjectSecretProfile(opts.Profile) {
		return 2, fmt.Errorf("le profil doit se composer de 1 à 64 lettres ASCII, chiffres, tirets ou tirets bas")
	}
	if strings.EqualFold(opts.Profile, "production") || strings.EqualFold(opts.Profile, "prod") {
		fmt.Fprintln(os.Stderr, "Attention : profil de production sélectionné explicitement ; vérifiez qu'il s'agit bien des identifiants du projet voulus.")
	}
	if opts.Apply && opts.Fallback {
		fmt.Fprintln(os.Stderr, "Attention : --fallback enregistre les valeurs dans un fichier JSON local équivalent à du texte en clair ; codebox ne chiffre pas ce fichier.")
	}
	if len(opts.Files) == 0 {
		if !tty {
			return 2, fmt.Errorf("l'import non interactif exige un ou plusieurs arguments --file PATH")
		}
		candidates, err := discoverProjectDotenvFiles(project.Root)
		if err != nil {
			return 1, err
		}
		opts.Files, err = projectSecretsSelectFiles(candidates)
		if err != nil {
			return 1, err
		}
	}
	files, err := readProjectSecretsFiles(project.Root, opts.Files)
	if err != nil {
		return 1, err
	}
	credentialStore, err := projectSecretsCredentialStoreFn(opts.Fallback)
	if err != nil {
		return 1, err
	}
	store, err := projectSecretsStoreFactoryFn(project.Root, projectSecretsStateDirFn(), credentialStore)
	if err != nil {
		return 1, err
	}
	ctx := context.Background()
	if opts.Apply {
		// An earlier explicit import may have stopped between writing its
		// journal and cleanup. Apply runs that recovery before showing a new
		// preview; preview-only commands remain read-only.
		if err := store.Recover(ctx); err != nil {
			return 1, fmt.Errorf("récupérez l'opération précédente sur les secrets du projet avant l'aperçu : %w", err)
		}
	}
	for i := range files {
		file := &files[i]
		preview, err := store.Preview(ctx, opts.Profile, file.Component, file.Parsed.Values)
		if err != nil {
			return 1, fmt.Errorf("aperçu %s : %w", file.Component, err)
		}
		file.Preview = preview
		printProjectSecretsPreview(project.Root, opts.Profile, *file, preview)
	}
	if !opts.Apply {
		fmt.Println("Aperçu uniquement. Aucune valeur d'identifiant n'a été affichée ni enregistrée. Utilisez --apply pour continuer.")
		return 0, nil
	}
	if !opts.Yes {
		description := "Les fichiers sources restent inchangés. Cela n'approuve pas la livraison par le runtime."
		if opts.Fallback {
			description += " Le magasin de fichiers sélectionné équivaut à du texte en clair et n'est protégé que par les permissions du système de fichiers."
		}
		confirm, err := projectSecretsConfirmFn("Enregistrer ces valeurs ?", description, false)
		if err != nil {
			return 1, err
		}
		if !confirm {
			fmt.Println("Annulé. Aucun identifiant enregistré.")
			return 0, nil
		}
	}
	// The user may have edited a dotenv file while reviewing a TTY preview.
	// Re-read every source and fail closed before the first store write.
	freshFiles, err := readProjectSecretsFiles(project.Root, opts.Files)
	if err != nil {
		return 1, fmt.Errorf("revérifiez les fichiers dotenv sélectionnés : %w", err)
	}
	for i := range files {
		if i >= len(freshFiles) || files[i].Component != freshFiles[i].Component || files[i].Fingerprint != freshFiles[i].Fingerprint {
			return 1, fmt.Errorf("%s a changé après l'aperçu ; relancez l'import pour en revoir le contenu actuel", files[i].Component)
		}
		freshFiles[i].Preview = files[i].Preview
	}
	files = freshFiles
	for _, file := range files {
		result, err := store.ImportIfRevision(ctx, opts.Profile, file.Component, file.Parsed.Values, file.Preview.Revision)
		if err != nil {
			if result.Committed {
				return 1, fmt.Errorf("%s enregistré, mais le nettoyage est en attente : %w", file.Component, err)
			}
			return 1, fmt.Errorf("échec de l'import de %s ; les fichiers listés avant peuvent déjà être enregistrés : %w", file.Component, err)
		}
		fmt.Printf("%s enregistré (%d valeurs ajoutées, %d modifiées, %d inchangées, %d supprimées).\n", file.Component, result.Added, result.Replaced, result.Unchanged, result.Removed)
	}
	fmt.Println("Secrets enregistrés pour ce projet/profil. La livraison par le runtime reste désactivée tant qu'elle n'est pas configurée séparément.")
	return 0, nil
}

func projectSecretsAskProfile() (string, error) {
	var profile string
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Profil d'environnement").Description("Utilisez development/test par défaut. La production n'est choisie qu'explicitement.").Value(&profile),
	)))
	return strings.TrimSpace(profile), err
}

func projectSecretsSelectFiles(candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("aucun fichier dotenv candidat trouvé dans le projet")
	}
	options := make([]huh.Option[string], len(candidates))
	for i, candidate := range candidates {
		options[i] = huh.NewOption(candidate, candidate)
	}
	var selected []string
	err := huhRun(huh.NewForm(huh.NewGroup(
		newDotenvFilesMultiSelect(options, &selected),
	)))
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("aucun fichier dotenv sélectionné")
	}
	return selected, nil
}

// newDotenvFilesMultiSelect carries the selection keys in the description for
// the same reason as the init wizard's MultiSelects (#156): Enter only
// confirms the list.
func newDotenvFilesMultiSelect(options []huh.Option[string], value *[]string) *huh.MultiSelect[string] {
	return huh.NewMultiSelect[string]().
		Title("Fichiers dotenv").
		Description("Choisissez les fichiers du projet dont vous voulez importer les secrets ; la détection ne les lit ni ne les importe automatiquement. Espace ou x pour cocher/décocher ; Entrée pour valider la liste.").
		Options(options...).
		Value(value)
}

func discoverProjectDotenvFiles(root string) ([]string, error) {
	var candidates []string
	skipDirs := map[string]bool{
		".git": true, ".codebox": true, "node_modules": true, "vendor": true,
		"dist": true, "build": true, "target": true, ".next": true,
		".venv": true, "venv": true, "coverage": true,
	}
	const maxScannedEntries, maxDepth, maxCandidates = 50000, 8, 256
	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		scanned++
		if scanned > maxScannedEntries {
			return fmt.Errorf("la découverte des fichiers du projet a dépassé %d entrées ; sélectionnez les fichiers dotenv explicitement avec --file", maxScannedEntries)
		}
		if entry.IsDir() {
			if path != root && skipDirs[strings.ToLower(entry.Name())] {
				return filepath.SkipDir
			}
			if path != root {
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				if strings.Count(rel, string(filepath.Separator)) >= maxDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !codebox.IsProjectDotenvName(entry.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		candidates = append(candidates, filepath.ToSlash(rel))
		if len(candidates) > maxCandidates {
			return fmt.Errorf("le projet contient plus de %d fichiers dotenv candidats ; sélectionnez les fichiers explicitement avec --file", maxCandidates)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("découverte des fichiers dotenv candidats : %w", err)
	}
	sort.Strings(candidates)
	return candidates, nil
}

func readProjectSecretsFiles(root string, inputs []string) ([]projectSecretsFile, error) {
	seen := map[string]bool{}
	files := make([]projectSecretsFile, 0, len(inputs))
	for _, input := range inputs {
		if input == "" {
			return nil, fmt.Errorf("le chemin dotenv ne doit pas être vide")
		}
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("résolution du chemin dotenv : %w", err)
		}
		before, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("lecture du fichier dotenv sélectionné %s : %w", input, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("le fichier dotenv sélectionné %s doit être un fichier ordinaire, sans lien symbolique", input)
		}
		canonical, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("résolution du fichier dotenv sélectionné %s : %w", input, err)
		}
		rel, err := filepath.Rel(root, canonical)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("le fichier dotenv sélectionné doit se résoudre à l'intérieur de la racine du projet")
		}
		component := filepath.ToSlash(rel)
		if seen[component] {
			return nil, fmt.Errorf("le fichier dotenv %s a été sélectionné plusieurs fois", component)
		}
		if !codebox.IsProjectDotenvName(filepath.Base(component)) {
			return nil, fmt.Errorf("le fichier sélectionné %s n'appartient pas à la famille de noms dotenv", component)
		}
		file, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("ouverture du fichier dotenv sélectionné %s : %w", component, err)
		}
		afterOpen, statErr := file.Stat()
		afterLstat, lstatErr := os.Lstat(abs)
		if statErr != nil || lstatErr != nil || afterLstat.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, afterOpen) || !os.SameFile(afterLstat, afterOpen) || !afterOpen.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("le fichier dotenv sélectionné a changé pendant son ouverture")
		}
		if afterOpen.Size() > codebox.MaxProjectDotenvBytes {
			file.Close()
			return nil, fmt.Errorf("le fichier dotenv sélectionné %s dépasse la limite d'import de 1 MiB", component)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, codebox.MaxProjectDotenvBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("échec de la lecture du fichier dotenv sélectionné %s", component)
		}
		if len(data) > codebox.MaxProjectDotenvBytes {
			return nil, fmt.Errorf("le fichier dotenv sélectionné %s dépasse la limite d'import de 1 MiB", component)
		}
		parsed, err := codebox.ParseProjectDotenv(data)
		if err != nil {
			return nil, fmt.Errorf("analyse de %s : %w", component, err)
		}
		seen[component] = true
		files = append(files, projectSecretsFile{Component: component, Parsed: parsed, Fingerprint: sha256.Sum256(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Component < files[j].Component })
	return files, nil
}

func printProjectSecretsPreview(root, profile string, file projectSecretsFile, result codebox.ProjectSecretImportResult) {
	keys := make([]string, 0, len(file.Parsed.Values))
	for key := range file.Parsed.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Printf("Projet %s | profil %s | %s\n", displayProjectPath(root), profile, file.Component)
	fmt.Printf("  Variables : %s\n", strings.Join(keys, ", "))
	fmt.Printf("  Plan : %d valeurs ajoutées, %d modifiées, %d inchangées, %d supprimées\n", result.Added, result.Replaced, result.Unchanged, result.Removed)
	if len(file.Parsed.Duplicates) > 0 {
		fmt.Printf("  Affectations en double (la dernière définition l'emporte) : %s\n", strings.Join(file.Parsed.Duplicates, ", "))
	}
}

func displayProjectPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	if rel == "." {
		return "~"
	}
	return "~" + string(filepath.Separator) + rel
}
