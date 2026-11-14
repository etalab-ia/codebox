package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/packages/secrets"
)

// `codebox secrets init` allocates a remote project ID, writes the schema-7
// manifest, and derives host-local binding hints from dotenv files available
// in the project. It never writes a value into the manifest.
func secretsInitCmd(args []string) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("secrets init takes no arguments")
	}
	root, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	manifest, err := projectSecretsManifest(root)
	if err != nil {
		return 1, err
	}
	if manifest.Secrets != nil {
		return 1, fmt.Errorf("this project already declares shared secrets (%s); refusing to replace it", manifest.Secrets.ProjectID)
	}
	client, err := gatewayClient()
	if err != nil {
		return 1, err
	}
	created, err := client.CreateProject(context.Background(), secrets.CreateProjectRequest{})
	if err != nil {
		return 1, err
	}

	hints, err := deriveSecretBindingHints(root)
	if err != nil {
		return 1, err
	}
	manifest.Secrets = &codebox.ProjectSecretsManifest{
		ProjectID: created.ProjectID,
		Bindings:  hints,
	}
	if err := codebox.WriteProjectManifest(codebox.DefaultFS, codebox.ProjectManifestPath(root), *manifest); err != nil {
		return 1, err
	}
	fmt.Printf("Shared development secrets initialised for project %s.\n", created.ProjectID)
	fmt.Println("Commit .codebox/project.json, then run codebox secrets push to upload a development bundle.")
	return 0, nil
}

// deriveSecretBindingHints offers a conservative default: discovered dotenv
// variables are proxy-protected with no destination. The host must approve a
// real destination before startup; a manifest alone never authorises delivery.
func deriveSecretBindingHints(root string) ([]codebox.ProjectSecretBindingHint, error) {
	paths, err := discoverProjectDotenvFiles(root)
	if err != nil {
		return nil, err
	}
	paths, err = filterDevelopmentDotenvPaths(paths)
	if err != nil {
		return nil, err
	}
	files, err := readProjectSecretsFiles(root, paths)
	if err != nil {
		return nil, err
	}
	var out []codebox.ProjectSecretBindingHint
	for _, file := range files {
		names := make([]string, 0, len(file.Parsed.Values))
		for name := range file.Parsed.Values {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, codebox.ProjectSecretBindingHint{Component: file.Component, Name: name})
		}
	}
	return out, nil
}

// `codebox secrets push` reads the project's dotenv files, validates the
// component bundle, and pushes it to the gateway at the current revision.
func secretsPushCmd(args []string) (int, error) {
	if len(args) != 0 {
		return 2, fmt.Errorf("secrets push takes no arguments")
	}
	root, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	projectID, err := requiredProjectID(root)
	if err != nil {
		return 1, err
	}
	client, err := gatewayClient()
	if err != nil {
		return 1, err
	}
	paths, err := discoverProjectDotenvFiles(root)
	if err != nil {
		return 1, err
	}
	paths, err = filterDevelopmentDotenvPaths(paths)
	if err != nil {
		return 1, err
	}
	files, err := readProjectSecretsFiles(root, paths)
	if err != nil {
		return 1, err
	}
	components := map[string]map[string]string{}
	for _, file := range files {
		components[file.Component] = file.Parsed.Values
	}
	// Validate locally before sending a body that the gateway would reject.
	if _, err := secrets.MarshalBundle(components); err != nil {
		return 1, err
	}
	current, err := client.Resolve(context.Background(), projectID, nil)
	if err != nil {
		return 1, err
	}
	resp, err := client.PushSecrets(context.Background(), projectID, secrets.PushSecretsRequest{
		BaseRevision: current.Revision,
		Components:   components,
	})
	if err != nil {
		return 1, err
	}
	fmt.Printf("Pushed %d dotenv component(s) as revision %d. No secret value was printed.\n", len(components), resp.Revision)
	return 0, nil
}

func secretsMembersCmd(args []string) (int, error) {
	if len(args) == 0 {
		return 2, fmt.Errorf("usage: codebox secrets members <list|add|remove> [email]")
	}
	root, err := os.Getwd()
	if err != nil {
		return 1, err
	}
	projectID, err := requiredProjectID(root)
	if err != nil {
		return 1, err
	}
	client, err := gatewayClient()
	if err != nil {
		return 1, err
	}
	current, err := client.ListMembers(context.Background(), projectID)
	if err != nil {
		return 1, err
	}
	switch args[0] {
	case "list":
		fmt.Printf("Project %s members (revision %d):\n", projectID, current.Revision)
		for _, entry := range current.Entries {
			fmt.Printf("  %s: %s\n", entry.Kind, entry.Value)
		}
		return 0, nil
	case "add", "remove":
		if len(args) != 2 {
			return 2, fmt.Errorf("usage: codebox secrets members %s <email|siret:14-digits>", args[0])
		}
		kind, value, err := parseACLEntry(args[1])
		if err != nil {
			return 2, err
		}
		entries := append([]secrets.ACLEntry(nil), current.Entries...)
		if args[0] == "add" {
			entries = append(entries, secrets.ACLEntry{Kind: kind, Value: value})
		} else {
			kept := entries[:0]
			for _, e := range entries {
				if e.Kind == kind && e.Value == value {
					continue
				}
				kept = append(kept, e)
			}
			entries = kept
		}
		if _, err := client.UpdateMembers(context.Background(), projectID, secrets.UpdateMembersRequest{
			BaseRevision: current.Revision,
			Entries:      entries,
		}); err != nil {
			return 1, err
		}
		fmt.Printf("Updated project members. The owner list is unchanged by %s.\n", args[0])
		return 0, nil
	default:
		return 2, fmt.Errorf("unknown secrets members command %q", args[0])
	}
}

