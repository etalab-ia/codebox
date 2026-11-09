// Command just-code is a single Go binary that replaces the original justfile.
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

	"github.com/etalab-ia/just-code/internal/justcode"
)

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "just-code:", err)
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
		fmt.Println(justcode.MSBSDKVersion())
		return 0, nil
	}

	// The in-VM bootstrap is a hidden subcommand of this same binary. It runs
	// inside the Tart guest and must not touch host config or load .env.
	if len(args) > 0 && args[0] == justcode.GuestBootstrapCommand {
		overlay, err := justcode.DecodeGuestOverlay(argOr(args, justcode.GuestBootstrapModelsArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("invalid guest bootstrap model configuration: %w", err)
		}
		mcps, err := parseGuestMCPIDs(argOr(args, justcode.GuestBootstrapMCPArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("invalid guest bootstrap MCP selection: %w", err)
		}
		cfg := justcode.GuestConfig{
			Port:     argOr(args, 1, ""),
			Username: argOr(args, 2, ""),
			MTU:      argOr(args, 3, ""),
			// The model selection (P10) is non-secret managed configuration
			// and rides argv; secrets never do.
			OpenCodeOverlay: justcode.ManagedOverlay{Model: argOr(args, 4, ""), SmallModel: overlay.SmallModel, Models: overlay.Models, Providers: overlay.Providers, AlbertEnabled: overlay.AlbertEnabled, MCPConnectors: mcps},
			// The git identity (P11) rides argv the same way: the guest
			// cannot read the host settings file.
			GitName:  argOr(args, 6, ""),
			GitEmail: argOr(args, 7, ""),
		}
		return 0, justcode.RunGuestBootstrap(context.Background(), cfg)
	}

	// Isolation-full in-VM helpers, same hidden-subcommand contract: no host
	// config, no .env. __guest-prepare installs OpenCode; __guest-secrets
	// writes the 0600 env file from stdin.
	if len(args) > 0 && args[0] == justcode.GuestPrepareCommand {
		cfg := justcode.GuestConfig{Username: argOr(args, 1, ""), GitName: argOr(args, 2, ""), GitEmail: argOr(args, 3, "")}
		return exitCodeOf(nil), justcode.RunGuestPrepare(context.Background(), cfg)
	}
	if len(args) > 0 && args[0] == justcode.GuestSecretsCommand {
		overlay, err := justcode.DecodeGuestOverlay(argOr(args, justcode.GuestSecretsModelsArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("invalid guest model configuration: %w", err)
		}
		mcps, err := parseGuestMCPIDs(argOr(args, justcode.GuestSecretsMCPArgIndex, ""))
		if err != nil {
			return 2, fmt.Errorf("invalid guest MCP selection: %w", err)
		}
		// The model selection (P10) is the one managed field the in-guest
		// secrets file must reflect; argv carries no secrets, and the
		// overlay is non-secret configuration.
		guestOverlay := justcode.ManagedOverlay{Model: argOr(args, 2, ""), SmallModel: overlay.SmallModel, Models: overlay.Models, Providers: overlay.Providers, AlbertEnabled: overlay.AlbertEnabled, MCPConnectors: mcps}
		cfg := justcode.GuestConfig{Username: argOr(args, 1, ""), OpenCodeOverlay: guestOverlay}
		return exitCodeOf(nil), justcode.RunGuestSecrets(cfg)
	}
	// The detached statistics flusher is a hidden subcommand: it never
	// blocks the wizard that started it, and it re-checks consent before
	// sending anything.
	if len(args) > 0 && args[0] == justcode.StatsFlushCommand {
		_ = justcode.FlushStats(context.Background(), justcode.DefaultFS, justcode.DefaultStateDir())
		return 0, nil
	}

	// Parse arguments before the legacy dotenv load: `config explain`
	// reports the new resolver's view (flags > JUST_CODE_* env > project >
	// user > default), and a legacy .env must not leak into that view as
	// pseudo-env. The config command dispatches with the process
	// environment exactly as the user set it.
	parsed, err := parseArgs(args)
	if err != nil {
		return 2, err
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

	cfg := justcode.LoadConfigEnv()
	cfg.GuestCredentialsAcknowledged = parsed.ackGuestCreds
	if parsed.version {
		printBuildInfo()
		return 0, nil
	}

	// Project identity (P05/P06): the backends are bound to the current
	// project's instance so lifecycle operations are project-scoped. A
	// missing registry entry is not an error here — discovery derives the
	// deterministic instance name; registration happens at first start.
	pc, projErr := justcode.DiscoverProject(".")
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
		if err := justcode.RecoverProjectUpdate(justcode.DefaultFS, projectRoot); err != nil {
			return 1, fmt.Errorf("cannot recover the project's dependency update: %w\nIf recovery refused edits to the managed files, inspect the journal and run 'just-code update --recover --rollback' to restore the previous pair with backups", err)
		}
	}
	if parsed.action == "update" {
		if projErr != nil {
			return 2, fmt.Errorf("update requires a configured project directory: %w", projErr)
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
		packages, skillIDs, managed, localOnly, err := justcode.LoadProjectSkillState(justcode.DefaultFS, projectRoot, justcode.DefaultStateDir(), instance)
		if err != nil {
			return 1, fmt.Errorf("cannot resolve the project's pinned skills before runtime changes: %w", err)
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
		remote, err := projectGitHubRemote(projectRoot, justcode.DefaultStateDir(), instance)
		if err != nil {
			return 1, fmt.Errorf("the approved GitHub workflow cannot use this project origin; no runtime changes were made: %w", err)
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
		fmt.Fprintf(os.Stderr, "Warning: %v; using the default agent harness\n", harnessErr)
	}
	cfg.Harness = harness
	var launch *justcode.LaunchUX
	if isLaunchAction(parsed.action) && runtimeUsesMicrosandbox(parsed.runtime, manifestRuntime) {
		launch = justcode.NewLaunchUX(projectRoot, harness.DisplayName())
		launch.TimingStore = justcode.DefaultLaunchTimingStore()
	}
	// Resolve the isolation level before any backend is constructed: the
	// backends read cfg.Isolation at construction time, so applying the flag
	// afterwards would leave them in the wrong mode.
	cfg, isoErr := applyIsolation(cfg, parsed.isolation, manifestIsolation)
	// A harness without a server mode cannot run in backend isolation. The
	// pair is checked as soon as both are known, so the refusal happens before
	// a guest is created rather than when the launch finds nothing to attach to.
	if isoErr == nil {
		if err := justcode.ValidateHarnessIsolation(cfg.EffectiveHarness(), cfg.Isolation); err != nil {
			if isLaunchAction(parsed.action) {
				return 2, err
			}
			fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "Warning: %v; using the default guest sizing\n", resErr)
	}
	if isLaunchAction(parsed.action) && runtimeUsesMicrosandbox(parsed.runtime, manifestRuntime) {
		cfg, resErr = enforceHostResourceLimits(cfg, projectRoot, launch)
		if resErr != nil {
			return 1, resErr
		}
	}
	cfg.CredentialRef = resolveCredentialRef(projectRoot)

	// Managed OpenCode configuration (P10): the model selection from the
	// typed resolver (JUST_CODE_MODEL > manifest > user settings), the
	// composed OPENCODE_CONFIG_CONTENT carrying it, field conflicts
	// surfaced against the project config, and execution inputs gated
	// behind the host-local trust record.
	mcpSelection, mcpErr := resolveMCPSelection(projectRoot)
	if mcpErr != nil {
		if isLaunchAction(parsed.action) {
			return 1, fmt.Errorf("cannot resolve the project's managed MCP selection: %w", mcpErr)
		}
		fmt.Fprintf(os.Stderr, "Warning: the project's managed MCP selection cannot be read (%v); continuing without it\n", mcpErr)
	}
	modelSelection := resolveModelSelection(projectRoot)
	smallModelSelection := resolveSmallModelSelection(projectRoot)
	providerSettings, providerErr := resolveProviderSettings()
	if providerErr != nil {
		if isLaunchAction(parsed.action) {
			return 1, fmt.Errorf("cannot resolve the inference platforms: %w", providerErr)
		}
		fmt.Fprintf(os.Stderr, "Warning: the inference platforms cannot be resolved (%v); continuing without them\n", providerErr)
	} else {
		cfg.Providers, providerErr = justcode.ResolveProviders(providerSettings)
		if providerErr != nil {
			if isLaunchAction(parsed.action) {
				return 1, fmt.Errorf("cannot resolve the inference platforms: %w", providerErr)
			}
			fmt.Fprintf(os.Stderr, "Warning: the inference platforms cannot be resolved (%v); continuing without them\n", providerErr)
		}
	}
	if isLaunchAction(parsed.action) {
		if err := validateSmallModelProviderConnection(cfg.Providers, smallModelSelection); err != nil {
			return 1, err
		}
	}
	var albertModels []justcode.AlbertModelConfig
	if providersContain(cfg.Providers, justcode.ProviderAlbert) {
		albertModels = resolveEnabledAlbertModels(modelSelection, smallModelSelection)
	}
	overlay := justcode.ManagedOverlay{
		Model:         modelSelection,
		SmallModel:    smallModelSelection,
		Models:        albertModels,
		Providers:     resolveProviderOverlays(cfg.Providers, modelSelection, smallModelSelection),
		AlbertEnabled: providersContain(cfg.Providers, justcode.ProviderAlbert),
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
			fmt.Fprintf(os.Stderr, "Warning: the project OpenCode configuration cannot be read (%v); continuing without the conflict review\n", err)
		} else {
			return 1, err
		}
	}
	if conflictMsg != "" {
		if launch != nil && launch.Terminal {
			launch.AddWarning("des réglages OpenCode du projet sont remplacés par les réglages gérés ; détail : just-code config explain")
		} else {
			fmt.Fprintf(os.Stderr, "Warning: managed OpenCode fields differ from the project configuration:\n%s\n", conflictMsg)
		}
	}
	// Trust gate (P10): project plugins and MCP commands execute code at
	// OpenCode config load, so starting a runtime for this project requires
	// every execution input to be approved at its current content hash. A
	// changed file is a new decision, not an inherited one. Only start paths
	// are gated: stop, check, logs and shell operate on an existing guest.
	if startPath {
		if err := justcode.ValidateOpenCodeBackend(cfg); err != nil {
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
			return 2, fmt.Errorf("bindings requires a project directory: %v", projErr)
		}
		return bindingsCmd(parsed.bindingsArgs, instance)
	}

	// models shows the validated Albert catalogue (P10). It runs before
	// any runtime is constructed and needs no project.
	if parsed.action == "models" {
		return modelsCmd(parsed.modelsArgs)
	}

	// trust manages the host-local approval of execution-relevant project
	// OpenCode inputs (P10). It runs before any runtime is constructed.
	if parsed.action == "trust" {
		if projErr != nil {
			return 2, fmt.Errorf("trust requires a project directory: %v", projErr)
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
			return 2, fmt.Errorf("workspace requires a project directory: %v", projErr)
		}
		return workspaceCmd(parsed.workspaceArgs, cfg, instance)
	}

	// The dispatcher and its backends are built from the resolved config,
	// bound to the project instance when discovery succeeded. Discovery
	// failure falls back to the legacy singleton rather than blocking
	// lifecycle commands on a non-Git directory.
	var d *justcode.Dispatcher
	if projErr == nil {
		d = justcode.NewDispatcherForInstance(cfg, instance)
	} else {
		d = justcode.NewDispatcher(cfg)
	}
	d.SetOpenCodeOverlay(overlay)
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
	rt, err := justcode.ResolveRuntime(parsed.runtime, runtimePreference)
	if err != nil {
		return 2, err
	}
	if len(cfg.ProjectSkills) > 0 && rt != justcode.RuntimeMicrosandbox {
		return 2, fmt.Errorf("project skills currently require the sealed microsandbox runtime; no runtime changes were made")
	}
	// A typo in ISOLATION must not block commands that never read it, so it is
	// validated only here, where the level is actually consumed.
	if isoErr != nil {
		return 2, isoErr
	}

	switch parsed.action {
	case "attach":
		return attachCmd(d, cfg, rt, instance, projectRoot)
	case "start", "logs", "shell", "restart", "recreate", "clean", "doctor":
		return lifecycleCmd(d, rt, parsed.action)
	default:
		return 2, fmt.Errorf("Unknown argument: %s", parsed.action)
	}
}

func parseGuestAlbertModels(raw string) ([]justcode.AlbertModelConfig, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return nil, nil
	}
	var models []justcode.AlbertModelConfig
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, err
	}
	for _, model := range models {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Name) == "" {
			return nil, fmt.Errorf("model IDs and names must be non-empty")
		}
	}
	return models, nil
}

func runtimeUsesMicrosandbox(runtimeFlag string, manifestRuntime justcode.Runtime) bool {
	preference := os.Getenv("RUNTIME")
	if preference == "" {
		preference = string(manifestRuntime)
	}
	runtime, err := justcode.ResolveRuntime(runtimeFlag, preference)
	return err == nil && runtime == justcode.RuntimeMicrosandbox
}

func resolveMCPSelection(projectRoot string) ([]string, error) {
	manifest, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ids, err := justcode.ValidateMCPConnectorIDs(manifest.MCPConnectors)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func parseGuestMCPIDs(value string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return justcode.ValidateMCPConnectorIDs(strings.Split(value, ","))
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
func withProjectRootAsWorkspaceSource(cfg justcode.Config, pc justcode.ProjectContext, projErr error) justcode.Config {
	if cfg.WorkspaceDirSet || projErr != nil {
		return cfg
	}
	cfg.WorkspaceDir = pc.Root
	return cfg
}

// applyIsolation resolves the effective isolation level: an explicit flag
// wins, then an exported ISOLATION, then the project manifest's choice, then
// the built-in default. A project value only decides when nothing higher does,
// so a one-off `--isolation backend` still overrides what the project
// recorded. It exists as a separate step because the backends read
// cfg.Isolation when they are constructed: resolving the level after building
// the dispatcher would leave them in the wrong mode.
func applyIsolation(cfg justcode.Config, flag string, project justcode.Isolation) (justcode.Config, error) {
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
	iso, err := justcode.ResolveIsolationLevel(flag, preference, preferenceErr)
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
func projectRuntimeIsolation(projectRoot string) (justcode.Runtime, justcode.Isolation) {
	pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot))
	if err != nil {
		return "", ""
	}
	return justcode.Runtime(pm.Runtime), justcode.Isolation(pm.Isolation)
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
	"help": true, "version": true, "config": true,
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
				return p, fmt.Errorf("Select exactly one runtime.")
			}
			p.runtime = a
		case a == "--isolation" || strings.HasPrefix(a, "--isolation="):
			value := ""
			if a == "--isolation" {
				if i+1 >= len(args) {
					return p, fmt.Errorf("--isolation requires a value: backend or full")
				}
				i++
				value = args[i]
			} else {
				value = strings.TrimPrefix(a, "--isolation=")
			}
			if value == "" {
				return p, fmt.Errorf("--isolation requires a value: backend or full")
			}
			if p.isolation != "" && p.isolation != value {
				return p, fmt.Errorf("Select exactly one isolation level.")
			}
			p.isolation = value
		case a == "-h" || a == "--help":
			p.action = "help"
			actionSet = true
		case a == "-V" || a == "--version" || a == "-v":
			p.version = true
		case a == "--all":
			if p.stopAll {
				return p, fmt.Errorf("--all may be given only once")
			}
			p.stopAll = true
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
			return p, fmt.Errorf("The 'code' command was removed: run 'just-code' with no command to launch the selected runtime (Microsandbox/full by default).")
		default:
			return p, fmt.Errorf("Unknown argument: %s", a)
		}
	}
	if p.action == "" {
		p.action = "attach"
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
// JUST_CODE_CREDENTIAL_REF > project manifest credentialRef > user settings
// credentialRef. The manifest is read from the discovered project root, not
// from cwd: invoked from a subdirectory of a worktree, a cwd-relative read
// would miss it and silently inject the user-level credential into that
// project's sandbox. Reads are best-effort: a missing file means "no
// reference", and a malformed one is reported by `config explain`, not by
// every command that might start a runtime.
func resolveCredentialRef(projectRoot string) string {
	if v := strings.TrimSpace(os.Getenv("JUST_CODE_CREDENTIAL_REF")); v != "" {
		return v
	}
	if pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot)); err == nil && pm.CredentialRef != "" {
		return pm.CredentialRef
	}
	return resolveUserCredentialRef()
}

