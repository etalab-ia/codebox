package codebox

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	dotenv "github.com/go-rotini/dotenv"
)

// The import path used to refuse a whole file with one message shared by four
// unrelated faults ("dotenv file is too large or contains invalid text"), which
// named neither the variable nor the rule: a UTF-8 problem read as a size
// problem, and a carriage return as an unsupported syntax. The qualification
// protocol requires each refused class to name the variable and the reason
// (#208). These tests pin the diagnosis and, just as importantly, the safety
// property that made the original message vague: a parser's own error can
// quote the surrounding source, which may be the secret, so it must never be
// returned.
//
// This file is the go-rotini port of the godotenv-based diagnostics from PR
// #227. The hand-written grammar walker and its marker oracle are gone: the
// library reports the byte offset of the first fault, and every attribution
// here is derived from that position and the library's own statement
// boundaries. The tests that exercised the removed machinery (scan and
// re-parse budgets, marker generation) are replaced by their behavioral
// halves: the fault is still named on the same fixtures, and the work is one
// prefix parse by construction rather than by a budget.

func TestImportRefusalNamesTheVariableAndTheRule(t *testing.T) {
	for _, c := range []struct {
		name    string
		data    string
		wantVar string
		wantIn  string
	}{
		{
			name:    "bare carriage return inside a value",
			data:    "CR_VALUE=bad\rreturn\n",
			wantVar: "CR_VALUE",
			wantIn:  "carriage return",
		},
		{
			name:    "invalid UTF-8",
			data:    "INVALID_UTF8=bad\xff\xfe\n",
			wantVar: "INVALID_UTF8",
			wantIn:  "UTF-8",
		},
		{
			name:    "NUL byte",
			data:    "NUL_VALUE=bad\x00x\n",
			wantVar: "NUL_VALUE",
			wantIn:  "NUL",
		},
		{
			// A short, whitespace-free token is named (the qualification
			// protocol requires it); names that carry spaces or other stray
			// text are redacted, covered below.
			name:    "unsupported variable name",
			data:    "1invalid=1\n",
			wantVar: "1invalid",
			wantIn:  "not supported",
		},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: expected a refusal", c.name)
		}
		if !strings.Contains(err.Error(), c.wantVar) {
			t.Errorf("%s: the refusal must name the variable %q: %v", c.name, c.wantVar, err)
		}
		if !strings.Contains(err.Error(), c.wantIn) {
			t.Errorf("%s: the refusal must name the rule (%q): %v", c.name, c.wantIn, err)
		}
		// The generic wording was the defect: it must be gone.
		if strings.Contains(err.Error(), "too large or contains invalid text") {
			t.Errorf("%s: the merged message must be split: %v", c.name, err)
		}
		if strings.Contains(err.Error(), "syntax is invalid or unsupported") {
			t.Errorf("%s: a diagnosed fault must not fall back to the generic wording: %v", c.name, err)
		}
	}
}

// TestImportRefusalRedactsUnsafeVariableName pins the redaction half of the
// unsupported-name diagnosis. A rejected name is not guaranteed to be a safe
// identifier: it can be stray or pasted text to the left of a delimiter (e.g.
// a value typed before an '='), or long enough to bloat the refusal. Such
// names must not be echoed — the rule is stated, but the raw name never
// reaches the message.
func TestImportRefusalRedactsUnsafeVariableName(t *testing.T) {
	longName := strings.Repeat("a", 200)
	for _, c := range []struct {
		name, data, suspect string
	}{
		{"name with spaces", "has space=1\n", "has space"},
		{"overlong name", longName + "=1\n", longName},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: an unsupported variable name must be refused", c.name)
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: the refusal must state the rule: %v", c.name, err)
		}
		if strings.Contains(err.Error(), c.suspect) {
			t.Errorf("%s: the rejected name %q must be redacted: %v", c.name, c.suspect, err)
		}
		if len(err.Error()) > 512 {
			t.Errorf("%s: the refusal must stay small: %d bytes", c.name, len(err.Error()))
		}
	}
}

// TestImportRefusalNeverEchoesTheValue is the reason the original message was
// vague, and it must survive: a parser error can embed the source around the
// fault, and that source is often the secret itself. go-rotini's errors quote
// only the offending token, and the refusal messages here quote only variable
// names — never value text.
func TestImportRefusalNeverEchoesTheValue(t *testing.T) {
	const secret = "SUPERSECRET_TOKEN_9f3a"
	for _, c := range []struct{ name, data string }{
		{"carriage return", "K=" + secret + "\rtailfragment\n"},
		{"invalid UTF-8", "K=" + secret + "\xff\xfe\n"},
		{"NUL byte", "K=" + secret + "\x00x\n"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: expected a refusal", c.name)
		}
		// "tailfragment" is the value text that follows the fault; it must not
		// reach the message. Each fragment is chosen not to occur in the fixed
		// wording, so it cannot match the message itself.
		for _, fragment := range []string{secret, "SUPERSECRET", "9f3a", "tailfragment"} {
			if strings.Contains(err.Error(), fragment) {
				t.Errorf("%s: the refusal leaked %q: %v", c.name, fragment, err)
			}
		}
	}
}

