// Command codebox is a single Go binary that replaces the original justfile.
// It manages an OpenCode backend in a Microsandbox microVM or a Tart macOS VM.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/apps/codebox/internal/gatewayclient"
)

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "codebox:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	// Hidden flag for CI (msb-runtime-watch workflow): prints the Microsandbox
	// SDK version the binary embeds, without touching config or runtimes.
	if len(args) == 1 && args[0] == "-print-msb-sdk-version" {
		fmt.Println(codebox.MSBSDKVersion())
		return 0, nil
	}

	// The in-VM bootstrap is a hidden subcommand of this same binary. It runs
	// inside the Tart guest and must not touch host config or load .env.
	if len(args) > 0 && args[0] == codebox.GuestBootstrapCommand {
		overlay, err := codebox.DecodeGuestOverlay(argOr(args, codebox.GuestBootstrapModelsArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("configuration de modèle d'amorçage de la machine isolée invalide : %w", err)
		}
		mcps, err := parseGuestMCPIDs(argOr(args, codebox.GuestBootstrapMCPArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("sélection MCP d'amorçage de la machine isolée invalide : %w", err)
		}
		cfg := codebox.GuestConfig{
			Port:     argOr(args, 1, ""),
			Username: argOr(args, 2, ""),
			MTU:      argOr(args, 3, ""),
			// The model selection (P10) is non-secret managed configuration
			// and rides argv; secrets never do.
			OpenCodeOverlay: codebox.ManagedOverlay{Model: argOr(args, 4, ""), SmallModel: overlay.SmallModel, Models: overlay.Models, Providers: overlay.Providers, AlbertEnabled: overlay.AlbertEnabled, MCPConnectors: mcps},
			// The git identity (P11) rides argv the same way: the guest
			// cannot read the host settings file.
			GitName:  argOr(args, 6, ""),
			GitEmail: argOr(args, 7, ""),
		}
		return 0, codebox.RunGuestBootstrap(context.Background(), cfg)
	}

	// Isolation-full in-VM helpers, same hidden-subcommand contract: no host
	// config, no .env. __guest-prepare installs OpenCode; __guest-secrets
	// writes the 0600 env file from stdin.
	if len(args) > 0 && args[0] == codebox.GuestPrepareCommand {
		cfg := codebox.GuestConfig{Username: argOr(args, 1, ""), GitName: argOr(args, 2, ""), GitEmail: argOr(args, 3, "")}
		return exitCodeOf(nil), codebox.RunGuestPrepare(context.Background(), cfg)
	}
	if len(args) > 0 && args[0] == codebox.GuestSecretsCommand {
		overlay, err := codebox.DecodeGuestOverlay(argOr(args, codebox.GuestSecretsModelsArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("configuration de modèle de la machine isolée invalide : %w", err)
		}
		mcps, err := parseGuestMCPIDs(argOr(args, codebox.GuestSecretsMCPArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("sélection MCP de la machine isolée invalide : %w", err)
		}
		// The model selection (P10) is the one managed field the in-guest
		// secrets file must reflect; argv carries no secrets, and the
		// overlay is non-secret configuration.
		guestOverlay := codebox.ManagedOverlay{Model: argOr(args, 2, ""), SmallModel: overlay.SmallModel, Models: overlay.Models, Providers: overlay.Providers, AlbertEnabled: overlay.AlbertEnabled, MCPConnectors: mcps}
		cfg := codebox.GuestConfig{Username: argOr(args, 1, ""), OpenCodeOverlay: guestOverlay}
		return exitCodeOf(nil), codebox.RunGuestSecrets(cfg)
	}
	// The detached statistics flusher is a hidden subcommand: it never
	// blocks the wizard that started it, and it re-checks consent before
	// sending anything.
	if len(args) > 0 && args[0] == codebox.StatsFlushCommand {
		_ = codebox.FlushStats(context.Background(), codebox.DefaultFS, codebox.DefaultStateDir())
		return 0, nil
	}

	// Parse arguments before the legacy dotenv load: `config explain`
	// reports the new resolver's view (flags > CODEBOX_* env > project >
	// user > default), and a legacy .env must not leak into that view as
	// pseudo-env. The config command dispatches with the process
	// environment exactly as the user set it.
	parsed, err := parseArgs(args)
	if err != nil {
		return 2, err
	}
	var acpOutput *os.File
	if parsed.action == "acp" {
		acpOutput = os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = acpOutput }()
	}
	if parsed.action == "mcp" {
		return mcpCommand(parsed.mcpArgs)
	}

	if parsed.action == "config" {
		return configCmd(parsed)
	}
	if parsed.action == "ports" {
		return portsCommand(parsed.portsArgs)
	}
	if parsed.action == "secrets" {
		return secretsCmd(parsed.secretsArgs)
	}
	if parsed.action == "import" {
		return albertCodeImportCmd(parsed.importArgs)
	}
	// auth runs before any config load: credential operations must not
	// depend on a workspace, a .env or a resolved runtime.
	if parsed.action == "auth" {
		return authCmd(parsed.authArgs)
	}
	// setup runs before any config load: a first-run wizard must not depend
	// on a .env or a resolved runtime (P11).
	if parsed.action == "setup" {
		return setupCmd(parsed.setupArgs)
	}
	// stats manages the product-statistics consent and the local queue. It
	// needs no project config and must work before any credential resolves.
	if parsed.action == "stats" {
		return statsCmd(parsed.statsArgs)
	}

	cfg := codebox.LoadConfigEnv()
	cfg.GuestCredentialsAcknowledged = parsed.ackGuestCreds
	if parsed.version {
		printBuildInfo()
		return 0, nil
	}

	// Project identity (P05/P06): the backends are bound to the current
	// project's instance so lifecycle operations are project-scoped. A
	// missing registry entry is not an error here — discovery derives the
	// deterministic instance name; registration happens at first start.
	pc, projErr := codebox.DiscoverProject(".")
	instance := ""
	if projErr == nil {
		instance = pc.InstanceName()
	}
	// The project credentialRef is read from the discovered root, not from
	// cwd: invoked from a subdirectory of a worktree, a cwd-relative read
	// would miss the manifest and silently inject the user-level credential.
	projectRoot := "."
	if projErr == nil {
		projectRoot = pc.Root
	}
	manualRecovery := parsed.action == "update" && updateCommandSkipsStartupRecovery(parsed.updateArgs)
	if projErr == nil && (parsed.action == "init" || parsed.action == "update" || isLaunchAction(parsed.action)) && !manualRecovery {
		if err := codebox.RecoverProjectUpdate(codebox.DefaultFS, projectRoot); err != nil {
			return 1, fmt.Errorf("impossible de récupérer la mise à jour des dépendances du projet : %w\nSi la récupération a refusé de modifier les fichiers gérés, inspectez le journal et lancez 'codebox update --recover --rollback' pour restaurer la paire précédente à partir des sauvegardes", err)
		}
	}
	if parsed.action == "update" {
		if projErr != nil {
			return 2, fmt.Errorf("update exige un dossier de projet configuré : %w", projErr)
		}
		return updateCmd(parsed.updateArgs, projectRoot)
	}
	// The sealed transfer source (P22/P12).
	cfg = withProjectRootAsWorkspaceSource(cfg, pc, projErr)
	// A project with no configuration yet is offered one before the guest is
	// built (P12): the launch would otherwise pick every default silently, and
	// the choices it made would be invisible until someone read the review.
	// Read-only actions are never gated on it.
	if projErr == nil && isLaunchAction(parsed.action) {
		// configured is belt-and-braces: every false path also returns an
		// error, but a future caller must not read "no error" as "go ahead".
		configured, code, err := offerProjectInit(projectRoot, parsed)
		if err != nil || !configured {
			return code, err
		}
	}
	if projErr == nil && isLaunchAction(parsed.action) {
		packages, skillIDs, managed, localOnly, err := codebox.LoadProjectSkillState(codebox.DefaultFS, projectRoot, codebox.DefaultStateDir(), instance)
		if err != nil {
			return 1, fmt.Errorf("impossible de résoudre les skills épinglées du projet avant de modifier le runtime : %w", err)
		}
		cfg.ProjectSkills = packages
		cfg.ProjectSkillIDs = skillIDs
		cfg.ProjectSkillsManaged = managed
		cfg.ProjectSkillsLocalOnly = localOnly
	}
	// The GitHub origin is non-secret metadata. Resolve it only when this
	// project has a host-local approval for the optional GitHub binding; a
	// repository remote alone must never activate guest credentials.
	if projErr == nil && isLaunchAction(parsed.action) {
		remote, err := projectGitHubRemote(projectRoot, codebox.DefaultStateDir(), instance)
		if err != nil {
			return 1, fmt.Errorf("le flux GitHub approuvé ne peut pas utiliser l'origine de ce projet ; aucune modification du runtime n'a été faite : %w", err)
		} else {
			cfg.GitHubRemote = remote
		}
	}
	// The project manifest's runtime and isolation choices (P12b). They are
	// applied here, after discovery, because they are resolved from the
	// discovered root; the flag and the exported variable still win, per the
	// configuration contract (#29). Resolving them before any backend is
	// constructed is what makes them effective rather than merely displayed.
	manifestRuntime, manifestIsolation := projectRuntimeIsolation(projectRoot)
	// The agent harness (#176). Like the runtime and isolation, it is resolved
	// after discovery and before any backend is constructed: the backends read
	// cfg.Harness when they are built.
	harness, harnessErr := resolveHarnessSelection(projectRoot)
	if harnessErr != nil {
		if isLaunchAction(parsed.action) {
			return 2, harnessErr
		}
		fmt.Fprintf(os.Stderr, "Attention : %v ; utilisation de l'agent par défaut\n", harnessErr)
	}
	cfg.Harness = harness
	var launch *codebox.LaunchUX
	if isLaunchAction(parsed.action) && runtimeUsesMicrosandbox(parsed.runtime, manifestRuntime) {
		launch = codebox.NewLaunchUX(projectRoot, harness.DisplayName())
		launch.TimingStore = codebox.DefaultLaunchTimingStore()
	}
	// Resolve the isolation level before any backend is constructed: the
	// backends read cfg.Isolation at construction time, so applying the flag
	// afterwards would leave them in the wrong mode.
	cfg, isoErr := applyIsolation(cfg, parsed.isolation, manifestIsolation)
	// A harness without a server mode cannot run in backend isolation. The
	// pair is checked as soon as both are known, so the refusal happens before
	// a guest is created rather than when the launch finds nothing to attach to.
	if isoErr == nil {
		if err := codebox.ValidateHarnessIsolation(cfg.EffectiveHarness(), cfg.Isolation); err != nil {
			if isLaunchAction(parsed.action) {
				return 2, err
			}
			fmt.Fprintf(os.Stderr, "Attention : %v\n", err)
		}
	}
	// Guest sizing from the project manifest (P12b). A malformed value is
	// fatal only where the guest would actually be created; read-only
	// commands keep working, which is when the user needs them.
	cfg, resErr := applyProjectSandboxResources(cfg, projectRoot)
	if resErr != nil {
		if isLaunchAction(parsed.action) {
			return 1, resErr
		}
		fmt.Fprintf(os.Stderr, "Attention : %v ; utilisation du dimensionnement de machine isolée par défaut\n", resErr)
	}
	if isLaunchAction(parsed.action) && runtimeUsesMicrosandbox(parsed.runtime, manifestRuntime) {
		cfg, resErr = enforceHostResourceLimits(cfg, projectRoot, launch)
		if resErr != nil {
			return 1, resErr
		}
	}
	cfg.CredentialRef = resolveCredentialRef(projectRoot)

	// Managed OpenCode configuration (P10): the model selection from the
	// typed resolver (CODEBOX_MODEL > manifest > user settings), the
	// composed OPENCODE_CONFIG_CONTENT carrying it, field conflicts
	// surfaced against the project config, and execution inputs gated
	// behind the host-local trust record.
	mcpSelection, mcpErr := resolveMCPSelection(projectRoot)
	if mcpErr != nil {
		if isLaunchAction(parsed.action) {
			return 1, fmt.Errorf("impossible de résoudre la sélection MCP gérée du projet : %w", mcpErr)
		}
		fmt.Fprintf(os.Stderr, "Attention : la sélection MCP gérée du projet est illisible (%v) ; poursuite sans elle\n", mcpErr)
	}
	modelSelection := resolveModelSelection(projectRoot)
	smallModelSelection := resolveSmallModelSelection(projectRoot)
	providerSettings, providerErr := resolveProviderSettingsAt(projectRoot)
	if providerErr != nil {
		if isLaunchAction(parsed.action) {
			return 1, fmt.Errorf("impossible de résoudre les plateformes d'inférence : %w", providerErr)
		}
		fmt.Fprintf(os.Stderr, "Attention : les plateformes d'inférence ne peuvent pas être résolues (%v) ; poursuite sans elles\n", providerErr)
	} else {
		cfg.Providers, providerErr = codebox.ResolveProviders(providerSettings)
		if providerErr != nil {
			if isLaunchAction(parsed.action) {
				return 1, fmt.Errorf("impossible de résoudre les plateformes d'inférence : %w", providerErr)
			}
			fmt.Fprintf(os.Stderr, "Attention : les plateformes d'inférence ne peuvent pas être résolues (%v) ; poursuite sans elles\n", providerErr)
		}
	}
	if isLaunchAction(parsed.action) {
		if err := validateSmallModelProviderConnection(cfg.Providers, smallModelSelection); err != nil {
			return 1, err
		}
	}
	var albertModels []codebox.AlbertModelConfig
	if providersContain(cfg.Providers, codebox.ProviderAlbert) {
		albertModels = resolveEnabledAlbertModels(modelSelection, smallModelSelection)
	}
	overlay := codebox.ManagedOverlay{
		Model:         modelSelection,
		SmallModel:    smallModelSelection,
		Models:        albertModels,
		Providers:     resolveProviderOverlays(cfg.Providers, modelSelection, smallModelSelection),
		AlbertEnabled: providersContain(cfg.Providers, codebox.ProviderAlbert),
		MCPConnectors: mcpSelection,
	}
	conflictMsg, err := opencodeConfigReview(projectRoot, overlay)
	startPath := isLaunchAction(parsed.action)
	if err != nil {
		// A malformed project config is fatal only on the paths that would
		// load it: read-only commands (stop, check, logs, shell) must keep
		// working on a project whose config is broken — that is exactly when
		// the user needs them.
		if !startPath {
			fmt.Fprintf(os.Stderr, "Attention : la configuration OpenCode du projet est illisible (%v) ; poursuite sans l'analyse des conflits\n", err)
		} else {
			return 1, err
		}
	}
	if conflictMsg != "" {
		if launch != nil && launch.Terminal {
			launch.AddWarning("des réglages OpenCode du projet sont remplacés par les réglages gérés ; détail : codebox config explain")
		} else {
			fmt.Fprintf(os.Stderr, "Attention : des champs OpenCode gérés diffèrent de la configuration du projet :\n%s\n", conflictMsg)
		}
	}
	// Trust gate (P10): project plugins and MCP commands execute code at
	// OpenCode config load, and Pi runs the project's own extensions and
	// settings at launch, so starting a runtime for this project requires
	// every execution input to be approved at its current content hash. A
	// changed file is a new decision, not an inherited one. Only start paths
	// are gated: stop, check, logs and shell operate on an existing guest.
	if startPath {
		if err := codebox.ValidateOpenCodeBackend(cfg); err != nil {
			return 2, err
		}
		if err := requireTrustedExecutionInputs(projectRoot); err != nil {
			return 1, err
		}
	}

	// bindings manages the host-local per-project credential binding
	// approvals (P09). It needs the project instance, so it runs after
	// discovery and before any runtime is constructed.
	if parsed.action == "bindings" {
		if projErr != nil {
			return 2, fmt.Errorf("bindings exige un dossier de projet : %v", projErr)
		}
		return bindingsCmd(parsed.bindingsArgs, instance)
	}

	// models shows the validated Albert catalogue (P10). It runs before
	// any runtime is constructed and needs no project.
	if parsed.action == "models" {
		return modelsCmd(parsed.modelsArgs)
	}

	// trust manages the host-local approval of execution-relevant project
	// inputs (P10). It runs before any runtime is constructed.
	if parsed.action == "trust" {
		if projErr != nil {
			return 2, fmt.Errorf("trust exige un dossier de projet : %v", projErr)
		}
		return trustCmd(parsed.trustArgs, pc.Root)
	}

	// init writes the minimal project configuration (P12b). It runs before
	// any runtime is constructed and needs only a directory, so a machine that
	// cannot run a guest yet can still be configured.
	if parsed.action == "init" {
		return initCmd(parsed.initArgs)
	}

	// workspace manages the sealed guest workspace (P22): the transfer
	// manifest, the per-file re-inclusion decisions, the refresh, and the
	// reviewed change export. It needs the instance name, so it runs after
	// discovery like bindings; the transfer source is the configured
	// workspace dir, which discovery has already set.
	if parsed.action == "workspace" {
		if projErr != nil {
			return 2, fmt.Errorf("workspace exige un dossier de projet : %v", projErr)
		}
		return workspaceCmd(parsed.workspaceArgs, cfg, instance)
	}

	// The dispatcher and its backends are built from the resolved config,
	// bound to the project instance when discovery succeeded. Discovery
	// failure falls back to the legacy singleton rather than blocking
	// lifecycle commands on a non-Git directory.
	var d *codebox.Dispatcher
	if projErr == nil {
		d = codebox.NewDispatcherForInstance(cfg, instance)
	} else {
		d = codebox.NewDispatcher(cfg)
	}
	d.SetOpenCodeOverlay(overlay)
	if projErr == nil && runtimeUsesMicrosandbox(parsed.runtime, manifestRuntime) {
		// Set a create-time gate, not an immediate launch failure: an
		// existing instance must reconcile first so a revoked binding can be
		// unregistered even if the manifest still declares it.
		root := projectRoot
		d.SetProjectSecretApprovalGate(func() error { return ensureDeclaredBindingApprovals(root) })
		if err := configureGatewayProjectSecrets(d, projectRoot); err != nil {
			if isLaunchAction(parsed.action) {
				return 1, err
			}
			fmt.Fprintf(os.Stderr, "Attention : les secrets de développement partagés ne sont pas configurés (%v)\n", err)
		}
	}
	if launch != nil {
		d.SetLaunchUX(launch)
		launch.RecordModel(modelSelection)
		for _, model := range overlay.Models {
			if modelSelection == "albert/"+model.ID {
				launch.RecordModel(model.Name)
			}
		}
	}

	// These actions resolve their own target (or need none), so they must not be
	// gated on a configured runtime. check consumes the isolation level, so it
	// is the one action here that must surface a deferred isolation error.
	switch parsed.action {
	case "help":
		usage()
		return 0, nil
	case "version":
		printBuildInfo()
		return 0, nil
	case "stop":
		if parsed.stopAll {
			return 0, d.StopAll(context.Background())
		}
		return 0, d.Stop(context.Background())
	case "check":
		if isoErr != nil {
			return 2, isoErr
		}
		return 0, d.Check(context.Background(), nil)
	}

	// Precedence: an explicit flag, then an exported RUNTIME, then the project
	// manifest's choice, then the built-in default (P12b).
	runtimePreference := os.Getenv("RUNTIME")
	if runtimePreference == "" {
		runtimePreference = string(manifestRuntime)
	}
	rt, err := codebox.ResolveRuntime(parsed.runtime, runtimePreference)
	if err != nil {
		return 2, err
	}
	if len(cfg.ProjectSkills) > 0 && rt != codebox.RuntimeMicrosandbox {
		return 2, fmt.Errorf("les skills du projet exigent actuellement le runtime microsandbox scellé ; aucune modification du runtime n'a été faite")
	}
	// A typo in ISOLATION must not block commands that never read it, so it is
	// validated only here, where the level is actually consumed.
	if isoErr != nil {
		return 2, isoErr
	}

	switch parsed.action {
	case "acp":
		return acpCmd(d, cfg, rt, os.Stdin, acpOutput, os.Stderr)
	case "attach":
		return attachCmd(d, cfg, rt, instance, projectRoot)
	case "start", "logs", "shell", "restart", "recreate", "clean", "doctor":
		return lifecycleCmd(d, rt, parsed.action, parsed.follow)
	default:
		return 2, fmt.Errorf("Argument inconnu : %s", parsed.action)
	}
}

func parseGuestAlbertModels(raw string) ([]codebox.AlbertModelConfig, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return nil, nil
	}
	var models []codebox.AlbertModelConfig
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, err
	}
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Name) == "" {
			return nil, fmt.Errorf("les identifiants et noms de modèle doivent être non vides")
		}
	}
	return models, nil
}

