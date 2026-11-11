import assert from "node:assert/strict";
import test from "node:test";
import dotenv from "dotenv";
import { expand } from "dotenv-expand";

// These are proposed guest-visible markers, not credential values. Exercise
// the real loaders rather than mocking their interpolation behavior.
const markers = [
  "__CODEBOX_SECRET_COMPONENT_A_API_KEY__",
  "__CODEBOX_SECRET_COMPONENT_B_API_KEY__",
];

for (const marker of markers) {
  for (const quote of ["", "'", '"']) {
    test(`dotenv preserves opaque marker with ${quote || "no"} quotes`, () => {
      const parsed = dotenv.parse(`API_KEY=${quote}${marker}${quote}\n`);
      assert.equal(parsed.API_KEY, marker);
    });

    test(`dotenv-expand preserves opaque marker with ${quote || "no"} quotes`, () => {
      const parsed = dotenv.parse(`API_KEY=${quote}${marker}${quote}\n`);
      // Do not import, modify or interpolate the host's environment.
      const result = expand({ parsed, processEnv: {} });
      assert.equal(result.parsed.API_KEY, marker);
    });
  }
}

test("same variable in two components retains distinct markers", () => {
  const values = markers.map((marker) =>
    expand({ parsed: dotenv.parse(`API_KEY=${marker}\n`), processEnv: {} })
      .parsed.API_KEY,
  );
  assert.deepEqual(values, markers);
  assert.notEqual(values[0], values[1]);
});

test("default dollar placeholders are not safe through interpolation", () => {
  for (const quote of ["", "'", '"']) {
    const placeholder = "$MSB_CODEBOX_SECRET_A";
    const parsed = dotenv.parse(`API_KEY=${quote}${placeholder}${quote}\n`);
    assert.equal(parsed.API_KEY, placeholder);
    const result = expand({ parsed, processEnv: {} });
    assert.equal(result.parsed.API_KEY, "");
  }
});

// The guest-readable delivery encoding: a value is written as a single-quoted
// literal, and these are the value classes that form must carry exactly. The
// Go side (internal/codebox/project_secret_encoding_loaders_test.go) proves
// the encoder emits this form; these tests prove the loaders read it back.
const guestReadableValues = {
  PLAIN: "plainvalue",
  MULTILINE: "-----BEGIN KEY-----\nAAAA\n-----END KEY-----",
  TRAILING: "trailing   ",
  LEADING: "   leading",
  HASH: "hash # inside",
  DQUOTE: 'has " quote',
  BACKSLASH: "back\\slash",
  EMPTY: "",
};

for (const [name, value] of Object.entries(guestReadableValues)) {
  test(`single-quoted ${name} round-trips through dotenv`, () => {
    const parsed = dotenv.parse(`${name}='${value}'\n`);
    assert.equal(parsed[name], value);
  });

  test(`single-quoted ${name} round-trips through dotenv-expand`, () => {
    const parsed = dotenv.parse(`${name}='${value}'\n`);
    const result = expand({ parsed, processEnv: {} });
    assert.equal(result.parsed[name], value);
  });
}

test("a raw multiline value breaks the whole file for the line-oriented parsers", () => {
  // Not confined to one variable: this is why the value is encoded.
  const raw = "TLS_KEY=-----BEGIN KEY-----\nAAAA\n-----END KEY-----\nAPI_KEY=still-here\n";
  const parsed = dotenv.parse(raw);
  assert.notEqual(parsed.TLS_KEY, "-----BEGIN KEY-----\nAAAA\n-----END KEY-----");
});

test("single quotes cannot carry an apostrophe, and a dollar is loader-dependent", () => {
  // Pinned so a future attempt to "just escape it" has the evidence in front
  // of it. These are failures of the *quoted* form, not encoder refusals: the
  // encoder writes an apostrophe raw, and that raw form round-trips. Neither
  // is a Node problem, which is why the assertions below still pass.
  const apostrophe = "it's";
  const dollar = "pre$VAR-post";

  // The quoted spelling is the broken one. Node happens to read it back (it
  // takes the last quote on the line), but godotenv refuses the whole file and
  // python-dotenv misparses it, so the quoted form cannot be emitted. That is
  // why the encoder routes a value holding an apostrophe to the raw form; see
  // dotenvQuotedObstacle in internal/codebox/project_secret_guest_files.go.
  assert.equal(dotenv.parse(`K='${apostrophe}'\n`).K, apostrophe);

  // The dollar case is a genuine conflict between loaders rather than a
  // mangling: the same bytes mean the literal value to plain dotenv and
  // "pre-post" once expansion runs, so no encoding is exact for both and the
  // value is refused instead of silently altered.
  assert.equal(dotenv.parse(`K='${dollar}'\n`).K, dollar);
  assert.equal(expand({ parsed: dotenv.parse(`K='${dollar}'\n`), processEnv: {} }).parsed.K, "pre-post");
});

