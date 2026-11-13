package codebox

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRegisteredBindingsAreValid(t *testing.T) {
	if err := validateSecretBindings(msbSecretBindings()); err != nil {
		t.Fatalf("the built-in registry must be valid: %v", err)
	}
}

func TestBindingForKindResolvesPlatformKinds(t *testing.T) {
	for kind, wantEnv := range map[CredentialKind]string{
		CredentialAlbert:           msbAPISecretEnv,
		CredentialMistral:          MistralAPIKeyEnv,
		CredentialOpenAICompatible: OpenAICompatibleAPIKeyEnv,
	} {
		b, ok := bindingForKind(kind)
		if !ok || !b.Provider || b.GuestEnv != wantEnv {
			t.Fatalf("binding for %s = %+v (ok=%v)", kind, b, ok)
		}
	}
}

func TestValidateSecretBindingsRejectsCrossedDestinations(t *testing.T) {
	base := func() []msbSecretBinding {
		return []msbSecretBinding{
			{Kind: CredentialAlbert, GuestEnv: "ALBERT_API_KEY", HostEnv: "H1", AllowHosts: []string{"albert.example.fr"}},
			{Kind: CredentialGithub, GuestEnv: "GITHUB_TOKEN", HostEnv: "H2", AllowHosts: []string{"github.com"}, Optional: true},
		}
	}
	// A host allowed by two bindings could receive either credential: the
	// "no crossed destinations" invariant (issue #74) forbids it.
	crossed := base()
	crossed[1].AllowHosts = []string{"albert.example.fr"}
	if err := validateSecretBindings(crossed); err == nil || !strings.Contains(err.Error(), "albert.example.fr") {
		t.Fatalf("shared allowed host must be rejected: %v", err)
	}
	// Two bindings exposing the same guest variable would fight over it.
	dup := base()
	dup[1].GuestEnv = "ALBERT_API_KEY"
	if err := validateSecretBindings(dup); err == nil || !strings.Contains(err.Error(), "ALBERT_API_KEY") {
		t.Fatalf("duplicate guest variable must be rejected: %v", err)
	}
	// A non-DNS allow host (wildcard, scheme, bare word) is a config error.
	for _, host := range []string{"*.example.fr", "https://example.fr", "localhost", "example"} {
		bad := base()
		bad[1].AllowHosts = []string{host}
		if err := validateSecretBindings(bad); err == nil {
			t.Fatalf("allow host %q must be rejected", host)
		}
	}
	if err := validateSecretBindings(base()); err != nil {
		t.Fatalf("valid set rejected: %v", err)
	}
}

func TestBindingsRevisionIgnoresValues(t *testing.T) {
	a := testBindings("first-value")
	b := testBindings("rotated-value")
	if bindingsRevision(a) != bindingsRevision(b) {
		t.Fatal("a value rotation within an unchanged set must not move the revision")
	}
	// The source participates: an env-sourced and a store-sourced albert
	// binding are different states (revocation can only reach the latter).
	c := testBindings("first-value")
	c[0].source = bindingSourceStore
	if bindingsRevision(a) == bindingsRevision(c) {
		t.Fatal("the credential source must participate in the revision")
	}
	// Changing the guest-visible placeholder must refresh the proxy binding,
	// even when the stored value and all other binding metadata are unchanged.
	e := testBindings("first-value")
	e[0].Placeholder = "$MSB_PROJECT_API_KEY_rotated"
	if bindingsRevision(a) == bindingsRevision(e) {
		t.Fatal("the guest placeholder must participate in the revision")
	}
	// Adding an optional binding moves the revision.
	d := append(testBindings("first-value"), resolvedBinding{
		msbSecretBinding: msbSecretBindings()[1], source: bindingSourceStore, value: "tok",
	})
	if bindingsRevision(a) == bindingsRevision(d) {
		t.Fatal("adding a binding must move the revision")
	}
}

