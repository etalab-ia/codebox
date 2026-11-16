package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// memoryStore is an in-memory SecretStore whose revision semantics mirror the
// Scaleway adapter: versions are monotonic and a stale base revision conflicts.
type memoryStore struct {
	mu       sync.Mutex
	acls     map[string][][]byte
	bundles  map[string][][]byte
	readErr  error
	writeErr error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{acls: map[string][][]byte{}, bundles: map[string][][]byte{}}
}

func (m *memoryStore) Kind() string { return "memory" }

func (m *memoryStore) ProjectExists(_ context.Context, projectID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.acls[projectID]
	return ok, nil
}

func (m *memoryStore) CreateProject(_ context.Context, projectID, ownerEmail, _ string) error {
	acl, err := secrets.NewACL([]secrets.ACLEntry{{Kind: secrets.EntryOwner, Value: ownerEmail}})
	if err != nil {
		return err
	}
	bundle, err := secrets.MarshalBundle(map[string]map[string]string{})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.acls[projectID]; exists {
		return ErrStoreConflict
	}
	m.acls[projectID] = [][]byte{[]byte(acl.Format())}
	m.bundles[projectID] = [][]byte{bundle}
	return nil
}

func (m *memoryStore) ReadACL(_ context.Context, projectID string) ([]byte, uint32, error) {
	return m.read(m.acls, projectID)
}

func (m *memoryStore) WriteACL(_ context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error) {
	return m.write(m.acls, projectID, baseRevision, data)
}

func (m *memoryStore) ReadBundle(_ context.Context, projectID string) ([]byte, uint32, error) {
	return m.read(m.bundles, projectID)
}

func (m *memoryStore) WriteBundle(_ context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error) {
	return m.write(m.bundles, projectID, baseRevision, data)
}

func (m *memoryStore) read(store map[string][][]byte, projectID string) ([]byte, uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readErr != nil {
		return nil, 0, m.readErr
	}
	versions, ok := store[projectID]
	if !ok || len(versions) == 0 {
		return nil, 0, ErrStoreNotFound
	}
	return append([]byte(nil), versions[len(versions)-1]...), uint32(len(versions)), nil
}

func (m *memoryStore) write(store map[string][][]byte, projectID string, baseRevision uint32, data []byte) (uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	versions, ok := store[projectID]
	if !ok {
		return 0, ErrStoreNotFound
	}
	if uint32(len(versions)) != baseRevision {
		return 0, ErrStoreConflict
	}
	store[projectID] = append(versions, append([]byte(nil), data...))
	return uint32(len(store[projectID])), nil
}

// fakeProvider is a deterministic IdentityProvider for handler tests.
type fakeProvider struct {
	identity      Identity
	exchangeErr   error
	createErr     error
	lastState     string
	lastChallenge string
}

func (f *fakeProvider) AuthorizeURL(_ context.Context, state, nonce, codeChallenge string) (string, error) {
	f.lastState = state
	f.lastChallenge = codeChallenge
	return "https://proconnect.example/authorize?state=" + state + "&nonce=" + nonce, nil
}

func (f *fakeProvider) Exchange(_ context.Context, code, _, _ string) (Identity, error) {
	if f.exchangeErr != nil {
		return Identity{}, f.exchangeErr
	}
	if code == "" {
		return Identity{}, errors.New("missing code")
	}
	return f.identity, nil
}

func (f *fakeProvider) ValidateProjectCreation(Identity) error { return f.createErr }

// testServer builds a Server with a memory store and a fake provider.
type testServer struct {
	server   *Server
	provider *fakeProvider
	store    *memoryStore
	tokens   *TokenIssuer
}

const testSeed = "3J9m0Yk0m9Z1o2X3c4v5b6n7m8q9w0e1r2t3y4u5i6o"

func newTestServer(t *testing.T, identity Identity) *testServer {
	t.Helper()
	provider := &fakeProvider{identity: identity}
	store := newMemoryStore()
	tokens, err := NewTokenIssuer(testSeed, "https://gateway.test", 12*time.Hour, time.Now)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	server, err := NewServer(
		Config{
			PublicURL:      "https://gateway.test",
			TokenLifetime:  12 * time.Hour,
			TicketLifetime: 5 * time.Minute,
			AuditLogger:    slog.New(slog.NewJSONHandler(io.Discard, nil)),
		},
		provider, store, tokens,
		NewAttemptStore(10*time.Minute, 128, time.Now),
		NewRateLimiter(1000, time.Minute, 1024, time.Now),
		time.Now,
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &testServer{server: server, provider: provider, store: store, tokens: tokens}
}

// sessionToken mints a session token for the given identity.
func (ts *testServer) sessionToken(t *testing.T, claims TokenClaims) string {
	t.Helper()
	token, _, err := ts.tokens.IssueSession(claims)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	return token
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("response body is not JSON (%q): %v", rec.Body.String(), err)
	}
}

func newJSONRequest(method, target string, body any) *http.Request {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			panic(err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	return req
}
