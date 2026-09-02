package sqliteprovider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderValidatesAndRecoversHotRollbackJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hot-journal.db")
	command := exec.Command(os.Args[0], "-test.run=^TestProviderHotRollbackJournalChild$")
	command.Env = append(os.Environ(), "PICOCLAW_PROVIDER_HOT_JOURNAL="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hot-journal child: %v\n%s", err, output)
	}
	journal := path + "-journal"
	if info, err := os.Stat(journal); err != nil || info.Size() == 0 {
		t.Fatalf("hot rollback journal: info=%v error=%v", info, err)
	}
	if err := validateGenerationMembers(path, true); err != nil {
		t.Fatalf("validate hot rollback generation: %v", err)
	}

	database, err := OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow("SELECT value FROM hot_journal_fixture WHERE id=1").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "committed" {
		t.Fatalf("recovered value = %q, want committed", value)
	}
}

func TestProviderHotRollbackJournalChild(t *testing.T) {
	path := os.Getenv("PICOCLAW_PROVIDER_HOT_JOURNAL")
	if path == "" {
		return
	}
	database, err := OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if configureErr := ConfigureOffline(context.Background(), database, 5*time.Second); configureErr != nil {
		t.Fatal(configureErr)
	}
	if _, schemaErr := database.Exec(`
		CREATE TABLE hot_journal_fixture(id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO hot_journal_fixture(id, value) VALUES (1, 'committed');
	`); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(
		context.Background(),
		"UPDATE hot_journal_fixture SET value='uncommitted' WHERE id=1",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-journal"); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
