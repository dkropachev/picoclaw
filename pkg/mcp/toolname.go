package mcp

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

const (
	maxToolNameLength      = 64
	maxToolNameComponent   = 64
	toolNameHashSuffixSize = 9
)

// ToolIdentity identifies one logical tool exposed by an MCP server.
type ToolIdentity struct {
	Server string
	Tool   string
}

// ErrCanonicalToolNameCollision identifies two distinct MCP tool identities
// that cannot be distinguished by their provider-facing registration name.
var ErrCanonicalToolNameCollision = errors.New("canonical MCP tool name collision")

// CanonicalToolNameCollisionError describes an ambiguous provider-facing MCP
// tool name. Callers must not register either identity under Name because doing
// so would make execution depend on registration order.
type CanonicalToolNameCollisionError struct {
	Name   string
	First  ToolIdentity
	Second ToolIdentity
}

func (e *CanonicalToolNameCollisionError) Error() string {
	if e == nil {
		return ErrCanonicalToolNameCollision.Error()
	}
	return fmt.Sprintf(
		"%s %q for %q/%q and %q/%q",
		ErrCanonicalToolNameCollision,
		e.Name,
		e.First.Server,
		e.First.Tool,
		e.Second.Server,
		e.Second.Tool,
	)
}

func (e *CanonicalToolNameCollisionError) Unwrap() error {
	return ErrCanonicalToolNameCollision
}

// CanonicalToolName returns the provider-facing registration name for an MCP
// server/tool pair. Its output is stable and capped at the 64-character limit
// used by OpenAI-compatible tool APIs.
//
// The hash behavior intentionally preserves the historical MCP registration
// algorithm: original, unsanitized bytes are hashed whenever sanitization is
// lossy or the combined name must be truncated.
func CanonicalToolName(serverName, toolName string) string {
	sanitizedServer := CanonicalToolNameComponent(serverName)
	sanitizedTool := CanonicalToolNameComponent(toolName)
	full := fmt.Sprintf("mcp_%s_%s", sanitizedServer, sanitizedTool)

	lossless := strings.ToLower(serverName) == sanitizedServer &&
		strings.ToLower(toolName) == sanitizedTool
	if lossless && len(full) <= maxToolNameLength {
		return full
	}

	h := fnv.New32a()
	_, _ = h.Write([]byte(serverName + "\x00" + toolName))
	suffix := fmt.Sprintf("%08x", h.Sum32())

	base := full
	if len(base) > maxToolNameLength-toolNameHashSuffixSize {
		base = strings.TrimRight(full[:maxToolNameLength-toolNameHashSuffixSize], "_")
	}
	return base + "_" + suffix
}

// DetectCanonicalToolNameCollision returns a deterministic typed error when
// distinct logical MCP tools map to the same provider-facing registration
// name. Repeated copies of the exact same identity are not ambiguous.
func DetectCanonicalToolNameCollision(identities []ToolIdentity) error {
	type namedIdentity struct {
		name string
		ToolIdentity
	}
	named := make([]namedIdentity, 0, len(identities))
	for _, identity := range identities {
		named = append(named, namedIdentity{
			name:         CanonicalToolName(identity.Server, identity.Tool),
			ToolIdentity: identity,
		})
	}
	sort.Slice(named, func(i, j int) bool {
		if named[i].name != named[j].name {
			return named[i].name < named[j].name
		}
		if named[i].Server != named[j].Server {
			return named[i].Server < named[j].Server
		}
		return named[i].Tool < named[j].Tool
	})

	for i := 1; i < len(named); i++ {
		first, second := named[i-1], named[i]
		if first.name != second.name ||
			(first.Server == second.Server && first.Tool == second.Tool) {
			continue
		}
		return &CanonicalToolNameCollisionError{
			Name:   first.name,
			First:  first.ToolIdentity,
			Second: second.ToolIdentity,
		}
	}
	return nil
}

// CanonicalToolNameComponent returns the normalized component used inside a
// canonical MCP tool name. Most callers should use CanonicalToolName instead.
func CanonicalToolNameComponent(value string) string {
	value = strings.ToLower(value)
	var out strings.Builder
	out.Grow(len(value))

	previousUnderscore := false
	for _, char := range value {
		allowed := (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') ||
			char == '_' || char == '-'
		if !allowed {
			if !previousUnderscore {
				out.WriteByte('_')
				previousUnderscore = true
			}
			continue
		}
		if char == '_' {
			if previousUnderscore {
				continue
			}
			previousUnderscore = true
		} else {
			previousUnderscore = false
		}
		out.WriteRune(char)
	}

	result := strings.Trim(out.String(), "_")
	if result == "" {
		result = "unnamed"
	}
	if len(result) > maxToolNameComponent {
		result = result[:maxToolNameComponent]
	}
	return result
}