// TestImportAcceptsTheCarriageReturnFormsThatAreLegal guards the fix against
// over-reach. A carriage return is only fatal where it splits a value: CRLF is
// an ordinary line ending, and a bare CR followed by a well-formed assignment
// parses as two assignments. Refusing those would reject valid files. A
// leading byte order mark is accepted too: go-rotini strips it, and so does
// the importer before handing the data over.
func TestImportAcceptsTheCarriageReturnFormsThatAreLegal(t *testing.T) {
	for _, c := range []struct {
		name, data string
		want       map[string]string
	}{
		{"CRLF line endings", "A=ok\r\nB=two\r\n", map[string]string{"A": "ok", "B": "two"}},
		{"bare CR between assignments", "A=1\rB=2\n", map[string]string{"A": "1", "B": "2"}},
		{"lone CRLF", "\r\n", map[string]string{}},
		{"byte order mark", "\xef\xbb\xbfA=ok\r\nB=two\r\n", map[string]string{"A": "ok", "B": "two"}},
		{"export prefix", "export A=1\n", map[string]string{"A": "1"}},
	} {
		got, err := ParseProjectDotenv([]byte(c.data))
		if err != nil {
			t.Errorf("%s: must parse, not refuse: %v", c.name, err)
			continue
		}
		for wantKey, wantVal := range c.want {
			if got.Values[wantKey] != wantVal {
				t.Errorf("%s: expected %s=%q, got %q", c.name, wantKey, wantVal, got.Values[wantKey])
			}
		}
		if len(got.Values) != len(c.want) {
			t.Errorf("%s: expected %d keys, got %d: %v", c.name, len(c.want), len(got.Values), got.Values)
		}
	}
}

// TestImportRefusalNamesTheCarriageReturnSplitAtEOF is the P2 counterpart to
// the newline-carrying case: a bare CR that splits a value whose trailing
// fragment sits at the very end of the file (no following newline). The
// previous loader silently parsed that fragment as a stray empty-named
// assignment, so the import succeeded with a mangled value; go-rotini refuses
// it at the fragment, which is what makes the EOF shape diagnosable at all.
func TestImportRefusalNamesTheCarriageReturnSplitAtEOF(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"split at EOF no newline", "K=bad\rreturn"},
		{"split at EOF", "K=bad\rtail"},
		{"split then more CRs", "A=1\rK=bad\rtail\r"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: a carriage return splitting a value must be refused", c.name)
		}
		if !strings.Contains(err.Error(), "K:") {
			t.Errorf("%s: the refusal must name K: %v", c.name, err)
		}
		if !strings.Contains(err.Error(), "carriage return") {
			t.Errorf("%s: the refusal must state the carriage-return rule: %v", c.name, err)
		}
		if strings.Contains(err.Error(), "variable name") && strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: the empty-variable-name message must not replace the CR diagnosis: %v", c.name, err)
		}
	}
}

// TestImportRefusalNamesStrayTextWithoutAnAssignment covers the other
// empty-named shape: text with no '=' or ':' delimiter, preceded by a newline
// rather than a bare CR, is genuine stray content and must be refused with a
// message that says so instead of the generic wording.
func TestImportRefusalNamesStrayTextWithoutAnAssignment(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"stray text after a line", "A=1\nfoo"},
		{"bare value line", "=value\n"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: stray non-assignment text must be refused", c.name)
		}
		if !strings.Contains(err.Error(), "not a named assignment") {
			t.Errorf("%s: the refusal must name the stray-text class: %v", c.name, err)
		}
		if strings.Contains(err.Error(), "variable name") && strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: the empty-variable-name message must not be used: %v", c.name, err)
		}
	}
}

// TestImportRefusalNamesTheEnclosingAssignmentNotTheContinuation covers the
// non-disclosure hole an independent review found in the first version of this
// fix. That version located a fault's line by splitting on newlines, but a
// quoted value can span physical lines — so in `K="first\nSECRET_TOKEN=...`
// it read the second line as an assignment and printed what was actually value
// text. Attribution now walks the parser's own entry spans, where the fault
// belongs to K and no continuation line is ever mistaken for a key.
func TestImportRefusalNamesTheEnclosingAssignmentNotTheContinuation(t *testing.T) {
	const secret = "SUPERSECRET_TOKEN_9f3a"
	_, err := ParseProjectDotenv([]byte("K=\"first\n" + secret + "=\xff\"\n"))
	if err == nil {
		t.Fatal("an invalid byte inside a multiline value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("the refusal must name the enclosing assignment K: %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "9f3a") {
		t.Errorf("the refusal leaked value text from a continuation line: %v", err)
	}
}

// TestImportRefusalBlamesOnlyTheFaultyCarriageReturn is the ordering guard: a
// bare carriage return is a legal separator, so when the parser fails on
// something else the carriage return must not be accused. The failure below is
// on the BROKEN line, which a newline — not a bare CR — precedes, so no
// attribution fires and the message stays generic.
func TestImportRefusalBlamesOnlyTheFaultyCarriageReturn(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A=1\rB=2\nBROKEN=\"\n"))
	if err == nil {
		t.Fatal("an unterminated quoted value must be refused")
	}
	if strings.Contains(err.Error(), "A:") || strings.Contains(err.Error(), "B:") {
		t.Errorf("a legal bare-CR separator must not be blamed for an unrelated failure: %v", err)
	}
	if strings.Contains(err.Error(), "carriage return") {
		t.Errorf("the failure is an unterminated quote, not a carriage return: %v", err)
	}
}

