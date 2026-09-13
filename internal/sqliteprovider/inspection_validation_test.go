package sqliteprovider

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databasevalidation"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func newInspectionDomainValidationFixture(
	t *testing.T,
) (Inspection, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "validation.db")
	database, openErr := OpenStore(path, time.Second)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := Configure(t.Context(), database, time.Second, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		t.Context(),
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT NOT NULL) STRICT`,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		t.Context(),
		`INSERT INTO items (id, value) VALUES (1, 'one')`,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `PRAGMA user_version = 1`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), path, time.Second)
	if err != nil || !inspection.Exists || inspection.Empty || inspection.Version != 1 {
		t.Fatalf("inspect validation fixture = %#v, %v", inspection, err)
	}
	t.Cleanup(func() { _ = inspection.Release() })
	return inspection, path
}

func TestInspectionDomainValidationIsBoundScopedAndReadOnly(t *testing.T) {
	inspection, path := newInspectionDomainValidationFixture(t)
	var (
		retainedGeneration databasevalidation.Generation
		ignoredReadErr     error
	)
	validationErr := inspection.ValidateDomain(
		t.Context(),
		"global/auth",
		"auth",
		func(_ context.Context, generation databasevalidation.Generation) error {
			if generation.StoreID() != "global/auth" || generation.Domain() != "auth" {
				return errors.New("validation binding changed")
			}
			count, err := generation.ReadScalar("SELECT COUNT(*) FROM items")
			if err != nil || count.Kind != databasevalidation.ScalarInt64 || count.Int64 != 1 {
				return errors.Join(errors.New("validation row count changed"), err)
			}
			value, err := generation.ReadScalar("SELECT value FROM items WHERE id = ?", "1")
			if err != nil || value.Kind != databasevalidation.ScalarText || value.Text != "one" {
				return errors.Join(errors.New("validation scalar changed"), err)
			}
			_, ignoredReadErr = generation.ReadScalar("DELETE FROM items")
			retainedGeneration = generation
			return nil
		},
	)
	if validationErr == nil || ignoredReadErr == nil ||
		!errors.Is(validationErr, ignoredReadErr) {
		t.Fatalf(
			"ignored scalar read failure = validation:%v read:%v",
			validationErr,
			ignoredReadErr,
		)
	}
	if retainedGeneration.StoreID() != "" || retainedGeneration.Domain() != "" {
		t.Fatal("retained validation generation kept its binding")
	}
	if _, err := retainedGeneration.ReadScalar("SELECT COUNT(*) FROM items"); !errors.Is(
		err, errInspectionValidationScopeExpired,
	) {
		t.Fatalf("retained validation generation read = %v", err)
	}
	if _, err := retainedGeneration.ReadScalar(
		"DELETE FROM items", strings.Repeat("x", maximumInspectionValidationArgumentBytes+1),
	); !errors.Is(err, errInspectionValidationScopeExpired) {
		t.Fatalf("retained invalid validation read = %v", err)
	}

	if adopted, adoptErr := inspection.Adopt(); adoptErr == nil || adopted != nil {
		t.Fatalf("failed validation remained adoptable = %#v, %v", adopted, adoptErr)
	}

	inspection, _ = newInspectionDomainValidationFixture(t)
	validationErr = inspection.ValidateDomain(
		t.Context(), "global/auth", "auth",
		func(_ context.Context, generation databasevalidation.Generation) error {
			value, readErr := generation.ReadScalar("SELECT value FROM items WHERE id = ?", "1")
			if readErr != nil || value.Kind != databasevalidation.ScalarText || value.Text != "one" {
				return errors.Join(errors.New("validation scalar changed"), readErr)
			}
			return nil
		},
	)
	if validationErr != nil {
		t.Fatal(validationErr)
	}
	adopted, adoptErr := inspection.Adopt()
	if adoptErr != nil || adopted == nil {
		t.Fatalf("adopt validated inspection = %#v, %v", adopted, adoptErr)
	}
	defer adopted.Close()
	var queryOnly int
	if err := adopted.QueryRowContext(t.Context(), "PRAGMA query_only").Scan(&queryOnly); err != nil ||
		queryOnly != 0 {
		t.Fatalf("adopted query_only = %d, %v", queryOnly, err)
	}
	var value string
	if err := adopted.QueryRowContext(
		t.Context(), "SELECT value FROM items WHERE id = 1",
	).Scan(&value); err != nil || value != "one" {
		t.Fatalf("validated generation changed = %q, %v", value, err)
	}
	if info, err := os.Lstat(path); err != nil || info == nil || !info.Mode().IsRegular() {
		t.Fatalf("validated path changed: %#v, %v", info, err)
	}
}

func TestInspectionDomainValidationRejectsMutationScriptsAndPathDisclosure(t *testing.T) {
	for _, statement := range []string{
		"INSERT INTO items (id, value) VALUES (2, 'two') RETURNING id",
		"SELECT COUNT(*) FROM items; DELETE FROM items",
		"PRAGMA query_only = OFF",
		"WITH candidate AS (SELECT 1) SELECT * FROM candidate",
		"SELECT file FROM pragma_database_list",
		"SELECT * FROM pragma_module_list",
		"SELECT readfile('/tmp/secret')",
		"SELECT writefile('/tmp/side-effect', 'x')",
		"SELECT load_extension('unsafe')",
	} {
		t.Run(strings.ReplaceAll(statement[:min(len(statement), 24)], " ", "_"), func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			err := inspection.ValidateDomain(
				t.Context(), "global/auth", "auth",
				func(_ context.Context, generation databasevalidation.Generation) error {
					_, err := generation.ReadScalar(statement)
					return err
				},
			)
			if !errors.Is(err, errInspectionValidationContract) ||
				IsInspectionIntegrity(err) || !IsInspectionInfrastructure(err) {
				t.Fatalf("restricted statement error = %v", err)
			}
			if adopted, adoptErr := inspection.Adopt(); adoptErr == nil || adopted != nil {
				t.Fatalf("restricted statement inspection remained adoptable = %#v, %v", adopted, adoptErr)
			}
		})
	}

	for name, arguments := range map[string][]string{
		"NUL":       {"bad\x00value"},
		"too large": {strings.Repeat("x", maximumInspectionValidationArgumentBytes+1)},
		"too many":  make([]string, maximumInspectionValidationArguments+1),
	} {
		t.Run("arguments "+name, func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			err := inspection.ValidateDomain(
				t.Context(), "global/auth", "auth",
				func(_ context.Context, generation databasevalidation.Generation) error {
					_, readErr := generation.ReadScalar("SELECT 1", arguments...)
					return readErr
				},
			)
			if !IsInspectionInfrastructure(err) ||
				!errors.Is(err, errInspectionValidationContract) {
				t.Fatalf("invalid scalar arguments = %v", err)
			}
		})
	}
}

func TestInspectionDomainValidationClassifiesErrorsAndScopeMisuse(t *testing.T) {
	canary := errors.New("inspection domain validation canary")
	t.Run("logical mismatch", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		err := inspection.ValidateDomain(
			t.Context(), "global/auth", "auth",
			func(context.Context, databasevalidation.Generation) error { return canary },
		)
		if !IsInspectionIntegrity(err) || IsInspectionInfrastructure(err) ||
			!errors.Is(err, canary) {
			t.Fatalf("logical validation mismatch = %v", err)
		}
		if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
			t.Fatalf("invalid exact generation remained adoptable = %#v, %v", database, adoptErr)
		}
	})

	for name, forged := range map[string]error{
		"forged canceled": context.Canceled,
		"forged deadline": context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			err := inspection.ValidateDomain(
				t.Context(), "global/auth", "auth",
				func(context.Context, databasevalidation.Generation) error { return forged },
			)
			if !IsInspectionIntegrity(err) || IsInspectionInfrastructure(err) ||
				!errors.Is(err, forged) {
				t.Fatalf("forged callback cancellation = %v", err)
			}
		})
	}

	t.Run("validator panic", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		err := inspection.ValidateDomain(
			t.Context(), "global/auth", "auth",
			func(context.Context, databasevalidation.Generation) error { panic("secret") },
		)
		if !IsInspectionInfrastructure(err) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("panicked validation = %v", err)
		}
		if database, err := inspection.Adopt(); database != nil || err == nil {
			t.Fatalf("poisoned inspection remained adoptable = %#v, %v", database, err)
		}
	})

	t.Run("validator Goexit", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		err := inspection.ValidateDomain(
			t.Context(), "global/auth", "auth",
			func(context.Context, databasevalidation.Generation) error {
				runtime.Goexit()
				return nil
			},
		)
		if !IsInspectionInfrastructure(err) ||
			!errors.Is(err, errInspectionValidationContract) {
			t.Fatalf("Goexit validation = %v", err)
		}
		if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
			t.Fatalf("Goexit validation remained adoptable = %#v, %v", database, adoptErr)
		}
	})

	t.Run("parent context cancellation", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		err := inspection.ValidateDomain(
			canceled, "global/auth", "auth",
			func(context.Context, databasevalidation.Generation) error { return nil },
		)
		if !errors.Is(err, context.Canceled) || IsInspectionIntegrity(err) {
			t.Fatalf("canceled validation = %v", err)
		}
	})

	t.Run("ignored query failure remains sticky", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		var readErr error
		err := inspection.ValidateDomain(
			t.Context(), "global/auth", "auth",
			func(_ context.Context, generation databasevalidation.Generation) error {
				_, readErr = generation.ReadScalar("SELECT missing_column FROM items")
				return nil
			},
		)
		if readErr == nil || !errors.Is(err, readErr) ||
			!errors.Is(err, errInspectionValidationQueryUnavailable) ||
			IsInspectionInfrastructure(err) || IsInspectionIntegrity(err) {
			t.Fatalf("ignored scalar query failure = validation:%v read:%v", err, readErr)
		}
	})

	t.Run("concurrent read", func(t *testing.T) {
		state := &inspectionValidationState{active: true, inFlight: true}
		generation := inspectionValidationGeneration{state: state}
		if _, err := generation.ReadScalar("SELECT 1"); !errors.Is(err, errInspectionValidationContract) ||
			!IsInspectionInfrastructure(err) {
			t.Fatalf("concurrent validation read = %v", err)
		}
	})
}

func TestInspectionDomainValidationPreservesAdmittedFailureDuringCancellation(t *testing.T) {
	for _, test := range []struct {
		name           string
		parentCancel   bool
		infrastructure bool
	}{
		{name: "deadline infrastructure", infrastructure: true},
		{name: "deadline integrity"},
		{name: "parent cancellation infrastructure", parentCancel: true, infrastructure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			canary := errors.New("admitted cancellation-boundary failure")
			rawErr := errors.Join(errInspectionIntegrity, canary)
			if test.infrastructure {
				rawErr = errors.Join(errInspectionInfrastructure, canary)
			}
			readStarted := make(chan struct{})
			ops := defaultInspectionValidationOps()
			ops.readScalar = func(
				ctx context.Context,
				_ *sql.Conn,
				_ string,
				_ []string,
			) (databasevalidation.Scalar, error) {
				close(readStarted)
				<-ctx.Done()
				return databasevalidation.Scalar{}, rawErr
			}
			ctx := t.Context()
			var cancel context.CancelFunc
			if test.parentCancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			result := make(chan error, 1)
			go func() {
				result <- inspection.validateDomainWithLimits(
					ctx,
					"global/auth",
					"auth",
					func(_ context.Context, generation databasevalidation.Generation) error {
						_, _ = generation.ReadScalar("SELECT 1")
						return nil
					},
					inspectionValidationLimits{
						maximumDuration: 20 * time.Millisecond,
						cleanupTimeout:  time.Second,
						ops:             ops,
					},
				)
			}()
			<-readStarted
			if cancel != nil {
				cancel()
			}
			err := <-result
			if !errors.Is(err, canary) ||
				IsInspectionInfrastructure(err) != test.infrastructure ||
				IsInspectionIntegrity(err) == test.infrastructure {
				t.Fatalf("cancellation-boundary classification = %v", err)
			}
			if test.parentCancel {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("parent cancellation was lost: %v", err)
				}
			} else if !errors.Is(err, errInspectionValidationDeadline) {
				t.Fatalf("owned validation deadline was lost: %v", err)
			}
			if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
				t.Fatalf("failed cancellation-boundary validation remained adoptable = %#v, %v", database, adoptErr)
			}
			if releaseErr := inspection.Release(); releaseErr != nil {
				t.Fatal(releaseErr)
			}
		})
	}
}

func TestInspectionValidationClassifiesRawCorruptionBeforeCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-database.db")
	if err := os.WriteFile(path, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err := DSN(path, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	database, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	rawErr := database.QueryRowContext(
		t.Context(), "PRAGMA main.user_version",
	).Scan(&version)
	if rawErr == nil || !isSQLiteIntegrityFailure(rawErr) {
		t.Fatalf("raw corruption fixture = %v", rawErr)
	}
	classified := inspectionValidationQueryFailure(rawErr)
	if !IsInspectionIntegrity(classified) || !errors.Is(classified, rawErr) {
		t.Fatalf("raw corruption classification = %v", classified)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	result := inspectionValidationCausePrecedence(canceled, classified)
	if !errors.Is(result, context.Canceled) || !IsInspectionIntegrity(result) ||
		!errors.Is(result, rawErr) {
		t.Fatalf("corruption plus cancellation = %v", result)
	}
}

func TestInspectionDomainValidationQueryOnlyStatePreservesCancellation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "initial check"
		if enabled {
			name = "enable verification"
		}
		t.Run(name, func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			canary := errors.New("query-only cancellation race")
			connection := new(sql.Conn)
			ops := defaultInspectionValidationOps()
			ops.validateQuery = func(context.Context, Inspection) error { return nil }
			ops.revalidate = func(context.Context, Inspection) error { return nil }
			ops.connection = func(context.Context, Inspection) (*sql.Conn, error) {
				return connection, nil
			}
			ops.requireQueryOnly = func(context.Context, *sql.Conn, bool) error {
				if !enabled {
					return errors.Join(errInspectionValidationQueryOnlyState, canary)
				}
				return nil
			}
			ops.setQueryOnly = func(_ context.Context, _ *sql.Conn, requested bool) error {
				if enabled && requested {
					return errors.Join(errInspectionValidationQueryOnlyState, canary)
				}
				return nil
			}
			ops.discardConnection = func(*sql.Conn) error { return nil }
			ops.closeConnection = func(*sql.Conn) error { return nil }
			err := inspection.validateDomainWithLimits(
				ctx,
				"global/auth",
				"auth",
				func(context.Context, databasevalidation.Generation) error { return nil },
				inspectionValidationLimits{
					maximumDuration: time.Second,
					cleanupTimeout:  time.Second,
					ops:             ops,
				},
			)
			if !errors.Is(err, context.Canceled) || !errors.Is(err, canary) ||
				!IsInspectionInfrastructure(err) {
				t.Fatalf("query-only state plus cancellation = %v", err)
			}
			if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
				t.Fatalf("query-only state failure remained adoptable = %#v, %v", database, adoptErr)
			}
		})
	}
}

func TestInspectionDomainValidationScalarKindsAndCardinality(t *testing.T) {
	inspection, _ := newInspectionDomainValidationFixture(t)
	var retainedState *inspectionValidationState
	err := inspection.ValidateDomain(
		t.Context(),
		"workspace/repository-reviews",
		"repository-reviews",
		func(_ context.Context, generation databasevalidation.Generation) error {
			retainedState = generation.(inspectionValidationGeneration).state
			tests := []struct {
				statement string
				arguments []string
				want      databasevalidation.Scalar
			}{
				{
					statement: "SELECT value FROM items WHERE id = ?",
					arguments: []string{"missing"},
					want:      databasevalidation.Scalar{Kind: databasevalidation.ScalarNoRow},
				},
				{
					statement: "SELECT NULL",
					want:      databasevalidation.Scalar{Kind: databasevalidation.ScalarNull},
				},
				{
					statement: "SELECT COUNT(*) FROM items",
					want: databasevalidation.Scalar{
						Kind: databasevalidation.ScalarInt64, Int64: 1,
					},
				},
				{
					statement: "SELECT value FROM items WHERE id = ?",
					arguments: []string{"1"},
					want: databasevalidation.Scalar{
						Kind: databasevalidation.ScalarText, Text: "one",
					},
				},
			}
			for _, test := range tests {
				got, err := generation.ReadScalar(test.statement, test.arguments...)
				if err != nil || got != test.want {
					return errors.Join(
						errors.New("inspection validation scalar changed"),
						err,
					)
				}
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	assertInspectionValidationStateScrubbed(t, retainedState)

	for _, statement := range []string{
		"SELECT value, id FROM items",
		"SELECT value FROM items UNION ALL SELECT value FROM items",
		"SELECT CAST(value AS BLOB) FROM items",
		"SELECT 1.5",
	} {
		t.Run(strings.ReplaceAll(statement, " ", "_"), func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			err := inspection.ValidateDomain(
				t.Context(), "global/auth", "auth",
				func(_ context.Context, generation databasevalidation.Generation) error {
					_, readErr := generation.ReadScalar(statement)
					return readErr
				},
			)
			if !IsInspectionInfrastructure(err) ||
				!errors.Is(err, errInspectionValidationContract) {
				t.Fatalf("unsupported scalar shape/type = %v", err)
			}
		})
	}
}

func TestInspectionDomainValidationSynchronizesCancellationAndScrubsCapability(t *testing.T) {
	t.Run("deadline is delivered to callback", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		started := make(chan struct{})
		var retained inspectionValidationGeneration
		start := time.Now()
		err := inspection.validateDomainWithLimits(
			t.Context(),
			"global/auth",
			"auth",
			func(ctx context.Context, generation databasevalidation.Generation) error {
				retained = generation.(inspectionValidationGeneration)
				close(started)
				<-ctx.Done()
				return nil
			},
			inspectionValidationLimits{
				maximumDuration: 20 * time.Millisecond,
				cleanupTimeout:  20 * time.Millisecond,
			},
		)
		<-started
		if elapsed := time.Since(start); elapsed > time.Second ||
			IsInspectionInfrastructure(err) ||
			!errors.Is(err, errInspectionValidationDeadline) {
			t.Fatalf("callback deadline = elapsed:%s error:%v", elapsed, err)
		}
		assertInspectionValidationStateScrubbed(t, retained.state)
		if err := inspection.Release(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("admitted scalar read drains before return", func(t *testing.T) {
		inspection, _ := newInspectionDomainValidationFixture(t)
		releaseRead := make(chan struct{})
		readStarted := make(chan struct{})
		var retained inspectionValidationGeneration
		result := make(chan error, 1)
		go func() {
			result <- inspection.validateDomainWithLimits(
				t.Context(),
				"global/auth",
				"auth",
				func(_ context.Context, generation databasevalidation.Generation) error {
					retained = generation.(inspectionValidationGeneration)
					_, readErr := generation.ReadScalar("SELECT 1")
					return readErr
				},
				inspectionValidationLimits{
					maximumDuration: 20 * time.Millisecond,
					cleanupTimeout:  20 * time.Millisecond,
					ops: inspectionValidationOps{
						readScalar: func(
							context.Context,
							*sql.Conn,
							string,
							[]string,
						) (databasevalidation.Scalar, error) {
							close(readStarted)
							<-releaseRead
							return databasevalidation.Scalar{
								Kind: databasevalidation.ScalarInt64, Int64: 1,
							}, nil
						},
					},
				},
			)
		}()
		<-readStarted
		select {
		case err := <-result:
			t.Fatalf("validation returned before admitted read drained: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(releaseRead)
		err := <-result
		if IsInspectionInfrastructure(err) ||
			!errors.Is(err, errInspectionValidationDeadline) {
			t.Fatalf("synchronously drained scalar = %v", err)
		}
		assertInspectionValidationStateScrubbed(t, retained.state)
		if err := inspection.Release(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestInspectionDomainValidationExcludesAdoptReleaseAndSecondValidation(t *testing.T) {
	inspection, _ := newInspectionDomainValidationFixture(t)
	copyForAdopt := inspection
	copyForRelease := inspection
	copyForValidation := inspection
	callbackStarted := make(chan struct{})
	releaseCallback := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- inspection.ValidateDomain(
			t.Context(), "global/auth", "auth",
			func(context.Context, databasevalidation.Generation) error {
				close(callbackStarted)
				<-releaseCallback
				return nil
			},
		)
	}()
	<-callbackStarted
	if database, err := copyForAdopt.Adopt(); database != nil || err == nil {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("adoption overlapped exact validation = %#v, %v", database, err)
	}
	if err := copyForRelease.Release(); err == nil {
		t.Fatal("release overlapped exact validation")
	}
	if err := copyForValidation.ValidateDomain(
		t.Context(), "global/auth", "auth",
		func(context.Context, databasevalidation.Generation) error { return nil },
	); !IsInspectionInfrastructure(err) ||
		!errors.Is(err, errInspectionValidationContract) {
		t.Fatalf("second validation overlap = %v", err)
	}
	close(releaseCallback)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	database, err := inspection.Adopt()
	if err != nil || database == nil {
		t.Fatalf("adopt after exact validation = %#v, %v", database, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionDomainValidationCleanupFailStopsUnderLifecycleAuthority(t *testing.T) {
	inspection, _ := newInspectionDomainValidationFixture(t)
	ops := defaultInspectionValidationOps()
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	closeConnection := ops.closeConnection
	ops.closeConnection = func(connection *sql.Conn) error {
		close(closeStarted)
		<-releaseClose
		return closeConnection(connection)
	}
	result := make(chan error, 1)
	go func() {
		result <- inspection.validateDomainWithLimits(
			t.Context(),
			"global/auth",
			"auth",
			func(context.Context, databasevalidation.Generation) error { return nil },
			inspectionValidationLimits{
				maximumDuration: time.Second,
				cleanupTimeout:  20 * time.Millisecond,
				ops:             ops,
			},
		)
	}()
	<-closeStarted
	select {
	case err := <-result:
		t.Fatalf("validation returned before physical cleanup completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if database, err := inspection.Adopt(); database != nil || err == nil {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("adoption overlapped physical cleanup = %#v, %v", database, err)
	}
	if err := inspection.Release(); err == nil {
		t.Fatal("release overlapped physical cleanup")
	}
	close(releaseClose)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	database, err := inspection.Adopt()
	if err != nil || database == nil {
		t.Fatalf("adoption after cleanup = %#v, %v", database, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInspectionDomainValidationPreAdmissionRaceIsAtomic(t *testing.T) {
	tests := []struct {
		name string
		read func(databasevalidation.Generation) error
	}{
		{
			name: "invalid statement",
			read: func(generation databasevalidation.Generation) error {
				_, err := generation.ReadScalar("DELETE FROM items")
				return err
			},
		},
		{
			name: "oversized arguments",
			read: func(generation databasevalidation.Generation) error {
				_, err := generation.ReadScalar(
					"SELECT 1",
					make([]string, maximumInspectionValidationArguments+1)...,
				)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name+" admitted", func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			paused := make(chan struct{})
			releaseAdmission := make(chan struct{})
			ops := defaultInspectionValidationOps()
			ops.beforeReadAdmission = func() {
				close(paused)
				<-releaseAdmission
			}
			var retained inspectionValidationGeneration
			err := inspection.validateDomainWithLimits(
				t.Context(),
				"global/auth",
				"auth",
				func(_ context.Context, generation databasevalidation.Generation) error {
					retained = generation.(inspectionValidationGeneration)
					readResult := make(chan error, 1)
					go func() { readResult <- test.read(generation) }()
					<-paused
					close(releaseAdmission)
					_ = <-readResult
					return nil
				},
				inspectionValidationLimits{
					maximumDuration: time.Second,
					cleanupTimeout:  time.Second,
					ops:             ops,
				},
			)
			if !IsInspectionInfrastructure(err) ||
				!errors.Is(err, errInspectionValidationContract) {
				t.Fatalf("admitted pre-validation failure = %v", err)
			}
			assertInspectionValidationStateScrubbed(t, retained.state)
			if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
				t.Fatalf("admitted contract failure remained adoptable = %#v, %v", database, adoptErr)
			}
		})

		t.Run(test.name+" expired", func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			paused := make(chan struct{})
			releaseAdmission := make(chan struct{})
			ops := defaultInspectionValidationOps()
			ops.beforeReadAdmission = func() {
				close(paused)
				<-releaseAdmission
			}
			var retained inspectionValidationGeneration
			readResult := make(chan error, 1)
			validationResult := make(chan error, 1)
			go func() {
				validationResult <- inspection.validateDomainWithLimits(
					t.Context(),
					"global/auth",
					"auth",
					func(_ context.Context, generation databasevalidation.Generation) error {
						retained = generation.(inspectionValidationGeneration)
						go func() { readResult <- test.read(generation) }()
						<-paused
						return nil
					},
					inspectionValidationLimits{
						maximumDuration: time.Second,
						cleanupTimeout:  time.Second,
						ops:             ops,
					},
				)
			}()
			if err := <-validationResult; err != nil {
				t.Fatalf("expired pre-admission call changed validation result: %v", err)
			}
			close(releaseAdmission)
			if err := <-readResult; !errors.Is(err, errInspectionValidationScopeExpired) {
				t.Fatalf("expired pre-admission read = %v", err)
			}
			assertInspectionValidationStateScrubbed(t, retained.state)
			database, err := inspection.Adopt()
			if err != nil || database == nil {
				t.Fatalf("clean validation after expired read = %#v, %v", database, err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertInspectionValidationStateScrubbed(
	t *testing.T,
	state *inspectionValidationState,
) {
	t.Helper()
	if state == nil {
		t.Fatal("validation state was not captured")
	}
	state.Lock()
	defer state.Unlock()
	if state.active || state.storeID != "" || state.domain != "" || state.scope != nil ||
		state.connection != nil || state.readScalar != nil || state.failure != nil {
		t.Fatalf("validation state retained capability data: %#v", state)
	}
}

type inspectionValidationFaultBoundary struct {
	inner      inspectionValidationBoundary
	beginErr   error
	startedErr error
	checkErr   error
	closeErr   error
}

type inspectionValidationRecordingBoundary struct {
	inner  inspectionValidationBoundary
	record func(string)
}

func (boundary inspectionValidationRecordingBoundary) BeginAttempted() error {
	boundary.record("boundary begin attempted")
	return boundary.inner.BeginAttempted()
}

func (boundary inspectionValidationRecordingBoundary) Started() error {
	boundary.record("boundary started")
	return boundary.inner.Started()
}

func (boundary inspectionValidationRecordingBoundary) Check(ctx context.Context) error {
	boundary.record("boundary check")
	return boundary.inner.Check(ctx)
}

func (boundary inspectionValidationRecordingBoundary) Close() error {
	boundary.record("boundary close")
	return boundary.inner.Close()
}

func (boundary *inspectionValidationFaultBoundary) BeginAttempted() error {
	if boundary.beginErr != nil {
		return boundary.beginErr
	}
	return boundary.inner.BeginAttempted()
}

func (boundary *inspectionValidationFaultBoundary) Started() error {
	if boundary.startedErr != nil {
		return boundary.startedErr
	}
	return boundary.inner.Started()
}

func (boundary *inspectionValidationFaultBoundary) Check(ctx context.Context) error {
	if err := boundary.inner.Check(ctx); err != nil {
		return err
	}
	return boundary.checkErr
}

func (boundary *inspectionValidationFaultBoundary) Close() error {
	return errors.Join(boundary.inner.Close(), boundary.closeErr)
}

func TestInspectionDomainValidationFaultsFailClosed(t *testing.T) {
	canary := errors.New("inspection validation fault canary")
	tests := []struct {
		name          string
		mutate        func(*inspectionValidationOps)
		validate      func(context.Context, databasevalidation.Generation) error
		wantInfra     bool
		wantIntegrity bool
	}{
		{
			name: "connection",
			mutate: func(ops *inspectionValidationOps) {
				ops.connection = func(context.Context, Inspection) (*sql.Conn, error) {
					return nil, canary
				}
			},
		},
		{
			name: "query-only enable",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.setQueryOnly
				ops.setQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
					if enabled {
						return canary
					}
					return original(ctx, connection, enabled)
				}
			},
		},
		{
			name: "unexpected initial query-only state",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.requireQueryOnly
				ops.requireQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
					if !enabled {
						return errors.Join(errInspectionValidationQueryOnlyState, canary)
					}
					return original(ctx, connection, enabled)
				}
			},
			wantInfra: true,
		},
		{
			name: "boundary construction",
			mutate: func(ops *inspectionValidationOps) {
				ops.newBoundary = func(*sql.Conn) (inspectionValidationBoundary, error) {
					return nil, canary
				}
			},
			wantInfra: true,
		},
		{
			name:      "boundary begin attempted",
			mutate:    inspectionValidationBoundaryFaultMutation(canary, nil, nil, nil),
			wantInfra: true,
		},
		{
			name: "SQL begin",
			mutate: func(ops *inspectionValidationOps) {
				ops.begin = func(context.Context, *sql.Conn) error { return canary }
			},
			wantInfra: true,
		},
		{
			name:      "boundary started",
			mutate:    inspectionValidationBoundaryFaultMutation(nil, canary, nil, nil),
			wantInfra: true,
		},
		{
			name: "post-callback query-only check",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.requireQueryOnly
				ops.requireQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
					if enabled {
						return canary
					}
					return original(ctx, connection, enabled)
				}
			},
			wantInfra: true,
		},
		{
			name:      "boundary check",
			mutate:    inspectionValidationBoundaryFaultMutation(nil, nil, canary, nil),
			wantInfra: true,
		},
		{
			name:      "boundary close",
			mutate:    inspectionValidationBoundaryFaultMutation(nil, nil, nil, canary),
			wantInfra: true,
		},
		{
			name: "query-only reset",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.setQueryOnly
				ops.setQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
					err := original(ctx, connection, enabled)
					if !enabled {
						return errors.Join(err, canary)
					}
					return err
				}
			},
			wantInfra: true,
		},
		{
			name: "connection close",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.closeConnection
				ops.closeConnection = func(connection *sql.Conn) error {
					return errors.Join(original(connection), canary)
				}
			},
			wantInfra: true,
		},
		{
			name: "scalar query",
			mutate: func(ops *inspectionValidationOps) {
				ops.readScalar = func(
					context.Context,
					*sql.Conn,
					string,
					[]string,
				) (databasevalidation.Scalar, error) {
					return databasevalidation.Scalar{}, canary
				}
			},
			validate: func(_ context.Context, generation databasevalidation.Generation) error {
				_, err := generation.ReadScalar("SELECT 1")
				return err
			},
		},
		{
			name: "final revalidation",
			mutate: func(ops *inspectionValidationOps) {
				original := ops.revalidate
				calls := 0
				ops.revalidate = func(ctx context.Context, inspection Inspection) error {
					calls++
					if calls == 2 {
						return errors.Join(errInspectionIntegrity, canary)
					}
					return original(ctx, inspection)
				}
			},
			wantIntegrity: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection, _ := newInspectionDomainValidationFixture(t)
			ops := defaultInspectionValidationOps()
			test.mutate(&ops)
			validate := test.validate
			if validate == nil {
				validate = func(context.Context, databasevalidation.Generation) error { return nil }
			}
			err := inspection.validateDomainWithLimits(
				t.Context(),
				"global/auth",
				"auth",
				validate,
				inspectionValidationLimits{
					maximumDuration: time.Second,
					cleanupTimeout:  time.Second,
					ops:             ops,
				},
			)
			if !errors.Is(err, canary) || IsInspectionInfrastructure(err) != test.wantInfra ||
				IsInspectionIntegrity(err) != test.wantIntegrity {
				t.Fatalf("fault classification = %v", err)
			}
			if test.wantInfra {
				if database, adoptErr := inspection.Adopt(); database != nil || adoptErr == nil {
					t.Fatalf("infrastructure-failed inspection remained adoptable = %#v, %v", database, adoptErr)
				}
			}
		})
	}
}

func TestInspectionDomainValidationOperationOrder(t *testing.T) {
	inspection, _ := newInspectionDomainValidationFixture(t)
	ops := defaultInspectionValidationOps()
	var (
		lock   sync.Mutex
		events []string
	)
	record := func(event string) {
		lock.Lock()
		events = append(events, event)
		lock.Unlock()
	}
	validateQuery := ops.validateQuery
	ops.validateQuery = func(ctx context.Context, inspection Inspection) error {
		record("validate query")
		return validateQuery(ctx, inspection)
	}
	revalidate := ops.revalidate
	revalidations := 0
	ops.revalidate = func(ctx context.Context, inspection Inspection) error {
		revalidations++
		record(fmt.Sprintf("revalidate %d", revalidations))
		return revalidate(ctx, inspection)
	}
	connection := ops.connection
	ops.connection = func(ctx context.Context, inspection Inspection) (*sql.Conn, error) {
		record("connection")
		return connection(ctx, inspection)
	}
	requireQueryOnly := ops.requireQueryOnly
	ops.requireQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
		record(fmt.Sprintf("require query-only %t", enabled))
		return requireQueryOnly(ctx, connection, enabled)
	}
	setQueryOnly := ops.setQueryOnly
	ops.setQueryOnly = func(ctx context.Context, connection *sql.Conn, enabled bool) error {
		record(fmt.Sprintf("set query-only %t", enabled))
		return setQueryOnly(ctx, connection, enabled)
	}
	newBoundary := ops.newBoundary
	ops.newBoundary = func(connection *sql.Conn) (inspectionValidationBoundary, error) {
		record("new boundary")
		boundary, err := newBoundary(connection)
		if err != nil {
			return nil, err
		}
		return inspectionValidationRecordingBoundary{inner: boundary, record: record}, nil
	}
	begin := ops.begin
	ops.begin = func(ctx context.Context, connection *sql.Conn) error {
		record("SQL begin")
		return begin(ctx, connection)
	}
	readScalar := ops.readScalar
	ops.readScalar = func(
		ctx context.Context,
		connection *sql.Conn,
		statement string,
		arguments []string,
	) (databasevalidation.Scalar, error) {
		record("scalar read")
		return readScalar(ctx, connection, statement, arguments)
	}
	closeConnection := ops.closeConnection
	ops.closeConnection = func(connection *sql.Conn) error {
		record("connection close")
		return closeConnection(connection)
	}
	release := ops.release
	ops.release = func(inspection Inspection) error {
		record("inspection release")
		return release(inspection)
	}
	err := inspection.validateDomainWithLimits(
		t.Context(),
		"workspace/0123456789abcdef/repository-reviews",
		"repository-reviews",
		func(_ context.Context, generation databasevalidation.Generation) error {
			record("callback begin")
			value, err := generation.ReadScalar("SELECT COUNT(*) FROM items")
			record("callback end")
			if err != nil || value.Kind != databasevalidation.ScalarInt64 || value.Int64 != 1 {
				return errors.Join(errors.New("ordered scalar read failed"), err)
			}
			return nil
		},
		inspectionValidationLimits{
			maximumDuration: time.Second,
			cleanupTimeout:  time.Second,
			ops:             ops,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"validate query",
		"revalidate 1",
		"connection",
		"require query-only false",
		"set query-only true",
		"new boundary",
		"boundary begin attempted",
		"SQL begin",
		"boundary started",
		"callback begin",
		"scalar read",
		"callback end",
		"require query-only true",
		"boundary check",
		"boundary close",
		"set query-only false",
		"connection close",
		"revalidate 2",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("validation operations = %#v, want %#v", events, want)
	}
}

func TestInspectionDomainValidationFailuresDoNotChangeMainGenerationFile(t *testing.T) {
	tests := []struct {
		name     string
		ctx      func(*testing.T) context.Context
		validate func(context.Context, databasevalidation.Generation) error
	}{
		{
			name: "semantic mismatch",
			validate: func(context.Context, databasevalidation.Generation) error {
				return errors.New("domain mismatch")
			},
		},
		{
			name: "query failure",
			validate: func(_ context.Context, generation databasevalidation.Generation) error {
				_, err := generation.ReadScalar("SELECT missing_column FROM items")
				return err
			},
		},
		{
			name: "contract violation",
			validate: func(_ context.Context, generation databasevalidation.Generation) error {
				_, err := generation.ReadScalar("DELETE FROM items")
				return err
			},
		},
		{
			name: "panic",
			validate: func(context.Context, databasevalidation.Generation) error {
				panic("private")
			},
		},
		{
			name: "cancellation",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			validate: func(context.Context, databasevalidation.Generation) error { return nil },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection, path := newInspectionDomainValidationFixture(t)
			before := inspectionValidationFileSnapshot(t, path)
			ctx := t.Context()
			if test.ctx != nil {
				ctx = test.ctx(t)
			}
			if err := inspection.ValidateDomain(
				ctx, "global/auth", "auth", test.validate,
			); err == nil {
				t.Fatal("failing validation returned nil")
			}
			after := inspectionValidationFileSnapshot(t, path)
			if after != before {
				t.Fatalf("generation files changed: before=%#v after=%#v", before, after)
			}
		})
	}
}

type inspectionValidationFile struct {
	Mode os.FileMode
	Size int
	Hash [sha256.Size]byte
}

func inspectionValidationFileSnapshot(
	t *testing.T,
	path string,
) inspectionValidationFile {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("validation fixture main is not regular: %s", info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return inspectionValidationFile{
		Mode: info.Mode(), Size: len(data), Hash: sha256.Sum256(data),
	}
}

func inspectionValidationBoundaryFaultMutation(
	beginErr error,
	startedErr error,
	checkErr error,
	closeErr error,
) func(*inspectionValidationOps) {
	return func(ops *inspectionValidationOps) {
		original := ops.newBoundary
		ops.newBoundary = func(connection *sql.Conn) (inspectionValidationBoundary, error) {
			boundary, err := original(connection)
			if err != nil {
				return nil, err
			}
			return &inspectionValidationFaultBoundary{
				inner: boundary, beginErr: beginErr, startedErr: startedErr,
				checkErr: checkErr, closeErr: closeErr,
			}, nil
		}
	}
}

func TestInspectionDomainValidationRejectsInvalidInputs(t *testing.T) {
	inspection, _ := newInspectionDomainValidationFixture(t)
	valid := func(context.Context, databasevalidation.Generation) error { return nil }
	for _, test := range []struct {
		name     string
		ctx      context.Context
		storeID  string
		domain   string
		validate func(context.Context, databasevalidation.Generation) error
	}{
		{name: "nil context", storeID: "global/auth", domain: "auth", validate: valid},
		{name: "invalid store", ctx: t.Context(), storeID: "bad id", domain: "auth", validate: valid},
		{name: "invalid domain", ctx: t.Context(), storeID: "global/auth", domain: "Auth", validate: valid},
		{name: "nil validator", ctx: t.Context(), storeID: "global/auth", domain: "auth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := inspection.ValidateDomain(
				test.ctx, dblayer.StoreID(test.storeID), test.domain, test.validate,
			); !errors.Is(err, errInspectionUnavailable) {
				t.Fatalf("invalid validation input = %v", err)
			}
		})
	}
	if err := (Inspection{}).ValidateDomain(
		t.Context(), "global/auth", "auth", valid,
	); !errors.Is(err, errInspectionUnavailable) {
		t.Fatalf("empty inspection validation = %v", err)
	}
}

func TestInspectionDomainValidationHelperBoundaries(t *testing.T) {
	var generation inspectionValidationGeneration
	if generation.StoreID() != "" || generation.Domain() != "" {
		t.Fatal("nil validation generation exposed a binding")
	}
	if _, err := generation.ReadScalar("SELECT 1"); !IsInspectionInfrastructure(err) ||
		!errors.Is(err, errInspectionValidationContract) {
		t.Fatalf("nil validation generation read = %v", err)
	}
	var state *inspectionValidationState
	state.recordFailure(errors.New("ignored"))
	if err := inspectionValidationQueryFailure(nil); err != nil {
		t.Fatalf("nil query failure = %v", err)
	}
	if _, err := readInspectionValidationScalar(
		nil, nil, "SELECT 1", nil,
	); !IsInspectionInfrastructure(err) {
		t.Fatalf("nil scalar reader = %v", err)
	}
	ops := defaultInspectionValidationOps()
	if err := ops.closeConnection(nil); err != nil {
		t.Fatalf("nil validation connection close = %v", err)
	}
	if err := discardInspectionValidationConnection(nil); err != nil {
		t.Fatalf("nil validation connection discard = %v", err)
	}
	if err := setInspectionValidationQueryOnly(nil, nil, true); err == nil {
		t.Fatal("nil query-only transition succeeded")
	}
	if err := requireInspectionValidationQueryOnly(nil, nil, true); err == nil {
		t.Fatal("nil query-only check succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	canary := inspectionValidationContractFailure("cancellation canary")
	if err := classifyInspectionDomainValidationError(canceled, canary); !errors.Is(err, context.Canceled) ||
		!errors.Is(err, errInspectionInfrastructure) {
		t.Fatalf("cancellation plus infrastructure = %v", err)
	}
	if err := classifyInspectionDomainValidationError(
		t.Context(), context.Canceled,
	); !errors.Is(err, context.Canceled) || !IsInspectionIntegrity(err) {
		t.Fatalf("forged callback cancellation classification = %v", err)
	}
	if err := classifyInspectionDomainValidationError(
		t.Context(), errors.Join(context.DeadlineExceeded, errors.New("forged")),
	); !errors.Is(err, context.DeadlineExceeded) || !IsInspectionIntegrity(err) {
		t.Fatalf("forged callback deadline classification = %v", err)
	}
	if err := classifyInspectionDomainValidationError(t.Context(), nil); err != nil {
		t.Fatalf("nil validation classification = %v", err)
	}
	for _, whitespace := range []byte{' ', '\t', '\n', '\r', '\v', '\f'} {
		if !inspectionValidationWhitespace(whitespace) {
			t.Errorf("validation whitespace %q was rejected", whitespace)
		}
	}
	if inspectionValidationWhitespace('x') {
		t.Fatal("non-whitespace validation byte was accepted")
	}
}
