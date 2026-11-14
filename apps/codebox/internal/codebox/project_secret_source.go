package codebox

import (
	"context"
	"errors"
	"fmt"
)

// ProjectSecretValue describes one resolved project secret value. It carries
// only the value, a non-secret source label, and the opaque store reference
// when one exists. Host-local approvals stay authoritative for delivery mode,
// destinations, placeholders and recovery compatibility.
type ProjectSecretValue struct {
	Value     string
	Source    string
	Reference string
}

// ProjectSecretIdentity names one imported variable within a project.
type ProjectSecretIdentity struct {
	Profile   string
	Component string
	Name      string
}

func (i ProjectSecretIdentity) String() string {
	return i.Profile + "/" + i.Component + "/" + i.Name
}

// ProjectSecretValueRequest is one approved delivery whose value the runtime
// needs. Reference comes from the local project-secret index and is used only
// to build the persisted binding metadata; a remote source may leave it empty.
type ProjectSecretValueRequest struct {
	Identity  ProjectSecretIdentity
	Reference string
}

// ProjectSecretValueSource resolves secret values for a set of approved
// identities. Implementations must resolve all requested identities at once so
// a remote source can fetch one project bundle per start/recovery instead of
// performing one network request per variable.
type ProjectSecretValueSource interface {
	ResolveProjectSecretValues(ctx context.Context, requests []ProjectSecretValueRequest) (map[ProjectSecretIdentity]ProjectSecretValue, error)
}

// ErrProjectSecretValueSourceUnconfigured means a project declares a remote
// source but this build has no resolver installed. It fails closed rather than
// falling back to local OS-store values that may be stale or belong to another
// project.
var ErrProjectSecretValueSourceUnconfigured = errors.New("project declares remote secrets but no secret value resolver is configured")

// resolvedSecretSet is the one value-resolution result shared by start,
// guest-readable dotenv delivery, and recovery within a single runtime call.
// It gives every read path the same value and source label for an identity,
// and makes an injected remote source perform at most one fetch per call site.
type resolvedSecretSet struct {
	values map[ProjectSecretIdentity]ProjectSecretValue
}

func (s resolvedSecretSet) get(identity ProjectSecretIdentity) (ProjectSecretValue, error) {
	value, ok := s.values[identity]
	if !ok {
		return ProjectSecretValue{}, fmt.Errorf("resolved secret set has no value for %s", identity.String())
	}
	return value, nil
}

// resolveProjectSecretValueSet resolves every value a project delivery record
// needs, once. It is the only place the runtime chooses between the local OS
// store and an injected remote source, and it is shared by all three value-read
// paths so they cannot disagree about a value.
func (m *MicrosandboxRuntime) resolveProjectSecretValueSet(ctx context.Context, store *ProjectSecretStore, deliveries []ProjectSecretDelivery) (resolvedSecretSet, error) {
	requests := make([]ProjectSecretValueRequest, 0, len(deliveries))
	seen := map[ProjectSecretIdentity]bool{}
	for _, d := range deliveries {
		identity := ProjectSecretIdentity{Profile: d.Profile, Component: d.Component, Name: d.Name}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		requests = append(requests, ProjectSecretValueRequest{Identity: identity, Reference: d.Reference})
	}
	if len(requests) == 0 {
		return resolvedSecretSet{values: map[ProjectSecretIdentity]ProjectSecretValue{}}, nil
	}
	// A caller may set ProjectSecretsDeclared explicitly (the CLI will, once
	// it has loaded the manifest). Also derive it here from the project's own
	// manifest when a workspace root is available, so a declared remote source
	// fails closed on every path that reaches this resolver.
	declared := m.ProjectSecretsDeclared
	if !declared && m.cfg.WorkspaceDir != "" {
		if manifest, err := ReadProjectManifest(DefaultFS, ProjectManifestPath(m.cfg.WorkspaceDir)); err == nil {
			declared = manifest.Secrets != nil
		}
	}
	if declared && m.ProjectSecretValues == nil {
		return resolvedSecretSet{}, ErrProjectSecretValueSourceUnconfigured
	}

	if source := m.ProjectSecretValues; source != nil {
		values, err := source.ResolveProjectSecretValues(ctx, requests)
		if err != nil {
			return resolvedSecretSet{}, err
		}
		for _, req := range requests {
			value, ok := values[req.Identity]
			if !ok {
				return resolvedSecretSet{}, fmt.Errorf("remote secret source did not resolve %s", req.Identity.String())
			}
			if value.Reference == "" {
				value.Reference = req.Reference
				values[req.Identity] = value
			}
		}
		return resolvedSecretSet{values: values}, nil
	}

	values := make(map[ProjectSecretIdentity]ProjectSecretValue, len(requests))
	for _, req := range requests {
		id := req.Identity
		value, err := store.Get(ctx, id.Profile, id.Component, id.Name)
		if err != nil {
			// An APPROVED project secret whose backing OS-store entry is gone is
			// not the tolerable "not stored yet" case: only a re-import or a
			// revoked approval repairs it, so start must fail (#204). A typed
			// StoreError (locked/denied/unavailable) stays distinguishable.
			if isNotFound(err) {
				return resolvedSecretSet{}, fmt.Errorf("%w (%s)", ErrProjectSecretImportMissing, id.String())
			}
			return resolvedSecretSet{}, fmt.Errorf("cannot read project secret %s: %w", id.String(), err)
		}
		values[id] = ProjectSecretValue{Value: value, Source: store.Kind(), Reference: req.Reference}
	}
	return resolvedSecretSet{values: values}, nil
}
