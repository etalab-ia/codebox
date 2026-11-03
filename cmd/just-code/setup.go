package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/etalab-ia/just-code/internal/justcode"
	"golang.org/x/term"
)

// setupCmd implements `just-code setup` (P11): the global setup wizard.
// Stages: preflight (read-only, fatal blockers first — no credential prompt
// before a fatal virtualization failure) -> Albert credential (masked,
// validated; rejection vs network failure distinguished) -> optional GitHub
// credential -> editable git identity -> model selection -> review ->
// apply (settings + managed runtime). An interrupted setup resumes at the
// unfinished stage via the host-state journal.
//
//	setup              # interactive wizard
//	setup doctor       # read-only diagnosis, no prompts, no writes
//	setup doctor --json # machine-readable read-only diagnosis
//	setup --fallback   # use the consented file credential store
//	setup --no-color   # plain terminal output
//
// The setup wizard retains its line-based rendering for pasted and non-TTY
// input. The read-only doctor report uses the installed TUI table library
// only when stdout is a styled terminal; both renderers consume engine data.
func setupCmd(args []string) (int, error) {
	sub := ""
	fallback, noColor, jsonOutput := false, false, false
	for _, a := range args {
		switch a {
		case "doctor":
			sub = "doctor"
		case "--fallback":
			fallback = true
		case "--no-color":
			noColor = true
		case "--json":
			jsonOutput = true
		default:
			return 2, fmt.Errorf("Unknown setup argument: %s (expected doctor, --fallback, --no-color or --json)", a)
		}
	}
	if jsonOutput && sub != "doctor" {
		return 2, fmt.Errorf("--json is only valid with 'just-code setup doctor'")
	}
	if noColor {
		// --no-color promises plain terminal output; the line renderer
		// emits no styling, so keep it even on a TTY.
		previous := wizardNoColor
		defer func() { wizardNoColor = previous }()
		wizardNoColor = true
	}
	if sub == "doctor" {
		return setupDoctorCmd(jsonOutput)
	}
	if isTTY() && !stdoutIsTTY() {
		return 1, fmt.Errorf("interactive setup requires stdout to be a terminal; stdout is redirected or points at %s", os.DevNull)
	}
	return setupRunCmd(fallback)
}

// setupDoctorCmd prints the read-only diagnosis. Its probes are injectable so
// tests do not depend on the host's KVM state, PATH, network, or settings.
var setupDoctorCollectFn = func() justcode.DiagnosticsReport {
	return (justcode.Diagnostics{}).Collect(context.Background())
}

// setupPreflightFn runs the host preflight the wizard gates on. It is a package
// variable so tests can reach the fresh-start branch (intro, then the first
// prompt) on a host where the real preflight would be fatal, such as a
// KVM-less CI runner.
var setupPreflightFn = func(ctx context.Context) justcode.SetupPreflight {
	return (justcode.SetupPreflighter{}).RunPreflight(ctx)
}

func setupDoctorCmd(jsonOutput bool) (int, error) {
	report := setupDoctorCollectFn()
	if jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			return 1, err
		}
		if report.Fatal || hasFailedDiagnostics(report) {
			return 1, nil
		}
		return 0, nil
	}
	if doctorRendererFn() {
		fmt.Println(huhDiagnosticsDescription(report))
	} else {
		printDiagnosticsPlain(report)
	}
	if report.Fatal || hasFailedDiagnostics(report) {
		fmt.Println("Result: one or more checks failed. Fix the failures above and re-run.")
		return 1, nil
	}
	fmt.Println("Result: no blocking issue found. Run 'just-code setup' to change configuration or install the runtime.")
	return 0, nil
}

// printDiagnosticsPlain is the stable non-TTY representation: no styling, no
// terminal control, and the same section/item/status facts as the TUI view.
func printDiagnosticsPlain(report justcode.DiagnosticsReport) {
	fmt.Printf("Platform: %s\n", report.Platform)
	for _, section := range report.Sections {
		fmt.Println(section.Title + ":")
		for _, item := range section.Items {
			fmt.Printf("  %s: %s (%s)\n", item.Name, item.Value, item.Status)
			if item.Detail != "" {
				fmt.Printf("    %s\n", item.Detail)
			}
		}
	}
}

func hasFailedDiagnostics(report justcode.DiagnosticsReport) bool {
	for _, section := range report.Sections {
		for _, item := range section.Items {
			if item.Status == justcode.DiagnosticFailed {
				return true
			}
		}
	}
	return false
}