// TestImportRefusalDoesNotBlameALegalSeparatorForBackticks: the carriage-return
// branch and the backtick branch can both claim the same failure, and only one
// of them is right. A bare CR in front of a statement that fails its key scan
// really did cut the preceding value short. But go-rotini reports the backtick
// refusal only after a key and a delimiter were scanned, so the statement after
// the CR is a complete assignment: `A=ok\rK=1` parses as two assignments, and
// the CR is not the fault. Classifying the backtick first is what keeps that
// distinction; the reverse order accuses a separator that did its job.
func TestImportRefusalDoesNotBlameALegalSeparatorForBackticks(t *testing.T) {
	const secret = "SUPERSECRET_9f3a"
	for _, in := range []string{
		"A=ok\rK=`" + secret + "`\n",
		"A=\"ok\"\rK=`" + secret + "`\n",
		"B=1\rA=ok\rK=`" + secret + "`\n",
	} {
		_, err := ParseProjectDotenv([]byte(in))
		if err == nil {
			t.Fatalf("%q: a backtick value must be refused", in)
		}
		msg := err.Error()
		if strings.Contains(msg, "carriage return") {
			t.Errorf("%q: a legal separator must not be blamed for a backtick refusal: %v", in, err)
		}
		if !strings.Contains(msg, "backtick") {
			t.Errorf("%q: the real fault is the backtick value: %v", in, err)
		}
		if !strings.Contains(msg, "K:") {
			t.Errorf("%q: the refusal must name K, the statement that carries the backtick: %v", in, err)
		}
		if strings.Contains(msg, secret) {
			t.Errorf("%q: the refusal must not quote the value: %v", in, err)
		}
	}
}

// TestImportRefusalRecognizesYAMLDelimiter pins the second grammar gap: the
// parser accepts ':' as an assignment delimiter, so a diagnostic that only
// looks for '=' fails to name the variable on those lines.
func TestImportRefusalRecognizesYAMLDelimiter(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("API_KEY: bad\xff\n"))
	if err == nil {
		t.Fatal("an invalid byte must be refused")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Errorf("a colon-delimited assignment must be named like an equals one: %v", err)
	}
}

// TestCarriageReturnRemedyIsAccurate pins the advice itself. Converting the CR
// to LF does NOT repair `K=bad\rtail`: the trailing fragment is still a line
// with no assignment. Telling the user to do it would send them in a circle.
func TestCarriageReturnRemedyIsAccurate(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("K=bad\rtail\n"))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	msg := err.Error()
	if strings.Contains(msg, "convert the file to LF") {
		t.Errorf("LF conversion does not repair this shape, so it must not be advised: %v", err)
	}
	if !strings.Contains(msg, "remove the carriage return") {
		t.Errorf("the advice must be the remedy that works: %v", err)
	}
}

// The three cases below were review findings on the godotenv-based walker,
// each a fresh divergence between a hand-written grammar walk and the parser
// it imitated: comment blocks, carriage returns inside quoted values, and
// escape parity. They are kept as regression fixtures: with the library
// providing the grammar, they now pin that the delegation itself holds.

// TestImportRefusalDoesNotBlameACommentLine: a byte inside a comment belongs
// to no variable. Attribution walks entry spans, and a comment entry yields
// no key, so the refusal stays file-level.
func TestImportRefusalDoesNotBlameACommentLine(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"NUL in a leading comment", "# bad\x00\nNEXT=ok\n"},
		{"NUL in a middle comment", "A=1\n# bad\x00\nB=2\n"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: expected a refusal", c.name)
		}
		for _, innocent := range []string{"NEXT:", "A:", "B:"} {
			if strings.Contains(err.Error(), innocent) {
				t.Errorf("%s: a comment byte must not be attributed to %s: %v", c.name, innocent, err)
			}
		}
	}
}

// TestImportRefusalIgnoresCarriageReturnInsideQuotes: the parser keeps a CR
// inside a quoted value as content rather than treating it as a line ending,
// so a CR there cannot explain a failure elsewhere in the file. The failure
// below is on the BROKEN line, which a newline precedes.
func TestImportRefusalIgnoresCarriageReturnInsideQuotes(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A=\"ok\rstill\"\nBROKEN=\"\n"))
	if err == nil {
		t.Fatal("an unterminated quoted value must be refused")
	}
	if strings.Contains(err.Error(), "A:") {
		t.Errorf("a carriage return inside a quoted value must not be blamed: %v", err)
	}
	if strings.Contains(err.Error(), "carriage return") {
		t.Errorf("the real fault is the unterminated quote: %v", err)
	}
}

// TestImportRefusalHandlesEscapedQuotes: the parser closes a quoted value on
// an escaped quote and keeps it open on an odd backslash run. A byte after
// such a value must be attributed with the same parity, or the wrong variable
// is named.
func TestImportRefusalHandlesEscapedQuotes(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A=\"x\\\\\"\nB=bad\x00\n"))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if strings.Contains(err.Error(), "A:") {
		t.Errorf("the byte after an escaped quote belongs to B, not A: %v", err)
	}
}

// TestImportRefusalAttributesDollarAdjacentByte: go-rotini performs no
// expansion by default, so a '$' in a value is literal and a fault just after
// it is attributed like any other byte. (The godotenv-based oracle had to
// mask dollars because that loader expanded them; here the property is the
// library's own no-expansion default, pinned in the contract test below.)
func TestImportRefusalAttributesDollarAdjacentByte(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("K=secret$\x00\n"))
	if err == nil {
		t.Fatal("a NUL byte must be refused")
	}
	if !strings.Contains(err.Error(), "K") {
		t.Errorf("a byte after a literal dollar must still be attributed to K: %v", err)
	}
}

// TestImportRefusalAttributesOverwrittenDuplicate: the parser resolves
// duplicate assignments last-definition-wins, so a bad byte in an overwritten
// value is absent from the final map. Attribution walks entry spans, which
// record every assignment in order, so the first A is still named.
func TestImportRefusalAttributesOverwrittenDuplicate(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A=bad\x00\nA=good\n"))
	if err == nil {
		t.Fatal("a NUL byte must be refused")
	}
	if !strings.Contains(err.Error(), "A") {
		t.Errorf("an overwritten duplicate still names its key: %v", err)
	}
}

