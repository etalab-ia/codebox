package justcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
)

// This file implements the inference-platform model introduced by issue #180:
// a setup run can connect Albert API, Mistral AI and one OpenAI-compatible
// platform, each with its own credential and model list. Albert keeps its
// fixed endpoint and curated catalogue; the other platforms are described by
// a base URL and read their raw model listing at runtime.
//
// A platform's base URL is security-relevant: it decides the only host that
// may ever receive the platform's credential through the secret proxy. The
// rules live here, next to the type, so the wizard, the settings validator
// and the binding registry cannot drift apart.

// ProviderKind identifies one inference platform kind.
type ProviderKind string

const (
	// ProviderAlbert is the Albert API, the sovereign platform.
	ProviderAlbert ProviderKind = "albert"
	// ProviderMistral is Mistral AI's hosted API.
	ProviderMistral ProviderKind = "mistral"
	// ProviderOpenAICompatible is a user-named OpenAI-compatible platform.
	ProviderOpenAICompatible ProviderKind = "openai-compatible"
)

const (
	// albertBaseURL is the Albert API base URL. It is fixed: a configurable
	// Albert endpoint would let a typo send the Albert key to another host.
	albertBaseURL = "https://albert.api.etalab.gouv.fr/v1"
	// mistralBaseURL is Mistral AI's API base URL, fixed for the same reason.
	mistralBaseURL = "https://api.mistral.ai/v1"
)

const (
	// MistralAPIKeyEnv is the guest variable holding the Mistral key.
	MistralAPIKeyEnv = "MISTRAL_API_KEY"
	// OpenAICompatibleAPIKeyEnv is the guest variable holding the
	// OpenAI-compatible platform key.
	OpenAICompatibleAPIKeyEnv = "OPENAI_COMPATIBLE_API_KEY"
)

// AllProviderAPIKeyEnvs lists every managed platform credential variable, for
// stale-registration cleanup: a platform removed from the settings must have
// its proxy registration dropped even though it is no longer configured.
func AllProviderAPIKeyEnvs() []string {
	return []string{msbAPISecretEnv, MistralAPIKeyEnv, OpenAICompatibleAPIKeyEnv}
}

// ProviderSettings is the persisted, non-secret description of one connected
// platform. Credentials live in the credential store, never here.
type ProviderSettings struct {
	Kind ProviderKind `json:"kind"`
	// Name is the display name. Required for an OpenAI-compatible platform
	// (the user names it); the built-in names apply to the other kinds.
	Name string `json:"name,omitempty"`
	// BaseURL is the OpenAI-compatible platform's base URL. Empty for Albert
	// and Mistral, whose endpoints are fixed.
	BaseURL string `json:"baseURL,omitempty"`
}

// InferenceProvider is a resolved platform: its endpoint, its credential
// binding and the only host that may receive that credential.
type InferenceProvider struct {
	Kind    ProviderKind
	Name    string
	BaseURL string
	// CatalogueURL is the models listing endpoint.
	CatalogueURL string
	// CredentialKind is the store entry holding the platform key.
	CredentialKind CredentialKind
	// APIKeyEnv is the guest variable the key is exposed under.
	APIKeyEnv string
	// Host is the DNS name the secret proxy may send the key to.
	Host string
}

// DefaultProviderSettings is the historical single-platform configuration:
// Albert API. An empty settings.Providers means exactly this, so settings
// written before #180 keep working unchanged.
func DefaultProviderSettings() []ProviderSettings {
	return []ProviderSettings{{Kind: ProviderAlbert}}
}

// ResolveProviders validates the configured platforms and applies their
// built-in defaults. At least one platform is required.
func ResolveProviders(settings []ProviderSettings) ([]InferenceProvider, error) {
	if len(settings) == 0 {
		settings = DefaultProviderSettings()
	}
	seen := map[ProviderKind]bool{}
	hosts := map[string]ProviderKind{}
	out := make([]InferenceProvider, 0, len(settings))
	for _, s := range settings {
		if seen[s.Kind] {
			return nil, fmt.Errorf("the platform %s is configured more than once", s.Kind)
		}
		seen[s.Kind] = true
		p, err := ResolveProvider(s)
		if err != nil {
			return nil, err
		}
		if prev, dup := hosts[p.Host]; dup {
			return nil, fmt.Errorf("platforms %s and %s share the host %s: a credential must have exactly one destination set", prev, p.Kind, p.Host)
		}
		hosts[p.Host] = p.Kind
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one inference platform is required")
	}
	return out, nil
}

