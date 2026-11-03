package justcode

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSetupStore is an in-memory SetupStore for wizard tests.
type fakeSetupStore struct {
	verifyErr error
	putErr    error
	put       map[string]string
}

func (f *fakeSetupStore) Kind() string { return "fake" }
func (f *fakeSetupStore) Verify(ctx context.Context) error {
	return f.verifyErr
}
func (f *fakeSetupStore) Put(ctx context.Context, kind CredentialKind, value string) error {
	if f.put == nil {
		f.put = map[string]string{}
	}
	f.put[string(kind)] = value
	return f.putErr
}

func TestSetupJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	j, err := ReadSetupJournal(DefaultFS, dir)
	if err != nil || j.Stage != StagePreflight {
		t.Fatalf("fresh journal: %+v, %v", j, err)
	}
	j.Stage = StageCredential
	j.CredentialKinds = []string{"albert"}
	verified := true
	j.AlbertVerified = &verified
	j.GitHubLogin = "kaaloo"
	j.GitHubVerified = &verified
	j.GitName = "N"
	j.DefaultModel = "m"
	if err := WriteSetupJournal(DefaultFS, dir, j); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSetupJournal(DefaultFS, dir)
	if err != nil || got.Stage != StageCredential || len(got.CredentialKinds) != 1 || got.GitName != "N" || got.AlbertVerified == nil || !*got.AlbertVerified || got.GitHubLogin != "kaaloo" || got.GitHubVerified == nil || !*got.GitHubVerified {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	if err := RemoveSetupJournal(DefaultFS, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SetupJournalPath(dir)); !os.IsNotExist(err) {
		t.Fatal("journal must be removed")
	}
}

