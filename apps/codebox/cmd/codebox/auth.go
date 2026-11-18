package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

// authCmd implements `codebox auth <subcommand>` (P08): global credentials
// stored in the OS-native store (macOS Keychain, Windows Credential
// Manager, Linux Secret Service), with an owner-only file fallback that
// requires explicit consent. The secret is never accepted through argv,
// never printed, and never written to a project file.
//
//	auth add [albert|mistral|openai-compatible|github|context7] [--stdin] [--fallback]
//	auth status
//	auth remove [albert] [--fallback]
//	auth use-fallback [albert]
func authCmd(args []string) (int, error) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage : codebox auth add|status|remove [kind] [--stdin] [--fallback] | auth use-fallback [kind]")
		return 2, nil
	}
	switch args[0] {
	case "add":
		return authAddCmd(args[1:])
	case "status":
		return authStatusCmd()
	case "remove":
		return authRemoveCmd(args[1:])
	case "use-fallback":
		return authUseFallbackCmd(args[1:])
	default:
		return 2, fmt.Errorf("commande auth inconnue : %s (attendu : add, status, remove ou use-fallback)", args[0])
	}
}

// parseAuthArgs returns the credential kind, the --stdin flag and the
// --fallback flag. The kind defaults to albert; the known optional kinds
// (github, context7) are accepted, and unknown kinds are rejected so a typo
// does not store a credential nothing will ever read. A credentialRef may
// still name an arbitrary store entry — the CLI manages the known kinds.
func parseAuthArgs(args []string) (codebox.CredentialKind, bool, bool, error) {
	kind := codebox.CredentialAlbert
	stdin, fallback := false, false
	for _, a := range args {
		switch a {
		case "--stdin":
			stdin = true
		case "--fallback":
			fallback = true
		default:
			if strings.HasPrefix(a, "-") {
				return kind, stdin, fallback, fmt.Errorf("type d'identifiant ou option inconnu : %s (attendu : albert, mistral, openai-compatible, github, context7, --stdin ou --fallback)", a)
			}
			if !codebox.IsKnownCredentialKind(a) {
				return kind, stdin, fallback, fmt.Errorf("type d'identifiant inconnu : %s (attendu : albert, mistral, openai-compatible, github ou context7)", a)
			}
			kind = codebox.CredentialKind(a)
		}
	}
	return kind, stdin, fallback, nil
}

// authStore resolves the store for an operation. Without --fallback it is
// the native store; with --fallback it is the consented file store. A nil
// native store (unsupported platform) is an explicit error, not a silent
// fallback.
func authStore(fallback bool) (codebox.CredentialStore, error) {
	if fallback {
		return codebox.ConsentFileStore()
	}
	s := codebox.DefaultCredentialStore()
	if s == nil {
		return nil, fmt.Errorf("aucun trousseau natif sur cette plateforme ; indiquez --fallback pour utiliser le magasin de fichiers réservé au propriétaire après consentement")
	}
	return s, nil
}

// readSecret obtains the secret without echoing it. Interactively it
// reads a masked line (the terminal is put in no-echo mode when
// supported); with --stdin it reads one line from standard input, the
// documented noninteractive path. argv is never a source.
func readSecret(stdin bool) (string, error) {
	if stdin || !isTTY() {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("lire le secret depuis l'entrée standard : %w", err)
		}
		return strings.TrimSpace(line), nil
	}
	// Interactive: no-echo when the terminal supports it. The no-echo
	// helper is best-effort; a terminal that cannot be configured still
	// reads a line, and the user is warned on the line above.
	if wizardHuhFn() {
		value, err := huhSecret("Secret", "Saisie masquée. Le secret est enregistré dans le trousseau natif du système.")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(value), nil
	}
	fmt.Print("Saisissez le secret (saisie masquée lorsque le terminal le permet) : ")
	value, err := readPassword()
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func authAddCmd(args []string) (int, error) {
	kind, useStdin, fallback, err := parseAuthArgs(args)
	if err != nil {
		return 2, err
	}
	if !useStdin && isTTY() && !stdoutIsTTY() {
		return 1, fmt.Errorf("auth add interactif exige que la sortie standard soit un terminal ; elle est redirigée ou pointe vers %s", os.DevNull)
	}
	store, err := authStore(fallback)
	if err != nil {
		return 1, err
	}
	// Probe the store first: an unavailable or locked store must fail
	// BEFORE the user types the secret. An absent file store is the one
	// expected first-run state when --fallback explicitly consents to create
	// it on the first non-empty Put; Verify itself remains side-effect-free.
	if err := store.Verify(context.Background()); err != nil &&
		!(fallback && errors.Is(err, codebox.ErrFileStoreAbsent)) {
		return 1, err
	}
	value, err := readSecret(useStdin)
	if err != nil {
		return 1, err
	}
	if value == "" {
		return 1, fmt.Errorf("secret vide ; rien n'a été enregistré")
	}
	if err := store.Put(context.Background(), kind, value); err != nil {
		return 1, err
	}
	// Bump the rotation marker after a successful write: a value change on a
	// running instance must schedule a refresh, not be swallowed by the
	// no-op path.
	codebox.BumpCredentialGeneration(kind)
	// Record which store received the value. A later read honours it, so the
	// user's explicit --fallback choice survives into every start even on a
	// host whose native adapter exists but cannot answer. The write also
	// repoints the record: storing natively after a fallback write must not
	// leave reads pinned to the older file value.
	if err := codebox.RecordCredentialStore(codebox.DefaultStateDir(), kind, store.Kind()); err != nil {
		// The value is stored but the choice is not, so the next start would
		// fall back to the native-first chain and — on a host without a D-Bus
		// session — fail to reach the credential just written. Reporting
		// success here would be exactly the false reassurance #201 is about.
		return 1, fmt.Errorf("identifiant %q enregistré dans le magasin %s, mais ce choix n'a pas pu être consigné (%w) ; le prochain démarrage pourrait chercher dans le magasin natif à la place", kind, store.Kind(), err)
	}
	fmt.Printf("Identifiant %q enregistré dans le magasin %s. Il est référencé par son nom (credentialRef), jamais écrit dans un fichier de projet.\n", kind, store.Kind())
	return 0, nil
}