func resolveUserCredentialRef() string {
	if path, err := justcode.UserSettingsPath(); err == nil {
		if us, err := justcode.ReadUserSettings(justcode.DefaultFS, path); err == nil {
			return us.CredentialRef
		}
	}
	return ""
}

// resolveModelSelection applies the model precedence (P10):
// JUST_CODE_MODEL > project manifest > user settings. Empty means the
// embedded default, which the overlay applies itself.
func resolveModelSelection(projectRoot string) string {
	if v := strings.TrimSpace(os.Getenv("JUST_CODE_MODEL")); v != "" {
		return canonicalAlbertModelRef(v)
	}
	if pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot)); err == nil && pm.Model != "" {
		return canonicalAlbertModelRef(pm.Model)
	}
	if path, err := justcode.UserSettingsPath(); err == nil {
		if us, err := justcode.ReadUserSettings(justcode.DefaultFS, path); err == nil && us.DefaultModel != "" {
			return canonicalAlbertModelRef(us.DefaultModel)
		}
	}
	return ""
}

// resolveHarnessSelection applies the harness precedence (#176):
// JUST_CODE_HARNESS > project manifest > default (OpenCode). An unrecognised
// value is an error rather than a silent fallback: running a different agent
// than the project asked for is exactly what this setting exists to prevent.
func resolveHarnessSelection(projectRoot string) (justcode.Harness, error) {
	if v := strings.TrimSpace(os.Getenv("JUST_CODE_HARNESS")); v != "" {
		return justcode.ResolveHarness(v)
	}
	if pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot)); err == nil && pm.Harness != "" {
		return justcode.ResolveHarness(pm.Harness)
	}
	return justcode.ResolveHarness("")
}

