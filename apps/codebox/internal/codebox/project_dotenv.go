package codebox

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	dotenv "github.com/go-rotini/dotenv"
)

const MaxProjectDotenvBytes = 1 << 20

type ProjectDotenv struct {
	Values     map[string]string
	Duplicates []string
}

// ParseProjectDotenv parses a bounded dotenv file as data using go-rotini.
// Dollar expansion is off by default in go-rotini, so references remain
// literal; no environment is read and no command is evaluated. Duplicate
// assignments use the library's last-definition-wins rule and are reported for
// preview.
//
// Refusals either name the variable and the rule (#208) or deliberately stay
// generic: go-rotini's errors quote only the offending token, never a value,
// and we never attach source text to a message.
func ParseProjectDotenv(data []byte) (ProjectDotenv, error) {
	if len(data) > MaxProjectDotenvBytes {
		return ProjectDotenv{}, fmt.Errorf("dotenv file is %d bytes, over the %d-byte import limit", len(data), MaxProjectDotenvBytes)
	}
	// go-rotini strips a leading UTF-8 BOM before parsing and reports all
	// positions relative to the stripped source; strip it here so every offset
	// below indexes data directly.
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		data = data[3:]
	}
	// go-rotini tolerates invalid UTF-8 and NUL bytes inside values, so these
	// are checked on the whole file, and the variable whose value carries the
	// bad byte is named when attribution is unambiguous.
	if !utf8.Valid(data) {
		off := firstInvalidUTF8Byte(data)
		if name := attributeByteToKey(data, off); name != "" && safeNameToEcho(name) {
			return ProjectDotenv{}, fmt.Errorf("%s: the value is not valid UTF-8; dotenv files must be UTF-8", name)
		}
		return ProjectDotenv{}, fmt.Errorf("the dotenv file contains text no loader can decode: the value is not valid UTF-8")
	}
	if i := bytes.IndexByte(data, 0); i >= 0 {
		if name := attributeByteToKey(data, i); name != "" && safeNameToEcho(name) {
			return ProjectDotenv{}, fmt.Errorf("%s: the value contains a NUL byte, which no dotenv loader accepts", name)
		}
		return ProjectDotenv{}, fmt.Errorf("the dotenv file contains a NUL byte, which no dotenv loader accepts")
	}

	f, err := dotenv.Parse(data, dotenv.WithMaxFileSize(MaxProjectDotenvBytes), dotenv.WithMaxLineLength(MaxProjectDotenvBytes))
	if err != nil {
		if errors.Is(err, dotenv.ErrFileTooLarge) || errors.Is(err, dotenv.ErrLineTooLong) {
			return ProjectDotenv{}, fmt.Errorf("dotenv file is over the %d-byte import limit", MaxProjectDotenvBytes)
		}
		var pe *dotenv.ParseError
		if errors.As(err, &pe) {
			// A backtick refusal belongs to the statement that contains it, so it
			// is classified before the carriage-return branch. go-rotini reports
			// this error only after scanning a key and a delimiter, which proves
			// the statement after a bare carriage return is a complete
			// assignment: the carriage return separated two statements rather
			// than cutting a value short, and blaming it would send the reader
			// after a separator that did its job.
			if rerr := backtickRefusal(data, pe); rerr != nil {
				return ProjectDotenv{}, rerr
			}
			if name, bareCR := crSplitName(data, pe); bareCR {
				if name == "" {
					// The split is real but the preceding assignment's name is
					// not safe to echo; stay generic rather than guess.
					return ProjectDotenv{}, fmt.Errorf("dotenv syntax is invalid or unsupported; the offending line is not quoted here because it may contain a secret value")
				}
				return ProjectDotenv{}, fmt.Errorf("%s: the value contains a carriage return, which ends the assignment early, so the rest of the line is read as a separate statement that is not a valid assignment; remove the carriage return from the value, or make the remainder a complete assignment", name)
			}
			if rerr := nameScanRefusal(data, pe); rerr != nil {
				return ProjectDotenv{}, rerr
			}
		}
		return ProjectDotenv{}, fmt.Errorf("dotenv syntax is invalid or unsupported; the offending line is not quoted here because it may contain a secret value")
	}

	values := f.ToMap()
	for key := range values {
		if !validSecretIdentifier(key) {
			if safeNameToEcho(key) {
				return ProjectDotenv{}, fmt.Errorf("variable name %q is not supported; names may use letters, digits and underscore, may not start with a digit, and are limited to 128 characters", key)
			}
			return ProjectDotenv{}, fmt.Errorf("the dotenv file contains a variable name that is not supported; names may use letters, digits and underscore, may not start with a digit, and are limited to 128 characters")
		}
	}

	// go-rotini's AST records every assignment in order, so duplicates are
	// found without re-implementing the statement grammar (a value that spans
	// several physical lines cannot be mistaken for an assignment).
	duplicates := map[string]bool{}
	seen := map[string]bool{}
	for _, e := range f.Entries() {
		if e.Kind != dotenv.EntryAssignment {
			continue
		}
		if seen[e.Key] {
			duplicates[e.Key] = true
		}
		seen[e.Key] = true
	}
	dupList := make([]string, 0, len(duplicates))
	for key := range duplicates {
		dupList = append(dupList, key)
	}
	sort.Strings(dupList)
	return ProjectDotenv{Values: values, Duplicates: dupList}, nil
}

