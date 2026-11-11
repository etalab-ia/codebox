package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/etalab-ia/codebox/internal/codebox"
	"github.com/mattn/go-runewidth"
)

// initCmd implements `codebox init` (P12b): the minimal project setup. The
// decisions and the files they produce live in internal/codebox/initwizard.go;
// this file renders the questions and owns the non-TTY contract.
//
//	init                                  # interactive, every question defaulted
//	init --root <dir> --runtime tart ...  # non-interactive, all inputs on the command line
//	init --replace                        # allow replacing an existing manifest
//	init --yes                            # accept the review without asking
//
// Non-TTY contract (P12 acceptance): with no terminal, the command never
// waits. It fails and names exactly which inputs are missing, so a script can
// supply them on the next run.
func initCmd(args []string) (int, error) {
	// Help wins wherever it appears, and needs no terminal.
	for _, a := range args {
		if a == "--help" || a == "-h" {
			initUsage()
			return 0, nil
		}
	}
	opts, err := parseInitArgs(args)
	if err != nil {
		return 2, err
	}
	if err := validateInitOptions(opts); err != nil {
		return 2, err
	}
	tty := initTTYFn()
	if tty && !opts.Yes && !initStdoutTTYFn() {
		return 1, fmt.Errorf("la configuration interactive exige que la sortie standard soit un terminal ; elle est redirigée ou pointe vers %s", os.DevNull)
	}
	if tty && !opts.Yes {
		opts = initOptionsWithDisplayedRoot(opts)
		// Keep the preflight offer's reader from consuming input intended for
		// setup's own raw-terminal intro. A one-byte buffer avoids read-ahead.
		in := bufio.NewReaderSize(os.Stdin, 1)
		proceed, code, err := ensureInitSetup(in)
		if err != nil || !proceed {
			return code, err
		}
		if !printInitIntro(opts.Root, in) {
			return 1, fmt.Errorf("cancelled; nothing was written")
		}
		return initRun(opts, in, tty)
	}
	return initRun(opts, bufio.NewReader(os.Stdin), tty)
}

func initOptionsWithDisplayedRoot(opts initOptions) initOptions {
	if opts.Root == "" {
		opts.Root, _ = os.Getwd()
	}
	if opts.Set == nil {
		opts.Set = make(map[string]bool)
	}
	opts.Set["root"] = true
	return opts
}

var initAlbertCredentialCheckFn = func() error {
	_, err := resolveAlbertKeyForDefaultCatalogue()
	return err
}

var runInitSetupFn = setupRunCmd

var initTTYFn = isTTY

var initStdoutTTYFn = stdoutIsTTY

func initSetupNeeded() (bool, error) {
	// Global setup owns the account credential and default settings. A
	// project's credentialRef is not evidence that global setup is missing.
	err := initAlbertCredentialCheckFn()
	if errors.Is(err, codebox.ErrCredentialNotFound) {
		return true, nil
	}
	// A store/keychain failure does not prove setup is missing. Leave the
	// existing init path available rather than converting an uncertain probe
	// into a new blocker.
	return false, nil
}

func ensureInitSetup(in *bufio.Reader) (bool, int, error) {
	needed, err := initSetupNeeded()
	if err != nil || !needed {
		return true, 0, err
	}
	answer, ok := promptLine(in, "La configuration initiale n'a pas été faite. Lancer 'codebox setup' d'abord ? [Y/n]: ")
	if !ok || strings.EqualFold(strings.TrimSpace(answer), "n") {
		fmt.Println("Vous pourrez lancer 'codebox setup' avant de configurer ce projet.")
		return false, 0, nil
	}
	code, err := runInitSetupFn(false)
	if err != nil || code != 0 {
		return false, code, err
	}
	return true, 0, nil
}

func printInitIntro(root string, in *bufio.Reader) bool {
	if root == "" {
		root, _ = os.Getwd()
	}
	project := filepath.Base(filepath.Clean(root))
	if project == "." || project == string(filepath.Separator) || project == "" {
		project = root
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		if rel, err := filepath.Rel(home, root); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root = filepath.Join("~", rel)
		}
	}
	fmt.Printf(`Configuration du projet : %s
Dossier : %s

codebox va préparer une machine isolée pour ce projet.
Microsandbox (recommandé) travaille sur une copie filtrée : vos fichiers .env
et les fichiers ignorés par git ne sont pas copiés.

5 étapes :
  1. Agent (harnais) : le moteur qui travaille dans la machine isolée.
  2. Machine isolée : le type d'environnement qui exécute l'agent.
  3. Skills (facultatif) : des savoir-faire pour l'agent (sécurité, accessibilité…).
  4. MCP (facultatif) : des services que l'agent peut interroger (data.gouv.fr, chrome-devtools…).
  5. GitHub (facultatif) : autoriser l'agent à proposer ses modifications sur ce dépôt.

Les réglages techniques (ressources, isolation…) ont des valeurs par défaut.
Les autres types de machine isolée et le partage du dossier qu'ils impliquent
sont expliqués dans les réglages avancés.

La configuration est enregistrée à la fin de ces étapes, puis le projet démarre.
Appuyez sur Entrée pour commencer, ou sur Ctrl+C pour quitter.
`, project, root)
	_, ok := promptLine(in, "")
	return ok
}

// initOptions is the flag surface, and doubles as the answers already supplied
// on the command line.
type initOptions struct {
	Root            string
	Runtime         string
	Isolation       string
	Harness         string
	Model           string
	SmallModel      string
	CPUs            int
	MemoryMB        int
	CredentialRef   string
	GitHub          bool
	Skills          []string
	MCPConnectors   []string
	SkillsLocalOnly bool
	Replace         bool
	Yes             bool
	// Advanced opens the expert settings screen once, after the wizard's
	// questions and before the configuration is applied (#333). It has no
	// effect on non-interactive runs.
	Advanced bool
	// FromLaunch records that the launch flow is driving the setup, which only
	// changes the closing message.
	FromLaunch   bool
	hostCPUs     int
	hostMemoryMB int
	maxCPUs      int
	maxMemoryMB  int
	// Set records which fields came from the command line, so the interactive
	// flow only asks for what is missing and the non-TTY flow knows what to
	// report.
	Set map[string]bool
}

func parseInitArgs(args []string) (initOptions, error) {
	opts := initOptions{Set: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			// A following flag is not a value: `init --root --yes` must be
			// reported as a missing value rather than as a root named
			// "--yes", which would fail later as an incomprehensible path.
			if strings.HasPrefix(args[i+1], "-") {
				return "", fmt.Errorf("%s needs a value, got the flag %q", a, args[i+1])
			}
			i++
			return args[i], nil
		}
		switch {
		case a == "--replace":
			opts.Replace = true
		case a == "--yes" || a == "-y":
			opts.Yes = true
		case a == "--advanced":
			opts.Advanced = true
		case a == "--github":
			opts.GitHub, opts.Set["github"] = true, true
		case a == "--skill":
			v, err := value()
			if err != nil {
				return opts, err
			}
			if opts.Set["clear-skills"] {
				return opts, fmt.Errorf("--skill and --clear-skills cannot be used together")
			}
			opts.Skills = append(opts.Skills, v)
			opts.Set["skills"] = true
		case a == "--clear-skills":
			if opts.Set["skills"] {
				return opts, fmt.Errorf("--skill and --clear-skills cannot be used together")
			}
			opts.Set["clear-skills"], opts.Set["skills"] = true, true
		case a == "--mcp":
			v, err := value()
			if err != nil {
				return opts, err
			}
			if opts.Set["clear-mcps"] {
				return opts, fmt.Errorf("--mcp and --clear-mcps cannot be used together")
			}
			opts.MCPConnectors = append(opts.MCPConnectors, v)
			opts.Set["mcps"] = true
		case a == "--clear-mcps":
			if opts.Set["mcps"] {
				return opts, fmt.Errorf("--mcp and --clear-mcps cannot be used together")
			}
			opts.Set["clear-mcps"], opts.Set["mcps"] = true, true
		case a == "--local-only-skills" || a == "--versioned-skills":
			if opts.Set["skills-storage"] {
				return opts, fmt.Errorf("choose only one skill storage mode")
			}
			opts.Set["skills-storage"] = true
			opts.SkillsLocalOnly = a == "--local-only-skills"
		case a == "--root":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.Root, opts.Set["root"] = v, true
		case a == "--runtime":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.Runtime, opts.Set["runtime"] = v, true
		case a == "--isolation":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.Isolation, opts.Set["isolation"] = v, true
		case a == "--harness":
			v, err := value()
			if err != nil {
				return opts, err
			}
			if _, err := codebox.ResolveHarness(v); err != nil {
				return opts, err
			}
			opts.Harness, opts.Set["harness"] = v, true
		case a == "--model":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.Model, opts.Set["model"] = v, true
		case a == "--small-model":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.SmallModel, opts.Set["small-model"] = v, true
		case a == "--credential-ref":
			v, err := value()
			if err != nil {
				return opts, err
			}
			opts.CredentialRef, opts.Set["credential-ref"] = v, true
		case a == "--cpus":
			v, err := value()
			if err != nil {
				return opts, err
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return opts, fmt.Errorf("--cpus must be a whole number, got %q", v)
			}
			opts.CPUs, opts.Set["cpus"] = n, true
		case a == "--memory-mb":
			v, err := value()
			if err != nil {
				return opts, err
			}
			n, err := codebox.ParseMemorySize(v)
			if err != nil {
				return opts, fmt.Errorf("--memory-mb: %w", err)
			}
			opts.MemoryMB, opts.Set["memory-mb"] = n, true
		default:
			return opts, fmt.Errorf("unknown init option %q (see 'codebox help')", a)
		}
	}
	return opts, nil
}