func runtimeUsesMicrosandbox(runtimeFlag string, manifestRuntime codebox.Runtime) bool {
	preference := os.Getenv("RUNTIME")
	if preference == "" {
		preference = string(manifestRuntime)
	}
	runtime, err := codebox.ResolveRuntime(runtimeFlag, preference)
	return err == nil && runtime == codebox.RuntimeMicrosandbox
}

func resolveMCPSelection(projectRoot string) ([]string, error) {
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids, err := codebox.ValidateMCPConnectorIDs(manifest.MCPConnectors)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func parseGuestMCPIDs(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return codebox.ValidateMCPConnectorIDs(strings.Split(value, ","))
}

// withProjectRootAsWorkspaceSource points the sealed transfer source at the
// discovered project root when the user has not configured one.
//
// Under the sealed model (P22) WORKSPACE_DIR is the source whose filtered
// content is transferred into the guest. The historical default (./workspace,
// an empty subdirectory) would provision a guest with nothing in it, and the
// user would see a working agent and an empty project rather than an error.
// An explicit WORKSPACE_DIR/PROJECT_DIR is always honoured as given, and a
// discovery failure leaves the configured value alone.
func withProjectRootAsWorkspaceSource(cfg codebox.Config, pc codebox.ProjectContext, projErr error) codebox.Config {
	if cfg.WorkspaceDirSet || projErr != nil {
		return cfg
	}
	cfg.WorkspaceDir = pc.Root
	return cfg
}

// configureGatewayProjectSecrets reads the project's non-secret `secrets`
// declaration and injects the remote value source into the Microsandbox
// backend. A project without that declaration keeps the local OS-store
// workflow; a declared project always requires a session and resolver so a
// checkout cannot silently launch with stale local values.
func configureGatewayProjectSecrets(d *codebox.Dispatcher, projectRoot string) error {
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if manifest.Secrets == nil {
		return nil
	}
	if err := codebox.ValidateProjectSecretsManifest(manifest.Secrets); err != nil {
		return err
	}
	store := codebox.DefaultCredentialStore()
	if store == nil {
		return fmt.Errorf("aucun trousseau natif n'est disponible pour la session de passerelle ; lancez codebox secrets login")
	}
	source, _, err := gatewayclient.BuildRuntimeValueSource(context.Background(), manifest.Secrets.ProjectID, store, nil)
	if err != nil {
		return err
	}
	d.SetGatewayProjectSecrets(source, manifest.Secrets)
	return nil
}

// applyIsolation resolves the effective isolation level: an explicit flag
// wins, then an exported ISOLATION, then the project manifest's choice, then
// the built-in default. A project value only decides when nothing higher does,
// so a one-off `--isolation backend` still overrides what the project
// recorded. It exists as a separate step because the backends read
// cfg.Isolation when they are constructed: resolving the level after building
// the dispatcher would leave them in the wrong mode.
func applyIsolation(cfg codebox.Config, flag string, project codebox.Isolation) (codebox.Config, error) {
	preference, preferenceErr := cfg.Isolation, cfg.IsolationErr
	if project != "" {
		// An exported but EMPTY value is treated as "not set" for both
		// ISOLATION and RUNTIME: an empty string selects nothing, so it must
		// not silently suppress the project's choice.
		if strings.TrimSpace(os.Getenv("ISOLATION")) == "" {
			// The project choice replaces the built-in default, not an
			// exported value: env beats project.
			preference, preferenceErr = project, nil
		}
	}
	iso, err := codebox.ResolveIsolationLevel(flag, preference, preferenceErr)
	if err != nil {
		return cfg, err
	}
	cfg.Isolation = iso
	// The level is resolved now, so a deferred env error would be stale.
	cfg.IsolationErr = nil
	return cfg, nil
}

// projectRuntimeIsolation reads the project manifest's runtime and isolation
// choices, returning empty values when there is no manifest. A malformed value
// is left for the consumer to reject: ResolveRuntime and ResolveIsolation
// already produce the messages for an unknown name.
func projectRuntimeIsolation(projectRoot string) (codebox.Runtime, codebox.Isolation) {
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		return "", ""
	}
	return codebox.Runtime(pm.Runtime), codebox.Isolation(pm.Isolation)
}