// TestImportRefusalStaysFastOnManyCandidateBytes guards the work spent on
// attribution. The attribution is one prefix parse regardless of how many
// bare carriage returns the file carries, so a file full of them must answer
// as fast as a small one. The timeout is a hang backstop only, not the
// contract: CI runs `go test -race`, which slows execution enough that a
// wall-clock budget would measure the runner.
func TestImportRefusalStaysFastOnManyCandidateBytes(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("K=1\r")
	}
	b.WriteString("BROKEN=\"\n")
	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unterminated quoted value must be refused")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attribution must stay bounded on a file full of bare carriage returns")
	}
}

// TestImportRefusalReachesTheFaultyCarriageReturnBeyondLegalOnes: a legitimate
// file can carry many legal bare-CR separators before the one that actually
// splits a value. The attribution does not iterate over candidates at all — it
// reads the parser's reported position — so the fault is reached whatever its
// position.
func TestImportRefusalReachesTheFaultyCarriageReturnBeyondLegalOnes(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "A%d=1\r", i)
	}
	b.WriteString("K=bad\rtail\n")
	_, err := ParseProjectDotenv([]byte(b.String()))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("the faulty carriage return must be named even after legal ones: %v", err)
	}
	if !strings.Contains(err.Error(), "carriage return") {
		t.Errorf("the refusal must state the carriage-return rule: %v", err)
	}
}

// TestImportRefusalNamesTheVariableOnANearCapLine: the attribution reparses
// run under the same limits as the main parse, so a line just under the 1 MiB
// import cap still gets its variable named — both on the byte-attribution
// path (a NUL in a long value) and on the carriage-return split path (the
// prefix reparse must accept the same long line the main parse accepted).
// The library's default line cap currently equals the importer's cap, so the
// un-fixed code also passes today; the contract pin on the default is what
// notices a version bump that breaks that equality.
func TestImportRefusalNamesTheVariableOnANearCapLine(t *testing.T) {
	long := strings.Repeat("v", MaxProjectDotenvBytes-600)

	in := "K=" + long + "\x00tail\n"
	_, err := ParseProjectDotenv([]byte(in))
	if err == nil {
		t.Fatal("a NUL in a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("the refusal must name K even on a near-cap line: %v", err)
	}

	in = "A=" + long + "\rK=bad\rtail\n"
	_, err = ParseProjectDotenv([]byte(in))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("the split must be attributed to K, not lost to the long line: %v", err)
	}
}

// TestImportSizeLimitStillReportsSize pins the one fault that really is about
// size: it must say so rather than fall into the encoding wording.
func TestImportSizeLimitStillReportsSize(t *testing.T) {
	big := make([]byte, MaxProjectDotenvBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	_, err := ParseProjectDotenv(big)
	if err == nil {
		t.Fatal("a file over the limit must be refused")
	}
	if !strings.Contains(err.Error(), "limit") || !strings.Contains(err.Error(), "byte") {
		t.Fatalf("the refusal must name the size and the limit: %v", err)
	}
	if strings.Contains(err.Error(), "UTF-8") || strings.Contains(err.Error(), "carriage return") {
		t.Fatalf("a size fault must not be reported as an encoding fault: %v", err)
	}
}

// TestImportRefusalSkipsCommentAndBlankCarriageReturnSeparators: comments and
// blank lines are legal statements too, so a file that uses them as bare-CR
// separators must still have its fault named. The godotenv-based classifier
// re-parsed the whole input once per separator; here the prefix parse sees
// them once, as statements.
func TestImportRefusalSkipsCommentAndBlankCarriageReturnSeparators(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 8000; i++ {
		b.WriteString("A=1\r# comment\n")
	}
	b.WriteString("K=bad\rtail\n")
	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a carriage return splitting a value must be refused")
		}
		if !strings.Contains(err.Error(), "K:") {
			t.Errorf("the fault must be named past comment separators: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("comment-only separators must not slow the diagnosis")
	}
}

// TestImportRefusalKeepsAMalformedSeparatorAsCandidate: `tail="` is not a
// well-formed statement — its quote is unterminated — so a carriage return in
// front of it may be the cause and must stay attributed. The parser fails
// inside the fragment, and the fragment follows a bare CR: that is the whole
// test of the attribution rule.
func TestImportRefusalKeepsAMalformedSeparatorAsCandidate(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("K=bad\rtail=\"\n"))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("a malformed-looking separator must not hide the fault: %v", err)
	}
}

// TestImportRefusalNamesQuotedDuplicateOverwrittenByLaterAssignment: when the
// faulty value is quoted and a later duplicate overwrites it, the entry span
// still covers the faulty original, so the key is named.
func TestImportRefusalNamesQuotedDuplicateOverwrittenByLaterAssignment(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A=\"bad\x00\"\nA=good\n"))
	if err == nil {
		t.Fatal("a NUL byte in a value must be refused")
	}
	if !strings.Contains(err.Error(), "A:") {
		t.Errorf("a quoted duplicate overwritten later must still be named: %v", err)
	}
	if !strings.Contains(err.Error(), "NUL") {
		t.Errorf("the refusal must state the NUL rule: %v", err)
	}
}

// TestImportRefusalNamesTheFaultPastOverlappingLegalSeparators: CR-only blank
// separators in front of one long statement must not cost the diagnosis. The
// godotenv-based walk charged each such separator a scan to a distant line
// end; the position-driven attribution never looks at them.
func TestImportRefusalNamesTheFaultPastOverlappingLegalSeparators(t *testing.T) {
	var b strings.Builder
	b.WriteString("\r\r\r\r\r")
	b.WriteString("A=" + strings.Repeat("x", 900_000) + "\n")
	b.WriteString("K=bad\rtail\n")
	if b.Len() > MaxProjectDotenvBytes {
		t.Fatalf("fixture is %d bytes, over the import limit", b.Len())
	}
	_, err := ParseProjectDotenv([]byte(b.String()))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("overlapping legal separators must not cost the diagnosis: %v", err)
	}
}