// initRun is the testable core: the reader and the TTY verdict are passed in,
// so the non-TTY contract can be exercised without a terminal.
// errInitNoAnswer is the cancellation signal for the interactive line wizard:
// a closed input is a refusal to answer, never consent to apply defaults.
var errInitNoAnswer = errors.New("aucune réponse ; rien n'a été enregistré")

func initRun(opts initOptions, in *bufio.Reader, tty bool) (code int, err error) {
	answers := codebox.InitAnswers{
		Root:               opts.Root,
		Runtime:            codebox.Runtime(opts.Runtime),
		Isolation:          codebox.Isolation(opts.Isolation),
		Harness:            opts.Harness,
		Model:              opts.Model,
		SmallModel:         normalizeInitModelInput(opts.SmallModel),
		CPUs:               opts.CPUs,
		MemoryMB:           opts.MemoryMB,
		CredentialRef:      opts.CredentialRef,
		GitHubWorkflow:     opts.GitHub,
		Skills:             opts.Skills,
		MCPConnectors:      opts.MCPConnectors,
		MCPsSet:            opts.Set["mcps"],
		SkillsSet:          opts.Set["skills"],
		SkillsLocalOnly:    opts.SkillsLocalOnly,
		SkillsLocalOnlySet: opts.Set["skills-storage"],
	}
	if answers.Root == "" {
		if cwd, err := os.Getwd(); err == nil {
			answers.Root = cwd
		}
	}
	// The root is the one answer that cannot be defaulted by the engine from
	// nothing, and in a script it is the most likely thing to be wrong.
	if !tty && !opts.Set["root"] {
		return 1, fmt.Errorf("aucun terminal disponible : indiquez le projet explicitement, par exemple 'codebox init --root <dir> --yes'.\n" +
			"Chaque question peut être répondue par une option : --runtime, --isolation, --model, --small-model, --cpus, --memory-mb, --credential-ref, --github, --skill, --clear-skills, --mcp, --clear-mcps, --local-only-skills, --versioned-skills, --replace, --yes")
	}
	answers = seedInitAnswersFromManifest(opts, answers)

	// The catalogue check reads the credential reference of the project being
	// configured, not of whatever directory the caller happens to run from,
	// and it resolves the root the same way the engine will (a path inside a
	// worktree refers to the worktree's manifest).
	wizardHostResources := codebox.HostResources{}
	wizard := codebox.InitWizard{
		HostResources: &wizardHostResources,
		ResolveSkills: resolveInitProjectSkillsFn,
		VerifySkills:  verifyInitProjectSkillsFn,
		Expert:        initExpertFlags(opts),
		ResolveDefaultModel: func() string {
			if path, err := codebox.UserSettingsPath(); err == nil {
				if us, err := codebox.ReadUserSettings(codebox.DefaultFS, path); err == nil && us.DefaultModel != "" {
					return canonicalAlbertModelRef(us.DefaultModel)
				}
			}
			return ""
		},
		ValidateModel: func(model string) (string, error) {
			root := answers.Root
			if pc, err := codebox.DiscoverProject(root); err == nil {
				root = pc.Root
			}
			return catalogueModelWarningFn(root, model)
		},
		ValidateSmallModel: func(model string) (string, error) {
			root := answers.Root
			if pc, err := codebox.DiscoverProject(root); err == nil {
				root = pc.Root
			}
			return validateInitSmallModelInput(root, model)
		},
	}
	// Flags are validated before any question: a typo on the command line must
	// not send the user through six prompts to learn about it.
	if err := validateInitOptions(opts); err != nil {
		return 2, err
	}
	hostResources, hostErr := detectHostResourcesFn()
	if hostErr != nil {
		return 1, fmt.Errorf("cannot determine host CPU and memory capacity; cannot enforce the guest resource limit: %v", hostErr)
	}
	if hostResources.CPUs <= 0 || hostResources.MemoryMB <= 0 {
		return 1, fmt.Errorf("host resource detection returned incomplete CPU or memory capacity; cannot enforce the guest resource limit")
	}
	wizardHostResources = hostResources
	opts.hostCPUs = hostResources.CPUs
	opts.hostMemoryMB = hostResources.MemoryMB
	opts.maxCPUs = codebox.MaxGuestCPUs(hostResources.CPUs)
	opts.maxMemoryMB = codebox.MaxGuestMemoryMB(hostResources.MemoryMB)
	if opts.maxCPUs < 1 {
		return 1, fmt.Errorf("host resource detection reported no logical CPUs available for the guest")
	}
	if opts.maxMemoryMB < 1 {
		return 1, fmt.Errorf("host memory is too small to allocate guest memory")
	}
	if opts.Set["cpus"] && opts.CPUs > opts.maxCPUs {
		return 2, fmt.Errorf("--cpus %d exceeds detected host capacity of %d logical CPUs", opts.CPUs, opts.maxCPUs)
	}
	if opts.Set["memory-mb"] && opts.MemoryMB > opts.maxMemoryMB {
		return 2, fmt.Errorf("--memory-mb %s exceeds detected host capacity of %s", codebox.FormatMemorySize(opts.MemoryMB), codebox.FormatMemorySize(opts.maxMemoryMB))
	}
	if !opts.Set["cpus"] && opts.maxCPUs == 1 {
		if answers.CPUs > opts.maxCPUs {
			answers.CPUs = 0
		}
		if tty && !opts.Yes {
			fmt.Println("Only one whole guest CPU is available; selected 1 CPU automatically.")
		}
	}
	var plan codebox.InitPlan
	// The wizard applies the configuration as soon as its questions pass
	// (#333): there is no confirmation screen. askQuestions gates the
	// default-path questions; only the GitHub re-probe below reopens them.
	askQuestions := true
	interactiveReview := tty && !opts.Yes
	// --advanced opens the expert settings once, after the questions (#333).
	advancedDone := !opts.Advanced
	for {
		useHuh := tty && !opts.Yes && wizardHuhFn()
		if tty && !opts.Yes && askQuestions {
			if useHuh {
				answers, err = askInitQuestionsHuh(opts, answers)
				if err != nil {
					return 1, err
				}
			} else {
				answers, err = askInitQuestions(in, opts, answers)
				if err != nil {
					return 1, err
				}
			}
		}
		if !opts.Set["cpus"] && opts.maxCPUs == 1 && answers.CPUs > opts.maxCPUs {
			// The only valid whole-core choice is automatic; discard an
			// oversized legacy manifest value so init --replace can repair it.
			answers.CPUs = 0
		}
		// Inherited settings may be invalid on this host or against today's
		// catalogue. Let interactive replacement repair them before Plan can
		// reject them, without weakening flag or non-interactive validation.
		if tty && !opts.Yes && opts.Replace {
			if field, repairErr := invalidInheritedInitSetting(opts, answers); repairErr != nil {
				fmt.Printf("\nUn réglage du projet doit être corrigé avant l'application : %v\n", repairErr)
				askQuestions = false
				if field == "runtime" {
					// An invalid runtime goes back to the VM step (#333),
					// not to the expert settings screen.
					if useHuh {
						answers, err = askVMStepHuh(opts, answers)
					} else {
						if _, readErr := in.Peek(1); readErr != nil {
							return 1, fmt.Errorf("aucune réponse ; rien n'a été enregistré : %w", readErr)
						}
						answers, err = askVMStepLine(in, opts, answers)
					}
				} else {
					answers.Advanced = true
					if useHuh {
						answers, err = askInitAdvancedHuh(opts, answers)
					} else {
						if _, readErr := in.Peek(1); readErr != nil {
							return 1, fmt.Errorf("aucune réponse ; rien n'a été enregistré : %w", readErr)
						}
						answers, err = askInitAdvancedLine(in, opts, answers)
					}
				}
				if err != nil {
					return 1, err
				}
				continue
			}
		}
		answers = withBrowserResourceGuidance(answers)
		guestCPUs := answers.CPUs
		if guestCPUs <= 0 {
			guestCPUs = codebox.RecommendedDefaultGuestCPUs(hostResources.CPUs)
		}
		guestMemoryMB := answers.MemoryMB
		if guestMemoryMB <= 0 {
			guestMemoryMB = codebox.RecommendedDefaultGuestMemoryMB(hostResources.MemoryMB)
		}
		if err := codebox.ValidateGuestResources(hostResources, guestCPUs, guestMemoryMB); err != nil {
			return 1, err
		}
		if answers.GitHubWorkflow {
			// Re-verify immediately before writing, with the same check the
			// question used: an origin, a stored token, and the rights the
			// workflow needs. A token revoked between the question and this
			// point is refused here, before anything is written (#169).
			status := githubInitProbeFn(answers.Root)
			var blocked error
			switch {
			case !status.HasOrigin:
				blocked = fmt.Errorf("the project has no readable origin remote; configure a GitHub.com origin to allow the agent to push")
			case status.TokenError != nil:
				// A store that cannot be read is not a missing token: the
				// remedy is to unlock it, and saying "add a token" would send
				// the user down the wrong path.
				blocked = fmt.Errorf("the GitHub credential store could not be read: %w", status.TokenError)
			case !status.HasToken:
				blocked = fmt.Errorf("no GitHub token is stored on this machine; add one with 'codebox auth add github'")
			case status.Rights != codebox.GitHubRepoRightsOK:
				blocked = errors.New(gitHubRightsMessage(status))
			}
			if blocked != nil {
				// Reopening only helps when the GitHub step will run again:
				// --github skips that step, so continuing would re-ask every
				// other question and block here forever. Fail with the cause
				// named in that case, and in any run with nobody to re-ask.
				if !tty || opts.Yes || opts.Set["github"] {
					return 1, blocked
				}
				fmt.Printf("\nLa situation GitHub a changé depuis votre réponse : %v\n", blocked)
				fmt.Println("Réouverture des questions ; vos réponses sont conservées.")
				askQuestions = true
				continue
			}
			answers.GitHubRemote = status.Remote
		}
		plan, err = wizard.Plan(answers)
		if err != nil {
			return 1, err
		}
		if plan.ExistingManifest != nil && !opts.Replace {
			fmt.Print(codebox.FormatInitReview(plan))
			return 1, fmt.Errorf("%s existe déjà ; relancez avec --replace pour le remplacer, ou modifiez-le directement", plan.ManifestPath)
		}
		// Interactive runs apply directly and get the recap afterwards
		// (#333); non-interactive runs keep the full review before the
		// write, since nobody is there to see a post-mortem.
		if !interactiveReview {
			fmt.Print(codebox.FormatInitReview(plan))
		}
		if !tty && !opts.Yes {
			resourceWarnings := codebox.GuestResourceWarnings(hostResources, plan.GuestCPUs, plan.GuestMemoryMB)
			if len(resourceWarnings) > 0 {
				return 1, fmt.Errorf("non-interactive setup with high resource allocation requires explicit confirmation; rerun with --yes")
			}
		}
		if !tty || opts.Yes {
			break
		}
		if !advancedDone {
			// --advanced opens the expert settings once, after the
			// questions and before the configuration is applied (#333).
			advancedDone = true
			askQuestions = false
			answers.Advanced = true
			if useHuh {
				answers, err = askInitAdvancedHuh(opts, answers)
			} else {
				if _, readErr := in.Peek(1); readErr != nil {
					return 1, fmt.Errorf("aucune réponse ; rien n'a été enregistré : %w", readErr)
				}
				answers, err = askInitAdvancedLine(in, opts, answers)
			}
			if err != nil {
				return 1, err
			}
			continue
		}
		break
	}
	revokeContext7 := plan.ExistingManifest != nil && containsConnector(plan.ExistingManifest.MCPConnectors, "context7") && !containsConnector(plan.Answers.MCPConnectors, "context7")
	context7Revoked, context7Live := false, false
	if revokeContext7 {
		instance := codebox.InstanceName(plan.Answers.Root, filepath.Base(plan.Answers.Root))
		path := codebox.BindingApprovalsPath(mcpBindingStateDirFn(), instance)
		if err := codebox.RevokeBindingApproval(codebox.DefaultFS, path, codebox.CredentialContext7); err != nil {
			return 1, fmt.Errorf("Context7 credential approval could not be revoked; project selection was not changed: %w", err)
		}
		// The revoke is attempted regardless of the recorded runtime: the
		// recorded runtime can lag a runtime switch, and a preserved sandbox
		// from the previous runtime would keep its proxy registration (and
		// the guest's enabled connector) until its next boot. A nonexistent
		// instance is revoked=false, nil, so the call is safe when no
		// Microsandbox instance was ever created. When the control plane
		// cannot be reached to verify the instance, the deselection proceeds
		// with a warning: the host approval is already revoked (the primary
		// authorization), a stale registration in a preserved sandbox is
		// inert without it, and the next reconcile strips it because absence
		// is authoritative in the binding refresh.
		context7Revoked, context7Live, err = revokeMCPBindingFn(context.Background(), instance, codebox.CredentialContext7)
		if err != nil {
			if errors.Is(err, codebox.ErrLookupFailed) {
				fmt.Fprintf(os.Stderr, "Attention : impossible de vérifier l'instance Microsandbox %s pour retirer l'enregistrement Context7 (%v) ; l'autorisation de ce poste est révoquée et l'enregistrement obsolète sera retiré au prochain démarrage.\n", instance, err)
				context7Revoked, context7Live = false, false
				err = nil
			} else {
				return 1, fmt.Errorf("Context7 host approval was revoked, but the Microsandbox credential could not be revoked; project selection was not changed: %w", err)
			}
		}
	}
	if err := wizard.Apply(plan, opts.Replace); err != nil {
		if revokeContext7 {
			return 1, fmt.Errorf("project configuration was not written; Context7 authorization was revoked: %w", err)
		}
		return 1, err
	}
	if revokeContext7 {
		if context7Revoked && context7Live {
			fmt.Println("Context7 credential approval revoked and removed from the running guest.")
		} else if context7Revoked {
			fmt.Println("Context7 credential approval revoked and removed from the stopped guest.")
		} else {
			fmt.Println("Context7 credential approval revoked on this host.")
		}
	}
	if plan.Answers.GitHubWorkflow {
		if err := approveGitHubForProjectFn(plan.Answers.Root); err != nil {
			return 1, fmt.Errorf("project configuration was written, but GitHub approval was not recorded; run 'codebox bindings approve github': %w", err)
		}
		fmt.Println("Accord GitHub enregistré pour ce projet sur ce poste ; il n'est pas écrit dans le projet.")
	}
	if interactiveReview {
		// The interactive recap lands after the write (#333): the wizard
		// applies directly, so this is the summary of what was done.
		fmt.Print(codebox.FormatInteractiveInitReview(plan))
	}
	recordInitStats(plan.Answers)
	// The trailer is advice for the standalone command. When the launch flow
	// drives the setup, the launch is already continuing: telling the user to
	// run it would describe a step the code does not need.
	if opts.FromLaunch {
		fmt.Println("\nProjet configuré. Poursuite du lancement…")
	} else {
		fmt.Println("\nProjet configuré.\nLancez l'agent : codebox")
	}
	return 0, nil
}