func yn(b bool) string {
	if b {
		return "ok"
	}
	return "FAILED"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

// setupRunCmd runs the interactive wizard. Every prompt reads one line;
// empty + Ctrl-D (EOF) cancels the current stage, and "cancel" at the first
// prompt of the flow exits with the journal preserved for a later resume.
func setupRunCmd(fallback bool) (int, error) {
	return setupWizardRun(fallback, bufio.NewReader(os.Stdin), nil, nil, nil)
}

// setupIntroScreen is the first thing a fresh `just-code setup` shows. It is
// displayed only when the run can both render it and read the answer, so a
// piped run (no terminal) is never blocked waiting for a keypress nobody can
// send. The banner is the one the README opens with.
const setupIntroScreen = `       _            __                    __
      (_)_  _______/ /_   _________  ____/ /__
     / / / / / ___/ __/  / ___/ __ \/ __  / _ \
    / / /_/ (__  ) /_   / /__/ /_/ / /_/ /  __/
 __/ /\__,_/____/\__/   \___/\____/\__,_/\___/
/___/

just code : coder avec l'IA souveraine de l'État, dans une machine isolée.

Cette configuration se fait une seule fois sur ce poste.
Durée : environ 3 minutes. Espace disque : environ 2 Go.

3 étapes :
  1. Plateformes d'inférence (au moins une) : Albert API, Mistral AI ou une plateforme compatible OpenAI.
  2. Sélection des modèles à utiliser.
  3. GitHub (facultatif) : pour pousser les modifications sur GitHub en pull request.

Vos identifiants et votre identité git sont enregistrés au fil des questions,
pour que la configuration puisse reprendre si elle est interrompue.
Le modèle est confirmé au récapitulatif final.
Appuyez sur Entrée pour commencer, ou sur Ctrl+C pour quitter.
`

// setupIntroFn reports whether the intro screen can be shown and answered: it
// needs both an input terminal (to read the keypress) and an output terminal
// (otherwise the screen goes to a file and the program blocks on an invisible
// prompt). It is a package variable so tests can exercise the intro without a
// terminal.
var setupIntroFn = func() bool { return stdinIsTTYFn() && stdoutIsTTY() }

// waitForEnterFn reads one Enter press from the terminal. It is a package
// variable so tests can exercise the intro without a terminal.
var waitForEnterFn = waitForEnter

// waitForEnter waits for one Enter press on the terminal. Two constraints meet
// here:
//
//   - a buffered reader would pull in whatever the user pasted after the
//     newline, and that typed-ahead text belongs to the next prompt — the
//     Albert key, which readPassword reads straight from the terminal and
//     would never see;
//   - with echo on, a key pasted together with Enter is printed and stays in
//     the scrollback before the password prompt can hide it.
//
// On a terminal, term.ReadPassword satisfies both: it turns echo off and reads
// exactly one line (the kernel hands over complete lines, so a following line
// stays queued). Off a terminal there is nothing to echo-hide and no key
// prompt to protect, so a byte-at-a-time read keeps the "consume only the
// newline" contract for piped input.
func waitForEnter() bool {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		if _, err := term.ReadPassword(fd); err != nil {
			return false
		}
		fmt.Println()
		return true
	}
	one := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(one)
		if n > 0 && one[0] == '\n' {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// printSetupIntro shows the intro and waits for Enter. It returns false when
// the user could not be read (end of input), so the caller stops without
// having written anything.
func printSetupIntro() bool {
	fmt.Print(setupIntroScreen)
	fmt.Println()
	if !waitForEnterFn() {
		fmt.Println("Configuration interrompue.")
		return false
	}
	return true
}

// setupWizardRun is the testable core of the wizard: input comes from in,
// and the network-facing seams (Albert validation, runtime installation)
// are injectable so tests never hit the network or download anything. A nil
// validate uses the real endpoint probe; a nil ensure installs the real
// managed runtime.
func setupWizardRun(fallback bool, in *bufio.Reader, validate func(context.Context, string) error, ensure func(context.Context) error, catalogue func(model string) string) (int, error) {
	ctx := context.Background()
	stateDir := justcode.DefaultStateDir()
	fs := justcode.DefaultFS

	j, err := justcode.ReadSetupJournal(fs, stateDir)
	if err != nil {
		return 1, err
	}
	// A resumed run reuses the store the interrupted run used (journal
	// StoreKind), so credentials never split across two stores. An
	// explicit --fallback flag still wins.
	if !fallback && j.StoreKind == "file" {
		fallback = true
	}
	if j.Stage == justcode.StagePreflight && len(j.CredentialKinds) == 0 && j.GitName == "" {
		// Fresh start: preflight first, so a fatal blocker surfaces before
		// any prompt.
		res := setupPreflightFn(ctx)
		j.Preflight = res
		if res.Fatal {
			fmt.Println("This host cannot run just-code's managed runtime:")
			printPreflight(res)
			_ = justcode.WriteSetupJournal(fs, stateDir, j)
			return 1, fmt.Errorf("setup stopped: fix the failures above, then re-run 'just-code setup'")
		}
		// Intro before any prompt: on a fresh run the user has just been
		// dropped into the wizard with no explanation of what it will ask
		// for. A resumed run skips it (the else branch below).
		if setupIntroFn() {
			if !printSetupIntro() {
				recordSetupAbandon(stateDir, "intro")
				return 1, nil
			}
		}
		j.Stage = justcode.StageCredential
		if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	} else {
		fmt.Println("Reprise de la configuration là où elle s'était arrêtée (les clés déjà enregistrées sont conservées).")
	}

	validateAlbertKey := func(ctx context.Context, key string) error {
		if validate != nil {
			return validate(ctx, key)
		}
		probe := justcode.ValidateAlbertKey(ctx, nil, key)
		switch {
		case probe.Rejected:
			return justcode.RejectedCredentialError{Detail: probe.Detail}
		case probe.Unreachable:
			return fmt.Errorf("could not reach the Albert endpoint: %s", probe.Detail)
		}
		return nil
	}
	w := justcode.SetupWizard{
		FS:       fs,
		StateDir: stateDir,
		Store: func(ctx context.Context) (justcode.SetupStore, error) {
			return authStore(fallback)
		},
		ValidateAlbert: validateAlbertKey,
		ValidateProvider: func(ctx context.Context, provider justcode.InferenceProvider, key string) justcode.CredentialProbe {
			if provider.Kind == justcode.ProviderAlbert {
				if validate != nil {
					if err := validate(ctx, key); err != nil {
						var rejected justcode.RejectedCredentialError
						if errors.As(err, &rejected) {
							return justcode.CredentialProbe{Rejected: true, Detail: rejected.Detail}
						}
						return justcode.CredentialProbe{Unreachable: true, Detail: err.Error()}
					}
					return justcode.CredentialProbe{}
				}
				return justcode.ValidateAlbertKey(ctx, nil, key)
			}
			return justcode.ValidateProviderKey(ctx, nil, provider, key)
		},
		ValidateGitHub: githubTokenProbeFn,
		EnsureRuntime:  ensure,
	}

	editing := false
setupStages:
	// Stage: inference platforms and their keys.
	providers, err := setupPlatformStage(ctx, w, in, fallback, &j, editing, fs, stateDir)
	if err != nil {
		return 1, err
	}

	// Stage: model selection (catalogue-validated when reachable). It runs
	// right after the Albert key because the catalogue check needs that key,
	// and because the announced flow is key -> models -> GitHub -> recap.
	// A journal has the run's own working value; without one (a completed
	// install being reconfigured) the saved user setting is the current value,
	// so an unanswered prompt must not look like "clear the model".
	model := j.DefaultModel
	modelAnswered := j.ModelChosen
	models := append([]string(nil), j.Models...)
	if !modelAnswered {
		if settings, ok := currentUserSettings(); ok {
			model = settings.DefaultModel
			models = append([]string(nil), settings.Models...)
		}
	}
	if len(models) == 0 {
		models = setupModelIDsWithLegacyDefault(model)
	}
	if model == "" {
		if providersContain(providers, justcode.ProviderAlbert) {
			model = defaultSetupModel()
		}
	} else {
		model = canonicalSetupModelRef(model)
	}
	if len(models) > 0 && !setupModelEnabled(model, models) && providersContain(providers, justcode.ProviderAlbert) {
		model = setupModelRef(models[0])
	}
	fmt.Println()
	if wizardHuhFn() {
		selected, defaultModel, ok, err := promptSetupModels(providers, models, model)
		if errors.Is(err, huh.ErrUserAborted) {
			fmt.Println("Setup cancelled.")
			recordSetupAbandon(stateDir, "models")
			return 1, nil
		}
		if err != nil {
			return 1, err
		}
		if ok {
			models, model, modelAnswered = selected, defaultModel, true
		}
	} else {
		fmt.Println("Models (comma-separated references such as albert/Qwen3-Coder-30B-A3B-Instruct or mistral/mistral-large-latest; Enter keeps the current selection):")
		chosen, answered := promptLine(in, "Models: ")
		if answered {
			if strings.TrimSpace(chosen) != "" {
				models = parseSetupModelIDs(chosen)
				if len(models) == 0 {
					return 1, fmt.Errorf("select at least one model")
				}
				model = setupModelRefForSelected(models[0])
			}
			modelAnswered = true
		}
	}
	// Validate against the P10 catalogue when it is reachable; an
	// unreachable catalogue (first run offline, or the endpoint down) is a
	// warning, not a blocker — the key itself was already validated above.
	if model != "" {
		validateModel := validateModelAgainstCatalogue
		if catalogue != nil {
			validateModel = catalogue
		}
		for _, id := range models {
			if kind, _ := justcode.ProviderModelID(id); kind != justcode.ProviderAlbert {
				continue
			}
			ref := "albert/" + id
			if warn := validateModel(ref); warn != "" {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", warn)
			}
		}
	}
	// Record the choice immediately. The GitHub and git-identity stages come
	// next and can be aborted; without this, resuming would reload the stale
	// journal value and lose a model the user had already picked.
	//
	// The in-memory j must be updated too: later stages write the whole journal
	// back, so a journal-only write here would be overwritten by the stale copy.
	//
	// Only an ANSWERED choice is journaled. An unanswered prompt (EOF, Ctrl-D)
	// leaves the saved setting alone: resetting it would silently undo a
	// configuration the user never asked to change.
	// Preserve answered-ness separately so a resumed run can distinguish an
	// explicit selection from an interruption before the prompt was answered.
	if modelAnswered && (j.DefaultModel != model || !sameStrings(j.Models, models) || !j.ModelChosen) {
		j.DefaultModel = model
		j.Models = append([]string(nil), models...)
		j.ModelChosen = true
		if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	}

	// Stage: optional GitHub credential. "Plus tard" is the default and
	// blocks nothing: an unreadable answer (EOF, aborted form) anywhere in
	// the stage is treated as "Plus tard", never as a cancellation — the
	// stage exists to help, not to gate the rest of setup.
	hasGitHub := justcode.ContainsCredentialKind(j.CredentialKinds, "github")
	keepGitHub := false
	if editing && hasGitHub {
		label := "Garder le jeton enregistré"
		if j.GitHubLogin != "" {
			label = "Garder le jeton enregistré (@" + j.GitHubLogin + ")"
		}
		choice, err := setupExistingCredentialChoice(in, label, "Remplacer le jeton")
		if err != nil {
			return 1, err
		}
		keepGitHub = choice == "keep"
		j.GitHubReplacePending = !keepGitHub
		if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	}
	if !keepGitHub && (editing || !hasGitHub || j.GitHubReplacePending) {
		if editing {
			j.SkipGitHub = false
		}
		if j.SkipGitHub && !j.GitHubReplacePending {
			// A resumed run keeps the earlier "no" without re-asking.
		} else {
			fmt.Println()
			fmt.Println(githubScreenText(fallback))
			addGitHub := false
			if wizardHuhFn() {
				choice := "later"
				err := huhRun(huh.NewForm(
					huh.NewGroup(
						huh.NewSelect[string]().
							Options(
								huh.NewOption("Ajouter un jeton maintenant", "add"),
								huh.NewOption("Plus tard (just-code auth add github)", "later"),
							).
							Value(&choice),
					),
				))
				if err != nil && !errors.Is(err, huh.ErrUserAborted) {
					return 1, err
				}
				addGitHub = err == nil && choice == "add"
			} else {
				addGitHub = githubAddChoice(in)
			}
			stored := false
			githubLogin := ""
			githubVerified := false
			if addGitHub {
				var err error
				stored, githubLogin, githubVerified, err = githubTokenStage(ctx, w, in)
				if err != nil {
					return 1, err
				}
			}
			if stored {
				j.CredentialKinds = addCredentialKind(j.CredentialKinds, "github")
				j.GitHubLogin = githubLogin
				j.GitHubVerified = &githubVerified
				j.GitHubReplacePending = false
				j.SkipGitHub = false
			} else {
				if hasGitHub {
					if j.GitHubReplacePending {
						fmt.Println("Le jeton précédent est conservé. Le remplacement reste à terminer lors de la reprise.")
						j.SkipGitHub = false
					} else {
						fmt.Println("Le jeton déjà enregistré est conservé.")
					}
				} else {
					fmt.Println(githubLaterMessage)
					j.SkipGitHub = true
				}
			}
			if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
				return 1, err
			}
		}
	}

	// Stage: git identity (editable, defaulting to the host git config).
	if editing || j.GitName == "" || j.GitEmail == "" {
		fmt.Println()
		name, email := j.GitName, j.GitEmail
		if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
			hostName, hostEmail := hostGitIdentityFn()
			if strings.TrimSpace(name) == "" {
				name = hostName
			}
			if strings.TrimSpace(email) == "" {
				email = hostEmail
			}
		}
		if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
			fmt.Println("L'agent signe ses commits à votre nom. Votre git config n'en contient pas : indiquez-les.")
		}
		if wizardHuhFn() {
			var fields []huh.Field
			if editing || strings.TrimSpace(name) == "" {
				fields = append(fields, huh.NewInput().Title("Prénom et nom").Value(&name))
			}
			if editing || strings.TrimSpace(email) == "" {
				fields = append(fields, huh.NewInput().Title("Adresse e-mail").Value(&email))
			}
			err := runSetupIdentityForm(fields)
			if errors.Is(err, huh.ErrUserAborted) {
				// Identity is persisted here, not at final apply: an
				// abort must cancel setup, not save partial values.
				fmt.Println("\nSetup cancelled.")
				recordSetupAbandon(stateDir, "identity")
				return 1, nil
			}
			if err != nil && !errors.Is(err, huh.ErrUserAborted) {
				return 1, err
			}
		} else {
			if editing || strings.TrimSpace(name) == "" {
				prompted, _ := promptLine(in, identityPrompt("Prénom et nom", name))
				if strings.TrimSpace(prompted) != "" {
					name = prompted
				}
			}
			if editing || strings.TrimSpace(email) == "" {
				prompted, _ := promptLine(in, identityPrompt("Adresse e-mail", email))
				if strings.TrimSpace(prompted) != "" {
					email = prompted
				}
			}
		}
		if strings.TrimSpace(name) == "" || strings.TrimSpace(email) == "" {
			return 1, fmt.Errorf("le nom et l'adresse e-mail sont nécessaires pour signer les commits")
		}
		j.GitName, j.GitEmail = strings.TrimSpace(name), strings.TrimSpace(email)
		if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	}

	// Review.
	fmt.Println()
	fmt.Println("Récapitulatif")
	// Consent is asked once, at the end, and covers the later `init` runs.
	// It is asked before the recap so the recap can state the answer, and its
	// default is No.
	statsEnabled, statsAsked := justcode.ReadStatsConsent(fs, userSettingsPathOrEmpty())
	if j.StatsConsentAsked {
		// A run interrupted after answering but before applying keeps its
		// decision in the journal. Both `stats on` and `stats off` synchronize
		// that journal, so its decision is always the freshest one: restore it.
		statsEnabled, statsAsked = j.StatsEnabled, true
	}
	// Snapshot the durable decision at the review: only a CHANGE after this
	// point is an external `stats on`/`stats off`, never an answer the user
	// just gave inside this wizard.
	reviewDurableEnabled, reviewDurableAsked := statsEnabled, statsAsked
	if !statsAsked || editing {
		answer, answered, err := setupStatsConsent(in)
		if err != nil {
			return 1, err
		}
		if answered {
			// Only an actual answer changes the decision: a skipped question
			// (piped input) must not undo an explicit `stats on`.
			statsEnabled = answer
			statsAsked = true
			j.StatsConsentAsked = true
			j.StatsEnabled = answer
			if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
				return 1, err
			}
		}
	}
	j.StatsEnabled = statsEnabled
	albertStore := "le trousseau"
	if fallback {
		albertStore = "un fichier protégé"
	}
	keyStatus := "état inconnu (journal historique)"
	if j.AlbertVerified != nil && !*j.AlbertVerified {
		keyStatus = "non vérifiée"
	} else if j.AlbertVerified != nil {
		keyStatus = "vérifiée"
	}
	var platformNames []string
	for _, provider := range providers {
		platformNames = append(platformNames, provider.Name)
	}
	fmt.Printf("  Plateformes         %s\n", strings.Join(platformNames, ", "))
	if providersContain(providers, justcode.ProviderAlbert) {
		fmt.Printf("  Clé Albert API      enregistrée dans %s (%s)\n", albertStore, keyStatus)
	}
	for _, provider := range providers {
		if provider.Kind == justcode.ProviderAlbert {
			continue
		}
		status := "vérifiée"
		if entry, ok := j.Platform(provider.Kind); ok && entry.Verified != nil && !*entry.Verified {
			status = "non vérifiée"
		}
		fmt.Printf("  Clé %-16s enregistrée dans %s (%s)\n", provider.Name, albertStore, status)
	}
	if j.Preflight.DiskFreeBytes > 0 {
		fmt.Printf("  Espace disque       %s disponibles\n", humanBytes(j.Preflight.DiskFreeBytes))
	}
	if justcode.ContainsCredentialKind(j.CredentialKinds, "github") {
		if j.GitHubReplacePending {
			if j.GitHubLogin != "" {
				fmt.Printf("  GitHub              jeton actuel @%s conservé ; remplacement en attente\n", j.GitHubLogin)
			} else {
				fmt.Println("  GitHub              jeton actuel conservé ; remplacement en attente")
			}
		} else if j.GitHubVerified == nil {
			fmt.Println("  GitHub              jeton enregistré, état de vérification inconnu (journal historique)")
		} else if !*j.GitHubVerified {
			fmt.Println("  GitHub              jeton enregistré, non vérifié")
		} else if j.GitHubLogin != "" {
			fmt.Printf("  GitHub              jeton vérifié, compte @%s\n", j.GitHubLogin)
		} else {
			fmt.Println("  GitHub              jeton vérifié, compte non disponible")
		}
	} else {
		fmt.Println("  GitHub              plus tard")
	}
	fmt.Printf("  Signature commits   %s <%s>\n", strings.TrimSpace(j.GitName), strings.TrimSpace(j.GitEmail))
	if model != "" {
		selectedModels := make([]string, 0, len(models))
		for _, id := range models {
			selectedModels = append(selectedModels, setupModelLabel(id))
		}
		fmt.Printf("  Modèles             %s (par défaut : %s)\n", strings.Join(selectedModels, ", "), setupModelLabel(model))
	} else if !modelAnswered {
		// The prompt was never answered: the saved setting is untouched, so the
		// recap must not announce a change the run will not make.
		fmt.Println("  Modèles             sélection actuelle conservée")
	} else {
		fmt.Println("  Modèles             Qwen3 Coder (par défaut)")
	}
	if statsEnabled {
		fmt.Println("  Statistiques d'usage : partagées")
	} else {
		fmt.Println("  Statistiques d'usage : non partagées")
	}
	fmt.Println()
	fmt.Println(setupRuntimeSummary(j.Preflight))
	choice, err := setupReviewChoice(in)
	if err != nil {
		return 1, err
	}
	switch choice {
	case "edit":
		editing = true
		goto setupStages
	case "quit":
		recordSetupAbandon(stateDir, "review")
		return 1, fmt.Errorf("configuration non appliquée; aucun paramètre ni runtime n'a été appliqué")
	}
	if j.GitHubReplacePending {
		fmt.Println("Remplacement GitHub annulé ; le jeton existant sera conservé.")
		j.GitHubReplacePending = false
		if err := justcode.WriteSetupJournal(fs, stateDir, j); err != nil {
			return 1, err
		}
	}

	// Apply: write the settings (only now — a cancelled apply must leave
	// the settings untouched), then install the runtime.
	if err := w.SaveIdentity(j.GitName, j.GitEmail); err != nil {
		return 1, err
	}
	// Re-resolve consent at apply time: the wizard may have been sitting at
	// the recap while the user ran `stats on` or `stats off` in another
	// terminal. Only a durable decision that CHANGED since the review counts
	// as external; an unchanged durable value is the user's own answer in
	// this wizard, not something to override it.
	if latest, latestAsked := justcode.ReadStatsConsent(fs, userSettingsPathOrEmpty()); latestAsked && (latest != reviewDurableEnabled || latestAsked != reviewDurableAsked) {
		statsEnabled, statsAsked = latest, true
		j.StatsEnabled = latest
		j.StatsConsentAsked = true
	} else if latest, jerr := justcode.ReadSetupJournal(fs, stateDir); jerr == nil && latest.StatsConsentAsked {
		statsEnabled, statsAsked = latest.StatsEnabled, true
	}
	if err := w.SaveSettings(model, models, modelAnswered, providers, statsEnabled, statsAsked); err != nil {
		return 1, err
	}
	fmt.Println()
	fmt.Println("Installation de la machine isolée…")
	if err := w.InstallRuntime(ctx); err != nil {
		// Journal preserved: the retry skips the credential prompts.
		return 1, fmt.Errorf("runtime installation failed: %v (re-run 'just-code setup' to resume; the credential prompts will be skipped)", err)
	}
	justcode.RecordStatsEvent(fs, stateDir, justcode.StatsEventInput{
		Kind:    "setup",
		Version: version,
		Setup: &justcode.SetupStats{
			Platforms:    setupStatsProviders(providers),
			Models:       append([]string(nil), j.Models...),
			DefaultModel: j.DefaultModel,
			GitHubToken:  justcode.ContainsCredentialKind(j.CredentialKinds, "github") || setupCredentialPresentFn(ctx, justcode.CredentialGithub),
			GitConfig:    strings.TrimSpace(j.GitName) != "" && strings.TrimSpace(j.GitEmail) != "",
		},
	})
	fmt.Println()
	fmt.Println(setupCompletionMessage())
	return 0, nil
}