// TestImportFaultScanStaysLinearAcrossQuotedValues: a quoted value holding
// many carriage returns is the shape that used to cost one whole-file pass
// per CR. The carriage returns are content, the prefix parses once, and the
// fault is named.
func TestImportFaultScanStaysLinearAcrossQuotedValues(t *testing.T) {
	const quotes = 4000
	var b strings.Builder
	b.WriteString("A=\"")
	b.WriteString(strings.Repeat("a\r", quotes))
	b.WriteString("\"\rK=bad\rtail\n")

	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a carriage return splitting a value must be refused")
		}
		if !strings.HasPrefix(err.Error(), "K:") {
			t.Errorf("the fault is in K: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("quoted carriage returns must not slow the diagnosis")
	}
}

// TestImportRefusalNamesTheFaultAfterAQuotedValueOnTheSameLine: the
// godotenv-based re-cut used to swallow every CR-separated statement between
// a quote and the next line end, so `P=ok\rA="a\rb"\rK=bad\rtail` reported P
// — a legal separator — while the fault was two statements later in K. The
// attribution reads the parser's position instead of re-cutting, and the
// prefix parse ends at the reported fault, so K is named. The single-quoted
// and escaped-quote forms pin that the prefix parse relies on the library's
// own quote handling.
func TestImportRefusalNamesTheFaultAfterAQuotedValueOnTheSameLine(t *testing.T) {
	cases := []struct{ name, input string }{
		{"double quoted", "P=ok\rA=\"a\rb\"\rK=bad\rtail\n"},
		{"single quoted", "P=ok\rA='a\rb'\rK=bad\rtail\n"},
		{"escaped quote inside", "P=ok\rA=\"a\\\"c\rb\"\rK=bad\rtail\n"},
		{"several CRs inside", "A=\"a\rb\rc\"\rK=bad\rtail\n"},
		{"legal statement between", "A=\"a\rb\"\rB=ok\rK=bad\rtail\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProjectDotenv([]byte(tc.input))
			if err == nil {
				t.Fatal("a carriage return splitting a value must be refused")
			}
			if !strings.HasPrefix(err.Error(), "K:") {
				t.Errorf("the fault is in K, not in the separators before it: %v", err)
			}
		})
	}
}

// TestImportRefusalIgnoresQuotedCarriageReturnsInABudgetedFile: a legal quoted
// value holding several carriage returns in a near-limit file must not cost
// the diagnosis. They are content, not separators; the prefix parse sees them
// once. The fixture sits at the import limit so the case cannot be dismissed
// as small.
func TestImportRefusalIgnoresQuotedCarriageReturnsInABudgetedFile(t *testing.T) {
	padLen := MaxProjectDotenvBytes - 6000
	var b strings.Builder
	b.WriteString("A=\"a\rb\rc\rd\re\"\n")
	b.WriteString("P=" + strings.Repeat("z", padLen) + "\n")
	b.WriteString("K=bad\rtail\n")
	if b.Len() > MaxProjectDotenvBytes {
		t.Fatalf("fixture is %d bytes, over the import limit", b.Len())
	}
	_, err := ParseProjectDotenv([]byte(b.String()))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("legal quoted carriage returns must not cost the diagnosis: %v", err)
	}
}

// TestImportRefusalBoundsSeparatorFilterWork: twenty thousand CR-only blank
// separators in front of a malformed statement used to cost one scan each,
// quadratic on a file the size limit admits. The position-driven attribution
// does not iterate separators at all; the refusal must stay generic (the
// failure follows a newline, not a bare CR) and must not name a variable.
func TestImportRefusalBoundsSeparatorFilterWork(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("\r", 20000))
	b.WriteString("A=" + strings.Repeat("x", 20) + "\n")
	b.WriteString("BROKEN=\"\n")

	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a malformed file must be refused")
		}
		// The generic fallback is the expected outcome; a wrong variable name
		// would not be.
		if strings.Contains(err.Error(), "BROKEN") {
			t.Errorf("the refusal must not name a variable it did not prove: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a flood of blank separators must not slow the refusal")
	}
}

// TestImportRefusalStillNamesTheFaultAfterTheFilterBudget guards the obvious
// over-correction: bounding the work must not cost a diagnosis on an ordinary
// file. Two thousand legal bare-CR separators precede the fault here.
func TestImportRefusalStillNamesTheFaultAfterTheFilterBudget(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "A%d=1\r", i)
	}
	b.WriteString("K=bad\rtail\n")
	_, err := ParseProjectDotenv([]byte(b.String()))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("a legal-separator prefix must not cost the diagnosis: %v", err)
	}
}

// TestImportRefusalNamesTheFaultInALargeClassicMacFile pins the largest
// classic-Mac file the importer accepts: ~1 MiB of CR-terminated assignments
// with the fault on the last line. The attribution is one prefix parse of that
// file, so the diagnosis must arrive with it.
func TestImportRefusalNamesTheFaultInALargeClassicMacFile(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < MaxProjectDotenvBytes-64; i++ {
		fmt.Fprintf(&b, "KEY%05d=value%05d\r", i, i)
	}
	b.WriteString("K=bad\rtail\n")
	if b.Len() > MaxProjectDotenvBytes {
		t.Fatalf("fixture is %d bytes, over the import limit; it would test the wrong refusal", b.Len())
	}
	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a carriage return splitting a value must be refused")
		}
		if !strings.Contains(err.Error(), "K:") {
			t.Errorf("a large well-formed file must not cost the diagnosis: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a large classic-Mac file must not slow the diagnosis")
	}
}