// resolveSmallModelSelection reads the explicit project-only lightweight
// model. Empty deliberately leaves the overlay free to follow the effective
// main model (including JUST_CODE_MODEL) instead of pinning it here.
func resolveSmallModelSelection(projectRoot string) string {
	if pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot)); err == nil && pm.SmallModel != "" {
		return canonicalAlbertModelRef(pm.SmallModel)
	}
	return ""
}

// validateSmallModelProviderConnection prevents a manually edited manifest
// from selecting the Albert-only model after Albert was disconnected.
func validateSmallModelProviderConnection(providers []justcode.InferenceProvider, smallModel string) error {
	if smallModel == justcode.MinistralSmallModelRef && !providersContain(providers, justcode.ProviderAlbert) {
		return fmt.Errorf("small model %q requires Albert API to be connected; run 'just-code setup' to connect it, or clear smallModel in the project manifest", smallModel)
	}
	return nil
}

// resolveProviderSettings reads the connected platforms from the user
// settings. An empty list means the historical Albert-only configuration,
// which the engine applies as its default.
func resolveProviderSettings() ([]justcode.ProviderSettings, error) {
	path, err := justcode.UserSettingsPath()
	if err != nil {
		return nil, err
	}
	us, err := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if err != nil {
		return nil, err
	}
	return us.Providers, nil
}

