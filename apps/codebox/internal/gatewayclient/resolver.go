package gatewayclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
	"github.com/etalab-ia/codebox/packages/secrets"
)

// ValueSource adapts a gateway Client to codebox.ProjectSecretValueSource. It
// performs exactly one resolve call per ResolveProjectSecretValues invocation
// and maps component/name pairs back to the requested identities.
type ValueSource struct {
	Client    *Client
	ProjectID string
}

// NewValueSource builds a remote resolver for the project manifest's ID.
func NewValueSource(client *Client, projectID string) (*ValueSource, error) {
	if client == nil {
		return nil, fmt.Errorf("gateway client is required")
	}
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return nil, err
	}
	return &ValueSource{Client: client, ProjectID: projectID}, nil
}

// ResolveProjectSecretValues resolves all requested identities through one
// gateway resolve call. It fails closed when a requested component or variable
// is absent, because an approved binding must never be silently omitted.
func (s *ValueSource) ResolveProjectSecretValues(ctx context.Context, requests []codebox.ProjectSecretValueRequest) (map[codebox.ProjectSecretIdentity]codebox.ProjectSecretValue, error) {
	out := make(map[codebox.ProjectSecretIdentity]codebox.ProjectSecretValue, len(requests))
	if len(requests) == 0 {
		return out, nil
	}
	components := make([]string, 0, len(requests))
	seenComponent := map[string]bool{}
	for _, req := range requests {
		if !seenComponent[req.Identity.Component] {
			seenComponent[req.Identity.Component] = true
			components = append(components, req.Identity.Component)
		}
	}
	resolved, err := s.Client.Resolve(ctx, s.ProjectID, components)
	if err != nil {
		return nil, err
	}
	for _, req := range requests {
		vars, ok := resolved.Components[req.Identity.Component]
		if !ok {
			return nil, fmt.Errorf("gateway bundle has no component %s (revision %d)", req.Identity.Component, resolved.Revision)
		}
		value, ok := vars[req.Identity.Name]
		if !ok {
			return nil, fmt.Errorf("gateway bundle has no variable %s in %s (revision %d)", req.Identity.Name, req.Identity.Component, resolved.Revision)
		}
		out[req.Identity] = codebox.ProjectSecretValue{
			Value:     value,
			Source:    "gateway",
			Reference: req.Reference,
		}
	}
	return out, nil
}

// BuildRuntimeValueSource resolves the configured gateway URL, reads the
// session token from the OS credential store, and constructs the runtime's
// remote value source. A missing URL simply means "local workflow"; a declared
// project without a session token fails with a login instruction.
func BuildRuntimeValueSource(ctx context.Context, projectID string, store codebox.CredentialStore, httpClient *http.Client) (codebox.ProjectSecretValueSource, bool, error) {
	baseURL, err := codebox.SecretsGatewayURL()
	if err != nil {
		return nil, false, err
	}
	if baseURL == "" {
		return nil, false, nil
	}
	token, err := store.Get(ctx, codebox.CredentialSecretsGateway)
	if err != nil {
		return nil, true, fmt.Errorf("gateway session missing; run codebox secrets login: %w", err)
	}
	client, err := New(baseURL, token, httpClient)
	if err != nil {
		return nil, true, err
	}
	source, err := NewValueSource(client, projectID)
	if err != nil {
		return nil, true, err
	}
	return source, true, nil
}
