package codebox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBindingSet(bindings ...ProjectSecretBinding) projectSecretBindingSet {
	return projectSecretBindingSet{
		SchemaVersion: projectSecretBindingSchemaVersion,
		Namespace:     "ns",
		Profile:       "development",
		Bindings:      bindings,
	}
}

func binding(component, name, guestEnv string, hosts ...string) ProjectSecretBinding {
	return ProjectSecretBinding{
		Profile: "development", Component: component, Name: name,
		GuestEnv: guestEnv, Destinations: hosts,
	}
}

func TestProjectSecretGuestEnvSeparatesComponents(t *testing.T) {
	root := ProjectSecretGuestEnv(".env", "API_KEY")
	nested := ProjectSecretGuestEnv("apps/api/.env", "API_KEY")
	other := ProjectSecretGuestEnv("apps/worker/.env", "API_KEY")
	if root != "JC_API_KEY" || nested != "JC_APPS_API_API_KEY" || other != "JC_APPS_WORKER_API_KEY" {
		t.Fatalf("derived guest variables: %q %q %q", root, nested, other)
	}
	if nested == other || root == nested {
		t.Fatal("components sharing a variable name must not collide")
	}
	for _, name := range []string{root, nested, other} {
		if !isGuestEnvName(name) {
			t.Fatalf("%q is not a usable guest variable name", name)
		}
	}
}

func TestProjectSecretPlaceholderIsStableAndDollarFree(t *testing.T) {
	a := ProjectSecretPlaceholder("ns", "development", ".env", "API_KEY")
	b := ProjectSecretPlaceholder("ns", "development", "apps/api/.env", "API_KEY")
	if a == b {
		t.Fatal("distinct identities must not share a placeholder")
	}
	if a != ProjectSecretPlaceholder("ns", "development", ".env", "API_KEY") {
		t.Fatal("the placeholder must be stable for one identity")
	}
	for _, marker := range []string{a, b} {
		if strings.ContainsAny(marker, "$ \t\n") || !strings.HasPrefix(marker, "__CODEBOX_SECRET_") || !strings.HasSuffix(marker, "__") {
			t.Fatalf("placeholder %q is not a portable opaque marker", marker)
		}
	}
}

