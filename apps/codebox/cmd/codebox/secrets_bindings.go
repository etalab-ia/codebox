package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// `codebox secrets bindings ...` and `codebox secrets profile ...` (Phase
// 2): the host-local approvals that decide which imported project secrets the
// Microsandbox proxy may expose, and to which destinations. Approving a
// destination is the security decision, so it is recorded on this host and
// never in the project.

type projectSecretBindingsOptions struct {
	Root      string
	Profile   string
	Component string
	Name      string
	GuestEnv  string
	Destin    []string
	// Delivery is "" for the default (proxy) or "--delivery guest" for a
	// value the guest reads locally.
	Delivery   string
	Clear      bool
	Fallback   bool
	ConfirmYes bool
}

func parseProjectSecretBindingsArgs(args []string) (projectSecretBindingsOptions, error) {
	opts := projectSecretBindingsOptions{Root: "."}
	seen := map[string]bool{}
	positional := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--root", "--profile", "--component", "--name", "--guest-env", "--destination", "--delivery":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return opts, fmt.Errorf("%s requires a value", a)
			}
			i++
			value := args[i]
			switch a {
			case "--root":
				opts.Root = value
			case "--profile":
				opts.Profile = value
			case "--component":
				opts.Component = value
			case "--name":
				opts.Name = value
			case "--guest-env":
				opts.GuestEnv = value
			case "--destination":
				opts.Destin = append(opts.Destin, value)
			case "--delivery":
				if value != codebox.ProjectSecretDeliveryProxy && value != codebox.ProjectSecretDeliveryGuest {
					return opts, fmt.Errorf("--delivery must be %q or %q", codebox.ProjectSecretDeliveryProxy, codebox.ProjectSecretDeliveryGuest)
				}
				opts.Delivery = value
			}
			seen[a] = true
		case "--clear", "--fallback", "--yes":
			if seen[a] {
				return opts, fmt.Errorf("%s may be given only once", a)
			}
			seen[a] = true
			switch a {
			case "--clear":
				opts.Clear = true
			case "--fallback":
				opts.Fallback = true
			case "--yes":
				opts.ConfirmYes = true
			}
		default:
			if strings.HasPrefix(a, "-") {
				return opts, fmt.Errorf("unknown secrets bindings option %q", a)
			}
			positional = append(positional, a)
		}
	}
	// The component and variable may be given positionally, which reads better
	// for the common case: `bindings approve development ".env" API_KEY`.
	if len(positional) > 0 && opts.Profile == "" {
		opts.Profile = positional[0]
		positional = positional[1:]
	}
	if len(positional) > 0 && opts.Component == "" {
		opts.Component = positional[0]
		positional = positional[1:]
	}
	if len(positional) > 0 && opts.Name == "" {
		opts.Name = positional[0]
		positional = positional[1:]
	}
	if len(positional) > 0 {
		return opts, fmt.Errorf("unexpected argument %q", positional[0])
	}
	return opts, nil
}

// projectSecretStoreForCLI resolves the store for the current project, using
// the same credential backend the import wrote to. The fallback store is
// selected only with the explicit --fallback flag, exactly like `auth` — but a
// store kind the project record already names wins over both, so `bindings
// list` reports what was actually imported instead of what a fresh probe would
// have chosen.
func projectSecretStoreForCLI(root string, fallback bool) (*codebox.ProjectSecretStore, error) {
	project, err := codebox.DiscoverProject(root)
	if err != nil {
		return nil, err
	}
	store, err := projectSecretsCredentialStoreFn(fallback)
	if err != nil {
		// The requested store could not be built — for instance --fallback on a
		// host with no usable config directory. Proceed only when the project
		// record names a store this host can actually resolve; otherwise the
		// original failure must survive, or the command would silently run
		// against a store the user did not ask for, and could mutate metadata
		// there.
		recorded, recErr := codebox.RecordedCredentialStore(project.Root, projectSecretsStateDirFn())
		if recErr != nil || recorded == nil {
			return nil, err
		}
		store = recorded
	} else if store, err = codebox.PreferRecordedCredentialStore(store, project.Root, projectSecretsStateDirFn()); err != nil {
		// The recorded kind wins. On a headless host the native adapter
		// resolves without error but never held the imported values, so
		// consulting the flag alone is exactly the mismatch this fixes.
		return nil, err
	}
	return projectSecretsStoreFactoryFn(project.Root, projectSecretsStateDirFn(), store)
}

func secretsProfileCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	if opts.Clear && opts.Profile != "" {
		return 2, fmt.Errorf("give either a profile name or --clear, not both")
	}
	store, err := projectSecretStoreForCLI(opts.Root, opts.Fallback)
	if err != nil {
		return 1, err
	}
	if err := codebox.SetProjectSecretActiveProfile(codebox.DefaultFS, projectSecretsStateDirFn(), store.Namespace(), opts.Profile); err != nil {
		return 1, err
	}
	if opts.Clear {
		fmt.Println("Cleared the active project-secret profile. No project secret is delivered until a profile is selected again.")
		return 0, nil
	}
	fmt.Printf("Active project-secret profile for %s: %s.\n", store.Root(), opts.Profile)
	fmt.Println("Approved bindings for this profile are delivered at the next start.")
	return 0, nil
}

func secretsBindingsCmd(args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: codebox secrets bindings <list|approve|revoke> [--root PATH] [--profile NAME] [--fallback]")
		fmt.Fprintln(os.Stderr, "  --fallback selects the owner-only file store, for hosts with no usable native store (headless Linux without a Secret Service session)")
		fmt.Fprintln(os.Stderr, "  approve asks for confirmation unless --yes is given; a non-interactive approval requires --yes")
		return 2, nil
	}
	switch args[0] {
	case "list":
		return secretsBindingsListCmd(args[1:])
	case "approve":
		return secretsBindingsApproveCmd(args[1:])
	case "revoke":
		return secretsBindingsRevokeCmd(args[1:])
	default:
		return 2, fmt.Errorf("unknown secrets bindings command %q (expected list, approve or revoke)", args[0])
	}
}

func secretsBindingsListCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	store, err := projectSecretStoreForCLI(opts.Root, opts.Fallback)
	if err != nil {
		return 1, err
	}
	record, err := codebox.ReadProjectSecretBindingsRecordFor(store)
	if err != nil {
		return 1, err
	}
	profile := record.Profile
	if profile == "" {
		profile = "(none selected)"
	}
	fmt.Printf("Project secret bindings for %s (profile: %s)\n", store.Root(), profile)
	if len(record.Bindings) == 0 {
		fmt.Println("  No approved binding.")
	} else {
		guestReadable := 0
		for _, b := range record.Bindings {
			if b.GuestReadable() {
				guestReadable++
				fmt.Printf("  %s#%s -> READABLE INSIDE THE SANDBOX (written to %s)\n", b.Component, b.Name, b.Component)
				fmt.Printf("      the real value is delivered as a file; the agent and anything it runs can read it\n")
				continue
			}
			fmt.Printf("  %s#%s -> %s (proxy-protected)\n", b.Component, b.Name, b.GuestEnv)
			fmt.Printf("      destinations: %s (host-local approval, never versioned)\n", strings.Join(b.Destinations, ", "))
		}
		if guestReadable > 0 {
			fmt.Printf("  %d binding(s) deliver a real value inside the sandbox; a snapshot taken while they are delivered captures them.\n", guestReadable)
		}
	}
	if record.Pending != "" {
		fmt.Printf("  NOT DELIVERABLE: %s\n", record.Pending)
	}
	if len(record.Undelivered) > 0 {
		sort.Strings(record.Undelivered)
		fmt.Printf("  Imported but not delivered: %s\n", strings.Join(record.Undelivered, ", "))
		fmt.Println("  Approve one with: codebox secrets bindings approve <profile> <component> <NAME> --destination <host>")
	}
	fmt.Println("Values are never shown here, and approving a binding does not display or copy any value.")
	return 0, nil
}

func secretsBindingsApproveCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	if opts.Profile == "" || opts.Component == "" || opts.Name == "" {
		return 2, fmt.Errorf("usage: codebox secrets bindings approve <profile> <component> <NAME> " +
			"[--destination <host> ...] [--guest-env NAME] [--delivery proxy|guest]")
	}
	guestReadable := opts.Delivery == codebox.ProjectSecretDeliveryGuest
	if !guestReadable && len(opts.Destin) == 0 {
		return 2, fmt.Errorf("at least one --destination is required: approving a destination is what allows the value to be substituted toward it")
	}
	if guestReadable && len(opts.Destin) > 0 {
		return 2, fmt.Errorf("--delivery guest takes no --destination: the value is read inside the sandbox, not substituted toward a host")
	}
	if guestReadable && opts.GuestEnv != "" {
		return 2, fmt.Errorf("--delivery guest takes no --guest-env: the value is delivered as a file, not as an environment placeholder")
	}
	// Confirmation was declared by --yes but never read, so the flag promised
	// a prompt that did not exist — including for --delivery guest, the action
	// that makes a real value readable inside the sandbox. The import path
	// already requires --yes for a non-interactive write; approval now follows
	// the same rule.
	//
	// The gate runs before the store is built and before any value is read: an
	// invocation that must be refused as invalid automation should not touch
	// the credential store, unlock a collection, or fail with an unrelated
	// lookup error instead of the --yes requirement.
	if !opts.ConfirmYes && !projectSecretsTTYFn() {
		return 2, fmt.Errorf("non-interactive approval requires explicit --yes")
	}
	store, err := projectSecretStoreForCLI(opts.Root, opts.Fallback)
	if err != nil {
		return 1, err
	}
	stateDir := projectSecretsStateDirFn()
	binding := codebox.ProjectSecretBinding{
		Profile:      opts.Profile,
		Component:    opts.Component,
		Name:         opts.Name,
		Delivery:     opts.Delivery,
		GuestEnv:     opts.GuestEnv,
		Destinations: opts.Destin,
	}
	remoteProject := projectDeclaresSharedSecrets(opts.Root)
	if binding.GuestEnv == "" && !guestReadable {
		binding.GuestEnv = codebox.ProjectSecretGuestEnv(binding.Component, binding.Name)
	}
	// A remote project has no local imported value. Host-local approval only
	// records delivery metadata; the resolved gateway bundle is checked when a
	// guest-readable file is actually rendered at launch.
	if guestReadable && !remoteProject {
		// Fail here rather than at the next start: whether a value can be
		// written into a dotenv file exactly is a property of the value, and
		// the user is present now. A value that no encoding carries would
		// otherwise refuse every start until it is withdrawn.
		value, err := store.Get(context.Background(), binding.Profile, binding.Component, binding.Name)
		if err != nil {
			return 1, fmt.Errorf("cannot read %s (%s) to check that it can be delivered: %w", binding.Name, binding.Component, err)
		}
		if err := codebox.CheckGuestReadableValue(value); err != nil {
			return 1, fmt.Errorf("%s (%s) cannot be delivered as a guest-readable dotenv value: %w\n"+
				"Use the default proxy delivery if the application reaches the service over the network, or wait for standalone file delivery for this one.",
				binding.Name, binding.Component, err)
		}
	}
	if guestReadable {
		fmt.Printf("Approving %s#%s for %s as GUEST-READABLE (written to %s)\n",
			binding.Component, binding.Name, binding.Profile, binding.Component)
		fmt.Println("This is not the protected mode. The real value is written into the sandbox so an application can read it locally, which means:")
		fmt.Println("  - everything running in that sandbox, including the agent, can read the value;")
		fmt.Println("  - it stays readable for as long as the sandbox holds it;")
		fmt.Println("  - a snapshot taken while it is delivered captures it, and deleting it later does not clean that snapshot.")
		fmt.Println("Use this only for values an application must consume locally (a database password, a signing key). Protect anything reached over the network with the default delivery instead.")
	} else {
		fmt.Printf("Approving %s#%s for %s -> guest %s, destinations: %s\n",
			binding.Component, binding.Name, binding.Profile, binding.GuestEnv, strings.Join(binding.Destinations, ", "))
		fmt.Println("The value stays in the credential store; only its placeholder enters the guest, and it is substituted toward those destinations alone.")
	}
	// Validate before writing. Approving used to record the binding and switch
	// the active profile first, then check that it resolves — so a binding that
	// could not resolve (an unimported variable, a profile mismatch) left a
	// phantom approval and a changed active profile behind on the exit-1 path,
	// and `bindings list` then showed state the user never successfully
	// created. Nothing is written until the resolution question is answered.
	if !remoteProject {
		if err := store.ValidateBindingAgainst(binding, opts.Profile); err != nil {
			return 1, fmt.Errorf("this approval would not resolve, so nothing was recorded: %w", err)
		}
	} else if err := validateRemoteBindingAgainstManifest(opts.Root, binding); err != nil {
		return 1, fmt.Errorf("this remote approval does not match the project declaration, so nothing was recorded: %w", err)
	}
	// An approval already in effect is reported as such: approvals are
	// upserted, so declining a re-run that only changes a destination or the
	// delivery mode leaves the previous approval active. Saying nothing was
	// recorded could read as "no sandbox access". A read failure is not fatal:
	// the prompt is a safety net, not a gate, and the approval itself still
	// validates — it only means this note cannot be shown.
	replacesExisting := false
	if record, rerr := codebox.ReadProjectSecretBindingsRecordFor(store); rerr == nil {
		for _, existing := range record.Bindings {
			if existing.Profile == binding.Profile && existing.Component == binding.Component && existing.Name == binding.Name {
				replacesExisting = true
				break
			}
		}
	}
	if !opts.ConfirmYes {
		description := "The value stays in the credential store; this records which guest variable or file receives it."
		if guestReadable {
			description = "This makes the real value readable inside the sandbox, where any process including the agent can read it."
		}
		confirm, err := projectSecretsConfirmFn("Approve this binding?", description, false)
		if err != nil {
			return 1, err
		}
		if !confirm {
			fmt.Println("Cancelled. No approval recorded.")
			if replacesExisting {
				fmt.Println("Note: the previous approval for this binding is unchanged and remains in effect.")
			}
			return 0, nil
		}
	}
	// Record the approval first: it validates the binding, and a rejected
	// approval must not leave the project pointed at a different profile.
	if err := codebox.ApproveProjectSecretBinding(codebox.DefaultFS, stateDir, store.Namespace(), binding); err != nil {
		return 1, err
	}
	if err := codebox.SetProjectSecretActiveProfile(codebox.DefaultFS, stateDir, store.Namespace(), opts.Profile); err != nil {
		return 1, err
	}
	// Re-check what was actually persisted. The pre-write validation used an
	// in-memory candidate; this one reads the record back, so a failure here
	// means the write itself did not land as intended and must not be reported
	// as a working approval.
	if _, _, err := store.ApprovedDeliveries(); err != nil {
		return 1, fmt.Errorf("the approval was recorded but does not resolve: %w", err)
	}
	fmt.Println("Approved. It takes effect at the next start of this project's sandbox.")
	return 0, nil
}

func secretsBindingsRevokeCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	if opts.Profile == "" || opts.Component == "" || opts.Name == "" {
		return 2, fmt.Errorf("usage: codebox secrets bindings revoke <profile> <component> <NAME>")
	}
	store, err := projectSecretStoreForCLI(opts.Root, opts.Fallback)
	if err != nil {
		return 1, err
	}
	stateDir := projectSecretsStateDirFn()
	// Read the binding before removing it: the consequence of a revocation is
	// not the same in the two modes, and the plaintext mode is the one where a
	// wrong message is dangerous.
	wasGuestReadable := false
	if record, err := codebox.ReadProjectSecretBindingsRecordFor(store); err == nil {
		for _, b := range record.Bindings {
			if b.Profile == opts.Profile && b.Component == opts.Component && b.Name == opts.Name {
				wasGuestReadable = b.GuestReadable()
			}
		}
	}
	removed, err := codebox.RevokeProjectSecretBinding(codebox.DefaultFS, stateDir, store.Namespace(), opts.Profile, opts.Component, opts.Name)
	if err != nil {
		return 1, err
	}
	if !removed {
		fmt.Printf("No approval matched %s#%s for profile %s; nothing changed.\n", opts.Component, opts.Name, opts.Profile)
		return 0, nil
	}
	fmt.Printf("Revoked the %s#%s project secret binding. The imported value stays in the credential store.\n", opts.Component, opts.Name)
	if wasGuestReadable {
		fmt.Println("The real value is still readable inside the running sandbox until the next start applies the revocation. Run 'codebox start' (it reconciles the delivery) or 'codebox restart'.")
		fmt.Println("A snapshot taken while it was delivered still holds it; revoking here cannot reach that copy.")
	} else {
		fmt.Println("Run 'codebox restart' (or 'codebox start') to unregister it from the running sandbox: the proxy registration is dropped at the next apply, and the guest keeps an inert placeholder until it restarts.")
	}
	return 0, nil
}
