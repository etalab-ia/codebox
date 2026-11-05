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
	"github.com/etalab-ia/just-code/internal/justcode"
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
	Parsed      justcode.ProjectDotenv
	Preview     justcode.ProjectSecretImportResult
	Fingerprint [32]byte
}

var projectSecretsTTYFn = func() bool { return wizardHuhFn() }
var projectSecretsConfirmFn = huhConfirm
var projectSecretsStateDirFn = justcode.DefaultStateDir
var projectSecretsCredentialStoreFn = func(fallback bool) (justcode.CredentialStore, error) {
	if fallback {
		return justcode.ConsentFileStore()
	}
	store := justcode.DefaultCredentialStore()
	if store == nil {
		return nil, fmt.Errorf("no native credential store is available; use --fallback only after consenting to the owner-only plaintext store")
	}
	return store, nil
}
var projectSecretsStoreFactoryFn = justcode.NewProjectSecretStore

func parseProjectSecretsImportArgs(args []string) (projectSecretsImportOptions, error) {
	opts := projectSecretsImportOptions{Root: "."}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--root", "--profile", "--file":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("%s requires a value", a)
			}
			i++
			value := args[i]
			switch a {
			case "--root":
				if seen[a] {
					return opts, fmt.Errorf("--root may be given only once")
				}
				opts.Root = value
			case "--profile":
				if seen[a] {
					return opts, fmt.Errorf("--profile may be given only once")
				}
				opts.Profile = value
			case "--file":
				opts.Files = append(opts.Files, value)
			}
			seen[a] = true
		case "--apply", "--yes", "--fallback":
			if seen[a] {
				return opts, fmt.Errorf("%s may be given only once", a)
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
			return opts, fmt.Errorf("unknown secrets import option %q", a)
		}
	}
	if opts.Yes && !opts.Apply {
		return opts, fmt.Errorf("--yes requires --apply")
	}
	return opts, nil
}

func secretsCmd(args []string) (int, error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printSecretsUsage()
		return 0, nil
	}
	if args[0] != "import" {
		if args[0] == "bindings" {
			return secretsBindingsCmd(args[1:])
		}
		if args[0] == "profile" {
			return secretsProfileCmd(args[1:])
		}
		return 2, fmt.Errorf("unknown secrets command %q (expected import, bindings or profile)", args[0])
	}
	return projectSecretsImportCmd(args[1:])
}

