package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

// The encoding contract, checked against a real loader rather than against the
// encoder's own idea of what it wrote. Node and Python are covered by
// tests/integration/project-secrets/ (same encoded forms); this closes the
// loop for the Go loader, which is the strictest of the three: it refuses the
// whole file on a raw newline.

func TestEncodedValuesRoundTripThroughGodotenv(t *testing.T) {
	values := map[string]string{
		"PLAIN":     "plainvalue",
		"MULTILINE": "-----BEGIN KEY-----\nAAAA\n-----END KEY-----",
		"TRAILING":  "trailing   ",
		"LEADING":   "   leading",
		"HASH":      "hash # inside",
		"DQUOTE":    `has " quote`,
		"BACKSLASH": `back\slash`,
		"EMPTY":     "",
	}
	var b strings.Builder
	for name, value := range values {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b.WriteString(name + "=" + literal + "\n")
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := godotenv.Read(path)
	if err != nil {
		t.Fatalf("the generated file must load: %v", err)
	}
	for name, want := range values {
		if got[name] != want {
			t.Fatalf("%s: godotenv read %q, want %q", name, got[name], want)
		}
	}
}

func TestARawValueWouldBreakTheWholeFile(t *testing.T) {
	// This is the defect the encoding exists to prevent: the failure is not
	// confined to the variable, godotenv refuses the entire file, so every
	// other secret in it is lost too.
	path := filepath.Join(t.TempDir(), ".env")
	raw := "TLS_KEY=-----BEGIN KEY-----\nAAAA\n-----END KEY-----\nAPI_KEY=still-here\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := godotenv.Read(path); err == nil {
		t.Fatal("a raw multiline value must break the file, which is why it is encoded")
	}
}

