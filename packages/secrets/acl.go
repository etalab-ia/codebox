package secrets

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Role identifies a caller's effective permission level on a project.
type Role string

const (
	// RoleOwner grants full project access: resolve secrets, push secret
	// bundles, and update the project ACL.
	RoleOwner Role = "owner"

	// RoleMember grants read-only secret resolution for the project.
	RoleMember Role = "member"
)

// EntryKind distinguishes email principals from organization-wide SIRET rules.
type EntryKind string

const (
	EntryOwner  EntryKind = "owner"
	EntryMember EntryKind = "member"
	EntrySIRET  EntryKind = "siret"
)

// MaxACLBytes caps the raw size of an `/acl/<projectId>` secret.
const MaxACLBytes = 32 * 1024

// MaxACLEntries bounds the number of principals in a single project ACL.
const MaxACLEntries = 256

// ErrNoOwner is returned when an ACL write would leave a project with zero
// explicit `owner:<email>` principals, preventing accidental lockout.
var ErrNoOwner = errors.New("project ACL must retain at least one owner:<email> entry")

// ACLEntry represents one normalized rule in `/acl/<projectId>`.
type ACLEntry struct {
	Kind  EntryKind `json:"kind"`
	Value string    `json:"value"`
}

// ParseError reports a fail-closed syntax error in a line-oriented ACL payload.
// Malformed lines are never skipped silently.
type ParseError struct {
	Line   int
	Reason string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("invalid ACL at line %d: %s", e.Line, e.Reason)
}

// ACL is a validated, immutable in-memory access-control list for one project.
type ACL struct {
	entries []ACLEntry
	owners  map[string]bool
	members map[string]bool
	sirets  map[string]bool
}

// Entries returns a defensive copy of the canonicalized ACL entries.
func (a ACL) Entries() []ACLEntry {
	out := make([]ACLEntry, len(a.entries))
	copy(out, a.entries)
	return out
}

// Authorize evaluates whether a verified ProConnect identity (email and
// optional 14-digit SIRET) may access the project, returning the highest
// granted role. Explicit owner status always takes precedence over member or
// SIRET rules.
func (a ACL) Authorize(email, siret string) (Role, bool) {
	normEmail, err := NormalizeEmail(email)
	if err == nil {
		if a.owners[normEmail] {
			return RoleOwner, true
		}
		if a.members[normEmail] {
			return RoleMember, true
		}
	}
	normSIRET := strings.TrimSpace(siret)
	if ValidateSIRET(normSIRET) == nil && a.sirets[normSIRET] {
		return RoleMember, true
	}
	return "", false
}

