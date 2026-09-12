package modelevaluation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }
func hashJSON(raw []byte) string  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// CanonicalConfig sorts object keys and normalizes equivalent decimal numbers
// without rounding large integer values through float64.
func CanonicalConfig(raw json.RawMessage) (json.RawMessage, string, error) {
	value, err := strictJSON(raw, MaxConfigBytes)
	if err != nil {
		return nil, "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, "", invalid("config must be an object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", invalid("config cannot be encoded")
	}
	return canonical, hashJSON(canonical), nil
}
func strictJSON(raw []byte, maxBytes int) (any, error) {
	if len(raw) == 0 || len(raw) > maxBytes || !utf8.Valid(raw) {
		return nil, invalid("JSON size or encoding is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("JSON must contain exactly one value")
	}
	return value, nil
}
func readJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, invalid("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, invalid("malformed JSON")
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, invalid("malformed object")
				}
				key, ok := keyToken.(string)
				if !ok || len(key) > 256 || len(object) >= 2048 {
					return nil, invalid("object keys exceed limits")
				}
				if _, exists := object[key]; exists {
					return nil, invalid("duplicate object key")
				}
				value, err := readJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
				return nil, invalid("malformed object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				if len(array) >= 4096 {
					return nil, invalid("array exceeds limit")
				}
				value, err := readJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
				return nil, invalid("malformed array")
			}
			return array, nil
		default:
			return nil, invalid("unexpected JSON delimiter")
		}
	}
	if number, ok := token.(json.Number); ok {
		return canonicalNumber(number)
	}
	return token, nil
}
func canonicalNumber(number json.Number) (json.Number, error) {
	text := number.String()
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return "", invalid("number must be finite")
	}
	negative := strings.HasPrefix(text, "-")
	text = strings.TrimPrefix(text, "-")
	mantissa, expText, hasExponent := strings.Cut(strings.ToLower(text), "e")
	exponent := 0
	if hasExponent {
		value, err := strconv.Atoi(expText)
		if err != nil || value < -10000 || value > 10000 {
			return "", invalid("number exponent exceeds limit")
		}
		exponent = value
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		exponent -= len(mantissa) - dot - 1
		mantissa = strings.ReplaceAll(mantissa, ".", "")
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return json.Number("0"), nil
	}
	if parsed == 0 {
		return "", invalid("number underflows finite precision")
	}
	trimmed := strings.TrimRight(mantissa, "0")
	exponent += len(mantissa) - len(trimmed)
	mantissa = trimmed
	if negative {
		mantissa = "-" + mantissa
	}
	if exponent != 0 {
		mantissa += "e" + strconv.Itoa(exponent)
	}
	return json.Number(mantissa), nil
}
