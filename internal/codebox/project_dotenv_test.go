package codebox

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseProjectDotenvPreservesLiteralValues(t *testing.T) {
	source := "# note\r\nexport EMPTY=\r\nSIMPLE= value with spaces \r\n" +
		"LITERAL='$ONE ${TWO} $(touch /tmp/not-run)'\r\n" +
		"MULTILINE=\"first\nsecond $THREE\"\r\n"
	parsed, err := ParseProjectDotenv([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"EMPTY":     "",
		"SIMPLE":    "value with spaces",
		"LITERAL":   "$ONE ${TWO} $(touch /tmp/not-run)",
		"MULTILINE": "first\nsecond $THREE",
	}
	if !reflect.DeepEqual(parsed.Values, want) {
		t.Fatalf("parsed values differ: keys=%v; values deliberately not printed", keysOf(parsed.Values))
	}
	if len(parsed.Duplicates) != 0 {
		t.Fatalf("unexpected duplicate keys: %v", parsed.Duplicates)
	}
}

func TestParseProjectDotenvReportsDuplicateKeysLastWins(t *testing.T) {
	parsed, err := ParseProjectDotenv([]byte("A=first\nB=other\nA=last\n"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Values["A"] != "last" || !reflect.DeepEqual(parsed.Duplicates, []string{"A"}) {
		t.Fatalf("duplicate handling: values=%v duplicates=%v", keysOf(parsed.Values), parsed.Duplicates)
	}
}

func TestParseProjectDotenvReportsColonDuplicateKeysLastWins(t *testing.T) {
	parsed, err := ParseProjectDotenv([]byte("A: first\nB: other\nA: last\n"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Values["A"] != "last" || !reflect.DeepEqual(parsed.Duplicates, []string{"A"}) {
		t.Fatalf("duplicate handling with ':' delimiter: values=%v duplicates=%v", keysOf(parsed.Values), parsed.Duplicates)
	}
}

func TestParseProjectDotenvReportsMixedSeparatorDuplicates(t *testing.T) {
	parsed, err := ParseProjectDotenv([]byte("A: first\nA=last\n"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Values["A"] != "last" || !reflect.DeepEqual(parsed.Duplicates, []string{"A"}) {
		t.Fatalf("duplicate handling across ':' and '=' delimiters: values=%v duplicates=%v", keysOf(parsed.Values), parsed.Duplicates)
	}
}

func TestParseProjectDotenvNoFalseColonDuplicate(t *testing.T) {
	parsed, err := ParseProjectDotenv([]byte("A: first\nB: second\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Duplicates) != 0 {
		t.Fatalf("unexpected duplicate keys for distinct colon assignments: %v", parsed.Duplicates)
	}
}

func TestParseProjectDotenvRejectsUnsafeAndMalformedInput(t *testing.T) {
	for _, source := range []string{"BAD-KEY=value\n", "BROKEN=\"unterminated-secret", "KEY=value\x00"} {
		_, err := ParseProjectDotenv([]byte(source))
		if err == nil || strings.Contains(err.Error(), "unterminated-secret") {
			t.Fatalf("malformed dotenv should fail without echoing contents: %v", err)
		}
	}
	if _, err := ParseProjectDotenv([]byte(strings.Repeat("X", MaxProjectDotenvBytes+1))); err == nil {
		t.Fatal("oversized dotenv accepted")
	}
}

func keysOf(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