// setupPlatformStage asks which inference platforms to connect and stores one
// key per platform. A resumed run reuses the journal's platform set and skips
// the platforms whose key is already stored; "Modifier" re-asks everything.
// setupCredentialPresentFn reports whether a platform's credential actually
// exists in the store. It is a seam so tests do not read the host keychain.
var setupCredentialPresentFn = func(ctx context.Context, kind justcode.CredentialKind) bool {
	_, err := justcode.ReadStoredCredential(ctx, kind)
	return err == nil
}

var setupChoosePlatformsFn = chooseSetupPlatforms

var setupResumePlatformsFn = confirmResumePlatforms

// providerSettingsOf converts resolved providers back to the settings form.
func providerSettingsOf(providers []justcode.InferenceProvider) []justcode.ProviderSettings {
	out := make([]justcode.ProviderSettings, 0, len(providers))
	for _, p := range providers {
		out = append(out, justcode.ProviderSettings{Kind: p.Kind, Name: p.Name, BaseURL: p.BaseURL})
	}
	return out
}

func setupPlatformStage(ctx context.Context, w justcode.SetupWizard, in *bufio.Reader, fallback bool, j *justcode.SetupJournal, editing bool, fs justcode.FS, stateDir string) ([]justcode.InferenceProvider, error) {
	platforms := j.Platforms
	selectPlatforms := len(platforms) == 0 && len(j.CredentialKinds) == 0
	if len(platforms) == 0 && len(j.CredentialKinds) == 0 {
		// A completed install has no journal. Seed the platform choices from
		// the saved settings so re-running setup edits the existing
		// configuration instead of silently replacing it with the default.
		if us, ok := currentUserSettings(); ok && len(us.Providers) > 0 {
			for _, settings := range us.Providers {
				provider, err := justcode.ResolveProvider(settings)
				if err != nil {
					continue
				}
				platforms = append(platforms, justcode.SetupPlatform{
					Kind: string(provider.Kind), Name: provider.Name, BaseURL: provider.BaseURL,
				})
				// Only mark a credential as already stored when it actually
				// exists: a removed key (or an import that never stored one)
				// must be asked again, not skipped as "already recorded".
				if setupCredentialPresentFn(ctx, provider.CredentialKind) {
					j.CredentialKinds = addCredentialKind(j.CredentialKinds, string(provider.CredentialKind))
				}
			}
		}
	}
	if len(platforms) == 0 {
		// A journal written before #180 carries credential kinds but no
		// platform list: infer the platforms it already connected instead of
		// asking again on resume.
		for _, kind := range []justcode.ProviderKind{justcode.ProviderAlbert, justcode.ProviderMistral, justcode.ProviderOpenAICompatible} {
			if justcode.ContainsCredentialKind(j.CredentialKinds, string(kind)) {
				platforms = append(platforms, justcode.SetupPlatform{Kind: string(kind)})
			}
		}
	}
	if selectPlatforms || editing {
		chosen, err := setupChoosePlatformsFn(platforms, in)
		if err != nil {
			return nil, err
		}
		platforms = chosen
	} else if len(platforms) > 0 {
		// A resumed run must be able to revisit its platform selection
		// (#305): an interruption on a platform key must not lock the user
		// into that platform. Stored keys and the other journaled answers
		// stay untouched either way.
		keep, err := setupResumePlatformsFn(platforms, in, stateDir)
		if err != nil {
			return nil, err
		}
		if !keep {
			chosen, err := setupChoosePlatformsFn(platforms, in)
			if err != nil {
				return nil, err
			}
			platforms = chosen
		}
	}
	// The OpenAI-compatible platform cannot be defaulted: collect its name and
	// base URL before the resolver can validate the set.
	for i := range platforms {
		if platforms[i].Kind != string(justcode.ProviderOpenAICompatible) {
			continue
		}
		if strings.TrimSpace(platforms[i].Name) != "" && strings.TrimSpace(platforms[i].BaseURL) != "" {
			continue
		}
		resolved, err := promptProviderEndpoint(in, justcode.InferenceProvider{
			Kind: justcode.ProviderOpenAICompatible, Name: platforms[i].Name, BaseURL: platforms[i].BaseURL,
		})
		if err != nil {
			return nil, err
		}
		platforms[i].Name = resolved.Name
		platforms[i].BaseURL = resolved.BaseURL
	}
	j.Platforms = platforms
	providers, err := justcode.ResolveProviders(j.PlatformSettings())
	if err != nil {
		return nil, err
	}
	j.SetPlatforms(providers)
	if err := justcode.WriteSetupJournal(fs, stateDir, *j); err != nil {
		return nil, err
	}

	for index := range providers {
		provider := providers[index]
		stored := justcode.ContainsCredentialKind(j.CredentialKinds, string(provider.CredentialKind))
		if stored && !editing {
			fmt.Printf("Clé %s déjà enregistrée.\n", provider.Name)
			continue
		}
		if stored && editing {
			fmt.Printf("Clé %s déjà enregistrée.\n", provider.Name)
			choice, err := setupExistingCredentialChoice(in, "Garder la clé enregistrée", "Remplacer la clé")
			if err != nil {
				return nil, err
			}
			if choice == "keep" {
				continue
			}
		}
		updatedProvider, verified, err := providerKeyStage(ctx, w, in, provider, fallback, stateDir)
		if err != nil {
			return nil, err
		}
		// The key stage can correct an OpenAI-compatible base URL after an
		// incompatible response; persist the corrected endpoint, not the one
		// the user started with.
		provider = updatedProvider
		providers[index] = updatedProvider
		// Revalidate the whole set: a corrected endpoint may collide with
		// another platform's host, which would make every later launch fail.
		if _, err := justcode.ResolveProviders(providerSettingsOf(providers)); err != nil {
			return nil, err
		}
		j.CredentialKinds = addCredentialKind(j.CredentialKinds, string(provider.CredentialKind))
		if provider.Kind == justcode.ProviderAlbert {
			value := verified
			j.AlbertVerified = &value
		}
		if entry, ok := j.Platform(provider.Kind); ok {
			value := verified
			entry.Verified = &value
			entry.Name = provider.Name
			entry.BaseURL = provider.BaseURL
			for i := range j.Platforms {
				if j.Platforms[i].Kind == entry.Kind {
					j.Platforms[i] = entry
				}
			}
		}
		if j.StoreKind == "" {
			if store, err := w.ResolveStore(ctx); err == nil {
				j.StoreKind = store.Kind()
			}
		}
		if err := justcode.WriteSetupJournal(fs, stateDir, *j); err != nil {
			return nil, err
		}
	}
	return providers, nil
}

