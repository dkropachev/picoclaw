package reposcope

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

var validCodeTypes = map[CodeType]struct{}{
	CodeTypeHotpath:   {},
	CodeTypeCode:      {},
	CodeTypeTest:      {},
	CodeTypeBenchTest: {},
}

// NormalizeScope validates and canonicalizes a scope without interpreting its
// free-text guidance.
func NormalizeScope(scope Scope) (Scope, error) {
	if len(scope.FreeText) > MaxFreeTextBytes || !utf8.ValidString(scope.FreeText) ||
		hasUnsafeTextControl(scope.FreeText) {
		return Scope{}, fmt.Errorf("%w: free text is not bounded valid UTF-8", ErrInvalidScope)
	}
	if len(scope.IncludePrefixes) > MaxScopePrefixes || len(scope.ExcludePrefixes) > MaxScopePrefixes {
		return Scope{}, fmt.Errorf("%w: too many folder prefixes", ErrInvalidScope)
	}
	includes, err := normalizePrefixes(scope.IncludePrefixes)
	if err != nil {
		return Scope{}, err
	}
	excludes, err := normalizePrefixes(scope.ExcludePrefixes)
	if err != nil {
		return Scope{}, err
	}
	types := append([]CodeType(nil), scope.CodeTypes...)
	seenTypes := make(map[CodeType]struct{}, len(types))
	for _, codeType := range types {
		if _, ok := validCodeTypes[codeType]; !ok {
			return Scope{}, fmt.Errorf("%w: unknown code type %q", ErrInvalidScope, codeType)
		}
		seenTypes[codeType] = struct{}{}
	}
	types = types[:0]
	for codeType := range seenTypes {
		types = append(types, codeType)
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	return Scope{
		IncludePrefixes: includes,
		ExcludePrefixes: excludes,
		CodeTypes:       types,
		FreeText:        scope.FreeText,
	}, nil
}

func normalizePrefixes(prefixes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(prefixes))
	for _, raw := range prefixes {
		prefix, err := normalizePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidScope, err)
		}
		seen[prefix] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for prefix := range seen {
		result = append(result, prefix)
	}
	sort.Strings(result)
	return result, nil
}

func normalizePrefix(raw string) (string, error) {
	prefix := strings.TrimSpace(raw)
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || prefix == "." {
		return ".", nil
	}
	if len(prefix) > MaxRepositoryPathBytes || !utf8.ValidString(prefix) || hasUnsafePathControl(prefix) ||
		strings.Contains(prefix, "\\") ||
		strings.HasPrefix(prefix, "/") ||
		strings.HasPrefix(prefix, "./") {
		return "", fmt.Errorf("unsafe repository prefix %q", raw)
	}
	clean := path.Clean(prefix)
	if clean != prefix || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe repository prefix %q", raw)
	}
	return clean, nil
}

func normalizeFilePath(raw string) (string, error) {
	if raw == "" || raw == "." || len(raw) > MaxRepositoryPathBytes || !utf8.ValidString(raw) ||
		hasUnsafePathControl(raw) ||
		strings.Contains(raw, "\\") ||
		strings.HasPrefix(raw, "/") ||
		strings.HasPrefix(raw, "./") ||
		strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("unsafe repository path %q", raw)
	}
	clean := path.Clean(raw)
	if clean != raw || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe repository path %q", raw)
	}
	return clean, nil
}

func hasUnsafePathControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func hasUnsafeTextControl(value string) bool {
	for _, char := range value {
		if (char < 0x20 && char != '\n' && char != '\r' && char != '\t') || char == 0x7f {
			return true
		}
	}
	return false
}

// Allows reports whether a canonical repository path and code type are inside
// the hard scope. FreeText is never consulted.
func (scope Scope) Allows(filePath string, codeType CodeType) (bool, error) {
	normalized, err := NormalizeScope(scope)
	if err != nil {
		return false, err
	}
	return normalized.allowsNormalized(filePath, codeType)
}

func (scope Scope) allowsNormalized(filePath string, codeType CodeType) (bool, error) {
	filePath, err := normalizeFilePath(filePath)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrInvalidScope, err)
	}
	if _, ok := validCodeTypes[codeType]; !ok {
		return false, fmt.Errorf("%w: unknown code type %q", ErrInvalidScope, codeType)
	}
	if len(scope.CodeTypes) > 0 && !containsCodeType(scope.CodeTypes, codeType) {
		return false, nil
	}
	included := len(scope.IncludePrefixes) == 0
	for _, prefix := range scope.IncludePrefixes {
		if hasPathPrefix(filePath, prefix) {
			included = true
			break
		}
	}
	if !included {
		return false, nil
	}
	for _, prefix := range scope.ExcludePrefixes {
		if hasPathPrefix(filePath, prefix) {
			return false, nil
		}
	}
	return true, nil
}

func containsCodeType(types []CodeType, target CodeType) bool {
	for _, codeType := range types {
		if codeType == target {
			return true
		}
	}
	return false
}

func hasPathPrefix(filePath, prefix string) bool {
	return prefix == "." || filePath == prefix || strings.HasPrefix(filePath, prefix+"/")
}
