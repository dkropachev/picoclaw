package agent

// AgentDefinitionTasks returns the construction-time tasks declared in an
// AGENT.md document. Frontmatter is excluded before the Tasks section is
// parsed, matching the runtime definition loader.
func AgentDefinitionTasks(data []byte) []string {
	frontmatter, body, unterminated := splitAgentFrontmatter(string(data))
	if unterminated {
		return nil
	}
	if _, err := decodeAgentFrontmatter(frontmatter); err != nil {
		return nil
	}
	return extractAgentTasks(body)
}
