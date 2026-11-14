// Package secrets defines the shared wire contract, validation invariants, and
// access-control list (ACL) format used by both the codebox CLI and the
// stateless secrets-gateway service.
//
// The package intentionally depends only on the Go standard library so both
// applications and standalone security scanners (GOWORK=off) can build and
// verify it without transitive network or runtime dependencies.
package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// BundleSchemaVersion is the current wire and storage format version for
	// per-project secret bundles stored in Scaleway Secret Manager.
	BundleSchemaVersion = 1

	// MaxBundleBytes caps the serialized JSON size of a project secret bundle.
	// Scaleway Secret Manager enforces a 64 KiB payload ceiling per secret
	// version; 60 KiB leaves deterministic headroom for envelope metadata.
	MaxBundleBytes = 60 * 1024

	// MaxComponentsPerBundle bounds the number of dotenv components in one
	// project bundle.
	MaxComponentsPerBundle = 32

	// MaxVariablesPerComponent bounds the number of variables per dotenv
	// component.
	MaxVariablesPerComponent = 256

	// MaxDestinationsPerBinding matches codebox's host-binding destination cap.
	MaxDestinationsPerBinding = 32

	// DeliveryProxy substitutes a placeholder at the Microsandbox network
	// boundary toward explicitly approved destination hosts.
	DeliveryProxy = "proxy"

	// DeliveryGuest writes the real secret value into a guest-readable dotenv
	// file for applications that consume credentials locally.
	DeliveryGuest = "guest"
)

// Redacted API error codes returned by secrets-gateway. Error payloads never
// echo secret values, OAuth authorization codes, or bearer tokens.
const (
	ErrCodeUnauthorized        = "unauthorized"
	ErrCodeMFARequired         = "mfa_required"
	ErrCodeForbidden           = "forbidden"
	ErrCodeNotFound            = "not_found"
	ErrCodeConflict            = "conflict"
	ErrCodeInvalidRequest      = "invalid_request"
	ErrCodeProductionForbidden = "production_forbidden"
	ErrCodePayloadTooLarge     = "payload_too_large"
	ErrCodeACLCorrupt          = "acl_corrupt"
	ErrCodeRateLimited         = "rate_limited"
	ErrCodeUpstreamUnavailable = "upstream_unavailable"
)

var (
	// ErrProductionScopeForbidden is returned whenever a caller attempts to
	// target a production scope or profile. Shared secrets in codebox are
	// strictly scoped to development workflows.
	ErrProductionScopeForbidden = errors.New("production secrets are out of scope; shared codebox secrets support development workflows only")

	// ErrBundleTooLarge is returned when a serialized bundle exceeds MaxBundleBytes.
	ErrBundleTooLarge = fmt.Errorf("secret bundle exceeds maximum size (%d bytes)", MaxBundleBytes)

	// ErrInvalidProjectID is returned when a project identifier is not a
	// canonical lowercase RFC 4122 UUIDv4.
	ErrInvalidProjectID = errors.New("project ID must be a canonical lowercase UUIDv4")
)

// Bundle is the versioned per-project secret payload stored under
// /secrets/<projectId> in the dedicated codebox-dev-secrets Scaleway project.
type Bundle struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Components    map[string]map[string]string `json:"components"`
}

// BindingHint is non-secret delivery metadata stored in .codebox/project.json
// so teammates do not have to discover each variable's proxy host or delivery
// mode manually. Declaring a BindingHint in a repository never authorizes
// delivery; every host approves destinations locally in bindings.json.
type BindingHint struct {
	Component    string   `json:"component"`
	Name         string   `json:"name"`
	Delivery     string   `json:"delivery,omitempty"`
	GuestEnv     string   `json:"guestEnv,omitempty"`
	Destinations []string `json:"destinations,omitempty"`
}

// NormalizedDelivery returns DeliveryProxy when Delivery is empty, or the
// explicit delivery mode otherwise.
func (h BindingHint) NormalizedDelivery() string {
	if h.Delivery == "" {
		return DeliveryProxy
	}
	return h.Delivery
}

