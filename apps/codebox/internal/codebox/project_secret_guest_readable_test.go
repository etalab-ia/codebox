package codebox

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Phase 4: a value the application must consume locally. The guest holds the
// real value instead of a marker, so the contract differs in kind: the approval
// is explicit, the file is owner-only, and the trade is stated.

func strPtr(s string) *string { return &s }

func guestDelivery(component, name, value string) ProjectSecretDelivery {
	return ProjectSecretDelivery{
		ProjectSecretBinding: ProjectSecretBinding{
			Profile: "development", Component: component, Name: name,
			Delivery: ProjectSecretDeliveryGuest,
		},
		Reference: "project-env-ns-" + name,
		Value:     strPtr(value),
	}
}

func TestGuestReadableBindingValidation(t *testing.T) {
	ok := testBindingSet(ProjectSecretBinding{Profile: "development", Component: ".env", Name: "DB_PASSWORD", Delivery: ProjectSecretDeliveryGuest})
	if err := validateProjectSecretBindingSet(ok, "ns"); err != nil {
		t.Fatalf("a guest-readable binding must validate: %v", err)
	}
	withDestination := testBindingSet(ProjectSecretBinding{Profile: "development", Component: ".env", Name: "DB_PASSWORD",
		Delivery: ProjectSecretDeliveryGuest, Destinations: []string{"db.example.com"}})
	if err := validateProjectSecretBindingSet(withDestination, "ns"); err == nil {
		t.Fatal("a guest-readable binding must not declare destinations")
	}
	withEnv := testBindingSet(ProjectSecretBinding{Profile: "development", Component: ".env", Name: "DB_PASSWORD",
		Delivery: ProjectSecretDeliveryGuest, GuestEnv: "JC_DB_PASSWORD"})
	if err := validateProjectSecretBindingSet(withEnv, "ns"); err == nil {
		t.Fatal("a guest-readable binding must not declare a guest variable")
	}
	unknown := testBindingSet(ProjectSecretBinding{Profile: "development", Component: ".env", Name: "DB_PASSWORD", Delivery: "whenever"})
	if err := validateProjectSecretBindingSet(unknown, "ns"); err == nil {
		t.Fatal("an unknown delivery mode must be refused")
	}
}

func TestBuildGuestSecretFilesRendersValuesForGuestReadableBindings(t *testing.T) {
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
		guestDelivery(".env", "DB_PASSWORD", "local-secret-value"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file for one component: %+v", files)
	}
	file := files[0]
	if !file.Secret {
		t.Fatal("a file holding a real value must be marked secret")
	}
	if !strings.Contains(file.Content, "DB_PASSWORD='local-secret-value'\n") {
		t.Fatalf("the real value must be rendered: %q", file.Content)
	}
	if !strings.Contains(file.Content, "API_KEY=__CODEBOX_SECRET_aaa__\n") {
		t.Fatalf("the proxy binding must still render its marker: %q", file.Content)
	}
	if !strings.Contains(file.Content, "WARNING") {
		t.Fatalf("a file holding a real value must warn: %q", file.Content)
	}
	if strings.Contains(file.Content, "DB_PASSWORD=__CODEBOX_SECRET") {
		t.Fatal("a guest-readable variable must not be rendered as a marker")
	}
}

func TestBuildGuestSecretFilesWithoutSecretsKeepsThePlaceholderHeader(t *testing.T) {
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Secret || strings.Contains(files[0].Content, "WARNING") {
		t.Fatalf("a marker-only file must not be flagged secret: %+v", files[0])
	}
}

func TestBuildGuestSecretFilesRequiresAResolvedValue(t *testing.T) {
	unresolved := guestDelivery(".env", "DB_PASSWORD", "")
	unresolved.Value = nil
	if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{unresolved}); err == nil {
		t.Fatal("a guest-readable binding whose value was not resolved must be refused, not rendered empty")
	}
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{guestDelivery(".env", "EMPTY_ONE", "")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files[0].Content, "EMPTY_ONE=''\n") {
		t.Fatalf("an empty secret must render as an empty assignment: %q", files[0].Content)
	}
}

