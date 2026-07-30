package api

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	capabilityModeAll      = "all"
	capabilityModeInherit  = "inherit"
	capabilityModeNone     = "none"
	capabilityModeSelected = "selected"

	agentCapabilityValuesLimit   = 1024
	agentCapabilityValueMaxBytes = 1024
	agentDefinitionSourceCurrent = "agent"
	agentDefinitionSourceLegacy  = "legacy"
	agentDefinitionSourceNone    = "missing"
	agentDefinitionFileCurrent   = "AGENT.md"
	agentDefinitionFileLegacy    = "AGENTS.md"
)

var errInvalidAgentFrontmatter = errors.New("invalid agent frontmatter")

type agentCapabilityPolicy struct {
	Mode            string   `json:"mode"`
	Values          []string `json:"values"`
	InheritedValues []string `json:"-"`
}

type agentCapabilities struct {
	Tools      agentCapabilityPolicy `json:"tools"`
	Skills     agentCapabilityPolicy `json:"skills"`
	MCPServers agentCapabilityPolicy `json:"mcp_servers"`
}

type agentCapabilitiesDocument struct {
	source           string
	path             string
	raw              []byte
	mode             uint32
	model            string
	hasFrontmatter   bool
	frontmatterStart int
	frontmatterEnd   int
	newline          string
	root             *yaml.Node
	capabilities     agentCapabilities
}

func parseAgentCapabilitiesDocument(
	source string,
	path string,
	file agentDefinitionFile,
	inheritedSkills []string,
) (agentCapabilitiesDocument, error) {
	document := agentCapabilitiesDocument{
		source: source,
		path:   path,
		raw:    append([]byte(nil), file.Data...),
		mode:   uint32(file.Mode),
		newline: func() string {
			if bytes.Contains(file.Data, []byte("\r\n")) {
				return "\r\n"
			}
			if bytes.Contains(file.Data, []byte("\r")) {
				return "\r"
			}
			return "\n"
		}(),
	}
	document.capabilities = defaultAgentCapabilities(inheritedSkills)
	if source != agentDefinitionSourceCurrent {
		return document, nil
	}

	frontmatter, start, end, ok := exactAgentFrontmatter(file.Data)
	if !ok {
		if startsAgentFrontmatter(file.Data) {
			return document, errInvalidAgentFrontmatter
		}
		return document, nil
	}
	document.hasFrontmatter = true
	document.frontmatterStart = start
	document.frontmatterEnd = end

	var root yaml.Node
	if err := yaml.Unmarshal(frontmatter, &root); err != nil {
		return document, errInvalidAgentFrontmatter
	}
	if len(root.Content) == 0 ||
		len(root.Content) == 1 && isYAMLNull(root.Content[0]) {
		replaceEmptyAgentFrontmatterWithMapping(&root, frontmatter)
	}
	mapping, ok := yamlDocumentMapping(&root)
	if !ok || mappingHasDuplicateKeys(mapping) ||
		!yamlAgentFrontmatterRoundTripSafe(&root) {
		return document, errInvalidAgentFrontmatter
	}
	runtimeFrontmatter, err := parseRuntimeAgentFrontmatter(frontmatter)
	if err != nil {
		return document, errInvalidAgentFrontmatter
	}
	document.model = strings.TrimSpace(runtimeFrontmatter.Model)
	document.root = &root

	tools, err := policyFromYAMLField(mapping, "tools", capabilityModeAll, true)
	if err != nil {
		return document, errInvalidAgentFrontmatter
	}
	skills, err := policyFromYAMLField(mapping, "skills", capabilityModeInherit, false)
	if err != nil {
		return document, errInvalidAgentFrontmatter
	}
	skills.InheritedValues = normalizedCapabilityValues(inheritedSkills, false)
	mcpServers, err := policyFromYAMLField(mapping, "mcpServers", capabilityModeAll, true)
	if err != nil {
		return document, errInvalidAgentFrontmatter
	}
	document.capabilities = agentCapabilities{
		Tools:      tools,
		Skills:     skills,
		MCPServers: mcpServers,
	}
	return document, nil
}

func defaultAgentCapabilities(inheritedSkills []string) agentCapabilities {
	return agentCapabilities{
		Tools: agentCapabilityPolicy{
			Mode:   capabilityModeAll,
			Values: []string{},
		},
		Skills: agentCapabilityPolicy{
			Mode:            capabilityModeInherit,
			Values:          []string{},
			InheritedValues: normalizedCapabilityValues(inheritedSkills, false),
		},
		MCPServers: agentCapabilityPolicy{
			Mode:   capabilityModeAll,
			Values: []string{},
		},
	}
}