func TestValidateProjectSecretBindingSet(t *testing.T) {
	valid := testBindingSet(
		binding(".env", "API_KEY", "JC_API_KEY", "api.example.com"),
		binding("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "api.example.com"),
	)
	// Overlapping destinations across bindings are qualified behavior now.
	if err := validateProjectSecretBindingSet(valid, "ns"); err != nil {
		t.Fatalf("overlapping destinations must be accepted: %v", err)
	}

	reserved := msbSecretBindings()
	for _, r := range reserved {
		set := testBindingSet(binding(".env", "KEY", r.GuestEnv, "api.example.com"))
		if err := validateProjectSecretBindingSet(set, "ns"); err == nil {
			t.Fatalf("reserved guest variable %s must be refused", r.GuestEnv)
		}
	}

	cases := map[string]projectSecretBindingSet{
		"duplicate guest variable": testBindingSet(
			binding(".env", "A", "JC_SHARED", "api.example.com"),
			binding(".env", "B", "JC_SHARED", "api.example.com"),
		),
		"duplicate identity": testBindingSet(
			binding(".env", "A", "JC_ONE", "api.example.com"),
			binding(".env", "A", "JC_TWO", "api.example.com"),
		),
		"no destination": testBindingSet(binding(".env", "A", "JC_ONE")),
		"scheme in destination": testBindingSet(
			binding(".env", "A", "JC_ONE", "https://api.example.com"),
		),
		"wildcard destination":   testBindingSet(binding(".env", "A", "JC_ONE", "*.example.com")),
		"repeated destination":   testBindingSet(binding(".env", "A", "JC_ONE", "api.example.com", "api.example.com")),
		"invalid guest variable": testBindingSet(binding(".env", "A", "1BAD", "api.example.com")),
		"invalid profile": projectSecretBindingSet{
			SchemaVersion: projectSecretBindingSchemaVersion, Profile: "../etc",
			Bindings: []ProjectSecretBinding{binding(".env", "A", "JC_ONE", "api.example.com")},
		},
	}
	for name, set := range cases {
		if err := validateProjectSecretBindingSet(set, "ns"); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := validateProjectSecretBindingSet(valid, "other-namespace"); err == nil {
		t.Fatal("a record from another namespace must be refused")
	}
}

func TestProjectSecretBindingsRoundTripAndForeignNamespace(t *testing.T) {
	fs, dir := DefaultFS, t.TempDir()
	path := filepath.Join(dir, "bindings.json")
	if set, err := ReadProjectSecretBindings(fs, path, "ns"); err != nil || len(set.Bindings) != 0 || set.Profile != "" {
		t.Fatalf("a missing record must read as empty: %+v, %v", set, err)
	}
	want := testBindingSet(
		binding("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "api.example.com"),
		binding(".env", "TOKEN", "JC_TOKEN", "api.example.com", "auth.example.com"),
	)
	if err := WriteProjectSecretBindings(fs, path, "ns", want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadProjectSecretBindings(fs, path, "ns")
	if err != nil || got.Profile != "development" || len(got.Bindings) != 2 {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	// Sorted by identity: .env#TOKEN precedes apps/api/.env#API_KEY.
	if got.Bindings[0].Name != "TOKEN" || got.Bindings[1].Name != "API_KEY" {
		t.Fatalf("records must be stored in a stable order: %+v", got.Bindings)
	}
	if _, err := ReadProjectSecretBindings(fs, path, "another-namespace"); err == nil {
		t.Fatal("reading a foreign namespace must fail closed")
	}
	if err := WriteProjectSecretBindings(fs, path, "ns", testBindingSet(binding(".env", "A", "1BAD", "api.example.com"))); err == nil {
		t.Fatal("a malformed set must never be persisted")
	}
}

func TestProjectSecretDeliveries(t *testing.T) {
	ctx := context.Background()
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	s := projectSecretFixture(t, root, state, store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "dev-value"})
	importProjectSecrets(t, s, "development", "apps/api/.env", map[string]string{"API_KEY": "api-value"})
	importProjectSecrets(t, s, "test", ".env", map[string]string{"OTHER": "test-value"})

	path := ProjectSecretBindingsPath(state, s.namespace)
	// No record yet: nothing is delivered and nothing is reported missing.
	deliveries, undelivered, err := s.Deliveries(ctx)
	if err != nil || len(deliveries) != 0 || len(undelivered) != 3 {
		t.Fatalf("no approvals: %+v, %v, %v", deliveries, undelivered, err)
	}

	set := testBindingSet(
		binding(".env", "API_KEY", "JC_API_KEY", "api.example.com"),
		binding("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "api.example.com", "auth.example.com"),
	)
	if err := WriteProjectSecretBindings(DefaultFS, path, s.namespace, set); err != nil {
		t.Fatal(err)
	}
	deliveries, undelivered, err = s.Deliveries(ctx)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("approved deliveries: %+v, %v", deliveries, err)
	}
	if len(undelivered) != 1 || undelivered[0] != ".env#OTHER" {
		t.Fatalf("undelivered entries must be reported (test profile excluded): %v", undelivered)
	}
	// The reference must be the index's opaque entry, not a value.
	for _, d := range deliveries {
		if d.Reference == "" || strings.Contains(d.Reference, "value") || !strings.HasPrefix(d.Reference, "project-env-") {
			t.Fatalf("delivery reference must be the opaque index entry: %q", d.Reference)
		}
	}

	// An approval for a variable that was never imported fails closed.
	missing := testBindingSet(binding(".env", "NOT_IMPORTED", "JC_NOT_IMPORTED", "api.example.com"))
	if err := WriteProjectSecretBindings(DefaultFS, path, s.namespace, missing); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deliveries(ctx); err == nil {
		t.Fatal("an approved but unimported binding must block delivery")
	}

	// Bindings recorded for another profile must not be delivered under the
	// active one.
	mismatch := testBindingSet(binding("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "api.example.com"))
	mismatch.Bindings[0].Profile = "test"
	if err := WriteProjectSecretBindings(DefaultFS, path, s.namespace, mismatch); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deliveries(ctx); err == nil {
		t.Fatal("a profile mismatch must fail closed rather than deliver the wrong value")
	}

	// Bindings with no active profile must fail rather than guess.
	noProfile := testBindingSet(binding(".env", "API_KEY", "JC_API_KEY", "api.example.com"))
	noProfile.Profile = ""
	if err := WriteProjectSecretBindings(DefaultFS, path, s.namespace, noProfile); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Deliveries(ctx); err == nil {
		t.Fatal("bindings without an active profile must fail closed")
	}
}

func TestProjectSecretBindingsRecordShapeAndPermissions(t *testing.T) {
	state := t.TempDir()
	path := ProjectSecretBindingsPath(state, "ns")
	if !strings.HasSuffix(path, filepath.Join("project-secrets", "ns", "bindings.json")) {
		t.Fatalf("binding record must live in host state alone: %s", path)
	}
	set := testBindingSet(binding(".env", "API_KEY", "JC_API_KEY", "api.example.com"))
	if err := WriteProjectSecretBindings(DefaultFS, path, "ns", set); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The record is metadata: no value, no credential reference.
	text := string(raw)
	for _, forbidden := range []string{"project-env-", "dev-value", "Reference"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("binding record must not carry %q", forbidden)
		}
	}
	if !strings.Contains(text, "JC_API_KEY") || !strings.Contains(text, "api.example.com") {
		t.Fatalf("binding record must carry the guest variable and destinations: %s", text)
	}
}