func recordInitAbandon(root, stage string) {
	codebox.RecordStatsEvent(codebox.DefaultFS, codebox.DefaultStateDir(), codebox.StatsEventInput{
		Kind: "abandon", Stage: stage, Version: version, ProjectRoot: root,
	})
}

// recordInitStats queues the non-nominative init summary. It is a no-op
// without consent, and it never carries the project path or name.
func recordInitStats(answers codebox.InitAnswers) {
	selectedSkills := map[string]bool{}
	for _, id := range answers.Skills {
		selectedSkills[id] = true
	}
	skills := map[string]bool{}
	for _, id := range answers.KnownSkills {
		skills[id] = selectedSkills[id]
	}
	for id := range selectedSkills {
		if _, known := skills[id]; !known {
			skills[id] = true
		}
	}
	selectedMCPs := map[string]bool{}
	for _, id := range answers.MCPConnectors {
		selectedMCPs[id] = true
	}
	mcps := map[string]bool{}
	for _, connector := range codebox.MCPConnectors() {
		mcps[connector.ID] = selectedMCPs[connector.ID]
	}
	for _, connector := range codebox.BrowserMCPs() {
		mcps[connector.ID] = selectedMCPs[connector.ID]
	}
	storage := "versioned"
	if answers.SkillsLocalOnly {
		storage = "local-only"
	}
	codebox.RecordStatsEvent(codebox.DefaultFS, codebox.DefaultStateDir(), codebox.StatsEventInput{
		Kind:        "init",
		Version:     version,
		ProjectRoot: answers.Root,
		Init: &codebox.InitStats{
			Skills:        skills,
			MCPs:          mcps,
			GitHubCase:    answers.GitHubCase,
			Advanced:      answers.Advanced,
			Harness:       initHarnessStatsValue(answers.Harness),
			Runtime:       string(answers.Runtime),
			Isolation:     string(answers.Isolation),
			SkillsStorage: storage,
		},
	})
}

func initHarnessStatsValue(harness string) string {
	if strings.TrimSpace(harness) == "" {
		return "opencode"
	}
	return harness
}

// invalidInheritedInitSetting reports the first inherited setting that this
// host or today's catalogue rejects. The returned field name routes the
// repair: "runtime" reopens the VM step, anything else opens the expert
// settings screen (#333).
func invalidInheritedInitSetting(opts initOptions, answers codebox.InitAnswers) (string, error) {
	if !opts.Set["runtime"] {
		if _, err := codebox.ResolveRuntime("", string(answers.Runtime)); err != nil {
			return "runtime", err
		}
	}
	if !opts.Set["cpus"] && answers.CPUs != 0 && (answers.CPUs < 1 || answers.CPUs > opts.maxCPUs) {
		return "cpus", fmt.Errorf("processeurs demandés : %d. Maximum sur ce poste : %d processeurs", answers.CPUs, opts.maxCPUs)
	}
	if !opts.Set["memory-mb"] && answers.MemoryMB != 0 && (answers.MemoryMB < 1 || answers.MemoryMB > opts.maxMemoryMB) {
		return "memory-mb", fmt.Errorf("mémoire demandée : %s. Maximum sur ce poste : %s", codebox.FormatMemorySize(answers.MemoryMB), codebox.FormatMemorySize(opts.maxMemoryMB))
	}
	if !opts.Set["model"] && strings.TrimSpace(answers.Model) != "" {
		if _, err := validateInitModelInput(answers.Root, answers.Model, initHarnessValue(answers)); err != nil {
			return "model", err
		}
	}
	if !opts.Set["small-model"] && strings.TrimSpace(answers.SmallModel) != "" {
		if _, err := validateInitSmallModelInput(answers.Root, answers.SmallModel); err != nil {
			return "small-model", err
		}
	}
	return "", nil
}

func withBrowserResourceGuidance(answers codebox.InitAnswers) codebox.InitAnswers {
	if !codebox.HasBrowserMCPSelection(answers.MCPConnectors) {
		answers.BrowserResourceGuidance = ""
		return answers
	}
	// Recompute on every review pass: the user may have changed CPU or
	// memory settings in the edit form since the previous guidance was made.
	host, err := detectHostResourcesFn()
	guestCPUs, guestMemoryMB := answers.CPUs, answers.MemoryMB
	if err == nil {
		if guestCPUs <= 0 {
			guestCPUs = codebox.RecommendedDefaultGuestCPUs(host.CPUs)
		}
		if guestMemoryMB <= 0 {
			guestMemoryMB = codebox.RecommendedDefaultGuestMemoryMB(host.MemoryMB)
		}
	}
	answers.BrowserResourceGuidance = codebox.BrowserResourceGuidance(host, guestCPUs, guestMemoryMB, err)
	return answers
}

