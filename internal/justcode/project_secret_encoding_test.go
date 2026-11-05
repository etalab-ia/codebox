package justcode

import (
	"strings"
	"testing"
)

// The encoding is the contract for "delivered exactly": a value that cannot be
// represented must be refused, never written in a form a loader would change.

func TestDotenvLiteralRoundTripsThroughTheQualifiedLoaders(t *testing.T) {
	// Verified against Node dotenv 16.6.1 (with and without dotenv-expand
	// 13.0.0), python-dotenv 1.2.4 and godotenv 1.5.1: single quotes preserve
	// all of these exactly. The Node and Python halves of that measurement run
	// in tests/integration/project-secrets; this test covers godotenv, whose
	// pin is unchanged.
	for _, value := range []string{
		"plain",
		"line1\nline2",
		"-----BEGIN KEY-----\nAAAA\n-----END KEY-----",
		"trailing   ",
		"   leading",
		"hash # inside",
		`has " quote`,
		// A backslash is safe on its own, including at the start: only a pair
		// and a trailing one are refused below.
		`back\slash`,
		`\leading`,
		"",
	} {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%q must be representable: %v", value, err)
		}
		if !strings.HasPrefix(literal, "'") || !strings.HasSuffix(literal, "'") {
			t.Fatalf("%q must be single-quoted, got %q", value, literal)
		}
		if literal[1:len(literal)-1] != value {
			t.Fatalf("%q must survive verbatim inside the quotes, got %q", value, literal)
		}
	}
}

