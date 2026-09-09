package databasemigration

// D4a intentionally lands the reviewed filesystem substrate before D4b wires
// it into archive orchestration. These anchors keep that dormant intermediate
// slice lint-clean without executing or exporting the primitives.
var (
	_ = backupExistingIdentity
	_ = pinPrivateBackupDirectory
	_ = validatePinnedPrivateBackupDirectory
	_ = backupPathMatchesOpened
	_ = copyBackupFile
	_ = createPinnedBackupDirectory
	_ = writePrivateBackupFileExclusive
	_ = defaultBackupControlWriteOps
)