func seedInitAnswersFromManifest(opts initOptions, answers codebox.InitAnswers) codebox.InitAnswers {
	// Clear previous-project values first. If the new root has no manifest,
	// this leaves each non-flag answer at the built-in default instead of
	// carrying settings across projects. Flags remain the caller's explicit
	// choices.
	if !opts.Set["runtime"] {
		answers.Runtime = ""
	}
	if !opts.Set["isolation"] {
		answers.Isolation = ""
	}
	if !opts.Set["harness"] {
		answers.Harness = ""
	}
	if !opts.Set["model"] {
		answers.Model = ""
	}
	if !opts.Set["small-model"] {
		answers.SmallModel = ""
	}
	if !opts.Set["cpus"] {
		answers.CPUs = 0
	}
	if !opts.Set["memory-mb"] {
		answers.MemoryMB = 0
	}
	if !opts.Set["credential-ref"] {
		answers.CredentialRef = ""
	}
	if !opts.Set["github"] {
		answers.GitHubWorkflow = false
	}
	answers.GitHubRemote = codebox.GitHubRemote{}
	answers.BrowserResourceGuidance = ""
	if !opts.Set["mcps"] {
		answers.MCPConnectors = nil
		answers.MCPsSet = false
	}
	if !opts.Set["skills"] {
		answers.Skills = nil
		answers.SkillsSet = false
	}
	if !opts.Set["skills-storage"] {
		answers.SkillsLocalOnly = false
		answers.SkillsLocalOnlySet = false
	}
	project, err := codebox.DiscoverProject(answers.Root)
	if err != nil {
		return answers
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(project.Root))
	if err != nil {
		return answers
	}
	if !opts.Set["runtime"] {
		answers.Runtime = codebox.Runtime(manifest.Runtime)
	}
	if !opts.Set["isolation"] {
		answers.Isolation = codebox.Isolation(manifest.Isolation)
	}
	// The harness is part of what reconfiguration preserves: rerunning init on
	// a Pi project without --harness must not quietly write OpenCode back,
	// which is exactly what a non-interactive `init --replace --yes` would do.
	if !opts.Set["harness"] {
		answers.Harness = manifest.Harness
	}
	if !opts.Set["model"] {
		answers.Model = manifest.Model
	}
	if !opts.Set["small-model"] {
		answers.SmallModel = manifest.SmallModel
	}
	if !opts.Set["cpus"] {
		answers.CPUs = manifest.CPUs
	}
	if !opts.Set["memory-mb"] {
		answers.MemoryMB = manifest.MemoryMB
	}
	if !opts.Set["credential-ref"] {
		answers.CredentialRef = manifest.CredentialRef
	}
	if !opts.Set["mcps"] {
		answers.MCPConnectors = append([]string(nil), manifest.MCPConnectors...)
	}
	if !opts.Set["skills-storage"] {
		answers.SkillsLocalOnly = manifest.SkillsLocalOnly
	}
	return answers
}

func containsConnector(ids []string, wanted string) bool {
	for _, id := range ids {
		if id == wanted {
			return true
		}
	}
	return false
}

var projectSkillCatalogueFn = codebox.ProjectSkillCatalogue
var resolveInitProjectSkillsFn = codebox.ResolveProjectSkills
var verifyInitProjectSkillsFn = func(ids []string, locks map[string]codebox.SkillLock) error {
	_, err := codebox.LoadLockedSkillPackages(ids, locks)
	return err
}
var detectHostResourcesFn = codebox.DetectHostResources

func currentProjectSkillSelection(root string) ([]string, error) {
	project, err := codebox.DiscoverProject(root)
	if err != nil {
		return nil, err
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(project.Root))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !manifest.SkillsLocalOnly {
		return append([]string(nil), manifest.Skills...), nil
	}
	path, err := codebox.LocalSkillSelectionsPath(codebox.DefaultStateDir(), project.InstanceName())
	if err != nil {
		return nil, err
	}
	selection, err := codebox.ReadLocalSkillSelections(codebox.DefaultFS, path)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), selection.Skills...), nil
}

// validateInitOptions rejects a bad flag value before any prompt runs. The
// engine validates everything again; this is only about failing early with the
// message a command-line user expects.
func validateInitOptions(opts initOptions) error {
	if opts.Set["small-model"] {
		if _, err := validateInitSmallModelInput(opts.Root, opts.SmallModel); err != nil {
			return fmt.Errorf("--small-model: %w", err)
		}
	}
	if opts.Set["skills"] {
		if err := codebox.ValidateProjectSkillIDs(opts.Skills); err != nil {
			return err
		}
	}
	if opts.Set["runtime"] {
		if _, err := codebox.ResolveRuntime(opts.Runtime, ""); err != nil {
			return err
		}
	}
	if opts.Set["isolation"] {
		if _, err := codebox.ResolveIsolation(opts.Isolation, ""); err != nil {
			return err
		}
	}
	// An explicit zero is not "use the default": the flag being present means
	// the caller wants to set the value, and 0 is outside the supported range.
	if opts.Set["cpus"] && (opts.CPUs < 1 || opts.CPUs > codebox.MaxSandboxCPUs) {
		return fmt.Errorf("--cpus must be 1 to %d, got %d", codebox.MaxSandboxCPUs, opts.CPUs)
	}
	if opts.Set["memory-mb"] && (opts.MemoryMB < 1 || opts.MemoryMB > codebox.MaxSandboxMemoryMB) {
		return fmt.Errorf("--memory-mb must be 1 to %d, got %d", codebox.MaxSandboxMemoryMB, opts.MemoryMB)
	}
	if opts.Set["mcps"] {
		if _, err := codebox.ValidateMCPNewSelection(opts.MCPConnectors); err != nil {
			return err
		}
	}
	return nil
}

// askHarnessStepLine is the line-mode twin of askHarnessStepHuh (#333):
// OpenCode is the historical harness and the recommended default; Pi is the
// lighter terminal agent, installed in the same isolated machine (#176). A
// blank answer keeps the current choice.
func askHarnessStepLine(in *bufio.Reader, opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	fmt.Printf("\n%s\n", initHarnessStepTitle(opts))
	fmt.Println("OpenCode : agent historique, avec ses greffons et ses MCP. Pi : agent en terminal plus léger, dans la même machine isolée.")
	current := effectiveInitHarness(answers)
	for {
		answer, ok := promptLine(in, "  harnais ["+current+"] (opencode/pi) : ")
		if !ok {
			return answers, errInitNoAnswer
		}
		if strings.TrimSpace(answer) == "" {
			answers.Harness = current
			return answers, nil
		}
		value := strings.ToLower(strings.TrimSpace(answer))
		resolved, err := codebox.ResolveHarness(value)
		if err != nil {
			fmt.Println("  Répondez opencode ou pi.")
			continue
		}
		answers.Harness = string(resolved)
		return answers, nil
	}
}

// askVMStepLine is the line-mode twin of askVMStepHuh (#333): Microsandbox is
// recommended, Tart and agent-vm share the project folder with the VM. It is
// skipped when --runtime came from the command line, and a blank answer keeps
// the current choice.
func askVMStepLine(in *bufio.Reader, opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	if opts.Set["runtime"] {
		return answers, nil
	}
	fmt.Printf("\n%s\n", initVMStepTitle(opts))
	fmt.Println("Microsandbox : copie filtrée du projet (.env et secrets exclus), démarrage rapide. Tart et agent-vm : votre dossier est partagé avec la VM.")
	current := effectiveInitRuntime(answers)
	if notice := initRuntimeNotice(current); notice != "" {
		fmt.Println(notice)
	}
	for {
		answer, ok := promptLine(in, "  machine ["+string(current)+"] (microsandbox/tart/agent-vm) : ")
		if !ok {
			return answers, errInitNoAnswer
		}
		if strings.TrimSpace(answer) == "" {
			answers.Runtime = current
			return answers, nil
		}
		resolved, err := codebox.ResolveRuntime(strings.TrimSpace(answer), "")
		if err != nil {
			fmt.Printf("  %v.\n", err)
			continue
		}
		answers.Runtime = resolved
		return answers, nil
	}
}