// authUseFallbackCmd records that an already-stored credential lives in the
// consented file store, without asking for the value again.
//
// This exists for hosts that stored with `--fallback` before the store choice
// was recorded: the value is already on disk and correct, and asking the user to
// re-enter a secret they cannot read back would be the only alternative. It
// verifies the claim before recording it — a pin to a store that does not hold
// the kind would turn a working default chain into a hard not-found.
func authUseFallbackCmd(args []string) (int, error) {
	kind, _, _, err := parseAuthArgs(args)
	if err != nil {
		return 2, err
	}
	store, err := codebox.NewFileCredentialStore()
	if err != nil {
		return 1, err
	}
	if !store.HasCredential(kind) {
		return 1, fmt.Errorf("aucun identifiant %q dans le magasin de fichiers : enregistrez-en un d'abord avec 'codebox auth add %s --fallback'", kind, kind)
	}
	if err := codebox.RecordCredentialStore(codebox.DefaultStateDir(), kind, store.Kind()); err != nil {
		return 1, err
	}
	fmt.Printf("Enregistré que %q se trouve dans le magasin %s. Les prochains démarrages le liront là ; la valeur n'est ni lue ni modifiée par cette commande.\n", kind, store.Kind())
	return 0, nil
}

func authStatusCmd() (int, error) {
	ctx := context.Background()
	// Printed last, from every path: which store a start will read each kind
	// from is the recorded choice, not a probe. Saying so is what lets a
	// headless host trust that `start` uses the value it stored with
	// --fallback.
	defer func() {
		for _, kind := range codebox.KnownCredentialKinds() {
			recorded := codebox.RecordedCredentialStoreForKind(codebox.DefaultStateDir(), kind)
			if recorded == "" {
				continue
			}
			if store, definite := codebox.CredentialStoreForRead(codebox.DefaultStateDir(), kind); definite {
				fmt.Printf("%s : lu depuis le magasin %s\n", kind, store)
				continue
			}
			// A native record is where the value was last written, not a pin:
			// resolution still falls through to the file when the native entry
			// is gone, so promising the native store would contradict `start`.
			fmt.Printf("%s : dernière écriture dans le magasin %s (un démarrage se rabat sur le magasin de fichiers s'il y est absent)\n", kind, recorded)
		}
	}()
	native := codebox.DefaultCredentialStore()
	fmt.Printf("Trousseau natif : %s\n", codebox.StoreAvailability())
	if native != nil {
		if err := native.Verify(ctx); err != nil {
			var se *codebox.StoreError
			if errors.As(err, &se) {
				fmt.Printf("  état : %s (%s)\n", se.State, se.Err)
			} else {
				fmt.Printf("  état : erreur (%v)\n", err)
			}
		} else {
			// The probe is read-only by contract, so it cannot prove a write
			// will succeed: a keyring whose collection is locked answers a
			// lookup and then refuses to store. Saying so here is the
			// difference between "reachable" and "usable".
			fmt.Println("  état : joignable (sonde en lecture seule ; une collection verrouillée peut encore refuser les écritures)")
		}
		for _, kind := range codebox.KnownCredentialKinds() {
			if _, err := native.Get(ctx, kind); err == nil {
				fmt.Printf("  %s : enregistré\n", kind)
			} else if codebox.IsCredentialNotFound(err) {
				fmt.Printf("  %s : non enregistré\n", kind)
			} else {
				fmt.Printf("  %s : illisible (%v)\n", kind, err)
			}
		}
	}
	// The fallback store is reported but never created by a status read.
	fileStore, err := codebox.NewFileCredentialStore()
	if err == nil {
		if err := fileStore.Verify(ctx); err != nil {
			if errors.Is(err, codebox.ErrFileStoreAbsent) {
				fmt.Println("Magasin de fichiers de secours : absent (jamais créé ; 'auth add --fallback' y consent)")
				return 0, nil
			}
			var se *codebox.StoreError
			if errors.As(err, &se) {
				fmt.Printf("Magasin de fichiers de secours : %s (%s)\n", se.State, se.Err)
			} else {
				fmt.Printf("Magasin de fichiers de secours : erreur (%v)\n", err)
			}
			return 0, nil
		}
		var stored []string
		for _, kind := range codebox.KnownCredentialKinds() {
			if fileStore.HasCredential(kind) {
				stored = append(stored, string(kind))
			}
		}
		if len(stored) > 0 {
			fmt.Printf("Magasin de fichiers de secours : présent, contient : %s\n", strings.Join(stored, ", "))
		} else {
			fmt.Println("Magasin de fichiers de secours : présent, rien d'enregistré")
		}
	}
	return 0, nil
}

