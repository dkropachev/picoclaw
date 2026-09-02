//go:build unix

package sqliteprovider

import (
	"errors"
	"testing"
	"time"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func TestStagedMigrationDirectorySyncFailureIsOutcomeUnknown(t *testing.T) {
	path := createStagedMigrationFixture(t)
	originalSync := stagedCutoverDirectorySync
	stagedCutoverDirectorySync = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { stagedCutoverDirectorySync = originalSync })

	err := MigrateStagedOffline(t.Context(), path, 5*time.Second, 1, installStagedFixtureTable)
	if dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatalf("cutover sync failure code = %s, error = %v", dblayer.CodeOf(err), err)
	}
	ready, inspectErr := HasSchemaObjects(t.Context(), path, 5*time.Second, "installed")
	if inspectErr != nil || !ready {
		t.Fatalf("installed generation ready=%t err=%v", ready, inspectErr)
	}
}
