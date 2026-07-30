package api

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gomarkdown/markdown"
	"github.com/gomarkdown/markdown/ast"
	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	picoskills "github.com/sipeed/picoclaw/pkg/skills"
)

const (
	agentCapabilitySkillScanLimit         = 1024
	agentCapabilitySkillFileMaxBytes      = 64 << 10
	agentCapabilitySkillAggregateMaxBytes = 1 << 20
	agentCapabilitySkillReadDirBatchSize  = 64
)

type agentCapabilitySkillRoot struct {
	path   string
	source string
}

type agentCapabilitySkillMetadata struct {
	name        string
	description string
}

func buildAgentCapabilitySkillCatalog(
	workspace string,
) ([]agentCapabilitySkillCatalogItem, bool) {
	roots := []agentCapabilitySkillRoot{
		{path: filepath.Join(workspace, "skills"), source: "workspace"},
		{path: filepath.Join(globalConfigDir(), "skills"), source: "global"},
		{path: builtinSkillsDir(), source: "builtin"},
	}
	items := make([]agentCapabilitySkillCatalogItem, 0, agentCapabilityCatalogLimit)
	seenNames := make(map[string]struct{}, agentCapabilityCatalogLimit)
	seenRoots := make(map[string]struct{}, len(roots))
	scannedEntries := 0
	readBytes := int64(0)
	truncated := false

	for _, root := range roots {
		if strings.TrimSpace(root.path) == "" {
			continue
		}
		cleanRoot := filepath.Clean(root.path)
		if _, exists := seenRoots[cleanRoot]; exists {
			continue
		}
		seenRoots[cleanRoot] = struct{}{}

		directories, rootTruncated, stop := boundedAgentCapabilitySkillDirectories(
			cleanRoot,
			&scannedEntries,
		)
		truncated = truncated || rootTruncated
		for _, directoryName := range directories {
			if len(items) == agentCapabilityCatalogLimit {
				truncated = true
				stop = true
				break
			}

			remainingBytes := int64(agentCapabilitySkillAggregateMaxBytes) - readBytes
			content, size, ok := readAgentCapabilitySkillFile(
				filepath.Join(cleanRoot, directoryName, "SKILL.md"),
				remainingBytes,
			)
			if !ok {
				truncated = true
				continue
			}
			readBytes += size

			metadata := parseAgentCapabilitySkillMetadata(directoryName, content)
			if !validAgentCapabilitySkillMetadata(metadata) {
				truncated = true
				continue
			}
			identityKey := strings.ToLower(metadata.name)
			if _, exists := seenNames[identityKey]; exists {
				truncated = true
				continue
			}
			seenNames[identityKey] = struct{}{}
			items = append(items, agentCapabilitySkillCatalogItem{
				Name:   metadata.name,
				Source: root.source,
			})
		}
		if stop {
			break
		}
	}
	return items, truncated
}

func boundedAgentCapabilitySkillDirectories(
	root string,
	scannedEntries *int,
) ([]string, bool, bool) {
	directory, err := openAgentCapabilityCatalogDirectory(root)
	if err != nil {
		return nil, !errors.Is(err, fs.ErrNotExist), false
	}
	defer directory.Close()

	names := make([]string, 0)
	truncated := false
	stop := false
	for {
		entries, readErr := directory.ReadDir(agentCapabilitySkillReadDirBatchSize)
		for _, entry := range entries {
			if *scannedEntries == agentCapabilitySkillScanLimit {
				truncated = true
				stop = true
				break
			}
			*scannedEntries++
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		if stop || errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			truncated = true
			break
		}
	}
	sort.Strings(names)
	return names, truncated, stop
}