// TestImportRefusalNamesAValueSplitBySeveralCarriageReturns: a value split by
// a run of carriage returns fails on the first fragment, and the prefix before
// that fragment's separator still ends at the split assignment, so K is named
// without having to reason about the rest of the run.
func TestImportRefusalNamesAValueSplitBySeveralCarriageReturns(t *testing.T) {
	for _, in := range []string{
		"K=bad\rtail\rend\n",
		"K=a\rb\rc\rd\n",
	} {
		_, err := ParseProjectDotenv([]byte(in))
		if err == nil {
			t.Fatalf("a value split by carriage returns must be refused: %q", in)
		}
		if !strings.Contains(err.Error(), "K:") {
			t.Errorf("a value split by several carriage returns must be named: %q -> %v", in, err)
		}
		if !strings.Contains(err.Error(), "carriage return") {
			t.Errorf("the refusal must state the carriage-return rule: %v", err)
		}
	}
}

// TestImportRefusalDoesNotClaimTheRemainderHasNoAssignment is a wording guard:
// `K=bad\rtail="` is attributed to K, but the remainder does contain an
// assignment delimiter and fails on its unterminated quote, so a message
// asserting the remainder "has no assignment" describes the wrong fault. The
// rule is about validity, and the message says so.
func TestImportRefusalDoesNotClaimTheRemainderHasNoAssignment(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("K=bad\rtail=\"\n"))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("the fault must be named: %v", err)
	}
	if strings.Contains(err.Error(), "has no assignment") {
		t.Errorf("the remainder does contain a delimiter; the message must not claim otherwise: %v", err)
	}
}

// TestImportRefusalBoundsWorkOnManyMalformedFragments: a file of malformed
// fragments used to spend a whole-file re-parse on each one. The attribution
// parses once and reports; whatever it names, the message must not quote
// source text.
func TestImportRefusalBoundsWorkOnManyMalformedFragments(t *testing.T) {
	var b strings.Builder
	b.WriteString("BROKEN=\"\n")
	for i := 0; i < 4000; i++ {
		b.WriteString("K=\"\r")
	}
	done := make(chan error, 1)
	go func() {
		_, err := ParseProjectDotenv([]byte(b.String()))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a malformed file must be refused")
		}
		// Either a proven key or the generic fallback is acceptable; quoted
		// source text is not.
		if strings.Contains(err.Error(), "tail") || strings.Contains(err.Error(), "K=\"") {
			t.Errorf("the refusal must not quote source text: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("attribution must terminate on a file of malformed fragments")
	}
}

// TestImportRefusalNamesSingleQuotedDuplicateOverwrittenByLaterAssignment:
// both `"` and `'` quote values, so a quoted duplicate may use either; the
// entry span covers both forms.
func TestImportRefusalNamesSingleQuotedDuplicateOverwrittenByLaterAssignment(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("A='bad\x00'\nA=good\n"))
	if err == nil {
		t.Fatal("a NUL byte in a value must be refused")
	}
	if !strings.Contains(err.Error(), "A:") {
		t.Errorf("a single-quoted duplicate overwritten later must still be named: %v", err)
	}
	if !strings.Contains(err.Error(), "NUL") {
		t.Errorf("the refusal must state the NUL rule: %v", err)
	}
}

// TestImportRefusalStillNamesTheFaultAcrossCRLFLineEndings pins the CRLF
// boundary: a CRLF pair is an ordinary line ending, never a fault candidate,
// so a file that uses CRLF endings must still have a genuine bare-CR split
// diagnosed, and the CRLF pairs themselves must never be blamed.
func TestImportRefusalStillNamesTheFaultAcrossCRLFLineEndings(t *testing.T) {
	for _, c := range []struct{ name, data string }{
		{"fault after CRLF", "A=ok\r\nK=bad\rtail\r\n"},
		{"CRLF everywhere", "A=ok\r\nK=bad\rtail\r\nB=2\r\n"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: a carriage return splitting a value must be refused", c.name)
		}
		if !strings.Contains(err.Error(), "K:") {
			t.Errorf("%s: the fault is in K: %v", c.name, err)
		}
		if !strings.Contains(err.Error(), "carriage return") {
			t.Errorf("%s: the refusal must state the carriage-return rule: %v", c.name, err)
		}
	}
}

// TestImportRefusalAmbiguousLocationNamesNothing pins the non-disclosure guard
// on attribution: when the assignment the carriage return split has a name
// that is not safe to echo, no variable can be named without risking pasted
// text, so the refusal stays generic. Naming it would echo material that was
// never a real variable name.
func TestImportRefusalAmbiguousLocationNamesNothing(t *testing.T) {
	longName := strings.Repeat("a", 200)
	_, err := ParseProjectDotenv([]byte(longName + "=bad\rtail\n"))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "syntax is invalid or unsupported") {
		t.Errorf("an unnameable split must fall back to the generic wording: %v", err)
	}
	if strings.Contains(err.Error(), longName) {
		t.Errorf("the unsafe name must be redacted: %v", err)
	}
	if strings.Contains(err.Error(), "carriage return") {
		t.Errorf("the generic fallback must not claim a carriage-return rule it cannot tie to a variable: %v", err)
	}
}

// The tests below are specific to the go-rotini port: they pin the behaviors
// that changed with the library and the regressions found while writing the
// position-driven attribution.