// chooseSetupPlatforms renders the platform selection. Albert API is listed
// first and pre-checked but can be unchecked; at least one platform is
// required.
func chooseSetupPlatforms(current []justcode.SetupPlatform, in *bufio.Reader) ([]justcode.SetupPlatform, error) {
	order := []justcode.ProviderKind{justcode.ProviderAlbert, justcode.ProviderMistral, justcode.ProviderOpenAICompatible}
	labels := map[justcode.ProviderKind]string{
		justcode.ProviderAlbert:           "Albert API                 l'IA souveraine de l'État (recommandé)",
		justcode.ProviderMistral:          "Mistral AI                 modèles de Mistral AI, hors infrastructure de l'État",
		justcode.ProviderOpenAICompatible: "Compatible OpenAI          toute plateforme compatible OpenAI (Scaleway, OpenRouter…)",
	}
	defaultKinds := []string{string(justcode.ProviderAlbert)}
	if len(current) > 0 {
		defaultKinds = defaultKinds[:0]
		for _, p := range current {
			defaultKinds = append(defaultKinds, p.Kind)
		}
	}
	selected := defaultKinds
	if !wizardHuhFn() {
		fmt.Println("[1/3] Plateformes d'inférence")
		fmt.Println()
		fmt.Println("Choisissez où l'agent envoie ses demandes. Entrez les numéros séparés par des virgules pour en connecter plusieurs.")
		fmt.Println()
		for i, kind := range order {
			mark := " "
			if slices.Contains(defaultKinds, string(kind)) {
				mark = "x"
			}
			fmt.Printf("  %d. [%s] %s\n", i+1, mark, labels[kind])
		}
		answer, ok := promptLine(in, "Plateformes (Entrée pour confirmer la sélection affichée) : ")
		if !ok {
			return nil, errSetupCancelled
		}
		answer = strings.TrimSpace(answer)
		if answer != "" {
			selected = nil
			seen := map[string]bool{}
			for _, part := range strings.Split(answer, ",") {
				n, err := strconv.Atoi(strings.TrimSpace(part))
				if err != nil || n < 1 || n > len(order) {
					return nil, fmt.Errorf("choix invalide %q ; entrez un ou plusieurs numéros de 1 à %d", strings.TrimSpace(part), len(order))
				}
				kind := string(order[n-1])
				if !seen[kind] {
					selected = append(selected, kind)
					seen[kind] = true
				}
			}
		}
	} else {
		var options []huh.Option[string]
		for _, kind := range order {
			options = append(options, huh.NewOption(labels[kind], string(kind)))
		}
		err := huhRun(huh.NewForm(huh.NewGroup(
			keepMultiSelectCursorOnResize(huh.NewMultiSelect[string]().
				Title("[1/3] Plateformes d'inférence").
				Description("Choisissez où l'agent envoie ses demandes. Vous pouvez en connecter plusieurs.\n\nEspace : cocher ou décocher. Entrée : valider.").
				Height(len(options) + 10).
				Validate(func(values []string) error {
					if len(values) == 0 {
						return fmt.Errorf("connectez au moins une plateforme d'inférence")
					}
					return nil
				}).
				Options(options...).
				Value(&selected)),
		)))
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, errSetupCancelled
		}
		if err != nil {
			return nil, err
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("connectez au moins une plateforme d'inférence")
	}
	var out []justcode.SetupPlatform
	seen := map[string]bool{}
	for _, kind := range order {
		for _, chosen := range selected {
			if chosen != string(kind) || seen[chosen] {
				continue
			}
			seen[chosen] = true
			entry := justcode.SetupPlatform{Kind: string(kind)}
			for _, old := range current {
				if old.Kind == string(kind) {
					entry.Name = old.Name
					entry.BaseURL = old.BaseURL
					entry.Verified = old.Verified
				}
			}
			out = append(out, entry)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("plateforme inconnue ; choisissez albert, mistral ou openai-compatible")
	}
	return out, nil
}

// setupPlatformDisplayName maps a provider kind to the short label used in
// resume wording. An unknown kind falls back to its raw value so the message
// stays truthful.
func setupPlatformDisplayName(kind string) string {
	switch justcode.ProviderKind(kind) {
	case justcode.ProviderAlbert:
		return "Albert API"
	case justcode.ProviderMistral:
		return "Mistral AI"
	case justcode.ProviderOpenAICompatible:
		return "Compatible OpenAI"
	default:
		return kind
	}
}

// confirmResumePlatforms asks, on a resumed run, whether to keep the platforms
// selected before the interruption or to revisit the selection (#305). A piped
// run keeps the journaled selection without asking: its next input line
// belongs to the platform key prompts (same rule as the stats consent), so
// pre-#305 scripts keep working. EOF on the line prompt also keeps the
// selection; an explicit huh abort cancels the setup.
func confirmResumePlatforms(platforms []justcode.SetupPlatform, in *bufio.Reader, stateDir string) (bool, error) {
	if !stdinIsTTYFn() {
		return true, nil
	}
	names := make([]string, 0, len(platforms))
	for _, p := range platforms {
		names = append(names, setupPlatformDisplayName(p.Kind))
	}
	selection := strings.Join(names, ", ")
	if !wizardHuhFn() {
		fmt.Printf("Plateformes sélectionnées avant l'interruption : %s.\n", selection)
		for {
			answer, ok := promptLine(in, "[C]ontinuer  [M]odifier : ")
			if !ok {
				return true, nil
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "", "c", "continuer":
				return true, nil
			case "m", "modifier":
				return false, nil
			default:
				fmt.Println("Réponse non reconnue.")
			}
		}
	}
	keep := true
	err := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewSelect[bool]().
			Title("Reprise de la configuration").
			Description("Plateformes sélectionnées avant l'interruption : "+selection+".").
			Options(
				huh.NewOption("Continuer avec ces plateformes", true),
				huh.NewOption("Modifier la sélection", false),
			).
			Value(&keep),
	)))
	if errors.Is(err, huh.ErrUserAborted) {
		return false, cancelledSetupError(stateDir)
	}
	if err != nil {
		return false, err
	}
	return keep, nil
}

// promptProviderEndpoint collects the name and base URL of the
// OpenAI-compatible platform, which cannot be defaulted.
func promptProviderEndpoint(in *bufio.Reader, provider justcode.InferenceProvider) (justcode.InferenceProvider, error) {
	name := provider.Name
	baseURL := provider.BaseURL
	for {
		if wizardHuhFn() {
			if err := huhRun(huh.NewForm(huh.NewGroup(
				huh.NewInput().Title("Nom de la plateforme (ex. Scaleway)").Value(&name),
				huh.NewInput().Title("URL de base (ex. https://api.scaleway.ai/v1)").Value(&baseURL),
			))); err != nil {
				if errors.Is(err, huh.ErrUserAborted) {
					return provider, errSetupCancelled
				}
				return provider, err
			}
		} else {
			answer, ok := promptLine(in, "Nom de la plateforme (ex. Scaleway) : ")
			if !ok {
				return provider, errSetupCancelled
			}
			if strings.TrimSpace(answer) != "" {
				name = strings.TrimSpace(answer)
			}
			answer, ok = promptLine(in, "URL de base (ex. https://api.scaleway.ai/v1) : ")
			if !ok {
				return provider, errSetupCancelled
			}
			if strings.TrimSpace(answer) != "" {
				baseURL = strings.TrimSpace(answer)
			}
		}
		resolved, err := justcode.ResolveProvider(justcode.ProviderSettings{
			Kind: justcode.ProviderOpenAICompatible, Name: name, BaseURL: baseURL,
		})
		if err == nil {
			return resolved, nil
		}
		fmt.Printf("%v\n", err)
		if !stdinIsTTYFn() {
			return provider, err
		}
	}
}

// providerStoreLine mirrors the Albert screen's store wording.
func providerStoreLine(fallback bool) string {
	if fallback {
		return "La clé est enregistrée dans un fichier de ce poste, lisible par votre seul compte, jamais dans vos projets."
	}
	return "La clé est enregistrée dans le trousseau de ce poste, jamais dans vos projets."
}

// offStateWarning is the required notice for every platform other than
// Albert API.
func offStateWarning(name string) string {
	return fmt.Sprintf("Vos demandes et les extraits de code envoyés à l'agent partent vers %s,\nhors de l'infrastructure de l'État.", name)
}

// providerKeyScreenHeader is the [1/3] screen for one platform. Albert keeps
// its unchanged copy; the other platforms carry the off-State warning.
func providerKeyScreenHeader(provider justcode.InferenceProvider, fallback bool) string {
	switch provider.Kind {
	case justcode.ProviderMistral:
		return fmt.Sprintf(`[1/3] Clé Mistral AI

L'agent peut utiliser les modèles de Mistral AI.
%s
Vous n'avez pas encore de clé ?
  Créez-la ici : https://console.mistral.ai

%s
`, providerStoreLine(fallback), offStateWarning(provider.Name))
	case justcode.ProviderOpenAICompatible:
		return fmt.Sprintf(`[1/3] Clé %s

Plateforme : %s
URL de base : %s
%s
Mistral AI et les plateformes compatibles OpenAI lisent leur catalogue de modèles à l'exécution.

%s
`, provider.Name, provider.Name, provider.BaseURL, providerStoreLine(fallback), offStateWarning(provider.Name))
	default:
		return albertKeyScreenHeader(fallback)
	}
}

// providerKeyPrompt labels the masked input.
func providerKeyPrompt(provider justcode.InferenceProvider) string {
	if provider.Kind == justcode.ProviderAlbert {
		return albertKeyPrompt
	}
	return "Collez votre clé (elle ne s'affiche pas) : "
}

// providerRejectedMessage explains a refused key for the platform.
func providerRejectedMessage(provider justcode.InferenceProvider, detail string) string {
	switch provider.Kind {
	case justcode.ProviderMistral:
		return fmt.Sprintf("Mistral AI a refusé cette clé (%s). Vérifiez qu'elle est complète, sans espace, ou créez-en une nouvelle : https://console.mistral.ai", detail)
	case justcode.ProviderOpenAICompatible:
		return fmt.Sprintf("%s a refusé cette clé (%s). Vérifiez qu'elle est complète et active.", provider.Name, detail)
	default:
		return albertRejectedMessage(detail)
	}
}

// providerUnreachableMessage distinguishes a network or proxy failure from a
// refusal.
func providerUnreachableMessage(provider justcode.InferenceProvider) string {
	switch provider.Kind {
	case justcode.ProviderAlbert:
		return albertUnreachableMessage
	default:
		return fmt.Sprintf("%s ne répond pas. Vérifiez votre connexion, le proxy de votre administration ou l'URL de base.", provider.Name)
	}
}

