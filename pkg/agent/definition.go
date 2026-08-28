package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gomarkdown/markdown/parser"
	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/logger"
)

var errUnterminatedAgentFrontmatter = errors.New(
	"unterminated AGENT.md frontmatter",
)

// AgentDefinitionSource identifies which agent bootstrap file produced the definition.
type AgentDefinitionSource string

const (
	// AgentDefinitionSourceAgent indicates the new AGENT.md format.
	AgentDefinitionSourceAgent AgentDefinitionSource = "AGENT.md"
	// AgentDefinitionSourceAgents indicates the legacy AGENTS.md format.
	AgentDefinitionSourceAgents AgentDefinitionSource = "AGENTS.md"
)

// AgentFrontmatter holds machine-readable AGENT.md configuration.
//
// Known fields are exposed directly for convenience. Fields keeps the full
// parsed frontmatter so future refactors can read additional keys without
// changing the loader contract again.
type AgentFrontmatter struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Tools       []string       `json:"tools,omitempty"`
	Model       string         `json:"model,omitempty"`
	MaxTurns    *int           `json:"maxTurns,omitempty"`
	Skills      []string       `json:"skills,omitempty"`
	MCPServers  []string       `json:"mcpServers,omitempty"`
	Fields      map[string]any `json:"-"`
}

// AgentPromptDefinition represents the parsed AGENT.md or AGENTS.md prompt file.
type AgentPromptDefinition struct {
	Path           string           `json:"path"`
	Raw            string           `json:"raw"`
	Body           string           `json:"body"`
	Tasks          []string         `json:"tasks,omitempty"`
	RawFrontmatter string           `json:"raw_frontmatter,omitempty"`
	Frontmatter    AgentFrontmatter `json:"frontmatter"`
	FrontmatterErr string           `json:"frontmatter_error,omitempty"`
}

// SoulDefinition represents the resolved SOUL.md file linked to the agent.
type SoulDefinition struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// UserDefinition represents the resolved USER.md file linked to the workspace.
type UserDefinition struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// AgentContextDefinition captures the workspace agent definition in a runtime-friendly shape.
type AgentContextDefinition struct {
	Source        AgentDefinitionSource  `json:"source,omitempty"`
	Agent         *AgentPromptDefinition `json:"agent,omitempty"`
	Soul          *SoulDefinition        `json:"soul,omitempty"`
	User          *UserDefinition        `json:"user,omitempty"`
	DefinitionErr string                 `json:"definition_error,omitempty"`
}

// LoadAgentDefinition parses the workspace agent bootstrap files.
//
// It prefers the new AGENT.md format and its paired SOUL.md file. When the
// structured files are absent, it falls back to the legacy AGENTS.md layout so
// the current runtime can transition incrementally.
func (cb *ContextBuilder) LoadAgentDefinition() AgentContextDefinition {
	return loadAgentDefinition(cb.workspace)
}

func loadAgentDefinition(workspace string) AgentContextDefinition {
	definition := AgentContextDefinition{}
	definition.User = loadUserDefinition(workspace)
	agentPath := filepath.Join(workspace, string(AgentDefinitionSourceAgent))
	if file, exists, err := ReadAgentDefinitionFile(agentPath); exists || err != nil {
		if err != nil {
			definition.Source = AgentDefinitionSourceAgent
			definition.DefinitionErr = err.Error()
		} else {
			prompt := parseAgentPromptDefinition(agentPath, string(file.Data))
			definition.Source = AgentDefinitionSourceAgent
			definition.Agent = &prompt
			soulPath := filepath.Join(workspace, "SOUL.md")
			if content, readErr := os.ReadFile(soulPath); readErr == nil {
				definition.Soul = &SoulDefinition{
					Path:    soulPath,
					Content: string(content),
				}
			}
		}
		return definition
	}

	legacyPath := filepath.Join(workspace, string(AgentDefinitionSourceAgents))
	if file, exists, err := ReadAgentDefinitionFile(legacyPath); exists || err != nil {
		if err != nil {
			definition.Source = AgentDefinitionSourceAgents
			definition.DefinitionErr = err.Error()
		} else {
			definition.Source = AgentDefinitionSourceAgents
			definition.Agent = &AgentPromptDefinition{
				Path: legacyPath,
				Raw:  string(file.Data),
				Body: string(file.Data),
			}
		}
	}

	defaultSoulPath := filepath.Join(workspace, "SOUL.md")
	if definition.Source != "" || fileExists(defaultSoulPath) {
		if content, err := os.ReadFile(defaultSoulPath); err == nil {
			definition.Soul = &SoulDefinition{
				Path:    defaultSoulPath,
				Content: string(content),
			}
		}
	}

	return definition
}

func (definition AgentContextDefinition) trackedPaths(workspace string) []string {
	paths := []string{
		filepath.Join(workspace, string(AgentDefinitionSourceAgent)),
		filepath.Join(workspace, "SOUL.md"),
		filepath.Join(workspace, "USER.md"),
	}
	if definition.Source != AgentDefinitionSourceAgent {
		paths = append(paths,
			filepath.Join(workspace, string(AgentDefinitionSourceAgents)),
			filepath.Join(workspace, "IDENTITY.md"),
		)
	}
	return uniquePaths(paths)
}