// ResolveProvider validates one platform definition.
func ResolveProvider(s ProviderSettings) (InferenceProvider, error) {
	switch s.Kind {
	case ProviderAlbert:
		if strings.TrimSpace(s.BaseURL) != "" && normalizeBaseURL(s.BaseURL) != albertBaseURL {
			return InferenceProvider{}, fmt.Errorf("Albert API uses a fixed endpoint; remove the custom base URL")
		}
		return InferenceProvider{
			Kind: ProviderAlbert, Name: "Albert API (État)", BaseURL: albertBaseURL,
			CatalogueURL: albertCatalogueURL, CredentialKind: CredentialAlbert,
			APIKeyEnv: msbAPISecretEnv, Host: msbAllowHost,
		}, nil
	case ProviderMistral:
		if strings.TrimSpace(s.BaseURL) != "" && normalizeBaseURL(s.BaseURL) != mistralBaseURL {
			return InferenceProvider{}, fmt.Errorf("Mistral AI uses a fixed endpoint; remove the custom base URL")
		}
		host, err := providerHost(mistralBaseURL)
		if err != nil {
			return InferenceProvider{}, err
		}
		return InferenceProvider{
			Kind: ProviderMistral, Name: "Mistral AI", BaseURL: mistralBaseURL,
			CatalogueURL: mistralBaseURL + "/models", CredentialKind: CredentialMistral,
			APIKeyEnv: MistralAPIKeyEnv, Host: host,
		}, nil
	case ProviderOpenAICompatible:
		name := strings.TrimSpace(s.Name)
		if name == "" {
			return InferenceProvider{}, fmt.Errorf("an OpenAI-compatible platform needs a name")
		}
		base, err := normalizeProviderBaseURL(s.BaseURL)
		if err != nil {
			return InferenceProvider{}, err
		}
		host, err := providerHost(base)
		if err != nil {
			return InferenceProvider{}, err
		}
		return InferenceProvider{
			Kind: ProviderOpenAICompatible, Name: name, BaseURL: base,
			CatalogueURL: base + "/models", CredentialKind: CredentialOpenAICompatible,
			APIKeyEnv: OpenAICompatibleAPIKeyEnv, Host: host,
		}, nil
	default:
		return InferenceProvider{}, fmt.Errorf("unknown inference platform kind %q", s.Kind)
	}
}

// normalizeBaseURL trims a trailing slash so comparisons and catalogue URLs
// are stable.
func normalizeBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// normalizeProviderBaseURL validates a user-supplied base URL and returns its
// canonical form. The proxy can only allow a DNS name, so insecure schemes,
// credentials in the URL, query strings, fragments and non-default ports are
// refused rather than silently reduced to a host that would still receive the
// key.
func normalizeProviderBaseURL(raw string) (string, error) {
	base := normalizeBaseURL(raw)
	if base == "" {
		return "", fmt.Errorf("a base URL is required for an OpenAI-compatible platform")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("the base URL must use https")
	}
	if u.User != nil {
		return "", fmt.Errorf("the base URL must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the base URL must not contain a query string or fragment")
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", fmt.Errorf("the base URL must use the default https port")
	}
	base = strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/")
	if _, err := providerHost(base); err != nil {
		return "", err
	}
	return base, nil
}

// providerHost extracts and validates the credential destination of a base
// URL. This is the rule for an arbitrary host: the entered base URL defines
// exactly one destination, and that destination must be a DNS name the proxy
// can enforce.
func providerHost(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("the base URL must use https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("the base URL has no host")
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("the base URL must name a DNS host, not an IP address")
	}
	if !isDNSName(host) {
		return "", fmt.Errorf("%q is not a DNS name the secret proxy can allow", host)
	}
	return host, nil
}

// ProviderForKind resolves a single platform kind with built-in defaults.
func ProviderForKind(kind ProviderKind) (InferenceProvider, error) {
	return ResolveProvider(ProviderSettings{Kind: kind})
}

// ProviderModelRef is the settings/OpenCode reference for a model on a
// platform ("mistral/mistral-large-latest"). Albert keeps its historical
// provider prefix.
func ProviderModelRef(kind ProviderKind, id string) string {
	return string(kind) + "/" + strings.TrimPrefix(id, string(kind)+"/")
}

// SplitModelRef splits "provider/model". A value without a slash is treated
// as an Albert model, the historical settings format.
func SplitModelRef(ref string) (ProviderKind, string) {
	if i := strings.Index(ref, "/"); i > 0 {
		return ProviderKind(ref[:i]), ref[i+1:]
	}
	return ProviderAlbert, ref
}

// ModelRefProvider returns the platform a settings reference belongs to. Only
// the known provider prefixes denote another platform; a bare value is an
// Albert model, even when its API ID contains a slash (openai/gpt-oss-120b,
// Qwen/Qwen3-Coder-…).
func ModelRefProvider(ref string) ProviderKind {
	kind, _ := SplitModelRef(ref)
	switch kind {
	case ProviderAlbert, ProviderMistral, ProviderOpenAICompatible:
		return kind
	}
	return ProviderAlbert
}

