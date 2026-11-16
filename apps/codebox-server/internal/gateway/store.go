package gateway

import (
	"context"
	"errors"
)

// SecretStore is the storage boundary the gateway uses for a project's ACL and
// secret bundle. Implementations must never log or echo payload bytes.
type SecretStore interface {
	// ProjectExists reports whether an ACL secret exists for the project.
	ProjectExists(ctx context.Context, projectID string) (bool, error)

	// CreateProject creates an empty bundle and an initial ACL holding the
	// caller as sole owner. It must fail if either secret already exists.
	CreateProject(ctx context.Context, projectID, ownerEmail, aclNeedsMFA string) error

	// ReadACL returns the current ACL payload and its revision.
	ReadACL(ctx context.Context, projectID string) (data []byte, revision uint32, err error)

	// WriteACL stores a new ACL revision and returns its revision number it
	// expects to be based on, failing with ErrStoreConflict when the current
	// revision differs.
	WriteACL(ctx context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error)

	// ReadBundle returns the latest enabled bundle payload and its revision.
	ReadBundle(ctx context.Context, projectID string) (data []byte, revision uint32, err error)

	// WriteBundle stores a new bundle revision, failing with ErrStoreConflict
	// when the current revision differs from baseRevision.
	WriteBundle(ctx context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error)

	// Kind names the backend for diagnostics ("scaleway", "memory").
	Kind() string
}

// Storage error classes the HTTP layer maps to status codes.
var (
	// ErrStoreNotFound means the requested secret does not exist.
	ErrStoreNotFound = errors.New("secret not found")

	// ErrStoreConflict means the caller's base revision is stale.
	ErrStoreConflict = errors.New("secret was modified since the supplied base revision")

	// ErrStoreUnavailable means the backing service could not be reached or
	// rejected the gateway's own credential; it is never the caller's fault.
	ErrStoreUnavailable = errors.New("secret store unavailable")

	// ErrStoreInvalid means the stored payload could not be interpreted.
	ErrStoreInvalid = errors.New("secret store returned an unusable payload")
)