// albertEnvKeyStage proposes the Albert key found in ALBERT_API_KEY (#308).
// Priority: a key already recorded in the setup journal wins without any
// proposal (providerKeyStage is not called then); the environment is only
// consulted when a key must be entered. The variable's value is NEVER
// printed. The key is validated through the same probe as a typed key, and
// nothing is stored before an explicit storage agreement. handled=false
// means the manual key loop must run (no variable, or the user prefers
// another key). A piped run skips the proposal entirely: its next input line
// belongs to the key prompt and no agreement may be assumed.
func albertEnvKeyStage(ctx context.Context, w justcode.SetupWizard, in *bufio.Reader, provider justcode.InferenceProvider, fallback bool, stateDir string) (handled, verified bool, err error) {
	if !stdinIsTTYFn() {
		return false, false, nil
	}
	key := strings.TrimSpace(os.Getenv("ALBERT_API_KEY"))
	if key == "" {
		return false, false, nil
	}
	use, err := albertEnvKeyChoice(in)
	if err != nil {
		return true, false, err
	}
	if !use {
		return false, false, nil
	}
	for {
		probe, err := w.ValidateProviderCredential(ctx, provider, key)
		if err != nil {
			return true, false, err
		}
		if probe.Rejected {
			fmt.Println(providerRejectedMessage(provider, probe.Detail))
			fmt.Println()
			return false, false, nil
		}
		if probe.Incompatible {
			fmt.Printf("La réponse d'Albert API n'est pas un catalogue de modèles compatible OpenAI. (%s)\n\n", probe.Detail)
			return false, false, nil
		}
		if !probe.Unreachable {
			verified = true
		}
		if probe.Unreachable {
			fmt.Printf("%s (%s)\n", providerUnreachableMessage(provider), probe.Detail)
			retry, abort := credentialRetryChoice(in, providerUnreachableMessage(provider))
			if abort {
				recordSetupAbandon(stateDir, "credentials")
				return true, false, cancelledSetupError(stateDir)
			}
			if retry {
				fmt.Println()
				continue
			}
		}
		break
	}
	if _, err := w.ResolveStore(ctx); err != nil {
		return true, false, err
	}
	store, err := confirmAlbertEnvKeyStorage(in, fallback)
	if err != nil {
		return true, false, err
	}
	if !store {
		return false, false, nil
	}
	if err := w.PutCredential(ctx, provider.CredentialKind, key); err != nil {
		return true, false, err
	}
	if verified {
		fmt.Println("✅ Clé valide.")
	} else {
		fmt.Println("Clé enregistrée sans vérification.")
	}
	return true, verified, nil
}

// albertEnvKeyChoice asks whether to use the ALBERT_API_KEY value or type
// another key. Only the variable's NAME is shown, never its value.
func albertEnvKeyChoice(in *bufio.Reader) (bool, error) {
	if wizardHuhFn() {
		use := true
		err := huhRun(huh.NewForm(huh.NewGroup(
			huh.NewSelect[bool]().
				Title("Clé Albert API trouvée").
				Description("Une clé est disponible dans la variable d'environnement ALBERT_API_KEY. Elle ne sera jamais affichée.").
				Options(
					huh.NewOption("Utiliser cette clé", true),
					huh.NewOption("Saisir une autre clé", false),
				).
				Value(&use),
		)))
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return use, err
	}
	fmt.Println("Une clé Albert API est disponible dans la variable d'environnement ALBERT_API_KEY (sa valeur n'est pas affichée).")
	for {
		answer, ok := promptLine(in, "[U]tiliser cette clé  [S]aisir une autre clé : ")
		if !ok {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "u", "utiliser", "o", "y":
			return true, nil
		case "s", "saisir", "n":
			return false, nil
		default:
			fmt.Println("Réponse non reconnue.")
		}
	}
}

// confirmAlbertEnvKeyStorage asks the explicit agreement required before an
// environment-provided key is persisted (#308). Enter means yes; EOF or a
// refusal falls back to typing another key, never to a silent storage.
func confirmAlbertEnvKeyStorage(in *bufio.Reader, fallback bool) (bool, error) {
	question := "Enregistrer cette clé dans le trousseau de ce poste ?"
	if fallback {
		question = "Enregistrer cette clé dans un fichier de ce poste, lisible par votre seul compte ?"
	}
	if wizardHuhFn() {
		store := true
		err := huhRun(huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(question).Affirmative("Oui").Negative("Non").Value(&store),
		)))
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return store, err
	}
	for {
		answer, ok := promptLine(in, question+" [O/n] ")
		if !ok {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "o", "oui", "y", "yes":
			return true, nil
		case "n", "non":
			return false, nil
		default:
			fmt.Println("Réponse non reconnue.")
		}
	}
}

// providerKeyStage runs the masked-key loop for one platform: validate,
// explain, re-ask on a refusal, offer retry or continue on an unreachable
// endpoint. It returns whether the stored key passed validation.
func providerKeyStage(ctx context.Context, w justcode.SetupWizard, in *bufio.Reader, provider justcode.InferenceProvider, fallback bool, stateDir string) (justcode.InferenceProvider, bool, error) {
	if provider.Kind == justcode.ProviderAlbert {
		handled, verified, err := albertEnvKeyStage(ctx, w, in, provider, fallback, stateDir)
		if handled {
			return provider, verified, err
		}
	}
	fmt.Println(providerKeyScreenHeader(provider, fallback))
	if _, err := w.ResolveStore(ctx); err != nil {
		return provider, false, err
	}
	for {
		var key string
		if wizardHuhFn() {
			value, err := huhSecret("Clé "+provider.Name, "L'entrée est masquée. Laissez vide pour annuler.")
			if err != nil && !errors.Is(err, huh.ErrUserAborted) {
				return provider, false, err
			}
			key = value
		} else {
			value, ok := promptSecret(in, providerKeyPrompt(provider))
			if !ok {
				recordSetupAbandon(stateDir, "credentials")
				return provider, false, cancelledSetupError(stateDir)
			}
			key = value
		}
		if key == "" {
			recordSetupAbandon(stateDir, "credentials")
			return provider, false, cancelledSetupError(stateDir)
		}
		probe, err := w.ValidateProviderCredential(ctx, provider, key)
		if err != nil {
			return provider, false, err
		}
		if probe.Rejected {
			fmt.Println(providerRejectedMessage(provider, probe.Detail))
			fmt.Println()
			continue
		}
		if probe.Incompatible {
			fmt.Printf("La réponse de %s n'est pas un catalogue de modèles compatible OpenAI. Vérifiez l'URL de base (elle doit pointer vers la racine de l'API, par exemple https://api.scaleway.ai/v1). (%s)\n\n", provider.Name, probe.Detail)
			if provider.Kind == justcode.ProviderOpenAICompatible {
				updated, uerr := promptProviderEndpoint(in, provider)
				if uerr != nil {
					return provider, false, uerr
				}
				provider = updated
				fmt.Println(providerKeyScreenHeader(provider, fallback))
			}
			continue
		}
		if probe.Unreachable {
			fmt.Printf("%s (%s)\n", providerUnreachableMessage(provider), probe.Detail)
			retry, abort := credentialRetryChoice(in, providerUnreachableMessage(provider))
			if abort {
				recordSetupAbandon(stateDir, "credentials")
				return provider, false, cancelledSetupError(stateDir)
			}
			if retry {
				fmt.Println()
				continue
			}
		}
		if err := w.PutCredential(ctx, provider.CredentialKind, key); err != nil {
			return provider, false, err
		}
		verified := !probe.Unreachable
		if probe.Unreachable {
			fmt.Println("Clé enregistrée sans vérification.")
		} else {
			fmt.Println("✅ Clé valide.")
		}
		return provider, verified, nil
	}
}

func setupCompletionMessage() string {
	return "Configuration terminée.\n\nÉtape suivante, dans le dossier d'un projet :\n  cd mon-projet\n  just-code\n\nVérifier l'installation à tout moment : just-code setup doctor"
}

// statsConsentText is the single consent question, asked once at the end of
// setup (#181). Its default is No.
const statsConsentText = `À des fins d'analyse du produit, acceptez-vous de partager avec l'équipe just-code
des informations non nominatives sur votre configuration (les choix faits dans
cet assistant) ? Jamais votre code, vos fichiers ni vos échanges avec l'agent.
Modifiable à tout moment : just-code stats off`

// setupStatsConsent asks the question. Only an explicit yes enables sending;
// an empty answer, EOF or anything else keeps the default No. The bool is the
// answer, the second bool reports whether the user actually answered.
func setupStatsConsent(in *bufio.Reader) (enabled bool, answered bool, err error) {
	if !wizardHuhFn() {
		// A piped run cannot answer a consent question meaningfully: its
		// next line belongs to another prompt. Keep the default No and leave
		// the question unasked; `just-code stats on` is the non-interactive
		// opt-in.
		return false, false, nil
	}
	value := false
	if ferr := huhRun(huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(statsConsentText).
			Affirmative("Oui").
			Negative("Non").
			Value(&value),
	))); ferr != nil {
		if errors.Is(ferr, huh.ErrUserAborted) {
			return false, false, fmt.Errorf("setup cancelled")
		}
		return false, false, ferr
	}
	return value, true, nil
}

// userSettingsPathOrEmpty is the settings path, or "" when it cannot be
// resolved (the consent read then reports "not asked").
func userSettingsPathOrEmpty() string {
	path, err := justcode.UserSettingsPath()
	if err != nil {
		return ""
	}
	return path
}

func runSetupIdentityForm(fields []huh.Field) error {
	if len(fields) == 0 {
		return nil
	}
	return huhRun(huh.NewForm(huh.NewGroup(fields...)))
}

func setupRuntimeSummary(preflight justcode.SetupPreflight) string {
	if preflight.RuntimeInstalled {
		return "La machine isolée est déjà installée. just-code la vérifiera et la réutilisera, sans téléchargement."
	}
	return "just-code téléchargera et vérifiera la machine isolée si aucune installation valide n'est disponible.\nEspace recommandé : environ 2 Go ; le contrôle bloque sous 1 Gio libre. Durée estimée : quelques minutes selon votre connexion."
}

func setupReviewChoice(in *bufio.Reader) (string, error) {
	if wizardHuhFn() {
		choice := "apply"
		err := huhRun(huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Que souhaitez-vous faire ?").
				Options(
					huh.NewOption("Appliquer", "apply"),
					huh.NewOption("Modifier (retour à l'étape 1)", "edit"),
					huh.NewOption("Quitter sans appliquer (identifiants conservés)", "quit"),
				).
				Value(&choice),
		)))
		if errors.Is(err, huh.ErrUserAborted) {
			return "quit", nil
		}
		return choice, err
	}
	for {
		answer, ok := promptLine(in, "[A] Appliquer  [M] Modifier  [Q] Quitter sans appliquer (identifiants conservés) : ")
		if !ok {
			return "quit", nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "a", "appliquer", "o", "y", "yes":
			return "apply", nil
		case "m", "modifier":
			return "edit", nil
		case "q", "quitter", "n":
			return "quit", nil
		default:
			fmt.Println("Réponse non reconnue.")
		}
	}
}

func setupExistingCredentialChoice(in *bufio.Reader, keepLabel, replaceLabel string) (string, error) {
	if wizardHuhFn() {
		choice := "keep"
		err := huhRun(huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().
				Title("Identifiant déjà enregistré").
				Options(huh.NewOption(keepLabel, "keep"), huh.NewOption(replaceLabel, "replace")).
				Value(&choice),
		)))
		if errors.Is(err, huh.ErrUserAborted) {
			return "keep", nil
		}
		return choice, err
	}
	for {
		answer, ok := promptLine(in, "[G]arder  [R]emplacer : ")
		if !ok {
			return "keep", nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "g", "garder":
			return "keep", nil
		case "r", "remplacer":
			return "replace", nil
		default:
			fmt.Println("Réponse non reconnue.")
		}
	}
}