// ProviderModelIDs returns the checked model IDs of one platform from a
// mixed settings list. Bare IDs are Albert IDs.
func ProviderModelIDs(refs []string, kind ProviderKind) []string {
	var out []string
	for _, ref := range refs {
		if ModelRefProvider(ref) != kind {
			continue
		}
		id := ref
		if hasKnownProviderPrefix(ref) {
			_, id = SplitModelRef(ref)
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

// hasKnownProviderPrefix reports whether ref starts with a managed platform
// prefix. It is what separates "mistral/mistral-large" from an Albert API ID
// such as "Qwen/Qwen3-Coder-30B-A3B-Instruct".
func hasKnownProviderPrefix(ref string) bool {
	i := strings.Index(ref, "/")
	if i <= 0 {
		return false
	}
	switch ProviderKind(ref[:i]) {
	case ProviderAlbert, ProviderMistral, ProviderOpenAICompatible:
		return true
	}
	return false
}

// ProviderModelID splits a settings reference into its platform and model ID.
// A bare value without a managed prefix is an Albert model ID kept verbatim,
// so slash-containing Albert API IDs survive.
func ProviderModelID(ref string) (ProviderKind, string) {
	if hasKnownProviderPrefix(ref) {
		return SplitModelRef(ref)
	}
	return ProviderAlbert, ref
}

// ValidateProviderKey probes a platform's models endpoint with a candidate
// key. It never performs inference and never follows a redirect, so the
// Authorization header cannot be replayed to another host.
func ValidateProviderKey(ctx context.Context, client *http.Client, p InferenceProvider, key string) CredentialProbe {
	if p.Kind == ProviderAlbert {
		return validateAlbertKeyAt(ctx, client, key, p.CatalogueURL)
	}
	return validateModelsEndpointKey(ctx, client, key, p.CatalogueURL)
}

// validateModelsEndpointKey is the generic OpenAI-compatible probe. It
// distinguishes the outcomes the wizard must report separately (#309): a
// rejected key, an endpoint whose URL does not serve a models listing
// (incompatible: a missing /models route, a redirect, or a non-catalogue
// body), and a TEMPORARY unavailability (transport failure, 429, 5xx),
// which must never read as an URL compatibility problem.
func validateModelsEndpointKey(ctx context.Context, client *http.Client, key, endpoint string) CredentialProbe {
	client = noRedirectClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
			return CredentialProbe{Unreachable: true, Detail: "timeout: the endpoint did not answer in time"}
		}
		return CredentialProbe{Unreachable: true, Detail: err.Error()}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		var raw struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&raw); err != nil {
			return CredentialProbe{Incompatible: true, Detail: "HTTP 200 with a body that is not a models listing"}
		}
		if raw.Data == nil {
			return CredentialProbe{Incompatible: true, Detail: "HTTP 200 without a models data array"}
		}
		return CredentialProbe{}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		// A redirect almost always means a wrong base URL (moved API root,
		// missing path segment). It is never followed, so the Authorization
		// header cannot be replayed to another host.
		return CredentialProbe{Incompatible: true, Detail: fmt.Sprintf("HTTP %d (redirect not followed; the Authorization header is never replayed to another host)", resp.StatusCode)}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return CredentialProbe{Rejected: true, Detail: fmt.Sprintf("HTTP %d: the key was not accepted", resp.StatusCode)}
	case resp.StatusCode == http.StatusNotFound:
		return CredentialProbe{Incompatible: true, Detail: "HTTP 404: no models route at this base URL (check the API root, for example https://host/v1)"}
	case resp.StatusCode == http.StatusTooManyRequests:
		return CredentialProbe{Unreachable: true, Detail: "HTTP 429: rate limited (temporary; retry later)"}
	case resp.StatusCode >= 500:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d: server error (temporary; retry later)", resp.StatusCode)}
	default:
		return CredentialProbe{Unreachable: true, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
}

// FetchProviderModels reads a platform's raw model listing. Albert keeps its
// curated text-generation filter; the other platforms expose exactly the
// models they report, sorted by ID.
func FetchProviderModels(ctx context.Context, client *http.Client, p InferenceProvider, key string) ([]CatalogueModel, error) {
	catalogue, err := FetchCatalogue(ctx, client, key, p.CatalogueURL)
	if err != nil {
		return nil, err
	}
	if p.Kind == ProviderAlbert {
		return catalogue.TextGenerationModels(), nil
	}
	models := make([]CatalogueModel, 0, len(catalogue.Models))
	seen := map[string]bool{}
	for _, m := range catalogue.Models {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
