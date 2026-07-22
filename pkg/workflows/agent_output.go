package workflows

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type AgentOutputContract struct {
	Format         string         `json:"format,omitempty"`
	Schema         map[string]any `json:"schema,omitempty"`
	RepairAttempts int            `json:"repair_attempts,omitempty"`
}

type StructuredOutputResult struct {
	Structured any    `json:"structured,omitempty"`
	RawJSON    string `json:"raw_json,omitempty"`
	Valid      bool   `json:"valid"`
	Error      string `json:"error,omitempty"`
}

func ParseAgentOutputContract(raw any) (*AgentOutputContract, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		format := strings.ToLower(strings.TrimSpace(v))
		if format == "" {
			return nil, nil
		}
		if format != "json" {
			return nil, fmt.Errorf("unsupported agent output format %q", v)
		}
		return &AgentOutputContract{Format: format, RepairAttempts: 1}, nil
	case map[string]any:
		format := strings.ToLower(strings.TrimSpace(nativeString(v, "format")))
		if format == "" {
			if _, ok := v["schema"]; ok {
				format = "json"
			}
		}
		if format == "" {
			return nil, nil
		}
		if format != "json" {
			return nil, fmt.Errorf("unsupported agent output format %q", format)
		}
		schema, err := normalizeSchemaMap(v["schema"])
		if err != nil {
			return nil, fmt.Errorf("invalid agent output schema: %w", err)
		}
		repairAttempts := nativeInt(v, "repair_attempts", 0)
		if repairAttempts == 0 {
			repairAttempts = nativeInt(v, "repairAttempts", 0)
		}
		if repairAttempts < 0 {
			repairAttempts = 0
		}
		if repairAttempts == 0 {
			repairAttempts = 1
		}
		return &AgentOutputContract{
			Format:         format,
			Schema:         schema,
			RepairAttempts: repairAttempts,
		}, nil
	default:
		return nil, fmt.Errorf("agent output must be a string or map")
	}
}

func (c *AgentOutputContract) Enabled() bool {
	return c != nil && strings.EqualFold(strings.TrimSpace(c.Format), "json")
}