// parsedArgs is the result of a single pass over argv. Commands, runtime flags,
// the isolation flag, and the version flag may appear in any order.
type parsedArgs struct {
	// action is "attach" (the default) or one of the named commands.
	action string
	// runtime is the raw --microsandbox/--tart flag, or "".
	runtime string
	// isolation is the raw --isolation value (backend or full), or "".
	isolation string
	// stopAll records the explicit --all flag: `stop --all` stops every
	// managed instance, not just the current project's (P06).
	stopAll bool
	// follow records the explicit logs --follow flag. Finite output is the
	// default so pipes and one-shot diagnostics always terminate.
	follow bool
	// ackGuestCreds records --acknowledge-guest-credentials (P09): required
	// to start a Tart or agent-vm runtime, which transports the credential
	// into the guest in plaintext.
	ackGuestCreds bool
	version       bool
	// configArgs holds the words after the config command.
	configArgs []string
	portsArgs  []string
	// authArgs holds the words after the auth command.
	authArgs []string
	// bindingsArgs holds the words after the bindings command.
	bindingsArgs []string
	// trustArgs holds the words after the trust command.
	trustArgs []string
	// modelsArgs holds the words after the models command.
	modelsArgs []string
	// workspaceArgs holds the words after the workspace command.
	workspaceArgs []string
	secretsArgs   []string
	// importArgs holds the words after the import command.
	importArgs []string
	// mcpArgs holds the words belonging to the mcp command; global flags are
	// still parsed around it by the shared argument pass.
	mcpArgs []string
	// initArgs holds the words after the init command.
	initArgs []string
	// setupArgs holds the words after the setup command.
	setupArgs []string
	// statsArgs holds the words after the stats command.
	statsArgs []string
	// updateArgs holds the words after the update command.
	updateArgs []string
}