// firstInvalidUTF8Byte returns the offset of the first byte that is not valid
// UTF-8, or -1 when the whole slice is valid.
func firstInvalidUTF8Byte(data []byte) int {
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

// attributeByteToKey maps a byte offset in data to the key of the assignment
// statement whose source span contains it, using go-rotini's AST. A byte that
// falls in a comment or blank line, or in a file that does not parse, yields no
// key, so the caller falls back to a file-level message.
func attributeByteToKey(data []byte, off int) string {
	if off < 0 || off >= len(data) {
		return ""
	}
	// Same limits as the main parse: an attribution that reparses under
	// different caps would lose the variable name on lines the main parse
	// accepts.
	f, err := dotenv.Parse(data, dotenv.WithMaxFileSize(MaxProjectDotenvBytes), dotenv.WithMaxLineLength(MaxProjectDotenvBytes))
	if err != nil {
		return ""
	}
	entries := f.Entries()
	for i, e := range entries {
		end := len(data)
		if i+1 < len(entries) {
			end = entries[i+1].Pos.Offset
		}
		if off >= e.Pos.Offset && off < end {
			if e.Kind == dotenv.EntryAssignment {
				return e.Key
			}
			return ""
		}
	}
	return ""
}

// crSplitName names the variable whose value a bare carriage return split. The
// boolean reports whether the failing statement was preceded by a bare
// carriage return at all, whether or not a name could be tied to it.
//
// The attribution is position-driven and uses only the parser's own notion of
// statement boundaries. go-rotini fails at the first faulty statement and
// reports the byte offset of the failure; the statement that failed starts
// right after the separator that precedes that offset. When that separator is
// a bare carriage return (not a CRLF pair), the text before it parsed cleanly,
// and its last assignment is named as the variable whose value the carriage
// return cut short. The prefix is re-parsed once as a sanity guard; because
// go-rotini fails at the first fault, that parse cannot fail in practice.
//
// A CRLF pair is an ordinary line ending and never a candidate. A statement
// after a bare carriage return that fails for its own reasons (an unsupported
// name, an unterminated quote) is attributed the same way: telling the user
// which assignment the carriage return ended is still the actionable fact,
// and this tradeoff is documented on the issue (#208).
func crSplitName(data []byte, pe *dotenv.ParseError) (string, bool) {
	if pe == nil {
		return "", false
	}
	off := pe.Pos.Offset
	if off < 0 || off > len(data) {
		return "", false
	}
	fragStart := off
	for fragStart > 0 && data[fragStart-1] != '\n' && data[fragStart-1] != '\r' {
		fragStart--
	}
	if fragStart == 0 {
		return "", false
	}
	sep := fragStart - 1
	if data[sep] != '\r' {
		return "", false // separated by a newline, not a bare CR
	}
	if sep > 0 && data[sep-1] == '\n' {
		// The CR follows a newline, so it terminates an empty statement, not
		// the preceding assignment: that assignment's line ended cleanly at
		// the LF and contains no carriage return to report. (A CRLF pair is
		// already excluded above — its fragment is preceded by the LF.)
		return "", false
	}
	prefix := data[:sep]
	// Same limits as the main parse, for the same reason as in
	// attributeByteToKey.
	pf, err := dotenv.Parse(prefix, dotenv.WithMaxFileSize(MaxProjectDotenvBytes), dotenv.WithMaxLineLength(MaxProjectDotenvBytes))
	if err != nil {
		return "", true
	}
	entries := pf.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == dotenv.EntryAssignment {
			if safeNameToEcho(entries[i].Key) {
				return entries[i].Key, true
			}
			return "", true
		}
	}
	return "", true
}