func exactAgentFrontmatter(data []byte) ([]byte, int, int, bool) {
	first, next, ok := exactLine(data, 0)
	if !ok || string(first) != "---" {
		return nil, 0, 0, false
	}
	for offset := next; offset <= len(data); {
		line, following, lineOK := exactLine(data, offset)
		if !lineOK {
			break
		}
		if string(line) == "---" {
			return data[next:offset], next, offset, true
		}
		if following <= offset {
			break
		}
		offset = following
	}
	return nil, 0, 0, false
}

func startsAgentFrontmatter(data []byte) bool {
	first, _, ok := exactLine(data, 0)
	return ok && string(first) == "---"
}

func exactLine(data []byte, offset int) ([]byte, int, bool) {
	if offset < 0 || offset > len(data) {
		return nil, offset, false
	}
	if offset == len(data) {
		return nil, offset, false
	}
	for index := offset; index < len(data); index++ {
		switch data[index] {
		case '\n':
			return data[offset:index], index + 1, true
		case '\r':
			next := index + 1
			if next < len(data) && data[next] == '\n' {
				next++
			}
			return data[offset:index], next, true
		}
	}
	return data[offset:], len(data), true
}

func yamlDocumentMapping(root *yaml.Node) (*yaml.Node, bool) {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, false
	}
	mapping := root.Content[0]
	return mapping, mapping.Kind == yaml.MappingNode
}

func mappingHasDuplicateKeys(mapping *yaml.Node) bool {
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return true
		}
		if _, exists := seen[key.Value]; exists {
			return true
		}
		seen[key.Value] = struct{}{}
	}
	return len(mapping.Content)%2 != 0
}

func yamlAgentFrontmatterRoundTripSafe(root *yaml.Node) bool {
	var inspect func(*yaml.Node) bool
	inspect = func(node *yaml.Node) bool {
		if node == nil {
			return false
		}
		customTag := strings.HasPrefix(node.Tag, "!") &&
			!strings.HasPrefix(node.Tag, "!!")
		if node.Kind == yaml.AliasNode || node.Anchor != "" ||
			node.Tag == "!!merge" || customTag {
			return false
		}
		if node.Kind == yaml.MappingNode {
			if len(node.Content)%2 != 0 {
				return false
			}
			for index := 0; index < len(node.Content); index += 2 {
				key := node.Content[index]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" ||
					key.Value == "<<" {
					return false
				}
			}
		}
		for _, child := range node.Content {
			if !inspect(child) {
				return false
			}
		}
		return true
	}
	return inspect(root)
}

type runtimeAgentFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tools       []string `yaml:"tools"`
	Model       string   `yaml:"model"`
	MaxTurns    *int     `yaml:"maxTurns"`
	Skills      []string `yaml:"skills"`
	MCPServers  []string `yaml:"mcpServers"`
}

func parseRuntimeAgentFrontmatter(frontmatter []byte) (runtimeAgentFrontmatter, error) {
	rawFields := make(map[string]any)
	if err := yaml.Unmarshal(frontmatter, &rawFields); err != nil {
		return runtimeAgentFrontmatter{}, err
	}
	var typed runtimeAgentFrontmatter
	if err := yaml.Unmarshal(frontmatter, &typed); err != nil {
		return runtimeAgentFrontmatter{}, err
	}
	return typed, nil
}

func policyFromYAMLField(
	mapping *yaml.Node,
	field string,
	defaultMode string,
	lower bool,
) (agentCapabilityPolicy, error) {
	value, found := mappingValue(mapping, field)
	if !found {
		return agentCapabilityPolicy{Mode: defaultMode, Values: []string{}}, nil
	}
	if isYAMLNull(value) {
		mode := capabilityModeNone
		if defaultMode == capabilityModeInherit {
			mode = capabilityModeInherit
		}
		return agentCapabilityPolicy{Mode: mode, Values: []string{}}, nil
	}
	if value.Kind != yaml.SequenceNode {
		return agentCapabilityPolicy{}, errInvalidAgentFrontmatter
	}
	raw := make([]string, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return agentCapabilityPolicy{}, errInvalidAgentFrontmatter
		}
		raw = append(raw, item.Value)
	}
	values, err := normalizeExistingCapabilityValues(raw, lower)
	if err != nil {
		return agentCapabilityPolicy{}, err
	}
	if len(values) == 0 {
		return agentCapabilityPolicy{Mode: capabilityModeNone, Values: []string{}}, nil
	}
	return agentCapabilityPolicy{Mode: capabilityModeSelected, Values: values}, nil
}

func mappingValue(mapping *yaml.Node, field string) (*yaml.Node, bool) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == field {
			return mapping.Content[index+1], true
		}
	}
	return nil, false
}

func isYAMLNull(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == "!!null"
}