func identityPrompt(title, current string) string {
	if strings.TrimSpace(current) == "" {
		return title + " : "
	}
	return title + " [" + current + "] : "
}

func addCredentialKind(kinds []string, kind string) []string {
	if !justcode.ContainsCredentialKind(kinds, kind) {
		return append(kinds, kind)
	}
	return kinds
}

func hostGitIdentity() (string, string) {
	read := func(key string) string {
		value, err := exec.Command("git", "config", "--global", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(value))
	}
	return read("user.name"), read("user.email")
}

var hostGitIdentityFn = hostGitIdentity

func setupModelLabel(ref string) string {
	kind, id := justcode.ProviderModelID(ref)
	if kind != justcode.ProviderAlbert {
		return id
	}
	for _, model := range justcode.CuratedAlbertModels() {
		if id == model.ID || id == model.OpenCodeID || ref == model.Ref() || ref == "albert/"+model.ID {
			return model.Name
		}
	}
	return id
}

func currentUserSettings() (justcode.UserSettings, bool) {
	path, err := justcode.UserSettingsPath()
	if err != nil {
		return justcode.UserSettings{}, false
	}
	us, err := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if err != nil {
		return justcode.UserSettings{}, false
	}
	return us, true
}

func defaultSetupModelIDs() []string {
	var ids []string
	for _, model := range justcode.CuratedAlbertModels() {
		if model.Default {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func setupModelIDsWithLegacyDefault(model string) []string {
	ids := defaultSetupModelIDs()
	model = canonicalSetupModelRef(model)
	if model == "" || !strings.HasPrefix(model, "albert/") || setupModelEnabled(model, ids) {
		return ids
	}
	id := strings.TrimPrefix(model, "albert/")
	return append(ids, id)
}

func defaultSetupModel() string {
	for _, model := range justcode.CuratedAlbertModels() {
		if model.Default {
			return model.Ref()
		}
	}
	return ""
}

func setupModelEnabled(model string, ids []string) bool {
	model = canonicalSetupModelRef(model)
	kind, id := justcode.ProviderModelID(model)
	if kind != justcode.ProviderAlbert {
		return containsString(ids, model) || containsString(ids, id)
	}
	for _, choice := range justcode.CuratedAlbertModels() {
		if choice.Ref() == model && containsString(ids, choice.ID) {
			return true
		}
	}
	return containsString(ids, id) || containsString(ids, "albert/"+id)
}

func canonicalSetupModelRef(model string) string {
	for _, choice := range justcode.CuratedAlbertModels() {
		if model == choice.ID || model == choice.OpenCodeID || model == "albert/"+choice.ID || model == choice.Ref() {
			return choice.Ref()
		}
	}
	if model == "" || strings.Contains(model, "/") {
		return model
	}
	return "albert/" + model
}

func canonicalSetupModelID(id string) string {
	for _, choice := range justcode.CuratedAlbertModels() {
		if id == choice.ID || id == choice.OpenCodeID || id == choice.Ref() || id == "albert/"+choice.ID {
			return choice.ID
		}
	}
	return strings.TrimPrefix(id, "albert/")
}

func setupModelRef(id string) string {
	for _, choice := range justcode.CuratedAlbertModels() {
		if choice.ID == id {
			return choice.Ref()
		}
	}
	return "albert/" + id
}

// setupModelRefForSelected returns the reference to store as default for a
// selected settings entry: a provider reference stays as-is, a bare value is
// an Albert model ID.
func setupModelRefForSelected(id string) string {
	if kind, _ := justcode.SplitModelRef(id); isSetupProviderKind(kind) {
		return canonicalSetupModelRef(id)
	}
	return setupModelRef(id)
}

func isSetupProviderKind(kind justcode.ProviderKind) bool {
	switch kind {
	case justcode.ProviderAlbert, justcode.ProviderMistral, justcode.ProviderOpenAICompatible:
		return true
	}
	return false
}

func parseSetupModelIDs(input string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(input, ",") {
		id := strings.TrimSpace(raw)
		if kind, rest := justcode.SplitModelRef(id); isSetupProviderKind(kind) {
			if kind == justcode.ProviderAlbert {
				id = canonicalSetupModelID("albert/" + rest)
			} else {
				id = string(kind) + "/" + rest
			}
		}
		if id != "" && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func providersContain(providers []justcode.InferenceProvider, kind justcode.ProviderKind) bool {
	for _, provider := range providers {
		if provider.Kind == kind {
			return true
		}
	}
	return false
}

var setupModelCatalogueFn = func() (justcode.Catalogue, bool, error) {
	apiKey, err := resolveAlbertKeyForCatalogue()
	if err != nil {
		return justcode.Catalogue{}, false, err
	}
	cache := justcode.CatalogueCache{
		FS:  justcode.DefaultFS,
		Dir: justcode.DefaultStateDir(),
		Fetch: func(ctx context.Context) (justcode.Catalogue, error) {
			return justcode.FetchCatalogue(ctx, nil, apiKey, justcode.AlbertCatalogueURL())
		},
	}
	return cache.Load(context.Background())
}

// setupModelChoice is one selectable model, from any configured platform.
type setupModelChoice struct {
	Provider    justcode.ProviderKind
	ID          string
	RefID       string
	Name        string
	Description string
	Default     bool
	Group       string
}

// Ref is the OpenCode model reference and the picker's identity.
func (c setupModelChoice) Ref() string {
	return string(c.Provider) + "/" + c.RefID
}

// albertSetupChoices converts the curated Albert set.
func albertSetupChoices() []setupModelChoice {
	curated := justcode.CuratedAlbertModels()
	out := make([]setupModelChoice, 0, len(curated))
	for _, choice := range curated {
		out = append(out, setupModelChoice{
			Provider: justcode.ProviderAlbert, ID: choice.ID, RefID: choice.OpenCodeID,
			Name: choice.Name, Description: choice.Description, Default: choice.Default,
		})
	}
	return out
}

// setupModelCatalogueFor loads the catalogue of one non-Albert platform.
// Tests inject their own loader.
var setupProviderCatalogueFn = func(provider justcode.InferenceProvider) ([]justcode.CatalogueModel, error) {
	apiKey, err := resolveProviderKeyForCatalogue(provider)
	if err != nil {
		return nil, err
	}
	return justcode.FetchProviderModels(context.Background(), nil, provider, apiKey)
}

type setupModelPicker struct {
	choices         []setupModelChoice
	selected        map[string]bool
	unavailable     map[string]bool
	cursor          int
	defaultModel    string
	externalDefault bool
	errorMessage    string
	cancelled       bool
	done            bool
}

func (m setupModelPicker) Init() tea.Cmd { return nil }

func (m setupModelPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.InterruptMsg); ok {
		m.cancelled = true
		return m, tea.Quit
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if (key.Mod&tea.ModCtrl != 0 && key.Code == 'c') || key.Code == tea.KeyEscape {
		m.cancelled = true
		return m, tea.Quit
	}
	choice := m.choices[m.cursor]
	switch key.Code {
	case tea.KeyUp:
		m.cursor = (m.cursor + len(m.choices) - 1) % len(m.choices)
		m.errorMessage = ""
	case tea.KeyDown:
		m.cursor = (m.cursor + 1) % len(m.choices)
		m.errorMessage = ""
	case tea.KeySpace:
		if !m.isAvailable(choice.Ref()) {
			m.errorMessage = "Ce modèle est indisponible dans le catalogue de sa plateforme."
			break
		}
		if m.selected[choice.Ref()] {
			if m.selectedCount() == 1 {
				m.errorMessage = "Sélectionnez au moins un modèle."
				break
			}
			delete(m.selected, choice.Ref())
			if m.defaultModel == choice.Ref() {
				m.defaultModel = m.firstSelectedRef()
			}
		} else {
			m.selected[choice.Ref()] = true
		}
		m.errorMessage = ""
	case tea.KeyLeft, tea.KeyRight:
		if !m.selected[choice.Ref()] {
			m.errorMessage = "Cochez d'abord le modèle choisi par défaut."
			break
		}
		if !m.isAvailable(choice.Ref()) {
			m.errorMessage = "Ce modèle est indisponible dans le catalogue de sa plateforme."
			break
		}
		m.defaultModel = choice.Ref()
		m.externalDefault = false
		m.errorMessage = ""
	case tea.KeyEnter:
		if m.selectedCount() == 0 {
			m.errorMessage = "Sélectionnez au moins un modèle."
			break
		}
		if !m.externalDefault && (m.defaultModel == "" || !m.defaultModelSelected()) {
			m.defaultModel = m.firstSelectedRef()
		}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m setupModelPicker) View() tea.View {
	var b strings.Builder
	b.WriteString("[2/3] Sélection des modèles à utiliser\n\n")
	b.WriteString("Choisissez les modèles que l'agent pourra utiliser.\n")
	b.WriteString("Vous pourrez passer de l'un à l'autre pendant une session avec /models.\n\n")
	if m.externalDefault {
		b.WriteString("Modèle actuel hors catalogue : " + m.defaultModel + " (conservé avec Entrée).\n\n")
	}
	lastGroup := ""
	for index, choice := range m.choices {
		if choice.Group != "" && choice.Group != lastGroup {
			if lastGroup != "" {
				b.WriteString("\n")
			}
			b.WriteString(choice.Group + "\n")
			lastGroup = choice.Group
		}
		cursor := "  "
		if index == m.cursor {
			cursor = "> "
		}
		mark := "[ ]"
		if m.selected[choice.Ref()] {
			mark = "[x]"
		}
		line := fmt.Sprintf("%s%s %-32s %s", cursor, mark, choice.Name, choice.Description)
		if m.defaultModel == choice.Ref() {
			line += " (par défaut)"
		}
		if !m.isAvailable(choice.Ref()) {
			line += " (indisponible)"
			line = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nEspace : cocher ou décocher.  ← → : choisir le modèle par défaut.  Entrée : valider.\n")
	b.WriteString("Détails : https://guides.ia.numerique.gouv.fr/albert-api/modeles/available-models\n")
	b.WriteString("Vous pourrez modifier ce choix plus tard avec : just-code setup")
	if m.errorMessage != "" {
		b.WriteString("\n\n" + m.errorMessage)
	}
	return tea.NewView(b.String())
}

func (m setupModelPicker) isAvailable(ref string) bool {
	return !m.unavailable[ref]
}

func (m setupModelPicker) selectedCount() int {
	count := 0
	for ref, selected := range m.selected {
		if selected && m.isAvailable(ref) {
			count++
		}
	}
	return count
}

func (m setupModelPicker) firstSelectedRef() string {
	for _, choice := range m.choices {
		if m.selected[choice.Ref()] && m.isAvailable(choice.Ref()) {
			return choice.Ref()
		}
	}
	return ""
}

func (m setupModelPicker) defaultModelSelected() bool {
	for _, choice := range m.choices {
		if choice.Ref() == m.defaultModel {
			return m.selected[choice.Ref()] && m.isAvailable(choice.Ref())
		}
	}
	return false
}

var setupModelPickerRun = func(initial setupModelPicker) (setupModelPicker, error) {
	result, err := tea.NewProgram(initial, tea.WithOutput(os.Stdout)).Run()
	if err != nil {
		return initial, err
	}
	final, ok := result.(setupModelPicker)
	if !ok {
		return initial, fmt.Errorf("model picker returned an unexpected state")
	}
	if final.cancelled {
		return final, huh.ErrUserAborted
	}
	return final, nil
}

func promptSetupModels(providers []justcode.InferenceProvider, current []string, currentDefault string) ([]string, string, bool, error) {
	var choices []setupModelChoice
	unavailable := map[string]bool{}
	albertConfigured := false
	for _, provider := range providers {
		if provider.Kind == justcode.ProviderAlbert {
			albertConfigured = true
		}
	}
	if albertConfigured {
		catalogue, fresh, catalogueErr := setupModelCatalogueFn()
		catalogueUsable := catalogueErr == nil && fresh
		curated := albertSetupChoices()
		for _, choice := range curated {
			choice.Group = "Albert API"
			if catalogueUsable {
				if _, ok := catalogue.Find(choice.ID); !ok {
					unavailable[choice.Ref()] = true
				}
			}
			choices = append(choices, choice)
		}
		for _, raw := range current {
			kind, id := justcode.ProviderModelID(raw)
			if kind != justcode.ProviderAlbert || id == "" {
				continue
			}
			known := false
			for _, choice := range curated {
				if choice.ID == id || choice.RefID == id {
					known = true
				}
			}
			if !known {
				choices = append(choices, setupModelChoice{
					Provider: justcode.ProviderAlbert, ID: id, RefID: id, Name: "Modèle actuel",
					Description: id + " (conservé depuis les réglages existants)", Group: "Albert API",
				})
			}
		}
		if !catalogueUsable {
			fmt.Println("Catalogue Albert API indisponible : sélection par défaut conservée.")
		}
	}
	for _, provider := range providers {
		if provider.Kind == justcode.ProviderAlbert {
			continue
		}
		models, err := setupProviderCatalogueFn(provider)
		if err != nil {
			fmt.Printf("Catalogue %s indisponible : sélection actuelle conservée.\n", provider.Name)
			for _, id := range justcode.ProviderModelIDs(current, provider.Kind) {
				choices = append(choices, setupModelChoice{
					Provider: provider.Kind, ID: id, RefID: id, Name: id,
					Description: "conservé depuis les réglages existants", Group: provider.Name,
				})
			}
			continue
		}
		known := map[string]bool{}
		for _, model := range models {
			known[model.ID] = true
			choices = append(choices, setupModelChoice{
				Provider: provider.Kind, ID: model.ID, RefID: model.ID,
				Name: model.ID, Group: provider.Name,
			})
		}
		for _, id := range justcode.ProviderModelIDs(current, provider.Kind) {
			if !known[id] {
				choices = append(choices, setupModelChoice{
					Provider: provider.Kind, ID: id, RefID: id, Name: "Modèle actuel",
					Description: id + " (conservé depuis les réglages existants)", Group: provider.Name,
				})
				unavailable[string(provider.Kind)+"/"+id] = true
			}
		}
	}
	if len(choices) == 0 {
		return nil, "", false, fmt.Errorf("aucun modèle n'est disponible sur les plateformes configurées")
	}

	selected := map[string]bool{}
	for _, choice := range choices {
		matches := false
		if choice.Provider == justcode.ProviderAlbert {
			for _, id := range current {
				kind, raw := justcode.ProviderModelID(id)
				if kind != justcode.ProviderAlbert {
					continue
				}
				if raw == choice.ID || justcode.ProviderModelRef(kind, raw) == choice.Ref() {
					matches = true
				}
			}
		} else {
			matches = containsString(current, choice.Ref())
		}
		if matches && !unavailable[choice.Ref()] {
			selected[choice.Ref()] = true
		}
	}
	if len(current) == 0 {
		// Fresh selection: the curated Albert defaults when Albert is
		// connected, otherwise the first available model of the first
		// platform.
		for _, choice := range choices {
			if choice.Provider == justcode.ProviderAlbert && choice.Default && !unavailable[choice.Ref()] {
				selected[choice.Ref()] = true
			}
		}
	}
	if len(selected) == 0 {
		for _, choice := range choices {
			if !unavailable[choice.Ref()] {
				selected[choice.Ref()] = true
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, "", false, fmt.Errorf("aucun modèle sélectionné n'est disponible dans les catalogues")
	}
	defaultModel := canonicalSetupModelRef(currentDefault)
	// A default belonging to a managed platform that is no longer connected
	// must be replaced: keeping it would save an unusable model. Only a
	// genuinely unmanaged reference (e.g. a custom provider) is preserved.
	managedDefault := strings.HasPrefix(defaultModel, "albert/") ||
		strings.HasPrefix(defaultModel, "mistral/") ||
		strings.HasPrefix(defaultModel, "openai-compatible/")
	externalDefault := defaultModel != "" && !setupModelRefKnown(defaultModel, choices) && !managedDefault
	if !selected[defaultModel] && !externalDefault {
		defaultModel = firstSelectedChoiceRef(choices, selected)
	}
	initial := setupModelPicker{
		choices:         choices,
		selected:        selected,
		unavailable:     unavailable,
		defaultModel:    defaultModel,
		externalDefault: externalDefault,
	}
	picked, err := setupModelPickerRun(initial)
	if err != nil {
		return nil, "", false, err
	}
	if !picked.done || picked.selectedCount() == 0 {
		return nil, "", false, fmt.Errorf("sélectionnez au moins un modèle disponible")
	}
	var modelIDs []string
	for _, choice := range choices {
		if picked.selected[choice.Ref()] && picked.isAvailable(choice.Ref()) {
			if choice.Provider == justcode.ProviderAlbert {
				modelIDs = append(modelIDs, choice.ID)
			} else {
				modelIDs = append(modelIDs, choice.Ref())
			}
		}
	}
	return modelIDs, picked.defaultModel, true, nil
}

func setupModelRefKnown(ref string, choices []setupModelChoice) bool {
	for _, choice := range choices {
		if choice.Ref() == ref {
			return true
		}
	}
	return false
}

func firstSelectedChoiceRef(choices []setupModelChoice, selected map[string]bool) string {
	for _, choice := range choices {
		if selected[choice.Ref()] {
			return choice.Ref()
		}
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// validateModelAgainstCatalogue checks the chosen model against the P10
// catalogue (cache-first, network when reachable). It returns a warning
// string; catalogue unavailability is NOT a warning (the model is still
// written, and 'just-code models' re-checks later).
func validateModelAgainstCatalogue(model string) string {
	apiKey, err := resolveAlbertKeyForCatalogue()
	if err != nil {
		return "" // no key resolvable yet: nothing to validate against
	}
	cache := justcode.CatalogueCache{
		FS:  justcode.DefaultFS,
		Dir: justcode.DefaultStateDir(),
		Fetch: func(ctx context.Context) (justcode.Catalogue, error) {
			return justcode.FetchCatalogue(ctx, nil, apiKey, justcode.AlbertCatalogueURL())
		},
	}
	catalogue, _, err := cache.Load(context.Background())
	if err != nil {
		return "" // catalogue unreachable and no cache: not a validation failure
	}
	if _, ok := catalogue.Find(model); !ok {
		return fmt.Sprintf("the model %q is not in the catalogue; it may have been removed. 'just-code models' lists the valid ids.", model)
	}
	return ""
}

// errSetupCancelled marks a user cancellation, so abandonment statistics are
// only recorded for an explicit quit, never for a fatal error.
var errSetupCancelled = errors.New("setup annulé")

func cancelledSetupError(stateDir string) error {
	return fmt.Errorf("%w ; relancez 'just-code setup' pour reprendre là où il s'était arrêté (journal : %s)", errSetupCancelled, justcode.SetupJournalPath(stateDir))
}

// recordSetupAbandon queues the last stage reached when the user leaves the
// wizard. RecordStatsEvent resolves consent itself: a journal answer wins
// when present, and the durable decision applies otherwise (a user who
// already opted in does not lose their exits on a fresh run).
func recordSetupAbandon(stateDir, stage string) {
	justcode.RecordStatsEvent(justcode.DefaultFS, stateDir, justcode.StatsEventInput{
		Kind:    "abandon",
		Stage:   stage,
		Version: version,
	})
}

// setupStatsProviders lists the platforms actually connected at the end of
// setup, by kind. The final selection is what counts: a platform removed
// during "Modifier" must not be reported because its key was stored earlier.
// A user-supplied platform name is never recorded.
func setupStatsProviders(providers []justcode.InferenceProvider) []string {
	platforms := make([]string, 0, len(providers))
	for _, provider := range providers {
		platforms = append(platforms, string(provider.Kind))
	}
	return platforms
}

// Albert API links shown on the key screen: where to create a key, and the
// getting-started guide. The tests pin both.
const (
	albertCreateKeyURL  = "https://albert.playground.etalab.gouv.fr/"
	albertQuickstartURL = "https://guides.ia.numerique.gouv.fr/albert-api/prise-en-main/quickstart"
)

// albertKeyScreenHeader introduces the first of the three steps and says where
// a key comes from. fallback selects the file-store wording: the native
// keychain and the consented file store are not the same guarantee, so the
// screen must not promise a keychain when --fallback is in use.
func albertKeyScreenHeader(fallback bool) string {
	storeLine := "La clé est enregistrée dans le trousseau de ce poste, jamais dans vos projets."
	if fallback {
		storeLine = "La clé est enregistrée dans un fichier de ce poste, lisible par votre seul compte, jamais dans vos projets."
	}
	return fmt.Sprintf(`[1/3] Clé Albert API

L'agent utilise Albert API, l'API d'IA générative de l'État.
%s
Vous n'avez pas encore de clé ?
  Créez-la ici : %s
  Guide de prise en main : %s
`, storeLine, albertCreateKeyURL, albertQuickstartURL)
}

// albertKeyPrompt is the input label; the typed value is never echoed.
const albertKeyPrompt = "Collez votre clé (elle ne s'affiche pas) : "

// albertRejectedMessage explains a refused key and points at the key page
// again, so the user can fix or replace it without leaving the wizard.
func albertRejectedMessage(detail string) string {
	return fmt.Sprintf("Albert API a refusé cette clé (%s). Vérifiez qu'elle est complète, sans espace, ou créez-en une nouvelle : %s",
		detail, albertCreateKeyURL)
}

// albertUnreachableMessage distinguishes a network or proxy failure from a
// refusal: the key may well be valid, so the user chooses whether to continue.
const albertUnreachableMessage = "Albert API ne répond pas. Vérifiez votre connexion ou le proxy de votre administration."

// albertRetryPrompt offers the two answers to an unreachable endpoint. The key
// is NOT stored yet at this point: [R] re-enters it, [C] stores it unvalidated,
// and any other outcome (abort, end of input) stores nothing.
const albertRetryPrompt = "[R] Réessayer  [C] Continuer sans vérifier"

// albertRetryChoice asks whether to retry Albert key validation.
func albertRetryChoice(in *bufio.Reader) (retry, abort bool) {
	return credentialRetryChoice(in, albertUnreachableMessage)
}

// credentialRetryChoice asks whether to retry validation. It returns
// retry=true to re-enter the credential, abort=true to stop, and neither for
// a continue-without-verifying decision.
//
// A typo must not change the course of the run: an answer that is neither R
// nor C asks again. A missing answer (EOF) or an aborted form stops, because
// only an explicit choice may authorize storing an unvalidated credential.
func credentialRetryChoice(in *bufio.Reader, unreachableMessage string) (retry, abort bool) {
	if wizardHuhFn() {
		// huh only returns one of the two options (it re-asks on its own for
		// anything else), so an error here is an abort, never a "continue".
		choice := ""
		err := huhRun(huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(unreachableMessage).
					Options(
						huh.NewOption("Réessayer", "retry"),
						huh.NewOption("Continuer sans vérifier", "continue"),
					).
					Value(&choice),
			),
		))
		switch {
		case err != nil:
			return false, true
		case choice == "retry":
			return true, false
		default:
			return false, false
		}
	}
	fmt.Println(albertRetryPrompt)
	for {
		answer, ok := promptLine(in, "> ")
		if !ok {
			// End of input: nothing was chosen, so stop rather than store.
			return false, true
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "r", "réessayer", "reessayer":
			return true, false
		case "c", "continuer":
			return false, false
		}
		fmt.Printf("Réponse non reconnue. %s\n", albertRetryPrompt)
	}
}

// githubTokenProbeFn validates a candidate GitHub token against the API. It
// is a package variable so tests can decide the probe outcome without the
// network, the same pattern as setupPreflightFn.
var githubTokenProbeFn = func(ctx context.Context, token string) justcode.CredentialProbe {
	return justcode.ValidateGitHubToken(ctx, nil, token)
}

// githubTokenURL is where a fine-grained token is created. Pinned by tests.
const githubTokenURL = "https://github.com/settings/personal-access-tokens/new"

// githubScreenText is the [3/3] screen: what GitHub is for, what to do
// without it, which token to create and with what rights. fallback selects
// the file-store wording, as on the Albert key screen: the screen must not
// promise a keychain when --fallback is in use.
func githubScreenText(fallback bool) string {
	storeLine := "Le jeton est enregistré dans le trousseau de ce poste."
	if fallback {
		storeLine = "Le jeton est enregistré dans un fichier de ce poste, lisible par votre seul compte."
	}
	return fmt.Sprintf(`[3/3] GitHub (facultatif)

À quoi ça sert : l'agent travaille dans une machine isolée. Pour récupérer son travail,
il peut pousser ses modifications sur GitHub et ouvrir une pull request en brouillon,
que vous relisez avant d'accepter les modifications.
Sans GitHub, vous récupérez les modifications avec : just-code workspace apply

Il faut un jeton GitHub « fine-grained » :
  1. Créez-le ici : %s
  2. Accès aux dépôts : uniquement les dépôts sur lesquels l'agent travaillera.
  3. Droits : Contents (lecture et écriture), Pull requests (lecture et écriture).
  4. Si le dépôt appartient à une organisation, elle peut devoir approuver le jeton.

%s Il n'est activé pour un projet
qu'après votre accord, lors de la configuration de ce projet.
`, githubTokenURL, storeLine)
}

// githubChoicePrompt is the line-renderer form of the screen's two options.
// Enter (empty answer) is the default: "Plus tard". The legacy y/n answers
// stay accepted: the previous screen asked a yes/no question and users (and
// scripts) answering out of habit must not be rejected.
const githubChoicePrompt = "[A] Ajouter un jeton maintenant  [P] Plus tard (just-code auth add github) [P] : "

// githubAddChoice reads the choice between adding a token now and deferring.
// An unreadable answer (EOF) is "Plus tard": the stage is optional, so the
// safe default never blocks the rest of setup.
func githubAddChoice(in *bufio.Reader) bool {
	for {
		answer, ok := promptLine(in, githubChoicePrompt)
		if !ok {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "p", "plus tard", "n", "non":
			return false
		case "a", "ajouter", "o", "oui", "y", "yes":
			return true
		}
		fmt.Printf("Réponse non reconnue. %s\n", githubChoicePrompt)
	}
}

const (
	// githubTokenPrompt is the input label; the typed value is never echoed.
	githubTokenPrompt = "Collez votre jeton (il ne s'affiche pas ; vide pour passer) : "
	// githubRefusedMessage explains a refused token; the wizard re-asks.
	githubRefusedMessage = "GitHub a refusé ce jeton. Vérifiez qu'il est complet, ou créez-en un nouveau."
	// githubUnreachableMessage distinguishes a network or proxy failure from
	// a refusal: the token may well be valid, so the user chooses.
	githubUnreachableMessage = "GitHub ne répond pas. Vérifiez votre connexion ou le proxy de votre administration."
	// githubClassicMessage warns about a broad-scope token and offers to
	// replace it. The [C]/[R] hint is part of the line-renderer message; the
	// huh renderer shows its own options with the warning as the title.
	githubClassicMessage = "Ce jeton est un jeton classique, avec des droits larges. Un jeton fine-grained, limité à vos dépôts, est recommandé. [C] Continuer  [R] Remplacer"
	// githubLaterMessage confirms the "Plus tard" outcome and the remedy.
	githubLaterMessage = "Plus tard : l'étape se rattrape avec : just-code auth add github"
)

// githubTokenStage runs the masked-token loop: validate, explain, re-ask on
// a refusal, warn on a broad-scope token. It returns stored=true once a
// token is in the store. Every exit that stores nothing (empty answer,
// aborted form, EOF) is the penalty-free "Plus tard" outcome.
func githubTokenStage(ctx context.Context, w justcode.SetupWizard, in *bufio.Reader) (bool, string, bool, error) {
	// Resolve and verify the store BEFORE prompting: a locked or
	// unavailable store must fail before the user types a secret (the same
	// contract the Albert stage keeps).
	if _, err := w.ResolveStore(ctx); err != nil {
		return false, "", false, err
	}
	for {
		var token string
		if wizardHuhFn() {
			value, err := huhSecret("Jeton GitHub", "L'entrée est masquée. Laissez vide pour passer cette étape.")
			if errors.Is(err, huh.ErrUserAborted) {
				return false, "", false, nil
			}
			if err != nil {
				return false, "", false, err
			}
			token = value
		} else {
			value, ok := promptSecret(in, githubTokenPrompt)
			if !ok {
				return false, "", false, nil
			}
			token = value
		}
		if token == "" {
			return false, "", false, nil
		}
		// Validate BEFORE writing: a token the user has not accepted (or
		// GitHub has not accepted) must not reach the store, or a later
		// setup would skip this screen and use it.
		probe, err := w.ValidateCredential(ctx, justcode.CredentialGithub, token)
		if err != nil {
			return false, "", false, err
		}
		if probe.Rejected {
			fmt.Println(githubRefusedMessage)
			fmt.Println()
			continue
		}
		if probe.Unreachable {
			fmt.Printf("%s (%s)\n", githubUnreachableMessage, probe.Detail)
			retry, abort := credentialRetryChoice(in, githubUnreachableMessage)
			if abort {
				return false, "", false, nil
			}
			if retry {
				fmt.Println()
				continue
			}
		}
		if probe.BroadScope {
			replace, abort := githubClassicChoice(in)
			if abort {
				return false, "", false, nil
			}
			if replace {
				fmt.Println()
				continue
			}
		}
		if err := w.PutCredential(ctx, justcode.CredentialGithub, token); err != nil {
			return false, "", false, err
		}
		switch {
		case probe.Unreachable:
			// "Continuer sans vérifier": stored, but unvalidated.
			fmt.Println("Jeton enregistré sans vérification.")
		case probe.Login != "":
			fmt.Printf("✅ Jeton valide : compte %s.\n", probe.Login)
		default:
			fmt.Println("✅ Jeton valide.")
		}
		return true, probe.Login, !probe.Unreachable, nil
	}
}

// githubClassicChoice asks what to do with a valid but broad-scope (classic)
// token: replace=true re-enters a token, abort=true leaves the stage, and
// neither accepts the token. The huh renderer shows the warning without the
// [C]/[R] hint, which belongs to the line renderer.
func githubClassicChoice(in *bufio.Reader) (replace, abort bool) {
	if wizardHuhFn() {
		choice := ""
		err := huhRun(huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title(githubClassicWarning).
					Options(
						huh.NewOption("Continuer", "continue"),
						huh.NewOption("Remplacer", "replace"),
					).
					Value(&choice),
			),
		))
		switch {
		case err != nil:
			return false, true
		case choice == "replace":
			return true, false
		default:
			return false, false
		}
	}
	fmt.Println(githubClassicMessage)
	for {
		answer, ok := promptLine(in, "> ")
		if !ok {
			return false, true
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "r", "remplacer":
			return true, false
		case "c", "continuer":
			return false, false
		}
		fmt.Printf("Réponse non reconnue. %s\n", githubClassicMessage)
	}
}

// githubClassicWarning is githubClassicMessage without the line-renderer's
// [C]/[R] hint, for the huh select title.
const githubClassicWarning = "Ce jeton est un jeton classique, avec des droits larges. Un jeton fine-grained, limité à vos dépôts, est recommandé."

func printPreflight(res justcode.SetupPreflight) {
	fmt.Printf("  Platform: %s\n", res.Platform)
	if !res.VirtualizationOK {
		fmt.Printf("  Virtualization: %s\n", res.VirtualizationDetail)
	}
	if res.DiskDetail != "" {
		// A probe ran and failed: report the failure, never a fabricated 0.
		fmt.Printf("  Disk free: probe failed (%s)\n", res.DiskDetail)
	} else if !res.DiskOK && res.DiskFreeBytes >= 0 {
		fmt.Printf("  Disk free: %s (need at least 1 GiB)\n", humanBytes(res.DiskFreeBytes))
	}
}

// promptLine reads one line, reporting EOF as not-ok.
func promptLine(in *bufio.Reader, prompt string) (string, bool) {
	fmt.Print(prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return line, true
}

// stdinIsTTYFn is the TTY probe used by the wizard's promptSecret. It is a
// package variable so tests can force the non-TTY piped path regardless of
// what the test binary's stdin is (go test runs with stdin on a character
// device, which the probe would call a TTY).
var stdinIsTTYFn = isTTY

// promptSecret reads a secret with echo disabled where supported.
func promptSecret(in *bufio.Reader, prompt string) (string, bool) {
	fmt.Print(prompt)
	if stdinIsTTYFn() {
		// Typed-ahead input: a line the user pasted before the prompt
		// appeared sits in the bufio buffer, and readPassword reads the
		// terminal directly — the buffered line would be lost or misread by
		// the NEXT prompt. Consume it first.
		if in.Buffered() > 0 {
			line, _ := promptLine(in, "")
			_ = line
		}
		value, err := readPassword()
		fmt.Println()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(value), true
	}
	// Non-TTY (piped answers): read a line and TRIM it. The raw line keeps
	// its newline, and a secret with a trailing newline both fails header
	// validation (net/http rejects it) and is stored corrupted.
	line, ok := promptLine(in, "")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(line), true
}
