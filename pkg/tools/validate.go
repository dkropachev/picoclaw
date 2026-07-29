package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// validateToolArgs validates args against a JSON Schema-like map.
// schema is expected to have optional keys: "properties", "required", "additionalProperties".
func validateToolArgs(schema map[string]any, args map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	if args == nil {
		args = map[string]any{}
	}

	if err := checkRequired(schema, args); err != nil {
		return err
	}

	propsRaw, ok := schema["properties"]
	if !ok {
		return nil // no properties defined — accept any args
	}

	props, ok := propsRaw.(map[string]any)
	if !ok {
		return nil
	}

	additional := allowsAdditional(schema)

	for key, val := range args {
		propSchemaRaw, known := props[key]
		if !known {
			if !additional {
				return fmt.Errorf("unexpected property %q", key)
			}
			continue
		}
		propSchema, ok := propSchemaRaw.(map[string]any)
		if !ok {
			continue // can't validate without a proper schema map
		}
		if err := checkType(key, val, propSchema); err != nil {
			return err
		}
	}

	return nil
}

// checkRequired verifies that every field listed in schema["required"] is present in args.
func checkRequired(schema map[string]any, args map[string]any) error {
	reqRaw, ok := schema["required"]
	if !ok {
		return nil
	}

	var required []string

	switch r := reqRaw.(type) {
	case []string:
		required = r
	case []any:
		for _, v := range r {
			s, ok := v.(string)
			if ok {
				required = append(required, s)
			}
		}
	default:
		return nil
	}

	for _, field := range required {
		if _, present := args[field]; !present {
			return fmt.Errorf("missing required property %q", field)
		}
	}
	return nil
}

// allowsAdditional returns true when the schema explicitly sets
// "additionalProperties" to true, or when the key is absent (default: reject extras).
func allowsAdditional(schema map[string]any) bool {
	v, ok := schema["additionalProperties"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// checkType validates that val matches the JSON Schema type declared in propSchema.
func checkType(key string, val any, propSchema map[string]any) error {
	typeRaw, ok := propSchema["type"]
	if !ok {
		return nil // no type constraint
	}
	typeName, ok := typeRaw.(string)
	if !ok {
		return nil
	}

	switch typeName {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("property %q: expected string, got %T", key, val)
		}
	case "integer":
		switch v := val.(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
				return fmt.Errorf("property %q: expected integer, got float64 with fractional part", key)
			}
		case int:
			// ok
		case int64:
			// ok
		case json.Number:
			if !validJSONNumber(v) || !jsonNumberIsInteger(v) {
				return fmt.Errorf("property %q: expected integer, got json.Number with fractional part", key)
			}
		default:
			return fmt.Errorf("property %q: expected integer, got %T", key, val)
		}
	case "number":
		switch v := val.(type) {
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("property %q: expected finite number, got %T", key, val)
			}
		case json.Number:
			if !validJSONNumber(v) {
				return fmt.Errorf("property %q: expected finite number, got invalid json.Number", key)
			}
		case int, int64:
			// ok
		default:
			return fmt.Errorf("property %q: expected number, got %T", key, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("property %q: expected boolean, got %T", key, val)
		}
	case "array":
		arr, ok := val.([]any)
		if !ok {
			return fmt.Errorf("property %q: expected array, got %T", key, val)
		}
		if err := checkArrayItems(key, arr, propSchema); err != nil {
			return err
		}
	case "object":
		obj, ok := val.(map[string]any)
		if !ok {
			return fmt.Errorf("property %q: expected object, got %T", key, val)
		}
		if err := validateToolArgs(propSchema, obj); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}

	if err := checkEnum(key, val, propSchema); err != nil {
		return err
	}

	return nil
}

// checkArrayItems validates each element of arr against the "items" sub-schema.
func checkArrayItems(key string, arr []any, propSchema map[string]any) error {
	itemsRaw, ok := propSchema["items"]
	if !ok {
		return nil
	}
	itemSchema, ok := itemsRaw.(map[string]any)
	if !ok {
		return nil
	}
	for i, elem := range arr {
		elemKey := fmt.Sprintf("%s[%d]", key, i)
		if err := checkType(elemKey, elem, itemSchema); err != nil {
			return err
		}
	}
	return nil
}

func validJSONNumber(value json.Number) bool {
	_, err := json.Marshal(value)
	return err == nil
}

func jsonNumberIsInteger(value json.Number) bool {
	raw := value.String()
	mantissa := raw
	exponent := ""
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa = raw[:index]
		exponent = raw[index+1:]
	}
	mantissa = strings.TrimPrefix(mantissa, "-")

	fractionDigits := 0
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		fractionDigits = len(mantissa) - point - 1
	}

	trailingZeros := 0
	allZero := true
	for index := len(mantissa) - 1; index >= 0; index-- {
		digit := mantissa[index]
		if digit == '.' {
			continue
		}
		if digit != '0' {
			allZero = false
			break
		}
		trailingZeros++
	}
	if allZero {
		return true
	}

	// The number is an integer exactly when:
	//
	//	significand * 10^(exponent-fractionDigits)
	//
	// has enough trailing zeroes to cancel a negative decimal scale. Compare
	// the exponent against that threshold with saturation bounded by the input
	// length; parsing an attacker-controlled exponent must never construct
	// 10^exponent or another exponent-sized value.
	threshold := fractionDigits - trailingZeros
	return jsonExponentAtLeast(exponent, threshold, len(raw)+1)
}

func jsonExponentAtLeast(raw string, threshold, bound int) bool {
	if raw == "" {
		return 0 >= threshold
	}

	sign := 1
	switch raw[0] {
	case '-':
		sign = -1
		raw = raw[1:]
	case '+':
		raw = raw[1:]
	}

	magnitude := 0
	for index := 0; index < len(raw); index++ {
		digit := int(raw[index] - '0')
		if magnitude > (bound-digit)/10 {
			magnitude = bound
			break
		}
		magnitude = magnitude*10 + digit
		if magnitude >= bound {
			magnitude = bound
			break
		}
	}
	return sign*magnitude >= threshold
}

// checkEnum validates that val is one of the allowed enum values in propSchema.
func checkEnum(key string, val any, propSchema map[string]any) error {
	enumRaw, ok := propSchema["enum"]
	if !ok {
		return nil
	}

	switch ev := enumRaw.(type) {
	case []any:
		for _, allowed := range ev {
			if val == allowed {
				return nil
			}
		}
	case []string:
		s, ok := val.(string)
		if ok {
			for _, allowed := range ev {
				if s == allowed {
					return nil
				}
			}
		}
	default:
		return nil // unknown enum format, skip
	}

	return fmt.Errorf("property %q: value %v is not in enum", key, val)
}
