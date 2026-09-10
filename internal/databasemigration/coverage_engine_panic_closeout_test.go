package databasemigration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrationCleanupPanicPreservesCommittedOutcomeClassification(t *testing.T) {
	const secret = "secret cleanup panic payload"
	panicCleanup := func() error { panic(secret) }
	cutover := Result{Stores: []StoreResult{{cutover: true}}}

	t.Run("successful provider", func(t *testing.T) {
		err := runMigrationPreparedSource(panicCleanup, func() error { return nil })
		if !errors.Is(err, errPostMigrationCleanup) || strings.Contains(err.Error(), secret) {
			t.Fatalf("successful provider cleanup panic = %v", err)
		}
		if outcome := migrationTerminalOutcome(false, cutover, err); outcome != "complete_with_cleanup_error" {
			t.Fatalf("cleanup panic outcome = %q", outcome)
		}
	})

	t.Run("uncertain provider", func(t *testing.T) {
		providerErr := database.NewError(database.CodeOutcomeUnknown, "provider uncertainty")
		err := runMigrationPreparedSource(panicCleanup, func() error { return providerErr })
		if database.CodeOf(err) != database.CodeOutcomeUnknown ||
			!errors.Is(err, providerErr) || strings.Contains(err.Error(), secret) {
			t.Fatalf("uncertain provider cleanup panic = %v", err)
		}
		if outcome := migrationTerminalOutcome(false, cutover, err); outcome != "outcome_unknown" {
			t.Fatalf("uncertain cleanup panic outcome = %q", outcome)
		}
	})
}

func TestMigrationDrainPanicMakesEveryProviderOutcomeUnknown(t *testing.T) {
	const (
		drainSecret = "secret drain panic payload"
		runSecret   = "secret provider panic payload"
	)
	providerErr := database.NewError(database.CodeOutcomeUnknown, "provider uncertainty")
	tests := []struct {
		name         string
		run          func() (sqliteprovider.MaintenanceResult, error)
		wantProvider bool
	}{
		{
			name: "successful provider",
			run: func() (sqliteprovider.MaintenanceResult, error) {
				return sqliteprovider.MaintenanceResult{}, nil
			},
		},
		{
			name: "uncertain provider",
			run: func() (sqliteprovider.MaintenanceResult, error) {
				return sqliteprovider.MaintenanceResult{}, providerErr
			},
			wantProvider: true,
		},
		{
			name: "provider panic",
			run: func() (sqliteprovider.MaintenanceResult, error) {
				panic(runSecret)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drainCalls := 0
			_, err := runMigrationProviderChild(
				func(context.Context, error) error {
					drainCalls++
					panic(drainSecret)
				},
				test.run,
			)
			if drainCalls != 1 || database.CodeOf(err) != database.CodeOutcomeUnknown ||
				strings.Contains(err.Error(), drainSecret) || strings.Contains(err.Error(), runSecret) {
				t.Fatalf("provider/drain panic calls=%d error=%v", drainCalls, err)
			}
			if test.wantProvider && !errors.Is(err, providerErr) {
				t.Fatalf("provider/drain panic lost provider cause: %v", err)
			}
		})
	}
}

func TestMigrationDrainRejectsUnavailableCallback(t *testing.T) {
	if err := callMigrationProviderDrain(nil, nil); err == nil {
		t.Fatal("provider drain accepted nil callback")
	}
}