// askInitQuestions prompts only for the decisions the command line did not
// supply, showing the value that will be used if the answer is empty — which
// is the flag's value when one was given, not the engine default. The expert
// settings (isolation, resources, credential reference) are not asked here:
// they open with --advanced (#333).
func askInitQuestions(in *bufio.Reader, opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	fmt.Println("codebox init — project configuration")
	fmt.Println()
	if !opts.Set["root"] {
		currentRoot := answers.Root
		fmt.Printf("Project root [%s]:\n", answers.Root)
		answer, ok := promptLine(in, "  root: ")
		if !ok {
			return answers, errInitNoAnswer
		}
		if strings.TrimSpace(answer) != "" {
			answers.Root = strings.TrimSpace(answer)
			if answers.Root != currentRoot {
				answers = seedInitAnswersFromManifest(opts, answers)
			}
		}
	}
	// The harness and VM steps come after the root prompt: a root change
	// reseeds the answers from the target project's manifest, and asking
	// before it would wipe the user's fresh choices (#333).
	var stepErr error
	if !opts.Set["harness"] {
		answers, stepErr = askHarnessStepLine(in, opts, answers)
		if stepErr != nil {
			return answers, stepErr
		}
	}
	answers, stepErr = askVMStepLine(in, opts, answers)
	if stepErr != nil {
		return answers, stepErr
	}
	if !opts.Set["skills"] {
		catalogue, catalogueErr := projectSkillCatalogueFn(context.Background())
		if catalogueErr != nil {
			fmt.Fprintf(os.Stderr, "Attention : le catalogue de skills épinglé du projet est indisponible : %v\n", catalogueErr)
		} else {
			for _, skill := range catalogue {
				answers.KnownSkills = append(answers.KnownSkills, skill.ID)
			}
		}
		if !answers.SkillsSet {
			current, err := currentProjectSkillSelection(answers.Root)
			if err != nil {
				return answers, fmt.Errorf("read current project skills: %w", err)
			}
			answers.Skills = current
			if catalogueErr == nil && len(current) == 0 && !projectManifestExists(answers.Root) && initSkillInCatalogue(catalogue, "official/securite-developpement") {
				answers.Skills = []string{"official/securite-developpement"}
				answers.SkillsSet = true
			}
		}
		if catalogueErr == nil {
			for {
				printInitProjectSkills(answers.Skills, catalogue, initSkillsStepTitle(opts))
				current := strings.Join(initSkillNames(answers.Skills), ",")
				if current == "" {
					current = "aucune"
				}
				answer, ok := promptLine(in, fmt.Sprintf("  Skills sélectionnées (%d) [%s] (noms séparés par des virgules; Entrée garde, `none` efface): ", len(answers.Skills), current))
				if !ok {
					return answers, errInitNoAnswer
				}
				if strings.TrimSpace(answer) != "" {
					value := strings.TrimSpace(answer)
					if strings.EqualFold(value, "none") {
						answers.Skills = nil
						answers.SkillsSet = true
					} else {
						selected, err := parseInitSkillSelection(value)
						if err != nil {
							fmt.Printf("  %v. Corrigez votre sélection et réessayez.\n", err)
							continue
						}
						selected = append(selected, retainedInitSkillIDs(answers.Skills, initSkillOptions(catalogue))...)
						answers.Skills = selected
						answers.SkillsSet = true
					}
				}
				if len(answers.Skills) > 0 {
					break
				}
				proceed, err := confirmNoInitSkills(in)
				if err != nil {
					return answers, err
				}
				if proceed {
					answers.SkillsSet = true
					break
				}
			}
			// Skills absent from the catalogue cannot be resolved again:
			// record them so Plan preserves their existing pins verbatim.
			answers.RetainedSkills = retainedInitSkillIDs(answers.Skills, initSkillOptions(catalogue))
		} else {
			fmt.Println("La sélection actuelle est conservée. Relancez `codebox init` lorsque le catalogue est accessible pour modifier les skills.")
		}
	}
	if !opts.Set["skills-storage"] {
		localOnly := answers.SkillsLocalOnly
		if !answers.SkillsLocalOnlySet {
			if pc, err := codebox.DiscoverProject(answers.Root); err == nil {
				if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil {
					localOnly = manifest.SkillsLocalOnly
				}
			}
		}
		defaultStorage := "Avec le projet"
		if localOnly {
			defaultStorage = "Sur ce poste uniquement"
		}
		fmt.Println("Où enregistrer les skills ?")
		fmt.Println("  Avec le projet : elles sont versionnées, un coéquipier qui clone le projet les retrouve.")
		fmt.Println("  Sur ce poste uniquement : rien n'est ajouté au dépôt.")
		answer, ok := promptLine(in, "  stockage ["+defaultStorage+"]: ")
		if !ok {
			return answers, errInitNoAnswer
		}
		answers.SkillsLocalOnly = localOnly
		if strings.TrimSpace(answer) != "" {
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "projet", "avec le projet", "versioned":
				answers.SkillsLocalOnly = false
			case "poste", "sur ce poste uniquement", "local-only":
				answers.SkillsLocalOnly = true
			default:
				return answers, fmt.Errorf("storage must be 'projet' or 'poste'")
			}
		}
		answers.SkillsLocalOnlySet = true
	}
	if !opts.Set["mcps"] {
		fmt.Printf("\n%s\n", initMCPStepTitle(opts))
		fmt.Println(codebox.MCPStepIntro)
		current := append([]string(nil), answers.MCPConnectors...)
		haveManifest := false
		if !answers.MCPsSet {
			current = nil
			if pc, err := codebox.DiscoverProject(answers.Root); err == nil {
				if manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil {
					current = manifest.MCPConnectors
					haveManifest = true
				}
			}
		}
		if len(current) == 0 && !haveManifest && !answers.MCPsSet {
			current = append([]string(nil), codebox.DefaultFreshMCPIDs...)
		}
		answers.MCPConnectors = append([]string(nil), current...)
		for {
			for _, choice := range codebox.MCPSelections() {
				marker := "[ ]"
				if containsString(current, choice.ID) {
					marker = "[x]"
				}
				fmt.Printf("  %s %-14s %s\n", marker, choice.ID, choice.Description)
			}
			currentLabel := strings.Join(current, ",")
			if currentLabel == "" {
				currentLabel = "none"
			}
			answer, ok := promptLine(in, "  MCP IDs ["+currentLabel+"] (IDs séparés par des virgules ; Entrée garde la sélection, `none` efface tout) : ")
			if !ok {
				return answers, errInitNoAnswer
			}
			trimmed := strings.TrimSpace(answer)
			if trimmed == "" {
				break
			}
			if strings.EqualFold(trimmed, "none") {
				if !promptNoMCPConfirmation(in) {
					continue
				}
				answers.MCPsSet = true
				answers.MCPConnectors = nil
				current = nil
				break
			}
			var ids []string
			for _, id := range strings.Split(trimmed, ",") {
				ids = append(ids, strings.TrimSpace(id))
			}
			validated, err := codebox.ValidateMCPNewSelection(ids)
			if err != nil {
				fmt.Printf("  %s\n", err)
				continue
			}
			answers.MCPsSet = true
			answers.MCPConnectors = validated
			current = validated
			break
		}
		for _, id := range answers.MCPConnectors {
			if reason, removed := codebox.RemovedMCPIDs[id]; removed {
				fmt.Printf("  %s : %s.\n", id, reason)
			}
		}
		if codebox.HasBrowserMCPSelection(answers.MCPConnectors) {
			fmt.Println(codebox.BrowserDownloadNote)
		}
	}
	if codebox.HasBrowserMCPSelection(answers.MCPConnectors) {
		host, detectErr := detectHostResourcesFn()
		answers.BrowserResourceGuidance = codebox.BrowserResourceGuidance(host, answers.CPUs, answers.MemoryMB, detectErr)
		fmt.Println("\n" + answers.BrowserResourceGuidance)
	}
	if !opts.Set["github"] {
		// The GitHub step verifies on this screen and renders one distinct
		// screen per situation (#169): an approved repo+token, a project not
		// linked to GitHub, a project with no stored token, or a token
		// without the needed rights. Nothing is decided here that the later
		// review could reject, so a refusal never throws away the other
		// answers (#155).
	githubStep:
		for {
			probe := githubInitProbeFn(answers.Root)
			switch {
			case !gitHubAvailableForRuntime(answers.Runtime):
				// The Guest GitHub workflow is Microsandbox-only. Offering the
				// approval under Tart or agent-vm would only fail at the end,
				// after every other answer, which is the #155 symptom. Say so
				// on this screen and keep the run going.
				fmt.Println(gitHubRuntimeScreen(answers.Runtime, initGitHubStepTitle(opts)))
				quit, err := gitHubContinueLoop(in)
				if err != nil {
					return answers, err
				}
				if quit {
					recordInitAbandon(answers.Root, "github")
					return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
				}
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
				break githubStep
			case !probe.HasOrigin:
				answers.GitHubCase = 2
				fmt.Println(gitHubNoOriginScreen(initGitHubStepTitle(opts)))
				quit, err := gitHubContinueLoop(in)
				if err != nil {
					return answers, err
				}
				if quit {
					recordInitAbandon(answers.Root, "github")
					return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
				}
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
				break githubStep
			case probe.TokenError != nil:
				answers.GitHubCase = 3
				// A locked or unavailable store must not be reported as "no
				// token yet": the remedy is to unlock it, not to add a token.
				// GitHub stays optional, so this is a screen with a way out
				// rather than an abort of the whole wizard.
				fmt.Println(gitHubStoreErrorScreen(probe.TokenError, initGitHubStepTitle(opts)))
				quit, err := gitHubContinueLoop(in)
				if err != nil {
					return answers, err
				}
				if quit {
					recordInitAbandon(answers.Root, "github")
					return answers, fmt.Errorf("vous avez quitté l'assistant ; rien n'a été écrit")
				}
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
				break githubStep
			case !probe.HasToken:
				answers.GitHubCase = 3
				fmt.Println(gitHubNoTokenScreen(probe, initGitHubStepTitle(opts)))
				addNow, err := gitHubTokenLoop(in)
				if err != nil {
					return answers, err
				}
				if addNow {
					stored, _, _, err := githubTokenStage(context.Background(), initGitHubAddWizard(), in)
					if err != nil {
						return answers, err
					}
					if stored {
						continue
					}
				}
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
				break githubStep
			case probe.Rights == codebox.GitHubRepoRightsOK:
				answers.GitHubCase = 1
				fmt.Println(gitHubApprovedScreen(probe, initGitHubStepTitle(opts)))
				yes, err := gitHubYesDefault(in)
				if err != nil {
					return answers, err
				}
				answers.GitHubRemote = probe.Remote
				answers.GitHubWorkflow = yes
				break githubStep
			default:
				answers.GitHubCase = 4
				fmt.Println(gitHubInsufficientScreen(probe, initGitHubStepTitle(opts)))
				reverify, err := gitHubRightsLoop(in)
				if err != nil {
					return answers, err
				}
				if reverify {
					continue
				}
				answers.GitHubWorkflow = false
				answers.GitHubRemote = codebox.GitHubRemote{}
				break githubStep
			}
		}
	}
	return answers, nil
}