func (c *AgentOutputContract) Instruction() string {
	if !c.Enabled() {
		return ""
	}
	var b strings.Builder
	b.WriteString("Structured output contract:\n")
	b.WriteString("- Return only valid JSON.\n")
	b.WriteString("- Do not wrap the JSON in markdown.\n")
	b.WriteString("- Do not include prose before or after the JSON.\n")
	if len(c.Schema) > 0 {
		b.WriteString("- The JSON must match this schema:\n")
		data, err := json.MarshalIndent(c.Schema, "", "  ")
		if err == nil {
			b.Write(data)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func ValidateAgentStructuredOutput(text string, contract *AgentOutputContract) StructuredOutputResult {
	if !contract.Enabled() {
		return StructuredOutputResult{Valid: true}
	}
	raw, parsed, err := ExtractJSONValue(text)
	if err != nil {
		return StructuredOutputResult{Valid: false, Error: err.Error()}
	}
	if len(contract.Schema) > 0 {
		if err := validateJSONSchemaValue(parsed, contract.Schema, "$"); err != nil {
			return StructuredOutputResult{
				Structured: parsed,
				RawJSON:    raw,
				Valid:      false,
				Error:      err.Error(),
			}
		}
	}
	return StructuredOutputResult{Structured: parsed, RawJSON: raw, Valid: true}
}

func ExtractJSONValue(text string) (string, any, error) {
	candidates := jsonCandidates(text)
	var lastErr error
	for _, candidate := range candidates {
		var parsed any
		decoder := json.NewDecoder(strings.NewReader(candidate))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err != nil {
			lastErr = err
			continue
		}
		if decoder.More() {
			lastErr = fmt.Errorf("trailing JSON content")
			continue
		}
		normalized, err := json.Marshal(parsed)
		if err != nil {
			return candidate, parsed, fmt.Errorf("normalize JSON response: %w", err)
		}
		return string(normalized), parsed, nil
	}
	if lastErr != nil {
		return "", nil, fmt.Errorf("response did not contain valid JSON: %w", lastErr)
	}
	return "", nil, fmt.Errorf("response did not contain JSON")
}

func CombineStructuredOutputs(values []any, schema map[string]any) any {
	if len(values) == 0 {
		return map[string]any{}
	}
	if len(values) == 1 {
		return values[0]
	}
	if schemaType(schema) == "object" {
		return combineStructuredObjects(values, schema)
	}
	return map[string]any{"results": values}
}

// MergeStructuredOutputs is kept for compatibility. New code should use
// CombineStructuredOutputs.
func MergeStructuredOutputs(values []any, schema map[string]any) any {
	return CombineStructuredOutputs(values, schema)
}

func CompareStructuredOutputs(left, right any) map[string]any {
	leftNorm := canonicalJSON(left)
	rightNorm := canonicalJSON(right)
	if leftNorm == rightNorm {
		return map[string]any{
			"match":  true,
			"method": "canonical_json",
		}
	}
	leftArrays := comparableArrayFields(left)
	rightArrays := comparableArrayFields(right)
	if len(leftArrays) > 0 && reflect.DeepEqual(leftArrays, rightArrays) {
		return map[string]any{
			"match":  true,
			"method": "array_fields",
		}
	}
	leftIdentities := comparableObjectArrayIdentities(left)
	rightIdentities := comparableObjectArrayIdentities(right)
	if len(leftIdentities) > 0 && reflect.DeepEqual(leftIdentities, rightIdentities) {
		return map[string]any{
			"match":  true,
			"method": "object_array_identities",
		}
	}
	return map[string]any{
		"match":  false,
		"method": "canonical_json",
		"left":   leftNorm,
		"right":  rightNorm,
	}
}

func EstimateAgentPayloadTokens(value any) int {
	if value == nil {
		return 0
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	default:
		data, err := json.Marshal(v)
		if err != nil {
			text = fmt.Sprint(v)
		} else {
			text = string(data)
		}
	}
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

func comparableObjectArrayIdentities(value any) map[string][]string {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string)
	for key, raw := range object {
		items, ok := raw.([]any)
		if !ok || len(items) == 0 {
			continue
		}
		signatures := make([]string, 0, len(items))
		for _, item := range items {
			mapped, ok := item.(map[string]any)
			if !ok {
				signatures = nil
				break
			}
			signature := objectIdentitySignature(mapped)
			if signature == "" {
				signatures = nil
				break
			}
			signatures = append(signatures, signature)
		}
		if len(signatures) == len(items) {
			sort.Strings(signatures)
			out[key] = signatures
		}
	}
	return out
}

func objectIdentitySignature(value map[string]any) string {
	for _, keys := range [][]string{
		{"id"},
		{"key"},
		{"scope_id", "task"},
		{"scope_id"},
		{"file", "line", "severity"},
		{"path", "line", "severity"},
		{"file", "line"},
		{"path", "line"},
		{"file", "severity", "title"},
		{"path", "severity", "title"},
	} {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			part := identityScalar(value[key])
			if part == "" {
				parts = nil
				break
			}
			parts = append(parts, key+"="+part)
		}
		if len(parts) == len(keys) {
			return strings.Join(parts, "|")
		}
	}
	return ""
}

func identityScalar(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case bool:
		return fmt.Sprint(v)
	case float64:
		return fmt.Sprint(v)
	case json.Number:
		return v.String()
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func normalizeSchemaMap(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	if m, ok := raw.(map[string]any); ok {
		return m, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func jsonCandidates(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	candidates := []string{text}
	candidates = append(candidates, fencedJSONCandidates(text)...)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 {
			end := -1
			for i := len(lines) - 1; i >= 1; i-- {
				if strings.TrimSpace(lines[i]) == "```" {
					end = i
					break
				}
			}
			if end > 1 {
				bodyLines := append([]string(nil), lines[1:end]...)
				if len(bodyLines) > 0 {
					first := strings.ToLower(strings.TrimSpace(bodyLines[0]))
					if first == "json" || first == "javascript" || first == "js" {
						bodyLines = bodyLines[1:]
					}
				}
				candidates = append(candidates, strings.TrimSpace(strings.Join(bodyLines, "\n")))
			}
		}
	}
	for _, pair := range [][2]byte{{'{', '}'}, {'[', ']'}} {
		start := strings.IndexByte(text, pair[0])
		end := strings.LastIndexByte(text, pair[1])
		if start >= 0 && end > start {
			candidates = append(candidates, strings.TrimSpace(text[start:end+1]))
		}
	}
	return uniqueStrings(candidates)
}

func fencedJSONCandidates(text string) []string {
	var out []string
	remaining := text
	for {
		start := strings.Index(remaining, "```")
		if start < 0 {
			break
		}
		afterStart := remaining[start+3:]
		lineEnd := strings.Index(afterStart, "\n")
		if lineEnd < 0 {
			break
		}
		label := strings.ToLower(strings.TrimSpace(afterStart[:lineEnd]))
		bodyAndRest := afterStart[lineEnd+1:]
		end := strings.Index(bodyAndRest, "```")
		if end < 0 {
			break
		}
		body := strings.TrimSpace(bodyAndRest[:end])
		if label == "" || label == "json" || label == "javascript" || label == "js" {
			out = append(out, body)
		}
		remaining = bodyAndRest[end+3:]
	}
	return out
}

func validateJSONSchemaValue(value any, schema map[string]any, path string) error {
	if len(schema) == 0 {
		return nil
	}
	if expected := schemaType(schema); expected != "" {
		if !schemaTypeMatches(value, expected) {
			return fmt.Errorf("%s must be %s", path, expected)
		}
	}
	if enumValues, ok := schema["enum"]; ok {
		if !schemaEnumContains(enumValues, value) {
			return fmt.Errorf("%s must be one of %v", path, enumValues)
		}
	}
	switch schemaType(schema) {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		for _, key := range schemaRequired(schema) {
			if _, ok := obj[key]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		props := schemaProperties(schema)
		for key, child := range props {
			if item, ok := obj[key]; ok {
				if err := validateJSONSchemaValue(item, child, path+"."+key); err != nil {
					return err
				}
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		if itemSchema := schemaItems(schema); len(itemSchema) > 0 {
			for i, item := range arr {
				if err := validateJSONSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func schemaType(schema map[string]any) string {
	raw, ok := schema["type"]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.ToLower(strings.TrimSpace(s))
				if s != "null" {
					return s
				}
			}
		}
	}
	return ""
}

func schemaTypeMatches(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		switch v := value.(type) {
		case json.Number:
			_, err := v.Int64()
			return err == nil
		case int, int64:
			return true
		case float64:
			return v == float64(int64(v))
		default:
			return false
		}
	case "number":
		switch value.(type) {
		case json.Number, int, int64, float64:
			return true
		default:
			return false
		}
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func schemaEnumContains(raw any, value any) bool {
	items, ok := raw.([]any)
	if !ok {
		return true
	}
	valueJSON := canonicalJSON(value)
	for _, item := range items {
		if canonicalJSON(item) == valueJSON {
			return true
		}
	}
	return false
}

func schemaRequired(schema map[string]any) []string {
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); strings.TrimSpace(s) != "" && ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func schemaProperties(schema map[string]any) map[string]map[string]any {
	raw, ok := schema["properties"]
	if !ok {
		return nil
	}
	props, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]map[string]any, len(props))
	for key, value := range props {
		if child, err := normalizeSchemaMap(value); err == nil && child != nil {
			out[key] = child
		}
	}
	return out
}

func schemaItems(schema map[string]any) map[string]any {
	child, err := normalizeSchemaMap(schema["items"])
	if err != nil {
		return nil
	}
	return child
}

func combineStructuredObjects(values []any, schema map[string]any) map[string]any {
	props := schemaProperties(schema)
	result := map[string]any{}
	keys := make([]string, 0, len(props))
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		childSchema := props[key]
		switch schemaType(childSchema) {
		case "array":
			var combined []any
			for _, value := range values {
				obj, ok := value.(map[string]any)
				if !ok {
					continue
				}
				arr, ok := obj[key].([]any)
				if !ok {
					continue
				}
				combined = append(combined, arr...)
			}
			result[key] = dedupeAnySlice(combined)
		case "string":
			parts := make([]string, 0, len(values))
			for _, value := range values {
				obj, ok := value.(map[string]any)
				if !ok {
					continue
				}
				text := strings.TrimSpace(fmt.Sprint(obj[key]))
				if text != "" && text != "<nil>" {
					parts = append(parts, text)
				}
			}
			result[key] = strings.Join(uniqueStrings(parts), "\n")
		default:
			for _, value := range values {
				obj, ok := value.(map[string]any)
				if !ok {
					continue
				}
				if item, ok := obj[key]; ok {
					result[key] = item
				}
			}
		}
	}
	for _, value := range values {
		obj, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for key, item := range obj {
			if _, exists := result[key]; !exists {
				result[key] = item
			}
		}
	}
	return result
}

func comparableArrayFields(value any) map[string][]string {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for key, item := range obj {
		arr, ok := item.([]any)
		if !ok {
			continue
		}
		values := make([]string, 0, len(arr))
		for _, elem := range arr {
			values = append(values, canonicalJSON(elem))
		}
		sort.Strings(values)
		out[key] = values
	}
	return out
}

func dedupeAnySlice(values []any) []any {
	seen := map[string]struct{}{}
	out := make([]any, 0, len(values))
	for _, value := range values {
		key := canonicalJSON(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func canonicalJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, data); err != nil {
		return string(data)
	}
	return compacted.String()
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
