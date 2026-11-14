package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/etalab-ia/codebox/packages/secrets"
)

// Scaleway secret path layout inside the dedicated codebox-dev-secrets project.
const (
	ACLPath    = "/acl"
	SecretPath = "/secrets"
)

// ScalewayStore implements SecretStore over Scaleway Secret Manager. The
// gateway's IAM application is scoped to the dedicated, infrastructure-free
// project, so a compromised gateway can never reach another project's secrets.
type ScalewayStore struct {
	baseURL    string
	secretKey  string
	projectID  string
	region     string
	httpClient *http.Client
}

// NewScalewayStore builds a store. baseURL is injectable so tests never reach
// the network.
func NewScalewayStore(baseURL, secretKey, projectID, region string, httpClient *http.Client) (*ScalewayStore, error) {
	if strings.TrimSpace(secretKey) == "" || strings.TrimSpace(projectID) == "" {
		return nil, errors.New("Scaleway secret key and project ID are required")
	}
	switch region {
	case "fr-par", "nl-ams", "pl-waw":
	default:
		return nil, fmt.Errorf("unsupported Scaleway region %q", region)
	}
	if strings.HasSuffix(baseURL, "/") {
		baseURL = strings.TrimSuffix(baseURL, "/")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &ScalewayStore{baseURL: baseURL, secretKey: secretKey, projectID: projectID, region: region, httpClient: httpClient}, nil
}

// Kind names the backend for diagnostics.
func (s *ScalewayStore) Kind() string { return "scaleway" }

func (s *ScalewayStore) secretsURL() string {
	return fmt.Sprintf("%s/secret-manager/v1beta1/regions/%s/secrets", s.baseURL, s.region)
}

// listSecretID resolves a path/name pair to its secret ID, returning
// ErrStoreNotFound when no such secret exists.
func (s *ScalewayStore) listSecretID(ctx context.Context, path, name string) (string, error) {
	q := url.Values{}
	q.Set("project_id", s.projectID)
	q.Set("path", path)
	q.Set("name", name)
	q.Set("page_size", "2")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.secretsURL()+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: secret list unreadable", ErrStoreUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: secret list returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	var doc struct {
		Secrets []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("%w: secret list is not valid JSON", ErrStoreInvalid)
	}
	for _, item := range doc.Secrets {
		if item.Path == path && item.Name == name {
			return item.ID, nil
		}
	}
	return "", ErrStoreNotFound
}

// ensureSecret returns the secret ID, creating a protected secret if absent.
func (s *ScalewayStore) ensureSecret(ctx context.Context, path, name, description string) (string, error) {
	id, err := s.listSecretID(ctx, path, name)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, ErrStoreNotFound) {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{
		"project_id":  s.projectID,
		"name":        name,
		"path":        path,
		"description": description,
		"protected":   true,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.secretsURL(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: secret creation unreadable", ErrStoreUnavailable)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// A concurrent creation makes the list the authoritative check.
		if id, listErr := s.listSecretID(ctx, path, name); listErr == nil {
			return id, nil
		}
		return "", fmt.Errorf("%w: secret creation returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	var created struct {
		ID        string `json:"id"`
		Protected bool   `json:"protected"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		return "", fmt.Errorf("%w: secret creation payload is not valid JSON", ErrStoreInvalid)
	}
	if !created.Protected {
		if err := s.protectSecret(ctx, created.ID); err != nil {
			return "", err
		}
	}
	return created.ID, nil
}

func (s *ScalewayStore) protectSecret(ctx context.Context, secretID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.secretsURL()+"/"+secretID+"/protect", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: enabling secret protection returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	return nil
}

// readVersion accesses a version by path/name and returns its decoded payload
// plus revision number.
func (s *ScalewayStore) readVersion(ctx context.Context, path, name, revision string) ([]byte, uint32, error) {
	q := url.Values{}
	q.Set("project_id", s.projectID)
	q.Set("secret_path", path)
	q.Set("secret_name", name)
	endpoint := fmt.Sprintf("%s/secret-manager/v1beta1/regions/%s/secrets-by-path/versions/%s/access?%s",
		s.baseURL, s.region, url.PathEscape(revision), q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: secret version unreadable", ErrStoreUnavailable)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, 0, ErrStoreNotFound
	default:
		return nil, 0, fmt.Errorf("%w: secret version access returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	var doc struct {
		Revision uint32 `json:"revision"`
		Data     string `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("%w: secret version payload is not valid JSON", ErrStoreInvalid)
	}
	raw, err := base64.StdEncoding.DecodeString(doc.Data)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: secret version payload is not base64", ErrStoreInvalid)
	}
	return raw, doc.Revision, nil
}

// latestRevision resolves the current revision number for a secret without
// returning its payload.
func (s *ScalewayStore) latestRevision(ctx context.Context, path, name, revision string) (uint32, error) {
	id, err := s.listSecretID(ctx, path, name)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.secretsURL()+"/"+id+"/versions/"+url.PathEscape(revision), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Accept", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("%w: secret version metadata unreadable", ErrStoreUnavailable)
	}
	if resp.StatusCode == http.StatusNotFound {
		return 0, ErrStoreNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: secret version metadata returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	var doc struct {
		Revision uint32 `json:"revision"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return 0, fmt.Errorf("%w: secret version metadata is not valid JSON", ErrStoreInvalid)
	}
	return doc.Revision, nil
}

// writeVersion appends a new immutable version and disables the previous one.
func (s *ScalewayStore) writeVersion(ctx context.Context, path, name, description string, payload []byte) (uint32, error) {
	id, err := s.ensureSecret(ctx, path, name, description)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(map[string]any{
		"data":             base64.StdEncoding.EncodeToString(payload),
		"description":      description,
		"disable_previous": true,
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.secretsURL()+"/"+id+"/versions", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Auth-Token", s.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("%w: secret version creation unreadable", ErrStoreUnavailable)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("%w: secret version creation returned HTTP %d", ErrStoreUnavailable, resp.StatusCode)
	}
	var doc struct {
		Revision uint32 `json:"revision"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return 0, fmt.Errorf("%w: secret version creation payload is not valid JSON", ErrStoreInvalid)
	}
	return doc.Revision, nil
}

// ProjectExists reports whether the ACL secret exists for projectID.
func (s *ScalewayStore) ProjectExists(ctx context.Context, projectID string) (bool, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return false, err
	}
	_, err := s.listSecretID(ctx, ACLPath, projectID)
	if errors.Is(err, ErrStoreNotFound) {
		return false, nil
	}
	return err == nil, err
}