// ParseACL parses the plain line-oriented ACL format stored in `/acl/<id>`:
//
//   - Blank lines and lines starting with `#` are ignored.
//   - `owner:<email>` grants RoleOwner.
//   - `member:<email>` or bare `<email>` grants RoleMember.
//   - `siret:<14-digits>` grants RoleMember to callers authenticated under that SIRET.
//
// Any malformed line, control character, unknown prefix, or ACL lacking an
// owner fails closed with an error; no partial ACL is returned.
func ParseACL(text string) (ACL, error) {
	if len(text) > MaxACLBytes {
		return ACL{}, fmt.Errorf("ACL exceeds maximum size (%d bytes)", MaxACLBytes)
	}
	lines := strings.Split(text, "\n")
	var raw []ACLEntry
	for idx, line := range lines {
		lineNo := idx + 1
		line = strings.TrimSuffix(line, "\r")
		for _, r := range line {
			if (r < 32 && r != '\t') || r == 127 {
				return ACL{}, &ParseError{Line: lineNo, Reason: "contains control characters"}
			}
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := parseACLEntryLine(trimmed)
		if err != nil {
			return ACL{}, &ParseError{Line: lineNo, Reason: err.Error()}
		}
		raw = append(raw, entry)
		if len(raw) > MaxACLEntries {
			return ACL{}, &ParseError{Line: lineNo, Reason: fmt.Sprintf("exceeds maximum of %d ACL entries", MaxACLEntries)}
		}
	}
	return NewACL(raw)
}

func parseACLEntryLine(line string) (ACLEntry, error) {
	if strings.ContainsAny(line, " \t") {
		return ACLEntry{}, errors.New("unexpected whitespace inside entry (place comments on their own '#' line)")
	}
	if idx := strings.IndexByte(line, ':'); idx >= 0 {
		prefix := strings.ToLower(line[:idx])
		val := line[idx+1:]
		switch prefix {
		case string(EntryOwner):
			email, err := NormalizeEmail(val)
			if err != nil {
				return ACLEntry{}, err
			}
			return ACLEntry{Kind: EntryOwner, Value: email}, nil
		case string(EntryMember):
			email, err := NormalizeEmail(val)
			if err != nil {
				return ACLEntry{}, err
			}
			return ACLEntry{Kind: EntryMember, Value: email}, nil
		case string(EntrySIRET):
			if err := ValidateSIRET(val); err != nil {
				return ACLEntry{}, err
			}
			return ACLEntry{Kind: EntrySIRET, Value: val}, nil
		default:
			return ACLEntry{}, fmt.Errorf("unknown rule prefix %q (expected owner:, member:, or siret:)", prefix)
		}
	}
	email, err := NormalizeEmail(line)
	if err != nil {
		return ACLEntry{}, err
	}
	return ACLEntry{Kind: EntryMember, Value: email}, nil
}

// NewACL validates, deduplicates, and canonicalizes a slice of ACLEntry rules,
// enforcing that at least one explicit owner remains present.
func NewACL(entries []ACLEntry) (ACL, error) {
	if len(entries) > MaxACLEntries {
		return ACL{}, fmt.Errorf("ACL exceeds maximum of %d entries", MaxACLEntries)
	}
	owners := make(map[string]bool)
	members := make(map[string]bool)
	sirets := make(map[string]bool)

	for _, e := range entries {
		switch e.Kind {
		case EntryOwner:
			email, err := NormalizeEmail(e.Value)
			if err != nil {
				return ACL{}, fmt.Errorf("invalid owner entry: %w", err)
			}
			owners[email] = true
		case EntryMember:
			email, err := NormalizeEmail(e.Value)
			if err != nil {
				return ACL{}, fmt.Errorf("invalid member entry: %w", err)
			}
			members[email] = true
		case EntrySIRET:
			s := strings.TrimSpace(e.Value)
			if err := ValidateSIRET(s); err != nil {
				return ACL{}, fmt.Errorf("invalid siret entry: %w", err)
			}
			sirets[s] = true
		default:
			return ACL{}, fmt.Errorf("unsupported ACL entry kind %q", e.Kind)
		}
	}

	if len(owners) == 0 {
		return ACL{}, ErrNoOwner
	}

	// An explicit owner does not also need a redundant member entry.
	for owner := range owners {
		delete(members, owner)
	}

	canonical := make([]ACLEntry, 0, len(owners)+len(members)+len(sirets))
	for _, email := range sortedKeys(owners) {
		canonical = append(canonical, ACLEntry{Kind: EntryOwner, Value: email})
	}
	for _, email := range sortedKeys(members) {
		canonical = append(canonical, ACLEntry{Kind: EntryMember, Value: email})
	}
	for _, s := range sortedKeys(sirets) {
		canonical = append(canonical, ACLEntry{Kind: EntrySIRET, Value: s})
	}

	return ACL{
		entries: canonical,
		owners:  owners,
		members: members,
		sirets:  sirets,
	}, nil
}

// Format serializes the ACL into canonical line-oriented text suitable for
// storing in Scaleway Secret Manager and inspecting in the Scaleway Console.
func (a ACL) Format() string {
	var b strings.Builder
	b.WriteString("# codebox project ACL (managed via `codebox secrets members`)\n")
	for _, e := range a.entries {
		b.WriteString(string(e.Kind))
		b.WriteByte(':')
		b.WriteString(e.Value)
		b.WriteByte('\n')
	}
	return b.String()
}

// NormalizeEmail validates and lowercases an email address for ACL matching.
func NormalizeEmail(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 5 || len(s) > 254 {
		return "", fmt.Errorf("invalid email address %q", raw)
	}
	if strings.ContainsAny(s, " \t\r\n/\\:") {
		return "", fmt.Errorf("invalid email address %q", raw)
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 {
		return "", fmt.Errorf("invalid email address %q", raw)
	}
	local, domain := s[:at], s[at+1:]
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", fmt.Errorf("invalid email local part in %q", raw)
	}
	for _, r := range local {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", r)) {
			return "", fmt.Errorf("invalid character in email %q", raw)
		}
	}
	if err := ValidateDestinationHost(domain); err != nil {
		return "", fmt.Errorf("invalid email domain in %q", raw)
	}
	return s, nil
}

// ValidateSIRET checks that s consists of exactly 14 ASCII digits. It does not
// enforce a Luhn checksum because ProConnect sandbox/test identity providers
// issue synthetic 14-digit SIRET values.
func ValidateSIRET(s string) error {
	if len(s) != 14 {
		return fmt.Errorf("SIRET %q must be exactly 14 digits", s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fmt.Errorf("SIRET %q must contain digits only", s)
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