// askInitAdvancedLine is the line-wizard half of the expert settings screen
// (#333): isolation and guest resources, opened with --advanced. A bad value
// re-asks the question instead of aborting, so a typo never loses the answers
// already given. Blank keeps the current value; `default` resets to the
// built-in default.
func askInitAdvancedLine(in *bufio.Reader, opts initOptions, answers codebox.InitAnswers) (codebox.InitAnswers, error) {
	fmt.Println()
	fmt.Println("Réglages avancés")
	fmt.Println("Ces valeurs conviennent à la plupart des projets. Modifiez-les seulement si besoin.")
	fmt.Println("Petit modèle : " + initSmallModelDisplayName(answers) + " — sert à générer les titres de session.")

	if !opts.Set["isolation"] {
		fmt.Println()
		fmt.Println("Mode d'isolation :")
		fmt.Println("  complet : l'agent et son interface tournent dans la machine isolée")
		fmt.Println("  partiel : l'interface tourne sur votre poste, l'agent dans la machine isolée")
		current := answers.Isolation
		if current == "" {
			current = codebox.IsolationFull
		}
		for {
			answer, ok := promptLine(in, "  isolation ["+isolationDisplayName(current)+"] (`default` revient à complet) : ")
			if !ok {
				return answers, errInitNoAnswer
			}
			if strings.TrimSpace(answer) == "" {
				break
			}
			switch strings.ToLower(strings.TrimSpace(answer)) {
			case "default", "défaut":
				answers.Isolation = codebox.IsolationFull
			case "complet", "full":
				answers.Isolation = codebox.IsolationFull
			case "partiel", "backend":
				if codebox.HarnessRequiresFullIsolation(initHarnessValue(answers)) {
					fmt.Println("  Pi exige l'isolation complète : il n'a pas de mode serveur auquel s'attacher depuis votre poste.")
					fmt.Println("  Choisissez « complet », ou revenez au harnais OpenCode.")
					continue
				}
				answers.Isolation = codebox.IsolationBackend
			default:
				fmt.Println("  Choisissez complet ou partiel.")
				continue
			}
			break
		}
	}

	if !opts.Set["cpus"] {
		for {
			answer, ok := promptLine(in, fmt.Sprintf("  processeurs [%d] (maximum sur ce poste : %d ; `default` revient à la valeur par défaut) : ",
				resolvedOrDefault(answers.CPUs, codebox.RecommendedDefaultGuestCPUs(opts.hostCPUs)), opts.maxCPUs))
			trimmed := strings.TrimSpace(answer)
			if !ok {
				return answers, errInitNoAnswer
			}
			if trimmed == "" {
				break
			}
			if strings.EqualFold(trimmed, "default") {
				answers.CPUs = 0
				break
			}
			n, convErr := strconv.Atoi(trimmed)
			if convErr != nil {
				fmt.Println("  Entrez un nombre entier.")
				continue
			}
			if n < 1 {
				fmt.Println("  Au moins 1 processeur.")
				continue
			}
			if n > opts.maxCPUs {
				fmt.Printf("  Maximum sur ce poste : %d processeurs.\n", opts.maxCPUs)
				continue
			}
			answers.CPUs = n
			break
		}
	}

	if !opts.Set["memory-mb"] {
		for {
			answer, ok := promptLine(in, fmt.Sprintf("  mémoire [%s] (6 Go conseillés avec chrome-devtools ; `default` revient à la valeur par défaut) : ",
				codebox.FormatMemorySize(resolvedOrDefault(answers.MemoryMB, codebox.RecommendedDefaultGuestMemoryMB(opts.hostMemoryMB)))))
			trimmed := strings.TrimSpace(answer)
			if !ok {
				return answers, errInitNoAnswer
			}
			if trimmed == "" {
				break
			}
			if strings.EqualFold(trimmed, "default") {
				answers.MemoryMB = 0
				break
			}
			n, convErr := codebox.ParseMemorySize(trimmed)
			if convErr != nil {
				fmt.Println("  Format attendu : 4096, 4G ou 2.5G.")
				continue
			}
			if n < 1 {
				fmt.Println("  Au moins 1 Mo.")
				continue
			}
			if n > opts.maxMemoryMB {
				fmt.Printf("  Maximum sur ce poste : %s.\n", codebox.FormatMemorySize(opts.maxMemoryMB))
				continue
			}
			answers.MemoryMB = n
			break
		}
	}

	if notice := initRuntimeNotice(effectiveInitRuntime(answers)); notice != "" {
		fmt.Println(notice)
	}
	if !opts.Set["model"] {
		for {
			answer, ok := promptLine(in, "  modèle principal ["+initModelDisplayName(answers.Model)+"] ('défaut' reprend le modèle de setup ; Entrée conserve le choix) : ")
			if !ok {
				return answers, errInitNoAnswer
			}
			if strings.TrimSpace(answer) == "" {
				break
			}
			warning, err := validateInitModelInput(answers.Root, answer, initHarnessValue(answers))
			if err != nil {
				fmt.Printf("  %s\n", err)
				continue
			}
			answers.Model = normalizeInitModelInput(answer)
			if warning != "" {
				fmt.Println("  " + warning)
			}
			break
		}
	}
	if !opts.Set["small-model"] {
		for {
			answer, ok := promptLine(in, "  petit modèle ["+initSmallModelDisplayName(answers)+"] ('défaut' suit le modèle principal ; Entrée conserve le choix) : ")
			if !ok {
				return answers, errInitNoAnswer
			}
			if strings.TrimSpace(answer) == "" {
				break
			}
			warning, err := validateInitSmallModelInput(answers.Root, answer)
			if err != nil {
				fmt.Printf("  %s\n", err)
				continue
			}
			answers.SmallModel = normalizeInitModelInput(answer)
			if warning != "" {
				fmt.Println("  " + warning)
			}
			break
		}
	}
	return answers, nil
}

// isolationDisplayName renders the isolation mode in the wizard's wording.
func isolationDisplayName(iso codebox.Isolation) string {
	if iso == codebox.IsolationBackend {
		return "partiel"
	}
	return "complet"
}

// promptNoMCPConfirmation asks the zero-selection confirmation and returns
// true when the user keeps an empty MCP set. It reads through the wizard's
// own buffered reader: wrapping `in` in a fresh bufio.Reader here would
// prefetch and discard the answers queued behind this one.
func promptNoMCPConfirmation(in *bufio.Reader) bool {
	fmt.Print("Aucun MCP sélectionné. Continuer sans MCP ? (O/n) ")
	answer, err := in.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "" || answer == "o" || answer == "oui" || answer == "y" || answer == "yes"
}

func printProjectSkills(skills []codebox.ProjectSkill) {
	for _, skill := range skills {
		label := skill.ID
		if skill.Experimental {
			label += " [experimental]"
		}
		desc := strings.Join(strings.Fields(skill.Description), " ")
		fmt.Println(truncateLine(fmt.Sprintf("  %-36s %s", label, desc), 78))
	}
}

func printInitProjectSkills(selected []string, catalogue []codebox.ProjectSkill, stepPrefix string) {
	selectedSet := make(map[string]bool, len(selected))
	for _, id := range selected {
		selectedSet[id] = true
	}
	fmt.Println("\n" + initSkillsTitle(len(selected), stepPrefix))
	fmt.Println("Une skill apporte à l'agent un savoir-faire de l'État : règles, références, exemples.")
	fmt.Println("Il s'en sert quand votre demande s'y rapporte.")
	for _, id := range initSkillOrder {
		check := " "
		if selectedSet[id] {
			check = "x"
		}
		name := strings.TrimPrefix(id, "official/")
		line := fmt.Sprintf("  [%s] %s %s", check, name, initSkillDescriptions[id])
		if ansi.StringWidth(line) > 80 {
			fmt.Printf("  [%s] %s\n      %s\n", check, name, initSkillDescriptions[id])
		} else {
			fmt.Println(line)
		}
	}
	fmt.Println("Saisissez un ou plusieurs noms séparés par des virgules. Entrée garde la sélection.")
	fmt.Println(initSkillsCountLabel(len(selected)))
	retained := retainedInitSkillIDs(selected, initSkillOptions(catalogue))
	if len(retained) > 0 {
		fmt.Printf("Skills déjà configurées hors de la liste conservées si vous saisissez de nouveaux noms : %s. `none` efface tout.\n",
			strings.Join(initSkillNames(retained), ", "))
	}
}

func initSkillsCountLabel(count int) string {
	if count == 1 {
		return "1 skill sélectionnée."
	}
	return fmt.Sprintf("%d skills sélectionnées.", count)
}

func initSkillNames(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, "official/") {
			names = append(names, strings.TrimPrefix(id, "official/"))
		} else {
			names = append(names, id)
		}
	}
	return names
}

func parseInitSkillSelection(input string) ([]string, error) {
	byName := make(map[string]string, len(initSkillOrder))
	for _, id := range initSkillOrder {
		byName[strings.TrimPrefix(id, "official/")] = id
		byName[id] = id
	}
	var selected []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(input, ",") {
		name = strings.TrimSpace(name)
		id, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown skill %q; choose one of the six listed names", name)
		}
		if !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	return selected, nil
}

func confirmNoInitSkills(in *bufio.Reader) (bool, error) {
	fmt.Println("Aucune skill sélectionnée. Continuer sans skill ? (O/n)")
	answer, ok := promptLine(in, "  ")
	if !ok {
		return false, errors.New("no answer for the empty skill selection confirmation")
	}
	answer = strings.TrimSpace(answer)
	return answer == "" || strings.EqualFold(answer, "o") || strings.EqualFold(answer, "oui") || strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func projectManifestExists(root string) bool {
	pc, err := codebox.DiscoverProject(root)
	if err != nil {
		return false
	}
	_, err = codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root))
	return err == nil
}

// truncateLine caps the composed line at n terminal cells, so one catalogue
// entry never wraps on an 80-column terminal. Width is measured in display
// cells (a CJK or emoji rune occupies two), and truncation stays on rune
// boundaries, so wide characters are never split mid-sequence.
func truncateLine(s string, n int) string {
	if ansi.StringWidth(s) <= n {
		return s
	}
	var b strings.Builder
	width := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if width+w > n-1 {
			break
		}
		b.WriteRune(r)
		width += w
	}
	b.WriteString("…")
	return b.String()
}