// actionNames lists the commands that can be typed. It deliberately excludes
// "code": a bare invocation launches the selected runtime (Microsandbox/full
// by default), so there is no such command to type. "version" is accepted as a
// convenience beyond the TypeScript CLI, which exposes only -V/--version.
var actionNames = map[string]bool{
	"start": true, "stop": true, "check": true, "logs": true, "shell": true,
	"restart": true, "recreate": true, "clean": true, "doctor": true,
	"help": true, "version": true, "config": true, "acp": true,
	"ports": true,
	"auth":  true, "bindings": true,
	"workspace": true,
	"init":      true,
	"update":    true,
}

func runtimeFlag(a string) bool {
	return a == "--microsandbox" || a == "--tart" || a == "--agent-vm"
}

// parseArgs mirrors the TypeScript CLI's parser: a single pass that accepts the
// action and runtime flags in any order, and rejects anything unrecognised
// rather than silently ignoring it. The isolation flag takes a value, either
// as the next token (--isolation full) or inline (--isolation=full), so the
// pass is index-based to consume the value.
func parseArgs(args []string) (parsedArgs, error) {
	var p parsedArgs
	actionSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case runtimeFlag(a):
			if p.runtime != "" && p.runtime != a {
				return p, fmt.Errorf("Sélectionnez exactement un runtime.")
			}
			p.runtime = a
		case a == "--isolation" || strings.HasPrefix(a, "--isolation="):
			value := ""
			if a == "--isolation" {
				if i+1 >= len(args) {
					return p, fmt.Errorf("--isolation exige une valeur : backend ou full")
				}
				i++
				value = args[i]
			} else {
				value = strings.TrimPrefix(a, "--isolation=")
			}
			if value == "" {
				return p, fmt.Errorf("--isolation exige une valeur : backend ou full")
			}
			if p.isolation != "" && p.isolation != value {
				return p, fmt.Errorf("Sélectionnez exactement un niveau d'isolation.")
			}
			p.isolation = value
		case a == "-h" || a == "--help":
			p.action = "help"
			actionSet = true
		case a == "-V" || a == "--version" || a == "-v":
			p.version = true
		case a == "--all":
			if p.stopAll {
				return p, fmt.Errorf("--all ne peut être fourni qu'une fois")
			}
			p.stopAll = true
		case a == "--follow":
			if p.follow {
				return p, fmt.Errorf("--follow ne peut être fourni qu'une fois")
			}
			p.follow = true
		case a == "--acknowledge-guest-credentials":
			p.ackGuestCreds = true
		case a == "config" && !actionSet:
			p.action = "config"
			actionSet = true
			// Everything after the config command belongs to it.
			p.configArgs = args[i+1:]
			return p, nil
		case a == "ports" && !actionSet:
			p.action = "ports"
			actionSet = true
			p.portsArgs = args[i+1:]
			return p, nil
		case a == "secrets" && !actionSet:
			p.action = "secrets"
			actionSet = true
			p.secretsArgs = args[i+1:]
			return p, nil
		case a == "auth" && !actionSet:
			p.action = "auth"
			actionSet = true
			// Everything after the auth command belongs to it.
			p.authArgs = args[i+1:]
			return p, nil
		case a == "bindings" && !actionSet:
			p.action = "bindings"
			actionSet = true
			// Everything after the bindings command belongs to it.
			p.bindingsArgs = args[i+1:]
			return p, nil
		case a == "workspace" && !actionSet:
			p.action = "workspace"
			actionSet = true
			// Everything after the workspace command belongs to it.
			p.workspaceArgs = args[i+1:]
			return p, nil
		case a == "import" && !actionSet:
			p.action = "import"
			actionSet = true
			p.importArgs = args[i+1:]
			return p, nil
		case a == "init" && !actionSet:
			p.action = "init"
			actionSet = true
			// Everything after the init command belongs to it.
			p.initArgs = args[i+1:]
			return p, nil
		case a == "trust" && !actionSet:
			p.action = "trust"
			actionSet = true
			// Everything after the trust command belongs to it.
			p.trustArgs = args[i+1:]
			return p, nil
		case a == "models" && !actionSet:
			p.action = "models"
			actionSet = true
			// Everything after the models command belongs to it.
			p.modelsArgs = args[i+1:]
			return p, nil
		case a == "setup" && !actionSet:
			p.action = "setup"
			actionSet = true
			// Everything after the setup command belongs to it.
			p.setupArgs = args[i+1:]
			return p, nil
		case a == "stats" && !actionSet:
			p.action = "stats"
			actionSet = true
			p.statsArgs = args[i+1:]
			return p, nil
		case a == "update" && !actionSet:
			p.action = "update"
			actionSet = true
			// Everything after the update command belongs to it.
			p.updateArgs = args[i+1:]
			return p, nil
		case a == "mcp" && !actionSet:
			p.action = "mcp"
			actionSet = true
		case p.action == "mcp":
			p.mcpArgs = append(p.mcpArgs, a)
		case actionNames[a] && !actionSet:
			p.action = a
			actionSet = true
		case a == "code":
			return p, fmt.Errorf("La commande 'code' a été supprimée : lancez 'codebox' sans commande pour démarrer le runtime sélectionné (Microsandbox/full par défaut).")
		default:
			return p, fmt.Errorf("Argument inconnu : %s", a)
		}
	}
	if p.action == "" {
		p.action = "attach"
	}
	if p.follow && p.action != "logs" {
		return p, fmt.Errorf("--follow est réservé à la commande logs")
	}
	return p, nil
}

// argOr returns args[i], or def when it is absent or empty.
func argOr(args []string, i int, def string) string {
	if i < len(args) && args[i] != "" {
		return args[i]
	}
	return def
}

