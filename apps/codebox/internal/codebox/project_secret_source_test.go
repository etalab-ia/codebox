package codebox

import (
	"context"
	"errors"
	"testing"
)

type countingProjectSecretSource struct {
	calls  int
	values map[ProjectSecretIdentity]ProjectSecretValue
	err    error
}

func (s *countingProjectSecretSource) ResolveProjectSecretValues(_ context.Context, requests []ProjectSecretValueRequest) (map[ProjectSecretIdentity]ProjectSecretValue, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := map[ProjectSecretIdentity]ProjectSecretValue{}
	for _, req := range requests {
		if v, ok := s.values[req.Identity]; ok {
			out[req.Identity] = v
		}
	}
	return out, nil
}

func TestResolveProjectSecretValueSetRemoteResolvesAllOnce(t *testing.T) {
	id := ProjectSecretIdentity{Profile: "development", Component: ".env", Name: "API_KEY"}
	source := &countingProjectSecretSource{values: map[ProjectSecretIdentity]ProjectSecretValue{
		id: {Value: "remote-value", Source: "gateway", Reference: "remote-ref"},
	}}
	runtime := &MicrosandboxRuntime{ProjectSecretValues: source}
	deliveries := []ProjectSecretDelivery{{ProjectSecretBinding: ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
	}, Reference: "local-ref"}}

	set, err := runtime.resolveProjectSecretValueSet(context.Background(), nil, deliveries)
	if err != nil {
		t.Fatalf("resolveProjectSecretValueSet: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("remote source calls = %d, want 1", source.calls)
	}
	value, err := set.get(id)
	if err != nil || value.Value != "remote-value" || value.Source != "gateway" || value.Reference != "remote-ref" {
		t.Fatalf("set.get = %+v, %v", value, err)
	}
}

func TestResolveProjectSecretValueSetRemoteMissingValueFailsClosed(t *testing.T) {
	id := ProjectSecretIdentity{Profile: "development", Component: ".env", Name: "API_KEY"}
	runtime := &MicrosandboxRuntime{ProjectSecretValues: &countingProjectSecretSource{values: map[ProjectSecretIdentity]ProjectSecretValue{}}}
	deliveries := []ProjectSecretDelivery{{ProjectSecretBinding: ProjectSecretBinding{
		Profile: id.Profile, Component: id.Component, Name: id.Name,
	}}}
	if _, err := runtime.resolveProjectSecretValueSet(context.Background(), nil, deliveries); err == nil {
		t.Fatal("a remote source that omitted a requested value succeeded")
	}
}

func TestResolveProjectSecretValueSetRemoteErrorPropagates(t *testing.T) {
	sentinel := errors.New("gateway unavailable")
	runtime := &MicrosandboxRuntime{ProjectSecretValues: &countingProjectSecretSource{err: sentinel}}
	deliveries := []ProjectSecretDelivery{{ProjectSecretBinding: ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
	}}}
	if _, err := runtime.resolveProjectSecretValueSet(context.Background(), nil, deliveries); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped sentinel", err)
	}
}

func TestResolveProjectSecretValueSetDeclaredWithoutResolverFailsClosed(t *testing.T) {
	runtime := &MicrosandboxRuntime{ProjectSecretsDeclared: true}
	deliveries := []ProjectSecretDelivery{{ProjectSecretBinding: ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
	}}}
	if _, err := runtime.resolveProjectSecretValueSet(context.Background(), nil, deliveries); !errors.Is(err, ErrProjectSecretValueSourceUnconfigured) {
		t.Fatalf("error = %v, want ErrProjectSecretValueSourceUnconfigured", err)
	}
}

func TestResolveProjectSecretValueSetManifestDeclaredWithoutResolverFailsClosed(t *testing.T) {
	tmp := t.TempDir()
	path := ProjectManifestPath(tmp)
	manifest := ProjectManifest{Secrets: &ProjectSecretsManifest{ProjectID: "22222222-2222-4222-8222-222222222222"}}
	// The runtime reads through DefaultFS, so write to the isolated temp root.
	if err := WriteProjectManifest(DefaultFS, path, manifest); err != nil {
		t.Fatalf("WriteProjectManifest: %v", err)
	}
	runtime := &MicrosandboxRuntime{cfg: Config{WorkspaceDir: tmp}}
	deliveries := []ProjectSecretDelivery{{ProjectSecretBinding: ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
	}}}
	if _, err := runtime.resolveProjectSecretValueSet(context.Background(), nil, deliveries); !errors.Is(err, ErrProjectSecretValueSourceUnconfigured) {
		t.Fatalf("error = %v, want ErrProjectSecretValueSourceUnconfigured", err)
	}
}

func TestGatewayRevisionMarkerChangesBindingsRevision(t *testing.T) {
	makeBinding := func(reference string) resolvedBinding {
		return resolvedBinding{
			msbSecretBinding: msbSecretBinding{
				Kind: "JC_API_KEY", GuestEnv: "JC_API_KEY", Placeholder: "__CB__",
				Project: &ProjectBindingRef{Profile: "development", Component: ".env", Name: "API_KEY", Reference: reference},
			},
			source: bindingSourceStore,
			store:  "gateway",
			entry:  reference,
		}
	}
	before := bindingsRevision([]resolvedBinding{makeBinding("gateway-rev-1/r1")})
	after := bindingsRevision([]resolvedBinding{makeBinding("gateway-rev-2/r1")})
	if before == after {
		t.Fatal("a gateway bundle revision change did not move bindingsRevision")
	}
}

func TestGatewayProjectIdentityChangesBindingsRevision(t *testing.T) {
	makeBinding := func(reference string) resolvedBinding {
		return resolvedBinding{
			msbSecretBinding: msbSecretBinding{
				Kind: "JC_API_KEY", GuestEnv: "JC_API_KEY", Placeholder: "__CB__",
				Project: &ProjectBindingRef{Profile: "development", Component: ".env", Name: "API_KEY", Reference: reference},
			},
			source: bindingSourceStore,
			store:  "gateway",
			entry:  reference,
		}
	}
	before := bindingsRevision([]resolvedBinding{makeBinding("gateway-11111111-1111-4111-8111-111111111111-rev-4/r1")})
	after := bindingsRevision([]resolvedBinding{makeBinding("gateway-22222222-2222-4222-8222-222222222222-rev-4/r1")})
	if before == after {
		t.Fatal("switching to another gateway project at the same revision did not move bindingsRevision")
	}
}