func validateCapabilityPolicy(
	policy agentCapabilityPolicy,
	allowedModes map[string]struct{},
	lower bool,
) (agentCapabilityPolicy, error) {
	if _, ok := allowedModes[policy.Mode]; !ok {
		return agentCapabilityPolicy{}, errors.New("unsupported capability mode")
	}
	values, err := validateRequestedCapabilityValues(policy.Values, lower)
	if err != nil {
		return agentCapabilityPolicy{}, err
	}
	if policy.Mode == capabilityModeSelected {
		if len(values) == 0 {
			return agentCapabilityPolicy{}, errors.New("selected capability values are required")
		}
	} else if len(values) != 0 {
		return agentCapabilityPolicy{}, errors.New("capability values require selected mode")
	}
	policy.Values = values
	policy.InheritedValues = nil
	return policy, nil
}

func validateRequestedCapabilityValues(values []string, lower bool) ([]string, error) {
	if len(values) > agentCapabilityValuesLimit {
		return nil, errors.New("too many capability values")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if raw != value || value == "" || len(value) > agentCapabilityValueMaxBytes ||
			!utf8.ValidString(value) || containsCapabilityControl(value) {
			return nil, errors.New("invalid capability value")
		}
		key := strings.ToLower(value)
		if lower {
			if value != key {
				return nil, errors.New("capability value must be normalized")
			}
		}
		if _, exists := seen[key]; exists {
			return nil, errors.New("duplicate capability value")
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeExistingCapabilityValues(values []string, lower bool) ([]string, error) {
	if len(values) > agentCapabilityValuesLimit {
		return nil, errors.New("too many capability values")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if len(value) > agentCapabilityValueMaxBytes || !utf8.ValidString(value) ||
			containsCapabilityControl(value) {
			return nil, errors.New("invalid capability value")
		}
		key := strings.ToLower(value)
		if lower {
			value = key
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizedCapabilityValues(values []string, lower bool) []string {
	normalized, err := normalizeExistingCapabilityValues(values, lower)
	if err != nil {
		return []string{}
	}
	return normalized
}

func safeCapabilityIdentifier(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) ||
			character == '/' || character == '\\' {
			return false
		}
	}
	return true
}

func containsCapabilityControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func applyCapabilityPolicy(
	document *agentCapabilitiesDocument,
	field string,
	policy agentCapabilityPolicy,
	defaultMode string,
) error {
	if document.root == nil {
		document.root = newAgentCapabilitiesYAMLRoot()
	}
	mapping, ok := yamlDocumentMapping(document.root)
	if !ok {
		return errInvalidAgentFrontmatter
	}
	if policy.Mode == defaultMode {
		removeMappingField(mapping, field)
		return nil
	}
	value := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	if policy.Mode == capabilityModeSelected {
		for _, item := range policy.Values {
			value.Content = append(value.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: item,
			})
		}
	}
	setMappingField(mapping, field, value)
	return nil
}

func newAgentCapabilitiesYAMLRoot() *yaml.Node {
	return &yaml.Node{
		Kind: yaml.DocumentNode,
		Content: []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
		}},
	}
}

func replaceEmptyAgentFrontmatterWithMapping(root *yaml.Node, frontmatter []byte) {
	if root == nil {
		return
	}
	mapping := newAgentCapabilitiesYAMLRoot().Content[0]
	comments := collectYAMLComments(root)
	if len(comments) == 0 {
		for _, line := range bytes.FieldsFunc(frontmatter, func(character rune) bool {
			return character == '\r' || character == '\n'
		}) {
			trimmed := strings.TrimSpace(string(line))
			if strings.HasPrefix(trimmed, "#") {
				comments = append(comments, trimmed)
			}
		}
	}
	appendYAMLComments(&mapping.HeadComment, comments...)
	root.HeadComment = ""
	root.LineComment = ""
	root.FootComment = ""
	root.Kind = yaml.DocumentNode
	root.Tag = ""
	root.Content = []*yaml.Node{mapping}
}

func removeMappingField(mapping *yaml.Node, field string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != field {
			continue
		}
		preserveRemovedYAMLComments(
			mapping,
			index,
			mapping.Content[index],
			mapping.Content[index+1],
		)
		mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
		return
	}
}

func setMappingField(mapping *yaml.Node, field string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == field {
			previous := mapping.Content[index+1]
			value.HeadComment = previous.HeadComment
			value.LineComment = previous.LineComment
			value.FootComment = previous.FootComment
			if previous.Kind == value.Kind {
				value.Style = previous.Style
			}
			removedComments := reuseCapabilitySequenceChildren(
				field,
				previous,
				value,
			)
			if len(value.Content) > 0 {
				appendYAMLComments(
					&value.Content[len(value.Content)-1].FootComment,
					removedComments...,
				)
			} else {
				appendYAMLComments(&mapping.FootComment, removedComments...)
			}
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field},
		value,
	)
}

