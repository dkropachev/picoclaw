package databasemigration

import "github.com/sipeed/picoclaw/internal/fileidentity"

type backupSession struct {
	root           string
	finalRoot      string
	identity       fileidentity.Identity
	parent         string
	parentIdentity fileidentity.Identity
	manifest       BackupManifest
}