// statementName extracts the attempted variable name from the statement line
// containing off: the text left of the first '=' or ':' delimiter, with an
// `export` prefix consumed the way the library consumes it. The boolean is
// false when the line carries no delimiter, i.e. it is not an assignment
// attempt at all.
func statementName(data []byte, off int) (string, bool) {
	lineStart := off
	for lineStart > 0 && data[lineStart-1] != '\n' && data[lineStart-1] != '\r' {
		lineStart--
	}
	lineEnd := lineStart
	for lineEnd < len(data) && data[lineEnd] != '\n' && data[lineEnd] != '\r' {
		lineEnd++
	}
	line := data[lineStart:lineEnd]
	delim := bytes.IndexAny(line, "=:")
	if delim < 0 {
		return "", false
	}
	return stripExportPrefix(strings.TrimSpace(string(line[:delim]))), true
}

// stripExportPrefix removes the `export` keyword the library consumes before
// scanning a name. Verified against go-rotini v1.0.3: `export K=1`,
// `export\tK=1` and `export  K=1` all yield the name K, while `exportK=1`
// yields the name exportK. So the keyword needs at least one blank after it,
// and a glued `export` is part of the name.
func stripExportPrefix(name string) string {
	after, ok := strings.CutPrefix(name, "export")
	if !ok {
		return name
	}
	trimmed := strings.TrimLeft(after, " \t")
	if len(trimmed) == len(after) {
		return name
	}
	return strings.TrimSpace(trimmed)
}

// nameScanRefusal classifies the key-scan failures go-rotini reports — the
// shapes earlier loaders silently accepted as assignments with exotic names.
//
// When the failing statement carries a delimiter, the text left of it is the
// attempted variable name; it is echoed only when it is a safe identifier
// token (see safeNameToEcho), otherwise the rule is named without the text.
// When it carries no delimiter at all, it is stray text rather than an
// assignment attempt, and the message says so. Both messages deliberately
// avoid quoting any other source text.
func nameScanRefusal(data []byte, pe *dotenv.ParseError) error {
	if pe == nil {
		return nil
	}
	if !strings.HasPrefix(pe.Message, "expected '=") && !strings.HasPrefix(pe.Message, "invalid identifier start byte") {
		return nil
	}
	off := pe.Pos.Offset
	if off < 0 || off > len(data) {
		return nil
	}
	// A key scan runs at the start of a statement, so the name is the text
	// left of the statement's delimiter.
	name, hasDelim := statementName(data, off)
	if !hasDelim || name == "" {
		return fmt.Errorf("the dotenv file contains text that is not a named assignment; every entry must be an assignment like KEY=value")
	}
	if safeNameToEcho(name) {
		return fmt.Errorf("variable name %q is not supported; names may use letters, digits and underscore, may not start with a digit, and are limited to 128 characters", name)
	}
	return fmt.Errorf("the dotenv file contains a variable name that is not supported; names may use letters, digits and underscore, may not start with a digit, and are limited to 128 characters")
}

// backtickRefusal classifies go-rotini's backtick-value refusal: the library
// rejects a value that opens with a backtick unless backticks are enabled,
// and they stay disabled here deliberately — the previous loader imported the
// backticks literally inside the value, which is a silent value change. The
// message names the variable and the rule, and never the value.
func backtickRefusal(data []byte, pe *dotenv.ParseError) error {
	if pe == nil {
		return nil
	}
	if !strings.HasPrefix(pe.Message, "backtick-quoted value not allowed") {
		return nil
	}
	off := pe.Pos.Offset
	if off < 0 || off > len(data) {
		return nil
	}
	// The failure is positioned on the backtick that opens the value, after a
	// key scan that succeeded, so the statement always carries a delimiter.
	name, _ := statementName(data, off)
	if safeNameToEcho(name) {
		return fmt.Errorf("%s: the value starts with a backtick, which is not a supported quoting form; use double or single quotes, or remove the backticks", name)
	}
	return fmt.Errorf("the dotenv file contains a value that starts with a backtick, which is not a supported quoting form; use double or single quotes, or remove the backticks")
}

// safeNameToEcho reports whether a rejected secret name is safe to include in a
// refusal message. A name is echoed only when it is a short, non-empty,
// whitespace-free run of printable ASCII — a plausible identifier token.
// go-rotini accepts names containing spaces, so a name that is long, empty, or
// contains whitespace or control bytes may be stray or pasted text sitting left
// of a delimiter, and echoing it could leak material that was never a real
// variable name.
func safeNameToEcho(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}
