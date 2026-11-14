package codebox

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// resolveRemoteProjectSecretDeliveries derives approved deliveries for a
// project that declares a remote development-secrets source. Unlike the local
// import path it does not consult the OS credential-store index: the gateway
// owns the values, and the host-local bindings.json owns the delivery decision.
// A manifest hint alone is not an approval; every delivery must have a matching
// active host-local binding with the same component, variable, profile and
// delivery shape.
func (m *MicrosandboxRuntime) resolveRemoteProjectSecretDeliveries(ctx context.Context) (projectSecretResolution, error) {
	if m.ProjectSecretsManifest == nil {
		return projectSecretResolution{}, nil
	}
	store, err := m.projectSecretStore()
	if err != nil {
		return projectSecretResolution{}, err
	}
	set, err := ReadProjectSecretBindings(DefaultFS, ProjectSecretBindingsPath(m.stateDirOrDefault(), store.Namespace()), store.Namespace())
	if err != nil {
		return projectSecretResolution{}, err
	}
	if len(set.Bindings) == 0 {
		return projectSecretResolution{}, nil
	}
	if set.Profile == "" {
		return projectSecretResolution{}, errors.New("project secret bindings exist but no active profile is selected")
	}

	byIdentity := map[string]ProjectSecretBinding{}
	for _, b := range set.Bindings {
		if b.Profile != set.Profile {
			return projectSecretResolution{}, fmt.Errorf("project secret binding %s belongs to profile %q while the active profile is %q", b.Name, b.Profile, set.Profile)
		}
		byIdentity[b.identity()] = b
	}

	deliveries := make([]ProjectSecretDelivery, 0, len(m.ProjectSecretsManifest.Bindings))
	seen := map[string]bool{}
	for _, hint := range m.ProjectSecretsManifest.Bindings {
		identity := ProjectSecretIdentity{Profile: set.Profile, Component: hint.Component, Name: hint.Name}
		id := identity.Profile + "\x00" + identity.Component + "\x00" + identity.Name
		if seen[id] {
			return projectSecretResolution{}, fmt.Errorf("project secrets manifest declares %s in %s twice", hint.Name, hint.Component)
		}
		seen[id] = true
		binding, approved := byIdentity[id]
		if !approved {
			continue
		}
		wantGuest := hint.Delivery == ProjectSecretDeliveryGuest
		if binding.GuestReadable() != wantGuest {
			return projectSecretResolution{}, fmt.Errorf("host approval for %s in %s does not match the declared delivery mode", hint.Name, hint.Component)
		}
		if !wantGuest && !EqualStringSets(binding.Destinations, hint.Destinations) {
			// Hint destinations are optional: an empty list means the host must
			// choose and approve at least one destination. A non-empty list is
			// a versioned policy and must match the host approval exactly.
			if len(hint.Destinations) > 0 {
				return projectSecretResolution{}, fmt.Errorf("host approval for %s in %s does not match the declared destinations", hint.Name, hint.Component)
			}
		}
		if binding.GuestEnv == "" && !binding.GuestReadable() {
			binding.GuestEnv = ProjectSecretGuestEnv(binding.Component, binding.Name)
		}
		deliveries = append(deliveries, ProjectSecretDelivery{
			ProjectSecretBinding: binding,
			Reference:            "gateway:" + identity.String(),
			Marker:               ProjectSecretPlaceholder(store.Namespace(), binding.Profile, binding.Component, binding.Name),
		})
	}
	sort.Slice(deliveries, func(i, j int) bool { return deliveries[i].identity() < deliveries[j].identity() })
	return projectSecretResolution{deliveries: deliveries}, nil
}

// EqualStringSets reports whether two string slices contain the same
// elements, ignoring order. It is used for destination allowlists, whose
// order has no security meaning.
func EqualStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// ValidateProjectSecretBindingForRemote checks one host-local approval's
// structural invariants using the same rules as the local binding set, without
// consulting any imported value. It is used by remote-project approval flows.
func ValidateProjectSecretBindingForRemote(namespace string, b ProjectSecretBinding) error {
	set := projectSecretBindingSet{
		SchemaVersion: projectSecretBindingSchemaVersion,
		Namespace:     namespace,
		Profile:       b.Profile,
		Bindings:      []ProjectSecretBinding{b},
	}
	return validateProjectSecretBindingSet(set, namespace)
}