func TestLegacySetupJournalCredentialStatusIsUnknown(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"schemaVersion":1,"stage":"credential","credentialKinds":["albert","github"]}`)
	if err := os.WriteFile(SetupJournalPath(dir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSetupJournal(DefaultFS, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.AlbertVerified != nil || got.GitHubVerified != nil || got.GitHubLogin != "" {
		t.Fatalf("legacy journal should preserve unknown credential state, got %+v", got)
	}
}

func TestSetupJournalCorruptFileNamesPathAndRemedy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(SetupJournalPath(dir), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSetupJournal(DefaultFS, dir)
	if err == nil {
		t.Fatal("a corrupt journal must fail")
	}
	if !stringsContainsLower(err.Error(), "remove") || !stringsContainsLower(err.Error(), SetupJournalPath(dir)) {
		t.Fatalf("the error must name the path and the remedy: %v", err)
	}
}

func TestStoreCredentialJournalsKindNeverValue(t *testing.T) {
	dir := t.TempDir()
	store := &fakeSetupStore{}
	w := SetupWizard{FS: DefaultFS, StateDir: dir, Store: func(context.Context) (SetupStore, error) {
		return store, nil
	}}
	probe, err := w.StoreCredential(context.Background(), CredentialAlbert, "the-secret-value")
	if err != nil || probe.Rejected || probe.Unreachable {
		t.Fatalf("store: %+v, %v", probe, err)
	}
	got := mustJournal(t, dir)
	for _, k := range got.CredentialKinds {
		if k == "the-secret-value" {
			t.Fatal("the journal must never contain the credential value")
		}
	}
	if len(got.CredentialKinds) != 1 || got.CredentialKinds[0] != "albert" {
		t.Fatalf("the kind must be journaled: %+v", got.CredentialKinds)
	}
	// Structural check too: the raw file must not embed the value anywhere
	// (not just in the kinds field).
	raw, err := os.ReadFile(SetupJournalPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("the-secret-value")) {
		t.Fatal("the journal file must never contain the credential value")
	}
}

func mustJournal(t *testing.T, dir string) SetupJournal {
	t.Helper()
	j, err := ReadSetupJournal(DefaultFS, dir)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestStoreCredentialVerifyBeforePut(t *testing.T) {
	dir := t.TempDir()
	store := &fakeSetupStore{verifyErr: fmt.Errorf("locked")}
	w := SetupWizard{FS: DefaultFS, StateDir: dir, Store: func(context.Context) (SetupStore, error) {
		return store, nil
	}}
	if _, err := w.StoreCredential(context.Background(), CredentialAlbert, "v"); err == nil {
		t.Fatal("a locked store must fail before the credential is typed and stored")
	}
	if len(store.put) != 0 {
		t.Fatal("nothing may be stored when verification fails")
	}
}

func TestStoreCredentialDistinguishesRejectionFromNetwork(t *testing.T) {
	dir := t.TempDir()
	// Rejection: returned as probe.Rejected with no error, nothing stored
	// until the user retries.
	w := SetupWizard{
		FS: DefaultFS, StateDir: dir,
		Store: func(context.Context) (SetupStore, error) { return &fakeSetupStore{}, nil },
		ValidateAlbert: func(context.Context, string) error {
			return RejectedCredentialError{Detail: "HTTP 401"}
		},
	}
	probe, err := w.StoreCredential(context.Background(), CredentialAlbert, "bad")
	if err != nil {
		t.Fatalf("rejection is a probe, not a hard error: %v", err)
	}
	if !probe.Rejected || probe.Unreachable {
		t.Fatalf("probe = %+v", probe)
	}
	// Unreachable: the credential is stored (the user decides), and the
	// probe reports the network state.
	w.ValidateAlbert = func(context.Context, string) error {
		return fmt.Errorf("dial tcp: connection refused")
	}
	probe, err = w.StoreCredential(context.Background(), CredentialAlbert, "good")
	if err != nil || probe.Rejected || !probe.Unreachable {
		t.Fatalf("unreachable probe = %+v, %v", probe, err)
	}
}

func TestValidateGitHubTokenOutcomes(t *testing.T) {
	// A 401 (or 403) is a refusal: the token is wrong or revoked.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	probe := validateGitHubTokenAt(context.Background(), nil, "github_pat_x", srv.URL)
	if !probe.Rejected || probe.Unreachable {
		t.Fatalf("401 probe = %+v", probe)
	}

	// A valid fine-grained token reports the account and no scope warning.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"login":"moncompte"}`)
	}))
	defer srv2.Close()
	probe = validateGitHubTokenAt(context.Background(), nil, "github_pat_x", srv2.URL)
	if probe.Rejected || probe.Unreachable || probe.Login != "moncompte" || probe.BroadScope {
		t.Fatalf("fine-grained probe = %+v", probe)
	}

	// A valid token without the fine-grained prefix (classic ghp_, legacy
	// hex, OAuth) is broad-scope: the wizard warns.
	for _, token := range []string{"ghp_classic", "0123456789abcdef0123456789abcdef01234567", "gho_oauth"} {
		probe = validateGitHubTokenAt(context.Background(), nil, token, srv2.URL)
		if probe.Rejected || probe.Unreachable || !probe.BroadScope {
			t.Fatalf("classic %q probe = %+v", token, probe)
		}
	}

	// A redirect must never be followed: the Authorization header would be
	// replayed to another host. It reports unreachable, not valid.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
	}))
	defer srv3.Close()
	probe = validateGitHubTokenAt(context.Background(), srv3.Client(), "github_pat_x", srv3.URL)
	if !probe.Unreachable || probe.Rejected {
		t.Fatalf("redirect probe = %+v", probe)
	}

	// A 200 without a login (captive portal, HTML error page) is not a
	// validated token.
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>login</html>`)
	}))
	defer srv4.Close()
	probe = validateGitHubTokenAt(context.Background(), nil, "github_pat_x", srv4.URL)
	if !probe.Unreachable || probe.Rejected {
		t.Fatalf("non-user probe = %+v", probe)
	}
}

func TestStoreCredentialGitHubUsesTheGitHubProbe(t *testing.T) {
	dir := t.TempDir()
	called := ""
	w := SetupWizard{
		FS: DefaultFS, StateDir: dir,
		Store: func(context.Context) (SetupStore, error) { return &fakeSetupStore{}, nil },
		ValidateGitHub: func(_ context.Context, token string) CredentialProbe {
			called = token
			return CredentialProbe{Rejected: true, Detail: "HTTP 401"}
		},
	}
	probe, err := w.StoreCredential(context.Background(), CredentialGithub, "tok")
	if err != nil {
		t.Fatalf("rejection is a probe, not a hard error: %v", err)
	}
	if called != "tok" {
		t.Fatalf("the GitHub probe was not called with the token: %q", called)
	}
	if !probe.Rejected {
		t.Fatalf("probe = %+v", probe)
	}
}

func TestValidateAlbertKeyRejectsRedirectsAndNonCatalogueBodies(t *testing.T) {
	// A redirect must never be followed: the Authorization header would be
	// replayed to another host. It reports unreachable, not valid.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/", http.StatusFound)
	}))
	defer srv.Close()
	probe := validateAlbertKeyAt(context.Background(), srv.Client(), "k", srv.URL)
	if !probe.Unreachable || probe.Rejected {
		t.Fatalf("redirect probe = %+v", probe)
	}
	// A 200 with a non-catalogue body (captive portal, HTML error page) is
	// not a validated key.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>login</html>")
	}))
	defer srv2.Close()
	probe = validateAlbertKeyAt(context.Background(), nil, "k", srv2.URL)
	if !probe.Unreachable || probe.Rejected {
		t.Fatalf("non-catalogue probe = %+v", probe)
	}
	// A 200 with a real catalogue listing validates.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}))
	defer srv3.Close()
	probe = validateAlbertKeyAt(context.Background(), nil, "k", srv3.URL)
	if probe.Unreachable || probe.Rejected {
		t.Fatalf("catalogue probe = %+v", probe)
	}
}

func TestPreflightFatalWithoutKVM(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the /dev/kvm probe is Linux-specific")
	}
	if _, err := os.Stat("/dev/kvm"); err == nil {
		t.Skip("host has KVM; the fatal path cannot be exercised")
	}
	// An empty RuntimeHome forces the not-installed path, so the primitive
	// probe runs even when the developer machine has a real runtime.
	empty := t.TempDir()
	p := SetupPreflighter{RuntimeHome: empty}
	res := p.RunPreflight(context.Background())
	if res.VirtualizationOK {
		t.Fatalf("expected the missing KVM to be fatal: %+v", res)
	}
	if !stringsContainsLower(res.VirtualizationDetail, "/dev/kvm") {
		t.Fatalf("detail must name the missing primitive: %q", res.VirtualizationDetail)
	}
	if !res.Fatal {
		t.Fatal("a missing virtualization primitive must be fatal")
	}
}

func TestPreflightDiskProbeSeam(t *testing.T) {
	p := SetupPreflighter{
		Doctor: func(context.Context) (string, error) { return "KVM: available", nil },
		StatFS: func(string) (int64, error) { return 4 << 30, nil },
	}
	// Pretend the runtime is installed by pointing RuntimeHome at a dir
	// with a trusted marker; simpler: the doctor seam still runs only when
	// installed — use RuntimeHome to an empty dir (not installed) and check
	// the primitive path on this host, then assert disk.
	res := p.RunPreflight(context.Background())
	if !res.DiskOK || res.DiskFreeBytes != 4<<30 {
		t.Fatalf("disk = %+v", res)
	}
}

// strings helper for case-insensitive contains (test-only).
func stringsContainsLower(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// TestInstallRuntimeCompletesAndClearsJournal pins resumability: after a
// successful runtime install, the journal is removed; a failed install
// (injectable) leaves it for the retry to skip the credential prompts.
func TestInstallRuntimeCompletesAndClearsJournal(t *testing.T) {
	dir := t.TempDir()
	// Journal mid-flow: credential stored, identity saved.
	j := SetupJournal{Stage: StageCredential, CredentialKinds: []string{"albert"}, GitName: "N", GitEmail: "e"}
	if err := WriteSetupJournal(DefaultFS, dir, j); err != nil {
		t.Fatal(err)
	}
	fail := fmt.Errorf("download interrupted")
	w := SetupWizard{FS: DefaultFS, StateDir: dir, EnsureRuntime: func(context.Context) error { return fail }}
	if err := w.InstallRuntime(context.Background()); err == nil {
		t.Fatal("a failed install must surface its error")
	}
	// Journal survives the failure: the retry resumes without re-asking.
	if _, err := ReadSetupJournal(DefaultFS, dir); err != nil {
		t.Fatalf("journal must survive a failed install: %v", err)
	}
	w.EnsureRuntime = func(context.Context) error { return nil }
	if err := w.InstallRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(SetupJournalPath(dir)); !os.IsNotExist(err) {
		t.Fatal("a completed setup must remove the journal")
	}
}

func TestSetupSaveSettingsPersistsProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))

	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderAlbert}, {Kind: ProviderMistral}})
	if err != nil {
		t.Fatal(err)
	}
	w := SetupWizard{FS: DefaultFS, StateDir: t.TempDir()}
	models := []string{"Qwen/Qwen3-Coder-30B-A3B-Instruct", "mistral/mistral-large-latest"}
	if err := w.SaveSettings("mistral/mistral-large-latest", models, true, providers, false, false); err != nil {
		t.Fatal(err)
	}
	path, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := ReadUserSettings(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(us.Providers) != 2 || us.Providers[1].Kind != ProviderMistral {
		t.Fatalf("providers = %+v", us.Providers)
	}
	if us.DefaultModel != "mistral/mistral-large-latest" || len(us.Models) != 2 {
		t.Fatalf("model settings = %q %v", us.DefaultModel, us.Models)
	}
	if us.Providers[0].BaseURL != albertBaseURL {
		t.Fatalf("resolved platform endpoints must be persisted: %+v", us.Providers[0])
	}
}

func TestSetupSaveSettingsUsesStoredAlbertBeforeEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderAlbert}})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	j := SetupJournal{SchemaVersion: 1, Stage: StageCredential, CredentialKinds: []string{string(CredentialAlbert)}}
	if err := WriteSetupJournal(DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	w := SetupWizard{FS: DefaultFS, StateDir: stateDir}
	if err := w.SaveSettings("", nil, false, providers, false, false); err != nil {
		t.Fatal(err)
	}
	path, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	us, err := ReadUserSettings(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if us.CredentialRef != string(CredentialAlbert) {
		t.Fatalf("credentialRef = %q, want albert", us.CredentialRef)
	}
	value, source, _, _, err := resolveAlbertWith(func(context.Context, CredentialKind, string) (string, string, error) {
		return "stored-secret", "file", nil
	}, context.Background(), Config{APIKey: "environment-secret", CredentialRef: us.CredentialRef}, "")
	if err != nil || value != "stored-secret" || source != bindingSourceStore {
		t.Fatalf("resolution = %q from %q, err %v; want the stored replacement", value, source, err)
	}
}

func TestSetupSaveSettingsPreservesCustomCredentialRef(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	path, err := UserSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteUserSettings(DefaultFS, path, UserSettings{SchemaVersion: 1, CredentialRef: "work"}); err != nil {
		t.Fatal(err)
	}
	providers, err := ResolveProviders([]ProviderSettings{{Kind: ProviderAlbert}})
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	j := SetupJournal{SchemaVersion: 1, Stage: StageCredential, CredentialKinds: []string{string(CredentialAlbert)}}
	if err := WriteSetupJournal(DefaultFS, stateDir, j); err != nil {
		t.Fatal(err)
	}
	w := SetupWizard{FS: DefaultFS, StateDir: stateDir}
	if err := w.SaveSettings("", nil, false, providers, false, false); err != nil {
		t.Fatal(err)
	}
	us, err := ReadUserSettings(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if us.CredentialRef != "work" {
		t.Fatalf("custom credentialRef was replaced: %q", us.CredentialRef)
	}
}