func parseACLEntry(raw string) (secrets.EntryKind, string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "siret:") {
		s := strings.TrimPrefix(raw, "siret:")
		if err := secrets.ValidateSIRET(s); err != nil {
			return "", "", err
		}
		return secrets.EntrySIRET, s, nil
	}
	email, err := secrets.NormalizeEmail(raw)
	if err != nil {
		return "", "", err
	}
	return secrets.EntryMember, email, nil
}

// ensureDeclaredBindingApprovals compares a schema-7 manifest's non-secret
// hints with the host-local binding approvals. A repository manifest is never
// an authorization: when a hint is missing or its destination/delivery changed,
// launch fails closed and the user must run the explicit binding approval. In
// a TTY, one consolidated French confirmation screen explains the gap.
func ensureDeclaredBindingApprovals(root string) error {
	manifest, err := projectSecretsManifest(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if manifest.Secrets == nil || len(manifest.Secrets.Bindings) == 0 {
		return nil
	}
	store, err := projectSecretStoreForCLI(root, false)
	if err != nil {
		return err
	}
	record, err := codebox.ReadProjectSecretBindingsRecordFor(store)
	if err != nil {
		return err
	}
	existing := map[string]codebox.ProjectSecretBinding{}
	for _, b := range record.Bindings {
		existing[b.Component+"\x00"+b.Name] = b
	}
	var missing []string
	var changed []string
	for _, hint := range manifest.Secrets.Bindings {
		id := hint.Component + "\x00" + hint.Name
		b, ok := existing[id]
		if !ok {
			missing = append(missing, hint.Component+"#"+hint.Name)
			continue
		}
		wantGuest := hint.Delivery == codebox.ProjectSecretDeliveryGuest
		if b.GuestReadable() != wantGuest {
			changed = append(changed, hint.Component+"#"+hint.Name)
			continue
		}
		// Empty hint destinations mean the host must choose at least one real
		// destination. A non-empty hint list is a versioned policy and must
		// match the host approval exactly.
		if len(hint.Destinations) == 0 {
			if len(b.Destinations) == 0 {
				changed = append(changed, hint.Component+"#"+hint.Name)
			}
		} else if !codebox.EqualStringSets(b.Destinations, hint.Destinations) {
			changed = append(changed, hint.Component+"#"+hint.Name)
		}
	}
	if len(missing) == 0 && len(changed) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(changed)
	message := fmt.Sprintf(
		"Des secrets partagés sont déclarés, mais %d liaison(s) ne sont pas approuvées sur ce poste et %d ont changé.\n"+
			"Une modification de manifeste ne donne jamais accès à une valeur. Approuvez les destinations explicitement avec `codebox secrets bindings approve`.",
		len(missing), len(changed),
	)
	if projectSecretsTTYFn() {
		ok, err := projectSecretsConfirmFn("Approuver les secrets déclarés sur ce poste ?", message, false)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("shared secrets declarations are not approved on this host")
		}
	}
	return fmt.Errorf("shared secrets declarations are not approved on this host (%d missing, %d changed); run codebox secrets bindings approve", len(missing), len(changed))
}

func projectDeclaresSharedSecrets(root string) bool {
	manifest, err := projectSecretsManifest(root)
	return err == nil && manifest.Secrets != nil
}

func validateRemoteBindingAgainstManifest(root string, binding codebox.ProjectSecretBinding) error {
	manifest, err := projectSecretsManifest(root)
	if err != nil {
		return err
	}
	if manifest.Secrets == nil {
		return fmt.Errorf("this project does not declare shared secrets")
	}
	for _, hint := range manifest.Secrets.Bindings {
		if hint.Component != binding.Component || hint.Name != binding.Name {
			continue
		}
		wantGuest := hint.Delivery == codebox.ProjectSecretDeliveryGuest
		if binding.GuestReadable() != wantGuest {
			return fmt.Errorf("delivery mode for %s#%s does not match the declaration", binding.Component, binding.Name)
		}
		if wantGuest {
			return nil
		}
		if len(hint.Destinations) > 0 && !codebox.EqualStringSets(binding.Destinations, hint.Destinations) {
			return fmt.Errorf("destinations for %s#%s do not match the declaration", binding.Component, binding.Name)
		}
		if len(binding.Destinations) == 0 {
			return fmt.Errorf("at least one destination is required for %s#%s", binding.Component, binding.Name)
		}
		return codebox.ValidateProjectSecretBindingForRemote("", binding)
	}
	return fmt.Errorf("%s#%s is not declared by this project's shared-secrets manifest", binding.Component, binding.Name)
}

// filterDevelopmentDotenvPaths rejects production-named dotenv files. Shared
// development secrets must never upload a `.env.production`, `.env.prod` or
// `production.env` file merely because it exists in the checkout.
func filterDevelopmentDotenvPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		base := strings.ToLower(filepath.Base(path))
		for _, forbidden := range []string{".env.production", ".env.prod", "production.env", "prod.env", "prd.env"} {
			if base == forbidden || strings.HasPrefix(base, forbidden+".") {
				return nil, fmt.Errorf("refusing to upload production-named dotenv file %s; shared secrets support development files only", path)
			}
		}
		out = append(out, path)
	}
	return out, nil
}
