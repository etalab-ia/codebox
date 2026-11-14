package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// readStrictJSON reads a request body and proves it is well-formed JSON before
// any field is decoded into a Go string. encoding/json silently rewrites an
// unpaired UTF-16 surrogate to U+FFFD and a JSON null to the zero value, so
// validating after decoding would accept altered credentials instead of
// rejecting the request. The checks here mirror `secrets.UnmarshalBundle`,
// which guards the stored payload the same way.
func readStrictJSON(r *http.Request, limit int64) ([]byte, error) {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		return nil, errors.New("request body must be JSON")
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, errors.New("request body could not be read")
	}
	if int64(len(data)) > limit {
		return nil, errors.New("request body is too large")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("request body is not valid UTF-8")
	}
	if hasUnpairedSurrogate(data) {
		return nil, errors.New("request body contains an unpaired UTF-16 surrogate escape")
	}
	if err := rejectJSONNulls(data); err != nil {
		return nil, err
	}
	return data, nil
}

// hasUnpairedSurrogate scans JSON string literals for surrogate escapes that do
// not form a valid pair, which json would otherwise replace with U+FFFD.
func hasUnpairedSurrogate(data []byte) bool {
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
		switch {
		case c == '"':
			inString = false
			i++
		case c != '\\':
			i++
		case i+1 >= len(data):
			return false
		case data[i+1] != 'u':
			i += 2
		case i+6 > len(data):
			return false
		default:
			u1, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
			if err != nil {
				return false
			}
			if u1 >= 0xDC00 && u1 <= 0xDFFF {
				return true // lone low surrogate
			}
			if u1 >= 0xD800 && u1 <= 0xDBFF {
				if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
					return true // lone high surrogate
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
	}
	return false
}

// rejectJSONNulls walks the document and refuses any null token. Every request
// payload the gateway accepts has non-nullable fields, so a null can only be a
// malformed or malicious request, never a meaningful value.
func rejectJSONNulls(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return walkJSON(dec, 0)
}

func walkJSON(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("request body is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return errors.New("request body is not a valid JSON payload")
	}
	if tok == nil {
		return errors.New("request body must not contain null values")
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return errors.New("request body is not a valid JSON payload")
			}
			if _, isString := key.(string); !isString {
				return errors.New("request body has a non-string object key")
			}
			if err := walkJSON(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkJSON(dec, depth+1); err != nil {
				return err
			}
		}
	}
	// Consume the closing delimiter.
	if _, err := dec.Token(); err != nil {
		return errors.New("request body is not a valid JSON payload")
	}
	return nil
}

// decodeStrictJSON proves the body is lossless JSON and then decodes it,
// rejecting unknown fields and trailing data. Nothing downstream can therefore
// observe a coerced surrogate or a null turned into an empty string.
func decodeStrictJSON(r *http.Request, dst any, limit int64) error {
	data, err := readStrictJSON(r, limit)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("request body does not match the expected shape: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body contains trailing data")
	}
	return nil
}