func printSecretsUsage() {
	fmt.Println("Usage: just-code secrets import [--root PATH] --profile NAME [--file PATH ...] [--apply [--yes]] [--fallback]")
	fmt.Println("       just-code secrets bindings list | approve <profile> <component> <NAME> --destination <host> | revoke <profile> <component> <NAME>")
	fmt.Println("       just-code secrets profile <name> | --clear")
	fmt.Println("Preview is read-only. --apply stores literal values in the selected project/profile/file scope; it does not approve or deliver them to the guest.")
	fmt.Println("Approving a binding (secrets bindings approve) is the separate host-local step that lets the sandbox proxy expose one imported value toward specific destinations.")
	fmt.Println("Without --file, an interactive terminal offers bounded dotenv-file discovery. Non-interactive use must name each --file.")
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
		return 2, fmt.Errorf("--yes requires --apply")
	}
	if opts.Apply && !opts.Yes && !tty {
		return 2, fmt.Errorf("non-interactive apply requires explicit --yes")
	}
	project, err := justcode.DiscoverProject(opts.Root)
	if err != nil {
		return 1, err
	}
	if opts.Profile == "" {
		if !tty {
			return 2, fmt.Errorf("non-interactive import requires --profile NAME")
		}
		opts.Profile, err = projectSecretsAskProfile()
		if err != nil {
			return 1, err
		}
	}
	if !justcode.IsValidProjectSecretProfile(opts.Profile) {
		return 2, fmt.Errorf("profile must use 1–64 ASCII letters, digits, hyphens or underscores")
	}
	if strings.EqualFold(opts.Profile, "production") || strings.EqualFold(opts.Profile, "prod") {
		fmt.Fprintln(os.Stderr, "Warning: production profile selected explicitly; verify that these are the intended project credentials.")
	}
	if opts.Apply && opts.Fallback {
		fmt.Fprintln(os.Stderr, "Warning: --fallback stores values in a local plaintext-equivalent JSON file; just-code does not encrypt that file.")
	}
	if len(opts.Files) == 0 {
		if !tty {
			return 2, fmt.Errorf("non-interactive import requires one or more --file PATH arguments")
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
			return 1, fmt.Errorf("recover the previous project-secret operation before previewing: %w", err)
		}
	}
	for i := range files {
		file := &files[i]
		preview, err := store.Preview(ctx, opts.Profile, file.Component, file.Parsed.Values)
		if err != nil {
			return 1, fmt.Errorf("preview %s: %w", file.Component, err)
		}
		file.Preview = preview
		printProjectSecretsPreview(project.Root, opts.Profile, *file, preview)
	}
	if !opts.Apply {
		fmt.Println("Preview only. No credential values were displayed or stored. Use --apply to continue.")
		return 0, nil
	}
	if !opts.Yes {
		description := "The source files stay unchanged. This does not approve runtime delivery."
		if opts.Fallback {
			description += " The selected file store is plaintext-equivalent and protected only by filesystem permissions."
		}
		confirm, err := projectSecretsConfirmFn("Store these values?", description, false)
		if err != nil {
			return 1, err
		}
		if !confirm {
			fmt.Println("Cancelled. No credentials stored.")
			return 0, nil
		}
	}
	// The user may have edited a dotenv file while reviewing a TTY preview.
	// Re-read every source and fail closed before the first store write.
	freshFiles, err := readProjectSecretsFiles(project.Root, opts.Files)
	if err != nil {
		return 1, fmt.Errorf("recheck the selected dotenv files: %w", err)
	}
	for i := range files {
		if i >= len(freshFiles) || files[i].Component != freshFiles[i].Component || files[i].Fingerprint != freshFiles[i].Fingerprint {
			return 1, fmt.Errorf("%s changed after preview; run the import again to review its current contents", files[i].Component)
		}
		freshFiles[i].Preview = files[i].Preview
	}
	files = freshFiles
	for _, file := range files {
		result, err := store.ImportIfRevision(ctx, opts.Profile, file.Component, file.Parsed.Values, file.Preview.Revision)
		if err != nil {
			if result.Committed {
				return 1, fmt.Errorf("stored %s, but cleanup is pending: %w", file.Component, err)
			}
			return 1, fmt.Errorf("import %s failed; earlier listed files may already be stored: %w", file.Component, err)
		}
		fmt.Printf("Stored %s (%d added, %d changed, %d unchanged, %d removed).\n", file.Component, result.Added, result.Replaced, result.Unchanged, result.Removed)
	}
	fmt.Println("Secrets stored for this project/profile. Runtime delivery remains disabled until separately configured.")
	return 0, nil
}

func projectSecretsAskProfile() (string, error) {
	var profile string
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Environment profile").Description("Use development/test by default. Production is only selected explicitly.").Value(&profile),
	)))
	return strings.TrimSpace(profile), err
}

func projectSecretsSelectFiles(candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no dotenv candidates found in the project")
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
		return nil, fmt.Errorf("no dotenv files selected")
	}
	return selected, nil
}

// newDotenvFilesMultiSelect carries the selection keys in the description for
// the same reason as the init wizard's MultiSelects (#156): Enter only
// confirms the list.
func newDotenvFilesMultiSelect(options []huh.Option[string], value *[]string) *huh.MultiSelect[string] {
	return huh.NewMultiSelect[string]().
		Title("Dotenv files").
		Description("Choisissez les fichiers du projet dont vous voulez importer les secrets ; la détection ne les lit ni ne les importe automatiquement. Espace ou x pour cocher/décocher ; Entrée pour valider la liste.").
		Options(options...).
		Value(value)
}