// resolveProviderOverlays turns the configured platforms into the non-Albert
// provider blocks of the managed OpenCode config. Albert keeps its curated
// model definitions in ManagedOverlay.Models.
func resolveProviderOverlays(providers []justcode.InferenceProvider, activeModels ...string) []justcode.ProviderOverlay {
	path, err := justcode.UserSettingsPath()
	if err != nil {
		return nil
	}
	us, readErr := justcode.ReadUserSettings(justcode.DefaultFS, path)
	if readErr != nil {
		return nil
	}
	var out []justcode.ProviderOverlay
	for _, provider := range providers {
		if provider.Kind == justcode.ProviderAlbert {
			continue
		}
		ids := justcode.ProviderModelIDs(us.Models, provider.Kind)
		for _, activeModel := range activeModels {
			activeKind, activeID := justcode.ProviderModelID(activeModel)
			if activeModel != "" && activeKind == provider.Kind && activeID != "" && !containsString(ids, activeID) {
				ids = append(ids, activeID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		models := make([]justcode.ProviderModelConfig, 0, len(ids))
		for _, id := range ids {
			ref := string(provider.Kind) + "/" + id
			config := justcode.ProviderModelConfig{ID: id, Name: id}
			if apiID := us.ProviderModelIDs[ref]; apiID != "" {
				config.ModelID = apiID
			}
			models = append(models, config)
		}
		out = append(out, justcode.ProviderOverlay{
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
	for _, choice := range justcode.CuratedAlbertModels() {
		if model == "albert/"+choice.ID || model == choice.ID || model == choice.OpenCodeID || model == choice.Ref() {
			return choice.Ref()
		}
	}
	return model
}

func canonicalAlbertModelID(id string) string {
	for _, choice := range justcode.CuratedAlbertModels() {
		if id == choice.ID || id == choice.OpenCodeID || id == choice.Ref() || id == "albert/"+choice.ID {
			return choice.ID
		}
	}
	return id
}

func resolveEnabledAlbertModels(activeModels ...string) []justcode.AlbertModelConfig {
	for i, activeModel := range activeModels {
		activeModels[i] = canonicalAlbertModelRef(activeModel)
	}
	path, err := justcode.UserSettingsPath()
	var ids []string
	if err == nil {
		if settings, readErr := justcode.ReadUserSettings(justcode.DefaultFS, path); readErr == nil {
			for _, ref := range justcode.ProviderModelIDs(settings.Models, justcode.ProviderAlbert) {
				ids = append(ids, canonicalAlbertModelID(ref))
			}
		}
	}
	if len(ids) == 0 {
		embeddedOnly := true
		for _, activeModel := range activeModels {
			if activeModel != "" && activeModel != "albert/qwen3-coder" {
				embeddedOnly = false
			}
		}
		if embeddedOnly {
			return nil
		}
		for _, choice := range justcode.CuratedAlbertModels() {
			if choice.Default {
				ids = append(ids, choice.ID)
			}
		}
	}
	var configured []justcode.AlbertModelConfig
	choices := justcode.CuratedAlbertModels()
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
			configured = append(configured, justcode.AlbertModelConfig{ID: id, Name: id})
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
			configured = append(configured, justcode.AlbertModelConfig{ID: activeID, Name: activeID})
		}
	}
	return configured
}

// opencodeConfigReview surfaces managed-field conflicts between the overlay
// and the project's OpenCode configuration (D-001: OpenCode ignores unknown
// keys and never exposes a merge diff, so just-code must detect them). The
// overlay still wins (final merge), so the message says so.
func opencodeConfigReview(projectRoot string, overlay justcode.ManagedOverlay) (string, error) {
	cfgPath := justcode.FindProjectOpenCodeConfig(projectRoot)
	if cfgPath == "" {
		return "", nil
	}
	project, err := justcode.ParseProjectOpenCodeConfig(projectRoot, cfgPath)
	if err != nil {
		return "", err
	}
	conflicts := justcode.DetectOverlayConflicts(project, overlay)
	return justcode.FormatConflicts(conflicts), nil
}

// requireTrustedExecutionInputs blocks the launch when the project carries
// execution-relevant OpenCode inputs that are unapproved or changed since
// approval. Read-only commands (stop, check, status, logs, shell on an
// already-running guest) are not gated here; only paths that start a
// runtime.
func requireTrustedExecutionInputs(projectRoot string) error {
	unapproved, err := justcode.DiscoverUnapprovedInputs(justcode.DefaultFS, justcode.DefaultStateDir(), projectRoot)
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
	return fmt.Errorf("this project declares OpenCode inputs that execute code or launch processes, and they are not approved at their current content:\n%s\n"+
		"Review them, then run 'just-code trust approve' to record approval for the current content",
		strings.Join(lines, "\n"))
}

// configCmd implements `just-code config <subcommand>`. It is read-only: it
// resolves and explains the effective configuration without mutating the
// environment or writing any file. `explain` shows each managed field with
// its winning source (flags > JUST_CODE_* env > project > user > default),
// secrets never included; `import-env` previews a bounded legacy .env import
// without touching the original file.
func configCmd(parsed parsedArgs) (int, error) {
	args := parsed.configArgs
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: just-code config explain | import-env <path>")
		return 2, nil
	}
	switch args[0] {
	case "explain":
		return configExplainCmd(parsed)
	case "import-env":
		if len(args) < 2 {
			return 2, fmt.Errorf("Usage: just-code config import-env <path-to-.env>")
		}
		return configImportEnvCmd(args[1])
	default:
		return 2, fmt.Errorf("Unknown config command: %s (expected explain or import-env)", args[0])
	}
}

// configExplainCmd resolves the managed fields and prints them with
// provenance. The legacy environment variables (RUNTIME, ISOLATION, ...) are
// NOT part of the new resolution: explain reports the new-path view only,
// so the two paths cannot be confused during the deprecation interval.
// Invocation flags are the highest-precedence source and are passed through
// from parseArgs, so `just-code --tart config explain` reports the flag.
func configExplainCmd(parsed parsedArgs) (int, error) {
	settingsPath, err := justcode.UserSettingsPath()
	if err != nil {
		return 0, err
	}
	us, err := justcode.ReadUserSettings(justcode.DefaultFS, settingsPath)
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
	env := justcode.EnvSettings(os.LookupEnv)
	cwd, _ := os.Getwd()
	proj := map[string]string{}
	pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(cwd))
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
	entries := justcode.Explain(justcode.Settings{
		Flag:    flag,
		Env:     env,
		Project: proj,
		User:    user,
	})
	fmt.Print(justcode.FormatExplain(entries))
	return 0, nil
}

// configImportEnvCmd previews the bounded legacy .env import. It never writes
// the original or any managed file; the user reviews the preview and applies
// the result explicitly later (P11's wizard owns the apply step).
func configImportEnvCmd(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	imp := justcode.ImportLegacyDotenv(string(data))
	fmt.Print(imp.FormatLegacyImport())
	return 0, nil
}

func attachCmd(d *justcode.Dispatcher, cfg justcode.Config, rt justcode.Runtime, instance, projectRoot string) (int, error) {
	b, err := d.Backend(rt)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if cfg.Isolation != justcode.IsolationFull {
		if err := justcode.ValidateOpenCodeBackend(cfg); err != nil {
			return 0, err
		}
		help, err := justcode.OpenCodeHelp(ctx, "--help")
		if err != nil || !strings.Contains(help, "--server") {
			return 0, fmt.Errorf("OpenCode v2 is required on the host; install it with 'npm install -g @opencode/cli@%s'", justcode.OpenCodeVersion)
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
	if cfg.Isolation == justcode.IsolationFull {
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
	// A typo in JUST_CODE_START_TIMEOUT must not block commands that never start
	// a runtime, so it is validated only here, where it is actually read.
	if cfg.StartTimeoutErr != nil {
		return 0, cfg.StartTimeoutErr
	}
	// Fail before starting a runtime rather than after, when the TUI cannot open.
	if _, err := exec.LookPath("opencode"); err != nil {
		return 0, fmt.Errorf("the 'opencode' CLI is required to attach the TUI but was not found on PATH; install it with 'npm install -g @opencode/cli@2.2.5'")
	}
	// Microsandbox's launch report has already waited before saving its timing.
	if d.Launch == nil {
		fmt.Fprintln(os.Stderr, "Préparation de l'agent…")
		health := justcode.DefaultHealthConfig()
		health.Deadline = cfg.StartTimeout
		health.Progress = os.Stderr
		if err := justcode.WaitHealthy(ctx, endpoint, cfg.Username, cfg.Password, health, nil); err != nil {
			return 0, fmt.Errorf("%w\n  Run 'just-code logs --%s' to see why, or 'just-code restart --%s' to recreate it.", err, rt, rt)
		}
	}

	hasSessions, sessionErr := justcode.OpenCodeRemoteHasSessions(ctx, endpoint, cfg.Username, cfg.Password)
	var help string
	var helpErr error
	if sessionErr == nil && hasSessions {
		help, helpErr = justcode.OpenCodeHelp(ctx, "--help")
	}
	resumeArgs, warn := justcode.OpenCodeResumeArgs(hasSessions, sessionErr, help, helpErr)
	if warn {
		if d.Launch != nil && d.Launch.Terminal {
			d.Launch.AddWarning("reprise automatique indisponible ; les sessions restent accessibles dans OpenCode")
		} else {
			justcode.WarnOpenCodeResumeUnavailable("sur l'hôte")
		}
	}
	printLaunchReport(d.Launch)
	attachErr := justcode.RunInteractiveEnv(justcode.OpenCodeClientEnv(cfg.Password), "opencode", openCodeAttachArgs(endpoint, resumeArgs)...)
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

func lifecycleCmd(d *justcode.Dispatcher, rt justcode.Runtime, action string) (int, error) {
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
		err = b.Logs()
	case "shell":
		err = b.Shell()
	default:
		err = fmt.Errorf("unsupported action %q", action)
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

func startWithLaunchReport(ctx context.Context, d *justcode.Dispatcher, b justcode.Backend) error {
	if d.Launch != nil && d.Launch.Terminal {
		fmt.Fprintf(os.Stderr, "Démarrage de la machine isolée pour %s…\n", d.Launch.ProjectName)
	}
	begun := launchNowFn()
	if err := justcode.StartOrReconcile(ctx, b); err != nil {
		return err
	}
	if err := d.WaitLaunchReady(ctx, b); err != nil {
		return err
	}
	d.Launch.Observe(launchNowFn().Sub(begun))
	if m, ok := b.(*justcode.MicrosandboxRuntime); ok && d.Launch != nil {
		d.RecordLaunchSkills()
		d.Launch.RecordMCP(m.OpenCodeOverlay.MCPConnectors)
	}
	return nil
}

func printLaunchReport(launch *justcode.LaunchUX) {
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
// explicit replacement is `just-code clean --<runtime>` followed by start,
// which names the instance and the loss in its own output.
func confirmDestructiveRecreate(rt justcode.Runtime) (int, error) {
	fmt.Fprintf(os.Stderr, "Warning: recreate --%s rebuilds the %s environment from scratch and DESTROYS: guest sessions, tools installed in the guest, and guest-only files.\n", rt, rt)
	if !isTTY() {
		return 1, fmt.Errorf("recreate is destructive and requires an interactive confirmation; run 'just-code clean --%s' then 'just-code start --%s' if you really want to recreate", rt, rt)
	}
	fmt.Print("Recreate and lose that state? Type the runtime name to confirm: ")
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(reply) != string(rt) {
		return 1, fmt.Errorf("not confirmed; nothing was destroyed")
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
	fmt.Println(`just-code - manage the OpenCode sandbox

Usage:
  just-code [command] [--microsandbox | --tart | --agent-vm] [--isolation backend | full]

Run just-code with no command to launch the selected runtime. On an
unconfigured host, the default is Microsandbox/full: the agent and native
OpenCode TUI run inside the guest. Backend isolation is an explicit option
that runs the guest backend and attaches the host TUI.

Commands:
  start      Start a backend without attaching the TUI
  stop       Stop the current project's instance (stop --all for every
             just-code instance)
  check      Check the active backend and Albert provider
  restart    Stop and start the current project's instance (non-destructive)
  recreate   Rebuild the selected sandbox from scratch (destructive; asks
             to confirm)
  logs       Follow logs for the selected runtime
  shell      Open a shell inside the selected runtime
  clean      Remove the selected sandbox and its local state
  doctor     Check the selected runtime installation
  config     Show or preview managed configuration (explain, import-env)
  ports      Configure Microsandbox TCP preview ports (list, add, remove)
  import     Preview or import an existing Albert Code project
  auth       Manage global credentials (add, status, remove)
  secrets    Import and manage project-scoped dotenv values (import)
  bindings   Approve or revoke optional credential bindings for this
             project (list, approve, revoke)
  trust      Approve execution-relevant project OpenCode inputs (status,
             approve)
  models     List the validated Albert models (live, or last-known-good)
  mcp        Check configured remote MCP endpoints (status)
  init       Configure this project: sharing mode, model, guest resources,
             remote MCPs and credential reference. Interactive by default;
             see 'just-code init --help' for the flag surface.
  workspace  Manage the sealed guest workspace: what crosses into the guest,
             per-file re-inclusions, refresh and reviewed export (status,
             sync, allow, deny, export). The host checkout is never mounted.
  setup      Configure the machine: preflight, credentials, identity, model,
             then install the managed runtime (setup doctor for the
             read-only diagnosis)
  update     Review and apply pinned project skill updates
  version    Print the build identity
  help       Show this help

Runtime selection:
  Microsandbox in isolation full is the built-in default: the agent runs in the
  microVM and its workspace is sealed (no host file is mounted). Pass
  --microsandbox, --tart or --agent-vm, or set RUNTIME, to override it; an
  explicit flag always takes precedence. --isolation backend selects the
  historical mode where the TUI runs on the host. Tart and agent-vm mount the
  checkout, hand the credential to the guest in plaintext, and require
  --acknowledge-guest-credentials to start.

  A .env in the working directory is NOT loaded implicitly any more; exported
  variables still are. To see what an existing .env holds and where each value
  belongs, run 'just-code config import-env <path>' (preview only). To preview
  project-scoped application secret import, run:
  'just-code secrets import --profile development --file .env'. Import does not
  enable guest delivery. The Albert
  key belongs in the credential store: 'just-code auth add albert'.

Isolation:
  --isolation full (default): the whole agent, TUI included, runs inside the
  sandbox; the host is only a terminal passthrough.
  --isolation backend: the agent runs as a server inside the sandbox and the
  TUI attaches from the host. ISOLATION is used when no flag is provided; the
  flag always takes precedence. How each runtime handles the credential in
  either mode is stated above.`)
}

// applyProjectSandboxResources applies the project manifest's guest sizing.
//
// Precedence follows the configuration contract (#29): an exported variable
// beats the project manifest, which beats the built-in default. The manifest
// is where the project setup records the answer to "how much machine does this
// project get", so a value written there must actually size the guest — a
// recorded setting nothing reads is worse than not asking.
func applyProjectSandboxResources(cfg justcode.Config, projectRoot string) (justcode.Config, error) {
	pm, err := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot))
	if err != nil {
		// A missing manifest is the normal zero-flag state. A present but
		// unreadable one is reported by the paths that own that decision.
		return cfg, nil
	}
	cfg.Ports = append([]justcode.PortMapping(nil), pm.Ports...)
	cfg.PortsConfigured = pm.PortsConfigured || pm.Ports != nil
	if cfg.PortsConfigured {
		if err := justcode.ValidatePortMappings(cfg.Ports); err != nil {
			return cfg, fmt.Errorf("invalid project port mapping: %w", err)
		}
	}
	// Zero means "not set in the manifest", which is the common case. Any
	// other out-of-range value — negative, or above what the runtime can
	// carry — is reported rather than ignored: a hand-edited manifest should
	// fail loudly on both sides of the range, not just above it.
	if value, set := os.LookupEnv("JUST_CODE_CPUS"); !set || value == "" {
		switch {
		case pm.CPUs == 0:
		case pm.CPUs < 0 || pm.CPUs > justcode.MaxSandboxCPUs:
			return cfg, fmt.Errorf("the project manifest asks for %d CPUs; the sandbox runtime accepts 1 to %d", pm.CPUs, justcode.MaxSandboxCPUs)
		default:
			cfg.CPUs = pm.CPUs
		}
	}
	if value, set := os.LookupEnv("JUST_CODE_MEMORY_MB"); !set || value == "" {
		switch {
		case pm.MemoryMB == 0:
		case pm.MemoryMB < 0 || pm.MemoryMB > justcode.MaxSandboxMemoryMB:
			return cfg, fmt.Errorf("the project manifest asks for %d MiB; the sandbox runtime accepts 1 to %d", pm.MemoryMB, justcode.MaxSandboxMemoryMB)
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
func enforceHostResourceLimits(cfg justcode.Config, projectRoot string, reports ...*justcode.LaunchUX) (justcode.Config, error) {
	host, err := detectHostResourcesFn()
	if err != nil {
		return cfg, fmt.Errorf("cannot determine host CPU and memory capacity; cannot enforce the guest resource limit: %v", err)
	}
	if host.CPUs <= 0 || host.MemoryMB <= 0 {
		return cfg, fmt.Errorf("host resource detection returned incomplete CPU or memory capacity; cannot enforce the guest resource limit")
	}
	pm, manifestErr := justcode.ReadProjectManifest(justcode.DefaultFS, justcode.ProjectManifestPath(projectRoot))
	manifestPresent := manifestErr == nil
	if manifestErr != nil && !os.IsNotExist(manifestErr) {
		return cfg, fmt.Errorf("cannot inspect project resource settings: %w", manifestErr)
	}
	cpuExplicit := false
	if value, ok := os.LookupEnv("JUST_CODE_CPUS"); ok && value != "" {
		cpuExplicit = true
	} else if manifestPresent && pm.CPUs != 0 {
		cpuExplicit = true
	}
	memoryExplicit := false
	if value, ok := os.LookupEnv("JUST_CODE_MEMORY_MB"); ok && value != "" {
		memoryExplicit = true
	} else if manifestPresent && pm.MemoryMB != 0 {
		memoryExplicit = true
	}
	maxCPUs := justcode.MaxGuestCPUs(host.CPUs)
	maxMemoryMB := justcode.MaxGuestMemoryMB(host.MemoryMB)
	if maxCPUs < 1 {
		return cfg, fmt.Errorf("host resource detection reported no logical CPUs available for the guest")
	}
	if maxMemoryMB < 1 {
		return cfg, fmt.Errorf("host memory is too small to allocate guest memory")
	}
	if !cpuExplicit && cfg.CPUs > justcode.RecommendedDefaultGuestCPUs(host.CPUs) {
		cfg.CPUs = justcode.RecommendedDefaultGuestCPUs(host.CPUs)
	}
	if !memoryExplicit && cfg.MemoryMB > justcode.RecommendedDefaultGuestMemoryMB(host.MemoryMB) {
		cfg.MemoryMB = justcode.RecommendedDefaultGuestMemoryMB(host.MemoryMB)
	}
	if err := justcode.ValidateGuestResources(host, cfg.CPUs, cfg.MemoryMB); err != nil {
		return cfg, err
	}
	for _, warning := range justcode.GuestResourceWarnings(host, cfg.CPUs, cfg.MemoryMB) {
		if len(reports) > 0 && reports[0] != nil && reports[0].Terminal {
			reports[0].AddWarning("ressources de la machine hôte limitées ; détail : just-code doctor")
		} else {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
		}
	}
	return cfg, nil
}