// TestImportRefusalNamesTheSplitWithALaterAssignmentOnTheFragmentLine: the
// failing fragment's own line may contain a delimiter in a LATER, unrelated
// statement. The first draft of the port checked for a delimiter anywhere
// after the fragment and refused to attribute `K=bad\rtail\nB=2` — the
// delimiter in B's statement hid K's fault. The attribution now ignores
// everything after the reported position.
func TestImportRefusalNamesTheSplitWithALaterAssignmentOnTheFragmentLine(t *testing.T) {
	_, err := ParseProjectDotenv([]byte("K=bad\rtail\nB=2\n"))
	if err == nil {
		t.Fatal("a carriage return splitting a value must be refused")
	}
	if !strings.Contains(err.Error(), "K:") {
		t.Errorf("a later statement must not hide the fault: %v", err)
	}
}

// TestImportRefusalNamesHyphenatedNameAsUnsupported: the previous loader
// accepted `MY-KEY` as a variable name and refused it in the identifier check
// with a generic message; go-rotini fails the key scan, and the name is safe
// to echo, so the refusal now names it.
func TestImportRefusalNamesHyphenatedNameAsUnsupported(t *testing.T) {
	for _, c := range []struct {
		name, data, wantVar string
	}{
		{"hyphen in name", "MY-KEY=1\n", "MY-KEY"},
		{"digit start after export", "export 1bad=1\n", "1bad"},
	} {
		_, err := ParseProjectDotenv([]byte(c.data))
		if err == nil {
			t.Fatalf("%s: an unsupported variable name must be refused", c.name)
		}
		if !strings.Contains(err.Error(), c.wantVar) {
			t.Errorf("%s: the refusal must name %q: %v", c.name, c.wantVar, err)
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: the refusal must state the rule: %v", c.name, err)
		}
	}
}

// TestImportRefusalRefusesBacktickValues: the previous loader treated a
// backtick as an ordinary character, so `K=` + backtick-quoted text imported
// with the backticks inside the value. go-rotini refuses backtick-quoted
// values unless explicitly enabled. Refusing is deliberate: a silent value
// change would be worse than a loud refusal. The refusal names the variable
// and the rule, and never quotes the value.
func TestImportRefusalRefusesBacktickValues(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		named string // empty when the attempted name is not safe to echo
	}{
		{"quoted", "K=`SUPERSECRET`\n", "K"},
		{"unterminated", "K=`X\n", "K"},
		{"export-prefixed", "export K=`X`\n", "K"},
		{"tab-separated export prefix", "export\tK=`X`\n", "K"},
		{"yaml-delimiter", "K: `X`\n", "K"},
		{"second-line", "A=1\nK=`SECRET2`\n", "K"},
		{"oversized-name", strings.Repeat("N", 200) + "=`X`\n", ""},
	}
	for _, c := range cases {
		_, err := ParseProjectDotenv([]byte(c.in))
		if err == nil {
			t.Fatalf("%s: a backtick-quoted value must be refused", c.name)
		}
		msg := err.Error()
		if strings.Contains(msg, "SUPERSECRET") || strings.Contains(msg, "SECRET2") {
			t.Errorf("%s: the refusal must not quote the value: %v", c.name, err)
		}
		if !strings.Contains(msg, "backtick") {
			t.Errorf("%s: the refusal must state the rule: %v", c.name, err)
		}
		if c.named != "" && !strings.Contains(msg, c.named+":") {
			t.Errorf("%s: the refusal must name the variable %q: %v", c.name, c.named, err)
		}
		if c.named == "" && strings.Contains(msg, strings.Repeat("N", 20)) {
			t.Errorf("%s: a name that is not safe to echo must not be quoted: %v", c.name, err)
		}
	}
	// A bad name fails its key scan before the value is reached, so the name
	// rule is reported, not the backtick rule.
	_, err := ParseProjectDotenv([]byte("MY-KEY=`X`\n"))
	if err == nil || strings.Contains(err.Error(), "backtick") {
		t.Errorf("a key-scan failure must take precedence over the backtick rule: %v", err)
	}
	// A backtick that does not open the value is literal content, not a
	// refusal: mid-value in a raw value, and anywhere inside quotes.
	for _, in := range []string{"K=va`lue\n", "K=\"a`b\"\n", "K='a`b'\n"} {
		got, err := ParseProjectDotenv([]byte(in))
		if err != nil {
			t.Errorf("%q must import with the backtick as literal content: %v", in, err)
		}
		if err == nil && strings.Contains(got.Values["K"], "`") == false {
			t.Errorf("%q must keep the backtick in the value: %q", in, got.Values["K"])
		}
	}
}