// WhoAmIResponse reports the caller's verified ProConnect identity.
type WhoAmIResponse struct {
	Sub       string    `json:"sub"`
	Email     string    `json:"email"`
	SIRET     string    `json:"siret,omitempty"`
	ACR       string    `json:"acr"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// CreateProjectRequest is sent by `codebox secrets init`.
type CreateProjectRequest struct {
	Name string `json:"name,omitempty"`
}

// CreateProjectResponse returns the newly allocated project UUID and initial
// Scaleway Secret Manager revisions.
type CreateProjectResponse struct {
	ProjectID       string `json:"projectId"`
	OwnerEmail      string `json:"ownerEmail"`
	ACLRevision     uint32 `json:"aclRevision"`
	SecretsRevision uint32 `json:"secretsRevision"`
}

// MembersResponse returns the parsed ACL entries and current Scaleway revision
// for `/acl/<projectId>`.
type MembersResponse struct {
	ProjectID string     `json:"projectId"`
	Revision  uint32     `json:"revision"`
	Entries   []ACLEntry `json:"entries"`
}

// UpdateMembersRequest replaces the ACL at BaseRevision.
type UpdateMembersRequest struct {
	BaseRevision uint32     `json:"baseRevision"`
	Entries      []ACLEntry `json:"entries"`
}

// PushSecretsRequest uploads a complete component secret map at BaseRevision.
type PushSecretsRequest struct {
	Scope        string                       `json:"scope,omitempty"`
	BaseRevision uint32                       `json:"baseRevision"`
	Components   map[string]map[string]string `json:"components"`
}

// PushSecretsResponse reports the new immutable Scaleway revision.
type PushSecretsResponse struct {
	ProjectID string `json:"projectId"`
	Revision  uint32 `json:"revision"`
}

// ResolveRequest requests the latest enabled development secret bundle for a
// project, optionally filtered to a subset of dotenv component paths.
type ResolveRequest struct {
	Scope      string   `json:"scope,omitempty"`
	Components []string `json:"components,omitempty"`
}

// ResolveResponse returns the resolved component values in memory along with
// the backing Scaleway revision.
type ResolveResponse struct {
	ProjectID  string                       `json:"projectId"`
	Revision   uint32                       `json:"revision"`
	Components map[string]map[string]string `json:"components"`
}

// DeviceTokenExchangeRequest exchanges a one-time `--no-browser` login code
// for the signed gateway JWT after the user completes ProConnect in a browser.
type DeviceTokenExchangeRequest struct {
	SessionID string `json:"sessionId"`
	Verifier  string `json:"verifier"`
}

// TokenResponse carries a signed gateway bearer token.
type TokenResponse struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
	Identity  WhoAmIResponse `json:"identity"`
}

// APIError is the structured error envelope returned by secrets-gateway.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// NewProjectID generates a random canonical lowercase RFC 4122 UUIDv4.
func NewProjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate project ID: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	), nil
}

// ValidateProjectID enforces canonical lowercase UUIDv4 syntax so a projectId
// can never inject path segments or metacharacters into Scaleway secret names.
func ValidateProjectID(id string) error {
	if len(id) != 36 {
		return ErrInvalidProjectID
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ErrInvalidProjectID
			}
		case 14:
			if c != '4' {
				return ErrInvalidProjectID
			}
		case 19:
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return ErrInvalidProjectID
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return ErrInvalidProjectID
			}
		}
	}
	return nil
}

// RejectProductionScope rejects any scope or profile name that designates a
// production environment ("prod", "production", "prd", or prefixed variants).
func RejectProductionScope(scope string) error {
	norm := strings.ToLower(strings.TrimSpace(scope))
	if norm == "" {
		return nil
	}
	switch norm {
	case "prod", "production", "prd", "live":
		return ErrProductionScopeForbidden
	}
	for _, prefix := range []string{"prod-", "prod_", "production-", "production_", "prd-", "prd_"} {
		if strings.HasPrefix(norm, prefix) {
			return ErrProductionScopeForbidden
		}
	}
	for _, suffix := range []string{"-prod", "_prod", "-production", "_production"} {
		if strings.HasSuffix(norm, suffix) {
			return ErrProductionScopeForbidden
		}
	}
	return nil
}

// ValidateComponentPath checks that p is a clean relative dotenv path inside a
// project root (for example ".env" or "apps/api/.env.local"), matching
// codebox's validSecretComponent contract.
func ValidateComponentPath(p string) error {
	if !utf8.ValidString(p) {
		return fmt.Errorf("component path contains invalid UTF-8")
	}
	if p == "" || len(p) > 256 || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:") {
		return fmt.Errorf("invalid dotenv component path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid dotenv component path %q", p)
		}
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return fmt.Errorf("invalid control character in component path")
		}
	}
	return nil
}

// ValidateVariableName checks that name is a portable 1..128 ASCII environment
// variable identifier ([A-Za-z_][A-Za-z0-9_]*).
func ValidateVariableName(name string) error {
	if name == "" || len(name) > 128 {
		return fmt.Errorf("invalid secret variable name")
	}
	for i, r := range name {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(i > 0 && r >= '0' && r <= '9') {
			return fmt.Errorf("invalid secret variable name %q", name)
		}
	}
	return nil
}

// ValidateVariableValue checks that value is valid UTF-8 without NUL bytes.
// Unlike local OS credential stores, remote bundles do not impose the 951-byte
// Windows Credential Manager limit per variable; total size is bounded by
// MaxBundleBytes.
func ValidateVariableValue(name, value string) error {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("secret variable %s must be valid UTF-8 without NUL bytes", name)
	}
	return nil
}

// ValidateDestinationHost checks that host is a canonical lowercase DNS name
// suitable for Microsandbox proxy allowlisting (no scheme, port, path,
// wildcard, or bare IPv4 literal).
func ValidateDestinationHost(host string) error {
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") {
		return fmt.Errorf("destination %q is not a valid lowercase DNS hostname", host)
	}
	labels := strings.Split(host, ".")
	allNumericLabels := true
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("destination %q is not a valid lowercase DNS hostname", host)
		}
		labelDigitsOnly := true
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("destination %q is not a valid lowercase DNS hostname", host)
			}
			if r < '0' || r > '9' {
				labelDigitsOnly = false
			}
		}
		if !labelDigitsOnly {
			allNumericLabels = false
		}
	}
	if allNumericLabels {
		return fmt.Errorf("destination %q must be a DNS hostname, not an IPv4 address", host)
	}
	return nil
}

// DeriveGuestEnv computes codebox's deterministic default guest variable name
// for a dotenv component path and secret identifier (for example ".env" +
// "API_KEY" -> "JC_API_KEY", "apps/api/.env" + "API_KEY" ->
// "JC_APPS_API_API_KEY").
func DeriveGuestEnv(component, name string) string {
	var parts []string
	dir, base := path.Split(component)
	for _, segment := range strings.Split(strings.Trim(dir, "/"), "/") {
		if segment == "" || segment == "." {
			continue
		}
		if s := sanitizeGuestEnvSegment(segment); s != "" {
			parts = append(parts, s)
		}
	}
	if stem := dotenvComponentStem(base); stem != "" {
		parts = append(parts, stem)
	}
	return "JC_" + strings.Join(append(parts, name), "_")
}

func dotenvComponentStem(base string) string {
	lower := strings.ToLower(base)
	switch {
	case lower == ".env":
		return ""
	case strings.HasPrefix(lower, ".env."):
		return sanitizeGuestEnvSegment(strings.TrimPrefix(lower, ".env."))
	case strings.HasSuffix(lower, ".env"):
		return sanitizeGuestEnvSegment(strings.TrimSuffix(lower, ".env"))
	default:
		return sanitizeGuestEnvSegment(lower)
	}
}

func sanitizeGuestEnvSegment(segment string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToUpper(segment) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// ValidateBindingHints validates a slice of manifest binding hints for
// structural soundness, uniqueness, and delivery-mode invariants.
func ValidateBindingHints(hints []BindingHint) error {
	seenIdentity := make(map[string]bool, len(hints))
	seenGuestEnv := make(map[string]string, len(hints))
	for _, h := range hints {
		if err := ValidateComponentPath(h.Component); err != nil {
			return err
		}
		if err := ValidateVariableName(h.Name); err != nil {
			return err
		}
		id := h.Component + "\x00" + h.Name
		if seenIdentity[id] {
			return fmt.Errorf("duplicate binding hint for %s in %s", h.Name, h.Component)
		}
		seenIdentity[id] = true

		switch h.NormalizedDelivery() {
		case DeliveryGuest:
			if len(h.Destinations) > 0 {
				return fmt.Errorf("guest-readable binding hint %s (%s) must not declare destinations", h.Name, h.Component)
			}
			if h.GuestEnv != "" {
				return fmt.Errorf("guest-readable binding hint %s (%s) must not declare guestEnv", h.Name, h.Component)
			}
		case DeliveryProxy:
			effectiveEnv := h.GuestEnv
			if effectiveEnv == "" {
				effectiveEnv = DeriveGuestEnv(h.Component, h.Name)
			}
			if err := ValidateVariableName(effectiveEnv); err != nil {
				return fmt.Errorf("binding hint %s (%s) has invalid guestEnv %q", h.Name, h.Component, effectiveEnv)
			}
			if prev, dup := seenGuestEnv[effectiveEnv]; dup {
				return fmt.Errorf("binding hints %s and %s:%s share guestEnv %s", prev, h.Component, h.Name, effectiveEnv)
			}
			seenGuestEnv[effectiveEnv] = h.Component + ":" + h.Name
			if len(h.Destinations) == 0 {
				return fmt.Errorf("proxy binding hint %s (%s) requires at least one destination host", h.Name, h.Component)
			}
			if len(h.Destinations) > MaxDestinationsPerBinding {
				return fmt.Errorf("proxy binding hint %s (%s) exceeds %d destinations", h.Name, h.Component, MaxDestinationsPerBinding)
			}
			seenHost := make(map[string]bool, len(h.Destinations))
			for _, host := range h.Destinations {
				if err := ValidateDestinationHost(host); err != nil {
					return fmt.Errorf("binding hint %s (%s): %w", h.Name, h.Component, err)
				}
				if seenHost[host] {
					return fmt.Errorf("binding hint %s (%s) lists destination %s twice", h.Name, h.Component, host)
				}
				seenHost[host] = true
			}
		default:
			return fmt.Errorf("binding hint %s (%s) has unsupported delivery mode %q", h.Name, h.Component, h.Delivery)
		}
	}
	return nil
}

// ValidateComponents validates a map of component -> variable -> value.
func ValidateComponents(components map[string]map[string]string) error {
	if len(components) > MaxComponentsPerBundle {
		return fmt.Errorf("bundle exceeds maximum component count (%d)", MaxComponentsPerBundle)
	}
	for comp, vars := range components {
		if err := ValidateComponentPath(comp); err != nil {
			return err
		}
		if len(vars) > MaxVariablesPerComponent {
			return fmt.Errorf("component %s exceeds maximum variable count (%d)", comp, MaxVariablesPerComponent)
		}
		for name, val := range vars {
			if err := ValidateVariableName(name); err != nil {
				return fmt.Errorf("component %s: %w", comp, err)
			}
			if err := ValidateVariableValue(name, val); err != nil {
				return fmt.Errorf("component %s: %w", comp, err)
			}
		}
	}
	return nil
}

// ValidateBundle checks schema version and component map invariants.
func ValidateBundle(b Bundle) error {
	if b.SchemaVersion != BundleSchemaVersion {
		return fmt.Errorf("unsupported secret bundle schemaVersion %d", b.SchemaVersion)
	}
	return ValidateComponents(b.Components)
}

// MarshalBundle validates and deterministically serializes a Bundle, enforcing
// MaxBundleBytes on the resulting JSON payload.
func MarshalBundle(components map[string]map[string]string) ([]byte, error) {
	if err := ValidateComponents(components); err != nil {
		return nil, err
	}
	clean := make(map[string]map[string]string, len(components))
	for comp, vars := range components {
		if len(vars) == 0 {
			continue
		}
		copied := make(map[string]string, len(vars))
		for k, v := range vars {
			copied[k] = v
		}
		clean[comp] = copied
	}
	b := Bundle{
		SchemaVersion: BundleSchemaVersion,
		Components:    clean,
	}
	if err := ValidateBundle(b); err != nil {
		return nil, err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("marshal secret bundle: %w", err)
	}
	if len(data) > MaxBundleBytes {
		return nil, ErrBundleTooLarge
	}
	return data, nil
}

// UnmarshalBundle parses and validates a serialized secret Bundle using a
// strict token-level JSON reader so null values, non-integer schema versions,
// duplicate keys, unknown fields, trailing tokens, and malformed UTF-8 or
// unpaired UTF-16 surrogates all fail closed without silent coercion.
func UnmarshalBundle(data []byte) (Bundle, error) {
	if len(data) > MaxBundleBytes {
		return Bundle{}, ErrBundleTooLarge
	}
	if !utf8.Valid(data) || hasUnpairedUTF16Surrogate(data) {
		return Bundle{}, errors.New("parse secret bundle: payload contains invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	if err := expectJSONDelim(dec, '{', "bundle root"); err != nil {
		return Bundle{}, err
	}

	var b Bundle
	seenTop := make(map[string]bool, 2)
	for {
		tok, err := dec.Token()
		if err != nil {
			return Bundle{}, fmt.Errorf("parse secret bundle: %w", err)
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			break
		}
		key, ok := tok.(string)
		if !ok {
			return Bundle{}, errors.New("parse secret bundle: expected object key")
		}
		if seenTop[key] {
			return Bundle{}, fmt.Errorf("parse secret bundle: duplicate top-level key %q", key)
		}
		seenTop[key] = true

		switch key {
		case "schemaVersion":
			vTok, err := dec.Token()
			if err != nil {
				return Bundle{}, fmt.Errorf("parse secret bundle schemaVersion: %w", err)
			}
			num, ok := vTok.(json.Number)
			if !ok {
				return Bundle{}, errors.New("parse secret bundle: schemaVersion must be an integer")
			}
			ver, err := strconv.Atoi(num.String())
			if err != nil {
				return Bundle{}, errors.New("parse secret bundle: schemaVersion must be an integer")
			}
			b.SchemaVersion = ver
		case "components":
			comps, err := decodeBundleComponents(dec)
			if err != nil {
				return Bundle{}, err
			}
			b.Components = comps
		default:
			return Bundle{}, fmt.Errorf("parse secret bundle: unknown field %q", key)
		}
	}

	if !seenTop["schemaVersion"] || !seenTop["components"] {
		return Bundle{}, errors.New("parse secret bundle: schemaVersion and components are required")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Bundle{}, errors.New("parse secret bundle: trailing data")
	}
	if err := ValidateBundle(b); err != nil {
		return Bundle{}, err
	}
	for comp, vars := range b.Components {
		if len(vars) == 0 {
			delete(b.Components, comp)
		}
	}
	return b, nil
}

func decodeBundleComponents(dec *json.Decoder) (map[string]map[string]string, error) {
	if err := expectJSONDelim(dec, '{', "components"); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string)
	seenComp := make(map[string]bool)
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("parse secret bundle components: %w", err)
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			return out, nil
		}
		comp, ok := tok.(string)
		if !ok {
			return nil, errors.New("parse secret bundle: expected component path string")
		}
		if seenComp[comp] {
			return nil, fmt.Errorf("parse secret bundle: duplicate component %q", comp)
		}
		seenComp[comp] = true

		if err := expectJSONDelim(dec, '{', "component "+comp); err != nil {
			return nil, err
		}
		vars := make(map[string]string)
		seenVar := make(map[string]bool)
		for {
			kTok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("parse secret bundle component %s: %w", comp, err)
			}
			if d, ok := kTok.(json.Delim); ok && d == '}' {
				break
			}
			name, ok := kTok.(string)
			if !ok {
				return nil, fmt.Errorf("parse secret bundle component %s: expected variable name", comp)
			}
			if seenVar[name] {
				return nil, fmt.Errorf("parse secret bundle component %s: duplicate variable %q", comp, name)
			}
			seenVar[name] = true

			vTok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("parse secret bundle variable %s: %w", name, err)
			}
			val, ok := vTok.(string)
			if !ok {
				return nil, fmt.Errorf("parse secret bundle: variable %s in %s must be a JSON string", name, comp)
			}
			vars[name] = val
		}
		out[comp] = vars
	}
}

func expectJSONDelim(dec *json.Decoder, want rune, where string) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("parse secret bundle (%s): %w", where, err)
	}
	d, ok := tok.(json.Delim)
	if !ok || d != json.Delim(want) {
		return fmt.Errorf("parse secret bundle: %s must be a JSON object", where)
	}
	return nil
}

func hasUnpairedUTF16Surrogate(data []byte) bool {
	inString := false
	for i := 0; i < len(data); {
		c := data[i]
		if !inString {
			if c == '"' {
				inString = true
			}
			i++
			continue
		}
		if c == '"' {
			inString = false
			i++
			continue
		}
		if c != '\\' {
			i++
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i += 2
			continue
		}
		if i+6 > len(data) {
			return false
		}
		u1, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if u1 >= 0xDC00 && u1 <= 0xDFFF {
			return true
		}
		if u1 >= 0xD800 && u1 <= 0xDBFF {
			if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return true
			}
			u2, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || u2 < 0xDC00 || u2 > 0xDFFF {
				return true
			}
			i += 12
			continue
		}
		i += 6
	}
	return false
}

// CanonicalizeBindingHints returns a copy of hints sorted by (Component, Name)
// with sorted destination lists and default "proxy" omitted for clean diffs.
func CanonicalizeBindingHints(hints []BindingHint) []BindingHint {
	out := make([]BindingHint, len(hints))
	for i, h := range hints {
		c := BindingHint{
			Component: h.Component,
			Name:      h.Name,
			GuestEnv:  h.GuestEnv,
		}
		if h.Delivery == DeliveryGuest {
			c.Delivery = DeliveryGuest
		}
		if len(h.Destinations) > 0 {
			c.Destinations = append([]string(nil), h.Destinations...)
			sort.Strings(c.Destinations)
		}
		out[i] = c
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].Name < out[j].Name
	})
	return out
}
