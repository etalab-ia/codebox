package justcode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// This file implements the P21 assembled diagnostics model. It is a
// read-only support report: probes are classified, values are non-secret,
// and missing configuration is reported as missing rather than invented.

// DiagnosticStatus is the display severity for one check.
type DiagnosticStatus string

const (
	DiagnosticOK     DiagnosticStatus = "ok"
	DiagnosticWarn   DiagnosticStatus = "warn"
	DiagnosticFailed DiagnosticStatus = "failed"
	DiagnosticInfo   DiagnosticStatus = "info"
)

// DiagnosticItem is one named check or fact in a section.
type DiagnosticItem struct {
	Name   string           `json:"name"`
	Value  string           `json:"value"`
	Detail string           `json:"detail,omitempty"`
	Status DiagnosticStatus `json:"status"`
}

// DiagnosticSection groups related checks without requiring callers to build
// the rendered text.
type DiagnosticSection struct {
	Title string           `json:"title"`
	Items []DiagnosticItem `json:"items"`
}

// DiagnosticsReport is the full read-only support report.
type DiagnosticsReport struct {
	Platform string              `json:"platform"`
	Fatal    bool                `json:"fatal"`
	Sections []DiagnosticSection `json:"sections"`
}

// Diagnostics collects the report. Network-facing and host-command probes
// are injectable; the production defaults never send a credential value.
type Diagnostics struct {
	Preflight   *SetupPreflight
	FS          FS
	StateDir    string
	ProjectDir  string
	LookPath    func(string) (string, error)
	Run         func(context.Context, string, ...string) (ExecResult, error)
	ProbeAlbert func(context.Context, string) CredentialProbe
	VerifyStore func(context.Context) (string, error)
	ReadAlbert  func(context.Context) (string, error)
	// ReadCredential probes one credential kind in the store chain (native
	// then the consented file fallback) and reports which store answered.
	// The value is discarded by the caller and never reaches the report.
	ReadCredential func(context.Context, CredentialKind) (string, string, error)
}

var diagnosticVersionPattern = regexp.MustCompile(`^(?:go)?v?\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.-]+)?$`)

// Collect builds the assembled support report. It does not install, write,
// start, stop, or authenticate as a side effect.
func (d Diagnostics) Collect(ctx context.Context) DiagnosticsReport {
	preflight := d.Preflight
	if preflight == nil {
		res := (SetupPreflighter{}).RunPreflight(ctx)
		preflight = &res
	}
	fs := d.FS
	if fs == nil {
		fs = DefaultFS
	}
	stateDir := d.StateDir
	if stateDir == "" {
		stateDir = DefaultStateDir()
	}
	projectDir := d.ProjectDir
	if projectDir == "" {
		projectDir = "."
	}

	report := DiagnosticsReport{Platform: preflight.Platform, Fatal: preflight.Fatal}
	report.Sections = append(report.Sections,
		hostSection(*preflight),
		d.runtimeSection(ctx, fs, stateDir, projectDir, *preflight),
		d.toolchainSection(ctx),
		d.providerSection(ctx, fs),
		d.projectSection(fs, stateDir, projectDir),
		d.credentialSection(ctx, fs, stateDir),
	)
	redactDiagnosticPaths(&report, d.ProjectDir, stateDir)
	return report
}