// resolveCredentialRef applies the credential-reference precedence (P09):
// CODEBOX_CREDENTIAL_REF > project manifest credentialRef > user settings
// credentialRef. The manifest is read from the discovered project root, not
// from cwd: invoked from a subdirectory of a worktree, a cwd-relative read
// would miss it and silently inject the user-level credential into that
// project's sandbox. Reads are best-effort: a missing file means "no
// reference", and a malformed one is reported by `config explain`, not by
// every command that might start a runtime.
func resolveCredentialRef(projectRoot string) string {
	if v := strings.TrimSpace(os.Getenv("CODEBOX_CREDENTIAL_REF")); v != "" {
		return v
	}
	if pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot)); err == nil && pm.CredentialRef != "" {
		return pm.CredentialRef
	}
	return resolveUserCredentialRef()
}

func resolveUserCredentialRef() string {
	if path, err := codebox.UserSettingsPath(); err == nil {
		if us, err := codebox.ReadUserSettings(codebox.DefaultFS, path); err == nil {
			return us.CredentialRef
		}
	}
	return ""
}

// resolveModelSelection applies the model precedence (P10):
// CODEBOX_MODEL > project manifest > user settings. Empty means the
// embedded default, which the overlay applies itself.
func resolveModelSelection(projectRoot string) string {
	if v := strings.TrimSpace(os.Getenv("CODEBOX_MODEL")); v != "" {
		return canonicalAlbertModelRef(v)
	}
	if pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot)); err == nil && pm.Model != "" {
		return canonicalAlbertModelRef(pm.Model)
	}
	if path, err := codebox.UserSettingsPath(); err == nil {
		if us, err := codebox.ReadUserSettings(codebox.DefaultFS, path); err == nil && us.DefaultModel != "" {
			return canonicalAlbertModelRef(us.DefaultModel)
		}
	}
	return ""
}

// resolveHarnessSelection applies the harness precedence (#176):
// CODEBOX_HARNESS > project manifest > default (OpenCode). An unrecognised
// value is an error rather than a silent fallback: running a different agent
// than the project asked for is exactly what this setting exists to prevent.
func resolveHarnessSelection(projectRoot string) (codebox.Harness, error) {
	if v := strings.TrimSpace(os.Getenv("CODEBOX_HARNESS")); v != "" {
		return codebox.ResolveHarness(v)
	}
	if pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot)); err == nil && pm.Harness != "" {
		return codebox.ResolveHarness(pm.Harness)
	}
	return codebox.ResolveHarness("")
}

// resolveSmallModelSelection reads the explicit project-only lightweight
// model. Empty deliberately leaves the overlay free to follow the effective
// main model (including CODEBOX_MODEL) instead of pinning it here.
func resolveSmallModelSelection(projectRoot string) string {
	if pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot)); err == nil && pm.SmallModel != "" {
		return canonicalAlbertModelRef(pm.SmallModel)
	}
	return ""
}

// validateSmallModelProviderConnection prevents a manually edited manifest
// from selecting the Albert-only model after Albert was disconnected.
func validateSmallModelProviderConnection(providers []codebox.InferenceProvider, smallModel string) error {
	if smallModel == codebox.MinistralSmallModelRef && !providersContain(providers, codebox.ProviderAlbert) {
		return fmt.Errorf("le petit modèle %q exige que l'API Albert soit connectée ; lancez 'codebox setup' pour la connecter, ou effacez smallModel dans le manifeste du projet", smallModel)
	}
	return nil
}

// resolveProviderSettings reads the connected platforms from the user
// settings. An empty list means the historical Albert-only configuration,
// which the engine applies as its default.
func resolveProviderSettings() ([]codebox.ProviderSettings, error) {
	path, err := codebox.UserSettingsPath()
	if err != nil {
		return nil, err
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, path)
	if err != nil {
		return nil, err
	}
	return us.Providers, nil
}

// resolveProviderSettingsAt applies the project's provider contract when one is
// recorded, falling back to machine settings for existing projects. The legacy
// project credentialRef remains an Albert-only default; an explicit per-provider
// reference takes precedence over it.
func resolveProviderSettingsAt(projectRoot string) ([]codebox.ProviderSettings, error) {
	settings, err := resolveProviderSettings()
	if err != nil {
		return nil, err
	}
	manifest, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, err
	}
	if len(manifest.Providers) > 0 {
		if err := codebox.ValidateProjectProviders(manifest.Providers, settings); err != nil {
			return nil, err
		}
		settings = append([]codebox.ProviderSettings(nil), manifest.Providers...)
	}
	if ref := strings.TrimSpace(manifest.CredentialRef); ref != "" {
		if len(settings) == 0 {
			settings = codebox.DefaultProviderSettings()
		}
		for i := range settings {
			if settings[i].Kind == codebox.ProviderAlbert && strings.TrimSpace(settings[i].CredentialRef) == "" {
				settings[i].CredentialRef = ref
			}
		}
	}
	return settings, nil
}