func discoverProjectDotenvFiles(root string) ([]string, error) {
	var candidates []string
	skipDirs := map[string]bool{
		".git": true, ".just-code": true, "node_modules": true, "vendor": true,
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
			return fmt.Errorf("project file discovery exceeded %d entries; select dotenv files explicitly with --file", maxScannedEntries)
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
		if entry.Type()&os.ModeSymlink != 0 || !justcode.IsProjectDotenvName(entry.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		candidates = append(candidates, filepath.ToSlash(rel))
		if len(candidates) > maxCandidates {
			return fmt.Errorf("project contains more than %d dotenv candidates; select files explicitly with --file", maxCandidates)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover dotenv candidates: %w", err)
	}
	sort.Strings(candidates)
	return candidates, nil
}

func readProjectSecretsFiles(root string, inputs []string) ([]projectSecretsFile, error) {
	seen := map[string]bool{}
	files := make([]projectSecretsFile, 0, len(inputs))
	for _, input := range inputs {
		if input == "" {
			return nil, fmt.Errorf("dotenv path must not be empty")
		}
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve dotenv path: %w", err)
		}
		before, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("read selected dotenv file %s: %w", input, err)
		}
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("selected dotenv file %s must be a regular non-symlink file", input)
		}
		canonical, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("resolve selected dotenv file %s: %w", input, err)
		}
		rel, err := filepath.Rel(root, canonical)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("selected dotenv file must resolve inside the project root")
		}
		component := filepath.ToSlash(rel)
		if seen[component] {
			return nil, fmt.Errorf("dotenv file %s was selected more than once", component)
		}
		if !justcode.IsProjectDotenvName(filepath.Base(component)) {
			return nil, fmt.Errorf("selected file %s is not in the dotenv filename family", component)
		}
		file, err := os.Open(abs)
		if err != nil {
			return nil, fmt.Errorf("open selected dotenv file %s: %w", component, err)
		}
		afterOpen, statErr := file.Stat()
		afterLstat, lstatErr := os.Lstat(abs)
		if statErr != nil || lstatErr != nil || afterLstat.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, afterOpen) || !os.SameFile(afterLstat, afterOpen) || !afterOpen.Mode().IsRegular() {
			file.Close()
			return nil, fmt.Errorf("selected dotenv file changed while being opened")
		}
		if afterOpen.Size() > justcode.MaxProjectDotenvBytes {
			file.Close()
			return nil, fmt.Errorf("selected dotenv file %s exceeds the 1 MiB import limit", component)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, justcode.MaxProjectDotenvBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("read selected dotenv file %s failed", component)
		}
		if len(data) > justcode.MaxProjectDotenvBytes {
			return nil, fmt.Errorf("selected dotenv file %s exceeds the 1 MiB import limit", component)
		}
		parsed, err := justcode.ParseProjectDotenv(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", component, err)
		}
		seen[component] = true
		files = append(files, projectSecretsFile{Component: component, Parsed: parsed, Fingerprint: sha256.Sum256(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Component < files[j].Component })
	return files, nil
}

func printProjectSecretsPreview(root, profile string, file projectSecretsFile, result justcode.ProjectSecretImportResult) {
	keys := make([]string, 0, len(file.Parsed.Values))
	for key := range file.Parsed.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fmt.Printf("Project %s | profile %s | %s\n", displayProjectPath(root), profile, file.Component)
	fmt.Printf("  Variables: %s\n", strings.Join(keys, ", "))
	fmt.Printf("  Plan: %d added, %d changed, %d unchanged, %d removed\n", result.Added, result.Replaced, result.Unchanged, result.Removed)
	if len(file.Parsed.Duplicates) > 0 {
		fmt.Printf("  Duplicate assignments (last definition wins): %s\n", strings.Join(file.Parsed.Duplicates, ", "))
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