// CreateProject creates the project ACL and an empty secret bundle.
func (s *ScalewayStore) CreateProject(ctx context.Context, projectID, ownerEmail, _ string) error {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return err
	}
	acl, err := secrets.NewACL([]secrets.ACLEntry{{Kind: secrets.EntryOwner, Value: ownerEmail}})
	if err != nil {
		return err
	}
	if _, err := s.writeVersion(ctx, ACLPath, projectID, "codebox project ACL", []byte(acl.Format())); err != nil {
		return err
	}
	bundle, err := secrets.MarshalBundle(map[string]map[string]string{})
	if err != nil {
		return err
	}
	if _, err := s.writeVersion(ctx, SecretPath, projectID, "codebox project secret bundle", bundle); err != nil {
		return err
	}
	return nil
}

// ReadACL returns the latest enabled ACL payload and revision.
func (s *ScalewayStore) ReadACL(ctx context.Context, projectID string) ([]byte, uint32, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return nil, 0, err
	}
	return s.readVersion(ctx, ACLPath, projectID, "latest_enabled")
}

// WriteACL appends a new ACL revision, enforcing baseRevision.
func (s *ScalewayStore) WriteACL(ctx context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return 0, err
	}
	current, err := s.latestRevision(ctx, ACLPath, projectID, "latest_enabled")
	if err != nil {
		return 0, err
	}
	if current != baseRevision {
		return 0, ErrStoreConflict
	}
	return s.writeVersion(ctx, ACLPath, projectID, "codebox project ACL", data)
}

// ReadBundle returns the latest enabled secret bundle and revision.
func (s *ScalewayStore) ReadBundle(ctx context.Context, projectID string) ([]byte, uint32, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return nil, 0, err
	}
	return s.readVersion(ctx, SecretPath, projectID, "latest_enabled")
}

// WriteBundle appends a new bundle revision, enforcing baseRevision.
func (s *ScalewayStore) WriteBundle(ctx context.Context, projectID string, baseRevision uint32, data []byte) (uint32, error) {
	if err := secrets.ValidateProjectID(projectID); err != nil {
		return 0, err
	}
	current, err := s.latestRevision(ctx, SecretPath, projectID, "latest_enabled")
	if err != nil {
		return 0, err
	}
	if current != baseRevision {
		return 0, ErrStoreConflict
	}
	return s.writeVersion(ctx, SecretPath, projectID, "codebox project secret bundle", data)
}

func (s *ScalewayStore) do(req *http.Request) (*http.Response, error) {
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return resp, nil
}