// resolveProviderOverlays turns the configured platforms into the non-Albert
// provider blocks of the managed OpenCode config. Albert keeps its curated
// model definitions in ManagedOverlay.Models.
func resolveProviderOverlays(providers []codebox.InferenceProvider, activeModels ...string) []codebox.ProviderOverlay {
	path, err := codebox.UserSettingsPath()
	if err != nil {
		return nil
	}
	us, readErr := codebox.ReadUserSettings(codebox.DefaultFS, path)
	if readErr != nil {
		return nil
	}
	var out []codebox.ProviderOverlay
	for _, provider := range providers {
		if provider.Kind == codebox.ProviderAlbert {
			continue
		}
		ids := codebox.ProviderModelIDs(us.Models, provider.Kind)
		for _, activeModel := range activeModels {
			activeKind, activeID := codebox.ProviderModelID(activeModel)
			if activeModel != "" && activeKind == provider.Kind && activeID != "" && !containsString(ids, activeID) {
				ids = append(ids, activeID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		models := make([]codebox.ProviderModelConfig, 0, len(ids))
		for _, id := range ids {
			ref := string(provider.Kind) + "/" + id
			config := codebox.ProviderModelConfig{ID: id, Name: id}
			if apiID := us.ProviderModelIDs[ref]; apiID != "" {
				config.ModelID = apiID
			}
			models = append(models, config)
		}
		out = append(out, codebox.ProviderOverlay{
			Kind:      string(provider.Kind),
			Name:      provider.Name,
			BaseURL:   provider.BaseURL,
			APIKeyEnv: provider.APIKeyEnv,
			Models:    models,
		})
	}
	return out
}

func canonicalAlbertModelRef(model string) string {
	for _, choice := range codebox.CuratedAlbertModels() {
		if model == "albert/"+choice.ID || model == choice.ID || model == choice.OpenCodeID || model == choice.Ref() {
			return choice.Ref()
		}
	}
	return model
}

func canonicalAlbertModelID(id string) string {
	for _, choice := range codebox.CuratedAlbertModels() {
		if id == choice.ID || id == choice.OpenCodeID || id == choice.Ref() || id == "albert/"+choice.ID {
			return choice.ID
		}
	}
	return id
}

func resolveEnabledAlbertModels(activeModels ...string) []codebox.AlbertModelConfig {
	for i, activeModel := range activeModels {
		activeModels[i] = canonicalAlbertModelRef(activeModel)
	}
	path, err := codebox.UserSettingsPath()
	var ids []string
	if err == nil {
		if settings, readErr := codebox.ReadUserSettings(codebox.DefaultFS, path); readErr == nil {
			for _, ref := range codebox.ProviderModelIDs(settings.Models, codebox.ProviderAlbert) {
				ids = append(ids, canonicalAlbertModelID(ref))
			}
		}
	}
	if len(ids) == 0 {
		embeddedOnly := true
		for _, activeModel := range activeModels {
			if activeModel != "" && activeModel != "albert/deepseek-v4-flash" {
				embeddedOnly = false
			}
		}
		if embeddedOnly {
			return nil
		}
		for _, choice := range codebox.CuratedAlbertModels() {
			if choice.Default {
				ids = append(ids, choice.ID)
			}
		}
	}
	var configured []codebox.AlbertModelConfig
	choices := codebox.CuratedAlbertModels()
	for _, id := range ids {
		found := false
		for _, choice := range choices {
			if id == choice.ID {
				configured = append(configured, choice.AlbertModelConfig())
				found = true
				break
			}
		}
		if !found {
			configured = append(configured, codebox.AlbertModelConfig{ID: id, Name: id})
		}
	}
	for _, activeModel := range activeModels {
		if !strings.HasPrefix(activeModel, "albert/") {
			continue
		}
		activeID := strings.TrimPrefix(activeModel, "albert/")
		activeConfigured := false
		for _, choice := range choices {
			if choice.Ref() == activeModel || "albert/"+choice.ID == activeModel || activeID == choice.ID || activeID == choice.OpenCodeID {
				activeConfigured = containsString(ids, choice.ID)
				if !activeConfigured {
					configured = append(configured, choice.AlbertModelConfig())
				}
				break
			}
		}
		for _, model := range configured {
			if model.ID == activeID {
				activeConfigured = true
			}
		}
		if !activeConfigured {
			configured = append(configured, codebox.AlbertModelConfig{ID: activeID, Name: activeID})
		}
	}
	return configured
}

// opencodeConfigReview surfaces managed-field conflicts between the overlay
// and the project's OpenCode configuration (D-001: OpenCode ignores unknown
// keys and never exposes a merge diff, so codebox must detect them). The
// overlay still wins (final merge), so the message says so.
func opencodeConfigReview(projectRoot string, overlay codebox.ManagedOverlay) (string, error) {
	cfgPath := codebox.FindProjectOpenCodeConfig(projectRoot)
	if cfgPath == "" {
		return "", nil
	}
	project, err := codebox.ParseProjectOpenCodeConfig(projectRoot, cfgPath)
	if err != nil {
		return "", err
	}
	conflicts := codebox.DetectOverlayConflicts(project, overlay)
	return codebox.FormatConflicts(conflicts), nil
}

// requireTrustedExecutionInputs blocks the launch when the project carries
// execution-relevant inputs that are unapproved or changed since approval:
// OpenCode project plugins and MCP commands, and Pi project extensions and
// settings. Read-only commands (stop, check, status, logs, shell on an
// already-running guest) are not gated here; only paths that start a
// runtime.
func requireTrustedExecutionInputs(projectRoot string) error {
	unapproved, err := codebox.DiscoverUnapprovedInputs(codebox.DefaultFS, codebox.DefaultStateDir(), projectRoot)
	if err != nil {
		return err
	}
	if len(unapproved) == 0 {
		return nil
	}
	if launchTrustTTYFn() {
		return approveLaunchInputs(projectRoot, unapproved)
	}
	var lines []string
	for _, u := range unapproved {
		lines = append(lines, fmt.Sprintf("  %s (%s)", u.Path, u.Reason))
	}
	return fmt.Errorf("ce projet déclare des entrées qui exécutent du code ou lancent des processus (greffons OpenCode et commandes MCP, extensions et réglages Pi), et elles ne sont pas approuvées pour leur contenu actuel :\n%s\n"+
		"Relisez-les, puis lancez 'codebox trust approve' pour enregistrer l'approbation du contenu actuel",
		strings.Join(lines, "\n"))
}

// configCmd implements `codebox config <subcommand>`. It is read-only: it
// resolves and explains the effective configuration without mutating the
// environment or writing any file. `explain` shows each managed field with
// its winning source (flags > CODEBOX_* env > project > user > default),
// secrets never included; `import-env` previews a bounded legacy .env import
// without touching the original file.
func configCmd(parsed parsedArgs) (int, error) {
	args := parsed.configArgs
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage : codebox config explain | import-env <chemin>")
		return 2, nil
	}
	switch args[0] {
	case "explain":
		return configExplainCmd(parsed)
	case "import-env":
		if len(args) < 2 {
			return 2, fmt.Errorf("Usage : codebox config import-env <chemin-vers-.env>")
		}
		return configImportEnvCmd(args[1])
	default:
		return 2, fmt.Errorf("Commande config inconnue : %s (attendu explain ou import-env)", args[0])
	}
}

// configExplainCmd resolves the managed fields and prints them with
// provenance. The legacy environment variables (RUNTIME, ISOLATION, ...) are
// NOT part of the new resolution: explain reports the new-path view only,
// so the two paths cannot be confused during the deprecation interval.
// Invocation flags are the highest-precedence source and are passed through
// from parseArgs, so `codebox --tart config explain` reports the flag.
func configExplainCmd(parsed parsedArgs) (int, error) {
	settingsPath, err := codebox.UserSettingsPath()
	if err != nil {
		return 0, err
	}
	us, err := codebox.ReadUserSettings(codebox.DefaultFS, settingsPath)
	if err != nil {
		return 0, err
	}
	user := map[string]string{}
	if us.DefaultRuntime != "" {
		user["runtime"] = us.DefaultRuntime
	}
	if us.DefaultIsolation != "" {
		user["isolation"] = us.DefaultIsolation
	}
	if us.DefaultModel != "" {
		user["model"] = us.DefaultModel
	}
	if us.CredentialRef != "" {
		user["credential_ref"] = us.CredentialRef
	}
	flag := map[string]string{}
	if parsed.runtime != "" {
		flag["runtime"] = parsed.runtime
	}
	if parsed.isolation != "" {
		flag["isolation"] = parsed.isolation
	}
	env := codebox.EnvSettings(os.LookupEnv)
	cwd, _ := os.Getwd()
	proj := map[string]string{}
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(cwd))
	if err == nil {
		// Field presence is preserved even for explicit empty strings: an
		// empty project value means "turn this field off", which must win
		// over a lower-precedence user value.
		proj["runtime"] = pm.Runtime
		proj["isolation"] = pm.Isolation
		proj["model"] = pm.Model
		proj["credential_ref"] = pm.CredentialRef
		if pm.CPUs > 0 {
			proj["cpus"] = fmt.Sprintf("%d", pm.CPUs)
		}
		if pm.MemoryMB > 0 {
			proj["memory_mb"] = fmt.Sprintf("%d", pm.MemoryMB)
		}
	} else if !os.IsNotExist(err) {
		// A present-but-invalid manifest is a configuration problem, not an
		// absent one: report it rather than silently showing defaults.
		return 0, err
	}
	entries := codebox.Explain(codebox.Settings{
		Flag:    flag,
		Env:     env,
		Project: proj,
		User:    user,
	})
	fmt.Print(codebox.FormatExplain(entries))
	return 0, nil
}

// configImportEnvCmd previews the bounded legacy .env import. It never writes
// the original or any managed file; the user reviews the preview and applies
// the result explicitly later (P11's wizard owns the apply step).
func configImportEnvCmd(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("lecture de %s : %w", path, err)
	}
	imp := codebox.ImportLegacyDotenv(string(data))
	fmt.Print(imp.FormatLegacyImport())
	return 0, nil
}

