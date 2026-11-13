"""Real python-dotenv compatibility tests, isolated from host credentials."""

import io
import os
import unittest
from unittest.mock import patch

from dotenv import dotenv_values


class ProjectSecretLoaderTests(unittest.TestCase):
    def test_opaque_markers_survive_all_quote_modes(self):
        markers = [
            "__CODEBOX_SECRET_COMPONENT_A_API_KEY__",
            "__CODEBOX_SECRET_COMPONENT_B_API_KEY__",
        ]
        with patch.dict(os.environ, {}, clear=True):
            for marker in markers:
                for quote in ["", "'", '"']:
                    for interpolate in [False, True]:
                        with self.subTest(marker=marker, quote=quote, interpolate=interpolate):
                            values = dotenv_values(
                                stream=io.StringIO(f"API_KEY={quote}{marker}{quote}\n"),
                                interpolate=interpolate,
                            )
                            self.assertEqual(values["API_KEY"], marker)

    def test_dollar_behavior_depends_on_bracing(self):
        with patch.dict(os.environ, {}, clear=True):
            literal = dotenv_values(stream=io.StringIO("API_KEY=$MSB_CODEBOX_SECRET_A\n"))
            expanded = dotenv_values(stream=io.StringIO("API_KEY=${MSB_CODEBOX_SECRET_A}\n"))
        self.assertEqual(literal["API_KEY"], "$MSB_CODEBOX_SECRET_A")
        self.assertEqual(expanded["API_KEY"], "")


class GuestReadableEncodingTests(unittest.TestCase):
    """The single-quoted literal is the guest-readable delivery encoding.

    The Go encoder emits this form (see
    internal/codebox/project_secret_encoding_loaders_test.go); these cases
    prove python-dotenv reads it back exactly, with and without interpolation.
    """

    VALUES = {
        "PLAIN": "plainvalue",
        "MULTILINE": "-----BEGIN KEY-----\nAAAA\n-----END KEY-----",
        "TRAILING": "trailing   ",
        "LEADING": "   leading",
        "HASH": "hash # inside",
        "DQUOTE": 'has " quote',
        "BACKSLASH": "back\\slash",
        "EMPTY": "",
    }

    def test_single_quoted_values_round_trip(self):
        with patch.dict(os.environ, {}, clear=True):
            for name, value in self.VALUES.items():
                for interpolate in [False, True]:
                    with self.subTest(name=name, interpolate=interpolate):
                        values = dotenv_values(
                            stream=io.StringIO(f"{name}='{value}'\n"),
                            interpolate=interpolate,
                        )
                        self.assertEqual(values[name], value)


class QuotedFormFailureTests(unittest.TestCase):
    """Why the quoted form cannot carry a backslash pair or a trailing backslash.

    These are properties of the failing form, not encoder refusals: the encoder
    writes both shapes unquoted, and each reads back exactly. Node reads all of
    these quoted shapes exactly; python-dotenv does not, so the choice is driven
    by this loader. The shape matters: a value is always followed by another
    quoted value in a generated file, and that is where the trailing backslash
    does its damage.
    """

    def _values(self, body):
        with patch.dict(os.environ, {}, clear=True):
            return dotenv_values(stream=io.StringIO(body))

    def test_a_backslash_pair_is_silently_collapsed(self):
        values = self._values("K_TARGET='a\\\\b'\nK_AFTER='after'\n")
        self.assertEqual(values["K_AFTER"], "after")
        self.assertNotEqual(values["K_TARGET"], "a\\\\b")
        self.assertEqual(values["K_TARGET"], "a\\b")

    def test_a_trailing_backslash_loses_the_value_and_the_next_line(self):
        values = self._values("K_TARGET='trailing\\'\nK_AFTER='after'\n")
        self.assertIsNone(values.get("K_TARGET"))
        self.assertIsNone(values.get("K_AFTER"))

    def test_an_interior_and_a_leading_backslash_are_fine(self):
        values = self._values("K_A='back\\slash'\nK_B='\\leading'\nK_AFTER='after'\n")
        self.assertEqual(values["K_A"], "back\\slash")
        self.assertEqual(values["K_B"], "\\leading")
        self.assertEqual(values["K_AFTER"], "after")


