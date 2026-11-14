package secrets

import (
	"errors"
	"testing"
)

func TestParseFormatAndAuthorizeACL(t *testing.T) {
	raw := `
# Core team leads and members
owner:Lead.One@numerique.gouv.fr

# Bare email defaults to member
alice.martin@numerique.gouv.fr
member:prestataire@agence.fr
# Deduplicated when already an owner:
member:lead.one@numerique.gouv.fr

# Incubator SIRET
siret:13002526500013
`
	acl, err := ParseACL(raw)
	if err != nil {
		t.Fatalf("ParseACL() unexpected error: %v", err)
	}

	if role, ok := acl.Authorize("lead.one@numerique.gouv.fr", ""); !ok || role != RoleOwner {
		t.Fatalf("owner auth = (%q, %v), want (owner, true)", role, ok)
	}
	if role, ok := acl.Authorize("ALICE.MARTIN@numerique.gouv.fr", ""); !ok || role != RoleMember {
		t.Fatalf("alice auth = (%q, %v), want (member, true)", role, ok)
	}
	if role, ok := acl.Authorize("other@ministere.gouv.fr", "13002526500013"); !ok || role != RoleMember {
		t.Fatalf("siret auth = (%q, %v), want (member, true)", role, ok)
	}
	if role, ok := acl.Authorize("intruder@example.com", "99999999999999"); ok {
		t.Fatalf("unexpected authorization for intruder: role=%q", role)
	}

	formatted := acl.Format()
	reparsed, err := ParseACL(formatted)
	if err != nil {
		t.Fatalf("ParseACL(formatted) unexpected error: %v", err)
	}
	if formatted != reparsed.Format() {
		t.Fatalf("Format() is not idempotent:\nfirst:\n%s\nsecond:\n%s", formatted, reparsed.Format())
	}
}

func TestParseACLFailsClosedAndPreventsOwnerLockout(t *testing.T) {
	// Removing the last owner fails closed with ErrNoOwner.
	if _, err := ParseACL("member:alice@numerique.gouv.fr\n"); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("ParseACL(no owner) = %v, want ErrNoOwner", err)
	}
	if _, err := NewACL([]ACLEntry{{Kind: EntrySIRET, Value: "13002526500013"}}); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("NewACL(no owner) = %v, want ErrNoOwner", err)
	}

	// Malformed lines report exact 1-based line numbers instead of being ignored.
	cases := []struct {
		name string
		body string
		line int
	}{
		{
			name: "inline comment whitespace",
			body: "owner:lead@numerique.gouv.fr\nalice@numerique.gouv.fr # inline not allowed\n",
			line: 2,
		},
		{
			name: "unknown prefix",
			body: "owner:lead@numerique.gouv.fr\nadmin:bob@numerique.gouv.fr\n",
			line: 2,
		},
		{
			name: "short siret",
			body: "owner:lead@numerique.gouv.fr\n\nsiret:12345\n",
			line: 3,
		},
		{
			name: "control character",
			body: "owner:lead@numerique.gouv.fr\x00\n",
			line: 1,
		},
	}
	for _, tc := range cases {
		_, err := ParseACL(tc.body)
		var perr *ParseError
		if !errors.As(err, &perr) {
			t.Fatalf("%s: ParseACL error = %v, want *ParseError", tc.name, err)
		}
		if perr.Line != tc.line {
			t.Errorf("%s: ParseError.Line = %d, want %d (%v)", tc.name, perr.Line, tc.line, perr)
		}
	}
}

func FuzzParseACL(f *testing.F) {
	f.Add("owner:lead@numerique.gouv.fr\nmember:dev@beta.gouv.fr\nsiret:13002526500013\n")
	f.Add("# comment\nowner:a@b.fr\r\n")
	f.Add("admin:nobody@example.com")
	f.Fuzz(func(t *testing.T, input string) {
		acl, err := ParseACL(input)
		if err != nil {
			return
		}
		formatted := acl.Format()
		acl2, err := ParseACL(formatted)
		if err != nil {
			t.Fatalf("canonical Format() output failed to re-parse: %v\n%s", err, formatted)
		}
		if acl2.Format() != formatted {
			t.Fatalf("canonical Format() output drifted across round-trip")
		}
	})
}