// TestDotenvParserContractPinnedToRotini records the go-rotini v1.0.3
// behaviors the diagnostics above depend on, so a dependency bump that changes
// any of them fails loudly instead of silently changing what is reported.
// Each case states the exact contract and how the code relies on it.
func TestDotenvParserContractPinnedToRotini(t *testing.T) {
	// A bare CR after a value makes the trailing fragment fail its key scan,
	// positioned ON the fragment — the error position is what crSplitName
	// walks back from. Both the newline-terminated and the EOF forms fail;
	// the previous loader silently accepted the EOF form.
	for _, in := range []string{"K=bad\rreturn\n", "K=bad\rreturn"} {
		_, err := dotenv.Parse([]byte(in))
		var pe *dotenv.ParseError
		if err == nil || !errors.As(err, &pe) {
			t.Fatalf("contract: %q must fail with a positioned error, got %v", in, err)
		}
		if pe.Pos.Line != 2 {
			t.Errorf("contract: %q must fail on the fragment (line 2), got line %d", in, pe.Pos.Line)
		}
	}
	// nameScanRefusal classifies on these exact message prefixes; a reworded
	// message must be noticed here, not in a misdiagnosed refusal.
	if _, err := dotenv.Parse([]byte("1bad=1\n")); err == nil {
		t.Error("contract: a digit-start key must fail the parse")
	} else if !strings.HasPrefix(err.Error(), "dotenv: line 1, column 1: invalid identifier start byte") {
		t.Errorf("contract: digit-start failure must carry the pinned prefix, got %v", err)
	}
	if _, err := dotenv.Parse([]byte("has space=1\n")); err == nil {
		t.Error("contract: a space in a key must fail the parse")
	} else if !strings.Contains(err.Error(), "expected '=' or ':' after key") {
		t.Errorf("contract: missing-delimiter failure must carry the pinned prefix, got %v", err)
	}
	// CRLF pairs are normalized before parsing, not treated as a fault.
	if v, err := dotenv.Parse([]byte("A=\"x\r\ny\"\n")); err != nil || v.ToMap()["A"] != "x\ny" {
		t.Errorf("contract: CRLF inside a quoted value is normalized, not a fault: %v", err)
	}
	// A bare CR between assignments is a legal separator.
	if v, err := dotenv.Parse([]byte("A=1\rB=2\n")); err != nil || v.ToMap()["A"] != "1" || v.ToMap()["B"] != "2" {
		t.Errorf("contract: a bare CR between assignments must split them legally: %v", err)
	}
	// A bare CR inside quotes is value content, not a terminator.
	if _, err := dotenv.Parse([]byte("A=\"ok\rstill\"\n")); err != nil {
		t.Errorf("contract: a bare CR inside quotes is value content, not a terminator: %v", err)
	}
	// NUL and invalid UTF-8 pass through values — the importer's whole-file
	// prechecks exist because of this. If the library starts refusing them,
	// attribution falls back to file-level messages (still safe), but the
	// change must be seen here.
	if _, err := dotenv.Parse([]byte("A=bad\x00x\n")); err != nil {
		t.Errorf("contract: a NUL in a value must pass the parse (the importer prechecks it): %v", err)
	}
	if _, err := dotenv.Parse([]byte("A=bad\xff\n")); err != nil {
		t.Errorf("contract: invalid UTF-8 in a value must pass the parse (the importer prechecks it): %v", err)
	}
	// No expansion by default: a '$' is literal, which is the importer's
	// preserve-literal-values contract.
	if v, err := dotenv.Parse([]byte("K=$A\n")); err != nil || v.ToMap()["K"] != "$A" {
		t.Errorf("contract: a dollar must stay literal without expansion: %v", err)
	}
	// Duplicates resolve last-definition-wins, and entries record every
	// assignment in order — the span attribution for overwritten duplicates
	// depends on both.
	if v, err := dotenv.Parse([]byte("A=1\nA=2\n")); err != nil || v.ToMap()["A"] != "2" {
		t.Errorf("contract: duplicates must resolve last-definition-wins: %v", err)
	}
	if f, err := dotenv.Parse([]byte("A=1\nA=2\n")); err != nil || len(f.Entries()) != 2 {
		t.Errorf("contract: entries must record every assignment in order: %v", err)
	}
	// An unterminated quote fails AT the opening quote, not at EOF: the
	// walk-back in crSplitName must not cross a quote into an earlier
	// statement.
	_, err := dotenv.Parse([]byte("BROKEN=\"\n"))
	var pe *dotenv.ParseError
	if err == nil || !errors.As(err, &pe) {
		t.Fatalf("contract: an unterminated quote must fail with a positioned error, got %v", err)
	}
	if pe.Pos.Offset != 7 {
		t.Errorf("contract: an unterminated quote must be reported at the quote (offset 7), got %d", pe.Pos.Offset)
	}
	// A value that opens with a backtick fails with this exact message,
	// positioned ON the backtick — backtickRefusal classifies on the prefix
	// and names the variable left of the delimiter on that line.
	_, err = dotenv.Parse([]byte("K=`X`\n"))
	if err == nil || !errors.As(err, &pe) {
		t.Fatalf("contract: a backtick-quoted value must fail with a positioned error, got %v", err)
	}
	if !strings.HasPrefix(pe.Message, "backtick-quoted value not allowed") {
		t.Errorf("contract: the backtick refusal must carry the pinned prefix, got %q", pe.Message)
	}
	if pe.Pos.Offset != 2 {
		t.Errorf("contract: the backtick refusal must be positioned on the backtick (offset 2), got %d", pe.Pos.Offset)
	}
	// A backtick that does not open the value is literal content: mid-value
	// in a raw value, and anywhere inside quotes. The refusal boundary is
	// the value's first byte only.
	for _, in := range []string{"K=va`lue\n", "K=\"a`b\"\n", "K='a`b'\n"} {
		if _, err := dotenv.Parse([]byte(in)); err != nil {
			t.Errorf("contract: %q must keep a non-opening backtick as content: %v", in, err)
		}
	}
	// A bad name fails its key scan before the value is reached, so the
	// name rule is reported, never the backtick rule.
	if _, err := dotenv.Parse([]byte("MY-KEY=`X`\n")); err == nil || !strings.Contains(err.Error(), "expected '=' or ':' after key") {
		t.Errorf("contract: a key-scan failure must take precedence over the backtick rule: %v", err)
	}
	// The default line cap equals the importer's 1 MiB cap. The attribution
	// reparses set it explicitly, but a version bump that changes the default
	// is exactly the divergence the explicit option guards against, so it
	// must be noticed here.
	longLine := "K=\"" + strings.Repeat("v", MaxProjectDotenvBytes-16) + "\"\n"
	if _, err := dotenv.Parse([]byte(longLine)); err != nil {
		t.Errorf("contract: a line just under the 1 MiB cap must parse with default options: %v", err)
	}
}