// The GitHub step (3.9) renders one distinct screen per situation and
// verifies on that screen, never after the recap (#169). The screen text is
// the approved copy from the wizard epic; the input helpers keep an invalid
// answer on the same screen instead of throwing the run away.

func gitHubRepoDisplay(r codebox.GitHubRemote) string {
	if r.Repo == "" {
		return ""
	}
	return "github.com/" + r.Repo
}

// gitHubAccountLabel names the account the verified token authenticates as.
// The name is what makes the check verifiable by the reader. When GitHub did
// not report it, the rights are still verified, so say that rather than
// rendering an empty "compte ,". Both UIs share this so they cannot drift.
func gitHubAccountLabel(p codebox.GitHubProjectStatus) string {
	if p.Login == "" {
		return "jeton accepté"
	}
	return "compte " + p.Login
}

func gitHubApprovedScreen(p codebox.GitHubProjectStatus, title string) string {
	return fmt.Sprintf(`%s

Dépôt détecté : %s
Jeton GitHub : %s, droits vérifiés sur ce dépôt ✅

Autoriser l'agent à pousser ses modifications sur une branche
et à ouvrir une pull request en brouillon sur ce dépôt ? (O/n)
Vous gardez la main : rien n'est fusionné sans votre accord.
`, title, gitHubRepoDisplay(p.Remote), gitHubAccountLabel(p))
}

func gitHubStoreErrorScreen(err error, title string) string {
	return fmt.Sprintf(`%s

Le mot de passe de votre poste protège les identifiants, et il n'a pas pu être
lu. Tant qu'il est verrouillé, codebox ne peut pas savoir si un jeton GitHub
est disponible.

  > Continuer sans GitHub
    Quitter, déverrouiller le trousseau, puis relancer : codebox init

Détail : %v
`, title, err)
}

func gitHubNoOriginScreen(title string) string {
	return title + `

Ce projet n'est pas relié à GitHub.
L'agent travaille sur une copie du projet. Pour récupérer ses modifications
dans votre dossier, vous les relirez puis les appliquerez avec : codebox workspace apply

  > Continuer
    Quitter, relier le projet à GitHub, puis relancer : codebox init
`
}

// gitHubAvailableForRuntime reports whether the chosen runtime can support the
// GitHub workflow at all. Only Microsandbox seals the workspace and hands the
// guest a proxied credential; Tart and agent-vm mount the project, so there is
// no branch to push from a sealed copy.
func gitHubAvailableForRuntime(runtime codebox.Runtime) bool {
	if runtime == "" {
		return true
	}
	return runtime == codebox.RuntimeMicrosandbox
}

func gitHubRuntimeScreen(runtime codebox.Runtime, title string) string {
	name := string(runtime)
	if name == "" {
		name = string(codebox.RuntimeMicrosandbox)
	}
	return fmt.Sprintf(`%s

Le partage de modifications par GitHub n'est pas disponible avec %s.
Il fonctionne avec Microsandbox, qui travaille sur une copie scellée du projet.
Avec Tart et agent-vm, le dossier du projet est monté dans la machine : vous
récupérez les modifications directement dans votre dossier.

  > Continuer sans GitHub
    Quitter, choisir Microsandbox dans les questions précédentes, puis relancer
`, title, name)
}

func gitHubNoTokenScreen(p codebox.GitHubProjectStatus, title string) string {
	return fmt.Sprintf(`%s

Dépôt détecté : %s
Aucun jeton GitHub n'est enregistré sur ce poste.

  > Plus tard (codebox auth add github, puis codebox init)
    Ajouter un jeton maintenant      → même écran qu'au setup (cf. 3.4)
`, title, gitHubRepoDisplay(p.Remote))
}

func gitHubInsufficientScreen(p codebox.GitHubProjectStatus, title string) string {
	return fmt.Sprintf(`%s

Dépôt détecté : %s
%s

[R] Revérifier  [C] Continuer sans GitHub
`, title, gitHubRepoDisplay(p.Remote), gitHubRightsMessage(p))
}

func gitHubRightsMessage(p codebox.GitHubProjectStatus) string {
	switch p.Rights {
	case codebox.GitHubRepoRightsNoAccess:
		return fmt.Sprintf("Votre jeton n'a pas accès à %s. Ajoutez ce dépôt au jeton sur GitHub.", p.Remote.Repo)
	case codebox.GitHubRepoRightsInsufficient:
		return "Votre jeton n'a pas les droits nécessaires. Il lui faut Contents et Pull requests en lecture et écriture."
	case codebox.GitHubRepoRightsSSO:
		owner := p.Remote.Repo
		if parts := strings.SplitN(owner, "/", 2); len(parts) == 2 {
			owner = parts[0]
		}
		return fmt.Sprintf("Votre jeton n'est pas autorisé pour l'organisation %s. Autorisez-le pour cette organisation sur GitHub (Single sign-on), puis revérifiez.", owner)
	case codebox.GitHubRepoRightsOrgPending:
		owner := p.Remote.Repo
		if parts := strings.SplitN(owner, "/", 2); len(parts) == 2 {
			owner = parts[0]
		}
		return fmt.Sprintf("L'organisation %s n'a pas encore approuvé votre jeton.", owner)
	case codebox.GitHubRepoRightsInvalid:
		return "Votre jeton n'est plus accepté par GitHub. Vérifiez-le avec « codebox auth add github »."
	default:
		return "Impossible de vérifier les droits de votre jeton pour le moment (réseau)."
	}
}

var gitHubNoOriginPrompt = "Continuer (Entrée) ou Quitter (q) : "
var gitHubNoTokenPrompt = "Plus tard (Entrée) ou Ajouter un jeton maintenant (a) : "
var gitHubRightsPrompt = "[R] Revérifier  [C] Continuer sans GitHub : "

// gitHubYesDefault reads the case-1 (O/n) answer. Enter defaults to Oui;
// only an explicit non/non declines.
func gitHubYesDefault(in *bufio.Reader) (bool, error) {
	for {
		// The screen above already ends with the question and its (O/n)
		// hint, so the prompt stays empty rather than asking twice.
		answer, ok := promptLine(in, "")
		if !ok {
			return false, fmt.Errorf("no answer; nothing was written")
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "o", "oui", "y", "yes":
			return true, nil
		case "n", "non", "no":
			return false, nil
		default:
			fmt.Println("Réponse non reconnue. Répondez O (oui) ou n (non).")
		}
	}
}

// gitHubContinueLoop reads the case-2 choice. It reports true when the user
// chooses to quit init. Enter defaults to Continuer.
func gitHubContinueLoop(in *bufio.Reader) (bool, error) {
	for {
		answer, ok := promptLine(in, gitHubNoOriginPrompt)
		if !ok {
			return false, fmt.Errorf("no answer; nothing was written")
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "c", "continuer", "continuer sans github":
			return false, nil
		case "q", "quitter":
			return true, nil
		default:
			fmt.Println("Réponse non reconnue. Continuer (Entrée) ou quitter (q).")
		}
	}
}

// gitHubTokenLoop reads the case-3 choice. It reports true when the user
// wants to add a token now. Enter defaults to Plus tard.
func gitHubTokenLoop(in *bufio.Reader) (bool, error) {
	for {
		answer, ok := promptLine(in, gitHubNoTokenPrompt)
		if !ok {
			return false, fmt.Errorf("no answer; nothing was written")
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "p", "plus tard":
			return false, nil
		case "a", "ajouter", "ajouter un jeton maintenant":
			return true, nil
		default:
			fmt.Println("Réponse non reconnue. Plus tard (Entrée) ou Ajouter maintenant (a).")
		}
	}
}

// gitHubRightsLoop reads the case-4 choice. It reports true when the user
// wants to re-verify. Enter defaults to Continuer sans GitHub.
func gitHubRightsLoop(in *bufio.Reader) (bool, error) {
	for {
		answer, ok := promptLine(in, gitHubRightsPrompt)
		if !ok {
			return false, fmt.Errorf("no answer; nothing was written")
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "r", "revérifier", "reverifier":
			return true, nil
		case "", "c", "continuer", "continuer sans github":
			return false, nil
		default:
			fmt.Println("Réponse non reconnue. [R] Revérifier ou [C] Continuer sans GitHub.")
		}
	}
}

// initGitHubAddWizard builds a minimal setup wizard for the "Ajouter un jeton
// maintenant" branch of the case-3 screen. It reuses setup's own token stage,
// so the add flow and its validation are identical to setup's (cf. 3.4).
func initGitHubAddWizard() codebox.SetupWizard {
	return codebox.SetupWizard{
		FS: codebox.DefaultFS,
		Store: func(ctx context.Context) (codebox.SetupStore, error) {
			return authStore(false)
		},
		ValidateGitHub: githubTokenProbeFn,
	}
}

// githubInitProbeFn classifies the project's GitHub situation for the init
// screen (origin present, token present, and what the token may do). Tests
// replace this seam to drive each of the four situations without a network or
// a real credential store.
var githubInitProbeFn = func(root string) codebox.GitHubProjectStatus {
	return codebox.ProbeGitHubForProject(context.Background(), root, nil)
}

// githubInitPreflightFn resolves the stored token and the host origin before
// the project manifest is written. Tests replace this seam so they do not
// inherit a real keychain or repository remote.
var githubInitPreflightFn = func(root string) (codebox.GitHubRemote, error) {
	if _, err := codebox.ReadStoredCredential(context.Background(), codebox.CredentialGithub); err != nil {
		return codebox.GitHubRemote{}, fmt.Errorf("GitHub workflow requires a stored GitHub credential; add one with 'codebox auth add github': %w", err)
	}
	remote, err := codebox.GitHubOriginForProject(context.Background(), root)
	if err != nil {
		return codebox.GitHubRemote{}, fmt.Errorf("GitHub workflow requires a GitHub.com origin remote: %w", err)
	}
	return remote, nil
}