// Node reads every single-quoted backslash shape exactly, including the pair
// and the trailing backslash the other two loaders mishandle. Those are why the
// raw fallback exists: it is not Node's doing. The need is driven by
// python-dotenv (the pair, silently collapsed, and the trailing backslash) and
// godotenv (the trailing backslash, refused as an unterminated quote).
test("Node reads the single-quoted backslash shapes the other loaders mishandle", () => {
  const values = {
    interior: "back\\slash",
    leading: "\\leading",
    pair: "a\\\\b",
    trailing: "trailing\\",
  };
  // The realistic shape: the risky value followed by another quoted value,
  // which is what a generated file contains.
  const body = Object.entries(values)
    .map(([name, value]) => `K_${name}='${value}'\n`)
    .join("");
  const parsed = dotenv.parse(`${body}K_AFTER='after'\n`);
  for (const [name, value] of Object.entries(values)) {
    assert.equal(parsed[`K_${name}`], value, `Node must read K_${name} exactly`);
  }
  assert.equal(parsed.K_AFTER, "after");
});

// The unquoted fallback, exercised on the backslash shapes quoting cannot
// carry through every loader: a backslash pair and a trailing backslash. The
// Go encoder writes a value unquoted whenever quoting cannot carry it and the
// raw form can (see dotenvRawObstacle), which also covers an apostrophe. Every
// value in this table is emitted raw by the encoder; a value it carries quoted
// despite a backslash (a single interior one, such as `a\=b`) is in the
// quoted table below. The Go tests assert each choice.
const rawFallback = {
  PAIR: "a\\\\b",
  QUAD: "a\\\\\\\\b",
  WINDOWS_PATH: "C:\\Users\\",
  PROGRAM_FILES: "C:\\Program Files\\",
  UNC: "\\\\server\\share",
  TRAILING: "trailing\\",
};

test("dotenv reads a raw backslash pair and a trailing backslash exactly", () => {
  const body = Object.entries(rawFallback)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const parsed = dotenv.parse(`${body}K_AFTER='after'\n`);
  for (const [name, value] of Object.entries(rawFallback)) {
    assert.equal(parsed[`K_${name}`], value, `Node must read raw K_${name} exactly`);
  }
  assert.equal(parsed.K_AFTER, "after");
});

// The raw form carries non-ASCII verbatim, because an unquoted value is never
// decoded. These are the values the widened predicate now accepts: before, a
// non-ASCII byte was refused outright, which rejected most of the world's file
// names over a hazard that only exists inside quotes.
const rawNonASCII = {
  ACCENTED_PATH: "C:\\Répertoire\\",
  TRAILING_ACCENT: "naïve\\",
  GREEK: "Ω\\",
  CJK: "秘密\\",
  EMOJI: "🚀\\",
  MIXED: "C:\\Données\\rép-🚀\\",
};

test("dotenv reads a raw non-ASCII path exactly", () => {
  const body = Object.entries(rawNonASCII)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const parsed = dotenv.parse(`${body}K_AFTER='after'\n`);
  for (const [name, value] of Object.entries(rawNonASCII)) {
    assert.equal(parsed[`K_${name}`], value, `Node must read raw K_${name} exactly`);
  }
  assert.equal(parsed.K_AFTER, "after");
});

test("dotenv-expand reads a raw non-ASCII path exactly", () => {
  const body = Object.entries(rawNonASCII)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const result = expand({
    parsed: dotenv.parse(`${body}K_AFTER='after'\n`),
    processEnv: {},
  });
  for (const [name, value] of Object.entries(rawNonASCII)) {
    assert.equal(
      result.parsed[`K_${name}`],
      value,
      `Node with dotenv-expand must read raw K_${name} exactly`,
    );
  }
  assert.equal(result.parsed.K_AFTER, "after");
});

// Quotes and backticks are positional, not forbidden. The encoder refuses a
// leading quote and a wrapping backtick pair only where no other form is
// exact, and delivers everything else. These are the values it now delivers
// raw, each measured through the real loader; the Go side asserts the same
// set (project_secret_encoding_test.go, positional quotes and backticks).
// The form is picked by the encoder, so only the shapes quoting cannot carry
// appear raw: a single quote cannot be escaped in a quoted value, and a
// backslash pair collapses in one.
const positionalRaw = {
  APOSTROPHE: "it's",
  APOSTROPHE_AND_PAIR: "it's a\\\\b",
  WINDOWS_APOSTROPHE: "C:\\Users\\O'Brien\\",
  DQUOTE_AND_PAIR: 'a"b\\\\',
  BACKTICK_AND_PAIR: "a`b\\\\",
};

test("dotenv reads a raw value with an interior or trailing quote exactly", () => {
  const body = Object.entries(positionalRaw)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const parsed = dotenv.parse(`${body}K_AFTER='after'\n`);
  for (const [name, value] of Object.entries(positionalRaw)) {
    assert.equal(parsed[`K_${name}`], value, `Node must read raw K_${name} exactly`);
  }
  assert.equal(parsed.K_AFTER, "after");
});