func TestStoreBoundEntries(t *testing.T) {
	// github, store-sourced from the native store; its entry is its own kind.
	bindings := append(testBindings("k"), resolvedBinding{
		msbSecretBinding: msbSecretBindings()[1], source: bindingSourceStore, value: "tok", store: "native",
	})
	if got := storeBoundEntries(bindings); !reflect.DeepEqual(got, []string{"github@native#github"}) {
		t.Fatalf("storeBoundEntries = %v", got)
	}
	// An Albert binding fed by a differently-named credentialRef records the
	// entry it was read from *and* the guest binding it fed: revocation
	// addresses entries, and "github" here must drop ALBERT_API_KEY, not
	// GITHUB_TOKEN.
	ref := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore,
		value: "k", store: "file", entry: "github",
	}}
	if got := storeBoundEntries(ref); !reflect.DeepEqual(got, []string{"github@file#albert"}) {
		t.Fatalf("credentialRef entry must be recorded with its guest binding: %v", got)
	}
	envOnly := testBindings("k")
	if got := storeBoundEntries(envOnly); got != nil {
		t.Fatalf("env-sourced set must yield no store-bound records: %v", got)
	}
}

func TestParseBoundStoreEntryFormats(t *testing.T) {
	cases := []struct {
		record  string
		form    boundRecordForm
		entry   string
		store   string
		binding CredentialKind
	}{
		{"github@native#albert", boundRecordFull, "github", "native", CredentialAlbert},
		{"albert@file#albert", boundRecordFull, "albert", "file", CredentialAlbert},
		{"albert@native", boundRecordInterim, "albert", "native", CredentialAlbert},
		{"albert", boundRecordLegacy, "albert", "", CredentialAlbert},
		{pendingBoundEntry, boundRecordPending, "", "", ""},
	}
	for _, c := range cases {
		got, form := parseBoundStoreEntry(c.record)
		if form != c.form {
			t.Fatalf("%q: form = %v, want %v", c.record, form, c.form)
		}
		if form == boundRecordPending {
			continue
		}
		if got.Entry != c.entry || got.Store != c.store || got.Binding != c.binding {
			t.Fatalf("%q: %+v", c.record, got)
		}
	}
}

// TestEntryVerdictMapsEntryToGuestBinding pins the Codex P1 on the second
// review: revocation addresses credential-store entries, but what it must
// drop is the guest binding the entry fed — not the binding sharing the
// entry's name.
func TestEntryVerdictMapsEntryToGuestBinding(t *testing.T) {
	// credentialRef "github" feeds ALBERT_API_KEY from the fallback store.
	bound := []string{"github@file#albert"}
	verdict, bindings, known := entryVerdicts(bound, "github", "file")
	if !known || verdict != bindingThisStore || len(bindings) != 1 || bindings[0] != CredentialAlbert {
		t.Fatalf("removing the entry must drop the guest binding it fed: %v, %v, %v", verdict, bindings, known)
	}
	// Editing the native store must not touch a fallback-bound instance.
	verdict, _, known = entryVerdicts(bound, "github", "native")
	if !known || verdict != bindingOther {
		t.Fatalf("another store's entry must be left alone: %v, %v", verdict, known)
	}
	// A record for a different entry does not mention this one.
	if _, _, known := entryVerdicts([]string{"albert@native#albert"}, "github", "native"); known {
		t.Fatal("an unrelated record must not answer for this entry")
	}
	// A legacy bare record has no store, so it cannot be ruled out.
	verdict, bindings, known = entryVerdicts([]string{"albert"}, "albert", "file")
	if !known || verdict != bindingThisStore || len(bindings) != 1 || bindings[0] != CredentialAlbert {
		t.Fatalf("a store-less record must be revoked: %v, %v, %v", verdict, bindings, known)
	}
}

// TestEntryVerdictCollectsEveryFedBinding pins the Codex P1 on the third
// review: one entry can feed several guest bindings at once (credentialRef
// "github" plus an approved GitHub binding), and revocation must remove all
// of them, not just the first recorded.
func TestEntryVerdictCollectsEveryFedBinding(t *testing.T) {
	bound := []string{"github@native#albert", "github@native#github"}
	verdict, bindings, known := entryVerdicts(bound, "github", "native")
	if !known || verdict != bindingThisStore {
		t.Fatalf("the entry is bound in this store: %v, %v, %v", verdict, bindings, known)
	}
	if len(bindings) != 2 || bindings[0] != CredentialAlbert || bindings[1] != CredentialGithub {
		t.Fatalf("both fed bindings must be collected: %v", bindings)
	}
}

