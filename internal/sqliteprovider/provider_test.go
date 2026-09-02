package sqliteprovider

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDSNOwnsDurabilityConfiguration(t *testing.T) {
	t.Parallel()

	dsn, err := DSN(filepath.Join(t.TempDir(), "store with spaces.db"), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"file:", "foreign_keys", "busy_timeout", "5000", "synchronous", "FULL",
	} {
		if !strings.Contains(dsn, required) {
			t.Fatalf("DSN() = %q, missing %q", dsn, required)
		}
	}
}

func TestOpenAndBusyClassification(t *testing.T) {
	t.Parallel()

	dsn, err := DSN(":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = dsn
	database, err := OpenStore(":memory:", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := Configure(t.Context(), database, 5*time.Second, true); err != nil {
		t.Fatal(err)
	}

	var foreignKeys, busyTimeout, synchronous int
	if err := database.QueryRow(`SELECT fk.foreign_keys, bt.timeout, sm.synchronous
		FROM pragma_foreign_keys AS fk
		CROSS JOIN pragma_busy_timeout AS bt
		CROSS JOIN pragma_synchronous AS sm`).Scan(
		&foreignKeys,
		&busyTimeout,
		&synchronous,
	); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 {
		t.Fatalf("provider settings = %d, %d, %d", foreignKeys, busyTimeout, synchronous)
	}
	if IsBusyOrLocked(errors.New("ordinary")) {
		t.Fatal("ordinary error classified as retryable")
	}
}
