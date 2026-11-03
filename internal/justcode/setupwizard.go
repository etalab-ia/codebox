package justcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file implements the P11 setup engine. The CLI layer (cmd/just-code)
// renders the prompts; this layer owns the ordered operations and the
// journal transitions, so the flow is testable without a terminal. Every
// operation is one from a preceding PR (P08 store, P10 catalogue): the
// wizard composes real domain operations, it does not reimplement them.

// SetupStore abstracts the credential-store operations the wizard needs.
// Production uses the real stores via the P08 path; tests inject fakes.
type SetupStore interface {
	Kind() string
	Verify(ctx context.Context) error
	Put(ctx context.Context, kind CredentialKind, value string) error
}

// SetupWizard drives the stages over injectable operations.
type SetupWizard struct {
	FS       FS
	StateDir string
	// Store resolves the credential store for the Albert (and optional
	// GitHub) credential.
	Store func(ctx context.Context) (SetupStore, error)
	// ValidateAlbert probes the Albert endpoint with the candidate key.
	// Nil skips validation (tests decide the outcome explicitly).
	ValidateAlbert func(ctx context.Context, key string) error
	// ValidateProvider probes an inference platform's models endpoint with
	// the candidate key. Nil uses the built-in probe for the platform kind.
	ValidateProvider func(ctx context.Context, provider InferenceProvider, key string) CredentialProbe
	// ValidateGitHub probes the GitHub API with the candidate token. Nil
	// skips validation (tests decide the outcome explicitly).
	ValidateGitHub func(ctx context.Context, token string) CredentialProbe
	// EnsureRuntime installs the managed runtime when needed. Nil uses
	// ensureMSBRuntime.
	EnsureRuntime func(ctx context.Context) error
}

// ResolveStore resolves and verifies the credential store without storing
// anything, so the CLI can fail on a locked or unavailable store BEFORE the
// user types a secret. A consented-but-absent file store is the normal
// first-run state and passes: the first Put creates it.
func (w SetupWizard) ResolveStore(ctx context.Context) (SetupStore, error) {
	store, err := w.store(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.Verify(ctx); err != nil {
		if !errors.Is(err, ErrFileStoreAbsent) && !errors.Is(err, ErrFileStoreNotConsented) {
			return nil, err
		}
	}
	return store, nil
}

// CredentialProbe distinguishes the validation outcomes the plan requires:
// a rejected key is reported as rejected (the user retries), an unreachable
// endpoint is reported as network trouble (the user may continue or stop),
// and neither blocks the store write when validation is unavailable.
type CredentialProbe struct {
	Rejected    bool
	Unreachable bool
	// Incompatible marks a reachable endpoint whose answer is not a models
	// listing (an HTML page, a captive portal, a different API). It applies
	// to OpenAI-compatible platforms, where the user supplies the URL.
	Incompatible bool
	Detail       string
	// Login is the account the credential authenticates as, when the
	// endpoint reports it (GitHub GET /user). Empty when unknown.
	Login string
	// BroadScope marks a valid credential broader than the recommended
	// kind (a GitHub token without the fine-grained prefix). The caller
	// decides whether to warn, accept, or re-ask.
	BroadScope bool
}

// ValidateAlbertKey probes the models endpoint with the key: a 401/403 is a
// rejection (the key is wrong or revoked), other transport errors are
// unreachable, and a 200 with a models listing means the key is valid.
// Redirects are NOT followed: the Authorization header must never be replayed
// to another host, so a 3xx is reported as unreachable (the endpoint moved
// or is misconfigured, which is a configuration problem, not a key problem).
// No chat completion is ever performed — validating with a catalogue listing
// never spends inference.
func ValidateAlbertKey(ctx context.Context, client *http.Client, key string) CredentialProbe {
	return validateAlbertKeyAt(ctx, client, key, albertCatalogueURL)
}

// noRedirectClient returns a client that never follows redirects: the
// Authorization header must never be replayed to another host. A
// caller-supplied client is cloned rather than mutated (it is often shared —
// the CLI reuses one per process), and an explicit caller policy is
// respected.
func noRedirectClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	c := *client
	if c.CheckRedirect == nil {
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return &c
}

// validateAlbertKeyAt is ValidateAlbertKey against an explicit URL (tests).
func validateAlbertKeyAt(ctx context.Context, client *http.Client, key, url string) CredentialProbe {
	client = noRedirectClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		// Sniff the body: the models endpoint returns a JSON object with a
		// "data" array. A 200 with anything else (a captive portal, an HTML
		// error page) is not a validated key.
		var raw struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
			return CredentialProbe{Unreachable: true, Detail: "HTTP 200 with a non-catalogue body (endpoint misconfigured?)"}
		}
		if raw.Data == nil {
			return CredentialProbe{Unreachable: true, Detail: "HTTP 200 with a non-catalogue body (no models data)"}
		}
		return CredentialProbe{}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d (redirect not followed; the Authorization header is never replayed to another host)", resp.StatusCode)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return CredentialProbe{Rejected: true, Detail: fmt.Sprintf("HTTP %d: the key was not accepted", resp.StatusCode)}
	default:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
}

