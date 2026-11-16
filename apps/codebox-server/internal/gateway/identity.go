package gateway

import "context"

// IdentityProvider is the authentication seam the HTTP layer depends on.
// *OIDCClient implements it against ProConnect; tests substitute a fake so
// handler behaviour is exercised without network or cryptography.
type IdentityProvider interface {
	// AuthorizeURL builds the provider redirect for a login attempt.
	AuthorizeURL(ctx context.Context, state, nonce, codeChallenge string) (string, error)
	// Exchange verifies an authorization code and returns the identity.
	Exchange(ctx context.Context, code, codeVerifier, expectedNonce string) (Identity, error)
	// ValidateProjectCreation applies the creation-time eligibility guard.
	ValidateProjectCreation(id Identity) error
}