// The shapes quoting cannot carry are written unquoted, not refused: an
// unquoted value has no escape processing in any pinned loader.
func TestDotenvLiteralUsesTheRawFormForTheBackslashShapes(t *testing.T) {
	for name, value := range map[string]string{
		"backslash pair":     `a\\b`,
		"trailing backslash": `trailing\`,
		"windows path":       `C:\Users\`,
		"unc path":           `\\server\share`,
		"leading pair":       `\\leading`,
		"pair then a space":  `a\\b c`,
	} {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s (%q) must be representable: %v", name, value, err)
		}
		if literal != value {
			t.Fatalf("%s must be written unquoted and verbatim: got %q, want %q", name, literal, value)
		}
	}
}

// A value that quoting cannot carry is not automatically refused: an unquoted
// value is never decoded, so anything the loaders do not strip, cut or expand
// passes through byte-for-byte. Non-ASCII is the large case of this, and it is
// most of the world's file names.
func TestDotenvLiteralUsesTheRawFormForNonASCII(t *testing.T) {
	for name, value := range map[string]string{
		"accented windows path":  `C:\Répertoire\`,
		"accented trailing path": `naïve\`,
		"greek":                  `Ω\`,
		"cjk":                    `秘密\`,
		"emoji":                  `🚀\`,
		"arabic":                 `مجلد\`,
		"mixed":                  `C:\Données\rép-🚀\`,
		"accents and pair":       `C:\\Répertoire`,
	} {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s (%q) must be representable: %v", name, value, err)
		}
		if literal != value {
			t.Fatalf("%s must be written unquoted and verbatim: got %q, want %q", name, literal, value)
		}
	}
}

// A carriage return is refused by both forms. Quoting would normalise it to a
// newline in Node and Python; raw would end the value. Refusing is the only
// outcome that does not alter the secret.
func TestDotenvLiteralRefusesACarriageReturn(t *testing.T) {
	for name, value := range map[string]string{
		"bare cr":         "a\rb",
		"crlf":            "line1\r\nline2",
		"cr at the end":   "a\r",
		"cr in a key pem": "-----BEGIN KEY-----\r\nAAAA\r\n-----END KEY-----",
	} {
		_, err := dotenvLiteral(value)
		if err == nil {
			t.Fatalf("%s must be refused: a quoted value is normalised and a raw one ends at the CR", name)
		}
		if !strings.Contains(err.Error(), "carriage return") {
			t.Fatalf("%s: the refusal must name the carriage return: %v", name, err)
		}
	}
}

// The edge of an unquoted value is where the loaders trim. The raw form must
// refuse a character the loaders strip at the edge, whatever it is, because it
// would deliver the value with that character silently gone.
func TestDotenvRawObstacleRefusesATrimmedEdge(t *testing.T) {
	for name, value := range map[string]string{
		"leading space":            " a",
		"trailing space":           "a ",
		"leading tab":              "\ta",
		"trailing tab":             "a\t",
		"trailing nbsp":            "a\u00a0",
		"leading nbsp":             "\u00a0a",
		"trailing ideographic":     "a\u3000",
		"leading ideographic":      "\u3000a",
		"trailing hair space":      "a\u200a",
		"leading ogham space":      "\u1680a",
		"leading file separator":   "\x1ca",
		"trailing group separator": "a\x1d",
		"trailing bom":             "a\ufeff",
		"leading bom":              "\ufeffa",
		"vertical tab":             "\x0ba",
	} {
		if err := dotenvRawObstacle(value); err == nil {
			t.Fatalf("%s (%q) must be refused raw: the loaders strip that edge character", name, value)
		}
	}
	// The same characters are exact in the interior, so raw must carry them.
	for name, value := range map[string]string{
		"interior tab":            "a\tb",
		"interior nbsp":           "a\u00a0b",
		"interior ideographic":    "a\u3000b",
		"interior file separator": "a\x1cb",
		"interior bom":            "a\ufeffb",
	} {
		if err := dotenvRawObstacle(value); err != nil {
			t.Fatalf("%s (%q) must be representable raw: %v", name, value, err)
		}
	}
}

// The refusal above is about the raw form, not about the value: quoting keeps
// leading and trailing whitespace exactly, so those values are still
// delivered, in their quoted form. Only a value that needs raw for another
// reason (a backslash pair, a trailing backslash) and also carries a trimmed
// edge has no form left.
func TestDotenvLiteralStillDeliversWhitespaceEdgesByQuoting(t *testing.T) {
	for name, value := range map[string]string{
		"leading nbsp":   "\u00a0a",
		"trailing nbsp":  "a\u00a0",
		"trailing space": "a ",
		"leading tab":    "\ta",
		"trailing bom":   "a\ufeff",
		"interior nbsp":  "a\u00a0b",
	} {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s (%q) must be representable by quoting: %v", name, value, err)
		}
		if literal != "'"+value+"'" {
			t.Fatalf("%s must be single-quoted and verbatim: got %q", name, literal)
		}
	}
	// A pair plus a trimmed edge has no form: quoting collapses the pair,
	// raw drops the edge character.
	if _, err := dotenvLiteral(`a\\b `); err == nil {
		t.Fatal("a backslash pair with a trailing space must be refused: neither form is exact")
	}
}

// Invalid UTF-8 is refused before either form. Measured: python-dotenv raises
// UnicodeDecodeError and loses the file, Go and Node substitute replacement
// characters (and a quoted invalid byte can make godotenv read the following
// line into this value), so no encoding delivers the original bytes.
func TestDotenvLiteralRefusesInvalidUTF8(t *testing.T) {
	for name, value := range map[string]string{
		"truncated sequence": "ab\xc3",
		"lone continuation":  "C:\\R\xe9pertoire\\",
		"overlong":           "ab\xc0\xaf",
		"bad byte":           "ab\xff",
	} {
		_, err := dotenvLiteral(value)
		if err == nil {
			t.Fatalf("%s must be refused: the loaders decode it as replacement characters", name)
		}
		if !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("%s: the refusal must name the encoding: %v", name, err)
		}
	}
}

// Quoting stays the default for everything it can carry, so nothing that
// worked before changes form.
func TestDotenvLiteralPrefersQuotesWhenTheyWork(t *testing.T) {
	for _, value := range []string{"plain", `a\b`, `\leading`, "has # hash", "trailing   ", ""} {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if !strings.HasPrefix(literal, "'") {
			t.Fatalf("%q is representable quoted, so it must stay quoted; got %q", value, literal)
		}
	}
}

func TestDotenvLiteralRefusesWhatNoLoaderAgreesOn(t *testing.T) {
	// Built from escapes so the file carries no ambiguous quoting: bs is a
	// backslash, bt a backtick.
	const bs = "\x5c"
	const bt = "\x60"
	for name, value := range map[string]string{
		"dollar": "pre$VAR-post",
		"braced": "a${VAR}b",
		// A "#" is fine when quoting carries it, so it is refused only in
		// combination with a shape quoting cannot: here a trailing backslash.
		"hash and a backslash":       "before#after" + bs,
		"space hash and a backslash": "value # comment" + bs,
		// Neither form can carry these. A leading single or double quote opens
		// a quoted value, which godotenv refuses and python-dotenv misparses; a
		// wrapping backtick pair is stripped by plain Node dotenv; and a
		// newline forces quoting, which a trailing backslash in the same value
		// rules out.
		"leading single quote":         "'abc",
		"leading double quote plus bs": "\"abc" + bs,
		"backticks plus a quote":       bt + "a'b" + bt,
		"backticks plus a pair":        bt + "a" + bs + bs + "b" + bt,
		"newline plus pair":            "line1\nline2" + bs,
	} {
		if _, err := dotenvLiteral(value); err == nil {
			t.Fatalf("%s must be refused rather than written ambiguously", name)
		}
	}
}

// A refusal is a conjunction, not a list of characters: quoting must be ruled
// out by the value *and* the raw form must be ruled out as well. These values
// are the counter-examples to a flat reading of the refusal list -- none of
// them carries an apostrophe, and each is refused because the character that
// defeats quoting is not the one that defeats the raw form. A documented
// refusal list that omits the raw obstacles makes an approver expect these to
// succeed.
func TestDotenvLiteralRefusesOnlyWhenBothFormsFail(t *testing.T) {
	const bs = "\x5c"
	for name, value := range map[string]string{
		// Quoting fails on the backslash pair; raw fails on the "#".
		"pair then hash": "a" + bs + bs + "b#c",
		// Quoting fails on the trailing backslash; raw fails on the newline.
		// The order matters: this is a newline followed by a trailing
		// backslash. A trailing backslash followed by a newline is carried
		// quoted, since the backslash escapes the closing quote into the
		// newline that quoting preserves.
		"newline then trailing backslash": "line1\nline2" + bs,
		// Quoting fails on the backslash pair; raw fails on the trimmed space.
		"pair then trailing space": "a" + bs + bs + "b ",
	} {
		err := CheckGuestReadableValue(value)
		if err == nil {
			t.Fatalf("%s (%q) must be refused: neither form can carry it", name, value)
		}
		if !strings.Contains(err.Error(), "quoted:") || !strings.Contains(err.Error(), "unquoted:") {
			t.Fatalf("%s: the refusal must name both forms: %v", name, err)
		}
	}
}

// A quote or a backtick is positional, not forbidden. Quoting stays the
// default, so a value these loaders all read back verbatim when quoted is
// delivered quoted even though it holds a quote, a backtick or a "#". The raw
// fallback then carries the shapes quoting cannot: an interior or trailing
// quote or backtick. Only a leading single or double quote (which opens a
// quoted value) and a matching backtick pair wrapping the whole value (which
// plain Node dotenv strips) are refused. Measured through all three pinned
// loaders; the Node and Python suites repeat the same cases against real
// loaders.
func TestDotenvLiteralDeliversPositionalQuotesAndBackticks(t *testing.T) {
	const bs = "\x5c"
	const bt = "\x60"
	raw := map[string]string{
		"apostrophe":                   "it's",
		"apostrophe and a pair":        "it's a" + bs + bs + "b",
		"windows path with a quote":    "C:" + bs + "Users" + bs + "O'Brien" + bs,
		"double quote and a pair":      "a\"b" + bs + bs,
		"interior backtick and a pair": "a" + bt + "b" + bs + bs,
	}
	quoted := map[string]string{
		"double quote alone": "a\"b",
		// A leading double quote is carried by quoting: it only breaks the
		// raw form, where it would open a quoted value. It is refused only
		// when another character has already ruled quoting out.
		"leading double quote":    "\"abc",
		"backticks around a word": bt + "ab" + bt,
		"lone leading backtick":   bt + "abc",
		"empty backtick pair":     bt + bt,
		"hash alone":              "before#after",
		"space hash alone":        "value # comment",
	}
	for name, value := range raw {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s (%q) must be deliverable raw: %v", name, value, err)
		}
		if literal != value {
			t.Fatalf("%s must be written unquoted and verbatim: got %q", name, literal)
		}
	}
	for name, value := range quoted {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s (%q) must be deliverable by quoting: %v", name, value, err)
		}
		if literal != "'"+value+"'" {
			t.Fatalf("%s must be quoted and verbatim: got %q", name, literal)
		}
	}
}

func TestDotenvLiteralRefusalNamesBothForms(t *testing.T) {
	// Neither form can carry these: a quoted value expands the "$" (and the
	// trailing space is fine quoted), while an unquoted one trims the space.
	for name, value := range map[string]string{
		"dollar and a trailing space": "pre$VAR-post ",
		"newline and a pair":          "line1\nline2\\",
	} {
		_, err := dotenvLiteral(value)
		if err == nil {
			t.Fatalf("%s must be refused: neither form can carry it", name)
		}
		for _, want := range []string{"quoted:", "unquoted:"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal must explain both attempts (%q missing): %v", want, err)
			}
		}
	}
}

func TestGuestReadableDeliveryRefusesAnUnrepresentableValue(t *testing.T) {
	_, err := BuildGuestSecretFiles([]ProjectSecretDelivery{guestDelivery(".env", "DB_PASSWORD", "pa$$word")})
	if err == nil {
		t.Fatal("a value no encoding can carry must fail the delivery, not be written mangled")
	}
	if !strings.Contains(err.Error(), "DB_PASSWORD") || !strings.Contains(err.Error(), "$") {
		t.Fatalf("the error must name the variable and the offending character: %v", err)
	}
	// The case that is worse than mangling: a newline in a raw value makes
	// godotenv refuse the entire file, so it must never be written raw.
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{guestDelivery(".env", "TLS_KEY", "-----BEGIN KEY-----\nAAAA\n-----END KEY-----")})
	if err != nil {
		t.Fatal(err)
	}
	line := ""
	for _, l := range strings.Split(files[0].Content, "\n") {
		if strings.HasPrefix(l, "TLS_KEY=") {
			line = l
		}
	}
	if line != "TLS_KEY='-----BEGIN KEY-----" {
		t.Fatalf("a multiline value must be single-quoted, not spread over lines: %q", files[0].Content)
	}
}