// TestCredentialGenerationBumpsOnWrite pins the rotation detection the third
// review asked for: `auth add` bumps a non-secret per-kind counter, so
// reconcile can see a value change without hashing or deriving anything from
// the value itself.
func TestCredentialGenerationBumpsOnWrite(t *testing.T) {
	dir := t.TempDir()
	if n, err := credentialGenerationIn(DefaultFS, dir, CredentialAlbert); err != nil || n != 0 {
		t.Fatalf("initial generation = %d, %v", n, err)
	}
	if err := writeCredentialGeneration(DefaultFS, dir, CredentialAlbert); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialGeneration(DefaultFS, dir, CredentialAlbert); err != nil {
		t.Fatal(err)
	}
	if n, err := credentialGenerationIn(DefaultFS, dir, CredentialAlbert); err != nil || n != 2 {
		t.Fatalf("generation after two writes = %d, %v", n, err)
	}
	// Kinds are independent.
	if n, err := credentialGenerationIn(DefaultFS, dir, CredentialGithub); err != nil || n != 0 {
		t.Fatalf("github generation = %d, %v", n, err)
	}
}

// TestBindingsRevisionIncludesStore pins the second review's P2: when both
// stores hold the same entry, losing the native one changes only which store
// answers, so the revision must move or a healthy instance takes the no-op
// path and never rebinds from the fallback.
func TestBindingsRevisionIncludesStore(t *testing.T) {
	fromNative := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore, value: "k", store: "native", entry: "albert",
	}}
	fromFile := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore, value: "k", store: "file", entry: "albert",
	}}
	if bindingsRevision(fromNative) == bindingsRevision(fromFile) {
		t.Fatal("a native-to-fallback transition must move the revision")
	}
	// A different entry feeding the same binding is also a different state.
	otherEntry := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore, value: "k", store: "native", entry: "github",
	}}
	if bindingsRevision(fromNative) == bindingsRevision(otherEntry) {
		t.Fatal("a different credentialRef entry must move the revision")
	}
}

// TestCredentialGenerationMovesTheRevision pins the third review's P1: a
// value rotation must be visible to reconcile even when the binding set is
// otherwise unchanged. The generation counter participates in the config
// revision, so `auth add` over an existing credential schedules a refresh on
// the next reconcile instead of the no-op path.
func TestCredentialGenerationMovesTheRevision(t *testing.T) {
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	rotated := base
	rotated.CredentialGeneration = "3"
	if base.ConfigRevision() == rotated.ConfigRevision() {
		t.Fatal("a credential generation change must move the revision")
	}
}