func TestSecretFileIsRestrictedBeforeThePayloadExists(t *testing.T) {
	m, client := newGuestFSRuntime(t, nil)
	managed, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		guestDelivery(".env", "DB_PASSWORD", "local-secret"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	guestPath := msbGuestWorkspace + "/.env"
	if len(managed) != 1 || managed[0] != guestPath {
		t.Fatalf("managed: %v", managed)
	}
	if mode := client.modes[guestPath]; mode != 0o600 {
		t.Fatalf("the delivered file must end up 0600, got %v", mode)
	}

	// The ordering is the security property. Writing the payload first and
	// restricting afterwards would leave the secret readable by every account
	// in the guest for that window, and a crash inside the window would leave
	// it that way for good.
	create, write, move := -1, -1, -1
	for i, call := range client.calls {
		switch {
		case strings.HasPrefix(call, "exec ") && strings.Contains(call, "umask 077; : > '"):
			create = i
		case strings.HasPrefix(call, "writefile "):
			write = i
		case strings.Contains(call, "stat -c %a") && strings.Contains(call, "mv -f"):
			move = i
		}
	}
	if create < 0 || write < 0 || move < 0 {
		t.Fatalf("expected a pre-create, a payload write and a checked rename: %v", client.calls)
	}
	if !(create < write && write < move) {
		t.Fatalf("the temporary file must exist with its mode before the payload lands: create=%d write=%d move=%d", create, write, move)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "exec ") && strings.Contains(call, "chmod ") {
			t.Fatalf("restricting after the payload exists is the window this avoids: %v", call)
		}
	}
}

func TestAPublishedFileWithTheWrongModeIsRefused(t *testing.T) {
	// The control for the check above: force the temporary file to 0644 and
	// the delivery must fail without publishing anything, rather than trusting
	// that the pre-creation worked.
	m, client := newGuestFSRuntime(t, nil)
	wrong := os.FileMode(0o644)
	client.tmpModeOverride = &wrong
	if _, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		guestDelivery(".env", "DB_PASSWORD", "local-secret"),
	}, nil); err == nil {
		t.Fatal("a temporary file that is not 0600 must not be published under its final name")
	}
	if _, ok := client.files[msbGuestWorkspace+"/.env"]; ok {
		t.Fatal("nothing may be published when the restriction is missing")
	}
}

func TestMarkerOnlyFileIsNotChmodded(t *testing.T) {
	m, client := newGuestFSRuntime(t, nil)
	if _, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.modes[msbGuestWorkspace+"/.env"]; ok {
		t.Fatal("a marker-only file needs no restriction")
	}
}

func TestGuestReadableBindingIsNotRegisteredWithTheProxy(t *testing.T) {
	// The approvals must live where the runtime reads them: its own state dir,
	// not a second temporary one.
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	state := m.StateDir
	importProjectSecrets(t, s, "development", ".env", map[string]string{"DB_PASSWORD": "local-secret", "API_KEY": "remote-secret"})
	if err := SetProjectSecretActiveProfile(DefaultFS, state, s.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, state, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "DB_PASSWORD", Delivery: ProjectSecretDeliveryGuest,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, state, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY", GuestEnv: "JC_API_KEY",
		Destinations: []string{"api.example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sawProxy := false
	for _, b := range bindings {
		if b.Project == nil {
			continue
		}
		if b.Project.Name == "DB_PASSWORD" {
			t.Fatal("a guest-readable value must not be registered with the network proxy")
		}
		if b.Project.Name == "API_KEY" {
			sawProxy = true
		}
	}
	if !sawProxy {
		t.Fatal("the proxy-protected binding must still resolve")
	}
}