// githubUserURL is the endpoint a candidate token is validated against:
// GET /user answers the authenticated account, which is all the wizard
// needs. Checking the account never spends anything on the token's rights.
const githubUserURL = "https://api.github.com/user"

// githubFineGrainedPrefix marks fine-grained personal access tokens, the
// only token kind limited to chosen repositories. Every other valid token
// (classic ghp_, legacy 40-char hex, OAuth) carries broad rights; the
// wizard's warning and remediation are the same for all of them.
const githubFineGrainedPrefix = "github_pat_"

// ValidateGitHubToken probes the GitHub API with the candidate token: a
// 401/403 is a rejection (the token is wrong or revoked), other transport
// errors are unreachable, and a 200 with a login means the token is valid.
// Redirects are NOT followed, same contract as ValidateAlbertKey. A valid
// token without the fine-grained prefix is reported as BroadScope.
func ValidateGitHubToken(ctx context.Context, client *http.Client, token string) CredentialProbe {
	return validateGitHubTokenAt(ctx, client, token, githubUserURL)
}

// validateGitHubTokenAt is ValidateGitHubToken against an explicit URL
// (tests).
func validateGitHubTokenAt(ctx context.Context, client *http.Client, token, url string) CredentialProbe {
	client = noRedirectClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		// Sniff the body: /user returns a JSON object with the account
		// login. A 200 with anything else (a captive portal, an HTML error
		// page) is not a validated token.
		var raw struct {
			Login string `json:"login"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&raw); err != nil {
			return CredentialProbe{Unreachable: true, Detail: "HTTP 200 with a non-user body (endpoint misconfigured?)"}
		}
		if raw.Login == "" {
			return CredentialProbe{Unreachable: true, Detail: "HTTP 200 with a non-user body (no login)"}
		}
		probe := CredentialProbe{Login: raw.Login}
		if !strings.HasPrefix(token, githubFineGrainedPrefix) {
			probe.BroadScope = true
		}
		return probe
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d (redirect not followed; the Authorization header is never replayed to another host)", resp.StatusCode)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return CredentialProbe{Rejected: true, Detail: fmt.Sprintf("HTTP %d: the token was not accepted", resp.StatusCode)}
	default:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
}

// ValidateCredential probes one candidate credential WITHOUT storing it, so a
// caller can ask about an unreachable endpoint before anything is written. A
// refusal is returned as a probe outcome (never an error); an unreachable
// endpoint is a distinct signal. An unavailable or locked store still fails
// here, before the user is asked anything.
func (w SetupWizard) ValidateCredential(ctx context.Context, kind CredentialKind, value string) (CredentialProbe, error) {
	store, err := w.store(ctx)
	if err != nil {
		return CredentialProbe{}, err
	}
	// Probe the store first: an unavailable or locked store fails BEFORE
	// the user types anything (the auth add contract, kept here).
	if err := store.Verify(ctx); err != nil {
		// An absent file store is a normal first-run state for the
		// consented fallback: the wizard's own Put creates it. Any other
		// failure (locked, denied, unavailable) stays fatal.
		if !errors.Is(err, ErrFileStoreAbsent) && !errors.Is(err, ErrFileStoreNotConsented) {
			return CredentialProbe{}, err
		}
	}
	return w.probeCredential(ctx, kind, value), nil
}

// ValidateProviderCredential is ValidateCredential for one resolved platform.
// It resolves the store first (an unavailable store must fail before the user
// types anything) and then probes the platform's own models endpoint.
func (w SetupWizard) ValidateProviderCredential(ctx context.Context, provider InferenceProvider, value string) (CredentialProbe, error) {
	store, err := w.store(ctx)
	if err != nil {
		return CredentialProbe{}, err
	}
	if err := store.Verify(ctx); err != nil {
		if !errors.Is(err, ErrFileStoreAbsent) && !errors.Is(err, ErrFileStoreNotConsented) {
			return CredentialProbe{}, err
		}
	}
	return w.probeProvider(ctx, provider, value), nil
}

// probeProvider is the validation half of a platform credential store: it
// never writes.
func (w SetupWizard) probeProvider(ctx context.Context, provider InferenceProvider, value string) CredentialProbe {
	if w.ValidateProvider != nil {
		return w.ValidateProvider(ctx, provider, value)
	}
	return ValidateProviderKey(ctx, nil, provider, value)
}

// probeCredential is the validation half of StoreCredential: it never writes.
func (w SetupWizard) probeCredential(ctx context.Context, kind CredentialKind, value string) CredentialProbe {
	switch kind {
	case CredentialAlbert:
		if w.ValidateAlbert == nil {
			return CredentialProbe{}
		}
		if verr := w.ValidateAlbert(ctx, value); verr != nil {
			var rejected RejectedCredentialError
			if errors.As(verr, &rejected) {
				return CredentialProbe{Rejected: true, Detail: rejected.Detail}
			}
			return CredentialProbe{Unreachable: true, Detail: verr.Error()}
		}
		return CredentialProbe{}
	case CredentialGithub:
		if w.ValidateGitHub == nil {
			return CredentialProbe{}
		}
		return w.ValidateGitHub(ctx, value)
	default:
		return CredentialProbe{}
	}
}

// PutCredential stores one credential and journals its kind (never the value).
// It performs no network validation: callers that want the key checked use
// ValidateCredential first and only store once the outcome is acceptable — the
// separation that keeps an unaccepted key out of the store.
func (w SetupWizard) PutCredential(ctx context.Context, kind CredentialKind, value string) error {
	store, err := w.store(ctx)
	if err != nil {
		return err
	}
	// Probe the store first: an unavailable or locked store fails BEFORE
	// the user types anything (the auth add contract, kept here).
	if err := store.Verify(ctx); err != nil {
		// An absent file store is a normal first-run state for the
		// consented fallback: the wizard's own Put creates it. Any other
		// failure (locked, denied, unavailable) stays fatal.
		if !errors.Is(err, ErrFileStoreAbsent) && !errors.Is(err, ErrFileStoreNotConsented) {
			return err
		}
	}
	if err := store.Put(ctx, kind, value); err != nil {
		return err
	}
	// Record the store that received the value, so a later start reads it back
	// from there instead of re-probing a native adapter that may be
	// unreachable. Best-effort: the credential is stored, and losing the record
	// only restores the previous default chain. Written through the wizard's own
	// FS so an injected filesystem stays injected.
	_ = writeRecordedCredentialStore(w.FS, w.StateDir, kind, store.Kind())
	j, err := ReadSetupJournal(w.FS, w.StateDir)
	if err != nil {
		return err
	}
	if !containsStr(j.CredentialKinds, string(kind)) {
		j.CredentialKinds = append(j.CredentialKinds, string(kind))
	}
	return WriteSetupJournal(w.FS, w.StateDir, j)
}

// StoreCredential validates and stores one credential. A rejected key yields a
// Rejected probe and stores nothing; an unreachable endpoint yields an
// Unreachable probe and stores the value unvalidated (the caller decides,
// because for an existing credential that is the useful default). A caller
// that must ask before writing uses ValidateCredential + PutCredential.
func (w SetupWizard) StoreCredential(ctx context.Context, kind CredentialKind, value string) (CredentialProbe, error) {
	probe, err := w.ValidateCredential(ctx, kind, value)
	if err != nil {
		return CredentialProbe{}, err
	}
	if probe.Rejected {
		return probe, nil
	}
	if err := w.PutCredential(ctx, kind, value); err != nil {
		return probe, err
	}
	return probe, nil
}

// RejectedCredentialError marks a key the endpoint refused: retryable by
// re-entering the key, distinct from any network failure.
type RejectedCredentialError struct{ Detail string }

func (e RejectedCredentialError) Error() string {
	return "the Albert key was rejected: " + e.Detail
}

// SaveIdentity journals the git identity (no store involvement).
func (w SetupWizard) SaveIdentity(name, email string) error {
	// The identity is persisted in the durable user settings, not just the
	// journal: the journal is removed at completion, and the guest
	// bootstrap reads the settings to configure git. The journal copy
	// tracks wizard progress for the review display.
	path, err := UserSettingsPath()
	if err != nil {
		return err
	}
	us, err := ReadUserSettings(w.FS, path)
	if err != nil {
		return err
	}
	us.GitName = name
	us.GitEmail = email
	if err := WriteUserSettings(w.FS, path, us); err != nil {
		return err
	}
	j, err := ReadSetupJournal(w.FS, w.StateDir)
	if err != nil {
		return err
	}
	j.GitName = name
	j.GitEmail = email
	return WriteSetupJournal(w.FS, w.StateDir, j)
}

// SaveSettings writes the resolved global settings and journals the model set.
// The settings file is the P04 managed schema; the identity fields are not
// part of it (they live as git config at runtime), so only the model choices
// are persisted here.
//
// modelChosen is false when the model prompt was never answered (EOF, Ctrl-D):
// the stored model is then left exactly as it was, so an unanswered run cannot
// silently reset a configuration the user never asked to change.
func (w SetupWizard) SaveSettings(model string, models []string, modelChosen bool, providers []InferenceProvider, statsEnabled, statsConsentAsked bool) error {
	path, err := UserSettingsPath()
	if err != nil {
		return err
	}
	us, err := ReadUserSettings(w.FS, path)
	if err != nil {
		return err
	}
	j, err := ReadSetupJournal(w.FS, w.StateDir)
	if err != nil {
		return err
	}
	if modelChosen {
		us.DefaultModel = model
		us.Models = append([]string(nil), models...)
	}
	if len(providers) > 0 {
		us.Providers = make([]ProviderSettings, 0, len(providers))
		for _, p := range providers {
			us.Providers = append(us.Providers, ProviderSettings{Kind: p.Kind, Name: p.Name, BaseURL: p.BaseURL})
		}
	}
	albertConfigured := false
	for _, provider := range providers {
		if provider.Kind == ProviderAlbert {
			albertConfigured = true
			break
		}
	}
	if us.CredentialRef == "" && albertConfigured && ContainsCredentialKind(j.CredentialKinds, string(CredentialAlbert)) {
		// Setup and runtime must agree on priority: a key the user stored or
		// entered manually wins over ALBERT_API_KEY. Preserve any explicit
		// custom credentialRef already chosen by the user (#308).
		us.CredentialRef = string(CredentialAlbert)
	}
	us.StatsEnabled = statsEnabled
	us.StatsConsentAsked = statsConsentAsked
	if err := WriteUserSettings(w.FS, path, us); err != nil {
		return err
	}
	if modelChosen {
		j.DefaultModel = model
		j.Models = append([]string(nil), models...)
		j.ModelChosen = true
	}
	j.StatsEnabled = statsEnabled
	j.StatsConsentAsked = statsConsentAsked
	return WriteSetupJournal(w.FS, w.StateDir, j)
}

// InstallRuntime installs the managed runtime (resumable: a trusted
// installation is a no-op) and completes setup.
func (w SetupWizard) InstallRuntime(ctx context.Context) error {
	ensure := w.EnsureRuntime
	if ensure == nil {
		ensure = func(ctx context.Context) error {
			return ensureMSBRuntime(ctx, nil)
		}
	}
	if err := ensure(ctx); err != nil {
		return err
	}
	return RemoveSetupJournal(w.FS, w.StateDir)
}

func (w SetupWizard) store(ctx context.Context) (SetupStore, error) {
	if w.Store != nil {
		return w.Store(ctx)
	}
	s := DefaultCredentialStore()
	if s == nil {
		return nil, fmt.Errorf("no native credential store on this platform; run 'just-code auth add --fallback' to consent to the owner-only file store, or retry setup with --fallback")
	}
	return s, nil
}
