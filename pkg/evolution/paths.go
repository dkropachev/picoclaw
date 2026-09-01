package evolution

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
)

type Paths struct {
	Workspace     string
	RootDir       string
	Database      string
	LegacyArchive string
	// Deprecated: legacy migration source. SQLite is authoritative.
	LearningRecords string
	// Deprecated: legacy migration source. SQLite is authoritative.
	TaskRecords string
	// Deprecated: legacy migration source. SQLite is authoritative.
	PatternRecords string
	// Deprecated: legacy migration source. SQLite is authoritative.
	SkillDrafts string
	// Deprecated: legacy migration source directory. SQLite is authoritative.
	ProfilesDir string
	BackupsDir  string
}

func workspaceScopeDir(workspaceID string) string {
	sum := sha1.Sum([]byte(workspaceID))
	base := filepath.Base(filepath.Clean(workspaceID))
	base = sanitizeWorkspaceComponent(base)
	if base == "" || base == "." {
		base = "workspace"
	}
	return base + "-" + hex.EncodeToString(sum[:6])
}

func sanitizeWorkspaceComponent(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.':
			builder.WriteRune(character)
		default:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func NewPaths(workspace, override string) Paths {
	root := strings.TrimSpace(override)
	if root == "" {
		root = filepath.Join(workspace, "state", "evolution")
	}

	return Paths{
		Workspace:       workspace,
		RootDir:         root,
		Database:        filepath.Join(root, "evolution.db"),
		LegacyArchive:   filepath.Join(root, "legacy-json", "evolution-v1"),
		LearningRecords: filepath.Join(root, "learning-records.jsonl"),
		TaskRecords:     filepath.Join(root, "task-records.jsonl"),
		PatternRecords:  filepath.Join(root, "pattern-records.jsonl"),
		SkillDrafts:     filepath.Join(root, "skill-drafts.json"),
		ProfilesDir:     filepath.Join(root, "profiles"),
		BackupsDir:      filepath.Join(root, "backups"),
	}
}