func TestWithHostSecretsPublishesAndRestores(t *testing.T) {
	bindings := testBindings("secret-value")
	hostEnv := bindings[0].HostEnv
	os.Unsetenv(hostEnv)
	// A pre-existing variable is restored, not lost.
	t.Setenv("CODEBOX_HOST_PREEXISTING", "original")
	pre := resolvedBinding{msbSecretBinding: msbSecretBinding{
		Kind: CredentialGithub, GuestEnv: "G", HostEnv: "CODEBOX_HOST_PREEXISTING", AllowHosts: []string{"example.fr"},
	}, source: bindingSourceStore, value: "override"}
	all := append(bindings, pre)

	var seen, seenPre string
	err := withHostSecrets(all, func() error {
		seen = os.Getenv(hostEnv)
		seenPre = os.Getenv("CODEBOX_HOST_PREEXISTING")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "secret-value" || seenPre != "override" {
		t.Fatalf("transport variables not published: %q, %q", seen, seenPre)
	}
	if _, ok := os.LookupEnv(hostEnv); ok {
		t.Fatalf("transport variable %s was not removed afterwards", hostEnv)
	}
	if got := os.Getenv("CODEBOX_HOST_PREEXISTING"); got != "original" {
		t.Fatalf("pre-existing variable clobbered: %q", got)
	}
}

func TestResolveAlbertPrecedence(t *testing.T) {
	// The legacy environment wins over the default stored credential and
	// loses to an explicit credentialRef.
	v, src, _, entry, err := resolveAlbert(context.Background(), Config{APIKey: "env-key"}, "")
	if err != nil || v != "env-key" || src != bindingSourceEnv {
		t.Fatalf("env source: %q, %q, %v", v, src, err)
	}
	if entry != "" {
		t.Fatalf("an env-sourced value has no store entry: %q", entry)
	}
	// A credentialRef records the entry it named, so revocation can address
	// it even when it differs from the binding's own kind.
	ref := Config{CredentialRef: "my-albert"}
	v, src, _, entry, err = resolveAlbertWith(func(context.Context, CredentialKind, string) (string, string, error) {
		return "ref-value", "file", nil
	}, context.Background(), ref, "")
	if err != nil || v != "ref-value" || src != bindingSourceStore || entry != "my-albert" {
		t.Fatalf("credentialRef resolution: %q, %q, %q, %v", v, src, entry, err)
	}
	// An explicit credentialRef that names nothing stored is a hard error
	// naming the reference, never a silent fallback to another source.
	_, _, _, _, err = resolveAlbert(context.Background(), Config{
		APIKey:        "env-key",
		CredentialRef: "definitely-not-stored-anywhere",
	}, "")
	if err == nil || !strings.Contains(err.Error(), "definitely-not-stored-anywhere") {
		t.Fatalf("unknown credentialRef must fail by name: %v", err)
	}
}

func TestResolveAlbertTrimsEnvironment(t *testing.T) {
	value, source, _, _, err := resolveAlbertWith(func(context.Context, CredentialKind, string) (string, string, error) {
		t.Fatal("a non-empty trimmed environment value must win without a store read")
		return "", "", nil
	}, context.Background(), Config{APIKey: "  environment-secret \n"}, "")
	if err != nil || value != "environment-secret" || source != bindingSourceEnv {
		t.Fatalf("trimmed environment = %q from %q, err %v", value, source, err)
	}
	value, source, _, _, err = resolveAlbertWith(func(context.Context, CredentialKind, string) (string, string, error) {
		return "stored-secret", "file", nil
	}, context.Background(), Config{APIKey: " \n\t "}, "")
	if err != nil || value != "stored-secret" || source != bindingSourceStore {
		t.Fatalf("whitespace-only environment must fall back to the store: %q from %q, err %v", value, source, err)
	}
}

func TestResolveBindingsSkipsUnapprovedOptionals(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].Kind != CredentialAlbert {
		t.Fatalf("only the required binding may resolve without approval: %+v", bindings)
	}
}

func TestResolveBindingsApprovedOptionalIsBound(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.APIKey = ""
	m.credentialRead = stubReader("stored")
	path := bindingApprovalsPath(m.StateDir, m.InstanceName())
	if err := ApproveBinding(DefaultFS, path, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 || bindings[1].Kind != CredentialGithub || bindings[1].value != "stored-github" {
		t.Fatalf("approved optional binding must resolve from the store: %+v", bindings)
	}
	if bindings[1].source != bindingSourceStore {
		t.Fatalf("optional bindings are store-sourced: %q", bindings[1].source)
	}
}

func TestResolveBindingsApprovedButUnstoredIsSkipped(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	path := bindingApprovalsPath(m.StateDir, m.InstanceName())
	if err := ApproveBinding(DefaultFS, path, CredentialContext7); err != nil {
		t.Fatal(err)
	}
	bindings, err := m.resolveBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 {
		t.Fatalf("an approved-but-unstored optional must be skipped, not fatal: %+v", bindings)
	}
}

func TestApproveBindingRejectsUnknownAndRequired(t *testing.T) {
	path := bindingApprovalsPath(t.TempDir(), "inst")
	if err := ApproveBinding(DefaultFS, path, CredentialKind("gitlab")); err == nil {
		t.Fatal("unknown kind approved")
	}
	if err := ApproveBinding(DefaultFS, path, CredentialAlbert); err == nil {
		t.Fatal("the required albert binding needs no approval and must be rejected")
	}
	if err := ApproveBinding(DefaultFS, path, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	a, err := ReadBindingApprovals(DefaultFS, path)
	if err != nil || !a.Approves(CredentialGithub) {
		t.Fatalf("approval round trip: %+v, %v", a, err)
	}
	if err := RevokeBindingApproval(DefaultFS, path, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	a, err = ReadBindingApprovals(DefaultFS, path)
	if err != nil || a.Approves(CredentialGithub) {
		t.Fatalf("revocation round trip: %+v, %v", a, err)
	}
}

// TestStartCreateRotatesSentinelBeforeLaunch pins the create-path ordering:
// create (sentinel) -> rotate to env reference -> launch. A rotation failure
// must remove the incomplete sandbox.
func TestStartCreateRotatesSentinelBeforeLaunch(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var createIdx, rotateIdx, execIdx = -1, -1, -1
	for i, call := range client.calls {
		switch {
		case call == "create":
			createIdx = i
		case call == "rotate "+msbSandbox:
			rotateIdx = i
		case strings.HasPrefix(call, "exec "):
			if execIdx == -1 {
				execIdx = i
			}
		}
	}
	if createIdx == -1 || rotateIdx == -1 || execIdx == -1 || !(createIdx < rotateIdx && rotateIdx < execIdx) {
		t.Fatalf("expected create -> rotate -> launch ordering: %v", client.calls)
	}
}

func TestStartCreateRotationFailureRemovesSandbox(t *testing.T) {
	client := &fakeMSBClient{modifyErr: errTest}
	m := newTestMicrosandbox(t, client)
	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("rotation failure must be reported with the rollback: %v", err)
	}
	if !hasCall(client, "remove "+msbSandbox) {
		t.Fatalf("the incomplete sandbox must be removed: %v", client.calls)
	}
	if hasCall(client, "exec ") {
		t.Fatalf("the start script must not run on an unrotated sandbox: %v", client.calls)
	}
}

// TestStaleBindingIsRemovedFromTheProxy pins the Codex P1 on PR #87: the SDK
// modification is a patch, so omitting a binding never removes it. An
// optional binding whose approval is lifted must be named in SecretsRemove,
// or its proxy registration (and therefore its credential) survives every
// later refresh and restart.
func TestStaleBindingIsRemovedFromTheProxy(t *testing.T) {
	// Only albert is desired; github and context7 are approved nowhere.
	options := msbNextStartOptions(nil, bindingsMetadata(testBindings("k")), nil)
	for _, guestEnv := range []string{"GITHUB_TOKEN", "CONTEXT7_API_KEY", MistralAPIKeyEnv, OpenAICompatibleAPIKeyEnv} {
		if !containsString(options.SecretsRemove, guestEnv) {
			t.Fatalf("%s must be revoked in the same modification: %v", guestEnv, options.SecretsRemove)
		}
		if _, present := options.Secrets[guestEnv]; present {
			t.Fatalf("%s must not be re-registered: %+v", guestEnv, options.Secrets)
		}
	}
	if containsString(options.SecretsRemove, msbAPISecretEnv) {
		t.Fatalf("a desired binding must never be in SecretsRemove: %v", options.SecretsRemove)
	}
	// Every managed guest variable is still scrubbed from the guest
	// environment, which is what removes a raw value an earlier version
	// persisted there.
	for _, guestEnv := range []string{msbAPISecretEnv, "GITHUB_TOKEN", "CONTEXT7_API_KEY", MistralAPIKeyEnv, OpenAICompatibleAPIKeyEnv} {
		if !containsString(options.EnvRemove, guestEnv) {
			t.Fatalf("%s must stay in EnvRemove: %v", guestEnv, options.EnvRemove)
		}
	}
	// With every platform and both optionals present, nothing is stale.
	providers, err := ResolveProviders([]ProviderSettings{
		{Kind: ProviderAlbert}, {Kind: ProviderMistral},
		{Kind: ProviderOpenAICompatible, Name: "Scaleway", BaseURL: "https://api.scaleway.ai/v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := msbSecretBindingsFor(providers)
	if got := msbNextStartOptions(nil, all, nil).SecretsRemove; len(got) != 0 {
		t.Fatalf("nothing may be stale when every binding is desired: %v", got)
	}
}

// TestUnresolvedBindingSetCarriesAppliedState pins the companion decision:
// a refresh built from an unresolved (empty) binding set must not strip the
// references the guest already holds, and must keep the bound-store markers
// so a later revocation still knows what the instance resolved from.
func TestUnresolvedBindingSetCarriesAppliedState(t *testing.T) {
	applied := &InstanceState{CredentialRev: "abc", BoundCredentials: []string{"albert@native"}}
	d := (&MicrosandboxRuntime{}).desiredState(false, nil, applied)
	// The revision is carried, so the set is not classified as changed by
	// an absence that only reflects a missing credential.
	if d.CredentialRev != "abc" {
		t.Fatalf("unresolved apply must carry the applied revision: %+v", d)
	}
	if !reflect.DeepEqual(d.BoundCredentials, []string{"albert@native"}) {
		t.Fatalf("bound-store markers must survive: %v", d.BoundCredentials)
	}
	// With nothing applied, the unknown marker keeps revocation from
	// skipping an instance an earlier apply may have bound.
	bare := (&MicrosandboxRuntime{}).desiredState(false, nil, nil)
	if !reflect.DeepEqual(bare.BoundCredentials, []string{pendingBoundEntry}) {
		t.Fatalf("unresolved apply without state must record the marker: %v", bare.BoundCredentials)
	}
}

var errTest = errorString("test failure")

type errorString string

func (e errorString) Error() string { return string(e) }

// stubReader returns a credentialReader that answers every kind from one
// store, so resolution and revocation paths can be driven without a keychain.
func stubReader(value string) credentialReader {
	return func(_ context.Context, kind CredentialKind, from string) (string, string, error) {
		if value == "" {
			return "", "", ErrCredentialNotFound
		}
		store := from
		if store == "" {
			store = "native"
		}
		return value + "-" + string(kind), store, nil
	}
}

// TestReadStoredWithNoSilentFallback pins the third review's P1: a pinned
// native store that answers "unavailable" must surface that error even when a
// consented file store holds the kind. Only a genuine not-found continues to
// the fallback; anything else is a state the runtime must not paper over.
func TestReadStoredWithNoSilentFallback(t *testing.T) {
	unavailable := storeErrorf("get", "secret-service", "unavailable", "daemon down")
	// Pinned native + fallback answering: the native error must surface.
	_, _, err := readStoredWithNative(context.Background(), CredentialAlbert,
		func(context.Context, CredentialKind) (string, error) { return "", unavailable },
		func(context.Context, CredentialKind) (string, error) { return "fallback-value", nil })
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("the native store's unavailable state must surface: %v", err)
	}
	// Native not-found + fallback holding the entry: the fallback is the
	// documented continuation.
	v, store, err := readStoredWithNative(context.Background(), CredentialAlbert,
		func(context.Context, CredentialKind) (string, error) { return "", ErrCredentialNotFound },
		func(context.Context, CredentialKind) (string, error) { return "fallback-value", nil })
	if err != nil || v != "fallback-value" || store != "file" {
		t.Fatalf("not-found must continue to the fallback: %q, %q, %v", v, store, err)
	}
	// Native locked: hard error, the same no-silent-downgrade contract.
	locked := storeErrorf("get", "secret-service", "locked", "collection locked")
	_, _, err = readStoredWithNative(context.Background(), CredentialAlbert,
		func(context.Context, CredentialKind) (string, error) { return "", locked },
		func(context.Context, CredentialKind) (string, error) { return "fallback-value", nil })
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a locked native store must surface: %v", err)
	}
}

// TestResolveBindingsApprovedOptionalHardError pins the fourth review's P2:
// a locked/denied/unavailable/corrupt store on an APPROVED optional binding
// aborts the resolution. Skipping it would make the omission the desired
// set, and the refresh would strip the persisted registration.
func TestResolveBindingsApprovedOptionalHardError(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.credentialRead = func(_ context.Context, kind CredentialKind, from string) (string, string, error) {
		if kind == CredentialGithub {
			return "", "", storeErrorf("get", "secret-service", "locked", "collection locked")
		}
		return "value", "native", nil
	}
	path := bindingApprovalsPath(m.StateDir, m.InstanceName())
	if err := ApproveBinding(DefaultFS, path, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	_, err := m.resolveBindings(context.Background())
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("a locked store on an approved binding must abort, not skip: %v", err)
	}
}

// TestStoreGenerationComposite pins the fourth review's P1: the rotation
// marker must cover EVERY store-sourced binding, so rotating an approved
// optional credential moves the composite even though Albert resolves first.
func TestStoreGenerationComposite(t *testing.T) {
	fs, stateDir := DefaultFS, t.TempDir()
	// Albert store-sourced (generation 1) + github approved (generation 5).
	albert := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore,
		value: "k", store: "native", entry: "albert",
	}}
	github := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[1], source: bindingSourceStore,
		value: "tok", store: "native", entry: "github",
	}}
	both := append(append([]resolvedBinding{}, albert...), github...)

	base := storeGenerationOf(fs, stateDir, both)
	if base == "" || !strings.Contains(base, "albert@native") || !strings.Contains(base, "github@native") {
		t.Fatalf("the composite must cover every store-sourced binding: %q", base)
	}
	// Bumping only the github generation must move the composite.
	if err := writeCredentialGeneration(fs, stateDir, CredentialGithub); err != nil {
		t.Fatal(err)
	}
	rotated := storeGenerationOf(fs, stateDir, both)
	if base == rotated {
		t.Fatal("rotating a non-first binding must move the composite marker")
	}
	// Removing the github binding changes the composite too.
	if storeGenerationOf(fs, stateDir, albert) == base {
		t.Fatal("dropping a binding must change the composite marker")
	}
}

