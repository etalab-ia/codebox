package codebox

import (
	"reflect"
	"testing"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

// P02/P09 contract fixtures: pin the SDK secret surface that codebox relies
// on for credential transport, as characterized empirically in
// docs/decisions/2026-09-24-microsandbox-credential-transport.md.
//
// The SDK's wire serializer (buildModifyRequestJSON) is unexported, so these
// fixtures pin the exported option shapes plus codebox's own call sites.
// The two persistence paths observed on the host DB (msb 0.7.0 runtime,
// SDK v0.7.2):
//   - env reference path: only {"kind":"env","var":...} is persisted, never
//     the raw value. P09 migrated every codebox call site to it.
//   - SDK Value path: the raw value is persisted in cleartext in the sandbox
//     config JSON (~/.microsandbox/db/msb.db, mode 0644). codebox must
//     never use it; the create surface (which is Value-only) is fed the inert
//     bootstrap sentinel and rotated to a reference immediately.

// TestP02SecretEntryDefaultsPinsTheTransportContract pins the SDK option
// shape: headers-only substitution, TLS identity required, auto placeholder.
func TestP02SecretEntryDefaultsPinsTheTransportContract(t *testing.T) {
	entry := msb.Secret.Env("ALBERT_API_KEY", "secret-value", msb.SecretEnvOptions{
		Allow: []string{"albert.api.etalab.gouv.fr"},
	})
	if entry.EnvVar != "ALBERT_API_KEY" || entry.Value != "secret-value" {
		t.Fatalf("entry = %+v", entry)
	}
	if !reflect.DeepEqual(entry.Allow, []string{"albert.api.etalab.gouv.fr"}) {
		t.Fatalf("allow = %v", entry.Allow)
	}
	if entry.Placeholder != "" {
		t.Fatalf("custom placeholder set unexpectedly: %q", entry.Placeholder)
	}
	if entry.RequireTLSIdentity != nil {
		t.Fatalf("require TLS identity = %+v, want nil (defaults to true server-side)", entry.RequireTLSIdentity)
	}
	if entry.Substitution.Headers != nil || entry.Substitution.Query || entry.Substitution.Body {
		t.Fatalf("substitution = %+v, want zero value (headers-only server default)", entry.Substitution)
	}
	if entry.ViolationAction != "" {
		t.Fatalf("violation action = %q, want empty (sandbox default block-and-log)", entry.ViolationAction)
	}
}

// TestP09CreateOptionsNeverPersistRawValueOnHost pins the P09 create-path
// contract: the SDK create surface is Value-only, so codebox registers the
// inert bootstrap sentinel (never a credential) and rotates to the host env
// reference immediately after creation. A regression here reintroduces the
// P02 finding (raw value persisted in cleartext in ~/.microsandbox/db/msb.db,
// mode 0644) and must not be "fixed" by updating the test.
func TestP09CreateOptionsNeverPersistRawValueOnHost(t *testing.T) {
	spec := msbSandboxSpec{
		Image:           msbImage,
		SealedWorkspace: true,
		Bindings: bindingsMetadata([]resolvedBinding{
			{msbSecretBinding: mustBinding(t, CredentialAlbert), source: bindingSourceEnv, value: "raw-secret-value"},
		}),
	}
	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(spec) {
		option(&cfg)
	}
	if len(cfg.Secrets) != 1 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets[0].Value != msbSecretBootstrapValue {
		t.Fatalf("create path must register the bootstrap sentinel, never a value: %+v", cfg.Secrets[0])
	}
	if cfg.Secrets[0].Value == "raw-secret-value" {
		t.Fatal("the create path persisted the raw credential on the host")
	}
}

// TestP09NextStartOptionsUseEnvReference is the modify-path twin: the refresh
// is a host env reference ({"kind":"env","var":...}), which persists only the
// variable name — never the value.
func TestP09NextStartOptionsUseEnvReference(t *testing.T) {
	bindings := bindingsMetadata([]resolvedBinding{
		{msbSecretBinding: mustBinding(t, CredentialAlbert), source: bindingSourceStore, value: "raw-secret-value"},
	})
	options := msbNextStartOptions(nil, bindings, nil)
	spec, ok := options.Secrets[msbAPISecretEnv]
	if !ok {
		t.Fatalf("secrets = %+v", options.Secrets)
	}
	if spec.Value != "" {
		t.Fatalf("refresh path must not inline a value: %+v", spec)
	}
	if spec.Env == "" || spec.Store != "" {
		t.Fatalf("refresh path must be an env reference, exclusively: %+v", spec)
	}
	if !reflect.DeepEqual(spec.AllowedHosts, []string{msbAllowHost}) {
		t.Fatalf("allowed hosts = %+v", spec)
	}
}

func TestCustomPlaceholderSurvivesCreateAndModifyOptions(t *testing.T) {
	binding := mustBinding(t, CredentialAlbert)
	binding.GuestEnv = "PAYMENTS_API_KEY"
	binding.HostEnv = "CODEBOX_HOST_PAYMENTS_API_KEY"
	binding.Placeholder = "$MSB_PROJECT_API_KEY_8b88b278"
	binding.AllowHosts = []string{"payments.example.com"}

	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(msbSandboxSpec{
		Image:           msbImage,
		SealedWorkspace: true,
		Bindings:        []msbSecretBinding{binding},
	}) {
		option(&cfg)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Placeholder != binding.Placeholder {
		t.Fatalf("create secret placeholder = %+v, want %q", cfg.Secrets, binding.Placeholder)
	}
	if cfg.Secrets[0].Value != msbSecretBootstrapValue {
		t.Fatalf("create secret value = %q, want inert bootstrap sentinel", cfg.Secrets[0].Value)
	}

	for name, opts := range map[string]msb.ModifyOptions{
		"next-start": msbNextStartOptions(nil, []msbSecretBinding{binding}, nil),
		"live":       msbRotateLiveOptions([]msbSecretBinding{binding}),
	} {
		got, ok := opts.Secrets[binding.GuestEnv]
		if !ok || got.Placeholder != binding.Placeholder || got.Env != binding.HostEnv {
			t.Errorf("%s secret spec = %+v, want placeholder %q and env reference %q", name, got, binding.Placeholder, binding.HostEnv)
		}
	}
}

func mustBinding(t *testing.T, kind CredentialKind) msbSecretBinding {
	t.Helper()
	b, ok := bindingForKind(kind)
	if !ok {
		t.Fatalf("no registered binding for %s", kind)
	}
	return b
}

// TestP02SecretModifySpecSourceKinds pins the three mutually exclusive
// secret sources on the modify surface. Env is the only source that avoids
// persisting the raw value on the host today; Store is a host-side secret
// store reference with no CLI/SDK backend in 0.7.0/v0.7.2 (wire-only).
func TestP02SecretModifySpecSourceKinds(t *testing.T) {
	specType := reflect.TypeOf(msb.SecretModifySpec{})
	for _, want := range []string{"Env", "Value", "Store", "Placeholder"} {
		if field, ok := specType.FieldByName(want); !ok || field.Type.Kind() != reflect.String {
			t.Fatalf("SecretModifySpec.%s missing or not a string: %+v", want, field)
		}
	}
	if field, ok := specType.FieldByName("AllowedHosts"); !ok || field.Type.Kind() != reflect.Slice {
		t.Fatalf("SecretModifySpec.AllowedHosts missing or not a slice: %+v", field)
	}
	byEnv := msb.SecretModifySpec{Env: "HOST_VAR", AllowedHosts: []string{"h.example"}}
	byStore := msb.SecretModifySpec{Store: "vault://prod/db", AllowedHosts: []string{"h.example"}}
	if byEnv.Value != "" || byEnv.Store != "" {
		t.Fatalf("env source must not carry value/store: %+v", byEnv)
	}
	if byStore.Value != "" || byStore.Env != "" {
		t.Fatalf("store source must not carry value/env: %+v", byStore)
	}
}