func loadUserDefinition(workspace string) *UserDefinition {
	userPath := filepath.Join(workspace, "USER.md")
	if content, err := os.ReadFile(userPath); err == nil {
		return &UserDefinition{
			Path:    userPath,
			Content: string(content),
		}
	}

	return nil
}

func parseAgentPromptDefinition(path, content string) AgentPromptDefinition {
	frontmatter, body, unterminated := splitAgentFrontmatter(content)
	parsedFrontmatter := AgentFrontmatter{}
	var err error
	if unterminated {
		err = errUnterminatedAgentFrontmatter
	} else {
		parsedFrontmatter, err = parseAgentFrontmatter(path, frontmatter)
	}
	var tasks []string
	if !unterminated && err == nil {
		tasks = extractAgentTasks(body)
	}
	return AgentPromptDefinition{
		Path:           path,
		Raw:            content,
		Body:           body,
		Tasks:          tasks,
		RawFrontmatter: frontmatter,
		Frontmatter:    parsedFrontmatter,
		FrontmatterErr: errorString(err),
	}
}

func parseAgentFrontmatter(path, frontmatter string) (AgentFrontmatter, error) {
	parsed, err := decodeAgentFrontmatter(frontmatter)
	if err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToParseAgentMDFrontmatter,
			logger.NewSafeFields(
				agentDiagnosticPathField(path),
				agentDiagnosticErrorField(logger.ErrorClassValidation, err),
			),
		)
	}
	return parsed, err
}

func decodeAgentFrontmatter(frontmatter string) (AgentFrontmatter, error) {
	frontmatter = strings.TrimSpace(frontmatter)
	if frontmatter == "" {
		return AgentFrontmatter{}, nil
	}

	rawFields := make(map[string]any)
	if err := yaml.Unmarshal([]byte(frontmatter), &rawFields); err != nil {
		return AgentFrontmatter{}, err
	}

	var typed struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Tools       []string `yaml:"tools"`
		Model       string   `yaml:"model"`
		MaxTurns    *int     `yaml:"maxTurns"`
		Skills      []string `yaml:"skills"`
		MCPServers  []string `yaml:"mcpServers"`
	}
	if err := yaml.Unmarshal([]byte(frontmatter), &typed); err != nil {
		return AgentFrontmatter{}, err
	}

	return AgentFrontmatter{
		Name:        strings.TrimSpace(typed.Name),
		Description: strings.TrimSpace(typed.Description),
		Tools:       append([]string(nil), typed.Tools...),
		Model:       strings.TrimSpace(typed.Model),
		MaxTurns:    typed.MaxTurns,
		Skills:      append([]string(nil), typed.Skills...),
		MCPServers:  append([]string(nil), typed.MCPServers...),
		Fields:      rawFields,
	}, nil
}

func splitAgentFrontmatter(
	content string,
) (frontmatter, body string, unterminated bool) {
	normalized := string(parser.NormalizeNewlines([]byte(content)))
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", content, false
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return strings.Join(lines[1:], "\n"), "", true
	}

	frontmatter = strings.Join(lines[1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(body, "\n")
	return frontmatter, body, false
}

func extractAgentTasks(body string) []string {
	normalized := string(parser.NormalizeNewlines([]byte(body)))
	lines := strings.Split(normalized, "\n")
	inTasks := false
	tasks := make([]string, 0)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if isAgentTasksHeading(trimmed, lower) {
			inTasks = true
			continue
		}
		if !inTasks {
			continue
		}
		if strings.HasPrefix(trimmed, "#") && !isAgentTasksHeading(trimmed, lower) {
			break
		}
		if trimmed == "" {
			if len(tasks) > 0 {
				break
			}
			continue
		}
		task, ok := parseAgentTaskBullet(trimmed)
		if !ok {
			if len(tasks) > 0 {
				break
			}
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func isAgentTasksHeading(trimmed, lower string) bool {
	if lower == "tasks:" {
		return true
	}
	if strings.TrimSpace(strings.TrimLeft(trimmed, "#")) == "Tasks" {
		return true
	}
	return false
}

func parseAgentTaskBullet(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* "} {
		if strings.HasPrefix(line, prefix) {
			task := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			return task, task != ""
		}
	}
	dot := strings.Index(line, ". ")
	if dot > 0 {
		allDigits := true
		for _, r := range line[:dot] {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			task := strings.TrimSpace(line[dot+2:])
			return task, task != ""
		}
	}
	return "", false
}

func relativeWorkspacePath(workspace, path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	relativePath, err := filepath.Rel(workspace, path)
	if err == nil && relativePath != "." && !strings.HasPrefix(relativePath, "..") {
		return filepath.ToSlash(relativePath)
	}
	return filepath.Clean(path)
}

func uniquePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if slices.Contains(result, cleaned) {
			continue
		}
		result = append(result, cleaned)
	}
	return result
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
