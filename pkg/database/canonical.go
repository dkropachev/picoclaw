package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var ErrNonCanonicalJSON = errors.New("database protocol JSON is not canonical")

// MarshalCanonical encodes value as compact JSON with lexically sorted object
// keys and stable number spellings. The same representation is required on
// every protocol and discovery boundary.
func MarshalCanonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal database protocol JSON: %w", err)
	}
	canonical, err := canonicalizeJSON(raw)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// UnmarshalCanonical rejects whitespace, alternate key ordering, duplicate
// object members, trailing data, and every other noncanonical representation
// before decoding into destination.
func UnmarshalCanonical(raw []byte, destination any) error {
	canonical, err := canonicalizeJSON(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return ErrNonCanonicalJSON
	}
	if destination == nil {
		return NewError(CodeInvalid, "canonical JSON destination is nil")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode database protocol JSON: %w", err)
	}
	return nil
}

func unmarshalCanonicalStrict(raw []byte, destination any) error {
	canonical, err := canonicalizeJSON(raw)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return ErrNonCanonicalJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode database protocol JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func canonicalizeJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode database protocol JSON: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	value, err := normalizeCanonicalNumbers(value)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize database protocol JSON: %w", err)
	}
	return canonical, nil
}

type canonicalNumber string

func (number canonicalNumber) MarshalJSON() ([]byte, error) {
	if number == "" {
		return nil, errors.New("database protocol JSON number is invalid")
	}
	return []byte(number), nil
}

func normalizeCanonicalNumbers(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		normalized, err := normalizeCanonicalNumber(value.String())
		if err != nil {
			return nil, err
		}
		return canonicalNumber(normalized), nil
	case []any:
		for index := range value {
			normalized, err := normalizeCanonicalNumbers(value[index])
			if err != nil {
				return nil, err
			}
			value[index] = normalized
		}
		return value, nil
	case map[string]any:
		for key, item := range value {
			normalized, err := normalizeCanonicalNumbers(item)
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}

func normalizeCanonicalNumber(raw string) (string, error) {
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	mantissa, exponentText, hasExponent := strings.Cut(raw, "e")
	if !hasExponent {
		mantissa, exponentText, hasExponent = strings.Cut(raw, "E")
	}
	exponent := int64(0)
	if hasExponent {
		parsed, err := strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return "", errors.New("database protocol JSON number exponent is invalid")
		}
		exponent = parsed
	}
	integer, fraction, hasFraction := strings.Cut(mantissa, ".")
	if integer == "" || hasFraction && fraction == "" {
		return "", errors.New("database protocol JSON number is invalid")
	}
	digits := integer + fraction
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	decimalExponent := exponent - int64(len(fraction))
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		decimalExponent++
	}
	scientificExponent := decimalExponent + int64(len(digits)) - 1
	var result string
	if scientificExponent >= -6 && scientificExponent < 21 {
		decimalPosition := int64(len(digits)) + decimalExponent
		switch {
		case decimalPosition <= 0:
			result = "0." + strings.Repeat("0", int(-decimalPosition)) + digits
		case decimalPosition >= int64(len(digits)):
			result = digits + strings.Repeat("0", int(decimalPosition-int64(len(digits))))
		default:
			result = digits[:decimalPosition] + "." + digits[decimalPosition:]
		}
	} else {
		result = digits[:1]
		if len(digits) > 1 {
			result += "." + digits[1:]
		}
		result += "e" + strconv.FormatInt(scientificExponent, 10)
	}
	if negative {
		result = "-" + result
	}
	return result, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("database protocol JSON contains trailing data")
		}
		return fmt.Errorf("decode trailing database protocol JSON: %w", err)
	}
	return nil
}