// TestStoreGenerationFollowsTheResolvedStore pins the fifth review's P1: the
// generation source must be the store the binding resolved from. A stale
// fallback generation (entries survive Remove in the file store) must not
// mask a native rotation.
func TestStoreGenerationFollowsTheResolvedStore(t *testing.T) {
	fs, stateDir := DefaultFS, t.TempDir()
	native := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore,
		value: "k", store: "native", entry: "albert",
	}}
	// Host-side counter (what a native-sourced binding reads) at N.
	if err := writeCredentialGeneration(fs, stateDir, CredentialAlbert); err != nil {
		t.Fatal(err)
	}
	base := storeGenerationOf(fs, stateDir, native)
	if !strings.Contains(base, "albert@native:1") {
		t.Fatalf("a native-sourced binding reads the host-side counter: %q", base)
	}
	// A native rotation moves the composite even though the file store
	// exists and would answer a generation for the same entry.
	if err := writeCredentialGeneration(fs, stateDir, CredentialAlbert); err != nil {
		t.Fatal(err)
	}
	rotated := storeGenerationOf(fs, stateDir, native)
	if base == rotated {
		t.Fatal("a native rotation must move the composite marker")
	}
	// The file store's own counter participates only for file-sourced
	// bindings.
	fromFile := []resolvedBinding{{
		msbSecretBinding: msbSecretBindings()[0], source: bindingSourceStore,
		value: "k", store: "file", entry: "albert",
	}}
	if got := storeGenerationOf(fs, stateDir, fromFile); !strings.Contains(got, "albert@file:") {
		t.Fatalf("a file-sourced binding must use the file store's counter: %q", got)
	}
}

func TestProjectSecretsCannotShadowAProviderVariable(t *testing.T) {
	reserved := reservedGuestEnvs()
	for _, env := range []string{msbAPISecretEnv, MistralAPIKeyEnv, OpenAICompatibleAPIKeyEnv, "GITHUB_TOKEN", "CONTEXT7_API_KEY"} {
		if !reserved[env] {
			t.Fatalf("%s must be reserved against project-secret shadowing", env)
		}
	}
}