func readAgentCapabilitySkillFile(
	path string,
	remainingBytes int64,
) ([]byte, int64, bool) {
	if remainingBytes < 0 {
		return nil, 0, false
	}
	file, err := openAgentDefinitionNoFollow(path)
	if err != nil {
		return nil, 0, false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() ||
		info.Size() < 0 ||
		info.Size() > agentCapabilitySkillFileMaxBytes ||
		info.Size() > remainingBytes {
		return nil, 0, false
	}
	content, err := io.ReadAll(io.LimitReader(
		file,
		int64(agentCapabilitySkillFileMaxBytes)+1,
	))
	if err != nil ||
		int64(len(content)) != info.Size() ||
		len(content) > agentCapabilitySkillFileMaxBytes {
		return nil, 0, false
	}
	return content, int64(len(content)), true
}

func safeAgentCapabilitySkillFile(path string) bool {
	_, _, ok := readAgentCapabilitySkillFile(
		path,
		agentCapabilitySkillAggregateMaxBytes,
	)
	return ok
}

func parseAgentCapabilitySkillMetadata(
	directoryName string,
	content []byte,
) agentCapabilitySkillMetadata {
	frontmatter, body := splitAgentCapabilitySkillFrontmatter(string(content))
	title, bodyDescription := extractAgentCapabilityMarkdownMetadata(body)
	metadata := agentCapabilitySkillMetadata{
		name:        directoryName,
		description: bodyDescription,
	}
	if title != "" &&
		picoskills.ValidateSkillName(title) == nil &&
		len(title) <= picoskills.MaxNameLength {
		metadata.name = title
	}
	if frontmatter == "" {
		return metadata
	}

	var jsonMetadata struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if json.Unmarshal([]byte(frontmatter), &jsonMetadata) == nil {
		if jsonMetadata.Name != "" {
			metadata.name = jsonMetadata.Name
		}
		if jsonMetadata.Description != "" {
			metadata.description = jsonMetadata.Description
		}
		return metadata
	}

	var yamlMetadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(frontmatter), &yamlMetadata) == nil {
		if yamlMetadata.Name != "" {
			metadata.name = yamlMetadata.Name
		}
		if yamlMetadata.Description != "" {
			metadata.description = yamlMetadata.Description
		}
	}
	return metadata
}

func validAgentCapabilitySkillMetadata(metadata agentCapabilitySkillMetadata) bool {
	return metadata.name == strings.TrimSpace(metadata.name) &&
		picoskills.ValidateSkillName(metadata.name) == nil &&
		metadata.description != "" &&
		len(metadata.description) <= picoskills.MaxDescriptionLength
}

func splitAgentCapabilitySkillFrontmatter(content string) (string, string) {
	normalized := string(parser.NormalizeNewlines([]byte(content)))
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", content
	}
	end := -1
	for index := 1; index < len(lines); index++ {
		if lines[index] == "---" {
			end = index
			break
		}
	}
	if end == -1 {
		return "", content
	}
	frontmatter := strings.Join(lines[1:end], "\n")
	body := strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")
	return frontmatter, body
}

func extractAgentCapabilityMarkdownMetadata(content string) (string, string) {
	markdownParser := parser.NewWithExtensions(parser.CommonExtensions)
	document := markdown.Parse([]byte(content), markdownParser)
	if document == nil {
		return "", ""
	}

	title := ""
	description := ""
	ast.WalkFunc(document, func(node ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}
		switch typed := node.(type) {
		case *ast.Heading:
			if title == "" && typed.Level == 1 {
				title = agentCapabilityMarkdownNodeText(typed)
			}
		case *ast.Paragraph:
			if description == "" {
				description = agentCapabilityMarkdownNodeText(typed)
			}
		}
		if title != "" && description != "" {
			return ast.Terminate
		}
		return ast.GoToNext
	})
	return title, description
}

func agentCapabilityMarkdownNodeText(node ast.Node) string {
	var builder strings.Builder
	ast.WalkFunc(node, func(child ast.Node, entering bool) ast.WalkStatus {
		if !entering {
			return ast.GoToNext
		}
		switch typed := child.(type) {
		case *ast.Text:
			builder.Write(typed.Literal)
		case *ast.Code:
			builder.Write(typed.Literal)
		case *ast.Softbreak, *ast.Hardbreak, *ast.NonBlockingSpace:
			builder.WriteByte(' ')
		}
		return ast.GoToNext
	})
	return strings.Join(strings.Fields(builder.String()), " ")
}