// The same values through dotenv-expand. A backtick has no expansion meaning,
// so this is where they would be lost if it did.
test("dotenv-expand reads a raw interior or trailing quote exactly", () => {
  const body = Object.entries(positionalRaw)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const result = expand({
    parsed: dotenv.parse(`${body}K_AFTER='after'\n`),
    processEnv: {},
  });
  for (const [name, value] of Object.entries(positionalRaw)) {
    assert.equal(
      result.parsed[`K_${name}`],
      value,
      `Node with dotenv-expand must read raw K_${name} exactly`,
    );
  }
  assert.equal(result.parsed.K_AFTER, "after");
});

// The values the single-quoted form carries. A quote is ordinary inside single
// quotes, so a double quote, a leading or wrapping backtick and a hash all pass
// through untouched.
const positionalQuoted = {
  DQUOTE_ALONE: 'a"b',
  EQUALS: "a\\=b",
  BACKTICKS_AROUND_A_WORD: "`ab`",
  LONE_LEADING_BACKTICK: "`abc",
  EMPTY_BACKTICK_PAIR: "``",
  HASH_ALONE: "before#after",
  SPACE_HASH_ALONE: "value # comment",
};

test("single-quoted interior quotes and backticks round-trip through dotenv", () => {
  const body = Object.entries(positionalQuoted)
    .map(([name, value]) => `K_${name}='${value}'\n`)
    .join("");
  const parsed = dotenv.parse(`${body}K_AFTER='after'\n`);
  for (const [name, value] of Object.entries(positionalQuoted)) {
    assert.equal(parsed[`K_${name}`], value, `Node must read quoted K_${name} exactly`);
  }
  assert.equal(parsed.K_AFTER, "after");
});

// The shapes no encoding can carry, and what each loader does with them. This
// is the evidence behind the refusal: Node strips a wrapping backtick pair from
// an unquoted value, while python-dotenv and godotenv keep it, so raw is wrong
// for Node and quoted is wrong for Python; see
// internal/codebox/project_secret_encoding_loaders_test.go.
test("Node strips a wrapping backtick pair the encoder refuses", () => {
  assert.equal(dotenv.parse("K=`a'b`\n").K, "a'b");
  assert.equal(dotenv.parse("K=`a\\\\b`\n").K, "a\\\\b");
  // Quoting carries both in Node, but python-dotenv and godotenv refuse the
  // quoted form, so neither form is exact in every loader.
  assert.equal(dotenv.parse("K='`a'b`'\n").K, "`a'b`");
});

// A carriage return is refused by the encoder, and this is why: inside quotes,
// Node normalises CR and CRLF to LF, so a secret whose newlines came from a
// Windows source would be altered in place rather than delivered.
test("a quoted carriage return is normalised to a newline", () => {
  assert.equal(dotenv.parse("K='a\rb'\n").K, "a\nb");
  assert.equal(dotenv.parse("K='line1\r\nline2'\n").K, "line1\nline2");
});

// The edge of an unquoted value is where Node trims; the interior is exact.
test("Node trims a raw edge but keeps an interior whitespace character", () => {
  assert.equal(dotenv.parse("K=a\u00a0\n").K, "a");
  assert.equal(dotenv.parse("K=\u00a0a\n").K, "a");
  assert.equal(dotenv.parse("K=a\u00a0b\n").K, "a\u00a0b");
  assert.equal(dotenv.parse("K=\ufeffa\n").K, "a");
  assert.equal(dotenv.parse("K=a\ufeff\n").K, "a");
});

// The loader must never evaluate a value it is only reading: `dotenv-expand`
// 12.0.3 and 13.0.0 keep `$(…)` verbatim with no, single, and double quotes,
// while `1000.0.0` imports `child_process` (`execSync`) and returns the command
// output in all three, so a project `.env` becomes code execution for any
// application that has adopted an expanding loader. That is why the
// `1000.0.0` major is refused. Codebox's own generated guest files cannot
// carry the syntax anyway — `dotenvLiteral` refuses `$` in the quoted and the
// unquoted form alike — so these tests pin the dependency property, not a
// delivery path. The marker tests above cannot see the difference, because a
// marker contains no substitution syntax.
for (const quote of ["", "'", '"']) {
  test(`dotenv-expand does not evaluate command substitution with ${quote || "no"} quotes`, () => {
    const payload = "$(echo INJECTED)";
    const result = expand({
      parsed: dotenv.parse(`API_KEY=${quote}${payload}${quote}\n`),
      processEnv: {},
    });
    assert.equal(
      result.parsed.API_KEY,
      payload,
      "dotenv-expand must return the substitution text verbatim, never its result",
    );
  });
}

test("dotenv-expand reads a raw backslash pair and trailing backslash exactly", () => {
  const body = Object.entries(rawFallback)
    .map(([name, value]) => `K_${name}=${value}\n`)
    .join("");
  const result = expand({
    parsed: dotenv.parse(`${body}K_AFTER='after'\n`),
    processEnv: {},
  });
  for (const [name, value] of Object.entries(rawFallback)) {
    assert.equal(
      result.parsed[`K_${name}`],
      value,
      `Node with dotenv-expand must read raw K_${name} exactly`,
    );
  }
  assert.equal(result.parsed.K_AFTER, "after");
});
