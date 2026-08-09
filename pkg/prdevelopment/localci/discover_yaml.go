package localci

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const maximumYAMLNodes = 100_000

func decodeStrictYAML(raw []byte) (*yaml.Node, error) {
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("YAML must be NUL-free UTF-8")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("YAML contains multiple documents")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("YAML root must be a mapping")
	}
	nodes := 0
	if err := validateYAMLNode(document.Content[0], 0, &nodes); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

func validateYAMLNode(node *yaml.Node, depth int, nodes *int) error {
	if node == nil || depth > 64 || *nodes > maximumYAMLNodes {
		return errors.New("YAML complexity limit exceeded")
	}
	(*nodes)++
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are unsupported")
	}
	if node.Tag != "" && node.Tag != "!!map" && node.Tag != "!!seq" &&
		node.Tag != "!!str" && node.Tag != "!!int" && node.Tag != "!!bool" &&
		node.Tag != "!!null" {
		return fmt.Errorf("unsupported YAML tag %q", node.Tag)
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return errors.New("YAML mapping is malformed")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" {
				return errors.New("YAML mapping key must be a plain string")
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("YAML contains duplicate key %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child, depth+1, nodes); err != nil {
			return err
		}
	}
	return nil
}

func yamlMapping(node *yaml.Node) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("YAML value must be a mapping")
	}
	values := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		values[node.Content[index].Value] = node.Content[index+1]
	}
	return values, nil
}

func yamlString(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", errors.New("YAML value must be a string")
	}
	return node.Value, nil
}

func yamlOptionalString(values map[string]*yaml.Node, key string) (string, error) {
	node, exists := values[key]
	if !exists {
		return "", nil
	}
	return yamlString(node)
}

func yamlBool(node *yaml.Node) (bool, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return false, errors.New("YAML value must be a boolean")
	}
	return strconv.ParseBool(node.Value)
}

func yamlInteger(node *yaml.Node) (int64, error) {
	if node == nil || node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return 0, errors.New("YAML value must be an integer")
	}
	return strconv.ParseInt(node.Value, 10, 64)
}

func yamlStringSequence(node *yaml.Node) ([]string, error) {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil, errors.New("YAML value must be a sequence")
	}
	values := make([]string, 0, len(node.Content))
	for _, child := range node.Content {
		value, err := yamlString(child)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func yamlEnvironment(node *yaml.Node) ([]EnvironmentVariable, error) {
	if node == nil {
		return nil, nil
	}
	values, err := yamlMapping(node)
	if err != nil {
		return nil, err
	}
	result := make([]EnvironmentVariable, 0, len(values))
	for name, valueNode := range values {
		value, valueErr := yamlString(valueNode)
		if valueErr != nil {
			return nil, fmt.Errorf("environment %s: %w", name, valueErr)
		}
		if strings.Contains(value, "${{") {
			return nil, fmt.Errorf("environment %s contains a workflow expression", name)
		}
		result = append(result, EnvironmentVariable{Name: name, Value: value})
	}
	return result, nil
}

func rejectUnknownYAMLKeys(values map[string]*yaml.Node, allowed ...string) error {
	allowlist := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowlist[key] = struct{}{}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if _, allowedKey := allowlist[key]; !allowedKey {
			return fmt.Errorf("unsupported YAML key %q", key)
		}
	}
	return nil
}