func attachCmd(d *codebox.Dispatcher, cfg codebox.Config, rt codebox.Runtime, instance, projectRoot string) (int, error) {
	b, err := d.Backend(rt)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if cfg.Isolation != codebox.IsolationFull {
		if err := codebox.ValidateOpenCodeBackend(cfg); err != nil {
			return 0, err
		}
		help, err := codebox.OpenCodeHelp(ctx, "--help")
		if err != nil || !strings.Contains(help, "--server") {
			return 0, fmt.Errorf("OpenCode v2 est requis sur le poste ; installez-le avec 'npm install -g @opencode/cli@%s'", codebox.OpenCodeVersion)
		}
	}
	if err := d.Prepare(ctx, rt); err != nil {
		return 0, err
	}
	// Reconcile when the backend supports it (P07): an existing instance
	// gets its configuration classified and an interrupted apply resumed,
	// instead of a blind start.
	if err := startWithLaunchReport(ctx, d, b); err != nil {
		return 0, err
	}
	if cfg.Isolation == codebox.IsolationFull {
		// The whole agent lives in the guest: no host opencode preflight, no
		// health endpoint, no attach. The host is only a terminal passthrough.
		agentErr := b.RunAgent(ctx)
		code := exitCodeOf(agentErr)
		exitScreen(ctx, d, cfg, rt, instance)
		return code, agentErr
	}
	endpoint, err := b.Endpoint(ctx)
	if err != nil {
		return 0, err
	}
	// A typo in CODEBOX_START_TIMEOUT must not block commands that never start
	// a runtime, so it is validated only here, where it is actually read.
	if cfg.StartTimeoutErr != nil {
		return 0, cfg.StartTimeoutErr
	}
	// Fail before starting a runtime rather than after, when the TUI cannot open.
	if _, err := exec.LookPath("opencode"); err != nil {
		return 0, fmt.Errorf("la CLI 'opencode' est requise pour attacher l'interface mais est introuvable dans le PATH ; installez-la avec 'npm install -g @opencode/cli@2.2.5'")
	}
	// Microsandbox's launch report has already waited before saving its timing.
	if d.Launch == nil {
		fmt.Fprintln(os.Stderr, "Préparation de l'agent…")
		health := codebox.DefaultHealthConfig()
		health.Deadline = cfg.StartTimeout
		health.Progress = os.Stderr
		if err := codebox.WaitHealthy(ctx, endpoint, cfg.Username, cfg.Password, health, nil); err != nil {
			return 0, fmt.Errorf("%w\n  Lancez 'codebox logs --%s' pour voir la cause, ou 'codebox restart --%s' pour la recréer.", err, rt, rt)
		}
	}

	hasSessions, sessionErr := codebox.OpenCodeRemoteHasSessions(ctx, endpoint, cfg.Username, cfg.Password)
	var help string
	var helpErr error
	if sessionErr == nil && hasSessions {
		help, helpErr = codebox.OpenCodeHelp(ctx, "--help")
	}
	resumeArgs, warn := codebox.OpenCodeResumeArgs(hasSessions, sessionErr, help, helpErr)
	if warn {
		if d.Launch != nil && d.Launch.Terminal {
			d.Launch.AddWarning("reprise automatique indisponible ; les sessions restent accessibles dans OpenCode")
		} else {
			codebox.WarnOpenCodeResumeUnavailable("sur l'hôte")
		}
	}
	printLaunchReport(d.Launch)
	attachErr := codebox.RunInteractiveEnv(codebox.OpenCodeClientEnv(cfg.Password), "opencode", openCodeAttachArgs(endpoint, resumeArgs)...)
	code := exitCodeOf(attachErr)
	exitScreen(ctx, d, cfg, rt, instance)
	return code, attachErr
}

// openCodeAttachArgs keeps the host-TUI backend path consistent with the
// guest TUI paths: resume the latest conversation, while leaving /new
// available inside OpenCode to start a fresh one.
func openCodeAttachArgs(endpoint string, resumeArgs []string) []string {
	args := []string{"--server", endpoint}
	return append(args, resumeArgs...)
}

func lifecycleCmd(d *codebox.Dispatcher, rt codebox.Runtime, action string, follow bool) (int, error) {
	b, err := d.Backend(rt)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()

	// start, restart and recreate all refuse to run while another
	// runtime's instance is active. recreate displaces the selected
	// runtime's own instance, not another runtime's: leaving Prepare out
	// would start a second environment while the first is still up,
	// violating the one-active-instance policy.
	if action == "start" || action == "restart" || action == "recreate" {
		if err := d.Prepare(ctx, rt); err != nil {
			return 0, err
		}
	}

	switch action {
	case "start":
		err = startWithLaunchReport(ctx, d, b)
	case "restart":
		// restart is non-destructive (P07): it stops and starts the existing
		// instance, preserving disk and guest state.
		err = b.Restart(ctx)
	case "recreate":
		// recreate is the explicit destructive rebuild. The destruction is
		// named and confirmed; on a non-TTY (CI, scripts) it refuses rather
		// than destroying silently.
		if code, cerr := confirmRecreateFn(rt); cerr != nil {
			return code, cerr
		}
		err = b.Recreate(ctx)
	case "clean":
		err = b.Clean(ctx)
	case "doctor":
		err = b.Doctor(ctx)
	case "logs":
		err = b.Logs(ctx, follow)
	case "shell":
		err = b.Shell()
	default:
		err = fmt.Errorf("action non prise en charge %q", action)
	}
	if err == nil && (action == "restart" || action == "recreate") {
		err = d.WaitLaunchReady(ctx, b)
	}
	if err == nil && (action == "start" || action == "restart" || action == "recreate") {
		printLaunchReport(d.Launch)
	}
	return 0, err
}

var launchNowFn = time.Now

func startWithLaunchReport(ctx context.Context, d *codebox.Dispatcher, b codebox.Backend) error {
	if d.Launch != nil && d.Launch.Terminal {
		fmt.Fprintf(os.Stderr, "Démarrage de la machine isolée pour %s…\n", d.Launch.ProjectName)
	}
	begun := launchNowFn()
	if err := codebox.StartOrReconcile(ctx, b); err != nil {
		return err
	}
	if err := d.WaitLaunchReady(ctx, b); err != nil {
		return err
	}
	d.Launch.Observe(launchNowFn().Sub(begun))
	if m, ok := b.(*codebox.MicrosandboxRuntime); ok && d.Launch != nil {
		d.RecordLaunchSkills()
		d.Launch.RecordMCP(m.OpenCodeOverlay.MCPConnectors)
	}
	return nil
}

func printLaunchReport(launch *codebox.LaunchUX) {
	if launch == nil || !launch.Terminal {
		return
	}
	for _, line := range launch.Checklist() {
		fmt.Fprintln(os.Stderr, line)
	}
}

var confirmRecreateFn = confirmDestructiveRecreate

// confirmDestructiveRecreate asks the user to name what recreate destroys.
// On a non-TTY (CI, scripts) it refuses rather than destroying silently: the
// explicit replacement is `codebox clean --<runtime>` followed by start,
// which names the instance and the loss in its own output.
func confirmDestructiveRecreate(rt codebox.Runtime) (int, error) {
	fmt.Fprintf(os.Stderr, "Attention : recreate --%s reconstruit de zéro l'environnement %s et DÉTRUIT : les sessions de la machine isolée, les outils installés dans la machine isolée et les fichiers propres à la machine isolée.\n", rt, rt)
	if !isTTY() {
		return 1, fmt.Errorf("recreate est destructeur et exige une confirmation interactive ; lancez 'codebox clean --%s' puis 'codebox start --%s' si vous voulez vraiment recréer", rt, rt)
	}
	fmt.Print("Recréer et perdre cet état ? Saisissez le nom du runtime pour confirmer : ")
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(reply) != string(rt) {
		return 1, fmt.Errorf("non confirmé ; rien n'a été détruit")
	}
	return 0, nil
}

// isTTY reports whether stdin can actually carry an interactive answer.
//
// A character device is not sufficient: /dev/null is one, and
// `something < /dev/null` is exactly how a script says "never wait on input".
// Reporting it as a terminal made the non-interactive paths take the
// interactive branch, which ranges from a confusing failure to silently
// accepting a default the caller meant to be asked about.
func isTTY() bool {
	return isTTYFile(os.Stdin)
}