func TestATrailingBackslashIsWrittenRawBecauseQuotingBreaksTheFile(t *testing.T) {
	// Even single-quoted, a trailing backslash escapes the closing quote: the
	// Go loader refuses the whole file. That is why the encoder writes this
	// shape unquoted, which godotenv reads exactly.
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("K_TARGET='trailing\\'\nK_AFTER='after'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := godotenv.Read(path); err == nil {
		t.Fatal("godotenv must refuse a file whose quoted value ends with a backslash, which is why the encoder does not emit that form")
	}
	literal, err := dotenvLiteral(`trailing\`)
	if err != nil {
		t.Fatalf("a trailing backslash must be delivered raw, not refused: %v", err)
	}
	if literal != `trailing\` {
		t.Fatalf("a trailing backslash must be written unquoted and verbatim: %q", literal)
	}
	if err := os.WriteFile(path, []byte("K_TARGET="+literal+"\nK_AFTER=after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := godotenv.Read(path)
	if err != nil {
		t.Fatalf("the raw form must load: %v", err)
	}
	if got["K_TARGET"] != `trailing\` || got["K_AFTER"] != "after" {
		t.Fatalf("raw trailing backslash: got %q, next line %q", got["K_TARGET"], got["K_AFTER"])
	}
}

// The values the raw predicate accepts are checked against the strictest
// loader, not just against the predicate. A backslash pair, a trailing
// backslash or a value quoting cannot carry forces the raw form; anything else
// stays quoted, so the table states which form each value must take and every
// value is read back. Non-ASCII and the positional quote and backtick cases
// share the table because both are about what an unquoted value carries.
func TestRawAndQuotedValuesRoundTripThroughGodotenv(t *testing.T) {
	const bs = "\x5c"
	const bt = "\x60"
	values := map[string]struct {
		value string
		raw   bool
	}{
		"ACCENTED_PATH":   {`C:\Répertoire\`, true},
		"TRAILING_ACCENT": {`naïve\`, true},
		"GREEK":           {`Ω\`, true},
		"CJK":             {`秘密\`, true},
		"EMOJI":           {`🚀\`, true},
		"ARABIC":          {`مجلد\`, true},
		"MIXED":           {`C:\Données\rép-🚀\`, true},
		"INTERIOR_TAB":    {"a\tb", false},
		"INTERIOR_NBSP":   {"a\u00a0b", false},
		// A quote or a backtick is an ordinary byte unless it is first (a
		// quote) or wraps the whole value (a backtick pair). These force the
		// raw form through a backslash shape quoting cannot carry.
		"APOSTROPHE":          {"it's", true},
		"APOSTROPHE_PAIR":     {"it's a" + bs + bs + "b", true},
		"WINDOWS_QUOTE_PATH":  {"C:" + bs + "Users" + bs + "O'Brien" + bs, true},
		"DQUOTE_PAIR":         {"a\"b" + bs + bs, true},
		"BACKTICK_PAIR":       {"a" + bt + "b" + bs + bs, true},
		"DOUBLE_QUOTE_ALONE":  {"a\"b", false},
		"INTERIOR_BACKTICK":   {"a" + bt + "b", false},
		"BACKTICK_WORD":       {bt + "ab" + bt, false},
		"LEADING_BACKTICK":    {bt + "abc", false},
		"EMPTY_BACKTICK_PAIR": {bt + bt, false},
		"HASH":                {"before#after", false},
	}
	var b strings.Builder
	b.WriteString("K_BEFORE='before'\n")
	for name, entry := range values {
		literal, err := dotenvLiteral(entry.value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := "'" + entry.value + "'"
		if entry.raw {
			want = entry.value
		}
		if literal != want {
			t.Fatalf("%s: got %q, want %q", name, literal, want)
		}
		b.WriteString(name + "=" + literal + "\n")
	}
	b.WriteString("K_AFTER='after'\n")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := godotenv.Read(path)
	if err != nil {
		t.Fatalf("the generated file must load: %v", err)
	}
	if got["K_BEFORE"] != "before" || got["K_AFTER"] != "after" {
		t.Fatalf("the quoted neighbours must survive too: %q %q", got["K_BEFORE"], got["K_AFTER"])
	}
	for name, entry := range values {
		if got[name] != entry.value {
			t.Fatalf("%s: godotenv read %q, want %q", name, got[name], entry.value)
		}
	}
}

// The shapes the encoder refuses, each asserted against the encoder and the
// leading-quote case measured against godotenv as well, so the refusal rests on
// a loader's behaviour rather than on the predicate alone. Only the leading
// quote is refused by godotenv; a wrapping backtick pair is kept by godotenv
// and python-dotenv and stripped by plain Node dotenv, which is why it is
// refused: no single form is exact in every loader.
func TestRefusedShapesAndTheLeadingQuoteMeasurement(t *testing.T) {
	const bs = "\x5c"
	const bt = "\x60"
	// A plain wrapping backtick pair is not refused on its own: quoting carries
	// it (backticks are ordinary inside single quotes), so the raw refusal never
	// surfaces for a value quoting can take. It is refused only in a shape
	// quoting cannot carry, which is what the two ticks entries below are.
	refused := map[string]string{
		"LEADING_SQUOTE": "'abc",
		"LEADING_DQUOTE": "\"abc" + bs,
		// A single quote cannot be escaped in a quoted value and a backslash
		// pair collapses in one, so quoting is out; raw is out as well because
		// Node strips the wrapping backtick pair.
		"TICKS_AROUND_APOSTROPHE": bt + "a'b" + bt,
		"TICKS_AROUND_PAIR":       bt + "a" + bs + bs + "b" + bt,
		"DOLLAR":                  "pre$VAR-post",
	}
	for name, value := range refused {
		if _, err := dotenvLiteral(value); err == nil {
			t.Fatalf("%s (%q) must be refused: no form is exact in every loader", name, value)
		}
	}
	// And the measurement behind the leading-quote refusal: written raw, it
	// makes godotenv reject the whole file, not just this variable.
	path := filepath.Join(t.TempDir(), ".env")
	raw := "K_TARGET='abc\nK_AFTER='after'\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := godotenv.Read(path); err == nil {
		t.Fatal("godotenv must refuse a raw value that opens a quote, which is why a leading quote is refused")
	}
}

func TestEncodedRawValuesRoundTripThroughGodotenv(t *testing.T) {
	// The raw fallback and one quoted neighbour, checked against the strictest
	// loader, in the shape a generated file actually has: the value between
	// quoted values. The literal is taken from dotenvLiteral, so EQUALS is
	// carried quoted, exactly as the encoder decides.
	values := map[string]string{
		"PAIR":         `a\\b`,
		"WINDOWS_PATH": `C:\Users\`,
		"UNC":          `\\server\share`,
		"TRAILING":     `trailing\`,
		"EQUALS":       `a\=b`,
	}
	var b strings.Builder
	b.WriteString("K_BEFORE='before'\n")
	for name, value := range values {
		literal, err := dotenvLiteral(value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b.WriteString(name + "=" + literal + "\n")
	}
	b.WriteString("K_AFTER='after'\n")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := godotenv.Read(path)
	if err != nil {
		t.Fatalf("the generated file must load: %v", err)
	}
	if got["K_BEFORE"] != "before" || got["K_AFTER"] != "after" {
		t.Fatalf("the quoted neighbours must survive too: %q %q", got["K_BEFORE"], got["K_AFTER"])
	}
	for name, want := range values {
		if got[name] != want {
			t.Fatalf("%s: godotenv read %q, want %q", name, got[name], want)
		}
	}
}
