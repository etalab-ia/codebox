package codebox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	albertAgentsStart = "<!-- albert-code:agents:start -->"
	albertAgentsEnd   = "<!-- albert-code:agents:end -->"
	albertAPIEndpoint = "https://albert.api.etalab.gouv.fr/v1"
	maxImportFileSize = 1 << 20
)

var importedModelPattern = regexp.MustCompile(`^albert/[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var errUnsafeImportFile = errors.New("n'est pas un fichier régulier ; lecture refusée")

// AlbertCodeImport is a read-only interpretation of an existing Albert Code
// project. It deliberately carries names and decisions, never secret values
// or executable legacy configuration.
type AlbertCodeImport struct {
	Root                      string
	SkillNames                []string
	Skills                    []string
	MCPConnectors             []string
	Model                     string
	CredentialAvailable       bool
	CredentialNeedsReview     bool
	CredentialFile            string
	LegacyEnvKeys             []string
	RuntimeScriptPresent      bool
	ManagedAgentsPresent      bool
	MalformedAgentsMarkers    bool
	SourceFiles               []string
	ReviewItems               []string
	Warnings                  []string
	HasSkillsFile             bool
	SkillCatalogueUnavailable bool
	HasOpenCodeConfig         bool
	HasManagedAgentsFile      bool
	// Platforms and ProviderModels are the non-Albert platforms recovered
	// from an existing OpenCode configuration (an alpha-tester OpenRouter
	// setup). Models are full provider references.
	Platforms      []ProviderSettings
	ProviderModels []string
	// ProviderModelIDs maps a provider reference to the API model ID it
	// aliases, when the legacy config used one (e.g. "glm" ->
	// "z-ai/glm-4.6"). Without it, an import would silently replace the real
	// model ID with its alias.
	ProviderModelIDs map[string]string
	// AlbertPresent records that the legacy configuration declared an Albert
	// provider. An import without it must not implicitly connect Albert.
	AlbertPresent bool
}

// DiscoverAlbertCodeImport detects the documented legacy files and extracts
// only supported intent. It never writes, executes, or prints file contents.
func DiscoverAlbertCodeImport(root string) (AlbertCodeImport, error) {
	pc, err := DiscoverProject(root)
	if err != nil {
		return AlbertCodeImport{}, fmt.Errorf("racine du projet : %w", err)
	}
	plan := AlbertCodeImport{Root: pc.Root}
	if !pc.IsGit {
		return plan, fmt.Errorf("la racine du projet %s n'est pas un worktree Git ; l'import Albert Code exige un worktree Git", pc.Root)
	}
	if err := ensureImportDirectory(filepath.Join(pc.Root, ".opencode")); err != nil {
		return plan, err
	}

	legacyStateDir := filepath.Join(pc.Root, ".albert-code")
	if info, err := os.Lstat(legacyStateDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return plan, fmt.Errorf(".albert-code n'est pas un dossier régulier ; import refusé")
		}
	} else if !os.IsNotExist(err) {
		return plan, err
	}

	skillsPath := filepath.Join(pc.Root, ".albert-code", "skills.txt")
	if _, err := regularImportFile(skillsPath); err == nil {
		data, err := readImportFile(skillsPath)
		if err != nil {
			return plan, err
		}
		plan.HasSkillsFile = true
		plan.SourceFiles = append(plan.SourceFiles, ".albert-code/skills.txt")
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			name := strings.TrimSpace(line)
			if name == "" {
				continue
			}
			if !skillNamePattern.MatchString(name) {
				plan.ReviewItems = append(plan.ReviewItems, ".albert-code/skills.txt contient un nom de skill invalide")
				continue
			}
			if !seen[name] {
				seen[name] = true
				plan.SkillNames = append(plan.SkillNames, name)
			}
		}
	} else if errors.Is(err, errUnsafeImportFile) {
		plan.Warnings = append(plan.Warnings, ".albert-code/skills.txt n'est pas un fichier régulier ; il n'a pas été lu")
	} else if !os.IsNotExist(err) {
		return plan, err
	} else if info, statErr := os.Lstat(filepath.Join(pc.Root, ".albert-code")); statErr == nil && info.Mode()&os.ModeSymlink == 0 && info.IsDir() {
		plan.Warnings = append(plan.Warnings, ".albert-code existe mais skills.txt est absent ; aucune configuration partielle ne sera devinée")
	}

	selectedConfig := ""
	for _, rel := range opencodeConfigFiles {
		path := filepath.Join(pc.Root, rel)
		if _, err := regularImportFile(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if errors.Is(err, errUnsafeImportFile) {
				plan.Warnings = append(plan.Warnings, fmt.Sprintf("%s n'est pas un fichier régulier ; il n'a pas été lu", rel))
				continue
			}
			return plan, err
		}
		data, err := readImportFile(path)
		if err != nil {
			return plan, err
		}
		config, err := parseProjectOpenCodeConfig(data)
		if err != nil {
			return plan, fmt.Errorf("analyse de %s : %w", rel, err)
		}
		plan.HasOpenCodeConfig = true
		plan.SourceFiles = append(plan.SourceFiles, rel)
		selectedConfig = rel
		plan.importOpenCodeConfig(config)
		break
	}
	if plan.HasOpenCodeConfig {
		for _, rel := range opencodeConfigFiles {
			if rel == selectedConfig {
				continue
			}
			if _, err := os.Lstat(filepath.Join(pc.Root, rel)); err == nil {
				plan.ReviewItems = append(plan.ReviewItems, "plusieurs fichiers de configuration OpenCode existent ; seul le premier dans l'ordre de découverte est importé")
				break
			}
		}
	}

	agentsPath := filepath.Join(pc.Root, "AGENTS.md")
	if data, err := readImportFileIfPresent(agentsPath); errors.Is(err, errUnsafeImportFile) {
		plan.Warnings = append(plan.Warnings, "AGENTS.md n'est pas un fichier régulier ; il n'a pas été lu")
	} else if err != nil {
		return plan, err
	} else if data != nil {
		plan.HasManagedAgentsFile = true
		plan.SourceFiles = append(plan.SourceFiles, "AGENTS.md")
		starts, ends := strings.Count(string(data), albertAgentsStart), strings.Count(string(data), albertAgentsEnd)
		if starts == 1 && ends == 1 && strings.Index(string(data), albertAgentsStart) < strings.Index(string(data), albertAgentsEnd) {
			plan.ManagedAgentsPresent = true
		} else if starts > 0 || ends > 0 {
			plan.MalformedAgentsMarkers = true
			plan.ReviewItems = append(plan.ReviewItems, "AGENTS.md a des marqueurs de zone gérée Albert Code isolés ou répétés ; il sera laissé inchangé")
		}
	}

	dotenvPath := filepath.Join(pc.Root, ".env")
	if data, err := readImportFileIfPresent(dotenvPath); errors.Is(err, errUnsafeImportFile) {
		plan.CredentialFile = ".env"
		plan.Warnings = append(plan.Warnings, ".env n'est pas un fichier régulier ; il n'a pas été lu ni transféré")
	} else if err != nil {
		return plan, err
	} else if data != nil {
		plan.CredentialFile = ".env"
		plan.LegacyEnvKeys = ImportLegacyDotenv(string(data)).Keys
		_, plan.CredentialAvailable = ReadLegacyAlbertCredential(string(data))
		plan.CredentialNeedsReview = !plan.CredentialAvailable && LegacyAlbertCredentialNeedsManualReview(string(data))
		if plan.CredentialNeedsReview {
			plan.ReviewItems = append(plan.ReviewItems, ".env contient une affectation ALBERT_API_KEY non prise en charge ou répétée ; à traiter manuellement")
		}
		plan.SourceFiles = append(plan.SourceFiles, ".env (poste uniquement ; non transféré)")
	}
	if info, err := os.Lstat(filepath.Join(pc.Root, ".agent-vm.runtime.sh")); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			plan.Warnings = append(plan.Warnings, ".agent-vm.runtime.sh n'est pas un fichier régulier ; il n'a pas été inspecté")
		} else {
			plan.RuntimeScriptPresent = true
			plan.SourceFiles = append(plan.SourceFiles, ".agent-vm.runtime.sh (présence seulement ; jamais lu ni exécuté)")
		}
	} else if !os.IsNotExist(err) {
		return plan, err
	}
	plan.ReviewItems = uniqueSortedImportStrings(plan.ReviewItems)
	plan.Warnings = uniqueSortedImportStrings(plan.Warnings)
	return plan, nil
}

// ResolveSkills maps legacy basenames only to official catalogue entries.
// Unknown names remain visible as review items and are never guessed.
func (p *AlbertCodeImport) ResolveSkills(catalogue []ProjectSkill) {
	byName := make(map[string]string, len(catalogue))
	for _, skill := range catalogue {
		if strings.HasPrefix(skill.ID, "official/") {
			byName[skill.Name] = skill.ID
		}
	}
	for _, name := range p.SkillNames {
		if id, ok := byName[name]; ok {
			p.Skills = append(p.Skills, id)
		} else {
			p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("le skill hérité %q est absent du catalogue officiel actuel", name))
		}
	}
	sort.Strings(p.Skills)
	sort.Strings(p.ReviewItems)
}

func (p *AlbertCodeImport) importOpenCodeConfig(config map[string]any) {
	providerOK := false
	modelKnown := false
	if providers, ok := config["provider"].(map[string]any); ok {
		if albert, ok := providers["albert"].(map[string]any); ok {
			p.AlbertPresent = true
			options, optionsOK := albert["options"].(map[string]any)
			if optionsOK && len(options) == 2 && options["baseURL"] == albertAPIEndpoint && options["apiKey"] == "{env:ALBERT_API_KEY}" && albert["npm"] == "@ai-sdk/openai-compatible" && albert["name"] != nil {
				providerOK = true
			}
			knownProviderKeys := map[string]bool{"npm": true, "name": true, "options": true, "models": true}
			for key := range albert {
				if !knownProviderKeys[key] {
					p.ReviewItems = append(p.ReviewItems, "provider.albert contient des réglages personnalisés ; valeurs non reprises")
				}
			}
			if models, ok := albert["models"].(map[string]any); ok {
				if model, ok := config["model"].(string); ok && importedModelPattern.MatchString(model) {
					_, modelKnown = models[strings.TrimPrefix(model, "albert/")]
				}
			}
			if albert["npm"] != "@ai-sdk/openai-compatible" || albert["name"] == nil || !providerOK {
				p.ReviewItems = append(p.ReviewItems, "provider.albert contient des réglages personnalisés ; valeurs non reprises")
			}
		}
		for name, value := range providers {
			if name == "albert" {
				continue
			}
			entry, ok := value.(map[string]any)
			if !ok {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("provider.%q a une forme non prise en charge ; valeurs non reprises", name))
				continue
			}
			options, optionsOK := entry["options"].(map[string]any)
			baseURL, _ := options["baseURL"].(string)
			apiKey, _ := options["apiKey"].(string)
			if !optionsOK || entry["npm"] != "@ai-sdk/openai-compatible" || !strings.HasPrefix(baseURL, "https://") || !strings.HasPrefix(apiKey, "{env:") {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("provider.%q est personnalisé ou non pris en charge ; valeurs non reprises", name))
				continue
			}
			if len(p.Platforms) > 0 {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("provider.%q est une plateforme compatible OpenAI supplémentaire ; seule la première est importée", name))
				continue
			}
			display := name
			if name == "openrouter" {
				display = "OpenRouter"
			}
			settings := ProviderSettings{
				Kind: ProviderOpenAICompatible, Name: display, BaseURL: baseURL,
			}
			if _, err := ResolveProvider(settings); err != nil {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("impossible d'importer provider.%q : %v", name, err))
				continue
			}
			p.Platforms = append(p.Platforms, settings)
			if models, ok := entry["models"].(map[string]any); ok {
				for id, definition := range models {
					ref := ProviderModelRef(ProviderOpenAICompatible, id)
					p.ProviderModels = append(p.ProviderModels, ref)
					if fields, ok := definition.(map[string]any); ok {
						if apiID, _ := fields["modelID"].(string); apiID != "" && apiID != id {
							if p.ProviderModelIDs == nil {
								p.ProviderModelIDs = map[string]string{}
							}
							p.ProviderModelIDs[ref] = apiID
						}
					}
				}
				sort.Strings(p.ProviderModels)
			}
			if model, ok := config["model"].(string); ok && strings.HasPrefix(model, name+"/") {
				p.Model = ProviderModelRef(ProviderOpenAICompatible, strings.TrimPrefix(model, name+"/"))
				modelKnown = true
			}
			p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("plateforme %s importée ; sa clé se stocke avec : codebox auth add openai-compatible", display))
		}
	}
	if model, ok := config["model"].(string); ok && model != "" {
		if p.Model != "" {
			// An OpenAI-compatible platform already claimed the default.
		} else if providerOK && modelKnown && importedModelPattern.MatchString(model) {
			p.Model = model
		} else {
			p.ReviewItems = append(p.ReviewItems, "la sélection modèle/fournisseur est personnalisée ou non prise en charge ; valeurs non reprises")
		}
	}
	if !providerOK && config["provider"] != nil {
		p.ReviewItems = append(p.ReviewItems, "la configuration des fournisseurs est personnalisée ou non prise en charge ; valeurs non reprises")
	}

	if raw, ok := config["mcp"].(map[string]any); ok {
		for name, value := range raw {
			entry, ok := value.(map[string]any)
			if !ok {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("mcp.%q a une forme non prise en charge ; valeurs non reprises", name))
				continue
			}
			enabled, _ := entry["enabled"].(bool)
			if !enabled {
				if !legacyMCPMatches(name, entry) {
					p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("mcp.%q a des réglages personnalisés ou non pris en charge et restera dans la configuration source", name))
				}
				continue
			}
			if legacyMCPMatches(name, entry) {
				if _, removed := RemovedMCPIDs[name]; removed {
					p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("mcp.%q n'est plus proposé par codebox ; il n'est pas importé", name))
					continue
				}
				p.MCPConnectors = append(p.MCPConnectors, name)
			} else {
				p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("mcp.%q est personnalisé ou non pris en charge ; valeurs non reprises et non importées", name))
			}
		}
	}
	for _, key := range []string{"small_model", "permission"} {
		if _, ok := config[key]; ok {
			p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("le réglage OpenCode %q est conservé mais non importé", key))
		}
	}
	for key := range config {
		switch key {
		case "$schema", "model", "small_model", "provider", "mcp", "permission":
		default:
			p.ReviewItems = append(p.ReviewItems, fmt.Sprintf("le réglage OpenCode %q est conservé mais non importé", key))
		}
	}
	sort.Strings(p.MCPConnectors)
}

func uniqueSortedImportStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	sort.Strings(values)
	unique := values[:1]
	for _, value := range values[1:] {
		if value != unique[len(unique)-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func legacyMCPMatches(name string, entry map[string]any) bool {
	allowedKeys := map[string]map[string]bool{
		"data-gouv":       {"type": true, "url": true, "enabled": true},
		"context7":        {"type": true, "url": true, "enabled": true, "headers": true},
		"playwright":      {"type": true, "command": true, "enabled": true},
		"chrome-devtools": {"type": true, "command": true, "enabled": true},
	}
	allowed, ok := allowedKeys[name]
	if !ok {
		return false
	}
	for key := range entry {
		if !allowed[key] {
			return false
		}
	}
	switch name {
	case "data-gouv":
		return entry["type"] == "remote" && entry["url"] == "https://mcp.data.gouv.fr/mcp"
	case "context7":
		if entry["type"] != "remote" || entry["url"] != "https://mcp.context7.com/mcp" {
			return false
		}
		if raw, ok := entry["headers"]; ok {
			headers, ok := raw.(map[string]any)
			return ok && len(headers) == 1 && headers["Authorization"] == "Bearer {env:CONTEXT7_API_KEY}"
		}
		return true
	case "playwright":
		return entry["type"] == "local" && commandMatches(entry["command"], "npx", "-y", "@playwright/mcp@latest")
	case "chrome-devtools":
		return entry["type"] == "local" && commandMatches(entry["command"], "npx", "-y", "chrome-devtools-mcp@latest", "--headless=true", "--isolated=true")
	default:
		return false
	}
}

func commandMatches(raw any, expected ...string) bool {
	command, ok := raw.([]any)
	if !ok || len(command) != len(expected) {
		return false
	}
	for i, item := range command {
		if item != expected[i] {
			return false
		}
	}
	return true
}

func (p AlbertCodeImport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Aperçu de l'import Albert Code pour %s\n", p.Root)
	if len(p.SourceFiles) == 0 {
		b.WriteString("Aucun fichier de projet Albert Code pris en charge n'a été détecté.\n")
		for _, warning := range p.Warnings {
			fmt.Fprintf(&b, "Avertissement : %s\n", warning)
		}
		for _, item := range p.ReviewItems {
			fmt.Fprintf(&b, "À vérifier : %s\n", item)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "Sources détectées : %s\n", strings.Join(p.SourceFiles, ", "))
	fmt.Fprintf(&b, "Cible : Microsandbox, isolation complète (clone scellé dans la sandbox)\n")
	if p.Model != "" {
		fmt.Fprintf(&b, "Modèle : %s\n", p.Model)
	}
	if len(p.Platforms) > 0 {
		var names []string
		for _, platform := range p.Platforms {
			names = append(names, platform.Name)
		}
		fmt.Fprintf(&b, "Plateformes : %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, "Skills : %s\n", displayIDs(p.Skills))
	fmt.Fprintf(&b, "MCP : %s\n", displayIDs(p.MCPConnectors))
	if p.SkillCatalogueUnavailable {
		fmt.Fprintf(&b, "Skills hérités non résolus (catalogue indisponible) : %s\n", strings.Join(p.SkillNames, ", "))
	} else if p.HasSkillsFile && len(p.SkillNames) > 0 {
		b.WriteString("Les noms de skills hérités sont résolus vers le catalogue officiel épinglé actuel ; les anciennes installations dans la sandbox ne sont pas copiées.\n")
	}
	if p.ManagedAgentsPresent {
		b.WriteString("AGENTS.md : zone gérée Albert Code reconnue ; le fichier source sera conservé.\n")
	}
	if len(p.Skills) > 0 {
		b.WriteString("AGENTS.md ne recevra que la section de skills gérée par codebox ; le texte existant et la zone gérée Albert Code sont conservés.\n")
	}
	if p.CredentialFile != "" {
		fmt.Fprintf(&b, "Hygiène du poste : %s reste sur le poste et n'est pas transféré ; vérifiez s'il contient des identifiants et nettoyez-le seulement en action séparée, après avoir vérifié le magasin natif.\n", p.CredentialFile)
		if len(p.LegacyEnvKeys) > 0 {
			fmt.Fprintf(&b, "Noms de variables .env (valeurs non reprises ; aucune n'est importée implicitement) : %q\n", p.LegacyEnvKeys)
		}
	}
	if p.CredentialAvailable {
		b.WriteString("Un identifiant Albert en clair est présent dans .env ; les octets secrets ne sont pas repris. Utilisez --import-albert-key pour le stocker dans le magasin d'identifiants natif.\n")
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(&b, "Avertissement : %s\n", warning)
	}
	for _, item := range p.ReviewItems {
		fmt.Fprintf(&b, "À vérifier : %s\n", item)
	}
	b.WriteString("L'ancienne sandbox, ses sessions, les fichiers non suivis et les outils installés ne sont pas transférés. La sandbox reçoit un clone filtré par les règles de transfert par défaut (refus) ; .env n'est pas copié. Les fichiers de configuration hérités et .git/info/exclude ne sont pas modifiés.\n")
	return b.String()
}

func displayIDs(ids []string) string {
	if len(ids) == 0 {
		return "aucun"
	}
	return strings.Join(ids, ", ")
}

func readImportFile(path string) ([]byte, error) {
	file, info, err := openRegularImportFile(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > maxImportFileSize {
		return nil, fmt.Errorf("%s dépasse la limite de taille de l'importateur", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxImportFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("lecture de %s : %w", path, err)
	}
	if len(data) > maxImportFileSize {
		return nil, fmt.Errorf("%s dépasse la limite de taille de l'importateur", path)
	}
	return data, nil
}

// ReadAlbertCodeImportFile reads a bounded regular file for the import command.
func ReadAlbertCodeImportFile(path string) ([]byte, error) {
	return readImportFile(path)
}

func readImportFileIfPresent(path string) ([]byte, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return readImportFile(path)
}

func regularImportFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", errUnsafeImportFile, path)
	}
	return info, nil
}

func openRegularImportFile(path string) (*os.File, os.FileInfo, error) {
	pathInfo, err := regularImportFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("ouverture de %s : %w", path, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("inspection du fichier ouvert %s : %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
		file.Close()
		return nil, nil, fmt.Errorf("%w : %s a changé pendant l'ouverture", errUnsafeImportFile, path)
	}
	return file, openedInfo, nil
}

func ensureImportDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s n'est pas un dossier régulier ; import refusé", path)
	}
	return nil
}