// resolveCredentialRefForAuth applies the same precedence as the run path
// (env > project manifest > user settings), from the discovered project root.
// auth runs before project resolution in main, so the discovery is done here;
// a failure means "no reference", never a blocked removal.
func resolveCredentialRefForAuth() string {
	if v := strings.TrimSpace(os.Getenv("CODEBOX_CREDENTIAL_REF")); v != "" {
		return v
	}
	if pc, err := codebox.DiscoverProject("."); err == nil {
		if pm, err := codebox.ReadProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(pc.Root)); err == nil && pm.CredentialRef != "" {
			return pm.CredentialRef
		}
	}
	if path, err := codebox.UserSettingsPath(); err == nil {
		if us, err := codebox.ReadUserSettings(codebox.DefaultFS, path); err == nil {
			return us.CredentialRef
		}
	}
	return ""
}

func authRemoveCmd(args []string) (int, error) {
	kind, _, fallback, err := parseAuthArgs(args)
	if err != nil {
		return 2, err
	}
	store, err := authStore(fallback)
	if err != nil {
		return 1, err
	}
	// P09: revoke the access before deleting the credential. Microsandbox
	// instances bound through the proxy are revoked in place (live when
	// running, persisted when stopped); Tart/agent-vm instances hold the
	// plaintext credential, so removal while one runs is refused rather than
	// reported as a revocation it cannot be.
	// Revocation is scoped to the store being edited: the native and
	// fallback stores can both hold the kind, and removing one must not
	// revoke sandboxes bound to the other.
	storeName := "native"
	if fallback {
		storeName = "file"
	}
	// The project's credentialRef decides which store entry feeds the Albert
	// binding here. auth runs before project/config resolution in main, so it
	// is read explicitly: without it, removing a differently-named entry that
	// feeds Albert would look harmless while a Tart/agent-vm guest still
	// holds the readable credential.
	report, err := codebox.RevokeCredential(context.Background(), string(kind), storeName, resolveCredentialRefForAuth())
	if err != nil {
		return 1, err
	}
	for _, name := range report.LiveRevoked {
		fmt.Printf("Identifiant %q révoqué sur la machine isolée en cours d'exécution %s.\n", kind, name)
	}
	for _, name := range report.StoppedCleared {
		fmt.Printf("Référence persistante de %q effacée de %s (effective à son prochain démarrage).\n", kind, name)
	}
	if len(report.Revolved) > 0 {
		fmt.Printf("Liaisons de la machine isolée révoquées : %s.\n", strings.Join(report.Revolved, ", "))
	}
	if report.PlaceholderDangles {
		fmt.Fprintf(os.Stderr, "Attention : une machine isolée en cours d'exécution conserve dans son environnement le marqueur %s désormais inerte jusqu'à son redémarrage ; les requêtes vers l'hôte auparavant autorisé échoueront, mais la chaîne du marqueur elle-même est divulguée. Redémarrez l'instance pour l'effacer.\n", kind)
	}
	if len(report.Pending) > 0 {
		fmt.Fprintf(os.Stderr, "Remarque : la source de %q n'a pas pu être confirmée pour %v ; leurs liaisons ont été révoquées plutôt que laissées en place. Redémarrez-les ou recréez-les pour supprimer toute référence orpheline.\n", kind, report.Pending)
	}
	if err := store.Remove(context.Background(), kind); err != nil {
		if codebox.IsCredentialNotFound(err) {
			fmt.Printf("Aucun identifiant %q enregistré dans le magasin %s.\n", kind, store.Kind())
			return 0, nil
		}
		return 1, err
	}
	// The entry is gone from this store. Drop the record only when it names this
	// store: the other store may still hold the kind, and its own record must
	// keep pointing at it.
	_ = codebox.ForgetCredentialStore(codebox.DefaultStateDir(), kind, store.Kind())
	fmt.Printf("Identifiant %q supprimé du magasin %s.\n", kind, store.Kind())
	return 0, nil
}