func redactDiagnosticPaths(report *DiagnosticsReport, projectDir, stateDir string) {
	replacements := map[string]string{
		os.Getenv("MSB_PATH"):           "$MSB_PATH",
		os.Getenv("MSB_LIBKRUNFW_PATH"): "$MSB_LIBKRUNFW_PATH",
		os.Getenv("MSB_HOME"):           "$MSB_HOME",
		os.Getenv("XDG_CONFIG_HOME"):    "$XDG_CONFIG_HOME",
		os.Getenv("XDG_STATE_HOME"):     "$XDG_STATE_HOME",
		os.Getenv("APPDATA"):            "$APPDATA",
		os.Getenv("USERPROFILE"):        "$USERPROFILE",
	}
	if home, err := os.UserHomeDir(); err == nil {
		replacements[home] = "~"
	}
	if configDir, err := os.UserConfigDir(); err == nil {
		replacements[configDir] = "$USER_CONFIG_DIR"
	}
	if stateDir != "" {
		replacements[stateDir] = "$JUST_CODE_STATE_DIR"
	}
	if projectDir != "" {
		if absolute, err := filepath.Abs(projectDir); err == nil {
			replacements[absolute] = "<project>"
		}
		if project, err := DiscoverProject(projectDir); err == nil {
			replacements[project.Root] = "<project>"
		}
	}
	keys := make([]string, 0, len(replacements))
	for path := range replacements {
		if path != "" && filepath.IsAbs(path) {
			keys = append(keys, path)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for sectionIndex := range report.Sections {
		for itemIndex := range report.Sections[sectionIndex].Items {
			item := &report.Sections[sectionIndex].Items[itemIndex]
			for _, path := range keys {
				item.Value = strings.ReplaceAll(item.Value, path, replacements[path])
				item.Detail = strings.ReplaceAll(item.Detail, path, replacements[path])
			}
		}
	}
}

func hostSection(res SetupPreflight) DiagnosticSection {
	disk := humanFreeBytes(res.DiskFreeBytes)
	if res.DiskDetail != "" {
		disk = "échec du contrôle"
	}
	return DiagnosticSection{Title: "Capacité de l'hôte", Items: []DiagnosticItem{
		{Name: "Plateforme", Value: res.Platform, Status: DiagnosticInfo},
		{Name: "Virtualisation", Value: yesNo(res.VirtualizationOK), Detail: res.VirtualizationDetail, Status: statusFor(res.VirtualizationOK)},
		{Name: "Espace disque libre", Value: disk, Detail: res.DiskDetail, Status: statusFor(res.DiskOK)},
	}}
}

func (d Diagnostics) runtimeSection(ctx context.Context, fs FS, stateDir, projectDir string, res SetupPreflight) DiagnosticSection {
	section := DiagnosticSection{Title: "Runtime et état de l'instance du projet"}
	section.Items = append(section.Items, DiagnosticItem{
		Name:   "Runtime Microsandbox géré",
		Value:  yesNo(res.RuntimeInstalled),
		Detail: runtimeInstallDetail(res.RuntimeInstalled),
		Status: runtimeInstalledStatus(res.RuntimeInstalled),
	})
	externalConfigured := os.Getenv("MSB_PATH") != "" || os.Getenv("MSB_LIBKRUNFW_PATH") != ""
	if externalConfigured {
		if err := validateInstalledMSBRuntime(ctx); err != nil {
			section.Items = append(section.Items, DiagnosticItem{
				Name: "Runtime Microsandbox explicite", Value: "échec", Detail: err.Error(), Status: DiagnosticFailed,
			})
		} else {
			section.Items = append(section.Items, DiagnosticItem{
				Name: "Runtime Microsandbox explicite", Value: "validé", Detail: "chemins et version vérifiés ; aucun fichier modifié", Status: DiagnosticOK,
			})
		}
	} else if res.RuntimeInstalled {
		if err := validateInstalledMSBRuntime(ctx); err != nil {
			section.Items = append(section.Items, DiagnosticItem{
				Name: "Intégrité du runtime géré", Value: "échec", Detail: err.Error(), Status: DiagnosticFailed,
			})
		} else {
			section.Items = append(section.Items, DiagnosticItem{
				Name: "Intégrité du runtime géré", Value: "validé", Detail: "marqueur de provenance et fichiers requis vérifiés", Status: DiagnosticOK,
			})
		}
	} else {
		section.Items = append(section.Items, DiagnosticItem{
			Name: "Runtime sélectionné", Value: "non installé", Detail: "doctor n'a rien installé ni réparé", Status: DiagnosticInfo,
		})
	}
	project, err := DiscoverProject(projectDir)
	if err != nil {
		section.Items = append(section.Items, DiagnosticItem{
			Name: "Instance du projet", Value: "indisponible", Detail: err.Error(), Status: DiagnosticWarn,
		})
		return section
	}
	state, err := ReadInstanceState(fs, instanceStatePath(stateDir, project.InstanceName()))
	if err != nil {
		section.Items = append(section.Items, DiagnosticItem{Name: "Instance du projet", Value: project.InstanceName(), Detail: err.Error(), Status: DiagnosticWarn})
		return section
	}
	section.Items = append(section.Items, DiagnosticItem{Name: "Instance du projet", Value: project.InstanceName(), Status: DiagnosticInfo})
	if state == nil {
		section.Items = append(section.Items, DiagnosticItem{Name: "État enregistré de la machine", Value: "aucun", Detail: "aucune instance gérée n'a été créée pour ce projet", Status: DiagnosticInfo})
	} else {
		section.Items = append(section.Items, DiagnosticItem{Name: "État enregistré de la machine", Value: "configuré", Detail: "les réglages fixés à la création sont enregistrés pour cette instance", Status: DiagnosticOK})
	}
	return section
}

func runtimeInstalledStatus(installed bool) DiagnosticStatus {
	if installed {
		return DiagnosticOK
	}
	return DiagnosticInfo
}

func runtimeInstallDetail(installed bool) string {
	if installed {
		return "marqueur de provenance et fichiers requis présents"
	}
	return "non installé ; lancez 'just-code start --microsandbox' ou 'just-code setup'"
}

func (d Diagnostics) toolchainSection(ctx context.Context) DiagnosticSection {
	section := DiagnosticSection{Title: "Toolchain hôte"}
	for _, tool := range []string{"git", "go", "node", "opencode"} {
		section.Items = append(section.Items, d.toolItem(ctx, tool))
	}
	section.Items = append(section.Items, DiagnosticItem{
		Name:   "SDK Microsandbox",
		Value:  MSBSDKVersion(),
		Detail: "version du SDK embarqué ; le runtime géré doit correspondre",
		Status: DiagnosticInfo,
	})
	return section
}

func (d Diagnostics) toolItem(ctx context.Context, tool string) DiagnosticItem {
	lookPath := d.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath(tool)
	if err != nil {
		status := DiagnosticFailed
		detail := "installez-le ou ajoutez-le au PATH"
		if tool == "go" {
			status, detail = DiagnosticInfo, "facultatif sur un poste utilisant un binaire just-code précompilé"
		}
		return DiagnosticItem{Name: tool, Value: "introuvable", Detail: detail, Status: status}
	}
	run := d.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) (ExecResult, error) {
			return OSRunner{}.Run(ctx, name, args...)
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, runErr := run(probeCtx, path, versionProbeArgs(tool)...)
	value := "trouvé"
	status := DiagnosticOK
	detail := ""
	if runErr != nil || res.ExitCode != 0 {
		value = "inutilisable"
		status = DiagnosticFailed
		if tool == "go" {
			status = DiagnosticInfo
		}
		detail = fmt.Sprintf("échec de la sonde de version (code de sortie %d)", res.ExitCode)
		if runErr != nil {
			detail = "impossible de lancer la sonde de version"
		}
	} else if version := firstVersionToken(res.Stdout + res.Stderr); version != "" {
		value = version
	} else {
		detail = "la sortie de version n'a pas été reconnue"
	}
	return DiagnosticItem{Name: tool, Value: value, Detail: detail, Status: status}
}

// versionProbeArgs returns the arguments that make a tool print its version.
// Almost every tool accepts --version, but Go does not: `go --version` exits 2
// with a usage message, so a machine with a working Go was reported as
// "unusable". The per-tool spelling lives here so the probe stays one call.
func versionProbeArgs(tool string) []string {
	switch tool {
	case "go":
		// `go version` is the documented form; --version is not a Go flag.
		return []string{"version"}
	default:
		return []string{"--version"}
	}
}

func firstVersionToken(output string) string {
	for _, field := range strings.Fields(output) {
		token := strings.Trim(field, "()[],:;")
		if diagnosticVersionPattern.MatchString(token) {
			return token
		}
	}
	return ""
}

// unreachedFallbackItem reports that the consented file store holds kind even
// though the read path never consulted it, and names the two ways out. It is
// what turns "credential store denied" into an actionable line on a headless
// host: the value is already on disk, so the remedy is not to store it again.
//
// It only runs on the error path, and it reports presence, never a value: the
// file store's own Get result is discarded except for success.
func (d Diagnostics) unreachedFallbackItem(kind CredentialKind) (DiagnosticItem, bool) {
	file, err := NewFileCredentialStore()
	if err != nil {
		return DiagnosticItem{}, false
	}
	if !file.HasCredential(kind) {
		return DiagnosticItem{}, false
	}
	return DiagnosticItem{
		Name:  "Clé du magasin fichier",
		Value: "présente, non lue",
		Detail: fmt.Sprintf("le magasin %s n'a pas pu être lu, donc le magasin fichier contenant %q n'a pas été consulté ; "+
			"déclarez-le avec 'just-code auth use-fallback %s', exportez ALBERT_API_KEY ou ouvrez une session trousseau sur ce poste", kind, kind, kind),
		Status: DiagnosticWarn,
	}, true
}

func (d Diagnostics) providerSection(ctx context.Context, fs FS) DiagnosticSection {
	section := DiagnosticSection{Title: "Vérification Albert"}
	key := strings.TrimSpace(os.Getenv("ALBERT_API_KEY"))
	source := "variable d'environnement"
	if key == "" {
		read := d.ReadAlbert
		if read == nil {
			read = func(ctx context.Context) (string, error) { return ReadStoredCredential(ctx, CredentialAlbert) }
		}
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		stored, err := read(readCtx)
		cancel()
		if err == nil {
			key = strings.TrimSpace(stored)
			source = "magasin d'identifiants"
		} else if !errors.Is(err, ErrCredentialNotFound) && !errors.Is(err, ErrFileStoreAbsent) && !errors.Is(err, ErrFileStoreNotConsented) {
			var storeErr *StoreError
			if errors.As(err, &storeErr) {
				section.Items = append(section.Items, DiagnosticItem{Name: "Magasin d'identifiants Albert", Value: storeErr.State, Detail: "le magasin " + storeErr.Kind + " n'a pas pu être lu", Status: DiagnosticWarn})
			} else {
				section.Items = append(section.Items, DiagnosticItem{Name: "Magasin d'identifiants Albert", Value: "non vérifié", Detail: "la clé n'a pas pu être lue", Status: DiagnosticWarn})
			}
			// The read failed before it could reach the consented file store.
			// Saying whether that store actually holds the credential is the
			// difference between "store one" and "it is already there; record
			// it or open a keyring session" (#201).
			if item, ok := d.unreachedFallbackItem(CredentialAlbert); ok {
				section.Items = append(section.Items, item)
			}
		}
	}
	if key == "" {
		section.Items = append(section.Items, DiagnosticItem{
			Name: "Clé Albert", Value: "non configurée", Detail: "définissez ALBERT_API_KEY, enregistrez une clé avec 'just-code auth add albert' ou lancez 'just-code setup'", Status: DiagnosticWarn,
		})
		return section
	}
	section.Items = append(section.Items, DiagnosticItem{
		Name: "Clé Albert", Value: "présente", Detail: source + " ; la valeur de la clé n'est jamais affichée", Status: DiagnosticOK,
	})
	probe := d.ProbeAlbert
	if probe == nil {
		probe = func(ctx context.Context, value string) CredentialProbe {
			return ValidateAlbertKey(ctx, nil, value)
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := probe(probeCtx, key)
	probeDetail := strings.ReplaceAll(result.Detail, key, "[redacted]")
	switch {
	case result.Rejected:
		section.Items = append(section.Items, DiagnosticItem{Name: "Point d'accès Albert", Value: "refusé", Detail: probeDetail, Status: DiagnosticFailed})
	case result.Unreachable:
		section.Items = append(section.Items, DiagnosticItem{Name: "Point d'accès Albert", Value: "inaccessible", Detail: probeDetail, Status: DiagnosticWarn})
	default:
		section.Items = append(section.Items, DiagnosticItem{Name: "Point d'accès Albert", Value: "vérifié", Detail: "le catalogue de modèles a accepté la clé", Status: DiagnosticOK})
	}
	return section
}

func (d Diagnostics) projectSection(fs FS, stateDir, projectDir string) DiagnosticSection {
	section := DiagnosticSection{Title: "Skills et MCP du projet"}
	project, err := DiscoverProject(projectDir)
	if err != nil {
		return DiagnosticSection{Title: section.Title, Items: []DiagnosticItem{{Name: "Projet", Value: "indisponible", Detail: err.Error(), Status: DiagnosticFailed}}}
	}
	manifestPath := ProjectManifestPath(project.Root)
	manifest, err := ReadProjectManifest(fs, manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DiagnosticSection{Title: section.Title, Items: []DiagnosticItem{{Name: "Projet", Value: project.Name, Detail: "aucun manifeste .just-code ; lancez 'just-code init'", Status: DiagnosticInfo}}}
		}
		return DiagnosticSection{Title: section.Title, Items: []DiagnosticItem{{Name: "Projet", Value: project.Name, Detail: err.Error(), Status: DiagnosticFailed}}}
	}
	runtimeName := manifest.Runtime
	if runtimeName == "" {
		runtimeName = "par défaut (microsandbox)"
	}
	isolation := manifest.Isolation
	if isolation == "" {
		isolation = "par défaut (full)"
	}
	section.Items = append(section.Items,
		DiagnosticItem{Name: "Projet", Value: project.Name, Detail: "projet actuel", Status: DiagnosticInfo},
		DiagnosticItem{Name: "Runtime déclaré", Value: runtimeName, Status: DiagnosticInfo},
		DiagnosticItem{Name: "Isolation déclarée", Value: isolation, Status: DiagnosticInfo},
	)
	if _, err := fs.ReadFile(ProjectUpdateJournalPath(project.Root)); err == nil {
		section.Items = append(section.Items, DiagnosticItem{
			Name: "Mise à jour du projet en attente", Value: "récupération nécessaire", Detail: "doctor n'a ni récupéré ni modifié la transaction ; lancez 'just-code update --recover'", Status: DiagnosticWarn,
		})
	} else if !os.IsNotExist(err) {
		section.Items = append(section.Items, DiagnosticItem{Name: "Mise à jour du projet en attente", Value: "illisible", Detail: err.Error(), Status: DiagnosticWarn})
	}

	packages, ids, localOnly, err := ReadProjectSkillDiagnosticState(fs, project.Root, stateDir, project.InstanceName())
	if err != nil {
		value := "erreur"
		detail := err.Error()
		if len(ids) > 0 {
			value = fmt.Sprintf("%d sélectionnés ; non prêts", len(ids))
			detail = strings.Join(ids, ", ") + ": " + err.Error()
		}
		section.Items = append(section.Items, DiagnosticItem{Name: "Skills", Value: value, Detail: detail, Status: DiagnosticFailed})
		if localOnly {
			section.Items = append(section.Items, DiagnosticItem{Name: "Stockage des skills", Value: "sur ce poste", Detail: "la sélection n'est pas partagée avec les clones du dépôt", Status: DiagnosticInfo})
		} else if len(ids) > 0 {
			section.Items = append(section.Items, DiagnosticItem{Name: "Stockage des skills", Value: "versionné", Detail: "le manifeste et le fichier de verrouillage sont dans le projet", Status: DiagnosticInfo})
		}
	} else if len(ids) == 0 {
		section.Items = append(section.Items, DiagnosticItem{Name: "Skills", Value: "aucun sélectionné", Status: DiagnosticInfo})
	} else {
		section.Items = append(section.Items, DiagnosticItem{Name: "Skills", Value: fmt.Sprintf("%d sélectionnés", len(ids)), Detail: strings.Join(ids, ", "), Status: DiagnosticOK})
		for _, pkg := range packages {
			section.Items = append(section.Items, DiagnosticItem{Name: "Artefact de skill", Value: pkg.ID, Detail: shortDigest(pkg.Lock.SHA256), Status: DiagnosticOK})
		}
		if localOnly {
			section.Items = append(section.Items, DiagnosticItem{Name: "Stockage des skills", Value: "sur ce poste", Detail: "la sélection n'est pas partagée avec les clones du dépôt", Status: DiagnosticInfo})
		} else {
			section.Items = append(section.Items, DiagnosticItem{Name: "Stockage des skills", Value: "versionné", Detail: "le manifeste et le fichier de verrouillage sont dans le projet", Status: DiagnosticInfo})
		}
	}

	selected, err := ValidateMCPConnectorIDs(manifest.MCPConnectors)
	if err != nil {
		section.Items = append(section.Items, DiagnosticItem{Name: "MCPs", Value: "erreur", Detail: err.Error(), Status: DiagnosticFailed})
	} else if len(selected) == 0 {
		section.Items = append(section.Items, DiagnosticItem{Name: "MCPs", Value: "aucun sélectionné", Status: DiagnosticInfo})
	} else {
		section.Items = append(section.Items, DiagnosticItem{Name: "MCPs", Value: fmt.Sprintf("%d sélectionnés", len(selected)), Detail: strings.Join(selected, ", "), Status: DiagnosticInfo})
		for _, choice := range MCPSelections() {
			if !containsMCPConnector(selected, choice.ID) {
				continue
			}
			if choice.Local {
				section.Items = append(section.Items, DiagnosticItem{Name: "MCP invité " + choice.ID, Value: "sélectionné", Detail: "la disponibilité suppose un test d'intégration invité actif", Status: DiagnosticInfo})
			} else {
				section.Items = append(section.Items, DiagnosticItem{Name: "MCP distant " + choice.ID, Value: "sélectionné", Detail: "l'établissement de la connexion n'est pas testé par setup doctor", Status: DiagnosticInfo})
			}
		}
	}
	return section
}

func (d Diagnostics) credentialSection(ctx context.Context, fs FS, stateDir string) DiagnosticSection {
	section := DiagnosticSection{Title: "Stockage des identifiants"}
	journal, err := ReadSetupJournal(fs, stateDir)
	verifyStore := d.VerifyStore
	if verifyStore == nil {
		verifyStore = func(ctx context.Context) (string, error) {
			return verifyCredentialStore(ctx, journal.StoreKind)
		}
	}
	storeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	storeKind, storeErr := verifyStore(storeCtx)
	cancel()
	if storeErr == nil {
		// The probe does not write, so it cannot promise a write will land.
		section.Items = append(section.Items, DiagnosticItem{Name: "Disponibilité du magasin d'identifiants", Value: storeKind, Detail: "sonde en lecture seule réussie ; une collection verrouillée peut encore refuser les écritures", Status: DiagnosticOK})
	} else {
		var typed *StoreError
		if errors.As(storeErr, &typed) {
			section.Items = append(section.Items, DiagnosticItem{Name: "Disponibilité du magasin d'identifiants", Value: typed.State, Detail: "le magasin " + typed.Kind + " n'a pas pu être vérifié", Status: DiagnosticWarn})
		} else {
			section.Items = append(section.Items, DiagnosticItem{Name: "Disponibilité du magasin d'identifiants", Value: "non vérifié", Detail: "échec de la sonde en lecture seule", Status: DiagnosticWarn})
		}
	}
	if journal.StoreKind != "" {
		section.Items = append(section.Items, DiagnosticItem{Name: "Magasin d'identifiants", Value: journal.StoreKind, Detail: "type seul ; aucune valeur secrète n'est affichée", Status: DiagnosticInfo})
	}
	// The inventory comes from the store, never from the journal: a setup
	// that finishes removes its journal, so the journal cannot prove that a
	// key is absent. Same read path and classification as 'auth status'.
	section.Items = append(section.Items, d.storedCredentialsItem(ctx))
	// The journal is reported as what it is: a resume marker, never an
	// inventory. It costs one read of the same small file already read
	// above; the extra read is what separates "absent" (no resume record)
	// from "present but at the first stage" (setup interrupted), which the
	// journal contents alone cannot distinguish.
	section.Items = append(section.Items, setupJournalItem(fs, stateDir, journal, err))
	if us, err := d.userSettings(fs); err != nil {
		section.Items = append(section.Items, DiagnosticItem{Name: "Paramètres utilisateur", Value: "erreur", Detail: err.Error(), Status: DiagnosticFailed})
	} else if us.CredentialRef != "" {
		section.Items = append(section.Items, DiagnosticItem{Name: "Référence de clé par défaut", Value: "configurée", Detail: "nom de la référence omis du rapport", Status: DiagnosticInfo})
	}
	return section
}

// storedCredentialsItem probes the credential store for every known kind and
// reports which ones hold a value. It reuses the runtime's own read path so
// doctor confirms exactly what a launch would confirm, instead of trusting the
// setup journal, which successful setup deletes. The value is discarded here:
// only kind names and the store that answered reach the report.
//
// Absence and unreadable are different claims, and the classification follows
// the read path's own semantics. A native keyring that refuses a read cannot
// distinguish "absent" from "locked", so that kind is named as unreadable and
// doctor declines to call the store empty. A host with no native keyring at
// all is not a failure: the run path resolves credentials from the consented
// file fallback there, so doctor still reports that inventory.
func (d Diagnostics) storedCredentialsItem(ctx context.Context) DiagnosticItem {
	detail := "lu depuis le magasin d'identifiants ; seuls les types sont indiqués, les valeurs ne sont jamais affichées"
	read := d.ReadCredential
	if read == nil {
		read = func(ctx context.Context, kind CredentialKind) (string, string, error) {
			return readStoredCredential(ctx, kind)
		}
	}
	var stored, unreadable, stores, absent []string
	// One deadline for the whole inventory, not one per kind: a keyring
	// daemon that is down would otherwise cost a timeout for every kind,
	// and a diagnostic must not take a multiple of its own budget to answer.
	probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	for _, kind := range KnownCredentialKinds() {
		value, store, err := read(probeCtx, kind)
		switch {
		case err == nil && strings.TrimSpace(value) != "":
			stored = append(stored, string(kind))
			if store != "" && !slices.Contains(stores, store) {
				stores = append(stores, store)
			}
		case err == nil || IsCredentialNotFound(err):
			// The runtime's own read path collapses "native absent, fallback
			// missing or never consented" into ErrCredentialNotFound, so a
			// not-found outcome means the store genuinely holds nothing for
			// this kind. An empty value is an absence too, not a stored
			// credential. Anything else is a store that refused the read,
			// which is a different claim and is counted separately.
			absent = append(absent, string(kind))
		default:
			unreadable = append(unreadable, string(kind))
		}
	}
	if len(stored) == 0 && len(absent) == 0 {
		// Nothing was read at all, so nothing is known about the inventory.
		return DiagnosticItem{Name: "Clés enregistrées", Value: "non lues", Status: DiagnosticWarn,
			Detail: "le magasin d'identifiants a refusé toutes les lectures, ce qui ne prouve pas qu'aucune clé n'est enregistrée"}
	}
	value := "aucune"
	status := DiagnosticInfo
	if len(stored) > 0 {
		value = strings.Join(stored, ", ")
		if len(stores) > 0 {
			detail = fmt.Sprintf("lu depuis le magasin d'identifiants %s ; seuls les types sont indiqués, les valeurs ne sont jamais affichées", strings.Join(stores, " et "))
		}
		status = DiagnosticOK
	}
	if len(stored) == 0 && len(unreadable) == 0 {
		detail = "le magasin ne contient aucun des types connus ; enregistrez une clé avec 'just-code auth add albert' ou lancez 'just-code setup'"
	}
	if len(unreadable) > 0 {
		if len(stored) == 0 {
			value = "inconnues"
		} else {
			value += " (partiel)"
		}
		return DiagnosticItem{
			Name: "Clés enregistrées", Value: value, Status: DiagnosticWarn,
			Detail: fmt.Sprintf("%s ; illisibles : %s (le magasin a refusé la lecture, ce qui ne prouve pas l'absence)", detail, strings.Join(unreadable, ", ")),
		}
	}
	return DiagnosticItem{Name: "Clés enregistrées", Value: value, Detail: detail, Status: status}
}

// setupJournalItem reports the wizard's progress record as what it is: a
// resume marker. Its absence proves neither setup history nor credential absence.
func setupJournalItem(fs FS, stateDir string, journal SetupJournal, readErr error) DiagnosticItem {
	if readErr != nil {
		return DiagnosticItem{Name: "Journal de setup", Value: "erreur", Detail: readErr.Error(), Status: DiagnosticFailed}
	}
	if _, err := fs.ReadFile(SetupJournalPath(stateDir)); err != nil {
		if os.IsNotExist(err) {
			return DiagnosticItem{Name: "Journal de setup", Value: "absent", Detail: "aucun point de reprise de setup n'est présent ; le journal n'est pas un inventaire des clés enregistrées", Status: DiagnosticInfo}
		}
		return DiagnosticItem{Name: "Journal de setup", Value: "illisible", Detail: "le fichier journal n'a pas pu être lu", Status: DiagnosticWarn}
	}
	value := string(journal.Stage)
	if value == "" {
		value = "en cours"
	}
	detail := "setup interrompu : relancez 'just-code setup' pour reprendre"
	if len(journal.CredentialKinds) > 0 {
		detail = fmt.Sprintf("setup interrompu : %s enregistrés à ce stade ; le magasin d'identifiants fait foi sur ce qui est réellement stocké", strings.Join(journal.CredentialKinds, ", "))
	}
	return DiagnosticItem{Name: "Journal de setup", Value: value, Detail: detail, Status: DiagnosticWarn}
}

func verifyCredentialStore(ctx context.Context, preferred string) (string, error) {
	if preferred == "file" {
		store, err := NewFileCredentialStore()
		if err != nil {
			return "file", err
		}
		if err := store.Verify(ctx); err != nil {
			return store.Kind(), err
		}
		return store.Kind(), nil
	}
	store := DefaultCredentialStore()
	if store == nil {
		return "none", errors.New("native credential store is not supported on this platform")
	}
	if err := store.Verify(ctx); err != nil {
		return store.Kind(), err
	}
	return store.Kind(), nil
}

// ReadProjectSkillDiagnosticState validates only persisted project intent and
// reads the already-cached archives. It deliberately skips the launch loader:
// diagnostics must not acquire project locks or recover interrupted updates.
func ReadProjectSkillDiagnosticState(fs FS, projectRoot, stateDir, instance string) ([]SkillPackage, []string, bool, error) {
	manifest, err := ReadProjectManifest(fs, ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	lock, err := ReadLockfile(fs, ProjectLockPath(projectRoot))
	if err != nil {
		return nil, nil, false, err
	}
	if err := ValidateDependencySet(manifest, lock); err != nil {
		return nil, nil, false, err
	}
	ids, pins, localOnly := manifest.Skills, lock.Skills, manifest.SkillsLocalOnly
	if localOnly {
		path, err := LocalSkillSelectionsPath(stateDir, instance)
		if err != nil {
			return nil, nil, false, err
		}
		local, err := ReadLocalSkillSelections(fs, path)
		if err != nil {
			return nil, nil, false, err
		}
		ids, pins = local.Skills, local.Pins
	}
	packages, err := LoadLockedSkillPackages(ids, pins)
	if err != nil {
		return nil, append([]string(nil), ids...), localOnly, err
	}
	return packages, append([]string(nil), ids...), localOnly, nil
}

func (d Diagnostics) userSettings(fs FS) (UserSettings, error) {
	path, err := UserSettingsPath()
	if err != nil {
		return UserSettings{}, err
	}
	return ReadUserSettings(fs, path)
}

func shortDigest(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

func statusFor(ok bool) DiagnosticStatus {
	if ok {
		return DiagnosticOK
	}
	return DiagnosticFailed
}

func yesNo(ok bool) string {
	if ok {
		return "ok"
	}
	return "échec"
}

func humanFreeBytes(n int64) string {
	switch {
	case n < 0:
		return "inconnu"
	case n >= 1<<30:
		return fmt.Sprintf("%.1f Go", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f Mo", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d o", n)
	}
}

// DiagnosticSummaryPath returns a stable human-readable value for support
// logs; it contains no secret material.
func (r DiagnosticsReport) SummaryPath() string {
	var failed []string
	for _, section := range r.Sections {
		for _, item := range section.Items {
			if item.Status == DiagnosticFailed {
				failed = append(failed, section.Title+"/"+item.Name)
			}
		}
	}
	if len(failed) == 0 {
		return "ok"
	}
	return "failed: " + strings.Join(failed, ", ")
}