func reuseCapabilitySequenceChildren(
	field string,
	previous *yaml.Node,
	next *yaml.Node,
) []string {
	if previous == nil || next == nil ||
		previous.Kind != yaml.SequenceNode ||
		next.Kind != yaml.SequenceNode {
		return nil
	}
	available := make(map[string][]*yaml.Node, len(previous.Content))
	for _, child := range previous.Content {
		key := capabilitySequenceNodeKey(field, child)
		available[key] = append(available[key], child)
	}
	reused := make(map[*yaml.Node]struct{}, len(next.Content))
	content := make([]*yaml.Node, 0, len(next.Content))
	for _, child := range next.Content {
		key := capabilitySequenceNodeKey(field, child)
		candidates := available[key]
		if len(candidates) == 0 {
			content = append(content, child)
			continue
		}
		existing := candidates[0]
		available[key] = candidates[1:]
		reused[existing] = struct{}{}
		content = append(content, existing)
	}
	var removedComments []string
	for _, child := range previous.Content {
		if _, retained := reused[child]; retained {
			continue
		}
		removedComments = append(removedComments, collectYAMLComments(child)...)
	}
	next.Content = content
	return removedComments
}

func capabilitySequenceNodeKey(field string, node *yaml.Node) string {
	if node == nil {
		return ""
	}
	value := strings.TrimSpace(node.Value)
	if field == "skills" {
		return value
	}
	return strings.ToLower(value)
}

func preserveRemovedYAMLComments(
	mapping *yaml.Node,
	index int,
	key *yaml.Node,
	value *yaml.Node,
) {
	comments := append(collectYAMLComments(key), collectYAMLComments(value)...)
	if len(comments) == 0 {
		return
	}
	joined := strings.Join(comments, "\n")
	if index+2 < len(mapping.Content) {
		next := mapping.Content[index+2]
		if next.HeadComment == "" {
			next.HeadComment = joined
		} else {
			next.HeadComment = joined + "\n" + next.HeadComment
		}
		return
	}
	if mapping.FootComment == "" {
		mapping.FootComment = joined
	} else {
		mapping.FootComment += "\n" + joined
	}
}

func collectYAMLComments(node *yaml.Node) []string {
	if node == nil {
		return nil
	}
	comments := make([]string, 0, 3)
	for _, comment := range []string{
		node.HeadComment,
		node.LineComment,
		node.FootComment,
	} {
		if strings.TrimSpace(comment) != "" {
			comments = append(comments, comment)
		}
	}
	for _, child := range node.Content {
		comments = append(comments, collectYAMLComments(child)...)
	}
	return comments
}

func appendYAMLComments(target *string, comments ...string) {
	filtered := make([]string, 0, len(comments))
	for _, comment := range comments {
		if strings.TrimSpace(comment) != "" {
			filtered = append(filtered, comment)
		}
	}
	if len(filtered) == 0 {
		return
	}
	joined := strings.Join(filtered, "\n")
	if *target == "" {
		*target = joined
		return
	}
	*target += "\n" + joined
}

func renderAgentCapabilitiesDocument(document agentCapabilitiesDocument) ([]byte, error) {
	if document.root == nil {
		return append([]byte(nil), document.raw...), nil
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(document.root); err != nil {
		return nil, fmt.Errorf("encode agent frontmatter: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close agent frontmatter encoder: %w", err)
	}
	frontmatter := bytes.TrimPrefix(encoded.Bytes(), []byte("---\n"))
	frontmatter = bytes.TrimSuffix(frontmatter, []byte("...\n"))
	if len(frontmatter) == 0 || frontmatter[len(frontmatter)-1] != '\n' {
		frontmatter = append(frontmatter, '\n')
	}
	if document.newline != "\n" {
		frontmatter = bytes.ReplaceAll(
			frontmatter,
			[]byte("\n"),
			[]byte(document.newline),
		)
	}
	if document.hasFrontmatter {
		result := make([]byte, 0, len(document.raw)+len(frontmatter))
		result = append(result, document.raw[:document.frontmatterStart]...)
		result = append(result, frontmatter...)
		result = append(result, document.raw[document.frontmatterEnd:]...)
		return result, nil
	}
	delimiter := []byte("---" + document.newline)
	result := make([]byte, 0, len(document.raw)+len(frontmatter)+len(delimiter)*2)
	result = append(result, delimiter...)
	result = append(result, frontmatter...)
	result = append(result, delimiter...)
	result = append(result, document.raw...)
	return result, nil
}

func capabilityPoliciesEqual(left, right agentCapabilityPolicy) bool {
	if left.Mode != right.Mode || len(left.Values) != len(right.Values) {
		return false
	}
	for index := range left.Values {
		if left.Values[index] != right.Values[index] {
			return false
		}
	}
	return true
}
