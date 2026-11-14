package codebox

import (
	"context"
	"strings"
	"testing"
)

// Phase 2 runtime wiring: an approved project binding must reach the runtime
// binding set, and a lifted approval must be named explicitly so the runtime's
// patch-style secret modification actually unregisters it.

func approveProjectBindingForTest(t *testing.T, m *MicrosandboxRuntime, b ProjectSecretBinding) {
	t.Helper()
	store, err := m.projectSecretStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := SetProjectSecretActiveProfile(DefaultFS, m.StateDir, store.Namespace(), b.Profile); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, store.Namespace(), b); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBindingsIncludesApprovedProjectSecret(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value"})

	approveProjectBindingForTest(t, m, ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
		GuestEnv: "JC_API_KEY", Destinations: []string{"api.example.com"},
	})

	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var project *resolvedBinding
	for i := range bindings {
		if bindings[i].Project != nil {
			project = &bindings[i]
		}
	}
	if project == nil {
		t.Fatalf("the approved project binding did not resolve: %+v", bindings)
	}
	if project.GuestEnv != "JC_API_KEY" || project.value != "project-value" {
		t.Fatalf("resolved project binding is wrong: %+v", *project)
	}
	if project.Placeholder != ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY") {
		t.Fatalf("placeholder must match the qualified marker derivation: %q", project.Placeholder)
	}
	if strings.Contains(project.Placeholder, "$") {
		t.Fatal("the placeholder must stay dollar-free: the guest loaders expand it away otherwise")
	}
	if len(project.AllowHosts) != 1 || project.AllowHosts[0] != "api.example.com" {
		t.Fatalf("approved destinations must be the only allowed hosts: %v", project.AllowHosts)
	}
	if project.Project == nil || project.Project.Reference == "" {
		t.Fatal("a project binding must carry the opaque index reference")
	}
	if project.HostEnv == "" || !isGuestEnvName(project.GuestEnv) {
		t.Fatalf("transport variable missing or invalid: %q", project.HostEnv)
	}
}

func TestResolveBindingsWithoutApprovalDeliversNothing(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value"})

	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An imported but unapproved value must never be bound implicitly.
	for _, b := range bindings {
		if b.Project != nil {
			t.Fatalf("an unapproved import reached the runtime: %+v", b)
		}
	}
}

func TestResolveBindingsFailsClosedOnApprovedButMissingEntry(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value"})
	// Approve a variable that was never imported.
	approveProjectBindingForTest(t, m, ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "NOT_IMPORTED",
		GuestEnv: "JC_NOT_IMPORTED", Destinations: []string{"api.example.com"},
	})
	if _, err := m.resolveBindings(context.Background()); err == nil {
		t.Fatal("an approved but unimported binding must block the start, not be skipped")
	}
}

func TestResolveBindingsFailsClosedOnLockedProjectStore(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value"})
	approveProjectBindingForTest(t, m, ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
		GuestEnv: "JC_API_KEY", Destinations: []string{"api.example.com"},
	})
	// A store that refuses reads must fail the start: dropping the binding
	// here would make the omission the desired set, and the runtime patch
	// semantics would then unregister a live credential.
	store.mu.Lock()
	store.values = map[CredentialKind]string{}
	store.mu.Unlock()
	if _, err := m.resolveBindings(context.Background()); err == nil {
		t.Fatal("a project value that no longer resolves must block the start")
	}
}

func TestStaleProjectBindingsAreNamedForRemoval(t *testing.T) {
	desired := []msbSecretBinding{
		{Kind: CredentialAlbert, GuestEnv: msbAPISecretEnv, HostEnv: "CODEBOX_HOST_ALBERT_API_KEY", AllowHosts: []string{msbAllowHost}},
		{Kind: CredentialKind("JC_KEPT"), GuestEnv: "JC_KEPT", HostEnv: "CODEBOX_HOST_PROJECT_JC_KEPT", AllowHosts: []string{"api.example.com"}},
	}
	stale := staleBindingGuestEnvs(desired, []string{"JC_KEPT", "JC_DROPPED"})
	if !containsString(stale, "JC_DROPPED") {
		t.Fatalf("the lifted approval must be unregistered, got %v", stale)
	}
	// A previously applied variable that is still desired must not be removed,
	// or every refresh would drop a live binding.
	for _, env := range stale {
		if env == "JC_KEPT" || env == msbAPISecretEnv {
			t.Fatalf("a desired binding was scheduled for removal: %v", stale)
		}
	}
	// With nothing previously applied the result must equal the old behavior:
	// only registered built-ins absent from the desired set.
	onlyAlbert := []msbSecretBinding{desired[0]}
	got := staleBindingGuestEnvs(onlyAlbert, nil)
	if strings.Join(got, ",") != "CONTEXT7_API_KEY,GITHUB_TOKEN,MISTRAL_API_KEY,OPENAI_COMPATIBLE_API_KEY" {
		t.Fatalf("built-in inventory changed: %v", got)
	}
}

func TestProjectSecretBindingsRecordSurfacesUnresolvedState(t *testing.T) {
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	s := projectSecretFixture(t, root, state, store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value", "OTHER": "second"})

	record, err := ReadProjectSecretBindingsRecordFor(s)
	if err != nil {
		t.Fatal(err)
	}
	if record.Profile != "" || len(record.Bindings) != 0 {
		t.Fatalf("no approval yet: %+v", record)
	}
	if strings.Join(record.Undelivered, ",") != ".env#API_KEY,.env#OTHER" {
		t.Fatalf("imported-but-undelivered entries must be listed: %v", record.Undelivered)
	}

	if err := SetProjectSecretActiveProfile(DefaultFS, state, s.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, state, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY", Destinations: []string{"api.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	record, err = ReadProjectSecretBindingsRecordFor(s)
	if err != nil {
		t.Fatal(err)
	}
	if record.Profile != "development" || len(record.Bindings) != 1 || record.Pending != "" {
		t.Fatalf("approved record: %+v", record)
	}
	// The guest variable is derived when the caller does not supply one.
	if record.Bindings[0].GuestEnv != "JC_API_KEY" {
		t.Fatalf("guest variable must be derived: %q", record.Bindings[0].GuestEnv)
	}
	if strings.Join(record.Undelivered, ",") != ".env#OTHER" {
		t.Fatalf("the unapproved import must still be reported: %v", record.Undelivered)
	}

	revoked, err := RevokeProjectSecretBinding(DefaultFS, state, s.Namespace(), "development", ".env", "API_KEY")
	if err != nil || !revoked {
		t.Fatalf("revocation: %v, %v", revoked, err)
	}
	if again, err := RevokeProjectSecretBinding(DefaultFS, state, s.Namespace(), "development", ".env", "API_KEY"); err != nil || again {
		t.Fatalf("revoking an unapproved binding must be a no-op: %v, %v", again, err)
	}
	record, err = ReadProjectSecretBindingsRecordFor(s)
	if err != nil || len(record.Bindings) != 0 {
		t.Fatalf("after revocation: %+v, %v", record, err)
	}
	// The active profile survives revocation, so re-approving is one step.
	if record.Profile != "development" {
		t.Fatalf("active profile must survive a revocation: %q", record.Profile)
	}
}