func isTTYFile(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if devNull, err := os.Open(os.DevNull); err == nil {
		defer func() { _ = devNull.Close() }()
		if nullInfo, err := devNull.Stat(); err == nil && os.SameFile(info, nullInfo) {
			return false
		}
	}
	return true
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func usage() {
	fmt.Println(`codebox — gérer la machine isolée OpenCode

Usage :
  codebox [commande] [--microsandbox | --tart | --agent-vm] [--isolation backend | full]

Lancez codebox sans commande pour démarrer le runtime sélectionné. Sur un
poste non configuré, la valeur par défaut est Microsandbox/full : l'agent et
l'interface OpenCode native tournent dans la machine isolée. L'isolation
backend est une option explicite qui exécute le backend dans la machine isolée
et attache l'interface du poste.

Commandes :
  start      Démarrer un backend sans attacher l'interface
  acp        Exposer l'agent invité via Agent Client Protocol (Microsandbox/full)
  stop       Arrêter l'instance du projet courant (stop --all pour toutes les
             instances codebox)
  check      Vérifier le backend actif et le fournisseur Albert
  restart    Arrêter puis démarrer l'instance du projet courant (non destructif)
  recreate   Reconstruire de zéro la machine isolée sélectionnée (destructif ;
             demande confirmation)
  logs       Afficher les journaux du runtime sélectionné et quitter
             (logs --follow pour les suivre)
  shell      Ouvrir un shell dans le runtime sélectionné
  clean      Supprimer la machine isolée sélectionnée et son état local
  doctor     Vérifier l'installation du runtime sélectionné
  config     Afficher la configuration gérée (explain)
  auth       Gérer les identifiants globaux (add, status, remove)
  bindings   Approuver ou révoquer les liaisons d'identifiants optionnelles de
             ce projet (list, approve, revoke)
  trust      Approuver les entrées de projet liées à l'exécution (status,
             approve)
  mcp        Vérifier les points d'accès MCP distants configurés (status)
  init       Configurer ce projet : mode de partage, modèle, ressources de la
             machine isolée, MCP distants et référence d'identifiant.
             Interactif par défaut ; voir 'codebox init --help' pour les options.
  workspace  Gérer l'espace de travail scellé de la machine isolée : ce qui
             franchit la frontière, les ré-inclusions par fichier, le
             rafraîchissement et l'export relu (status, sync, allow, deny,
             export). Le dossier du projet sur le poste n'est jamais monté.
  setup      Configurer le poste : prérequis, identifiants, identité, modèle,
             puis installer le runtime géré (setup doctor pour le diagnostic
             en lecture seule)
  stats      Gérer le partage optionnel de statistiques d'usage (on, off, show)
  update     Relire et appliquer les mises à jour de skills épinglées du projet
  version    Afficher l'identité de compilation
  help       Afficher cette aide

Sélection du runtime :
  Microsandbox en isolation full est la valeur par défaut : l'agent tourne dans
  la microVM et son espace de travail est scellé (aucun fichier du poste n'est
  monté). Passez --microsandbox, --tart ou --agent-vm, ou définissez RUNTIME,
  pour la remplacer ; un flag explicite l'emporte toujours. --isolation backend
  sélectionne le mode historique où l'interface tourne sur le poste. Tart et
  agent-vm montent le dossier, transmettent l'identifiant à la machine isolée en
  clair et exigent --acknowledge-guest-credentials pour démarrer.

  Un .env du dossier de travail n'est pas chargé implicitement ; les variables
  exportées le sont toujours. La clé d'API se stocke dans le trousseau avec
  'codebox setup' ou 'codebox auth add albert'.

Isolation :
  --isolation full (défaut) : tout l'agent, interface comprise, tourne dans la
  machine isolée ; le poste n'est qu'un terminal de passage.
  --isolation backend : l'agent tourne comme serveur dans la machine isolée et
  l'interface s'y attache depuis le poste. ISOLATION est utilisée quand aucun
  flag n'est fourni ; le flag l'emporte toujours. La façon dont chaque runtime
  gère l'identifiant dans chaque mode est décrite ci-dessus.`)
}

// applyProjectSandboxResources applies the project manifest's guest sizing.
//
// Precedence follows the configuration contract (#29): an exported variable
// beats the project manifest, which beats the built-in default. The manifest
// is where the project setup records the answer to "how much machine does this
// project get", so a value written there must actually size the guest — a
// recorded setting nothing reads is worse than not asking.
func applyProjectSandboxResources(cfg codebox.Config, projectRoot string) (codebox.Config, error) {
	pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	if err != nil {
		// A missing manifest is the normal zero-flag state. A present but
		// unreadable one is reported by the paths that own that decision.
		return cfg, nil
	}
	cfg.Ports = append([]codebox.PortMapping(nil), pm.Ports...)
	cfg.PortsConfigured = pm.PortsConfigured || pm.Ports != nil
	if cfg.PortsConfigured {
		if err := codebox.ValidatePortMappings(cfg.Ports); err != nil {
			return cfg, fmt.Errorf("correspondance de ports du projet invalide : %w", err)
		}
	}
	// Zero means "not set in the manifest", which is the common case. Any
	// other out-of-range value — negative, or above what the runtime can
	// carry — is reported rather than ignored: a hand-edited manifest should
	// fail loudly on both sides of the range, not just above it.
	if value, set := os.LookupEnv("CODEBOX_CPUS"); !set || value == "" {
		switch {
		case pm.CPUs == 0:
		case pm.CPUs < 0 || pm.CPUs > codebox.MaxSandboxCPUs:
			return cfg, fmt.Errorf("le manifeste du projet demande %d processeurs ; le runtime de la machine isolée accepte 1 à %d", pm.CPUs, codebox.MaxSandboxCPUs)
		default:
			cfg.CPUs = pm.CPUs
		}
	}
	if value, set := os.LookupEnv("CODEBOX_MEMORY_MB"); !set || value == "" {
		switch {
		case pm.MemoryMB == 0:
		case pm.MemoryMB < 0 || pm.MemoryMB > codebox.MaxSandboxMemoryMB:
			return cfg, fmt.Errorf("le manifeste du projet demande %d MiB ; le runtime de la machine isolée accepte 1 à %d", pm.MemoryMB, codebox.MaxSandboxMemoryMB)
		default:
			cfg.MemoryMB = pm.MemoryMB
		}
	}
	return cfg, nil
}

// enforceHostResourceLimits ensures the effective guest request, after env
// and manifest precedence have been applied, stays within detected host
// capacity. Only implicit built-in defaults are reduced automatically;
// explicit project or environment choices fail above the hard ceiling.
func enforceHostResourceLimits(cfg codebox.Config, projectRoot string, reports ...*codebox.LaunchUX) (codebox.Config, error) {
	host, err := detectHostResourcesFn()
	if err != nil {
		return cfg, fmt.Errorf("impossible de déterminer la capacité CPU et mémoire du poste ; impossible d'appliquer la limite de ressources de la machine isolée : %v", err)
	}
	if host.CPUs <= 0 || host.MemoryMB <= 0 {
		return cfg, fmt.Errorf("la détection des ressources du poste a renvoyé une capacité CPU ou mémoire incomplète ; impossible d'appliquer la limite de ressources de la machine isolée")
	}
	pm, manifestErr := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(projectRoot))
	manifestPresent := manifestErr == nil
	if manifestErr != nil && !os.IsNotExist(manifestErr) {
		return cfg, fmt.Errorf("impossible d'inspecter les réglages de ressources du projet : %w", manifestErr)
	}
	cpuExplicit := false
	if value, ok := os.LookupEnv("CODEBOX_CPUS"); ok && value != "" {
		cpuExplicit = true
	} else if manifestPresent && pm.CPUs != 0 {
		cpuExplicit = true
	}
	memoryExplicit := false
	if value, ok := os.LookupEnv("CODEBOX_MEMORY_MB"); ok && value != "" {
		memoryExplicit = true
	} else if manifestPresent && pm.MemoryMB != 0 {
		memoryExplicit = true
	}
	maxCPUs := codebox.MaxGuestCPUs(host.CPUs)
	maxMemoryMB := codebox.MaxGuestMemoryMB(host.MemoryMB)
	if maxCPUs < 1 {
		return cfg, fmt.Errorf("la détection des ressources du poste ne signale aucun processeur logique disponible pour la machine isolée")
	}
	if maxMemoryMB < 1 {
		return cfg, fmt.Errorf("la mémoire du poste est trop faible pour allouer la mémoire de la machine isolée")
	}
	if !cpuExplicit && cfg.CPUs > codebox.RecommendedDefaultGuestCPUs(host.CPUs) {
		cfg.CPUs = codebox.RecommendedDefaultGuestCPUs(host.CPUs)
	}
	if !memoryExplicit && cfg.MemoryMB > codebox.RecommendedDefaultGuestMemoryMB(host.MemoryMB) {
		cfg.MemoryMB = codebox.RecommendedDefaultGuestMemoryMB(host.MemoryMB)
	}
	if err := codebox.ValidateGuestResources(host, cfg.CPUs, cfg.MemoryMB); err != nil {
		return cfg, err
	}
	for _, warning := range codebox.GuestResourceWarnings(host, cfg.CPUs, cfg.MemoryMB) {
		if len(reports) > 0 && reports[0] != nil && reports[0].Terminal {
			reports[0].AddWarning("ressources de la machine hôte limitées ; détail : codebox doctor")
		} else {
			fmt.Fprintf(os.Stderr, "Attention : %s\n", warning)
		}
	}
	return cfg, nil
}