class RawFallbackEncodingTests(unittest.TestCase):
    """The unquoted fallback, on the backslash shapes quoting cannot carry.

    The Go encoder writes a value unquoted when the single-quoted form cannot
    represent it and the raw form can (see dotenvRawObstacle in
    internal/codebox/project_secret_guest_files.go); an apostrophe also forces
    the raw form. These cases prove the backslash shapes, and the raw literals
    real values take, read back exactly. The value sits between quoted values,
    which is the shape a generated file actually has.
    """

    # Each value is written exactly as a loader must read it back. "quoted"
    # and "raw" name the form the Go encoder picks for it; the Go tests
    # (project_secret_encoding_test.go) assert that choice, so the two sides
    # stay in step.
    CASES = {
        "a\\b": "quoted",  # one interior backslash: quotes are enough
        "a\\\\b": "raw",  # a backslash pair, collapsed when quoted
        "C:\\Users\\": "raw",  # ends with a backslash
        "\\\\server\\share": "raw",  # a UNC path, opened by a backslash pair
        "trailing\\": "raw",  # ends with a backslash
        "x\\": "raw",  # ends with a backslash
        "a\\=b": "quoted",
        "9a8b7c": "quoted",
    }

    def _values(self, body):
        with patch.dict(os.environ, {}, clear=True):
            return dotenv_values(stream=io.StringIO(body))

    def test_values_between_quoted_values_round_trip(self):
        for value, form in self.CASES.items():
            with self.subTest(value=value, form=form):
                literal = f"'{value}'" if form == "quoted" else value
                body = f"K_BEFORE='before'\nK_TARGET={literal}\nK_AFTER='after'\n"
                values = self._values(body)
                self.assertEqual(values["K_BEFORE"], "before")
                self.assertEqual(values["K_TARGET"], value)
                self.assertEqual(values["K_AFTER"], "after")

    # Non-ASCII is not a reason to refuse a raw value: python-dotenv reads an
    # unquoted value as bytes-to-text without decoding escapes, so a path with
    # an accent, a Greek letter or a CJK character arrives whole.
    NON_ASCII_CASES = {
        "C:\\Répertoire\\": "raw",
        "naïve\\": "raw",
        "Ω\\": "raw",
        "秘密\\": "raw",
        "🚀\\": "raw",
        "C:\\Données\\rép-🚀\\": "raw",
    }

    def test_raw_non_ascii_values_round_trip(self):
        for value, form in self.NON_ASCII_CASES.items():
            with self.subTest(value=value, form=form):
                literal = value if form == "raw" else f"'{value}'"
                body = f"K_BEFORE='before'\nK_TARGET={literal}\nK_AFTER='after'\n"
                values = self._values(body)
                self.assertEqual(values["K_BEFORE"], "before")
                self.assertEqual(values["K_TARGET"], value)
                self.assertEqual(values["K_AFTER"], "after")

    # Quotes and backticks are positional, not forbidden. These are the values
    # the encoder delivers, each read back through the real loader; the Go side
    # asserts the same set and the chosen form (project_secret_encoding_test.go,
    # positional quotes and backticks). A single quote cannot be escaped in a
    # quoted value and a backslash pair collapses in one, so those shapes are
    # written raw.
    POSITIONAL_RAW = {
        "it's",
        "it's a\\\\b",
        "C:\\Users\\O'Brien\\",
        'a"b\\\\',
        "a`b\\\\",
    }

    POSITIONAL_QUOTED = {
        'a"b',
        "`ab`",
        "`abc",
        "``",
        "before#after",
        "value # comment",
    }

    def test_raw_interior_and_trailing_quotes_round_trip(self):
        for value in sorted(self.POSITIONAL_RAW):
            with self.subTest(value=value):
                body = f"K_BEFORE='before'\nK_TARGET={value}\nK_AFTER='after'\n"
                values = self._values(body)
                self.assertEqual(values["K_BEFORE"], "before")
                self.assertEqual(values["K_TARGET"], value)
                self.assertEqual(values["K_AFTER"], "after")

    def test_quoted_interior_quotes_and_backticks_round_trip(self):
        for value in sorted(self.POSITIONAL_QUOTED):
            with self.subTest(value=value):
                body = f"K_BEFORE='before'\nK_TARGET='{value}'\nK_AFTER='after'\n"
                values = self._values(body)
                self.assertEqual(values["K_BEFORE"], "before")
                self.assertEqual(values["K_TARGET"], value)
                self.assertEqual(values["K_AFTER"], "after")

    def test_a_wrapping_backtick_pair_keeps_its_backticks(self):
        # The other half of the refusal: python-dotenv keeps a wrapping backtick
        # pair that plain Node dotenv strips from an unquoted value, so no form
        # is exact in every loader. This is why the encoder refuses it.
        self.assertEqual(self._values("K_TARGET=`a'b`\n")["K_TARGET"], "`a'b`")
        self.assertEqual(self._values("K_TARGET=`a\\\\b`\n")["K_TARGET"], "`a\\\\b`")

    def _values_from_file(self, content):
        """Read through a real file, which is how the guest application reads.

        This matters: python-dotenv opens the file in text mode and Python's
        universal newlines translate CR and CRLF to LF at the read, before the
        parser sees anything. dotenv_values(stream=...) skips that translation,
        so a stream-based test would report CR as preserved while the shipping
        path normalises it.
        """
        import tempfile

        with tempfile.TemporaryDirectory() as d:
            path = os.path.join(d, ".env")
            with open(path, "wb") as f:
                f.write(content)
            with patch.dict(os.environ, {}, clear=True):
                return dotenv_values(path)

    def test_a_carriage_return_is_normalised_when_quoted(self):
        # The reason the encoder refuses a CR outright: read through a file,
        # python-dotenv sees a newline where the CR was, so a secret pasted
        # from a Windows source would be altered rather than delivered.
        self.assertEqual(self._values_from_file(b"K='a\rb'\n")["K"], "a\nb")
        self.assertEqual(self._values_from_file(b"K='line1\r\nline2'\n")["K"], "line1\nline2")

    def test_a_raw_newline_ends_the_value(self):
        # A raw newline cannot be carried at all: the value stops at it and
        # the next line becomes a key of its own, which is why a multiline
        # value is refused rather than written raw.
        values = self._values_from_file(b"K_TARGET=a\nb\nK_AFTER='after'\n")
        self.assertEqual(values["K_TARGET"], "a")
        self.assertIn("b", values)
        self.assertEqual(values["K_AFTER"], "after")

    def test_invalid_utf8_loses_the_whole_file(self):
        # The reason the encoder refuses invalid UTF-8 in both forms: the
        # loader raises on the undecodable byte and every other secret in the
        # file is lost with it.
        with self.assertRaises(UnicodeDecodeError):
            self._values_from_file(b"K_BEFORE='before'\nK_TARGET=ab\xff\nK_AFTER='after'\n")

    def test_a_raw_trailing_backslash_at_end_of_file_is_kept(self):
        self.assertEqual(self._values("K_TARGET=trailing\\")["K_TARGET"], "trailing\\")

    def test_quoting_loses_exactly_what_raw_preserves(self):
        # The reason the fallback exists: python-dotenv collapses a quoted
        # backslash pair to one, while the raw form is exact. A pair such as
        # a\\b is read back as a\b when quoted and as a\\b when raw.
        quoted = self._values("K_TARGET='a\\\\b'\n")["K_TARGET"]
        raw = self._values("K_TARGET=a\\\\b\n")["K_TARGET"]
        self.assertEqual(quoted, "a\\b")
        self.assertEqual(raw, "a\\\\b")


if __name__ == "__main__":
    # Last in the file on purpose: unittest.main() collects the tests defined
    # above it and runs them, so anything after it would never be registered.
    # This is how the README documents running this file.
    unittest.main()
