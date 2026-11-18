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
				return opts, fmt.Errorf("%s exige une valeur", a)
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
					return opts, fmt.Errorf("--delivery doit valoir %q ou %q", codebox.ProjectSecretDeliveryProxy, codebox.ProjectSecretDeliveryGuest)
				}
				opts.Delivery = value
			}
			seen[a] = true
		case "--clear", "--fallback", "--yes":
			if seen[a] {
				return opts, fmt.Errorf("%s ne peut être indiqué qu'une fois", a)
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
				return opts, fmt.Errorf("option inconnue de secrets bindings %q", a)
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
		return opts, fmt.Errorf("argument inattendu %q", positional[0])
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
		return 2, fmt.Errorf("indiquez soit un nom de profil, soit --clear, mais pas les deux")
	}
	store, err := projectSecretStoreForCLI(opts.Root, opts.Fallback)
	if err != nil {
		return 1, err
	}
	if err := codebox.SetProjectSecretActiveProfile(codebox.DefaultFS, projectSecretsStateDirFn(), store.Namespace(), opts.Profile); err != nil {
		return 1, err
	}
	if opts.Clear {
		fmt.Println("Profil de secrets de projet actif effacé. Aucun secret de projet n'est livré tant qu'un profil n'est pas sélectionné à nouveau.")
		return 0, nil
	}
	fmt.Printf("Profil de secrets de projet actif pour %s : %s.\n", store.Root(), opts.Profile)
	fmt.Println("Les liaisons approuvées pour ce profil sont livrées au prochain démarrage.")
	return 0, nil
}

func secretsBindingsCmd(args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage : codebox secrets bindings <list|approve|revoke> [--root PATH] [--profile NAME] [--fallback]")
		fmt.Fprintln(os.Stderr, "  --fallback sélectionne le magasin de fichiers réservé à votre utilisateur, pour les postes sans magasin natif utilisable (Linux sans session, sans Secret Service)")
		fmt.Fprintln(os.Stderr, "  approve demande confirmation sauf avec --yes ; une approbation non interactive exige --yes")
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
		return 2, fmt.Errorf("commande inconnue de secrets bindings %q (attendu : list, approve ou revoke)", args[0])
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
		profile = "(aucun sélectionné)"
	}
	fmt.Printf("Liaisons de secrets de projet pour %s (profil : %s)\n", store.Root(), profile)
	if len(record.Bindings) == 0 {
		fmt.Println("  Aucune liaison approuvée.")
	} else {
		guestReadable := 0
		for _, b := range record.Bindings {
			if b.GuestReadable() {
				guestReadable++
				fmt.Printf("  %s#%s -> LISIBLE DANS LA MACHINE ISOLÉE (écrit dans %s)\n", b.Component, b.Name, b.Component)
				fmt.Printf("      la valeur réelle est livrée sous forme de fichier ; l'agent et tout ce qu'il exécute peuvent la lire\n")
				continue
			}
			fmt.Printf("  %s#%s -> %s (protégé par le proxy)\n", b.Component, b.Name, b.GuestEnv)
			fmt.Printf("      destinations : %s (approbation locale au poste, jamais versionnée)\n", strings.Join(b.Destinations, ", "))
		}
		if guestReadable > 0 {
			fmt.Printf("  %d liaison(s) livrent une valeur réelle dans la machine isolée ; un instantané pris pendant la livraison les capture.\n", guestReadable)
		}
	}
	if record.Pending != "" {
		fmt.Printf("  NON LIVRABLE : %s\n", record.Pending)
	}
	if len(record.Undelivered) > 0 {
		sort.Strings(record.Undelivered)
		fmt.Printf("  Importés mais non livrés : %s\n", strings.Join(record.Undelivered, ", "))
		fmt.Println("  Approuvez-en une avec : codebox secrets bindings approve <profile> <component> <NAME> --destination <host>")
	}
	fmt.Println("Aucune valeur n'est jamais affichée ici, et approuver une liaison n'affiche ni ne copie aucune valeur.")
	return 0, nil
}

func secretsBindingsApproveCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	if opts.Profile == "" || opts.Component == "" || opts.Name == "" {
		return 2, fmt.Errorf("usage : codebox secrets bindings approve <profile> <component> <NAME> " +
			"[--destination <host> ...] [--guest-env NAME] [--delivery proxy|guest]")
	}
	guestReadable := opts.Delivery == codebox.ProjectSecretDeliveryGuest
	if !guestReadable && len(opts.Destin) == 0 {
		return 2, fmt.Errorf("au moins une --destination est requise : approuver une destination est ce qui autorise la substitution de la valeur vers celle-ci")
	}
	if guestReadable && len(opts.Destin) > 0 {
		return 2, fmt.Errorf("--delivery guest n'accepte pas --destination : la valeur est lue dans la machine isolée, elle n'est pas substituée vers un poste")
	}
	if guestReadable && opts.GuestEnv != "" {
		return 2, fmt.Errorf("--delivery guest n'accepte pas --guest-env : la valeur est livrée sous forme de fichier, pas comme marqueur d'environnement")
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
		return 2, fmt.Errorf("l'approbation non interactive exige --yes explicitement")
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
			return 1, fmt.Errorf("impossible de lire %s (%s) pour vérifier qu'elle peut être livrée : %w", binding.Name, binding.Component, err)
		}
		if err := codebox.CheckGuestReadableValue(value); err != nil {
			return 1, fmt.Errorf("%s (%s) ne peut pas être livrée comme valeur dotenv lisible dans la machine isolée : %w\n"+
				"Utilisez la livraison proxy par défaut si l'application joint le service par le réseau, ou attendez la livraison en fichier autonome pour celle-ci.",
				binding.Name, binding.Component, err)
		}
	}
	if guestReadable {
		fmt.Printf("Approbation de %s#%s pour %s en LISIBLE DANS LA MACHINE ISOLÉE (écrit dans %s)\n",
			binding.Component, binding.Name, binding.Profile, binding.Component)
		fmt.Println("Ce n'est pas le mode protégé. La valeur réelle est écrite dans la machine isolée pour qu'une application puisse la lire localement, ce qui signifie :")
		fmt.Println("  - tout ce qui tourne dans cette machine isolée, y compris l'agent, peut lire la valeur ;")
		fmt.Println("  - elle reste lisible aussi longtemps que la machine isolée la détient ;")
		fmt.Println("  - un instantané pris pendant qu'elle est livrée la capture, et la supprimer ensuite ne nettoie pas cet instantané.")
		fmt.Println("Réservez ce mode aux valeurs qu'une application doit consommer localement (un mot de passe de base de données, une clé de signature). Protégez plutôt par la livraison par défaut tout ce qui est joint par le réseau.")
	} else {
		fmt.Printf("Approbation de %s#%s pour %s -> variable invitée %s, destinations : %s\n",
			binding.Component, binding.Name, binding.Profile, binding.GuestEnv, strings.Join(binding.Destinations, ", "))
		fmt.Println("La valeur reste dans le trousseau ; seul son marqueur entre dans la machine isolée, et il n'est substitué que vers ces destinations.")
	}
	// Validate before writing. Approving used to record the binding and switch
	// the active profile first, then check that it resolves — so a binding that
	// could not resolve (an unimported variable, a profile mismatch) left a
	// phantom approval and a changed active profile behind on the exit-1 path,
	// and `bindings list` then showed state the user never successfully
	// created. Nothing is written until the resolution question is answered.
	if !remoteProject {
		if err := store.ValidateBindingAgainst(binding, opts.Profile); err != nil {
			return 1, fmt.Errorf("cette approbation ne se résoudrait pas, donc rien n'a été enregistré : %w", err)
		}
	} else if err := validateRemoteBindingAgainstManifest(opts.Root, binding); err != nil {
		return 1, fmt.Errorf("cette approbation distante ne correspond pas à la déclaration du projet, donc rien n'a été enregistré : %w", err)
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
		description := "La valeur reste dans le trousseau ; cela enregistre quelle variable invitée ou quel fichier la reçoit."
		if guestReadable {
			description = "Cela rend la valeur réelle lisible dans la machine isolée, où tout processus, y compris l'agent, peut la lire."
		}
		confirm, err := projectSecretsConfirmFn("Approuver cette liaison ?", description, false)
		if err != nil {
			return 1, err
		}
		if !confirm {
			fmt.Println("Annulé. Aucune approbation enregistrée.")
			if replacesExisting {
				fmt.Println("Remarque : l'approbation précédente de cette liaison est inchangée et reste en vigueur.")
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
		return 1, fmt.Errorf("l'approbation a été enregistrée mais ne se résout pas : %w", err)
	}
	fmt.Println("Approuvé. Cela prend effet au prochain démarrage de la machine isolée de ce projet.")
	return 0, nil
}

func secretsBindingsRevokeCmd(args []string) (int, error) {
	opts, err := parseProjectSecretBindingsArgs(args)
	if err != nil {
		return 2, err
	}
	if opts.Profile == "" || opts.Component == "" || opts.Name == "" {
		return 2, fmt.Errorf("usage : codebox secrets bindings revoke <profile> <component> <NAME>")
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
		fmt.Printf("Aucune approbation ne correspond à %s#%s pour le profil %s ; rien n'a changé.\n", opts.Component, opts.Name, opts.Profile)
		return 0, nil
	}
	fmt.Printf("Liaison de secret de projet %s#%s révoquée. La valeur importée reste dans le trousseau.\n", opts.Component, opts.Name)
	if wasGuestReadable {
		fmt.Println("La valeur réelle reste lisible dans la machine isolée en cours jusqu'à ce que le prochain démarrage applique la révocation. Lancez 'codebox start' (il réconcilie la livraison) ou 'codebox restart'.")
		fmt.Println("Un instantané pris pendant qu'elle était livrée la détient toujours ; révoquer ici ne peut pas atteindre cette copie.")
	} else {
		fmt.Println("Lancez 'codebox restart' (ou 'codebox start') pour la désinscrire de la machine isolée en cours : l'inscription proxy est retirée à la prochaine application, et la machine isolée conserve un marqueur inerte jusqu'à son redémarrage.")
	}
	return 0, nil
}
