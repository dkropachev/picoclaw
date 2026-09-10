package databasemigration

import (
	"crypto/sha256"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type backupSession struct {
	root           string
	finalRoot      string
	identity       fileidentity.Identity
	parent         string
	parentIdentity fileidentity.Identity
	manifest       BackupManifest
	statusKnown    bool
	statusIdentity fileidentity.Identity
	statusRevision uint64
	statusOutcome  string
	statusDigest   [sha256.Size]byte
}