func approveGitHubForProject(root string) error {
	pc, err := codebox.DiscoverProject(root)
	if err != nil {
		return err
	}
	path := codebox.BindingApprovalsPath(codebox.DefaultStateDir(), pc.InstanceName())
	return codebox.ApproveBinding(codebox.DefaultFS, path, codebox.CredentialGithub)
}

var approveGitHubForProjectFn = approveGitHubForProject
var revokeMCPBindingFn = codebox.RevokeInstanceBinding
var mcpBindingStateDirFn = codebox.DefaultStateDir

// resolvedOrDefault renders the value a reader can reason about: what will
// actually be used, rather than a zero meaning "unset".
func resolvedOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

// catalogueModelWarning validates a model against the P10 catalogue through the
// same cache the other commands use. An unreachable catalogue is a warning, not
// a refusal: the model is still recorded and 'codebox models' re-checks it.
// catalogueModelWarningFn is the catalogue validator the flow uses. It is a
// package variable so a test can exercise the flow without an Albert
// credential and without a network: the real validator needs both, and a test
// that silently depends on them fails on the machine that has them.
var catalogueModelWarningFn = catalogueModelWarningForRoot

// catalogueModelFetchFn is the network seam for the real validator. Tests
// replace it with an httptest Albert endpoint, leaving alias translation and
// chat-type validation under test rather than stubbing the whole validator.
var catalogueModelFetchFn = codebox.FetchCatalogue

// catalogueModelAPIKeyFn seals the credential dependency when testing the
// real validator without consulting a host keychain.
var catalogueModelAPIKeyFn = resolveAlbertKeyForCatalogueAt

func catalogueModelWarningForRoot(projectRoot, model string) (string, error) {
	apiKey, err := catalogueModelAPIKeyFn(projectRoot)
	if err != nil {
		return "le modèle a été enregistré sans vérification : aucun identifiant Albert ne permet de consulter le catalogue", nil
	}
	cache := codebox.CatalogueCache{
		FS:  codebox.DefaultFS,
		Dir: codebox.DefaultStateDir(),
		Fetch: func(ctx context.Context) (codebox.Catalogue, error) {
			return catalogueModelFetchFn(ctx, nil, apiKey, codebox.AlbertCatalogueURL())
		},
	}
	catalogue, _, err := cache.Load(context.Background())
	if err != nil {
		return "le modèle a été enregistré sans vérification : le catalogue Albert est injoignable", nil
	}
	found, ok := catalogue.Find(canonicalAlbertModelID(model))
	if !ok {
		// A listed miss is the user's answer being wrong, not a validation
		// outage: stop so the answer can change.
		return "", fmt.Errorf("the model %q is not in the Albert catalogue; run 'codebox models' for the valid ids", model)
	}
	chat := false
	for _, candidate := range catalogue.TextGenerationModels() {
		if candidate.ID == found.ID {
			chat = true
			break
		}
	}
	if !chat {
		return "", fmt.Errorf("the model %q is not a chat model in the Albert catalogue; run 'codebox models' for the valid ids", model)
	}
	return "", nil
}

// initUsage is the init-specific help, kept next to the flags it documents so
// the two cannot drift.
func initUsage() {
	fmt.Print(`Usage: codebox init [options]

Configure this project: where the agent runs, which model it uses, how much
machine it gets, which remote MCPs it may use, and which credential it may use. The result is
.codebox/project.json (plus lock.json), read by the launch path.

Options:
  --root <dir>            project directory (default: the current directory;
                          a Git worktree root is used when the directory is
                          inside one)
  --runtime <name>        microsandbox (default), tart or agent-vm
  --isolation <level>     full (default) or backend
  --harness <name>        opencode (default) or pi; pi requires full isolation
  --model <id>            OpenCode model; empty keeps the built-in default
  --small-model <id>      Lightweight model for OpenCode titles (for example
                          ministral-3-8b-instruct-2512); empty or 'default'
                          follows the main model
  --cpus <n>              guest CPUs (max detected host capacity)
  --memory-mb <size>      guest memory in MiB or GiB (e.g. 4096, 4G, 2.5G; max detected host RAM)
  --credential-ref <ref>  project credential reference (default: the global one)
  --github                enable pushing changes to the project's GitHub repository
  --skill <id>            select an exact catalogue skill (repeatable; e.g. official/rgaa)
  --clear-skills          remove all managed project skills
  --mcp <id>              select data-gouv or context7 (repeatable)
  --clear-mcps            remove all managed remote MCPs
  --local-only-skills     keep skill selection and pins in host state, not the checkout
  --versioned-skills      store skill selection and pins in .codebox/
  --replace               allow replacing an existing manifest
  --yes, -y               accept the review without asking

With no terminal, the command never waits: it fails and names the inputs it is
missing, so a script can supply them on the next run.
`)
}

// isLaunchAction reports whether an action builds a guest, which is what makes
// the project configuration relevant.
func isLaunchAction(action string) bool {
	switch action {
	case "attach", "start", "restart", "recreate":
		return true
	default:
		return false
	}
}

// offerProjectInit runs the project setup when the launch is about to happen
// with no project configuration at all.
//
// The point is not to nag: it is that a zero-flag launch would otherwise
// choose the runtime, the isolation, the model and the guest sizing silently,
// and the user would have no moment at which those choices were visible. With
// no terminal there is nobody to ask, so it fails and names the command that
// answers everything — never a prompt that cannot be answered.
//
// It returns configured=true when the launch may proceed (a configuration
// exists, or the offer was accepted and written).
func offerProjectInit(projectRoot string, parsed parsedArgs) (configured bool, code int, err error) {
	return offerProjectInitWithReader(projectRoot, parsed, nil, isTTY())
}

// offerProjectInitWithReader is the testable core: the reader and the TTY
// verdict are supplied so the offer can be exercised without a terminal.
func offerProjectInitWithReader(projectRoot string, parsed parsedArgs, given *bufio.Reader, tty bool) (configured bool, code int, err error) {
	if _, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot)); err == nil {
		return true, 0, nil
	} else if !os.IsNotExist(err) {
		// A present-but-unreadable manifest must not be silently replaced —
		// and it must not be silently ignored either. The launch readers
		// tolerate a broken manifest (so a read-only command keeps working),
		// which means nothing downstream will say so: this is the one place
		// holding the error, so it has to say it.
		fmt.Fprintf(os.Stderr, "Attention : %s est illisible (%v) ; le lancement ignore les réglages qu'il contient.\n"+
			"            Corrigez-le ou supprimez-le, ou lancez 'codebox init --replace' pour le réécrire.\n",
			codebox.ProjectManifestPath(projectRoot), err)
		return true, 0, nil
	}
	// An explicit flag or variable means the caller already made the choices
	// this offer would collect, so there is nothing to ask about.
	// The environment counts: `RUNTIME=tart codebox start` in CI is a caller
	// who has answered, and prompting someone who cannot answer would turn a
	// working launch into a failure. An empty exported value is not a choice
	// (it selects nothing), which mirrors how the isolation resolution treats
	// it.
	if parsed.runtime != "" || parsed.isolation != "" ||
		strings.TrimSpace(os.Getenv("RUNTIME")) != "" || strings.TrimSpace(os.Getenv("ISOLATION")) != "" {
		return true, 0, nil
	}
	if !tty {
		return false, 1, fmt.Errorf("ce projet n'a pas de configuration (%s) et aucun terminal n'est disponible pour la demander.\n"+
			"Lancez 'codebox init --root %q --yes' pour accepter les valeurs par défaut, ou 'codebox init' dans un terminal pour choisir",
			codebox.ProjectManifestPath(projectRoot), projectRoot)
	}
	if given == nil && !stdoutIsTTY() {
		return false, 1, fmt.Errorf("la configuration interactive du projet exige que la sortie standard soit un terminal ; elle est redirigée ou pointe vers %s", os.DevNull)
	}

	fmt.Printf("Ce projet n'a pas encore de configuration codebox (%s).\n", codebox.ProjectManifestPath(projectRoot))
	// One reader for both steps: a line the user typed ahead (or a piped
	// answer list) must not be split between two buffers.
	in := given
	if in == nil {
		in = bufio.NewReader(os.Stdin)
	}
	if tty && wizardHuhFn() {
		proceed, err := huhConfirm("Configurer ce projet maintenant ?", "Ce projet n'a pas encore de configuration codebox. L'assistant écrit le manifeste du projet.", true)
		if err != nil {
			return false, 1, fmt.Errorf("projet non configuré : rien n'a été écrit. Lancez 'codebox init' quand vous voudrez le configurer")
		}
		if !proceed {
			return false, 1, fmt.Errorf("projet non configuré : rien n'a été écrit. Lancez 'codebox init' quand vous voudrez le configurer")
		}
		if !printInitIntro(projectRoot, in) {
			return false, 1, fmt.Errorf("projet non configuré : rien n'a été écrit. Lancez 'codebox init' quand vous voudrez le configurer")
		}
		code, err = initRun(initOptions{Root: projectRoot, FromLaunch: true, Set: map[string]bool{"root": true}}, nil, true)
		if err != nil || code != 0 {
			return false, code, err
		}
		return true, 0, nil
	}
	answer, _ := promptLine(in, "Configurer maintenant ? [O/n] : ")
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "n", "non", "no":
		return false, 1, fmt.Errorf("projet non configuré : rien n'a été écrit. Lancez 'codebox init' quand vous voudrez le configurer")
	}
	if !printInitIntro(projectRoot, in) {
		return false, 1, fmt.Errorf("projet non configuré : rien n'a été écrit. Lancez 'codebox init' quand vous voudrez le configurer")
	}
	code, err = initRun(initOptions{Root: projectRoot, FromLaunch: true, Set: map[string]bool{"root": true}}, in, true)
	if err != nil || code != 0 {
		return false, code, err
	}
	return true, 0, nil
}
